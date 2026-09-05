---
name: strangler-migration
description: Migrate a capability incrementally by placing a proxy, facade, adapter, or anti-corruption layer at a stable seam and routing traffic from an old implementation to a new one. Use for low-risk legacy replacement.
---

# The Strangler

Make old and new implementations coexist while the seam remains observable and reversible.

## Workflow

1. Choose one capability with a clear request/event boundary and define success metrics.
2. Characterize legacy behavior and dependencies. If no API exists, create a facade or intercept events without changing callers.
3. Define the seam contract, ownership, timeouts, retries, idempotency, error mapping, and data consistency rules.
4. Implement the new path behind the seam. Start with shadow reads, dark launches, or internal traffic where safe.
5. Compare old and new results with normalized diffs; classify expected differences, defects, and nondeterminism.
6. Route a small cohort or capability slice, monitor latency, errors, side effects, and data drift, and keep rollback immediate.
7. Expand routing gradually, delete migrated legacy paths, then remove the transitional seam only after evidence and cleanup checks pass.

## Required output

Produce a migration plan, seam contract, routing/rollback controls, comparison report, dashboards or logs, and a legacy-removal checklist.

## Guardrails

- Never dual-write without idempotency, reconciliation, and failure handling.
- Do not shadow operations that cause irreversible side effects unless explicitly neutralized.
- Keep old and new paths independently diagnosable.
- Treat data drift and rollback readiness as release gates.
