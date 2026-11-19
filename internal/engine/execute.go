package engine

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/scryer-labs/scryer-ts-js-engine/internal/analysis"
	"os"
	"path/filepath"
)

const (
	MaxRequestBytes   = 1 << 20
	MaxRequestIDBytes = 256
)

type Request struct {
	ProtocolVersion int                      `json:"protocolVersion"`
	ID              string                   `json:"requestId"`
	Stack           string                   `json:"stack"`
	Operation       string                   `json:"operation"`
	Coverage        analysis.CoverageRequest `json:"coverage"`
	Impact          struct {
		Repository, Before, After, BeforeRoot, AfterRoot string
		Resolve                                          bool
	} `json:"impact"`
	Scan struct {
		Repository string   `json:"repository"`
		Static     *bool    `json:"static"`
		Facts      []string `json:"facts"`
	} `json:"scan"`
}

func ParseRequest(data []byte) (Request, error) {
	var request Request
	if err := json.Unmarshal(data, &request); err != nil || request.ID == "" || len(request.ID) > MaxRequestIDBytes {
		return Request{}, errors.New("invalid request JSON or requestId")
	}
	return request, nil
}

type protocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type report struct {
	SchemaVersion int            `json:"schemaVersion"`
	Root          string         `json:"root"`
	Facts         map[string]any `json:"facts"`
}

func (r Request) Execute(ctx context.Context) ([]byte, error) {
	var result any
	var failure *protocolError
	if r.ProtocolVersion != 1 || r.Stack != "ts-js" {
		failure = &protocolError{"INVALID_REQUEST", "unsupported protocol version or stack"}
	} else {
		switch r.Operation {
		case "scan":
			result, failure = r.scan(ctx)
		case "coverage":
			value, err := collectCoverage(ctx, r.Coverage)
			if err != nil {
				failure = &protocolError{"TS_JS_COVERAGE_ERROR", err.Error()}
			} else {
				result = value
			}
		case "impact":
			dependencyNotes := map[string][]string{}
			if !filepath.IsAbs(r.Impact.BeforeRoot) || !filepath.IsAbs(r.Impact.AfterRoot) {
				failure = &protocolError{"INVALID_REQUEST", "impact requires absolute snapshot roots"}
				break
			}
			if r.Impact.Resolve {
				for _, root := range []string{r.Impact.BeforeRoot, r.Impact.AfterRoot} {
					notes, err := prepareSnapshotDependencies(ctx, root, r.Impact.Repository)
					dependencyNotes[root] = notes
					if err != nil {
						failure = &protocolError{"TS_JS_IMPACT_ERROR", err.Error()}
						break
					}
				}
				if failure != nil {
					break
				}
			}
			before, err := collectImpact(ctx, r.Impact.BeforeRoot, r.Impact.Resolve)
			if err != nil {
				failure = &protocolError{"TS_JS_IMPACT_ERROR", "before: " + err.Error()}
				break
			}
			after, err := collectImpact(ctx, r.Impact.AfterRoot, r.Impact.Resolve)
			if err != nil {
				failure = &protocolError{"TS_JS_IMPACT_ERROR", "after: " + err.Error()}
				break
			}
			before.Notes = append(before.Notes, dependencyNotes[r.Impact.BeforeRoot]...)
			after.Notes = append(after.Notes, dependencyNotes[r.Impact.AfterRoot]...)
			result = analysis.ImpactFacts{SchemaVersion: 1, BeforeSHA: r.Impact.Before, AfterSHA: r.Impact.After, Before: before, After: after}
		default:
			failure = &protocolError{"UNSUPPORTED_OPERATION", "TS/JS engine does not support operation " + r.Operation}
		}
	}
	return json.Marshal(struct {
		ProtocolVersion int            `json:"protocolVersion"`
		ID              string         `json:"requestId"`
		Stack           string         `json:"stack"`
		Operation       string         `json:"operation"`
		Report          any            `json:"report"`
		Error           *protocolError `json:"error"`
	}{1, r.ID, "ts-js", r.Operation, result, failure})
}

func (r Request) scan(ctx context.Context) (*report, *protocolError) {
	if r.ProtocolVersion != 1 || r.Stack != "ts-js" {
		return nil, &protocolError{"INVALID_REQUEST", "unsupported protocol version or stack"}
	}
	if r.Operation != "scan" {
		return nil, &protocolError{"UNSUPPORTED_OPERATION", "TS/JS engine does not support operation " + r.Operation}
	}
	selected := r.Scan.Facts
	if len(selected) == 0 {
		selected = []string{"dependencies"}
	}
	for _, fact := range selected {
		if fact != "dependencies" && fact != "architecture" && fact != "files" && fact != "ipc" && fact != "call-graph" {
			return nil, &protocolError{"UNSUPPORTED_FACT", "TS/JS engine does not support " + fact}
		}
	}
	root := r.Scan.Repository
	if !filepath.IsAbs(root) {
		return nil, &protocolError{"INVALID_REQUEST", "scan requires an absolute repository directory"}
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, &protocolError{"INVALID_REQUEST", "scan repository must be an existing directory"}
	}
	result, err := scanProject(ctx, root, selected, r.Scan.Static != nil && !*r.Scan.Static)
	if err != nil {
		return nil, &protocolError{"TS_JS_SCAN_ERROR", err.Error()}
	}
	return &report{1, root, result}, nil
}
