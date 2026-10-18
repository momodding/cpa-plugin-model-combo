# Account-Aware Combo Routing and Final Audit Plan

## Context

Complete CPA Model Combo v2 against production code and executed behavior, not phase labels or existing TODO state. Preserve working Combo v2 model selection, failure handling, protocol forwarding, stream commitment, legacy route migration, usage storage, and host-owned transport/authentication. Close confirmed gaps: no account policy/config/runtime, no `AuthID` pinning, no graphical combo/account editor, validation rejects the dashboard's current `combos` payload, storage remains schema 1 with no persisted combo health, session affinity is absent, required architecture/manual documents are absent, and release evidence is macOS-only.

Pinned CLIProxyAPI v7.3.9 provides the required host boundary. `host.auth.list` returns metadata-only `HostAuthFileEntry` records. `buildHostAuthFileEntry` maps `ID: auth.ID`, while model execution forwards `HostModelExecutionRequest.AuthID` into CPA's pinned-auth metadata and CPA filters candidates by exact `candidate.ID`; sync and stream therefore share strict pin semantics. `AuthIndex` is a management/runtime lookup identifier and must not be used for execution. Discovery must never call `host.auth.get`, read credential files, store OAuth payloads, or perform provider transport directly.

Existing verified work remains authoritative only for paths actually exercised: identity, legacy normalization, immutable combo plans, group selectors, failure/circuit behavior, capability filtering, protocol forwarding, pre-commit stream fallback, raw JSON dashboard assembly, local tests/race/vet, and native macOS build. Re-audit each original phase criterion after all changes because account retries alter attempt accounting and because current dashboard and persistence do not meet P1 acceptance.

## Approach

1. **Create an evidence matrix before changing behavior.** Map every Definition of Done item from phases 1–8 to production symbols, permanent tests, and an executable verification case. Classify each item `PASS`, `PARTIAL`, `FAIL`, or `BLOCKED`; a passing unit test alone is not `PASS` for integration, browser, platform, or manual criteria. Use this matrix as the source for `docs/final-implementation-audit.md` and keep failures open until fixed or externally blocked.

2. **Add strict account-pool schema without changing legacy behavior.** Extend canonical config with reusable top-level pools and an optional target reference:

   ```yaml
   account_pools:
     premium-codex:
       strategy: weighted-round-robin
       accounts:
         - key: account-a
           auth_name: mock-account-a.json
           provider: codex
           priority: 0
           weight: 2
         - key: account-b
           auth_name: mock-account-b.json
           provider: codex
           priority: 0
           weight: 1
         - key: account-c
           auth_name: mock-account-c.json
           provider: codex
           priority: 10
           weight: 1
   combos:
     - alias: coding
       groups:
         - name: primary
           strategy: priority
           targets:
             - model: codex/gpt
               account_pool: premium-codex
   ```

   Pool names and account `key` values are trimmed, case-insensitively unique, and limited to 300 bytes. `auth_name` and `provider` are required exact metadata matches; schema rejects `auth_id`, `auth_index`, empty values, duplicates, and references to unknown pools. Accepted pool strategies are `priority` and `weighted-round-robin`. `priority` requires weight 1; weighted strategy requires weight `1..1_000_000`. Priority is `0..1_000_000`, lower first. Limit 128 pools and 256 accounts per pool. `routes` and `model-routes` cannot attach account policy and retain current one-group normalization. A target with no `account_pool` omits `AuthID` and delegates account selection to CPA exactly as before. Config stores operator-visible `auth_name` plus non-sensitive logical `key`, never `AuthID` or `AuthIndex`.

3. **Compile immutable account plans and reload-safe selector state.** Add `accountPlan` entries to `candidatePlan` by resolved pool reference, with stable signatures including pool strategy, declaration order, key, auth name, provider, priority, and weight. Add `accountRuntime` with one mutex and state per pool signature: priority cursor for deterministic declaration-order traversal and smooth-WRR current weights. Preserve state only when signature is identical on reload; reset changed pools; never persist cursor or WRR weights. Selection first restricts to the lowest numeric priority tier containing an eligible, unattempted account. `priority` selects declaration order; `weighted-round-robin` applies smooth WRR within that tier with deterministic declaration-order ties. When a tier is exhausted, selection advances to the next numeric tier. Request-local account attempts are keyed by pool plus logical account key and exact discovered ID; runtime locks are never held across callbacks, sleeps, stream reads, or storage.

