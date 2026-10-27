package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type projectToolingPinnedRunner struct {
	commit string
	files  map[string][]byte
	mode   string
	calls  []string
}

func (r *projectToolingPinnedRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, strings.Join(args, " "))
	if reflect.DeepEqual(args, []string{"ls-tree", "-r", "-z", r.commit, "--", ".concord/scenarios", "internal"}) {
		var names []string
		for name := range r.files {
			if strings.HasPrefix(name, ".concord/scenarios/") || strings.HasPrefix(name, "internal/") {
				names = append(names, name)
			}
		}
		slices.Sort(names)
		var out strings.Builder
		for _, name := range names {
			fmt.Fprintf(&out, "100644 blob %s\t%s\x00", strings.Repeat("b", 40), name)
		}
		return []byte(out.String()), nil
	}
	if reflect.DeepEqual(args, []string{"symbolic-ref", "refs/remotes/origin/HEAD"}) {
		return []byte("refs/remotes/origin/main\n"), nil
	}
	if reflect.DeepEqual(args, []string{"rev-parse", "--verify", "origin/main^{commit}"}) {
		return []byte(r.commit + "\n"), nil
	}
	if len(args) == 5 && args[0] == "ls-tree" && args[1] == "-z" && args[2] == r.commit && args[3] == "--" {
		if _, exists := r.files[args[4]]; !exists {
			return nil, nil
		}
		mode := "100644"
		if r.mode != "" && args[4] != projectToolingManifestPath {
			mode = r.mode
		}
		// The fake blob identity is the literal path; the fake cat-file resolves it.
		return []byte(mode + " blob " + args[4] + "\t" + args[4] + "\x00"), nil
	}
	if len(args) == 4 && args[0] == "ls-tree" && args[1] == r.commit && args[2] == "--" {
		if _, exists := r.files[args[3]]; exists {
			return []byte("present"), nil
		}
		return nil, nil
	}
	if len(args) == 3 && args[0] == "cat-file" && args[1] == "blob" {
		if data, exists := r.files[args[2]]; exists {
			return data, nil
		}
	}
	return nil, fmt.Errorf("unexpected git invocation: %s", strings.Join(args, " "))
}

func projectToolingRecipeRunner() *projectToolingPinnedRunner {
	return &projectToolingPinnedRunner{commit: strings.Repeat("a", 40), files: map[string][]byte{
		projectToolingManifestPath:     toolingCoverageDocument("go test -count=1 ./internal/demo", []any{toolingCoverageEntry()}),
		".concord/oracles/demo.json":   []byte(`{"kind":"go_top_level_v1","package_cwd":"internal/demo","test_files":["internal/demo/demo_test.go"],"fixture_files":[".concord/scenarios/demo.json"],"cases":{"case:demo":["TestDemo"],"case:other":["TestOther"]}}`),
		"internal/demo/demo_test.go":   []byte("package demo\nimport \"testing\"\nfunc TestDemo(t *testing.T) { t.Fatal(\"bad\") }\nfunc TestOther(t *testing.T) { t.Fatal(\"bad\") }\n"),
		".concord/scenarios/demo.json": []byte(`{"id":"demo"}`),
	}}
}

func TestProjectToolingResolutionPinsOneCommitAndInputs(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "tooling-pinned")
	seedWorkCanonicalPath(t, s, t.TempDir())
	runner := projectToolingRecipeRunner()
	m, err := resolveWorkProjectTooling(context.Background(), s, "tooling-pinned", runner)
	if err != nil {
		t.Fatal(err)
	}
	if m.SourceCommit != runner.commit || m.SourceProjectID != "project" || m.ManifestDigest != nativeDigest(runner.files[projectToolingManifestPath]) || len(m.ResolvedCoverage) != 1 {
		t.Fatalf("missing immutable manifest identity: %+v", m)
	}
	c := m.ResolvedCoverage[0]
	if c.RecipeSource.CommitOID != runner.commit || c.RecipeSource.ProjectID != "project" || c.RecipeDigest != nativeDigest(c.RecipeRaw) || !reflect.DeepEqual(c.Recipe.Witnesses, []string{"TestDemo"}) || len(c.Files) != 2 {
		t.Fatalf("missing case/input identity: %+v", c)
	}
	for _, f := range c.Files {
		if f.SHA256 != nativeDigest(runner.files[f.Path]) || f.BlobOID == "" {
			t.Fatalf("input digest missing: %+v", f)
		}
	}
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "ls-tree origin/main") || strings.HasPrefix(call, "show origin/main") {
			t.Fatalf("mutable ref read after pin: %s", call)
		}
	}
}

