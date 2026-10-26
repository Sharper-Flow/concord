package store

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // Git's SHA-1 object format is an interoperability requirement.
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/version"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"
)

type nativeGoOracleManifest struct {
	Kind         string              `json:"kind"`
	PackageCwd   string              `json:"package_cwd"`
	TestFiles    []string            `json:"test_files"`
	FixtureFiles []string            `json:"fixture_files"`
	Cases        map[string][]string `json:"cases"`
}

type nativeGoEnvironment struct {
	GoExecutable       string `json:"go_executable"`
	GoRoot             string `json:"go_root"`
	ModuleCache        string `json:"module_cache"`
	Version            string `json:"version"`
	GOOS               string `json:"goos"`
	GOARCH             string `json:"goarch"`
	ToolchainIdentity  string `json:"toolchain_identity"`
	Digest             string `json:"digest"`
	ModuleInputsDigest string `json:"module_inputs_digest"`
}

var nativeOracleTestName = regexp.MustCompile(`^Test[A-Z][A-Za-z0-9_]*$`)
var nativeGoToolchainName = regexp.MustCompile(`^go1\.[0-9]+(\.[0-9]+|beta[0-9]+|rc[0-9]+)?$`)

func nativeUnavailable(detail string) error {
	return oracleFailure(KindUnavailable, detail, "repair the pinned harness or installed inputs, then request explicit native preparation")
}

func nativeOracleSelector(m nativeGoOracleManifest, c OracleControl) ([]string, error) {
	names, err := nativeOracleCaseTests(m, c)
	if err != nil {
		return nil, err
	}
	selector := "^(" + strings.Join(names, "|") + ")$"
	if len("-test.run="+selector) > maxWorktreeVerifyCommandPart {
		return nil, oracleFailure(KindLimitExceeded, "rendered test selector exceeds 256 bytes", "select a bounded literal-name union")
	}
	logical := []string{"go", "test", "-count=1", "-run", selector, "."}
	if !slices.Equal(logical, c.Argv) {
		return nil, nativeUnavailable("control argv is not the exact closed go test selector")
	}
	if err := validateWorktreeVerifyCommand(logical); err != nil {
		return nil, err
	}
	return names, nil
}

