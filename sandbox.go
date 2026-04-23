package e2b

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	filesystempb "github.com/e2b-dev/infra/packages/shared/pkg/grpc/envd/filesystem"
	processpb "github.com/e2b-dev/infra/packages/shared/pkg/grpc/envd/process"
)

// Sandbox is a handle to a live E2B microVM. Methods are safe for
// concurrent use.
type Sandbox struct {
	mu                 sync.Mutex
	client             sandboxTransport
	closed             bool
	allowShellFallback bool
}

// ID returns the E2B sandbox identifier.
func (s *Sandbox) ID() string { return s.client.record.SandboxID }

// TemplateID returns the template the sandbox was cloned from.
func (s *Sandbox) TemplateID() string { return s.client.record.TemplateID }

// EnvdURL returns the envd base URL for this sandbox.
func (s *Sandbox) EnvdURL() string { return s.client.api.envdBaseURL(s.client.record) }

// ReadFile returns the contents of a file inside the sandbox.
// When AllowShellFallback is set and the envd HTTP call fails, a
// shell-based `cat` is tried as a backstop.
func (s *Sandbox) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if err := s.ensureActive(); err != nil {
		return nil, err
	}
	content, err := s.client.api.readFile(ctx, s.client.record, path)
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
	if err := s.ensureActive(); err != nil {
		return err
	}
	return s.client.api.writeFile(ctx, s.client.record, path, content)
}

// ListFiles enumerates files beneath the given prefix, up to 32 levels
// deep. Directories are skipped. When AllowShellFallback is set and the
// envd RPC fails, a `find` backstop is used.
func (s *Sandbox) ListFiles(ctx context.Context, prefix string) ([]FileInfo, error) {
	if err := s.ensureActive(); err != nil {
		return nil, err
	}
	items, err := s.listFilesRPC(ctx, prefix)
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

func (s *Sandbox) listFilesRPC(ctx context.Context, prefix string) ([]FileInfo, error) {
	req := connect.NewRequest(&filesystempb.ListDirRequest{
		Path:  prefix,
		Depth: 32,
	})
	req.Header().Set("Authorization", s.client.api.authHeader())
	s.client.api.setEnvdHeaders(req.Header(), s.client.record)
	resp, err := s.client.filesClient.ListDir(ctx, req)
	if err != nil {
		return nil, normalizeRPCError(err)
	}
	items := make([]FileInfo, 0, len(resp.Msg.Entries))
	for _, entry := range resp.Msg.Entries {
		if entry.GetType() != filesystempb.FileType_FILE_TYPE_FILE {
			continue
		}
		items = append(items, FileInfo{
			Path: entry.GetPath(),
			Size: entry.GetSize(),
		})
	}
	return items, nil
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
	if err := s.ensureActive(); err != nil {
		return ExecResult{}, err
	}
	if len(request.Command) == 0 {
		return ExecResult{}, fmt.Errorf("e2b: ExecRequest.Command must be non-empty")
	}

	execCtx := ctx
	cancel := func() {}
	if request.Timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, request.Timeout)
	}
	defer cancel()

	stdin := false
	req := connect.NewRequest(&processpb.StartRequest{
		Process: &processpb.ProcessConfig{
			Cmd:  request.Command[0],
			Args: request.Command[1:],
			Envs: request.Environment,
			Cwd:  stringPtr(request.WorkingDirectory),
		},
		Stdin: &stdin,
	})
	req.Header().Set("Authorization", s.client.api.authHeader())
	req.Header().Set("Keepalive-Ping-Interval", "50")
	s.client.api.setEnvdHeaders(req.Header(), s.client.record)

	stream, err := s.client.processClient.Start(execCtx, req)
	if err != nil {
		return ExecResult{}, normalizeRPCError(err)
	}
	defer stream.Close()

	result := ExecResult{Metadata: map[string]string{}}
	var stdout strings.Builder
	var stderr strings.Builder
	for stream.Receive() {
		event := stream.Msg().GetEvent().GetEvent()
		switch e := event.(type) {
		case *processpb.ProcessEvent_Data:
			data := e.Data.GetOutput()
			switch out := data.(type) {
			case *processpb.ProcessEvent_DataEvent_Stdout:
				_, _ = stdout.Write(out.Stdout)
			case *processpb.ProcessEvent_DataEvent_Stderr:
				_, _ = stderr.Write(out.Stderr)
			}
		case *processpb.ProcessEvent_End:
			result.ExitCode = int(e.End.GetExitCode())
			if errorMessage := e.End.GetError(); errorMessage != "" {
				result.Metadata["error"] = errorMessage
			}
		}
	}
	if err := stream.Err(); err != nil {
		return ExecResult{}, normalizeRPCError(err)
	}
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	return result, nil
}

// Destroy terminates the sandbox. Safe to call more than once; only the
// first call hits the control plane. Returns nil when the sandbox was
// already gone.
func (s *Sandbox) Destroy(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	if err := s.client.api.destroySandbox(ctx, s.client.record.SandboxID); err != nil {
		if errors.Is(err, ErrSandboxNotFound) {
			return nil
		}
		return err
	}
	return nil
}

func (s *Sandbox) ensureActive() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSandboxDestroyed
	}
	return nil
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