func TestProjectToolingResolutionReturnsTypedDeclarationRefusal(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprint(malformed), func(t *testing.T) {
			s := openTemp(t)
			seedWork(t, s, "tooling-invalid")
			seedWorkCanonicalPath(t, s, t.TempDir())
			r := projectToolingRecipeRunner()
			if malformed {
				r.files[projectToolingManifestPath] = []byte(`{"schema_version":`)
			} else {
				r.files[projectToolingManifestPath] = toolingCoverageDocument("go test ./other", []any{toolingCoverageEntry()})
			}
			_, err := resolveWorkProjectTooling(t.Context(), s, "tooling-invalid", r)
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "tooling") || failure.RecoveryAction == "" {
				t.Fatalf("expected typed manifest repair refusal, got %v", err)
			}
		})
	}
}

func TestProjectToolingResolutionRefusesUnqualifiedInputs(t *testing.T) {
	for _, mutation := range []string{"missing-manifest", "missing-recipe", "symlink", "missing-input", "unknown-case", "unrelated-command"} {
		t.Run(mutation, func(t *testing.T) {
			r := projectToolingRecipeRunner()
			switch mutation {
			case "missing-manifest":
				delete(r.files, projectToolingManifestPath)
			case "missing-recipe":
				delete(r.files, ".concord/oracles/demo.json")
			case "symlink":
				r.mode = "120000"
			case "missing-input":
				delete(r.files, ".concord/scenarios/demo.json")
			case "unknown-case":
				r.files[".concord/oracles/demo.json"] = []byte(strings.ReplaceAll(string(r.files[".concord/oracles/demo.json"]), "case:demo", "case:missing"))
			case "unrelated-command":
				r.files[projectToolingManifestPath] = toolingCoverageDocument("go test ./other", []any{toolingCoverageEntry()})
			}
			m, err := resolveProjectToolingAtCommit(context.Background(), r, "synthetic", "project", r.commit)
			if mutation == "missing-manifest" {
				if m != nil || err != nil {
					t.Fatalf("missing is not distinct: %+v %v", m, err)
				}
				return
			}
			if err == nil || m != nil {
				t.Fatalf("unqualified source accepted: %+v %v", m, err)
			}
		})
	}
}

func TestProjectToolingResolutionRejectsUnrelatedCase(t *testing.T) {
	for _, mutation := range []string{"unrelated-test", "wrong-package", "package-for-test", "selected-for-package", "unrelated-scenario", "parent-for-scenario"} {
		t.Run(mutation, func(t *testing.T) {
			r := projectToolingRecipeRunner()
			entry := toolingCoverageEntry()
			switch mutation {
			case "unrelated-test":
				entry["case_id"] = "case:other"
			case "wrong-package":
				entry["anchor"] = map[string]any{"kind": "go_test", "value": "other.TestDemo"}
			case "package-for-test":
				r.files[".concord/oracles/demo.json"] = []byte(`{"kind":"go_package_v1","package_cwd":"internal/demo","cases":{"case:demo":["go_package:./internal/demo"]}}`)
			case "selected-for-package":
				entry["anchor"] = map[string]any{"kind": "go_package", "value": "./internal/demo"}
			case "unrelated-scenario", "parent-for-scenario":
				entry["anchor"] = map[string]any{"kind": "scenario", "value": "required-scenario"}
				r.files[".concord/scenarios/demo.json"] = []byte(`{"scenarios":[{"id":"required-scenario"}]}`)
				if mutation == "unrelated-scenario" {
					r.files[".concord/oracles/demo.json"] = []byte(strings.ReplaceAll(string(r.files[".concord/oracles/demo.json"]), `"TestDemo"`, `"TestDemo/unrelated-scenario"`))
				}
			}
			r.files[projectToolingManifestPath] = toolingCoverageDocument("go test ./...", []any{entry})
			if _, err := resolveProjectToolingAtCommit(context.Background(), r, "synthetic", "project", r.commit); err == nil {
				t.Fatal("unrelated recipe case qualified typed anchor")
			}
		})
	}
}

