#!/usr/bin/env bash
# preflight.sh: disposable startup validation for deployment config
# Static checks: template, env substitution, gitignore, compose syntax
# Podman runtime verification is deferred to container startup
#
# Usage: bash deploy/podman/preflight.sh [workspace_path]
# Exit: 0 if all checks pass; non-zero if any check fails

set -eu

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE="${1:-}"

# Helper functions
log_pass() { echo "✓ $*"; }
log_fail() { echo "✗ $*" >&2; exit 1; }
log_info() { echo "→ $*"; }

# Check: template exists and is readable
log_info "Checking template file..."
if [[ ! -f "$SCRIPT_DIR/config.yaml.template" ]]; then
	log_fail "config.yaml.template not found at $SCRIPT_DIR/config.yaml.template"
fi
log_pass "config.yaml.template exists"

# Check: .env.example exists (reference doc)
if [[ ! -f "$SCRIPT_DIR/.env.example" ]]; then
	log_fail ".env.example not found; required as template"
fi
log_pass ".env.example present"

# Check: .gitignore patterns
log_info "Validating .gitignore patterns..."
if [[ ! -f "$SCRIPT_DIR/.gitignore" ]]; then
	log_fail ".gitignore not found"
fi
if ! grep -q "^\.env$" "$SCRIPT_DIR/.gitignore"; then
	log_fail ".gitignore missing .env pattern (would expose real keys)"
fi
if ! grep -q "\.env.local" "$SCRIPT_DIR/.gitignore"; then
	log_fail ".gitignore missing .env.local pattern"
fi
if ! grep -q "^config.yaml$" "$SCRIPT_DIR/.gitignore"; then
	log_fail ".gitignore missing config.yaml pattern"
fi
log_pass ".gitignore patterns correct"

# Read template once
template_raw=$(cat "$SCRIPT_DIR/config.yaml.template")

# Check: template structure (key sections must exist)
log_info "Validating template structure..."
required_sections=("host: " "port: " "api-keys:" "plugins:" "model-combo:" "data_path:" "retention_days:" "resilience:")
for section in "${required_sections[@]}"; do
	if ! echo "$template_raw" | grep -q "$section"; then
		log_fail "template missing section: $section"
	fi
done
log_pass "template contains all required sections"

# Check: loopback default
if ! echo "$template_raw" | grep -q 'host: "127.0.0.1"'; then
	log_fail "template does not enforce loopback binding (127.0.0.1); security risk"
fi
log_pass "template enforces loopback default"

# Check: template uses placeholders for secrets (all api-key and secret-key values should be @PLACEHOLDER@)
log_info "Checking template uses only placeholders for secrets..."
if echo "$template_raw" | grep -E '^[[:space:]]*(api-key|secret-key):[[:space:]]*[^@[:space:]]' | grep -v '@' >/dev/null 2>&1; then
	log_fail "template contains non-placeholder secrets (should use @PLACEHOLDER@)"
fi
log_pass "template uses only placeholders"

# Check: placeholder replacements are documented
log_info "Checking placeholder documentation..."
placeholder_count=$(echo "$template_raw" | grep -o "@[A-Z_]*@" | sort -u | wc -l)
if [[ $placeholder_count -lt 5 ]]; then
	log_fail "template has no/few placeholders; may not be a template"
fi
log_pass "template has documented placeholders ($placeholder_count unique)"
# Check: compose.yaml exists (basic YAML structure without yaml module)
log_info "Checking Compose file..."
if [[ ! -f "$SCRIPT_DIR/compose.yaml" ]]; then
	log_fail "compose.yaml not found"
fi

# Validate YAML structure via grep (no external module required)
compose_raw=$(cat "$SCRIPT_DIR/compose.yaml")
if ! echo "$compose_raw" | grep -q "version:"; then
	log_fail "compose.yaml missing version field"
fi
if ! echo "$compose_raw" | grep -q "services:"; then
	log_fail "compose.yaml missing services section"
fi
log_pass "compose.yaml has valid structure"

# Check: compose references required volumes and mounts
log_info "Validating Compose volume bindings..."
required_bindings=("CLIProxyAPI/config.yaml" "CLIProxyAPI/plugins" "cli-proxy-api" "CLIProxyAPI/data" "CLIProxyAPI/logs" "CLIProxyAPI/static")
for binding in "${required_bindings[@]}"; do
	if ! echo "$compose_raw" | grep -q "$binding"; then
		log_fail "compose.yaml missing volume binding: $binding"
	fi
done
log_pass "compose.yaml has required volume bindings"

# Check: config and plugins mounts are read-only
if ! echo "$compose_raw" | grep -A 3 "config.yaml" | grep -q "read_only"; then
	log_fail "compose.yaml: config.yaml mount should be read-only"
fi
if ! echo "$compose_raw" | grep -A 3 "plugins" | grep -q "read_only"; then
	log_fail "compose.yaml: plugins mount should be read-only"
fi
log_pass "compose.yaml has read-only flags on config and plugins"

# Test environment substitution (if workspace provided)
if [[ -n "$WORKSPACE" ]]; then
	log_info "Testing template substitution in workspace..."
	mkdir -p "$WORKSPACE"

	# Generate test keys
	test_api_key=$(tr -dc 'A-Za-z0-9' </dev/urandom | head -c 32)
	test_mgmt_key=$(tr -dc 'A-Za-z0-9' </dev/urandom | head -c 48)

	# Render template
	rendered="$WORKSPACE/config.yaml"
	sed \
		-e "s|@API_KEY@|$test_api_key|g" \
		-e "s|@MANAGEMENT_KEY@|$test_mgmt_key|g" \
		-e "s|@PROVIDER_NAME@|test-provider|g" \
		-e "s|@BASE_URL@|http://127.0.0.1:19080/v1|g" \
		-e "s|@API_KEY_VALUE@|test_key|g" \
		"$SCRIPT_DIR/config.yaml.template" > "$rendered"

	# Check: no placeholders remain
	if grep -q "@[A-Z_]*@" "$rendered"; then
		unreplaced=$(grep -o "@[A-Z_]*@" "$rendered" | sort -u | tr '\n' ',' | sed 's/,$//')
		log_fail "config rendering failed; unreplaced placeholders: $unreplaced"
	fi
	log_pass "all placeholders substituted"

	# Check: rendered config is valid YAML via Ruby Psych
	if ! ruby -e "require 'yaml'; YAML.load_file('$rendered')" 2>/dev/null; then
		log_fail "rendered config.yaml has invalid YAML syntax"
	fi
	log_pass "rendered config.yaml is valid YAML"

	# Verify key structure in rendered config
	if ! grep -q "host: \"127.0.0.1\"" "$rendered"; then
		log_fail "rendered config lost loopback binding"
	fi
	if ! grep -q "data_path: " "$rendered"; then
		log_fail "rendered config missing data_path"
	fi
	log_pass "rendered config preserves structure"
fi

echo ""
log_pass "All preflight checks passed ✓"
