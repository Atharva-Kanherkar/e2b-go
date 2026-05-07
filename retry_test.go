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

func TestControlPlaneJSONRetriesSuccessAfterTransientStatus(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got, want := r.Method, http.MethodPost; got != want {
			t.Fatalf("method = %s, want %s", got, want)
		}
		if got, want := r.Header.Get("X-API-KEY"), "test-key"; got != want {
			t.Fatalf("X-API-KEY = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("Content-Type"), "application/json"; got != want {
			t.Fatalf("Content-Type = %q, want %q", got, want)
		}

		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if got, want := payload["worker"], "pdf"; got != want {
			t.Fatalf("payload[worker] = %q, want %q", got, want)
		}

		if calls == 1 {
			http.Error(w, "transient", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	api := newAPIClient(Config{
		APIKey:     "test-key",
		APIBaseURL: server.URL,
		RetryPolicy: RetryPolicy{
			MaxAttempts:    3,
			InitialBackoff: time.Second,
			MaxBackoff:     time.Second,
		},
	})
	var sleeps []time.Duration
	api.sleep = func(ctx context.Context, delay time.Duration) error {
		sleeps = append(sleeps, delay)
		return ctx.Err()
	}

	var response map[string]string
	_, _, err := api.doJSONWithResponse(context.Background(), http.MethodPost, server.URL+"/convert", map[string]string{"worker": "pdf"}, &response, nil, nil)
	if err != nil {
		t.Fatalf("doJSONWithResponse() error = %v", err)
	}
	if got, want := response["status"], "ok"; got != want {
		t.Fatalf("response[status] = %q, want %q", got, want)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if len(sleeps) != 1 || sleeps[0] != time.Second {
		t.Fatalf("sleeps = %v, want [1s]", sleeps)
	}
}

func TestRetryPolicyMaxAttemptsOneDisablesRetries(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "transient", http.StatusInternalServerError)
	}))
	defer server.Close()

	api := newAPIClient(Config{
		APIKey: "test-key",
		RetryPolicy: RetryPolicy{
			MaxAttempts: 1,
		},
	})
	api.sleep = func(context.Context, time.Duration) error {
		t.Fatal("sleep should not be called when retries are disabled")
		return nil
	}

	_, _, err := api.doJSONWithResponse(context.Background(), http.MethodGet, server.URL, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("doJSONWithResponse() error = nil, want error")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestControlPlaneDoesNotRetryNonRetryableHTTPStatus(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()

	api := newAPIClient(Config{APIKey: "test-key"})
	api.sleep = func(context.Context, time.Duration) error {
		t.Fatal("sleep should not be called for non-retryable status")
		return nil
	}

	_, _, err := api.doJSONWithResponse(context.Background(), http.MethodGet, server.URL, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("doJSONWithResponse() error = nil, want error")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestRetryAfterSecondsAndHTTPDate(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "2")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	api := newAPIClient(Config{
		APIKey: "test-key",
		RetryPolicy: RetryPolicy{
			MaxAttempts:    2,
			InitialBackoff: time.Millisecond,
			MaxBackoff:     time.Millisecond,
		},
	})
	var sleeps []time.Duration
	api.sleep = func(ctx context.Context, delay time.Duration) error {
		sleeps = append(sleeps, delay)
		return ctx.Err()
	}

	_, _, err := api.doJSONWithResponse(context.Background(), http.MethodGet, server.URL, nil, nil, map[int]struct{}{http.StatusNoContent: {}}, nil)
	if err != nil {
		t.Fatalf("doJSONWithResponse() error = %v", err)
	}
	if len(sleeps) != 1 || sleeps[0] != 2*time.Second {
		t.Fatalf("sleeps = %v, want [2s]", sleeps)
	}

	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	delay, ok := retryAfterDelay(now.Add(3*time.Second).Format(http.TimeFormat), now)
	if !ok {
		t.Fatal("retryAfterDelay(HTTP-date) ok = false, want true")
	}
	if delay != 3*time.Second {
		t.Fatalf("retryAfterDelay(HTTP-date) = %v, want 3s", delay)
	}
}

func TestRetryStopsWhenContextCanceledBeforeSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "transient", http.StatusInternalServerError)
		cancel()
	}))
	defer server.Close()

	api := newAPIClient(Config{APIKey: "test-key"})
	api.sleep = func(context.Context, time.Duration) error {
		t.Fatal("sleep should not be called after context cancellation")
		return nil
	}

	_, _, err := api.doJSONWithResponse(ctx, http.MethodGet, server.URL, nil, nil, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("doJSONWithResponse() error = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestEnvdFileHTTPRetriesReadsAndWrites(t *testing.T) {
	record := sandboxRecord{
		SandboxID:       "sbx-pdf",
		EnvdVersion:     "0.4.4",
		EnvdAccessToken: "envd-token",
	}

	t.Run("read", func(t *testing.T) {
		calls := 0
		api := newAPIClient(Config{
			RetryPolicy: RetryPolicy{MaxAttempts: 2, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
		})
		api.sleep = func(ctx context.Context, delay time.Duration) error { return ctx.Err() }
		api.envdHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if got, want := req.Header.Get("X-Access-Token"), "envd-token"; got != want {
				t.Fatalf("X-Access-Token = %q, want %q", got, want)
			}
			if got, want := req.URL.Query().Get("path"), "/workspace/in.pdf"; got != want {
				t.Fatalf("path query = %q, want %q", got, want)
			}
			if calls == 1 {
				return testHTTPResponse(http.StatusBadGateway, "bad gateway", nil), nil
			}
			return testHTTPResponse(http.StatusOK, "pdf-bytes", nil), nil
		})

		content, err := api.readFile(context.Background(), record, "/workspace/in.pdf")
		if err != nil {
			t.Fatalf("readFile() error = %v", err)
		}
		if got, want := string(content), "pdf-bytes"; got != want {
			t.Fatalf("readFile() = %q, want %q", got, want)
		}
		if calls != 2 {
			t.Fatalf("calls = %d, want 2", calls)
		}
	})

	t.Run("write", func(t *testing.T) {
		calls := 0
		var bodies []string
		var contentTypes []string

		api := newAPIClient(Config{
			RetryPolicy: RetryPolicy{MaxAttempts: 2, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
		})
		api.sleep = func(ctx context.Context, delay time.Duration) error { return ctx.Err() }
		api.envdHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			bodies = append(bodies, string(body))
			contentTypes = append(contentTypes, req.Header.Get("Content-Type"))

			if calls == 1 {
				return testHTTPResponse(http.StatusTooManyRequests, "rate limited", nil), nil
			}
			return testHTTPResponse(http.StatusNoContent, "", nil), nil
		})

		if err := api.writeFile(context.Background(), record, "/workspace/out.pdf", []byte("converted-pdf")); err != nil {
			t.Fatalf("writeFile() error = %v", err)
		}
		if calls != 2 {
			t.Fatalf("calls = %d, want 2", calls)
		}
		if len(bodies) != 2 || bodies[0] != bodies[1] {
			t.Fatalf("request bodies were not preserved across attempts")
		}
		if !strings.Contains(bodies[0], "converted-pdf") {
			t.Fatalf("request body %q does not contain file content", bodies[0])
		}
		if len(contentTypes) != 2 || contentTypes[0] == "" || contentTypes[0] != contentTypes[1] {
			t.Fatalf("Content-Type headers = %v, want same non-empty value", contentTypes)
		}
	})
}

