package e2b

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"connectrpc.com/connect"
	processpb "github.com/e2b-dev/infra/packages/shared/pkg/grpc/envd/process"
)

// ProcessInfo describes a running command or PTY session.
type ProcessInfo struct {
	PID              uint32
	Tag              string
	Command          string
	Args             []string
	Environment      map[string]string
	WorkingDirectory string
}

// CommandStartRequest starts a background command.
type CommandStartRequest struct {
	Command          []string
	WorkingDirectory string
	Environment      map[string]string
	Tag              string
	Stdin            bool
	OnStdout         func(string)
	OnStderr         func(string)
}

// CommandConnectOptions controls command stream callbacks.
type CommandConnectOptions struct {
	OnStdout func(string)
	OnStderr func(string)
}

// PTYStartRequest starts a shell-backed PTY session.
type PTYStartRequest struct {
	Cols             uint32
	Rows             uint32
	WorkingDirectory string
	Environment      map[string]string
	Tag              string
	OnData           func([]byte)
}

// PTYConnectOptions controls PTY stream callbacks.
type PTYConnectOptions struct {
	OnData func([]byte)
}

// CommandResult is returned by CommandHandle.Wait.
type CommandResult struct {
	ExitCode  int
	Stdout    string
	Stderr    string
	PTYOutput []byte
	Metadata  map[string]string
}

type processInputMode int

const (
	processInputModeNone processInputMode = iota
	processInputModeStdin
	processInputModePTY
)

// CommandHandle tracks a running command or PTY session.
type CommandHandle struct {
	pid       uint32
	transport sandboxTransport
	cancel    context.CancelFunc
	inputMode processInputMode

	onStdout func(string)
	onStderr func(string)
	onPTY    func([]byte)

	done chan struct{}

	mu     sync.Mutex
	stdout strings.Builder
	stderr strings.Builder
	pty    bytes.Buffer
	result *CommandResult
	err    error
}

// PID returns the process ID.
func (h *CommandHandle) PID() uint32 {
	return h.pid
}

// Stdout returns the stdout captured so far.
func (h *CommandHandle) Stdout() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stdout.String()
}

// Stderr returns the stderr captured so far.
func (h *CommandHandle) Stderr() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stderr.String()
}

// PTYOutput returns the PTY bytes captured so far.
func (h *CommandHandle) PTYOutput() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]byte(nil), h.pty.Bytes()...)
}

// Disconnect stops receiving stream events without killing the process.
func (h *CommandHandle) Disconnect() {
	h.cancel()
}

// SendStdin writes to a command's stdin stream.
func (h *CommandHandle) SendStdin(ctx context.Context, data []byte) error {
	if h.inputMode != processInputModeStdin {
		return fmt.Errorf("e2b: stdin is not enabled for process %d", h.pid)
	}
	return sendProcessInput(ctx, h.transport, h.pid, &processpb.ProcessInput{
		Input: &processpb.ProcessInput_Stdin{Stdin: data},
	})
}

// CloseStdin closes stdin, signaling EOF to the process.
func (h *CommandHandle) CloseStdin(ctx context.Context) error {
	if h.inputMode != processInputModeStdin {
		return fmt.Errorf("e2b: stdin is not enabled for process %d", h.pid)
	}
	return closeProcessStdin(ctx, h.transport, h.pid)
}

// SendPTY writes bytes to a PTY session.
func (h *CommandHandle) SendPTY(ctx context.Context, data []byte) error {
	if h.inputMode != processInputModePTY {
		return fmt.Errorf("e2b: process %d is not a PTY session", h.pid)
	}
	return sendProcessInput(ctx, h.transport, h.pid, &processpb.ProcessInput{
		Input: &processpb.ProcessInput_Pty{Pty: data},
	})
}

// ResizePTY resizes a PTY session.
func (h *CommandHandle) ResizePTY(ctx context.Context, cols uint32, rows uint32) error {
	if h.inputMode != processInputModePTY {
		return fmt.Errorf("e2b: process %d is not a PTY session", h.pid)
	}
	return updatePTYSize(ctx, h.transport, h.pid, cols, rows)
}

