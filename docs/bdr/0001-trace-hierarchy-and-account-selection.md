# BDR 0001: Trace hierarchy and account selection

## Status and provenance

Accepted for this repository. With user approval this supersedes the sibling `cpa-model-combo-request-id-hierarchy` BDR 0005's stock-v7 identity limitation and retains BDR 0004's hierarchy and historical-data intent, adapted to stock CPA v8.0.37. The sibling records are provenance, not previously binding decisions here.

## Request details

A trace identifies a logical inbound parent; lifecycle request IDs remain separate so collisions are not silently merged. A completed parent can have no children. Each delivered execution usage record is one child, sorted by start time then stored sequence. Its display ordinal is not a claim about network attempts. Parent outcome and timing come from inbound completion, independently of child failure and numeric status. Endpoint metadata may be a CPA route template. Legacy identity-less rows retain historical grouping and are not joined by inferred trace. Ambiguous traces remain unassigned with a warning.

Filtering and pagination operate on parents and retain all children of a matching parent. Overview, breakdown, tokens and costs count delivered execution records, not parent rows. Expandable details expose provider, model, safe credential label and result without displaying raw completion errors or inventing unreported executions.

## Configuration

The authenticated account catalog groups registered models by credential and shows entitlement as unknown: registry membership is not proof of access. No selected accounts means all accounts allowed. Selected IDs are ordered, exact and unique. Stale or disabled selections remain visible for removal; incompatible targets are not silently deleted. Saving a restriction validates that each selected account is enabled and offers at least one target, and each target is offered by at least one selected account. Catalog failure fails closed.

Restricted execution tries eligible accounts in saved order for each chosen target. Retryable failures may advance to another permitted account or target; nonretryable errors and cancellation stop. Streaming never switches after the first payload. Unrestricted routes continue to let CPA select credentials.
