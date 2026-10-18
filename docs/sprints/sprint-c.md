# Sprint C: Visual Combo & Account Pool Management

## Overview

Sprint C provides visual editors for combos and account pools alongside raw JSON editors. Route table remains visual-only (no raw sync). Model catalog loaded from `/v1/models`. Account discovery unavailable (CPA v7.3.9 management lacks callback identity).

## Visual Editors

### Routes

Fields: alias, strategy (priority|round-robin), cooldown_seconds, targets with model (dropdown), weight.

### Combos & Groups

Combos contain ordered groups. Groups render with strategy selector (priority|round-robin) and target list. Each target specifies model, weight, account_pool, capabilities.

Parse extracts from raw JSON: alias, groups with name, strategy, cooldown_seconds, targets.

Serialize writes back: alias, groups with name, strategy, cooldown_seconds (if set), targets.

Unknown fields in raw JSON preserved via `__raw` object on combo, group, target.

Group fallback is unsupported and is not emitted.

### Account Pools

Pools contain name, strategy (priority|weighted_round_robin), account entries.

Each entry has key, auth_name (manual text), provider (manual text).

Unknown fields preserved via `__raw` object on pool and entry.


Dashboard never exposes OAuth tokens, refresh tokens, credential contents, AuthID, AuthIndex, file paths, email, or management secrets.

## Management APIs

- Load model catalog: GET `/v1/models`
- Validate config: POST `/v0/management/plugins/model-combo/validate` with routes, combos, account_pools
- Save config: PATCH `/v0/management/plugins/model-combo/config` with enabled, priority, routes, combos, account_pools

Client-side validation checks routes only (aliases, strategies, cooldown integers, target models, weight ranges). Combo and pool validation deferred to server.

## Raw JSON Editors

Combos and pools have raw JSON input/output synchronized with visual editors. Parse errors shown inline; visual state unchanged on parse failure.

## Verification

Checks run:

- `go test ./... -count=1` — pass (macOS linker version mismatch warning nonfatal)
- `go vet ./...` — pass
- `make fmt-check` — pass
- `node --check dashboard/configuration/tab.js` — pass
- `go test . -run 'TestModelCombo(ABIAdvertisesConfigurationResource|Management)|TestAssembleModelComboDashboardEmbedsEveryAssetOnce' -count=1` — pass (macOS linker version mismatch warning nonfatal)
- Ruby Psych parsed `tests/e2e/lab/compose.yaml` and `tests/e2e/lab/config.yaml.template`
- `bash -n tests/e2e/lab/lab.sh` passed
- `tests/e2e/lab/lab.sh up` without opt-in refused with exit 2 and started no containers
- `git diff --check` — whitespace clean

## Limitations

Disposable browser E2E remains unrun. Blockers: Podman command unavailable; `/usr/bin/google-chrome` and other local Chrome/Chromium unavailable; `node_modules` missing and `npm ls --depth=0` reports unmet `playwright-core@1.63.0`. No isolated workspace or services created; no teardown required.
