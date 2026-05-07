package e2b

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// VolumeInfo describes a persistent volume.
type VolumeInfo struct {
	VolumeID string
	Name     string
}

// VolumeAndToken describes a persistent volume plus its content API token.
type VolumeAndToken struct {
	VolumeID string
	Name     string
	Token    string
}

// VolumeEntryType describes the type of a volume filesystem entry.
type VolumeEntryType string

const (
	VolumeEntryTypeUnknown   VolumeEntryType = "unknown"
	VolumeEntryTypeFile      VolumeEntryType = "file"
	VolumeEntryTypeDirectory VolumeEntryType = "directory"
	VolumeEntryTypeSymlink   VolumeEntryType = "symlink"
)

// VolumeEntryInfo describes a file or directory in a volume.
type VolumeEntryInfo struct {
	Name         string
	Type         VolumeEntryType
	Path         string
	Size         int64
	Mode         uint32
	UID          uint32
	GID          uint32
	AccessTime   time.Time
	ModifiedTime time.Time
	ChangeTime   time.Time
	Target       string
}

// VolumeMetadataOptions updates ownership or mode.
type VolumeMetadataOptions struct {
	UID  *uint32
	GID  *uint32
	Mode *uint32
}

// VolumeWriteOptions controls file and directory creation.
type VolumeWriteOptions struct {
	UID   *uint32
	GID   *uint32
	Mode  *uint32
	Force bool
}

type volumeRecord struct {
	VolumeID string `json:"volumeID"`
	Name     string `json:"name"`
}

type volumeAndTokenRecord struct {
	VolumeID string `json:"volumeID"`
	Name     string `json:"name"`
	Token    string `json:"token"`
}

type volumeEntryRecord struct {
	Name   string          `json:"name"`
	Type   VolumeEntryType `json:"type"`
	Path   string          `json:"path"`
	Size   int64           `json:"size"`
	Mode   uint32          `json:"mode"`
	UID    uint32          `json:"uid"`
	GID    uint32          `json:"gid"`
	ATime  time.Time       `json:"atime"`
	MTime  time.Time       `json:"mtime"`
	CTime  time.Time       `json:"ctime"`
	Target string          `json:"target"`
}

type newVolumeRequest struct {
	Name string `json:"name"`
}

type updateVolumePathRequest struct {
	UID  *uint32 `json:"uid,omitempty"`
	GID  *uint32 `json:"gid,omitempty"`
	Mode *uint32 `json:"mode,omitempty"`
}

// Volume is a handle to a persistent E2B volume.
type Volume struct {
	api      *apiClient
	volumeID string
	name     string
	token    string
}

// ID returns the volume ID.
func (v *Volume) ID() string { return v.volumeID }

// Name returns the volume name.
func (v *Volume) Name() string { return v.name }

// CreateVolume creates a new persistent volume.
func (c *Client) CreateVolume(ctx context.Context, name string) (*Volume, error) {
	var record volumeAndTokenRecord
	if err := c.api.doJSON(
		ctx,
		http.MethodPost,
		c.config.apiBaseURL()+"/volumes",
		newVolumeRequest{Name: strings.TrimSpace(name)},
		&record,
		nil,
		nil,
	); err != nil {
		return nil, err
	}
	return c.newVolume(record), nil
}

// ConnectVolume loads an existing volume and returns a handle to its content
// API.
func (c *Client) ConnectVolume(ctx context.Context, volumeID string) (*Volume, error) {
	record, err := c.GetVolumeInfo(ctx, volumeID)
	if err != nil {
		return nil, err
	}
	return c.newVolume(volumeAndTokenRecord{
		VolumeID: record.VolumeID,
		Name:     record.Name,
		Token:    record.Token,
	}), nil
}

// GetVolumeInfo loads a volume and its content token.
func (c *Client) GetVolumeInfo(ctx context.Context, volumeID string) (VolumeAndToken, error) {
	var record volumeAndTokenRecord
	if err := c.api.doJSON(
		ctx,
		http.MethodGet,
		c.config.apiBaseURL()+"/volumes/"+strings.TrimSpace(volumeID),
		nil,
		&record,
		nil,
		ErrVolumeNotFound,
	); err != nil {
		return VolumeAndToken{}, err
	}
	return VolumeAndToken{
		VolumeID: record.VolumeID,
		Name:     record.Name,
		Token:    record.Token,
	}, nil
}

