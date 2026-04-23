package e2b

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type sandboxNetworkRecord struct {
	AllowPublicTraffic *bool    `json:"allowPublicTraffic"`
	AllowOut           []string `json:"allowOut"`
	DenyOut            []string `json:"denyOut"`
	MaskRequestHost    string   `json:"maskRequestHost"`
}

type sandboxLifecycleRecord struct {
	AutoResume bool   `json:"autoResume"`
	OnTimeout  string `json:"onTimeout"`
}

type sandboxVolumeMountRecord struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type sandboxInfoRecord struct {
	TemplateID          string                     `json:"templateID"`
	Alias               string                     `json:"alias"`
	SandboxID           string                     `json:"sandboxID"`
	StartedAt           time.Time                  `json:"startedAt"`
	EndAt               time.Time                  `json:"endAt"`
	EnvdVersion         string                     `json:"envdVersion"`
	AllowInternetAccess *bool                      `json:"allowInternetAccess"`
	Domain              *string                    `json:"domain"`
	CPUCount            int                        `json:"cpuCount"`
	MemoryMB            int                        `json:"memoryMB"`
	DiskSizeMB          int                        `json:"diskSizeMB"`
	Metadata            map[string]string          `json:"metadata"`
	State               SandboxState               `json:"state"`
	Network             *sandboxNetworkRecord      `json:"network"`
	Lifecycle           *sandboxLifecycleRecord    `json:"lifecycle"`
	VolumeMounts        []sandboxVolumeMountRecord `json:"volumeMounts"`
}

type connectSandboxRequest struct {
	Timeout int `json:"timeout"`
}

type setTimeoutRequest struct {
	Timeout int `json:"timeout"`
}

type snapshotInfoRecord struct {
	SnapshotID string   `json:"snapshotID"`
	Names      []string `json:"names"`
}

type createSnapshotWireRequest struct {
	Name string `json:"name,omitempty"`
}

type sandboxMetricRecord struct {
	Timestamp     time.Time `json:"timestamp"`
	TimestampUnix int64     `json:"timestampUnix"`
	CPUCount      int       `json:"cpuCount"`
	CPUUsedPct    float64   `json:"cpuUsedPct"`
	MemUsed       int64     `json:"memUsed"`
	MemTotal      int64     `json:"memTotal"`
	DiskUsed      int64     `json:"diskUsed"`
	DiskTotal     int64     `json:"diskTotal"`
}

func (c *apiClient) listSandboxes(ctx context.Context, request ListSandboxesRequest) (ListSandboxesResponse, error) {
	query := url.Values{}
	if metadata := encodeMetadataQuery(request.Metadata); metadata != "" {
		query.Set("metadata", metadata)
	}
	if len(request.States) > 0 {
		states := make([]string, 0, len(request.States))
		for _, state := range request.States {
			if strings.TrimSpace(string(state)) == "" {
				continue
			}
			states = append(states, string(state))
		}
		if len(states) > 0 {
			query.Set("state", strings.Join(states, ","))
		}
	}
	if request.Limit > 0 {
		query.Set("limit", strconv.Itoa(request.Limit))
	}
	if strings.TrimSpace(request.NextToken) != "" {
		query.Set("nextToken", strings.TrimSpace(request.NextToken))
	}

	endpoint := c.config.apiBaseURL() + "/v2/sandboxes"
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	var records []sandboxInfoRecord
	status, headers, err := c.doJSONWithResponse(ctx, http.MethodGet, endpoint, nil, &records, nil, nil)
	if err != nil {
		return ListSandboxesResponse{}, err
	}
	if status != http.StatusOK {
		return ListSandboxesResponse{}, fmt.Errorf("e2b: unexpected status %d listing sandboxes", status)
	}

	return ListSandboxesResponse{
		Sandboxes: convertSandboxInfos(records),
		NextToken: headers.Get("X-Next-Token"),
	}, nil
}

