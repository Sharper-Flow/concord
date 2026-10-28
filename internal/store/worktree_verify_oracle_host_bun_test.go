package store

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// bunTarEntryForTest is one raw tar entry for a synthetic package archive.
type bunTarEntryForTest struct {
	name     string
	typeflag byte
	body     string
	mode     int64
	linkname string
}

func bunTarballForTest(t *testing.T, entries []bunTarEntryForTest) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		mode := e.mode
		if mode == 0 {
			mode = 0644
		}
		hdr := &tar.Header{Name: e.name, Typeflag: e.typeflag, Mode: mode, Linkname: e.linkname, Format: tar.FormatPAX}
		if e.typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// bunPackageJSONForTest renders one archive package.json.
func bunPackageJSONForTest(t *testing.T, fields map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func bunIntegrityForTest(data []byte) string {
	sum := sha512.Sum512(data)
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

// writeBunCacheForTest stores one tarball at its npm cacache content path.
func writeBunCacheForTest(t *testing.T, cache string, data []byte) string {
	t.Helper()
	sum := sha512.Sum512(data)
	file := nativeBunCacheContent(cache, sum[:])
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	return bunIntegrityForTest(data)
}

func TestNativeBunRangeTable(t *testing.T) {
	cases := []struct {
		rng, version string
		want         bool
	}{
		{"^1.2.3", "1.2.3", true},
		{"^1.2.3", "1.9.0", true},
		{"^1.2.3", "2.0.0", false},
		{"^1.2.3", "1.2.2", false},
		{"^0.2.3", "0.2.9", true},
		{"^0.2.3", "0.3.0", false},
		{"^0.0.3", "0.0.3", true},
		{"^0.0.3", "0.0.4", false},
		{"^1.2", "1.9.9", true},
		{"^0.0", "0.0.9", true},
		{"^0.0", "0.1.0", false},
		{"~1.2.3", "1.2.9", true},
		{"~1.2.3", "1.3.0", false},
		{"~1", "1.9.0", true},
		{"~1", "2.0.0", false},
		{"~>1.2", "1.2.5", true},
		{"1.x", "1.4.0", true},
		{"1.x", "2.0.0", false},
		{"1.2.*", "1.2.7", true},
		{"*", "3.0.0", true},
		{"", "3.0.0", true},
		{"x", "0.0.1", true},
		{"1.2.3", "1.2.3", true},
		{"=1.2.3", "1.2.4", false},
		{"v1.2.3", "1.2.3", true},
		{">=1.2.3 <2", "1.5.0", true},
		{">= 1.2.3 < 2", "2.0.0", false},
		{">1.2", "1.2.9", false},
		{">1.2", "1.3.0", true},
		{"<1.2", "1.1.9", true},
		{"<1.2", "1.2.0", false},
		{"<=1.2", "1.2.9", true},
		{"<=1.2", "1.3.0", false},
		{"1.2.3 - 2.3.4", "2.3.4", true},
		{"1.2.3 - 2.3.4", "2.3.5", false},
		{"1.2 - 2", "2.9.9", true},
		{"1.2 - 2", "3.0.0", false},
		{"^1.0.0 || ^2.0.0", "2.1.0", true},
		{"^1.0.0 || ^2.0.0", "3.0.0", false},
		// The default prerelease rule: a prerelease satisfies only a set
		// with a prerelease comparator on the same tuple.
		{"^1.2.3", "1.3.0-beta.1", false},
		{"^1.2.3-beta.1", "1.2.3-beta.2", true},
		{"^1.2.3-beta.1", "1.2.4-beta.1", false},
		{">=1.0.0-rc.1", "1.0.0-rc.10", true},
		{">=1.0.0-rc.1", "1.0.0-rc.0", false},
		{"*", "1.0.0-alpha", false},
		{"1.0.0+build.5", "1.0.0", true},
	}
	for _, c := range cases {
		r, ok := parseNativeSemverRange(c.rng)
		if !ok {
			t.Errorf("range %q refused", c.rng)
			continue
		}
		v, ok := parseNativeSemver(c.version)
		if !ok {
			t.Fatalf("version %q refused", c.version)
		}
		if got := r.satisfies(v); got != c.want {
			t.Errorf("%q satisfies %q = %v, want %v", c.version, c.rng, got, c.want)
		}
	}
	for _, refused := range []string{"latest", "next", "github:a/b", "git+https://example.invalid/a.git", "https://example.invalid/a.tgz", "file:../a", "link:../a", "workspace:*", "catalog:", "1.2.3.4", "01.2.3", "1.x.3", "^", "1.2.3-", "1.2 - ", ">=a"} {
		if _, ok := parseNativeSemverRange(refused); ok {
			t.Errorf("unsupported specifier %q parsed as a range", refused)
		}
	}
	for _, refused := range []string{"1.2", "1.2.3.4", "v1.2.3", "1.02.3", "1.2.3-01", "1.2.3-", "1.2.3+"} {
		if _, ok := parseNativeSemver(refused); ok {
			t.Errorf("non-SemVer version %q parsed", refused)
		}
	}
}

// bunLockForTest renders one bun.lock text from the root workspace record
// and package tuples, with bun's trailing commas.
func bunLockForTest(t *testing.T, root map[string]any, packages map[string][]any) string {
	t.Helper()
	ws, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("{\n  \"lockfileVersion\": 1,\n  \"configVersion\": 1,\n  \"workspaces\": {\n    \"\": " + string(ws) + ",\n  },\n  \"packages\": {\n")
	for key, tuple := range packages {
		raw, err := json.Marshal(tuple)
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString("    \"" + key + "\": " + string(raw) + ",\n")
	}
	b.WriteString("  },\n}\n")
	return b.String()
}

var bunTestIntegrity = "sha512-" + strings.Repeat("A", 86) + "=="

func TestNativeBunLockGrammar(t *testing.T) {
	valid := bunLockForTest(t, map[string]any{"name": "fixture", "dependencies": map[string]string{"left": "^1.0.0"}}, map[string][]any{
		"left": {"left@1.0.0", "", map[string]any{"bin": map[string]string{"left": "cli.js"}}, bunTestIntegrity},
	})
	lock, err := parseNativeBunLock([]byte(valid))
	if err != nil {
		t.Fatalf("valid lock with trailing commas refused: %v", err)
	}
	if lock.Packages["left"].Name != "left" || lock.Packages["left"].Version != "1.0.0" || len(lock.Packages["left"].Integrity) != sha512.Size {
		t.Fatalf("parsed tuple %+v", lock.Packages["left"])
	}
	refusals := map[string]string{
		"comment":            strings.Replace(valid, "\"packages\"", "// note\n  \"packages\"", 1),
		"unknown top key":    strings.Replace(valid, "\"lockfileVersion\": 1,", "\"lockfileVersion\": 1, \"unknown\": [],", 1),
		"scoped overrides":   strings.Replace(valid, "\"lockfileVersion\": 1,", "\"lockfileVersion\": 1, \"overrides\": {\"left@1\":\"2\"},", 1),
		"catalog":            strings.Replace(valid, "\"lockfileVersion\": 1,", "\"lockfileVersion\": 1, \"catalog\": {},", 1),
		"patched":            strings.Replace(valid, "\"lockfileVersion\": 1,", "\"lockfileVersion\": 1, \"patchedDependencies\": {},", 1),
		"lock version 0":     strings.Replace(valid, "\"lockfileVersion\": 1", "\"lockfileVersion\": 0", 1),
		"config version 2":   strings.Replace(valid, "\"configVersion\": 1", "\"configVersion\": 2", 1),
		"second workspace":   strings.Replace(valid, "\"workspaces\": {", "\"workspaces\": { \"pkg\": {\"name\": \"pkg\"},", 1),
		"workspace field":    strings.Replace(valid, "\"name\":\"fixture\"", "\"name\":\"fixture\",\"bin\":{}", 1),
		"other registry":     strings.Replace(valid, "\"left@1.0.0\",\"\"", "\"left@1.0.0\",\"https://registry.example.invalid/\"", 1),
		"git tuple":          strings.Replace(valid, "[\"left@1.0.0\",\"\",{\"bin\":{\"left\":\"cli.js\"}},\""+bunTestIntegrity+"\"]", "[\"left@github:a/left#abc\",{},\"a-left-abc\"]", 1),
		"non-semver version": strings.Replace(valid, "left@1.0.0", "left@latest", 1),
		"sha1 integrity":     strings.Replace(valid, bunTestIntegrity, "sha1-AAAAAAAAAAAAAAAAAAAAAAAAAAA=", 1),
		"meta field":         strings.Replace(valid, "{\"bin\"", "{\"scripts\":{},\"bin\"", 1),
		"traversing key":     strings.Replace(valid, "\"left\": [", "\"../left\": [", 1),
		"node_modules key":   strings.Replace(valid, "\"left\": [", "\"node_modules/left\": [", 1),
		"dot key":            strings.Replace(valid, "\"left\": [", "\"left/.\": [", 1),
	}
	for name, text := range refusals {
		if text == valid {
			t.Fatalf("%s: mutation did not apply", name)
		}
		if _, err := parseNativeBunLock([]byte(text)); err == nil {
			t.Errorf("%s: lock admitted", name)
		}
	}
}

// bunVerifyForTest runs freshness and closure over one manifest and lock.
func bunVerifyForTest(t *testing.T, manifest map[string]any, root map[string]any, packages map[string][]any) ([]nativeBunInstall, error) {
	t.Helper()
	raw, _ := json.Marshal(manifest)
	return nativeBunPlanInstalls(raw, []byte(bunLockForTest(t, root, packages)))
}

func TestNativeBunLockFreshness(t *testing.T) {
	root := map[string]any{"name": "fixture", "dependencies": map[string]string{"left": "^1.0.0"}}
	packages := map[string][]any{"left": {"left@1.0.0", "", map[string]any{}, bunTestIntegrity}}
	if _, err := bunVerifyForTest(t, root, root, packages); err != nil {
		t.Fatalf("fresh lock refused: %v", err)
	}
	stale := map[string]any{"name": "fixture", "dependencies": map[string]string{"left": "^1.1.0"}}
	if _, err := bunVerifyForTest(t, stale, root, packages); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale lock admitted: %v", err)
	}
	renamed := map[string]any{"name": "other", "dependencies": map[string]string{"left": "^1.0.0"}}
	if _, err := bunVerifyForTest(t, renamed, root, packages); err == nil {
		t.Fatal("renamed manifest admitted")
	}
	devOnly := map[string]any{"name": "fixture", "dependencies": map[string]string{"left": "^1.0.0"}, "devDependencies": map[string]string{"right": "1"}}
	if _, err := bunVerifyForTest(t, devOnly, root, packages); err == nil {
		t.Fatal("manifest devDependency missing from the lock admitted")
	}
	for _, field := range []string{"resolutions", "workspaces", "catalog", "catalogs", "patchedDependencies"} {
		m := map[string]any{"name": "fixture", "dependencies": map[string]string{"left": "^1.0.0"}, field: map[string]string{}}
		if _, err := bunVerifyForTest(t, m, root, packages); err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("package.json %s admitted: %v", field, err)
		}
	}
}

