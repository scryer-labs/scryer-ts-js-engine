package script

import (
	"context"
	"fmt"

	sitter "github.com/tree-sitter/go-tree-sitter"
	js "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	ts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"github.com/scryer-labs/scryer-ts-js-engine/internal/source"
)

type Import struct {
	SemanticTarget string `json:"semanticTargetFile,omitempty"`
	source.Location
	Specifier  string `json:"specifier"`
	Kind       string `json:"kind"`
	TargetFile string `json:"targetFile,omitempty"`
	Resolution string `json:"resolution"`
}

type Observer func(context.Context, source.Fragment, *sitter.Node) error

func Parse(ctx context.Context, fragment source.Fragment, observe Observer) (bool, error) {
	parser := sitter.NewParser()
	defer parser.Close()
	var language *sitter.Language
	switch fragment.Language {
	case "TypeScript":
		language = sitter.NewLanguage(ts.LanguageTypescript())
	case "TSX":
		language = sitter.NewLanguage(ts.LanguageTSX())
	case "JavaScript", "JSX":
		language = sitter.NewLanguage(js.Language())
	default:
		return false, fmt.Errorf("unsupported script language %q", fragment.Language)
	}
	if err := parser.SetLanguage(language); err != nil {
		return false, err
	}
	tree := parser.ParseWithOptions(func(offset int, _ sitter.Point) []byte {
		return fragment.Bytes[offset:]
	}, nil, &sitter.ParseOptions{ProgressCallback: func(sitter.ParseState) bool { return ctx.Err() != nil }})
	if tree != nil {
		defer tree.Close()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if tree == nil {
		return false, fmt.Errorf("could not parse %s", fragment.File)
	}
	root := tree.RootNode()
	return root.HasError(), observe(ctx, fragment, root)
}

func Imports(fragment source.Fragment, root *sitter.Node) []Import {
	var result []Import
	for i := uint(0); i < root.NamedChildCount(); i++ {
		node := root.NamedChild(i)
		var kind string
		switch node.Kind() {
		case "import_statement":
			kind = "import"
		case "export_statement":
			kind = "re-export"
		default:
			continue
		}
		if value, ok := source.String(node.ChildByFieldName("source"), fragment.Bytes); ok {
			result = append(result, Import{Location: fragment.Location(node), Specifier: value, Kind: kind})
		}
	}
	return result
}

// DefaultImports returns local names for statically declared default imports.
func DefaultImports(fragment source.Fragment, root *sitter.Node) map[string][]string {
	result := map[string][]string{}
	for i := uint(0); i < root.NamedChildCount(); i++ {
		node := root.NamedChild(i)
		if node.Kind() != "import_statement" || hasToken(node, "type") || node.HasError() {
			continue
		}
		module, ok := source.String(node.ChildByFieldName("source"), fragment.Bytes)
		if !ok {
			continue
		}
		clause := node.NamedChild(0)
		if clause == nil || clause.Kind() != "import_clause" {
			continue
		}
		if name := clause.NamedChild(0); name != nil && name.Kind() == "identifier" {
			local := name.Utf8Text(fragment.Bytes)
			result[local] = append(result[local], module)
		}
	}
	return result
}
