<!-- source-branch: feat/model-combo-routing-v2-stabilization -->
<!-- work-branch: omp/investigate-router-attribution-2 -->

# Request Details router tree

## Context

Request Details currently renders every persisted `storedUsageRecord` as one flat `<tr>`. Routed fallback attempts therefore appear as unrelated provider rows, while CPA's outer executor usage callback often creates an otherwise identical unattributed row. Live read-only evidence from `/Users/momodding/Documents/cliproxyapi-podman/plugins/model-router.db` showed 359 rows: 134 routed, 135 unattributed, and 90 direct; 131 groups are one routed row plus one exact unattributed copy. `router_model` is the configured client-visible route alias; `provider_model` is the physical model selected for an attempt. They are intentionally different.

Change Request Details into a collapsed-by-default tree. One top-level row represents one logical router execution; its child rows represent provider attempts, including failed attempts before a successful fallback. Direct, unattributed, and historical routed records without a correlation ID remain standalone rows so old payloads stay readable without guessing relationships.

## Repository preflight

1. Before copying this plan into the repository or making any feature edit, remain on source branch `feat/model-combo-routing-v2-stabilization`. This preflight intentionally precedes normal plan persistence and work-branch creation to honor the required clean starting state.
2. Current read-only status shows 14 modified tracked files: `config.go`, `config_test.go`, `dashboard/configuration/panel.html`, `dashboard/configuration/routes.js`, `dashboard/configuration/tab.js`, `dashboard/shared.js`, `docs/v2/06-manual-test-readiness.md`, `docs/v2/08-configuration-migration.md`, `execute.go`, `failure.go`, `failure_test.go`, `main.go`, `management.go`, and `stream.go`; it also shows untracked `cpa-plugin-model-combo`, confirmed by `file` as a 19 MiB Mach-O executable. Delete only that generated executable. Stage exactly the 14 tracked files and commit them as `feat: stabilize model combo routing v2`; do not create or stage router-tree files in this commit.
3. Branch currently has no configured upstream and remote `origin` is `git@github.com:momodding/cpa-plugin-model-combo.git`. Push the preflight commit with `git push -u origin feat/model-combo-routing-v2-stabilization`.
4. Run `git status --porcelain`; require empty output. If deletion, commit hooks, push, or the clean check fails, stop before copying the plan or editing feature files. Once clean, copy this approved plan to `plans/investigate-router-attribution.md`, record source/work branch comments there, and create `omp/investigate-router-attribution` (or the smallest unused `-N` suffix) from the pushed commit before implementation.

## Scope and compatibility

- Reuse the existing Request Details table, filters, column preferences, sort controls, and pagination controls. Add no dependency and no second details surface.
- Add optional JSON fields to `requests.payload`; do not alter the SQLite table or rewrite existing rows. Missing fields decode to zero values.
- Keep overview, charts, and grouped-usage accounting unchanged. The tree changes only the request-details page shape; this avoids changing billing/token totals as part of a display feature.
- Do not heuristically merge historical rows by timestamp or similar values. Existing unattributed duplicates remain standalone. Newly correlated routed attempts group deterministically.
- Do not expose internal correlation IDs in visible columns. IDs are opaque UI keys only.
- Persist and display only provider attempts that emit a CPA usage callback; do not synthesize transport-level attempts or billing records. CPA outer duplicate callbacks remain standalone unattributed records and continue contributing to overview/group totals.
- Keep `/usage/requests` and the dashboard management response backward-compatible at the field level: each top-level item retains all existing `usageRequestDetail` fields and gains optional `attempts` and `attempt_count`. Existing clients can continue reading the representative row.

## Data model and correlation

1. Extend `attributionResult` and `attributionMarker` in `attribution.go` with `RouterRequestID string` and `Attempt int`. Change `MarkRouted` to accept both values and copy them into the marker/result. Direct and unresolved attribution return zero values.
2. In `execute.go:executeWithHost` and `stream.go:executeStreamWithHost`, create one opaque request ID with standard-library `crypto/rand.Text()` after route matching and before the attempt loop. Pass the same ID and the loop's zero-based `attempt` to every `MarkRouted` call. This covers nonstream and stream execution, success on the first provider, eligible failures, and later fallback attempts without relying on CPA metadata that is not present on `UsageRecord`.
3. Add optional `router_request_id` and `router_attempt` fields to `storedUsageRecord` in `usage_types.go`; copy them in `storedRecordFromUsage` in `usage_record.go`. Keep `router_attempt` zero-based internally; ordering also uses `Sequence` as a deterministic tie-breaker.
4. Continue consuming one attribution marker per provider usage callback. CPA's later outer callback remains unattributed and therefore cannot become a false child of the router tree. This change does not invent correlation for callbacks the host did not report.