// Kill sends SIGKILL to the process.
func (h *CommandHandle) Kill(ctx context.Context) (bool, error) {
	return killProcess(ctx, h.transport, h.pid)
}

// Wait blocks until the process stream ends and returns the final result.
func (h *CommandHandle) Wait() (CommandResult, error) {
	<-h.done

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.err != nil {
		return CommandResult{}, h.err
	}
	if h.result == nil {
		return CommandResult{}, fmt.Errorf("e2b: process %d exited without a result", h.pid)
	}
	return *h.result, nil
}

// ListProcesses lists running commands and PTY sessions.
func (s *Sandbox) ListProcesses(ctx context.Context) ([]ProcessInfo, error) {
	transport, err := s.activeTransport()
	if err != nil {
		return nil, err
	}

	req := connect.NewRequest(&processpb.ListRequest{})
	setProcessRPCHeaders(req, transport, false)

	resp, err := transport.processClient.List(ctx, req)
	if err != nil {
		return nil, normalizeProcessRPCError(err)
	}

	items := make([]ProcessInfo, 0, len(resp.Msg.GetProcesses()))
	for _, process := range resp.Msg.GetProcesses() {
		config := process.GetConfig()
		items = append(items, ProcessInfo{
			PID:              process.GetPid(),
			Tag:              process.GetTag(),
			Command:          config.GetCmd(),
			Args:             append([]string(nil), config.GetArgs()...),
			Environment:      cloneStringMap(config.GetEnvs()),
			WorkingDirectory: config.GetCwd(),
		})
	}
	return items, nil
}

// StartCommand starts a background command and returns a handle.
func (s *Sandbox) StartCommand(ctx context.Context, request CommandStartRequest) (*CommandHandle, error) {
	transport, err := s.activeTransport()
	if err != nil {
		return nil, err
	}
	if len(request.Command) == 0 {
		return nil, fmt.Errorf("e2b: CommandStartRequest.Command must be non-empty")
	}

	streamCtx, cancel := context.WithCancel(ctx)
	stdin := request.Stdin
	req := connect.NewRequest(&processpb.StartRequest{
		Process: &processpb.ProcessConfig{
			Cmd:  request.Command[0],
			Args: request.Command[1:],
			Envs: request.Environment,
			Cwd:  stringPtr(request.WorkingDirectory),
		},
		Tag:   stringPtr(request.Tag),
		Stdin: &stdin,
	})
	setProcessRPCHeaders(req, transport, true)

	stream, err := transport.processClient.Start(streamCtx, req)
	if err != nil {
		cancel()
		return nil, normalizeProcessRPCError(err)
	}

	pid, err := awaitProcessStart(stream, func(message *processpb.StartResponse) *processpb.ProcessEvent {
		return message.GetEvent()
	})
	if err != nil {
		cancel()
		stream.Close()
		return nil, err
	}

	return newCommandHandle(
		stream,
		func(message *processpb.StartResponse) *processpb.ProcessEvent { return message.GetEvent() },
		transport,
		pid,
		processInputModeStdinIf(request.Stdin),
		cancel,
		request.OnStdout,
		request.OnStderr,
		nil,
	), nil
}

// ConnectProcess reconnects to a running command stream by PID.
func (s *Sandbox) ConnectProcess(ctx context.Context, pid uint32, options CommandConnectOptions) (*CommandHandle, error) {
	transport, err := s.activeTransport()
	if err != nil {
		return nil, err
	}

	streamCtx, cancel := context.WithCancel(ctx)
	req := connect.NewRequest(&processpb.ConnectRequest{
		Process: processSelectorPID(pid),
	})
	setProcessRPCHeaders(req, transport, true)

	stream, err := transport.processClient.Connect(streamCtx, req)
	if err != nil {
		cancel()
		return nil, normalizeProcessRPCError(err)
	}

	startPID, err := awaitProcessStart(stream, func(message *processpb.ConnectResponse) *processpb.ProcessEvent {
		return message.GetEvent()
	})
	if err != nil {
		cancel()
		stream.Close()
		return nil, err
	}

	return newCommandHandle(
		stream,
		func(message *processpb.ConnectResponse) *processpb.ProcessEvent { return message.GetEvent() },
		transport,
		startPID,
		processInputModeStdin,
		cancel,
		options.OnStdout,
		options.OnStderr,
		nil,
	), nil
}

