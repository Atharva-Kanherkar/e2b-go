package e2b

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	filesystempb "github.com/e2b-dev/infra/packages/shared/pkg/grpc/envd/filesystem"
	"github.com/e2b-dev/infra/packages/shared/pkg/grpc/envd/filesystem/filesystemconnect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestFilesystemRPCMethods(t *testing.T) {
	sb := newFilesystemTestSandbox(t)

	entries, err := sb.ListDir(context.Background(), "/workspace", 3)
	if err != nil {
		t.Fatalf("ListDir() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if got, want := entries[0].Type, FilesystemEntryTypeDirectory; got != want {
		t.Fatalf("entries[0].Type = %q, want %q", got, want)
	}
	if got, want := entries[1].Type, FilesystemEntryTypeFile; got != want {
		t.Fatalf("entries[1].Type = %q, want %q", got, want)
	}

	created, err := sb.MakeDir(context.Background(), "/workspace/new")
	if err != nil {
		t.Fatalf("MakeDir(new) error = %v", err)
	}
	if !created {
		t.Fatalf("MakeDir(new) = false, want true")
	}

	created, err = sb.MakeDir(context.Background(), "/workspace/existing")
	if err != nil {
		t.Fatalf("MakeDir(existing) error = %v", err)
	}
	if created {
		t.Fatalf("MakeDir(existing) = true, want false")
	}

	info, err := sb.Stat(context.Background(), "/workspace/item")
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got, want := info.Path, "/workspace/item"; got != want {
		t.Fatalf("Stat().Path = %q, want %q", got, want)
	}
	if info.ModifiedTime.IsZero() {
		t.Fatal("Stat().ModifiedTime is zero, want populated timestamp")
	}

	renamed, err := sb.Rename(context.Background(), "/workspace/item", "/workspace/renamed")
	if err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	if got, want := renamed.Path, "/workspace/renamed"; got != want {
		t.Fatalf("Rename().Path = %q, want %q", got, want)
	}

	if err := sb.Remove(context.Background(), "/workspace/renamed"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
}

func TestSandboxExistsHandlesNotFound(t *testing.T) {
	sb := newFilesystemTestSandbox(t)

	exists, err := sb.Exists(context.Background(), "/workspace/item")
	if err != nil {
		t.Fatalf("Exists(existing) error = %v", err)
	}
	if !exists {
		t.Fatalf("Exists(existing) = false, want true")
	}

	exists, err = sb.Exists(context.Background(), "/missing")
	if err != nil {
		t.Fatalf("Exists(missing) error = %v", err)
	}
	if exists {
		t.Fatalf("Exists(missing) = true, want false")
	}

	_, err = sb.Stat(context.Background(), "/missing")
	if !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("Stat(missing) error = %v, want ErrFileNotFound", err)
	}

	exists, err = sb.Exists(context.Background(), "/explode")
	if err == nil {
		t.Fatal("Exists(explode) error = nil, want propagated RPC error")
	}
	if exists {
		t.Fatalf("Exists(explode) = true, want false")
	}
}

