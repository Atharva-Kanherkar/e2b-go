# e2b-go

[![Go Reference](https://pkg.go.dev/badge/github.com/Atharva-Kanherkar/e2b-go.svg)](https://pkg.go.dev/github.com/Atharva-Kanherkar/e2b-go)
[![Go Report Card](https://goreportcard.com/badge/github.com/Atharva-Kanherkar/e2b-go)](https://goreportcard.com/report/github.com/Atharva-Kanherkar/e2b-go)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](./LICENSE)

An unofficial Go SDK for [E2B](https://e2b.dev).

`e2b-go` gives Go applications access to the same core runtime workflows that
exist in the JavaScript and Python SDKs: sandbox lifecycle management,
filesystem operations, command execution, PTY sessions, snapshots, metrics, and
persistent volumes.

## Why This Exists

E2B ships official SDKs for JavaScript and Python, but there is no first-class
Go SDK today. This project fills that gap with a Go-native client aimed at
practical parity for backend and agent workloads.

This project is unofficial and is not affiliated with or endorsed by E2B. If an
official Go SDK lands upstream, this repo should ideally become unnecessary.

## Status

This project is in **beta**.

- The core sandbox, process, PTY, and volume runtime surface is implemented.
- `go test ./...` passes.
- `go test -race ./...` passes.
- Live integration coverage against a real E2B environment is still missing.

That means the SDK is in good shape for early adopters, but it should still be
treated as a fast-moving package rather than a fully hardened, long-term-stable
API.

## Requirements

- Go `1.25+`
- An E2B API key

## Installation

```bash
go get github.com/Atharva-Kanherkar/e2b-go@latest
```

## Quick Start

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
	client := e2b.NewClient("E2B_API_KEY")

	sb, err := client.CreateSandbox(ctx, e2b.CreateRequest{
		TemplateID: "base",
		Timeout:    5 * time.Minute,
	})
	if err != nil {
		panic(err)
	}
	defer sb.Destroy(ctx)

	if err := sb.WriteFile(ctx, "/workspace/hello.txt", []byte("hi")); err != nil {
		panic(err)
	}

	result, err := sb.Exec(ctx, e2b.ExecRequest{
		Command: []string{"cat", "/workspace/hello.txt"},
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(result.Stdout) // hi
}
```

## Feature Coverage

### Sandbox lifecycle

- Create and destroy sandboxes
- Connect to existing sandboxes
- Fetch sandbox info and metrics
- Pause sandboxes
- Update sandbox timeouts
- Create and list snapshots

### Filesystem

- Read and write files
- List files or full directory entries
- Stat and existence checks
- Create directories
- Rename and remove paths
- Watch directories for filesystem events
- Optional shell fallback for older or inconsistent envd behavior

### Commands and PTY

- Run foreground commands with collected stdout and stderr
- Start background commands and reconnect to running processes
- Send stdin, close stdin, and kill running processes
- Create PTY sessions, send PTY input, resize terminals, and reconnect

### Volumes

- Create, connect, list, inspect, and destroy persistent volumes
- Read, write, stat, list, mkdir, update metadata, and remove volume paths

## Package Overview

For full API documentation, see the package docs on
[pkg.go.dev](https://pkg.go.dev/github.com/Atharva-Kanherkar/e2b-go).

The public surface is organized around three main handles:

- `Client` for control-plane operations such as sandbox and volume lifecycle
- `Sandbox` for envd-backed filesystem, command, PTY, and runtime methods
- `Volume` for persistent volume content operations

## Error Handling

The package exposes sentinel errors intended for `errors.Is` checks:

- `e2b.ErrSandboxNotFound`
- `e2b.ErrFileNotFound`
- `e2b.ErrSandboxDestroyed`
- `e2b.ErrVolumeNotFound`

## Retry Policy

Transient control-plane REST failures and direct envd file HTTP reads/writes are
retried by default. Customize the policy with `Config.RetryPolicy`:

```go
client := e2b.NewClientWithConfig(e2b.Config{
	APIKey: "E2B_API_KEY",
	RetryPolicy: e2b.RetryPolicy{
		MaxAttempts:    4,
		InitialBackoff: 250 * time.Millisecond,
		MaxBackoff:     2 * time.Second,
	},
})
```

Use `RetryPolicy{MaxAttempts: 1}` to disable retries.

## CreateRequest

`CreateRequest` controls sandbox provisioning:

| Field | Purpose |
| --- | --- |
| `TemplateID` | Template to clone from. Required. |
| `Timeout` | Sandbox lifetime cap. Zero uses the server default. |
| `Metadata` | Metadata attached to the sandbox on create. |
| `EnvVars` | Environment variables injected into sandbox processes. |
| `AllowInternetAccess` | Enables unrestricted internet egress when `true`. |
| `NetworkAllowlist` | Egress allowlist used when internet access is otherwise disabled. |
| `AdditionalPackages` | Debian packages installed with `apt-get` before `CreateSandbox` returns. |
| `AllowShellFallback` | Enables shell-based fallbacks for selected filesystem operations. |

## Architecture

The SDK uses three underlying transports:

- **Control plane REST API** for sandbox lifecycle, metadata, snapshots, and team volumes
- **Envd ConnectRPC** for sandbox filesystem, command, and PTY operations
- **Volume content REST API** for persistent volume file operations using a bearer token

## Testing

The current test suite is unit-test heavy and focuses on:

- control-plane request and response normalization
- envd ConnectRPC request shapes and stream handling
- concurrency-sensitive command and watch flows
- bearer-auth volume content operations

Run the checks locally with:

```bash
go test ./...
go test -race ./...
```

## Roadmap

The highest-value remaining gaps are:

- live integration tests against a real E2B environment
- signed upload and download URL helpers
- higher-level Git convenience APIs that exist in other SDKs

## Contributing

Issues and pull requests are welcome.

If you want to extend surface area or align behavior with upstream SDKs, opening
an issue first is helpful so the API shape can stay coherent.

## Relationship To Upstream

This repository exists because there is no official Go SDK at the time of
writing. If the E2B team decides to ship or adopt one upstream, aligning this
project with that effort would be the best long-term outcome.

## License

MIT. See [LICENSE](./LICENSE).
