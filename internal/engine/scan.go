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

	"github.com/scryer-labs/scryer-ts-js-engine/internal/framework/svelte"
	"github.com/scryer-labs/scryer-ts-js-engine/internal/framework/tauri"
	"github.com/scryer-labs/scryer-ts-js-engine/internal/script"
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

func scanProject(ctx context.Context, root string, selected []string) (map[string]any, error) {
	fsys := os.DirFS(root)
	packages := map[string]string{}
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
		} else if language := languages[path.Ext(file)]; language != "" {
			paths[file] = true
			files = append(files, fileFact{Path: file, Language: language, ComponentIDs: []string{}})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	facts := map[string]any{}
	if slices.Contains(selected, "dependencies") {
		facts["dependencies"] = map[string]any{"basis": "package-json-declarations", "manifests": manifests}
	}
	if slices.Contains(selected, "architecture") {
		type component struct {
			ID   string `json:"componentId"`
			Name string `json:"name"`
			Kind string `json:"kind"`
			File string `json:"file"`
		}
		components := make([]component, 0, len(manifests))
		for _, pkg := range manifests {
			name := pkg.Name
			if name == "" {
				name = path.Dir(pkg.File)
			}
			components = append(components, component{pkg.File, name, "package", pkg.File})
		}
		facts["architecture"] = map[string]any{"projectId": root, "basis": "package-json-declarations", "components": components}
	}
	wantFiles, wantIPC := slices.Contains(selected, "files"), slices.Contains(selected, "ipc")
	if !wantFiles && !wantIPC {
		return facts, nil
	}
	var framework *tauri.Collector
	if wantIPC {
		framework = tauri.New(fsys)
	}
	imports := []script.Import{}
	for i := range files {
		file := &files[i]
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if owner := owningPackage(file.Path, packages); owner != "" {
			file.ComponentIDs = append(file.ComponentIDs, owner)
		}
		data, err := fs.ReadFile(fsys, file.Path)
		if err != nil {
			return nil, err
		}
		fragments := []source.Fragment{{File: file.Path, Language: file.Language, Bytes: data}}
		if file.Language == "Svelte" {
			fragments, err = svelte.Scripts(ctx, file.Path, data)
			if err != nil {
				return nil, err
			}
		}
		for _, fragment := range fragments {
			hasErrors, err := script.Parse(ctx, fragment, func(ctx context.Context, part source.Fragment, root *sitter.Node) error {
				if wantFiles {
					for _, edge := range script.Imports(part, root) {
						resolveImport(&edge, paths)
						imports = append(imports, edge)
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
	}
	if wantFiles {
		facts["files"] = map[string]any{"projectId": root, "files": files, "imports": imports}
	}
	if framework != nil {
		facts["ipc"] = framework.Facts()
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

func resolveImport(edge *script.Import, paths map[string]bool) {
	edge.Resolution = "unresolved"
	if !strings.HasPrefix(edge.Specifier, "./") && !strings.HasPrefix(edge.Specifier, "../") {
		return
	}
	base := path.Join(path.Dir(edge.File), edge.Specifier)
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
	if len(candidates) == 1 {
		edge.TargetFile, edge.Resolution = candidates[0], "resolved"
	}
	if len(candidates) > 1 {
		edge.Resolution = "ambiguous"
	}
}
