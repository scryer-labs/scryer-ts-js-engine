package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"encoding/json"
	"github.com/scryer-labs/scryer-ts-js-engine/internal/analysis"
	"github.com/scryer-labs/scryer-ts-js-engine/internal/script"
	"github.com/scryer-labs/scryer-ts-js-engine/internal/semantic"
)

func impactFixture(t *testing.T, files map[string]string) (string, analysis.ImpactSnapshot) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "project space 世界")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	for file, source := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, file)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, file), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := collectImpact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	return root, result
}

func Test_should_retain_callback_bodies_without_guessing_callback_invocation(t *testing.T) {
	_, snapshot := impactFixture(t, map[string]string{"value.js": `
function changed() { return 1; }
export function caller() {
  useEffect(() => changed());
  items.map(item => changed());
  promise.then(() => changed());
  record("ordinary business label", () => changed());
  useHook(() => { nested(() => changed()); });
}`})
	ids := map[string]analysis.ImpactSymbol{}
	for _, symbol := range snapshot.Symbols {
		ids[symbol.ID] = symbol
	}
	bodyCalls := 0
	for _, call := range snapshot.Calls {
		if call.Callee != "changed" {
			continue
		}
		bodyCalls++
		caller, ok := ids[call.Caller]
		if !ok || !strings.Contains(caller.Signature, "callback") || call.Candidate == "" {
			t.Fatalf("lost closure caller/body edge: %+v %+v", call, caller)
		}
		if caller.Role == "TEST" {
			t.Fatalf("business label classified as test: %+v", caller)
		}
	}
	if bodyCalls != 5 {
		t.Fatalf("got %d closure calls: %+v", bodyCalls, snapshot)
	}
	for _, call := range snapshot.Calls {
		if target := ids[call.Candidate]; strings.Contains(target.Signature, "callback") {
			t.Fatalf("invented invocation into passed callback: %+v", call)
		}
	}
}