func TestControlPlaneRetriesTemporaryNetworkErrors(t *testing.T) {
	calls := 0
	api := newAPIClient(Config{
		APIKey:      "test-key",
		RetryPolicy: RetryPolicy{MaxAttempts: 2, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
	})
	api.sleep = func(ctx context.Context, delay time.Duration) error { return ctx.Err() }
	api.controlHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, temporaryNetworkError{}
		}
		return testHTTPResponse(http.StatusOK, `{"status":"ok"}`, map[string]string{"Content-Type": "application/json"}), nil
	})

	var response map[string]string
	_, _, err := api.doJSONWithResponse(context.Background(), http.MethodGet, "https://api.e2b.test/retry", nil, &response, nil, nil)
	if err != nil {
		t.Fatalf("doJSONWithResponse() error = %v", err)
	}
	if got, want := response["status"], "ok"; got != want {
		t.Fatalf("response[status] = %q, want %q", got, want)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type temporaryNetworkError struct{}

func (temporaryNetworkError) Error() string   { return "temporary network error" }
func (temporaryNetworkError) Timeout() bool   { return false }
func (temporaryNetworkError) Temporary() bool { return true }

func testHTTPResponse(status int, body string, headers map[string]string) *http.Response {
	resp := &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	for key, value := range headers {
		resp.Header.Set(key, value)
	}
	return resp
}
