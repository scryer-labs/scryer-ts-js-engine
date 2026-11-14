package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/scryer-labs/scryer-ts-js-engine/internal/framework/react"
	"github.com/scryer-labs/scryer-ts-js-engine/internal/framework/svelte"
	"github.com/scryer-labs/scryer-ts-js-engine/internal/framework/tauri"
	"github.com/scryer-labs/scryer-ts-js-engine/internal/script"
	"github.com/scryer-labs/scryer-ts-js-engine/internal/semantic"
	"github.com/scryer-labs/scryer-ts-js-engine/internal/source"
)

type manifest struct {
	Name                 string            `json:"name,omitempty"`
	Dependencies         map[string]string `json:"dependencies,omitempty"`
	DevDependencies      map[string]string `json:"devDependencies,omitempty"`
	PeerDependencies     map[string]string `json:"peerDependencies,omitempty"`
	OptionalDependencies map[string]string `json:"optionalDependencies,omitempty"`
	File                 string            `json:"file"`
}

type fileFact struct {
	Path         string   `json:"path"`
	Language     string   `json:"language"`
	ComponentIDs []string `json:"componentIds"`
	SyntaxErrors bool     `json:"syntaxErrors,omitempty"`
}

var languages = map[string]string{
	".js": "JavaScript", ".mjs": "JavaScript", ".cjs": "JavaScript", ".jsx": "JSX",
	".ts": "TypeScript", ".mts": "TypeScript", ".cts": "TypeScript", ".tsx": "TSX", ".svelte": "Svelte",
}