// ListVolumes returns all team volumes.
func (c *Client) ListVolumes(ctx context.Context) ([]VolumeInfo, error) {
	var records []volumeRecord
	if err := c.api.doJSON(ctx, http.MethodGet, c.config.apiBaseURL()+"/volumes", nil, &records, nil, nil); err != nil {
		return nil, err
	}

	items := make([]VolumeInfo, 0, len(records))
	for _, record := range records {
		items = append(items, VolumeInfo{
			VolumeID: record.VolumeID,
			Name:     record.Name,
		})
	}
	return items, nil
}

// DestroyVolume deletes a volume. It returns false when the volume is already
// gone.
func (c *Client) DestroyVolume(ctx context.Context, volumeID string) (bool, error) {
	status, _, err := c.api.doJSONWithResponse(
		ctx,
		http.MethodDelete,
		c.config.apiBaseURL()+"/volumes/"+strings.TrimSpace(volumeID),
		nil,
		nil,
		map[int]struct{}{
			http.StatusNoContent: {},
			http.StatusNotFound:  {},
		},
		nil,
	)
	if err != nil {
		return false, err
	}
	return status == http.StatusNoContent, nil
}

// Destroy deletes the volume represented by this handle.
func (v *Volume) Destroy(ctx context.Context) (bool, error) {
	status, _, err := v.api.doJSONWithResponse(
		ctx,
		http.MethodDelete,
		v.api.config.apiBaseURL()+"/volumes/"+v.volumeID,
		nil,
		nil,
		map[int]struct{}{
			http.StatusNoContent: {},
			http.StatusNotFound:  {},
		},
		nil,
	)
	if err != nil {
		return false, err
	}
	return status == http.StatusNoContent, nil
}

// List returns directory contents from the volume.
func (v *Volume) List(ctx context.Context, path string, depth uint32) ([]VolumeEntryInfo, error) {
	query := url.Values{}
	query.Set("path", path)
	if depth > 0 {
		query.Set("depth", strconv.FormatUint(uint64(depth), 10))
	}

	var records []volumeEntryRecord
	if err := v.doJSON(ctx, http.MethodGet, "/volumecontent/"+v.volumeID+"/dir", query, nil, &records, nil, ErrFileNotFound); err != nil {
		return nil, err
	}

	items := make([]VolumeEntryInfo, 0, len(records))
	for _, record := range records {
		items = append(items, convertVolumeEntry(record))
	}
	return items, nil
}

// MakeDir creates a directory in the volume.
func (v *Volume) MakeDir(ctx context.Context, path string, options VolumeWriteOptions) (VolumeEntryInfo, error) {
	query := url.Values{}
	query.Set("path", path)
	addVolumeWriteOptions(query, options)

	var record volumeEntryRecord
	if err := v.doJSON(ctx, http.MethodPost, "/volumecontent/"+v.volumeID+"/dir", query, nil, &record, nil, ErrFileNotFound); err != nil {
		return VolumeEntryInfo{}, err
	}
	return convertVolumeEntry(record), nil
}

// Stat returns metadata about a path in the volume.
func (v *Volume) Stat(ctx context.Context, path string) (VolumeEntryInfo, error) {
	query := url.Values{}
	query.Set("path", path)

	var record volumeEntryRecord
	if err := v.doJSON(ctx, http.MethodGet, "/volumecontent/"+v.volumeID+"/path", query, nil, &record, nil, ErrFileNotFound); err != nil {
		return VolumeEntryInfo{}, err
	}
	return convertVolumeEntry(record), nil
}

// Exists reports whether a path exists in the volume.
func (v *Volume) Exists(ctx context.Context, path string) (bool, error) {
	_, err := v.Stat(ctx, path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrFileNotFound) {
		return false, nil
	}
	return false, err
}

// UpdateMetadata updates uid/gid/mode for a volume path.
func (v *Volume) UpdateMetadata(ctx context.Context, path string, options VolumeMetadataOptions) (VolumeEntryInfo, error) {
	query := url.Values{}
	query.Set("path", path)

	var record volumeEntryRecord
	if err := v.doJSON(
		ctx,
		http.MethodPatch,
		"/volumecontent/"+v.volumeID+"/path",
		query,
		updateVolumePathRequest{UID: options.UID, GID: options.GID, Mode: options.Mode},
		&record,
		nil,
		ErrFileNotFound,
	); err != nil {
		return VolumeEntryInfo{}, err
	}
	return convertVolumeEntry(record), nil
}

