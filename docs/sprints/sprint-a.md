# Sprint A — Account / CPA Protocol

## Executable plan
1. Establish CPA v7.3.9 callback contract from pinned SDK and host source.
2. Decode `host.auth.list` wire envelope exactly and reject unsafe records.
3. Pin selected configured credentials by exact `AuthID`.
4. Verify bounded account, model, and group fallback for sync and streams.
5. Run focused unit tests and safe disposable CPA-host integration coverage.

## Baseline / SDK contract
- Module pins `github.com/router-for-me/CLIProxyAPI/v7 v7.3.9`.
- CPA `sdk/pluginapi/types.go:680-735` defines each returned `HostAuthFileEntry`, including `ID`, `Name`, `Provider`, availability, and retry fields.
- CPA `internal/pluginhost/host_callbacks.go` returns `host.auth.list` as an object result with `files`; CPA `internal/pluginhost/auth_callbacks_test.go:40-95` calls the real host auth manager and decodes that object. The protocol payload is therefore `{ "files": [...] }`, not a bare array.
- Root cause for A1: callback client previously decoded a bare entry array, while CPA v7.3.9 returns an object envelope. That mismatch could discard or misinterpret host credentials.

## Concurrent half-open worker findings

### Worker A
CPA contract source evidence established above. Dependency test-only `internal/pluginhost` helpers cannot be imported by this module because Go `internal` visibility forbids it.

### Worker B
No separate worker result available. Routing implementation and repository tests inspected directly.

## Implementation matrix

| Item | Files | Status | Evidence |
| --- | --- | --- | --- |
| A1 host auth envelope / fail closed | `host_callbacks.go`, `host_callbacks_test.go` | Pass | `ListAuth` decodes required `files` object, rejects absent/null/non-array/malformed values, blank IDs, and duplicate IDs. Wire-shaped callback unit tests cover valid envelope and each rejection. |
| A3 AuthID pinning / allowlist | `account.go`, `execute.go`, `stream.go`, `account_test.go`, `execute_test.go`, `stream_test.go` | Pass | Resolver admits only exact configured `AuthName` and `Provider`; ambiguity, empty ID, disabled/unavailable/status-disabled, and future retry time are excluded. Sync/stream requests use resolved `ID`, never `AuthIndex`. |
| A4 sync fallback / bounded retry | `execute.go`, `execute_test.go` | Pass | Account candidates are unique per candidate pass, bounded by resolved accounts and attempt ledger; retryable first account failure selects second account, then normal target/group exhaustion remains ordered. |
| A5 stream fallback / no committed retry | `stream.go`, `stream_test.go` | Pass | Stream retries before payload; once `receivedPayload` is true it returns without trying another target. |

## Test evidence
- `go test ./... -run 'TestCallbackModelHostListAuth' -count=1` — pass (`ok github.com/momodding/cpa-model-combo`, 7.659s; macOS linker emitted non-fatal deployment-target warning).
- `go test ./... -run 'Test(ExecuteWithHostPinsResolvedAccountID|ExecuteWithHostRetriesAnotherPooledAccount|ResolveAccountPoolFailsClosed|ResolveAccountPoolMatchesExactMetadata|ExecuteStreamRetriesAnotherPooledAccount|ExecuteStreamFailsOverBeforePayload|ExecuteStreamDoesNotRetryAfterPayload)$' -count=1` — pass (`ok github.com/momodding/cpa-model-combo`, 5.794s; same non-fatal linker warning).
- Covered assertions: exact opaque `AuthID`; second pooled account after retryable sync/stream failure; strict case-sensitive metadata matching and ambiguity rejection; stream target fallback before payload; one stream start and one emitted payload after late post-payload `429`.

## Disposable CPA integration
- Attempted command against pinned local CPA source: `CPA_SOURCE=/Users/momodding/go/pkg/mod/github.com/router-for-me/!c!l!i!proxy!a!p!i/v7@v7.3.9 go test -tags=integration ./... -run '^TestModelComboWithCLIProxyAPI$' -count=1`.
- Fixture is repository `integration_test.go`: it starts CPA locally with a temporary configuration, loads native plugin, uses an `httptest` upstream provider, and stops process via test cleanup. It is actual CPA-host coverage, not a callback mock.
- Result: blocked. Command produced only the known non-fatal macOS linker deployment-target warning, then exceeded its 300-second deadline with no test result or CPA startup diagnostic. Do not treat this run as integration proof.

## Blockers
- No implementation blocker. CPA dependency test helpers are not reusable outside dependency due to Go `internal` rules. Disposable CPA integration remains unverified because its local test command timed out after 300 seconds without actionable diagnostics.

## Final verification — 2026-10-16

| Check | Status | Actual result |
| --- | --- | --- |
| `go test ./... -count=1` | PASS | `ok github.com/momodding/cpa-model-combo 24.088s`; wall time 43.09s. |
| `go test -race ./... -count=1` | PASS | `ok github.com/momodding/cpa-model-combo 136.220s`; wall time 218.96s. |
| `go vet ./...` | PASS | No output; wall time 8.37s. |
| `make check` | PASS | `fmt-check` passed after `gofmt -w host_callbacks_test.go`; tests passed in 22.977s, race tests in 141.048s, and `go vet` emitted no diagnostics; wall time 221.93s. |
| `make build` | PASS | Built `dist/model-combo.dylib`; wall time 7.18s. |

All commands emitting Go link output warned that generated `go.o` targets macOS 13.0 while linked target is 12.0. This did not fail tests or build.

### A2 half-open evidence

Merged from `sprint-a-half-open.md`: expired open circuits are selectable only when probe occupancy is clear. Selector chooses strategy winner before atomically reserving it under runtime lock, preventing concurrent duplicate probes and reservation of non-selected candidates. Concurrent selector regressions cover one active probe and selected-only occupancy. Existing `markSuccess`, `markFailure`, and `releaseProbe` cover successful completion, terminal failure/cancellation, recoverable retry, and stream terminal outcomes. Focused selector verification passed: `go test ./... -run 'TestComboSelector.*HalfOpen' -count=1` in 5.447s.

### Final matrix

| Sprint A area | Status |
| --- | --- |
| A1 auth callback envelope | PASS |
| A2 half-open probe reservation and release | PASS |
| Full tests, race tests, vet, build | PASS |
| Repository formatting gate | PASS |

### Residual risks and Sprint B readiness

Sprint B is ready. Residual non-functional warning: local Go linker deployment-target mismatch (macOS 13.0 object linked for 12.0).
