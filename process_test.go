package e2b

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	processpb "github.com/e2b-dev/infra/packages/shared/pkg/grpc/envd/process"
	"github.com/e2b-dev/infra/packages/shared/pkg/grpc/envd/process/processconnect"
)

func TestCommandStartHandleCollectsOutputAndExit(t *testing.T) {
	var stdoutChunks []string
	var stderrChunks []string

	service := &testProcessService{
		t: t,
		start: func(_ context.Context, req *connect.Request[processpb.StartRequest], stream *connect.ServerStream[processpb.StartResponse]) error {
			verifyProcessHeaders(t, req.Header(), true)
			if got, want := req.Msg.GetProcess().GetCmd(), "python"; got != want {
				t.Fatalf("command = %q, want %q", got, want)
			}
			if got, want := req.Msg.GetProcess().GetArgs()[0], "app.py"; got != want {
				t.Fatalf("arg[0] = %q, want %q", got, want)
			}
			if got, want := req.Msg.GetProcess().GetCwd(), "/workspace"; got != want {
				t.Fatalf("cwd = %q, want %q", got, want)
			}
			if got, want := req.Msg.GetProcess().GetEnvs()["FOO"], "bar"; got != want {
				t.Fatalf("env FOO = %q, want %q", got, want)
			}
			if got, want := req.Msg.GetTag(), "job-1"; got != want {
				t.Fatalf("tag = %q, want %q", got, want)
			}
			if !req.Msg.GetStdin() {
				t.Fatal("stdin = false, want true")
			}

			if err := sendStartResponse(stream, 101); err != nil {
				return err
			}
			if err := sendStdoutResponse(stream, []byte("hello ")); err != nil {
				return err
			}
			if err := sendStderrResponse(stream, []byte("warn")); err != nil {
				return err
			}
			if err := sendStdoutResponse(stream, []byte("world")); err != nil {
				return err
			}
			return sendEndResponse(stream, 7, "boom")
		},
	}

	sb := newProcessTestSandbox(t, service)
	handle, err := sb.StartCommand(context.Background(), CommandStartRequest{
		Command:          []string{"python", "app.py"},
		WorkingDirectory: "/workspace",
		Environment:      map[string]string{"FOO": "bar"},
		Tag:              "job-1",
		Stdin:            true,
		OnStdout: func(chunk string) {
			stdoutChunks = append(stdoutChunks, chunk)
		},
		OnStderr: func(chunk string) {
			stderrChunks = append(stderrChunks, chunk)
		},
	})
	if err != nil {
		t.Fatalf("StartCommand() error = %v", err)
	}
	if got, want := handle.PID(), uint32(101); got != want {
		t.Fatalf("PID() = %d, want %d", got, want)
	}

	result, err := handle.Wait()
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if got, want := result.ExitCode, 7; got != want {
		t.Fatalf("ExitCode = %d, want %d", got, want)
	}
	if got, want := result.Stdout, "hello world"; got != want {
		t.Fatalf("Stdout = %q, want %q", got, want)
	}
	if got, want := result.Stderr, "warn"; got != want {
		t.Fatalf("Stderr = %q, want %q", got, want)
	}
	if got, want := result.Metadata["error"], "boom"; got != want {
		t.Fatalf("Metadata[error] = %q, want %q", got, want)
	}
	if got, want := handle.Stdout(), "hello world"; got != want {
		t.Fatalf("handle.Stdout() = %q, want %q", got, want)
	}
	if got, want := handle.Stderr(), "warn"; got != want {
		t.Fatalf("handle.Stderr() = %q, want %q", got, want)
	}
	if len(stdoutChunks) != 2 || stdoutChunks[0] != "hello " || stdoutChunks[1] != "world" {
		t.Fatalf("stdoutChunks = %#v, want two chunks", stdoutChunks)
	}
	if len(stderrChunks) != 1 || stderrChunks[0] != "warn" {
		t.Fatalf("stderrChunks = %#v, want one chunk", stderrChunks)
	}
}

