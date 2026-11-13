package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/scryer-labs/scryer-ts-js-engine/internal/framework/react"
	"github.com/scryer-labs/scryer-ts-js-engine/internal/framework/tauri"
	"github.com/scryer-labs/scryer-ts-js-engine/internal/script"
)

func TestScanSvelteAndTSXWithTauriImportEvidence(t *testing.T) {
	root := t.TempDir()
	for file, contents := range map[string]string{
		"package.json":              `{"name":"sample","dependencies":{"svelte":"5"}}`,
		"src-tauri/tauri.conf.json": `{}`,
		"src/Widget.tsx":            `export default function Widget() { return <div/>; }`,
		"src/page.svelte": `<!-- <script>fake()</script> -->
<script lang="ts">
import Widget from "./Widget";
import { invoke as dispatch } from "@tauri-apps/api/core";
import * as events from "@tauri-apps/api/event";
dispatch("select_candidate");
async function subscribe() { await events.listen<State>("render_update", () => {}); }
function local(dispatch: (name: string) => void) { dispatch("shadowed"); }
dispatch(dynamic);
</script>
<div></div>
`,
	} {
		target := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	facts, err := scanProject(context.Background(), root, []string{"architecture", "dependencies", "files", "ipc"})
	if err != nil {
		t.Fatal(err)
	}
	files := facts["files"].(map[string]any)
	imports := files["imports"].([]script.Import)
	if len(imports) != 3 || imports[0].TargetFile != "src/Widget.tsx" || imports[0].Line != 3 {
		t.Fatalf("unexpected imports: %+v", imports)
	}
	for _, file := range files["files"].([]fileFact) {
		if file.SyntaxErrors || len(file.ComponentIDs) != 1 || file.ComponentIDs[0] != "package.json" {
			t.Fatalf("unexpected source file: %+v", file)
		}
	}
	apps := facts["ipc"].(map[string]any)["tauri"].(map[string]any)["apps"].([]tauri.App)
	if len(apps) != 1 || apps[0].Config != "src-tauri/tauri.conf.json" || len(apps[0].Endpoints) != 2 {
		t.Fatalf("unexpected apps: %+v", apps)
	}
	for i, expected := range []struct{ name, role string }{{"select_candidate", "invoke"}, {"render_update", "listen"}} {
		endpoint := apps[0].Endpoints[i]
		if endpoint.Name != expected.name || endpoint.Role != expected.role || endpoint.Line != uint(i+6) {
			t.Fatalf("unexpected endpoint: %+v", endpoint)
		}
	}
}

func TestReactArchitectureResolvesDirectAndLocalComponents(t *testing.T) {
	root := t.TempDir()
	for file, contents := range map[string]string{
		"package.json": `{"name":"sample","dependencies":{"react":"19","react-markdown":"10"}}`,
		"Child.tsx":    `export const Child = () => <div/>;`,
		"style.css":    "",
		"App.tsx": `import { Child as Renamed } from "./Child";
import Markdown from "react-markdown";
import "./style.css";
const Local = () => <span/>;
function App() { const Dynamic = views[current]; return <>{items.map(item => <Renamed/>) }<Local/><External/><Markdown/><Dynamic/></>; }
export default App;`,
		"other/package.json": `{"name":"other"}`,
		"other/Widget.tsx":   `export const Widget = () => <div/>;`,
	} {
		target := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	report, err := scanProject(context.Background(), root, []string{"architecture"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report) != 1 {
		t.Fatalf("unexpected extra facts: %+v", report)
	}
	facts := report["architecture"].(map[string]any)["react"].(react.Facts)
	names := map[string]string{}
	for _, component := range facts.Components {
		names[component.ID] = component.Name
	}
	if len(names) != 3 || len(facts.Links) != 5 {
		t.Fatalf("unexpected React facts: %+v", facts)
	}
	for i, want := range []string{"Child", "Local", ""} {
		link := facts.Links[i]
		if names[link.From] != "App" || names[link.To] != want || link.Line != 5 {
			t.Fatalf("unexpected render link: %+v", link)
		}
	}
	if external := facts.Links[3]; external.Resolution != "external" || external.ImportSpecifier != "react-markdown" || external.To != "" {
		t.Fatalf("unexpected external link: %+v", external)
	}
	if dynamic := facts.Links[4]; dynamic.Resolution != "dynamic" || dynamic.To != "" {
		t.Fatalf("unexpected dynamic link: %+v", dynamic)
	}
	filesReport, err := scanProject(context.Background(), root, []string{"files"})
	if err != nil {
		t.Fatal(err)
	}
	imports := filesReport["files"].(map[string]any)["imports"].([]script.Import)
	if len(imports) != 3 || imports[2].TargetFile != "style.css" || imports[2].Resolution != "resolved" {
		t.Fatalf("unexpected stylesheet import: %+v", imports)
	}

}
