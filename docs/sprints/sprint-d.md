# Sprint D: Account Routing & Quota Attribution Architecture Audit

## Overview

Sprint D audits the account routing and quota attribution architecture. **This document is an architecture audit with no code changes, no verification checks, and no code runs at drafting time.** It defines blocking prerequisites for quota accounting—stable host identifiers and provider quota facts—and documents current capability gaps in attribution and storage that prevent safe account-level quota enforcement.

---

## 1. Observed Source Capabilities

### 1.1 Usage Callback Attribution

**Attribution Marking:**
- `MarkDirect()` records direct requests (no combo match)
- `MarkRouted()` records combo-matched requests with router and provider models
- `MarkDirectRequest()` marks RequestInterceptRequest events with optional RequestID
- Markers encode request timestamp, model list, and router/provider model strings

**Attribution Matching:**
- `Match(record)` traces storedUsageRecord back to earlier markers using fingerprint matching
- Result reports attribution `Kind` (direct/routed/unattributed) and `RouterModel` for routed requests
- Markers include optional account identity field—**not persistent in stored usage records** and **not exposed to storage or dashboard**

**Observable Marks:**
- Marks can distinguish routed vs. direct requests
- Marks cannot safely prove which account was selected at execution time (no account identity persisted in usage record)
- Marks cannot correlate usage with provider quota allocation/consumption (no provider credentials or quota state received)

---

### 1.2 Usage Storage Schema v1 (No Migration / No Account Dimensions)

**storedUsageRecord Structure:**
- Sequence, RequestedAt (metadata)
- Attribution (string: "direct"/"routed"/"unattributed")
- RouterModel, Provider, ProviderModel, ProviderAlias (routing)
- ExecutorType, AccountingMode, ReasoningMode, ReasoningEffort, ServiceTier (execution)
- MaskedAPIKey (credential mask: first 2 + last 2 chars with "******" in middle)
- Failed, StatusCode, LatencyNS, TTFTNS (attempt result)
- InputTokens, OutputTokens, ReasoningTokens, CachedTokens, CacheReadTokens, CacheCreationTokens, TotalTokens (billing)

**Absent Fields:**
- No `AuthID`, `AccountKey`, `AccountName`, or `PoolName` field → account identity not stored
- No `RequestID` or `AttemptID` field → attempt sequence not traceable
- No `LogicalRequestID` or `PhysicalAttemptID` field → host request/attempt correlation not possible
- No migration path defined; schema version hardcoded to version 1

**Schema Enforcement:**
- Version check compares usageStorageSchemaVersion against stored schema version
- Version mismatch (lower) raises "unsupported migration" error with no migration code present
- Version mismatch (higher) raises forward-compatibility error

---

### 1.3 Usage Dashboard & API (Aggregate/Paginated Routes, No Account Data)

**Management Routes:**
- GET `/v0/management/plugins/model-combo/usage/dashboard` → timeseries + detail page
- GET `/v0/management/plugins/model-combo/usage/overview` → cost summary
- GET `/v0/management/plugins/model-combo/usage/groups` → paginated grouping by router_model/provider/executor_type/accounting_mode
- GET `/v0/management/plugins/model-combo/usage/requests` → paginated request detail

**Filter Dimensions (not including accounts):**
- Time range (since, until)
- Router model (optional)
- Provider (optional)
- Attribution (optional: direct/routed/unattributed)
- Result (optional: success/failed)

**No Account Dimension:**
- Dashboard groups/filters omit account-level breakdown
- Requests page includes MaskedAPIKey (credential mask) but no account or auth identity
- No paginated route to list or summarize usage by account

---

### 1.4 Health Support (Cooldown / Circuit, No Rolling Account Measures)

**Cooldown & Circuit State:**
- Per-candidate (per-group) state: cooldown expiry, closed/open/half-open circuit, reopen count
- `MarkFailure()` updates cooldown and advances circuit state
- `MarkSuccess()` resets circuit to closed
- `ReleaseProbe()` allows expired-circuit candidates to re-enter closed state without health success/failure

**Account Cooldown:**
- `MarkAccountFailure()` sets cooldown expiry in per-pool map keyed by account identity
- `filterCooledAccounts()` excludes accounts with unexpired cooldown

