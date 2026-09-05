---
name: agent-ready-map
description: Map an unfamiliar codebase for agent use by locating high-fan-in modules, cycles, shared state, cascading dependencies, runtime entry points, and operational knowledge. Use when onboarding to a system or preparing architecture artifacts.
---

# The Map

Create a navigable model before changing architecture.

## Workflow

1. Establish scope: repositories, services, deploy targets, language/toolchain, and the requested capability.
2. Inventory entry points, package manifests, build scripts, tests, schemas, queues, HTTP/RPC handlers, jobs, and configuration.
3. Build dependency evidence. Prefer existing tools; otherwise use targeted searches and small scripts. Identify high fan-in modules, cycles, global mutable state, dynamic loading, and cross-service calls.
4. Trace one representative request or job end to end, including persistence and external effects.
5. Ask which findings are static versus runtime-observed. Do not present guesses as facts.
6. Write durable artifacts under `docs/architecture/` unless the repository has another convention:
   - `system-map.md`: scope, entry points, major components, and data flows
   - `dependency-map.md`: important edges, fan-in, cycles, and confidence
   - `context-index.md`: where an agent should look for each concern
   - `service-contracts.md`: API/event ownership, schemas, compatibility rules
   - `decisions/`: architecture decision records for material conclusions
   - `runbooks/`: build, test, deploy, rollback, and incident procedures
7. Validate links, commands, diagrams, and file paths. Record unknowns and follow-up probes.

## Required output

Report the inspected scope, evidence gathered, risk hotspots, generated files, unresolved unknowns, and the smallest next mapping task. Keep maps maintained as code changes.

## Guardrails

- Do not rewrite code merely to make the map cleaner.
- Do not infer ownership or runtime behavior from names alone.
- Preserve sensitive values; document configuration keys without copying secrets.
- Prefer reproducible commands and machine-readable inventories where useful.