func (c *apiClient) getSandboxInfo(ctx context.Context, sandboxID string) (SandboxInfo, error) {
	var record sandboxInfoRecord
	if err := c.doJSON(
		ctx,
		http.MethodGet,
		c.config.apiBaseURL()+"/sandboxes/"+sandboxID,
		nil,
		&record,
		nil,
		ErrSandboxNotFound,
	); err != nil {
		return SandboxInfo{}, err
	}
	return convertSandboxInfo(record), nil
}

func (c *apiClient) connectSandbox(ctx context.Context, request ConnectSandboxRequest) (sandboxRecord, error) {
	var record sandboxRecord
	if err := c.doJSON(
		ctx,
		http.MethodPost,
		c.config.apiBaseURL()+"/sandboxes/"+request.SandboxID+"/connect",
		connectSandboxRequest{Timeout: connectTimeoutSeconds(request.Timeout)},
		&record,
		nil,
		ErrSandboxNotFound,
	); err != nil {
		return sandboxRecord{}, err
	}
	return record, nil
}

func (c *apiClient) pauseSandbox(ctx context.Context, sandboxID string) (bool, error) {
	status, _, err := c.doJSONWithResponse(
		ctx,
		http.MethodPost,
		c.config.apiBaseURL()+"/sandboxes/"+sandboxID+"/pause",
		nil,
		nil,
		map[int]struct{}{
			http.StatusNoContent: {},
			http.StatusConflict:  {},
		},
		ErrSandboxNotFound,
	)
	if err != nil {
		return false, err
	}
	return status == http.StatusNoContent, nil
}

func (c *apiClient) setSandboxTimeout(ctx context.Context, sandboxID string, timeout time.Duration) error {
	return c.doJSON(
		ctx,
		http.MethodPost,
		c.config.apiBaseURL()+"/sandboxes/"+sandboxID+"/timeout",
		setTimeoutRequest{Timeout: durationToWholeSeconds(timeout)},
		nil,
		map[int]struct{}{http.StatusNoContent: {}},
		ErrSandboxNotFound,
	)
}

func (c *apiClient) getSandboxMetrics(ctx context.Context, sandboxID string, request SandboxMetricsRequest) ([]SandboxMetric, error) {
	query := url.Values{}
	if !request.Start.IsZero() {
		query.Set("start", strconv.FormatInt(request.Start.Unix(), 10))
	}
	if !request.End.IsZero() {
		query.Set("end", strconv.FormatInt(request.End.Unix(), 10))
	}

	endpoint := c.config.apiBaseURL() + "/sandboxes/" + sandboxID + "/metrics"
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	var records []sandboxMetricRecord
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &records, nil, ErrSandboxNotFound); err != nil {
		return nil, err
	}
	metrics := make([]SandboxMetric, 0, len(records))
	for _, record := range records {
		metrics = append(metrics, SandboxMetric{
			Timestamp:     record.Timestamp,
			TimestampUnix: record.TimestampUnix,
			CPUCount:      record.CPUCount,
			CPUUsedPct:    record.CPUUsedPct,
			MemUsed:       record.MemUsed,
			MemTotal:      record.MemTotal,
			DiskUsed:      record.DiskUsed,
			DiskTotal:     record.DiskTotal,
		})
	}
	return metrics, nil
}

func (c *apiClient) createSnapshot(ctx context.Context, sandboxID string, request CreateSnapshotRequest) (SnapshotInfo, error) {
	var record snapshotInfoRecord
	if err := c.doJSON(
		ctx,
		http.MethodPost,
		c.config.apiBaseURL()+"/sandboxes/"+sandboxID+"/snapshots",
		createSnapshotWireRequest{Name: strings.TrimSpace(request.Name)},
		&record,
		nil,
		ErrSandboxNotFound,
	); err != nil {
		return SnapshotInfo{}, err
	}
	return convertSnapshotInfo(record), nil
}