func TestCommandListConnectInputAndKill(t *testing.T) {
	service := &testProcessService{
		t: t,
		list: func(_ context.Context, req *connect.Request[processpb.ListRequest]) (*connect.Response[processpb.ListResponse], error) {
			verifyProcessHeaders(t, req.Header(), false)
			return connect.NewResponse(&processpb.ListResponse{
				Processes: []*processpb.ProcessInfo{
					{
						Pid: 202,
						Tag: stringPtr("job-2"),
						Config: &processpb.ProcessConfig{
							Cmd:  "python",
							Args: []string{"app.py"},
							Envs: map[string]string{"APP": "1"},
							Cwd:  stringPtr("/workspace"),
						},
					},
				},
			}), nil
		},
		connectStream: func(_ context.Context, req *connect.Request[processpb.ConnectRequest], stream *connect.ServerStream[processpb.ConnectResponse]) error {
			verifyProcessHeaders(t, req.Header(), true)
			if got, want := req.Msg.GetProcess().GetPid(), uint32(202); got != want {
				t.Fatalf("connect pid = %d, want %d", got, want)
			}
			if err := sendConnectStart(stream, 202); err != nil {
				return err
			}
			if err := sendConnectStdout(stream, []byte("attached")); err != nil {
				return err
			}
			return sendConnectEnd(stream, 0, "")
		},
		sendInput: func(_ context.Context, req *connect.Request[processpb.SendInputRequest]) (*connect.Response[processpb.SendInputResponse], error) {
			verifyProcessHeaders(t, req.Header(), false)
			if got, want := req.Msg.GetProcess().GetPid(), uint32(202); got != want {
				t.Fatalf("sendInput pid = %d, want %d", got, want)
			}
			if got, want := string(req.Msg.GetInput().GetStdin()), "hello\n"; got != want {
				t.Fatalf("stdin payload = %q, want %q", got, want)
			}
			return connect.NewResponse(&processpb.SendInputResponse{}), nil
		},
		closeStdin: func(_ context.Context, req *connect.Request[processpb.CloseStdinRequest]) (*connect.Response[processpb.CloseStdinResponse], error) {
			verifyProcessHeaders(t, req.Header(), false)
			if got, want := req.Msg.GetProcess().GetPid(), uint32(202); got != want {
				t.Fatalf("closeStdin pid = %d, want %d", got, want)
			}
			return connect.NewResponse(&processpb.CloseStdinResponse{}), nil
		},
		sendSignal: func(_ context.Context, req *connect.Request[processpb.SendSignalRequest]) (*connect.Response[processpb.SendSignalResponse], error) {
			verifyProcessHeaders(t, req.Header(), false)
			switch req.Msg.GetProcess().GetPid() {
			case 202:
				if got, want := req.Msg.GetSignal(), processpb.Signal_SIGNAL_SIGKILL; got != want {
					t.Fatalf("signal = %v, want SIGKILL", got)
				}
				return connect.NewResponse(&processpb.SendSignalResponse{}), nil
			case 999:
				return nil, connect.NewError(connect.CodeNotFound, errors.New("missing process"))
			default:
				t.Fatalf("unexpected SendSignal pid: %d", req.Msg.GetProcess().GetPid())
				return nil, nil
			}
		},
	}

	sb := newProcessTestSandbox(t, service)

	processes, err := sb.ListProcesses(context.Background())
	if err != nil {
		t.Fatalf("ListProcesses() error = %v", err)
	}
	if len(processes) != 1 {
		t.Fatalf("len(processes) = %d, want 1", len(processes))
	}
	if got, want := processes[0].Command, "python"; got != want {
		t.Fatalf("processes[0].Command = %q, want %q", got, want)
	}
	if got, want := processes[0].WorkingDirectory, "/workspace"; got != want {
		t.Fatalf("processes[0].WorkingDirectory = %q, want %q", got, want)
	}

	handle, err := sb.ConnectProcess(context.Background(), 202, CommandConnectOptions{})
	if err != nil {
		t.Fatalf("ConnectProcess() error = %v", err)
	}
	if err := handle.SendStdin(context.Background(), []byte("hello\n")); err != nil {
		t.Fatalf("handle.SendStdin() error = %v", err)
	}
	if err := handle.CloseStdin(context.Background()); err != nil {
		t.Fatalf("handle.CloseStdin() error = %v", err)
	}
	killed, err := handle.Kill(context.Background())
	if err != nil {
		t.Fatalf("handle.Kill() error = %v", err)
	}
	if !killed {
		t.Fatalf("handle.Kill() = false, want true")
	}
	missing, err := sb.KillProcess(context.Background(), 999)
	if err != nil {
		t.Fatalf("KillProcess(missing) error = %v", err)
	}
	if missing {
		t.Fatalf("KillProcess(missing) = true, want false")
	}

	result, err := handle.Wait()
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if got, want := result.Stdout, "attached"; got != want {
		t.Fatalf("Stdout = %q, want %q", got, want)
	}
}

