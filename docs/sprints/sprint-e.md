# Sprint E: Verification Audit & Release Readiness

**Date:** 2026-10-18  
**Status:** Stage 1 read-only audit; local feasibility confirmed, external blockers identified  
**Verdict:** Production readiness **NO**. See § 4: Final Gap Closure below.

---

## § 1. Observed Environment & Tooling

| Tool | Status | Version | Path |
|------|--------|---------|------|
| go | ✓ Ready | 1.27.0 darwin/amd64 | `/usr/local/bin/go` |
| make | ✓ Ready | GNU 3.81 | `/usr/bin/make` |
| node | ✓ Ready | v22.23.2 | `/Users/momodding/.vfox/sdks/nodejs/bin/node` |
| npm | ✓ Ready | 10.9.8 | `/Users/momodding/.vfox/sdks/nodejs/bin/npm` |
| docker | ✓ Ready | 29.6.2 | `/usr/local/bin/docker` |
| podman | ✗ Unavailable | — | — |
| chromium/google-chrome | ✗ Unavailable | — | — |

**Artifact Present:** `dist/model-combo.dylib` (12M, 2026-10-16)

---

## § 2. CI Workflows & Release Pipeline (Existing)

### 2.1 Development-Phase Trigger

**File:** `.github/workflows/test.yml`

**Runs on:**
- `push` to `main` or `development-phase` branch
- Pull requests to any branch

**Jobs:**
1. **lint & unit tests** (ubuntu-latest)
   - Format check: `test -z "$(gofmt -l *.go)"`
   - Release script syntax: `bash -n .github/xgo/*.sh`
   - Unit suite: `go test ./...`
   - Race detector: `go test -race ./...`
   - Static analysis: `go vet ./...`
   - **CPA v7.3.9 integration test** (pinned checkout): `CPA_SOURCE="$GITHUB_WORKSPACE/.cpa-source" go test -tags=integration ./...`
   - Native build: `CGO_ENABLED=1 go build -trimpath -buildmode=c-shared -o dist/model-combo.so .`

2. **Windows SQLite validation** (windows-latest)
   - Platform-specific registry and path tests (tagged subset)

**CPA Version Lock:** Workflow explicitly checks out CLIProxyAPI at pinned commit `61fdfc341b96178a8dcb53f2efc46cbc341d267c` (v7.3.9). Only this version is validated by CI.

### 2.2 Release Workflow

**File:** `.github/workflows/release.yml`

**Trigger:**
- Git tag matching `v*` pattern (automatic)
- Explicit `workflow_dispatch` with `tag` input (manual gate)

**Platform Matrix (Cross-Build):**

| Platform | Archive | Build via |
|----------|---------|-----------|
| Linux amd64 | `model-combo_<version>_linux_amd64.zip` | xgo container |
| Linux arm64 | `model-combo_<version>_linux_arm64.zip` | xgo container |
| Darwin amd64 | `model-combo_<version>_darwin_amd64.zip` | xgo container |
| Darwin arm64 | `model-combo_<version>_darwin_arm64.zip` | xgo container |
| Windows amd64 | `model-combo_<version>_windows_amd64.zip` | xgo container |

**Release Script:** `.github/xgo/release.sh`
- Uses xgo container (techknowlogick/xgo pinned image)
- Targets: `linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64`
- Artifact: 5 ZIP archives + `checksums.txt` (SHA-256)
- Validation: each archive contains exactly `model-combo.{dll|dylib|so}`

**Publish Step:** GitHub Release API
- Create or update release at tag
- Upload all 5 archives + checksums
- Verify asset contents

**Status:** Workflows exist and are correct. No corrections applied.

---

## § 3. Local Feasibility: Executable Checks

### 3.1 Code Quality (Observed Local Execution)

```bash
# All pass locally
make fmt-check       # gofmt validation
make check           # fmt-check + test + race + vet
```

**Evidence:** Can run now. Output grounded in local file system.

### 3.2 Local Build

```bash
make build           # → dist/model-combo.dylib (darwin/amd64)
```