// nativeOracleCaseTests returns the sorted distinct top-level tests the
// manifest maps the control's cases to: the witness set an execute run must
// observe running and passing.
func nativeOracleCaseTests(m nativeGoOracleManifest, c OracleControl) ([]string, error) {
	if len(m.Cases) != len(c.CaseIDs) {
		return nil, nativeUnavailable("manifest cases differ from the control")
	}
	set := map[string]bool{}
	for _, id := range c.CaseIDs {
		names, ok := m.Cases[id]
		if !ok || len(names) < 1 || len(names) > 8 {
			return nil, nativeUnavailable("each case must select 1 to 8 literal top-level tests")
		}
		local := map[string]bool{}
		for _, name := range names {
			if !nativeOracleTestName.MatchString(name) || len(name) > 64 || local[name] {
				return nil, nativeUnavailable("manifest test names must be unique ASCII top-level Test names of at most 64 bytes")
			}
			local[name] = true
			set[name] = true
		}
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func nativeControlledEnv(e nativeGoEnvironment, root string) []string {
	var env []string
	for _, key := range []string{"PATH", "LANG", "LC_ALL"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	env = append(env, "HOME="+filepath.Join(root, "home"), "TMPDIR="+filepath.Join(root, "tmp"), "GOCACHE="+filepath.Join(root, "cache"), "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOFLAGS=-mod=readonly", "CGO_ENABLED=0", "GOWORK=off", "GOENV=off", "GOOS="+e.GOOS, "GOARCH="+e.GOARCH)
	if e.GoRoot != "" {
		env = append(env, "GOROOT="+e.GoRoot)
	}
	if e.ModuleCache != "" {
		env = append(env, "GOMODCACHE="+e.ModuleCache)
	}
	return env
}

func nativeScratchDirectories(root string) error {
	for _, name := range []string{"home", "tmp", "cache"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			return err
		}
	}
	return nil
}

func nativeOracleTestContext(ctx context.Context, req WorktreeVerifyRequest) (context.Context, context.CancelFunc, error) {
	if req.CallerBudgetApplied {
		return ctx, func() {}, nil
	}
	if req.SupportedBudgetSeconds < 1 {
		return ctx, func() {}, nativeUnavailable("test stage has no supported operation ceiling")
	}
	child, cancel := context.WithTimeout(ctx, time.Duration(req.SupportedBudgetSeconds)*time.Second)
	return child, cancel, nil
}

func nativeOracleStageCapture(name string, capture *nativeStreamCapture) *nativeStreamCapture {
	if name == "metadata" {
		// Dependency metadata is consumed here, not mixed into the retained
		// test evidence. Its own cap cannot spend the witness stream budget.
		return &nativeStreamCapture{complete: true}
	}
	return capture
}

func resolveNativeGoEnvironment(ctx context.Context, pinned string) (nativeGoEnvironment, error) {
	e := nativeGoEnvironment{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	unavailable := func(cause string) (nativeGoEnvironment, error) {
		return e, nativeUnavailable("pinned toolchain " + pinned + " is unavailable (cached-only, GOPROXY=off): " + cause)
	}
	goPath, err := exec.LookPath("go")
	if err != nil {
		return unavailable(err.Error())
	}
	root, err := os.MkdirTemp("", "concord-native-toolchain-")
	if err != nil {
		return e, err
	}
	defer func() { _ = os.RemoveAll(root) }()
	if err := nativeScratchDirectories(root); err != nil {
		return e, err
	}
	// Only the module cache location comes from host Go configuration;
	// compiler selection and build flags remain controlled.
	e.ModuleCache = os.Getenv("GOMODCACHE")
	if e.ModuleCache == "" {
		gopath := os.Getenv("GOPATH")
		if gopath == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return unavailable(err.Error())
			}
			gopath = filepath.Join(home, "go")
		}
		e.ModuleCache = filepath.Join(filepath.SplitList(gopath)[0], "pkg", "mod")
	}
	if !filepath.IsAbs(e.ModuleCache) {
		return unavailable("module cache must be an absolute path")
	}
	e.ModuleCache = filepath.Clean(e.ModuleCache)
	// A missing checksum proof must fail locally, not fall back to HTTPS.
	sumdb := url.URL{Scheme: "file", Path: filepath.Join(e.ModuleCache, "cache", "download", "sumdb", "sum.golang.org")}
	probe := func(toolchain string) (map[string]string, error) {
		cmd := exec.CommandContext(ctx, goPath, "env", "-json", "GOVERSION", "GOROOT", "GOMODCACHE", "GOOS", "GOARCH") //nolint:gosec // installed command, fixed argv.
		cmd.Dir = root
		cmd.Env = nativeControlledEnv(e, root)
		for i, value := range cmd.Env {
			switch {
			case strings.HasPrefix(value, "GOTOOLCHAIN="):
				cmd.Env[i] = "GOTOOLCHAIN=" + toolchain
			case strings.HasPrefix(value, "GOSUMDB="):
				cmd.Env[i] = "GOSUMDB=sum.golang.org " + sumdb.String()
			}
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		raw, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		var values map[string]string
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, fmt.Errorf("malformed Go environment: %w", err)
		}
		return values, nil
	}
	values, err := probe("local")
	if err != nil || values["GOVERSION"] != pinned {
		values, err = probe(pinned)
		if err != nil {
			return unavailable(err.Error())
		}
	}
	e.Version, e.GoRoot = values["GOVERSION"], values["GOROOT"]
	if e.Version != pinned || values["GOOS"] != e.GOOS || values["GOARCH"] != e.GOARCH || e.GoRoot == "" || values["GOMODCACHE"] != e.ModuleCache {
		return unavailable("resolved environment does not match the exact pin and native target")
	}
	e.GoExecutable, err = filepath.EvalSymlinks(filepath.Join(e.GoRoot, "bin", "go"))
	if err != nil {
		return unavailable(err.Error())
	}
	b, err := os.ReadFile(e.GoExecutable)
	if err != nil {
		return unavailable(err.Error())
	}
	var tools []NativeOracleFile
	for _, name := range []string{"pkg/tool/" + e.GOOS + "_" + e.GOARCH + "/compile", "pkg/tool/" + e.GOOS + "_" + e.GOARCH + "/link", "pkg/tool/" + e.GOOS + "_" + e.GOARCH + "/asm", "src/cmd/test2json/main.go", "src/cmd/internal/test2json/test2json.go"} {
		data, err := os.ReadFile(filepath.Join(e.GoRoot, filepath.FromSlash(name)))
		if err != nil {
			return unavailable("compiler, linker, or converter source: " + err.Error())
		}
		tools = append(tools, NativeOracleFile{Path: name, SHA256: nativeDigest(data)})
	}
	toolsRaw, _ := json.Marshal(tools)
	e.ToolchainIdentity = e.Version + ":" + nativeDigest(append(b, toolsRaw...))
	e.Digest = nativeDigest([]byte(e.ToolchainIdentity + "\x00" + e.GOOS + "\x00" + e.GOARCH + "\x00GOTOOLCHAIN=local\x00GOPROXY=off\x00GOSUMDB=off\x00GOFLAGS=-mod=readonly\x00CGO_ENABLED=0\x00GOWORK=off\x00GOENV=off\x00private-disposable-per-lease"))
	return e, nil
}

func nativeGitFile(ctx context.Context, r GitRunner, repo, commit, name string) (NativeOracleFile, []byte, error) {
	var f NativeOracleFile
	if err := validateWorkContextRepoPath(name); err != nil {
		return f, nil, err
	}
	raw, err := r.Run(ctx, repo, "ls-tree", "-z", commit, "--", name)
	if err != nil {
		return f, nil, err
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 2 || parts[1] != "" {
		return f, nil, nativeUnavailable("pinned path must resolve to one regular Git blob")
	}
	header, file, ok := strings.Cut(parts[0], "\t")
	fields := strings.Fields(header)
	if !ok || file != name || len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return f, nil, nativeUnavailable("symlink, gitlink, directory, or missing pinned file is unsupported")
	}
	b, err := r.Run(ctx, repo, "cat-file", "blob", fields[2])
	if err != nil {
		return f, nil, err
	}
	f = NativeOracleFile{Path: name, BlobOID: fields[2], SHA256: nativeDigest(b)}
	return f, b, nil
}

func normalizeNativeGoOraclePlan(ctx context.Context, r GitRunner, entry WorktreeEntry, p nativeOraclePlan, f NativeOracleFile, raw []byte, req WorktreeVerifyRequest) (nativeOraclePlan, error) {
	b := p.Bundle
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p.Manifest); err != nil {
		return p, nativeUnavailable("recipe is not one closed Go manifest")
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return p, nativeUnavailable("recipe contains trailing input")
	}
	p.ManifestFile = f
	m := p.Manifest
	if m.Kind != "go_top_level_v1" || m.PackageCwd != b.Control.Cwd || len(m.TestFiles) < 1 || len(m.TestFiles) > 16 || len(m.FixtureFiles) > 16 {
		return p, nativeUnavailable("unsupported recipe family, cwd, or file inventory")
	}
	if err := validateWorkContextRepoPath(m.PackageCwd); err != nil {
		return p, err
	}
	// The harness removes the recipe from the snapshot before it writes the
	// declared files. A recipe that names a Go source, a module file, or a
	// declared input would delete candidate or harness bytes, so it refuses
	// here, before any snapshot exists.
	if manifest := b.Control.RecipeSource.Path; strings.HasSuffix(manifest, ".go") || slices.Contains([]string{"go.mod", "go.sum", "go.work"}, path.Base(manifest)) || slices.Contains(m.TestFiles, manifest) || slices.Contains(m.FixtureFiles, manifest) {
		return p, nativeUnavailable("recipe path collides with a Go source, module file, or declared harness input")
	}
	if _, err := nativeOracleSelector(m, b.Control); err != nil {
		return p, err
	}
	if err := collectNativeGoHarnessFiles(ctx, r, entry, &p); err != nil {
		return p, err
	}
	moduleInputs, pinned, err := nativeGoModuleInputs(ctx, r, entry, p)
	if err != nil {
		return p, err
	}
	if !bytes.Equal(moduleInputs[0], moduleInputs[1]) {
		return p, nativeUnavailable("candidate module dependency inputs differ from pinned preparation")
	}
	p.Environment, err = resolveNativeGoEnvironment(ctx, pinned)
	if err != nil {
		return p, err
	}
	p.Environment.ModuleInputsDigest = nativeDigest(moduleInputs[0])
	p.Environment.Digest = nativeDigest([]byte(p.Environment.Digest + "\x00" + p.Environment.ModuleInputsDigest))
	p.Stages = []NativeOracleStage{{Name: "dependencies", Argv: []string{"go", "mod", "verify"}}, {Name: "metadata", Argv: []string{"go", "list", "-json", "-deps", "-test", "."}}, {Name: "compile", Argv: []string{"go", "test", "-c", "-o", "$scratch/oracle.test", "."}}, {Name: "converter_compile", Argv: []string{"go", "build", "-o", "$scratch/test2json", "cmd/test2json"}}}
	if req.Oracle.Phase == "execute" {
		p.Stages = append(p.Stages, NativeOracleStage{Name: "test", Argv: []string{"$scratch/oracle.test", "-test.count=1", "-test.run=" + b.Control.Argv[4], "-test.v=test2json", "-test.paniconexit0"}}, NativeOracleStage{Name: "convert", Argv: []string{"$scratch/test2json", "-p", "$package"}})
	}
	for _, stage := range p.Stages {
		if err := validateWorktreeVerifyCommand(stage.Argv); err != nil {
			return p, err
		}
	}
	return p, nil
}

