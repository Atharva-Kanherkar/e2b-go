package e2b

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"
	filesystempb "github.com/e2b-dev/infra/packages/shared/pkg/grpc/envd/filesystem"
)

// FilesystemEntryType describes the kind of object returned by filesystem RPCs.
type FilesystemEntryType string

const (
	FilesystemEntryTypeUnknown   FilesystemEntryType = "unknown"
	FilesystemEntryTypeFile      FilesystemEntryType = "file"
	FilesystemEntryTypeDirectory FilesystemEntryType = "directory"
	FilesystemEntryTypeSymlink   FilesystemEntryType = "symlink"
)

// EntryInfo describes a filesystem object inside the sandbox.
type EntryInfo struct {
	Name          string
	Type          FilesystemEntryType
	Path          string
	Size          int64
	Mode          uint32
	Permissions   string
	Owner         string
	Group         string
	ModifiedTime  time.Time
	SymlinkTarget string
}

// FilesystemEventType is emitted by directory watches.
type FilesystemEventType string

const (
	FilesystemEventTypeCreate FilesystemEventType = "create"
	FilesystemEventTypeWrite  FilesystemEventType = "write"
	FilesystemEventTypeRemove FilesystemEventType = "remove"
	FilesystemEventTypeRename FilesystemEventType = "rename"
	FilesystemEventTypeChmod  FilesystemEventType = "chmod"
)

// FilesystemEvent is delivered to directory watch callbacks.
type FilesystemEvent struct {
	Name string
	Type FilesystemEventType
}

// WatchOptions configures sandbox directory watches.
type WatchOptions struct {
	Recursive bool
}

// WatchHandle controls a running directory watch.
type WatchHandle struct {
	stop func()
	done chan struct{}

	mu  sync.Mutex
	err error
}

// Stop stops the watch.
func (h *WatchHandle) Stop() {
	h.stop()
}

// Wait blocks until the watch exits. A normal Stop returns nil.
func (h *WatchHandle) Wait() error {
	<-h.done

	h.mu.Lock()
	defer h.mu.Unlock()
	return h.err
}

// ListDir lists files, directories, and symlinks under the given path.
// Depth defaults to 1 when zero.
func (s *Sandbox) ListDir(ctx context.Context, path string, depth uint32) ([]EntryInfo, error) {
	transport, err := s.activeTransport()
	if err != nil {
		return nil, err
	}
	return s.listDirRPC(ctx, transport, path, depth)
}

// Stat returns metadata about a single sandbox filesystem entry.
func (s *Sandbox) Stat(ctx context.Context, path string) (EntryInfo, error) {
	transport, err := s.activeTransport()
	if err != nil {
		return EntryInfo{}, err
	}

	req := connect.NewRequest(&filesystempb.StatRequest{Path: path})
	setEnvdRPCHeaders(req, transport)

	resp, err := transport.filesClient.Stat(ctx, req)
	if err != nil {
		return EntryInfo{}, normalizeRPCError(err)
	}
	if resp.Msg.GetEntry() == nil {
		return EntryInfo{}, fmt.Errorf("e2b: stat %q returned no entry", path)
	}
	return convertFilesystemEntry(resp.Msg.GetEntry()), nil
}

// Exists returns true when a file or directory exists.
func (s *Sandbox) Exists(ctx context.Context, path string) (bool, error) {
	_, err := s.Stat(ctx, path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrFileNotFound) {
		return false, nil
	}
	return false, err
}

// MakeDir creates a directory. It returns false when the directory already
// exists.
func (s *Sandbox) MakeDir(ctx context.Context, path string) (bool, error) {
	transport, err := s.activeTransport()
	if err != nil {
		return false, err
	}

	req := connect.NewRequest(&filesystempb.MakeDirRequest{Path: path})
	setEnvdRPCHeaders(req, transport)

	_, err = transport.filesClient.MakeDir(ctx, req)
	if err == nil {
		return true, nil
	}

	var connectErr *connect.Error
	if errors.As(err, &connectErr) && connectErr.Code() == connect.CodeAlreadyExists {
		return false, nil
	}

	return false, normalizeRPCError(err)
}

// Rename renames or moves a filesystem entry.
func (s *Sandbox) Rename(ctx context.Context, oldPath string, newPath string) (EntryInfo, error) {
	transport, err := s.activeTransport()
	if err != nil {
		return EntryInfo{}, err
	}

	req := connect.NewRequest(&filesystempb.MoveRequest{
		Source:      oldPath,
		Destination: newPath,
	})
	setEnvdRPCHeaders(req, transport)

	resp, err := transport.filesClient.Move(ctx, req)
	if err != nil {
		return EntryInfo{}, normalizeRPCError(err)
	}
	if resp.Msg.GetEntry() == nil {
		return EntryInfo{}, fmt.Errorf("e2b: rename %q -> %q returned no entry", oldPath, newPath)
	}
	return convertFilesystemEntry(resp.Msg.GetEntry()), nil
}

// Remove deletes a file or directory.
func (s *Sandbox) Remove(ctx context.Context, path string) error {
	transport, err := s.activeTransport()
	if err != nil {
		return err
	}

	req := connect.NewRequest(&filesystempb.RemoveRequest{Path: path})
	setEnvdRPCHeaders(req, transport)

	_, err = transport.filesClient.Remove(ctx, req)
	if err != nil {
		return normalizeRPCError(err)
	}
	return nil
}