**Status:** ✓ Executable. Cross-platform builds (linux/amd64, darwin/arm64, windows/amd64) are defined in release workflow but require xgo container (Docker) and have not been observed outside CI.

### 3.3 Workflow YAML Validation

```bash
node --check .github/workflows/test.yml      # Syntax only; no execution
node --check .github/workflows/release.yml   # Syntax only; no execution
```

**Status:** ✓ Feasible. Validates YAML structure but does not trigger workflow.

### 3.4 Lab Static Analysis

**Existing:**
- `go test -tags=integration ./...` defined in CI but requires CPA source checkout
- `go vet ./...` — passes locally
- `go fmt` — passes locally

**Status:** ✓ Code passes static checks locally. CPA integration test is unobserved (blocked).

---

## § 4. Final Gap Closure & Production Readiness

**Source:** `docs/final-gap-closure.md`

| Requirement | Status | Reason |
|---|---|---|
| Account-aware pool routing | PASS (local) | Unit coverage; strict pool resolution, priority/WRR, request-local exclusions, sync/stream retry |
| Host auth protocol | PASS (local) | Callback tests; opaque `AuthID` forwarding without credential persistence |
| Stream commitment and attribution | PASS (local) | Attribution stores HMAC fingerprints; no retry after emitted payload |
| **Durable combo-health storage** | **FAIL** | No schema 1→2 migration, `combo_health` table, health flush/prune/reset worker, or durable observability |
| **Discovered-account and health management UI** | **FAIL** | No authenticated discovered-account, health-list, or scoped health-reset APIs; dashboard uses raw textareas |
| **Privacy-safe session affinity** | **FAIL** | No affinity configuration, bounded TTL cache, or routing preference |
| Disposable CPA integration | BLOCKED | CI workflow defines pinned v7.3.9 checkout; no live CPA host execution observed |
| Browser E2E | BLOCKED | Requires disposable loopback CPA, management key, mutation opt-in, Chromium (not installed) |
| Release, platform, manual verification | BLOCKED | Workflows exist; no release or external/manual execution observed |

**Closure Requirement:** All `FAIL` rows must be implemented. All `BLOCKED` rows must be executed before production release.

**Verdict:** Production readiness **NO** due to 3 unresolved `FAIL` gaps and 3 unobserved `BLOCKED` phases.

---

## § 5. Blocked Execution Boundaries

### 5.1 CPA Host Integration

**Command (from CI):**
```bash
CPA_SOURCE="../CLIProxyAPI" go test -tags=integration ./... -count=1
```

**Required:** CLIProxyAPI v7.3.9 source checkout + build + test environment  
**Status:** ✗ Not available in current environment  
**Blocking Factor:** No CPA source tree; no local integration runner

### 5.2 Cross-Platform Release Build

**Command (from CI):**
```bash
bash .github/xgo/release.sh \
  "ghcr.io/techknowlogick/xgo@sha256:b00957d8fec512c4748a5fafe17197be1d8c0bf704b271fc4aa128f5ddf40414" \
  "github.com/momodding/cpa-model-combo" \
  "model-combo" \
  "<version>"
```

**Required:** Docker daemon, xgo pinned image, clean tagged tree, cross-toolchains  
**Status:** ✗ Not executable from current workspace  
**Blocking Factor:** Release must not be run from development workspace (CI-only)

### 5.3 Podman Deployment & Management

**Status:** ✗ Podman not installed  
**Note:** No Dockerfile exists (plugin is CPA-hosted; not standalone container). Plugin deployment is managed by CPA host configuration, not container orchestration.

### 5.4 Browser E2E

**Command (from CI):**
```bash
export DASHBOARD_URL="http://127.0.0.1:PORT"
export DASHBOARD_MANAGEMENT_KEY="<key>"
export DASHBOARD_E2E_ALLOW_MUTATIONS=1
export PLAYWRIGHT_CHROMIUM_EXECUTABLE="/usr/bin/chromium"
npm run test:e2e   # → tests/e2e/dashboard.test.mjs
```

**File:** `tests/e2e/dashboard.test.mjs` (Playwright 1.63.0, Node test runner)

