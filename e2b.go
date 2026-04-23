package e2b

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/e2b-dev/infra/packages/shared/pkg/grpc/envd/filesystem/filesystemconnect"
	"github.com/e2b-dev/infra/packages/shared/pkg/grpc/envd/process/processconnect"
)

// Client is an authenticated handle to the E2B control plane. Safe for
// concurrent use.
type Client struct {
	api    *apiClient
	config Config
}

// NewClient constructs a Client with just an API key. For more control
// (custom timeout, staging host), use NewClientWithConfig.
func NewClient(apiKey string) *Client {
	return NewClientWithConfig(Config{APIKey: apiKey})
}

// NewClientWithConfig constructs a Client from a fully-specified Config.
func NewClientWithConfig(config Config) *Client {
	return &Client{
		api:    newAPIClient(config),
		config: config,
	}
}

// CreateSandbox provisions a new sandbox from the given template and
// returns a handle for interacting with it. Callers are responsible for
// calling Destroy to release the sandbox.
//
// AdditionalPackages, when non-empty, are installed via apt-get inside
// the sandbox before the call returns. Install failures cause the
// sandbox to be destroyed and the error propagated.
func (c *Client) CreateSandbox(ctx context.Context, request CreateRequest) (*Sandbox, error) {
	if strings.TrimSpace(request.TemplateID) == "" {
		return nil, fmt.Errorf("e2b: CreateRequest.TemplateID is required")
	}

	var network *networkConfig
	if len(request.NetworkAllowlist) > 0 {
		network = &networkConfig{AllowOut: request.NetworkAllowlist}
	}

	record, err := c.api.createSandbox(ctx, createSandboxRequest{
		TemplateID:          request.TemplateID,
		Timeout:             int(request.Timeout.Round(time.Second) / time.Second),
		Metadata:            request.Metadata,
		Secure:              true,
		AllowInternetAccess: request.AllowInternetAccess,
		EnvVars:             request.EnvVars,
		Network:             network,
	})
	if err != nil {
		return nil, err
	}

	sb := &Sandbox{
		client: sandboxTransport{
			api:           c.api,
			record:        record,
			processClient: c.api.processClient(record),
			filesClient:   c.api.filesystemClient(record),
		},
		allowShellFallback: request.AllowShellFallback,
	}

	if len(request.AdditionalPackages) > 0 {
		if err := sb.installAdditionalPackages(ctx, request.AdditionalPackages); err != nil {
			_ = sb.Destroy(ctx)
			return nil, err
		}
	}

	return sb, nil
}

// EnvdURL returns the envd base URL for a given sandbox ID. Useful for
// debugging or direct gRPC-Web inspection. The sandbox must exist;
// otherwise the URL will 404.
func (c *Client) EnvdURL(sandboxID string) string {
	return c.api.envdBaseURL(sandboxRecord{SandboxID: sandboxID})
}

// sandboxTransport is the set of transport clients a Sandbox needs to
// operate. Internal.
type sandboxTransport struct {
	api           *apiClient
	record        sandboxRecord
	processClient processconnect.ProcessClient
	filesClient   filesystemconnect.FilesystemClient
}
