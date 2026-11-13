package svelte

import (
	"github.com/scryer-labs/scryer-ts-js-engine/internal/script"
	"io/fs"
	"path"
	"strings"
)

type Component struct {
	ID        string `json:"componentId"`
	File      string `json:"file"`
	PackageID string `json:"packageId,omitempty"`
	Kind      string `json:"kind"`
}

type Link struct {
	Tag
	From       string `json:"fromComponentId"`
	To         string `json:"toComponentId,omitempty"`
	Resolution string `json:"resolution"`
}

type Facts struct {
	Basis       string      `json:"basis"`
	Limitations []string    `json:"limitations"`
	Components  []Component `json:"components"`
	Links       []Link      `json:"links"`
}

func NewFacts() Facts {
	return Facts{Basis: "svelte-static-tags", Limitations: []string{"template-scope", "dynamic-components", "template-expressions"}, Components: []Component{}, Links: []Link{}}
}

func (f *Facts) Add(file, owner string, tags []Tag, imports map[string][]string, resolve func(string) string) {
	f.Components = append(f.Components, Component{file, file, owner, "svelte-component"})
	for _, tag := range tags {
		link := Link{Tag: tag, From: file, Resolution: "unresolved"}
		if modules := imports[tag.Name]; len(modules) == 1 {
			if target := resolve(modules[0]); path.Ext(target) == ".svelte" {
				link.To, link.Resolution = target, "import-candidate"
			}
		}
		f.Links = append(f.Links, link)
	}
}

// AddKitImportCandidate records the default SvelteKit layout when no explicit
// alias resolved. Custom kit.files.lib configuration can override this layout.
func AddKitImportCandidate(edge *script.Import, root string, paths map[string]bool) {
	suffix, ok := strings.CutPrefix(edge.Specifier, "$lib/")
	if !ok || suffix == "" || !fs.ValidPath(suffix) {
		return
	}
	base := path.Join(root, "src/lib", suffix)
	candidates := []string{}
	for _, ext := range []string{"", ".ts", ".tsx", ".js", ".jsx", ".svelte"} {
		for _, file := range []string{base + ext, path.Join(base, "index"+ext)} {
			if paths[file] {
				candidates = append(candidates, file)
			}
		}
	}
	if len(candidates) == 1 {
		edge.TargetFile, edge.Resolution = candidates[0], "convention-candidate"
	}
}
