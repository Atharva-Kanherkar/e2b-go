package e2b

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestControlPlaneRetriesSuccessAfterTransientStatus(t *testing.T) {
	var calls int
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got, want := r.Header.Get("X-API-KEY"), "test-key"; got != want {
			t.Fatalf("X-API-KEY = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("Content-Type"), "application/json"; got != want {
			t.Fatalf("Content-Type = %q, want %q", got, want)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll body: %v", err)
		}
		bodies = append(bodies, string(body))

		if calls == 1 {
			http.Error(w, "try again", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sandboxID":"sbx-123","templateID":"base","envdVersion":"0.4.4","envdAccessToken":"envd-token"}`))
	}))
	defer server.Close()

	client := newAPIClient(Config{
		APIKey:     "test-key",
		APIBaseURL: server.URL,
		RetryPolicy: RetryPolicy{
			MaxAttempts:    2,
			InitialBackoff: time.Nanosecond,
			MaxBackoff:     time.Nanosecond,
		},
	})
	client.retrySleep = noRetrySleep

	record, err := client.createSandbox(context.Background(), createSandboxRequest{TemplateID: "base", Secure: true})
	if err != nil {
		t.Fatalf("createSandbox() error = %v", err)
	}
	if got, want := record.SandboxID, "sbx-123"; got != want {
		t.Fatalf("SandboxID = %q, want %q", got, want)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if len(bodies) != 2 || bodies[0] == "" || bodies[0] != bodies[1] {
		t.Fatalf("request bodies were not preserved across attempts: %#v", bodies)
	}
}

func TestRetryPolicyMaxAttemptsOneDisablesRetries(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := newAPIClient(Config{
		APIKey:      "test-key",
		APIBaseURL:  server.URL,
		RetryPolicy: RetryPolicy{MaxAttempts: 1},
	})
	client.retrySleep = func(context.Context, time.Duration) error {
		t.Fatal("retrySleep should not be called when MaxAttempts is 1")
		return nil
	}

	err := client.destroySandbox(context.Background(), "sbx-123")
	if err == nil {
		t.Fatal("destroySandbox() error = nil, want 503")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestControlPlaneDoesNotRetryNonRetryableStatus(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()

	client := newAPIClient(Config{
		APIKey:     "test-key",
		APIBaseURL: server.URL,
		RetryPolicy: RetryPolicy{
			MaxAttempts:    3,
			InitialBackoff: time.Nanosecond,
			MaxBackoff:     time.Nanosecond,
		},
	})
	client.retrySleep = func(context.Context, time.Duration) error {
		t.Fatal("retrySleep should not be called for HTTP 400")
		return nil
	}

	err := client.getSandboxInfo(context.Background(), "sbx-123")
	if err == nil {
		t.Fatal("getSandboxInfo() error = nil, want 400")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestRetryAfterHeaderControlsBackoff(t *testing.T) {
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		header     string
		wantDelay  time.Duration
		statusCode int
	}{
		{name: "seconds", header: "2", wantDelay: 2 * time.Second, statusCode: http.StatusTooManyRequests},
		{name: "http date", header: now.Add(3 * time.Second).Format(http.TimeFormat), wantDelay: 3 * time.Second, statusCode: http.StatusServiceUnavailable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				if calls == 1 {
					w.Header().Set("Retry-After", test.header)
					http.Error(w, "wait", test.statusCode)
					return
				}
				_, _ = w.Write([]byte(`[]`))
			}))
			defer server.Close()

			var delays []time.Duration
			client := newAPIClient(Config{
				APIKey:     "test-key",
				APIBaseURL: server.URL,
				RetryPolicy: RetryPolicy{
					MaxAttempts:    2,
					InitialBackoff: time.Nanosecond,
					MaxBackoff:     time.Nanosecond,
				},
			})
			client.retryNow = func() time.Time { return now }
			client.retrySleep = func(ctx context.Context, delay time.Duration) error {
				delays = append(delays, delay)
				return ctx.Err()
			}

			if _, err := client.listSandboxes(context.Background(), ListSandboxesRequest{}); err != nil {
				t.Fatalf("listSandboxes() error = %v", err)
			}
			if calls != 2 {
				t.Fatalf("calls = %d, want 2", calls)
			}
			if len(delays) != 1 || delays[0] != test.wantDelay {
				t.Fatalf("delays = %v, want [%v]", delays, test.wantDelay)
			}
		})
	}
}

