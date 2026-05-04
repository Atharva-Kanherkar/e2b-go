package e2b

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/e2b-dev/infra/packages/shared/pkg/grpc/envd/filesystem/filesystemconnect"
	"github.com/e2b-dev/infra/packages/shared/pkg/grpc/envd/process/processconnect"
)

// apiClient speaks to the E2B control-plane REST API and builds
// envd-scoped ConnectRPC clients for individual sandboxes.
type apiClient struct {
	controlHTTPClient *http.Client
	envdHTTPClient    *http.Client
	config            Config
	retryPolicy       RetryPolicy
	retrySleep        retrySleepFunc
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
		retryPolicy:       config.retryPolicy(),
		retrySleep:        sleepWithContext,
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
	status, _, body, err := c.doEnvdFileRequest(ctx, record, http.MethodGet, c.envdBaseURL(record)+"/files?"+values.Encode(), nil, "")
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
	status, _, respBody, err := c.doEnvdFileRequest(ctx, record, http.MethodPost, c.envdBaseURL(record)+"/files?"+values.Encode(), body.Bytes(), writer.FormDataContentType())
	if err != nil {
		return err
	}
	if status >= 300 {
		return normalizeHTTPError(status, string(respBody), nil)
	}
	return nil
}

func (c *apiClient) doEnvdFileRequest(ctx context.Context, record sandboxRecord, method string, rawURL string, body []byte, contentType string) (int, http.Header, []byte, error) {
	return c.doHTTPRequest(ctx, c.envdHTTPClient, method, rawURL, body, true, nil, func(header http.Header) {
		c.setEnvdHeaders(header, record)
		if contentType != "" {
			header.Set("Content-Type", contentType)
		}
	})
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
	var body []byte
	if requestBody != nil {
		payload, err := json.Marshal(requestBody)
		if err != nil {
			return 0, nil, err
		}
		body = payload
	}

	retryableRequest := c.shouldRetryRequest(method, rawURL)
	status, headers, respBytes, err := c.doHTTPRequest(ctx, c.controlHTTPClient, method, rawURL, body, retryableRequest, allowedEmptyStatuses, func(header http.Header) {
		header.Set("X-API-KEY", c.config.APIKey)
		header.Set("Content-Type", "application/json")
	})
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

func (c *apiClient) doHTTPRequest(ctx context.Context, client *http.Client, method string, rawURL string, body []byte, retryableRequest bool, terminalStatuses map[int]struct{}, setHeaders func(http.Header)) (int, http.Header, []byte, error) {
	maxAttempts := c.retryPolicy.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultRetryMaxAttempts
	}
	if !retryableRequest {
		maxAttempts = 1
	}

	var lastStatus int
	var lastHeaders http.Header
	var lastBody []byte
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return lastStatus, lastHeaders, lastBody, err
		}

		var requestBody io.Reader
		if body != nil {
			requestBody = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, rawURL, requestBody)
		if err != nil {
			return 0, nil, nil, err
		}
		if setHeaders != nil {
			setHeaders(req.Header)
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			if attempt == maxAttempts-1 || !shouldRetryNetworkError(ctx, err) {
				return 0, nil, nil, err
			}
			if err := c.sleepBeforeRetry(ctx, attempt, nil); err != nil {
				return 0, nil, nil, err
			}
			continue
		}

		respBytes, readErr := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		lastStatus = resp.StatusCode
		lastHeaders = resp.Header.Clone()
		lastBody = respBytes

		if readErr != nil {
			lastErr = readErr
			if attempt == maxAttempts-1 || !shouldRetryNetworkError(ctx, readErr) {
				return resp.StatusCode, lastHeaders, nil, readErr
			}
			if err := c.sleepBeforeRetry(ctx, attempt, lastHeaders); err != nil {
				return resp.StatusCode, lastHeaders, respBytes, err
			}
			continue
		}
		if closeErr != nil {
			lastErr = closeErr
			if attempt == maxAttempts-1 || !shouldRetryNetworkError(ctx, closeErr) {
				return resp.StatusCode, lastHeaders, respBytes, closeErr
			}
			if err := c.sleepBeforeRetry(ctx, attempt, lastHeaders); err != nil {
				return resp.StatusCode, lastHeaders, respBytes, err
			}
			continue
		}
		if _, terminal := terminalStatuses[resp.StatusCode]; terminal {
			return resp.StatusCode, lastHeaders, respBytes, nil
		}
		if attempt < maxAttempts-1 && shouldRetryHTTPStatus(resp.StatusCode) {
			if err := c.sleepBeforeRetry(ctx, attempt, lastHeaders); err != nil {
				return resp.StatusCode, lastHeaders, respBytes, err
			}
			continue
		}

		return resp.StatusCode, lastHeaders, respBytes, nil
	}

	return lastStatus, lastHeaders, lastBody, lastErr
}

func (c *apiClient) sleepBeforeRetry(ctx context.Context, attempt int, header http.Header) error {
	sleep := c.retrySleep
	if sleep == nil {
		sleep = sleepWithContext
	}
	return sleep(ctx, retryDelay(c.retryPolicy, attempt, header, time.Now()))
}

func (c *apiClient) shouldRetryRequest(method string, rawURL string) bool {
	if isIdempotentHTTPMethod(method) {
		return true
	}
	if method != http.MethodPost {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return isSafeControlPlanePost(parsed.Path)
}

func isSafeControlPlanePost(requestPath string) bool {
	return strings.HasSuffix(requestPath, "/connect") ||
		strings.HasSuffix(requestPath, "/pause") ||
		strings.HasSuffix(requestPath, "/timeout")
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
