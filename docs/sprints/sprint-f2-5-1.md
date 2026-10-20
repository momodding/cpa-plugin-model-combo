# Sprint F2.5.1: Selector SWRR Integration Test Fix

## Plan
Work Package A hotfix: correct integration_test.go weighted-round-robin expected sequence to align selector's Stateless Weighted Round-Robin (SWRR) cycle behavior.

## Evidence
- **Baseline**: fdd93e36 (feat: add health dashboard)
- **Root Cause**: integration_test.go line 331 expected old grouped-RR pattern `[A,A,A,B,A,A,A,B]` instead of SWRR cycle `[A,A,B,A,A,A,B,A]`
- **Edit**: Changed wantWeightedModels slice to match selector's actual Stateless Weighted Round-Robin delivery order
- **Test Status**: PASS (3.416s with macOS warning), selector fully focused


## Work Package B: Health Dashboard Lifecycle
- **Edit**: Made Health refresh binding idempotent, stopped polling when Health loses focus, and resumed only when Health remains active and page becomes visible.
- **Race safety**: Added in-flight guard, abort controller, generation checks, and stale-response suppression for Health list requests.
- **Accessibility**: Fixed keyboard tab mapping for Health; existing tab ARIA selection and focus behavior now reaches all three tabs.
- **Rendering**: Circuit-open UI now requires a valid future `circuitOpenUntil`; expired circuit state reports half-open probe eligibility or occupancy.
- **Security**: Health list/reset continue through authenticated `requestManagementJSON`; rendered values use `textContent`; group-wide reset confirmation remains explicit.
- **Syntax**: PASS — `node --check dashboard/app.js && node --check dashboard/usage/tab.js && node --check dashboard/health/tab.js && node --check tests/e2e/dashboard.test.mjs`.
- **Behavior test status**: BLOCKED — `npm run test:e2e` cannot import `playwright-core` (`ERR_MODULE_NOT_FOUND`). After installing dependencies, it also requires a disposable loopback CPA instance, `DASHBOARD_URL`, `DASHBOARD_MANAGEMENT_KEY`, `DASHBOARD_E2E_ALLOW_MUTATIONS=1`, and Chromium.

## Native Integration
- **Status**: FAIL before plugin registration.
- **Command**: `CPA_SOURCE="$PWD/.cpa-source" go test -tags=integration . -run '^TestModelComboWithCLIProxyAPI$' -count=1 -v`; exit 1.
- **Cause observed**: CLIProxyAPI runtime `cgocallback` crash; this does not establish a plugin regression.
- **Toolchain context**: CPA source is clean at `61fdfc341b96178a8dcb53f2efc46cbc341d267c` and requires Go `1.26.0`; failed local probe used host Go `1.27.0`.
- **Docker lab**: `random_key` finite generator was fixed and verified. First pinned `golang:1.26` pull timed out at 600 seconds, then later completed. Docker builder then failed VCS stamping, indicating `-buildvcs=false`; this flag was added only to Docker build and `sh -n` passed. Final bounded startup timed out at 600.25 seconds while downloading Go dependencies, including CLIProxyAPI v7 `v7.3.9` and `go-sqlite3`. Compose never ran. No lab CPA container/process, readiness, health, or browser E2E was observed. Workspaces were retained. No retry followed final timeout.

## Work Package D: Regression Verification
- **C workflow audit**: No `.github` workflow changes found.
- **Normal test**: PASS (12.463s) — `go test ./... -count=1`.
- **Race test**: PASS (47.690s) — `go test -race ./... -count=1`.
- **Build**: PASS — `make build`.
- **Check**: PASS — `make check`; normal tests passed in 11.938s, race tests in 48.417s, then `go vet ./...` passed. macOS linker warnings only.
- **Native integration**: FAIL before registration — `CPA_SOURCE="$PWD/.cpa-source" go test -tags=integration . -run '^TestModelComboWithCLIProxyAPI$' -count=1 -v`. CLIProxyAPI exited before readiness with `exit status 2`: `runtime: g 23: unexpected return pc for runtime.cgocallback called from 0x10366c808`; `fatal error: unknown caller pc`.