func collectNativeGoHarnessFiles(ctx context.Context, r GitRunner, entry WorktreeEntry, p *nativeOraclePlan) error {
	m, b := p.Manifest, p.Bundle
	seen := map[string]bool{}
	total := 0
	for i, names := range [][]string{m.TestFiles, m.FixtureFiles} {
		for _, name := range names {
			if seen[name] || !strings.HasPrefix(name, m.PackageCwd+"/") || (i == 0 && !strings.HasSuffix(name, "_test.go")) || (i == 1 && (strings.HasSuffix(name, ".go") || path.Base(name) == "go.mod" || path.Base(name) == "go.sum")) {
				return nativeUnavailable("manifest paths must be unique contained test or data files, never production/module replacements")
			}
			seen[name] = true
			file, data, err := nativeGitFile(ctx, r, entry.Path, b.Control.RecipeSource.CommitOID, name)
			if err != nil {
				return err
			}
			total += len(data)
			if total > 4*1024*1024 {
				return oracleFailure(KindLimitExceeded, "named harness inputs exceed 4 MiB", "bound the declared test and fixture bytes")
			}
			p.Files = append(p.Files, file)
		}
	}
	slices.SortFunc(p.Files, func(a, b NativeOracleFile) int { return strings.Compare(a.Path, b.Path) })
	return nil
}

func nativeGoModuleInputs(ctx context.Context, r GitRunner, entry WorktreeEntry, p nativeOraclePlan) ([][]byte, string, error) {
	var moduleInputs [][]byte
	var pinned string
	for _, commit := range []string{p.SubjectCommit, p.Bundle.Control.RecipeSource.CommitOID} {
		_, mod, err := nativeGitFile(ctx, r, entry.Path, commit, "go.mod")
		if err != nil {
			return nil, "", nativeUnavailable("supported recipe requires a pinned root go.mod")
		}
		pin, err := nativePinnedGoToolchain(mod)
		if err != nil {
			return nil, "", err
		}
		if pinned == "" {
			pinned = pin
		}
		var sum []byte
		listed, err := r.Run(ctx, entry.Path, "ls-tree", "-z", commit, "--", "go.sum")
		if err != nil {
			return nil, "", err
		}
		if len(listed) > 0 {
			_, sum, err = nativeGitFile(ctx, r, entry.Path, commit, "go.sum")
			if err != nil {
				return nil, "", err
			}
		}
		moduleInputs = append(moduleInputs, append(append(bytes.Clone(mod), 0), sum...))
	}
	return moduleInputs, pinned, nil
}

