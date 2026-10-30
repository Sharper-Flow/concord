package store

import (
	"archive/tar"
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func bunPlanMetadataForTest(t *testing.T, manifest, metadata map[string]any, packages map[string][]any) ([]nativeBunInstall, error) {
	t.Helper()
	root := maps.Clone(manifest)
	delete(root, "overrides")
	delete(root, "trustedDependencies")
	text := bunLockForTest(t, root, packages)
	for key, value := range metadata {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		text = strings.Replace(text, "{", "{\n"+strconv.Quote(key)+":"+string(raw)+",", 1)
	}
	return nativeBunPlanInstalls([]byte(bunPackageJSONForTest(t, manifest)), []byte(text))
}

func TestNativeBunSimpleOverrides(t *testing.T) {
	manifest := map[string]any{"name": "fixture", "dependencies": map[string]string{"left": "1", "shared": "^1"}, "overrides": map[string]string{"shared": "^2"}}
	metadata := map[string]any{"overrides": manifest["overrides"]}
	packages := map[string][]any{
		"left":   {"left@1.0.0", "", map[string]any{"dependencies": map[string]string{"shared": "^1"}, "optionalDependencies": map[string]string{"optional": "1"}}, bunTestIntegrity},
		"shared": {"shared@2.1.0", "", map[string]any{}, bunTestIntegrity},
	}
	if installs, err := bunPlanMetadataForTest(t, manifest, metadata, packages); err != nil || len(installs) != 2 {
		t.Fatalf("simple override of root and transitive ranges refused: %v, %v", installs, err)
	}
	for name, mutate := range map[string]func(map[string]any, map[string]any, map[string][]any){
		"missing lock overrides":     func(m, l map[string]any, p map[string][]any) { delete(l, "overrides") },
		"missing manifest overrides": func(m, l map[string]any, p map[string][]any) { delete(m, "overrides") },
		"different ranges":           func(m, l map[string]any, p map[string][]any) { l["overrides"] = map[string]string{"shared": "2"} },
		"ineffective override": func(m, l map[string]any, p map[string][]any) {
			p["shared"] = []any{"shared@1.0.0", "", map[string]any{}, bunTestIntegrity}
		},
		"optional override": func(m, l map[string]any, p map[string][]any) {
			m["overrides"] = map[string]string{"shared": "^2", "optional": "2"}
			l["overrides"] = m["overrides"]
			p["optional"] = []any{"optional@1.0.0", "", map[string]any{}, bunTestIntegrity}
		},
		"peer override": func(m, l map[string]any, p map[string][]any) {
			m["peerDependencies"] = map[string]string{"optional": "1"}
			m["overrides"] = map[string]string{"shared": "^2", "optional": "2"}
			l["overrides"] = m["overrides"]
			p["optional"] = []any{"optional@1.0.0", "", map[string]any{}, bunTestIntegrity}
		},
		"unsupported original declaration": func(m, l map[string]any, p map[string][]any) {
			p["left"] = []any{"left@1.0.0", "", map[string]any{"dependencies": map[string]string{"shared": "git+https://example.invalid/shared.git"}}, bunTestIntegrity}
		},
	} {
		t.Run(name, func(t *testing.T) {
			m, l, p := maps.Clone(manifest), maps.Clone(metadata), maps.Clone(packages)
			mutate(m, l, p)
			if _, err := bunPlanMetadataForTest(t, m, l, p); err == nil {
				t.Fatal("invalid overrides admitted")
			}
		})
	}
	for _, value := range []any{nil, []string{"shared"}, map[string]any{"shared": map[string]string{".": "2"}}, map[string]string{"shared@1": "2"}, map[string]string{"left>shared": "2"}, map[string]string{"shared": "latest"}, map[string]string{"shared": "file:../shared"}, map[string]string{"shared": "npm:other@2"}} {
		m, l := maps.Clone(manifest), maps.Clone(metadata)
		m["overrides"], l["overrides"] = value, value
		if _, err := bunPlanMetadataForTest(t, m, l, packages); err == nil {
			t.Errorf("unsupported override admitted: %#v", value)
		}
	}
	// Bun does not apply overrides to npm aliases, even when the alias name matches.
	manifest["dependencies"] = map[string]string{"shared": "npm:shared@^1"}
	packages = map[string][]any{"shared": {"shared@1.0.0", "", map[string]any{}, bunTestIntegrity}}
	if _, err := bunPlanMetadataForTest(t, manifest, metadata, packages); err != nil {
		t.Fatalf("alias incorrectly overridden: %v", err)
	}
	manifest["dependencies"] = map[string]string{"alias": "npm:shared@^1"}
	manifest["overrides"] = map[string]string{"shared": "^2", "alias": "^3"}
	metadata["overrides"] = manifest["overrides"]
	packages = map[string][]any{"alias": {"shared@1.0.0", "", map[string]any{}, bunTestIntegrity}}
	if _, err := bunPlanMetadataForTest(t, manifest, metadata, packages); err != nil {
		t.Fatalf("alias target incorrectly overridden: %v", err)
	}
}

func TestNativeBunTrustedMetadata(t *testing.T) {
	manifest := map[string]any{"name": "fixture", "dependencies": map[string]string{"left": "1"}, "trustedDependencies": []string{"left", "@scope/tool"}}
	metadata := map[string]any{"trustedDependencies": []string{"@scope/tool", "left"}}
	packages := map[string][]any{"left": {"left@1.0.0", "", map[string]any{}, bunTestIntegrity}}
	if _, err := bunPlanMetadataForTest(t, manifest, metadata, packages); err != nil {
		t.Fatalf("matching trust set refused: %v", err)
	}
	for _, value := range []any{nil, "left", []string{"left"}, []string{"left", "left"}, []string{"../left"}} {
		l := maps.Clone(metadata)
		l["trustedDependencies"] = value
		if _, err := bunPlanMetadataForTest(t, manifest, l, packages); err == nil {
			t.Errorf("malformed or stale trust admitted: %#v", value)
		}
	}
	if _, err := bunPlanMetadataForTest(t, manifest, nil, packages); err == nil {
		t.Fatal("missing declared trust admitted")
	}
	delete(manifest, "trustedDependencies")
	if _, err := bunPlanMetadataForTest(t, manifest, metadata, packages); err == nil {
		t.Fatal("lock-only trust admitted")
	}
	manifest["trustedDependencies"] = []string{}
	if _, err := bunPlanMetadataForTest(t, manifest, nil, packages); err != nil {
		t.Fatalf("empty trust set omitted by Bun's serializer refused: %v", err)
	}
}

func TestNativeBunPrunesUnselectedBundles(t *testing.T) {
	root := map[string]any{"name": "fixture", "optionalDependencies": map[string]string{"@scope/mac": "1"}}
	packages := map[string][]any{
		"@scope/mac":              {"@scope/mac@1.0.0", "", map[string]any{"os": "darwin", "dependencies": map[string]string{"bundled": "1"}}, bunTestIntegrity},
		"@scope/mac/bundled":      {"bundled@1.0.0", "", map[string]any{"bundled": true}, "sha1-AAAAAAAAAAAAAAAAAAAAAAAAAAA="},
		"@scope/mac/bundled/deep": {"deep@1.0.0", "", map[string]any{}, bunTestIntegrity},
	}
	if installs, err := bunVerifyForTest(t, root, root, packages); err != nil || len(installs) != 0 {
		t.Fatalf("unselected platform subtree not pruned: %v, %v", installs, err)
	}
	for name, mutate := range map[string]func(map[string][]any){
		"selected parent": func(p map[string][]any) {
			p["@scope/mac"] = []any{"@scope/mac@1.0.0", "", map[string]any{"os": "linux"}, bunTestIntegrity}
		},
		"missing parent": func(p map[string][]any) { delete(p, "@scope/mac") },
		"unrelated prefix": func(p map[string][]any) {
			p["@scope/mac-other/bundled"] = p["@scope/mac/bundled"]
			delete(p, "@scope/mac/bundled")
		},
		"unknown bundle metadata": func(p map[string][]any) {
			p["@scope/mac/bundled"] = []any{"bundled@1.0.0", "", map[string]any{"bundled": true, "unknown": true}, "sha1-x"}
		},
		"invalid bundle key": func(p map[string][]any) {
			p["@scope/mac/../bundled"] = p["@scope/mac/bundled"]
		},
		"bundle flag false": func(p map[string][]any) {
			p["@scope/mac/bundled"] = []any{"bundled@1.0.0", "", map[string]any{"bundled": false}, bunTestIntegrity}
		},
		"unselected bundle without ancestor": func(p map[string][]any) {
			p["orphan"] = []any{"orphan@1.0.0", "", map[string]any{"bundled": true, "os": "darwin"}, bunTestIntegrity}
		},
		"malformed parent": func(p map[string][]any) {
			p["@scope/mac"] = []any{"@scope/mac@1.0.0", "", map[string]any{"os": 42}, bunTestIntegrity}
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := maps.Clone(packages)
			mutate(p)
			if _, err := bunVerifyForTest(t, root, root, p); err == nil {
				t.Fatal("unsafe pruning admitted")
			}
		})
	}
	root["dependencies"] = map[string]string{"deep": "1"}
	if _, err := bunVerifyForTest(t, root, root, packages); err == nil {
		t.Fatal("pruned subtree satisfied required edge")
	}
}

