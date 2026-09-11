# nPly contributor guidance

## Tooling and verification

- Use Mise-managed tools and the tasks in `mise.toml`. Do not change pinned tool versions to work around local environment problems.
- Install locked frontend dependencies with `mise run deps`. Go embeds `dist/`, so build the frontend before compiling Go. `mise run test` and `mise run build:server` already depend on the frontend build.
- Run checks appropriate to the change. `mise run test` builds/type-checks the frontend and runs the Go suite; `mise run build:server` builds the deployable binary. Documentation-only changes need source and diff checks, not an application rebuild.
- Report skipped tests explicitly. Database-backed tests skip without `DATABASE_URL`; Yjs conformance tests skip without `node_modules/yjs`. A passing suite with those skips does not establish database or Yjs interoperability coverage.
- Use isolated, disposable PostgreSQL 18+ databases for integration tests and administrative-command smoke tests. Do not mutate an existing database without explicit authorization. Remove only test resources you created; do not treat the persistent `mise run db` container as disposable.

## CLI and protocol output

- Keep help, shell completion, and argument validation independent of database, auth, and Wasm initialization. Initialize services only after valid command selection and argument validation, and only where needed.
- Keep diagnostics on stderr. Token secrets, token listings, and stdio MCP messages must remain clean on stdout; help and completion scripts also belong on stdout.
- The session dependency prints generated cookie keys directly to `os.Stdout`. Preserve the protection in `executeCLI`, and exercise startup with `COOKIE_KEY` unset when changing initialization or output routing.
- Tests that replace process-global streams must run serially and restore the original streams on both success and failure.

## CRDT changes

- Keep the Rust shim, Go ABI, and checked-in `internal/ydoc/ydoc.wasm` synchronized. After changing the shim, run `mise run build:ydoc` and include the rebuilt artifact in the change.
- Preserve the fresh, nonzero client-ID generation for independent server edits. Reusing an identity from the same document state can silently discard concurrent edits.
- When changing edit generation, test concurrent-update merging and interoperability with the browser's Yjs implementation. Go-only round trips are not a substitute for cross-implementation conformance.

## Naming and worktrees

- Use **nPly** in product prose. Preserve actual `canvas` executable, Go module, and configuration identifiers, including `CANVAS_ENV`, unless a compatibility migration is explicitly in scope.
- Confirm the target worktree, branch, and existing changes before editing or committing. Work in the designated worktree, stage only intended files, and never accidentally stage a nested worktree.