func nativePinnedGoToolchain(mod []byte) (string, error) {
	var toolchain, language string
	for _, line := range strings.Split(string(mod), "\n") {
		line, _, _ = strings.Cut(line, "//")
		fields := strings.Fields(line)
		if len(fields) == 0 || (fields[0] != "toolchain" && fields[0] != "go") {
			continue
		}
		if len(fields) != 2 {
			return "", nativeUnavailable("malformed module Go version directive")
		}
		target := &language
		if fields[0] == "toolchain" {
			target = &toolchain
		}
		if *target != "" {
			return "", nativeUnavailable("duplicate module Go version directive")
		}
		*target = fields[1]
	}
	if toolchain == "" {
		toolchain = "go" + language
		if strings.Count(language, ".") == 1 && version.Compare(toolchain, "go1.21") >= 0 {
			toolchain += ".0"
		}
	}
	if !version.IsValid(toolchain) || !nativeGoToolchainName.MatchString(toolchain) {
		return "", nativeUnavailable("module must name an exact Go toolchain or Go version")
	}
	return toolchain, nil
}

// A private index materializes regular Git blobs once. Links are refused
// before checkout, and hashes and executable modes verify the exported tree.
func materializeNativeSnapshot(ctx context.Context, r GitRunner, repo, commit, root string) error {
	raw, err := r.Run(ctx, repo, "ls-tree", "-r", "-z", commit)
	if err != nil {
		return err
	}
	type blob struct{ name, mode, oid string }
	var blobs []blob
	names := map[string]bool{}
	for _, entry := range strings.Split(string(raw), "\x00") {
		if entry == "" {
			continue
		}
		header, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
			return nativeUnavailable("snapshot contains unsupported symlink or gitlink")
		}
		if !fs.ValidPath(name) || name == ".git" || strings.HasPrefix(name, ".git/") {
			return nativeUnavailable("snapshot path is not contained")
		}
		blobs = append(blobs, blob{name, fields[0], fields[2]})
		names[name] = true
	}
	scratch, err := os.MkdirTemp("", "concord-native-index-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	if err := nativeScratchDirectories(scratch); err != nil {
		return err
	}
	env := append(nativeControlledEnv(nativeGoEnvironment{}, scratch), "GIT_INDEX_FILE="+filepath.Join(scratch, "index"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	for _, args := range [][]string{{"read-tree", commit}, {"checkout-index", "-a", "--prefix=" + root + string(filepath.Separator)}} {
		cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // fixed Git commands, separate immutable-subject argv.
		cmd.Dir, cmd.Env = repo, env
		if output, err := cmd.CombinedOutput(); err != nil {
			return nativeUnavailable(fmt.Sprintf("snapshot materialization: %v: %s", err, output))
		}
	}
	rooted, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = rooted.Close() }()
	for _, b := range blobs {
		if err := validateNativeSnapshotBlob(rooted, b.name, b.mode, b.oid); err != nil {
			return err
		}
	}
	return fs.WalkDir(rooted.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && !names[name] {
			return nativeUnavailable("snapshot contains a file outside the subject tree: " + name)
		}
		return nil
	})
}

func validateNativeSnapshotBlob(rooted *os.Root, name, mode, wantOID string) error {
	info, err := rooted.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || (info.Mode().Perm()&0111 != 0) != (mode == "100755") {
		return nativeUnavailable("snapshot file mode differs from subject: " + name)
	}
	data, err := rooted.ReadFile(name)
	if err != nil {
		return err
	}
	object := append([]byte(fmt.Sprintf("blob %d\x00", len(data))), data...)
	var oid string
	switch len(wantOID) {
	case 40:
		sum := sha1.Sum(object) //nolint:gosec // verify Git SHA-1 object identity, not a security primitive.
		oid = fmt.Sprintf("%x", sum)
	case 64:
		sum := sha256.Sum256(object)
		oid = fmt.Sprintf("%x", sum)
	default:
		return nativeUnavailable("unsupported Git object identity")
	}
	if oid != wantOID {
		return nativeUnavailable("snapshot blob differs from subject: " + name)
	}
	return nil
}

func nativeReplaceHarness(ctx context.Context, r GitRunner, repo, root string, p nativeOraclePlan) error {
	rooted, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = rooted.Close() }()
	// The recipe is producer metadata, not an undeclared runtime fixture.
	if err := rooted.Remove(p.ManifestFile.Path); err != nil && !os.IsNotExist(err) {
		return err
	}
	cwd := filepath.Join(root, filepath.FromSlash(p.Bundle.Control.Cwd))
	entries, err := os.ReadDir(cwd)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), "_test.go") {
			if err := os.Remove(filepath.Join(cwd, entry.Name())); err != nil {
				return err
			}
		}
	}
	for _, f := range p.Files {
		observed, data, err := nativeGitFile(ctx, r, repo, p.Bundle.Control.RecipeSource.CommitOID, f.Path)
		if err != nil {
			return err
		}
		if observed != f {
			return nativeUnavailable("pinned harness bytes changed")
		}
		if err := rooted.MkdirAll(path.Dir(f.Path), 0700); err != nil {
			return err
		}
		if err := rooted.WriteFile(f.Path, data, 0600); err != nil {
			return err
		}
	}
	return nil
}

