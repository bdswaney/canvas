# Canvas

A collaborative artifact workspace built with Go and TypeScript. The initial focus is shared state and synchronization, before editing UI or rendering.

## Toolchain

Mise is the entry point for project tooling and commands. Go and Node.js versions are pinned in `mise.toml`; npm comes with Node.js. Install mise before continuing.

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
