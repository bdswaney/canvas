# Canvas

A collaborative artifact workspace built with Go and TypeScript. The initial focus is shared state and synchronization, before editing UI or rendering.

## Toolchain

Mise is the entry point for project tooling and commands. Go, Node.js, and Air versions are pinned in `mise.toml`; npm comes with Node.js. Install mise before continuing.

From the repository root:

```sh
mise trust
mise install
mise run doctor
```

Run tools through mise to use the pinned versions without changing your shell configuration:

```sh
mise exec -- go version
mise exec -- node --version
mise exec -- npm --version
```

Optionally enable automatic tool selection in Bash by adding `eval "$(mise activate bash)"` to `~/.bashrc`.

## Dependency policy

- Declare development tools and runtimes in `mise.toml` with exact versions. Do not rely on globally installed Go, Node.js, or npm.
- Manage JavaScript/TypeScript libraries with npm through mise; commit `package.json` and `package-lock.json` when introduced. Use `mise exec -- npm ci` for reproducible installs once a lockfile exists.
- Manage Go libraries with Go modules through mise; commit `go.mod` and `go.sum` when introduced.
- Define repeatable project workflows as mise tasks as implementation is added. CI should use the same pinned tools and tasks.
- Do not commit credentials or machine-specific overrides. Use ignored `mise.local.toml` for local overrides.

## Frontend

The frontend uses React, TypeScript, Vite, and Mantine's off-the-shelf components. It contains a sign-in screen, an application shell, a connection status header, light and dark schemes, live peer pointers, and a collaborative Markdown editor bound to a Yjs document, with a rendered preview beside it.

```sh
mise run deps       # install dependencies from package-lock.json
mise run dev        # start Vite; open the URL printed in the terminal
mise run typecheck  # check TypeScript
mise run build      # type-check and build into dist/
mise run preview    # serve the existing build locally (not for production)
```

`src/main.tsx` loads Mantine's styles and provider. `src/App.tsx` holds the session gate and the one route shape the app has, `/doc/<id>`; `src/DocPicker.tsx` lists and creates documents and `src/Workspace.tsx` is the editor, preview, save button, and history drawer. `src/sync.ts` holds `useSync`, which owns one `Y.Doc`, its awareness state, and its relay connection, plus `useSharedText`, which hands out a named `Y.Text`. `src/api.ts` and `src/useSession.ts` handle sign-in and keep the session alive, `src/presence.ts` and `src/Cursors.tsx` add live pointers, `src/Editor.tsx` with `src/markdown.ts` binds a `Y.Text` to a Markdown-aware CodeMirror, and `src/Preview.tsx` renders that text beside it.

`mise run dev` proxies `/api` (WebSockets included) to `http://127.0.0.1:8080`, so run `mise run serve` alongside it when working on the frontend.

## Collaboration relay

`/api/sync/doc/{docID}` is a WebSocket endpoint speaking the y-websocket protocol, one socket per document. A socket for a document that does not exist is closed with code 4404 rather than conjuring a journal nothing can read.

The server does not interpret document contents. It keeps an append-only journal of Yjs updates per document, replays that journal to each joining client, and broadcasts every new update to the document's other clients. Yjs updates are idempotent and commutative, so replaying the log reconstructs the document. Awareness (presence) frames are relayed but never stored.

```
client                                  server
  |  <-- sync step 1 (empty vector) ------|   asks for state the client already has
  |  --- sync step 1 (state vector) ----->|
  |  <-- sync step 2 (each logged update) |   replayed history
  |  <-- sync step 2 (empty update) ------|   marks the client synced
  |  <-> update / awareness ------------->|   relayed to the other clients
```

The journal is unbounded: documents grow with every keystroke and are never compacted. Saving does **not** trim it — see the note under Saving below. Measured on a room with 399 updates, a joining client is sent 400 frames totalling 41 kB, replayed from memory in about 4 ms on loopback — cheap now, but it grows without limit and every joining client pays it. Squashing the log into a snapshot needs a Yjs implementation on the server and is deliberately left for later.

