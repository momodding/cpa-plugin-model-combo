# CLIProxyAPI + Model Combo Deployment Guide

Production deployment of CLIProxyAPI v7.3.9 with Model Combo plugin using Podman.

## Architecture

- **Image**: `docker.io/eceasy/cli-proxy-api:v7.3.9` (official CPA, no custom build)
- **Plugin**: `model-combo.so` (linux/amd64), `model-combo.dylib` (darwin, host-only), `model-combo.dll` (windows, host-only)
- **Storage**: Named Podman volumes for auth, plugins, usage data, logs (preserved on container restart/upgrade)
- **Networking**: Loopback bind (127.0.0.1) by default; rootless Podman supported
- **Lifecycle**: Graceful shutdown (30s grace period), health checks optional, restart unless-stopped

## Quick Start

### 1. Prerequisites

```bash
# Ensure Podman is installed and daemon is running
podman --version
podman ps  # should not error

# For rootless Podman, initialize user namespace (macOS/Linux)
# podman machine init --cpus 4 --memory 4096  # macOS
# podman machine start
```

### 2. Extract Plugin Binary

Download or build `model-combo.so` (linux/amd64) matching your target platform.

**Build locally:**
```bash
cd /path/to/cpa-model-combo
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -trimpath -buildmode=c-shared \
  -ldflags="-s -w -X main.pluginVersion=$(git describe --tags --always)" \
  -o dist/model-combo.so .
```

**Or obtain verified binary from release process:**
```bash
# Download prebuilt binary from official release channel
# Verify checksum before extraction to deployment directory
# (See project release documentation for verification steps)

# Example: extract downloaded archive to plugins/
mkdir -p deploy/podman/plugins
cp /path/to/downloaded/model-combo.so deploy/podman/plugins/
chmod 755 deploy/podman/plugins/model-combo.so
```

**Architecture validation:**
```bash
file deploy/podman/plugins/model-combo.so
# Expected: ELF 64-bit LSB shared object, x86-64, version 1 (GNU/Linux)
# Ensure architecture matches container: linux/amd64
```

### 3. Create Configuration

```bash
cd deploy/podman

# Copy environment template
cp .env.example .env

# Copy and customize CPA config (file, not directory)
cp config.yaml.template config.yaml

# Edit config.yaml
# - Replace @API_KEY@, @MANAGEMENT_KEY@, @PROVIDER_NAME@, @BASE_URL@, @API_KEY_VALUE@
# - Configure route aliases and model targets
# - Set data_path: /CLIProxyAPI/data/model-combo-usage.db (read from compose)
# - Set plugins.dir: /CLIProxyAPI/plugins (read from compose)

nano config.yaml
```

**Security best practice:** Never commit real API keys. Use `.env.local` (gitignored) or CI secrets:

```bash
# .env.local (gitignored)
CPA_API_KEY=$(openssl rand -hex 16)
CPA_MANAGEMENT_KEY=$(openssl rand -base64 36 | tr -d '=+/' | cut -c1-48)
CPA_PROVIDER_API_KEY=$(cat /path/to/secret/provider-key)
```

### 4. Prepare Volumes

```bash
# Create plugin directory (will be mounted as /CLIProxyAPI/plugins)
mkdir -p plugins
cp dist/model-combo.so plugins/
chmod 755 plugins/model-combo.so

# CPA will initialize auth, data, logs, static on first run
# Do not pre-create; Podman volumes are auto-initialized
```

### 5. Start CPA

```bash
# Load environment
source .env  # or .env.local

# Validate compose syntax
podman compose -f compose.yaml config >/dev/null && echo "Compose YAML is valid"

# Start in foreground (logs visible)
podman compose -f compose.yaml up

# Or start in background
podman compose -f compose.yaml up -d

# Verify container is running
podman ps | grep cpa-model-combo

# View logs
podman compose -f compose.yaml logs -f
```

### 6. Verify Deployment

