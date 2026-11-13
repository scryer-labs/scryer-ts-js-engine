// Package react collects JSX component declarations and composition evidence.
package react

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/scryer-labs/scryer-ts-js-engine/internal/source"
)

type Component struct {
	source.Location
	ID        string `json:"componentId"`
	Name      string `json:"name"`
	PackageID string `json:"packageId"`
	Kind      string `json:"kind"`
}

type Link struct {
	source.Location
	From            string `json:"fromComponentId"`
	To              string `json:"toComponentId,omitempty"`
	Tag             string `json:"tag"`
	Resolution      string `json:"resolution"`
	ImportSpecifier string `json:"importSpecifier,omitempty"`
	ImportedName    string `json:"importedName,omitempty"`
}

type Facts struct {
	Components []Component `json:"components"`
	Links      []Link      `json:"links"`
}

type Collector struct {
	facts   Facts
	locals  map[string]map[string]string
	exports map[string]map[string]string
	pending []pendingLink
}

type binding struct {
	specifier, exported string
}

type pendingLink struct {
	link     Link
	imported binding
	dynamic  bool
}

func New() *Collector {
	return &Collector{
		facts:  Facts{Components: []Component{}, Links: []Link{}},
		locals: map[string]map[string]string{}, exports: map[string]map[string]string{},
	}
}

func (c *Collector) Observe(ctx context.Context, part source.Fragment, root *sitter.Node, packageID string) error {
	if part.Language != "TSX" && part.Language != "JSX" {
		return nil
	}
	locals := map[string]string{}
	exports := map[string]string{}
	c.locals[part.File], c.exports[part.File] = locals, exports
	imports := directImports(part, root)
	var exportAliases [][2]string
	for i := uint(0); i < root.NamedChildCount(); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		item := root.NamedChild(i)
		declaration := item
		exported := item.Kind() == "export_statement"
		isDefault := exported && hasToken(item, "default")
		if exported {
			declaration = item.ChildByFieldName("declaration")
			if declaration == nil {
				exportAliases = append(exportAliases, localExports(item, part.Bytes)...)
				continue
			}
		}
		add := func(name, function *sitter.Node) error {
			if name == nil || function == nil {
				return nil
			}
			symbol := name.Utf8Text(part.Bytes)
			if !componentName(symbol) {
				return nil
			}
			body := function.ChildByFieldName("body")
			if body == nil || function.HasError() {
				return nil
			}
			tags, renders, err := renderSites(ctx, part, body)
			if err != nil || !renders {
				return err
			}
			loc := part.Location(name)
			id := fmt.Sprintf("%s:%d:%d:%s", loc.File, loc.Line, loc.Column, symbol)
			c.facts.Components = append(c.facts.Components, Component{Location: loc, ID: id, Name: symbol, PackageID: packageID, Kind: "react-component"})
			locals[symbol] = id
			if isDefault {
				exports["default"] = id
			} else if exported {
				exports[symbol] = id
			}
			for _, tag := range tags {
				c.pending = append(c.pending, pendingLink{
					link:     Link{Location: tag.location, From: id, Tag: tag.name, Resolution: "unresolved"},
					imported: imports[tag.name],
					dynamic:  tag.dynamic,
				})
			}
			return nil
		}
		switch declaration.Kind() {
		case "function_declaration":
			if err := add(declaration.ChildByFieldName("name"), declaration); err != nil {
				return err
			}
		case "lexical_declaration":
			if !hasToken(declaration, "const") {
				continue
			}
			for j := uint(0); j < declaration.NamedChildCount(); j++ {
				entry := declaration.NamedChild(j)
				name, value := entry.ChildByFieldName("name"), entry.ChildByFieldName("value")
				if entry.Kind() != "variable_declarator" || value == nil || name == nil || name.Kind() != "identifier" {
					continue
				}
				if value.Kind() == "arrow_function" || value.Kind() == "function_expression" {
					if err := add(name, value); err != nil {
						return err
					}
				}
			}
		}
	}
	for _, alias := range exportAliases {
		if id := locals[alias[0]]; id != "" {
			exports[alias[1]] = id
		}
	}
	return nil
}