## Tree API semantics

1. Add `Attempts []usageRequestDetail` (`json:"attempts,omitempty"`) and `AttemptCount int` (`json:"attempt_count,omitempty"`) to `usageRequestDetail`. Top-level standalone records have no `attempts`; correlated routed parents have all matched provider details in `attempts`, ordered by `router_attempt`, then `Sequence`.
2. Build request-detail pages from logical top-level items in `usage_query.go`:
   - Records with non-empty `router_request_id` and `attribution == "routed"` group by that ID.
   - A missing ID, direct attribution, or unattributed attribution produces one standalone top-level item.
   - Feed the request-detail collector the complete retained snapshot, group first, then apply every request filter to logical items. `From`/`To` compare the parent `RequestedAt` (earliest attempt); attribution and router model use parent values; provider model, source, and service tier match when any attempt matches; result matches the final-attempt parent result. Standalone rows use their own values. A matching parent always returns its full forwarding path, including attempts whose individual timestamps or provider fields would not match.
   - Set `Total`, `Offset`, and `Limit` in `usageRequestPage` to logical top-level items. Slice only after groups are complete so fallback children never split across pages.
3. Derive the parent representative from the final attempt (highest `router_attempt`, then `Sequence`) so existing provider/source/service-tier/result columns describe the provider that completed or ended the logical request. Override parent-level fields as follows:
   - `RequestedAt`: earliest attempt; `Sequence`: earliest attempt sequence for stable ties.
   - `RouterModel`, `Attribution`: shared routed values.
   - Token/cache counters: sums across all attempts; sum each child's `EffectiveCacheReadTokens` and set `CacheHit` when that aggregate is nonzero. Estimated cost: sum all numeric fields (`ContextTokens`, billable/cache token counts, and USD amounts); set `Priced` only when every attempt is priced; set aggregate `Source`, `AccountingMode`, and `PriceServiceTier` empty and `TierThreshold` zero because per-provider provenance remains available on children.
   - `LatencyNS`, `GenerationNS`: sums across attempts, reflecting sequential fallback work.
   - `TTFTNS`: sum of each earlier attempt's latency plus final attempt TTFT; if final TTFT is absent, keep zero rather than manufacture a value.
   - `TPS`: summed output tokens divided by summed generation duration; zero when generation duration is zero.
   - `Failed` and `Result`: final attempt outcome. `AttemptCount`: total number of attempts in the group.
   - Other provider-specific fields remain those of the final attempt.
4. Sort logical parents before pagination with the existing `compareRequests` field semantics applied to the derived parent. Thus time/router/final-provider/source/service-tier/result use representative values; latency/TTFT/TPS/total tokens/cost use the aggregates above. Keep `Sequence` as deterministic tie-breaker and preserve asc/desc behavior for every `requestSortFields` entry.
5. In `(*usageStore).queryUsage`, keep existing per-record filtering for overview/groups, but feed every retained record to the logical request collector so it can group before filtering. Replace the raw-row indexed request path (`countUsageRecords` plus `indexedUsageRequestPage`) with that logical selection path for request details. Count, sort, and paginate only after complete groups are built and filtered.
6. Keep `usageSchemaVersion` at 1: optional payload/response fields decode compatibly, and changing this constant would reject existing version-1 legacy migration input in `usage_migration.go`.

## Dashboard tree

1. In `dashboard/shared.js`, add `expandedRequests: new Set()` to `usageState`. Keys are `router_request_id`; expansion survives table rerenders and refreshes while the page is open. Before each render, remove keys not present in the new page so sort/filter/page navigation cannot retain stale state.
2. In `dashboard/usage/tables.js`:
   - Render one parent `<tr>` for every top-level item.
   - For correlated items, render a disclosure `<button type="button">` inside the Router model cell before `routerDisplay(item)`. Give it `data-request-toggle`, `aria-expanded`, `aria-label="Expand attempts for <router model>"`/`"Collapse attempts for <router model>"`, and `aria-controls` listing that parent's child-row IDs.
   - Render each attempt immediately after its parent with collision-free DOM IDs derived from the parent's persisted `Sequence` plus attempt position, `class="request-attempt-row"`, and `hidden` while collapsed. Child cells use the existing `requestCell`; the Router model cell displays `Attempt N` instead of repeating the route alias. Preserve provider model, result, latency, token, and cost values from each attempt.
   - Standalone direct, unattributed, and historical rows render exactly once with no disclosure.
   - Keep page status based on `page.items.length` and `page.total`, so it reports logical requests rather than rendered child-row count.
