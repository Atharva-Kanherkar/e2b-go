package e2b

import (
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPIBaseURL        = "https://api.e2b.app"
	defaultRequestTimeout    = 30 * time.Second
	defaultRetryMaxAttempts  = 3
	defaultRetryInitialDelay = 200 * time.Millisecond
	defaultRetryMaxDelay     = 2 * time.Second
	defaultRetryMultiplier   = 2
	defaultConnectTimeout    = 5 * time.Minute
	defaultDomain            = "e2b.app"
	defaultEnvdPort          = 49983
	defaultLegacySandboxUser = "user"
	envdDefaultUserVersion   = "0.4.0"
)

// Config carries connection settings for a Client. Zero values fall back to
// sensible defaults (production API, 30s HTTP timeout).
type Config struct {
	// APIKey authenticates all control-plane calls. Required.
	APIKey string
	// APIBaseURL overrides the control-plane host. Defaults to
	// https://api.e2b.app when empty.
	APIBaseURL string
	// RequestTimeout bounds every HTTP call to the control plane.
	// Defaults to 30s when zero.
	RequestTimeout time.Duration
	// RetryPolicy controls retries for transient HTTP failures. Nil uses
	// conservative defaults. Set MaxAttempts to 1 or use NoRetries to disable.
	RetryPolicy *RetryPolicy
}

// RetryPolicy controls retries for transient HTTP failures. MaxAttempts is the
// total number of tries, including the first request.
type RetryPolicy struct {
	MaxAttempts  int
	InitialDelay time.Duration
	MaxDelay     time.Duration
	Multiplier   float64
}

// NoRetries returns a policy that disables retries.
func NoRetries() *RetryPolicy {
	return &RetryPolicy{MaxAttempts: 1}
}

func (c Config) apiBaseURL() string {
	if strings.TrimSpace(c.APIBaseURL) == "" {
		return defaultAPIBaseURL
	}
	return strings.TrimRight(strings.TrimSpace(c.APIBaseURL), "/")
}

func (c Config) requestTimeout() time.Duration {
	if c.RequestTimeout <= 0 {
		return defaultRequestTimeout
	}
	return c.RequestTimeout
}

func (c Config) retryPolicy() RetryPolicy {
	policy := RetryPolicy{
		MaxAttempts:  defaultRetryMaxAttempts,
		InitialDelay: defaultRetryInitialDelay,
		MaxDelay:     defaultRetryMaxDelay,
		Multiplier:   defaultRetryMultiplier,
	}
	if c.RetryPolicy == nil {
		return policy
	}
	if c.RetryPolicy.MaxAttempts > 0 {
		policy.MaxAttempts = c.RetryPolicy.MaxAttempts
	}
	if c.RetryPolicy.InitialDelay > 0 {
		policy.InitialDelay = c.RetryPolicy.InitialDelay
	}
	if c.RetryPolicy.MaxDelay > 0 {
		policy.MaxDelay = c.RetryPolicy.MaxDelay
	}
	if c.RetryPolicy.Multiplier > 0 {
		policy.Multiplier = c.RetryPolicy.Multiplier
	}
	return policy
}

func durationToWholeSeconds(value time.Duration) int {
	if value <= 0 {
		return 0
	}
	return int((value + time.Second - 1) / time.Second)
}

func connectTimeoutSeconds(value time.Duration) int {
	if value <= 0 {
		value = defaultConnectTimeout
	}
	return durationToWholeSeconds(value)
}

func usesLegacySandboxUser(envdVersion string) bool {
	return compareVersionTriples(envdVersion, envdDefaultUserVersion) < 0
}

func compareVersionTriples(left string, right string) int {
	leftParts, leftOK := parseVersionTriple(left)
	rightParts, rightOK := parseVersionTriple(right)
	if !leftOK || !rightOK {
		return -1
	}

	for i := 0; i < len(leftParts); i++ {
		switch {
		case leftParts[i] < rightParts[i]:
			return -1
		case leftParts[i] > rightParts[i]:
			return 1
		}
	}
	return 0
}

func parseVersionTriple(raw string) ([3]int, bool) {
	var values [3]int

	version := strings.TrimSpace(raw)
	version = strings.TrimPrefix(version, "v")
	if version == "" {
		return values, false
	}

	if idx := strings.IndexAny(version, "-+"); idx >= 0 {
		version = version[:idx]
	}

	parts := strings.Split(version, ".")
	if len(parts) == 0 {
		return values, false
	}

	for i := 0; i < len(values) && i < len(parts); i++ {
		number, err := strconv.Atoi(parts[i])
		if err != nil {
			return values, false
		}
		values[i] = number
	}

	return values, true
}
