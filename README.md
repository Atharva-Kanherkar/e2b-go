# e2b-go

An unofficial Go SDK for [E2B](https://e2b.dev) sandboxes.

E2B ships official SDKs for JavaScript and Python but has no first-class Go
client. Issue [e2b-dev/E2B#985](https://github.com/e2b-dev/E2B/issues/985) has
been open since October 2025 with no upstream movement. This repo fills that
gap with a battle-tested client extracted from a production Go codebase
([AgentClash](https://github.com/agentclash/agentclash)).

> **Status:** parity-focused beta — unit tests pass, `go test -race ./...`
> passes, and the core sandbox + volume runtime surface is implemented. Live
> integration tests against a real E2B environment are still missing.

## Install

```bash
go get github.com/Atharva-Kanherkar/e2b-go
```

## Quick start

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/Atharva-Kanherkar/e2b-go"
)

func main() {
    ctx := context.Background()
    c := e2b.NewClient("E2B_API_KEY")

    sb, err := c.CreateSandbox(ctx, e2b.CreateRequest{
        TemplateID: "base",
        Timeout:    5 * time.Minute,
    })
    if err != nil { panic(err) }
    defer sb.Destroy(ctx)

    if err := sb.WriteFile(ctx, "/workspace/hello.txt", []byte("hi")); err != nil {
        panic(err)
    }

    out, err := sb.Exec(ctx, e2b.ExecRequest{
        Command: []string{"cat", "/workspace/hello.txt"},
    })
    if err != nil { panic(err) }
    fmt.Println(out.Stdout) // "hi"
}
```

## Surface

```go
// Control plane / lifecycle
e2b.NewClient(apiKey string) *Client
e2b.NewClientWithConfig(Config) *Client
(*Client).CreateSandbox(ctx, CreateRequest) (*Sandbox, error)
(*Client).ListSandboxes(ctx, ListSandboxesRequest) (ListSandboxesResponse, error)
(*Client).GetSandboxInfo(ctx, sandboxID) (SandboxInfo, error)
(*Client).ConnectSandbox(ctx, ConnectSandboxRequest) (*Sandbox, error)
(*Client).ListSnapshots(ctx, ListSnapshotsRequest) (ListSnapshotsResponse, error)
(*Client).DeleteSnapshot(ctx, snapshotID) (bool, error)
(*Client).CreateVolume(ctx, name) (*Volume, error)
(*Client).ConnectVolume(ctx, volumeID) (*Volume, error)
(*Client).GetVolumeInfo(ctx, volumeID) (VolumeAndToken, error)
(*Client).ListVolumes(ctx) ([]VolumeInfo, error)
(*Client).DestroyVolume(ctx, volumeID) (bool, error)

// Sandbox lifecycle / metadata
(*Sandbox).ID() string
(*Sandbox).TemplateID() string
(*Sandbox).EnvdURL() string
(*Sandbox).GetHost(port int) string
(*Sandbox).Connect(ctx, timeout) error
(*Sandbox).GetInfo(ctx) (SandboxInfo, error)
(*Sandbox).Pause(ctx) (bool, error)
(*Sandbox).SetTimeout(ctx, timeout) error
(*Sandbox).GetMetrics(ctx, SandboxMetricsRequest) ([]SandboxMetric, error)
(*Sandbox).CreateSnapshot(ctx, CreateSnapshotRequest) (SnapshotInfo, error)
(*Sandbox).ListSnapshots(ctx, ListSnapshotsRequest) (ListSnapshotsResponse, error)
(*Sandbox).Kill(ctx) error

// Sandbox filesystem
(*Sandbox).ReadFile(ctx, path) ([]byte, error)
(*Sandbox).WriteFile(ctx, path, content) error
(*Sandbox).ListFiles(ctx, prefix) ([]FileInfo, error)
(*Sandbox).ListDir(ctx, path, depth) ([]EntryInfo, error)
(*Sandbox).Stat(ctx, path) (EntryInfo, error)
(*Sandbox).Exists(ctx, path) (bool, error)
(*Sandbox).MakeDir(ctx, path) (bool, error)
(*Sandbox).Rename(ctx, oldPath, newPath) (EntryInfo, error)
(*Sandbox).Remove(ctx, path) error
(*Sandbox).WatchDir(ctx, path, WatchOptions, onEvent) (*WatchHandle, error)

// Commands / PTY
(*Sandbox).Exec(ctx, ExecRequest) (ExecResult, error)
(*Sandbox).ListProcesses(ctx) ([]ProcessInfo, error)
(*Sandbox).StartCommand(ctx, CommandStartRequest) (*CommandHandle, error)
(*Sandbox).ConnectProcess(ctx, pid, CommandConnectOptions) (*CommandHandle, error)
(*Sandbox).CreatePTY(ctx, PTYStartRequest) (*CommandHandle, error)
(*Sandbox).ConnectPTY(ctx, pid, PTYConnectOptions) (*CommandHandle, error)
(*Sandbox).Destroy(ctx) error

// Volumes
(*Volume).ID() string
(*Volume).Name() string
(*Volume).Destroy(ctx) (bool, error)
(*Volume).List(ctx, path, depth) ([]VolumeEntryInfo, error)
(*Volume).MakeDir(ctx, path, VolumeWriteOptions) (VolumeEntryInfo, error)
(*Volume).Stat(ctx, path) (VolumeEntryInfo, error)
(*Volume).Exists(ctx, path) (bool, error)
(*Volume).UpdateMetadata(ctx, path, VolumeMetadataOptions) (VolumeEntryInfo, error)
(*Volume).ReadFile(ctx, path) ([]byte, error)
(*Volume).WriteFile(ctx, path, content, VolumeWriteOptions) (VolumeEntryInfo, error)
(*Volume).Remove(ctx, path) error
```

### Error sentinels

Branch on these with `errors.Is`:

- `e2b.ErrSandboxNotFound` — control plane doesn't know the sandbox ID
  (usually it's been destroyed or timed out).
- `e2b.ErrFileNotFound` — file operation hit a missing path.
- `e2b.ErrSandboxDestroyed` — you called a method on a Sandbox whose
  `Destroy` has already run.
- `e2b.ErrVolumeNotFound` — control plane doesn't know the volume ID.

## What's in `CreateRequest`

| Field                 | Purpose                                                                          |
|-----------------------|----------------------------------------------------------------------------------|
| `TemplateID`          | Which E2B template the sandbox is cloned from. **Required.**                     |
| `Timeout`             | Lifetime cap. Zero = server default.                                             |
| `Metadata`            | Attached to the sandbox; visible on the control plane.                           |
| `EnvVars`             | Injected into every process in the sandbox.                                      |
| `AllowInternetAccess` | Enable general internet egress. Defaults to `false` (network-isolated).          |
| `NetworkAllowlist`    | Egress allowlist when `AllowInternetAccess=false`.                               |
| `AdditionalPackages`  | Installed via `apt-get` at startup.                                              |
| `AllowShellFallback`  | Use `cat`/`find` fallbacks when envd RPC fails (e.g. on older envd versions).    |

## Architecture

Three transports:

- **Control plane** — REST calls to `api.e2b.app/sandboxes` for create /
  destroy, plus sandbox metadata, snapshots, and team volumes.
- **Envd** — [ConnectRPC](https://connectrpc.com) to the envd agent running
  inside each sandbox, for filesystem + process operations. Uses generated
  stubs from `github.com/e2b-dev/infra/packages/shared`.
- **Volume content API** — REST calls to `api.e2b.app/volumecontent/...`
  authenticated with the per-volume bearer token returned by the control plane.

When envd responses don't come through (older envd, RPC blipped,
filesystem call returning `NOT_FOUND` misleadingly), `AllowShellFallback`
retries via `sh -c` exec for `ReadFile` / `ListFiles`. Default off.

## Status

- ✅ Sandbox create / destroy / connect / info / pause / timeout / metrics / snapshots
- ✅ File read / write / list / stat / exists / mkdir / rename / remove / watch
- ✅ Background commands, process reconnect/list/stdin/kill, and PTY sessions
- ✅ Persistent volume create / connect / list / destroy and content operations
- ✅ `apt-get` additional-packages install at create time
- ✅ Unit tests for control-plane wire format, ConnectRPC streams, and error normalization
- ✅ `go test -race ./...`
- ⏳ Integration tests against a real E2B
- ⏳ Signed upload / download URL helpers
- ⏳ Git convenience wrappers from the JS / Python SDKs

## Relationship to E2B upstream

Unofficial. Not affiliated with or endorsed by E2B. If the E2B team wants
to absorb this into `e2b-dev/E2B/packages/go-sdk`, that would be the
ideal outcome; until then, this lives here.

## Contributing

Issues and PRs welcome. Ping
[@Atharva-Kanherkar](https://github.com/Atharva-Kanherkar) to coordinate on
surface expansions.

## License

MIT — see [LICENSE](./LICENSE).