func TestPTYCreateConnectResizeAndInput(t *testing.T) {
	var callbackData [][]byte

	service := &testProcessService{
		t: t,
		start: func(_ context.Context, req *connect.Request[processpb.StartRequest], stream *connect.ServerStream[processpb.StartResponse]) error {
			verifyProcessHeaders(t, req.Header(), true)
			if req.Msg.GetPty() == nil {
				t.Fatal("CreatePTY start request missing PTY config")
			}
			if got, want := req.Msg.GetProcess().GetCmd(), "/bin/bash"; got != want {
				t.Fatalf("pty command = %q, want %q", got, want)
			}
			args := req.Msg.GetProcess().GetArgs()
			if len(args) != 2 || args[0] != "-i" || args[1] != "-l" {
				t.Fatalf("pty args = %#v, want [-i -l]", args)
			}
			if got, want := req.Msg.GetProcess().GetCwd(), "/workspace"; got != want {
				t.Fatalf("pty cwd = %q, want %q", got, want)
			}
			if got, want := req.Msg.GetTag(), "shell-1"; got != want {
				t.Fatalf("pty tag = %q, want %q", got, want)
			}
			if got, want := req.Msg.GetPty().GetSize().GetCols(), uint32(80); got != want {
				t.Fatalf("pty cols = %d, want %d", got, want)
			}
			if got, want := req.Msg.GetPty().GetSize().GetRows(), uint32(24); got != want {
				t.Fatalf("pty rows = %d, want %d", got, want)
			}
			envs := req.Msg.GetProcess().GetEnvs()
			if got, want := envs["APP"], "1"; got != want {
				t.Fatalf("env APP = %q, want %q", got, want)
			}
			if envs["TERM"] == "" || envs["LANG"] == "" || envs["LC_ALL"] == "" {
				t.Fatalf("expected TERM/LANG/LC_ALL defaults, got %#v", envs)
			}

			if err := sendStartResponse(stream, 303); err != nil {
				return err
			}
			if err := sendPTYResponse(stream, []byte("prompt> ")); err != nil {
				return err
			}
			return sendEndResponse(stream, 0, "")
		},
		connectStream: func(_ context.Context, req *connect.Request[processpb.ConnectRequest], stream *connect.ServerStream[processpb.ConnectResponse]) error {
			verifyProcessHeaders(t, req.Header(), true)
			if got, want := req.Msg.GetProcess().GetPid(), uint32(303); got != want {
				t.Fatalf("connect pty pid = %d, want %d", got, want)
			}
			if err := sendConnectStart(stream, 303); err != nil {
				return err
			}
			if err := sendConnectPTY(stream, []byte("attached")); err != nil {
				return err
			}
			return sendConnectEnd(stream, 0, "")
		},
		sendInput: func(_ context.Context, req *connect.Request[processpb.SendInputRequest]) (*connect.Response[processpb.SendInputResponse], error) {
			verifyProcessHeaders(t, req.Header(), false)
			if got, want := req.Msg.GetProcess().GetPid(), uint32(303); got != want {
				t.Fatalf("pty sendInput pid = %d, want %d", got, want)
			}
			if got, want := string(req.Msg.GetInput().GetPty()), "ls\n"; got != want {
				t.Fatalf("pty payload = %q, want %q", got, want)
			}
			return connect.NewResponse(&processpb.SendInputResponse{}), nil
		},
		update: func(_ context.Context, req *connect.Request[processpb.UpdateRequest]) (*connect.Response[processpb.UpdateResponse], error) {
			verifyProcessHeaders(t, req.Header(), false)
			if got, want := req.Msg.GetProcess().GetPid(), uint32(303); got != want {
				t.Fatalf("pty update pid = %d, want %d", got, want)
			}
			if got, want := req.Msg.GetPty().GetSize().GetCols(), uint32(120); got != want {
				t.Fatalf("pty update cols = %d, want %d", got, want)
			}
			if got, want := req.Msg.GetPty().GetSize().GetRows(), uint32(40); got != want {
				t.Fatalf("pty update rows = %d, want %d", got, want)
			}
			return connect.NewResponse(&processpb.UpdateResponse{}), nil
		},
		sendSignal: func(_ context.Context, req *connect.Request[processpb.SendSignalRequest]) (*connect.Response[processpb.SendSignalResponse], error) {
			verifyProcessHeaders(t, req.Header(), false)
			if got, want := req.Msg.GetProcess().GetPid(), uint32(303); got != want {
				t.Fatalf("pty kill pid = %d, want %d", got, want)
			}
			return connect.NewResponse(&processpb.SendSignalResponse{}), nil
		},
	}

	sb := newProcessTestSandbox(t, service)

	pty, err := sb.CreatePTY(context.Background(), PTYStartRequest{
		Cols:             80,
		Rows:             24,
		WorkingDirectory: "/workspace",
		Environment:      map[string]string{"APP": "1"},
		Tag:              "shell-1",
		OnData: func(data []byte) {
			callbackData = append(callbackData, append([]byte(nil), data...))
		},
	})
	if err != nil {
		t.Fatalf("CreatePTY() error = %v", err)
	}

	result, err := pty.Wait()
	if err != nil {
		t.Fatalf("pty.Wait() error = %v", err)
	}
	if got, want := string(result.PTYOutput), "prompt> "; got != want {
		t.Fatalf("PTYOutput = %q, want %q", got, want)
	}
	if len(callbackData) != 1 || string(callbackData[0]) != "prompt> " {
		t.Fatalf("callbackData = %#v, want one prompt chunk", callbackData)
	}

	attached, err := sb.ConnectPTY(context.Background(), 303, PTYConnectOptions{})
	if err != nil {
		t.Fatalf("ConnectPTY() error = %v", err)
	}
	if err := attached.SendPTY(context.Background(), []byte("ls\n")); err != nil {
		t.Fatalf("SendPTY() error = %v", err)
	}
	if err := attached.ResizePTY(context.Background(), 120, 40); err != nil {
		t.Fatalf("ResizePTY() error = %v", err)
	}
	killed, err := attached.Kill(context.Background())
	if err != nil {
		t.Fatalf("Kill() error = %v", err)
	}
	if !killed {
		t.Fatalf("Kill() = false, want true")
	}

	attachedResult, err := attached.Wait()
	if err != nil {
		t.Fatalf("attached.Wait() error = %v", err)
	}
	if got, want := string(attachedResult.PTYOutput), "attached"; got != want {
		t.Fatalf("attached PTYOutput = %q, want %q", got, want)
	}
}

