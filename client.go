package e2b

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/e2b-dev/infra/packages/shared/pkg/grpc/envd/filesystem/filesystemconnect"
	"github.com/e2b-dev/infra/packages/shared/pkg/grpc/envd/process/processconnect"
)

// apiClient speaks to the E2B control-plane REST API and builds
// envd-scoped ConnectRPC clients for individual sandboxes.
type apiClient struct {
	controlHTTPClient *http.Client
	envdHTTPClient    *http.Client
	config            Config
	sleep             retrySleepFunc
}

type sandboxRecord struct {
	SandboxID        string  `json:"sandboxID"`
	TemplateID       string  `json:"templateID"`
	EnvdVersion      string  `json:"envdVersion"`
	Domain           *string `json:"domain"`
	EnvdAccessToken  string  `json:"envdAccessToken"`
	TrafficAuthToken *string `json:"trafficAccessToken"`
}

func newAPIClient(config Config) *apiClient {
	return &apiClient{
		controlHTTPClient: &http.Client{Timeout: config.requestTimeout()},
		envdHTTPClient:    &http.Client{},
		config:            config,
	}
}

func (c *apiClient) createSandbox(ctx context.Context, request createSandboxRequest) (sandboxRecord, error) {
	var record sandboxRecord
	if err := c.doJSON(ctx, http.MethodPost, c.config.apiBaseURL()+"/sandboxes", request, &record, nil, nil); err != nil {
		return sandboxRecord{}, err
	}
	return record, nil
}

func (c *apiClient) destroySandbox(ctx context.Context, sandboxID string) error {
	return c.doJSON(
		ctx,
		http.MethodDelete,
		c.config.apiBaseURL()+"/sandboxes/"+sandboxID,
		nil,
		nil,
		map[int]struct{}{http.StatusNoContent: {}},
		ErrSandboxNotFound,
	)
}

func (c *apiClient) envdBaseURL(record sandboxRecord) string {
	domain := defaultDomain
	if record.Domain != nil && strings.TrimSpace(*record.Domain) != "" {
		domain = strings.TrimSpace(*record.Domain)
	}
	return fmt.Sprintf("https://%d-%s.%s", defaultEnvdPort, record.SandboxID, domain)
}

func (c *apiClient) filesystemClient(record sandboxRecord) filesystemconnect.FilesystemClient {
	return filesystemconnect.NewFilesystemClient(c.envdHTTPClient, c.envdBaseURL(record))
}

func (c *apiClient) processClient(record sandboxRecord) processconnect.ProcessClient {
	return processconnect.NewProcessClient(c.envdHTTPClient, c.envdBaseURL(record))
}

func (c *apiClient) readFile(ctx context.Context, record sandboxRecord, filePath string) ([]byte, error) {
	values := c.envdFileQuery(record, filePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.envdBaseURL(record)+"/files?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	c.setEnvdHeaders(req.Header, record)
	status, _, body, err := c.doHTTPWithRetry(ctx, c.envdHTTPClient, req.Method, req.URL.String(), nil, req.Header)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, normalizeHTTPError(status, string(body), ErrFileNotFound)
	}
	return body, nil
}

func (c *apiClient) writeFile(ctx context.Context, record sandboxRecord, filePath string, content []byte) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", path.Base(strings.TrimSpace(filePath)))
	if err != nil {
		return err
	}
	if _, err := part.Write(content); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	values := c.envdFileQuery(record, filePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.envdBaseURL(record)+"/files?"+values.Encode(), &body)
	if err != nil {
		return err
	}
	c.setEnvdHeaders(req.Header, record)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	status, _, respBody, err := c.doHTTPWithRetry(ctx, c.envdHTTPClient, req.Method, req.URL.String(), body.Bytes(), req.Header)
	if err != nil {
		return err
	}
	if status >= 300 {
		return normalizeHTTPError(status, string(respBody), nil)
	}
	return nil
}

func (c *apiClient) setEnvdHeaders(header http.Header, record sandboxRecord) {
	header.Set("X-Access-Token", record.EnvdAccessToken)
	header.Set("E2b-Sandbox-Id", record.SandboxID)
	header.Set("E2b-Sandbox-Port", strconv.Itoa(defaultEnvdPort))
}

func (c *apiClient) envdFileQuery(record sandboxRecord, filePath string) url.Values {
	values := url.Values{}
	values.Set("path", filePath)
	if username := legacySandboxUsername(record.EnvdVersion); username != "" {
		values.Set("username", username)
	}
	return values
}

func legacySandboxUsername(envdVersion string) string {
	if usesLegacySandboxUser(envdVersion) {
		return defaultLegacySandboxUser
	}
	return ""
}

func legacySandboxAuthHeader(envdVersion string) string {
	username := legacySandboxUsername(envdVersion)
	if username == "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"))
}

func (c *apiClient) doJSON(ctx context.Context, method string, rawURL string, requestBody any, responseBody any, allowedEmptyStatuses map[int]struct{}, notFoundErr error) error {
	_, _, err := c.doJSONWithResponse(ctx, method, rawURL, requestBody, responseBody, allowedEmptyStatuses, notFoundErr)
	return err
}

func (c *apiClient) doJSONWithResponse(ctx context.Context, method string, rawURL string, requestBody any, responseBody any, allowedEmptyStatuses map[int]struct{}, notFoundErr error) (int, http.Header, error) {
	var bodyBytes []byte
	if requestBody != nil {
		payload, err := json.Marshal(requestBody)
		if err != nil {
			return 0, nil, err
		}
		bodyBytes = payload
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("X-API-KEY", c.config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	status, headers, respBytes, err := c.doHTTPWithRetry(ctx, c.controlHTTPClient, req.Method, req.URL.String(), bodyBytes, req.Header)
	if err != nil {
		return 0, nil, err
	}

	if _, ok := allowedEmptyStatuses[status]; ok {
		return status, headers, nil
	}
	if status >= 300 {
		return status, headers, normalizeHTTPError(status, string(respBytes), notFoundErr)
	}
	if responseBody == nil {
		return status, headers, nil
	}
	if err := json.Unmarshal(respBytes, responseBody); err != nil {
		return status, headers, err
	}
	return status, headers, nil
}

// createSandboxRequest is the wire format of POST /sandboxes. Kept
// unexported because the E2B API shape is not a stable public contract.
type createSandboxRequest struct {
	TemplateID          string            `json:"templateID"`
	Timeout             int               `json:"timeout"`
	Metadata            map[string]string `json:"metadata,omitempty"`
	Secure              bool              `json:"secure"`
	AllowInternetAccess bool              `json:"allow_internet_access"`
	EnvVars             map[string]string `json:"envVars,omitempty"`
	Network             *networkConfig    `json:"network,omitempty"`
}

type networkConfig struct {
	AllowOut []string `json:"allowOut,omitempty"`
}
