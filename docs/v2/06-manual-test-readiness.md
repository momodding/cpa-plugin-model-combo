# Routing-only manual-test readiness

## Scope

This guide tests only alias routing inside CPA native plugin host.

It covers route loading, selection, cooldown, fallback, streams, reload, and cleanup.

It excludes accounts, OAuth, credentials, provider setup, usage tracking, dashboard, pricing, and real provider requests.

The plugin selects physical model names through CPA host `model_executor` callbacks.

It does not call providers directly.

No CPA integration was executed for this guide.

This guide makes no production-readiness claim.

## Current build artifacts

Build on same OS and CPU architecture as target CPA host.

`Makefile` builds a Go `c-shared` library with `CGO_ENABLED=1`:

```sh
make build
```

Verified artifact: macOS amd64 `dist/model-router.dylib`, built by `make build`.

Linux `dist/model-router.so` and Windows `dist/model-router.dll` are Makefile targets.

They were not built here.

Build uses plugin ID `model-router` and version default `0.4.2`.

Required ABI exports are `cliproxy_plugin_init`, `ModelRouterPluginCall`, `ModelRouterPluginFree`, and `ModelRouterPluginShutdown`.

Configure plugin identity as `model-router` with host library extension.

Do not rename library basename.

Before manual work, use `make fmt-check`, `make test`, `make race`, `make vet`, or `make check` as needed.

## Prerequisites

Use CPA v7.3.9 for this manual test; later versions are unverified.

CPA must support native plugins, `model_router`, `executor`, `model_registrar`, request interception, `usage_plugin`, and `host.model.*` callbacks.

Use disposable paths only:

```text
${CPA_BIN}       Disposable CPA executable
${CPA_ROOT}      Empty disposable CPA root
${CPA_CONFIG}    Disposable CPA configuration
${PLUGIN_DIR}    ${CPA_ROOT}/plugins
${ROUTER_DB}     ${CPA_ROOT}/router-test.db
```

Copy built library into `${PLUGIN_DIR}`.

Never overwrite a shared plugin directory.

Never edit, restart, or remove any container.

Do not configure real providers, real provider endpoints, accounts, or credentials.

Configure disposable CPA-local scripted test doubles only.

Test doubles must record received physical model name, return planned status or stream, and expose no external provider path.

Direct standalone plugin calls cannot run routing execution.

Without native callback context, `Execute` returns:

```text
model-router execution requires the native host callback context
```

Without native stream bridge, `ExecuteStream` returns:

```text
model-router streaming requires the native stream bridge
```

## Routing-only configuration

Place following plugin configuration under disposable CPA `plugins.configs.model-router` entry.

Physical model values identify local test-double behaviors only.

```yaml
plugins:
  enabled: true
  dir: "${PLUGIN_DIR}"
  configs:
    model-router:
      enabled: true
      priority: 100
      data_path: "${ROUTER_DB}"
      retention_days: 1
      fallback:
        fallback_on_status: [401, 402, 403, 404, 408, 429, 500, 502, 503, 504]
        no_fallback_on_status: []
		stream_fallback_before_payload: true
      routes:
        - alias: route-priority
          strategy: priority
          cooldown_seconds: 1
          targets:
            - model: test/priority-first
            - model: test/priority-second

        - alias: route-weighted
          strategy: round-robin
          cooldown_seconds: 1
          targets:
            - model: test/weighted-a
              weight: 3
            - model: test/weighted-b
              weight: 1

        - alias: route-random
          strategy: random
          cooldown_seconds: 1
          targets:
            - model: test/random-a
            - model: test/random-b

        - alias: route-lkg
          strategy: last_known_good
          cooldown_seconds: 1
          success_rate_threshold: 0.9
          minimum_samples: 20
          observation_window_minutes: 60
          targets:
            - model: test/lkg-a
            - model: test/lkg-b
```

`alias` is required and must be unique.

`targets` is required.

