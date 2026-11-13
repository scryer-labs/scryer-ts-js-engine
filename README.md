# scryer-ts-js-engine

A static TypeScript and JavaScript language engine for Scryer.

```sh
./scripts/build
scryer engine install ts-js --from "$PWD/build/install/scryer-ts-js-engine"
scryer project scan /path/to/project --facts dependencies,architecture,files,ipc,call-graph --json
```

The macOS package uses Go, cgo, and the Scryer C ABI. Scanning does not run
Node, package managers, or a frontend build.

- `dependencies` reads dependency declarations in `package.json`.
- `architecture` lists package components.
- `files` lists JavaScript, JSX, TypeScript, TSX and Svelte files, package
  ownership, static imports and re-exports. Relative import resolution is
  based on source paths and unique candidates. Explicit relative imports can
  point to existing assets such as CSS and JSON.
- `call-graph` records functions, closures and call sites, and resolves direct
  calls through supported local bindings and named/default relative imports.
- `ipc` records literal Tauri command and event endpoints with source positions
  and an app config path. It checks the imported Tauri API binding.

`internal/script` parses generic scripts. `internal/framework/svelte`
extracts Svelte script blocks and preserves original file positions.
`internal/framework/tauri` collects frontend IPC observations.
`internal/framework/react` adds JSX component declarations and render sites
under `architecture.react` for packages declaring React. It can resolve
same-file declarations and direct relative imports. Tags with external
dependency evidence or a runtime binding retain that classification and
their source locations. Svelte markup and CSS, React wrappers and re-export
resolution, and TypeScript alias resolution are outside this slice.

## License

[Apache License 2.0](LICENSE)
