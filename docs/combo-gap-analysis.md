# Model Combo — Gap Analysis

Audit date: 2026-10-21. Read-only source inspection. Local unit suite passes; CPA integration, browser, and release execution remain unobserved. Production readiness: `NO`.

---

## Evidence basis

- Source inspection: `account.go`, `execute.go`, `stream.go`, `host_callbacks.go`, `main.go`, `health_persistence.go`, `management.go`, `attribution.go`, `config_test.go`, `account_test.go`, `execute_test.go`, `stream_test.go`, `host_callbacks_test.go`, `integration_test.go`
- Existing docs: `docs/final-gap-closure.md`, `docs/final-implementation-audit.md`, `docs/final-verification-report.md`, `docs/final-release-readiness.md`
- New regression tests added 2026-10-21 (this session): `TestExecuteStreamFailsClosedWhenAuthListFails`, `TestSelectResolvedAccountPriorityUsesDeclarationOrderWithThreeAccounts`

---

## Gap matrix

| # | Area | Status | Source location | Gap description | User-visible risk |
|---|---|---|---|---|---|
| 1 | CPA integration — account pools | BLOCKED | `integration_test.go:118–175` | Integration test config uses no `account_pools`, no auth fixtures, no `host.auth.list` call. Account pool routing, `AuthID` forwarding to provider, pooled retry, and stream commitment are unobserved under a real CPA host. | Silent regression: production account routing behavior is unverified. |
| 2 | Session affinity | BLOCKED | `docs/final-gap-closure.md:12` | CPA v7.3.9 `ExecutorRequest` lacks trusted session ID. Config validation rejects `session_affinity: enabled`. No enabled-affinity runtime path exists. | Feature cannot ship; any attempt to enable affinity is rejected at startup. |
| 3 | Discovered-account management | BLOCKED | `management.go:52–66` | CPA v7.3.9 `ManagementRequest` lacks caller identity and host-auth entries. No discovered-account route can be added safely. | Operators cannot view or select resolved accounts via dashboard or API. |
| 4 | `host.auth.list` wire behavior | PARTIAL | `host_callbacks.go:49–74` | Unit mocks verify wire envelope format, missing-ID, and duplicate-ID rejection. No CPA callback round-trip is observed; real CPA response shape and field ordering are untested. | Unexpected CPA response format causes fail-closed (safe) but silently disables all pooled routes. |
| 5 | `AuthID` forwarding end-to-end | PARTIAL | `execute.go:90–96`, `stream.go:98–104` | `hostRequestWithAuth` sets `AuthID`; unit tests confirm the field. No integration test exercises CPA upstream credential selection via the forwarded ID. | CPA may silently ignore or misroute `AuthID`; no observable proof. |
| 6 | Stream commitment — CPA boundary | PARTIAL | `stream.go:112–114` | `TestExecuteStreamDoesNotRetryAfterPayload` (unit) confirms no retry after `receivedPayload`. No integration test verifies that the CPA + plugin boundary does not buffer or retry after a partial stream error. | Client could receive duplicate or corrupted stream content if CPA-level retry fires after partial emit. |
| 7 | Stream fail-closed on `ListAuth` error | PASS | `stream_test.go:96–121` | Added 2026-10-21: `TestExecuteStreamFailsClosedWhenAuthListFails` verifies `executeStreamWithHost` returns 503 and makes zero `StartStream` calls when `ListAuth` errors. Previously untested; sync had equivalent coverage. | None (now covered). |
| 8 | Priority declaration order with 3+ accounts | PASS | `account_test.go:63–91` | Added 2026-10-21: `TestSelectResolvedAccountPriorityUsesDeclarationOrderWithThreeAccounts` proves selection steps through three same-priority accounts in declaration order, and returns `ok=false` once all are exhausted. | None (now covered). |
| 9 | Health persistence — CPA shutdown lifecycle | PARTIAL | `health_persistence.go` | Local deterministic ABI test (1.682s, race 3.555s) confirms persist/prune/reopen semantics. CPA plugin `Close()` call under real host shutdown is unobserved. | In-flight queue items may be lost on abrupt CPA termination. |
| 10 | Browser dashboard E2E | BLOCKED | `tests/e2e/dashboard.test.mjs` | E2E source covers configuration, health, usage, and keyboard behavior. No browser run observed: Chromium unavailable; Docker lab timed out at 600.25s downloading Go dependencies before Compose ran. | Dashboard interactive behavior is locally unproven. |
| 11 | Release artifacts | BLOCKED | `docs/final-release-readiness.md:14` | Release script started but was cancelled during image pull before cross-build. Linux `.so`, darwin arm64 `.dylib`, and Windows `.dll` all unverified. | Plugin cannot be shipped to non-darwin-amd64 platforms. |

---

## Not a gap (corrected)

**Request-scoped auth-list snapshot across multiple pools** — `authListed` short-circuits to one `ListAuth()` call per request. This is correct: `host.auth.list` returns all host auth file entries, not pool-specific entries. All pools in a single request resolve against the same snapshot. No stale-data risk exists.

---

## Closure requirements

1. **Gaps 1, 4, 5, 6** — require disposable CPA host with at least two auth file entries and `account_pools` config in the integration test or a new dedicated fixture.
2. **Gap 2** — requires CPA to supply a trusted session identifier in `ExecutorRequest`.
3. **Gap 3** — requires CPA to supply caller identity and host-auth entries in `ManagementRequest`.
4. **Gap 9** — requires observed CPA plugin lifecycle (load, shutdown) with health snapshot verification.
5. **Gap 10** — requires Chromium or headless browser and disposable loopback CPA instance.
6. **Gap 11** — requires release runner with xgo/Docker cross-build toolchain.
