# scryer-ts-js-engine

A TypeScript and JavaScript fact and analysis engine for Scryer.

## Install and scan

With a current Scryer CLI, install or upgrade the official macOS arm64 or
Windows amd64 package:

```sh
scryer engine install ts-js
scryer engine upgrade ts-js
scryer project scan /path/to/project --facts dependencies,architecture,files,ipc,call-graph --json --compact
```

For engine development, build with the Go version in `go.mod`, cgo and a C
compiler. Install the resulting local package into Scryer:

```sh
./scripts/build
scryer engine install ts-js --from "$PWD/build/install/scryer-ts-js-engine"
```

## Static facts

Static scanning reads source and manifests without Node or a frontend build.

- `dependencies` reads dependency declarations in `package.json`.
- `architecture` lists package components.
- `files` lists JavaScript, JSX, TypeScript, TSX and Svelte files, package
  ownership, static imports and re-exports. Relative import resolution is
  based on source paths and unique candidates. Local JSONC `tsconfig.json`
  path mappings and relative `extends` are supported. SvelteKit's default
  `$lib` layout is reported as a convention candidate when no alias resolves. Explicit relative imports can
  point to existing assets such as CSS and JSON.
- `call-graph` records functions, closures and call sites, and resolves direct
  calls through stable local bindings and named/default imports, including
  explicit re-exports and single wildcard forwarding. Reassigned bindings
  are left unresolved. Imported test registrations from Vitest, Jest,
  Playwright, and `node:test` carry call-site evidence.
- `ipc` records literal Tauri command and event endpoints with source positions
  and an app config path. It checks the imported Tauri API binding.

`internal/script` parses generic scripts. `internal/framework/svelte`
extracts Svelte script blocks and preserves original file positions.
`internal/framework/tauri` collects frontend IPC observations.
`internal/framework/react` adds JSX component declarations and render sites
under `architecture.react` for packages declaring React. It can resolve
same-file declarations and direct relative imports. Tags with external
dependency evidence or a runtime binding retain that classification and
their source locations. It also follows supported re-exports and same-block
`const` JSX initializers returned by the component.

`architecture.svelte` records component files and statically visible template
tags. A unique default import provides an `import-candidate` link to a Svelte
file. Template scope and dynamic component selection can change the binding.

Call sites with insufficient evidence carry `resolutionReason`.
`limitations` covers dynamic calls, React wrappers and classes, Svelte
template expressions, dynamic IPC channels, test-runner globals and coverage.
Static import resolution has limited package-exports and project-reference
support. The optional compiler pass below can use configured project references.

## License

[Apache License 2.0](LICENSE)

## Toolchain-assisted resolution

`--resolve` with `files` or `call-graph` loads the TypeScript Compiler API
using project configuration and project references. It records semantic
targets separately from the static target. With Svelte and its compiler
adapter available, template calls and supported Tauri observations map back
to the original file. `--facts ipc --resolve` enables that enrichment alone.

The resolver needs Node and Scryer's toolchain dependencies, installed in
`~/.config/scryer/ts-toolchain` (under `SCRYER_HOME` when overridden). Use
`SCRYER_TS_TOOLCHAIN` for an explicit toolchain directory. For example:

```sh
npm install --prefix "${SCRYER_HOME:-$HOME/.config/scryer}/ts-toolchain" --ignore-scripts --save-exact \
  typescript@6.0.3 svelte@5.57.3 svelte2tsx@0.7.62 @jridgewell/trace-mapping@0.3.31
scryer project scan --facts files,call-graph,ipc --resolve --json --compact
```

The scan reads the project's installed declarations when available. Missing
tools, dependencies and compiler diagnostics can yield fallback or partial
results alongside the original static facts.

## Coverage and change impact

With a current Core CLI and this engine:

```sh
scryer project analyze --operations coverage -o coverage.html
scryer project analyze --resolve --operations impact,coverage -o impact.html
scryer project analyze --json --compact
```

Impact defaults to static scan evidence; `--resolve` also runs native tests and
collects after-snapshot coverage. Failed dynamic resolution/tests/coverage stop
analysis without static fallback. Core owns snapshot comparison, traversal and
report rendering; the engine supplies shared scan projections and normalized
JSON coverage. Multi-engine projects use the same command; CMake target selectors
belong to C++ and are not interpreted here.

Requires Node/npm and installed project test dependencies. Coverage supports
Vitest with matching `@vitest/coverage-v8`, Jest remapped Istanbul statements,
and untransformed native `node --test` V8 offsets. Import accepts Istanbul or
V8 JSON with these mapping limits. No unknown runner or missing mapping is
silently accepted. Dynamic impact reuses the TypeScript/Svelte scan resolver
and requires successful diagnostics. Snapshot dependency reuse requires identical
manifests/lockfiles; otherwise npm lockfiles use offline `npm ci --ignore-scripts`.
Imported test registrations can mark callbacks; globals/names alone cannot.

Native runtime acceptance currently targets macOS. Windows package builds and
ABI smoke checks do not establish equivalent runtime toolchain acceptance.
Coverage is aggregate execution evidence, not assertion effectiveness or a
causal test-to-caller path. The common JSON contract is maintained in Scryer
Core's `docs/contracts/COVERAGE.md` and `docs/contracts/IMPACT.md`.

## Release verification

The [GitHub workflow](.github/workflows/ci.yml) builds the platform packages
and exercises scan, static impact and coverage import through the ABI before publishing a version tag.