func validateNativeOraclePackageInputs(root string, p nativeOraclePlan) (string, error) {
	rooted, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer func() { _ = rooted.Close() }()
	fixtures := map[string]NativeOracleFile{}
	for _, f := range p.Files {
		if slices.Contains(p.Manifest.FixtureFiles, f.Path) {
			fixtures[f.Path] = f
		}
	}
	err = fs.WalkDir(rooted.FS(), p.Bundle.Control.Cwd, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nativeUnavailable("package input symlink is unsupported")
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nativeUnavailable("package inputs must be regular files")
		}
		rel := name
		data, err := rooted.ReadFile(name)
		if err != nil {
			return err
		}
		allowed := strings.HasSuffix(rel, ".go") || rel == "go.mod" || rel == "go.sum"
		if f, ok := fixtures[rel]; ok {
			allowed = f.SHA256 == nativeDigest(data)
		}
		if !allowed {
			return nativeUnavailable("undeclared non-Go package input: " + rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if _, err := rooted.Stat("go.work"); err == nil {
		return "", nativeUnavailable("Go workspaces are unsupported")
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return nativeSnapshotInputDigest(root)
}

// Fingerprint the complete materialized source, including local Go dependencies.
// No scratch compiler output or disposable cache lives in this source root.
func nativeSnapshotInputDigest(root string) (string, error) {
	rooted, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer func() { _ = rooted.Close() }()
	var inventory []NativeOracleFile
	err = fs.WalkDir(rooted.FS(), ".", func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nativeUnavailable("source input symlink is unsupported")
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nativeUnavailable("source inputs must remain regular files")
		}
		data, err := rooted.ReadFile(name)
		if err != nil {
			return err
		}
		inventory = append(inventory, NativeOracleFile{Path: name, SHA256: nativeDigest(data)})
		return nil
	})
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(inventory)
	if err != nil {
		return "", err
	}
	return nativeDigest(data), nil
}

type nativeGoPackage struct {
	Dir                                                         string
	ImportPath                                                  string
	Name                                                        string
	GoFiles, TestGoFiles, XTestGoFiles, CgoFiles                []string
	SFiles, CFiles, CXXFiles, MFiles, HFiles, FFiles, SysoFiles []string
	EmbedFiles, TestEmbedFiles, XTestEmbedFiles                 []string
	Module                                                      *struct {
		Dir     string
		Replace *struct{ Dir string }
	}
	Error *struct{ Err string }
}

func validateNativeGoMetadata(raw []byte, root string, p nativeOraclePlan) (string, error) {
	rooted, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer func() { _ = rooted.Close() }()
	d := json.NewDecoder(bytes.NewReader(raw))
	var target *nativeGoPackage
	fixtures := map[string]NativeOracleFile{}
	for _, f := range p.Files {
		if slices.Contains(p.Manifest.FixtureFiles, f.Path) {
			fixtures[f.Path] = f
		}
	}
	cwd := filepath.Join(root, filepath.FromSlash(p.Bundle.Control.Cwd))
	for {
		var pkg nativeGoPackage
		err := d.Decode(&pkg)
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", nativeUnavailable("Go build metadata is malformed")
		}
		if err := validateNativeGoPackageMetadata(pkg, root); err != nil {
			return "", err
		}
		if pkg.Dir == cwd && !strings.Contains(pkg.ImportPath, " [") && !strings.HasSuffix(pkg.ImportPath, ".test") {
			selectedPackage := pkg
			target = &selectedPackage
		}
		if !nativeContained(root, pkg.Dir) {
			continue
		}
		if pkg.Dir != cwd {
			if err := validateNativeLocalPackageClosure(root, pkg.Dir, p); err != nil {
				return "", err
			}
		}
		if err := validateNativeGoEmbeds(rooted, root, pkg, fixtures); err != nil {
			return "", err
		}
	}
	if target == nil {
		return "", nativeUnavailable("declared package has no build-selected metadata")
	}
	if err := validateNativeGoSelectedTests(rooted, root, cwd, *target, p); err != nil {
		return "", err
	}
	return target.ImportPath, nil
}

func validateNativeGoSelectedTests(rooted *os.Root, root, cwd string, target nativeGoPackage, p nativeOraclePlan) error {
	selected, err := nativeGoSelectedTestFiles(root, cwd, target, p.Manifest.TestFiles)
	if err != nil {
		return err
	}
	found := map[string]bool{}
	for _, name := range selected {
		if err := nativeGoFileTests(rooted, path.Join(p.Bundle.Control.Cwd, name), found); err != nil {
			return err
		}
	}
	names, err := nativeOracleSelector(p.Manifest, p.Bundle.Control)
	if err != nil {
		return err
	}
	for _, name := range names {
		if !found[name] {
			return nativeUnavailable("selected top-level test is not build-selected: " + name)
		}
	}
	return nil
}

func validateNativeGoPackageMetadata(pkg nativeGoPackage, root string) error {
	if pkg.Error != nil || len(pkg.CgoFiles) > 0 {
		return nativeUnavailable("Go package has unavailable inputs or cgo")
	}
	if nativeContained(root, pkg.Dir) && (len(pkg.SFiles)+len(pkg.CFiles)+len(pkg.CXXFiles)+len(pkg.MFiles)+len(pkg.HFiles)+len(pkg.FFiles)+len(pkg.SysoFiles) > 0) {
		return nativeUnavailable("repository compiler inputs are not pure Go")
	}
	if pkg.Module != nil && pkg.Module.Replace != nil && pkg.Module.Replace.Dir != "" && !nativeContained(root, pkg.Module.Replace.Dir) {
		return nativeUnavailable("local module replacement escapes the controlled snapshot")
	}
	return nil
}

