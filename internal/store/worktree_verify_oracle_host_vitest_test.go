package store

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func nativeVitestManifestForTest() (nativeTargetOracleManifest, OracleControl) {
	witnesses := []string{
		nativeTargetVitestIdentity("app", "tests/app.test.ts", []string{"outer > inner"}, "renders"),
		nativeTargetVitestIdentity("app", "tests/app.test.ts", []string{"outer", "inner"}, "renders"),
		nativeTargetVitestIdentity("server", "tests/server.test.ts", nil, "local server"),
	}
	slices.Sort(witnesses)
	m := nativeTargetOracleManifest{
		Kind: nativeTargetKind, Materializer: nativeTargetMaterializerBun, Runner: nativeTargetRunnerVitest, Root: ".",
		HarnessFiles: []string{"vitest.config.ts", "svelte.config.js", "tsconfig.json", "tests/app.test.ts", "tests/server.test.ts", "tests/setup.ts", "eslint.config.mjs", "check-eslint.mjs", "check-svelte.mjs"},
		SupportFiles: []string{}, Cases: map[string][]string{"case:fixture": witnesses}, VitestConfig: "vitest.config.ts", SvelteKit: true,
		Pin: nativeTargetPin{Bun: "1.4.0", Node: nativeTargetNodeVersion, Vitest: nativeTargetVitestVersion, Vite: nativeTargetViteVersion, DepManifest: "package.json", DepLock: "bun.lock"},
	}
	return m, OracleControl{Cwd: ".", CaseIDs: []string{"case:fixture"}, Argv: append([]string{"vitest"}, witnesses...)}
}

func TestNativeTargetVitestManifest(t *testing.T) {
	m, c := nativeVitestManifestForTest()
	if err := validateNativeTargetManifest(m, c); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeTargetSelector(m, c); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*nativeTargetOracleManifest){
		"foreign pair":     func(m *nativeTargetOracleManifest) { m.Materializer = "uv" },
		"no node":          func(m *nativeTargetOracleManifest) { m.Pin.Node = "" },
		"wrong node":       func(m *nativeTargetOracleManifest) { m.Pin.Node = "24.14.0" },
		"wrong vitest":     func(m *nativeTargetOracleManifest) { m.Pin.Vitest = "4.1.6" },
		"wrong vite":       func(m *nativeTargetOracleManifest) { m.Pin.Vite = "8.2.1" },
		"foreign pin":      func(m *nativeTargetOracleManifest) { m.Pin.Python = "3.13.7" },
		"un-pinned config": func(m *nativeTargetOracleManifest) { m.VitestConfig = "other.ts" },
		"checks": func(m *nativeTargetOracleManifest) {
			m.Checks = []nativeTargetCheck{{Witness: "x", Argv: []string{"node", "x.js"}}}
		},
		"display name": func(m *nativeTargetOracleManifest) {
			m.Cases["case:fixture"] = []string{"app > outer > inner > renders"}
		},
		"noncanonical": func(m *nativeTargetOracleManifest) {
			m.Cases["case:fixture"] = []string{`["app", "tests/app.test.ts",[],"renders"]`}
		},
		"un-pinned test": func(m *nativeTargetOracleManifest) {
			m.Cases["case:fixture"] = []string{`["app","other.test.ts",[],"renders"]`}
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := deepCopyNativeTargetManifest(t, m)
			mutate(&changed)
			if err := validateNativeTargetManifest(changed, c); err == nil {
				t.Fatal("invalid recipe admitted")
			}
		})
	}
	a := nativeTargetVitestIdentity("app", "tests/app.test.ts", []string{"outer > inner"}, "renders")
	b := nativeTargetVitestIdentity("app", "tests/app.test.ts", []string{"outer", "inner"}, "renders")
	if a == b {
		t.Fatal("structural ancestry collapsed into display name")
	}
}

