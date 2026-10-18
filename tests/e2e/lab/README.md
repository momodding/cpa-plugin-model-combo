# Disposable Podman E2E Lab

This lab starts a new local CLIProxyAPI v7.3.9 instance with a freshly built Linux `model-combo.so`.

## Prerequisites

Before running the lab, verify:

- **Podman CLI**: Install [Podman](https://podman.io) matching your OS (macOS, Linux, Windows).
  ```sh
  podman --version  # e.g., podman version 5.0.0
  ```
  
- **Podman machine** (macOS/Windows only):
  ```sh
  podman machine list
  # If no machine, create one:
  podman machine init --cpus 4 --memory 4096
  podman machine start
  ```

- **Go 1.26+**: For building `model-combo.so` inside container. Host Go not required; container uses `golang:1.26`.
  ```sh
  go version  # e.g., go version go1.26.0 darwin/amd64
  ```

- **npm and Node 20+**: For browser test runner (Playwright).
  ```sh
  npm --version && node --version
  # e.g., npm 10.5.0, v20.10.0
  ```

- **Chromium or Google Chrome**: For browser automation. Lab searches `/usr/bin/google-chrome` by default.
  ```sh
  which google-chrome || which chromium || which chromium-browser
  # If not found, set: export PLAYWRIGHT_CHROMIUM_EXECUTABLE=/path/to/chrome
  ```

- **Network access**: CPA pulls images from Docker Hub; Go build pulls from GitHub releases.

## Environment Variables

Customize lab behavior without editing scripts:

| Variable | Default | Purpose |
|----------|---------|---------|
| `CPA_E2E_ALLOW_START` | (unset) | **REQUIRED**. Set to `1` to permit container startup. |
| `CPA_E2E_PORT` | `18317` | Local port for CPA API and dashboard. Change to avoid conflicts. |
| `CPA_E2E_IMAGE` | `docker.io/eceasy/cli-proxy-api:v7.3.9` | CPA image tag. Override to test different versions. |
| `DASHBOARD_URL` | (auto) | CPA dashboard URL. Printed by `up`; used by browser test. |
| `DASHBOARD_MANAGEMENT_KEY` | (auto) | Dashboard auth key. Printed by `up`; used by browser test. |
| `DASHBOARD_E2E_ALLOW_MUTATIONS` | (unset) | **REQUIRED for browser test**. Set to `1` to permit mutations. |
| `PLAYWRIGHT_CHROMIUM_EXECUTABLE` | `/usr/bin/google-chrome` | Path to Chromium/Chrome binary. |

## Setup and Startup

```sh
# Explicit opt-in to prevent accidental container startup
CPA_E2E_ALLOW_START=1 tests/e2e/lab/lab.sh up
```

The script:
1. Creates a private temporary workspace in `/tmp/cpa-model-combo-e2e.XXXXXX` (unique per run).
2. Generates random API key and management key (printed to stdout; copy for browser test).
3. Renders `config.yaml` from `config.yaml.template` with keys and inline routes.
4. Validates `compose.yaml` syntax and environment substitution.
5. Builds `model-combo.so` inside a transient `golang:1.26` container (CGO_ENABLED=1, linux/amd64).
6. Starts CPA via `podman compose up -d` (detached).
7. Prints exports for browser test (stdout; save for next step).

**Example output:**
```
CPA E2E workspace: /tmp/cpa-model-combo-e2e.abc123
CPA listening on http://127.0.0.1:18317
export DASHBOARD_URL=http://127.0.0.1:18317
export DASHBOARD_MANAGEMENT_KEY=aAbBcCdDeEfFgGhHiIjJkKlLmMnNoOpPqQrRsStT
```

## Verify Startup

```sh
# Check CPA container is running
podman ps | grep cpa-model-combo

# View startup logs
podman compose -f tests/e2e/lab/compose.yaml logs

# Test API (requires copied API key from setup)
export CPA_API_KEY=<from-setup-output>
curl -s http://127.0.0.1:18317/health \
  -H "Authorization: Bearer $CPA_API_KEY" | jq .

# Open dashboard in browser
# http://127.0.0.1:18317 (dashboard key from setup)
```

## Browser E2E Test

Browser test verifies dashboard tab retention, route management, and usage data flow:

```sh
# Copy env exports from 'up' output:
export DASHBOARD_URL=http://127.0.0.1:18317
export DASHBOARD_MANAGEMENT_KEY=aAbBcCdDeEfFgGhHiIjJkKlLmMnNoOpPqQrRsStT

# REQUIRED: opt-in to mutations (saves routes, clears config)
export DASHBOARD_E2E_ALLOW_MUTATIONS=1

# Optional: override Chromium/Chrome path
# export PLAYWRIGHT_CHROMIUM_EXECUTABLE=/usr/bin/chromium

# Run tests
npm run test:e2e
```

**Test behavior:**
- Connects to disposable CPA (created by lab).
- Tests dashboard tab navigation, route add/remove, usage refresh.
- Mutates configuration (opt-in via `DASHBOARD_E2E_ALLOW_MUTATIONS=1`).
- Does NOT persist state to production; safe to run repeatedly.
- Requires Chromium binary; set `PLAYWRIGHT_CHROMIUM_EXECUTABLE` if not in default path.

**Exit codes:**
- `0`: All tests passed.
- `1`: Test failure (assertion, timeout, network).
- `2`: Missing env var or invalid URL.

## Cleanup and Removal

```sh
# Graceful cleanup (removes containers, preserves workspace for re-run)
tests/e2e/lab/lab.sh down /tmp/cpa-model-combo-e2e.abc123
```

The script:
1. Validates workspace path (must be in `/tmp/cpa-model-combo-e2e.*` pattern).
2. Requires marker file `.cpa-model-combo-e2e-lab` to prevent accidental deletion.
3. Runs `podman compose down --volumes --remove-orphans` (removes containers and local volumes).
4. Deletes workspace directory (`rm -rf`).

**Safety boundary:**
- Lab only removes workspaces it created (marked with `.cpa-model-combo-e2e-lab`).
- Workspace must be in `/tmp/cpa-model-combo-e2e.*` (temporary path only).
- Does NOT delete host config, auth, logs, or production volumes.

## Exit Codes and Error Recovery

### `lab.sh up`

| Code | Meaning | Recovery |
|------|---------|----------|
| `0` | Success; workspace and exports printed | Proceed to browser test or cleanup |
| `1` | Podman not found, compose validation failed, or build error | Check `podman version`, verify compose.yaml syntax, see logs below |
| `2` | Usage error (missing `CPA_E2E_ALLOW_START=1`) | Re-run with `CPA_E2E_ALLOW_START=1` |
| other | Unexpected error (Go build, plugin load, network) | Check podman logs |

### `lab.sh down`

| Code | Meaning | Recovery |
|------|---------|----------|
| `0` | Success; workspace deleted | Workspace is gone; re-run `up` if needed |
| `1` | Podman not found | Install Podman; check PATH |
| `2` | Workspace path invalid or safety check failed | Use exact path from `up` output; do not edit path manually |
| other | Podman compose failed | Check `podman volume ls`, manual cleanup: `podman volume rm cpa_*` |

### Debugging Lab Issues

**CPA fails to start:**
```sh
# View detailed logs
podman logs cpa-model-combo

# Check plugin was built
podman volume inspect cpa_plugins

# Verify config syntax
podman compose -f tests/e2e/lab/compose.yaml config
```

**Browser test times out:**
```sh
# Ensure CPA is running
curl http://127.0.0.1:18317  # may show 401 (no auth), that's OK

# Check Chromium path
which google-chrome || echo "Chrome not found"

# Manually set executable
export PLAYWRIGHT_CHROMIUM_EXECUTABLE=$(which chromium)
npm run test:e2e
```

**Podman socket connection refused:**
```sh
# On macOS/Windows, ensure machine is started
podman machine start

# On Linux, verify socket
ls -la /run/podman/podman.sock  # rootful
ls -la /run/user/$(id -u)/podman/podman.sock  # rootless
```

**Port already in use:**
```sh
# Use different port
CPA_E2E_PORT=18318 tests/e2e/lab/lab.sh up

# Export for browser test
export DASHBOARD_URL=http://127.0.0.1:18318
export DASHBOARD_MANAGEMENT_KEY=<from-up-output>
```

## Safety Boundary

Lab is isolated; does NOT:
- Mount host system directories outside workspace.
- Access production CPA config, auth, plugins, or logs.
- Expose CPA to the network beyond loopback (127.0.0.1).
- Persist state to the host or version control.
- Require elevated permissions (rootless Podman supported).

CPA binds only to `127.0.0.1:${CPA_E2E_PORT}` (default 18317). Fake model targets (127.0.0.1:19080) are unstarted; provide a mock provider if testing route traffic.

## Differences from Production Deployment

Lab is **disposable and ephemeral**; production is **persistent and managed**:

| Aspect | Lab | Production |
|--------|-----|-----------|
| Lifetime | One test run; deleted after `down` | Long-lived; managed by init system or orchestrator |
| Storage | Temporary volumes; cleared on `down` | Named volumes; preserved across upgrades |
| Config | Rendered from template each run | Pre-created; version controlled or secrets management |
| Keys | Random per run; printed to stdout | Pre-shared via secrets manager (Vault, CI secrets, etc.) |
| Backup | None (disposable) | Regular snapshots of `/CLIProxyAPI/data` volume |
| Networking | Loopback only (127.0.0.1) | May expose via reverse proxy (nginx, Caddy) |
| Credentials | Fake provider on loopback | Real provider credentials |
| Monitoring | Lab logs only | CPA usage DB, request logs, dashboards |

For production, use `deploy/podman/compose.yaml` and follow the [Production Deployment Guide](../../deploy/podman/README.md).
Lab binds CPA only to `127.0.0.1`. It does not mount `../cliproxyapi-podman`, user auths, source plugin state, source logs, source static files, or OAuth callback ports. Fake model targets point to an unstarted loopback endpoint; provide a disposable mock provider before sending route traffic.