4. **Discover metadata through the real host callback and resolve fail-closed.** Extend `modelHost`/`callbackModelHost` with `ListAuth()`, implemented with `pluginabi.MethodHostAuthList` and an empty JSON object. Decode only `HostAuthFileEntry`; do not expose a method for `host.auth.get`. For each request containing an account-aware target, lazily fetch one auth snapshot and reuse it for that request. Resolve each configured account by exact case-sensitive `(Name, Provider)`. Eligible records require nonempty `ID`, `Disabled == false`, `Unavailable == false`, status not equal to `disabled`, and no future `NextRetryAfter`. Zero matches means unavailable; multiple matches mean ambiguous; both are fail-closed and never fall back to unpinned CPA execution. Discovery/decode errors exhaust that strict pool for the target without penalizing model health. Return only safe pool/key/status diagnostics, never IDs, indexes, labels, email, path, status message, or callback payload.

5. **Layer account attempts inside each selected model attempt.** Keep model group ordering and model selector state unchanged. After selecting one model candidate, build its account sequence. Unpooled target executes once with empty `AuthID`. Pooled target may execute each eligible account at most once while global `max_attempts` and deadline permit; each actual host call consumes one global attempt. Pass exact selected `HostAuthFileEntry.ID` to `hostRequest(...).AuthID` for both `Execute` and `StartStream`. Retry another account only for existing fallback-eligible failure classes; terminal request/cancellation/deadline behavior remains terminal. After account exhaustion, advance to next model/group. Defer model-health mutation until target success or account exhaustion: success calls `MarkSuccess`; exhausted target calls `MarkFailure` once only if at least one account attempt produced a penalizing failure; auth discovery, missing/disabled account, capability conflict, and terminal request failures do not poison model health. Release half-open ownership on every terminal path. Final errors include safe combo/group/model/pool/account-key attempt summaries and never upstream bodies beyond existing bounded redaction.

6. **Preserve stream atomicity with account fallback.** Pass selected `AuthID` into `forwardStreamAttempt`. Start/open/read failure before any nonempty payload is successfully emitted may select another account, then another model/group, subject to one global ledger. Once payload emission succeeds, mark model/account attempt successful and never select another account or model; later read/emit errors close the active upstream and plugin streams once. Emit failure remains terminal because client commitment is unknowable. Add close-once and concurrent stream tests around account exhaustion.

7. **Make account attribution exact but privacy-safe.** Replace one marker per model selection with one marker per actual host attempt. Marker stores combo/group/candidate/strategy/ordinal, logical account `key`, and process-keyed HMAC of selected `AuthID`; raw ID is never retained. Match a `UsageRecord.AuthID` only by exact HMAC equality. If absent or ambiguous, leave account key empty and attribution `unattributed`; never infer from provider/name/time alone. Handle CPA's reentrant usage callback by attaching an observed usage record to its bounded in-memory attempt marker, then finalizing and writing it after the host call returns; direct/unmatched usage keeps current behavior. Completion records failure class, failed/final flags, attempt ordinal/count, and monotonic success latency. Expired incomplete markers flush as unattributed rather than disappear. Persist only logical account key, never auth name, ID/index, label/email, session values, request data, or full errors.

8. **Finish phase-5 storage and health observability.** Migrate SQLite schema 1→2 in one transaction. Add `combo_health(config_fingerprint, combo_key, group_key, candidate_key, consecutive_failures, circuit_open, open_until_ns, reopen_count, success_count, failure_count, latency_ewma_ns, error_ewma, sample_count, updated_at_ns)` with primary key on the first four fields and an `updated_at_ns` prune index. Extend JSON usage rows/aggregates with optional combo, group/index, candidate, attempt ordinal/count, strategy, failure class, final/failed, and logical account key; old payloads decode with zero values. Add a bounded queue of 256 health keys, nonblocking enqueue, coalescing worker, flush every 250 ms or 64 keys, and two-second orderly shutdown flush. A full queue increments an in-memory dropped-write counter. Restore only fingerprints present in current compiled config, clamp future timestamps to current time plus configured maximum circuit-open duration, and prune stale fingerprints/retention rows. Preserve WAL, busy timeout, revision-protected prices, preferences, existing bbolt migration, and file permissions. Reset defaults to usage only; `include_health: true` is required to clear health, and scoped health reset validates exact combo/group/candidate membership.