func TestRetryRespectsContextCancellationBeforeNextAttempt(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	client := newAPIClient(Config{
		APIKey:     "test-key",
		APIBaseURL: server.URL,
		RetryPolicy: RetryPolicy{
			MaxAttempts:    3,
			InitialBackoff: time.Second,
			MaxBackoff:     time.Second,
		},
	})
	client.retrySleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}

	err := client.destroySandbox(ctx, "sbx-123")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("destroySandbox() error = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestEnvdFileHTTPRetriesReadsAndWrites(t *testing.T) {
	transport := &retryFileTransport{t: t}
	client := newAPIClient(Config{
		RetryPolicy: RetryPolicy{
			MaxAttempts:    2,
			InitialBackoff: time.Nanosecond,
			MaxBackoff:     time.Nanosecond,
		},
	})
	client.envdHTTPClient.Transport = transport
	client.retrySleep = noRetrySleep

	record := sandboxRecord{
		SandboxID:       "sbx-files",
		EnvdVersion:     "0.4.4",
		EnvdAccessToken: "envd-token",
	}

	content, err := client.readFile(context.Background(), record, "/workspace/input.pdf")
	if err != nil {
		t.Fatalf("readFile() error = %v", err)
	}
	if string(content) != "pdf-bytes" {
		t.Fatalf("readFile() = %q, want pdf-bytes", string(content))
	}

	if err := client.writeFile(context.Background(), record, "/workspace/output.pdf", []byte("updated-pdf")); err != nil {
		t.Fatalf("writeFile() error = %v", err)
	}

	if transport.getCalls != 2 {
		t.Fatalf("GET calls = %d, want 2", transport.getCalls)
	}
	if transport.postCalls != 2 {
		t.Fatalf("POST calls = %d, want 2", transport.postCalls)
	}
	if len(transport.postBodies) != 2 || !bytes.Equal(transport.postBodies[0], transport.postBodies[1]) {
		t.Fatalf("POST bodies were not preserved across attempts")
	}
	if len(transport.postContentTypes) != 2 || transport.postContentTypes[0] == "" || transport.postContentTypes[0] != transport.postContentTypes[1] {
		t.Fatalf("POST Content-Type was not preserved across attempts: %v", transport.postContentTypes)
	}
}

func TestTemporaryNetworkErrorsRetryForSafeMethods(t *testing.T) {
	transport := &temporaryNetworkTransport{}
	client := newAPIClient(Config{
		RetryPolicy: RetryPolicy{
			MaxAttempts:    2,
			InitialBackoff: time.Nanosecond,
			MaxBackoff:     time.Nanosecond,
		},
	})
	client.controlHTTPClient.Transport = transport
	client.retrySleep = noRetrySleep

	var records []sandboxInfoRecord
	status, _, err := client.doJSONWithResponse(context.Background(), http.MethodGet, "https://api.example.test/v2/sandboxes", nil, &records, nil, nil)
	if err != nil {
		t.Fatalf("doJSONWithResponse() error = %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if transport.calls != 2 {
		t.Fatalf("calls = %d, want 2", transport.calls)
	}
}

func noRetrySleep(ctx context.Context, _ time.Duration) error {
	return ctx.Err()
}

type retryFileTransport struct {
	t *testing.T

	mu               sync.Mutex
	getCalls         int
	postCalls        int
	postBodies       [][]byte
	postContentTypes []string
}

func (t *retryFileTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.t.Helper()

	if got, want := req.Header.Get("X-Access-Token"), "envd-token"; got != want {
		t.t.Fatalf("X-Access-Token = %q, want %q", got, want)
	}
	if got, want := req.Header.Get("E2b-Sandbox-Id"), "sbx-files"; got != want {
		t.t.Fatalf("E2b-Sandbox-Id = %q, want %q", got, want)
	}
	if got, want := req.Header.Get("E2b-Sandbox-Port"), "49983"; got != want {
		t.t.Fatalf("E2b-Sandbox-Port = %q, want %q", got, want)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	switch req.Method {
	case http.MethodGet:
		t.getCalls++
		if got, want := req.URL.Query().Get("path"), "/workspace/input.pdf"; got != want {
			t.t.Fatalf("GET path query = %q, want %q", got, want)
		}
		if t.getCalls == 1 {
			return retryTestResponse(http.StatusTooManyRequests, "slow down", nil), nil
		}
		return retryTestResponse(http.StatusOK, "pdf-bytes", nil), nil
	case http.MethodPost:
		t.postCalls++
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.t.Fatalf("ReadAll POST body: %v", err)
		}
		if !strings.Contains(string(body), "updated-pdf") {
			t.t.Fatalf("POST body does not contain file content: %q", string(body))
		}
		t.postBodies = append(t.postBodies, body)
		t.postContentTypes = append(t.postContentTypes, req.Header.Get("Content-Type"))
		if got, want := req.URL.Query().Get("path"), "/workspace/output.pdf"; got != want {
			t.t.Fatalf("POST path query = %q, want %q", got, want)
		}
		if t.postCalls == 1 {
			return retryTestResponse(http.StatusInternalServerError, "try again", nil), nil
		}
		return retryTestResponse(http.StatusNoContent, "", nil), nil
	default:
		t.t.Fatalf("method = %s, want GET or POST", req.Method)
		return nil, nil
	}
}

type temporaryNetworkTransport struct {
	calls int
}

func (t *temporaryNetworkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls++
	if t.calls == 1 {
		return nil, temporaryError("temporary network failure")
	}
	return retryTestResponse(http.StatusOK, "[]", nil), nil
}

type temporaryError string

func (e temporaryError) Error() string   { return string(e) }
func (e temporaryError) Timeout() bool   { return false }
func (e temporaryError) Temporary() bool { return true }

func retryTestResponse(status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