// ReadFile reads a file from the volume.
func (v *Volume) ReadFile(ctx context.Context, path string) ([]byte, error) {
	query := url.Values{}
	query.Set("path", path)

	status, _, body, err := v.doRequest(ctx, http.MethodGet, "/volumecontent/"+v.volumeID+"/file", query, nil, "")
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, normalizeHTTPError(status, string(body), ErrFileNotFound)
	}
	return body, nil
}

// WriteFile writes a file to the volume.
func (v *Volume) WriteFile(ctx context.Context, path string, content []byte, options VolumeWriteOptions) (VolumeEntryInfo, error) {
	query := url.Values{}
	query.Set("path", path)
	addVolumeWriteOptions(query, options)

	status, _, body, err := v.doRequest(ctx, http.MethodPut, "/volumecontent/"+v.volumeID+"/file", query, bytes.NewReader(content), "application/octet-stream")
	if err != nil {
		return VolumeEntryInfo{}, err
	}
	if status >= 300 {
		return VolumeEntryInfo{}, normalizeHTTPError(status, string(body), ErrFileNotFound)
	}

	var record volumeEntryRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return VolumeEntryInfo{}, err
	}
	return convertVolumeEntry(record), nil
}

// Remove deletes a file or directory from the volume.
func (v *Volume) Remove(ctx context.Context, path string) error {
	query := url.Values{}
	query.Set("path", path)

	_, _, err := v.doJSONWithStatus(ctx, http.MethodDelete, "/volumecontent/"+v.volumeID+"/path", query, nil, nil, map[int]struct{}{http.StatusNoContent: {}}, ErrFileNotFound)
	return err
}

func (c *Client) newVolume(record volumeAndTokenRecord) *Volume {
	return &Volume{
		api:      c.api,
		volumeID: record.VolumeID,
		name:     record.Name,
		token:    record.Token,
	}
}

func (v *Volume) doJSON(ctx context.Context, method string, route string, query url.Values, requestBody any, responseBody any, allowedStatuses map[int]struct{}, notFoundErr error) error {
	_, _, err := v.doJSONWithStatus(ctx, method, route, query, requestBody, responseBody, allowedStatuses, notFoundErr)
	return err
}

func (v *Volume) doJSONWithStatus(ctx context.Context, method string, route string, query url.Values, requestBody any, responseBody any, allowedStatuses map[int]struct{}, notFoundErr error) (int, http.Header, error) {
	var body io.Reader
	if requestBody != nil {
		payload, err := json.Marshal(requestBody)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(payload)
	}

	status, headers, responseBytes, err := v.doRequest(ctx, method, route, query, body, "application/json")
	if err != nil {
		return 0, nil, err
	}
	if _, ok := allowedStatuses[status]; ok {
		return status, headers, nil
	}
	if status >= 300 {
		return status, headers, normalizeHTTPError(status, string(responseBytes), notFoundErr)
	}
	if responseBody == nil {
		return status, headers, nil
	}
	if err := json.Unmarshal(responseBytes, responseBody); err != nil {
		return status, headers, err
	}
	return status, headers, nil
}

func (v *Volume) doRequest(ctx context.Context, method string, route string, query url.Values, body io.Reader, contentType string) (int, http.Header, []byte, error) {
	rawURL := v.api.config.apiBaseURL() + route
	if encoded := query.Encode(); encoded != "" {
		rawURL += "?" + encoded
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return 0, nil, nil, err
	}
	v.api.setUserAgent(req.Header)
	req.Header.Set("Authorization", "Bearer "+v.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := v.api.controlHTTPClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()

	responseBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, resp.Header.Clone(), nil, err
	}
	return resp.StatusCode, resp.Header.Clone(), responseBytes, nil
}

func addVolumeWriteOptions(query url.Values, options VolumeWriteOptions) {
	if options.UID != nil {
		query.Set("uid", strconv.FormatUint(uint64(*options.UID), 10))
	}
	if options.GID != nil {
		query.Set("gid", strconv.FormatUint(uint64(*options.GID), 10))
	}
	if options.Mode != nil {
		query.Set("mode", strconv.FormatUint(uint64(*options.Mode), 10))
	}
	if options.Force {
		query.Set("force", "true")
	}
}

func convertVolumeEntry(record volumeEntryRecord) VolumeEntryInfo {
	return VolumeEntryInfo{
		Name:         record.Name,
		Type:         record.Type,
		Path:         record.Path,
		Size:         record.Size,
		Mode:         record.Mode,
		UID:          record.UID,
		GID:          record.GID,
		AccessTime:   record.ATime,
		ModifiedTime: record.MTime,
		ChangeTime:   record.CTime,
		Target:       record.Target,
	}
}