9. **Finish phase-6 model affinity independently from accounts.** Add optional per-combo `session_affinity` exactly as defined by the original contract: disabled by default; one configured header; `ttl_seconds 1..86400`; `max_entries 1..100000`; global cap 100000. Reject `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`, `X-API-Key`, and configured API-key aliases case-insensitively. HMAC-SHA256 the single nonblank header value with a process-random secret and combo alias; multiple values disable affinity. Use a mutex-protected TTL LRU containing only digest and group/candidate signature. Prefer a binding only if capability and health eligible; rebind only after alternate model success. Account selection remains independent and is never sticky. Reload removes incompatible bindings; shutdown clears memory; no affinity state appears in logs, APIs, SQLite, or dashboard data.

10. **Expose authenticated management resources and replace raw editor with graphical management.** Pass management `HostCallbackID` into `callbackModelHost` so authenticated `GET /plugins/model-combo/accounts` can call `host.auth.list`. Return only `name`, `provider`, `status`, `disabled`, `unavailable`, `runtime_only`, `priority`, and retry timestamp; omit ID/index/path/email/project/account/status message and recent request data. Add health list/reset routes with pagination and exact scoped reset. Expand validation request to accept canonical `combos`, `account_pools`, resilience, fallback, and affinity fields; today’s `DisallowUnknownFields` rejection of `combos` must become a full canonical validation path.

    Replace `textarea#combos-json` as primary state with accessible nested controls: combo cards; ordered group cards; target rows; strategy/cooldown/weights/capability chips; target account-pool selector; account-pool cards; discovered-account dropdown; logical key/priority/weight; resilience/capability/affinity controls; health diagnostics and scoped reset. Keep legacy route cards unchanged until explicit **Convert to combos** action, then save canonical `combos` and clear legacy keys. Use up/down buttons, no drag-only interaction. Map server paths to exact controls, preserve invalid drafts, focus first error, preserve dirty snapshot, and keep management key only in existing browser-session flow. Discovery failures disable account selection edits but preserve drafts and show retry. Usage UI adds optional combo/group/failure/account-key columns without breaking old rows.

11. **Build permanent behavioral coverage, then real CPA account E2E.** Unit tests cover schema rejection/defaults, account resolution ambiguity/unavailability, priority-tier exhaustion, exact smooth-WRR cycles, concurrent selection, reload preservation/reset, global attempt bounds, model-health deferral, exact HMAC account attribution, sync/stream pin parity, post-commit prohibition, affinity privacy, migration rollback, queue drops/flush, health reset, and management redaction. Extend the disposable CPA harness with mock Accounts A/B/C and a mock upstream that identifies the credential it received. Use actual plugin ABI, `host.auth.list`, `host.model.execute[_stream]`, CPA auth manager, and exact pin enforcement—not selector-only doubles. Configure A/B in strict pool and leave C discoverable but excluded. Prove weighted requests use only A/B at configured ratio over a complete cycle; A retryable failure reaches B; C receives zero calls; disabled/missing/ambiguous accounts fail closed; pool exhaustion advances model/group; no-policy target delegates normally; stream pins selected account and never switches post-payload; concurrent calls remain race-free; usage stores logical account key and no raw ID/index. Set CPA `request-retry: 0` so observed retries belong to combo logic.

12. **Complete browser, docs, release, and manual audit.** Add Playwright Chromium E2E against disposable CPA: authenticate, discover A/B/C, create pool, create/reorder two-group combo, validate duplicate account, save/reload, send sync and stream requests, observe logical account usage and health, perform scoped reset, test keyboard labels, and capture desktop 1280×800 plus mobile 390×844 layouts. Create the four missing original documents (`docs/combo-gap-analysis.md`, `docs/combo-architecture.md`, `docs/combo-implementation-plan.md`, `docs/combo-manual-verification.md`) and the required final reports (`docs/final-implementation-audit.md`, `docs/final-gap-closure.md`, `docs/final-verification-report.md`). Update README and CHANGELOG with account schema/failure rules, privacy boundary, affinity, persistence, migration/rollback, stream invariant, release assets, and links.

    Build all five release archives with injected version, verify archive names/root contents/checksums/file architectures/registration metadata, then execute disposable CPA sync+stream smoke on every locally executable platform. Use Docker/QEMU for Linux amd64/arm64. Native-test Darwin amd64; Darwin arm64 and Windows amd64 are package-inspected unless compatible runners are present. Any unavailable external runner/Pi check is `BLOCKED`, never passed, and final report includes exact command and expected output. Do not deploy, commit, push, or use production credentials.

