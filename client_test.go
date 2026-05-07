package e2b

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestCreateSandboxRequestUsesSnakeCaseInternetField(t *testing.T) {
	payload, err := json.Marshal(createSandboxRequest{
		TemplateID:          "template",
		Timeout:             300,
		Secure:              true,
		AllowInternetAccess: false,
	})
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}

	if _, ok := decoded["allow_internet_access"]; !ok {
		t.Fatalf("payload missing allow_internet_access field: %s", string(payload))
	}
	if _, ok := decoded["allowInternetAccess"]; ok {
		t.Fatalf("payload unexpectedly contains allowInternetAccess field: %s", string(payload))
	}
}

func TestCreateSandboxRequestEnvVars(t *testing.T) {
	payload, err := json.Marshal(createSandboxRequest{
		TemplateID: "template",
		Timeout:    300,
		Secure:     true,
		EnvVars:    map[string]string{"FOO": "bar", "DB_URL": "postgres://localhost"},
	})
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}

	envVars, ok := decoded["envVars"]
	if !ok {
		t.Fatalf("payload missing envVars field: %s", string(payload))
	}
	envMap, ok := envVars.(map[string]any)
	if !ok {
		t.Fatalf("envVars is not a map: %T", envVars)
	}
	if envMap["FOO"] != "bar" {
		t.Errorf("envVars[FOO] = %v, want bar", envMap["FOO"])
	}
}

func TestCreateSandboxRequestNetwork(t *testing.T) {
	payload, err := json.Marshal(createSandboxRequest{
		TemplateID: "template",
		Timeout:    300,
		Secure:     true,
		Network:    &networkConfig{AllowOut: []string{"10.0.0.0/8", "192.168.0.0/16"}},
	})
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}

	network, ok := decoded["network"]
	if !ok {
		t.Fatalf("payload missing network field: %s", string(payload))
	}
	netMap, ok := network.(map[string]any)
	if !ok {
		t.Fatalf("network is not a map: %T", network)
	}
	allowOut, ok := netMap["allowOut"]
	if !ok {
		t.Fatalf("network missing allowOut field: %s", string(payload))
	}
	arr, ok := allowOut.([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("allowOut = %v, want 2-element array", allowOut)
	}
}

func TestCreateSandboxRequestNoOptionalFields(t *testing.T) {
	payload, err := json.Marshal(createSandboxRequest{
		TemplateID: "template",
		Timeout:    300,
		Secure:     true,
	})
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}

	if _, ok := decoded["envVars"]; ok {
		t.Errorf("payload should not contain envVars when empty: %s", string(payload))
	}
	if _, ok := decoded["network"]; ok {
		t.Errorf("payload should not contain network when nil: %s", string(payload))
	}
	if _, ok := decoded["metadata"]; ok {
		t.Errorf("payload should not contain metadata when nil: %s", string(payload))
	}
}

func TestNormalizeHTTPErrorUsesOperationSpecificNotFoundError(t *testing.T) {
	if err := normalizeHTTPError(404, "missing file", ErrFileNotFound); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("404 error = %v, want ErrFileNotFound", err)
	}
	if err := normalizeHTTPError(404, "missing sandbox", ErrSandboxNotFound); !errors.Is(err, ErrSandboxNotFound) {
		t.Fatalf("404 error = %v, want ErrSandboxNotFound", err)
	}
}

func TestConfigDefaults(t *testing.T) {
	c := Config{}
	if c.apiBaseURL() != defaultAPIBaseURL {
		t.Errorf("empty APIBaseURL should default to %q, got %q", defaultAPIBaseURL, c.apiBaseURL())
	}
	if c.requestTimeout() != defaultRequestTimeout {
		t.Errorf("zero RequestTimeout should default to %v, got %v", defaultRequestTimeout, c.requestTimeout())
	}
	policy := c.retryPolicy()
	if policy.MaxAttempts != defaultRetryMaxAttempts {
		t.Errorf("zero RetryPolicy MaxAttempts should default to %d, got %d", defaultRetryMaxAttempts, policy.MaxAttempts)
	}
	if policy.InitialBackoff != defaultInitialBackoff {
		t.Errorf("zero RetryPolicy InitialBackoff should default to %v, got %v", defaultInitialBackoff, policy.InitialBackoff)
	}
	if policy.MaxBackoff != defaultMaxBackoff {
		t.Errorf("zero RetryPolicy MaxBackoff should default to %v, got %v", defaultMaxBackoff, policy.MaxBackoff)
	}
}