Each target requires unique `model` and optional `weight` from 1 through 1000000.

Supported strategies are `priority`, `round-robin`, `random`, and `last_known_good`.

Omitted strategy defaults to `priority`.

Omitted or zero `cooldown_seconds` defaults to 60.

Default LKG policy is threshold `0.9`, minimum samples `20`, and window `60` minutes.

Use only one of `targets` and legacy `models`.

## Disposable test procedure

1. Create `${CPA_ROOT}` under a temporary directory.
2. Create `${PLUGIN_DIR}` and copy only matching native library into it.
3. Write `${CPA_CONFIG}` from routing-only example.
4. Configure local scripted test doubles for every `test/*` physical model.
5. Start disposable CPA process with `${CPA_CONFIG}`.
6. Confirm CPA loads `model-router` and registers four aliases.
7. Send requests only to aliases below through CPA test listener.
8. Inspect test-double request logs and disposable CPA logs after each case.
9. Stop disposable CPA process.
10. Remove `${CPA_ROOT}` after collecting routing evidence.

Do not use a production CPA root, process, container, provider, account, or credential.

## Test matrix

| Case | Setup and request | Expected routing evidence |
| --- | --- | --- |
| Loading | Start CPA with configuration above. | Plugin loads. Four aliases register. Invalid unknown config fields fail decoding. |
| Priority | Return success from both priority targets. Request `route-priority` repeatedly. | `test/priority-first` receives every request. |
| Weighted round-robin | Return success from both weighted targets. Send eight serial requests to `route-weighted`. | Ordered selection repeats three `test/weighted-a` choices then one `test/weighted-b` choice. |
| Random | Return success from both random targets. Send a statistically useful batch to `route-random`. | Only configured random targets receive requests. Do not require a fixed order. |
| LKG cold start | Clear `${ROUTER_DB}` before startup. Return success from both LKG targets. Request `route-lkg` fewer than 20 times. | Requests remain valid during insufficient-sample qualification. Record selected target sequence without claiming a qualified winner. |
| LKG qualification | Within 60 minutes, give one LKG target at least 20 samples at or above 0.9 success rate. Give other target a lower rate. | Subsequent `route-lkg` selection prefers qualifying target. |
| LKG degradation | After qualification, give selected target enough failures to fall below 0.9 in active window. Keep alternate qualified or healthy. | Selection no longer treats degraded target as last known good. |
| Fallback | Make priority-first return 503 before output. Request `route-priority`. | First target enters cooldown. CPA invokes `test/priority-second`. Client receives second target result. |
| Cooldown | Immediately repeat fallback case. | Cooled target is skipped. After one second, it becomes eligible again. |
| Stream success | Make priority-first emit complete stream. Start streamed `route-priority` request. | Client receives one complete stream from first target. |
| Stream startup failure | Make priority-first fail with 503 before first payload byte. Start streamed request. | CPA retries second target. Client receives second target stream. |
| Stream post-payload failure | Make priority-first emit payload then fail. Start streamed request. | Client receives failure without second-stream splice. Target cools down. |
| Reload | Keep route-weighted targets unchanged. Reload CPA config with unrelated route edit. | CPA reloads plugin. Weighted round-robin state remains for unchanged route. |
| Cleanup and rollback | Stop CPA. Remove `${CPA_ROOT}`. For config rollback, restore saved disposable config and restart only disposable process. | No files remain outside disposable root. No shared CPA or container changed. |

For fallback qualification, use status `503` because default fallback list includes it.

Treat cancellation and deadline errors as non-failover cases.

A `400` or `422` normally returns immediately.

## Evidence record

For each case, retain only disposable artifacts:

- CPA startup and reload logs.
- Test-double request log with alias and physical model.
- Client result or stream transcript.
- Timestamped route configuration revision.
- Cleanup command and confirmation that `${CPA_ROOT}` no longer exists.

Do not retain provider credentials, account data, OAuth data, or real traffic.