```bash
# Check container health
podman ps | grep cpa-model-combo

# Test CPA API (requires valid API key)
curl -s -X POST http://127.0.0.1:8317/v1/chat/completions \
  -H "Authorization: Bearer $CPA_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model": "your-alias", "messages": [{"role": "user", "content": "test"}]}' \
  | jq .

# Access dashboard (requires management key)
# Open http://127.0.0.1:8317/ in browser, enter $CPA_MANAGEMENT_KEY

# Check plugin was loaded
podman compose -f compose.yaml logs | grep -i "model-combo"

# Inspect volumes
podman volume inspect cpa_data
podman volume inspect cpa_auth
```

## Upgrade and Rollback

### Upgrade: Preserve Data and Config

```bash
cd deploy/podman

# Stop current container (named volumes persist)
podman compose -f compose.yaml down

# Update CPA_IMAGE tag in .env (e.g., v7.4.0)
# CPA_IMAGE=docker.io/eceasy/cli-proxy-api:v7.4.0

# Pull new image
podman pull docker.io/eceasy/cli-proxy-api:v7.4.0

# Start with same volumes (auth, data, logs, plugins preserved)
podman compose -f compose.yaml up -d

# Verify upgrade
podman compose -f compose.yaml logs | head -20
podman ps | grep cpa-model-combo
```

**Important:** Named volumes `cpa_auth`, `cpa_data`, `cpa_logs` are NOT deleted by `down`. If restarting on same host, they will be reused. Plugin binaries are bind-mounted from host directory `./plugins/`.

### Rollback: Restore Previous Version

```bash
# Stop current container
podman compose -f compose.yaml down

# Revert CPA_IMAGE in .env to previous version
# CPA_IMAGE=docker.io/eceasy/cli-proxy-api:v7.3.9

# Restart
podman compose -f compose.yaml up -d

# Volumes (auth, data) automatically restored
```

**NEVER delete volumes manually; they contain user authentication and usage data:**

```bash
# DO NOT run these:
podman volume rm cpa_auth cpa_data cpa_logs cpa_static
podman volume prune  # will delete orphaned volumes
```

### Plugin Update (model-combo.so)

1. Build or download new plugin binary.
2. Verify architecture matches container:
   ```bash
   file model-combo.so  # ELF 64-bit x86-64
   ```
3. Stop container:
   ```bash
   podman compose -f compose.yaml down
   ```
3. Replace plugin in host directory:
   ```bash
   # Plugin binaries are bind-mounted from ./plugins/ on host
   # Simply replace the file in that directory
   cp /path/to/dist/model-combo.so ./plugins/
   chmod 755 ./plugins/model-combo.so
   ```
4. Restart container:
   ```bash
   podman compose -f compose.yaml up -d
   ```

## Cleanup and Removal

### Stop Only (Volumes Preserved)

```bash
podman compose -f compose.yaml down

# Volumes still exist; container restarts will reuse them
podman volume ls | grep cpa_
```

### Full Removal (Delete All Volumes)

```bash
# Stop and remove container
podman compose -f compose.yaml down --volumes

# Alternatively, remove volumes manually
podman volume rm cpa_auth cpa_data cpa_logs cpa_static

# Verify all cleaned
podman volume ls | grep cpa_
```

**Warning:** Removing volumes deletes:
- User authentication records (`cpa_auth`)
- Usage database (`cpa_data`)
- Plugin binaries on host (`./plugins/` directory)

## Troubleshooting

### Container fails to start

```bash
podman compose -f compose.yaml logs
# Look for plugin load errors, config validation failures, port conflicts
```

### Plugin not loaded

```bash
podman compose -f compose.yaml logs | grep model-combo
# Verify plugins/model-combo.so exists and is executable
# Verify compose.yaml mounts /CLIProxyAPI/plugins
```

### Port already in use

```bash
# Change CPA_PORT in .env
CPA_PORT=18318

podman compose -f compose.yaml up -d
# Now accessible at http://127.0.0.1:18318
```

