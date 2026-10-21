# Model Combo — Manual Verification Guide

This document specifies what a human operator must verify against a live disposable CPA instance before production release is approved. Nothing in this document has been executed during automated audit. All steps require a real CPA host and compiled plugin.

Production readiness remains `NO` until every section below is marked verified with observed command output and date.

---

## Prerequisites

| Requirement | Notes |
|---|---|
| CPA v7.3.9 binary or Docker image `docker.io/eceasy/cli-proxy-api:v7.3.9` | Pinned commit `61fdfc341b96178a8dcb53f2efc46cbc341d267c` |
| Compiled plugin matching target OS/arch | `make build` produces `dist/model-combo.dylib` (darwin); CI produces `.so` (linux) and `.dll` (windows) |
| Two or more CPA auth file entries with distinct names and providers | Required to exercise account pool resolution |
| A disposable CPA instance (not production) | All steps mutate health and usage state |
| `DASHBOARD_URL`, `DASHBOARD_MANAGEMENT_KEY` set | Required for management verification steps |
| Chromium or compatible browser | Required for browser E2E |

---

## Section 1 — Plugin load and registration

**Goal:** Confirm the plugin loads cleanly and registers all routes.

1. Start CPA with the plugin directory containing `model-combo.dylib` (or `.so`/`.dll`).
2. Confirm CPA log shows no plugin load errors.
3. Call `GET /v0/management/plugins` (or equivalent). Confirm `model-combo` appears with status `enabled`.
4. Call `POST /plugins/model-combo/validate` with a minimal valid config. Expect HTTP 200.
5. Confirm all 12 management routes are reachable (HTTP 405 or 200, not 404).

**Acceptance:** Plugin listed as enabled; validate endpoint returns 200; all routes respond.

---

## Section 2 — Account pool resolution and AuthID pinning

**Goal:** Confirm the plugin calls `host.auth.list`, matches exact metadata, and forwards the opaque ID.

Prerequisites: At least two CPA auth file entries — one named `primary` with provider `openai`, one named `fallback` with provider `openai`.

Config:
```yaml
account_pools:
  pool:
    strategy: priority
    accounts:
      - {key: primary, auth_name: primary, provider: openai, priority: 0}
      - {key: fallback, auth_name: fallback, provider: openai, priority: 10}
combos:
  - alias: my-combo
    groups:
      - name: main
        targets:
          - model: openai/gpt-4o
            account_pool: pool
```

Steps:
1. Apply config via the dashboard or validate endpoint.
2. Send `POST /v1/chat/completions` with `{"model":"my-combo","messages":[{"role":"user","content":"ping"}]}`.
3. Observe CPA request logs. Confirm the upstream request was made with the credential belonging to `primary` (not `fallback`).
4. Temporarily disable or remove the `primary` auth entry. Resend the request.
5. Confirm the upstream request used the `fallback` credential.

**Acceptance:** Priority-0 account selected first; priority-10 fallback selected when priority-0 is absent.

---

## Section 3 — Fail-closed on `host.auth.list` error

**Goal:** Confirm the plugin returns 503 and makes no upstream call when auth list is unavailable.

Steps:
1. Configure a combo with an account pool (same as Section 2).
2. Simulate a CPA-side auth list failure (e.g., remove all auth files or configure a provider whose auth list returns an error).
3. Send a request to the combo alias.
4. Confirm the response is HTTP 503 with `"error": "model_combo_unavailable"`.
5. Confirm no upstream provider received a request.

**Acceptance:** 503 returned; no upstream call made.

---

## Section 4 — Stream commitment (no retry after first payload)

**Goal:** Confirm that once the plugin emits one SSE chunk to the client, it does not retry on subsequent errors.

Steps:
1. Configure a combo with two targets: a primary that sends one SSE chunk then errors, and a fallback.
2. Send a streaming request (`"stream":true`).
3. Confirm the client receives one chunk and then an error — it does not receive a response from the fallback.

**Acceptance:** No fallback attempt after first chunk emitted.

---

## Section 5 — Health persistence across restart

**Goal:** Confirm combo health survives plugin reload.

