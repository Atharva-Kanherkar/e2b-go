package e2b

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestVolumeControlPlaneMethods(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("X-API-KEY"), "test-key"; got != want {
			t.Fatalf("X-API-KEY = %q, want %q", got, want)
		}

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/volumes":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read create body: %v", err)
			}
			if got, want := string(body), "{\"name\":\"cache\"}"; got != want {
				t.Fatalf("create body = %q, want %q", got, want)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"volumeID":"vol-1","name":"cache","token":"jwt-token"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/volumes/vol-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"volumeID":"vol-1","name":"cache","token":"jwt-token"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/volumes/missing":
			http.Error(w, "missing volume", http.StatusNotFound)
		case r.Method == http.MethodGet && r.URL.Path == "/volumes":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"volumeID":"vol-1","name":"cache"}]`))
		case r.Method == http.MethodDelete && r.URL.Path == "/volumes/vol-1":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.Path == "/volumes/missing":
			http.Error(w, "missing volume", http.StatusNotFound)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClientWithConfig(Config{
		APIKey:         "test-key",
		APIBaseURL:     server.URL,
		RequestTimeout: time.Second,
	})

	volume, err := client.CreateVolume(context.Background(), "cache")
	if err != nil {
		t.Fatalf("CreateVolume() error = %v", err)
	}
	if got, want := volume.ID(), "vol-1"; got != want {
		t.Fatalf("volume.ID() = %q, want %q", got, want)
	}
	if got, want := volume.Name(), "cache"; got != want {
		t.Fatalf("volume.Name() = %q, want %q", got, want)
	}

	info, err := client.GetVolumeInfo(context.Background(), "vol-1")
	if err != nil {
		t.Fatalf("GetVolumeInfo() error = %v", err)
	}
	if got, want := info.Token, "jwt-token"; got != want {
		t.Fatalf("info.Token = %q, want %q", got, want)
	}

	connected, err := client.ConnectVolume(context.Background(), "vol-1")
	if err != nil {
		t.Fatalf("ConnectVolume() error = %v", err)
	}
	if got, want := connected.ID(), "vol-1"; got != want {
		t.Fatalf("connected.ID() = %q, want %q", got, want)
	}

	volumes, err := client.ListVolumes(context.Background())
	if err != nil {
		t.Fatalf("ListVolumes() error = %v", err)
	}
	if len(volumes) != 1 || volumes[0].VolumeID != "vol-1" {
		t.Fatalf("volumes = %#v, want one volume", volumes)
	}

	destroyed, err := connected.Destroy(context.Background())
	if err != nil {
		t.Fatalf("connected.Destroy() error = %v", err)
	}
	if !destroyed {
		t.Fatalf("connected.Destroy() = false, want true")
	}

	destroyed, err = client.DestroyVolume(context.Background(), "missing")
	if err != nil {
		t.Fatalf("DestroyVolume(missing) error = %v", err)
	}
	if destroyed {
		t.Fatalf("DestroyVolume(missing) = true, want false")
	}

	if _, err := client.GetVolumeInfo(context.Background(), "missing"); !errors.Is(err, ErrVolumeNotFound) {
		t.Fatalf("GetVolumeInfo(missing) error = %v, want ErrVolumeNotFound", err)
	}
}

func TestVolumeContentMethods(t *testing.T) {
	uid := uint32(1000)
	gid := uint32(1000)
	mode := uint32(0644)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer jwt-token"; got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}
		if got := r.Header.Get("X-API-KEY"); got != "" {
			t.Fatalf("X-API-KEY = %q, want empty on volume content API", got)
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/volumecontent/vol-1/dir":
			if got, want := r.URL.Query().Get("path"), "/data"; got != want {
				t.Fatalf("list path = %q, want %q", got, want)
			}
			if got, want := r.URL.Query().Get("depth"), "2"; got != want {
				t.Fatalf("list depth = %q, want %q", got, want)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"name":"data","type":"directory","path":"/data","size":0,"mode":493,"uid":0,"gid":0,"atime":"2026-04-23T09:00:00Z","mtime":"2026-04-23T09:00:00Z","ctime":"2026-04-23T09:00:00Z"},
				{"name":"file.txt","type":"file","path":"/data/file.txt","size":5,"mode":420,"uid":1000,"gid":1000,"atime":"2026-04-23T09:00:01Z","mtime":"2026-04-23T09:00:02Z","ctime":"2026-04-23T09:00:03Z"}
			]`))
		case r.Method == http.MethodPost && r.URL.Path == "/volumecontent/vol-1/dir":
			if got, want := r.URL.Query().Get("path"), "/data/new"; got != want {
				t.Fatalf("mkdir path = %q, want %q", got, want)
			}
			if got, want := r.URL.Query().Get("uid"), "1000"; got != want {
				t.Fatalf("mkdir uid = %q, want %q", got, want)
			}
			if got, want := r.URL.Query().Get("gid"), "1000"; got != want {
				t.Fatalf("mkdir gid = %q, want %q", got, want)
			}
			if got, want := r.URL.Query().Get("mode"), "420"; got != want {
				t.Fatalf("mkdir mode = %q, want %q", got, want)
			}
			if got, want := r.URL.Query().Get("force"), "true"; got != want {
				t.Fatalf("mkdir force = %q, want %q", got, want)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"name":"new","type":"directory","path":"/data/new","size":0,"mode":420,"uid":1000,"gid":1000,"atime":"2026-04-23T09:10:00Z","mtime":"2026-04-23T09:10:00Z","ctime":"2026-04-23T09:10:00Z"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/volumecontent/vol-1/path" && r.URL.Query().Get("path") == "/data/file.txt":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"file.txt","type":"file","path":"/data/file.txt","size":5,"mode":420,"uid":1000,"gid":1000,"atime":"2026-04-23T09:00:01Z","mtime":"2026-04-23T09:00:02Z","ctime":"2026-04-23T09:00:03Z"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/volumecontent/vol-1/path" && r.URL.Query().Get("path") == "/missing":
			http.Error(w, "missing path", http.StatusNotFound)
		case r.Method == http.MethodPatch && r.URL.Path == "/volumecontent/vol-1/path":
			if got, want := r.URL.Query().Get("path"), "/data/file.txt"; got != want {
				t.Fatalf("patch path = %q, want %q", got, want)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read patch body: %v", err)
			}
			if got, want := string(body), "{\"uid\":1000,\"gid\":1000,\"mode\":420}"; got != want {
				t.Fatalf("patch body = %q, want %q", got, want)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"file.txt","type":"file","path":"/data/file.txt","size":5,"mode":420,"uid":1000,"gid":1000,"atime":"2026-04-23T09:00:01Z","mtime":"2026-04-23T09:00:02Z","ctime":"2026-04-23T09:00:03Z"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/volumecontent/vol-1/file":
			if got, want := r.URL.Query().Get("path"), "/data/file.txt"; got != want {
				t.Fatalf("read path = %q, want %q", got, want)
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("hello"))
		case r.Method == http.MethodPut && r.URL.Path == "/volumecontent/vol-1/file":
			if got, want := r.URL.Query().Get("path"), "/data/file.txt"; got != want {
				t.Fatalf("write path = %q, want %q", got, want)
			}
			if got, want := r.Header.Get("Content-Type"), "application/octet-stream"; got != want {
				t.Fatalf("write Content-Type = %q, want %q", got, want)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read write body: %v", err)
			}
			if !bytes.Equal(body, []byte("updated")) {
				t.Fatalf("write body = %q, want updated", string(body))
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"name":"file.txt","type":"file","path":"/data/file.txt","size":7,"mode":420,"uid":1000,"gid":1000,"atime":"2026-04-23T09:20:01Z","mtime":"2026-04-23T09:20:02Z","ctime":"2026-04-23T09:20:03Z"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/volumecontent/vol-1/path":
			if got, want := r.URL.Query().Get("path"), "/data/file.txt"; got != want {
				t.Fatalf("remove path = %q, want %q", got, want)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClientWithConfig(Config{
		APIKey:         "test-key",
		APIBaseURL:     server.URL,
		RequestTimeout: time.Second,
	})
	volume := client.newVolume(volumeAndTokenRecord{
		VolumeID: "vol-1",
		Name:     "cache",
		Token:    "jwt-token",
	})

	entries, err := volume.List(context.Background(), "/data", 2)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if got, want := entries[0].Type, VolumeEntryTypeDirectory; got != want {
		t.Fatalf("entries[0].Type = %q, want %q", got, want)
	}

	mkdirEntry, err := volume.MakeDir(context.Background(), "/data/new", VolumeWriteOptions{
		UID:   &uid,
		GID:   &gid,
		Mode:  &mode,
		Force: true,
	})
	if err != nil {
		t.Fatalf("MakeDir() error = %v", err)
	}
	if got, want := mkdirEntry.Path, "/data/new"; got != want {
		t.Fatalf("mkdirEntry.Path = %q, want %q", got, want)
	}

	stat, err := volume.Stat(context.Background(), "/data/file.txt")
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got, want := stat.Type, VolumeEntryTypeFile; got != want {
		t.Fatalf("stat.Type = %q, want %q", got, want)
	}

	exists, err := volume.Exists(context.Background(), "/data/file.txt")
	if err != nil {
		t.Fatalf("Exists(existing) error = %v", err)
	}
	if !exists {
		t.Fatalf("Exists(existing) = false, want true")
	}

	exists, err = volume.Exists(context.Background(), "/missing")
	if err != nil {
		t.Fatalf("Exists(missing) error = %v", err)
	}
	if exists {
		t.Fatalf("Exists(missing) = true, want false")
	}

	updated, err := volume.UpdateMetadata(context.Background(), "/data/file.txt", VolumeMetadataOptions{
		UID:  &uid,
		GID:  &gid,
		Mode: &mode,
	})
	if err != nil {
		t.Fatalf("UpdateMetadata() error = %v", err)
	}
	if got, want := updated.UID, uint32(1000); got != want {
		t.Fatalf("updated.UID = %d, want %d", got, want)
	}

	content, err := volume.ReadFile(context.Background(), "/data/file.txt")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := string(content), "hello"; got != want {
		t.Fatalf("ReadFile() = %q, want %q", got, want)
	}

	written, err := volume.WriteFile(context.Background(), "/data/file.txt", []byte("updated"), VolumeWriteOptions{
		UID:   &uid,
		GID:   &gid,
		Mode:  &mode,
		Force: true,
	})
	if err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if got, want := written.Size, int64(7); got != want {
		t.Fatalf("written.Size = %d, want %d", got, want)
	}

	if err := volume.Remove(context.Background(), "/data/file.txt"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
}
