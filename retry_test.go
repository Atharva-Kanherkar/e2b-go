package e2b

import (
	"bytes"
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

// noopSleep is injected into apiClient in tests to make retries instant.
func noopSleep(_ context.Context, _ time.Duration) error { return nil }

// recordingSleep records every sleep duration and immediately returns.
type recordingSleep struct {
	durations []time.Duration
}

func (s *recordingSleep) sleep(_ context.Context, d time.Duration) error {
	s.durations = append(s.durations, d)
	return nil
}

// roundTripFunc adapts a plain function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// stubResponse builds a minimal *http.Response suitable for use in
// roundTripFunc-based tests.
func stubResponse(statusCode int, header http.Header, body string) *http.Response {
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{
		StatusCode: statusCode,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// newTestClient returns an apiClient pointed at server with noopSleep and the
// given RetryPolicy.
func newTestClient(server *httptest.Server, policy RetryPolicy) *apiClient {
	c := newAPIClient(Config{
		APIKey:      "test-key",
		APIBaseURL:  server.URL,
		RetryPolicy: policy,
	})
	c.sleep = noopSleep
	return c
}

// ---------------------------------------------------------------------------
// doJSONWithResponse retry tests
// ---------------------------------------------------------------------------

func TestRetrySuccessAfterTransientFailures(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"id": "sb-1"})
	}))
	defer server.Close()

	client := newTestClient(server, RetryPolicy{MaxAttempts: 3})
	var resp map[string]string
	_, _, err := client.doJSONWithResponse(context.Background(), http.MethodGet, server.URL+"/test", nil, &resp, nil, nil)
	if err != nil {
		t.Fatalf("expected success on third attempt, got: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if resp["id"] != "sb-1" {
		t.Fatalf("response body not decoded: %v", resp)
	}
}

func TestRetryDisabledWithMaxAttemptsOne(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := newTestClient(server, RetryPolicy{MaxAttempts: 1})
	_, _, err := client.doJSONWithResponse(context.Background(), http.MethodGet, server.URL+"/test", nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want exactly 1 (no retries)", attempts)
	}
}

func TestRetryNotTriggeredForNonRetryableStatus(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest) // 400 — not retryable
	}))
	defer server.Close()

	client := newTestClient(server, RetryPolicy{MaxAttempts: 3})
	_, _, err := client.doJSONWithResponse(context.Background(), http.MethodGet, server.URL+"/test", nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (400 is not retryable)", attempts)
	}
}

func TestRetryHonorsRetryAfterSeconds(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	rec := &recordingSleep{}
	client := newAPIClient(Config{
		APIKey:      "test-key",
		APIBaseURL:  server.URL,
		RetryPolicy: RetryPolicy{MaxAttempts: 3, InitialBackoff: 100 * time.Millisecond, MaxBackoff: 60 * time.Second},
	})
	client.sleep = rec.sleep

	_, _, err := client.doJSONWithResponse(context.Background(), http.MethodGet, server.URL+"/test", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.durations) != 1 {
		t.Fatalf("sleep called %d times, want 1", len(rec.durations))
	}
	if rec.durations[0] != 7*time.Second {
		t.Fatalf("sleep duration = %v, want 7s (from Retry-After)", rec.durations[0])
	}
}

func TestRetryHonorsRetryAfterHTTPDate(t *testing.T) {
	const retryDelaySecs = 5
	retryAt := time.Now().Add(retryDelaySecs * time.Second)
	retryAfterHeader := retryAt.UTC().Format(http.TimeFormat)

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", retryAfterHeader)
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	rec := &recordingSleep{}
	client := newAPIClient(Config{
		APIKey:      "test-key",
		APIBaseURL:  server.URL,
		RetryPolicy: RetryPolicy{MaxAttempts: 3, InitialBackoff: 100 * time.Millisecond, MaxBackoff: 60 * time.Second},
	})
	client.sleep = rec.sleep

	_, _, err := client.doJSONWithResponse(context.Background(), http.MethodGet, server.URL+"/test", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.durations) != 1 {
		t.Fatalf("sleep called %d times, want 1", len(rec.durations))
	}
	// Allow ±1 s tolerance for the HTTP-date computation.
	const tolerance = time.Second
	expected := time.Duration(retryDelaySecs) * time.Second
	if rec.durations[0] < expected-tolerance || rec.durations[0] > expected+tolerance {
		t.Fatalf("sleep duration = %v, want ~%v (from Retry-After date)", rec.durations[0], expected)
	}
}

func TestRetryContextCancelledDuringSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := newAPIClient(Config{
		APIKey:      "test-key",
		APIBaseURL:  server.URL,
		RetryPolicy: RetryPolicy{MaxAttempts: 5, InitialBackoff: time.Millisecond},
	})
	client.sleep = func(c context.Context, _ time.Duration) error {
		cancel()
		return c.Err()
	}

	_, _, err := client.doJSONWithResponse(ctx, http.MethodGet, server.URL+"/test", nil, nil, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (context cancelled during first sleep)", attempts)
	}
}

