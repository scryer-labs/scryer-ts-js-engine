package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/scryer-labs/scryer-ts-js-engine/internal/analysis"
)

func collectCoverage(ctx context.Context, request analysis.CoverageRequest) (analysis.CoverageReport, error) {
	root := request.Repository
	result := analysis.CoverageReport{SchemaVersion: 1, Root: root, Engine: "ts-js", Status: "unavailable", Runs: []analysis.CoverageRun{}}
	if !filepath.IsAbs(root) {
		return result, fmt.Errorf("coverage requires an absolute repository")
	}
	if len(request.TestTargets) > 0 || len(request.TestBinaries) > 0 {
		return result, fmt.Errorf("TS/JS coverage uses package test scripts, not CMake target/binary selection")
	}
	bounded, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	work, err := os.MkdirTemp("", "scryer-ts-js-coverage-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(work)
	profiles := map[string][]string{}
	status := "collected"
	if request.Artifact != "" {
		data, err := os.ReadFile(request.Artifact)
		if err != nil {
			return result, err
		}
		var shape map[string]json.RawMessage
		if err = json.Unmarshal(data, &shape); err != nil {
			return result, err
		}
		kind := "istanbul"
		if _, ok := shape["result"]; ok {
			kind = "v8"
		}
		profiles[kind] = []string{request.Artifact}
	} else {
		var manifests []string
		err = filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				switch entry.Name() {
				case ".git", ".orrery", ".scryer", "node_modules", "target", "dist", "build", "coverage", ".svelte-kit":
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Name() == "package.json" {
				manifests = append(manifests, file)
			}
			return nil
		})
		if err != nil {
			return result, err
		}
		for i, file := range manifests {
			var pkg struct {
				Scripts map[string]string `json:"scripts"`
			}
			data, err := os.ReadFile(file)
			if err != nil {
				return result, err
			}
			if err = json.Unmarshal(data, &pkg); err != nil {
				return result, err
			}
			script := strings.TrimSpace(pkg.Scripts["test"])
			if script == "" {
				continue
			}
			directory := filepath.Dir(file)
			kind := "istanbul"
			reports := filepath.Join(work, fmt.Sprintf("reports-%d", i))
			env := map[string]string{"CI": "true"}
			args := []string{"run", "test", "--"}
			switch {
			case regexp.MustCompile(`(^|\s)vitest(\s|$)`).MatchString(script):
				if _, err = analysis.Command(bounded, directory, nil, "node", "-e", `require.resolve('@vitest/coverage-v8',{paths:[process.cwd()]})`); err != nil {
					return result, fmt.Errorf("Vitest coverage requires the project's matching @vitest/coverage-v8 package: %w", err)
				}
				if !regexp.MustCompile(`\brun\b`).MatchString(script) {
					args = append(args, "run")
				}
				args = append(args, "--coverage", "--coverage.provider=v8", "--coverage.reporter=json", "--coverage.reportsDirectory="+reports)
			case regexp.MustCompile(`(^|\s)jest(\s|$)`).MatchString(script):
				args = append(args, "--coverage", "--coverageReporters=json", "--coverageDirectory="+reports, "--runInBand")
			case strings.HasPrefix(script, "node ") && strings.Contains(script, "--test") && !strings.ContainsAny(script, ";&|") && !strings.Contains(script, "--loader") && !strings.Contains(script, "--import"):
				kind = "v8"
				if err = os.MkdirAll(reports, 0700); err != nil {
					return result, err
				}
				env["NODE_V8_COVERAGE"] = reports
				args = []string{"run", "test"}
			default:
				return result, fmt.Errorf("unsupported test runner in %s; supported collection uses Vitest, Jest or native node --test, or import an Istanbul report", file)
			}
			_, failure := analysis.Command(bounded, directory, env, "npm", args...)
			if bounded.Err() != nil {
				return result, bounded.Err()
			}
			relative, _ := filepath.Rel(root, file)
			run := analysis.CoverageRun{Command: append([]string{"npm"}, args...), Directory: filepath.ToSlash(filepath.Dir(relative)), Configuration: map[string]string{"script": script, "coverageFormat": kind, "CI": "true"}, Name: "npm test: " + filepath.ToSlash(relative), Status: "succeeded"}
			var found []string
			if kind == "istanbul" {
				path := filepath.Join(reports, "coverage-final.json")
				if _, err := os.Stat(path); err == nil {
					found = []string{path}
				}
			} else {
				found, err = filepath.Glob(filepath.Join(reports, "coverage-*.json"))
				if err != nil {
					return result, err
				}
			}
			run.Profiles = len(found)
			if len(found) == 0 {
				run.Status = "no-profile"
				status = "partial"
			}
			if failure != nil {
				run.Status = "failed"
				run.Message = failure.Error()
				status = "partial"
			}
			profiles[kind] = append(profiles[kind], found...)
			result.Runs = append(result.Runs, run)
		}
	}
	files := map[string]*analysis.CoverageFile{}
	notes := []string{"Coverage is aggregate source evidence without per-test attribution or assertion-effectiveness proof. Vitest/Jest statement positions rely on the runner's original-source mapping; raw V8 offsets are accepted only for untransformed JavaScript."}
	artifactHash := sha256.New()
	for _, kind := range []string{"istanbul", "v8"} {
		if len(profiles[kind]) == 0 {
			continue
		}
		input, _ := json.Marshal(map[string]any{"root": root, "profiles": profiles[kind], "kind": kind})
		config := filepath.Join(work, "normalize-"+kind+".json")
		if err = os.WriteFile(config, input, 0600); err != nil {
			return result, err
		}
		raw, err := analysis.Command(bounded, root, nil, "node", "-e", analysis.CoverageScript, config)
		if err != nil {
			return result, err
		}
		var normalized struct {
			Files   []analysis.CoverageFile
			Notes   []string
			Omitted int
			SHA256  string
		}
		if err = json.Unmarshal(raw, &normalized); err != nil {
			return result, err
		}
		if normalized.Omitted > 0 {
			status = "partial"
		}
		notes = append(notes, normalized.Notes...)
		for _, profile := range profiles[kind] {
			data, err := os.ReadFile(profile)
			if err != nil {
				return result, err
			}
			_, _ = artifactHash.Write(data)
		}
		for _, file := range normalized.Files {
			if prior := files[file.Path]; prior != nil {
				prior.Regions = append(prior.Regions, file.Regions...)
			} else {
				copy := file
				files[file.Path] = &copy
			}
		}
	}
	if len(files) == 0 {
		result.Message = "No original-source coverage mapped to this project; run supported test scripts or import remapped Istanbul coverage."
		return result, nil
	}
	artifact := request.Artifact
	if artifact == "" {
		artifact = "ts-js-coverage.json"
	}
	evidence := analysis.CoverageEvidence{Format: "javascript-source-coverage", Version: "1", Artifact: artifact, SHA256: fmt.Sprintf("%x", artifactHash.Sum(nil)), SourceMatch: "unverified", Granularity: "aggregate-source-regions", Files: analysis.FinalizeFiles(files), Notes: notes}
	result.Status = status
	if status == "partial" {
		result.Message = "Test execution or original-source mapping was incomplete; inspect runs and coverage notes."
	}
	result.Coverage, err = json.Marshal(evidence)
	return result, err
}
