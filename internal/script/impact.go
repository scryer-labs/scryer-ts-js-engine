package script

import (
	"fmt"
	"sort"
	"strings"

	"github.com/scryer-labs/scryer-ts-js-engine/internal/analysis"
	"github.com/scryer-labs/scryer-ts-js-engine/internal/source"
	sitter "github.com/tree-sitter/go-tree-sitter"
)

func tokens(node *sitter.Node, data []byte) string {
	if node == nil || node.Kind() == "comment" {
		return ""
	}
	if node.ChildCount() == 0 {
		text := node.Utf8Text(data)
		return fmt.Sprintf("%d:%s;", len(text), text)
	}
	var value strings.Builder
	for i := uint(0); i < node.ChildCount(); i++ {
		value.WriteString(tokens(node.Child(i), data))
	}
	return value.String()
}

// callableAnchor describes source structure, never the implementation body or
// line offset. Repeated indistinguishable anchors deliberately share a key:
// Core reports that correspondence as ambiguous rather than guessing by order.
func callableAnchor(part source.Fragment, node *sitter.Node) string {
	if name := node.ChildByFieldName("name"); name != nil {
		return name.Utf8Text(part.Bytes)
	}
	parent := node.Parent()
	if parent == nil {
		return "anonymous function"
	}
	if parent.Kind() == "variable_declarator" {
		if name := parent.ChildByFieldName("name"); name != nil {
			return name.Utf8Text(part.Bytes)
		}
	}
	if parent.Kind() == "arguments" {
		if call := parent.Parent(); call != nil && call.Kind() == "call_expression" {
			callee := call.ChildByFieldName("function")
			if callee != nil {
				argument := uint(0)
				for i := uint(0); i < parent.NamedChildCount(); i++ {
					child := parent.NamedChild(i)
					if child.Kind() == "comment" {
						continue
					}
					if child.Id() == node.Id() {
						break
					}
					argument++
				}
				anchor := fmt.Sprintf("callback %s argument %d", tokens(callee, part.Bytes), argument)
				// A literal registration label separates named tests, but does not
				// classify them. Only resolved imported registration evidence does.
				for i := uint(0); i < parent.NamedChildCount(); i++ {
					label := parent.NamedChild(i)
					if label.Kind() == "comment" {
						continue
					}
					if label.Kind() == "string" {
						anchor += " label " + label.Utf8Text(part.Bytes)
					}
					break
				}
				return anchor
			}
		}
	}
	for ancestor := parent; ancestor != nil && !isFunction(ancestor); ancestor = ancestor.Parent() {
		switch ancestor.Kind() {
		case "jsx_attribute":
			if name := ancestor.NamedChild(0); name != nil {
				return "callback JSX attribute " + name.Utf8Text(part.Bytes)
			}
		case "pair":
			if key := ancestor.ChildByFieldName("key"); key != nil {
				return "callback property " + tokens(key, part.Bytes)
			}
		}
	}
	return "anonymous function in " + parent.Kind()
}

func impactIdentity(part source.Fragment, node *sitter.Node, _ string) (string, string, string, string, string) {
	if node.HasError() {
		return "", "", "", "", ""
	}
	registration := ""
	if parent := node.Parent(); parent != nil && parent.Kind() == "arguments" {
		if call := parent.Parent(); call != nil && call.Kind() == "call_expression" {
			registration = fmt.Sprintf("%s-%d:%d", symbolID(part, call), span(part, call).End.Line, span(part, call).End.Column)
		}
	}
	scope := []string{callableAnchor(part, node)}
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		if isFunction(parent) {
			scope = append([]string{callableAnchor(part, parent)}, scope...)
		} else if parent.Kind() == "class_declaration" || parent.Kind() == "class" {
			if name := parent.ChildByFieldName("name"); name != nil {
				scope = append([]string{name.Utf8Text(part.Bytes)}, scope...)
			}
		}
	}
	signature := strings.Join(scope, "::")
	key := part.File + "|" + signature
	for _, field := range []string{"parameter", "parameters"} {
		if parameters := node.ChildByFieldName(field); parameters != nil {
			key += "|" + tokens(parameters, part.Bytes)
		}
	}
	return key, signature, analysis.Hash([]byte(tokens(node, part.Bytes))), node.Utf8Text(part.Bytes), registration
}

// FileContext retains the full original file outside collected function spans,
// including Svelte templates/styles. Formatting outside functions is conservative
// unresolved context, rather than silently omitted non-script changes.
func (g *Graph) FileContext(file string, data []byte) {
	starts := []int{0}
	for i, b := range data {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	offset := func(p Position) int {
		if p.Line == 0 || int(p.Line) > len(starts) {
			return len(data) + 1
		}
		return starts[p.Line-1] + int(p.Column) - 1
	}

	type interval struct{ start, end int }
	intervals := []interval{}
	for _, definition := range g.ranges[file] {
		a, b := offset(definition.Start), offset(definition.End)
		if a >= 0 && a <= b && b <= len(data) {
			intervals = append(intervals, interval{a, b})
		}
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].start < intervals[j].start })
	var context strings.Builder
	end := 0
	for _, span := range intervals {
		if span.start < end {
			if span.end > end {
				end = span.end
			}
			continue
		}
		context.Write(data[end:span.start])
		context.WriteString("\x00function\x00")
		end = span.end
	}
	context.Write(data[end:])

	if g.Contexts == nil {
		g.Contexts = map[string]string{}
	}
	g.Contexts[file] = analysis.Hash([]byte(context.String()))
}
