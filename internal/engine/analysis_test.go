package engine

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/scryer-labs/scryer-ts-js-engine/internal/analysis"
)

func TestImpactKeepsFunctionIdentityAcrossCommentAndOffsetChanges(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "value.js")
	write := func(source string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("export function value() { return 1; }\nexport function caller() { return value(); }\n")
	before, err := collectImpact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	write("// moved\nexport function value() { return /* comment */ 1; }\nexport function caller() { return value(); }\n")
	after, err := collectImpact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Symbols) != 2 || len(after.Calls) != 1 || after.Calls[0].Candidate == "" {
		t.Fatal("missing shared scan graph")
	}
	if before.Symbols[0].Key != after.Symbols[0].Key || before.Symbols[0].Fingerprint != after.Symbols[0].Fingerprint {
		t.Fatal("formatting changed symbol identity")
	}
}
func TestNativeV8SpecificRangesOverrideScriptHits(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node is not installed")
	}
	root := t.TempDir()
	source := "function unused() { return 0; }\n"
	if err := os.WriteFile(filepath.Join(root, "value.js"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(t.TempDir(), "profile.json")
	data, _ := json.Marshal(map[string]any{"result": []any{map[string]any{"url": filepath.Join(root, "value.js"), "functions": []any{
		map[string]any{"ranges": []any{map[string]any{"startOffset": 0, "endOffset": len(source), "count": 1}}},
		map[string]any{"ranges": []any{map[string]any{"startOffset": 0, "endOffset": len(source) - 1, "count": 0}}},
	}}}})
	if err := os.WriteFile(profile, data, 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "input.json")
	input, _ := json.Marshal(map[string]any{"root": root, "profiles": []string{profile}, "kind": "v8"})
	if err := os.WriteFile(config, input, 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := analysis.Command(context.Background(), root, nil, "node", "-e", analysis.CoverageScript, config)
	if err != nil {
		t.Fatal(err)
	}
	var normalized struct{ Files []analysis.CoverageFile }
	if err = json.Unmarshal(raw, &normalized); err != nil {
		t.Fatal(err)
	}
	if len(normalized.Files) != 1 || len(normalized.Files[0].Regions) != 2 || normalized.Files[0].Regions[0].Count != 0 {
		t.Fatalf("script hits obscured a missed function: %s", raw)
	}
}
