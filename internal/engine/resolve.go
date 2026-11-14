package engine

import (
	"context"
	"slices"

	"github.com/scryer-labs/scryer-ts-js-engine/internal/script"
	"github.com/scryer-labs/scryer-ts-js-engine/internal/semantic"
)

func enrichSemantic(ctx context.Context, root string, files []fileFact, facts map[string]any) {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	result := semantic.Resolve(ctx, root, paths)
	calls, imports := uniqueTargets(result.Calls), uniqueTargets(result.Imports)
	result.Calls, result.Imports = nil, nil
	if graph, ok := facts["call-graph"].(*script.Graph); ok {
		graph.Semantic = result
		symbols := map[semantic.Position]string{}
		for _, symbol := range graph.Symbols {
			symbols[position(symbol.Definition)] = symbol.ID
		}
		for i := range graph.Calls {
			call := &graph.Calls[i]
			if target := symbols[calls[position(call.Site)]]; target != "" {
				call.SemanticTarget = target
			}
		}
	}
	if fileFacts, ok := facts["files"].(map[string]any); ok {
		fileFacts["semantic"] = result
		edges := fileFacts["imports"].([]script.Import)
		for i := range edges {
			edge := &edges[i]
			key := semantic.Position{File: edge.File, Line: edge.Line, Column: edge.Column}
			edge.SemanticTarget = imports[key].File
		}
	}
}

func position(r script.Range) semantic.Position {
	return semantic.Position{File: r.File, Line: r.Start.Line, Column: r.Start.Column}
}

func uniqueTargets(edges []semantic.Edge) map[semantic.Position]semantic.Position {
	candidates := map[semantic.Position][]semantic.Position{}
	for _, edge := range edges {
		if !slices.Contains(candidates[edge.Site], edge.Target) {
			candidates[edge.Site] = append(candidates[edge.Site], edge.Target)
		}
	}
	targets := map[semantic.Position]semantic.Position{}
	for site, options := range candidates {
		if len(options) == 1 {
			targets[site] = options[0]
		}
	}
	return targets
}
