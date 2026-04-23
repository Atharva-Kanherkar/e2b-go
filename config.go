package e2b

import (
	"strings"
	"time"
)

const (
	defaultAPIBaseURL     = "https://api.e2b.app"
	defaultRequestTimeout = 30 * time.Second
	defaultDomain         = "e2b.app"
	defaultEnvdPort       = 49983
	defaultSandboxUser    = "root"
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

func durationToWholeSeconds(value time.Duration) int {
	if value <= 0 {
		return 0
	}
	return int((value + time.Second - 1) / time.Second)
}
