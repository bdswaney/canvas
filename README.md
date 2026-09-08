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

The frontend uses React, TypeScript, Vite, and Mantine's off-the-shelf components. It contains an application shell, a connection status header, and a shared-notes textarea backed by a Yjs document; no editor or artifact preview is implemented.

```sh
mise run deps       # install dependencies from package-lock.json
mise run dev        # start Vite; open the URL printed in the terminal
mise run typecheck  # check TypeScript
mise run build      # type-check and build into dist/
mise run preview    # serve the existing build locally (not for production)
```

`src/main.tsx` loads Mantine's styles and provider. `src/App.tsx` contains the initial UI. `src/sync.ts` holds `useSync`, which owns one `Y.Doc` and its relay connection, and `useSharedText`, which mirrors a `Y.Text` into React state.

`mise run dev` proxies `/api` (WebSockets included) to `http://127.0.0.1:8080`, so run `mise run serve` alongside it when working on the frontend.

## Collaboration relay

`/api/sync/{room}` is a WebSocket endpoint speaking the y-websocket protocol. The room name comes from the first path segment of the page URL, so `/notes` and `/sketches` are separate documents and `/` is the `default` room.

The server does not interpret document contents. It keeps an append-only log of Yjs updates per room, replays that log to each joining client, and broadcasts every new update to the room's other clients. Yjs updates are idempotent and commutative, so replaying the log reconstructs the document. Awareness (presence) frames are relayed but never stored.

```
client                                  server
  |  <-- sync step 1 (empty vector) ------|   asks for state the client already has
  |  --- sync step 1 (state vector) ----->|
  |  <-- sync step 2 (each logged update) |   replayed history
  |  <-- sync step 2 (empty update) ------|   marks the client synced
  |  <-> update / awareness ------------->|   relayed to the other clients
```

The log is unbounded: rooms grow with every keystroke and are never compacted. Squashing the log into a snapshot needs a Yjs implementation on the server and is deliberately left for later.

## Persistence

Update logs are stored in Postgres in a single `room_updates` table, created on startup if missing. Set `DATABASE_URL` to enable it; without it the server keeps history in memory only and rooms are lost on restart.

```sh
mise run db       # start a local Postgres container on 127.0.0.1:5432
mise run db:stop  # stop it
export DATABASE_URL='postgres://canvas:canvas@127.0.0.1:5432/canvas?sslmode=disable'
```

`mise run test` skips the Postgres store test unless `DATABASE_URL` is set; every other test uses the in-memory store.

## Sharing a local server with ngrok

```sh
mise run tunnel   # ngrok http 8080; set PORT to expose a different port
```

The client derives its WebSocket scheme from the page, so the relay works over a tunnel's HTTPS origin without configuration. The server checks the `Origin` header against the request host and additionally allows `*.ngrok-free.dev`, `*.ngrok-free.app`, `*.ngrok.app`, and `*.ngrok.io`. Set `ORIGINS` (comma separated) to allow a different set. Keep `ADDR` on loopback; ngrok connects from the same machine.

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
