<!-- source-branch: main -->
<!-- work-branch: omp/sdk-v8-trace-accounts -->
# SDK v8 trace and account routing

## Scope and approved contract

Implement in the current cpa-model-combo-marketplace baseline (main at discovery: e919992). Upgrade github.com/router-for-me/CLIProxyAPI/v7 v7.3.9 to github.com/router-for-me/CLIProxyAPI/v8 v8.0.37. All changes are plugin-only; do not modify CPA, including the untracked .cpa-source/. Preserve untracked plans/ and unrelated work. No plugin rename.

Planning has performed source inspection only: no builds, tests or product edits. Execute only after plan approval. Use slug sdk-v8-trace-accounts for the approved plan and working branch. Persist this artifact under plans/ before branching; record source/work branch as required by session governance. Do not push.

User decisions:
- Current tree is authoritative, not the sibling hierarchy branch. Port compatible hierarchy pieces, not entire files or commits that would revert newer fixes.
- Supersede sibling ADR/BDR 0005's stock-v7 identity limitation, preserving 0004 hierarchy and historical semantics. Safe credential IDs and label/email snapshots may be retained; tokens, secrets, raw metadata and raw completion errors must not enter usage storage/UI.
- Omitted allowed_accounts and [] both mean unrestricted. Clearing selection explicitly removes restrictions.
- Per-credential registry membership is compatibility, not entitlement. Entitlement is Unknown when stock sources cannot verify model access; do not infer it from provider, model name, plan or tier. No provider entitlement API expansion.
- Approved observability boundary: one SDK TraceID parent with all delivered execution usage rows, including failures; order by start time with stable tie-break. Lifecycle-only failures stay on the parent. Never fabricate children for internal attempts with no delivered record. request_endpoint stores verified CPA request_path metadata, which may be a Gin route template rather than literal URL path.

## Evidence and architectural seams

Read .cpa-source/AGENTS.md and sibling /Users/momodding/Documents/cpa-model-combo-request-id-hierarchy/AGENTS.md plus ADR/BDR 0001–0005. Current tree has no root AGENTS or ADR/BDR. Record provenance and approved supersession explicitly in decision records, without pretending sibling records already govern this repository.

Stock source references, all pinned to v8.0.37 under https://github.com/router-for-me/CLIProxyAPI/tree/v8.0.37/:
- sdk/pluginapi/types.go: UsageRecord.RequestID is the execution UUID; TraceID is inbound parent identity; AuthID/AuthIndex identify credentials. HostModelExecutionRequest.AuthID pins execution. host.auth.models returns credential ID/index/provider/model membership, not entitlement. ABI 1 and schema 6 in sdk/pluginabi/types.go; do not import sibling custom schema-7 gates.
- sdk/api/handlers/handlers.go requestExecutionMetadata uses Gin FullPath, then URL.Path, stored under request_path. sdk/cliproxy/executor/types.go declares that literal key. sdk/api/handlers request interceptor/lifecycle plumbing forwards Metadata and TraceID. UsageRecord has no endpoint field: join from the inbound lifecycle, never from outgoing HostModelExecutionRequest.Path.
- internal/pluginhost/host_callbacks.go resolves HostCallbackID to original context before ExecuteModel; host_model_stream_callbacks.go preserves context values with context.WithoutCancel. Preserve the existing callback ID bridge for both modes.
- internal/runtime/executor/helps/usage_helpers.go allocates execution UUID and captures TraceID; TrackFailure/PublishFailure emit failure details. OpenAICompat allocates reporters per execution and reports failures and zero-usage completion. Some Antigravity failures precede reporter creation. This is not a complete network-attempt event feed.
- sdk/cliproxy/auth/conductor_selection.go, scheduler.go and conductor_home.go filter pinned IDs in regular, mixed, legacy, plugin-scheduler, Home and Antigravity credits selection, including streaming. Home validates returned ID; plugin scheduler responses must belong to prefiltered candidates. No escaping path found by read-only audit; native tests remain required.