3. Add delegated click handling to `initializeUsageTableEvents`: toggle the ID in `usageState.expandedRequests` and rerender the request table. Do not refetch or alter sorting/pagination.
4. In `dashboard/usage/styles.css`, add only tree-specific rules: unstyled disclosure button with a visible focus state, rotated chevron when expanded, muted/soft child-row background, and indentation in the child Router model cell. Retain hover, sticky header, horizontal scroll, and existing theme variables.
5. Keep collapsed as the default after initial load. Expansion is session/UI state only; do not add a saved preference.

## Assumptions & contingencies

- Treat untracked `cpa-plugin-model-combo` as generated build output because it is a 19 MiB Mach-O executable matching the project name; remove it rather than commit it. If it is an intended release artifact, revise the preflight before execution.
- Correlation begins with newly recorded rows only. Historical routed rows and CPA outer callbacks lack trustworthy logical IDs and stay standalone; no timestamp-based reconstruction or accounting deduplication is included.

## Implementation order

1. **Correlation plumbing** — update `attribution.go`, `execute.go`, `stream.go`, `usage_record.go`, and `usage_types.go`; update all callers and fixtures for the new `MarkRouted` signature.
2. **Logical request paging** — add group/aggregate helpers in `usage_query.go`, use them from `(*usageStore).queryUsage`, and preserve overview/group accumulation behavior.
3. **Tree rendering** — update `dashboard/shared.js`, `dashboard/usage/tables.js`, and `dashboard/usage/styles.css` using existing table/event conventions.
4. **Contract coverage** — update request API fixtures/assertions for the new optional response fields and logical pagination behavior.

## Critical files & anchors

- `attribution.go` — `attributionMarker`, `attributionResult`, and `MarkRouted`; durable correlation starts here.
- `execute.go` — `executeWithHost`; owns one nonstream logical ID and per-provider attempt indices.
- `stream.go` — `executeStreamWithHost`; mirrors correlation while preserving stream fallback cutoff after payload emission.
- `usage_query.go` — `(*usageStore).queryUsage`, `usageRequestSelection`, and `compareRequests`; grouping must precede request filtering, sorting, and paging without altering overview/group accounting.
- `dashboard/usage/tables.js` — `requestCell`, `renderRequestTable`, and `initializeUsageTableEvents`; render and toggle parent/child rows through existing table conventions.

## Verification

### Go tests

- `attribution_test.go`: a matched routed marker returns router alias, shared request ID, and exact attempt index; direct/unresolved results contain no router correlation.
- `execute_test.go`: one nonstream request gives all attempted providers the same non-empty ID and increasing indices; a separate request gets a different ID. Cover first-attempt success and failed-then-success fallback.
- `stream_test.go`: same assertions for stream fallback, including failure before any payload and no fallback after payload emission.
- `usage_store_test.go` / `usage_management_test.go`:
  - correlated attempts become one parent with ordered children and aggregated metrics;
  - direct, unattributed, and legacy routed payloads remain standalone;
  - logical `Total`, offsets, and limits never split children;
  - grouping precedes all request-detail filters; date uses parent time, attempt-valued filters match any child, result uses final attempt, and matched groups retain every child;
  - all `requestSortFields`, both orders, and existing tie behavior operate on parent representative/aggregate values;
  - an old JSON payload lacking new fields still decodes and pages;
  - overview/group totals still count stored usage records, not synthetic parents.

### Browser/E2E

Extend `tests/e2e/dashboard.test.mjs` with a deterministic mocked management response containing one two-attempt routed item and one standalone item. Verify:

- initial DOM has two visible parent/standalone rows and hidden attempt rows;
- disclosure has the expected accessible name, `aria-expanded="false"`, and valid `aria-controls` IDs;
- clicking reveals attempts in index order, updates `aria-expanded`/label, and clicking again hides them;
- provider/result cells retain each attempt's values;
- page status and next/previous enablement use logical request count, not child count;
- rerender from column visibility or refresh retains expansion for an item still on the page and drops it after navigation/filter removes it.

### Commands and smoke proof

1. Run focused Go tests for attribution, nonstream, stream, store, and management handlers.
2. Run full `go test ./...`.
3. Run `npm run test:e2e` for dashboard Playwright coverage.
4. Launch the existing dashboard test server, open Usage → Request Details in a real browser, expand a fallback request, and confirm parent/child ordering, focus visibility, horizontal scrolling, collapse behavior, and pagination text. Remove no data and run no migration.
