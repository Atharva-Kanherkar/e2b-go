# main — Test Contract

## Functional Behavior
Fix the concrete `e2b-go` issues identified in the repo review without widening the public API surface unnecessarily.

- `CreateSandbox` must serialize the internet-access request field as `allow_internet_access`.
- The create request payload must never include the incorrect `allowInternetAccess` key.
- Sandbox/create timeout values derived from `time.Duration` must round up to whole seconds instead of rounding to nearest second.
- `Config.RequestTimeout` must apply to control-plane HTTP calls only; envd filesystem/process clients must remain safe for long-lived streaming operations.
- Envd file and process operations must stop hardcoding `root`.
- For envd versions older than `0.4.0`, file HTTP calls must use legacy username `user`, and RPC/exec calls must send legacy basic auth for `user`.
- For envd versions `0.4.0` and newer, file HTTP calls must omit the legacy username query param, and RPC/exec calls must omit the legacy basic auth header.
- `Destroy` must only make a sandbox permanently closed after a successful delete or confirmed not-found response.
- If a destroy call fails transiently, the sandbox handle must remain retryable and a later destroy attempt must still reach the control plane.
- If `AdditionalPackages` installation fails during `CreateSandbox`, cleanup must use a fresh timeout-bounded context instead of reusing a potentially canceled request context.

## Unit Tests
- `TestCreateSandboxRequestUsesSnakeCaseInternetField` — marshaled create payload uses `allow_internet_access`.
- `TestDurationToWholeSecondsRoundsUp` — create timeout conversion ceilings fractional seconds.
- `TestNewAPIClientSeparatesControlAndEnvdTimeouts` — control-plane client keeps configured timeout while envd client has no client-wide timeout.
- `TestLegacySandboxUsernameForFileHTTP` — envd `<0.4.0` uses legacy `user`; envd `>=0.4.0` omits username.
- `TestLegacySandboxAuthHeader` — envd `<0.4.0` sends legacy auth; envd `>=0.4.0` omits it.
- `TestDestroyFailedRequestIsRetryable` — failed destroy does not permanently close the handle.

## Integration / Functional Tests
- `TestDestroyFailedRequestIsRetryable` uses `httptest.Server` to verify a transient DELETE failure is retried on a second `Destroy` call.
- Request-shape tests must inspect the exact JSON/query/header values emitted by the SDK helpers.

## Smoke Tests
- `go test ./...`

## E2E Tests
N/A — this change is limited to SDK request shaping, timeout plumbing, and retry behavior; no live E2B environment is available in this workspace.

## Manual / cURL Tests
Manual verification is repository-local rather than HTTP service based.

```bash
go test ./...
```

```bash
go test ./... -run 'TestCreateSandboxRequestUsesSnakeCaseInternetField|TestDestroyFailedRequestIsRetryable'
```