func TestProjectToolingScenarioCaseIdentity(t *testing.T) {
	for _, mutation := range []string{"exact", "missing-fixture", "duplicate-id", "other-corpus-duplicate", "deferred", "unrelated-parent", "package"} {
		t.Run(mutation, func(t *testing.T) {
			r := projectToolingRecipeRunner()
			entry := toolingCoverageEntry()
			entry["anchor"] = map[string]any{"kind": "scenario", "value": "required-id"}
			r.files[projectToolingManifestPath] = toolingCoverageDocument("go test ./internal/demo", []any{entry})
			r.files[".concord/scenarios/demo.json"] = []byte(`{"scenarios":[{"id":"required-id"}]}`)
			r.files[".concord/oracles/demo.json"] = []byte(strings.ReplaceAll(string(r.files[".concord/oracles/demo.json"]), `"TestDemo"`, `"TestDemo/required-id"`))
			switch mutation {
			case "missing-fixture":
				r.files[".concord/oracles/demo.json"] = []byte(strings.ReplaceAll(string(r.files[".concord/oracles/demo.json"]), `".concord/scenarios/demo.json"`, ""))
			case "duplicate-id":
				r.files[".concord/scenarios/demo.json"] = []byte(`{"scenarios":[{"id":"required-id","cases":[{"id":"required-id"}]}]}`)
			case "other-corpus-duplicate":
				r.files[".concord/scenarios/other.json"] = r.files[".concord/scenarios/demo.json"]
			case "deferred":
				r.files["internal/other/deferred_test.go"] = []byte(`package other; var _ = Deferrals["required-id"] = "not implemented"`)
			case "unrelated-parent":
				r.files[".concord/oracles/demo.json"] = []byte(strings.ReplaceAll(string(r.files[".concord/oracles/demo.json"]), "TestDemo/required-id", "TestMissing/required-id"))
			case "package":
				r.files[".concord/oracles/demo.json"] = []byte(`{"kind":"go_package_v1","package_cwd":"internal/demo","cases":{"case:demo":["go_package:./internal/demo"]}}`)
			}
			_, err := resolveProjectToolingAtCommit(context.Background(), r, "synthetic", "project", r.commit)
			if (err == nil) != (mutation == "exact") {
				t.Fatalf("scenario qualification %s: %v", mutation, err)
			}
		})
	}
}

func TestProjectToolingSelectedTargetCheckOwner(t *testing.T) {
	raw := []byte(`{"kind":"target_runner_v1","materializer":"none","runner":"check","root":".","harness_files":["scripts/check-demo.py","scripts/check-other.py"],"support_files":[],"cases":{"case:demo":["demo"],"case:other":["other"]},"checks":[{"witness":"demo","argv":["python3","scripts/check-demo.py"]},{"witness":"other","argv":["python3","scripts/check-other.py"]}],"pin":{"python":"3.13.7"}}`)
	recipe, err := ResolveNativeRecipeCase(raw, "case:demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(recipe.checks) != 1 || recipe.checks[0].Witness != "demo" {
		t.Fatalf("unselected commands leaked into case: %+v", recipe.checks)
	}
	for _, script := range []string{"scripts/check-demo.py", "scripts/check-other.py"} {
		want := script == "scripts/check-demo.py"
		if ProjectToolingCoverageLineage("python3 "+script, recipe) != want {
			t.Fatalf("harness inventory substituted for selected command: %s", script)
		}
		err := projectToolingAnchorCase(FootprintCheck{Kind: "validator", Value: script}, recipe, nil, nil, "python3 "+script)
		if (err == nil) != want {
			t.Fatalf("validator owner %s: %v", script, err)
		}
	}
}

