# codex/sdk-parity — Test Contract

## Functional Behavior
This branch expands `e2b-go` from the current create/read/write/list/exec/destroy core into parity with the upstream SDKs' core sandbox and volume runtime APIs.

- `Client` can list sandboxes, inspect a sandbox by ID, reconnect to an existing sandbox, update a sandbox timeout, pause a sandbox, fetch sandbox metrics, create snapshots, list snapshots, and delete snapshots.
- `Sandbox` exposes instance methods for reconnecting, inspecting info, updating timeout, pausing, listing snapshots, creating snapshots, deleting itself, and deriving externally reachable hostnames for sandbox ports.
- Filesystem APIs cover directory creation, rename/move, delete, existence checks, stat/info lookup, and directory watching in addition to the existing read/write/list methods.
- Command APIs cover listing running processes, starting background commands, connecting to running commands, sending stdin, closing stdin, killing commands, and waiting for exit with streamed stdout/stderr capture.
- PTY APIs cover creating a shell-backed PTY session, connecting to an existing PTY, sending input, resizing the terminal, killing the PTY, and consuming PTY output through a handle.
- Volume APIs cover create/connect/get-info/list/destroy and content operations for list, mkdir, stat, exists, metadata update, read, write, and remove.
- Existing behavior remains intact:
  - create still rounds timeout upward to whole seconds
  - modern envd sandboxes do not force legacy usernames/auth headers
  - failed `Destroy` calls remain retryable
  - `ErrSandboxNotFound`, `ErrFileNotFound`, and `ErrSandboxDestroyed` keep their existing semantics
- Scope assumption for this branch: parity targets the core runtime APIs that exist in upstream JS/Python SDKs today. Higher-level helpers such as Git convenience wrappers, MCP token helpers, and signed upload/download URL helpers are not part of this branch unless they are needed to support the runtime APIs above.

## Unit Tests
- `TestClientListSandboxesParsesRecords` — list response is normalized into exported sandbox info values.
- `TestClientConnectSandboxBuildsOperationalHandle` — connect/info response can be turned into a working `Sandbox`.
- `TestSandboxControlPlaneMethods` — sandbox instance methods call the right REST endpoints and map not-found/auth failures correctly.
- `TestSandboxGetHostUsesSandboxDomain` — host generation uses sandbox ID, requested port, and custom domain when present.
- `TestFilesystemRPCMethods` — mkdir/move/remove/stat/list requests use the expected envd RPC payloads and header behavior.
- `TestSandboxExistsHandlesNotFound` — exists returns `false` on RPC not-found and propagates other errors.
- `TestWatchHandleLifecycle` — watch streams require an initial start event, deliver filesystem events, and stop cleanly.
- `TestCommandStartHandleCollectsOutputAndExit` — background commands produce a handle that captures stdout/stderr, exit code, and metadata.
- `TestCommandListConnectInputAndKill` — process list/connect/send-stdin/close-stdin/send-signal flows use the expected RPC selectors.
- `TestPTYCreateConnectResizeAndInput` — PTY requests set bash defaults, dimensions, PTY payloads, and signal behavior correctly.
- `TestVolumeControlPlaneMethods` — create/connect/get-info/list/destroy use the correct control-plane endpoints and auth.
- `TestVolumeContentMethods` — volume content methods hit the right path/query/body shapes and map not-found errors correctly.

## Integration / Functional Tests
- `go test ./...` passes with the new parity APIs and unit tests.
- `go test -race ./...` passes to guard the new background handle synchronization and watch/stream state.

## Smoke Tests
- Existing quick-start flow still compiles conceptually: create sandbox, write file, exec command, destroy sandbox.
- New surface compiles conceptually for:
  - reconnecting to an existing sandbox
  - starting a background command and waiting on its handle
  - creating a PTY session
  - creating a volume and reading/writing a file

## E2E Tests
N/A — this repo still does not have live E2B integration coverage in this branch. The final review must call that out explicitly if it remains true.

## Manual / cURL Tests
N/A for this branch — verification is via unit tests and static contract review because no live E2B credentials or test environment are locked into the repo.
