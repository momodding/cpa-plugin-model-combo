# Sprint F3.1: Windows digest-key permission fix

## Baseline

- Branch: `development-phase`, tracking `origin/development-phase`, ahead/behind `0 0`, worktree clean at investigation time.
- Baseline SHA: `2b887fc7fd50c315554f48b5bee7e824e684dbbf` (`feat(router): add account-aware combo routing`).
- Actions run `39417339005` (workflow `test`, branch `development-phase`) head SHA matches baseline.

## CI status (observed)

- Run: https://github.com/momodding/cpa-plugin-model-combo/actions/runs/39417339005
- Job `test` (118609445857): success. https://github.com/momodding/cpa-plugin-model-combo/actions/runs/39417339005/job/118609445857
- Job `windows-sqlite` (118609446166): failure. https://github.com/momodding/cpa-plugin-model-combo/actions/runs/39417339005/job/118609446166
  - Step `Verify native Windows toolchain and source`: success.
  - Step `Test Windows SQLite paths and registration`: failure (exit code 1).
  - Post step `Run actions/setup-go@v7`: skipped (post-step only).
- Failing-step log: **unavailable**. Job log endpoint returns HTTP 403 ("Must have admin rights to Repository"). The public job page has no `go test` output. Failure excerpt is therefore limited to the annotation "Process completed with exit code 1."
- Local repo `origin` remote name observed as `cpa-plugin-model-router.git`; run URLs use `cpa-plugin-model-combo`.

## Observed failure (supplied by task)

- Windows digest-key test failure, as reported in the task. Not independently reproduced in this doc.

## Root cause (supplied failure interpretation, corrected)

- Windows synthetic file mode `0666` does not prove insecure permissions. NTFS DACL is authoritative on Windows.

## Actual implementation in current source

- Digest-key creation routes through platform-specific helpers.
- Unix creates the file atomically with private mode `0600`, then validates the opened handle with no-follow semantics.
- Windows creates the file with `CREATE_NEW` using a protected owner-only DACL for the current SID, then reads the file through a validated no-reparse handle.
- Existing insecure or malformed digest-key files fail closed. They are never repaired.
- This summary describes current source only. It does not claim the supplied Windows failure was reproduced or that Windows or CI tests passed.

## Required approach

- On Windows: create the digest-key file with a protected owner-only DACL for the current SID.
- On Unix: create the digest-key file atomically with private mode `0600`.
- Reject existing insecure or malformed digest-key files; do not chmod or rewrite them.
- Do not rely on POSIX mode bits as the security control on Windows.

## Implementation plan

1. Add a platform-specific helper that creates the digest-key file with owner-only permissions.
   - Unix: atomic create with `0600`; validate the opened handle with no-follow semantics.
   - Windows: `CREATE_NEW` with a protected owner-only DACL for the current SID; read via a validated no-reparse handle.
   - Existing insecure or malformed files fail closed.
2. Route all digest-key file creation through the helper. Remove any direct create path that bypasses it.
3. Keep the public behavior of the key load/store API unchanged.

## Test plan

- Unit: Unix create yields mode `0600`; insecure or malformed existing file fails closed.
- Windows-gated unit: `CREATE_NEW` yields protected owner-only DACL; insecure or malformed existing file fails closed.
- Existing `TestSQLite*`, `TestUsageStore*`, `TestABIRegistration*`, `TestLegacy*` must still pass.

## Verification plan

- Run locally on Unix: `go test ./...`, `go test -race ./...`, `go vet ./...`.
- Run Windows filter locally or in CI:
  `go test ./... -run '^(TestSQLite|TestUsageStoreDataPaths|TestUsageStoreRelativePathPersists|TestABIRegistration|TestLegacy|TestAccountDigestKey)' -count=1 -v`
- CI rerun on `development-phase` must show `test` and `windows-sqlite` both green.

## Acceptance gate

- F3.1 supported-platform gate: **PASS** for supported Linux (including Docker/Podman) and macOS (including Colima), based on prior observed Linux CI/local verification and passing local Unix checks below.
- Windows native runtime/deployment: **DEFERRED**, not a PASS. Windows ACL validation remains preserved in tests and workflow.
- `windows-sqlite` job conclusion: failure on current Actions run; overall Actions remains red.
- Digest-key file is created owner-only on Unix (`0600`) and with a protected owner-only DACL on Windows; insecure files fail closed.
- No product behavior change outside digest-key file permissions.

## Platform support gate

- **Supported:** Linux, including Docker/Podman; macOS, including Colima.
- **Deferred:** Windows native runtime/deployment. Do not treat failing `windows-sqlite` job as supported-platform pass.

## Rollback and risks

- Rollback: revert the F3.1 commit; restores prior permission handling.
- Risk: ACL API errors on Windows (permission denied, non-NTFS). Mitigation: fail closed with clear error.
- Risk: existing key files with loose permissions. Mitigation: fail closed; operators must replace insecure keys.
- Risk: root cause not confirmed from CI log (403). Confirm with the failing `go test -v` output before merge.

## Status

- Product code: unchanged.
- CI output for `windows-sqlite` step: unavailable (GitHub API admin restriction). No later checks claimed.

## Observed local verification

- `go test ./... -count=1`: PASS (15.323s).
- `go test -race ./... -count=1`: PASS (56.691s).
- `go vet ./...`: PASS.
- `make check`: PASS.
- `make build`: PASS. Nonfatal macOS deployment-target warning emitted.
- `git diff --check`: PASS.
- Integration command: FAILED. Failure limited to three CLIProxyAPI startup tests: readiness failures, and one upstream runtime fatal `bad sweepgen in refill`.
- Integration failure is in upstream CLIProxyAPI startup tests. It does not indicate F3.1 failure.
- Windows digest-key path: not verified by these local runs. No Windows pass is claimed.

## Supported-platform gate

- **PASS:** Linux, including Docker/Podman, and macOS, including Colima. Gate relies on prior observed Linux CI/local verification.
- **DEFERRED, not PASS:** Windows native runtime/deployment. Preserve Windows tests and ACL validation.
- Overall Actions remains **RED** because `windows-sqlite` failed on current run. Do not alter Actions to hide failure.
- Windows ACL diagnostic compilation is nonblocking backlog.
