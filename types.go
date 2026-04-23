package e2b

import "time"

// CreateRequest configures a new sandbox. TemplateID is required; all other
// fields are optional.
type CreateRequest struct {
	// TemplateID is the E2B template the sandbox is cloned from. Required.
	TemplateID string

	// Timeout bounds the lifetime of the sandbox. If zero, the E2B server's
	// default applies.
	Timeout time.Duration

	// Metadata is attached to the sandbox on creation and is visible on
	// the control plane. Commonly used for user / project / trace IDs.
	Metadata map[string]string

	// EnvVars are injected into every process started in the sandbox.
	EnvVars map[string]string

	// AllowInternetAccess controls whether the sandbox can reach the public
	// internet. Default is false (network-isolated).
	AllowInternetAccess bool

	// NetworkAllowlist is an egress allowlist of CIDRs / hostnames. Only
	// honored when AllowInternetAccess is false — the listed destinations
	// remain reachable.
	NetworkAllowlist []string

	// AdditionalPackages are installed via apt-get at sandbox start. Useful
	// for one-off extensions of a base template without rebuilding it.
	AdditionalPackages []string

	// AllowShellFallback enables shell-based fallbacks (cat, find) when
	// envd ConnectRPC calls fail. Useful for older envd versions or envd
	// bugs; default false.
	AllowShellFallback bool
}

// ExecRequest describes a command to run inside a sandbox.
type ExecRequest struct {
	// Command is an argv slice. Command[0] is the executable; the rest are
	// positional arguments.
	Command []string

	// WorkingDirectory sets the cwd. Empty means the sandbox default.
	WorkingDirectory string

	// Environment merges with the sandbox-level EnvVars for this call.
	Environment map[string]string

	// Timeout bounds the call. Zero means no deadline beyond the parent ctx.
	Timeout time.Duration
}

// ExecResult is returned by Sandbox.Exec after the process has finished.
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	// Metadata carries envd-provided diagnostics (e.g. an "error" key when
	// the process itself reported a launch failure).
	Metadata map[string]string
}

// FileInfo describes a file returned by Sandbox.ListFiles.
type FileInfo struct {
	Path string
	Size int64
}