func validateNativeGoEmbeds(rooted *os.Root, root string, pkg nativeGoPackage, fixtures map[string]NativeOracleFile) error {
	for _, names := range [][]string{pkg.EmbedFiles, pkg.TestEmbedFiles, pkg.XTestEmbedFiles} {
		for _, name := range names {
			absolute := filepath.Join(pkg.Dir, name)
			rel, err := filepath.Rel(root, absolute)
			if err != nil {
				return err
			}
			f, ok := fixtures[filepath.ToSlash(rel)]
			if !ok {
				return nativeUnavailable("resolved repository embed is not a declared pinned fixture: " + filepath.ToSlash(rel))
			}
			b, err := rooted.ReadFile(rel)
			if err != nil || nativeDigest(b) != f.SHA256 {
				return nativeUnavailable("resolved embed differs from its pinned fixture")
			}
		}
	}
	return nil
}

func nativeGoSelectedTestFiles(root, cwd string, target nativeGoPackage, testFiles []string) ([]string, error) {
	selected := append(slices.Clone(target.TestGoFiles), target.XTestGoFiles...)
	sort.Strings(selected)
	declared := make([]string, 0, len(testFiles))
	for _, name := range testFiles {
		rel, err := filepath.Rel(cwd, filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		declared = append(declared, rel)
	}
	sort.Strings(declared)
	if !slices.Equal(selected, declared) {
		return nil, nativeUnavailable("manifest test files are absent or excluded by the pinned build environment")
	}
	return selected, nil
}

func nativeGoFileTests(rooted *os.Root, name string, found map[string]bool) error {
	data, err := rooted.ReadFile(name)
	if err != nil {
		return err
	}
	file, err := parser.ParseFile(token.NewFileSet(), name, data, 0)
	if err != nil {
		return err
	}
	aliases := map[string]bool{}
	for _, imp := range file.Imports {
		if imp.Path.Value == `"testing"` {
			alias := "testing"
			if imp.Name != nil {
				alias = imp.Name.Name
			}
			aliases[alias] = true
		}
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		if fn.Name.Name == "TestMain" {
			return nativeUnavailable("custom TestMain is unsupported")
		}
		if !nativeOracleTestName.MatchString(fn.Name.Name) {
			continue
		}
		if found[fn.Name.Name] {
			return nativeUnavailable("duplicate top-level test name")
		}
		if err := validateNativeGoTestSignature(fn, aliases); err != nil {
			return err
		}
		found[fn.Name.Name] = true
	}
	return nil
}

func validateNativeGoTestSignature(fn *ast.FuncDecl, aliases map[string]bool) error {
	if fn.Type.TypeParams != nil || (fn.Type.Results != nil && len(fn.Type.Results.List) != 0) || fn.Type.Params == nil || len(fn.Type.Params.List) != 1 || len(fn.Type.Params.List[0].Names) > 1 {
		return nativeUnavailable("test signature is not func TestXxx(*testing.T)")
	}
	star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return nativeUnavailable("unsupported test signature")
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "T" {
		return nativeUnavailable("unsupported test signature")
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok || !aliases[ident.Name] {
		return nativeUnavailable("test parameter must be testing.T")
	}
	return nil
}

func nativeContained(root, name string) bool {
	rel, err := filepath.Rel(root, name)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func validateNativeLocalPackageClosure(root, dir string, p nativeOraclePlan) error {
	rooted, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = rooted.Close() }()
	relativeDir, err := filepath.Rel(root, dir)
	if err != nil {
		return err
	}
	return fs.WalkDir(rooted.FS(), filepath.ToSlash(relativeDir), func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nativeUnavailable("repository dependency contains a nonregular input")
		}
		rel := name
		if strings.HasSuffix(rel, ".go") || rel == "go.mod" || rel == "go.sum" {
			return nil
		}
		for _, f := range p.Files {
			if f.Path == rel && slices.Contains(p.Manifest.FixtureFiles, rel) {
				b, err := rooted.ReadFile(name)
				if err != nil {
					return err
				}
				if nativeDigest(b) == f.SHA256 {
					return nil
				}
			}
		}
		return nativeUnavailable("undeclared non-Go repository dependency input: " + rel)
	})
}

func nativeOracleWitnesses(raw []byte, pkg string, selected []string, stdout []byte) ([]string, error) {
	if err := validateNativeOracleWitnessEvents(raw, pkg, selected); err != nil {
		return nil, err
	}
	if err := validateNativeOracleWitnessFrames(stdout, selected); err != nil {
		return nil, err
	}
	return slices.Clone(selected), nil
}

func validateNativeOracleWitnessEvents(raw []byte, pkg string, selected []string) error {
	state := map[string]int{}
	for _, name := range selected {
		state[name] = 0
	}
	terminal := false
	started := false
	d := json.NewDecoder(bytes.NewReader(raw))
	for {
		var e struct{ Action, Package, Test string }
		err := d.Decode(&e)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nativeUnavailable("test2json conversion is malformed")
		}
		if terminal || e.Package != pkg || e.Action == "fail" {
			return nativeUnavailable("test2json contains conflicting or post-terminal events")
		}
		if e.Action == "start" {
			if started || e.Test != "" {
				return nativeUnavailable("test2json contains a conflicting package start")
			}
			started = true
			continue
		}
		if e.Action == "pass" && e.Test == "" {
			for _, phase := range state {
				if phase != 2 {
					return nativeUnavailable("package passed before every selected witness passed")
				}
			}
			terminal = true
			continue
		}
		if err := applyNativeOracleWitnessEvent(state, e.Action, e.Test); err != nil {
			return err
		}
	}
	if !terminal || len(state) == 0 {
		return nativeUnavailable("test2json has no complete package pass")
	}
	return nil
}

