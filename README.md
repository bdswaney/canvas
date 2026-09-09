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

## Navigation

The app shell is a two-column navbar: a rail of the two things Canvas has — projects and documents — and the contents of whichever is selected. A document is always a project's, so opening one by link has to ask the server which project it belongs to before the navbar can show its neighbours; the URL does not carry it.

Selecting in the rail and following a link both move it: the route leads and the rail follows, rather than the two holding separate ideas of where you are. Below `sm` the navbar collapses behind a burger and closes itself when a link is followed.

The navbar is navigation; a project's own page is where things are created, archived, and its members managed.

The frontend is grouped the same way the server is:

| Folder | Holds |
|---|---|
| `src/app` | the shell, the navbar, and routing |
| `src/api` | the fetch wrapper, the session, and one module per resource |
| `src/collab` | the Yjs layer: the provider, awareness, and remote pointers |
| `src/editor` | CodeMirror, the Markdown preview, and their styling |
| `src/components` | pieces used on more than one screen |
| `src/pages` | one file per route |

`src/api/client.ts` is the fetch wrapper and nothing else; `src/api/session.ts` is the session calls together with the hook over them, which were previously split across two files for no reason.

Icons come from `@tabler/icons-react`, imported by name so the bundle carries only the ones used — verified by checking that a used glyph is present in the built asset and an unused one is not.

The Go binary embeds `dist/`, and `go:embed` has twice served a stale copy in this repo after a rebuild. After `mise run build:server`, confirm the binary really has the current frontend:

```sh
strings bin/canvas | grep -o 'index-[A-Za-z0-9_-]*\.js' | sort -u   # must match dist/assets/
```

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

**The server can now read Yjs documents** — see the section below — so the compaction this paragraph describes as impossible is merely not built yet.

The journal is unbounded: documents grow with every keystroke and are never compacted. Saving does **not** trim it — see the note under Saving below. Measured on a document with 399 updates, a joining client is sent 400 frames totalling 41 kB, replayed from memory in about 4 ms on loopback — cheap now, but it grows without limit and every joining client pays it. Squashing the log into a snapshot needs a Yjs implementation on the server and is deliberately left for later.

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

## Projects and documents

A **project** contains documents and the people who can reach them. That is the whole hierarchy.

```
GET    /api/projects                       list projects
POST   /api/projects                       create one
DELETE /api/projects/{id}                  archive one
```

Because a document changes over REST with no socket to announce it, the project screen and the navbar refetch when the tab is looked at again, the same way a document picks up saves made elsewhere.

## Archiving

Nothing is deleted. `DELETE /api/projects/{id}` and `DELETE /api/docs/{id}` set a `deleted_at` timestamp, which hides the row from every read path.

This is not squeamishness. `projects` cascades to `docs`, which cascades to `doc_versions`, so a real delete of a project would destroy the saved history under it — and history is the one layer of Canvas that cannot be rebuilt. Everything else, the journal and the CRDT state, is derivable from it.

Archiving a project hides its documents with it, including ones that were never archived themselves. `ProjectMember` is where that is enforced: it returns false for an archived project, so the one gate every handler already shares also covers this rather than each caller remembering.

Two places where an archived row would otherwise stay usable, both invisible to a test that only checks the endpoint:

- `Doc` excludes archived documents, because that is the existence check the **collaboration socket** makes before accepting a connection. An archived document that still resolved there would be one people kept editing live.
- `SaveDoc` guards its version bump on `deleted_at IS NULL`, or a save into an archived document would quietly write history.

**There is no un-archive yet.** Nothing is lost and restoring one is an `UPDATE` away, but it needs a decision first about what restoring a document inside a still-archived project should mean.

## Documents, saving, and history

A document belongs to a project and has an immutable history of saved versions. Editing is live for everyone in the document, but nothing enters that history until someone saves.

```
GET    /api/docs[?projectId=]        list documents, optionally within one project
POST   /api/docs                     create one
GET    /api/docs/{id}                metadata, including the hash of the last saved artifact
POST   /api/docs/{id}/save           commit a version
GET    /api/docs/{id}/versions       history, newest first
POST   /api/docs/{id}/restore/{n}    hand back version n's artifact
```

