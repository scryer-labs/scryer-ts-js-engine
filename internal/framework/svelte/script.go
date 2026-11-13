package svelte

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	sitter "github.com/tree-sitter/go-tree-sitter"
	html "github.com/tree-sitter/tree-sitter-html/bindings/go"

	"github.com/scryer-labs/scryer-ts-js-engine/internal/source"
)

type Tag struct {
	source.Location
	Name string `json:"tag"`
}

func Scan(ctx context.Context, file string, contents []byte) ([]source.Fragment, []Tag, error) {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(html.Language())); err != nil {
		return nil, nil, err
	}
	tree := parser.ParseWithOptions(func(offset int, _ sitter.Point) []byte {
		return contents[offset:]
	}, nil, &sitter.ParseOptions{ProgressCallback: func(sitter.ParseState) bool { return ctx.Err() != nil }})
	if tree != nil {
		defer tree.Close()
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	var scripts []source.Fragment
	if tree == nil {
		return scripts, nil, nil
	}
	root := tree.RootNode()
	for i := uint(0); i < root.NamedChildCount(); i++ {
		element := root.NamedChild(i)
		if element.Kind() != "script_element" {
			continue
		}
		tag := element.NamedChild(0)
		lang := "JavaScript"
		if tag != nil {
			for j := uint(0); j < tag.NamedChildCount(); j++ {
				attr := tag.NamedChild(j)
				if attr.Kind() != "attribute" || attr.NamedChildCount() != 2 || attr.NamedChild(0).Utf8Text(contents) != "lang" {
					continue
				}
				switch strings.Trim(attr.NamedChild(1).Utf8Text(contents), "\"'") {
				case "ts", "typescript":
					lang = "TypeScript"
				case "js", "javascript":
					lang = "JavaScript"
				default:
					lang = ""
				}
			}
		}
		for j := uint(0); lang != "" && j < element.NamedChildCount(); j++ {
			child := element.NamedChild(j)
			if child.Kind() == "raw_text" {
				scripts = append(scripts, source.Fragment{File: file, Language: lang, Bytes: contents[child.StartByte():child.EndByte()], Start: child.StartPosition()})
			}
		}
	}
	var tags []Tag
	part := source.Fragment{File: file, Bytes: contents}
	var walk func(*sitter.Node) error
	walk = func(node *sitter.Node) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch node.Kind() {
		case "script_element", "style_element", "comment":
			return nil
		case "start_tag", "self_closing_tag":
			tag := node.NamedChild(0)
			if tag != nil && tag.Kind() == "tag_name" && !node.HasError() {
				name := tag.Utf8Text(contents)
				initial, _ := utf8.DecodeRuneInString(name)
				if unicode.IsUpper(initial) && !strings.ContainsAny(name, ".:-") {
					tags = append(tags, Tag{part.Location(tag), name})
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
	err := walk(root)
	return scripts, tags, err
}
