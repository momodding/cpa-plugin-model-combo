# Model Combo — Architecture

This document describes the production architecture as built and observable in source. It does not claim CPA integration, browser, or release behavior beyond what local tests observe.

---

## Plugin boundary

Model Combo compiles as a native C-shared plugin (`c-shared` build mode) loaded by CLIProxyAPI (CPA) v7.3.9. The plugin entry point is `main.go`. The plugin implements four CPA SDK interfaces:

| Interface | Registration point | Responsibility |
|---|---|---|
| `ModelRouter` | `main.go: RegisterModelRouter` | Route alias → combo plan lookup |
| `ModelRegistrar` | `main.go: RegisterModelRegistrar` | Publish alias model names to CPA |
| `ProviderExecutor` | `main.go: RegisterProviderExecutor` | Execute sync and stream requests |
| `UsagePlugin` | `main.go: RegisterUsagePlugin` | Intercept usage records for attribution |
| `RequestInterceptor` | `main.go: RegisterRequestInterceptor` | Pre-execution header/body inspection |

---

## Core types

### Configuration (`config.go`, `account.go`)

```
pluginConfig
  enabled         bool
  routes          []modelRouteYAML
  combos          []modelComboYAML
  account_pools   map[string]accountPoolYAML
```

`accountPoolYAML` normalizes to `accountPool` (via `normalizeAccountPools`, `account.go:206`). Each pool entry has:

```
account
  Key       string   // logical config identity; used for digest and cooldown key
  AuthName  string   // must exact-match HostAuthFileEntry.Name
  Provider  string   // must exact-match HostAuthFileEntry.Provider
  Priority  int      // lower integer = higher preference; 0 is highest
  Weight    int      // smooth WRR weight (strategy: weighted-round-robin only)
```

`auth_id` and `auth_index` fields are rejected at parse time (`config_test.go:482–484`).

`accountPlan` is the immutable runtime representation (`account.go:10`). Its `signature` field (`account.go:17–30`) length-prefixes every config string so distinct plans cannot share one signature.

### Runtime state (`health.go`, `main.go`)

```
comboRuntime
  accountCurrent   map[poolSig]map[key]int         // smooth WRR cursor
  accountCooldown  map[poolSig]map[logicalKey]time  // per-account cooldown
```

`logicalKey = account.Key + "\x00" + account.id` — compound key prevents collisions across pools with the same configured key (`execute.go:89`, `stream.go:97`).

---

## Account resolution flow

Per request, for each candidate that references an account pool:

```
host.ListAuth()                          host_callbacks.go:49
  → []HostAuthFileEntry{ID, Name, Provider, Disabled, Unavailable, Status, NextRetryAfter}

resolveAccountPool(plan, entries)        account.go:40
  → []resolvedAccount{key, id}
  Rules: exact Name+Provider match; one unambiguous match only;
         nonempty trimmed ID; not Disabled/Unavailable/disabled-status/cooling

filterCooledAccounts(runtime, sig, resolved)   account.go (health.go)
excludeResolvedAccounts(resolved, attempted)   account.go:66

selectResolvedAccount(runtime, plan, available)  account.go:81
  strategy=priority:         declaration order within lowest Priority tier
  strategy=weighted-round-robin: smooth WRR (current[k]+=weight; winner -= total)
```

`ListAuth()` is called once per request, before the first pooled candidate is attempted. The same snapshot is used for all pools in that request (`execute.go:46,63–65`; `stream.go:54,71–73`).

---

## Sync execution path (`execute.go`)

```
executeWithHost(request, host)
  → newAttemptLedger(maxAttempts, deadlineMs)
  → for each group in plan.groups:
      for ledger.canAttempt():
        selectFromGroup(plan, group, ledger, capabilities)
        [if pooled] ListAuth / resolveAccountPool / filterCooled / exclude / select
        [fail-closed] authErr || len(resolved)==0 → markCandidateExhausted + continue
        host.Execute(hostRequestWithAuth(request, bodyInfo, model, false, authID))
        on success → rewriteResponseModel + return
        on retryable failure → MarkAccountFailure / backoff / try next account or candidate
```

`hostRequestWithAuth` (`execute.go:156`) sets `HostModelExecutionRequest.AuthID` to the opaque runtime ID supplied by CPA via `host.auth.list`.

---

## Stream execution path (`stream.go`)

Mirrors sync exactly through account selection. Diverges at commitment:

```
executeStreamWithHost(ctx, request, pluginStreamID, host)
  → forwardStreamAttemptWithAuth(request, bodyInfo, target, requestedModel, pluginStreamID, host, authID)
      host.StartStream(hostRequestWithAuth(..., authID))
      receivedPayload := false
      for host.ReadStream(streamID):
        if len(chunk.Payload) > 0:
          receivedPayload = true
          host.Emit(pluginStreamID, rewriter.Rewrite(chunk.Payload))
          return receivedPayload, nil     ← committed; no further retry
      return receivedPayload, err
  if receivedPayload: MarkSuccess + return (no retry)
  if !receivedPayload: retry next account / candidate per ledger
```

Model field in SSE chunks is rewritten to the alias via `streamModelRewriter` (`stream.go:180`).

---

## Auth callback wire protocol (`host_callbacks.go`)

| Direction | Method | Payload |
|---|---|---|
| Plugin → CPA | `host.auth.list` | `{}` (empty struct) |
| CPA → Plugin | — | `{"files":[{ID, Name, Provider, Disabled, Unavailable, Status, NextRetryAfter},...]}` |

Validation (`host_callbacks.go:58–74`): `files` must be non-null; every entry must have a non-empty, trimmed ID; no duplicate IDs.

Auth execution calls forward `AuthID` through `HostModelExecutionRequest.AuthID`. The field is empty (`""`) for non-pooled routes.

---

## Attribution (`attribution.go`)

- Credential and auth ID stored as HMAC-SHA256 fingerprints only; raw values never persisted.
- Account digest key = `account.Key` only (logical configured key). The opaque runtime `account.id` is excluded so the digest survives auth rotation (`attribution.go:125–128`).
- `MarkRoutedWithAccount` (`attribution.go:81`) called before every `host.Execute` / `host.StartStream`.

---

## Health and persistence (`health.go`, `health_persistence.go`, `usage_store.go`)

- `comboRuntime` tracks per-candidate health (probes, successes, failures, cooldowns).
- After each mutation, `persistRuntimeComboHealth` (`main.go`) maps runtime keys to persisted combo/group snapshots and enqueues them to `healthPersistence`.
- `healthPersistence` (`health_persistence.go`) is an async coalescing queue with a bounded drop policy: new distinct groups are dropped when the queue is full; existing groups continue coalescing.
- SQLite schema (`usage_store.go`): `combo_health_v2` table; v1→v2 migration applied on open.
- Shutdown: `Close()` flushes the queue before returning.

---

## Management API (`management.go`)

Registered routes under `/plugins/model-combo/`:

| Method | Path | Purpose |
|---|---|---|
| POST | `/validate` | Validate config before save |
| GET | `/usage/dashboard` | Usage summary snapshot |
| GET | `/usage/overview` | Summaries, trends, costs, model breakdowns |
| GET | `/usage/groups` | Paginated usage dimension groups |
| GET | `/usage/requests` | Paginated request-level usage |
| GET | `/usage/prices` | Persisted model price book |
| PUT | `/usage/prices` | Revision-protected price book update |
| POST | `/usage/prices/sync` | Sync prices from models.dev |
| GET | `/usage/preferences` | Dashboard preferences |
| PUT | `/usage/preferences` | Dashboard preferences update |
| GET | `/health/list` | All persisted combo health snapshots |
| POST | `/health/reset` | Clear health for one combo group |

No discovered-account management route exists. CPA v7.3.9 `ManagementRequest` lacks caller identity and host-auth entries; this route cannot be implemented safely under the current protocol.

---

## Dashboard (`dashboard/`, `dashboard.html`)

Static assets embedded and served via CPA resource route. Panels:

- `dashboard/configuration/` — combo/route/account-pool editor
- `dashboard/usage/` — usage tracking tables and charts
- `dashboard/health/` — health status panel with group reset

Dashboard authenticates via CPA management key (`dashboard.html:25`). Session affinity UI is absent because affinity cannot be enabled under CPA v7.3.9.

---

## Key constraints (CPA v7.3.9)

| Constraint | Impact |
|---|---|
| `ManagementRequest` lacks caller identity and host-auth entries | Discovered-account management API cannot be implemented |
| `ExecutorRequest` lacks trusted session ID | Session affinity cannot be enabled at runtime |
| Host Go version must be 1.26.x for CPA source build | Native integration requires matching toolchain |
