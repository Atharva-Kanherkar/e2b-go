package e2b

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestClientListSandboxesParsesRecords(t *testing.T) {
	startedAt := time.Date(2026, 4, 23, 8, 0, 0, 0, time.UTC)
	endAt := startedAt.Add(5 * time.Minute)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/v2/sandboxes" {
			t.Fatalf("path = %s, want /v2/sandboxes", r.URL.Path)
		}
		if got := r.URL.Query().Get("metadata"); got != "app=prod&user=alice" {
			t.Fatalf("metadata query = %q, want app=prod&user=alice", got)
		}
		if got := r.URL.Query().Get("state"); got != "running,paused" {
			t.Fatalf("state query = %q, want running,paused", got)
		}
		if got := r.URL.Query().Get("limit"); got != "25" {
			t.Fatalf("limit query = %q, want 25", got)
		}
		if got := r.URL.Query().Get("nextToken"); got != "page-2" {
			t.Fatalf("nextToken query = %q, want page-2", got)
		}

		w.Header().Set("X-Next-Token", "page-3")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"sandboxID":   "sbx-1",
				"templateID":  "tmpl-base",
				"alias":       "team/base",
				"startedAt":   startedAt.Format(time.RFC3339),
				"endAt":       endAt.Format(time.RFC3339),
				"cpuCount":    2,
				"memoryMB":    1024,
				"diskSizeMB":  4096,
				"state":       "running",
				"envdVersion": "0.4.1",
				"metadata": map[string]string{
					"user": "alice",
				},
				"volumeMounts": []map[string]string{
					{"name": "cache", "path": "/mnt/cache"},
				},
			},
		})
	}))
	defer server.Close()

	client := NewClientWithConfig(Config{
		APIKey:         "test-key",
		APIBaseURL:     server.URL,
		RequestTimeout: time.Second,
		RetryPolicy:    RetryPolicy{MaxAttempts: 1},
	})

	resp, err := client.ListSandboxes(context.Background(), ListSandboxesRequest{
		Metadata: map[string]string{
			"user": "alice",
			"app":  "prod",
		},
		States:    []SandboxState{SandboxStateRunning, SandboxStatePaused},
		Limit:     25,
		NextToken: "page-2",
	})
	if err != nil {
		t.Fatalf("ListSandboxes() error = %v", err)
	}

	if got, want := resp.NextToken, "page-3"; got != want {
		t.Fatalf("NextToken = %q, want %q", got, want)
	}
	if len(resp.Sandboxes) != 1 {
		t.Fatalf("len(Sandboxes) = %d, want 1", len(resp.Sandboxes))
	}

	sandbox := resp.Sandboxes[0]
	if got, want := sandbox.SandboxID, "sbx-1"; got != want {
		t.Fatalf("SandboxID = %q, want %q", got, want)
	}
	if got, want := sandbox.TemplateID, "tmpl-base"; got != want {
		t.Fatalf("TemplateID = %q, want %q", got, want)
	}
	if got, want := sandbox.Name, "team/base"; got != want {
		t.Fatalf("Name = %q, want %q", got, want)
	}
	if !sandbox.StartedAt.Equal(startedAt) {
		t.Fatalf("StartedAt = %v, want %v", sandbox.StartedAt, startedAt)
	}
	if !sandbox.EndAt.Equal(endAt) {
		t.Fatalf("EndAt = %v, want %v", sandbox.EndAt, endAt)
	}
	if got, want := sandbox.Metadata["user"], "alice"; got != want {
		t.Fatalf("Metadata[user] = %q, want %q", got, want)
	}
	if len(sandbox.VolumeMounts) != 1 || sandbox.VolumeMounts[0].Name != "cache" || sandbox.VolumeMounts[0].Path != "/mnt/cache" {
		t.Fatalf("VolumeMounts = %#v, want one cache mount", sandbox.VolumeMounts)
	}
}

func TestClientConnectSandboxBuildsOperationalHandle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/sandboxes/sbx-123/connect" {
			t.Fatalf("path = %s, want /sandboxes/sbx-123/connect", r.URL.Path)
		}

		var payload map[string]int
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode connect body: %v", err)
		}
		if got, want := payload["timeout"], 300; got != want {
			t.Fatalf("timeout = %d, want %d", got, want)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sandboxID":          "sbx-123",
			"templateID":         "tmpl-connect",
			"envdVersion":        "0.4.2",
			"envdAccessToken":    "envd-token",
			"trafficAccessToken": "traffic-token",
			"domain":             "custom.e2b.dev",
		})
	}))
	defer server.Close()

	client := NewClientWithConfig(Config{
		APIKey:         "test-key",
		APIBaseURL:     server.URL,
		RequestTimeout: time.Second,
	})

	sb, err := client.ConnectSandbox(context.Background(), ConnectSandboxRequest{
		SandboxID:          "sbx-123",
		AllowShellFallback: true,
	})
	if err != nil {
		t.Fatalf("ConnectSandbox() error = %v", err)
	}

	if got, want := sb.ID(), "sbx-123"; got != want {
		t.Fatalf("ID() = %q, want %q", got, want)
	}
	if got, want := sb.TemplateID(), "tmpl-connect"; got != want {
		t.Fatalf("TemplateID() = %q, want %q", got, want)
	}
	if got, want := sb.EnvdURL(), "https://49983-sbx-123.custom.e2b.dev"; got != want {
		t.Fatalf("EnvdURL() = %q, want %q", got, want)
	}
	if got, want := sb.GetHost(8080), "8080-sbx-123.custom.e2b.dev"; got != want {
		t.Fatalf("GetHost(8080) = %q, want %q", got, want)
	}
}