func TestWatchHandleLifecycle(t *testing.T) {
	sb := newFilesystemTestSandbox(t)

	events := make(chan FilesystemEvent, 1)
	handle, err := sb.WatchDir(context.Background(), "/workspace/watch", WatchOptions{Recursive: true}, func(event FilesystemEvent) {
		events <- event
	})
	if err != nil {
		t.Fatalf("WatchDir() error = %v", err)
	}

	select {
	case event := <-events:
		if got, want := event.Name, "nested/file.txt"; got != want {
			t.Fatalf("event.Name = %q, want %q", got, want)
		}
		if got, want := event.Type, FilesystemEventTypeCreate; got != want {
			t.Fatalf("event.Type = %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for filesystem event")
	}

	handle.Stop()
	if err := handle.Wait(); err != nil {
		t.Fatalf("Wait() error = %v, want nil after Stop", err)
	}
}

func newFilesystemTestSandbox(t *testing.T) *Sandbox {
	t.Helper()

	service := &testFilesystemService{t: t}
	path, handler := filesystemconnect.NewFilesystemHandler(service)

	mux := http.NewServeMux()
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	api := newAPIClient(Config{})
	record := sandboxRecord{
		SandboxID:       "sbx-fs",
		TemplateID:      "tmpl-fs",
		EnvdVersion:     "0.4.4",
		EnvdAccessToken: "envd-token",
	}

	return &Sandbox{
		client: sandboxTransport{
			api:         api,
			record:      record,
			filesClient: filesystemconnect.NewFilesystemClient(server.Client(), server.URL),
		},
	}
}

type testFilesystemService struct {
	t *testing.T
}

func (s *testFilesystemService) Stat(_ context.Context, req *connect.Request[filesystempb.StatRequest]) (*connect.Response[filesystempb.StatResponse], error) {
	s.verifyHeaders(req.Header())

	switch req.Msg.GetPath() {
	case "/workspace/item":
		return connect.NewResponse(&filesystempb.StatResponse{
			Entry: testFilesystemEntry("item", filesystempb.FileType_FILE_TYPE_FILE, "/workspace/item"),
		}), nil
	case "/missing":
		return nil, connect.NewError(connect.CodeNotFound, errors.New("missing"))
	case "/explode":
		return nil, connect.NewError(connect.CodeInternal, errors.New("explode"))
	default:
		s.t.Fatalf("unexpected Stat path: %q", req.Msg.GetPath())
		return nil, nil
	}
}

func (s *testFilesystemService) MakeDir(_ context.Context, req *connect.Request[filesystempb.MakeDirRequest]) (*connect.Response[filesystempb.MakeDirResponse], error) {
	s.verifyHeaders(req.Header())

	switch req.Msg.GetPath() {
	case "/workspace/new":
		return connect.NewResponse(&filesystempb.MakeDirResponse{
			Entry: testFilesystemEntry("new", filesystempb.FileType_FILE_TYPE_DIRECTORY, "/workspace/new"),
		}), nil
	case "/workspace/existing":
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("already exists"))
	default:
		s.t.Fatalf("unexpected MakeDir path: %q", req.Msg.GetPath())
		return nil, nil
	}
}

func (s *testFilesystemService) Move(_ context.Context, req *connect.Request[filesystempb.MoveRequest]) (*connect.Response[filesystempb.MoveResponse], error) {
	s.verifyHeaders(req.Header())
	if got, want := req.Msg.GetSource(), "/workspace/item"; got != want {
		s.t.Fatalf("Move source = %q, want %q", got, want)
	}
	if got, want := req.Msg.GetDestination(), "/workspace/renamed"; got != want {
		s.t.Fatalf("Move destination = %q, want %q", got, want)
	}

	return connect.NewResponse(&filesystempb.MoveResponse{
		Entry: testFilesystemEntry("renamed", filesystempb.FileType_FILE_TYPE_FILE, "/workspace/renamed"),
	}), nil
}

func (s *testFilesystemService) ListDir(_ context.Context, req *connect.Request[filesystempb.ListDirRequest]) (*connect.Response[filesystempb.ListDirResponse], error) {
	s.verifyHeaders(req.Header())
	if got, want := req.Msg.GetPath(), "/workspace"; got != want {
		s.t.Fatalf("ListDir path = %q, want %q", got, want)
	}
	if got, want := req.Msg.GetDepth(), uint32(3); got != want {
		s.t.Fatalf("ListDir depth = %d, want %d", got, want)
	}

	return connect.NewResponse(&filesystempb.ListDirResponse{
		Entries: []*filesystempb.EntryInfo{
			testFilesystemEntry("workspace", filesystempb.FileType_FILE_TYPE_DIRECTORY, "/workspace"),
			testFilesystemEntry("item", filesystempb.FileType_FILE_TYPE_FILE, "/workspace/item"),
		},
	}), nil
}