func (c *Collector) Facts(resolve func(file, specifier string) string, external func(file, specifier string) bool) Facts {
	result := c.facts
	for _, site := range c.pending {
		link := site.link
		if site.dynamic {
			link.Resolution = "dynamic"
		} else if site.imported.specifier != "" {
			link.ImportSpecifier, link.ImportedName = site.imported.specifier, site.imported.exported
			file := resolve(link.File, site.imported.specifier)
			link.To = c.exports[file][site.imported.exported]
			if file == "" && external(link.File, site.imported.specifier) {
				link.Resolution = "external"
			}
		} else {
			link.To = c.locals[link.File][link.Tag]
		}
		if link.To != "" {
			link.Resolution = "resolved"
		}
		result.Links = append(result.Links, link)
	}
	return result
}

func componentName(name string) bool {
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}

func hasToken(node *sitter.Node, kind string) bool {
	for i := uint(0); i < node.ChildCount(); i++ {
		if node.Child(i).Kind() == kind {
			return true
		}
	}
	return false
}

func directImports(part source.Fragment, root *sitter.Node) map[string]binding {
	result := map[string]binding{}
	for i := uint(0); i < root.NamedChildCount(); i++ {
		item := root.NamedChild(i)
		if item.Kind() != "import_statement" || item.HasError() || hasToken(item, "type") {
			continue
		}
		specifier, ok := source.String(item.ChildByFieldName("source"), part.Bytes)
		clause := item.NamedChild(0)
		if !ok || clause == nil || clause.Kind() != "import_clause" {
			continue
		}
		for j := uint(0); j < clause.NamedChildCount(); j++ {
			node := clause.NamedChild(j)
			switch node.Kind() {
			case "identifier":
				result[node.Utf8Text(part.Bytes)] = binding{specifier, "default"}
			case "named_imports":
				for k := uint(0); k < node.NamedChildCount(); k++ {
					spec := node.NamedChild(k)
					if spec.Kind() != "import_specifier" || hasToken(spec, "type") {
						continue
					}
					name, alias := spec.ChildByFieldName("name"), spec.ChildByFieldName("alias")
					if name != nil && name.Kind() == "identifier" {
						exported := name.Utf8Text(part.Bytes)
						local := exported
						if alias != nil {
							local = alias.Utf8Text(part.Bytes)
						}
						result[local] = binding{specifier, exported}
					}
				}
			}
		}
	}
	return result
}

func localExports(item *sitter.Node, contents []byte) [][2]string {
	if item.ChildByFieldName("source") != nil {
		return nil
	}
	if value := item.ChildByFieldName("value"); hasToken(item, "default") && value != nil && value.Kind() == "identifier" {
		return [][2]string{{value.Utf8Text(contents), "default"}}
	}
	var exports [][2]string
	for i := uint(0); i < item.NamedChildCount(); i++ {
		clause := item.NamedChild(i)
		if clause.Kind() != "export_clause" {
			continue
		}
		for j := uint(0); j < clause.NamedChildCount(); j++ {
			spec := clause.NamedChild(j)
			name, alias := spec.ChildByFieldName("name"), spec.ChildByFieldName("alias")
			if spec.Kind() != "export_specifier" || name == nil || hasToken(spec, "type") {
				continue
			}
			local := name.Utf8Text(contents)
			exported := local
			if alias != nil {
				exported = alias.Utf8Text(contents)
			}
			exports = append(exports, [2]string{local, exported})
		}
	}
	return exports
}

type renderSite struct {
	name     string
	location source.Location
	dynamic  bool
}

