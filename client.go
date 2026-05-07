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

	"github.com/e2b-dev/infra/packages/shared/pkg/grpc/envd/filesystem/filesystemconnect"
	"github.com/e2b-dev/infra/packages/shared/pkg/grpc/envd/process/processconnect"
)

// apiClient speaks to the E2B control-plane REST API and builds
// envd-scoped ConnectRPC clients for individual sandboxes.
type apiClient struct {
	controlHTTPClient *http.Client
	envdHTTPClient    *http.Client
	config            Config
	sleep             sleepFunc
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
		sleep:             defaultSleep,
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
	rawURL := c.envdBaseURL(record) + "/files?" + c.envdFileQuery(record, filePath).Encode()

	var result []byte
	_, _, err := doWithRetry(ctx, c.config.RetryPolicy, c.sleep, func() (int, http.Header, bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return 0, nil, false, err
		}
		c.setEnvdHeaders(req.Header, record)

		resp, err := c.envdHTTPClient.Do(req)
		if err != nil {
			return 0, nil, isTransientNetworkError(err), err
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return resp.StatusCode, resp.Header.Clone(), false, err
		}
		if resp.StatusCode >= 300 {
			return resp.StatusCode, resp.Header.Clone(), isRetryableStatus(resp.StatusCode),
				normalizeHTTPError(resp.StatusCode, string(body), ErrFileNotFound)
		}
		result = body
		return resp.StatusCode, resp.Header.Clone(), false, nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (c *apiClient) writeFile(ctx context.Context, record sandboxRecord, filePath string, content []byte) error {
	// Build the multipart body once; replay the same bytes on every attempt.
	var formBuf bytes.Buffer
	writer := multipart.NewWriter(&formBuf)
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
	formBytes := formBuf.Bytes()
	contentType := writer.FormDataContentType()
	rawURL := c.envdBaseURL(record) + "/files?" + c.envdFileQuery(record, filePath).Encode()

	_, _, err = doWithRetry(ctx, c.config.RetryPolicy, c.sleep, func() (int, http.Header, bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(formBytes))
		if err != nil {
			return 0, nil, false, err
		}
		c.setEnvdHeaders(req.Header, record)
		req.Header.Set("Content-Type", contentType)

		resp, err := c.envdHTTPClient.Do(req)
		if err != nil {
			return 0, nil, isTransientNetworkError(err), err
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return resp.StatusCode, resp.Header.Clone(), false, err
		}
		if resp.StatusCode >= 300 {
			return resp.StatusCode, resp.Header.Clone(), isRetryableStatus(resp.StatusCode),
				normalizeHTTPError(resp.StatusCode, string(respBody), nil)
		}
		return resp.StatusCode, resp.Header.Clone(), false, nil
	})
	return err
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
	// Marshal once; create a fresh reader for every attempt so the body can
	// be replayed across retries.
	var payload []byte
	if requestBody != nil {
		var err error
		payload, err = json.Marshal(requestBody)
		if err != nil {
			return 0, nil, err
		}
	}

	return doWithRetry(ctx, c.config.RetryPolicy, c.sleep, func() (int, http.Header, bool, error) {
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
		if err != nil {
			return 0, nil, false, err
		}
		req.Header.Set("X-API-KEY", c.config.APIKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.controlHTTPClient.Do(req)
		if err != nil {
			return 0, nil, isTransientNetworkError(err), err
		}
		defer resp.Body.Close()

		respBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return resp.StatusCode, resp.Header.Clone(), false, err
		}
		if _, ok := allowedEmptyStatuses[resp.StatusCode]; ok {
			return resp.StatusCode, resp.Header.Clone(), false, nil
		}
		if resp.StatusCode >= 300 {
			return resp.StatusCode, resp.Header.Clone(), isRetryableStatus(resp.StatusCode),
				normalizeHTTPError(resp.StatusCode, string(respBytes), notFoundErr)
		}
		if responseBody == nil {
			return resp.StatusCode, resp.Header.Clone(), false, nil
		}
		if err := json.Unmarshal(respBytes, responseBody); err != nil {
			return resp.StatusCode, resp.Header.Clone(), false, err
		}
		return resp.StatusCode, resp.Header.Clone(), false, nil
	})
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
