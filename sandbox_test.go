package e2b

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDestroyAutoRetriesTransientFailure(t *testing.T) {
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("method = %s, want DELETE", r.Method)
		}
		if r.URL.Path != "/sandboxes/sbx-123" {
			t.Fatalf("path = %s, want /sandboxes/sbx-123", r.URL.Path)
		}

		deleteCalls++
		if deleteCalls == 1 {
			http.Error(w, "transient failure", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClientWithConfig(Config{
		APIKey:         "test-key",
		APIBaseURL:     server.URL,
		RequestTimeout: time.Second,
	})
	sb := &Sandbox{
		client: sandboxTransport{
			api:    client.api,
			record: sandboxRecord{SandboxID: "sbx-123"},
		},
	}

	// The retry policy handles the 500 automatically; Destroy returns nil.
	if err := sb.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy() error = %v, want nil (auto-retry should succeed)", err)
	}
	if deleteCalls != 2 {
		t.Fatalf("deleteCalls = %d, want 2 (one transient + one success)", deleteCalls)
	}
	// Idempotent: already destroyed, no further HTTP calls.
	if err := sb.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy() idempotent call error = %v, want nil", err)
	}
	if deleteCalls != 2 {
		t.Fatalf("deleteCalls after idempotent = %d, want 2", deleteCalls)
	}
}

func TestCleanupSandboxAfterCreateFailureReturnsOriginalError(t *testing.T) {
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deleteCalls++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClientWithConfig(Config{
		APIKey:         "test-key",
		APIBaseURL:     server.URL,
		RequestTimeout: time.Second,
	})
	sb := &Sandbox{
		client: sandboxTransport{
			api:    client.api,
			record: sandboxRecord{SandboxID: "sbx-cleanup"},
		},
	}

	expectedErr := errors.New("install additional packages failed")
	if err := client.cleanupSandboxAfterCreateFailure(sb, expectedErr); !errors.Is(err, expectedErr) {
		t.Fatalf("cleanupSandboxAfterCreateFailure() = %v, want original error %v", err, expectedErr)
	}
	if deleteCalls != 1 {
		t.Fatalf("deleteCalls = %d, want 1", deleteCalls)
	}
}