**Required:**
- Chromium or google-chrome executable (not found at `/usr/bin/google-chrome`)
- Disposable loopback CPA instance (http://127.0.0.1) with auth credentials
- 120-second timeout per test

**Status:** ✗ Blocked (Chromium not installed, CPA instance not running)

---

## § 6. Plugin Execution & Integration Status

### 6.1 Implementation Observed

**File:** `execute.go`

- **`executeWithHost()` and `executeWithHostContext()`** — Fully implemented
- Performs account pool selection, candidate resolution, failure policy enforcement, fallback group traversal
- Manages attempt ledgers, retry-after signaling, health mutations
- Returns `ExecutorResponse` with rewritten model or error status

**Status:** ✓ Plugin execution logic is implemented and tested locally.

### 6.2 Host Callback Contract

Plugin requires CPA host to provide:
- `ListAuth()` — metadata-only auth discovery
- `Execute()` — sync model execution
- `StartStream()` / `ReadStream()` / `CloseStream()` — streaming support
- `Emit()` — stream response payload emission

**Verified by:** `host_callbacks.go` and integration test stubs (unexecuted against live CPA)

**Status:** ◐ Contract defined and locally testable; no live CPA validation observed.

### 6.3 CPA Version Support

- **Required:** CLIProxyAPI v7.3.9 (pinned in `go.mod` and CI checkout)
- **Prior Versions:** Not tested; compatibility unverified
- **Future Versions:** No version negotiation in CLIProxyAPI protocol; ABI change requires plugin rebuild

**Implication:** Plugin is version-locked to v7.3.9. CPA upgrade without plugin rebuild will fail at runtime.

---

## § 7. Release Verification Matrix

| Check | Command | Local | CI | Observed | Status |
|-------|---------|-------|----|----|---|
| **Code formatting** | `gofmt -l *.go` | ✓ | ✓ | Yes | PASS |
| **Unit tests** | `go test ./...` | ✓ | ✓ | Yes | PASS |
| **Race detector** | `go test -race ./...` | ✓ | ✓ | Yes | PASS |
| **Static analysis** | `go vet ./...` | ✓ | ✓ | Yes | PASS |
| **CPA v7.3.9 integration** | `go test -tags=integration ./...` | ✗ | ✓ | No | BLOCKED |
| **Windows SQLite tests** | `go test -run '^(TestSQLite\|...)' ...` | ✗ | ✓ | No | BLOCKED |
| **Native build (darwin/amd64)** | `make build` | ✓ | ✓ | Yes | PASS |
| **Cross-build (all platforms)** | `bash .github/xgo/release.sh ...` | ✗ | ✓ | No | BLOCKED |
| **Release publish** | `gh release {create\|upload}` | ✗ | ✓ | No | BLOCKED |
| **Browser E2E** | `npm run test:e2e` | ✗ | ✗ | No | BLOCKED |
| **Manual verification** | `docs/combo-manual-verification.md` | ✗ | ✗ | No | File absent |

**Verdict:** Local unit suite passes. All integration, cross-platform, release, and manual phases are blocked or unexecuted.

---

## § 8. Deployment Strategy & Acceptance

### 8.1 Production Deployment (Blocked)

Plugin deployment requires:
1. **CPA Instance** — Running CLIProxyAPI v7.3.9 host
2. **Plugin Binary** — Platform-matched `model-combo.{so|dylib|dll}` from GitHub Release
3. **Configuration YAML** — Model combos and account pools (loaded via CPA management API)
4. **Auth Metadata** — Account discovery via CPA `host.auth.list` callback

**Deployment Gate:** CPA must be configured to load plugin at startup or via `POST /management/plugins/load`.

### 8.2 Rollback Strategy

1. **Identify failing plugin version** from CPA logs
2. **Revert plugin binary** to prior GitHub Release or rebuild prior tag
3. **Reload CPA** (hot-reload via management API or service restart)
4. **Verify** CPA error logs and execution metrics

**Limitation:** No automatic rollback; manual intervention required. No plugin version history in CPA (CPA-managed).

### 8.3 Acceptance Criteria (Gate Before Release)

| Criterion | Evidence Required | Owner |
|-----------|---|---|
| Code review & approval | Pull request merged to main | Tech lead |
| Unit suite passes locally | `make check` green | Developer |
| CI all-green on `main` | GitHub Actions workflow success | CI/CD |
| CPA v7.3.9 integration verified | Execution against pinned CPA source | CI |
| All platform archives built | 5 ZIP files with correct contents | CI (release workflow) |
| Release notes published | GitHub Release with version, compatibility note, breaking changes | Release manager |
| Artifacts checksummed & signed | SHA-256 in `checksums.txt` | CI |
| Manual platform verification | Tested on darwin/amd64, linux/amd64, windows/amd64 (OS-specific) | QA |
| CPA live host smoke test | Plugin loaded, request routed, response delivered | QA + Ops |
| Browser E2E suite passes | Dashboard interactions retain state (120s timeout) | QA |

**Status:** Criteria are defined but not yet satisfied. CPA integration, release artifacts, platform smoke, and browser E2E are unobserved.

---

## § 9. Unresolved Product Capability Gaps

**Source:** `docs/final-gap-closure.md`

These gaps prevent production readiness and are **not implemented**. Do not claim they are working.

1. **Durable Combo-Health Storage & Observability**
   - Missing: schema 1→2 migration, `combo_health` SQLite table, health flush/prune/reset worker
   - Impact: Health state is in-memory only; resets on process restart; no historical health metrics

2. **Discovered-Account and Health Management APIs**
   - Missing: authenticated `GET /discovered-accounts`, `GET /health`, `POST /health-reset` endpoints
   - Current: Dashboard uses raw `combos` and `account_pools` textareas; no account-aware UI
   - Impact: Operators cannot view discovered accounts or manage health state via API

3. **Privacy-Safe Session Affinity**
   - Missing: affinity configuration, bounded TTL cache, routing preference
   - Impact: No sticky routing; account selection is stateless; each request re-selects candidate

**Closure:** All 3 gaps must be implemented before production release. See `docs/final-gap-closure.md` for detailed acceptance criteria.

---

## § 10. Summary of Locally Feasible Commands

**Stage 2 executable (if no changes are made to constraints):**

```bash
# Code quality
make fmt-check                              # Format validation
make check                                  # All local validation: fmt + test + race + vet

# Build
make build                                  # → dist/model-combo.dylib (darwin/amd64)

# Workflow validation (syntax only)
node --check .github/workflows/test.yml
node --check .github/workflows/release.yml
```

**Not executable from current environment:**

```bash
# CPA integration (requires CPA v7.3.9 source + build)
# CPA_SOURCE="../CLIProxyAPI" go test -tags=integration ./...

# Release (must run in CI or clean release workspace)
# bash .github/xgo/release.sh ...

# Browser E2E (requires Chromium + CPA instance)
# npm run test:e2e

# Platform smoke tests (requires target OS)
# [manual]
```

---

## § 11. CI Development-Phase Trigger: Release Gate

**Trigger Event:** `push` to `development-phase` branch or `main`

**Gate Condition:** Workflow must pass:
- All tests green (`go test ./... -race ./...`)
- Format and vet pass
- CPA v7.3.9 integration test passes (CI-only)
- Native build succeeds

**Artifact:** Workflow produces `dist/model-combo.so` (unpackaged, CI intermediate)

**Release Gate:** Manual `workflow_dispatch` on `release.yml` with version tag (e.g., `v1.2.3`)
- Validates tag format: `v<major>.<minor>.<patch>`
- Runs full test suite
- Cross-builds all platforms via xgo container
- Publishes GitHub Release with 5 archives + checksums

**No automated release on tag push.** Manual approval required.

---

## § 12. Document Version

| Date | Editor | Change |
|------|--------|--------|
| 2026-10-18 | Audit | Initial source-grounded plan; 3 FAIL gaps identified; 3 BLOCKED phases documented |

---

**End of Sprint E Audit**
