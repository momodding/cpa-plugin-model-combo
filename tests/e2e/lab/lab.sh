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
Usage: tests/e2e/lab/lab.sh up|status|logs|down [workspace]

up requires CPA_E2E_ALLOW_START=1. It creates a private temporary workspace,
builds a Linux model-combo.so, and starts a disposable CPA lab.
status, logs, and down require the workspace path printed by up.
EOF
}

random_key() {
  key=$(LC_CTYPE=C dd if=/dev/urandom bs=1024 count=1 2>/dev/null | base64 | tr -dc 'A-Za-z0-9' | cut -c1-48)
  [ "${#key}" -eq 48 ] || return 1
  printf '%s\n' "$key"
}

state_value() {
  key=$1
  sed -n "s/^$key=//p" "$workspace/state"
}

write_state() {
  umask 077
  cat >"$workspace/state" <<EOF
workspace=$workspace
project=$project
image=$image
port=$port
container=$container
provider_container=$provider_container
started_at=$started_at
api_key=$api_key
management_key=$management_key
EOF
}

render_config() {
  sed \
    -e "s|@API_KEY@|$api_key|g" \
    -e "s|@MANAGEMENT_KEY@|$management_key|g" \
    "$template_file" >"$workspace/config.yaml"
}

redact_stream() {
  sed \
    -e "s|${api_key:-__unset__}|[REDACTED]|g" \
    -e "s|${management_key:-__unset__}|[REDACTED]|g"
}

require_docker() {
  if ! command -v docker >/dev/null 2>&1; then
    printf '%s\n' 'docker is required' >&2
    exit 1
  fi
}

compose() {
  CPA_E2E_WORKSPACE=$workspace \
  CPA_E2E_LAB_DIR=$lab_dir \
  CPA_E2E_PORT=0 \
  CPA_E2E_IMAGE=$image \
  docker compose --project-name "$project" -f "$compose_file" --project-directory "$workspace" "$@"
}

validate_compose() {
  compose config >/dev/null
}

build_plugin() {
  cache_root=${CPA_E2E_CACHE_DIR:-"$temp_dir/cpa-model-combo-e2e-go-cache"}
  umask 077
  mkdir -p "$cache_root/gomod" "$cache_root/gocache"
  docker run --rm \
    -e GOMODCACHE=/go/pkg/mod \
    -e GOCACHE=/root/.cache/go-build \
    -v "$repo_dir:/src:ro" \
    -v "$workspace/plugins:/out" \
    -v "$cache_root/gomod:/go/pkg/mod" \
    -v "$cache_root/gocache:/root/.cache/go-build" \
    -w /src \
    "${CPA_E2E_GO_IMAGE:-docker.io/library/golang:1.26}" \
    sh -c 'CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -buildmode=c-shared -o /out/model-combo.so .'
}

wait_for_cpa() {
  attempts=30
  while ! curl --fail --silent --show-error --max-time 2 \
    -H "Authorization: Bearer $api_key" \
    "http://127.0.0.1:$port/v1/models" >/dev/null; do
    attempts=$((attempts - 1))
    if [ "$attempts" -eq 0 ]; then
      printf '%s\n' 'CPA did not become ready' >&2
      return 1
    fi
    sleep 1
  done
}
require_owned_container() {
  expected_service=$2
  if ! labels=$(docker inspect -f '{{ index .Config.Labels "com.docker.compose.project" }} {{ index .Config.Labels "com.docker.compose.service" }} {{ index .Config.Labels "com.cpa-model-combo.e2e.lab" }}' "$1" 2>/dev/null); then
    printf '%s\n' 'workspace state references a missing lab container' >&2
    exit 2
  fi
  if [ "$labels" != "$project $expected_service true" ]; then
    printf '%s\n' 'workspace state does not belong to stored lab project' >&2
    exit 2
  fi
}


