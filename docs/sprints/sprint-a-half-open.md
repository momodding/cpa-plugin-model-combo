# Sprint A: Half-open probe reservation

## Pre-edit plan

1. Inspect selector and health state transitions for half-open candidates.
2. Reserve a probe only after strategy chooses one eligible candidate, while holding runtime state lock.
3. Release that reservation on every execution and stream terminal path, including cancellation and recoverable failures.
4. Add deterministic concurrent regression coverage proving one half-open candidate receives one active probe.
5. Run focused Go tests covering selector, health, execute, stream, and resilience behavior.

## Results

### Root cause

Expired open circuits were eligible again after their cooldown, but selection did not atomically reserve only strategy-selected candidate. Concurrent callers could therefore select same half-open candidate; candidate-list construction also risked reserving candidates that strategy did not select.

### Changes

- `selector.go`: skip occupied expired-open candidates; select strategy winner first; reserve only that winner while holding runtime lock. Weighted round-robin now returns selected eligible entry to common reservation path.
- `selector_test.go`: add concurrent single-candidate regression proving exactly one caller selects while occupancy is held, plus multi-candidate regression proving only selected candidate is occupied.
- `docs/sprints/sprint-a-half-open.md`: record plan, release audit, and verification.

### Release-path audit

| Path | Result |
| --- | --- |
| Non-recoverable execution failure | `markFailure` clears probe occupancy. |
| Recoverable execution failure before retry | `releaseProbe` clears occupancy. |
| Context cancellation/deadline | Classified non-recoverable and `markFailure` clears occupancy. |
| Successful execution completion | `markSuccess` clears occupancy and closes circuit. |
| Stream start/read/emit/close failure | Existing failure/retry paths call `markFailure` or `releaseProbe`. |
| Successful stream completion | Existing `markSuccess` clears occupancy. |

No release-path code change required: existing `markSuccess`, `markFailure`, and `releaseProbe` cover terminal, recoverable, cancellation, and stream outcomes.

## Verification

`go test ./... -run 'TestComboSelector.*HalfOpen' -count=1`

Passed: `ok github.com/momodding/cpa-model-combo 5.447s`. Go linker emitted existing macOS deployment-target warning (`go.o` built for 13.0, link target 12.0).
