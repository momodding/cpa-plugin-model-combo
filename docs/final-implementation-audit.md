# Final Implementation Audit

Status: open. This matrix is evidence ledger, not a claim of release readiness. `PASS` requires observed executable evidence at final integration boundary; static inspection and existing unit tests are `PARTIAL` until then.

| Phase / definition of done | Production evidence | Permanent evidence | Executable final check | Initial status | Gap / next action |
|---|---|---|---|---|---|
| 1. Identity, config normalization, legacy route migration | `config.go`, `router.go`, `combo.go` compile legacy routes and combos; `main.go` registers plugin | `config_test.go`, `router_test.go`, `workflow_test.go` | `go test ./... -count=1` | PASS (local) | CPA legacy coexistence smoke remains external. |
| 2. Immutable combo plans and group selectors | Account pool signatures participate in immutable candidate plans; `comboRuntime.Clone` preserves only unchanged pool WRR state | `TestGroupSignatureChangesWithAccountPool`, `TestComboRuntimeClonePreservesOnlyUnchangedAccountPools`, selector/health suites | Standard and race suites | PASS (local) | Runtime state resets on process restart; no durable affinity designed. |
| 3. Failure policy, circuit behavior, capability filtering | Sync and stream execution exhaust distinct resolved accounts before candidate fallback; health mutation deferred until pool exhaustion | `TestExecuteWithHostRetriesAnotherPooledAccount`, `TestExecuteStreamRetriesAnotherPooledAccount`, resilience/capability suites | Standard and race suites | PASS (local) | CPA end-to-end failure injection remains external. |
| 4. Host protocol forwarding and stream commitment | `host_callbacks.go`, `execute.go`, `stream.go` use metadata-only `host.auth.list` and exact opaque `AuthID` forwarding | `TestCallbackModelHostListAuthUsesEmptyWirePayload`, `TestCallbackModelHostRoutesEveryCallbackThroughWireSeam`, sync/stream pooled retry tests | Standard and race suites | PASS (local) | CPA sync and stream smoke remain external. |
| 5. Usage attribution and durable storage | Attribution stores only HMAC fingerprints; account-aware markers constrain opaque `AuthID` without persisting it. Usage SQLite schema remains `1`. | `TestAttributionTrackerRequiresMatchingOpaqueAuthID`, attribution/usage suites | Standard and race suites | PARTIAL | No account-routing affinity or health persistence; no schema migration required for runtime-only state. |
| 6. Account configuration and management UI | Config supports `account_pools`; dashboard loads, validates, and PATCHes raw object with combos and legacy routes. | `TestValidateModelComboManagementConfigAcceptsAccountPools` | JS syntax check, standard/race suites | PASS (local) | CPA configuration endpoint round-trip remains external. |
| 7. Session affinity | No session-affinity feature is implemented or persisted. Account smooth WRR state is runtime-scoped in `comboRuntime.accountCurrent`. | Clone isolation and account-selection tests | Source audit plus standard/race suites | PASS (scope) | Deliberate absence, not persistence failure. |
| 8. Documentation, integration, release, platform/manual audit | This ledger records local evidence. | `integration_test.go` covers existing CPA combo smoke. | See explicit blocked commands below. | BLOCKED | Required runner environments are unavailable. Production readiness: **NO**. |

## Observed constraints

- Account discovery is metadata-only through `host.auth.list`; no credential files, OAuth payloads, `AuthID`, or `AuthIndex` enter configuration or diagnostics.
- Account smooth WRR state is in-memory only: `comboRuntime.accountCurrent`, guarded by `accountMu`; no session-affinity map or health flush persists it.
- Usage SQLite schema remains `1` (`usage_store.go`); account-aware attribution stores HMAC fingerprints, not raw opaque IDs.
- Management validation accepts `enabled`, `routes`, `combos`, and `account_pools`; dashboard PATCH and validation requests carry same account pool object.
- Account selection must be implemented without credential reads: only metadata from host callback may enter routing decisions.

## Local verification observed

- `gofmt -w account.go account_test.go attribution.go attribution_test.go combo.go config.go execute.go execute_test.go health.go management.go management_test.go selector.go stream.go stream_test.go` then `make fmt-check` — PASS.
- `go test ./... -count=1` — PASS, `ok github.com/momodding/cpa-model-combo 10.715s`.
- `go test -race ./... -count=1` — PASS, `ok github.com/momodding/cpa-model-combo 47.852s`.
- `go vet ./...` — PASS.
- `node --check dashboard/configuration/tab.js` and `node --check dashboard/shared.js` — PASS.
- Both Go test runs emitted only known macOS target-SDK linker warnings.

## External verification blocked

| Blocked command or activity | Required runner / environment | Reason not observed |
|---|---|---|
| `CPA_SOURCE=../CLIProxyAPI go test -tags=integration ./... -count=1` | Checkout of CPA at pinned revision `61fdfc341b96178a8dcb53f2efc46cbc341d267c`, CPA build prerequisites, and local process/port permission | No CPA source checkout or integration runner is available. |
| `make build` followed by CPA native-plugin load, sync request, SSE request, and management `GET`/`PATCH` configuration round-trip | CPA host configured with compiled native library plus metadata-only auth fixtures for at least two accounts | No CPA host or safe auth fixture environment is available. |
| `bash .github/xgo/release.sh ghcr.io/techknowlogick/xgo@sha256:b00957d8fec512c4748a5fafe17197be1d8c0bf704b271fc4aa128f5ddf40414 github.com/momodding/cpa-model-combo model-combo <version>` | Docker daemon, pinned xgo image, clean tagged source tree, Linux/macOS/Windows cross-toolchains | No Docker/release runner is available; release must not be run from this workspace. |
| Browser/manual dashboard and privacy verification | Running CPA Management API, browser, test accounts, and redacted observable logs/storage | No CPA/browser/manual test environment is available. README links `docs/combo-manual-verification.md`, but file is absent. |

## Final gate

Do not close this audit until all plan commands that are runnable locally have observed output recorded here, external runner/Pi work is explicitly `BLOCKED` with command and runner requirement, and production readiness remains `NO` if any mandatory criterion fails or is blocked.
