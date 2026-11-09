<!-- source-branch: omp/investigate-router-attribution-2 -->
<!-- work-branch: omp/fix-management-plugin-page -->

# Fix blank Model Combo management plugin page

## Context

`/management.html#/plugin-pages/model-combo/0` resolves the registered resource and receives `GET /v0/resource/plugins/model-combo/config` with HTTP 200, but the embedded page stops during JavaScript bootstrap. `dashboard/shared.js` no longer defines `usageState.group`, while `dashboard/app.js` calls `applyUsagePreferences({})` at startup and `dashboard/usage/tab.js` immediately writes `usageState.group.limit`; this throws before `loadConfiguration()` can reveal `#workspace`. Recent CPA logs confirm successful resource responses without the subsequent `/v0/management/plugins/model-combo/config` request seen during working sessions.

## Approach

1. Restore the usage-group paging state contract in `dashboard/shared.js` without changing request-tree behavior:
   - Keep `expandedRequests:new Set()` in `usageState`.
   - Add the existing group defaults immediately before the request defaults: `group:{dimension:'provider_model',sort:'total_tokens',order:'desc',offset:0,limit:50}`.
   - Keep `request:{sort:'time',order:'desc',offset:0,limit:50}` unchanged.
   - Do not add defensive optional chaining in `applyUsagePreferences`; `dashboard/usage/tab.js`, `dashboard/usage/tables.js`, and `dashboard/usage/pricing.js` require a complete shared state object, and restoring that invariant fixes every caller once.

2. Add regression checks to both dashboard assembly and management ABI coverage:
   - In `TestAssembleModelRouterDashboardEmbedsEveryAssetOnce` in `dashboard_test.go`, require the assembled inline script to contain both complete `group` and `request` state initializers. This catches an embedded-source regression before management handling.
   - In `TestModelRouterManagementDashboardReusesCPAMCSessionAndTheme` in `management_test.go`, require the HTTP 200 management response body to contain the complete group initializer and startup call `applyUsagePreferences({})`. This verifies the resource served through `management.handle` contains the state needed by its first bootstrap consumer.
   - Keep existing registration/resource-path assertions unchanged: current CPA logs show `/v0/resource/plugins/model-combo/config` returns 200, so route registration is not the failing layer.

3. Rebuild and install the corrected Linux/amd64 plugin into the current mounted CPA deployment only after source tests pass:
   - Build from the committed work branch with `.github/xgo/release.sh`, pinned image `ghcr.io/techknowlogick/xgo@sha256:b00957d8fec512c4748a5fafe17197be1d8c0bf704b271fc4aa128f5ddf40414`, module `github.com/markhuangai/cpa-plugin-model-combo`, plugin ID `model-combo`, and the repository version from `Makefile` (`0.4.2`). The script uses `git archive HEAD`, so this step must run after the fix commit exists.
   - Extract `model-combo.so` from `dist/model-combo_0.4.2_linux_amd64.zip` to a temporary file, verify with `file` that it is an x86-64 ELF shared object, then replace `../cliproxyapi-podman/plugins/linux/amd64/model-combo.so`. Preserve no compatibility copy in the scanned plugin directory because CPA would attempt to load it as another plugin.
   - Restart only the `cliproxyapi` service with `podman compose restart cliproxyapi` from `../cliproxyapi-podman`; do not alter `config.yaml`, databases, usage history, or other plugins.

## Critical files & anchors

- `dashboard/shared.js` — `usageState` initializer; missing `group` object causes the bootstrap exception.
- `dashboard/usage/tab.js` — `applyUsagePreferences`; first startup consumer at lines 187–207 proves all required group fields.
- `dashboard/app.js` — top-level bootstrap order; `applyUsagePreferences({})` runs before `loadConfiguration()`.
- `dashboard_test.go` — `TestAssembleModelRouterDashboardEmbedsEveryAssetOnce`; source-assembly regression guard.
- `management_test.go` — `TestModelRouterManagementDashboardReusesCPAMCSessionAndTheme`; management ABI response contract.

## Verification

1. From repository root, run `node --check dashboard/shared.js` and `go test ./... -run 'TestAssembleModelRouterDashboardEmbedsEveryAssetOnce|TestModelRouterManagementDashboardReusesCPAMCSessionAndTheme'`. Expected: JavaScript parses and both tests pass with the restored state markers.
2. Run `go test ./...`. Expected: full package passes; existing request-tree, management, and usage behavior remains intact.
3. After deployment restart, inspect new CPA log lines. Expected: plugin loads/registers successfully, `GET /v0/resource/plugins/model-combo/config` returns 200, and opening the hash route causes subsequent successful `GET /v0/management/plugins/model-combo/config`, `/v0/management/api-keys`, and `/v1/models` requests rather than stopping after the resource response.
4. Open `http://127.0.0.1:8317/management.html#/plugin-pages/model-combo/0` in the management UI. Expected observable behavior: iframe displays Model Combo header and visible Configuration workspace; switching to Usage tracking renders controls without a page error. In browser console, require no `Cannot read properties of undefined` error involving `usageState.group`.
5. Run the existing browser E2E only against this disposable/local CPA instance with `DASHBOARD_URL=http://127.0.0.1:8317/v0/resource/plugins/model-combo/config`, `DASHBOARD_MANAGEMENT_KEY` set to the plaintext local management key, `DASHBOARD_E2E_ALLOW_MUTATIONS=1`, and `PLAYWRIGHT_CHROMIUM_EXECUTABLE` pointing to installed Chromium. Expected: `npm run test:e2e` passes. If no plaintext key or Chromium is available, record this check unavailable; steps 3–4 remain required and must prove the actual management hash page.

## Assumptions & contingencies

- Current mounted deployment at `../cliproxyapi-podman` is the target because its logs contain the reported hash-route requests and its `compose.yaml` mounts `./plugins` at `/CLIProxyAPI/plugins`. If that service is no longer the reported target during execution, apply the same tested Linux/amd64 artifact to the active target's configured plugin directory instead and restart only that CPA service.
- The resource endpoint is healthy, not missing: recent logs show HTTP 200 for `/v0/resource/plugins/model-combo/config`. If post-restart logs show 404 instead, stop UI debugging and verify plugin load/registration errors first; do not change the registered `/config` resource path or hash-route index.
- Replacing the currently loaded shared object requires a CPA restart; hot replacement alone is not accepted as verification.