Steps:
1. Drive enough failures on one combo group to mark it degraded.
2. Check `GET /plugins/model-combo/health/list`. Confirm the group appears with degraded state.
3. Stop and restart CPA (or reload the plugin if supported).
4. Check `GET /plugins/model-combo/health/list` again immediately after restart.
5. Confirm the degraded group state persists.
6. Call `POST /plugins/model-combo/health/reset` with the alias and group key.
7. Confirm the group state is cleared.

**Acceptance:** Health state present after restart; reset clears it.

---

## Section 6 — Attribution privacy

**Goal:** Confirm no raw auth credentials or opaque IDs appear in usage records.

Steps:
1. Send several requests through a pooled combo using two different auth entries.
2. Call `GET /plugins/model-combo/usage/requests` and inspect returned records.
3. Confirm no field contains a raw API key, bearer token, or opaque CPA auth ID.
4. Confirm attribution distinguishes the two accounts without exposing credentials.

**Acceptance:** No raw credentials in any usage API response.

---

## Section 7 — Dashboard browser verification

**Goal:** Confirm the dashboard loads, authenticates, and renders health and config panels correctly.

Prerequisites: `DASHBOARD_URL` pointing to the disposable CPA instance; `DASHBOARD_MANAGEMENT_KEY` set.

Steps:
1. Open `DASHBOARD_URL` in Chromium.
2. Enter the management key and click "Load configuration".
3. Confirm the workspace becomes visible and model status shows `ready`.
4. Navigate to the Health tab. Confirm health snapshots render for active combos.
5. Reset health for one group via the UI. Confirm the group disappears or updates.
6. Navigate to the Configuration tab. Confirm the combo/route/account-pool editor loads without error.
7. Make a non-destructive config change (e.g., add a comment or adjust a weight) and save. Confirm no error.

Alternatively run the automated browser suite:
```sh
DASHBOARD_URL=http://127.0.0.1:<port> \
DASHBOARD_MANAGEMENT_KEY=<key> \
DASHBOARD_E2E_ALLOW_MUTATIONS=1 \
npm run test:e2e
```

**Acceptance:** Dashboard loads; health panel renders; config editor saves; no management API errors (4xx/5xx) logged during the session.

---

## Section 8 — Weighted round-robin distribution

**Goal:** Confirm that a pool with weights 3:1 produces a 3:1 account selection ratio over a sample.

Config:
```yaml
account_pools:
  weighted:
    strategy: weighted-round-robin
    accounts:
      - {key: heavy, auth_name: primary, provider: openai, priority: 0, weight: 3}
      - {key: light, auth_name: fallback, provider: openai, priority: 0, weight: 1}
combos:
  - alias: weighted-combo
    groups:
      - name: main
        targets:
          - model: openai/gpt-4o
            account_pool: weighted
```

Steps:
1. Apply config. Send 20 requests to `weighted-combo`.
2. Inspect upstream logs or usage records for each auth entry.
3. Confirm `primary` was selected approximately 15 times and `fallback` approximately 5 times.

**Acceptance:** Observed ratio is approximately 3:1 (allow ±2 for the sample size).

---

## Blocked — cannot verify under CPA v7.3.9

The following items cannot be verified under the current CPA version and must be revisited when the CPA protocol is updated:

| Item | Blocker |
|---|---|
| Discovered-account management API | `ManagementRequest` lacks caller identity and host-auth entries |
| Enabled session affinity routing | `ExecutorRequest` lacks trusted session ID |

---

## Verification record

Update this table when each section is executed:

| Section | Verified | Date | Verifier | Notes |
|---|---|---|---|---|
| 1 — Plugin load | ☐ | | | |
| 2 — Account pool / AuthID | ☐ | | | |
| 3 — Fail-closed | ☐ | | | |
| 4 — Stream commitment | ☐ | | | |
| 5 — Health persistence | ☐ | | | |
| 6 — Attribution privacy | ☐ | | | |
| 7 — Dashboard browser | ☐ | | | |
| 8 — WRR distribution | ☐ | | | |

Production release is not approved until all ☐ boxes are filled with observed results.