func newProcessTestSandbox(t *testing.T, service *testProcessService) *Sandbox {
	t.Helper()

	path, handler := processconnect.NewProcessHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	api := newAPIClient(Config{})
	record := sandboxRecord{
		SandboxID:       "sbx-proc",
		TemplateID:      "tmpl-proc",
		EnvdVersion:     "0.4.4",
		EnvdAccessToken: "envd-token",
	}

	return &Sandbox{
		client: sandboxTransport{
			api:           api,
			record:        record,
			processClient: processconnect.NewProcessClient(server.Client(), server.URL),
		},
	}
}

type testProcessService struct {
	t             *testing.T
	list          func(context.Context, *connect.Request[processpb.ListRequest]) (*connect.Response[processpb.ListResponse], error)
	start         func(context.Context, *connect.Request[processpb.StartRequest], *connect.ServerStream[processpb.StartResponse]) error
	update        func(context.Context, *connect.Request[processpb.UpdateRequest]) (*connect.Response[processpb.UpdateResponse], error)
	sendInput     func(context.Context, *connect.Request[processpb.SendInputRequest]) (*connect.Response[processpb.SendInputResponse], error)
	sendSignal    func(context.Context, *connect.Request[processpb.SendSignalRequest]) (*connect.Response[processpb.SendSignalResponse], error)
	closeStdin    func(context.Context, *connect.Request[processpb.CloseStdinRequest]) (*connect.Response[processpb.CloseStdinResponse], error)
	connectStream func(context.Context, *connect.Request[processpb.ConnectRequest], *connect.ServerStream[processpb.ConnectResponse]) error
}

