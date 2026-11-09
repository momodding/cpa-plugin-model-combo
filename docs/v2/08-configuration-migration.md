# Configuration migration

Use canonical Model Router identity for configuration, artifacts, and dashboard links.

- Plugin identity: `model-router`.
- Artifacts: `model-router.dylib`, `model-router.so`, or `model-router.dll`.
- Dashboard resource: `/v0/resource/plugins/model-router/config`.

CPA v8.0.4 compatibility is unverified. Do not treat this guide as support confirmation for that version.

## Routing-only configuration

Model Router uses routing-only configuration. Configure model aliases with `routes` and model targets.

```yaml
routes:
  - alias: chat
    strategy: last_known_good
    targets:
      - model: provider/model-a
      - model: provider/model-b
    cooldown_seconds: 60
    success_rate_threshold: 0.9
    minimum_samples: 20
    observation_window_minutes: 60
    fallback:
      fallback_on_status: [429, 500, 502, 503, 504]
		stream_fallback_before_payload: true
```

`model-routes` remains accepted as a legacy alias for `routes`. Do not set both keys.
`models` remains accepted as a legacy alias for `targets`. Do not set both keys.
`cooldown-seconds` remains accepted as a legacy alias for `cooldown_seconds`. Do not set both keys.

The `last_known_good` strategy accepts these route fields:

- `success_rate_threshold` must be greater than zero and no greater than one.
- `minimum_samples` must be greater than zero.
- `observation_window_minutes` must be greater than zero.

Defaults are `0.9`, `20`, and `60` respectively.

Model Router does not support account-aware configuration. Remove `account_pools` and `combos`.
Replace each legacy account selection with explicit model `targets` on a route.

Unknown fields fail configuration decoding. This prevents silently ignored misspelled routing settings.

## Safe staged upgrade and rollback

1. Back up deployed configuration and existing plugin artifact.
2. Convert account-aware settings into explicit `routes` and `targets`.
3. Validate configuration on one compatible host before fleet deployment.
4. Install only matching `model-router` artifact for host operating system.
5. Confirm dashboard reads `/v0/resource/plugins/model-router/config`.
6. Upgrade remaining hosts after canary routing behaves as expected.
7. Roll back by restoring previous artifact and backed-up configuration together.

Do not mix old account-aware configurations with routing-only plugin versions.
Mixed versions can parse different schemas and route requests differently.