**Saves are client-authored**, because the Go server has no Yjs and cannot merge updates or encode a snapshot. A save carries two things: the `artifact` (the Markdown text, which is what history stores and people read) and a `snapshot` (`Y.encodeStateAsUpdate`, the CRDT state it came from). Both are read in the same tick — the preview's debounced snapshot would store history that disagrees with the document. The version number is assigned by the server as `current_version + 1` in the same transaction that writes the row, so two clients saving at once cannot collide.

**Saving deletes nothing from the journal.** Trimming it is only a size optimization — replay is idempotent, so stale rows cost bytes — and it cannot be done correctly yet: it needs to know which rows the saving client had seen, and nothing in the y-websocket protocol carries row ids. Adding a data-loss risk to save space we have already decided to spend is the wrong trade, so the snapshot is written for restore, and replay stays journal-only.

**Whether a document has unsaved changes is a client-side hash comparison** of the current text against `doc_state.artifact_sha256`. The tempting server-side test — "the journal has rows" — is wrong three ways: rows outlive a save, a word typed and deleted leaves rows with identical text, and merely opening a document appends a full sync frame, so every document would read dirty with no edits at all.

**Restoring hands the artifact back to the client**, which writes it into the live document and saves the result as a new version. The server cannot rebuild CRDT state from text, and history is never rewritten. Note that this is an edit rather than a rollback: the restore is a replace applied to the shared text, so a peer typing during it has their keystrokes merged into the restored text instead of discarded. Everyone converges on the same result, but that result is the restored version only if nobody else was mid-keystroke.

People are referenced by `SessionUsers.Id` and never by username, because usernames are mutable — the session package renames them in place. The username is joined in for display.

## Persistence

Documents, their saved versions, and their update journals are stored in Postgres. `DATABASE_URL` is required: accounts and document history both live there.

```sh
mise run db       # start a local Postgres container on 127.0.0.1:5432
mise run db:stop  # stop it
export DATABASE_URL='postgres://canvas:canvas@127.0.0.1:5432/canvas?sslmode=disable'
mise run migrate  # apply migrations (the server also does this at startup)
```

`mise run test` skips the Postgres half of the store suite unless `DATABASE_URL` is set; every other test uses the in-memory store, which is kept for exactly that reason. `TestStores` runs one suite against both implementations and asserts *which* id and *which* name come back rather than how many rows, because the two have drifted before — `Version.Author` once held an id in memory and a username in Postgres, and a test that only counted rows saw nothing.

## Migrations

Schema lives in `internal/migrate/migrations/`, embedded in the binary and applied at startup with golang-migrate. There are two independent sets, each with its own migrations table, so they can be numbered independently:

- `migrations/app` under it (`schema_migrations`) — this application's tables.
- `migrations/session` under it (`session_schema_migrations`) — copied verbatim from `github.com/cccteam/session`, because `go:embed` cannot reach into the module cache. Re-copy them when upgrading that module; the header comment in each file records where they came from.

The app set depends on the session set: `doc_versions.author_id` and `project_members.user_id` both reference `"SessionUsers"("Id")`. Roll the app set back before the session set, or the drop fails on a foreign key and reports it against the wrong migration.

`SessionUsers` uses `casefold()`, which requires **PostgreSQL 18 or newer**. The server checks `server_version_num` before running any migration and refuses to start on anything older, because otherwise the failure arrives partway through the session migrations as a syntax error pointing at the wrong thing. The development container is already `postgres:18-alpine`.

## Membership

**Membership in a project is the authorization boundary.** A document is reachable only by members of the project it belongs to. `docs.project_id` is `NOT NULL`, so every document has exactly one project and the gate is a single join.

```
GET    /api/users                              every account, for the member picker
GET    /api/projects/{id}/members              who is in a project
POST   /api/projects/{id}/members              add somebody
DELETE /api/projects/{id}/members/{userID}     remove them
```

The lists that would otherwise leak the existence of other people's work — projects and documents — are filtered in SQL by the person asking. Every single-row read and write is gated in the handler instead, so there is one copy of the rule rather than one per store. **Refusals are answered as `404`, never `403`**, so that ids cannot be probed for existence.

Four things that are decisions rather than consequences of the design:

