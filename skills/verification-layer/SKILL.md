---
name: verification-layer
description: Build an agent-checkable verification layer around existing software using characterization tests, API contract tests, integration checks, and end-to-end traces. Use before refactors, migrations, rewrites, or when behavior is poorly documented.
---

# The Verification Layer

Turn current behavior into an executable specification without silently redefining it.

## Workflow

1. Define the change boundary and critical user or business flows.
2. Inventory current tests, observability, fixtures, schemas, public APIs, and known failure modes.
3. Capture characterization tests for important existing behavior, including edge cases and error semantics. Label tests that preserve a bug or compatibility quirk.
4. Add contract tests at API, event, database, or CLI boundaries. Check schemas, status/error behavior, ordering, idempotency, retries, and compatibility.
5. Add focused integration tests for persistence and external dependencies using deterministic fakes or isolated test resources.
6. Add an end-to-end trace for each critical flow, connecting input, code path, side effects, and observable result.
7. Make verification runnable by an agent: document setup, commands, fixtures, expected output, cleanup, and flaky-test policy.
8. Run the narrowest checks first, then the full relevant suite. Record baseline failures separately from regressions.

## Required output

Leave tests, fixtures, trace documentation, and a verification command such as `make verify` or an equivalent. Summarize behavior covered, behavior intentionally not covered, and remaining confidence gaps.

## Guardrails

- Do not change production behavior solely to make a test pass.
- Avoid snapshots that hide meaningful differences; assert stable semantics.
- Never use live production data or credentials in fixtures.
- Keep tests deterministic, isolated, and safe to rerun.
