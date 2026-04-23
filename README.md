# e2b-go

An unofficial Go SDK for [E2B](https://e2b.dev) sandboxes.

E2B ships official SDKs for JavaScript and Python but has no first-class Go
client. Issue [e2b-dev/E2B#985](https://github.com/e2b-dev/E2B/issues/985) has
been open since October 2025 with no upstream movement. This repo fills that
gap with a battle-tested client extracted from a production Go codebase
([AgentClash](https://github.com/agentclash/agentclash)).

> **Status:** initial port — compiles, unit tests pass, no integration tests
> running against a real E2B yet.

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
// Control plane
e2b.NewClient(apiKey string) *Client
e2b.NewClientWithConfig(Config) *Client
(*Client).CreateSandbox(ctx, CreateRequest) (*Sandbox, error)

// Sandbox
(*Sandbox).ID() string
(*Sandbox).TemplateID() string
(*Sandbox).EnvdURL() string
(*Sandbox).ReadFile(ctx, path) ([]byte, error)
(*Sandbox).WriteFile(ctx, path, content) error
(*Sandbox).ListFiles(ctx, prefix) ([]FileInfo, error)
(*Sandbox).Exec(ctx, ExecRequest) (ExecResult, error)
(*Sandbox).Destroy(ctx) error
```

### Error sentinels

Branch on these with `errors.Is`:

- `e2b.ErrSandboxNotFound` — control plane doesn't know the sandbox ID
  (usually it's been destroyed or timed out).
- `e2b.ErrFileNotFound` — file operation hit a missing path.
- `e2b.ErrSandboxDestroyed` — you called a method on a Sandbox whose
  `Destroy` has already run.

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

Two transports:

- **Control plane** — REST calls to `api.e2b.app/sandboxes` for create /
  destroy. See [`client.go`](./client.go).
- **Envd** — [ConnectRPC](https://connectrpc.com) to the envd agent running
  inside each sandbox, for filesystem + process operations. Uses generated
  stubs from `github.com/e2b-dev/infra/packages/shared`.

When envd responses don't come through (older envd, RPC blipped,
filesystem call returning `NOT_FOUND` misleadingly), `AllowShellFallback`
retries via `sh -c` exec for `ReadFile` / `ListFiles`. Default off.

## Status

- ✅ Sandbox create / destroy
- ✅ File read / write / list (envd RPC + shell fallback)
- ✅ Process exec with streaming stdout/stderr
- ✅ `apt-get` additional-packages install at create time
- ✅ Unit tests for wire format + error normalization
- ⏳ Integration tests against a real E2B
- ⏳ Streaming exec (caller-side stdout/stderr reader)
- ⏳ Sandbox pause / resume
- ⏳ Upload / download by stream (not in memory)

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