Plugin seams:
- main.go HandleUsage currently performs heuristic attribution; attribution.go uses credential/time matching. New exact identity must take precedence; retain heuristic behavior only for old/identity-less rows and label it honestly.
- execute.go/stream.go own combo target fallback. hostRequest currently omits AuthID. Preserve cancellation, lifecycle cleanup, fallback status policy, cooldown and no-fallback-after-first-payload behavior.
- config.go, management.go and runtime.go own route parsing, strict JSON/YAML validation, persistence and signatures. dashboard/configuration/tab.js owns the full route editor; there is no routes.js.
- Current usage has RouterRequestID/RouterAttempt, but no stock exact trace/endpoint fields or sibling hierarchy UI. SQLite schema 1 stores JSON payloads in requests; port sibling schema-2 request_sessions migration rather than rewriting historical payloads.

## Execution units

### 1. Upgrade the SDK and native boundary

- Replace all product/test/example module imports and dependency references from /v7 to /v8, pin v8.0.37, regenerate go.sum through normal Go tooling. Update native integration CPA version/checkout and CI from v7.3.9 to the exact v8.0.37 commit resolved from the tag (do not use a Git tree SHA as a commit SHA).
- Adapt ABI RPC structures, registration, callbacks and lifecycle handlers against actual v8 types. Preserve plugin identity and management authentication. Do not add hand-written shadow identity fields where SDK fields now exist. Stay on ABI 1/schema 6.
- Extend callbackModelHost/modelHost only as needed for host.auth.models and host.auth.list with HostCallbackID propagation. Use safe response DTOs; never expose complete SDK auth objects to the browser.
- Add/update decision records explaining plugin-only scope, approved snapshot persistence, source provenance, observability boundary and entitlement policy. Update existing README/changelog/config examples in the final documentation unit.

### 2. Reuse Request Details hierarchy and safe snapshots

Port the sibling's storedRequestSession/RecordSession, usageRequestSelection/requestParent/orderRequestChildren/aggregateRequestAttempts and dashboard requestRow/renderRequestTable structure selectively. Preserve this tree's token-accounting, aggregation, attribution, shutdown and native lifecycle fixes.

Identity/data contract:
- Store explicit trace_id and execution request_id from stock UsageRecord without replacing one with the other. Parent identity is trace_id; lifecycle RequestCompletion.RequestID is separate and never assumed equal to a child UUID. Preserve RouterRequestID/RouterAttempt for legacy compatibility only.
- Persist session lifecycle by trace identity, with original lifecycle request ID, request_endpoint, requested/router model, stream, started/completed timestamps, outcome, HTTP status and latency. Capture the first inbound before-auth endpoint and model; nested execution completion must not overwrite the inbound session. Match completion by the captured lifecycle RequestID as well as TraceID.
- Usage callbacks can arrive before lifecycle completion or session persistence. Query joins and session upserts must tolerate either order; no timing-window identity joins. Detached usage callbacks must remain ingestible during existing graceful drain.
- Add auth_id, auth_index and credential_label snapshot to execution payloads. Resolve by exact credential ID, or unambiguous index if ID absent. Prefer safe Label then Email from stock auth listing. Do not use Source, filenames/paths, tokens, arbitrary metadata or raw error text as labels. Trim/control-character-clean and cap display labels; reject obvious credential prefixes. If no safe snapshot exists, display a neutral unavailable label, not a guessed identity. Keep original snapshot after credential removal.
- Catalog refresh is performed only with a valid host callback context (management/execution); usage ingestion reads the latest immutable safe snapshot and never blocks on remote auth lookup. Existing direct traffic can legitimately have an unavailable label before a catalog is populated; do not claim historical labels can be recovered after account removal.
- Child result uses Failed and Failure.StatusCode; zero status is unknown, not invented HTTP 200. Parent result comes from inbound completion, not the last received child. Show success/failure independently of whether a numeric status is available.
- Child order: RequestedAt ascending, then stored sequence for ties. Ordinals are display order, not a claimed CPA transport attempt number. Include all delivered rows; never synthesize usage, tokens, cost or provider attempts from lifecycle errors. Preserve existing usage accounting separately from parent counts.
- Parents without usage remain visible after completion; records without exact identity retain legacy standalone/group behavior. Preserve sibling historical grouping keys and avoid merging legacy IDs with exact trace IDs. Missing TraceID is not inferred from client API key or time. TraceID is only 8 hex characters: preserve lifecycle IDs to detect ambiguity; do not silently fuse demonstrably distinct sessions. If ambiguous, keep rows unassigned with an explicit correlation warning rather than inventing an association.
- Filters and pagination operate on logical parents, not child slices; child search matches retain the complete parent/children. Token/cost summaries count execution rows once. Show parent endpoint, trace, requested alias, outcome/timing, and expandable ordered children with provider/model/credential/result. Use existing table styles, keyboard-operable buttons and aria-expanded; preserve scroll and expansion state across refresh.