func TestNativeBunLockClosure(t *testing.T) {
	manifest := map[string]any{
		"name":                 "fixture",
		"dependencies":         map[string]string{"left": "^1.0.0", "alias": "npm:right@^2.0.0"},
		"devDependencies":      map[string]string{"tool": "~3.1.0"},
		"optionalDependencies": map[string]string{"mac-only": "1.0.0", "absent": "^1"},
	}
	packages := map[string][]any{
		"left":       {"left@1.0.0", "", map[string]any{"dependencies": map[string]string{"shared": "^1.0.0"}, "optionalDependencies": map[string]string{"fsevents": "2"}}, bunTestIntegrity},
		"left/inner": {"inner@1.0.0", "", map[string]any{}, bunTestIntegrity},
		"alias":      {"right@2.3.0", "", map[string]any{"dependencies": map[string]string{"inner": "^2"}}, bunTestIntegrity},
		"inner":      {"inner@2.0.0", "", map[string]any{}, bunTestIntegrity},
		"shared":     {"shared@1.4.0", "", map[string]any{"os": []string{"!win32"}, "cpu": "x64"}, bunTestIntegrity},
		"tool":       {"tool@3.1.4", "", map[string]any{}, bunTestIntegrity},
		"mac-only":   {"mac-only@1.0.0", "", map[string]any{"os": "darwin"}, bunTestIntegrity},
		"fsevents":   {"fsevents@2.3.3", "", map[string]any{"os": []string{"darwin"}, "cpu": []string{"arm64", "x64"}}, bunTestIntegrity},
	}
	installs, err := bunVerifyForTest(t, manifest, manifest, packages)
	if err != nil {
		t.Fatalf("closed lock refused: %v", err)
	}
	var dirs []string
	for _, in := range installs {
		dirs = append(dirs, in.Dir)
	}
	want := []string{"node_modules/alias", "node_modules/inner", "node_modules/left", "node_modules/left/node_modules/inner", "node_modules/shared", "node_modules/tool"}
	if strings.Join(dirs, ",") != strings.Join(want, ",") {
		t.Fatalf("selected installs %v, want %v (platform-skipped mac-only and fsevents)", dirs, want)
	}

	mutate := func(key string, tuple []any) map[string][]any {
		next := map[string][]any{}
		for k, v := range packages {
			next[k] = v
		}
		if tuple == nil {
			delete(next, key)
		} else {
			next[key] = tuple
		}
		return next
	}
	refusals := map[string]map[string][]any{
		"required dependency missing":        mutate("shared", nil),
		"required dependency platform-skips": mutate("shared", []any{"shared@1.4.0", "", map[string]any{"os": "!linux"}, bunTestIntegrity}),
		"wrong version":                      mutate("tool", []any{"tool@3.2.0", "", map[string]any{}, bunTestIntegrity}),
		"alias target mismatch":              mutate("alias", []any{"left@2.3.0", "", map[string]any{"dependencies": map[string]string{"inner": "^2"}}, bunTestIntegrity}),
		"name mismatch":                      mutate("tool", []any{"other@3.1.4", "", map[string]any{}, bunTestIntegrity}),
		"nested lookup misses":               mutate("left/inner", nil),
		"dist-tag edge":                      mutate("tool", []any{"tool@3.1.4", "", map[string]any{"dependencies": map[string]string{"inner": "latest"}}, bunTestIntegrity}),
		"protocol edge":                      mutate("tool", []any{"tool@3.1.4", "", map[string]any{"dependencies": map[string]string{"inner": "github:a/inner"}}, bunTestIntegrity}),
		// The nearest inner is not installed on linux, so Node reaches the
		// hoisted inner@2, outside the range of left's optional edge.
		"skipped nearest falls through": mutate("left/inner", []any{"inner@1.0.0", "", map[string]any{"os": "darwin"}, bunTestIntegrity}),
	}
	// The missing nested entry must fall through to the hoisted inner@2,
	// which left's own lookup reaches only for an unversioned edge; make
	// left depend on inner@^1 so the fallback fails the range.
	refusals["nested lookup misses"]["left"] = []any{"left@1.0.0", "", map[string]any{"dependencies": map[string]string{"shared": "^1.0.0", "inner": "^1"}}, bunTestIntegrity}
	refusals["skipped nearest falls through"]["left"] = []any{"left@1.0.0", "", map[string]any{"dependencies": map[string]string{"shared": "^1.0.0"}, "optionalDependencies": map[string]string{"inner": "^1"}}, bunTestIntegrity}
	for name, pkgs := range refusals {
		if _, err := bunVerifyForTest(t, manifest, manifest, pkgs); err == nil {
			t.Errorf("%s: lock admitted", name)
		}
	}
	// A dependency name with no lock entry is admitted only on an optional
	// edge, as the absent optional root dependency above shows.
	if !nativeBunPlatformMatch(true, []string{"!darwin", "!win32"}, "linux") || nativeBunPlatformMatch(true, []string{"none"}, "linux") || nativeBunPlatformMatch(true, []string{"linux", "!linux"}, "linux") {
		t.Fatal("platform negation rules")
	}
}