func (s *testProcessService) List(ctx context.Context, req *connect.Request[processpb.ListRequest]) (*connect.Response[processpb.ListResponse], error) {
	if s.list == nil {
		s.t.Fatal("List should not be called")
	}
	return s.list(ctx, req)
}

func (s *testProcessService) Connect(ctx context.Context, req *connect.Request[processpb.ConnectRequest], stream *connect.ServerStream[processpb.ConnectResponse]) error {
	if s.connectStream == nil {
		s.t.Fatal("Connect should not be called")
	}
	return s.connectStream(ctx, req, stream)
}

func (s *testProcessService) Start(ctx context.Context, req *connect.Request[processpb.StartRequest], stream *connect.ServerStream[processpb.StartResponse]) error {
	if s.start == nil {
		s.t.Fatal("Start should not be called")
	}
	return s.start(ctx, req, stream)
}

func (s *testProcessService) Update(ctx context.Context, req *connect.Request[processpb.UpdateRequest]) (*connect.Response[processpb.UpdateResponse], error) {
	if s.update == nil {
		s.t.Fatal("Update should not be called")
	}
	return s.update(ctx, req)
}

func (s *testProcessService) StreamInput(context.Context, *connect.ClientStream[processpb.StreamInputRequest]) (*connect.Response[processpb.StreamInputResponse], error) {
	s.t.Fatal("StreamInput should not be called")
	return nil, nil
}

func (s *testProcessService) SendInput(ctx context.Context, req *connect.Request[processpb.SendInputRequest]) (*connect.Response[processpb.SendInputResponse], error) {
	if s.sendInput == nil {
		s.t.Fatal("SendInput should not be called")
	}
	return s.sendInput(ctx, req)
}

func (s *testProcessService) SendSignal(ctx context.Context, req *connect.Request[processpb.SendSignalRequest]) (*connect.Response[processpb.SendSignalResponse], error) {
	if s.sendSignal == nil {
		s.t.Fatal("SendSignal should not be called")
	}
	return s.sendSignal(ctx, req)
}

func (s *testProcessService) CloseStdin(ctx context.Context, req *connect.Request[processpb.CloseStdinRequest]) (*connect.Response[processpb.CloseStdinResponse], error) {
	if s.closeStdin == nil {
		s.t.Fatal("CloseStdin should not be called")
	}
	return s.closeStdin(ctx, req)
}

func verifyProcessHeaders(t *testing.T, header http.Header, keepalive bool) {
	t.Helper()
	if got, want := header.Get("X-Access-Token"), "envd-token"; got != want {
		t.Fatalf("X-Access-Token = %q, want %q", got, want)
	}
	if got, want := header.Get("E2b-Sandbox-Id"), "sbx-proc"; got != want {
		t.Fatalf("E2b-Sandbox-Id = %q, want %q", got, want)
	}
	if got, want := header.Get("E2b-Sandbox-Port"), "49983"; got != want {
		t.Fatalf("E2b-Sandbox-Port = %q, want %q", got, want)
	}
	if got := header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want empty for modern envd", got)
	}
	if keepalive {
		if got, want := header.Get("Keepalive-Ping-Interval"), "50"; got != want {
			t.Fatalf("Keepalive-Ping-Interval = %q, want %q", got, want)
		}
	} else if got := header.Get("Keepalive-Ping-Interval"); got != "" {
		t.Fatalf("Keepalive-Ping-Interval = %q, want empty", got)
	}
}