## Editing and carets

`src/Editor.tsx` binds a `Y.Text` to CodeMirror 6 through `y-codemirror.next`, which handles character-level synchronization in both directions and draws every peer's caret and selection in the color that peer publishes. Undo is scoped to each client's own edits with a `Y.UndoManager`, so undo never reverts someone else's typing.

`src/markdown.ts` adds Markdown parsing and the editing commands that come with it: Enter continues a list or blockquote, and Backspace at the start of an item removes the marker. The grammar's GFM bundle is enabled, which adds tables, task lists, strikethrough, and bare-URL autolinks. Note that GFM table headers carry the generic `heading` tag rather than `heading1`-`heading6`, so they need their own highlight rule. Styling leans on weight and size rather than color, and dims the `#`, `*`, and `` ` `` markers so they stop competing with the text. The document is Markdown source, not a rendered preview; rendering is still out of scope.

## Color scheme

The app starts on `auto`, following the system, and the header toggle sets a scheme explicitly; Mantine remembers the choice. An inline script in `index.html` applies the stored scheme before React mounts so a reload does not flash the wrong one — Mantine ships that script for server rendering only, so this repeats its logic against the same storage key.

The editor follows along. Its own colors come from Mantine's CSS variables, and the Markdown highlight style has a light and a dark variant, each scoped with `themeType` so exactly one matches — an unscoped style applies to both schemes and wins on precedence, which is easy to miss because the light scheme still looks right. Toggling reconfigures the theme through a CodeMirror compartment rather than rebuilding the editor, so the document, selection, and peers' carets stay put.

Peer colors are mid tones that read on either background, and remote selections use a translucent tint of the peer's color instead of a pastel: a peer publishes one color to viewers on both schemes.

## Preview

`src/Preview.tsx` renders the shared Markdown through remark, beside the editor on wide screens and below it on narrow ones. `remark-gfm` keeps the preview reading the same dialect the editor highlights, so tables, task lists, strikethrough, and autolinks mean the same thing on both sides. Note that this is a second parser: the editor highlights with Lezer's incremental grammar, and the preview parses the whole document with remark. They agree on GFM by configuration, not by construction.

The document is written by one person and rendered in everyone else's browser, so it is treated as untrusted. Raw HTML in the source is never parsed (`rehype-raw` is deliberately not installed, so a `<script>` tag renders as nothing at all), `rehype-sanitize` drops anything outside its allowed schema, and react-markdown rejects `javascript:`, `data:`, and `vbscript:` URLs, leaving the link text with no `href`.

Rendering is driven by `useTextSnapshot`, which mirrors the `Y.Text` into React state on a trailing 150 ms debounce, so a burst of keystrokes — local or from a peer — costs one parse rather than one per character.

## Presence and cursors

Every client publishes an identity and its pointer position on the awareness channel. The identity is the signed-in username, with a color derived from it by hashing, so a person looks the same to everyone on every device with nothing to keep in sync. Pointers use a `pointer` field because `y-codemirror.next` owns `cursor` for text selections. Pointer positions are stored as fractions of the shared surface rather than pixels, so a cursor lands in the same place on a differently sized window, and are coalesced to one update per animation frame. Awareness state is never persisted: the server relays it and forgets it.

A closing tab announces its own departure on `pagehide`, because y-websocket only does that automatically under Node; without it a departed peer would linger until awareness times it out after 30 seconds.

## Documents, saving, and history

A document belongs to a project and has an immutable history of saved versions. Editing is live for everyone in the document, but nothing enters that history until someone saves.

```
GET    /api/docs                     list documents
POST   /api/docs                     create one
GET    /api/docs/{id}                metadata, including the hash of the last saved artifact
POST   /api/docs/{id}/save           commit a version
GET    /api/docs/{id}/versions       history, newest first
POST   /api/docs/{id}/restore/{n}    hand back version n's artifact
```

