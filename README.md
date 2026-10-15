# CPA Model Combo

[![test](https://github.com/momodding/cpa-model-combo/actions/workflows/test.yml/badge.svg)](https://github.com/momodding/cpa-model-combo/actions/workflows/test.yml)
[![release](https://github.com/momodding/cpa-model-combo/actions/workflows/release.yml/badge.svg)](https://github.com/momodding/cpa-model-combo/actions/workflows/release.yml)

Model Combo is native [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) plugin. It exposes client aliases, selects configured physical CPA models, and invokes CPA host model executor. CPA retains provider transport, credentials, account selection, protocol translation, proxying, and request logging.

## Install

Build matching host OS/architecture, then copy `model-combo.so`, `model-combo.dylib`, or `model-combo.dll` into `plugins.dir`.

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    model-combo:
      enabled: true
      priority: 100
      data_path: ./data/model-combo-usage.db
      retention_days: 365
      resilience:
        max_attempts: 4
        deadline_ms: 120000
        base_backoff_ms: 100
        max_backoff_ms: 2000
        honor_retry_after: true
        circuit_failure_threshold: 3
        circuit_open_seconds: 60
        circuit_max_open_seconds: 900
      combos:
        - alias: high-reasoning
          capability_policy: compatible-if-known
          groups:
            - name: premium
              strategy: priority
              cooldown_seconds: 60
              targets:
                - model: provider-a/reasoning-large
                  capabilities: [reasoning, tools]
                - model: provider-b/reasoning-large
                  capabilities: [reasoning, tools]
            - name: standard
              strategy: weighted-round-robin
              targets:
                - model: provider-c/reasoning-medium
                  weight: 2
                  capabilities: [reasoning, tools]
                - model: provider-d/reasoning-medium
                  weight: 1
                  capabilities: [reasoning]
```

Example model IDs are illustrative. Replace with exact IDs from CPA `/v1/models`.

## Routing contract

- Group order is fallback order. `priority`, `round-robin`, and smooth `weighted-round-robin` have independent state per combo/group.
- One candidate is attempted once per request. `max_attempts` covers every group.
- Capability declarations distinguish omission from `capabilities: []`: omission is unknown and remains eligible under `compatible-if-known`; explicit empty declares no capability support and is skipped for capability-required requests.
- `capability_policy: compatible-if-known` is default for compatibility. Set `require-declared` to skip capability-required requests unless target explicitly declares every required capability.
- CPA receives original request unchanged except top-level physical `model`; tool calls, reasoning fields, headers, query, and protocol remain CPA-owned. Successful response model fields return requested alias.
- Fallback occurs only before first payload successfully emits to client stream. Committed stream never splices providers.
- `deadline_ms` is inter-attempt orchestration budget. Pinned CPA v7.3.9 callback cannot cancel blocked in-flight host execution; CPA/provider timeouts control it.
- Native plugins are trusted in-process code. Never load unverified libraries. Plugin does not store provider credentials, prompts, request bodies, API keys, or raw session identifiers.

`fallback.no_fallback_on_status` overrides `fallback_on_status`. Default fallback statuses are `401,402,403,404,408,429,500,502,503,504`. Ordinary request `400` errors are terminal unless structured upstream data identifies explicit context or capability incompatibility.

## Legacy configuration and coexistence

`routes` and `model-routes` remain accepted. Each route normalizes to one combo with group `default`; `models` remains legacy shorthand for `targets`. Do not set both `routes` and `model-routes`. `routes` may coexist with `combos` only when aliases are unique.

Migration:

1. Install `model-combo` beside original `model-router`.
2. Copy old config unchanged beneath `plugins.configs.model-combo`.
3. Disable original router or ensure aliases are disjoint.
4. Verify `/v1/models` and Model Combo management page; convert legacy routes to canonical combos when ready.
5. Roll back by disabling Model Combo and re-enabling untouched original router. Default databases are distinct.

Do not enable same alias in both plugins. CPA router priority makes ownership order-dependent. Model Combo never reads or writes original router configuration.

Management resource: `/v0/resource/plugins/model-combo/config`. CPA owns management authentication.

## Dashboard configuration

Configuration page keeps legacy route editor for `routes`: alias, `priority` or `round-robin`, cooldown, and weighted targets. Use **Advanced Combo definitions** JSON editor for canonical `combos`, including ordered `groups`, `weighted-round-robin`, `capability_policy`, and target `capabilities`.

Editor loads and saves `combos` unchanged as JSON. It validates JSON syntax and requires an array before saving; empty editor saves `combos: []`. Route and combo aliases must remain unique. CPA management authentication still applies.

## Build and verify

```bash
make check
make build
CPA_SOURCE=../CLIProxyAPI go test -tags=integration ./... -count=1
```

Release workflow packages Linux (`amd64`, `arm64`), macOS (`amd64`, `arm64`), and Windows (`amd64`) archives. Each `model-combo_<version>_<goos>_<goarch>.zip` contains exactly root `model-combo.<ext>`; release also includes `checksums.txt` for all five archives.

## Documentation

- [Gap analysis](docs/combo-gap-analysis.md)
- [Architecture](docs/combo-architecture.md)
- [Implementation plan](docs/combo-implementation-plan.md)
- [Manual verification](docs/combo-manual-verification.md)
- [Usage storage](docs/usage-tracking.md)
- [Release notes](CHANGELOG.md)

## License

MIT. See [LICENSE](LICENSE).