Storage:
- Port additive request_sessions schema migration transactionally, with schema version advancing only on success. Preserve requests/minute_aggregates/store_state and every old JSON field. New execution fields are additive JSON; no rewriting old records.
- Include sessions in retention, reset, close/drain and reopen semantics using existing store ownership. Session upserts must not let a late start event erase completion. Do not add an independent unbounded in-memory history.
- Port management schema version 2 and sibling response hierarchy, adapting identity mapping above. Keep current summary meanings; expose logical parent count separately if needed by the hierarchy, never silently relabel callback totals.

### 3. Credential catalog and allowed_accounts

Add a plugin-management GET account catalog endpoint alongside existing configuration endpoints. Call host.auth.models with empty AuthIndex for all accounts, join safe host.auth.list identity/label/status fields by ID, and return only credentials [{id, auth_index, provider, label, disabled, models:[{id, entitlement:"unknown"}]}]. Registry-only accounts remain selectable by ID with unavailable label; disabled entries are visible but not executable. Exclude model-router self aliases to prevent recursion. No browser call to /v0/management/api-keys; remove the existing fetch-first-key /v1/models catalog path.

Configuration contract:
- Add allowed_accounts string array at route/combo level in YAML, management DTOs, normalized modelRoute, cloning/snapshots and serialization. IDs are exact case-sensitive identities, trimmed, unique; reject blank/non-string IDs. Preserve user order for deterministic account priority. Omitted/[] is unrestricted.
- Include account IDs/order in routeSignature so changing restrictions invalidates retained routing state. Old configs and legacy models syntax still load.
- Validate management saves against a fresh catalog before persistence: each selected ID must exist and be enabled; each target must be offered by at least one selected credential; each selected credential must support at least one route target. Different providers need not all support every target. Compare registry model IDs exactly, with only the existing thinking-suffix handling; no fuzzy provider/name guesses. Return field-specific 400 errors; callback/catalog failure returns 503 without saving.
- Startup/file reload performs structural validation without requiring a callback context. Runtime revalidates restricted candidate availability on each route execution using a fresh catalog; catalog failure/missing account cannot relax the restriction. Stale saved IDs remain visible/removable rather than silently being cleared.

Routing:
- Unrestricted routes retain existing CPA account selection and existing target selection/cooldown semantics; send empty AuthID.
- For restricted routes retain priority/weighted-round-robin target choice. For each target, derive eligible IDs in saved allowlist order from its model membership and active status. Attempt each eligible account at most once at plugin level, pinned via HostModelExecutionRequest.AuthID and its catalog provider via ForcedProvider. Never make an unpinned retry for a restricted route.
- Use the same account selection helper in execute.go and stream.go. Pass the selected credential through hostRequest/forwardStreamAttempt rather than recomputing it. Credential refresh/races are handled by host rejection, not substitution.
- On retry-eligible failure try the next permitted account, then next permitted target. Nonretryable errors stop immediately; cancellation stops immediately. Once any stream payload is received, do not change account or target. Mark target cooldown only when all its eligible account attempts fail with retryable failures, avoiding poisoning a usable account after one account's quota failure.
- No eligible restricted account yields a structured model_route_unavailable 503; never escape to a global credential. Do not log raw upstream bodies or credentials. CPA may internally retry the pinned credential; those delivered records are separate children under the same TraceID.

Frontend:
- Extend normalizeRoute/createRouteCard/serializeRoutes/validateClient/configurationSnapshot/saveConfiguration in dashboard/configuration/tab.js. Add provider-grouped labeled checkbox multiselect per combo, preserving selected stale IDs as invalid removable rows.
- Model dropdown is the union of selected credentials' compatible models, or all catalog credentials when unrestricted. Show per-credential membership and Unknown entitlement; never label registry models Free/Paid/Accessible. Account changes flag incompatible targets, never silently delete them. Disable save for invalid selection; backend remains authoritative.
- Explain clearly: "No accounts selected: all accounts allowed." Show loading/unavailable catalog state, not a misleading empty success. Preserve focus-safe target reordering, unsaved edits, existing weights and route controls.

### 4. Verification and release documentation