func (s *testFilesystemService) Remove(_ context.Context, req *connect.Request[filesystempb.RemoveRequest]) (*connect.Response[filesystempb.RemoveResponse], error) {
	s.verifyHeaders(req.Header())
	if got, want := req.Msg.GetPath(), "/workspace/renamed"; got != want {
		s.t.Fatalf("Remove path = %q, want %q", got, want)
	}
	return connect.NewResponse(&filesystempb.RemoveResponse{}), nil
}

func (s *testFilesystemService) WatchDir(ctx context.Context, req *connect.Request[filesystempb.WatchDirRequest], stream *connect.ServerStream[filesystempb.WatchDirResponse]) error {
	s.verifyHeaders(req.Header())
	if got, want := req.Msg.GetPath(), "/workspace/watch"; got != want {
		s.t.Fatalf("WatchDir path = %q, want %q", got, want)
	}
	if !req.Msg.GetRecursive() {
		s.t.Fatal("WatchDir recursive = false, want true")
	}

	if err := stream.Send(&filesystempb.WatchDirResponse{
		Event: &filesystempb.WatchDirResponse_Start{
			Start: &filesystempb.WatchDirResponse_StartEvent{},
		},
	}); err != nil {
		return err
	}
	if err := stream.Send(&filesystempb.WatchDirResponse{
		Event: &filesystempb.WatchDirResponse_Filesystem{
			Filesystem: &filesystempb.FilesystemEvent{
				Name: "nested/file.txt",
				Type: filesystempb.EventType_EVENT_TYPE_CREATE,
			},
		},
	}); err != nil {
		return err
	}
	if err := stream.Send(&filesystempb.WatchDirResponse{
		Event: &filesystempb.WatchDirResponse_Keepalive{
			Keepalive: &filesystempb.WatchDirResponse_KeepAlive{},
		},
	}); err != nil {
		return err
	}

	<-ctx.Done()
	return nil
}

func (s *testFilesystemService) CreateWatcher(context.Context, *connect.Request[filesystempb.CreateWatcherRequest]) (*connect.Response[filesystempb.CreateWatcherResponse], error) {
	s.t.Fatal("CreateWatcher should not be called")
	return nil, nil
}

func (s *testFilesystemService) GetWatcherEvents(context.Context, *connect.Request[filesystempb.GetWatcherEventsRequest]) (*connect.Response[filesystempb.GetWatcherEventsResponse], error) {
	s.t.Fatal("GetWatcherEvents should not be called")
	return nil, nil
}

func (s *testFilesystemService) RemoveWatcher(context.Context, *connect.Request[filesystempb.RemoveWatcherRequest]) (*connect.Response[filesystempb.RemoveWatcherResponse], error) {
	s.t.Fatal("RemoveWatcher should not be called")
	return nil, nil
}

func (s *testFilesystemService) verifyHeaders(header http.Header) {
	s.t.Helper()
	if got, want := header.Get("X-Access-Token"), "envd-token"; got != want {
		s.t.Fatalf("X-Access-Token = %q, want %q", got, want)
	}
	if got, want := header.Get("E2b-Sandbox-Id"), "sbx-fs"; got != want {
		s.t.Fatalf("E2b-Sandbox-Id = %q, want %q", got, want)
	}
	if got, want := header.Get("E2b-Sandbox-Port"), "49983"; got != want {
		s.t.Fatalf("E2b-Sandbox-Port = %q, want %q", got, want)
	}
	if got := header.Get("Authorization"); got != "" {
		s.t.Fatalf("Authorization = %q, want empty for modern envd", got)
	}
}

func testFilesystemEntry(name string, fileType filesystempb.FileType, path string) *filesystempb.EntryInfo {
	return &filesystempb.EntryInfo{
		Name:         name,
		Type:         fileType,
		Path:         path,
		Size:         123,
		Mode:         0644,
		Permissions:  "rw-r--r--",
		Owner:        "root",
		Group:        "root",
		ModifiedTime: timestamppb.New(time.Date(2026, 4, 23, 9, 30, 0, 0, time.UTC)),
	}
}