func renderSites(ctx context.Context, part source.Fragment, body *sitter.Node) ([]renderSite, bool, error) {
	var tags []renderSite
	renders := false
	var visit func(*sitter.Node, bool) error
	visit = func(node *sitter.Node, returned bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch node.Kind() {
		case "arrow_function":
			// JSX returned by a map callback in a render expression contributes to that expression.
			if returned && mapCallback(node, part.Bytes) {
				if callbackBody := node.ChildByFieldName("body"); callbackBody != nil {
					return visit(callbackBody, callbackBody.Kind() != "statement_block")
				}
			}
			return nil
		case "function_declaration", "function_expression", "class", "class_declaration", "method_definition":
			return nil
		case "return_statement":
			returned = true
		case "jsx_opening_element", "jsx_self_closing_element":
			if returned {
				renders = true
				if tag := node.ChildByFieldName("name"); tag != nil {
					name := tag.Utf8Text(part.Bytes)
					if tag.Kind() == "member_expression" || tag.Kind() == "identifier" && componentName(name) && !strings.Contains(name, "-") {
						base, _, _ := strings.Cut(name, ".")
						tags = append(tags, renderSite{name, part.Location(tag), locallyBound(tag, body, base, part.Bytes)})
					}
				}
			}
		}
		for i := uint(0); i < node.NamedChildCount(); i++ {
			if err := visit(node.NamedChild(i), returned); err != nil {
				return err
			}
		}
		return nil
	}
	err := visit(body, body.Kind() != "statement_block")
	return tags, renders, err
}

func mapCallback(node *sitter.Node, contents []byte) bool {
	args := node.Parent()
	if args == nil || args.Kind() != "arguments" {
		return false
	}
	call := args.Parent()
	if call == nil || call.Kind() != "call_expression" {
		return false
	}
	method := call.ChildByFieldName("function")
	if method == nil || method.Kind() != "member_expression" {
		return false
	}
	property := method.ChildByFieldName("property")
	return property != nil && property.Utf8Text(contents) == "map"
}

// Retain shadowed render sites without resolving them to a module binding.
func locallyBound(tag, body *sitter.Node, name string, contents []byte) bool {
	for scope := tag.Parent(); scope != nil; scope = scope.Parent() {
		if scope.Kind() == "statement_block" {
			for i := uint(0); i < scope.NamedChildCount(); i++ {
				statement := scope.NamedChild(i)
				switch statement.Kind() {
				case "lexical_declaration", "variable_declaration":
					for j := uint(0); j < statement.NamedChildCount(); j++ {
						decl := statement.NamedChild(j)
						if decl.Kind() == "variable_declarator" && bindsName(decl.ChildByFieldName("name"), name, contents) {
							return true
						}
					}
				case "function_declaration", "class_declaration":
					if bindsName(statement.ChildByFieldName("name"), name, contents) {
						return true
					}
				}
			}
		}
		for _, field := range []string{"parameter", "parameters"} {
			if bindsName(scope.ChildByFieldName(field), name, contents) {
				return true
			}
		}
		if scope.Id() == body.Id() {
			function := body.Parent()
			return function != nil && (bindsName(function.ChildByFieldName("parameters"), name, contents) ||
				bindsName(function.ChildByFieldName("parameter"), name, contents))
		}
	}
	return false
}

func bindsName(node *sitter.Node, name string, contents []byte) bool {
	if node == nil {
		return false
	}
	switch node.Kind() {
	case "identifier", "shorthand_property_identifier_pattern":
		return node.Utf8Text(contents) == name
	case "required_parameter", "optional_parameter":
		return bindsName(node.ChildByFieldName("pattern"), name, contents)
	case "assignment_pattern", "object_assignment_pattern":
		return bindsName(node.ChildByFieldName("left"), name, contents)
	case "pair_pattern":
		return bindsName(node.ChildByFieldName("value"), name, contents)
	case "formal_parameters", "object_pattern", "array_pattern", "rest_pattern":
		for i := uint(0); i < node.NamedChildCount(); i++ {
			if bindsName(node.NamedChild(i), name, contents) {
				return true
			}
		}
	}
	return false
}
