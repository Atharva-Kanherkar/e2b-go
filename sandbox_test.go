package e2b

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDestroyFailedRequestIsRetryable(t *testing.T) {
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
		RetryPolicy:    RetryPolicy{MaxAttempts: 1},
	})
	sb := &Sandbox{
		client: sandboxTransport{
			api:    client.api,
			record: sandboxRecord{SandboxID: "sbx-123"},
		},
	}

	if err := sb.Destroy(context.Background()); err == nil {
		t.Fatal("Destroy() error = nil, want transient failure")
	}
	if err := sb.ensureActive(); err != nil {
		t.Fatalf("ensureActive() after failed destroy = %v, want nil", err)
	}
	if err := sb.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy() retry error = %v, want nil", err)
	}
	if err := sb.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy() after success error = %v, want nil", err)
	}
	if deleteCalls != 2 {
		t.Fatalf("deleteCalls = %d, want 2", deleteCalls)
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