func TestProjectToolingGeneratedSelectedParity(t *testing.T) {
	for _, mutation := range []string{"exact", "unselected-generator", "no-check-mode", "unrelated-output", "unrelated-parity", "missing-symbol"} {
		t.Run(mutation, func(t *testing.T) {
			recipe := NativeRecipeCase{Kind: nativeTargetKind, Cwd: ".", runner: "check", HarnessFiles: []string{"scripts/generate-demo.py", "scripts/test-generate-demo.py"}, checks: []nativeTargetCheck{
				{Witness: "freshness", Argv: []string{"python3", "scripts/generate-demo.py", "--check"}},
				{Witness: "parity", Argv: []string{"python3", "scripts/test-generate-demo.py"}},
			}}
			files := map[string][]byte{
				"internal/demo/generated.go":    []byte("// Code generated by scripts/generate-demo.py; DO NOT EDIT.\npackage demo\nconst Identity=1\n"),
				"scripts/generate-demo.py":      []byte(`outputs = {"internal/demo/generated.go": "const Identity=1"}`),
				"scripts/test-generate-demo.py": []byte(`generator = "scripts/generate-demo.py"`),
			}
			switch mutation {
			case "unselected-generator":
				recipe.checks = recipe.checks[1:]
			case "no-check-mode":
				recipe.checks[0].Argv = recipe.checks[0].Argv[:2]
			case "unrelated-output":
				files["scripts/generate-demo.py"] = []byte(`outputs = {"internal/other/generated.go": "const Identity=1"}`)
			case "unrelated-parity":
				files["scripts/test-generate-demo.py"] = []byte(`generator = "scripts/generate-other.py"`)
			case "missing-symbol":
				files["internal/demo/generated.go"] = []byte("// Code generated by scripts/generate-demo.py; DO NOT EDIT.\npackage demo\n")
			}
			read := func(name string) ([]byte, error) { return files[name], nil }
			err := projectToolingAnchorCase(FootprintCheck{Kind: "generated", Value: "internal/demo/generated.go#Identity"}, recipe, read, nil, "python3 scripts/test-generate-demo.py")
			if (err == nil) != (mutation == "exact") {
				t.Fatalf("generated qualification %s: %v", mutation, err)
			}
		})
	}
}

func TestProjectToolingAdapterExactRuntimeWitness(t *testing.T) {
	for _, mutation := range []string{"exact", "unrelated-expect", "no-expect", "unselected-command", "wrong-file", "generic-check", "opaque-label"} {
		t.Run(mutation, func(t *testing.T) {
			recipe := NativeRecipeCase{Kind: nativeTargetKind, Cwd: "adapter/opencode", runner: "check", materializer: "bun", HarnessFiles: []string{"adapter/opencode/acceptance.py", "adapter/opencode/demo.test.ts", "adapter/opencode/other.test.ts"}, checks: []nativeTargetCheck{{Witness: "opaque-label", Argv: []string{"python3", "acceptance.py", "demo.test.ts", "--expect", "Required test"}}}}
			switch mutation {
			case "unrelated-expect":
				recipe.checks[0].Argv[4] = "Unrelated test"
			case "no-expect":
				recipe.checks[0].Argv = recipe.checks[0].Argv[:3]
			case "unselected-command":
				recipe.checks = nil
			case "wrong-file":
				recipe.checks[0].Argv[2] = "other.test.ts"
			case "generic-check":
				recipe.materializer = "none"
			case "opaque-label":
				recipe.checks[0].Argv[4] = "opaque-label"
			}
			err := projectToolingAnchorCase(FootprintCheck{Kind: "adapter_test", Value: "adapter/opencode/demo.test.ts#Required test"}, recipe, nil, nil, "bun test adapter/opencode/demo.test.ts")
			if (err == nil) != (mutation == "exact") {
				t.Fatalf("adapter qualification %s: %v", mutation, err)
			}
		})
	}
}

