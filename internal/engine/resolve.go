package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/scryer-labs/scryer-ts-js-engine/internal/framework/tauri"
	"github.com/scryer-labs/scryer-ts-js-engine/internal/source"

	"github.com/scryer-labs/scryer-ts-js-engine/internal/script"
	"github.com/scryer-labs/scryer-ts-js-engine/internal/semantic"
)

func enrichSemantic(ctx context.Context, root string, files []fileFact, facts map[string]any, framework *tauri.Collector) semantic.Report {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	result := semantic.Resolve(ctx, root, paths)
	calls, imports := uniqueTargets(result.Calls), uniqueTargets(result.Imports)
	observations := result.Calls
	if framework != nil {
		for _, endpoint := range result.Endpoints {
			framework.Add(tauri.Endpoint{
				Location: source.Location{File: endpoint.File, Line: endpoint.Line, Column: endpoint.Column},
				Kind:     endpoint.Kind, Role: endpoint.Role, Name: endpoint.Name,
			})
		}
	}
	result.Calls, result.Imports, result.Endpoints = nil, nil, nil
	if graph, ok := facts["call-graph"].(*script.Graph); ok {
		graph.Semantic = result
		symbols := map[semantic.Position]string{}
		for _, symbol := range graph.Symbols {
			symbols[position(symbol.Definition)] = symbol.ID
		}
		existing := map[semantic.Position]bool{}
		for i := range graph.Calls {
			call := &graph.Calls[i]
			existing[position(call.Site)] = true
			if target := symbols[calls[position(call.Site)]]; target != "" {
				call.SemanticTarget = target
			}
		}
		for _, edge := range observations {
			if !strings.HasSuffix(edge.Site.File, ".svelte") || existing[edge.Site] || calls[edge.Site] != edge.Target {
				continue
			}
			if target := symbols[edge.Target]; target != "" {
				point := script.Position{Line: edge.Site.Line, Column: edge.Site.Column}
				end := point
				if edge.End != nil && edge.End.File == edge.Site.File && edge.End.Line == edge.Site.Line && edge.End.Column >= edge.Site.Column {
					end.Column = edge.End.Column + 1
				}
				graph.Calls = append(graph.Calls, script.Call{
					ID:     fmt.Sprintf("%s:%d:%d:semantic", edge.Site.File, edge.Site.Line, edge.Site.Column),
					Callee: edge.Callee, SemanticTarget: target, Resolution: "resolved", Target: target,
					Site: script.Range{File: edge.Site.File, Start: point, End: end},
				})
				existing[edge.Site] = true
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
	return result
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
