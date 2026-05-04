package e2b

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRetryBackoffAndRetryAfter(t *testing.T) {
	policy := RetryPolicy{
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     250 * time.Millisecond,
		Multiplier:   2,
	}

	if got, want := retryBackoff(policy, 0), 100*time.Millisecond; got != want {
		t.Fatalf("retryBackoff(attempt 0) = %v, want %v", got, want)
	}
	if got, want := retryBackoff(policy, 1), 200*time.Millisecond; got != want {
		t.Fatalf("retryBackoff(attempt 1) = %v, want %v", got, want)
	}
	if got, want := retryBackoff(policy, 2), 250*time.Millisecond; got != want {
		t.Fatalf("retryBackoff(attempt 2) = %v, want capped %v", got, want)
	}

	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	if got, want := parseRetryAfter("3", now), 3*time.Second; got != want {
		t.Fatalf("parseRetryAfter(seconds) = %v, want %v", got, want)
	}
	httpDate := now.Add(5 * time.Second).Format(http.TimeFormat)
	if got, want := parseRetryAfter(httpDate, now), 5*time.Second; got != want {
		t.Fatalf("parseRetryAfter(http-date) = %v, want %v", got, want)
	}
	if got := parseRetryAfter("not a date", now); got != -1 {
		t.Fatalf("parseRetryAfter(invalid) = %v, want -1", got)
	}
}

func TestDoJSONSuccessAfterRetry(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "transient", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"ok": "yes"})
	}))
	defer server.Close()

	client := newAPIClient(Config{
		APIKey: "test-key",
		RetryPolicy: &RetryPolicy{
			MaxAttempts:  3,
			InitialDelay: time.Millisecond,
		},
	})
	var slept []time.Duration
	client.retrySleep = func(ctx context.Context, delay time.Duration) error {
		slept = append(slept, delay)
		return nil
	}

	var response map[string]string
	if _, _, err := client.doJSONWithResponse(context.Background(), http.MethodGet, server.URL, nil, &response, nil, nil); err != nil {
		t.Fatalf("doJSONWithResponse() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if got, want := response["ok"], "yes"; got != want {
		t.Fatalf("response[ok] = %q, want %q", got, want)
	}
	if len(slept) != 1 {
		t.Fatalf("len(slept) = %d, want 1", len(slept))
	}
}

func TestRetryAfterHeaderControlsSleep(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "2")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := newAPIClient(Config{RetryPolicy: &RetryPolicy{MaxAttempts: 2}})
	var slept []time.Duration
	client.retrySleep = func(ctx context.Context, delay time.Duration) error {
		slept = append(slept, delay)
		return nil
	}

	if _, _, err := client.doJSONWithResponse(context.Background(), http.MethodGet, server.URL, nil, nil, nil, nil); err != nil {
		t.Fatalf("doJSONWithResponse() error = %v", err)
	}
	if len(slept) != 1 || slept[0] != 2*time.Second {
		t.Fatalf("slept = %v, want [2s]", slept)
	}
}

func TestRetrySleepRespectsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "transient", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := newAPIClient(Config{RetryPolicy: &RetryPolicy{MaxAttempts: 2}})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, _, err := client.doJSONWithResponse(ctx, http.MethodGet, server.URL, nil, nil, nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("doJSONWithResponse() error = %v, want context deadline exceeded", err)
	}
}

func TestDisabledRetries(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "transient", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := newAPIClient(Config{RetryPolicy: NoRetries()})
	if _, _, err := client.doJSONWithResponse(context.Background(), http.MethodGet, server.URL, nil, nil, nil, nil); err == nil {
		t.Fatal("doJSONWithResponse() error = nil, want 500")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestNonRetryableStatusDoesNotRetry(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()

	client := newAPIClient(Config{})
	if _, _, err := client.doJSONWithResponse(context.Background(), http.MethodGet, server.URL, nil, nil, nil, nil); err == nil {
		t.Fatal("doJSONWithResponse() error = nil, want 400")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestUnsafePostDoesNotRetry(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "transient", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := newAPIClient(Config{RetryPolicy: &RetryPolicy{MaxAttempts: 3}})
	if _, _, err := client.doJSONWithResponse(context.Background(), http.MethodPost, server.URL+"/sandboxes", map[string]string{"templateID": "base"}, nil, nil, nil); err == nil {
		t.Fatal("doJSONWithResponse() error = nil, want 500")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestTemporaryNetworkErrorRetries(t *testing.T) {
	calls := 0
	client := newAPIClient(Config{RetryPolicy: &RetryPolicy{MaxAttempts: 2}})
	client.controlHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, temporaryTestError{}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Request:    req,
		}, nil
	})
	client.retrySleep = func(ctx context.Context, delay time.Duration) error { return nil }

	if _, _, err := client.doJSONWithResponse(context.Background(), http.MethodGet, "https://example.test", nil, nil, nil, nil); err != nil {
		t.Fatalf("doJSONWithResponse() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestEnvdFileRequestRetriesAndReplaysWriteBody(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got, want := r.Header.Get("X-Access-Token"), "envd-token"; got != want {
			t.Fatalf("X-Access-Token = %q, want %q", got, want)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if got, want := string(body), "payload"; got != want {
			t.Fatalf("body = %q, want %q", got, want)
		}
		if calls == 1 {
			http.Error(w, "transient", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := newAPIClient(Config{RetryPolicy: &RetryPolicy{MaxAttempts: 2}})
	client.retrySleep = func(ctx context.Context, delay time.Duration) error { return nil }

	status, _, _, err := client.doEnvdFileRequest(context.Background(), sandboxRecord{
		SandboxID:       "sbx-123",
		EnvdAccessToken: "envd-token",
	}, http.MethodPost, server.URL+"/files", []byte("payload"), "application/octet-stream")
	if err != nil {
		t.Fatalf("doEnvdFileRequest() error = %v", err)
	}
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", status)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type temporaryTestError struct{}

func (temporaryTestError) Error() string   { return "temporary network error" }
func (temporaryTestError) Timeout() bool   { return false }
func (temporaryTestError) Temporary() bool { return true }