// SendStdin writes to the stdin of a running process.
func (s *Sandbox) SendStdin(ctx context.Context, pid uint32, data []byte) error {
	transport, err := s.activeTransport()
	if err != nil {
		return err
	}
	return sendProcessInput(ctx, transport, pid, &processpb.ProcessInput{
		Input: &processpb.ProcessInput_Stdin{Stdin: data},
	})
}

// CloseStdin closes stdin for a running process.
func (s *Sandbox) CloseStdin(ctx context.Context, pid uint32) error {
	transport, err := s.activeTransport()
	if err != nil {
		return err
	}
	return closeProcessStdin(ctx, transport, pid)
}

// KillProcess sends SIGKILL to a running process.
func (s *Sandbox) KillProcess(ctx context.Context, pid uint32) (bool, error) {
	transport, err := s.activeTransport()
	if err != nil {
		return false, err
	}
	return killProcess(ctx, transport, pid)
}

// CreatePTY starts a shell-backed PTY session and returns a handle.
func (s *Sandbox) CreatePTY(ctx context.Context, request PTYStartRequest) (*CommandHandle, error) {
	transport, err := s.activeTransport()
	if err != nil {
		return nil, err
	}

	streamCtx, cancel := context.WithCancel(ctx)
	envs := cloneStringMap(request.Environment)
	if len(envs) == 0 {
		envs = map[string]string{}
	}
	if _, ok := envs["TERM"]; !ok {
		envs["TERM"] = "xterm-256color"
	}
	if _, ok := envs["LANG"]; !ok {
		envs["LANG"] = "C.UTF-8"
	}
	if _, ok := envs["LC_ALL"]; !ok {
		envs["LC_ALL"] = "C.UTF-8"
	}

	req := connect.NewRequest(&processpb.StartRequest{
		Process: &processpb.ProcessConfig{
			Cmd:  "/bin/bash",
			Args: []string{"-i", "-l"},
			Envs: envs,
			Cwd:  stringPtr(request.WorkingDirectory),
		},
		Pty: &processpb.PTY{
			Size: &processpb.PTY_Size{
				Cols: request.Cols,
				Rows: request.Rows,
			},
		},
		Tag: stringPtr(request.Tag),
	})
	setProcessRPCHeaders(req, transport, true)

	stream, err := transport.processClient.Start(streamCtx, req)
	if err != nil {
		cancel()
		return nil, normalizeProcessRPCError(err)
	}

	pid, err := awaitProcessStart(stream, func(message *processpb.StartResponse) *processpb.ProcessEvent {
		return message.GetEvent()
	})
	if err != nil {
		cancel()
		stream.Close()
		return nil, err
	}

	return newCommandHandle(
		stream,
		func(message *processpb.StartResponse) *processpb.ProcessEvent { return message.GetEvent() },
		transport,
		pid,
		processInputModePTY,
		cancel,
		nil,
		nil,
		request.OnData,
	), nil
}

// ConnectPTY reconnects to a running PTY session by PID.
func (s *Sandbox) ConnectPTY(ctx context.Context, pid uint32, options PTYConnectOptions) (*CommandHandle, error) {
	transport, err := s.activeTransport()
	if err != nil {
		return nil, err
	}

	streamCtx, cancel := context.WithCancel(ctx)
	req := connect.NewRequest(&processpb.ConnectRequest{
		Process: processSelectorPID(pid),
	})
	setProcessRPCHeaders(req, transport, true)

	stream, err := transport.processClient.Connect(streamCtx, req)
	if err != nil {
		cancel()
		return nil, normalizeProcessRPCError(err)
	}

	startPID, err := awaitProcessStart(stream, func(message *processpb.ConnectResponse) *processpb.ProcessEvent {
		return message.GetEvent()
	})
	if err != nil {
		cancel()
		stream.Close()
		return nil, err
	}

	return newCommandHandle(
		stream,
		func(message *processpb.ConnectResponse) *processpb.ProcessEvent { return message.GetEvent() },
		transport,
		startPID,
		processInputModePTY,
		cancel,
		nil,
		nil,
		options.OnData,
	), nil
}