func TestNativeBunOverridesPreserveVerifiedArchives(t *testing.T) {
	cache := t.TempDir()
	sentinel := filepath.Join(t.TempDir(), "script-ran")
	archive := map[string]any{"name": "left", "version": "1.0.0", "dependencies": map[string]string{"shared": "^1"}, "scripts": map[string]string{"postinstall": "touch " + sentinel}}
	tarball := func(m map[string]any) []byte {
		return bunTarballForTest(t, []bunTarEntryForTest{{name: "package/package.json", typeflag: tar.TypeReg, body: bunPackageJSONForTest(t, m)}})
	}
	left := tarball(archive)
	shared := tarball(map[string]any{"name": "shared", "version": "2.0.0"})
	manifest := map[string]any{"name": "fixture", "dependencies": map[string]string{"left": "1"}, "overrides": map[string]string{"shared": "2"}, "trustedDependencies": []string{"left"}}
	metadata := map[string]any{"overrides": manifest["overrides"], "trustedDependencies": manifest["trustedDependencies"]}
	packages := map[string][]any{
		"left":   {"left@1.0.0", "", map[string]any{"dependencies": map[string]string{"shared": "^1"}}, writeBunCacheForTest(t, cache, left)},
		"shared": {"shared@2.0.0", "", map[string]any{}, writeBunCacheForTest(t, cache, shared)},
	}
	installs, err := bunPlanMetadataForTest(t, manifest, metadata, packages)
	if err != nil {
		t.Fatal(err)
	}
	if err := materializeNativeBunTree(context.Background(), cache, t.TempDir(), installs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("trusted script executed: %v", err)
	}
	packages["left"] = []any{"left@1.0.0", "", map[string]any{"dependencies": map[string]string{"shared": "2"}}, bunIntegrityForTest(left)}
	installs, err = bunPlanMetadataForTest(t, manifest, metadata, packages)
	if err != nil {
		t.Fatal(err)
	}
	if err := materializeNativeBunTree(context.Background(), cache, t.TempDir(), installs); err == nil || !strings.Contains(err.Error(), "lock does not match package archive: left") {
		t.Fatalf("override excused rewritten archive declaration: %v", err)
	}
}

