# Sprint F4 — Supported deployment hardening

## Status

**In progress.** F4 follows the F3.1 supported-platform gate.

## Scope

Make supported Linux and macOS deployments reliable across container, browser, persistence, and recovery workflows. Supported environments are Linux, including Docker/Podman, and macOS, including Colima.

Windows native runtime/deployment remains **deferred**, not a supported-platform pass. Windows tests and ACL validation remain preserved. Windows ACL diagnostic compilation is nonblocking backlog. Overall Actions status remains reported separately and is red when `windows-sqlite` fails.

## Work packages

- **Docker and Colima:** Validate repeatable image builds, startup, configuration, health checks, and local development through Docker on Linux and Colima on macOS.
- **Browser:** Validate browser-facing workflows against supported deployments, including service reachability, authentication/configuration boundaries, and useful failure reporting.
- **Podman:** Validate rootless Podman deployment on Linux, including equivalent networking, storage, health, and lifecycle behavior.
- **Persistence and recovery:** Validate persistent storage, restart behavior, backup/restore or recovery procedures, and failure reporting without masking data-loss conditions.
- **Linux/macOS release:** Define release checks and operator documentation for supported Linux and macOS artifacts and deployment paths, including Docker/Podman and Colima where applicable.

## Reporting separation

Supported-platform results MUST report Linux and macOS evidence separately from deferred Windows results. A failed Windows job MUST remain visible and MUST NOT be converted into a supported-platform pass. Actions health, Windows diagnostic backlog, and supported-platform acceptance are separate status fields.

## Observed evidence and blocker

- Docker client `29.6.2` and server `29.5.2` were available.
- Disposable lab was attempted with `CPA_E2E_ALLOW_START=1`.
- CPA plugin build remained in dependency downloads until harness stopped it at 600 seconds. CPA never started.
- Therefore plugin loading, routing, health, authentication, restart, and browser checks are **NOT PASS**. No fake PASS is claimed.
- During attempt, two macOS lab cleanup bugs were fixed in `lab.sh`: trailing-slash `TMPDIR` normalization and lowercase Compose project suffix.
- Builder cleanup and workspace cleanup were verified.
- Remaining blocker: complete dependency download and rerun disposable lab before claiming CPA startup or end-to-end checks.

## F4-C browser evidence

- Node `v22.23.2` and npm `10.9.8` were available.
- `test:e2e` is `node --test tests/e2e/dashboard.test.mjs`.
- `playwright-core` version is `1.63.0`.
- Chrome app was absent. `chromium`, `chromium-browser`, and `google-chrome` were unresolved.
- CPA lab remained unavailable because of the prior 600-second plugin-build timeout.
- Browser E2E was not run and is **NOT PASS**. No browser PASS is claimed.

## F4-D Podman evidence

- `podman --version`, `podman machine list`, and `podman info` each exited `127` with `command not found: podman`.
- `bash deploy/podman/preflight.sh` passed static template, secrets, compose-structure, loopback-default, and read-only config/plugins-mount checks; this is not runtime validation.
 
## F4-E persistence and recovery evidence

- Focused `go test . -run '^TestSchemaV1toV3Migration$' -count=1` timed out after 300 seconds without output or exit result.
- Earlier prefix test also timed out at 300 seconds.
- One process inspection after timeout found no `go test` or test executable.
 
## F4-F release packaging evidence

- Release script/workflow require exactly four supported archives: Linux amd64/arm64 and Darwin amd64/arm64.
- Windows target/archive/cardinality gate was removed only from release packaging. Windows tests and ACL validation remain unchanged.
- `bash -n .github/xgo/release.sh` passed.
 
- Nonpublishing xgo release build was attempted but stalled; worker was cancelled.
- Cleanup inspection found no xgo container/image and no `cpa-model-combo-release.*` temp directory.
- No release archives or checksums were produced or verified. Artifacts, checksums, and runtime compatibility remain **NOT PASS**.


- Supported Linux and macOS deployment paths have repeatable checks and documented recovery procedures.
- Docker, Colima, and Podman workflows report actionable failures.
- Browser workflows pass against supported deployments.
- Persistence and recovery behavior is verified for supported environments.
- Release checks identify supported artifacts and deployment targets without claiming Windows native support.