**Saves are client-authored**, because the Go server has no Yjs and cannot merge updates or encode a snapshot. A save carries two things: the `artifact` (the Markdown text, which is what history stores and people read) and a `snapshot` (`Y.encodeStateAsUpdate`, the CRDT state it came from). Both are read in the same tick — the preview's debounced snapshot would store history that disagrees with the document. The version number is assigned by the server as `current_version + 1` in the same transaction that writes the row, so two clients saving at once cannot collide.

**Saving deletes nothing from the journal.** Trimming it is only a size optimization — replay is idempotent, so stale rows cost bytes — and it cannot be done correctly yet: it needs to know which rows the saving client had seen, and nothing in the y-websocket protocol carries row ids. Adding a data-loss risk to save space we have already decided to spend is the wrong trade, so the snapshot is written for restore, and replay stays journal-only.

**Whether a document has unsaved changes is a client-side hash comparison** of the current text against `doc_state.artifact_sha256`. The tempting server-side test — "the journal has rows" — is wrong three ways: rows outlive a save, a word typed and deleted leaves rows with identical text, and merely opening a document appends a full sync frame, so every document would read dirty with no edits at all.

**Restoring hands the artifact back to the client**, which writes it into the live document and saves the result as a new version. The server cannot rebuild CRDT state from text, and history is never rewritten.

People are referenced by `SessionUsers.Id` and never by username, because usernames are mutable — the session package renames them in place. The username is joined in for display.

## Persistence

Documents, their saved versions, and their update journals are stored in Postgres. `DATABASE_URL` is required: accounts and room history both live there.

```sh
mise run db       # start a local Postgres container on 127.0.0.1:5432
mise run db:stop  # stop it
export DATABASE_URL='postgres://canvas:canvas@127.0.0.1:5432/canvas?sslmode=disable'
mise run migrate  # apply migrations (the server also does this at startup)
```

`mise run test` skips the Postgres store test unless `DATABASE_URL` is set; every other test uses the in-memory store, which is kept for exactly that reason.

## Migrations

Schema lives in `migrations/`, embedded in the binary and applied at startup with golang-migrate. There are two independent sets, each with its own migrations table, so they can be numbered independently:

- `migrations/app` (`schema_migrations`) — this application's tables.
- `migrations/session` (`session_schema_migrations`) — copied verbatim from `github.com/cccteam/session`, because `go:embed` cannot reach into the module cache. Re-copy them when upgrading that module; the header comment in each file records where they came from.

`SessionUsers` uses `casefold()`, which requires **PostgreSQL 18 or newer**. The development container is already `postgres:18-alpine`; check any other deployment target before the first migration runs.

## Accounts and sessions

Authentication is username and password through `github.com/cccteam/session`, backed by the same Postgres pool as everything else. The HTTP route that creates users is itself behind authentication, so the first account is made from the command line:

```sh
mise run createuser alice hunter2hunter2
```

That leaves the password in shell history and in `ps` output, which is acceptable for bootstrapping a development database and not for anything else.

Endpoints live under `/api/session`: `GET` reports who you are, `POST` signs in, `DELETE` signs out. Set `COOKIE_KEY` to base64 of at least 32 random bytes (`head -c 32 /dev/urandom | base64`); leave it unset and the session package generates one at startup and prints it, which invalidates every session on restart.

Three things about this integration are easy to trip over:

- **The process is pinned to UTC** in `main.go`. Session rows are `timestamp without time zone`: the store writes local wall-clock time and reads it back as UTC. Run it anywhere but UTC without that line and every session looks hours old, so logins succeed and then immediately report as unauthenticated.
- **Development builds need `-tags insecurecookie`.** Without it session cookies are marked `Secure` and never survive a plain-http localhost login. `mise run serve` and `mise run dev:server` set it and write `bin/canvas-dev`; `mise run build:server` deliberately does not, so the deployable `bin/canvas` keeps secure cookies. The development build also works behind the HTTPS tunnel, which is the combination most likely to be running: its cookies are `SameSite=Strict` and simply omit `Secure`, which a browser accepts over HTTPS.
- **The dependency is not free.** Adding the session package takes the server binary from 19 MB to 58 MB, because its storage layer links the Spanner client and gRPC even though this app only ever uses the Postgres path.