func TestNativeTargetBunMaterializerMetadata(t *testing.T) {
	f := newNativeTargetBunFixture(t, nil, map[string]string{
		"plugin/scripts/check.js": strings.Replace(nativeTargetBunCheckJS, "left === 2", "left === 3", 1),
	})
	readObject := func(name string) map[string]any {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(f.repo, "plugin", name))
		if err != nil {
			t.Fatal(err)
		}
		raw, err = nativeBunStripTrailingCommas(raw)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	manifest, lock := readObject("package.json"), readObject("bun.lock")
	for _, m := range []map[string]any{manifest, lock} {
		m["overrides"] = map[string]string{"inner": "2.0.0"}
		m["trustedDependencies"] = []string{"left"}
	}
	pkgs := lock["packages"].(map[string]any)
	delete(pkgs, "left/inner")
	pkgs["mac-only"] = []any{"mac-only@1.0.0", "", map[string]any{"os": "darwin"}, bunTestIntegrity}
	pkgs["mac-only/bundled"] = []any{"bundled@1.0.0", "", map[string]any{"bundled": true}, "sha1-AAAAAAAAAAAAAAAAAAAAAAAAAAA="}
	writeRepoFilesForTest(t, f.repo, map[string]string{
		"plugin/package.json": bunPackageJSONForTest(t, manifest),
		"plugin/bun.lock":     bunPackageJSONForTest(t, lock),
	})
	before := f.cacheDigest(t)
	f.commitAndPrepareRepin(t, "metadata")
	o := NativeOracleRequest{Phase: "execute", PreparationRunRef: f.prepare.OperationRef}
	r, err := f.s.VerifyWorktree(context.Background(), f.request("metadata-execute", &o))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || f.launches != 1 {
		t.Fatalf("override/trust/pruned bundle execution %+v %v launches=%d", r, err, f.launches)
	}
	if f.cacheDigest(t) != before {
		t.Fatal("metadata-aware materialization changed shared cache")
	}
	if _, err := os.Stat(f.marker); !os.IsNotExist(err) {
		t.Fatalf("trusted lifecycle script ran: %v", err)
	}
}

