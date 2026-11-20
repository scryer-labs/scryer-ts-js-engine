package semantic

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func resolveFixture(t *testing.T, sources map[string]string) Report {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node is not installed")
	}
	root := t.TempDir()
	files := []string{}
	for name, source := range sources {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(name, ".json") {
			files = append(files, name)
		}
	}
	report := Resolve(context.Background(), root, files)
	if strings.Contains(report.Message, "Compiler API is unavailable") {
		t.Skip(report.Message)
	}
	return report
}

func TestInferredJavaScriptResolvesCommonJSAndESModulesWithoutConfig(t *testing.T) {
	report := resolveFixture(t, map[string]string{
		"main.cjs":  `const {value} = require('./value.cjs'); function caller(){ return value(); } caller();`,
		"value.cjs": `function value(){return 1;} module.exports={value};`,
		"main.mjs":  `import {other} from './other.mjs'; function caller(){return other();} caller();`,
		"other.mjs": `export function other(){return 2;}`,
	})
	if report.Status != "resolved" || report.ConfigurationBasis != "inferred-javascript" || report.Programs != 1 {
		t.Fatalf("%+v", report)
	}
	targets := map[string]bool{}
	for _, call := range report.Calls {
		if call.Site.File != call.Target.File {
			targets[call.Target.File] = true
		}
	}
	if !targets["value.cjs"] || !targets["other.mjs"] {
		t.Fatalf("cross-file bindings missing: %+v", report.Calls)
	}
}

func TestJavaScriptInferenceDoesNotHideSyntaxErrorsOrAcceptUnconfiguredTypeScript(t *testing.T) {
	malformed := resolveFixture(t, map[string]string{"broken.js": `function broken( {`})
	if malformed.Status != "partial" || malformed.Diagnostics == 0 {
		t.Fatalf("%+v", malformed)
	}
	typed := resolveFixture(t, map[string]string{"main.ts": `export function value(): number { return 1; }`})
	if typed.Status != "fallback" || typed.Programs != 0 {
		t.Fatalf("%+v", typed)
	}
}

func TestExplicitJSConfigRetainsTypeCheckingAndDynamicCallsStayUnbound(t *testing.T) {
	explicit := resolveFixture(t, map[string]string{
		"jsconfig.json": `{"compilerOptions":{"allowJs":true,"checkJs":true,"noEmit":true,"types":[]},"include":["*.js"]}`,
		"main.js":       "/** @param {number} n */\nfunction value(n){return n;} value('wrong');",
	})
	if explicit.ConfigurationBasis != "project-config" || explicit.Status != "partial" || explicit.Diagnostics == 0 {
		t.Fatalf("%+v", explicit)
	}
	unknown := resolveFixture(t, map[string]string{"main.js": `export function caller(value){return value();}`})
	if unknown.Status != "resolved" || len(unknown.Calls) != 0 {
		t.Fatalf("invented runtime target: %+v", unknown)
	}
}
