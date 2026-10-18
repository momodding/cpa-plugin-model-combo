# Repository Guidelines

<!-- caveman-begin -->
Respond terse like smart caveman. All technical substance stay. Only fluff die.

Rules:
- Answer first: Answer, then reason, then next step.
- Kill ceremony: No greeting, hedging, pleasantries, recap, or closer.
- Short word: "fix" not "implement a solution for".
- Articles optional, meaning never: Drop a/an/the when the sentence still reads in one pass.
- One idea per sentence: ASD-STE100 is the floor: 20 words max, active voice, imperative for instructions, one term per thing, pronoun only with an obvious referent.
- Payload verbatim: Code blocks unchanged.
- Tool runs: bounded status: No text between routine calls.
- User's language: Compress the style, not the language.
- Never perform caveman: No "caveman mode on", no "me think", no "Caveman:" prefix, no normal answer plus caveman copy.

Switch: /caveman (default), /ultracave (fragments, each fact once), /megacave (Classical Chinese 文言文)
Stop: "stop caveman" or "normal mode"

Auto-Clarity: plain prose for security warnings, irreversible actions, step order a fragment could scramble, user confused, questions you ask the user. Resume after.
Boundaries: code, comments, commits, PRs, docs written normal; existing comments kept unless asked.
Floor: code, commands, paths, numbers and error strings verbatim; never drop not/never/no/only.
<!-- caveman-end -->

## Project Overview

Native Go shared-library plugin for CLIProxyAPI. It resolves configured model aliases into resilient model/account routing, forwards requests through CPA host callbacks, and serves configuration and usage dashboard assets.

## Architecture & Data Flow

`main.go` builds `modelComboPlugin` from YAML and exposes plugin SDK hooks. `abi.go` bridges CPA native ABI calls to Go. `host_callbacks.go` is sole callback adapter: route host RPCs through `callbackModelHost.call`.

Request path: CPA plugin hook → `execute.go` or `stream.go` → immutable plans from `combo.go`/`config.go` → selector/runtime state in `selector.go`/`health.go` → CPA host execution callback. Account pools compile in `account.go`; only callback metadata resolves configured accounts. Stream fallback ends after first emitted payload.

Mutable selection and circuit state stays in `comboRuntime`, protected by its mutexes. Plans are immutable after config load. Preserve callback boundaries and avoid direct provider transport or credential-file access.

## Key Directories

- Root `*.go`: plugin ABI, config, routing, execution, storage, management, and unit tests; package is `main`.
- `dashboard/`: static dashboard UI. `app.js` wires shared helpers plus `configuration/` and `usage/` tabs.
- `docs/`: operator documentation and sprint/audit evidence.
- `.github/workflows/`: CI and release automation.
- `.github/xgo/`: release cross-compilation scripts.

## Development Commands

```bash
make fmt-check          # reject unformatted root Go files
make test               # go test ./...
make race               # go test -race ./...
make vet                # go vet ./...
make check              # fmt-check, test, race, vet
make build              # CGO shared library in dist/
node --check dashboard/configuration/tab.js
```

Run targeted tests while changing routing:

```bash
go test ./... -run 'TestExecute|TestExecuteStream|TestComboSelector' -count=1
```

CI also syntax-checks `.github/xgo/*.sh` and runs CPA integration using `CPA_SOURCE`:

```bash
CPA_SOURCE=/path/to/CLIProxyAPI go test -tags=integration ./... -count=1
```

## Code Conventions & Common Patterns

- Format root Go files with `gofmt`; do not hand-format generated whitespace.
- Keep production and tests in package `main`; tests use table-like direct construction and `fakeModelHost` callbacks.
- Validate untrusted YAML at config load. Use trimmed, normalized keys only where existing helpers do; preserve exact CPA metadata matches for account resolution.
- Return contextual errors; use `statusError`/`newRouteError` for HTTP-visible failures. Do not leak auth IDs, indexes, credential data, or callback payloads.
- Add behavior tests for new branches, retry boundaries, parsers, concurrency, and stream commitment. Assert observable host calls, selected `AuthID`, and emitted payloads—not implementation internals.
- Keep host integration behind `modelHost`. All CPA RPCs must flow through `callbackModelHost.call`; inject `hostRawCaller` for callback tests.
- Do not hold runtime locks across host callbacks, retries, sleeps, stream reads, or storage work.
- Dashboard is plain JavaScript/HTML/CSS with global shared state/functions. Reuse `dashboard/shared.js` and existing configuration/usage patterns; no framework or Node dependency implied.

## Important Files

- `main.go`: plugin construction and SDK entry points.
- `abi.go`: C ABI exports and native host bridge.
- `config.go`: YAML schema, normalization, validation, legacy route compatibility.
- `combo.go`, `selector.go`, `health.go`: compiled route plans, candidate selection, circuit state.
- `account.go`: account-pool planning and account selection.
- `execute.go`, `stream.go`: sync/stream routing and retry behavior.
- `host_callbacks.go`: CPA callback JSON protocol adapter.
- `management.go`: management resources and validation APIs.
- `dashboard/configuration/tab.js`: configuration editor behavior.
- `Makefile`, `go.mod`: supported local commands and Go version.

## Runtime/Tooling Preferences

Use Go version declared by `go.mod` (currently `1.26.0`). CGO is required for `make build` because output is a CPA-native shared library. Use `go` and `make`; this repository has no root Node package manager manifest. Node is only needed for static JavaScript syntax checks.

CPA dependency is pinned to `github.com/router-for-me/CLIProxyAPI/v7 v7.3.9`. Keep plugin ABI and callback payloads compatible with that version.

## Testing & QA

Before delivery, run narrow tests for changed routing path, then relevant `make` target(s). Non-trivial Go changes require at least `make check`; native ABI/build changes also require `make build`.

Integration tests need a disposable CPA source tree via `CPA_SOURCE`; do not claim integration coverage without observed host execution. CI verifies formatting, root Go tests, race detection, vet, release script syntax, CPA integration, and native build.
