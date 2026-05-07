package e2b

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fastRetrier returns a retrier with a no-op sleep so tests run instantly.
func fastRetrier(policy RetryPolicy) *retrier {
	r := newRetrier(policy)
	r.sleepFunc = func(_ context.Context, _ time.Duration) error { return nil }
	return r
}

func TestRetrierSuccessOnFirstAttempt(t *testing.T) {
	r := fastRetrier(RetryPolicy{MaxAttempts: 3})
	calls := 0
	err := r.do(context.Background(), func() (int, string, error) {
		calls++
		return http.StatusOK, "", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("want 1 call, got %d", calls)
	}
}

func TestRetrierSuccessAfterRetry(t *testing.T) {
	r := fastRetrier(RetryPolicy{MaxAttempts: 3})
	calls := 0
	err := r.do(context.Background(), func() (int, string, error) {
		calls++
		if calls < 3 {
			return http.StatusServiceUnavailable, "",
				normalizeHTTPError(http.StatusServiceUnavailable, "busy", nil)
		}
		return http.StatusOK, "", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("want 3 calls, got %d", calls)
	}
}

func TestRetrierDisabledWithMaxAttempts1(t *testing.T) {
	r := fastRetrier(RetryPolicy{MaxAttempts: 1})
	calls := 0
	err := r.do(context.Background(), func() (int, string, error) {
		calls++
		return http.StatusServiceUnavailable, "",
			normalizeHTTPError(http.StatusServiceUnavailable, "busy", nil)
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls != 1 {
		t.Fatalf("want 1 call (no retries), got %d", calls)
	}
}

func TestRetrierNonRetryableStatus(t *testing.T) {
	r := fastRetrier(RetryPolicy{MaxAttempts: 3})
	calls := 0
	err := r.do(context.Background(), func() (int, string, error) {
		calls++
		return http.StatusBadRequest, "",
			normalizeHTTPError(http.StatusBadRequest, "bad input", nil)
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls != 1 {
		t.Fatalf("want 1 call (non-retryable 400), got %d", calls)
	}
}

func TestRetrierRetryAfterSeconds(t *testing.T) {
	var sleptFor time.Duration
	r := newRetrier(RetryPolicy{MaxAttempts: 2})
	r.sleepFunc = func(_ context.Context, d time.Duration) error {
		sleptFor = d
		return nil
	}
	calls := 0
	err := r.do(context.Background(), func() (int, string, error) {
		calls++
		if calls == 1 {
			return http.StatusTooManyRequests, "3",
				normalizeHTTPError(http.StatusTooManyRequests, "rate limited", nil)
		}
		return http.StatusOK, "", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := 3 * time.Second; sleptFor != want {
		t.Fatalf("sleep = %v, want %v", sleptFor, want)
	}
}

func TestRetrierRetryAfterHTTPDate(t *testing.T) {
	var sleptFor time.Duration
	r := newRetrier(RetryPolicy{MaxAttempts: 2})
	r.sleepFunc = func(_ context.Context, d time.Duration) error {
		sleptFor = d
		return nil
	}
	// http.TimeFormat has second precision; parsed time will be within ~1s of now+3s.
	futureDate := time.Now().UTC().Add(3 * time.Second).Format(http.TimeFormat)
	calls := 0
	err := r.do(context.Background(), func() (int, string, error) {
		calls++
		if calls == 1 {
			return http.StatusTooManyRequests, futureDate,
				normalizeHTTPError(http.StatusTooManyRequests, "rate limited", nil)
		}
		return http.StatusOK, "", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sleptFor < 1*time.Second {
		t.Fatalf("sleep = %v, expected at least 1s (Retry-After: ~3s ahead)", sleptFor)
	}
}

func TestRetrierContextCancellationDuringSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := newRetrier(RetryPolicy{MaxAttempts: 3})
	r.sleepFunc = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}
	calls := 0
	err := r.do(ctx, func() (int, string, error) {
		calls++
		return http.StatusServiceUnavailable, "",
			normalizeHTTPError(http.StatusServiceUnavailable, "busy", nil)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got error %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("want 1 call before cancellation, got %d", calls)
	}
}

func TestRetrierContextAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := fastRetrier(RetryPolicy{MaxAttempts: 3})
	calls := 0
	err := r.do(ctx, func() (int, string, error) {
		calls++
		return http.StatusOK, "", nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got error %v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("want 0 calls for pre-cancelled context, got %d", calls)
	}
}

// ---- Envd file HTTP retry tests ----

// mockTransport records call counts and delegates to a handler without any
// real network activity, making envd URL scheme irrelevant.
type mockTransport struct {
	mu      sync.Mutex
	calls   int
	handler func(callN int, w http.ResponseWriter, r *http.Request)
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	m.mu.Lock()
	m.calls++
	n := m.calls
	m.mu.Unlock()
	rec := httptest.NewRecorder()
	m.handler(n, rec, req)
	return rec.Result(), nil
}

func newTestAPIClientWithTransport(t *testing.T, transport http.RoundTripper, policy RetryPolicy) *apiClient {
	t.Helper()
	c := newAPIClient(Config{RetryPolicy: policy})
	c.retrier.sleepFunc = func(_ context.Context, _ time.Duration) error { return nil }
	c.envdHTTPClient.Transport = transport
	c.controlHTTPClient.Transport = transport
	return c
}

func testSandboxRecord() sandboxRecord {
	return sandboxRecord{
		SandboxID:       "test-sb",
		EnvdVersion:     "0.5.0",
		EnvdAccessToken: "tok",
	}
}

func TestReadFileRetryOnTransientStatus(t *testing.T) {
	transport := &mockTransport{
		handler: func(n int, w http.ResponseWriter, _ *http.Request) {
			if n == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("hello"))
		},
	}
	c := newTestAPIClientWithTransport(t, transport, RetryPolicy{MaxAttempts: 3})

	data, err := c.readFile(context.Background(), testSandboxRecord(), "/test.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("got %q, want %q", string(data), "hello")
	}
	if transport.calls != 2 {
		t.Fatalf("want 2 HTTP calls, got %d", transport.calls)
	}
}

func TestReadFileNoRetryOnNonTransientStatus(t *testing.T) {
	transport := &mockTransport{
		handler: func(_ int, w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		},
	}
	c := newTestAPIClientWithTransport(t, transport, RetryPolicy{MaxAttempts: 3})

	_, err := c.readFile(context.Background(), testSandboxRecord(), "/missing.txt")
	if !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("got error %v, want ErrFileNotFound", err)
	}
	if transport.calls != 1 {
		t.Fatalf("want 1 HTTP call (404 not retried), got %d", transport.calls)
	}
}

func TestWriteFileRetryOnTransientStatus(t *testing.T) {
	transport := &mockTransport{
		handler: func(n int, w http.ResponseWriter, _ *http.Request) {
			if n == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.WriteHeader(http.StatusOK)
		},
	}
	c := newTestAPIClientWithTransport(t, transport, RetryPolicy{MaxAttempts: 3})

	err := c.writeFile(context.Background(), testSandboxRecord(), "/out.txt", []byte("data"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if transport.calls != 2 {
		t.Fatalf("want 2 HTTP calls, got %d", transport.calls)
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name   string
		header string
		wantGT time.Duration // result must be greater than this
		wantEQ time.Duration // if nonzero, result must equal this exactly
		wantZ  bool          // result must be zero
	}{
		{name: "empty", header: "", wantZ: true},
		{name: "zero seconds", header: "0", wantZ: true},
		{name: "three seconds", header: "3", wantEQ: 3 * time.Second},
		{name: "fractional", header: "1.5", wantEQ: 1500 * time.Millisecond},
		{name: "invalid", header: "invalid", wantZ: true},
		{name: "future date", header: time.Now().UTC().Add(5 * time.Second).Format(http.TimeFormat), wantGT: 0},
		{name: "past date", header: time.Now().UTC().Add(-5 * time.Second).Format(http.TimeFormat), wantZ: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRetryAfter(tt.header)
			if tt.wantZ && got != 0 {
				t.Fatalf("parseRetryAfter(%q) = %v, want 0", tt.header, got)
			}
			if tt.wantEQ != 0 && got != tt.wantEQ {
				t.Fatalf("parseRetryAfter(%q) = %v, want %v", tt.header, got, tt.wantEQ)
			}
			if !tt.wantZ && tt.wantEQ == 0 && got <= tt.wantGT {
				t.Fatalf("parseRetryAfter(%q) = %v, want > %v", tt.header, got, tt.wantGT)
			}
		})
	}
}

func TestRetryPolicyDefaults(t *testing.T) {
	p := RetryPolicy{}
	if got, want := p.maxAttempts(), defaultRetryMaxAttempts; got != want {
		t.Fatalf("maxAttempts() = %d, want %d", got, want)
	}
	if got, want := p.initialBackoff(), defaultRetryInitialBackoff; got != want {
		t.Fatalf("initialBackoff() = %v, want %v", got, want)
	}
	if got, want := p.maxBackoff(), defaultRetryMaxBackoff; got != want {
		t.Fatalf("maxBackoff() = %v, want %v", got, want)
	}
}

func TestRetryPolicyMaxAttempts1DisablesRetries(t *testing.T) {
	p := RetryPolicy{MaxAttempts: 1}
	if got := p.maxAttempts(); got != 1 {
		t.Fatalf("maxAttempts() = %d, want 1", got)
	}
}