**No Rolling Measures:**
- Health tracks per-candidate cooldown/circuit, not rolling success rate or adaptive weight adjustment
- Account cooldown is binary: expired or active, not a sliding window

---

### 1.5 Selection Support (Priority / Weighted Round-Robin)

**Selection Strategies:**
- `selectFromGroup()` filters eligible candidates (health-passed, capability-matched, not already attempted) then applies group strategy
- Priority strategy: lowest group index, then first eligible within group
- Weighted round-robin (WRR): smooth algorithm per eligible candidates only

**Account Selection:**
- Priority pools: selection restricted to lowest available priority tier; within tier, declaration order
- Weighted pools: smooth WRR over eligible accounts

**No Affinity / Session Stickiness:**
- sessionAffinityYAML reserved but marked unsupported; CPA v7.3.9 does not expose authenticated caller session ID to ExecutorRequest

---

## 2. D4 Quota Field & Rate-Limit Classification

### 2.1 No Authoritative Quota Field

**Rate-Limit Classification:**
- HTTP 429 (TooManyRequests) classified as `failureQuotaAuth` alongside 401/402/403/404
- Fallback policy treats 429 as retryable by default

**No Quota Constant or Callback:**
- No quota_limit, quota_used, quota_reset, or quota_window field in storedUsageRecord
- No HostExecutionResponse quota metadata exposed
- No provider rate-limit headers parsed or stored (e.g., RateLimit-Limit, RateLimit-Remaining, RateLimit-Reset)
- No callback to host for quota consumption or allocation update

**429 Handling is Reactive:**
- 429 triggers cooldown + circuit marking (same as other retryable auth/model/availability errors)
- Fallback selects next candidate; no proactive quota check before attempt
- Account cooldown applies after 429 response if account was used in attempt

---

### 2.2 Rate-Limit Classification ≠ Quota

**Distinction:**
- **Rate-Limit**: HTTP 429, traffic throttling by server; recovery automatic or via backoff
- **Quota**: cumulative allocation (tokens/requests/cost) exhausted; recovery requires provider plan change or next billing period

**Current Behavior:**
- Both classify under same failure class
- Both trigger candidate cooldown and account penalization
- No mechanism to distinguish permanent quota exhaustion from transient rate-limiting
- Retry budget + exponential backoff assumes transient recovery; quota exhaustion is permanent

---

## 3. Safety & Privacy Decisions

### 3.1 Attribution Data Protection

- Dashboard never exposes OAuth tokens, refresh tokens, credential contents, file paths, email, or management secrets
- MaskedAPIKey in storedUsageRecord is credential mask (first 2 + last 2 chars) for provider identification only
- No persistent raw account identity stored in or returned from usage records

### 3.2 Account Identity Separation

- Account keys, auth names, and provider credentials stored in accountPool config; **never transmitted in execution request or usage record**
- Account identity exposed only to account selection logic and cooldown filtering within plugin runtime
- Host callbacks return pre-resolved account identity; plugin uses for selection only

### 3.3 Execution Traceability Limits

- ExecutorRequest includes Model, Headers, Body, and optional RequestID from host
- Plugin passes target model, optional auth identity (not persisted), headers, body to execution
- No logical request ID or physical attempt ID generated or tracked by plugin; relied on caller (host) to provide RequestID

---

## 4. Concrete Blocked Items & Prerequisites

### 4.1 Blocking: Host Must Emit Stable Logical & Physical IDs

**Logical Request ID** (`logicalRequestID`):
- Unique identifier assigned by host to each user request before routing to plugin
- Stable across all routing attempts and retries for same request
- Purpose: correlate usage records for same end-user request across multiple provider attempts
- **Current Status**: Plugin does not capture or propagate logical request ID to usage record
- **Impact**: Cannot link retries to original request; cannot compute effective token cost across provider fallbacks

**Physical Attempt ID** (`physicalAttemptID`):
- Unique identifier assigned by host to each provider execution attempt
- Generated after account selection and immediately before provider call
- Distinct for each retry within a logical request
- Purpose: correlate usage record with provider response metadata (tokens, timing, status)
- **Current Status**: Not provided by host; not generated by plugin
- **Impact**: Cannot trace which account/provider attempt produced a given usage record; cannot match provider quota consumption to plugin-recorded usage

