---
name: boundary-redraw
description: Redesign software boundaries so agents can reason about units in context, using dependency evidence to modularize a monolith or consolidate fragmented services. Use when ownership, coupling, or change impact is unclear.
---

# The Boundary Redraw

Choose boundaries that make dependencies, ownership, and verification predictable.

## Workflow

1. Start from a concrete change or operational pain, not an abstract desire for microservices.
2. Use a dependency map to find high fan-in code, shared state, cycles, transaction boundaries, data ownership, and change coupling.
3. Propose candidate boundaries and compare them by cohesion, coupling, deployability, failure isolation, latency, data consistency, and agent context size.
4. Choose the smallest reversible boundary. Write an ADR with rejected alternatives and consequences.
5. Establish ownership and explicit interfaces before moving implementation. Add contract and characterization tests.
6. Enforce the boundary with package/module rules, dependency checks, linting, or build constraints.
7. Move one slice, validate behavior and operations, then repeat. Consolidate services when fragmentation creates more coupling than autonomy.

## Required output

Leave an ADR, before/after dependency sketch, ownership table, boundary enforcement check, migration slices, and verification commands.

## Guardrails

- A service boundary is not automatically better than a module boundary.
- Do not split shared databases or transactions without a consistency plan.
- Do not hide cycles behind interfaces; remove or explicitly isolate them.
- Keep a rollback path for every moved slice.