func (c *apiClient) listSnapshots(ctx context.Context, request ListSnapshotsRequest) (ListSnapshotsResponse, error) {
	query := url.Values{}
	if strings.TrimSpace(request.SandboxID) != "" {
		query.Set("sandboxID", strings.TrimSpace(request.SandboxID))
	}
	if request.Limit > 0 {
		query.Set("limit", strconv.Itoa(request.Limit))
	}
	if strings.TrimSpace(request.NextToken) != "" {
		query.Set("nextToken", strings.TrimSpace(request.NextToken))
	}

	endpoint := c.config.apiBaseURL() + "/snapshots"
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	var records []snapshotInfoRecord
	status, headers, err := c.doJSONWithResponse(ctx, http.MethodGet, endpoint, nil, &records, nil, nil)
	if err != nil {
		return ListSnapshotsResponse{}, err
	}
	if status != http.StatusOK {
		return ListSnapshotsResponse{}, fmt.Errorf("e2b: unexpected status %d listing snapshots", status)
	}

	items := make([]SnapshotInfo, 0, len(records))
	for _, record := range records {
		items = append(items, convertSnapshotInfo(record))
	}

	return ListSnapshotsResponse{
		Snapshots: items,
		NextToken: headers.Get("X-Next-Token"),
	}, nil
}

