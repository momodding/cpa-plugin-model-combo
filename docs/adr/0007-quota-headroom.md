# ADR 0007: Quota headroom routing

## Status

Accepted.

## Decision

Headroom routing is opt-in. Current provider quota is acquired through the local management API and fixed provider endpoints, never inferred from usage. Missing, failed or expired observations use the configured unknown remaining percentage, initially 50%, rather than stale observations. The value is a routing score, not a measured quota.

Diagnostics exclude credentials, raw headers and upstream responses. Account eligibility and credential privacy remain governed by ADR 0006; account registry membership does not establish entitlement.