func TestProjectToolingCoverageLineage(t *testing.T) {
	goRecipe := NativeRecipeCase{Kind: "go_top_level_v1", Cwd: "internal/demo"}
	for _, invocation := range []string{"go test ./...", "go test -race -v -json -count 1 -timeout=20m -p=2 -parallel 4 ./internal/...", "go test ./internal/demo"} {
		if !ProjectToolingCoverageLineage(invocation, goRecipe) {
			t.Fatalf("direct lineage refused: %s", invocation)
		}
	}
	for _, invocation := range []string{"go test ./internal/demolition/...", "go test ./other", "go test .", "go test -count=0 ./...", "go test -timeout=0s ./...", "go test -v=false ./...", "go test -v -v ./...", "go test -coverpkg=./other ./..."} {
		if ProjectToolingCoverageLineage(invocation, goRecipe) {
			t.Fatalf("unqualified lineage accepted: %s", invocation)
		}
	}
	target := NativeRecipeCase{Kind: "target_runner_v1", Cwd: ".", HarnessFiles: []string{"scripts/check-demo.py", "adapter/opencode/demo.test.ts"}, runner: "check", checks: []nativeTargetCheck{{Witness: "demo", Argv: []string{"python3", "scripts/check-demo.py"}}}}
	for _, invocation := range []string{"python3 scripts/check-demo.py"} {
		if !ProjectToolingCoverageLineage(invocation, target) {
			t.Fatalf("direct target lineage refused: %s", invocation)
		}
	}
	for _, invocation := range []string{"python3 scripts/check-other.py", "bun test adapter/opencode/other.test.ts", "bun test adapter/opencode/demo.test.ts", "go test ./..."} {
		if ProjectToolingCoverageLineage(invocation, target) {
			t.Fatalf("unrelated target lineage accepted: %s", invocation)
		}
	}
}

func toolingCoverageDocument(invocation string, coverage any) []byte {
	raw, _ := json.Marshal(map[string]any{"schema_version": "1.0", "project": "demo", "tools": []any{map[string]any{
		"id": "proof-test", "invocation": invocation, "purpose": "Verify coverage.", "tier": "fast", "coverage": coverage,
	}}})
	return raw
}

func toolingCoverageEntry() map[string]any {
	return map[string]any{"anchor": map[string]any{"kind": "go_test", "value": "internal/demo.TestDemo"}, "recipe_path": ".concord/oracles/demo.json", "case_id": "case:demo"}
}

func TestProjectToolingCoveragePreserved(t *testing.T) {
	m, err := ParseProjectToolingManifest(toolingCoverageDocument("go test -count=1 ./internal/demo", []any{toolingCoverageEntry()}))
	if err != nil {
		t.Fatal(err)
	}
	coverage := reflect.ValueOf(m.Declared[0]).FieldByName("Coverage")
	if !coverage.IsValid() || coverage.Len() != 1 {
		t.Fatal("explicit coverage was discarded")
	}
	if m.Declared[0].Coverage[0] != (ProjectToolCoverage{Anchor: FootprintCheck{Kind: "go_test", Value: "internal/demo.TestDemo"}, RecipePath: ".concord/oracles/demo.json", CaseID: "case:demo"}) {
		t.Fatalf("coverage changed: %+v", m.Declared[0].Coverage)
	}
}

