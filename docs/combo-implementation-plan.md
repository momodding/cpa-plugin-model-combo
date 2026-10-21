# Model Combo — Implementation Plan

This document records what was planned, what was built, and what remains. It is retrospective: it describes the implementation as it exists in source, not as a forward plan. Each sprint maps to observed source symbols and test coverage.

---

## Scope

Account-aware model routing for CLIProxyAPI (CPA) v7.3.9 as a native Go plugin. The plugin routes requests across configurable model combos, selects upstream accounts from host-managed auth pools, persists health state, tracks attribution, and provides a management dashboard.

---

## Sprint summary

### Sprint A — Core routing and combo execution

**Built:**
- `modelComboPlugin` type (`main.go`)
- Config decode and validation: `pluginConfig`, `modelRouteYAML`, `modelComboYAML`; `decodeRouterConfig`, `normalizeAccountPools`
- `comboRuntime` with probe reservation, success/failure marking, and cooldown
- `executeWithHost`: attempt ledger, group iteration, candidate selection, sync execution, model alias rewrite
- `executeStreamWithHost`: mirrors sync; `streamModelRewriter` for SSE alias rewrite
- `newAttemptLedger`, `canAttempt`, `recordAttempt`, `markCandidateExhausted`

**Tests:** `execute_test.go` (failover, alias rewrite, group advance), `stream_test.go` (failover, cancelled context, SSE rewrite), `resilience_test.go`

### Sprint B — Fallback policy and failure classification

**Built:**
- `classifyAttemptFailure` (`resilience.go:41–145`): precedence chain — cancelled, deadline, terminal request error, context-overflow, unsupported-capability, status-based policy lookup
- `fallbackPolicy`: `fallbackOnStatus` defaults, `noFallbackOnStatus` exclusions
- `backoffDelay`, `waitForBackoff`
- `stream_only_before_first_chunk` config field: validated (true accepted, false rejected) but the commitment gate is hardcoded via `receivedPayload` bool — field has no runtime branch

**Tests:** `resilience_test.go`, `failure_test.go`, `config_test.go`

### Sprint C — Account pools and host auth discovery

**Built:**
- `accountPool`, `account`, `accountPlan`, `resolvedAccount` types (`account.go`)
- `newAccountPlan`: length-prefix pool signature
- `resolveAccountPool`: exact `AuthName`/`Provider` match; rejects disabled, unavailable, ambiguous, blank-ID, cooling entries
- `selectResolvedAccount`: lowest priority tier; declaration order for `priority` strategy; smooth WRR for `weighted-round-robin`
- `callbackModelHost.ListAuth()` (`host_callbacks.go:49`): sends `{}`, validates `files`, checks ID presence and uniqueness
- `hostRequestWithAuth` (`execute.go:156`): sets `AuthID` in `HostModelExecutionRequest`
- `filterCooledAccounts`, `excludeResolvedAccounts`
- `MarkAccountFailure` (`health.go`): keyed by `poolSignature` + `account.key + "\x00" + account.id`
- Config rejects `auth_id` and `auth_index` at parse time
- Sync and stream fail-closed when `ListAuth` errors or resolves to zero accounts

**Tests:** `account_test.go` (strict resolution, priority, WRR, cooldown, signature), `host_callbacks_test.go` (wire envelope, malformed responses), `execute_test.go` (auth-list failure, account ID pinning, bounded attempts, account retry), `stream_test.go` (stream account retry, AuthID pinning, fail-closed on ListAuth error)

**Regression tests added 2026-10-21:**
- `TestExecuteStreamFailsClosedWhenAuthListFails` (`stream_test.go:96`)
- `TestSelectResolvedAccountPriorityUsesDeclarationOrderWithThreeAccounts` (`account_test.go:63`)

### Sprint D — Attribution privacy

**Built:**
- `attributionTracker` (`attribution.go`): HMAC-SHA256 fingerprints for credential and auth ID; never stores raw values
- `logicalAccountKey` (`attribution.go:127`): returns `account.Key` only; opaque runtime `account.id` excluded so digest survives auth rotation
- `MarkRoutedWithAccount`: called before every `Execute`/`StartStream`
- `UsagePlugin.OnUsage`: fingerprints incoming `AuthID` from CPA usage records

**Tests:** `attribution_test.go`

### Sprint E — Health persistence and management API

**Built (health):**
- SQLite `combo_health_v2` table; v1→v2 migration on open (`usage_store.go`)
- `healthPersistence`: async coalescing queue; bounded drop for new distinct groups; existing groups continue coalescing; flush on `Close()` (`health_persistence.go`)
- `persistRuntimeComboHealth` (`main.go`): maps runtime keys to persisted combo/group snapshots; invoked after each health mutation

**Built (management):**
- 12 management routes registered under `/plugins/model-combo/` (`management.go:52–66`)
- Health routes: `GET /health/list`, `POST /health/reset`
- Config validation route: `POST /validate`
- Usage routes: dashboard, overview, groups, requests, prices, preferences

**Tests:** `health_persistence_test.go`, `management_test.go`, `usage_store_test.go`, `usage_management_test.go`

### Sprint F0–F2 — Weighted round-robin, ABI fixes, dashboard

**Built:**
- Smooth WRR cursor in `comboRuntime.accountCurrent` (`account.go:108–135`)
- `testComboPluginFromConfig` and YAML-driven test helpers
- `dashboard/` assets: configuration editor, usage tables/charts/pricing, health panel
- Health dashboard: `dashboard/health/panel.html`, `tab.js`, `styles.css`
- `dashboard_test.go`: assembly markers, management key auth, failure reporting

**Blocked:**
- Discovered-account management route: `ManagementRequest` lacks required CPA fields
- Session affinity: `ExecutorRequest` lacks trusted session ID; config validation rejects enablement

### Sprint F3 — Gap closure (current state)

**Local coverage:** All F0–F2 items with local proof pass. See `docs/final-gap-closure.md`.

**Remaining blocked items:**
- CPA integration test with account pools and auth fixtures
- Browser E2E (Chromium + Docker lab)
- Release artifacts (cross-build)
- Discovered-account management (requires CPA protocol change)
- Enabled session affinity (requires CPA protocol change)

---

## What was not built

| Omission | Reason |
|---|---|
| Discovered-account management API | CPA v7.3.9 `ManagementRequest` lacks identity and host-auth entries |
| Enabled session affinity routing | CPA v7.3.9 `ExecutorRequest` lacks trusted session ID |
| Live health-list redaction of raw credentials | Health list returns structural snapshots; no raw auth credential surfaces |
| Per-request auth re-fetch | One `ListAuth` per request is intentional; the host list is stable per request |

---

## Acceptance criteria status

| Criterion | Status |
|---|---|
| Strict canonical account pools, exact auth name/provider, no persisted AuthID | PASS (local) |
| Fail-closed discovery via `host.auth.list` | PASS (local) |
| Exact AuthID in sync and stream | PASS (local) |
| Priority/SWRR selection | PASS (local) |
| Bounded account attempts | PASS (local) |
| Health/usage privacy | PASS (local) |
| Dashboard support | PASS (local) / BLOCKED (browser) |
| CPA integration | BLOCKED |
| Session affinity | BLOCKED (CPA v7.3.9 constraint) |
| Discovered-account management | BLOCKED (CPA v7.3.9 constraint) |

Production readiness: `NO`. See `docs/final-release-readiness.md`.