func TestRetryPolicyMaxAttemptsOneMeansNoRetries(t *testing.T) {
	policy := Config{RetryPolicy: RetryPolicy{MaxAttempts: 1}}.retryPolicy()
	if policy.MaxAttempts != 1 {
		t.Fatalf("MaxAttempts = %d, want 1", policy.MaxAttempts)
	}
}

func TestConfigTrimsAPIBaseURL(t *testing.T) {
	c := Config{APIBaseURL: "  https://staging.e2b.app/  "}
	if got, want := c.apiBaseURL(), "https://staging.e2b.app"; got != want {
		t.Errorf("apiBaseURL() = %q, want %q", got, want)
	}
}

func TestDurationToWholeSecondsRoundsUp(t *testing.T) {
	tests := []struct {
		name     string
		input    time.Duration
		expected int
	}{
		{name: "zero", input: 0, expected: 0},
		{name: "sub-second", input: 250 * time.Millisecond, expected: 1},
		{name: "exact second", input: time.Second, expected: 1},
		{name: "fractional second", input: time.Second + 250*time.Millisecond, expected: 2},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := durationToWholeSeconds(test.input); got != test.expected {
				t.Fatalf("durationToWholeSeconds(%v) = %d, want %d", test.input, got, test.expected)
			}
		})
	}
}

func TestNewAPIClientSeparatesControlAndEnvdTimeouts(t *testing.T) {
	client := newAPIClient(Config{RequestTimeout: 42 * time.Second})

	if got, want := client.controlHTTPClient.Timeout, 42*time.Second; got != want {
		t.Fatalf("controlHTTPClient.Timeout = %v, want %v", got, want)
	}
	if got := client.envdHTTPClient.Timeout; got != 0 {
		t.Fatalf("envdHTTPClient.Timeout = %v, want 0", got)
	}
}

func TestLegacySandboxUsernameForFileHTTP(t *testing.T) {
	client := newAPIClient(Config{})

	tests := []struct {
		name           string
		envdVersion    string
		wantLegacyUser bool
		wantUsername   string
	}{
		{name: "legacy envd", envdVersion: "0.3.9", wantLegacyUser: true, wantUsername: "user"},
		{name: "modern envd", envdVersion: "0.4.0", wantLegacyUser: false, wantUsername: ""},
		{name: "modern envd with v prefix", envdVersion: "v0.5.1", wantLegacyUser: false, wantUsername: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := sandboxRecord{EnvdVersion: test.envdVersion}
			values := client.envdFileQuery(record, "/workspace/example.txt")

			if got := usesLegacySandboxUser(test.envdVersion); got != test.wantLegacyUser {
				t.Fatalf("usesLegacySandboxUser(%q) = %v, want %v", test.envdVersion, got, test.wantLegacyUser)
			}
			if got := values.Get("path"); got != "/workspace/example.txt" {
				t.Fatalf("path query = %q, want /workspace/example.txt", got)
			}
			if got := values.Get("username"); got != test.wantUsername {
				t.Fatalf("username query = %q, want %q", got, test.wantUsername)
			}
		})
	}
}

func TestLegacySandboxAuthHeader(t *testing.T) {
	if got, want := legacySandboxAuthHeader("0.3.9"), "Basic dXNlcjo="; got != want {
		t.Fatalf("legacySandboxAuthHeader(legacy) = %q, want %q", got, want)
	}
	if got := legacySandboxAuthHeader("0.4.0"); got != "" {
		t.Fatalf("legacySandboxAuthHeader(modern) = %q, want empty string", got)
	}
}
