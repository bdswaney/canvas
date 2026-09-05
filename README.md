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

The frontend uses React, TypeScript, Vite, and Mantine's off-the-shelf components. It currently contains only an application shell; no editor, artifact preview, or collaboration connection is implemented.

```sh
mise run deps       # install dependencies from package-lock.json
mise run dev        # start Vite; open the URL printed in the terminal
mise run typecheck  # check TypeScript
mise run build      # type-check and build into dist/
mise run preview    # serve the existing build locally (not for production)
```

`src/main.tsx` loads Mantine's styles and provider. `src/App.tsx` contains the initial UI. Yjs is installed but is not wired into React.

## Go server and deep links

The Go module is `github.com/bdswaney/canvas`. `main.go` embeds the Vite build into the server binary and serves the application and its static assets.

```sh
mise run serve         # watch, rebuild, and serve at http://127.0.0.1:8080
mise run test          # build frontend and run Go routing tests
mise run build:server  # produce bin/canvas with frontend embedded
```

Set `ADDR` to override the listening address, for example `ADDR=127.0.0.1:9090 mise run serve`. Bind to `0.0.0.0:8080` only when you intend to expose the server beyond localhost.

Opening or refreshing an extensionless route such as `/artifacts/123` serves the React entry point. Missing files, `/assets/*`, and reserved `/api` paths return 404 instead of falling back to HTML. Only GET and HEAD are supported. React currently displays the same shell for every client route; route-specific screens are not implemented yet.

Because `dist/` is embedded at compile time and is not committed, run `mise run deps` and `mise run build` before invoking Go compilation directly on a fresh checkout. The server tasks handle the frontend build automatically. The resulting binary needs neither Node.js nor an external `dist/` directory at runtime. Rebuild it after frontend changes.

`mise run serve` runs Air using `.air.toml`. It builds once at startup, then rebuilds the frontend and restarts Go when Go or frontend source files change. Generated files in `dist/`, `.tmp/`, and `bin/` and dependencies in `node_modules/` are excluded to avoid rebuild loops. Build failures stop the old server instead of silently serving stale code. Press Ctrl+C to stop Air and its server.

Refresh the browser after an Air rebuild. For frontend hot module replacement without manual refresh, use `mise run dev` instead; Air's Go server serves the embedded build, not the live Vite source. Air is development-only; deploy `bin/canvas` from `mise run build:server`.