13. **Close audit only from observed evidence.** Run formatting, unit, race, vet, integration, browser, release, platform, migration/coexistence, and secret scans once at final integration boundary. Reconcile evidence matrix with outputs and final tree. `docs/final-gap-closure.md` lists each discovered gap and concrete closure. `docs/final-verification-report.md` separates implementation complete, unit tests passing, CPA integration verified, browser E2E verified, artifacts verified, platform execution verified, and deployment ready. Production readiness is `NO` while any mandatory local criterion fails or any required external platform/Pi criterion remains blocked.

## Critical files & anchors

1. `config.go` — `routerConfigYAML`, `comboTargetYAML`, normalization/validation; add account pools, target references, and affinity without weakening known-field validation.
2. `account.go` (new) — immutable account plans, metadata resolution, priority-tier/smooth-WRR runtime, signatures, request-local exclusions, and redacted result types.
3. `execute.go` — `modelHost`, `executeWithHostContext`, `hostRequest`; integrate discovery/account loops, global ledger, deferred health mutation, marker completion, and exact sync `AuthID`.
4. `stream.go` — `executeStreamWithHost`, `forwardStreamAttempt`; mirror account selection and exact pinning while preserving first-successful-emit commitment.
5. `management.go` — registration/dispatch/validation; add redacted account and health APIs and provide canonical data used by graphical dashboard assets.

## Verification

Run from repository root unless command changes directory. Concrete input→observable output requirements are part of each check.

1. Static and behavioral suite:

   ```bash
   make fmt-check
   go test ./... -count=1
   go test -race ./... -count=1
   go vet ./...
   node --check dashboard/configuration/tab.js
   ```

   Expected: all exit 0; race run reports no race; account tests observe exact `AuthID` on every host call, A:B full-cycle ratio, strict C exclusion, bounded call count, no stream fallback after emitted payload, schema-1→2 atomic migration, and no raw auth/session values in API or SQLite fixtures.

2. Disposable CPA integration using adjacent source when present, otherwise a writable temporary copy of pinned module source:

   ```bash
   CPA_SOURCE="${CPA_SOURCE:-../CLIProxyAPI}" go test -tags=integration ./... -run 'TestModelComboWithCLIProxyAPI|TestAccountAwareRoutingWithCLIProxyAPI' -count=1 -v
   ```

   Input: temporary CPA config, mock provider, mock auth records A/B/C, strict pool containing only A/B, `request-retry: 0`. Observable output: plugin registers as `model-combo`; host account callback discovers three metadata records; upstream authorization identifies selected A/B; C count is zero; configured weight cycle is exact; forced A failure reaches B; all-account exhaustion reaches next physical model/group; omitted policy lets CPA choose; sync and stream both pin exact selected auth; post-payload stream failure makes no new upstream call; concurrent request counts are complete; usage API shows logical account keys only.

3. Browser E2E and actual surface:

   ```bash
   cd e2e
   npm ci
   npx playwright install chromium
   npm test
   ```

   Expected: Chromium interacts by role/label with real resource page and disposable CPA; legacy conversion, nested reorder, account discovery/pool creation, validation focus, save/reload, request, usage/health, scoped reset, and keyboard navigation pass at 1280×800 and 390×844; no clipped action controls; traces/screenshots contain no management key or auth ID. Also open the same running page with browser tooling, perform one save→request→usage flow, inspect both viewports, and close the tab.

