# Final Release Readiness Assessment

**Date:** 2026-10-18  
**Verdict:** `NO` — Production release blocked by unresolved gaps and unverified deployment.

---

## Release Status Matrix

| Category | Status | Evidence / Gap |
|----------|--------|---|
| **Repo/Plugin Identity & ABI** | PASS | Go 1.27.0 darwin/amd64. Observed: `make build` created `dist/model-combo.dylib` (target 13 linked 12). CPA v7.3.9 pinned in `go.mod` and CI workflow checkout at commit `61fdfc341b96178a8dcb53f2efc46cbc341d267c`. |
| **CI Gates** | PASS (local) | `make check` passed: normal tests 11.938s, race tests 48.417s, and `go vet ./...`. Exact F2.5.1 GitHub Actions execution remains unobserved; native integration failed before plugin registration because CLIProxyAPI crashed. |
| **Release Artifacts/Platforms** | BLOCKED | Release script started post-static-check (credential-free, nonpublishing); cancelled during image pull before cross-build, archive, checksum. Linux/amd64 `.so`, darwin amd64/arm64 `.dylib`, windows `.dll`, linux arm64 `.so` all remain unverified. |
| **Podman Deployment** | BLOCKED | `deploy/podman/compose.yaml` static facts correct: plugin bind mount `/CLIProxyAPI/plugins`, persistent named volume `cpa_auth` at `/root/.cli-proxy-api`, data volume `cpa_data` at `/CLIProxyAPI/data`. CPA image pinned `docker.io/eceasy/cli-proxy-api:v7.3.9`. **Runtime execution unverified:** Podman unavailable on audit workstation; no live container health check, networking, graceful shutdown, or volume persistence observed. |
| **Disposable CPA Integration** | BLOCKED | CI workflow defines CPA v7.3.9 integration test; pinned checkout at `61fdfc341b96178a8dcb53f2efc46cbc341d267c`. No local CPA host launch, auth flow simulation, or live plugin loading observed. |
| **Browser E2E** | BLOCKED | Chromium unavailable on audit workstation. E2E requires disposable loopback CPA, management key, mutation opt-in, and browser automation. No observed execution. |
| **Cross-Sprint Functional Gaps** | PARTIAL | **Durable combo-health:** PARTIAL (schema v1→v2, callback-persist, health/list and health/reset APIs, current-plan filtering, deep-copy restore; shutdown lifecycle verified locally via deterministic ABI test: seed obsolete row + MarkFailure, persist snapshot, prune 24h TTL, close and reopen confirms obsolete pruned and nonhealthy configured health persists. Test ✓ PASS deterministic 1.682s, race 3.555s. NOT production-ready: CPA integration unverified). See `docs/sprints/sprint-f1.md`. **Remaining:** Discovered-account API (UI restricted to raw textareas). Privacy-safe session affinity (no TTL cache or routing preference). |

| **Sprint F0 Fixes** | PARTIAL/FAIL | Isolated: weighted-round-robin fixture updated, deterministic tests added, focused weighted race test passed, formatting fixed. Pre-edit baseline (sprint-e): `go test ./...`, race, vet, shell syntax passed. Post-edit: full gate did not complete—tagged CPA v7.3.9 integration build succeeded but runtime failed with Go 1.27 CGO callback `unexpected return pc` at `abi.go:164`. Go 1.26 unavailable for fallback. See `docs/sprints/sprint-f0.md`. |
---

## Verified Static Configuration

### Podman Compose Deployment

- **Container:** `cpa-model-combo` (CPA v7.3.9)
- **Plugin Bind Mount:** `./plugins/` → `/CLIProxyAPI/plugins` (read-only)
- **Auth Persistence:** Named volume `cpa_auth` → `/root/.cli-proxy-api` (CPA account credentials)
- **Data Persistence:** Named volume `cpa_data` → `/CLIProxyAPI/data` (Model Combo usage database)
- **Logs:** Named volume `cpa_logs` → `/CLIProxyAPI/logs`
- **Network:** Default loopback `127.0.0.1:8317` (remote override via `CPA_BIND_HOST`)
- **Lifecycle:** Restart unless-stopped, 30s grace period, no privilege escalation

### CI Workflow (`.github/workflows/test.yml`)

- **Triggers:** `push` (main/development-phase), pull requests
- **Jobs:**
  - Format check: `gofmt`
  - Unit suite + race detector: `go test ./...`
  - Static analysis: `go vet`
  - CPA v7.3.9 integration test (pinned commit)
  - Native builds: `.so` (linux/amd64), `.dylib` (darwin), `.dll` (windows platform-specific)
- **CPA Lock:** Checkout at pinned commit `61fdfc341b96178a8dcb53f2efc46cbc341d267c` (v7.3.9 only)

---

## Unresolved Production Blockers

### FAIL (Functional Implementation Required)