func scanProject(ctx context.Context, root string, selected []string, resolveMode bool) (map[string]any, error) {
	fsys := os.DirFS(root)
	packages := map[string]string{}
	packageManifests := map[string]manifest{}
	manifests := []manifest{}
	files := []fileFact{}
	paths := map[string]bool{}
	err := fs.WalkDir(fsys, ".", func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".orrery", ".scryer", ".svelte-kit", "node_modules", "target", "dist", "build", "coverage":
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		paths[file] = true
		if entry.Name() == "package.json" {
			raw, err := fs.ReadFile(fsys, file)
			if err != nil {
				return err
			}
			var pkg manifest
			if err := json.Unmarshal(raw, &pkg); err != nil {
				return fmt.Errorf("%s: %w", file, err)
			}
			pkg.File = file
			manifests = append(manifests, pkg)
			packages[path.Dir(file)] = file
			packageManifests[file] = pkg
		} else if language := languages[path.Ext(file)]; language != "" {
			files = append(files, fileFact{Path: file, Language: language, ComponentIDs: []string{}})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	configs := readTSConfigs(fsys, paths)
	facts := map[string]any{}
	if slices.Contains(selected, "dependencies") {
		facts["dependencies"] = map[string]any{"basis": "package-json-declarations", "manifests": manifests}
	}
	wantArchitecture := slices.Contains(selected, "architecture")
	var reactFacts *react.Collector
	reactPackages := map[string]bool{}
	if wantArchitecture {
		type component struct {
			ID   string `json:"componentId"`
			Name string `json:"name"`
			Kind string `json:"kind"`
			File string `json:"file"`
		}
		components := make([]component, 0, len(manifests))
		for _, pkg := range manifests {
			if pkg.declares("react") {
				reactPackages[pkg.File] = true
			}
			name := pkg.Name
			if name == "" {
				name = path.Dir(pkg.File)
			}
			components = append(components, component{pkg.File, name, "package", pkg.File})
		}
		facts["architecture"] = map[string]any{"projectId": root, "basis": "package-json-declarations", "components": components,
			"limitations": []string{"react-wrappers-and-classes", "react-data-flow", "svelte-dynamic-components"}}
		if len(reactPackages) > 0 {
			reactFacts = react.New()
		}
	}
	wantFiles, wantIPC := slices.Contains(selected, "files"), slices.Contains(selected, "ipc")
	var graph *script.Graph
	wantCalls := slices.Contains(selected, "call-graph")
	if wantCalls || reactFacts != nil {
		graph = script.NewGraph(root)
	}
	hasSvelte := wantArchitecture && slices.ContainsFunc(files, func(file fileFact) bool { return file.Language == "Svelte" })
	if !wantFiles && !wantIPC && reactFacts == nil && graph == nil && !hasSvelte {
		return facts, nil
	}
	var framework *tauri.Collector
	if wantIPC {
		framework = tauri.New(fsys)
	}
	imports := []script.Import{}
	svelteFacts := svelte.NewFacts()
	for i := range files {
		file := &files[i]
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !wantFiles && !wantIPC && graph == nil && file.Language != "TSX" && file.Language != "JSX" && file.Language != "Svelte" {
			continue
		}
		owner := owningPackage(file.Path, packages)
		if owner != "" {
			file.ComponentIDs = append(file.ComponentIDs, owner)
		}
		data, err := fs.ReadFile(fsys, file.Path)
		if err != nil {
			return nil, err
		}
		fragments := []source.Fragment{{File: file.Path, Language: file.Language, Bytes: data}}
		var tags []svelte.Tag
		svelteImports := map[string][]string{}
		if file.Language == "Svelte" {
			fragments, tags, err = svelte.Scan(ctx, file.Path, data)
			if err != nil {
				return nil, err
			}
		}
		for _, fragment := range fragments {
			hasErrors, err := script.Parse(ctx, fragment, func(ctx context.Context, part source.Fragment, root *sitter.Node) error {
				if wantArchitecture && file.Language == "Svelte" {
					for name, modules := range script.DefaultImports(part, root) {
						svelteImports[name] = append(svelteImports[name], modules...)
					}
				}
				if wantFiles {
					for _, edge := range script.Imports(part, root) {
						resolveImport(&edge, paths, configs)
						if edge.Resolution == "unresolved" && packageManifests[owner].declares("@sveltejs/kit") {
							svelte.AddKitImportCandidate(&edge, path.Dir(owner), paths)
						}
						imports = append(imports, edge)
					}
				}
				if reactFacts != nil && reactPackages[owner] {
					if err := reactFacts.Observe(ctx, part, root, owner); err != nil {
						return err
					}
				}
				if graph != nil {
					if err := graph.Observe(ctx, part, root, wantCalls); err != nil {
						return err
					}
				}
				if framework != nil {
					return framework.Observe(ctx, part, root)
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			file.SyntaxErrors = file.SyntaxErrors || hasErrors
		}
		if wantArchitecture && file.Language == "Svelte" {
			svelteFacts.Add(file.Path, owner, tags, svelteImports, func(specifier string) string {
				edge := script.Import{Specifier: specifier}
				edge.File = file.Path
				resolveImport(&edge, paths, configs)
				return edge.TargetFile
			})
		}
	}
	if len(svelteFacts.Components) > 0 {
		facts["architecture"].(map[string]any)["svelte"] = svelteFacts
	}
	if wantFiles {
		facts["files"] = map[string]any{"projectId": root, "files": files, "imports": imports}
	}
	resolve := func(file, specifier string) string {
		edge := script.Import{Specifier: specifier}
		edge.File = file
		resolveImport(&edge, paths, configs)
		return edge.TargetFile
	}
	if wantCalls {
		if err := graph.Resolve(ctx, resolve); err != nil {
			return nil, err
		}
		facts["call-graph"] = graph
	}
	if reactFacts != nil {
		facts["architecture"].(map[string]any)["react"] = reactFacts.Facts(resolve, func(file, name string) string {
			return graph.ExportedFunction(file, name, resolve)
		}, func(file, specifier string) bool {
			pkg := packageManifests[owningPackage(file, packages)]
			name, rest, _ := strings.Cut(specifier, "/")
			if strings.HasPrefix(name, "@") {
				subpath, _, _ := strings.Cut(rest, "/")
				name += "/" + subpath
			}
			return name != pkg.Name && pkg.declares(name)
		})
	}
	var resolution *semantic.Report
	if resolveMode && (wantFiles || wantCalls || wantIPC) {
		result := enrichSemantic(ctx, root, files, facts, framework)
		resolution = &result
	}
	if framework != nil {
		ipc := framework.Facts()
		ipc["limitations"] = []string{"dynamic-channel-names", "tauri-wrapper-flow"}
		if resolution != nil {
			ipc["semantic"] = resolution
		}
		facts["ipc"] = ipc
	}
	return facts, nil
}

func owningPackage(file string, packages map[string]string) string {
	for dir := path.Dir(file); ; dir = path.Dir(dir) {
		if owner := packages[dir]; owner != "" || dir == "." {
			return owner
		}
	}
}

func resolveImport(edge *script.Import, paths map[string]bool, configs map[string]tsConfig) {
	edge.Resolution = "unresolved"
	bases := aliasBases(edge.File, edge.Specifier, configs)
	if strings.HasPrefix(edge.Specifier, "./") || strings.HasPrefix(edge.Specifier, "../") {
		bases = []string{path.Join(path.Dir(edge.File), edge.Specifier)}
	}
	for _, base := range bases {
		if paths[base] {
			edge.TargetFile, edge.Resolution = base, "resolved"
			return
		}
		candidates := []string{}
		for _, ext := range []string{".ts", ".tsx", ".js", ".jsx", ".mts", ".cts", ".mjs", ".cjs", ".svelte"} {
			for _, file := range []string{base + ext, path.Join(base, "index"+ext)} {
				if paths[file] {
					candidates = append(candidates, file)
				}
			}
		}
		switch len(candidates) {
		case 1:
			edge.TargetFile, edge.Resolution = candidates[0], "resolved"
			return
		case 0:
			continue
		default:
			edge.Resolution = "ambiguous"
			return
		}
	}
}

func (m manifest) declares(name string) bool {
	return m.Dependencies[name] != "" || m.DevDependencies[name] != "" ||
		m.PeerDependencies[name] != "" || m.OptionalDependencies[name] != ""
}