func TestNativeBunLockVersionTwo(t *testing.T) {
	root := map[string]any{"name": "fixture", "dependencies": map[string]string{"left": "1"}}
	packages := map[string][]any{
		"left":        {"left@1.0.0", "", map[string]any{"dependencies": map[string]string{"shared": "^1"}}, bunTestIntegrity},
		"shared":      {"shared@2.0.0", "", map[string]any{}, bunTestIntegrity},
		"mac":         {"mac@1.0.0", "", map[string]any{"os": "darwin"}, bunTestIntegrity},
		"mac/bundled": {"bundled@1.0.0", "", map[string]any{"bundled": true}, "sha1-AAAAAAAAAAAAAAAAAAAAAAAAAAA="},
	}
	lock := strings.Replace(bunLockForTest(t, root, packages), `"lockfileVersion": 1`, `"lockfileVersion": 2`, 1)
	lock = strings.Replace(lock, "{", `{"overrides":{"shared":"2"},"trustedDependencies":["left"],`, 1)
	manifest := maps.Clone(root)
	manifest["overrides"] = map[string]string{"shared": "2"}
	manifest["trustedDependencies"] = []string{"left"}
	plan := func(text string, m map[string]any) ([]nativeBunInstall, error) {
		t.Helper()
		return nativeBunPlanInstalls([]byte(bunPackageJSONForTest(t, m)), []byte(text))
	}
	installs, err := plan(lock, manifest)
	if err != nil || len(installs) != 2 {
		t.Fatalf("version-2 registry closure refused: %v, %v", installs, err)
	}
	for name, mutation := range map[string][2]string{
		"version zero":             {`"lockfileVersion": 2`, `"lockfileVersion": 0`},
		"version three":            {`"lockfileVersion": 2`, `"lockfileVersion": 3`},
		"future version":           {`"lockfileVersion": 2`, `"lockfileVersion": 4`},
		"unknown top field":        {`"lockfileVersion": 2`, `"unknown":{},"lockfileVersion": 2`},
		"unknown package field":    {`"shared@2.0.0","",{}`, `"shared@2.0.0","",{"unknown":true}`},
		"off-registry archive":     {`"shared@2.0.0",""`, `"shared@2.0.0","https://example.invalid/shared.tgz"`},
		"missing integrity":        {`"shared@2.0.0","",{},"` + bunTestIntegrity + `"`, `"shared@2.0.0","",{},""`},
		"unsupported integrity":    {`"shared@2.0.0","",{},"` + bunTestIntegrity + `"`, `"shared@2.0.0","",{},"sha1-AAAAAAAAAAAAAAAAAAAAAAAAAAA="`},
		"git tuple":                {`"shared@2.0.0","",{},"` + bunTestIntegrity + `"`, `"shared@git+https://example.invalid/shared.git",{},"../tag"`},
		"selected bundle":          {`"os":"darwin"`, `"os":"linux"`},
		"effective range mismatch": {`"shared@2.0.0"`, `"shared@1.0.0"`},
		"override drift":           {`"overrides":{"shared":"2"}`, `"overrides":{"shared":"^2"}`},
		"trust drift":              {`"trustedDependencies":["left"]`, `"trustedDependencies":[]`},
	} {
		t.Run(name, func(t *testing.T) {
			text := strings.Replace(lock, mutation[0], mutation[1], 1)
			if text == lock {
				t.Fatal("mutation did not apply")
			}
			if _, err := plan(text, manifest); err == nil {
				t.Fatal("version-2 grammar/integrity violation admitted")
			}
		})
	}
}

