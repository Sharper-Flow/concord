package store

import (
	"archive/tar"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// bunFixturePackage is one synthetic registry package the bun fixture
// locks, caches, and installs.
type bunFixturePackage struct {
	key, name, version string
	deps               map[string]string
	extra              map[string]any
	files              map[string]string
}

// bunFixtureCheck is one pinned check of the bun fixture.
type bunFixtureCheck struct {
	witness string
	argv    []string
}

// nativeTargetBunDefaultScript is the default bun harness: it proves the
// bare bun is the per-run link and that candidate .env files never reached
// the process, then runs the pinned check under bun.
const nativeTargetBunDefaultScript = "#!/usr/bin/env bash\nset -euo pipefail\ntest \"$(command -v bun)\" = \"${TMPDIR%/tmp}/bin/bun\"\ntest -z \"${CANDIDATE_ENV:-}\"\nbun run scripts/check.js \"$1\"\n"

// nativeTargetBunCheckJS requires a nested dependency (left resolves its
// own inner@1) and an aliased one (alias is right@2, which resolves the
// hoisted inner@2), so both lookup paths decide the result.
const nativeTargetBunCheckJS = "const left = require(\"left\");\nconst right = require(\"alias\");\nconst value = Number(require(\"fs\").readFileSync(\"value.txt\", \"utf8\").trim());\nprocess.exit(left === 2 && right === 20 && value === Number(process.argv[2]) ? 0 : 1);\n"

// bunFixturePackagesForTest returns the fixture closure. Every lifecycle
// script touches marker, so any script run is observable.
func bunFixturePackagesForTest(marker string) []bunFixturePackage {
	touch := "touch " + marker
	return []bunFixturePackage{
		{key: "left", name: "left", version: "1.0.0", deps: map[string]string{"inner": "^1.0.0"},
			extra: map[string]any{"main": "index.js", "scripts": map[string]string{"preinstall": touch, "install": touch, "postinstall": touch, "prepare": touch}},
			files: map[string]string{"index.js": "module.exports = require(\"inner\") + 1;\n"}},
		{key: "left/inner", name: "inner", version: "1.0.0", files: map[string]string{"index.js": "module.exports = 1;\n"}},
		{key: "alias", name: "right", version: "2.0.0", deps: map[string]string{"inner": "^2.0.0"},
			files: map[string]string{"index.js": "module.exports = require(\"inner\") * 10;\n"}},
		{key: "inner", name: "inner", version: "2.0.0", extra: map[string]any{"scripts": map[string]string{"postinstall": touch}},
			files: map[string]string{"index.js": "module.exports = 2;\n"}},
	}
}

// bunFixtureForTest carries the bun fixture's private npm cache beside the
// shared target fixture.
type bunFixtureForTest struct {
	*nativeTargetFixture
	cache, marker string
	integrity     map[string]string
}

func newNativeTargetBunFixture(t *testing.T, checks []bunFixtureCheck, files map[string]string) *bunFixtureForTest {
	t.Helper()
	const work = "native-target-bun-work"
	seed := seedWorkflowReturnRouteFixture(t, work, "workflow.break_fix", "repair")
	home := t.TempDir()
	t.Setenv("HOME", home)
	f := &bunFixtureForTest{
		nativeTargetFixture: &nativeTargetFixture{s: seed.store, work: work, repo: t.TempDir(), actor: seed.owner, root: "plugin", recipePath: "plugin/recipe.json", materializer: "bun", runner: "check"},
		cache:               filepath.Join(home, ".npm", "_cacache"),
		marker:              filepath.Join(t.TempDir(), "script-ran"),
		integrity:           map[string]string{},
	}
	if checks == nil {
		checks = []bunFixtureCheck{{witness: "bun-value", argv: []string{"bash", "scripts/check.sh", "7"}}}
	}
	f.controlArgv = []string{"check"}
	var witnesses []string
	var manifestChecks []map[string]any
	for _, c := range checks {
		f.controlArgv = append(f.controlArgv, c.witness)
		witnesses = append(witnesses, c.witness)
		manifestChecks = append(manifestChecks, map[string]any{"witness": c.witness, "argv": c.argv})
	}
	slices.Sort(f.controlArgv[1:])
	tuples := map[string][]any{}
	for _, pkg := range bunFixturePackagesForTest(f.marker) {
		manifest := map[string]any{"name": pkg.name, "version": pkg.version}
		for k, v := range pkg.extra {
			manifest[k] = v
		}
		meta := map[string]any{}
		if pkg.deps != nil {
			manifest["dependencies"] = pkg.deps
			meta["dependencies"] = pkg.deps
		}
		entries := []bunTarEntryForTest{{name: "package/package.json", typeflag: tar.TypeReg, body: bunPackageJSONForTest(t, manifest)}}
		for _, name := range slices.Sorted(maps.Keys(pkg.files)) {
			entries = append(entries, bunTarEntryForTest{name: "package/" + name, typeflag: tar.TypeReg, body: pkg.files[name]})
		}
		integrity := writeBunCacheForTest(t, f.cache, bunTarballForTest(t, entries))
		f.integrity[pkg.key] = integrity
		tuples[pkg.key] = []any{pkg.name + "@" + pkg.version, "", meta, integrity}
	}
	root := map[string]any{"name": "bunfix", "dependencies": map[string]string{"left": "^1.0.0", "alias": "npm:right@^2.0.0"}}
	packageJSON := bunPackageJSONForTest(t, root)
	shellVersion := liveVersionForTest(t, "bash", `version ([0-9][0-9.]*)`)
	harness := []string{}
	for _, c := range checks {
		if !slices.Contains(harness, "plugin/"+c.argv[1]) {
			harness = append(harness, "plugin/"+c.argv[1])
		}
	}
	raw, err := json.Marshal(map[string]any{
		"kind":          "target_runner_v1",
		"materializer":  "bun",
		"runner":        "check",
		"root":          f.root,
		"harness_files": harness,
		"support_files": []string{"plugin/scripts/check.js"},
		"cases":         map[string][]string{"case:fixture": witnesses},
		"checks":        manifestChecks,
		"pin":           map[string]string{"bun": "1.4.0", "shell": shellVersion, "dep_manifest": "package.json", "dep_lock": "bun.lock"},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.manifestJSON = string(raw)
	all := map[string]string{
		"plugin/value.txt":        "7\n",
		"plugin/package.json":     packageJSON,
		"plugin/bun.lock":         bunLockForTest(t, root, tuples),
		"plugin/scripts/check.sh": nativeTargetBunDefaultScript,
		"plugin/scripts/check.js": nativeTargetBunCheckJS,
		f.recipePath:              f.manifestJSON,
	}
	for name, data := range files {
		all[name] = data
	}
	writeRepoFilesForTest(t, f.repo, all)
	f.commitAll(t, "pinned bun check harness")
	f.seedAuthority(t, seed)
	return f
}

func (f *bunFixtureForTest) cacheDigest(t *testing.T) string {
	t.Helper()
	digest, err := nativeBunTreeDigest(f.cache)
	if err != nil {
		t.Fatal(err)
	}
	return string(digest)
}

// executeErr runs one authorized execute and returns its refusal.
func (f *bunFixtureForTest) executeErr(t *testing.T, id string) error {
	t.Helper()
	o := f.authorize(t, id, f.actor)
	_, err := f.s.VerifyWorktree(context.Background(), f.request(id+"-execute", &o))
	return err
}

// The bun materializer installs a nested and an aliased dependency from
// integrity-verified cached tarballs, runs no package script, leaves the
// shared cache untouched, and grades the check through the pinned bun
// link; a production break fails truthfully.
func TestNativeTargetAxisBunCheck(t *testing.T) {
	var observed [][]string
	f := newNativeTargetBunFixture(t, nil, map[string]string{
		"plugin/bunfig.toml":         "preload = [\"./scripts/poison.js\"]\n",
		"plugin/.env":                "CANDIDATE_ENV=1\n",
		"plugin/sub/.env.production": "CANDIDATE_ENV=1\n",
		"plugin/scripts/poison.js":   "process.exit(3);\n",
	})
	before := f.cacheDigest(t)
	f.s.nativeOracleStageObserved = func(_ string, _ string, _ []string, env []string) { observed = append(observed, env) }
	r, err := f.s.VerifyWorktree(context.Background(), f.request("execute", &f.a))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || f.launches != 1 {
		t.Fatalf("bun+check execute %+v %v launches=%d", r, err, f.launches)
	}
	assertNativeTargetReceipt(t, r, "bun", "check", "plugin")
	if _, err := os.Stat(f.marker); err == nil {
		t.Fatal("a package lifecycle script ran")
	}
	if f.cacheDigest(t) != before {
		t.Fatal("materialization changed the shared npm cache")
	}
	if len(observed) == 0 {
		t.Fatal("no stage environment observed")
	}
	for _, env := range observed {
		var path string
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, "PATH="); ok {
				path = v
			}
		}
		first, _, _ := strings.Cut(path, string(os.PathListSeparator))
		if filepath.Base(first) != "bin" || !slices.Contains(env, "BUN_RUNTIME_TRANSPILER_CACHE_PATH=0") || !slices.Contains(env, "DO_NOT_TRACK=1") || !slices.ContainsFunc(env, func(kv string) bool {
			return strings.HasPrefix(kv, "BUN_INSTALL_CACHE_DIR=") && strings.HasSuffix(kv, "/bun-cache")
		}) {
			t.Fatalf("bun stage environment %v", env)
		}
	}
	if err := os.WriteFile(filepath.Join(f.repo, "plugin", "value.txt"), []byte("8\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.commitAndPrepare(t, "broken")
	o := f.authorize(t, "broken", f.actor)
	r, err = f.s.VerifyWorktree(context.Background(), f.request("broken-execute", &o))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "fail" || r.ExitCode == 0 {
		t.Fatalf("bun+check broken candidate %+v %v", r, err)
	}
	if err := validateNativeOracleProducerTx(context.Background(), f.s.db, f.work, &r, "fail"); err != nil {
		t.Fatalf("bun+check truthful failure receipt refused: %v", err)
	}
}

// A locked package missing from the cache refuses preparation and names
// the recovery; a corrupt entry refuses execution by its locked sha512.
func TestNativeTargetAxisBunCacheEntries(t *testing.T) {
	f := newNativeTargetBunFixture(t, nil, nil)
	sum, _ := decodeBunIntegrityForTest(f.integrity["left/inner"])
	content := nativeBunCacheContent(f.cache, sum)
	good, err := os.ReadFile(content)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(content, append(good, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.executeErr(t, "corrupt"); err == nil || !strings.Contains(err.Error(), "corrupt cache entry: inner@1.0.0") {
		t.Fatalf("corrupt cache entry was not refused: %v", err)
	}
	if err := os.Remove(content); err != nil {
		t.Fatal(err)
	}
	prepare := NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &f.bundle}
	_, err = f.s.VerifyWorktree(context.Background(), f.request("missing-prepare", &prepare))
	if err == nil || !strings.Contains(err.Error(), "1 locked packages are missing from the npm cache: inner@1.0.0") || !strings.Contains(err.Error(), "npm cache add --ignore-scripts inner@1.0.0") {
		t.Fatalf("missing cache entry was not refused at preparation: %v", err)
	}
	if f.launches != 0 {
		t.Fatalf("cache refusal launched the acceptance body: %d", f.launches)
	}
}

func decodeBunIntegrityForTest(integrity string) ([]byte, error) {
	lock, err := parseNativeBunPackageForPlatform("x", []json.RawMessage{json.RawMessage(`"x@1.0.0"`), json.RawMessage(`""`), json.RawMessage(`{}`), json.RawMessage(`"` + integrity + `"`)}, false)
	return lock.Integrity, err
}

// Candidate dependency inputs that differ from the recipe commit refuse, and
// so does any committed node_modules tree, wherever it sits.
func TestNativeTargetAxisBunCandidateInputs(t *testing.T) {
	f := newNativeTargetBunFixture(t, nil, nil)
	if err := os.WriteFile(filepath.Join(f.repo, "plugin", "package.json"), []byte(`{"name":"bunfix","dependencies":{"left":"^1.0.0"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	f.commitAll(t, "candidate drops a dependency")
	if _, err := f.s.VerifyWorktree(context.Background(), f.request("deps-subject", nil)); err != nil {
		t.Fatal(err)
	}
	if err := f.executeErr(t, "deps"); err == nil || !strings.Contains(err.Error(), "candidate dependency inputs differ from pinned preparation") {
		t.Fatalf("differing dependency inputs were not refused: %v", err)
	}
	gitRunStore(t, f.repo, "checkout", "HEAD~1", "--", "plugin/package.json")
	for i, tree := range []string{"plugin/node_modules/left/index.js", "plugin/lib/node_modules/x/index.js", "plugin/.venv/node_modules/x/index.js"} {
		writeRepoFilesForTest(t, f.repo, map[string]string{tree: "module.exports = 0;\n"})
		f.commitAndPrepare(t, "tree-"+strconv.Itoa(i))
		if err := f.executeErr(t, "tree-"+strconv.Itoa(i)); err == nil || !strings.Contains(err.Error(), "candidate carries a materialized dependency or cache tree") {
			t.Fatalf("committed %s was not refused: %v", tree, err)
		}
		gitRunStore(t, f.repo, "rm", "-q", "-r", filepath.Dir(tree))
	}
	if f.launches != 0 {
		t.Fatalf("candidate-input refusals launched the acceptance body: %d", f.launches)
	}
}

// Each run-time write to the bun closure refuses: the materialized tree, an
// extra node_modules (empty or inside an excluded subtree), the bun link,
// and the private install cache.
func TestNativeTargetAxisBunFingerprint(t *testing.T) {
	for name, c := range map[string]struct{ script, refusal string }{
		"tree write":       {"echo // >> node_modules/left/index.js", "dependency closure changed during execution"},
		"empty extra tree": {"mkdir -p lib/node_modules", "candidate carries a materialized dependency or cache tree: plugin/lib/node_modules"},
		"hidden tree":      {"mkdir -p .venv/node_modules", "candidate carries a materialized dependency or cache tree: plugin/.venv/node_modules"},
		"bun link":         {"ln -sfn /bin/true \"${TMPDIR%/tmp}/bin/bun\"", "bun link no longer names the pinned bun"},
		"install cache":    {"touch \"$BUN_INSTALL_CACHE_DIR/auto\"", "auto-installed packages into the private bun install cache"},
		"bin shadow":       {"ln -s /bin/true \"${TMPDIR%/tmp}/bin/node\"", "the per-run bin directory must hold only the pinned bun link"},
	} {
		t.Run(name, func(t *testing.T) {
			script := nativeTargetBunDefaultScript + c.script + "\n"
			f := newNativeTargetBunFixture(t, nil, map[string]string{"plugin/scripts/check.sh": script})
			if err := f.executeErr(t, "write"); err == nil || !strings.Contains(err.Error(), c.refusal) {
				t.Fatalf("%s was not refused: %v", name, err)
			}
		})
	}
	// Between two tests the runner digest is rechecked before the second
	// launch, so the first check's write never reaches it.
	f := newNativeTargetBunFixture(t, []bunFixtureCheck{
		{witness: "first", argv: []string{"bash", "scripts/write.sh"}},
		{witness: "second", argv: []string{"bash", "scripts/check.sh", "7"}},
	}, map[string]string{"plugin/scripts/write.sh": "#!/usr/bin/env bash\necho // >> node_modules/left/index.js\n"})
	if err := f.executeErr(t, "between"); err == nil || !strings.Contains(err.Error(), "consumed dependency closure changed before launch") {
		t.Fatalf("write between tests was not refused: %v", err)
	}
	if f.launches != 1 {
		t.Fatalf("between-test refusal launches=%d, want 1", f.launches)
	}
	// Preparation runs only the check-input stage, but its closure is
	// rechecked the same way: a write during preparation refuses readiness.
	f = newNativeTargetBunFixture(t, nil, nil)
	f.s.nativeOracleStageObserved = func(stage, cwd string, _ []string, _ []string) {
		if stage == "check_inputs" {
			if err := os.WriteFile(filepath.Join(cwd, "node_modules", "left", "index.js"), []byte("module.exports = 2;\n"), 0644); err != nil {
				t.Error(err)
			}
		}
	}
	prepare := NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &f.bundle}
	if _, err := f.s.VerifyWorktree(context.Background(), f.request("hooked-prepare", &prepare)); err == nil || !strings.Contains(err.Error(), "dependency closure changed during execution") {
		t.Fatalf("preparation-time closure write was not refused: %v", err)
	}
	if f.launches != 0 {
		t.Fatalf("preparation launched the acceptance body: %d", f.launches)
	}
}

// Only validated bun runtimes are admitted as pins.
func TestNativeTargetAxisBunVersionPin(t *testing.T) {
	f := newNativeTargetBunFixture(t, nil, nil)
	f.mutateRecipePin(t, map[string]any{"bun": "1.3.0"})
	f.commitAll(t, "unvalidated bun pin")
	b := f.bundle
	b.Control.RecipeSource.CommitOID = f.head
	if _, err := f.s.VerifyWorktree(context.Background(), f.request("pin-subject", nil)); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.VerifyWorktree(context.Background(), f.request("pin-prepare", &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &b}))
	if err == nil || !strings.Contains(err.Error(), "bun 1.3.0 is not a validated materializer runtime") {
		t.Fatalf("unvalidated bun pin admitted: %v", err)
	}
}

// A none recipe whose run creates node_modules inside the candidate tree is
// refused, and the refusal names the first changed path and the route.
func TestNativeTargetAxisNoneCreatesNodeModules(t *testing.T) {
	f := newNativeTargetCheckFixture(t)
	writeRepoFilesForTest(t, f.repo, map[string]string{
		nativeTargetFixtureName(f.root, "scripts/check.sh"): "#!/usr/bin/env bash\nset -euo pipefail\nmkdir -p node_modules/dep\necho 1 > node_modules/dep/index.js\n",
	})
	f.commitAndPrepareRepin(t, "installing")
	o := f.authorize(t, "installing", f.actor)
	_, err := f.s.VerifyWorktree(context.Background(), f.request("installing-execute", &o))
	want := "chkfix/node_modules/dep/index.js; the run created or changed chkfix/node_modules/dep/index.js inside the candidate tree; a recipe that needs dependencies declares a dependency materializer"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("none-recipe node_modules creation refusal %v, want %q", err, want)
	}
}

// The pinned bun writes an auto-installed package to BUN_INSTALL_CACHE_DIR
// even when bunfig names another cache directory, so the empty private
// install cache detects any auto-install. The host bun must be a validated
// runtime.
func TestNativeTargetBunAutoInstallConformance(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Fatalf("bun is required: %v", err)
	}
	out, err := exec.Command(bun, "--version").Output() //nolint:gosec // resolved test executable, fixed argv.
	if err != nil || !slices.Contains(nativeBunAdmittedVersions, strings.TrimSpace(string(out))) {
		t.Fatalf("host bun %q %v is not a validated runtime %v", out, err, nativeBunAdmittedVersions)
	}
	tarball := bunTarballForTest(t, []bunTarEntryForTest{
		{name: "package/package.json", typeflag: tar.TypeReg, body: `{"name":"autodep","version":"1.0.0","main":"index.js"}`},
		{name: "package/index.js", typeflag: tar.TypeReg, body: "module.exports = 42;\n"},
	})
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimSuffix(r.URL.Path, "/") {
		case "/autodep":
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "autodep", "dist-tags": map[string]string{"latest": "1.0.0"}, "versions": map[string]any{"1.0.0": map[string]any{"name": "autodep", "version": "1.0.0", "dist": map[string]string{"tarball": server.URL + "/autodep/-/autodep-1.0.0.tgz", "integrity": bunIntegrityForTest(tarball)}}}})
		case "/autodep/-/autodep-1.0.0.tgz":
			_, _ = w.Write(tarball)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	other := filepath.Join(root, "bunfig-cache")
	for _, dir := range []string{project, filepath.Join(root, "home"), filepath.Join(root, "tmp"), nativeBunInstallCacheDir(root)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeRepoFilesForTest(t, project, map[string]string{
		"main.js":     "const v = require(\"autodep\");\nif (v !== 42) process.exit(1);\n",
		"bunfig.toml": "[install]\nregistry = \"" + server.URL + "/\"\n[install.cache]\ndir = \"" + other + "\"\n",
	})
	cmd := exec.Command(bun, "run", "main.js") //nolint:gosec // resolved test executable, fixed argv.
	cmd.Dir = project
	cmd.Env = nativeTargetControlledEnv(&nativeTargetOraclePlan{Environment: nativeTargetEnvironment{Materializer: nativeTargetMaterializerBun}}, root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("auto-install probe failed: %v: %s", err, out)
	}
	entries, err := os.ReadDir(nativeBunInstallCacheDir(root))
	if err != nil || len(entries) == 0 {
		t.Fatalf("auto-install left the private install cache empty: %v", err)
	}
	if _, err := os.Stat(other); err == nil {
		t.Fatal("auto-install wrote to the bunfig cache directory")
	}
}
