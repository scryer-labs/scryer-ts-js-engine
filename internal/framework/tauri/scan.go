package tauri

import (
	"context"
	"io/fs"
	"path"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/scryer-labs/scryer-ts-js-engine/internal/source"
)

type Endpoint struct {
	source.Location
	Kind string `json:"kind"`
	Role string `json:"role"`
	Name string `json:"name"`
}

type App struct {
	Config    string     `json:"config"`
	Endpoints []Endpoint `json:"endpoints"`
}

type Collector struct {
	fs      fs.FS
	apps    []App
	configs map[string]string
	index   map[string]int
}

func New(fsys fs.FS) *Collector {
	return &Collector{fs: fsys, apps: []App{}, configs: map[string]string{}, index: map[string]int{}}
}

func (c *Collector) Facts() map[string]any {
	return map[string]any{"basis": "static-source", "tauri": map[string]any{"apps": c.apps}}
}

type binding struct {
	kind string
	role string
}

func (c *Collector) Observe(ctx context.Context, fragment source.Fragment, root *sitter.Node) error {
	config := c.config(path.Dir(fragment.File))
	if config == "" {
		return nil
	}
	named, namespaces := bindings(fragment, root)
	if len(named)+len(namespaces) == 0 {
		return nil
	}
	return visit(ctx, root, func(node *sitter.Node) {
		if node.Kind() != "call_expression" || node.HasError() {
			return
		}
		fn, args := node.ChildByFieldName("function"), node.ChildByFieldName("arguments")
		if fn == nil || args == nil {
			return
		}
		// The TypeScript grammar can wrap a generic awaited callee in await_expression.
		if fn.Kind() == "await_expression" {
			fn = firstArgument(fn)
			if fn == nil {
				return
			}
		}
		var match binding
		switch fn.Kind() {
		case "identifier":
			match = named[fn.Utf8Text(fragment.Bytes)]
			if shadowed(fn, fn.Utf8Text(fragment.Bytes), fragment.Bytes) {
				return
			}
		case "member_expression":
			obj, prop := fn.ChildByFieldName("object"), fn.ChildByFieldName("property")
			if obj == nil || obj.Kind() != "identifier" || prop == nil {
				return
			}
			name := obj.Utf8Text(fragment.Bytes)
			match = namespaces[name][prop.Utf8Text(fragment.Bytes)]
			if shadowed(fn, name, fragment.Bytes) {
				return
			}
		}
		if match.kind == "" {
			return
		}
		first := firstArgument(args)
		event, ok := source.String(first, fragment.Bytes)
		if !ok || event == "" {
			return
		}
		i, exists := c.index[config]
		if !exists {
			i = len(c.apps)
			c.index[config] = i
			c.apps = append(c.apps, App{Config: config})
		}
		c.apps[i].Endpoints = append(c.apps[i].Endpoints, Endpoint{fragment.Location(node), match.kind, match.role, event})
	})
}

func bindings(fragment source.Fragment, root *sitter.Node) (map[string]binding, map[string]map[string]binding) {
	named := map[string]binding{}
	namespaces := map[string]map[string]binding{}
	for i := uint(0); i < root.NamedChildCount(); i++ {
		stmt := root.NamedChild(i)
		if stmt.Kind() != "import_statement" || stmt.HasError() {
			continue
		}
		specifier, _ := source.String(stmt.ChildByFieldName("source"), fragment.Bytes)
		var exports map[string]binding
		switch specifier {
		case "@tauri-apps/api/core":
			exports = map[string]binding{"invoke": {"command", "invoke"}}
		case "@tauri-apps/api/event":
			exports = map[string]binding{"listen": {"event", "listen"}, "emit": {"event", "emit"}}
		default:
			continue
		}
		clause := stmt.NamedChild(0)
		if clause == nil || clause.Kind() != "import_clause" || hasToken(stmt, "type") {
			continue
		}
		for j := uint(0); j < clause.NamedChildCount(); j++ {
			item := clause.NamedChild(j)
			switch item.Kind() {
			case "namespace_import":
				if id := item.NamedChild(0); id != nil && id.Kind() == "identifier" {
					namespaces[id.Utf8Text(fragment.Bytes)] = exports
				}
			case "named_imports":
				for k := uint(0); k < item.NamedChildCount(); k++ {
					spec := item.NamedChild(k)
					if spec.Kind() != "import_specifier" || hasToken(spec, "type") {
						continue
					}
					id := spec.ChildByFieldName("name")
					if id == nil {
						continue
					}
					match, ok := exports[id.Utf8Text(fragment.Bytes)]
					if !ok {
						continue
					}
					if alias := spec.ChildByFieldName("alias"); alias != nil {
						id = alias
					}
					named[id.Utf8Text(fragment.Bytes)] = match
				}
			}
		}
	}
	return named, namespaces
}