### Rootless Podman socket issue

```bash
# Ensure user namespace is initialized
podman info | grep rootless

# For macOS, start machine first
podman machine start

# On Linux, initialize user session
podman system service unix:///run/user/$(id -u)/podman/podman.sock &
export DOCKER_HOST=unix:///run/user/$(id -u)/podman/podman.sock
```

### Usage database not persisting

```bash
# Verify cpa_data volume exists and is mounted
podman volume inspect cpa_data
podman compose -f compose.yaml exec cpa ls -la /CLIProxyAPI/data/

# Check CPA logs for data_path configuration
podman compose -f compose.yaml logs | grep data_path
```

## Security Considerations

### API Key Management

- **Never commit** real API keys to version control.
- Use `.env.local` (gitignored) or CI secrets for production.
- Rotate keys periodically; old keys can be removed from `config.yaml` without redeploying.

### Dashboard Access

- Management key is required for config/health mutations.
- Dashboard is exposed on CPA port; use reverse proxy (nginx, Caddy) for TLS in production.
- Disable dashboard if not needed: `disable-control-panel: true` in config.yaml.

### Container Permissions

- Default: CPA runs as UID 1000, GID 3000 (non-root, rootless-compatible).
- For production, create dedicated user:
  ```bash
  useradd -r -s /bin/false -u 1000 cpa
  ```
- Ensure volume mount points are readable/writable by CPA user.

### Network Isolation

- Default bind: `127.0.0.1:8317` (localhost only, safe).
- For remote access: Use reverse proxy with authentication (nginx, Caddy, Traefik).
- Never set `CPA_BIND_HOST=0.0.0.0` without firewall/proxy protection.

## Environment Variables Reference

| Variable | Default | Purpose |
|----------|---------|---------|
| `CPA_IMAGE` | `docker.io/eceasy/cli-proxy-api:v7.3.9` | CPA container image |
| `CPA_CONTAINER_NAME` | `cpa-model-combo` | Container name for `podman ps`, `podman logs` |
| `CPA_BIND_HOST` | `127.0.0.1` | Network bind address (localhost only by default) |
| `CPA_PORT` | `8317` | CPA API and dashboard port |
| `CPA_CONFIG_FILE` | `./config.yaml` | Config file path (mounted read-only at /CLIProxyAPI/config.yaml) |
| `CPA_PLUGIN_DIR` | `./plugins` | Plugin directory path (mounted read-only at /CLIProxyAPI/plugins) |
| `CPA_UID` / `CPA_GID` | `1000` / `3000` | Container user/group (non-root) |
| `CPA_MEM_LIMIT` | `1g` | Memory hard limit |
| `CPA_CPUS` | `2` | CPU count limit |
| `CPA_API_KEY` | — | CPA API authentication key (set in .env.local) |
| `CPA_MANAGEMENT_KEY` | — | Dashboard management key (set in .env.local) |

## Multi-Instance Deployment

To run multiple CPA instances with different configurations:

1. Create separate directories:
   ```bash
   mkdir deploy/podman-prod
   mkdir deploy/podman-staging
   ```

2. Copy `compose.yaml`, `config.yaml.template`, `.env.example` to each.

3. Use distinct container names and volume prefixes:
   ```bash
   # .env in deploy/podman-prod
   CPA_CONTAINER_NAME=cpa-prod
   CPA_PORT=8317
   CPA_VOLUME_AUTH=cpa_prod_auth
   CPA_VOLUME_DATA=cpa_prod_data
   # ... etc
   ```

4. Start each separately:
   ```bash
   cd deploy/podman-prod && podman compose up -d
   cd deploy/podman-staging && podman compose up -d
   ```

## Support and Debugging

- Check [CPA documentation](https://github.com/router-for-me/CLIProxyAPI)
- Check [Model Combo documentation](https://github.com/momodding/cpa-model-combo)
- Review container logs: `podman compose logs -f`
- Verify configuration: `podman compose config` (without secrets)
