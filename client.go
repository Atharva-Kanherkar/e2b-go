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
	retrySleep        retrySleepFunc
	retryNow          retryNowFunc
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
		retrySleep:        defaultRetrySleep,
		retryNow:          time.Now,
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
	headers := req.Header.Clone()
	c.setEnvdHeaders(headers, record)
	resp, err := c.doRetryableHTTP(ctx, c.envdHTTPClient, http.MethodGet, req.URL.String(), nil, headers, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, normalizeHTTPError(resp.StatusCode, string(body), ErrFileNotFound)
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
	headers := req.Header.Clone()
	c.setEnvdHeaders(headers, record)
	headers.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.doRetryableHTTP(ctx, c.envdHTTPClient, http.MethodPost, req.URL.String(), append([]byte(nil), body.Bytes()...), headers, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return normalizeHTTPError(resp.StatusCode, string(respBody), nil)
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
	var body []byte
	if requestBody != nil {
		payload, err := json.Marshal(requestBody)
		if err != nil {
			return 0, nil, err
		}
		body = payload
	}
	headers := make(http.Header)
	headers.Set("X-API-KEY", c.config.APIKey)
	headers.Set("Content-Type", "application/json")

	resp, err := c.doRetryableHTTP(ctx, c.controlHTTPClient, method, rawURL, body, headers, retryTemporaryNetworkErrors(method))
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, resp.Header.Clone(), err
	}
	if _, ok := allowedEmptyStatuses[resp.StatusCode]; ok {
		return resp.StatusCode, resp.Header.Clone(), nil
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, resp.Header.Clone(), normalizeHTTPError(resp.StatusCode, string(respBytes), notFoundErr)
	}
	if responseBody == nil {
		return resp.StatusCode, resp.Header.Clone(), nil
	}
	if err := json.Unmarshal(respBytes, responseBody); err != nil {
		return resp.StatusCode, resp.Header.Clone(), err
	}
	return resp.StatusCode, resp.Header.Clone(), nil
}

func (c *apiClient) doRetryableHTTP(ctx context.Context, client *http.Client, method string, rawURL string, body []byte, headers http.Header, retryNetworkErrors bool) (*http.Response, error) {
	policy := c.config.retryPolicy()

	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
		if err != nil {
			return nil, err
		}
		req.Header = headers.Clone()

		resp, err := client.Do(req)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			if attempt >= policy.MaxAttempts || !retryNetworkErrors || !isTemporaryNetworkError(err) {
				return nil, err
			}
			if err := c.retrySleep(ctx, policy.backoff(attempt)); err != nil {
				return nil, err
			}
			continue
		}

		if attempt >= policy.MaxAttempts || !isRetryableStatus(resp.StatusCode) {
			return resp, nil
		}

		delay := retryDelay(resp.Header, policy, attempt, c.retryNow())
		closeRetryResponse(resp)
		if err := c.retrySleep(ctx, delay); err != nil {
			return nil, err
		}
	}
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