4. Release artifacts:

   ```bash
   bash -n .github/xgo/setup.sh .github/xgo/release.sh
   rm -rf dist
   .github/xgo/release.sh "$XGO_IMAGE" github.com/momodding/cpa-model-combo model-combo 0.1.0
   shasum -a 256 -c dist/checksums.txt
   for archive in dist/model-combo_0.1.0_*.zip; do unzip -Z1 "$archive"; done
   file dist/*
   ```

   Expected: exactly five archives (`linux_amd64`, `linux_arm64`, `darwin_amd64`, `darwin_arm64`, `windows_amd64`) plus `checksums.txt`; every checksum passes; each archive contains only root installer-contract library/header files; library architecture matches archive; loading executable artifacts reports plugin ID `model-combo`, name `Model Combo`, version `0.1.0`, and supported registration schema.

5. Linux execution and portability:

   ```bash
   docker run --rm --platform linux/amd64 -v "$PWD/dist:/dist:ro" -v "$PWD/testdata/release-smoke:/smoke:ro" "$CPA_SMOKE_IMAGE" /smoke/run.sh /dist/model-combo_0.1.0_linux_amd64.zip
   docker run --rm --platform linux/arm64 -v "$PWD/dist:/dist:ro" -v "$PWD/testdata/release-smoke:/smoke:ro" "$CPA_SMOKE_IMAGE" /smoke/run.sh /dist/model-combo_0.1.0_linux_arm64.zip
   ```

   Expected per platform: CPA loads plugin, registration metadata matches, one sync and one stream request return alias-rewritten output, account A/B pinning is observed, and process exits cleanly. Run native Darwin amd64 smoke with the same disposable harness. If arm64 emulation, Darwin arm64, Windows, or Pi 4 cannot execute, record `BLOCKED` with runner requirement; package inspection is not execution proof.

6. Manual migration/coexistence and Pi procedure from `docs/combo-manual-verification.md`: copy unchanged legacy config under new ID; run original router and combo with disjoint aliases; explicitly convert; reload; rollback; exercise all strategies/failure classes/stream boundary/health reset. Pi input is 100 candidates across five combos, 1,000 sequential requests, concurrency 8, hot reload, usage query, restart, `GOMAXPROCS=2`. Observable limits: no OOM/deadlock/leaked process or stream/unbounded growth; queue and affinity caps hold; retention prunes; selection p95 under 5 ms excluding CPA/provider/backoff; record RSS, CPU, DB size/write rate, p50/p95, dropped writes, and goroutines.

7. Privacy scan over produced config snapshots, logs, management JSON, Playwright artifacts, and SQLite decoded rows. Seed unique sentinels for prompt, inbound Authorization, management key, mock `AuthID`, `AuthIndex`, and session header. Expected: no sentinel appears except mock auth name where explicitly present in operator configuration; usage/health/dashboard expose only logical account key and redacted metadata.

## Assumptions & contingencies

- **Account reference:** canonical config uses exact CPA metadata `(auth_name, provider)` because persisting `AuthID`/`AuthIndex` is forbidden. If operator requires a different stable non-secret handle, replace both fields atomically in schema, dashboard, docs, migration, and tests; default remains exact name/provider with ambiguity failing closed.
- **CPA source:** prefer `CPA_SOURCE=../CLIProxyAPI`. If absent, copy `/Users/momodding/go/pkg/mod/github.com/router-for-me/CLIProxyAPI/v7@v7.3.9` equivalent module-cache directory to a temporary writable directory and point `CPA_SOURCE` there; never edit module cache.
- **Mock OAuth execution:** first use stock CPA temporary supported auth files whose provider can target the mock upstream — unverified — confirm first. If stock CPA cannot execute OAuth-shaped mock records without external infrastructure, build a test-only provider plugin loaded by disposable CPA that registers A/B/C metadata-only mock OAuth records and forwards to local `httptest`; production plugin and CPA core remain unchanged.
- **Release version:** use `0.1.0` for injected artifact verification and keep local default `0.0.0-dev`. If release owner/version changes, update module/repository metadata, docs, workflow expectation, archive names, and registration assertion together before rerunning the full artifact gate.
- **Platform runners:** use Docker/QEMU for Linux architectures and native host for Darwin amd64. If user supplies Darwin arm64, Windows amd64, or Raspberry Pi 4 runners, execute the documented smoke there; otherwise final status is `BLOCKED` for those runtime-only checks with package verification reported separately.