func Test_should_keep_callback_correspondence_across_trivia_and_flag_duplicate_anchors(t *testing.T) {
	root, before := impactFixture(t, map[string]string{"value.js": `export function caller(){ useEffect(() => changed(1)); }`})
	if err := os.WriteFile(filepath.Join(root, "value.js"), []byte("// moved\r\nexport function caller() { useEffect( /* comment */ () => changed(2)); }\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := collectImpact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Symbols) != 2 || len(after.Symbols) != 2 {
		t.Fatal(before, after)
	}
	if before.Symbols[1].Key != after.Symbols[1].Key || before.Symbols[1].Fingerprint == after.Symbols[1].Fingerprint {
		t.Fatalf("callback correspondence lost on CRLF/comment movement: %+v %+v", before.Symbols[1], after.Symbols[1])
	}
	_, duplicate := impactFixture(t, map[string]string{"value.js": `function caller(){ useEffect(() => changed(1)); useEffect(() => changed(2)); }`})
	if len(duplicate.Symbols) != 3 || duplicate.Symbols[1].Key != duplicate.Symbols[2].Key || duplicate.Symbols[1].ID == duplicate.Symbols[2].ID {
		t.Fatalf("indistinguishable callbacks must remain explicit duplicate correspondence, not order-matched: %+v", duplicate.Symbols)
	}
}

func Test_should_require_imported_test_registration_while_retaining_all_callbacks(t *testing.T) {
	_, snapshot := impactFixture(t, map[string]string{"value.js": `import { test } from 'node:test';
test('real test', () => work());
record('not a test', () => work());
function business(test) { test('shadowed', () => work()); }
let mutable = () => work(); mutable = other; mutable();`})
	tests, unknownCallbacks := 0, 0
	for _, symbol := range snapshot.Symbols {
		if symbol.Role == "TEST" {
			tests++
		} else if strings.Contains(symbol.Signature, "callback") {
			unknownCallbacks++
		}
	}
	if tests != 1 || unknownCallbacks != 2 {
		t.Fatalf("incorrect registration roles: %+v", snapshot.Symbols)
	}
	for _, call := range snapshot.Calls {
		if call.Callee == "mutable" && call.Candidate != "" {
			t.Fatalf("reassigned function bound: %+v", call)
		}
	}
}

func Test_should_project_react_references_separately_from_calls(t *testing.T) {
	_, snapshot := impactFixture(t, map[string]string{
		"package.json": `{"dependencies":{"react":"18"}}`,
		"App.tsx":      `function changed(){ return 1; } export function Child(){ return <span/>; } export function App(){ return <Child onClick={() => changed()}/>; }`,
	})
	references, handlerBodies := map[string]int{}, 0
	for _, call := range snapshot.Calls {
		if call.Kind == "framework-reference" {
			references[call.CandidateBasis]++
			if call.Candidate == "" {
				t.Fatalf("lost source reference: %+v", call)
			}
		} else if call.Callee == "changed" && call.Candidate != "" {
			handlerBodies++
		}
	}
	if references["react-render"] != 1 || references["react-handler"] != 1 || handlerBodies != 1 {
		t.Fatalf("React references/body edges missing: %+v", snapshot)
	}
}

func Test_should_project_svelte_component_and_handler_references_as_non_executable(t *testing.T) {
	root, snapshot := impactFixture(t, map[string]string{
		"App.svelte":   "<script>import Child from './Child.svelte'; function changed(){return 1;} function handle(){changed();}</script>\r\n<Child/><button onclick={handle}>go</button>",
		"Child.svelte": `<p>child</p>`,
	})
	components, references := 0, map[string]int{}
	for _, symbol := range snapshot.Symbols {
		if symbol.ExecutionKind == "non-executable" {
			components++
		}
	}
	for _, call := range snapshot.Calls {
		if call.Kind == "framework-reference" {
			references[call.CandidateBasis]++
			if call.Candidate == "" {
				t.Fatalf("lost component/handler target: %+v", call)
			}
		}
	}
	if components != 2 || references["svelte-component"] != 1 || references["svelte-handler"] != 1 {
		t.Fatalf("Svelte source references missing: %+v", snapshot)
	}
	if err := os.WriteFile(filepath.Join(root, "App.svelte"), []byte(`<script>function handle(){}</script>{#each items as handle}<button onclick={handle}>go</button>{/each}`), 0600); err != nil {
		t.Fatal(err)
	}
	facts, err := scanProject(context.Background(), root, []string{"call-graph"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, leaked := facts["architecture"]; leaked {
		t.Fatal("unrequested architecture fact leaked")
	}
	for _, reference := range facts["call-graph"].(*script.Graph).References {
		if reference.Basis == "svelte-handler" && reference.To != "" {
			t.Fatalf("template scope guessed script binding: %+v", reference)
		}
	}
}

func Test_should_retain_configuration_and_template_only_changes_as_context(t *testing.T) {
	root, before := impactFixture(t, map[string]string{"package.json": `{"type":"module"}`, "App.svelte": `<script>function unchanged(){}</script><p>before</p>`})
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"type":"commonjs"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "App.svelte"), []byte(`<script>function unchanged(){}</script><p>after</p>`), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := collectImpact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	contexts := map[string]string{}
	for _, c := range before.Contexts {
		contexts[c.Path] = c.Fingerprint
	}
	changes := map[string]bool{}
	for _, c := range after.Contexts {
		changes[c.Path] = contexts[c.Path] != c.Fingerprint
	}
	if !changes["package.json"] || !changes["App.svelte"] {
		t.Fatalf("unassessed context discarded: %+v %+v", before.Contexts, after.Contexts)
	}
}

func Test_should_keep_compiler_callback_target_beside_unrelated_type_diagnostic(t *testing.T) {
	root, _ := impactFixture(t, map[string]string{
		"tsconfig.json": `{"compilerOptions":{"noEmit":true,"strict":true,"target":"ES2022","module":"NodeNext","moduleResolution":"NodeNext","types":[]},"include":["*.ts"]}`,
		"value.ts":      "export function changed() { return 1; }\r\nexport function caller() { [1].map(() => changed()); }\r\nconst unrelated: number = 'wrong';\r\n",
	})
	result, err := collectImpact(context.Background(), root, true)
	if err != nil && (strings.Contains(err.Error(), "Compiler API is unavailable") || strings.Contains(err.Error(), "executable file not found")) && os.Getenv("SCRYER_REQUIRE_SEMANTIC_TESTS") != "1" {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	var resolution semantic.Report
	if err := json.Unmarshal(result.Resolution, &resolution); err != nil {
		t.Fatal(err)
	}
	if resolution.Status != "partial" || resolution.Diagnostics == 0 || !resolution.UsableForImpact {
		t.Fatalf("lost partial compiler evidence: %+v", resolution)
	}
	ids := map[string]analysis.ImpactSymbol{}
	for _, symbol := range result.Symbols {
		ids[symbol.ID] = symbol
	}
	found := false
	for _, call := range result.Calls {
		if call.Callee == "changed" && call.Target != "" && strings.Contains(ids[call.Caller].Signature, "callback") {
			found = true
		}
	}
	if !found {
		t.Fatalf("valid callback edge lost beside unrelated diagnostic: %+v", result)
	}
}

func Test_should_keep_unprojected_syntax_error_body_in_context(t *testing.T) {
	root, before := impactFixture(t, map[string]string{"broken.js": `function broken(){ return ??? old; }`})
	if err := os.WriteFile(filepath.Join(root, "broken.js"), []byte(`function broken(){ return ??? changed; }`), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := collectImpact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Symbols) != 0 || len(after.Symbols) != 0 {
		t.Fatalf("syntax errors should not yield confident identities: %+v %+v", before.Symbols, after.Symbols)
	}
	if len(before.Contexts) != 1 || len(after.Contexts) != 1 || before.Contexts[0].Fingerprint == after.Contexts[0].Fingerprint {
		t.Fatalf("parse-invalid function was silently masked from context: %+v %+v", before.Contexts, after.Contexts)
	}
}