// SendPTYInput writes bytes to a running PTY session.
func (s *Sandbox) SendPTYInput(ctx context.Context, pid uint32, data []byte) error {
	transport, err := s.activeTransport()
	if err != nil {
		return err
	}
	return sendProcessInput(ctx, transport, pid, &processpb.ProcessInput{
		Input: &processpb.ProcessInput_Pty{Pty: data},
	})
}

// ResizePTY resizes a running PTY session.
func (s *Sandbox) ResizePTY(ctx context.Context, pid uint32, cols uint32, rows uint32) error {
	transport, err := s.activeTransport()
	if err != nil {
		return err
	}
	return updatePTYSize(ctx, transport, pid, cols, rows)
}

// KillPTY sends SIGKILL to a PTY session.
func (s *Sandbox) KillPTY(ctx context.Context, pid uint32) (bool, error) {
	return s.KillProcess(ctx, pid)
}

func newCommandHandle[T any](stream *connect.ServerStreamForClient[T], getEvent func(*T) *processpb.ProcessEvent, transport sandboxTransport, pid uint32, inputMode processInputMode, cancel context.CancelFunc, onStdout func(string), onStderr func(string), onPTY func([]byte)) *CommandHandle {
	handle := &CommandHandle{
		pid:       pid,
		transport: transport,
		cancel:    cancel,
		inputMode: inputMode,
		onStdout:  onStdout,
		onStderr:  onStderr,
		onPTY:     onPTY,
		done:      make(chan struct{}),
	}

	go consumeProcessStream(handle, stream, getEvent)
	return handle
}

func consumeProcessStream[T any](handle *CommandHandle, stream *connect.ServerStreamForClient[T], getEvent func(*T) *processpb.ProcessEvent) {
	defer close(handle.done)
	defer stream.Close()

	for stream.Receive() {
		event := getEvent(stream.Msg())
		if event == nil {
			continue
		}

		switch {
		case event.GetData() != nil:
			handle.appendProcessData(event.GetData())
		case event.GetEnd() != nil:
			handle.finishProcess(event.GetEnd())
		}
	}

	err := stream.Err()
	if err != nil {
		err = normalizeProcessRPCError(err)
	}

	handle.mu.Lock()
	if handle.result == nil && err == nil {
		err = fmt.Errorf("e2b: process %d stream ended without an end event", handle.pid)
	}
	handle.err = err
	handle.mu.Unlock()
}

func (h *CommandHandle) appendProcessData(data *processpb.ProcessEvent_DataEvent) {
	if stdout := data.GetStdout(); len(stdout) > 0 {
		text := string(stdout)
		h.mu.Lock()
		_, _ = h.stdout.WriteString(text)
		callback := h.onStdout
		h.mu.Unlock()
		if callback != nil {
			callback(text)
		}
		return
	}

	if stderr := data.GetStderr(); len(stderr) > 0 {
		text := string(stderr)
		h.mu.Lock()
		_, _ = h.stderr.WriteString(text)
		callback := h.onStderr
		h.mu.Unlock()
		if callback != nil {
			callback(text)
		}
		return
	}

	if pty := data.GetPty(); len(pty) > 0 {
		payload := append([]byte(nil), pty...)
		h.mu.Lock()
		_, _ = h.pty.Write(payload)
		callback := h.onPTY
		h.mu.Unlock()
		if callback != nil {
			callback(payload)
		}
	}
}