func hasToken(node *sitter.Node, kind string) bool {
	for i := uint(0); i < node.ChildCount(); i++ {
		if node.Child(i).Kind() == kind {
			return true
		}
	}
	return false
}

func firstArgument(args *sitter.Node) *sitter.Node {
	for i := uint(0); i < args.NamedChildCount(); i++ {
		if arg := args.NamedChild(i); arg.Kind() != "comment" {
			return arg
		}
	}
	return nil
}

func visit(ctx context.Context, node *sitter.Node, apply func(*sitter.Node)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	apply(node)
	for i := uint(0); i < node.NamedChildCount(); i++ {
		if err := visit(ctx, node.NamedChild(i), apply); err != nil {
			return err
		}
	}
	return nil
}

func (c *Collector) config(dir string) string {
	if config, exists := c.configs[dir]; exists {
		return config
	}
	var matches []string
	for _, base := range []string{dir, path.Join(dir, "src-tauri")} {
		for _, name := range []string{"tauri.conf.json", "tauri.conf.json5", "Tauri.toml"} {
			candidate := path.Join(base, name)
			if info, err := fs.Stat(c.fs, candidate); err == nil && info.Mode().IsRegular() {
				matches = append(matches, candidate)
			}
		}
	}
	config := ""
	if len(matches) == 1 {
		config = matches[0]
	} else if len(matches) == 0 && dir != "." {
		config = c.config(path.Dir(dir))
	}
	c.configs[dir] = config
	return config
}

// Bound common lexical shadowing. Unsupported bindings are not semantically resolved.
func shadowed(node *sitter.Node, name string, contents []byte) bool {
	for scope := node.Parent(); scope != nil && scope.Kind() != "program"; scope = scope.Parent() {
		for _, field := range []string{"parameters", "parameter"} {
			if params := scope.ChildByFieldName(field); params != nil && binds(params, name, contents) {
				return true
			}
		}
		if scope.Kind() != "statement_block" {
			continue
		}
		for i := uint(0); i < scope.NamedChildCount(); i++ {
			child := scope.NamedChild(i)
			switch child.Kind() {
			case "lexical_declaration", "variable_declaration":
				for j := uint(0); j < child.NamedChildCount(); j++ {
					decl := child.NamedChild(j)
					if id := decl.ChildByFieldName("name"); decl.Kind() == "variable_declarator" && id != nil && binds(id, name, contents) {
						return true
					}
				}
			case "function_declaration", "class_declaration":
				if id := child.ChildByFieldName("name"); id != nil && id.Utf8Text(contents) == name {
					return true
				}
			}
		}
	}
	return false
}

func binds(node *sitter.Node, name string, contents []byte) bool {
	switch node.Kind() {
	case "identifier", "shorthand_property_identifier_pattern":
		return node.Utf8Text(contents) == name
	case "required_parameter", "optional_parameter":
		if pattern := node.ChildByFieldName("pattern"); pattern != nil {
			return binds(pattern, name, contents)
		}
	case "pair_pattern":
		if value := node.ChildByFieldName("value"); value != nil {
			return binds(value, name, contents)
		}
	case "assignment_pattern":
		if left := node.ChildByFieldName("left"); left != nil {
			return binds(left, name, contents)
		}
	case "formal_parameters", "object_pattern", "array_pattern", "rest_pattern":
		for i := uint(0); i < node.NamedChildCount(); i++ {
			if binds(node.NamedChild(i), name, contents) {
				return true
			}
		}
	}
	return false
}
