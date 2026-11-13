package source

import (
	"bytes"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

type Location struct {
	File   string `json:"file"`
	Line   uint   `json:"line"`
	Column uint   `json:"column"`
}

type Fragment struct {
	File     string
	Language string
	Bytes    []byte
	Start    sitter.Point
}

func (f Fragment) Location(node *sitter.Node) Location {
	position := node.StartPosition()
	column := position.Column + 1
	if position.Row == 0 {
		column += f.Start.Column
	}
	return Location{File: f.File, Line: f.Start.Row + position.Row + 1, Column: column}
}

// String returns a plain, unescaped JS string value.
func String(node *sitter.Node, contents []byte) (string, bool) {
	if node == nil || node.Kind() != "string" || node.HasError() {
		return "", false
	}
	raw := contents[node.StartByte():node.EndByte()]
	if len(raw) < 2 || (raw[0] != '\'' && raw[0] != '"') ||
		raw[len(raw)-1] != raw[0] || bytes.IndexByte(raw[1:len(raw)-1], '\\') >= 0 {
		return "", false
	}
	return string(raw[1 : len(raw)-1]), true
}
