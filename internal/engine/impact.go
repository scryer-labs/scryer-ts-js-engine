package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/scryer-labs/scryer-ts-js-engine/internal/analysis"
	"github.com/scryer-labs/scryer-ts-js-engine/internal/script"
	"github.com/scryer-labs/scryer-ts-js-engine/internal/semantic"
)

func collectImpact(ctx context.Context, root string, resolve bool) (analysis.ImpactSnapshot, error) {
	result := analysis.ImpactSnapshot{Symbols: []analysis.ImpactSymbol{}, Calls: []analysis.ImpactCall{}, Contexts: []analysis.ImpactContext{}, Notes: []string{
		"TS/JS impact reuses scan's script/Svelte collection and optional TypeScript/Svelte compiler enrichment. Dynamic dispatch, generated wrappers and unmapped template expressions remain boundaries.",
		"Function token fingerprints exclude comments. Full-file source-context fingerprints retain templates/styles and can report formatting outside function definitions as unresolved context.",
		"Anonymous callback identities use lexical owner and registration/attribute structure, not line offsets or execution order. Repeated indistinguishable identities remain ambiguous. Passing or containing a callback does not prove invocation.",
	}}
	if !filepath.IsAbs(root) {
		return result, fmt.Errorf("impact requires absolute snapshot roots")
	}
	facts, err := scanProject(ctx, root, []string{"call-graph", "files"}, resolve)
	if err != nil {
		return result, err
	}
	graph := facts["call-graph"].(*script.Graph)
	if resolve {
		resolution, ok := graph.Semantic.(semantic.Report)
		if !ok || (resolution.Status != "resolved" && !(resolution.Status == "partial" && resolution.UsableForImpact)) {
			return result, fmt.Errorf("dynamic TS/JS scan did not resolve (%s; %d diagnostics): %s %s", resolution.Status, resolution.Diagnostics, resolution.Message, strings.Join(resolution.DiagnosticMessages, "\n"))
		}
		result.Resolution, _ = json.Marshal(resolution)
		if resolution.Status == "partial" {
			result.Notes = append(result.Notes, "Compiler diagnostics make this graph partial. Calls overlapping diagnostics have no semantic target; other compiler observations remain available without static fallback.")
		}
	}
	registered := map[string]*script.TestEvidence{}
	for _, call := range graph.Calls {
		if call.TestEvidence != nil && call.TestEvidence.Kind == "test-registration" {
			registered[call.ID] = call.TestEvidence
		}
	}
	for _, symbol := range graph.Symbols {
		if symbol.ImpactKey == "" {
			continue
		}
		role := "UNKNOWN"
		var testContext json.RawMessage
		if symbol.ImpactRegistration != "" {
			evidence := registered[symbol.ImpactRegistration]
			if evidence != nil {
				role = "TEST"
				testContext, _ = json.Marshal(map[string]any{"scope": "test-only", "evidence": []any{evidence}})
			}
		}
		raw, _ := json.Marshal(symbol.Definition)
		var location analysis.ImpactRange
		_ = json.Unmarshal(raw, &location)
		result.Symbols = append(result.Symbols, analysis.ImpactSymbol{ID: symbol.ID, Key: symbol.ImpactKey, Signature: symbol.ImpactSignature, Fingerprint: symbol.ImpactFingerprint, Source: symbol.ImpactSource, Location: location, Role: role, TestContext: testContext, ExecutionKind: symbol.ExecutionKind})
	}
	names := make([]string, 0, len(graph.Contexts))
	for file := range graph.Contexts {
		names = append(names, file)
	}
	sort.Strings(names)
	for _, file := range names {
		result.Contexts = append(result.Contexts, analysis.ImpactContext{Path: file, Fingerprint: graph.Contexts[file]})
	}
	for _, call := range graph.Calls {
		raw, _ := json.Marshal(call.Site)
		var site analysis.ImpactRange
		_ = json.Unmarshal(raw, &site)
		edge := analysis.ImpactCall{ID: call.ID, Caller: call.Caller, Callee: call.Callee, Site: site, Resolution: call.Resolution}
		if resolve {
			edge.Target = call.SemanticTarget
			edge.Kind = "scan-resolved"
			if edge.Target == "" {
				edge.Resolution = "unresolved-semantic-target"
			}
		} else {
			edge.Candidate = call.Target
		}
		result.Calls = append(result.Calls, edge)
	}
	for _, reference := range graph.References {
		raw, _ := json.Marshal(reference.Site)
		var site analysis.ImpactRange
		_ = json.Unmarshal(raw, &site)
		result.Calls = append(result.Calls, analysis.ImpactCall{
			ID: reference.ID, Caller: reference.From, Candidate: reference.To,
			Callee: reference.Name, Resolution: reference.Resolution,
			Kind: "framework-reference", CandidateBasis: reference.Basis, Site: site,
		})
	}

	return result, nil
}