require_workspace() {
  workspace=$1
  case "$workspace" in
    "$temp_dir"/cpa-model-combo-e2e.*) ;;
    *)
      printf '%s\n' 'refusing to use workspace outside temporary lab path' >&2
      exit 2
      ;;
  esac
  case "$workspace" in
    *..*|*/./*)
      printf '%s\n' 'refusing to use workspace outside temporary lab path' >&2
      exit 2
      ;;
  esac
  if [ ! -f "$workspace/.cpa-model-combo-e2e-lab" ] || [ ! -f "$workspace/state" ]; then
    printf '%s\n' 'command requires a workspace created by this lab' >&2
    exit 2
  fi
  stored_workspace=$(state_value workspace)
  project=$(state_value project)
  image=$(state_value image)
  port=$(state_value port)
  container=$(state_value container)
  provider_container=$(state_value provider_container)
  started_at=$(state_value started_at)
  api_key=$(state_value api_key)
  management_key=$(state_value management_key)
  if [ "$stored_workspace" != "$workspace" ] || [ -z "$project" ] || [ -z "$image" ] || [ -z "$port" ] || [ -z "$container" ] || [ -z "$provider_container" ] || [ -z "$started_at" ]; then
    printf '%s\n' 'workspace state is incomplete' >&2
    exit 2
  fi
  case "$project" in
    cpa-model-combo-e2e-?*) ;;
    *)
      printf '%s\n' 'workspace state project is not a lab project' >&2
      exit 2
      ;;
  esac
  marker_project=$(sed -n 's/^project=//p' "$workspace/.cpa-model-combo-e2e-lab")
  if [ "$marker_project" != "$project" ]; then
    printf '%s\n' 'workspace marker does not match stored lab project' >&2
    exit 2
  fi
  require_owned_container "$container" cpa
  require_owned_container "$provider_container" fake-provider
}

cleanup_failed_start() {
  status=$?
  trap - 0 HUP INT TERM
  if [ -n "${workspace:-}" ] && [ -f "$workspace/.cpa-model-combo-e2e-lab" ]; then
    compose logs --no-color 2>&1 | redact_stream >"$workspace/logs/startup-failure.log" || true
    printf 'startup failure logs (redacted): %s\n' "$workspace/logs/startup-failure.log" >&2
    compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  fi
  exit "$status"
}

start_lab() {
  if [ "${CPA_E2E_ALLOW_START:-}" != "1" ]; then
    printf '%s\n' 'Refusing to start. Set CPA_E2E_ALLOW_START=1 for a disposable local lab.' >&2
    exit 2
  fi
  require_docker

  umask 077
  workspace=$(mktemp -d "$temp_dir/cpa-model-combo-e2e.XXXXXX")
  suffix=$(printf '%s' "${workspace##*.}" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9')
  [ -n "$suffix" ] || { printf '%s\n' 'could not derive Compose project suffix' >&2; exit 1; }
  project="cpa-model-combo-e2e-$suffix"
  image=${CPA_E2E_IMAGE:-docker.io/eceasy/cli-proxy-api:v7.3.9}
  port=0
  container=
  provider_container=
  started_at=
  api_key=$(random_key)
  management_key=$(random_key)
  mkdir -p "$workspace/auth" "$workspace/plugins" "$workspace/logs" "$workspace/static"
  printf 'project=%s\n' "$project" >"$workspace/.cpa-model-combo-e2e-lab"
  trap cleanup_failed_start 0 HUP INT TERM

  render_config
  validate_compose
  build_plugin
  compose up -d
  container=$(compose ps -q cpa)
  provider_container=$(compose ps -q fake-provider)
  started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  write_state
  mapped=$(compose port cpa 8317)
  port=${mapped##*:}
  case "$port" in
    ''|*[!0-9]*)
      printf '%s\n' "could not determine CPA loopback port: $mapped" >&2
      exit 1
      ;;
  esac
  write_state
  wait_for_cpa
  trap - 0 HUP INT TERM

  printf 'CPA E2E workspace: %s\n' "$workspace"
  printf 'CPA container: %s\n' "$container"
  printf 'CPA listening on http://127.0.0.1:%s\n' "$port"
  printf '%s\n' "DASHBOARD_URL=http://127.0.0.1:$port DASHBOARD_MANAGEMENT_KEY=\$(sed -n 's/^management_key=//p' '$workspace/state') DASHBOARD_E2E_ALLOW_MUTATIONS=1 npm run test:e2e"
  printf 'Cleanup: %s down %s\n' "$lab_dir/lab.sh" "$workspace"
}

status_lab() {
  require_docker
  require_workspace "$1"
  compose ps
  if curl --fail --silent --show-error --max-time 2 \
    -H "Authorization: Bearer $api_key" \
    "http://127.0.0.1:$port/v1/models" >/dev/null; then
    reachability=ready
  else
    reachability=unreachable
  fi
  printf 'CPA E2E workspace: %s\n' "$workspace"
  printf 'CPA project: %s\n' "$project"
  printf 'CPA container: %s\n' "$container"
  printf 'Fake provider container: %s\n' "$provider_container"
  printf 'CPA reachability: %s\n' "$reachability"
  printf 'CPA listening on http://127.0.0.1:%s\n' "$port"
}

logs_lab() {
  require_docker
  require_workspace "$1"
  compose logs --no-color | redact_stream
}

stop_lab() {
  require_docker
  require_workspace "$1"
  compose down --volumes --remove-orphans
  rm -rf "$workspace"
}

case "${1:-}" in
  up)
    [ "$#" -eq 1 ] || { usage >&2; exit 2; }
    start_lab
    ;;
  status)
    [ "$#" -eq 2 ] || { usage >&2; exit 2; }
    status_lab "$2"
    ;;
  logs)
    [ "$#" -eq 2 ] || { usage >&2; exit 2; }
    logs_lab "$2"
    ;;
  down)
    [ "$#" -eq 2 ] || { usage >&2; exit 2; }
    stop_lab "$2"
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac
