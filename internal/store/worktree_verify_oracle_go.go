package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
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

func nativeControlledEnv(e nativeGoEnvironment, cache string) []string {
	var env []string
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if strings.HasPrefix(key, "GO") || strings.HasPrefix(key, "CGO") || key == "CC" || key == "CXX" {
			continue
		}
		env = append(env, v)
	}
	env = append(env, "GOTOOLCHAIN=local", "GOPROXY=off", "GOFLAGS=-mod=readonly", "CGO_ENABLED=0", "GOWORK=off", "GOENV=off", "GOOS="+e.GOOS, "GOARCH="+e.GOARCH)
	if e.GoRoot != "" {
		env = append(env, "GOROOT="+e.GoRoot)
	}
	if e.ModuleCache != "" {
		env = append(env, "GOMODCACHE="+e.ModuleCache)
	}
	if cache != "" {
		env = append(env, "GOCACHE="+cache)
	}
	return env
}

func resolveNativeGoEnvironment(ctx context.Context) (nativeGoEnvironment, error) {
	e := nativeGoEnvironment{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	goPath, err := exec.LookPath("go")
	if err != nil {
		return e, nativeUnavailable("installed Go executable is unavailable")
	}
	goPath, err = filepath.EvalSymlinks(goPath)
	if err != nil {
		return e, err
	}
	e.GoExecutable = goPath
	cmd := exec.CommandContext(ctx, goPath, "env", "-json", "GOVERSION", "GOROOT", "GOMODCACHE", "GOOS", "GOARCH") //nolint:gosec // installed executable read, fixed argv.
	cmd.Env = nativeControlledEnv(e, "")
	raw, err := cmd.Output()
	if err != nil {
		return e, nativeUnavailable("installed local Go environment is unavailable")
	}
	var values map[string]string
	if json.Unmarshal(raw, &values) != nil {
		return e, nativeUnavailable("Go environment metadata is malformed")
	}
	e.Version = values["GOVERSION"]
	e.GoRoot = values["GOROOT"]
	e.ModuleCache = values["GOMODCACHE"]
	if e.Version != "go1.26.7" || values["GOOS"] != e.GOOS || values["GOARCH"] != e.GOARCH || e.GoRoot == "" || e.ModuleCache == "" {
		return e, nativeUnavailable("installed toolchain must be go1.26.7 on the native target")
	}
	b, err := os.ReadFile(goPath)
	if err != nil {
		return e, err
	}
	var tools []NativeOracleFile
	for _, name := range []string{"pkg/tool/" + e.GOOS + "_" + e.GOARCH + "/compile", "pkg/tool/" + e.GOOS + "_" + e.GOARCH + "/link", "pkg/tool/" + e.GOOS + "_" + e.GOARCH + "/asm", "src/cmd/test2json/main.go", "src/cmd/internal/test2json/test2json.go"} {
		data, err := os.ReadFile(filepath.Join(e.GoRoot, filepath.FromSlash(name)))
		if err != nil {
			return e, nativeUnavailable("pinned compiler, linker, or converter source is unavailable")
		}
		tools = append(tools, NativeOracleFile{Path: name, SHA256: nativeDigest(data)})
	}
	toolsRaw, _ := json.Marshal(tools)
	e.ToolchainIdentity = e.Version + ":" + nativeDigest(append(b, toolsRaw...))
	e.Digest = nativeDigest([]byte(e.ToolchainIdentity + "\x00" + e.GOOS + "\x00" + e.GOARCH + "\x00GOTOOLCHAIN=local\x00GOPROXY=off\x00GOFLAGS=-mod=readonly\x00CGO_ENABLED=0\x00GOWORK=off\x00GOENV=off\x00private-disposable-per-lease"))
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

func normalizeNativeOraclePlan(ctx context.Context, r GitRunner, entry WorktreeEntry, req WorktreeVerifyRequest, b NativeOracleControlBundle, version int64, subject, identity string, auth nativeOracleAuthorization) (nativeOraclePlan, error) {
	p := nativeOraclePlan{Request: *req.Oracle, RequestID: req.RequestID, WorkID: req.WorkID, ProjectID: req.ProjectID, ContractVersion: version, SubjectCommit: subject, WorktreeIdentity: identity, Bundle: b, BundleDigest: nativeBundleDigest(b), Authorization: auth, StreamLimit: nativeOracleStreamLimit, CachePolicy: "private-disposable-per-lease"}
	if b.Control.RecipeSource.ProjectID != req.ProjectID || b.Owner.Mechanism.ProjectID != req.ProjectID {
		return p, nativeUnavailable("this recipe requires the ambient Project's single module")
	}
	f, raw, err := nativeGitFile(ctx, r, entry.Path, b.Control.RecipeSource.CommitOID, b.Control.RecipeSource.Path)
	if err != nil {
		return p, err
	}
	if len(raw) > 16*1024 {
		return p, oracleFailure(KindLimitExceeded, "recipe manifest exceeds 16384 bytes", "bound the pinned manifest")
	}
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
	seen := map[string]bool{}
	total := 0
	for i, names := range [][]string{m.TestFiles, m.FixtureFiles} {
		for _, name := range names {
			if seen[name] || !strings.HasPrefix(name, m.PackageCwd+"/") || (i == 0 && !strings.HasSuffix(name, "_test.go")) || (i == 1 && (strings.HasSuffix(name, ".go") || path.Base(name) == "go.mod" || path.Base(name) == "go.sum")) {
				return p, nativeUnavailable("manifest paths must be unique contained test or data files, never production/module replacements")
			}
			seen[name] = true
			file, data, err := nativeGitFile(ctx, r, entry.Path, b.Control.RecipeSource.CommitOID, name)
			if err != nil {
				return p, err
			}
			total += len(data)
			if total > 4*1024*1024 {
				return p, oracleFailure(KindLimitExceeded, "named harness inputs exceed 4 MiB", "bound the declared test and fixture bytes")
			}
			p.Files = append(p.Files, file)
		}
	}
	slices.SortFunc(p.Files, func(a, b NativeOracleFile) int { return strings.Compare(a.Path, b.Path) })
	p.Environment, err = resolveNativeGoEnvironment(ctx)
	if err != nil {
		return p, err
	}
	var moduleInputs [][]byte
	for _, commit := range []string{subject, b.Control.RecipeSource.CommitOID} {
		_, mod, err := nativeGitFile(ctx, r, entry.Path, commit, "go.mod")
		if err != nil {
			return p, nativeUnavailable("supported recipe requires a pinned root go.mod")
		}
		if !nativePinnedGoToolchain(mod) {
			return p, nativeUnavailable("module must pin toolchain go1.26.7")
		}
		var sum []byte
		listed, err := r.Run(ctx, entry.Path, "ls-tree", "-z", commit, "--", "go.sum")
		if err != nil {
			return p, err
		}
		if len(listed) > 0 {
			_, sum, err = nativeGitFile(ctx, r, entry.Path, commit, "go.sum")
			if err != nil {
				return p, err
			}
		}
		moduleInputs = append(moduleInputs, append(append(bytes.Clone(mod), 0), sum...))
	}
	if !bytes.Equal(moduleInputs[0], moduleInputs[1]) {
		return p, nativeUnavailable("candidate module dependency inputs differ from pinned preparation")
	}
	p.Environment.ModuleInputsDigest = nativeDigest(moduleInputs[0])
	p.Environment.Digest = nativeDigest([]byte(p.Environment.Digest + "\x00" + p.Environment.ModuleInputsDigest))
	p.Stages = []NativeOracleStage{{Name: "dependencies", Argv: []string{"go", "mod", "verify"}}, {Name: "metadata", Argv: []string{"go", "list", "-json", "-deps", "-test", "."}}, {Name: "compile", Argv: []string{"go", "test", "-c", "-o", "$scratch/oracle.test", "."}}, {Name: "converter_compile", Argv: []string{"go", "build", "-o", "$scratch/test2json", "cmd/test2json"}}}
	if req.Oracle.Phase == "execute" {
		p.Stages = append(p.Stages, NativeOracleStage{Name: "test", Argv: []string{"$scratch/oracle.test", "-test.count=1", "-test.run=" + b.Control.Argv[4], "-test.v=test2json", "-test.paniconexit0"}}, NativeOracleStage{Name: "convert", Argv: []string{"$scratch/test2json", "-p", "$package"}})
	}
	p.Stages = append(p.Stages, NativeOracleStage{Name: "dependencies_final", Argv: []string{"go", "mod", "verify"}})
	for _, stage := range p.Stages {
		if err := validateWorktreeVerifyCommand(stage.Argv); err != nil {
			return p, err
		}
	}
	return p, nil
}

func nativePinnedGoToolchain(mod []byte) bool {
	count := 0
	for _, line := range strings.Split(string(mod), "\n") {
		line, _, _ = strings.Cut(line, "//")
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "toolchain" {
			continue
		}
		if len(fields) != 2 || fields[1] != "go1.26.7" {
			return false
		}
		count++
	}
	return count == 1
}

// Snapshot only regular Git blobs. No links are followed or materialized.
func materializeNativeSnapshot(ctx context.Context, r GitRunner, repo, commit, root string) error {
	raw, err := r.Run(ctx, repo, "ls-tree", "-r", "-z", commit)
	if err != nil {
		return err
	}
	for _, entry := range strings.Split(string(raw), "\x00") {
		if entry == "" {
			continue
		}
		header, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
			return nativeUnavailable("snapshot contains unsupported symlink or gitlink")
		}
		if !fs.ValidPath(name) || strings.HasPrefix(name, ".git/") {
			return nativeUnavailable("snapshot path is not contained")
		}
		data, err := r.Run(ctx, repo, "cat-file", "blob", fields[2])
		if err != nil {
			return err
		}
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(file, data, 0600); err != nil {
			return err
		}
	}
	return nil
}

func nativeReplaceHarness(ctx context.Context, r GitRunner, repo, root string, p nativeOraclePlan) error {
	rooted, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer rooted.Close()
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
	fixtures := map[string]NativeOracleFile{}
	for _, f := range p.Files {
		if slices.Contains(p.Manifest.FixtureFiles, f.Path) {
			fixtures[f.Path] = f
		}
	}
	cwd := filepath.Join(root, filepath.FromSlash(p.Bundle.Control.Cwd))
	err := filepath.WalkDir(cwd, func(name string, d fs.DirEntry, err error) error {
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
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(name)
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
	if _, err := os.Stat(filepath.Join(root, "go.work")); err == nil {
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
	defer rooted.Close()
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
		if pkg.Error != nil || len(pkg.CgoFiles) > 0 {
			return "", nativeUnavailable("Go package has unavailable inputs or cgo")
		}
		if nativeContained(root, pkg.Dir) && (len(pkg.SFiles)+len(pkg.CFiles)+len(pkg.CXXFiles)+len(pkg.MFiles)+len(pkg.HFiles)+len(pkg.FFiles)+len(pkg.SysoFiles) > 0) {
			return "", nativeUnavailable("repository compiler inputs are not pure Go")
		}
		if pkg.Module != nil && pkg.Module.Replace != nil && pkg.Module.Replace.Dir != "" && !nativeContained(root, pkg.Module.Replace.Dir) {
			return "", nativeUnavailable("local module replacement escapes the controlled snapshot")
		}
		if pkg.Dir == cwd && !strings.Contains(pkg.ImportPath, " [") && !strings.HasSuffix(pkg.ImportPath, ".test") {
			copy := pkg
			target = &copy
		}
		if !nativeContained(root, pkg.Dir) {
			continue
		}
		if pkg.Dir != cwd {
			if err := validateNativeLocalPackageClosure(root, pkg.Dir, p); err != nil {
				return "", err
			}
		}
		for _, names := range [][]string{pkg.EmbedFiles, pkg.TestEmbedFiles, pkg.XTestEmbedFiles} {
			for _, name := range names {
				absolute := filepath.Join(pkg.Dir, name)
				rel, err := filepath.Rel(root, absolute)
				if err != nil {
					return "", err
				}
				f, ok := fixtures[filepath.ToSlash(rel)]
				if !ok {
					return "", nativeUnavailable("resolved repository embed is not a declared pinned fixture: " + filepath.ToSlash(rel))
				}
				b, err := os.ReadFile(absolute)
				if err != nil || nativeDigest(b) != f.SHA256 {
					return "", nativeUnavailable("resolved embed differs from its pinned fixture")
				}
			}
		}
	}
	if target == nil {
		return "", nativeUnavailable("declared package has no build-selected metadata")
	}
	selected := append(slices.Clone(target.TestGoFiles), target.XTestGoFiles...)
	sort.Strings(selected)
	declared := make([]string, 0, len(p.Manifest.TestFiles))
	for _, name := range p.Manifest.TestFiles {
		rel, err := filepath.Rel(cwd, filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return "", err
		}
		declared = append(declared, rel)
	}
	sort.Strings(declared)
	if !slices.Equal(selected, declared) {
		return "", nativeUnavailable("manifest test files are absent or excluded by the pinned build environment")
	}
	found := map[string]bool{}
	for _, name := range selected {
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(cwd, name), nil, 0)
		if err != nil {
			return "", err
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
				return "", nativeUnavailable("custom TestMain is unsupported")
			}
			if !nativeOracleTestName.MatchString(fn.Name.Name) {
				continue
			}
			if found[fn.Name.Name] {
				return "", nativeUnavailable("duplicate top-level test name")
			}
			if fn.Type.TypeParams != nil || (fn.Type.Results != nil && len(fn.Type.Results.List) != 0) || fn.Type.Params == nil || len(fn.Type.Params.List) != 1 || len(fn.Type.Params.List[0].Names) > 1 {
				return "", nativeUnavailable("test signature is not func TestXxx(*testing.T)")
			}
			star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
			if !ok {
				return "", nativeUnavailable("unsupported test signature")
			}
			sel, ok := star.X.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "T" {
				return "", nativeUnavailable("unsupported test signature")
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || !aliases[ident.Name] {
				return "", nativeUnavailable("test parameter must be testing.T")
			}
			found[fn.Name.Name] = true
		}
	}
	names, err := nativeOracleSelector(p.Manifest, p.Bundle.Control)
	if err != nil {
		return "", err
	}
	for _, name := range names {
		if !found[name] {
			return "", nativeUnavailable("selected top-level test is not build-selected: " + name)
		}
	}
	return target.ImportPath, nil
}

func nativeContained(root, name string) bool {
	rel, err := filepath.Rel(root, name)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func validateNativeLocalPackageClosure(root, dir string, p nativeOraclePlan) error {
	return filepath.WalkDir(dir, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nativeUnavailable("repository dependency contains a nonregular input")
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, ".go") || rel == "go.mod" || rel == "go.sum" {
			return nil
		}
		for _, f := range p.Files {
			if f.Path == rel && slices.Contains(p.Manifest.FixtureFiles, rel) {
				b, err := os.ReadFile(name)
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
			return nil, nativeUnavailable("test2json conversion is malformed")
		}
		if terminal || e.Package != pkg || e.Action == "fail" {
			return nil, nativeUnavailable("test2json contains conflicting or post-terminal events")
		}
		root, _, nested := strings.Cut(e.Test, "/")
		phase, known := state[root]
		switch e.Action {
		case "start":
			if started || e.Test != "" {
				return nil, nativeUnavailable("test2json contains a conflicting package start")
			}
			started = true
		case "output":
		case "pause", "cont":
			if !known || phase != 1 {
				return nil, nativeUnavailable("test2json continues a witness that is not running")
			}
		case "run":
			if !known || (!nested && phase != 0) || (nested && phase != 1) {
				return nil, nativeUnavailable("test2json contains duplicate or undeclared witness runs")
			}
			if !nested {
				state[root] = 1
			}
		case "pass":
			if e.Test == "" {
				for _, phase := range state {
					if phase != 2 {
						return nil, nativeUnavailable("package passed before every selected witness passed")
					}
				}
				terminal = true
			} else {
				if !known || phase != 1 {
					return nil, nativeUnavailable("test2json contains a duplicate or unordered witness pass")
				}
				if !nested {
					state[root] = 2
				}
			}
		case "skip":
			if !nested || !known || phase != 1 {
				return nil, nativeUnavailable("selected witness was skipped or undeclared")
			}
		default:
			return nil, nativeUnavailable("test2json contains an unsupported action")
		}
	}
	if !terminal || len(state) == 0 {
		return nil, nativeUnavailable("test2json has no complete package pass")
	}
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
		return nil, nativeUnavailable("raw test stdout lacks exactly one framed package pass")
	}
	for _, name := range selected {
		if runs[name] != 1 || passes[name] != 1 {
			return nil, nativeUnavailable("raw test stdout lacks unique framed witness events: " + name)
		}
	}
	return slices.Clone(selected), nil
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
	if err := os.Mkdir(cache, 0700); err != nil {
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
	currentEnv, err := resolveNativeGoEnvironment(ctx)
	if err != nil {
		return err
	}
	currentEnv.ModuleInputsDigest = p.Environment.ModuleInputsDigest
	currentEnv.Digest = nativeDigest([]byte(currentEnv.Digest + "\x00" + currentEnv.ModuleInputsDigest))
	if currentEnv != p.Environment {
		return nativeUnavailable("installed build environment changed after plan acquisition")
	}
	env := nativeControlledEnv(p.Environment, cache)
	cwd := filepath.Join(snapshot, filepath.FromSlash(p.Bundle.Control.Cwd))
	if !nativeContained(snapshot, cwd) {
		return nativeUnavailable("effective cwd escapes candidate snapshot")
	}
	rooted, err := os.OpenRoot(snapshot)
	if err != nil {
		return err
	}
	defer rooted.Close()
	packageRoot, err := rooted.OpenRoot(p.Bundle.Control.Cwd)
	if err != nil {
		return nativeUnavailable("effective cwd is not rooted in the candidate snapshot")
	}
	defer packageRoot.Close()
	packagePath := ""
	testStdout := []byte{}
	programExit := -1
	converterDigest := ""
	for _, stage := range p.Stages {
		cacheInfo, err := os.Lstat(cache)
		if err != nil || !cacheInfo.IsDir() || cacheInfo.Mode()&os.ModeSymlink != 0 {
			return nativeUnavailable("disposable cache no longer realizes the leased plan slot")
		}
		if stage.Name == "test" {
			before, err := snapshotOracleGitSubject(ctx, r, entry.Path)
			if err != nil {
				return err
			}
			if _, _, _, err := s.nativeOracleAdmission(ctx, req, entry, before, p.WorktreeIdentity, &p, false); err != nil {
				return err
			}
			actual, err := validateNativeOraclePackageInputs(snapshot, p)
			if err != nil || actual != digest {
				return nativeUnavailable("candidate inputs changed before test launch")
			}
			binary, err := os.ReadFile(filepath.Join(root, "oracle.test"))
			if err != nil || nativeDigest(binary) != result.BinaryDigest {
				return nativeUnavailable("test binary changed before launch")
			}
		}
		if stage.Name == "convert" {
			converter, err := os.ReadFile(filepath.Join(root, "test2json"))
			if err != nil || converterDigest == "" || nativeDigest(converter) != converterDigest {
				return nativeUnavailable("trusted converter binary changed before conversion")
			}
		}
		argv := slices.Clone(stage.Argv)
		for i, part := range argv {
			if part == "go" {
				argv[i] = p.Environment.GoExecutable
			} else if strings.HasPrefix(part, "$scratch/") {
				argv[i] = filepath.Join(root, strings.TrimPrefix(part, "$scratch/"))
			} else if part == "$package" {
				argv[i] = packagePath
			}
		}
		var stdin io.Reader
		if stage.Name == "convert" {
			stdin = bytes.NewReader(testStdout)
		}
		so, se := len(capture.stdout), len(capture.stderr)
		var launched func()
		if stage.Name == "test" {
			launched = s.nativeOracleProgramLaunched
		}
		if s.nativeOracleStageObserved != nil {
			s.nativeOracleStageObserved(stage.Name, cwd, slices.Clone(argv), slices.Clone(env))
		}
		exit, runErr := runNativeOracleStage(ctx, cwd, argv, env, stdin, capture, launched)
		stage.ExitCode = exit
		stage.StdoutOffset = so
		stage.StderrOffset = se
		stage.StdoutLength = len(capture.stdout) - so
		stage.StderrLength = len(capture.stderr) - se
		result.Stages = append(result.Stages, stage)
		if runErr != nil || !capture.complete {
			return nativeUnavailable(fmt.Sprintf("native %s failed: %v", stage.Name, runErr))
		}
		if stage.Name == "compile" && exit != 0 && req.Oracle.Phase == "execute" {
			result.Qualification = "fail"
			return nil
		}
		if exit != 0 && stage.Name != "test" {
			return nativeUnavailable("native " + stage.Name + " returned a nonzero exit")
		}
		switch stage.Name {
		case "metadata":
			packagePath, err = validateNativeGoMetadata(capture.stdout[so:], snapshot, p)
			if err != nil {
				return err
			}
		case "compile":
			name := filepath.Join(root, "oracle.test")
			stat, err := os.Stat(name)
			if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0111 == 0 {
				return nativeUnavailable("compiler produced no regular executable")
			}
			binary, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			result.BinaryDigest = nativeDigest(binary)
		case "converter_compile":
			name := filepath.Join(root, "test2json")
			stat, err := os.Lstat(name)
			if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0111 == 0 {
				return nativeUnavailable("converter compiler produced no regular executable")
			}
			binary, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			converterDigest = nativeDigest(binary)
		case "test":
			programExit = exit
			testStdout = bytes.Clone(capture.stdout[so:])
		case "convert":
			if programExit != 0 {
				result.Qualification = "fail"
			} else {
				result.ObservedTestNames, err = nativeOracleWitnesses(capture.stdout[so:], packagePath, result.SelectedTestNames, testStdout)
				if err != nil {
					return err
				}
				result.Qualification = "pass"
			}
		}
	}
	actual, err := validateNativeOraclePackageInputs(snapshot, p)
	if err != nil || actual != digest {
		return nativeUnavailable("consumed candidate inputs changed during execution")
	}
	finalEnv, err := resolveNativeGoEnvironment(ctx)
	if err != nil {
		return err
	}
	finalEnv.ModuleInputsDigest = p.Environment.ModuleInputsDigest
	finalEnv.Digest = nativeDigest([]byte(finalEnv.Digest + "\x00" + finalEnv.ModuleInputsDigest))
	if finalEnv != p.Environment {
		return nativeUnavailable("consumed toolchain identity changed during execution")
	}
	binary, err := os.ReadFile(filepath.Join(root, "oracle.test"))
	if err != nil || nativeDigest(binary) != result.BinaryDigest {
		return nativeUnavailable("consumed binary changed during execution")
	}
	converter, err := os.ReadFile(filepath.Join(root, "test2json"))
	if err != nil || nativeDigest(converter) != converterDigest {
		return nativeUnavailable("consumed converter changed during execution")
	}
	if req.Oracle.Phase == "prepare" {
		result.Qualification = "ready"
	}
	return nil
}
