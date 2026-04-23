# e2b-go

An unofficial Go SDK for [E2B](https://e2b.dev) sandboxes.

E2B ships official SDKs for JavaScript and Python but has no first-class Go
client. Issue [e2b-dev/E2B#985](https://github.com/e2b-dev/E2B/issues/985) has
been open since October 2025 with no upstream movement. This repo aims to
fill that gap.

> **Status:** scaffold. No code yet — this is the starting point for
> extracting a working client into a reusable library.

## Why

Building agents in Go that need a sandbox today means either:

1. Wrapping the JS/Python SDK in a subprocess (ugly, slow, dependency-heavy),
2. Pulling in [`conneroisu/groq-go/extensions/e2b`](https://pkg.go.dev/github.com/conneroisu/groq-go/extensions/e2b)
   (buried inside an unrelated LLM SDK), or
3. Rolling your own REST + ConnectRPC client against the E2B API and envd.

This project is option (3) done once, cleanly, as a standalone module.

## Planned surface

Shape is intentionally close to the official JS / Python SDKs so porting
agent code between them is frictionless:

```go
sb, err := e2b.NewSandbox(ctx, "template-id", e2b.WithAPIKey(key))
// → lifecycle: Create, List, Destroy, Connect to existing
// → filesystem: Read, Write, List
// → process: Exec (streaming stdout/stderr), Kill
// → metadata + env + network allowlists
```

Internally: REST for the control plane (sandbox CRUD), ConnectRPC to envd
for filesystem + process (matching what the official SDKs do).

## Relationship to E2B upstream

Unofficial. Not affiliated with or endorsed by E2B. If the E2B team decides
to absorb this into `e2b-dev/E2B/packages/go-sdk`, that would be the ideal
outcome; until then, this lives here.

## Contributing

Early days — issues and PRs welcome. Ping
[@Atharva-Kanherkar](https://github.com/Atharva-Kanherkar) if you want to
coordinate on a chunk of the surface.

## License

MIT — see [LICENSE](./LICENSE).