func TestSandboxControlPlaneMethods(t *testing.T) {
	startedAt := time.Date(2026, 4, 23, 9, 0, 0, 0, time.UTC)
	endAt := startedAt.Add(10 * time.Minute)
	metricsStart := startedAt.Add(-time.Minute)
	metricsEnd := startedAt.Add(time.Minute)

	pauseCalls := 0
	connectCalls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("X-API-KEY"), "test-key"; got != want {
			t.Fatalf("X-API-KEY = %q, want %q", got, want)
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/sandboxes/sbx-123":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sandboxID":           "sbx-123",
				"templateID":          "tmpl-1",
				"alias":               "team/worker",
				"startedAt":           startedAt.Format(time.RFC3339),
				"endAt":               endAt.Format(time.RFC3339),
				"cpuCount":            4,
				"memoryMB":            2048,
				"diskSizeMB":          8192,
				"state":               "running",
				"envdVersion":         "0.4.4",
				"envdAccessToken":     "envd-token",
				"allowInternetAccess": true,
				"domain":              "initial.e2b.dev",
				"metadata": map[string]string{
					"project": "agent",
				},
				"network": map[string]any{
					"allowPublicTraffic": true,
					"allowOut":           []string{"1.1.1.1/32"},
					"denyOut":            []string{"0.0.0.0/0"},
					"maskRequestHost":    "masked.example.com",
				},
				"lifecycle": map[string]any{
					"autoResume": true,
					"onTimeout":  "pause",
				},
				"volumeMounts": []map[string]string{
					{"name": "data", "path": "/mnt/data"},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/sandboxes/missing":
			http.Error(w, "missing sandbox", http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/sandboxes/sbx-123/pause":
			pauseCalls++
			if pauseCalls == 1 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.Error(w, "already paused", http.StatusConflict)
		case r.Method == http.MethodPost && r.URL.Path == "/sandboxes/sbx-123/timeout":
			var payload map[string]int
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode timeout body: %v", err)
			}
			if got, want := payload["timeout"], 90; got != want {
				t.Fatalf("timeout body = %d, want %d", got, want)
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/sandboxes/sbx-123/metrics":
			if got, want := r.URL.Query().Get("start"), strconv.FormatInt(metricsStart.Unix(), 10); got != want {
				t.Fatalf("metrics start = %q, want %q", got, want)
			}
			if got, want := r.URL.Query().Get("end"), strconv.FormatInt(metricsEnd.Unix(), 10); got != want {
				t.Fatalf("metrics end = %q, want %q", got, want)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"timestamp":     startedAt.Format(time.RFC3339),
					"timestampUnix": startedAt.Unix(),
					"cpuCount":      4,
					"cpuUsedPct":    12.5,
					"memUsed":       1024,
					"memTotal":      2048,
					"diskUsed":      4096,
					"diskTotal":     8192,
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/sandboxes/sbx-123/snapshots":
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode snapshot body: %v", err)
			}
			if got, want := payload["name"], "release-1"; got != want {
				t.Fatalf("snapshot name = %q, want %q", got, want)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"snapshotID": "snap-1:latest",
				"names":      []string{"team/release-1:latest"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/snapshots":
			if got, want := r.URL.Query().Get("sandboxID"), "sbx-123"; got != want {
				t.Fatalf("snapshot sandboxID = %q, want %q", got, want)
			}
			if got, want := r.URL.Query().Get("limit"), "10"; got != want {
				t.Fatalf("snapshot limit = %q, want %q", got, want)
			}
			if got, want := r.URL.Query().Get("nextToken"), "snap-page-1"; got != want {
				t.Fatalf("snapshot nextToken = %q, want %q", got, want)
			}
			w.Header().Set("X-Next-Token", "snap-page-2")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"snapshotID": "snap-1:latest",
					"names":      []string{"team/release-1:latest"},
				},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/templates/snap-1:latest":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.Path == "/templates/missing:latest":
			http.Error(w, "missing snapshot", http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/sandboxes/sbx-123/connect":
			connectCalls++
			var payload map[string]int
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode connect body: %v", err)
			}
			if got, want := payload["timeout"], 120; got != want {
				t.Fatalf("connect timeout = %d, want %d", got, want)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sandboxID":          "sbx-123",
				"templateID":         "tmpl-1",
				"envdVersion":        "0.4.4",
				"envdAccessToken":    "envd-token-2",
				"trafficAccessToken": "traffic-token-2",
				"domain":             "resumed.e2b.dev",
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClientWithConfig(Config{
		APIKey:         "test-key",
		APIBaseURL:     server.URL,
		RequestTimeout: time.Second,
	})
	sb := client.newSandbox(sandboxRecord{
		SandboxID:       "sbx-123",
		TemplateID:      "tmpl-1",
		EnvdVersion:     "0.4.4",
		EnvdAccessToken: "envd-token",
	}, false)

	info, err := sb.GetInfo(context.Background())
	if err != nil {
		t.Fatalf("GetInfo() error = %v", err)
	}
	if got, want := info.Name, "team/worker"; got != want {
		t.Fatalf("info.Name = %q, want %q", got, want)
	}
	if info.Network == nil || info.Lifecycle == nil {
		t.Fatalf("expected network and lifecycle info, got %#v %#v", info.Network, info.Lifecycle)
	}
	if got, want := info.VolumeMounts[0].Name, "data"; got != want {
		t.Fatalf("info.VolumeMounts[0].Name = %q, want %q", got, want)
	}

	paused, err := sb.Pause(context.Background())
	if err != nil {
		t.Fatalf("Pause() first call error = %v", err)
	}
	if !paused {
		t.Fatalf("Pause() first call = false, want true")
	}

	paused, err = sb.Pause(context.Background())
	if err != nil {
		t.Fatalf("Pause() second call error = %v", err)
	}
	if paused {
		t.Fatalf("Pause() second call = true, want false")
	}

	if err := sb.SetTimeout(context.Background(), 90*time.Second); err != nil {
		t.Fatalf("SetTimeout() error = %v", err)
	}

	metrics, err := sb.GetMetrics(context.Background(), SandboxMetricsRequest{
		Start: metricsStart,
		End:   metricsEnd,
	})
	if err != nil {
		t.Fatalf("GetMetrics() error = %v", err)
	}
	if len(metrics) != 1 || metrics[0].CPUUsedPct != 12.5 {
		t.Fatalf("metrics = %#v, want one CPU sample", metrics)
	}

	snapshot, err := sb.CreateSnapshot(context.Background(), CreateSnapshotRequest{Name: "release-1"})
	if err != nil {
		t.Fatalf("CreateSnapshot() error = %v", err)
	}
	if got, want := snapshot.SnapshotID, "snap-1:latest"; got != want {
		t.Fatalf("snapshot.SnapshotID = %q, want %q", got, want)
	}

	snapshots, err := sb.ListSnapshots(context.Background(), ListSnapshotsRequest{
		Limit:     10,
		NextToken: "snap-page-1",
	})
	if err != nil {
		t.Fatalf("ListSnapshots() error = %v", err)
	}
	if got, want := snapshots.NextToken, "snap-page-2"; got != want {
		t.Fatalf("snapshots.NextToken = %q, want %q", got, want)
	}
	if len(snapshots.Snapshots) != 1 {
		t.Fatalf("len(snapshots.Snapshots) = %d, want 1", len(snapshots.Snapshots))
	}

	deleted, err := client.DeleteSnapshot(context.Background(), "snap-1:latest")
	if err != nil {
		t.Fatalf("DeleteSnapshot(existing) error = %v", err)
	}
	if !deleted {
		t.Fatalf("DeleteSnapshot(existing) = false, want true")
	}

	deleted, err = client.DeleteSnapshot(context.Background(), "missing:latest")
	if err != nil {
		t.Fatalf("DeleteSnapshot(missing) error = %v", err)
	}
	if deleted {
		t.Fatalf("DeleteSnapshot(missing) = true, want false")
	}

	if err := sb.Connect(context.Background(), 2*time.Minute); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if connectCalls != 1 {
		t.Fatalf("connectCalls = %d, want 1", connectCalls)
	}
	if got, want := sb.GetHost(8080), "8080-sbx-123.resumed.e2b.dev"; got != want {
		t.Fatalf("GetHost(8080) after connect = %q, want %q", got, want)
	}

	if _, err := client.GetSandboxInfo(context.Background(), "missing"); !errors.Is(err, ErrSandboxNotFound) {
		t.Fatalf("GetSandboxInfo(missing) error = %v, want ErrSandboxNotFound", err)
	}
}

func TestSandboxGetHostUsesSandboxDomain(t *testing.T) {
	customDomain := "custom.e2b.dev"

	client := NewClientWithConfig(Config{APIKey: "test-key"})
	withCustomDomain := client.newSandbox(sandboxRecord{
		SandboxID: "sbx-custom",
		Domain:    &customDomain,
	}, false)
	withDefaultDomain := client.newSandbox(sandboxRecord{
		SandboxID: "sbx-default",
	}, false)

	if got, want := withCustomDomain.GetHost(3000), "3000-sbx-custom.custom.e2b.dev"; got != want {
		t.Fatalf("GetHost(custom) = %q, want %q", got, want)
	}
	if got, want := withDefaultDomain.GetHost(3000), "3000-sbx-default.e2b.app"; got != want {
		t.Fatalf("GetHost(default) = %q, want %q", got, want)
	}
}