**Host Callback Requirement:**
- Host must emit both IDs to plugin for storage in usage record
- Plugin must store logicalRequestID and physicalAttemptID before account quota enforcement can proceed
- No plugin-side workaround; host infrastructure change required

### 4.2 Blocking: Provider Quota Facts

**Provider Quota State** (per account):
- Quota limit (tokens/requests per period, or cost cap)
- Quota used (cumulative consumption in current period)
- Quota remaining (limit - used)
- Period window (reset time, frequency)
- **Current Status**: No provider quota metadata in execution response callback
- **Impact**: Cannot enforce pre-emptive quota gating; cannot warn when account approaches limit; cannot fairly distribute quota across accounts in pool

**Rate-Limit Headers:**
- Standard headers (RateLimit-Limit, RateLimit-Remaining, RateLimit-Reset, Retry-After)
- **Current Status**: Headers passed through response but not parsed or stored
- **Impact**: Cannot correlate 429 response to specific rate-limit window; cannot distinguish transient throttle from permanent quota exhaustion

**Host Callback Requirement:**
- Host must parse provider response headers and quota state, aggregate across provider accounts, expose to plugin via callback
- Plugin must receive quota state in execution response or via separate RPC
- Without host emission, plugin cannot implement quota-aware account selection or quota reservation

---

### 4.3 Blocking: Exact Routed Account Identity Emission

**Current Behavior:**
- Plugin marks routed requests with account identity via MarkRoutedWithAuth()
- Account identity not exposed in stored usage record
- Host does not return which account was used in execution response
- **Impact**: Cannot prove account accountability; cannot correlate usage to provider quota account; cannot audit account-level billing

**Host Callback Requirement:**
- Execution response must include exact account identity (account.id) that was used to call provider
- Plugin must store account identity (or verifiable hash) in usage record for account accountability
- Requires host to track which auth credential was passed to provider and echo it back to plugin

---

## 5. Verification

**git diff --check:** Passed after cleanup.

**Static audit:** No least-used residues in config.go, account.go, account_test.go, config_test.go.

**Go/race/vet/make check:** Not run. Sprint D contains no executable code changes; all requested strategies (least-used, rolling failure/latency, health-weighted selection) are unsafe without physicalAttemptID correlation from host ABI, deferred to D3–D6 pending infrastructure prerequisite.

### 5.1 Planned Verification Commands (Unrun)

Following commands document expected test coverage for D3–D6 implementation:

### 5.1 Attribution Matching Verification
```bash
# Build and run attribution tests (planned, not executed)
go test -run TestAttributionMarker ./...
go test -run TestAttributionMatch ./...
```
Expected: Markers correlate credentials/auth; matching finds closest marker within time window.

### 5.2 Usage Storage Schema
```bash
# Verify schema v1 fields and no account dimension (planned, not executed)
go test -run TestUsageStorageSchema ./...
go test -run TestStoredUsageRecordFields ./...
```
Expected: storedUsageRecord has no AuthID/AccountKey/LogicalRequestID/PhysicalAttemptID; schema version is 1.

### 5.3 Usage Dashboard Routes
```bash
# Verify dashboard, overview, groups, requests endpoints exist (planned, not executed)
go test -run TestUsageManagementRoutes ./...
```
Expected: All four routes return aggregate data; no account-level grouping.

### 5.4 Health & Account Cooldown
```bash
# Verify candidate cooldown and account cooldown separately managed (planned, not executed)
go test -run TestAccountCooldown ./...
go test -run TestCandidateCooldown ./...
```
Expected: Cooldown filters by account identity; independent of candidate circuit.

### 5.5 Failure Classification
```bash
# Verify 429 classified with other auth/availability errors (planned, not executed)
go test -run TestFailureClassification ./...
```
Expected: 429 and 401/402/403/404 share same failure class; no distinction for quota permanence.

### 5.6 Host Callback Absence
```bash
# Verify no quota/logicalRequestID/physicalAttemptID callback (planned, not executed)
grep -r "quota\|LogicalRequestID\|PhysicalAttemptID" ./
```
Expected: No quota or ID fields in execution response ABI.

### 5.7 Rate-Limit Header Parsing
```bash
# Verify response headers not parsed for rate-limit metadata (planned, not executed)
grep -r "RateLimit-\|Retry-After" ./ | grep -v test
```
Expected: Headers logged/passed but not extracted.

