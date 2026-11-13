package script

import (
	"context"
	"fmt"
	"slices"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/scryer-labs/scryer-ts-js-engine/internal/source"
)

type Position struct {
	Line   uint `json:"line"`
	Column uint `json:"column"`
}

type Range struct {
	File  string   `json:"file"`
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Symbol struct {
	ID         string `json:"symbolId"`
	Name       string `json:"name,omitempty"`
	Kind       string `json:"kind"`
	Definition Range  `json:"definition"`
}

type Call struct {
	ID         string `json:"callSiteId"`
	Caller     string `json:"callerSymbolId,omitempty"`
	Callee     string `json:"callee"`
	Target     string `json:"targetSymbolId,omitempty"`
	Resolution string `json:"resolution"`
	Site       Range  `json:"site"`
	binding    *binding
}

type Graph struct {
	ProjectID string   `json:"projectId"`
	Basis     string   `json:"basis"`
	Symbols   []Symbol `json:"symbols"`
	Calls     []Call   `json:"calls"`
	exports   map[string]map[string][]*binding
}

type binding struct {
	symbol, module, exported string
}

func NewGraph(root string) *Graph {
	return &Graph{ProjectID: root, Basis: "tree-sitter-syntax", Symbols: []Symbol{}, Calls: []Call{}, exports: map[string]map[string][]*binding{}}
}

func span(part source.Fragment, node *sitter.Node) Range {
	start := part.Location(node)
	end := node.EndPosition()
	column := end.Column + 1
	if end.Row == 0 {
		column += part.Start.Column
	}
	return Range{start.File, Position{start.Line, start.Column}, Position{part.Start.Row + end.Row + 1, column}}
}

func isFunction(node *sitter.Node) bool {
	switch node.Kind() {
	case "function_declaration", "generator_function_declaration", "function_expression", "generator_function", "arrow_function", "method_definition":
		return true
	}
	return false
}

func symbolID(part source.Fragment, node *sitter.Node) string {
	loc := part.Location(node)
	return fmt.Sprintf("%s:%d:%d", part.File, loc.Line, loc.Column)
}

func (g *Graph) Observe(ctx context.Context, part source.Fragment, root *sitter.Node) error {
	var walk func(*sitter.Node, string, []map[string][]*binding) error
	walk = func(node *sitter.Node, caller string, scopes []map[string][]*binding) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch node.Kind() {
		case "program", "statement_block", "switch_body":
			scope := localBindings(part, node)
			scopes = append(scopes, scope)
			if node.Kind() == "program" && part.Language != "Svelte" {
				// Svelte fragments share a file path but each has its own bindings.
				exports := g.exports[part.File]
				if exports == nil {
					exports = map[string][]*binding{}
					g.exports[part.File] = exports
				}
				addExports(part, node, scope, exports)
			}
		case "for_statement", "for_in_statement":
			scope := map[string][]*binding{}
			for _, field := range []string{"initializer", "left"} {
				if init := node.ChildByFieldName(field); init != nil {
					if init.Kind() == "lexical_declaration" || init.Kind() == "variable_declaration" {
						for i := uint(0); i < init.NamedChildCount(); i++ {
							bindPattern(part, init.NamedChild(i).ChildByFieldName("name"), scope)
						}
					} else if node.Kind() == "for_in_statement" {
						bindPattern(part, init, scope)
					}
				}
			}
			scopes = append(scopes, scope)
		case "catch_clause":
			scope := map[string][]*binding{}
			bindPattern(part, node.ChildByFieldName("parameter"), scope)
			scopes = append(scopes, scope)
		case "call_expression":
			callee := node.ChildByFieldName("function")
			if callee != nil {
				if callee.Kind() == "await_expression" && callee.NamedChildCount() == 1 {
					callee = callee.NamedChild(0)
				}
				location := span(part, node)
				site := Call{ID: fmt.Sprintf("%s-%d:%d", symbolID(part, node), location.End.Line, location.End.Column),
					Caller: caller, Callee: callee.Utf8Text(part.Bytes), Resolution: "unresolved", Site: location}
				if !node.HasError() && callee.Kind() == "identifier" {
					for _, scope := range slices.Backward(scopes) {
						if candidates, exists := scope[site.Callee]; exists {
							if len(candidates) == 1 {
								site.binding = candidates[0]
							}
							break
						}
					}
				}
				g.Calls = append(g.Calls, site)
			}
		}
		if isFunction(node) {
			caller = symbolID(part, node)
			symbol := Symbol{ID: caller, Kind: "function", Definition: span(part, node)}
			name := node.ChildByFieldName("name")
			if name == nil {
				if parent := node.Parent(); parent != nil && parent.Kind() == "variable_declarator" {
					name = parent.ChildByFieldName("name")
				}
			}
			if name != nil {
				symbol.Name = name.Utf8Text(part.Bytes)
			}
			if node.Kind() == "arrow_function" && name == nil {
				symbol.Kind = "lambda"
			}
			g.Symbols = append(g.Symbols, symbol)
			scope := map[string][]*binding{}
			for _, field := range []string{"parameter", "parameters"} {
				bindPattern(part, node.ChildByFieldName(field), scope)
			}
			if node.Kind() == "function_expression" || node.Kind() == "generator_function" {
				bindPattern(part, node.ChildByFieldName("name"), scope)
			}
			blockHoistedVars(part, node.ChildByFieldName("body"), scope)
			scopes = append(scopes, scope)
		}
		for i := uint(0); i < node.NamedChildCount(); i++ {
			if err := walk(node.NamedChild(i), caller, scopes); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root, "", nil)
}

func (g *Graph) Resolve(ctx context.Context, resolve func(file, specifier string) string) error {
	for i := range g.Calls {
		if err := ctx.Err(); err != nil {
			return err
		}
		call := &g.Calls[i]
		target := call.binding
		if target == nil {
			continue
		}
		if target.module != "" {
			file := resolve(call.Site.File, target.module)
			exports := g.exports[file][target.exported]
			if len(exports) != 1 || exports[0] == nil {
				continue
			}
			target = exports[0]
		}
		if target.symbol != "" {
			call.Target, call.Resolution = target.symbol, "resolved"
		}
	}
	return nil
}
