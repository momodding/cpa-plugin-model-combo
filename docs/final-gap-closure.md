# Final Gap Closure

Status: incomplete; production readiness `NO`.

| Requirement | Status | Evidence / remaining work |
|---|---|---|
| Account-aware pool routing | PASS (local) | Unit coverage exercises strict pool resolution, priority/WRR selection, request-local exclusions, sync/stream account retry, and half-open reservation. |
| Host auth protocol | PASS (local) | Callback tests prove `{}` for `host.auth.list`, CPA `{"files":[...]}` parsing, and exact opaque `AuthID` forwarding. |
| Stream commitment and attribution privacy | PASS (local) | Stream tests cover alias rewrite and no retry after emitted payload; attribution test requires matching HMAC-auth fingerprint. |
| Durable combo-health storage and observability | PARTIAL (local) | Schema v1→v2 migration (`combo_health_v2` table); health mutations callback-persisted; health/list and health/reset management APIs; current-plan filtering + deep-copy restore. **Shutdown lifecycle:** Verified locally via deterministic ABI test (persist snapshot, prune 24h TTL, close store; reopen confirms obsolete pruned, nonhealthy configured health persists). Test ✓ PASS deterministic (1.682s) and race (3.555s). NOT production-ready (CPA integration unverified). See `docs/sprints/sprint-f1.md`. |
| Discovered-account and health management UI | FAIL | No authenticated discovered-account, health-list, or scoped health-reset APIs; dashboard uses raw `combos` and `account_pools` textareas. |
| Privacy-safe session affinity | FAIL | No affinity configuration, bounded TTL cache, or routing preference exists. |
| Disposable CPA integration | BLOCKED | CI workflow defines pinned CPA v7.3.9 integration; no local/live CPA host execution observed. |
| Browser E2E | BLOCKED | Requires disposable loopback CPA, management key, mutation opt-in, and Chromium. |
| Release, platform, manual verification | BLOCKED | Workflows/scripts exist; no release, external platform, or manual execution observed. |

Closure requires implementation of every `FAIL` row plus observed execution for every `BLOCKED` row. `docs/final-implementation-audit.md` contains command-level evidence and constraints.
