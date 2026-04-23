package e2b

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Sandbox is a handle to a live E2B microVM. Methods are safe for
// concurrent use.
type Sandbox struct {
	mu                 sync.Mutex
	client             sandboxTransport
	destroying         bool
	destroyWait        chan struct{}
	closed             bool
	allowShellFallback bool
}

// ID returns the E2B sandbox identifier.
func (s *Sandbox) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client.record.SandboxID
}

// TemplateID returns the template the sandbox was cloned from.
func (s *Sandbox) TemplateID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client.record.TemplateID
}

// EnvdURL returns the envd base URL for this sandbox.
func (s *Sandbox) EnvdURL() string {
	s.mu.Lock()
	transport := s.client
	s.mu.Unlock()
	return transport.api.envdBaseURL(transport.record)
}

// ReadFile returns the contents of a file inside the sandbox.
// When AllowShellFallback is set and the envd HTTP call fails, a
// shell-based `cat` is tried as a backstop.
func (s *Sandbox) ReadFile(ctx context.Context, path string) ([]byte, error) {
	transport, err := s.activeTransport()
	if err != nil {
		return nil, err
	}
	content, err := transport.api.readFile(ctx, transport.record, path)
	if err == nil {
		return content, nil
	}
	if !s.allowShellFallback {
		return nil, err
	}
	fallback, ferr := s.readFileByCat(ctx, path)
	if ferr != nil {
		return nil, errors.Join(err, fmt.Errorf("fallback read: %w", ferr))
	}
	return fallback, nil
}

// WriteFile writes content to the given path inside the sandbox,
// creating parent directories as needed.
func (s *Sandbox) WriteFile(ctx context.Context, path string, content []byte) error {
	transport, err := s.activeTransport()
	if err != nil {
		return err
	}
	return transport.api.writeFile(ctx, transport.record, path, content)
}

// ListFiles enumerates files beneath the given prefix, up to 32 levels
// deep. Directories are skipped. When AllowShellFallback is set and the
// envd RPC fails, a `find` backstop is used.
func (s *Sandbox) ListFiles(ctx context.Context, prefix string) ([]FileInfo, error) {
	transport, err := s.activeTransport()
	if err != nil {
		return nil, err
	}
	items, err := s.listFilesRPC(ctx, transport, prefix)
	if err == nil {
		return items, nil
	}
	if !s.allowShellFallback {
		return nil, err
	}
	fallback, ferr := s.listFilesByFind(ctx, prefix)
	if ferr != nil {
		return nil, errors.Join(err, fmt.Errorf("fallback list: %w", ferr))
	}
	return fallback, nil
}

func (s *Sandbox) listFilesByFind(ctx context.Context, prefix string) ([]FileInfo, error) {
	path := strings.TrimSpace(prefix)
	if path == "" {
		path = "/workspace"
	}

	result, err := s.Exec(ctx, ExecRequest{
		Command: []string{"find", path, "-type", "f", "-printf", "%p\t%s\n"},
	})
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		if strings.Contains(result.Stderr, "No such file or directory") {
			return nil, ErrFileNotFound
		}
		return nil, fmt.Errorf("find exited with code %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	if strings.TrimSpace(result.Stdout) == "" {
		return []FileInfo{}, nil
	}

	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	items := make([]FileInfo, 0, len(lines))
	for _, line := range lines {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("unexpected find output line %q", line)
		}
		size, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse listed file size for %q: %w", parts[0], err)
		}
		items = append(items, FileInfo{
			Path: strings.TrimSpace(parts[0]),
			Size: size,
		})
	}
	return items, nil
}

func (s *Sandbox) readFileByCat(ctx context.Context, path string) ([]byte, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, ErrFileNotFound
	}

	result, err := s.Exec(ctx, ExecRequest{
		Command: []string{"sh", "-lc", "cat \"$1\"", "sh", trimmed},
	})
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		if strings.Contains(result.Stderr, "No such file or directory") {
			return nil, ErrFileNotFound
		}
		return nil, fmt.Errorf("cat exited with code %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return []byte(result.Stdout), nil
}

// Exec runs a command in the sandbox, collecting stdout and stderr, and
// returns once the process exits or the context is cancelled.
func (s *Sandbox) Exec(ctx context.Context, request ExecRequest) (ExecResult, error) {
	execCtx := ctx
	cancel := func() {}
	if request.Timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, request.Timeout)
	}
	defer cancel()

	handle, err := s.StartCommand(execCtx, CommandStartRequest{
		Command:          request.Command,
		WorkingDirectory: request.WorkingDirectory,
		Environment:      request.Environment,
	})
	if err != nil {
		return ExecResult{}, err
	}

	result, err := handle.Wait()
	if err != nil {
		return ExecResult{}, err
	}
	return ExecResult{
		ExitCode: result.ExitCode,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		Metadata: cloneStringMap(result.Metadata),
	}, nil
}

// Destroy terminates the sandbox. Safe to call more than once; only the
// first call hits the control plane. Returns nil when the sandbox was
// already gone.
func (s *Sandbox) Destroy(ctx context.Context) error {
	for {
		s.mu.Lock()
		switch {
		case s.closed:
			s.mu.Unlock()
			return nil
		case s.destroying:
			wait := s.destroyWait
			s.mu.Unlock()

			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		default:
			wait := make(chan struct{})
			s.destroying = true
			s.destroyWait = wait
			transport := s.client
			s.mu.Unlock()

			err := transport.api.destroySandbox(ctx, transport.record.SandboxID)

			s.mu.Lock()
			s.destroying = false
			s.destroyWait = nil
			if err == nil || errors.Is(err, ErrSandboxNotFound) {
				s.closed = true
			}
			close(wait)
			s.mu.Unlock()

			if err == nil || errors.Is(err, ErrSandboxNotFound) {
				return nil
			}
			return err
		}
	}
}

func (s *Sandbox) ensureActive() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.destroying {
		return ErrSandboxDestroyed
	}
	return nil
}

func (s *Sandbox) activeTransport() (sandboxTransport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.destroying {
		return sandboxTransport{}, ErrSandboxDestroyed
	}
	return s.client, nil
}

// installAdditionalPackages shells out to apt-get inside the sandbox.
// Called by Client.CreateSandbox when CreateRequest.AdditionalPackages
// is non-empty.
func (s *Sandbox) installAdditionalPackages(ctx context.Context, packages []string) error {
	installCmd := "apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends " + strings.Join(packages, " ")
	result, err := s.Exec(ctx, ExecRequest{
		Command: []string{"sh", "-c", installCmd},
		Timeout: 120 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("install additional packages: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("install additional packages: exit=%d stderr=%s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
