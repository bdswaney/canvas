---
name: headless-split
description: Separate UI concerns from data and domain behavior behind stable APIs, schemas, and contract tests. Use when a legacy frontend constrains backend work or agents need independent UI and backend change surfaces.
---

# The Headless Split

Create a headless capability that can be exercised without a browser.

## Workflow

1. Select one bounded capability and document its current UI-to-data flow.
2. Separate domain rules, persistence, authorization, and orchestration from rendering, routing, and view state.
3. Define a stable interface: commands/queries, inputs, outputs, errors, pagination, identity, and versioning. Use existing conventions where possible.
4. Implement the headless layer behind the current UI first; preserve behavior through characterization and contract tests.
5. Replace UI data access with the interface incrementally. Keep presentation-specific transformations at the edge.
6. Add independent domain tests and a thin UI integration test proving the contract is consumed correctly.
7. Document ownership, compatibility, and how another client can call the capability.

## Required output

Produce an interface/schema, implementation boundary, contract tests, migration notes, and a removal checklist for old UI-coupled paths.

## Guardrails

- Do not invent a generic platform abstraction before a real capability boundary exists.
- Keep authorization and validation explicit; never rely on the UI for security.
- Avoid duplicating business rules in the UI and headless layer.
- Preserve observable behavior unless the requested change explicitly changes it.