func TestNativeBunArchiveReconciliation(t *testing.T) {
	pkg := nativeBunPackage{Key: "left", Name: "left", Version: "1.0.0",
		Deps:          map[string]string{"shared": "^1.0.0"},
		OptDeps:       map[string]string{"fsevents": "2"},
		PeerDeps:      map[string]string{"react": "^18", "types": "*"},
		OptionalPeers: []string{"types"},
	}
	archive := map[string]any{
		"name": "left", "version": "1.0.0",
		"dependencies":         map[string]string{"shared": "^1.0.0", "fsevents": "2"},
		"optionalDependencies": map[string]string{"fsevents": "2"},
		"peerDependencies":     map[string]string{"react": "^18"},
		"peerDependenciesMeta": map[string]any{"types": map[string]bool{"optional": true}},
	}
	if err := reconcileNativeBunArchive(pkg, []byte(bunPackageJSONForTest(t, archive))); err != nil {
		t.Fatalf("matching archive with the extra * optional peer refused: %v", err)
	}
	mutations := map[string]func(m map[string]any){
		"version": func(m map[string]any) { m["version"] = "1.0.1" },
		"name":    func(m map[string]any) { m["name"] = "right" },
		"extra dep": func(m map[string]any) {
			m["dependencies"] = map[string]string{"shared": "^1.0.0", "fsevents": "2", "evil": "1"}
		},
		"edited range":   func(m map[string]any) { m["dependencies"] = map[string]string{"shared": "^1.1.0", "fsevents": "2"} },
		"extra optional": func(m map[string]any) { m["optionalDependencies"] = map[string]string{"fsevents": "2", "more": "1"} },
		"extra peer":     func(m map[string]any) { m["peerDependencies"] = map[string]string{"react": "^18", "vue": "3"} },
		"peer range":     func(m map[string]any) { m["peerDependencies"] = map[string]string{"react": "^17"} },
		"peer not optional": func(m map[string]any) {
			m["peerDependenciesMeta"] = map[string]any{"types": map[string]bool{"optional": false}}
		},
	}
	for name, mutate := range mutations {
		m := map[string]any{}
		for k, v := range archive {
			m[k] = v
		}
		mutate(m)
		if err := reconcileNativeBunArchive(pkg, []byte(bunPackageJSONForTest(t, m))); err == nil || !strings.Contains(err.Error(), "left") {
			t.Errorf("%s: archive mismatch admitted: %v", name, err)
		}
	}
	// A lock peer missing from both the archive peers and the metadata, even
	// one spelled *, is not an admitted extra.
	pkg.PeerDeps["ghost"] = "*"
	if err := reconcileNativeBunArchive(pkg, []byte(bunPackageJSONForTest(t, archive))); err == nil {
		t.Fatal("unbacked * lock peer admitted")
	}
}