func applyNativeOracleWitnessEvent(state map[string]int, action, test string) error {
	root, _, nested := strings.Cut(test, "/")
	phase, known := state[root]
	switch action {
	case "output":
	case "pause", "cont":
		if !known || phase != 1 {
			return nativeUnavailable("test2json continues a witness that is not running")
		}
	case "run":
		if !known || (!nested && phase != 0) || (nested && phase != 1) {
			return nativeUnavailable("test2json contains duplicate or undeclared witness runs")
		}
		if !nested {
			state[root] = 1
		}
	case "pass":
		if !known || phase != 1 {
			return nativeUnavailable("test2json contains a duplicate or unordered witness pass")
		}
		if !nested {
			state[root] = 2
		}
	case "skip":
		if !nested || !known || phase != 1 {
			return nativeUnavailable("selected witness was skipped or undeclared")
		}
	default:
		return nativeUnavailable("test2json contains an unsupported action")
	}
	return nil
}

func validateNativeOracleWitnessFrames(stdout []byte, selected []string) error {
	// The converter also recognizes unmarked text before marker mode begins.
	// Require the selected runs, passes, and trailer in the raw framed stream;
	// framing is not an authenticated origin and does not defeat init forgery.
	runs, passes := map[string]int{}, map[string]int{}
	trailers := 0
	for rest := stdout; ; {
		_, frame, found := bytes.Cut(rest, []byte{0x16})
		if !found {
			break
		}
		rest = frame
		lineBytes, _, _ := bytes.Cut(frame, []byte{'\n'})
		line := string(lineBytes)
		if line == "PASS" {
			trailers++
		}
		for _, name := range selected {
			if line == "=== RUN   "+name {
				runs[name]++
			}
			if strings.HasPrefix(line, "--- PASS: "+name+" (") && strings.HasSuffix(line, "s)") {
				passes[name]++
			}
		}
	}
	if trailers != 1 {
		return nativeUnavailable("raw test stdout lacks exactly one framed package pass")
	}
	for _, name := range selected {
		if runs[name] != 1 || passes[name] != 1 {
			return nativeUnavailable("raw test stdout lacks unique framed witness events: " + name)
		}
	}
	return nil
}

func (s *Store) runNativeGoOracle(ctx context.Context, r GitRunner, entry WorktreeEntry, req WorktreeVerifyRequest, p nativeOraclePlan, capture *nativeStreamCapture, result *NativeOracleResult) (returnErr error) {
	root, err := os.MkdirTemp("", "concord-native-oracle-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(root); err != nil && returnErr == nil {
			returnErr = err
		}
	}()
	cache := filepath.Join(root, "cache")
	snapshot := filepath.Join(root, "snapshot")
	if err := nativeScratchDirectories(root); err != nil {
		return err
	}
	if err := os.Mkdir(snapshot, 0700); err != nil {
		return err
	}
	commit := p.SubjectCommit
	if req.Oracle.Phase == "prepare" {
		commit = p.Bundle.Control.RecipeSource.CommitOID
	}
	if err := materializeNativeSnapshot(ctx, r, entry.Path, commit, snapshot); err != nil {
		return err
	}
	if err := nativeReplaceHarness(ctx, r, entry.Path, snapshot, p); err != nil {
		return err
	}
	digest, err := validateNativeOraclePackageInputs(snapshot, p)
	if err != nil {
		return err
	}
	result.InputManifestDigest = digest
	env := nativeControlledEnv(p.Environment, root)
	cwd := filepath.Join(snapshot, filepath.FromSlash(p.Bundle.Control.Cwd))
	if !nativeContained(snapshot, cwd) {
		return nativeUnavailable("effective cwd escapes candidate snapshot")
	}
	rooted, err := os.OpenRoot(snapshot)
	if err != nil {
		return err
	}
	defer func() { _ = rooted.Close() }()
	packageRoot, err := rooted.OpenRoot(p.Bundle.Control.Cwd)
	if err != nil {
		return nativeUnavailable("effective cwd is not rooted in the candidate snapshot")
	}
	defer func() { _ = packageRoot.Close() }()
	scratchRoot, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = scratchRoot.Close() }()
	run := nativeGoOracleRun{store: s, req: req, plan: p, capture: capture, result: result,
		root: root, snapshot: snapshot, cache: cache, cwd: cwd, env: env, scratchRoot: scratchRoot,
		digest: digest, testStdout: []byte{}, programExit: -1}
	return run.execute(ctx)
}

type nativeGoOracleRun struct {
	store                              *Store
	req                                WorktreeVerifyRequest
	plan                               nativeOraclePlan
	capture                            *nativeStreamCapture
	result                             *NativeOracleResult
	root, snapshot, cache, cwd, digest string
	env                                []string
	scratchRoot                        *os.Root
	packagePath                        string
	testStdout                         []byte
	programExit                        int
	converterDigest                    string
}

func (n *nativeGoOracleRun) execute(ctx context.Context) error {
	for _, stage := range n.plan.Stages {
		if err := n.validateStageInputs(stage.Name); err != nil {
			return err
		}
		exit, stdout, err := n.runStage(ctx, stage)
		if err != nil {
			return err
		}
		if stage.Name == "compile" && exit != 0 && n.req.Oracle.Phase == "execute" {
			n.result.Qualification = "fail"
			return nil
		}
		if exit != 0 && stage.Name != "test" {
			return nativeUnavailable("native " + stage.Name + " returned a nonzero exit")
		}
		if err := n.consumeStage(stage.Name, exit, stdout); err != nil {
			return err
		}
	}
	return n.finish()
}

