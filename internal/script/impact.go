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
func impactIdentity(part source.Fragment, node *sitter.Node, name string) (string, string, string, string, string) {
	if node.HasError() {
		return "", "", "", "", ""
	}
	registration := ""
	signature := name
	if signature == "" && node.Parent() != nil && node.Parent().Kind() == "arguments" {
		call := node.Parent().Parent()
		if call != nil && call.Kind() == "call_expression" && node.Parent().NamedChildCount() > 0 {
			label := node.Parent().NamedChild(0)
			if label.Kind() == "string" {
				signature = "test callback " + label.Utf8Text(part.Bytes)
				registration = fmt.Sprintf("%s-%d:%d", symbolID(part, call), span(part, call).End.Line, span(part, call).End.Column)
			}
		}
	}
	if signature == "" {
		return "", "", "", "", ""
	}
	scope := []string{}
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		switch parent.Kind() {
		case "class_declaration", "class", "function_declaration", "method_definition":
			if n := parent.ChildByFieldName("name"); n != nil {
				scope = append([]string{n.Utf8Text(part.Bytes)}, scope...)
			}
		}
	}
	signature = strings.Join(append(scope, signature), "::")
	key := part.File + "|" + signature
	if parameters := node.ChildByFieldName("parameters"); parameters != nil {
		key += "|" + tokens(parameters, part.Bytes)
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
