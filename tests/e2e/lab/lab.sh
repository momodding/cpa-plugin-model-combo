#!/bin/sh
set -eu

lab_dir=$(CDPATH= cd "$(dirname "$0")" && pwd)
repo_dir=$(CDPATH= cd "$lab_dir/../../.." && pwd)
compose_file="$lab_dir/compose.yaml"
template_file="$lab_dir/config.yaml.template"
temp_dir=${TMPDIR:-/tmp}
while [ "$temp_dir" != / ] && [ "${temp_dir%/}" != "$temp_dir" ]; do
  temp_dir=${temp_dir%/}
done


usage() {
  cat <<'EOF'
Usage: tests/e2e/lab/lab.sh up|down [workspace]

up requires CPA_E2E_ALLOW_START=1. It creates a private temporary workspace,
builds a Linux model-combo.so, starts disposable CPA, and prints browser test exports.
down requires the workspace path printed by up and removes only a marked lab workspace.
EOF
}

random_key() {
  key=$(LC_CTYPE=C dd if=/dev/urandom bs=1024 count=1 2>/dev/null | base64 | tr -dc 'A-Za-z0-9' | cut -c1-48)
  [ "${#key}" -eq 48 ] || return 1
  printf '%s\n' "$key"
}

render_config() {
  sed \
    -e "s|@API_KEY@|$api_key|g" \
    -e "s|@MANAGEMENT_KEY@|$management_key|g" \
    "$template_file" >"$workspace/config.yaml"
}

require_docker() {
  if ! command -v docker >/dev/null 2>&1; then
    printf '%s\n' 'docker is required' >&2
    exit 1
  fi
}

validate_compose() {
  CPA_E2E_WORKSPACE=$workspace \
  CPA_E2E_PORT=$port \
  CPA_E2E_IMAGE=$image \
  docker compose -f "$compose_file" --project-directory "$workspace" config >/dev/null
}

build_plugin() {
  docker run --rm \
    -v "$repo_dir:/src:ro" \
    -v "$workspace/plugins:/out" \
    -w /src \
    "${CPA_E2E_GO_IMAGE:-docker.io/library/golang:1.26}" \
    sh -c 'CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -buildmode=c-shared -o /out/model-combo.so .'
}

start_lab() {
  if [ "${CPA_E2E_ALLOW_START:-}" != "1" ]; then
    printf '%s\n' 'Refusing to start. Set CPA_E2E_ALLOW_START=1 for a disposable local lab.' >&2
    exit 2
  fi
  require_docker
  workspace=$(mktemp -d "$temp_dir/cpa-model-combo-e2e.XXXXXX")
  umask 077
  mkdir "$workspace/auth" "$workspace/plugins" "$workspace/logs" "$workspace/static"
  : >"$workspace/.cpa-model-combo-e2e-lab"
  port=${CPA_E2E_PORT:-18317}
  image=${CPA_E2E_IMAGE:-docker.io/eceasy/cli-proxy-api:v7.3.9}
  api_key=$(random_key)
  management_key=$(random_key)
  project="cpa-model-combo-e2e-$(basename "$workspace" | tr '[:upper:]' '[:lower:]' | tr -cd '[:alnum:]')"
  render_config
  validate_compose
  build_plugin
  CPA_E2E_WORKSPACE=$workspace \
  CPA_E2E_PORT=$port \
  CPA_E2E_IMAGE=$image \
  docker compose -p "$project" -f "$compose_file" --project-directory "$workspace" up -d
  cat <<EOF
Workspace: $workspace
Browser E2E:
  DASHBOARD_URL=http://127.0.0.1:$port DASHBOARD_MANAGEMENT_KEY=$management_key DASHBOARD_E2E_ALLOW_MUTATIONS=1 npm run test:e2e
Cleanup:
  $lab_dir/lab.sh down $workspace
EOF
}

stop_lab() {
  workspace=${1:-}
  if [ -z "$workspace" ] || [ ! -f "$workspace/.cpa-model-combo-e2e-lab" ]; then
    printf '%s\n' 'down requires a workspace created by this lab' >&2
    exit 2
  fi
  case "$workspace" in
    "$temp_dir"/cpa-model-combo-e2e.*) ;;
    *)
      printf '%s\n' 'refusing to remove workspace outside temporary lab path' >&2
      exit 2
      ;;
  esac
  require_docker
  project=$(cat "$workspace/project-name")
  CPA_E2E_WORKSPACE=$workspace \
  CPA_E2E_PORT=${CPA_E2E_PORT:-18317} \
  CPA_E2E_IMAGE=${CPA_E2E_IMAGE:-docker.io/eceasy/cli-proxy-api:v7.3.9} \
  docker compose -p "$project" -f "$compose_file" --project-directory "$workspace" down --volumes --remove-orphans
  rm -rf "$workspace"
}

case ${1:-} in
  up)
    start_lab
    ;;
  down)
    shift
    stop_lab "${1:-}"
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac
