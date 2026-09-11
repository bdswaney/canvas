# nPly

nPly is a collaborative Markdown workspace built with Go, React, and TypeScript. Organize documents into projects, edit together in real time, and save versions to a shared history. MCP clients can read and edit documents alongside browser users.

The editor uses CodeMirror and Yjs, with a GitHub-flavored Markdown preview, shared cursors, and undo for your own edits. PostgreSQL stores accounts, documents, saved versions, and live edits. The Go server serves the web app, collaboration relay, and MCP endpoint from one binary.

## Quick start

Install [Mise](https://mise.jdx.dev/getting-started.html) and Docker. The local database task uses Docker; you can use an existing **PostgreSQL 18 or newer** instance instead.

```sh
git clone https://github.com/bdswaney/nPly.git
cd nPly

mise trust
mise install
mise run deps
mise run build

mise run db
export DATABASE_URL='postgres://canvas:canvas@127.0.0.1:5432/canvas?sslmode=disable'

mise run createuser alice 'replace-with-a-local-password'
mise run serve
```

Open <http://127.0.0.1:8080>, sign in, and create a project and document.

The first account must be created from the command line. Replace the example password before running the command; CLI passwords are visible in shell history and process arguments. Migrations run automatically when the server or an administrative command starts.

`mise run serve` watches the source with Air, rebuilds the app, and restarts the server. It uses a development build that allows session cookies over plain HTTP. Refresh the browser after a rebuild.

The initial `mise run build` creates `dist/`, which Go embeds at compile time. It is required before running Go commands on a fresh checkout, including `createuser` and `migrate`.

## Development

Tool versions and repeatable commands live in [mise.toml](mise.toml). Use Mise rather than relying on Go or Node from your shell's `PATH`.

```sh
mise run doctor        # check installed tools
mise run typecheck     # check TypeScript
mise run build         # type-check and build the frontend
mise run dev:server    # build and run once, without the watcher
mise run db:stop       # stop the local database container
```

For frontend hot reload, keep the Go server running and start Vite in another terminal:

```sh
mise run dev
```

Open the URL Vite prints. It proxies `/api`, including WebSockets, to `http://127.0.0.1:8080`. If you change the backend address, update the target in [vite.config.ts](vite.config.ts).

Keep `DATABASE_URL` available in every shell that runs the server or administrative commands. Put machine-specific overrides in the ignored `mise.local.toml`; do not commit credentials. Commit dependency changes with their lockfiles: `package-lock.json`, `go.sum`, and the Rust shim's `Cargo.lock`.

### Tests

```sh
mise run test
```

This builds the frontend and runs `go test ./...`. Database-backed store, token, and session integration tests require `DATABASE_URL`; they are skipped when it is unset. Use a separate, disposable PostgreSQL database for tests, not a production database.

The Yjs conformance tests run Node against the installed `yjs` package. Run `mise run deps` first; those tests are skipped if `node_modules/yjs` is missing.

### Rebuilding the Yjs engine

The server runs the Rust [yrs](https://github.com/y-crdt/y-crdt) library as WebAssembly through [wazero](https://github.com/tetratelabs/wazero). The compiled module is checked in at `internal/ydoc/ydoc.wasm`, so normal Go builds and tests do not need a Wasm rebuild.

After changing `internal/ydoc/shim/`, use the pinned Rust toolchain and install its Wasm target:

```sh
mise exec -- rustup target add wasm32-wasip1
mise run build:ydoc
mise exec -- go test ./internal/ydoc -count=1
```

The Rust build also needs a native C compiler/linker for build dependencies. On Debian or Ubuntu, install it with `sudo apt-get install build-essential`. Include the rebuilt `ydoc.wasm` with any shim changes. Conformance tests check browser/server compatibility, including UTF-16 offsets and concurrent edits.

## Deployment

Build the production binary:

```sh
mise run build:server
```

The executable is still named `bin/canvas`. It includes the frontend and Wasm module. It needs PostgreSQL at runtime, but not Node, Rust, or a separate `dist/` directory. Rebuild it after frontend changes. `mise run preview` is a frontend preview server, not a deployment command.

The CLI provides help and shell completion without connecting to PostgreSQL:

```sh
./bin/canvas --help
./bin/canvas token --help
./bin/canvas completion bash
```

Run the binary without a subcommand to start the web server. Invalid commands and arguments return a nonzero exit status before database initialization.

Run the production binary behind an HTTPS reverse proxy that supports WebSockets. Production cookies require HTTPS; do not deploy a binary built with `-tags insecurecookie`.

| Variable | Purpose |
| --- | --- |
| `DATABASE_URL` | Required PostgreSQL connection string. PostgreSQL 18+ is required by the session schema. |
| `COOKIE_KEY` | Base64-encoded key containing at least 32 random bytes. Keep it stable across restarts. If unset, a key is generated and printed at startup, and existing sessions are invalidated on restart. |
| `ADDR` | Listen address; defaults to `127.0.0.1:8080`. |
| `CANVAS_ENV` | Set to `production` for deployment. Any nonempty value other than `development` requires explicit `ORIGINS`. |
| `ORIGINS` | Comma-separated allowed WebSocket origin patterns, such as `nply.example.com`. Unset development configurations allow ngrok wildcard hosts. |

Generate a cookie key once and store it with your deployment secrets:

```sh
head -c 32 /dev/urandom | base64
```

With `DATABASE_URL` and `COOKIE_KEY` set in the environment:

```sh
CANVAS_ENV=production \
ORIGINS=nply.example.com \
ADDR=127.0.0.1:8080 \
./bin/canvas
```

Replace the example hostname with your own. Bind beyond loopback only when required by your deployment. The server applies embedded migrations at startup; back up PostgreSQL before upgrades. Saved versions, unsaved edits, and account data all depend on that database.

If a rebuilt binary appears to serve an old frontend, compare its embedded asset name with `dist/assets/`:

```sh
strings bin/canvas | grep -o 'index-[A-Za-z0-9_-]*\.js' | sort -u
```

For temporary local sharing, install ngrok and run `mise run tunnel`. It targets port 8080 by default; `PORT` changes the tunnel target, not the server's listen address. Do not use the development wildcard-origin settings in production.

## MCP

### HTTP

Use the web server's `/api/mcp` endpoint with an MCP client that supports Streamable HTTP. This mode shares the browser collaboration relay, so edits reach users who already have the document open.

Create a token for an existing account:

```sh
mise run token create alice laptop
```

The secret is printed once; store it securely. Configure your MCP client with:

```text
URL: https://nply.example.com/api/mcp
Authorization: Bearer <your-token>
```

Use `http://127.0.0.1:8080/api/mcp` for a local development server. Tokens act as the named account and use the same project membership checks as browser requests. The server stores only a hash of the secret.

```sh
mise run token list alice
mise run token revoke 'replace-with-token-id'
```

### Stdio

A local MCP client can launch the built binary with `mcp` and an existing username:

```sh
./bin/canvas mcp alice
```

Pass `DATABASE_URL` through the client's environment. No access token is needed in this mode; the local process already has database access. For development, the equivalent command is `mise run mcp alice`.

Stdio runs in a separate process with its own relay. Its edits are stored, but do not reach browsers already connected to the web server; those browsers may overwrite them. Prefer HTTP for documents that may be open.

### Tools

| Tool | Action |
| --- | --- |
| `list_projects` | List the account's projects, with structured project metadata. |
| `create_project` | Create a project and become its first member. |
| `list_documents` | List accessible documents, optionally within one project, with structured metadata. |
| `read_document` | Read live text, including unsaved edits, or a specified saved version. Live reads also return a `baseVersion`. |
| `document_history` | List saved versions and their authors. |
| `create_document` | Create an empty document in a project. |
| `edit_document` | Apply a desired full-text value as a collaborative live edit without creating history. Pass `baseVersion` to refuse the edit if the document changed since it was read. |
| `save_document` | Save the current live text as a new history version. |
| `upsert_document` | Create or update an imported document by stable `sourceKey`; optionally save it in one call. |
| `archive_document` | Hide a document while keeping its saved versions. Marked destructive so clients can ask first. |

MCP `edit_document` calls update live text but do not create saved versions. Use `save_document` or Save in the browser to add the current text to history. `upsert_document` is intended for repeatable imports: its `sourceKey` is stable within a project, so repeating the call updates the existing document instead of creating a duplicate.

Edits are diffed against the current text, by line and then by character, and each change is applied separately. Text an edit leaves unchanged keeps its identity, so people typing there keep their work and their cursor position.

A diff cannot tell a client's change from one somebody else made after the client read the document, so `read_document` returns a `baseVersion` (a hash of the text it read) and `edit_document` accepts it back. If the text has changed since, nothing is written and the result carries the current text and its `baseVersion`; redo the edit against that. Without `baseVersion`, the text is applied to the document as it is now, which reverts any change the client did not see. Stale edits are refused rather than rebased because the server does not keep the text a client read.

An archived document no longer appears in `list_documents` and cannot be read, edited, saved, or archived again through MCP; unknown and inaccessible ids get the same answer. There is no search tool yet.

## Documents and access

Live edits are journalled to PostgreSQL as Yjs updates. **Save** records a separate version containing the Markdown text and a CRDT snapshot. Closing and reopening a document does not discard edits that have reached the server, even if they have not been saved to version history.

**Restore** replaces the live text with a selected saved version. It leaves existing history intact and does not automatically save a new version. Concurrent typing can merge into the restored text, so coordinate restores with other editors when you need an exact result.

Archiving hides a project or document without deleting its history. Archiving a project also hides its documents. There is no unarchive operation yet.

Project membership controls access to documents and MCP tools. Creating a project makes you a member. There are no owner or administrator roles within a project: any member can manage membership, and the last member cannot leave. Signed-in users can see the account list used by the member picker.

WebSocket authorization is checked when a connection opens. Signing out elsewhere or removing a member does not close their existing sockets; access is checked again when they reconnect.

### Storage and compaction

After a document's last browser disconnects, the relay attempts to compact its active update journal if it has at least 32 entries. Compaction merges those entries into one update and marks the originals as superseded. It does not delete the old rows or trim saved history, so it reduces replay work rather than providing a database retention policy. Saving a version does not compact the journal.

Back up the database, not just saved Markdown: the journal can contain edits that have never been saved to history.

## Source layout

```text
src/
  app/          Application shell and routing
  pages/        Projects, project details, and document workspace
  editor/       CodeMirror integration and Markdown preview
  collab/       Yjs provider, presence, and remote pointers
  api/          HTTP client and session hooks
  components/   Shared UI components

internal/
  api/          REST handlers
  auth/         Sessions and access tokens
  mcp/          MCP tools
  relay/        WebSocket relay, update injection, and compaction
  store/        PostgreSQL and in-memory stores
  migrate/      Embedded app and session migrations
  ydoc/         Go/Wasm Yjs engine and compatibility tests
  lib0/         Yjs wire-format helpers
  server/       HTTP routes and static asset serving
```

`main.go` wires the services together; `embed.go` embeds the frontend. Browser routes are `/`, `/project/<id>`, and `/doc/<id>`. The server supports direct links to these routes.

## Open work

The [issue tracker](https://github.com/bdswaney/nPly/issues) covers current work, including [document search](https://github.com/bdswaney/nPly/issues/18), [finer-grained MCP edits](https://github.com/bdswaney/nPly/issues/19), [MCP archiving](https://github.com/bdswaney/nPly/issues/20), [editor/preview modes](https://github.com/bdswaney/nPly/issues/7), [version diffs](https://github.com/bdswaney/nPly/issues/9), and [inline comments](https://github.com/bdswaney/nPly/issues/8).