Authenticated routes are grouped as `StartSession` → `SetXSRFToken`, then `ValidateSession` and `ValidateXSRFToken` for anything that writes. Sign-in cannot sit behind session validation, since there is no session yet. The XSRF token round-trips as a cookie the client copies into the `X-XSRF-TOKEN` header; the app calls `GET /api/session` at startup so the cookie exists before the first write, avoiding a redirect that would re-send the request body.

## Authenticating the collaboration socket

The WebSocket route is authenticated but carries no XSRF check, because a browser cannot set headers on a WebSocket handshake. What protects it is the session cookie being `SameSite=Strict`, so a cross-site handshake arrives with no cookie at all; the `Origin` check sits behind that.

The session is checked *after* the upgrade, and a socket without one is closed with code **4401**. y-websocket treats 4400-4499 as permanent and stops reconnecting; a rejected handshake would instead look like a network failure and be retried forever. The client listens for that code and returns to the login screen.

Sessions expire after ten minutes without an HTTP request, and only HTTP requests refresh them — someone typing over a WebSocket makes none. The client therefore polls `GET /api/session` every two minutes, and whenever a tab becomes visible again.

One known gap: the session is validated when the socket opens, not continuously. Signing out elsewhere does not close sockets that are already connected; they keep working until they reconnect.

## Sharing a local server with ngrok

```sh
mise run tunnel   # ngrok http 8080; set PORT to expose a different port
```

The client derives its WebSocket scheme from the page, so the relay works over a tunnel's HTTPS origin without configuration. The server checks the `Origin` header against the request host and additionally allows `*.ngrok-free.dev`, `*.ngrok-free.app`, `*.ngrok.app`, and `*.ngrok.io`. Set `ORIGINS` (comma separated) to allow a different set. Keep `ADDR` on loopback; ngrok connects from the same machine.

Those ngrok defaults are a development convenience and must not ship to a deployment: they let a page on any ngrok subdomain open a handshake. Set `ORIGINS` explicitly there.

## Go server and deep links

The Go module is `github.com/bdswaney/canvas`. `main.go` embeds the Vite build into the server binary and serves the application and its static assets.

```sh
mise run serve         # watch, rebuild, and serve at http://127.0.0.1:8080
mise run test          # build frontend and run Go routing tests
mise run build:server  # produce bin/canvas with frontend embedded
```

The Go module depends on chi for routing, `coder/websocket` for the relay, and pgx for Postgres.

Set `ADDR` to override the listening address, for example `ADDR=127.0.0.1:9090 mise run serve`. Bind to `0.0.0.0:8080` only when you intend to expose the server beyond localhost.

Opening or refreshing an extensionless route such as `/artifacts/123` serves the React entry point. Missing files, `/assets/*`, and reserved `/api` paths return 404 instead of falling back to HTML. Only GET and HEAD are supported. React currently displays the same shell for every client route; route-specific screens are not implemented yet.

Because `dist/` is embedded at compile time and is not committed, run `mise run deps` and `mise run build` before invoking Go compilation directly on a fresh checkout. The server tasks handle the frontend build automatically. The resulting binary needs neither Node.js nor an external `dist/` directory at runtime. Rebuild it after frontend changes.

`mise run serve` runs Air using `.air.toml`. It builds once at startup, then rebuilds the frontend and restarts Go when Go or frontend source files change. Generated files in `dist/`, `.tmp/`, and `bin/` and dependencies in `node_modules/` are excluded to avoid rebuild loops. Build failures stop the old server instead of silently serving stale code. Press Ctrl+C to stop Air and its server.

Refresh the browser after an Air rebuild. For frontend hot module replacement without manual refresh, use `mise run dev` instead; Air's Go server serves the embedded build, not the live Vite source. Air is development-only; deploy `bin/canvas` from `mise run build:server`.
