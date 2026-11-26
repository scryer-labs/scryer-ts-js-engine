package script

import (
	"context"
	"fmt"
	"slices"
	"strings"

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
	ExecutionKind      string `json:"executionKind,omitempty"`
	NameLocation       *Range `json:"nameLocation,omitempty"`
	ImpactKey          string `json:"-"`
	ImpactSignature    string `json:"-"`
	ImpactFingerprint  string `json:"-"`
	ImpactSource       string `json:"-"`
	ImpactRegistration string `json:"-"`
	ID                 string `json:"symbolId"`
	Name               string `json:"name,omitempty"`
	Kind               string `json:"kind"`
	Definition         Range  `json:"definition"`
}

type Call struct {
	SemanticTarget string        `json:"semanticTargetSymbolId,omitempty"`
	ID             string        `json:"callSiteId"`
	Caller         string        `json:"callerSymbolId,omitempty"`
	Callee         string        `json:"callee"`
	Target         string        `json:"targetSymbolId,omitempty"`
	Resolution     string        `json:"resolution"`
	Reason         string        `json:"resolutionReason,omitempty"`
	TestEvidence   *TestEvidence `json:"testEvidence,omitempty"`
	Site           Range         `json:"site"`
	binding        *binding
}

// TestEvidence marks the registration call, without classifying its enclosing file.
type TestEvidence struct {
	Module string `json:"module"`
	Export string `json:"export"`
	Kind   string `json:"kind"`
}

func testEvidence(candidate *binding) *TestEvidence {
	switch candidate.module {
	case "vitest", "@jest/globals", "node:test", "@playwright/test":
	default:
		return nil
	}
	switch candidate.exported {
	case "test", "it":
		return &TestEvidence{candidate.module, candidate.exported, "test-registration"}
	case "describe", "suite":
		return &TestEvidence{candidate.module, candidate.exported, "test-suite"}
	case "bench":
		if candidate.module == "vitest" {
			return &TestEvidence{candidate.module, candidate.exported, "benchmark-registration"}
		}
	}
	return nil
}

// Reference records a source dependency, never a proven function invocation.
type Reference struct {
	ID         string `json:"referenceId"`
	From       string `json:"sourceSymbolId"`
	To         string `json:"targetSymbolId,omitempty"`
	Name       string `json:"name"`
	Basis      string `json:"basis"`
	Resolution string `json:"resolution"`
	Site       Range  `json:"site"`
}

type Graph struct {
	ranges      map[string][]Range
	Contexts    map[string]string `json:"-"`
	Semantic    any               `json:"semantic,omitempty"`
	ProjectID   string            `json:"projectId"`
	Basis       string            `json:"basis"`
	Symbols     []Symbol          `json:"symbols"`
	Limitations []string          `json:"limitations"`
	Calls       []Call            `json:"calls"`
	References  []Reference       `json:"references"`
	locals      map[string]map[string][]*binding
	exports     map[string]map[string][]*binding
	stars       map[string][]string
}

type binding struct {
	symbol, module, exported string
}