func TestNativeTargetNodeCheckSlot(t *testing.T) {
	m, c := nativeVitestManifestForTest()
	m.Runner, m.VitestConfig = "check", ""
	m.Pin.Vitest, m.Pin.Vite = "", ""
	m.Checks = []nativeTargetCheck{{Witness: "eslint", Argv: []string{"node", "check-eslint.mjs"}}, {Witness: "svelte", Argv: []string{"node", "check-svelte.mjs"}}}
	m.Cases = map[string][]string{"case:fixture": {"eslint", "svelte"}}
	c.Argv = []string{"check", "eslint", "svelte"}
	if err := validateNativeTargetManifest(m, c); err != nil {
		t.Fatal(err)
	}
	p := &nativeTargetOraclePlan{Recipe: m, Environment: nativeTargetEnvironment{Node: "/pinned/node"}}
	stages, err := p.stages("prepare", []string{"eslint", "svelte"})
	if err != nil || len(stages) != 2 {
		t.Fatalf("stages %v %v", stages, err)
	}
	for _, stage := range stages {
		if !slices.Equal(stage.Argv[:2], []string{"node", "--check"}) {
			t.Fatalf("preparation executes check body: %v", stage)
		}
	}
	argv := nativeTargetStageArgv(p, "/scratch", []string{"node", "check-eslint.mjs", "node"})
	if !slices.Equal(argv, []string{"/pinned/node", "check-eslint.mjs", "node"}) {
		t.Fatal(argv)
	}
	for _, slot := range []string{"node", "bash", "python3"} {
		if err := validateNativeTargetCheckArgv([]string{slot, "--eval", "x"}); err == nil {
			t.Fatal("inline interpreter option admitted")
		}
	}
}

func TestNativeTargetVitestSchemaParity(t *testing.T) {
	data, err := os.ReadFile("../../contracts/agent-lane-packet.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var authored map[string]any
	if err := json.Unmarshal(data, &authored); err != nil {
		t.Fatal(err)
	}
	for _, schema := range []struct {
		document map[string]any
		name     string
	}{{authored, "lane_native_oracle_preparation"}, {payloadschema.Document(), "native_oracle_preparation"}} {
		defs := schema.document["$defs"].(map[string]any)
		preparation := defs[schema.name].(map[string]any)
		target := preparation["oneOf"].([]any)[1].(map[string]any)
		branches := target["oneOf"].([]any)
		if len(branches) != len(nativeTargetAdmittedPairs) {
			t.Fatalf("%s pair count %d != %d", schema.name, len(branches), len(nativeTargetAdmittedPairs))
		}
		pairs := []nativeTargetPair{}
		for _, raw := range branches {
			properties := raw.(map[string]any)["properties"].(map[string]any)
			pair := nativeTargetPair{properties["target_materializer"].(map[string]any)["const"].(string), properties["target_runner"].(map[string]any)["const"].(string)}
			if !slices.Contains(nativeTargetAdmittedPairs, pair) || slices.Contains(pairs, pair) {
				t.Fatalf("%s foreign/duplicate pair %v", schema.name, pair)
			}
			pairs = append(pairs, pair)
		}
	}
}

func TestNativeTargetNodeFingerprint(t *testing.T) {
	root := t.TempDir()
	node := filepath.Join(t.TempDir(), "node")
	if err := os.WriteFile(node, []byte("pinned executable bytes"), 0700); err != nil {
		t.Fatal(err)
	}
	p := &nativeTargetOraclePlan{Recipe: nativeTargetOracleManifest{Runner: nativeTargetRunnerCheck}, Environment: nativeTargetEnvironment{Node: node}}
	if err := nativeTargetInstallRunner(p, root, OracleControl{}); err != nil {
		t.Fatal(err)
	}
	before, err := nativeTargetNodeRunnerParts(p, root)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := nativeTargetToolDigests(p.Environment)
	if err != nil || len(tools) != 1 || tools[0].Path != "node" {
		t.Fatalf("Node is not in the pinned tool inventory: %v %v", tools, err)
	}
	if err := os.WriteFile(node, []byte("different executable bytes"), 0700); err != nil {
		t.Fatal(err)
	}
	after, err := nativeTargetNodeRunnerParts(p, root)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(bytes.Join(before, []byte{0}), bytes.Join(after, []byte{0})) {
		t.Fatal("Node executable drift disappeared from the runner fingerprint")
	}
	link := filepath.Join(nativeTargetNodeBinDir(root), "node")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/other/pinned/node", link); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeTargetNodeRunnerParts(p, root); err == nil {
		t.Fatal("Node link drift admitted")
	}
}

