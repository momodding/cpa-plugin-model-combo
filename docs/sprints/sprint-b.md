# Sprint B: Executor Session Identity & Streaming

## B1: CPA v7.3.9 ExecutorRequest Session Identity

**Finding:** CPA v7.3.9 ExecutorRequest has no reliable caller session ID.

- `session_affinity.enabled` config field default: `false`
- Enabling rejects with exact error:
  ```
  session_affinity.enabled requires a reliable caller session ID, but CPA v7.3.9 ExecutorRequest lacks one
  ```
- No upstream ExecutorRequest session_id field

---

## B2: Runtime Cooldown under Account Pool Signature

**Key Structure:**
- Configured logical `account.key` + exact opaque `AuthID`
- Joined: `account.key\x00AuthID`
- Runtime-only; exists under account pool signature lifetime
- Bounds: 100ms–5min

**Eviction:**
- `filterCooledAccounts()` scans selected pool entries by expiry
- Removes expired entries even when resolved list empty
- Deletes empty selected pool map after sweep

---

## B3: AuthName + Provider Resolution

**Matching:**
- Exact `AuthName` and `Provider` match required
- Rejects: ambiguous match, no match, blank ID, disabled auth, unavailable, retry pending

---

## B4: Stream Lifecycle & Context Binding

**StartStream:**
- Validates nonempty request `StreamID`
- Spawns goroutine
- Deferred recovery: panic → error message `stream orchestration panic: %v`, always calls `ClosePluginStream`
- `handleModelComboABIMethod` now passes its `ctx` through `startStream` to `executeStreamWithHost`
- `executeStreamWithHost` checks pre-cancelled context: returns `context.Canceled` before host work

**ForwardStreamAttemptWithAuth:**
- Deferred `CloseStream` after nonempty upstream stream ID

**Context & Cancellation Limitation:**
- Context propagated when supplied by dispatch layer
- Public CPA export (`ModelComboPluginCall`) invokes dispatcher with `context.Background()`, so CPA v7.3.9 exposes no caller cancellation/deadline linkage
- Live cancellation remains unavailable through this ABI

**Payload Commit:**
- First payload commit returns `receivedPayload`
- Blocks retry/failover paths once received
---

## B5: Request-Local Retry Policy

**Ledger & Bounds:**
- Configured request-local ledger enforces max attempts/deadline
- Bounded `Retry-After` header
- Jittered capped backoff applied

**Half-Open Reservation:**
- Reserve only selected candidate
- Guarded under runtime lock

**Status Classification:**
- Context overflow & unsupported capability: retryable
- Quota/auth (401, 402, 403, 404, 429): retry where fallback policy permits; no penalty applied
- Explicit cancellation/deadline: terminal

---

## Verification

### Passing Tests
```text
go test ./... -count=1                          PASS 27.900s
go test ./... -run 'TestAccountCooldownExcludesExactLogicalKey|TestAccountCooldownExpiresWithDeterministicClock|TestExecuteStream|TestStream' -count=1
                                                    PASS 5.662s
go test ./... -run 'TestExecuteStream(HonorsCancelledContext|FailsOverWhenStartReturnsStatusWithError|DoesNotRetryAfterPayload)' -count=1
                                                    PASS 5.731s
go test -race ./... -count=1                    PASS 134.807s
go vet ./...                                    PASS
make fmt-check                                  PASS
node --check dashboard/configuration/tab.js     PASS
make check                                      PASS (go test 21.584s; race 122.646s; vet)
make build                                      PASS
```

`make build` emitted non-blocking macOS linker warning: object targets 13.0, link target 12.0.

---

## Blockers / Out of Scope

- No disposable live CPA v7.3.9 host/Pi platform; integration unavailable
- Browser E2E variables/local CPA/Chrome/mutation flag absent; skipped
- Release/platform/manual not run
- No deploy, commit, or push

---

## Summary

Session ID absence accepted as CPA v7.3.9 constraint. Cooldown keyed by configured account + opaque auth ID, swept from pool on expiry. Resolution exact-matched; rejected on mismatch/unavailability. Stream startup validates StreamID before spawn; panic recovery closes stream. Context propagated when supplied by dispatch; public CPA ABI entrypoint supplies `context.Background()`, so live caller cancellation/deadline linkage unavailable. Retry bounded per request with policy-gated classification. Core gates verified; external platform unavailable.