func TestRetryPreservesRequestBodyAcrossAttempts(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	want := payload{Name: "pdfflow"}

	attempts := 0
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if attempts < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}))
	defer server.Close()

	client := newTestClient(server, RetryPolicy{MaxAttempts: 3})
	var got payload
	_, _, err := client.doJSONWithResponse(context.Background(), http.MethodPost, server.URL+"/test", want, &got, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, b := range bodies {
		var decoded payload
		if err := json.Unmarshal([]byte(b), &decoded); err != nil || decoded.Name != want.Name {
			t.Fatalf("attempt %d body = %q, want JSON with name=%q", i+1, b, want.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// Envd file (readFile / writeFile) retry tests
// ---------------------------------------------------------------------------

// makeEnvdClient returns an apiClient whose envdHTTPClient uses the provided
// RoundTripper.  The control client is wired to a dummy server (never hit).
func makeEnvdClient(rt http.RoundTripper, policy RetryPolicy) *apiClient {
	c := newAPIClient(Config{RetryPolicy: policy})
	c.envdHTTPClient = &http.Client{Transport: rt}
	c.sleep = noopSleep
	return c
}

func testRecord() sandboxRecord {
	return sandboxRecord{
		SandboxID:       "sb-test",
		TemplateID:      "base",
		EnvdVersion:     "0.4.0",
		EnvdAccessToken: "tok",
	}
}

func TestEnvdReadFileRetryOnTransientStatus(t *testing.T) {
	calls := 0
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls < 2 {
			return stubResponse(http.StatusServiceUnavailable, nil, "overload"), nil
		}
		return stubResponse(http.StatusOK, nil, "hello world"), nil
	})

	client := makeEnvdClient(rt, RetryPolicy{MaxAttempts: 3})
	data, err := client.readFile(context.Background(), testRecord(), "/workspace/file.txt")
	if err != nil {
		t.Fatalf("readFile returned error: %v", err)
	}
	if !bytes.Equal(data, []byte("hello world")) {
		t.Fatalf("readFile data = %q, want %q", data, "hello world")
	}
	if calls != 2 {
		t.Fatalf("transport calls = %d, want 2", calls)
	}
}

func TestEnvdWriteFileRetryOnTransientStatus(t *testing.T) {
	calls := 0
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls < 2 {
			return stubResponse(http.StatusTooManyRequests, nil, ""), nil
		}
		return stubResponse(http.StatusOK, nil, ""), nil
	})

	client := makeEnvdClient(rt, RetryPolicy{MaxAttempts: 3})
	err := client.writeFile(context.Background(), testRecord(), "/workspace/out.txt", []byte("pdf content"))
	if err != nil {
		t.Fatalf("writeFile returned error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("transport calls = %d, want 2", calls)
	}
}

func TestEnvdReadFileNoRetryOnClientError(t *testing.T) {
	calls := 0
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return stubResponse(http.StatusNotFound, nil, "not found"), nil
	})

	client := makeEnvdClient(rt, RetryPolicy{MaxAttempts: 3})
	_, err := client.readFile(context.Background(), testRecord(), "/workspace/missing.txt")
	if !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("expected ErrFileNotFound, got: %v", err)
	}
	if calls != 1 {
		t.Fatalf("transport calls = %d, want 1 (404 is not retryable)", calls)
	}
}

// ---------------------------------------------------------------------------
// RetryPolicy zero-value defaults
// ---------------------------------------------------------------------------

func TestRetryPolicyZeroValueDefaults(t *testing.T) {
	p := RetryPolicy{}
	if p.maxAttempts() != defaultRetryMaxAttempts {
		t.Errorf("maxAttempts() = %d, want %d", p.maxAttempts(), defaultRetryMaxAttempts)
	}
	if p.initialBackoff() != defaultRetryInitialBackoff {
		t.Errorf("initialBackoff() = %v, want %v", p.initialBackoff(), defaultRetryInitialBackoff)
	}
	if p.maxBackoff() != defaultRetryMaxBackoff {
		t.Errorf("maxBackoff() = %v, want %v", p.maxBackoff(), defaultRetryMaxBackoff)
	}
}

func TestRetryPolicyMaxAttemptsOneDisablesRetry(t *testing.T) {
	if got := (RetryPolicy{MaxAttempts: 1}).maxAttempts(); got != 1 {
		t.Errorf("maxAttempts() = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// parseRetryAfter unit tests
// ---------------------------------------------------------------------------

func TestParseRetryAfterSeconds(t *testing.T) {
	h := http.Header{"Retry-After": []string{"42"}}
	if d := parseRetryAfter(h); d != 42*time.Second {
		t.Fatalf("parseRetryAfter = %v, want 42s", d)
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	future := time.Now().Add(10 * time.Second)
	h := http.Header{"Retry-After": []string{future.UTC().Format(http.TimeFormat)}}
	d := parseRetryAfter(h)
	if d < 9*time.Second || d > 11*time.Second {
		t.Fatalf("parseRetryAfter = %v, want ~10s", d)
	}
}

func TestParseRetryAfterAbsent(t *testing.T) {
	if d := parseRetryAfter(nil); d != 0 {
		t.Fatalf("parseRetryAfter(nil) = %v, want 0", d)
	}
	if d := parseRetryAfter(make(http.Header)); d != 0 {
		t.Fatalf("parseRetryAfter(empty) = %v, want 0", d)
	}
}

// ---------------------------------------------------------------------------
// isRetryableStatus unit tests
// ---------------------------------------------------------------------------

func TestIsRetryableStatus(t *testing.T) {
	retryable := []int{408, 409, 425, 429, 500, 502, 503, 504}
	for _, code := range retryable {
		if !isRetryableStatus(code) {
			t.Errorf("isRetryableStatus(%d) = false, want true", code)
		}
	}
	nonRetryable := []int{200, 201, 204, 301, 400, 401, 403, 404, 422}
	for _, code := range nonRetryable {
		if isRetryableStatus(code) {
			t.Errorf("isRetryableStatus(%d) = true, want false", code)
		}
	}
}