---

## 6. Summary of Gaps & Next Steps

### 6.1 Attribution Gaps
| Capability | Current | Gap |
|---|---|---|
| Mark routed vs. direct | ✓ | Cannot prove which account selected |
| Recover RouterModel from usage | ✓ | Cannot recover account identity (not stored) |
| Correlate usage to request | ✗ | No logicalRequestID in record; no host emission |
| Correlate usage to attempt | ✗ | No physicalAttemptID in record; no host generation |

### 6.2 Storage Gaps
| Capability | Current | Gap |
|---|---|---|
| Aggregate usage by provider | ✓ | No account-level breakdown |
| Store usage with safety (no secrets) | ✓ | Account identity not stored; MaskedAPIKey only |
| Migrate schema | ✗ | No migration path; schema v1 hardcoded |
| Add account dimension | ✗ | Requires schema v2 + host IDs + migration code |

## D3–D6 Unsupported Variants & Architecture Notes

### D3: Least-Used Account Selection (Unsupported)

**Current State:** No in-flight request counting per account exists.

**Unsupported Variant:** Least-used strategy reusing `accountCurrent` smooth-WRR state from account.go.
- Requires instrumentation of both terminal non-streaming and streaming execution paths
- Requires in-flight count increment immediately before host call
- Requires in-flight count decrement on every terminal completion (including streaming close)
- Current implementation omits in-flight tracking; no such instrumentation present

**Safe Future Variant:** Process-local in-flight counts.
- Increment immediately before HostExecutor call
- Decrement on all completion paths (non-streaming response, streaming close, error)
- Separate counter instance per pool, not reusing smooth-WRR state
- Requires both terminal and streaming path tests before D3 delivery

---

### D5: Rolling Failure/Latency & Health-Weighted Strategies (Unsupported)

**Current State:** Health tracks per-candidate cooldown and circuit; no rolling window metrics per account.

**Unsupported Variants:**
- Rolling failure rate (% failures in last N requests per account)
- Rolling latency (P50/P99 latency per account in time window)
- Health-weighted account selection (prefer accounts with lower failure/latency)

**Blocker:** Exact physical-attempt completion tracking required.
- Each attempt must be tagged with unique ID (physicalAttemptID)
- Attempt must record timestamp, result, latency, account used
- Plugin must correlate HostExecutionResponse back to physicalAttemptID
- Rolling window aggregation cannot proceed without exact attempt-level facts
- **Currently unavailable:** No physicalAttemptID in HostExecutionResponse ABI

---

### D6: Runtime Architecture Notes

**Concurrency:**
- Runtime holds `accountMu` sync.Mutex protecting shared account pools
- Shared maps: cooldown state per pool, selection state per strategy
- **No in-flight accounting:** No locked counter for active requests per account

**Persistence:**
- SQLite single connection (or connection pool with serialization via transaction)
- One writer at a time to usage store
- No streaming write; all records buffered and flushed
- **No distributed quota state:** No cross-process quota coordination (would require external service)

### 6.3 Quota Gaps
| Capability | Current | Gap |
|---|---|---|
| Detect 429 rate-limit | ✓ | Conflated with permanent quota exhaustion |
| Proactive quota gating | ✗ | No provider quota state available |
| Quota-aware account selection | ✗ | No per-account quota tracking |
| Reserve quota before attempt | ✗ | No quota callback; unknown availability |

### 6.4 Prerequisites for D5 (Account Quota Enforcement)
1. Host emits stable logicalRequestID and physicalAttemptID (not plugin-side; host infrastructure)
2. Host emits exact routed account identity in HostExecutionResponse (authentication/audit trail)
3. Host emits provider quota state (RateLimit headers + quota facts) in callback
4. Plugin stores logicalRequestID, physicalAttemptID, account identity in usage record v2
5. Plugin implements quota-aware account selection (pre-emptive gating)
6. Schema migration path (v1 → v2) defined and tested

---

## 7. Archive: Sprint C Handoff

Sprint C concluded visual editors for combos and account pools with raw JSON fallback. Account discovery remains unavailable (CPA v7.3.9 management lacks callback identity for auth file).

All prerequisites from this audit (sections 4–6) must be resolved in D5–D6 before quota-aware account routing can be safely deployed.
