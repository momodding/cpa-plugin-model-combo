# BDR 0007: Quota tracking and headroom

## Status

Accepted.

## Decision

A Quota tracking dashboard configures polling and displays current provider observations and reasons for unknown quota. Headroom routes rank eligible accounts by observed remaining percentage or the configured unknown default, initially 50%. A failed or expired observation immediately becomes unknown; prior values are not presented as current or used for routing. Other routing strategies are unchanged. Existing account restrictions and privacy guarantees remain in force.