func (h *CommandHandle) finishProcess(end *processpb.ProcessEvent_EndEvent) {
	result := CommandResult{
		ExitCode: int(end.GetExitCode()),
		Metadata: map[string]string{},
	}
	if errorMessage := end.GetError(); errorMessage != "" {
		result.Metadata["error"] = errorMessage
	}

	h.mu.Lock()
	result.Stdout = h.stdout.String()
	result.Stderr = h.stderr.String()
	result.PTYOutput = append([]byte(nil), h.pty.Bytes()...)
	h.result = &result
	h.mu.Unlock()
}

func awaitProcessStart[T any](stream *connect.ServerStreamForClient[T], getEvent func(*T) *processpb.ProcessEvent) (uint32, error) {
	if !stream.Receive() {
		if err := stream.Err(); err != nil {
			return 0, normalizeProcessRPCError(err)
		}
		return 0, fmt.Errorf("e2b: expected process start event")
	}

	start := getEvent(stream.Msg()).GetStart()
	if start == nil {
		return 0, fmt.Errorf("e2b: expected process start event")
	}
	return start.GetPid(), nil
}

func setProcessRPCHeaders[T any](req *connect.Request[T], transport sandboxTransport, keepalive bool) {
	if authHeader := legacySandboxAuthHeader(transport.record.EnvdVersion); authHeader != "" {
		req.Header().Set("Authorization", authHeader)
	}
	if keepalive {
		req.Header().Set("Keepalive-Ping-Interval", "50")
	}
	transport.api.setEnvdHeaders(req.Header(), transport.record)
}

func processSelectorPID(pid uint32) *processpb.ProcessSelector {
	return &processpb.ProcessSelector{
		Selector: &processpb.ProcessSelector_Pid{Pid: pid},
	}
}

func sendProcessInput(ctx context.Context, transport sandboxTransport, pid uint32, input *processpb.ProcessInput) error {
	req := connect.NewRequest(&processpb.SendInputRequest{
		Process: processSelectorPID(pid),
		Input:   input,
	})
	setProcessRPCHeaders(req, transport, false)

	_, err := transport.processClient.SendInput(ctx, req)
	if err != nil {
		return normalizeProcessRPCError(err)
	}
	return nil
}

func closeProcessStdin(ctx context.Context, transport sandboxTransport, pid uint32) error {
	req := connect.NewRequest(&processpb.CloseStdinRequest{
		Process: processSelectorPID(pid),
	})
	setProcessRPCHeaders(req, transport, false)

	_, err := transport.processClient.CloseStdin(ctx, req)
	if err != nil {
		return normalizeProcessRPCError(err)
	}
	return nil
}

func killProcess(ctx context.Context, transport sandboxTransport, pid uint32) (bool, error) {
	req := connect.NewRequest(&processpb.SendSignalRequest{
		Process: processSelectorPID(pid),
		Signal:  processpb.Signal_SIGNAL_SIGKILL,
	})
	setProcessRPCHeaders(req, transport, false)

	_, err := transport.processClient.SendSignal(ctx, req)
	if err == nil {
		return true, nil
	}

	var connectErr *connect.Error
	if errors.As(err, &connectErr) && connectErr.Code() == connect.CodeNotFound {
		return false, nil
	}

	return false, normalizeProcessRPCError(err)
}

func updatePTYSize(ctx context.Context, transport sandboxTransport, pid uint32, cols uint32, rows uint32) error {
	req := connect.NewRequest(&processpb.UpdateRequest{
		Process: processSelectorPID(pid),
		Pty: &processpb.PTY{
			Size: &processpb.PTY_Size{
				Cols: cols,
				Rows: rows,
			},
		},
	})
	setProcessRPCHeaders(req, transport, false)

	_, err := transport.processClient.Update(ctx, req)
	if err != nil {
		return normalizeProcessRPCError(err)
	}
	return nil
}

func processInputModeStdinIf(enabled bool) processInputMode {
	if enabled {
		return processInputModeStdin
	}
	return processInputModeNone
}