// extractBunForTest extracts one tarball into a fresh destination.
func extractBunForTest(t *testing.T, tarball []byte, budget *nativeBunBudget) (string, []byte, error) {
	t.Helper()
	dest := t.TempDir()
	rooted, err := os.OpenRoot(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rooted.Close() }()
	if budget == nil {
		budget = &nativeBunBudget{}
	}
	manifest, err := extractNativeBunPackage(context.Background(), rooted, "node_modules/left", tarball, budget)
	return dest, manifest, err
}

func bunManifestEntryForTest(t *testing.T) bunTarEntryForTest {
	return bunTarEntryForTest{name: "package/package.json", typeflag: tar.TypeReg, body: bunPackageJSONForTest(t, map[string]any{"name": "left", "version": "1.0.0"})}
}

func TestNativeBunTarSafety(t *testing.T) {
	ok := bunTarballForTest(t, []bunTarEntryForTest{
		{name: "package/", typeflag: tar.TypeDir, mode: 0755},
		bunManifestEntryForTest(t),
		{name: "package/lib/", typeflag: tar.TypeDir, mode: 0700},
		{name: "package/lib/deep/", typeflag: tar.TypeDir},
		{name: "package/lib/deep/index.js", typeflag: tar.TypeReg, body: "module.exports = 1\n", mode: 0600},
		{name: "package/bin/cli.js", typeflag: tar.TypeReg, body: "#!/usr/bin/env node\n", mode: 0775},
	})
	old := syscall.Umask(0o077)
	dest, manifest, err := extractBunForTest(t, ok, nil)
	syscall.Umask(old)
	if err != nil || !strings.Contains(string(manifest), "\"left\"") {
		t.Fatalf("safe archive with nested directory headers refused: %v", err)
	}
	for name, want := range map[string]os.FileMode{"node_modules/left/lib/deep/index.js": 0644, "node_modules/left/bin/cli.js": 0755} {
		info, err := os.Lstat(filepath.Join(dest, name))
		if err != nil || info.Mode().Perm() != want || !info.Mode().IsRegular() {
			t.Errorf("%s mode %v %v, want %v", name, info, err, want)
		}
	}
	unsafe := map[string][]bunTarEntryForTest{
		"absolute path":       {{name: "/etc/evil", typeflag: tar.TypeReg, body: "x"}},
		"absolute in package": {{name: "package//etc/evil", typeflag: tar.TypeReg, body: "x"}},
		"dot-dot segment":     {{name: "package/../evil", typeflag: tar.TypeReg, body: "x"}},
		"dot-dot top":         {{name: "../evil", typeflag: tar.TypeReg, body: "x"}},
		"dot segment":         {{name: "package/./evil", typeflag: tar.TypeReg, body: "x"}},
		"backslash":           {{name: "package/..\\evil", typeflag: tar.TypeReg, body: "x"}},
		"symlink":             {{name: "package/link", typeflag: tar.TypeSymlink, linkname: "../../.."}},
		"inner symlink":       {{name: "package/link", typeflag: tar.TypeSymlink, linkname: "package.json"}},
		"hardlink":            {{name: "package/hard", typeflag: tar.TypeLink, linkname: "package/package.json"}},
		"char device":         {{name: "package/dev", typeflag: tar.TypeChar}},
		"block device":        {{name: "package/blk", typeflag: tar.TypeBlock}},
		"fifo":                {{name: "package/fifo", typeflag: tar.TypeFifo}},
		"two tops":            {{name: "other/file", typeflag: tar.TypeReg, body: "x"}},
		"bundled tree":        {{name: "package/node_modules/evil/index.js", typeflag: tar.TypeReg, body: "x"}},
		"duplicate":           {{name: "package/package.json", typeflag: tar.TypeReg, body: "{}"}},
		"file then dir":       {{name: "package/lib", typeflag: tar.TypeReg, body: "x"}, {name: "package/lib/a.js", typeflag: tar.TypeReg, body: "x"}},
		"dir then file":       {{name: "package/lib/a.js", typeflag: tar.TypeReg, body: "x"}, {name: "package/lib", typeflag: tar.TypeReg, body: "x"}},
		"top as file":         {{name: "package", typeflag: tar.TypeReg, body: "x"}},
	}
	for name, entries := range unsafe {
		all := append([]bunTarEntryForTest{bunManifestEntryForTest(t)}, entries...)
		dest, _, err := extractBunForTest(t, bunTarballForTest(t, all), nil)
		if err == nil || !strings.Contains(err.Error(), "package archive node_modules/left") {
			t.Errorf("%s: unsafe archive admitted: %v", name, err)
		}
		if _, statErr := os.Lstat(filepath.Join(filepath.Dir(dest), "evil")); statErr == nil {
			t.Errorf("%s: an entry escaped the destination", name)
		}
	}
	noManifest := bunTarballForTest(t, []bunTarEntryForTest{{name: "package/index.js", typeflag: tar.TypeReg, body: "x"}})
	if _, _, err := extractBunForTest(t, noManifest, nil); err == nil || !strings.Contains(err.Error(), "no package.json") {
		t.Fatalf("archive without package.json admitted: %v", err)
	}
	if _, _, err := extractBunForTest(t, []byte("not gzip"), nil); err == nil {
		t.Fatal("non-gzip archive admitted")
	}
}