func sendStartResponse(stream *connect.ServerStream[processpb.StartResponse], pid uint32) error {
	return stream.Send(&processpb.StartResponse{
		Event: &processpb.ProcessEvent{
			Event: &processpb.ProcessEvent_Start{
				Start: &processpb.ProcessEvent_StartEvent{Pid: pid},
			},
		},
	})
}

func sendStdoutResponse(stream *connect.ServerStream[processpb.StartResponse], data []byte) error {
	return stream.Send(&processpb.StartResponse{
		Event: &processpb.ProcessEvent{
			Event: &processpb.ProcessEvent_Data{
				Data: &processpb.ProcessEvent_DataEvent{
					Output: &processpb.ProcessEvent_DataEvent_Stdout{Stdout: data},
				},
			},
		},
	})
}

func sendStderrResponse(stream *connect.ServerStream[processpb.StartResponse], data []byte) error {
	return stream.Send(&processpb.StartResponse{
		Event: &processpb.ProcessEvent{
			Event: &processpb.ProcessEvent_Data{
				Data: &processpb.ProcessEvent_DataEvent{
					Output: &processpb.ProcessEvent_DataEvent_Stderr{Stderr: data},
				},
			},
		},
	})
}

func sendPTYResponse(stream *connect.ServerStream[processpb.StartResponse], data []byte) error {
	return stream.Send(&processpb.StartResponse{
		Event: &processpb.ProcessEvent{
			Event: &processpb.ProcessEvent_Data{
				Data: &processpb.ProcessEvent_DataEvent{
					Output: &processpb.ProcessEvent_DataEvent_Pty{Pty: data},
				},
			},
		},
	})
}

func sendEndResponse(stream *connect.ServerStream[processpb.StartResponse], exitCode int32, errorMessage string) error {
	end := &processpb.ProcessEvent_EndEvent{
		ExitCode: exitCode,
		Exited:   true,
		Status:   "completed",
	}
	if errorMessage != "" {
		end.Error = stringPtr(errorMessage)
	}
	return stream.Send(&processpb.StartResponse{
		Event: &processpb.ProcessEvent{
			Event: &processpb.ProcessEvent_End{End: end},
		},
	})
}

func sendConnectStart(stream *connect.ServerStream[processpb.ConnectResponse], pid uint32) error {
	return stream.Send(&processpb.ConnectResponse{
		Event: &processpb.ProcessEvent{
			Event: &processpb.ProcessEvent_Start{
				Start: &processpb.ProcessEvent_StartEvent{Pid: pid},
			},
		},
	})
}

func sendConnectStdout(stream *connect.ServerStream[processpb.ConnectResponse], data []byte) error {
	return stream.Send(&processpb.ConnectResponse{
		Event: &processpb.ProcessEvent{
			Event: &processpb.ProcessEvent_Data{
				Data: &processpb.ProcessEvent_DataEvent{
					Output: &processpb.ProcessEvent_DataEvent_Stdout{Stdout: data},
				},
			},
		},
	})
}

func sendConnectPTY(stream *connect.ServerStream[processpb.ConnectResponse], data []byte) error {
	return stream.Send(&processpb.ConnectResponse{
		Event: &processpb.ProcessEvent{
			Event: &processpb.ProcessEvent_Data{
				Data: &processpb.ProcessEvent_DataEvent{
					Output: &processpb.ProcessEvent_DataEvent_Pty{Pty: data},
				},
			},
		},
	})
}

func sendConnectEnd(stream *connect.ServerStream[processpb.ConnectResponse], exitCode int32, errorMessage string) error {
	end := &processpb.ProcessEvent_EndEvent{
		ExitCode: exitCode,
		Exited:   true,
		Status:   "completed",
	}
	if errorMessage != "" {
		end.Error = stringPtr(errorMessage)
	}
	return stream.Send(&processpb.ConnectResponse{
		Event: &processpb.ProcessEvent{
			Event: &processpb.ProcessEvent_End{End: end},
		},
	})
}
