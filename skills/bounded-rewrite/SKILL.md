---
name: bounded-rewrite
description: Replace a bounded component with a modern implementation when incremental migration is slower or riskier, using the existing system as specification and oracle. Use for deliberate rewrites with dual-run comparison and staged cutover.
---

# The Rewrite

Rewrite only a bounded component with an explicit oracle, comparison strategy, and exit plan.

## Workflow

1. Define the component boundary, non-goals, callers, data it owns, performance envelope, and cutover authority.
2. Capture the existing system's behavior through characterization and contract tests. Gather representative, sanitized fixtures and production-shaped workloads.
3. Choose the new language/runtime/packages and record operational, staffing, dependency, and rollback consequences in an ADR.
4. Build the new implementation behind a compatible interface. Keep the old system runnable.
5. Run both systems against the same inputs; compare outputs, errors, timing budgets, emitted events, and persisted effects. Use dual-read or carefully designed dual-write where needed.
6. Triage every divergence. Fix the implementation, update the specification only with explicit approval, or document an intentional compatibility change.
7. Cut over in stages with feature flags, health checks, metrics, rollback, and data reconciliation. Remove the old implementation only after an agreed soak period.

## Required output

Leave a bounded-scope ADR, compatibility contract, oracle fixtures, differential test harness, performance baseline, cutover/rollback runbook, and deletion criteria.

## Guardrails

- A rewrite without an oracle is a second implementation, not a migration.
- Do not expand scope while parity work is unresolved.
- Preserve security, authorization, transactional, and failure semantics—not only happy-path outputs.
- Do not delete the old system until rollback and data recovery have been demonstrated.
