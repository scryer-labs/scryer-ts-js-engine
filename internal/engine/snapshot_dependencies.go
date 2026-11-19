package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/scryer-labs/scryer-ts-js-engine/internal/analysis"
)

// Snapshot dependencies are execution inputs, not project source facts. They
// are prepared only for engine-owned comparison roots, never in the original.
func prepareSnapshotDependencies(ctx context.Context, root, original string) ([]string, error) {
	if original == "" || root == original {
		return nil, nil
	}
	var manifests []string
	err := filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", ".orrery", ".scryer", "target", "dist", "build", "coverage", ".svelte-kit":
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == "package.json" {
			manifests = append(manifests, file)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(manifests, func(i, j int) bool { return len(manifests[i]) < len(manifests[j]) })
	notes := []string{}
	for _, manifest := range manifests {
		data, err := os.ReadFile(manifest)
		if err != nil {
			return nil, err
		}
		var pkg map[string]json.RawMessage
		if err = json.Unmarshal(data, &pkg); err != nil {
			return nil, err
		}
		needs := false
		for _, key := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
			var deps map[string]any
			_ = json.Unmarshal(pkg[key], &deps)
			needs = needs || len(deps) > 0
		}
		if !needs {
			continue
		}
		directory := filepath.Dir(manifest)
		available := false
		for current := directory; ; current = filepath.Dir(current) {
			if info, err := os.Stat(filepath.Join(current, "node_modules")); err == nil && info.IsDir() {
				available = true
				break
			}
			if current == root || current == filepath.Dir(current) {
				break
			}
		}
		if available {
			continue
		}
		relative, err := filepath.Rel(root, directory)
		if err != nil || !filepath.IsLocal(relative) {
			return nil, fmt.Errorf("dependency directory is outside the snapshot")
		}
		source := filepath.Join(original, relative)
		match := true
		for _, name := range []string{"package.json", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock", "bun.lockb"} {
			left, le := os.ReadFile(filepath.Join(directory, name))
			right, re := os.ReadFile(filepath.Join(source, name))
			if os.IsNotExist(le) && os.IsNotExist(re) {
				continue
			}
			if le != nil || re != nil || !bytes.Equal(left, right) {
				match = false
				break
			}
		}
		// Keep generated dependency directories outside Git source-mutation checks.
		exclude := filepath.Join(root, ".git", "info", "exclude")
		if _, err := os.Stat(filepath.Dir(exclude)); err == nil {
			stream, err := os.OpenFile(exclude, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
			if err != nil {
				return nil, err
			}
			_, failure := stream.WriteString("\nnode_modules\nnode_modules/\n")
			closeErr := stream.Close()
			if failure != nil {
				return nil, failure
			}
			if closeErr != nil {
				return nil, closeErr
			}
		}
		target := filepath.Join(directory, "node_modules")
		if info, err := os.Stat(filepath.Join(source, "node_modules")); match && err == nil && info.IsDir() {
			dependencies, err := filepath.Abs(filepath.Join(source, "node_modules"))
			if err != nil {
				return nil, err
			}
			if err = os.Symlink(dependencies, target); err != nil {
				return nil, err
			}
			notes = append(notes, fmt.Sprintf("%s: reused the existing installed dependency tree after matching manifests and lockfiles; package.json SHA-256 %s. Installation integrity remains an ambient toolchain input.", filepath.ToSlash(relative), analysis.Hash(data)))
			continue
		}
		if _, err := os.Stat(filepath.Join(directory, "package-lock.json")); err != nil {
			if _, other := os.Stat(filepath.Join(directory, "npm-shrinkwrap.json")); other != nil {
				return nil, fmt.Errorf("%s: snapshot dependencies are unavailable; matching installed dependencies or a cached npm lockfile are required", manifest)
			}
		}
		_, err = analysis.Command(ctx, directory, map[string]string{"CI": "true"}, "npm", "ci", "--offline", "--ignore-scripts", "--no-audit", "--no-fund")
		if err != nil {
			return nil, fmt.Errorf("prepare frozen snapshot dependencies for %s: %w", manifest, err)
		}
		notes = append(notes, filepath.ToSlash(relative)+": installed snapshot dependencies with npm ci --offline --ignore-scripts; lifecycle scripts were not run.")
	}
	return notes, nil
}