Add narrow regression cases in existing test files; new files only where a new component needs isolated tests. Run each intended check once after implementation; no tests during planning.

Unit/contract evidence:
- ABI/schema6 registration and v8 envelope round-trips; host callback IDs and AuthID survive normal and stream requests.
- Identical concurrent client/model requests with different TraceIDs do not cross-attribute. Out-of-order success/failure usage and completion produce one correct parent each. Equal timestamps order stably. Lifecycle-only failure, missing trace, ambiguous trace, legacy RouterRequestID, historical JSON and unavailable/deleted credential labels remain honest.
- Schema1-to-schema2 open/reopen preserves old rows/aggregates; migration failure rolls back; sessions obey retention/reset/drain. Aggregates do not double count expanded children. Pagination never splits children.
- Secret-shaped labels, raw metadata, source tokens and error bodies never reach stored/API/UI snapshots. Ordinary safe label/email survives account deletion.
- Omitted/empty/duplicate/blank/stale/disjoint/mixed-provider allowlists; unknown entitlement; save callback failure leaves config unchanged. Route signature changes with restrictions.
- Account A fails then allowed B succeeds; unselected C is never called. Missing or disabled A fails closed. Target fallbacks preserve restrictions. Stream setup failure may switch allowed account; failure after payload cannot. Cancellation and cleanup close streams exactly once. Unrestricted routes retain existing behavior and fallback status policies.

Native integration:
- Use existing Unix CGO integration_test.go harness and a separate pristine CPA v8.0.37 checkout supplied via CPA_SOURCE, never mutate .cpa-source/. Extend TestModelRouterWithCLIProxyAPI local mock upstream to expose distinct mock credentials and record which receives traffic.
- Exercise concurrent inbound requests, nonstream target/account failure-to-success, streaming bootstrap fallback, post-payload failure, zero-usage completion and missing-account rejection. Poll management usage until asynchronous callbacks drain; assert original SDK TraceID joins, request_endpoint for standard chat/responses paths, ordered delivered failed/success children and credential labels. Assert no calls to excluded credentials. Tests do not depend on paid credentials or network providers.
- Run go test ./..., go test -race ./..., go vet ./...; run CPA_SOURCE=<pristine-v8-checkout> go test -tags=integration -run TestModelRouterWithCLIProxyAPI ./... with CGO_ENABLED=1. Build the native c-shared artifact using the existing Makefile target. Capture exact commands/results; compilation alone is not native/UI proof.

Browser:
- Extend tests/e2e/dashboard.test.mjs using existing playwright-core. Run against a disposable loopback native v8 instance with fresh usage DB and initially empty routes; set DASHBOARD_URL, DASHBOARD_MANAGEMENT_KEY and DASHBOARD_E2E_ALLOW_MUTATIONS=1, then npm run test:e2e.
- Verify actual rendered provider grouping, multiselect persistence/reload, Unknown labels, incompatible/stale selection feedback, clearing-selection warning, route reorder focus, hierarchy expand/collapse, endpoint/credential text and refresh stability. Capture browser screenshot evidence and native mock request evidence. Never use production credentials or a production configuration for mutation tests.

Update existing README/changelog/sample config with v8 prerequisite, empty allowlist semantics, compatibility versus entitlement, safe snapshot retention, delivered-execution observability boundary and route-template endpoint behavior. Document upgrade backup and rollback below.

## Rollout, rollback and stop condition

Back up configuration and SQLite using a consistent SQLite backup or after clean shutdown, including all data needed for rollback. Do not run old/new plugin writers against the same DB. Upgrade CPA externally to stock v8.0.37 and deploy its matching plugin artifact together; no CPA code patch is part of this work. Run isolated migration/native/browser proof before production rollout.

Rollback stops the plugin, restores matching old CPA/plugin artifacts and pre-upgrade config/database backup. Old schema-1 binaries must not be pointed at schema2; never decrement the version or delete session data to force compatibility. Post-upgrade data is retained separately if rollback is required. No destructive automatic downgrade.

Done means all four units implemented, all listed behavioral cases and actual UI/native surfaces verified, no /v7 product dependency remains, historical records preserved, and documentation accurately states approved stock limits. If prerequisites are unavailable, report the exact missing evidence rather than claiming verification. No additional abstractions, entitlement probes, CPA changes, historical credential guessing, or invented transport-attempt rows.