- **The migration seeds every account that existed into every project that existed.** That preserves exactly what was true the moment before it ran — everyone could see everything — and applies only to rows that already existed. Accounts and projects created afterwards get memberships explicitly.
- **Creating a project joins it.** This one is forced: otherwise you could not see what you had just made.
- **There are no roles.** Any member can add or remove any other, including the person who created the project. That is the same trust assumption the save endpoint already makes, and it means membership is a decision the whole group shares.
- **A project cannot be emptied.** The last member cannot leave, because a project with nobody in it is unreachable by anyone who could put it right.

`GET /api/users` shows every username to every signed-in person. That is a real disclosure, and it is deliberate: there is no way to grant access to somebody you cannot name, and the alternative is inviting by exact spelling with no feedback. Revisit it if Canvas ever holds more than one organisation.

Known gap: removing somebody does not close the sockets they already have open. They lose access at their next reconnect, like a signed-out session.

## Reading and editing documents on the server

For most of its life the server could not interpret a document. It relayed
update bytes it could not read, which is why saves are client-authored, why
restore hands text back to a client, and why the journal has never been
compacted.

`internal/ydoc` removes that limitation. It runs [yrs](https://github.com/y-crdt/y-crdt),
the Yjs organisation's Rust port, compiled to WebAssembly and executed by
[wazero](https://github.com/tetratelabs/wazero) — a pure-Go runtime, so this
needs no cgo and `CGO_ENABLED=0` still produces a single static binary. The
compiled module is committed as `internal/ydoc/ydoc.wasm` (about 240 kB), so
building or testing the server needs no Rust toolchain; `mise run build:ydoc`
regenerates it after a change under `internal/ydoc/shim/`.

The interface is deliberately stateless — no document handles cross into
WebAssembly, so there is no lifecycle to manage and nothing leaks when a call
fails:

- `Merge` collapses a sequence of updates into one, which is what compaction
  will store in place of the rows it replaces.
- `Text` reads a named `Y.Text`.
- `SetText` edits a document until it reads as the given text, and returns
  only the update that change produced.

`SetText` is an **edit, not a replacement**. The shared prefix and suffix are
left alone and only the span between them is rewritten, so somebody typing in
another paragraph keeps their work. Contrast `restore`, which really is a
replace and is documented as such because it cannot merge.

### yrs is not yjs

It is a second implementation of the same format, and a disagreement between
the two does not surface as a failed request — it silently corrupts a document
that the browsers and the server no longer read the same way. So the
compatibility claim is demonstrated rather than assumed:
`internal/ydoc/conformance_test.go` drives the **real `yjs` from
`node_modules`** through `testdata/yjs.mjs` and compares both directions —
what the server writes read by yjs, what yjs writes read by the server,
concurrent edits from both sides converging, and merges matching.

The sharpest edge is offsets. yrs counts bytes by default; yjs in the browser
counts UTF-16 code units. Left at the default, every index in a document
containing anything but ASCII disagrees with the clients. Removing that one
setting and running the suite turns `"👍👍 middle end"` into
`"👍👍 mi endddle"` — which is exactly the kind of silent corruption the suite
exists to catch, and why the tests edit documents that already contain
accented characters and emoji rather than only empty ones.

## Accounts and sessions

Authentication is username and password through `github.com/cccteam/session`, backed by the same Postgres pool as everything else. The HTTP route that creates users is itself behind authentication, so the first account is made from the command line:

```sh
mise run createuser alice hunter2hunter2
```

That leaves the password in shell history and in `ps` output, which is acceptable for bootstrapping a development database and not for anything else.

Removing one is `mise run deleteuser alice`, which deletes the account and expires its sessions. It will refuse while anything still references the account: saved versions hold the author's `SessionUsers.Id`, and the foreign key is what keeps history honest, so reassign or delete that work first.

Endpoints live under `/api/session`: `GET` reports who you are, `POST` signs in, `DELETE` signs out. Set `COOKIE_KEY` to base64 of at least 32 random bytes (`head -c 32 /dev/urandom | base64`); leave it unset and the session package generates one at startup and prints it, which invalidates every session on restart.

Three things about this integration are easy to trip over:

- **The process is pinned to UTC** in `main.go`. Session rows are `timestamp without time zone`: the store writes local wall-clock time and reads it back as UTC. Run it anywhere but UTC without that line and every session looks hours old, so logins succeed and then immediately report as unauthenticated.
- **Development builds need `-tags insecurecookie`.** Without it session cookies are marked `Secure` and never survive a plain-http localhost login. `mise run serve` and `mise run dev:server` set it and write `bin/canvas-dev`; `mise run build:server` deliberately does not, so the deployable `bin/canvas` keeps secure cookies. The development build also works behind the HTTPS tunnel, which is the combination most likely to be running: its cookies are `SameSite=Strict` and simply omit `Secure`, which a browser accepts over HTTPS.
- **The dependency is not free.** Adding the session package takes the server binary from 19 MB to 58 MB, because its storage layer links the Spanner client and gRPC even though this app only ever uses the Postgres path.

Authenticated routes are grouped as `StartSession` → `SetXSRFToken`, then `ValidateSession` and `ValidateXSRFToken` for anything that writes. Sign-in cannot sit behind session validation, since there is no session yet. The XSRF token round-trips as a cookie the client copies into the `X-XSRF-TOKEN` header; the app calls `GET /api/session` at startup so the cookie exists before the first write, avoiding a redirect that would re-send the request body.

## Authenticating the collaboration socket

The WebSocket route is authenticated but carries no XSRF check, because a browser cannot set headers on a WebSocket handshake. What protects it is the session cookie being `SameSite=Strict`, so a cross-site handshake arrives with no cookie at all; the `Origin` check sits behind that.

The session is checked *after* the upgrade, and a socket without one is closed with code **4401**. y-websocket treats 4400-4499 as permanent and stops reconnecting; a rejected handshake would instead look like a network failure and be retried forever. The client listens for that code and returns to the login screen.

Three close codes are in that permanent range:

| Code | Meaning | What the client does |
|---|---|---|
| 4401 | The session has lapsed | Re-checks the session and shows the login screen |
| 4404 | No such document | Says so, and stops |
| 4405 | Not a member of the document's project | Says so, and stops |

A permanent close is explained on screen rather than left as "disconnected", which is otherwise indistinguishable from a network problem the client will never retry out of.

Authorization is the project membership check, which the socket makes itself, because a live socket bypasses every REST handler.

Sessions expire after ten minutes without an HTTP request, and only HTTP requests refresh them — someone typing over a WebSocket makes none. The client therefore polls `GET /api/session` every two minutes, and whenever a tab becomes visible again.

Two known gaps: the session is validated when the socket opens, not continuously, so signing out elsewhere does not close sockets that are already connected — they keep working until they reconnect. The same is true of membership: removing somebody leaves their open sockets alive until they next reconnect.

## Sharing a local server with ngrok

```sh
mise run tunnel   # ngrok http 8080; set PORT to expose a different port
```

The client derives its WebSocket scheme from the page, so the relay works over a tunnel's HTTPS origin without configuration. The server checks the `Origin` header against the request host and additionally allows `*.ngrok-free.dev`, `*.ngrok-free.app`, `*.ngrok.app`, and `*.ngrok.io`. Set `ORIGINS` (comma separated) to allow a different set. Keep `ADDR` on loopback; ngrok connects from the same machine.

Those ngrok defaults are a development convenience: they let a page on any ngrok subdomain open a handshake. **`ORIGINS` is required unless `CANVAS_ENV` is unset or `development`** — the server refuses to start otherwise rather than falling back to the wildcards. In development it uses them and says so in the log.

## Go server and deep links

The Go module is `github.com/bdswaney/canvas`. `main.go` embeds the Vite build into the server binary and serves the application and its static assets.

The code is split along the seams that already existed, so each package can be read without the rest:

| Package | Holds | Depends on |
|---|---|---|
| `main` | startup, `createuser`/`deleteuser`, `ORIGINS`, the `dist` embed | everything |
| `internal/server` | the route table, the SPA fallback | api, relay, auth |
| `internal/api` | REST handlers for documents, projects, membership | store, auth |
| `internal/relay` | the Yjs socket: hub, doc sessions, close codes | store, auth, lib0 |
| `internal/auth` | session wiring and the `Authenticator` interface | — |
| `internal/store` | the model, `Store`, and both implementations | — |
| `internal/migrate` | the migration runner and the SQL files | — |
| `internal/lib0` | the varint codec the Yjs protocols use | — |
| `internal/ydoc` | reads and edits Yjs documents, via yrs on WebAssembly | — |

`internal/auth/authtest` holds the stub that stands in for the session package, so tests in every other package can exercise routing without a database. It is the reason `Authenticator` is defined once in `auth` rather than at each consumer.

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
