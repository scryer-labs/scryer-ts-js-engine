package svelte

import (
	"context"
	"path"
	"strings"
	"unicode"

	"github.com/scryer-labs/scryer-ts-js-engine/internal/script"
	"github.com/scryer-labs/scryer-ts-js-engine/internal/source"
	sitter "github.com/tree-sitter/go-tree-sitter"
	html "github.com/tree-sitter/tree-sitter-html/bindings/go"
)

func componentID(file string) string { return file + "#svelte-component" }

// AddComponent retains template/module context as a source entity. It is not an
// executable function and must not receive a method coverage hit.
func AddComponent(graph *script.Graph, file string, data []byte) {
	lines := strings.Split(string(data), "\n")
	graph.Symbols = append(graph.Symbols, script.Symbol{
		ID: componentID(file), Name: path.Base(file), Kind: "component", ExecutionKind: "non-executable",
		Definition: script.Range{File: file, Start: script.Position{Line: 1, Column: 1}, End: script.Position{Line: uint(len(lines)), Column: uint(len(lines[len(lines)-1])) + 1}},
		ImpactKey:  file + "|svelte-component", ImpactSignature: "Svelte component " + file,
		ImpactFingerprint: graph.Contexts[file], ImpactSource: string(data),
	})
}

// AddReferences keeps component and simple event-binding references distinct
// from invocations. Template-local scopes remain unresolved rather than being
// guessed from identically named script bindings.
func AddReferences(ctx context.Context, graph *script.Graph, file string, data []byte, facts Facts, resolve func(string, string) string) error {
	for _, link := range facts.Links {
		if link.File != file {
			continue
		}
		to := ""
		if link.To != "" {
			to = componentID(link.To)
		}
		graph.AddReference(componentID(file), to, link.Name, "svelte-component", link.Resolution,
			script.Range{File: file, Start: script.Position{Line: link.Line, Column: link.Column}, End: script.Position{Line: link.Line, Column: link.Column + uint(len(link.Name))}})
	}
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(html.Language())); err != nil {
		return err
	}
	tree := parser.ParseWithOptions(func(offset int, _ sitter.Point) []byte { return data[offset:] }, nil,
		&sitter.ParseOptions{ProgressCallback: func(sitter.ParseState) bool { return ctx.Err() != nil }})
	if tree == nil {
		return ctx.Err()
	}
	defer tree.Close()
	part := source.Fragment{File: file, Bytes: data}
	text := string(data)
	scoped := strings.Contains(text, "{#") || strings.Contains(text, "{:") || strings.Contains(text, "let:")
	var walk func(*sitter.Node) error
	walk = func(node *sitter.Node) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if node.Kind() == "script_element" || node.Kind() == "style_element" || node.Kind() == "comment" {
			return nil
		}
		if node.Kind() == "attribute" && !node.HasError() && node.NamedChildCount() >= 2 {
			name := node.NamedChild(0).Utf8Text(data)
			if strings.HasPrefix(name, "on:") || strings.HasPrefix(name, "on") {
				value := strings.TrimSpace(node.NamedChild(1).Utf8Text(data))
				if strings.HasPrefix(value, "{") && strings.HasSuffix(value, "}") {
					identifier := strings.TrimSpace(value[1 : len(value)-1])
					if simpleIdentifier(identifier) {
						target, resolution := "", "unresolved-template-binding"
						if !scoped {
							target = graph.ReferencedFunction(file, identifier, resolve)
						}
						if target != "" {
							resolution = "source-reference"
						}
						loc := part.Location(node)
						end := node.EndPosition()
						graph.AddReference(componentID(file), target, identifier, "svelte-handler", resolution,
							script.Range{File: file, Start: script.Position{Line: loc.Line, Column: loc.Column}, End: script.Position{Line: end.Row + 1, Column: end.Column + 1}})
					}
				}
			}
		}
		for i := uint(0); i < node.NamedChildCount(); i++ {
			if err := walk(node.NamedChild(i)); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(tree.RootNode())
}

func simpleIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if r == '_' || r == '$' || unicode.IsLetter(r) || i > 0 && unicode.IsDigit(r) {
			continue
		}
		return false
	}
	return true
}