func TestNativeBunVersionTwoFixtureArchives(t *testing.T) {
	m, _ := nativeVitestManifestForTest()
	pair, _ := nativeTargetPairOf(m)
	environment, err := resolveNativeTargetRuntime(context.Background(), m, pair)
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
	environment.NPMCacheDir = cache
	target := &nativeTargetOraclePlan{Recipe: m, Environment: environment}
	root := t.TempDir()
	snapshot := filepath.Join(root, "snapshot")
	if err := os.Mkdir(snapshot, 0700); err != nil {
		t.Fatal(err)
	}
	inputs := map[string][]byte{}
	for _, name := range []string{"package.json", "bun.lock"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "native-vitest", name))
		if err != nil {
			t.Fatal(err)
		}
		inputs[name] = raw
		target.DependencyFiles = append(target.DependencyFiles, NativeOracleFile{Path: name, SHA256: nativeDigest(raw)})
		if err := os.WriteFile(filepath.Join(snapshot, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	installs, err := nativeBunPlanInstalls(inputs["package.json"], inputs["bun.lock"])
	if err != nil {
		t.Fatal(err)
	}
	if err := nativeBunMissing(cache, installs); err != nil {
		if os.Getenv("OC_NATIVE_ISOLATION_REQUIRED") == "1" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	if err := materializeNativeBunRun(context.Background(), target, root, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeBunRunnerParts(target, root); err != nil {
		t.Fatal(err)
	}
	for _, file := range target.DependencyFiles {
		raw, err := os.ReadFile(filepath.Join(snapshot, file.Path))
		if err != nil || nativeDigest(raw) != file.SHA256 {
			t.Fatalf("materialization changed dependency input %s: %v", file.Path, err)
		}
	}
	t.Logf("verified, reconciled, extracted and fingerprinted %d selected registry archives", len(installs))
}

func TestNativeBunArchiveShadowedPeers(t *testing.T) {
	pkg := nativeBunPackage{Key: "left", Name: "left", Version: "1.0.0",
		Deps: map[string]string{"dep": "^2", "meta": "1"}, OptDeps: map[string]string{"opt": "^3"},
		PeerDeps: map[string]string{"peer": "^1"}, OptionalPeers: []string{"peer"},
	}
	archive := map[string]any{
		"name": "left", "version": "1.0.0",
		"dependencies":         map[string]string{"dep": "^2", "meta": "1", "opt": "^3"},
		"optionalDependencies": map[string]string{"opt": "^3"},
		"peerDependencies":     map[string]string{"dep": "^1", "opt": "^2", "peer": "^1"},
		"peerDependenciesMeta": map[string]any{"dep": map[string]bool{"optional": true}, "opt": map[string]bool{"optional": true}, "meta": map[string]bool{"optional": true}, "peer": map[string]bool{"optional": true}},
	}
	raw := []byte(bunPackageJSONForTest(t, archive))
	if err := reconcileNativeBunArchive(pkg, raw); err != nil {
		t.Fatalf("Bun's dependency-over-peer precedence refused: %v", err)
	}
	for name, mutate := range map[string]func(*nativeBunPackage){
		"drop active peer":            func(p *nativeBunPackage) { delete(p.PeerDeps, "peer") },
		"change required declaration": func(p *nativeBunPackage) { p.Deps["dep"] = "^1" },
		"drop required declaration":   func(p *nativeBunPackage) { delete(p.Deps, "dep") },
		"change optional declaration": func(p *nativeBunPackage) { p.OptDeps["opt"] = "^2" },
		"invent peer":                 func(p *nativeBunPackage) { p.PeerDeps["ghost"] = "*" },
		"drop optional peer metadata": func(p *nativeBunPackage) { p.OptionalPeers = nil },
	} {
		t.Run(name, func(t *testing.T) {
			p := pkg
			p.Deps, p.OptDeps, p.PeerDeps = maps.Clone(pkg.Deps), maps.Clone(pkg.OptDeps), maps.Clone(pkg.PeerDeps)
			mutate(&p)
			if err := reconcileNativeBunArchive(p, raw); err == nil {
				t.Fatal("unbacked archive declaration admitted")
			}
		})
	}
}
