package script

import (
	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/scryer-labs/scryer-ts-js-engine/internal/source"
)

func hasToken(node *sitter.Node, token string) bool {
	for i := uint(0); i < node.ChildCount(); i++ {
		if node.Child(i).Kind() == token {
			return true
		}
	}
	return false
}

func bindPattern(part source.Fragment, node *sitter.Node, scope map[string][]*binding) {
	if node == nil {
		return
	}
	switch node.Kind() {
	case "identifier", "shorthand_property_identifier_pattern":
		name := node.Utf8Text(part.Bytes)
		scope[name] = append(scope[name], nil)
	case "required_parameter", "optional_parameter":
		bindPattern(part, node.ChildByFieldName("pattern"), scope)
	case "assignment_pattern", "object_assignment_pattern":
		bindPattern(part, node.ChildByFieldName("left"), scope)
	case "pair_pattern":
		bindPattern(part, node.ChildByFieldName("value"), scope)
	case "formal_parameters", "array_pattern", "object_pattern", "rest_pattern":
		for i := uint(0); i < node.NamedChildCount(); i++ {
			bindPattern(part, node.NamedChild(i), scope)
		}
	}
}

func localBindings(part source.Fragment, node *sitter.Node) map[string][]*binding {
	scope := map[string][]*binding{}
	for i := uint(0); i < node.NamedChildCount(); i++ {
		item := node.NamedChild(i)
		if item.Kind() == "import_statement" {
			addImport(part, item, scope)
			continue
		}
		if item.Kind() == "export_statement" {
			item = item.ChildByFieldName("declaration")
		}
		if item == nil {
			continue
		}
		switch item.Kind() {
		case "function_declaration", "generator_function_declaration":
			if id := item.ChildByFieldName("name"); id != nil && !item.HasError() {
				name := id.Utf8Text(part.Bytes)
				scope[name] = append(scope[name], &binding{symbol: symbolID(part, item)})
			}
		case "class_declaration", "enum_declaration":
			bindPattern(part, item.ChildByFieldName("name"), scope)
		case "lexical_declaration", "variable_declaration":
			for j := uint(0); j < item.NamedChildCount(); j++ {
				decl := item.NamedChild(j)
				if decl.Kind() != "variable_declarator" {
					continue
				}
				id, value := decl.ChildByFieldName("name"), decl.ChildByFieldName("value")
				if id != nil && id.Kind() == "identifier" && value != nil && isFunction(value) && !value.HasError() && hasToken(item, "const") {
					name := id.Utf8Text(part.Bytes)
					scope[name] = append(scope[name], &binding{symbol: symbolID(part, value)})
				} else {
					bindPattern(part, id, scope)
				}
			}
		}
	}
	return scope
}

func blockHoistedVars(part source.Fragment, node *sitter.Node, scope map[string][]*binding) {
	if node == nil || isFunction(node) {
		return
	}
	if node.Kind() == "variable_declaration" && hasToken(node, "var") {
		for i := uint(0); i < node.NamedChildCount(); i++ {
			bindPattern(part, node.NamedChild(i).ChildByFieldName("name"), scope)
		}
		return
	}
	for i := uint(0); i < node.NamedChildCount(); i++ {
		blockHoistedVars(part, node.NamedChild(i), scope)
	}
}

func addImport(part source.Fragment, item *sitter.Node, scope map[string][]*binding) {
	module, ok := source.String(item.ChildByFieldName("source"), part.Bytes)
	if !ok || hasToken(item, "type") || item.HasError() {
		return
	}
	clause := item.NamedChild(0)
	if clause == nil || clause.Kind() != "import_clause" {
		return
	}
	for i := uint(0); i < clause.NamedChildCount(); i++ {
		child := clause.NamedChild(i)
		switch child.Kind() {
		case "identifier":
			name := child.Utf8Text(part.Bytes)
			scope[name] = append(scope[name], &binding{module: module, exported: "default"})
		case "namespace_import":
			bindPattern(part, child.NamedChild(0), scope)
		case "named_imports":
			for j := uint(0); j < child.NamedChildCount(); j++ {
				spec := child.NamedChild(j)
				name := spec.ChildByFieldName("name")
				if name == nil || name.Kind() != "identifier" || hasToken(spec, "type") {
					continue
				}
				local := name
				if alias := spec.ChildByFieldName("alias"); alias != nil {
					local = alias
				}
				key := local.Utf8Text(part.Bytes)
				scope[key] = append(scope[key], &binding{module: module, exported: name.Utf8Text(part.Bytes)})
			}
		}
	}
}

func addExports(part source.Fragment, node *sitter.Node, scope, exports map[string][]*binding) {
	add := func(name *sitter.Node, exported string) {
		if name == nil || name.Kind() != "identifier" {
			return
		}
		local := name.Utf8Text(part.Bytes)
		if exported == "" {
			exported = local
		}
		exports[exported] = append(exports[exported], scope[local]...)
	}
	for i := uint(0); i < node.NamedChildCount(); i++ {
		item := node.NamedChild(i)
		if item.Kind() != "export_statement" || item.ChildByFieldName("source") != nil {
			continue
		}
		if hasToken(item, "default") {
			name := item.ChildByFieldName("value")
			if decl := item.ChildByFieldName("declaration"); decl != nil {
				name = decl.ChildByFieldName("name")
				if name == nil && isFunction(decl) {
					exports["default"] = append(exports["default"], &binding{symbol: symbolID(part, decl)})
				}
			}
			if name != nil && isFunction(name) {
				exports["default"] = append(exports["default"], &binding{symbol: symbolID(part, name)})
			}
			add(name, "default")
			continue
		}
		if decl := item.ChildByFieldName("declaration"); decl != nil {
			add(decl.ChildByFieldName("name"), "")
			if decl.Kind() == "lexical_declaration" {
				for j := uint(0); j < decl.NamedChildCount(); j++ {
					add(decl.NamedChild(j).ChildByFieldName("name"), "")
				}
			}
		}
		for j := uint(0); j < item.NamedChildCount(); j++ {
			clause := item.NamedChild(j)
			if clause.Kind() != "export_clause" {
				continue
			}
			for k := uint(0); k < clause.NamedChildCount(); k++ {
				spec := clause.NamedChild(k)
				if hasToken(spec, "type") {
					continue
				}
				exported := ""
				if alias := spec.ChildByFieldName("alias"); alias != nil {
					exported = alias.Utf8Text(part.Bytes)
				}
				add(spec.ChildByFieldName("name"), exported)
			}
		}
	}
}