func (c *apiClient) deleteSnapshot(ctx context.Context, snapshotID string) (bool, error) {
	status, _, err := c.doJSONWithResponse(
		ctx,
		http.MethodDelete,
		c.config.apiBaseURL()+"/templates/"+snapshotID,
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

func (c *Client) ListSandboxes(ctx context.Context, request ListSandboxesRequest) (ListSandboxesResponse, error) {
	return c.api.listSandboxes(ctx, request)
}

func (c *Client) GetSandboxInfo(ctx context.Context, sandboxID string) (SandboxInfo, error) {
	if strings.TrimSpace(sandboxID) == "" {
		return SandboxInfo{}, fmt.Errorf("e2b: sandbox ID is required")
	}
	return c.api.getSandboxInfo(ctx, strings.TrimSpace(sandboxID))
}

func (c *Client) ConnectSandbox(ctx context.Context, request ConnectSandboxRequest) (*Sandbox, error) {
	if strings.TrimSpace(request.SandboxID) == "" {
		return nil, fmt.Errorf("e2b: ConnectSandboxRequest.SandboxID is required")
	}

	record, err := c.api.connectSandbox(ctx, request)
	if err != nil {
		return nil, err
	}
	return c.newSandbox(record, request.AllowShellFallback), nil
}

func (c *Client) ListSnapshots(ctx context.Context, request ListSnapshotsRequest) (ListSnapshotsResponse, error) {
	return c.api.listSnapshots(ctx, request)
}

func (c *Client) DeleteSnapshot(ctx context.Context, snapshotID string) (bool, error) {
	if strings.TrimSpace(snapshotID) == "" {
		return false, fmt.Errorf("e2b: snapshot ID is required")
	}
	return c.api.deleteSnapshot(ctx, strings.TrimSpace(snapshotID))
}

func (s *Sandbox) Connect(ctx context.Context, timeout time.Duration) error {
	transport, err := s.activeTransport()
	if err != nil {
		return err
	}

	record, err := transport.api.connectSandbox(ctx, ConnectSandboxRequest{
		SandboxID: transport.record.SandboxID,
		Timeout:   timeout,
	})
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.destroying {
		return ErrSandboxDestroyed
	}

	s.client.record = record
	s.client.processClient = transport.api.processClient(record)
	s.client.filesClient = transport.api.filesystemClient(record)
	return nil
}

func (s *Sandbox) GetHost(port int) string {
	s.mu.Lock()
	record := s.client.record
	s.mu.Unlock()

	domain := defaultDomain
	if record.Domain != nil && strings.TrimSpace(*record.Domain) != "" {
		domain = strings.TrimSpace(*record.Domain)
	}
	return fmt.Sprintf("%d-%s.%s", port, record.SandboxID, domain)
}

func (s *Sandbox) GetInfo(ctx context.Context) (SandboxInfo, error) {
	transport, err := s.activeTransport()
	if err != nil {
		return SandboxInfo{}, err
	}
	return transport.api.getSandboxInfo(ctx, transport.record.SandboxID)
}

func (s *Sandbox) Pause(ctx context.Context) (bool, error) {
	transport, err := s.activeTransport()
	if err != nil {
		return false, err
	}
	return transport.api.pauseSandbox(ctx, transport.record.SandboxID)
}

func (s *Sandbox) SetTimeout(ctx context.Context, timeout time.Duration) error {
	transport, err := s.activeTransport()
	if err != nil {
		return err
	}
	return transport.api.setSandboxTimeout(ctx, transport.record.SandboxID, timeout)
}

func (s *Sandbox) GetMetrics(ctx context.Context, request SandboxMetricsRequest) ([]SandboxMetric, error) {
	transport, err := s.activeTransport()
	if err != nil {
		return nil, err
	}
	return transport.api.getSandboxMetrics(ctx, transport.record.SandboxID, request)
}

func (s *Sandbox) CreateSnapshot(ctx context.Context, request CreateSnapshotRequest) (SnapshotInfo, error) {
	transport, err := s.activeTransport()
	if err != nil {
		return SnapshotInfo{}, err
	}
	return transport.api.createSnapshot(ctx, transport.record.SandboxID, request)
}

func (s *Sandbox) ListSnapshots(ctx context.Context, request ListSnapshotsRequest) (ListSnapshotsResponse, error) {
	transport, err := s.activeTransport()
	if err != nil {
		return ListSnapshotsResponse{}, err
	}
	if strings.TrimSpace(request.SandboxID) == "" {
		request.SandboxID = transport.record.SandboxID
	}
	return transport.api.listSnapshots(ctx, request)
}

// Kill is an alias for Destroy to match the control-plane naming used in the
// upstream SDKs.
func (s *Sandbox) Kill(ctx context.Context) error {
	return s.Destroy(ctx)
}

func convertSandboxInfos(records []sandboxInfoRecord) []SandboxInfo {
	items := make([]SandboxInfo, 0, len(records))
	for _, record := range records {
		items = append(items, convertSandboxInfo(record))
	}
	return items
}

func convertSandboxInfo(record sandboxInfoRecord) SandboxInfo {
	info := SandboxInfo{
		SandboxID:           record.SandboxID,
		TemplateID:          record.TemplateID,
		Name:                record.Alias,
		Metadata:            cloneStringMap(record.Metadata),
		StartedAt:           record.StartedAt,
		EndAt:               record.EndAt,
		State:               record.State,
		CPUCount:            record.CPUCount,
		MemoryMB:            record.MemoryMB,
		DiskSizeMB:          record.DiskSizeMB,
		EnvdVersion:         record.EnvdVersion,
		AllowInternetAccess: cloneBoolPtr(record.AllowInternetAccess),
		VolumeMounts:        make([]SandboxVolumeMount, 0, len(record.VolumeMounts)),
	}
	if record.Domain != nil {
		info.Domain = *record.Domain
	}
	if record.Network != nil {
		info.Network = &SandboxNetwork{
			AllowPublicTraffic: cloneBoolPtr(record.Network.AllowPublicTraffic),
			AllowOut:           append([]string(nil), record.Network.AllowOut...),
			DenyOut:            append([]string(nil), record.Network.DenyOut...),
			MaskRequestHost:    record.Network.MaskRequestHost,
		}
	}
	if record.Lifecycle != nil {
		info.Lifecycle = &SandboxLifecycle{
			AutoResume: record.Lifecycle.AutoResume,
			OnTimeout:  record.Lifecycle.OnTimeout,
		}
	}
	for _, mount := range record.VolumeMounts {
		info.VolumeMounts = append(info.VolumeMounts, SandboxVolumeMount{
			Name: mount.Name,
			Path: mount.Path,
		})
	}
	return info
}

func convertSnapshotInfo(record snapshotInfoRecord) SnapshotInfo {
	return SnapshotInfo{
		SnapshotID: record.SnapshotID,
		Names:      append([]string(nil), record.Names...),
	}
}

func encodeMetadataQuery(metadata map[string]string) string {
	if len(metadata) == 0 {
		return ""
	}

	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	values := url.Values{}
	for _, key := range keys {
		values.Set(key, metadata[key])
	}
	return values.Encode()
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