func NewGraph(root string) *Graph {
	return &Graph{ProjectID: root, Basis: "tree-sitter-syntax", Symbols: []Symbol{}, Calls: []Call{}, References: []Reference{}, locals: map[string]map[string][]*binding{}, exports: map[string]map[string][]*binding{}, stars: map[string][]string{}, Limitations: []string{"dynamic-dispatch", "external-package-symbols", "mutable-bindings", "svelte-template-expressions"}}
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

func (g *Graph) Observe(ctx context.Context, part source.Fragment, root *sitter.Node, collectCalls bool) error {
	writes := writtenNames(part, root)
	var walk func(*sitter.Node, string, []map[string][]*binding) error
	walk = func(node *sitter.Node, caller string, scopes []map[string][]*binding) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch node.Kind() {
		case "program", "statement_block", "switch_body":
			scope := localBindings(part, node, writes)
			scopes = append(scopes, scope)
			if node.Kind() == "program" {
				if g.locals[part.File] == nil {
					g.locals[part.File] = map[string][]*binding{}
				}
				for name, candidates := range scope {
					g.locals[part.File][name] = append(g.locals[part.File][name], candidates...)
				}
			}
			if node.Kind() == "program" && !strings.HasSuffix(part.File, ".svelte") {
				// Svelte fragments share a file path but each has its own bindings.
				exports := g.exports[part.File]
				if exports == nil {
					exports = map[string][]*binding{}
					g.exports[part.File] = exports
				}
				stars := g.stars[part.File]
				addExports(part, node, scope, exports, &stars)
				g.stars[part.File] = stars
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
				site.Reason = "indirect-call"
				if node.HasError() {
					site.Reason = "syntax-error"
				} else if callee.Kind() == "identifier" {
					site.Reason = "binding-not-found"
					for _, scope := range slices.Backward(scopes) {
						if candidates, exists := scope[site.Callee]; exists {
							site.Reason = "binding-not-statically-resolved"
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
				location := span(part, name)
				symbol.NameLocation = &location
			}
			if node.Kind() == "arrow_function" && name == nil {
				symbol.Kind = "lambda"
			}
			symbol.ImpactKey, symbol.ImpactSignature, symbol.ImpactFingerprint, symbol.ImpactSource, symbol.ImpactRegistration = impactIdentity(part, node, symbol.Name)
			g.Symbols = append(g.Symbols, symbol)
			if g.ranges == nil {
				g.ranges = map[string][]Range{}
			}
			if symbol.ImpactKey != "" {
				g.ranges[part.File] = append(g.ranges[part.File], symbol.Definition)
			}
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
	if !collectCalls {
		if !strings.HasSuffix(part.File, ".svelte") {
			exports := map[string][]*binding{}
			var stars []string
			addExports(part, root, localBindings(part, root, writes), exports, &stars)
			g.exports[part.File], g.stars[part.File] = exports, stars
		}
		return ctx.Err()
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
			call.Reason = "import-target-not-found"
			if file == "" {
				call.TestEvidence = testEvidence(target)
				continue
			}
			call.Reason = "export-not-statically-resolved"
			symbol := g.ExportedFunction(file, target.exported, resolve)
			if symbol == "" {
				continue
			}
			target = &binding{symbol: symbol}
		}
		if target.symbol != "" {
			call.Target, call.Resolution, call.Reason = target.symbol, "resolved", ""
		}
	}
	return nil
}

// ExportedFunction returns a source symbol ID when the export chain is unique.
func (g *Graph) ExportedFunction(file, name string, resolve func(string, string) string) string {
	return g.exportedSymbol(file, name, resolve, map[exportLookup]bool{})
}

type exportLookup struct{ file, name string }

func (g *Graph) exportedSymbol(file, name string, resolve func(string, string) string, seen map[exportLookup]bool) string {
	key := exportLookup{file, name}
	if file == "" || seen[key] {
		return ""
	}
	seen[key] = true
	defer delete(seen, key)
	if candidates, explicit := g.exports[file][name]; explicit {
		if len(candidates) != 1 || candidates[0] == nil {
			return ""
		}
		candidate := candidates[0]
		if candidate.module == "" {
			return candidate.symbol
		}
		return g.exportedSymbol(resolve(file, candidate.module), candidate.exported, resolve, seen)
	}
	// One star can safely forward the name. Multiple stars can hide conflicting
	// exports even when only one of their targets is a recognized function.
	if stars := g.stars[file]; name != "default" && len(stars) == 1 {
		return g.exportedSymbol(resolve(file, stars[0]), name, resolve, seen)
	}
	return ""
}

// AddReference keeps stable source locations and a relationship kind separate
// from calls, even when the target happens to be a function symbol.
func (g *Graph) AddReference(from, to, name, basis, resolution string, site Range) {
	g.References = append(g.References, Reference{
		ID:   fmt.Sprintf("%s:%d:%d:%s:%s", site.File, site.Start.Line, site.Start.Column, basis, to),
		From: from, To: to, Name: name, Basis: basis, Resolution: resolution, Site: site,
	})
}

// ReferencedFunction uses the same stable module bindings as call resolution.
// Ambiguous, reassigned and non-callable bindings never become source targets.
func (g *Graph) ReferencedFunction(file, name string, resolve func(string, string) string) string {
	candidates := g.locals[file][name]
	if len(candidates) != 1 || candidates[0] == nil {
		return ""
	}
	candidate := candidates[0]
	if candidate.module != "" {
		return g.ExportedFunction(resolve(file, candidate.module), candidate.exported, resolve)
	}
	return candidate.symbol
}