func TestNativeTargetVitestStructuredResults(t *testing.T) {
	m, _ := nativeVitestManifestForTest()
	selected := m.Cases["case:fixture"]
	good := nativeTargetVitestReport{Version: 1, Phase: "run", End: 1, Complete: true}
	for i, w := range selected {
		id := string(rune('a' + i))
		good.Collection = append(good.Collection, nativeTargetVitestRecord{ID: id, Witness: w, State: "run"})
		_, file, _ := nativeTargetVitestWitness(".", w)
		if file == "tests/app.test.ts" {
			state, result := "skip", "skipped"
			if w == selected[0] {
				state, result = "run", "passed"
			}
			good.Callbacks = append(good.Callbacks, nativeTargetVitestRecord{ID: id, Witness: w, State: state})
			good.Results = append(good.Results, nativeTargetVitestRecord{ID: id, Witness: w, State: result})
		}
	}
	if err := nativeTargetVerifyVitestReport(good, selected, selected[0]); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*nativeTargetVitestReport){
		"skip":              func(r *nativeTargetVitestReport) { r.Results[0].State = "skipped" },
		"todo":              func(r *nativeTargetVitestReport) { r.Collection[0].State = "todo" },
		"missing":           func(r *nativeTargetVitestReport) { r.Results = nil },
		"duplicate":         func(r *nativeTargetVitestReport) { r.Results = append(r.Results, r.Results[0]) },
		"collision":         func(r *nativeTargetVitestReport) { r.Collection[1].ID = r.Collection[0].ID },
		"extra":             func(r *nativeTargetVitestReport) { r.Results[1].Witness = "extra" },
		"incomplete":        func(r *nativeTargetVitestReport) { r.Complete = false },
		"no end":            func(r *nativeTargetVitestReport) { r.End = 0 },
		"extra end":         func(r *nativeTargetVitestReport) { r.End = 2 },
		"callback ancestry": func(r *nativeTargetVitestReport) { r.Callbacks[0].Witness = r.Callbacks[1].Witness },
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(good)
			var changed nativeTargetVitestReport
			_ = json.Unmarshal(raw, &changed)
			mutate(&changed)
			if err := nativeTargetVerifyVitestReport(changed, selected, selected[0]); err == nil {
				t.Fatal("invalid report admitted")
			}
		})
	}
	good.Phase, good.End = "collect", 0
	good.Callbacks, good.Results = nil, nil
	if err := nativeTargetVerifyVitestReport(good, selected, ""); err != nil {
		t.Fatal(err)
	}
	good.Results = []nativeTargetVitestRecord{{ID: "a", Witness: selected[0], State: "passed"}}
	if err := nativeTargetVerifyVitestReport(good, selected, ""); err == nil {
		t.Fatal("preparation body output admitted")
	}
	root := t.TempDir()
	for _, body := range []string{`{}`, `{"version":1,"unknown":true}`, `{} {}`, strings.Repeat(" ", nativeTargetVitestReportLimit+1)} {
		if err := os.WriteFile(filepath.Join(root, "report.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		r, err := nativeTargetReadVitestReport(root, "report.json")
		if err == nil {
			err = nativeTargetVerifyVitestReport(r, selected, "")
		}
		if err == nil {
			t.Fatal("malformed/bounded report admitted")
		}
	}
}

func TestNativeTargetSvelteKitStageOwnership(t *testing.T) {
	m, _ := nativeVitestManifestForTest()
	root := t.TempDir()
	snapshot := filepath.Join(root, "snapshot")
	if err := os.Mkdir(snapshot, 0700); err != nil {
		t.Fatal(err)
	}
	p := nativeOraclePlan{Target: &nativeTargetOraclePlan{Recipe: m}}
	n := nativeTargetOracleRun{root: root, cwd: snapshot, recipe: m}
	if err := n.freshGeneratedInputs(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "generated", "stale"), []byte("old stage"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeTargetInputInventory(p, snapshot, false); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeTargetInputInventory(p, snapshot, true); err == nil {
		t.Fatal("candidate generated slot admitted")
	}
	if err := n.freshGeneratedInputs(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "generated", "stale")); !os.IsNotExist(err) {
		t.Fatal("generated inputs reused between stages")
	}
	if err := os.Remove(filepath.Join(snapshot, ".svelte-kit")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(snapshot, ".svelte-kit")); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeTargetInputInventory(p, snapshot, false); err == nil {
		t.Fatal("redirected generated slot admitted")
	}
}

// This is the real pinned API and dependency closure, not a mock reporter.
// The explicit CI requirement turns missing offline preparation into failure.
func TestNativeTargetVitestFixture(t *testing.T) {
	m, c := nativeVitestManifestForTest()
	requiredIsolation := os.Getenv("OC_NATIVE_ISOLATION_REQUIRED") == "1"
	if requiredIsolation {
		m.Isolation = nativeIsolationLoopback
	}
	fixture := os.DirFS("../../testdata/native-vitest")
	manifest, err := fs.ReadFile(fixture, "package.json")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := fs.ReadFile(fixture, "bun.lock")
	if err != nil {
		t.Fatal(err)
	}
	installs, err := nativeBunPlanInstalls(manifest, lock)
	if err != nil {
		t.Fatal(err)
	}
	cache, err := nativeBunCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if configured := os.Getenv("OC_NATIVE_NPM_CACHE"); configured != "" {
		cache = filepath.Join(configured, "_cacache")
	}
	if err := nativeBunMissing(cache, installs); err != nil {
		if os.Getenv("OC_NATIVE_ISOLATION_REQUIRED") == "1" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	pair, _ := nativeTargetPairOf(m)
	environment, err := resolveNativeTargetRuntime(context.Background(), m, pair)
	if err != nil {
		t.Fatal(err)
	}
	environment.NPMCacheDir = cache
	preflight := &nativeTargetOraclePlan{Recipe: m, Environment: environment, DependencyFiles: []NativeOracleFile{
		{Path: path.Join(m.Root, m.Pin.DepLock), SHA256: nativeDigest(lock)},
		{Path: path.Join(m.Root, m.Pin.DepManifest), SHA256: nativeDigest(manifest)},
	}}
	if err := qualifyNativeIsolationArtifacts(context.Background(), preflight, map[string][]byte{m.Pin.DepManifest: manifest, m.Pin.DepLock: lock}); err != nil {
		t.Fatal(err)
	}
	environment = preflight.Environment
	var apiRoot, apiDigest string
	var apiTarget *nativeTargetOraclePlan
	for _, phase := range []string{"prepare", "execute"} {
		root, preserve := nativeIsolationFixtureScratch(t, requiredIsolation)
		t.Run(phase, func(t *testing.T) {
			snapshot := filepath.Join(root, "snapshot")
			if err := nativeScratchDirectories(root); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(snapshot, 0700); err != nil {
				t.Fatal(err)
			}
			if err := fs.WalkDir(fixture, ".", func(name string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return os.MkdirAll(filepath.Join(snapshot, name), 0700)
				}
				data, err := fs.ReadFile(fixture, name)
				if err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(snapshot, name), data, 0600)
			}); err != nil {
				t.Fatal(err)
			}
			target := &nativeTargetOraclePlan{Recipe: m, Environment: environment, DependencyFiles: []NativeOracleFile{
				{Path: path.Join(m.Root, m.Pin.DepLock), SHA256: nativeDigest(lock)},
				{Path: path.Join(m.Root, m.Pin.DepManifest), SHA256: nativeDigest(manifest)},
			}}
			p := nativeOraclePlan{Target: target}
			p.Bundle.Control = c
			baseline, err := nativeTargetInputInventory(p, snapshot, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := materializeNativeBunRun(context.Background(), target, root, snapshot); err != nil {
				t.Fatal(err)
			}
			if err := nativeTargetInstallRunner(target, root, c); err != nil {
				t.Fatal(err)
			}
			digest, err := nativeTargetRunnerDigest(target, root)
			if err != nil {
				t.Fatal(err)
			}
			p.Stages, err = target.stages(phase, m.Cases["case:fixture"])
			if err != nil {
				t.Fatal(err)
			}
			result := &NativeOracleResult{}
			n := nativeTargetOracleRun{store: &Store{}, req: WorktreeVerifyRequest{Oracle: &NativeOracleRequest{Phase: phase}, SupportedBudgetSeconds: 60}, plan: p, recipe: m, capture: &nativeStreamCapture{complete: true}, result: result, root: root, snapshot: snapshot, cwd: snapshot, env: nativeTargetControlledEnv(target, root), baseline: baseline, binaryDigest: digest, preserveScratch: preserve}
			if err := n.execute(context.Background()); err != nil {
				t.Fatalf("%s: %v\nstdout=%s\nstderr=%s", phase, err, n.capture.stdout, n.capture.stderr)
			}
			want := "pass"
			if phase == "prepare" {
				want = "ready"
			}
			if result.Qualification != want {
				t.Fatalf("qualification %s: %s %s", result.Qualification, n.capture.stdout, n.capture.stderr)
			}
			if requiredIsolation && !nativeIsolationResultMatches(p, &result.NativeOraclePreparation, result.Stages) {
				t.Fatal("fixture omitted observed isolation")
			}
			if phase == "prepare" && len(result.ObservedWitnessIDs) != 0 {
				t.Fatal("preparation executed acceptance")
			}
			if phase == "execute" {
				apiRoot, apiDigest, apiTarget = root, digest, target
			}
		})
	}
	if apiTarget != nil {
		t.Run("api-obligations", func(t *testing.T) {
			nativeTargetVitestAPI(t, filepath.Join(apiRoot, "snapshot", "node_modules"), requiredIsolation)
		})
		current, err := nativeTargetRunnerDigest(apiTarget, apiRoot)
		if err != nil || current != apiDigest {
			t.Fatalf("API qualification changed the verified dependency closure: %v", err)
		}
	}
}

// This separately qualifies the real public API when only a disposable
// dependency installation is available. It makes no archive-integrity claim.
func TestNativeTargetVitestAPI(t *testing.T) {
	modules := os.Getenv("TEST_CONCORD_NATIVE_VITEST_API_MODULES")
	if modules == "" {
		t.Skip("disposable API dependency tree not supplied; the verified-archive fixture owns CI qualification")
	}
	nativeTargetVitestAPI(t, modules, false)
}

func nativeTargetVitestAPI(t *testing.T, modules string, requiredIsolation bool) {
	t.Helper()
	m, c := nativeVitestManifestForTest()
	if requiredIsolation {
		m.Isolation = nativeIsolationLoopback
	}
	pair, _ := nativeTargetPairOf(m)
	environment, err := resolveNativeTargetRuntime(context.Background(), m, pair)
	if err != nil {
		t.Fatal(err)
	}
	if requiredIsolation {
		environment.NPMCacheDir, err = nativeBunCacheDir()
		if configured := os.Getenv("OC_NATIVE_NPM_CACHE"); configured != "" {
			environment.NPMCacheDir = filepath.Join(configured, "_cacache")
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"prepare", "execute", "eslint", "svelte", "test-fails", "eslint-fails", "svelte-fails", "skip", "todo", "duplicate", "extra", "un-pinned-setup", "un-pinned-plugin", "dynamic-skip"} {
		t.Run(name, func(t *testing.T) {
			phase, wantFailure := name, ""
			switch name {
			case "test-fails":
				phase, wantFailure = "execute", "incomplete or failed run"
			case "eslint-fails":
				phase, wantFailure = "eslint", "no-unused-vars"
			case "svelte-fails":
				phase, wantFailure = "svelte", "not assignable"
			case "skip", "todo":
				phase, wantFailure = "prepare", "collection executed or skipped a test"
			case "duplicate":
				phase, wantFailure = "prepare", "duplicate identity or API-ID collision"
			case "extra":
				phase, wantFailure = "prepare", "collection differs from selected population"
			case "un-pinned-setup", "un-pinned-plugin":
				phase, wantFailure = "prepare", "un-pinned configuration input"
			case "dynamic-skip":
				phase, wantFailure = "execute", "Vitest callbacks contain"
			}
			root, preserve := nativeIsolationFixtureScratch(t, requiredIsolation)
			snapshot := filepath.Join(root, "snapshot")
			if err := nativeScratchDirectories(root); err != nil {
				t.Fatal(err)
			}
			fixture := os.DirFS("../../testdata/native-vitest")
			if err := fs.WalkDir(fixture, ".", func(name string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return os.MkdirAll(filepath.Join(snapshot, name), 0700)
				}
				data, err := fs.ReadFile(fixture, name)
				if err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(snapshot, name), data, 0600)
			}); err != nil {
				t.Fatal(err)
			}
			if !requiredIsolation {
				if err := os.Symlink(modules, filepath.Join(snapshot, "node_modules")); err != nil {
					t.Fatal(err)
				}
			}
			if name == "prepare" || name == "test-fails" {
				if err := os.WriteFile(filepath.Join(snapshot, "src", "value.ts"), []byte("export const value = 8;\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "eslint-fails" || name == "svelte-fails" {
				body := "const unused = 1; export const value = 7;\n"
				if name == "svelte-fails" {
					body = "export const value: string = 7;\n"
				}
				if err := os.WriteFile(filepath.Join(snapshot, "src", "value.ts"), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if slices.Contains([]string{"skip", "todo", "duplicate", "extra", "dynamic-skip"}, name) {
				file := filepath.Join(snapshot, "tests", "server.test.ts")
				body, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				text := string(body)
				switch name {
				case "skip":
					text = strings.Replace(text, "test(", "test.skip(", 1)
				case "todo":
					text = strings.Replace(text, "test(", "test.todo(", 1)
				case "duplicate":
					text += "\ntest('local server', () => {});\n"
				case "extra":
					text += "\ntest('extra', () => {});\n"
				case "dynamic-skip":
					text = strings.Replace(text, "async () => {", "async (context) => { context.skip();", 1)
				}
				if err := os.WriteFile(file, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "un-pinned-setup" || name == "un-pinned-plugin" {
				file := filepath.Join(snapshot, "vitest.config.ts")
				body, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				text := string(body)
				if name == "un-pinned-setup" {
					text = strings.Replace(text, "setupFiles: ['tests/setup.ts']", "setupFiles: ['tests/unpinned.ts']", 1)
				} else {
					text = "import './tests/unpinned.ts';\n" + text
				}
				if err := os.WriteFile(filepath.Join(snapshot, "tests", "unpinned.ts"), []byte("export {};\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			target := &nativeTargetOraclePlan{Recipe: m, Environment: environment}
			if requiredIsolation {
				for _, name := range []string{m.Pin.DepManifest, m.Pin.DepLock} {
					data, err := os.ReadFile(filepath.Join(snapshot, name))
					if err != nil {
						t.Fatal(err)
					}
					target.DependencyFiles = append(target.DependencyFiles, NativeOracleFile{Path: name, SHA256: nativeDigest(data)})
				}
				if err := materializeNativeBunRun(context.Background(), target, root, snapshot); err != nil {
					t.Fatal(err)
				}
				if err := nativeIsolationBindTargetExports(target, snapshot); err != nil {
					t.Fatal(err)
				}
			}
			if err := nativeTargetInstallRunner(target, root, c); err != nil {
				t.Fatal(err)
			}
			p := nativeOraclePlan{Target: target}
			p.Bundle.Control = c
			var stages []NativeOracleStage
			if phase == "eslint" || phase == "svelte" {
				stages = []NativeOracleStage{{Name: "test", Argv: []string{"node", "check-" + phase + ".mjs"}}}
			} else {
				stages, err = target.stages(phase, m.Cases["case:fixture"])
				if err != nil {
					t.Fatal(err)
				}
			}
			n := nativeTargetOracleRun{store: &Store{}, req: WorktreeVerifyRequest{Oracle: &NativeOracleRequest{Phase: phase}, SupportedBudgetSeconds: 60}, plan: p, recipe: m, capture: &nativeStreamCapture{complete: true}, result: &NativeOracleResult{}, root: root, snapshot: snapshot, cwd: snapshot, env: nativeTargetControlledEnv(target, root), preserveScratch: preserve}
			if requiredIsolation {
				n.baseline, err = nativeTargetInputInventory(p, snapshot, false)
				if err != nil {
					t.Fatal(err)
				}
				n.binaryDigest, err = nativeTargetRunnerDigest(target, root)
				if err != nil {
					t.Fatal(err)
				}
			}
			failed := false
			for _, stage := range stages {
				if err := n.freshGeneratedInputs(); err != nil {
					t.Fatal(err)
				}
				exit, stdout, err := n.runStage(context.Background(), stage)
				if err == nil && exit == 0 && (phase == "prepare" || phase == "execute") {
					err = n.consumeStage(context.Background(), stage, stdout)
				}
				if err != nil || exit != 0 {
					diagnostic := string(n.capture.stdout) + string(n.capture.stderr)
					if err != nil {
						diagnostic += err.Error()
					}
					if wantFailure == "" || !strings.Contains(diagnostic, wantFailure) {
						t.Fatalf("API stage exit=%d err=%v\n%s", exit, err, diagnostic)
					}
					failed = true
					break
				}
			}
			if wantFailure != "" && !failed {
				t.Fatal("invalid native workload was accepted")
			}
		})
	}
}
