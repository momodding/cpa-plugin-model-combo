# ADR 0001: Stock v8 trace and account routing

## Status and provenance

Accepted. This is a decision for this repository, not an assertion that the sibling `cpa-model-combo-request-id-hierarchy` repository's records already governed it. With user approval, it supersedes that sibling's ADR 0005 stock-v7 identity limitation, while retaining the hierarchy and historical-data intent of its ADR 0004. The sibling's custom schema-7 requirement does not apply to stock v8.

## Context

Stock CLIProxyAPI v8.0.37 exposes inbound trace and execution request identities in delivered usage records, lifecycle metadata, credential identities, and per-credential model registry membership. Registry membership does not establish entitlement. Usage records do not carry the inbound endpoint; CPA lifecycle metadata supplies `request_path`, which can be a route template. The usage stream is not a complete transport-attempt feed.

## Decision

- Use the stock v8 SDK and plugin ABI, without modifying CPA. Keep the trace ID, execution request ID, and lifecycle request ID distinct. Store inbound sessions additively in SQLite schema 2 and preserve historical request payloads and legacy grouping.
- Join delivered executions only to an unambiguous trace; preserve separate lifecycle IDs to detect collisions. Completion determines parent outcome. Do not synthesize children, usage, or cost when no usage callback was delivered.
- Persist safe credential IDs/indexes and sanitized label or email snapshots. Refresh snapshots through valid host callback contexts, not during usage ingestion. Never persist tokens, raw metadata, completion errors, or guessed historical identities.
- Empty or omitted `allowed_accounts` is unrestricted. Restricted routes pin each execution to an enabled allowed credential and provider, using fresh registry membership; failures never relax the restriction. Model membership denotes compatibility only; entitlement remains unknown.
- Rollback restores a consistent pre-upgrade configuration/database backup and matching old CPA/plugin artifacts. Do not run old writers against schema 2 or downgrade the schema in place.

## Consequences

Known limitation: stock v8.0.37 invalidates the host callback instance (`loader_unix.go` `close()`) before running plugin shutdown. During unload the plugin therefore cannot close streams or emit its stalled-callback WARN through the host, and the `TestNativeUnloadAndAccountingDiagnostics` subcases (`read_stream`, `emit`, `stalled_log_callback`) fail in CI run 43980957529. This is a host lifecycle ordering issue that needs a CPA change; the plugin does not work around it and the diagnostics are not relaxed.

The deployment must pair the plugin with CPA v8.0.37. Some direct traffic can have unavailable labels before a catalog refresh; labels already stored may outlive deleted accounts. Lifecycle-only failures remain visible, but unreported internal attempts cannot be shown. Trace collisions must remain explicitly ambiguous. These privacy and accuracy limits take precedence over complete-looking histories.