1. **Discovered-Account & Health Management UI**
   Source: `docs/final-gap-closure.md` line 11
   Gap: No authenticated discovered-account API; health-list and health-reset endpoints exist but dashboard restricted to raw `combos` and `account_pools` textareas.

2. **Privacy-Safe Session Affinity**
   Source: `docs/final-gap-closure.md` line 12
   Gap: No affinity configuration interface, bounded TTL cache, or routing preference implementation.

### BLOCKED (Runtime Verification Required)

1. **Disposable CPA Integration**  
   Gap: CI workflow defines v7.3.9 test; no local CPA host launch or plugin live-load observed on audit workstation.

2. **Browser E2E**  
   Gap: Chromium unavailable. E2E suite requires disposable loopback CPA, management mutation opt-in, and browser automation.

3. **Release & Platform Verification**  
   Gap: CI workflows/scripts present; no external release process, binary distribution channel, or manual platform verification observed.

---

## Audit Environment Snapshot

| Tool | Status | Version/Path |
|------|--------|---|
| go | ✓ Ready | 1.27.0 darwin/amd64 |
| node | ✓ Ready | v22.23.2 |
| npm | ✓ Ready | 10.9.8 |
| docker | ✓ Ready | 29.6.2 |
| podman | ✗ Unavailable | — |
| chromium | ✗ Unavailable | — |

**Plugin Artifact:** `dist/model-combo.dylib` (12M, 2026-10-16)

---

## Closure Requirements

Closure requires **implementation of every functional gap plus observed execution for every blocked boundary.** All unresolved dependencies must be completed; all blocked categories must be live-verified.

**Status: Production release NOT READY. Verdict remains `NO`.**
---

## F2.5 Verification Update (2026-10-19)

### Recheck Against Release Blockers

| Phase | Status | Finding |
|-------|--------|---------|
| **Durable combo-health (F1)** | PASS | 1.682s deterministic; race 3.555s; closure `go test ./... -count=1` ✓ PASS 12.058s. All tests pass. NOT production-ready pending CPA integration verification. |
| **Focused WRR (F0→F2)** | PASS | 1.693s; semantics/priority/weight validation verified. Dashboard preservation confirmed. |
| **ABI health dispatch (F2 B)** | PASS | Route dispatch fixed; test passes. |
| **Discovered-account UI (F2 D)** | BLOCKED | **CPA v7.3.9 constraint:** ManagementRequest missing identity + host auth executor-only fields. Cannot authenticate. No authenticated discovered-account picker can be provided. |
| **Health dashboard (F2 E)** | PASS (local) | Panel/tab/styles/DOM verified; node syntax ✓; asset embed 10.613s; health endpoint 6.220s. Browser E2E remains unobserved: Docker lab first timed out pulling pinned `golang:1.26` at 600 seconds, then later completed; final bounded startup timed out at 600.25 seconds downloading Go dependencies, including CLIProxyAPI v7 `v7.3.9` and `go-sqlite3`. Compose never ran; Chromium remains unavailable. |
| **Session affinity (F2 F)** | BLOCKED | **CPA v7.3.9 constraint:** ExecutorRequest lacks session ID field. Config validation rejects enablement. SessionAffinity unit ✓ PASS 2.124s. Enabled affinity unavailable because trusted session identity absent. |

### CPA Integration Status

- **Native build:** Pre-host package compile blocked by pre-existing account_test.go compile errors (not introduced F2.5).
- **Unit closure:** `go test ./... -count=1` ✓ PASS 12.058s (macOS linker warning: 13.0→12.0, non-functional).
- **Browser E2E:** Unobserved. Docker lab `random_key` finite generator was fixed and verified. First pinned `golang:1.26` pull timed out at 600 seconds, then later completed. Docker builder VCS stamping failed; Docker-only `-buildvcs=false` was added and `sh -n` passed. Final bounded startup timed out at 600.25 seconds while downloading Go dependencies, including CLIProxyAPI v7 `v7.3.9` and `go-sqlite3`. Compose never ran. No lab CPA container/process, readiness, health, or browser E2E was observed. Workspaces were retained. No retry followed final timeout.
- **Native toolchain context:** CPA source requires Go `1.26.0`; failed local probe used host Go `1.27.0`. `tests/e2e/lab/lab.sh` uses Docker to build Linux c-shared plugin in `${CPA_E2E_GO_IMAGE:-docker.io/library/golang:1.26}`. Mismatch remains unproven; native validation must use CPA image/toolchain matching this build, or CI.

### Release Verdict Update

**No change to `NO`:** Blockers D (discovered-account) and F (session affinity) remain unresolved CPA v7.3.9 dependencies. F3 acceptance criteria absent; no F3 implementation claim. Production release blocked until:
1. CPA upgrades ManagementRequest and ExecutorRequest schemas (external dependency).
2. F3 acceptance defined.
3. Disposable CPA integration test passes.
4. Browser E2E (Chromium and Docker lab) verified.