// WatchDir starts watching a directory for filesystem events.
func (s *Sandbox) WatchDir(ctx context.Context, path string, options WatchOptions, onEvent func(FilesystemEvent)) (*WatchHandle, error) {
	transport, err := s.activeTransport()
	if err != nil {
		return nil, err
	}

	watchCtx, cancel := context.WithCancel(ctx)
	req := connect.NewRequest(&filesystempb.WatchDirRequest{
		Path:      path,
		Recursive: options.Recursive,
	})
	req.Header().Set("Keepalive-Ping-Interval", "50")
	setEnvdRPCHeaders(req, transport)

	stream, err := transport.filesClient.WatchDir(watchCtx, req)
	if err != nil {
		cancel()
		return nil, normalizeRPCError(err)
	}

	if !stream.Receive() {
		defer stream.Close()
		cancel()
		if err := stream.Err(); err != nil {
			return nil, normalizeRPCError(err)
		}
		return nil, fmt.Errorf("e2b: expected watch start event")
	}
	if stream.Msg().GetStart() == nil {
		defer stream.Close()
		cancel()
		return nil, fmt.Errorf("e2b: expected watch start event")
	}

	handle := &WatchHandle{
		stop: cancel,
		done: make(chan struct{}),
	}

	go handle.consumeWatchStream(stream, onEvent)
	return handle, nil
}

func (s *Sandbox) listDirRPC(ctx context.Context, transport sandboxTransport, path string, depth uint32) ([]EntryInfo, error) {
	if depth == 0 {
		depth = 1
	}

	req := connect.NewRequest(&filesystempb.ListDirRequest{
		Path:  path,
		Depth: depth,
	})
	setEnvdRPCHeaders(req, transport)

	resp, err := transport.filesClient.ListDir(ctx, req)
	if err != nil {
		return nil, normalizeRPCError(err)
	}

	items := make([]EntryInfo, 0, len(resp.Msg.GetEntries()))
	for _, entry := range resp.Msg.GetEntries() {
		items = append(items, convertFilesystemEntry(entry))
	}
	return items, nil
}

func (s *Sandbox) listFilesRPC(ctx context.Context, transport sandboxTransport, prefix string) ([]FileInfo, error) {
	entries, err := s.listDirRPC(ctx, transport, prefix, 32)
	if err != nil {
		return nil, err
	}

	items := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.Type != FilesystemEntryTypeFile {
			continue
		}
		items = append(items, FileInfo{
			Path: entry.Path,
			Size: entry.Size,
		})
	}
	return items, nil
}

func setEnvdRPCHeaders[T any](req *connect.Request[T], transport sandboxTransport) {
	if authHeader := legacySandboxAuthHeader(transport.record.EnvdVersion); authHeader != "" {
		req.Header().Set("Authorization", authHeader)
	}
	transport.api.setEnvdHeaders(req.Header(), transport.record)
}

func convertFilesystemEntry(entry *filesystempb.EntryInfo) EntryInfo {
	if entry == nil {
		return EntryInfo{}
	}

	var modified time.Time
	if entry.GetModifiedTime() != nil {
		modified = entry.GetModifiedTime().AsTime()
	}

	return EntryInfo{
		Name:          entry.GetName(),
		Type:          mapFilesystemEntryType(entry.GetType()),
		Path:          entry.GetPath(),
		Size:          entry.GetSize(),
		Mode:          entry.GetMode(),
		Permissions:   entry.GetPermissions(),
		Owner:         entry.GetOwner(),
		Group:         entry.GetGroup(),
		ModifiedTime:  modified,
		SymlinkTarget: entry.GetSymlinkTarget(),
	}
}

func mapFilesystemEntryType(value filesystempb.FileType) FilesystemEntryType {
	switch value {
	case filesystempb.FileType_FILE_TYPE_FILE:
		return FilesystemEntryTypeFile
	case filesystempb.FileType_FILE_TYPE_DIRECTORY:
		return FilesystemEntryTypeDirectory
	case filesystempb.FileType_FILE_TYPE_SYMLINK:
		return FilesystemEntryTypeSymlink
	default:
		return FilesystemEntryTypeUnknown
	}
}

func mapFilesystemEventType(value filesystempb.EventType) (FilesystemEventType, bool) {
	switch value {
	case filesystempb.EventType_EVENT_TYPE_CREATE:
		return FilesystemEventTypeCreate, true
	case filesystempb.EventType_EVENT_TYPE_WRITE:
		return FilesystemEventTypeWrite, true
	case filesystempb.EventType_EVENT_TYPE_REMOVE:
		return FilesystemEventTypeRemove, true
	case filesystempb.EventType_EVENT_TYPE_RENAME:
		return FilesystemEventTypeRename, true
	case filesystempb.EventType_EVENT_TYPE_CHMOD:
		return FilesystemEventTypeChmod, true
	default:
		return "", false
	}
}

func (h *WatchHandle) consumeWatchStream(stream *connect.ServerStreamForClient[filesystempb.WatchDirResponse], onEvent func(FilesystemEvent)) {
	defer close(h.done)
	defer stream.Close()

	for stream.Receive() {
		event := stream.Msg().GetFilesystem()
		if event == nil {
			continue
		}
		eventType, ok := mapFilesystemEventType(event.GetType())
		if !ok {
			continue
		}
		if onEvent != nil {
			onEvent(FilesystemEvent{
				Name: event.GetName(),
				Type: eventType,
			})
		}
	}

	err := stream.Err()
	if errors.Is(err, context.Canceled) {
		err = nil
	}
	if err != nil {
		err = normalizeRPCError(err)
	}

	h.mu.Lock()
	h.err = err
	h.mu.Unlock()
}