func TestProjectToolingCoverageSharedAndAbsent(t *testing.T) {
	r := projectToolingRecipeRunner()
	var document map[string]any
	if err := json.Unmarshal(r.files[projectToolingManifestPath], &document); err != nil {
		t.Fatal(err)
	}
	tools := document["tools"].([]any)
	other := map[string]any{"id": "other-test", "invocation": "go test -race ./internal/demo", "coverage": []any{toolingCoverageEntry()}}
	document["tools"] = append(tools, other)
	r.files[projectToolingManifestPath], _ = json.Marshal(document)
	m, err := resolveProjectToolingAtCommit(context.Background(), r, "synthetic", "project", r.commit)
	if err != nil || len(m.ResolvedCoverage) != 1 {
		t.Fatalf("shared identical relation: %+v %v", m, err)
	}
	other["invocation"] = "go test ./other"
	r.files[projectToolingManifestPath], _ = json.Marshal(document)
	if _, err := resolveProjectToolingAtCommit(context.Background(), r, "synthetic", "project", r.commit); err == nil {
		t.Fatal("shared binding bypassed second tool lineage")
	}
	other["invocation"] = "bin/oc-test conformance"
	delete(other, "coverage")
	document["tools"] = []any{other}
	r.files[projectToolingManifestPath], _ = json.Marshal(document)
	m, err = resolveProjectToolingAtCommit(context.Background(), r, "synthetic", "project", r.commit)
	if err != nil || len(m.ResolvedCoverage) != 0 || !ProjectToolingInvocationDeclared(m, []string{"bin/oc-test", "conformance"}) {
		t.Fatalf("old refine wrapper changed or gained proof: %+v %v", m, err)
	}
}

func TestProjectToolingCoverageClosedAndBounded(t *testing.T) {
	for _, mutation := range []string{"unknown", "bad-kind", "bad-path", "bad-case", "duplicate", "conflict", "null", "overflow"} {
		t.Run(mutation, func(t *testing.T) {
			entry := toolingCoverageEntry()
			coverage := any([]any{entry})
			switch mutation {
			case "unknown":
				entry["invented"] = true
			case "bad-kind":
				entry["anchor"] = map[string]any{"kind": "invented", "value": "x"}
			case "bad-path":
				entry["recipe_path"] = "../outside.json"
			case "bad-case":
				entry["case_id"] = "case:bad space"
			case "duplicate":
				coverage = []any{entry, entry}
			case "conflict":
				other := toolingCoverageEntry()
				other["case_id"] = "case:other"
				coverage = []any{entry, other}
			case "null":
				coverage = nil
			case "overflow":
				entries := make([]any, 65)
				for i := range entries {
					entries[i] = entry
				}
				coverage = entries
			}
			if _, err := ParseProjectToolingManifest(toolingCoverageDocument("go test ./internal/demo", coverage)); err == nil {
				t.Fatal("invalid coverage accepted")
			}
		})
	}
	base := string(toolingCoverageDocument("go test ./internal/demo", []any{}))
	for _, raw := range []string{strings.Replace(base, `"schema_version":`, `"invented":true,"schema_version":`, 1), strings.Replace(base, `"invocation":`, `"invented":true,"invocation":`, 1)} {
		if _, err := ParseProjectToolingManifest([]byte(raw)); err == nil {
			t.Fatal("unknown manifest field accepted")
		}
	}
}

func TestProjectToolingCoverageDirectGrammar(t *testing.T) {
	for _, invocation := range []string{"bin/oc-test targeted -- go test ./internal/demo", "go test -run TestDemo ./internal/demo", "go test -short ./internal/demo", "go test -tags=skip ./internal/demo", "go test -exec=helper ./internal/demo", "go test -args ./internal/demo", "go test -unknown ./internal/demo", "bun test --test-name-pattern=Demo adapter/opencode/demo.test.ts", "bun test --todo adapter/opencode/demo.test.ts", "bun test adapter/opencode", "python3 -c pass", "python3 scripts/check-demo.py --skip"} {
		t.Run(invocation, func(t *testing.T) {
			if _, err := ParseProjectToolingManifest(toolingCoverageDocument(invocation, []any{toolingCoverageEntry()})); err == nil {
				t.Fatal("non-direct or selecting invocation gained coverage")
			}
		})
	}
}