func (n *nativeGoOracleRun) validateStageInputs(name string) error {
	cacheInfo, err := os.Lstat(n.cache)
	if err != nil || !cacheInfo.IsDir() || cacheInfo.Mode()&os.ModeSymlink != 0 {
		return nativeUnavailable("disposable cache no longer realizes the leased plan slot")
	}
	if name == "test" {
		actual, err := validateNativeOraclePackageInputs(n.snapshot, n.plan)
		if err != nil || actual != n.digest {
			return nativeUnavailable("candidate inputs changed before test launch")
		}
		binary, err := n.scratchRoot.ReadFile("oracle.test")
		if err != nil || nativeDigest(binary) != n.result.BinaryDigest {
			return nativeUnavailable("test binary changed before launch")
		}
	}
	if name == "convert" {
		converter, err := n.scratchRoot.ReadFile("test2json")
		if err != nil || n.converterDigest == "" || nativeDigest(converter) != n.converterDigest {
			return nativeUnavailable("trusted converter binary changed before conversion")
		}
	}
	return nil
}

func (n *nativeGoOracleRun) stageArgv(stage NativeOracleStage) []string {
	argv := slices.Clone(stage.Argv)
	for i, part := range argv {
		switch {
		case part == "go":
			argv[i] = n.plan.Environment.GoExecutable
		case strings.HasPrefix(part, "$scratch/"):
			argv[i] = filepath.Join(n.root, strings.TrimPrefix(part, "$scratch/"))
		case part == "$package":
			argv[i] = n.packagePath
		}
	}
	return argv
}

func (n *nativeGoOracleRun) runStage(ctx context.Context, stage NativeOracleStage) (int, []byte, error) {
	argv := n.stageArgv(stage)
	s, capture, result := n.store, n.capture, n.result
	var stdin io.Reader
	if stage.Name == "convert" {
		stdin = bytes.NewReader(n.testStdout)
	}
	so, se := len(capture.stdout), len(capture.stderr)
	var launched func()
	if stage.Name == "test" {
		launched = s.nativeOracleProgramLaunched
	}
	if s.nativeOracleStageObserved != nil {
		s.nativeOracleStageObserved(stage.Name, n.cwd, slices.Clone(argv), slices.Clone(n.env))
	}
	stageCtx := ctx
	cancel := func() {}
	if stage.Name == "test" {
		var err error
		stageCtx, cancel, err = nativeOracleTestContext(ctx, n.req)
		if err != nil {
			return 0, nil, err
		}
	}
	stageCapture := nativeOracleStageCapture(stage.Name, capture)
	exit, runErr := runNativeOracleStage(stageCtx, n.cwd, argv, n.env, stdin, stageCapture, launched)
	cancel()
	stage.ExitCode = exit
	stage.StdoutOffset = so
	stage.StderrOffset = se
	stage.StdoutLength = len(capture.stdout) - so
	stage.StderrLength = len(capture.stderr) - se
	result.Stages = append(result.Stages, stage)
	if runErr != nil || !stageCapture.complete {
		return 0, nil, nativeUnavailable(fmt.Sprintf("native %s failed: %v", stage.Name, runErr))
	}
	if stage.Name == "metadata" {
		return exit, stageCapture.stdout, nil
	}
	return exit, capture.stdout[so:], nil
}

func (n *nativeGoOracleRun) consumeStage(name string, exit int, stdout []byte) error {
	var err error
	result := n.result
	switch name {
	case "metadata":
		n.packagePath, err = validateNativeGoMetadata(stdout, n.snapshot, n.plan)
		if err != nil {
			return err
		}
	case "compile":
		stat, err := n.scratchRoot.Stat("oracle.test")
		if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0111 == 0 {
			return nativeUnavailable("compiler produced no regular executable")
		}
		binary, err := n.scratchRoot.ReadFile("oracle.test")
		if err != nil {
			return err
		}
		result.BinaryDigest = nativeDigest(binary)
	case "converter_compile":
		stat, err := n.scratchRoot.Lstat("test2json")
		if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0111 == 0 {
			return nativeUnavailable("converter compiler produced no regular executable")
		}
		binary, err := n.scratchRoot.ReadFile("test2json")
		if err != nil {
			return err
		}
		n.converterDigest = nativeDigest(binary)
	case "test":
		n.programExit = exit
		n.testStdout = bytes.Clone(stdout)
	case "convert":
		if n.programExit != 0 {
			result.Qualification = "fail"
		} else {
			result.ObservedTestNames, err = nativeOracleWitnesses(stdout, n.packagePath, result.SelectedTestNames, n.testStdout)
			if err != nil {
				return err
			}
			result.Qualification = "pass"
		}
	}
	return nil
}

func (n *nativeGoOracleRun) finish() error {
	actual, err := validateNativeOraclePackageInputs(n.snapshot, n.plan)
	if err != nil || actual != n.digest {
		return nativeUnavailable("consumed candidate inputs changed during execution")
	}
	binary, err := n.scratchRoot.ReadFile("oracle.test")
	if err != nil || nativeDigest(binary) != n.result.BinaryDigest {
		return nativeUnavailable("consumed binary changed during execution")
	}
	converter, err := n.scratchRoot.ReadFile("test2json")
	if err != nil || nativeDigest(converter) != n.converterDigest {
		return nativeUnavailable("consumed converter changed during execution")
	}
	if n.req.Oracle.Phase == "prepare" {
		n.result.Qualification = "ready"
	}
	return nil
}