func TestNativeBunExtractionBounds(t *testing.T) {
	archive := bunTarballForTest(t, []bunTarEntryForTest{bunManifestEntryForTest(t), {name: "package/data.bin", typeflag: tar.TypeReg, body: "0123456789"}})
	limited := func(name string, budget *nativeBunBudget, entries []bunTarEntryForTest) {
		t.Helper()
		tarball := archive
		if entries != nil {
			tarball = bunTarballForTest(t, entries)
		}
		_, _, err := extractBunForTest(t, tarball, budget)
		if err == nil || !strings.Contains(err.Error(), "limit_exceeded") || !strings.Contains(err.Error(), name+" bound") {
			t.Errorf("%s bound not enforced: %v", name, err)
		}
	}
	// Each run-wide bound refuses the first unit past it; a budget already
	// at the bound stands in for the earlier packages of the same run.
	limited("tar-entry", &nativeBunBudget{entries: nativeBunMaxEntries - 1}, nil)
	limited("file-count", &nativeBunBudget{files: nativeBunMaxFiles - 1}, nil)
	limited("extracted-size", &nativeBunBudget{bytes: nativeBunMaxBytes - 10}, nil)
	deep := "package/" + strings.Repeat("d/", nativeBunMaxDepth) + "f.js"
	limited("path-depth", nil, []bunTarEntryForTest{bunManifestEntryForTest(t), {name: deep, typeflag: tar.TypeReg, body: "x"}})
	long := "package/" + strings.Repeat("n", nativeBunMaxPathBytes) + ".js"
	limited("path-length", nil, []bunTarEntryForTest{bunManifestEntryForTest(t), {name: long, typeflag: tar.TypeReg, body: "x"}})
	// The same budgets one unit lower admit the archive exactly at the bound.
	if _, _, err := extractBunForTest(t, archive, &nativeBunBudget{entries: nativeBunMaxEntries - 2, files: nativeBunMaxFiles - 2, bytes: nativeBunMaxBytes - 10 - int64(len(bunManifestEntryForTest(t).body))}); err != nil {
		t.Fatalf("archive at the bounds refused: %v", err)
	}
	// A declared entry size that would overflow the running total still
	// refuses before any byte is written.
	var huge bytes.Buffer
	zw := gzip.NewWriter(&huge)
	if err := tar.NewWriter(zw).WriteHeader(&tar.Header{Name: "package/huge.bin", Typeflag: tar.TypeReg, Mode: 0644, Size: math.MaxInt64, Format: tar.FormatPAX}); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := extractBunForTest(t, huge.Bytes(), &nativeBunBudget{bytes: 1}); err == nil || !strings.Contains(err.Error(), "extracted-size bound") {
		t.Fatalf("overflowing entry size bypassed the extracted-size bound: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rooted, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rooted.Close() }()
	if _, err := extractNativeBunPackage(ctx, rooted, "node_modules/left", archive, &nativeBunBudget{}); err == nil || err != context.Canceled {
		t.Fatalf("cancelled extraction %v, want context.Canceled", err)
	}
}

func TestNativeBunMaterializeEmptyTree(t *testing.T) {
	manifest := map[string]any{"name": "fixture"}
	installs, err := bunVerifyForTest(t, manifest, manifest, nil)
	if err != nil || len(installs) != 0 {
		t.Fatalf("empty lock plan = %+v, %v", installs, err)
	}
	dest := t.TempDir()
	cache := filepath.Join(t.TempDir(), "uncached")
	if err := materializeNativeBunTree(context.Background(), cache, dest, installs); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(dest, "node_modules")
	digest, err := nativeBunTreeDigest(tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(digest) == 0 {
		t.Fatal("empty tree has no root fingerprint")
	}
	entries, err := os.ReadDir(tree)
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty dependency tree = %v, %v", entries, err)
	}
}

func TestNativeBunCacheIntegrity(t *testing.T) {
	cache := t.TempDir()
	good := bunTarballForTest(t, []bunTarEntryForTest{bunManifestEntryForTest(t)})
	integrity := writeBunCacheForTest(t, cache, good)
	sum, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(integrity, "sha512-"))
	pkg := nativeBunPackage{Key: "left", Name: "left", Version: "1.0.0", Integrity: sum}
	if data, err := nativeBunReadCached(cache, pkg); err != nil || !bytes.Equal(data, good) {
		t.Fatalf("verified cache entry refused: %v", err)
	}
	if err := os.WriteFile(nativeBunCacheContent(cache, sum), append(good, 0), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeBunReadCached(cache, pkg); err == nil || !strings.Contains(err.Error(), "corrupt cache entry: left@1.0.0") {
		t.Fatalf("corrupt cache entry admitted: %v", err)
	}
	var installs []nativeBunInstall
	for i := range 10 {
		other := sha512.Sum512([]byte{byte(i)})
		installs = append(installs, nativeBunInstall{Pkg: nativeBunPackage{Name: "missing", Version: "1.0." + string(rune('0'+i)), Integrity: other[:]}})
	}
	err := nativeBunMissing(cache, installs)
	if err == nil || !strings.Contains(err.Error(), "10 locked packages are missing") || !strings.Contains(err.Error(), "npm cache add --ignore-scripts missing@1.0.0") || strings.Contains(err.Error(), "missing@1.0.8") {
		t.Fatalf("missing cache refusal %v", err)
	}
}

// The none recipe's input-change refusal names its first changed path, and
// a change under node_modules adds the materializer route.
func TestNativeTargetInputChangeNamesPath(t *testing.T) {
	before := []NativeOracleFile{{Path: "a.txt", SHA256: "1"}, {Path: "c.txt", SHA256: "3"}}
	for _, c := range []struct {
		after []NativeOracleFile
		want  string
	}{
		{before, ""},
		{[]NativeOracleFile{{Path: "a.txt", SHA256: "1"}, {Path: "b.txt", SHA256: "2"}, {Path: "c.txt", SHA256: "3"}}, "b.txt"},
		{[]NativeOracleFile{{Path: "a.txt", SHA256: "1"}}, "c.txt"},
		{[]NativeOracleFile{{Path: "a.txt", SHA256: "9"}, {Path: "c.txt", SHA256: "3"}}, "a.txt"},
		{[]NativeOracleFile{{Path: "a.txt", SHA256: "1"}, {Path: "c.txt", SHA256: "3"}, {Path: "plugin/node_modules/x/index.js", SHA256: "4"}}, "plugin/node_modules/x/index.js"},
	} {
		if got := nativeTargetFirstInputChange(before, c.after); got != c.want {
			t.Errorf("first change %q, want %q", got, c.want)
		}
	}
}
