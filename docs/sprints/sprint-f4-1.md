# Sprint F4.1 — Disposable E2E lab hardening

## Status

**Implementation evidence documented; runtime validation in progress/unobserved.**

## Source-backed implementation evidence

- **Dynamic loopback port:** `compose.yaml` publishes CPA target port `8317` with `CPA_E2E_PORT` defaulting to `0`; `lab.sh` discovers the assigned port and probes `127.0.0.1`.
- **Unique project/workspace:** `lab.sh` creates a private temporary workspace and unique Compose project suffix. Generated config, auth, plugins, logs, and static files remain scoped to that workspace.
- **State ownership checks:** lab containers carry Compose project, service, and lab marker labels. Lifecycle operations validate expected ownership before inspecting or removing state. The workspace marker `.cpa-model-combo-e2e-lab` records the generated Compose project (`project=<name>`). `require_workspace` rejects workspace paths containing `..` or `/./`, requires the stored project to match `cpa-model-combo-e2e-*`, and refuses when marker project differs from state project.
- **Failed-start diagnostics:** when startup fails, the cleanup trap captures `compose logs` through a key-redaction filter to `<workspace>/logs/startup-failure.log` before targeted Compose project teardown. The log path is printed to stderr. Capture does not require a state file.
- **No key output:** `up` prints a dashboard command that reads the management key from the workspace state file at run time, not the key itself. `logs` output is redacted for the generated API and management keys.
- **Fake provider service DNS:** `config.yaml.template` routes provider traffic to `http://fake-provider:19080/v1`; Compose places both services on internal `lab` network. Provider health uses `/health`.
- **Dedicated caches:** plugin builds use separate Go module and build cache directories under configurable `CPA_E2E_CACHE_DIR`, outside disposable workspace state.
- **Browser setup:** `package.json` uses `playwright`, and `dashboard.test.mjs` defaults `chromium.launch()` to Playwright-managed browser provisioning. Installation and execution are unobserved.

## Static verification observed

- `sh -n tests/e2e/lab/lab.sh` passed.
- `docker compose -f tests/e2e/lab/compose.yaml config` completed in a private temporary workspace with exit 0.
- `node --check tests/e2e/dashboard.test.mjs` passed.

## Runtime boundary

Lab startup attempt began, but tool harness failed with HTTP 400 before outcome. No runtime PASS is claimed. Streaming, persistence, browser execution, CI, cleanup, and supported-platform acceptance remain unobserved. Fake-provider streaming code is source-present evidence only.
