package store

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

type WorktreeGoSourceClosure struct {
	SubjectRef    string                `json:"subject_ref"`
	ModulePath    string                `json:"module_path"`
	SelectedCount int                   `json:"selected_count"`
	Digest        string                `json:"digest"`
	Complete      bool                  `json:"complete"`
	Reason        GoSourceClosureReason `json:"reason,omitempty"`
	Residual      string                `json:"residual"`
}

type GoSourceClosureReason string

const (
	goSourceNativeUnsupported GoSourceClosureReason = "native_inputs_unsupported"
	goSourceRuntimeResidual   string                = "runtime_reads_outside_HEAD_package_trees_unproved"
)

func observeWorktreeGoSources(ctx context.Context, dir string, environment []string) (*WorktreeGoSourceClosure, error) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	runner := ExecGitRunner{}
	head, err := runner.Run(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || !worktreeSHAPattern.MatchString(strings.TrimSpace(string(head))) {
		return nil, nativeUnavailable("Go inputs have no pinned commit")
	}
	commit := strings.TrimSpace(string(head))
	fields := "Dir,Module,Error,CgoFiles,CFiles,CXXFiles,MFiles,HFiles,FFiles,SFiles,SysoFiles,SwigFiles,SwigCXXFiles"
	capture := &nativeStreamCapture{complete: true}
	code, err := runNativeOracleStage(ctx, root, []string{"go", "list", "-race", "-p=4", "-mod=readonly", "-deps", "-test", "-json=" + fields, "./..."}, environment, nil, capture, nil)
	if err != nil || code != 0 || !capture.complete {
		return nil, nativeUnavailable("Go selected-input metadata is unavailable")
	}
	decoder := json.NewDecoder(bytes.NewReader(capture.stdout))
	dirs, modules := map[string]bool{}, map[string]bool{root: true}
	modulePath := ""
	for {
		var pkg struct {
			nativeGoPackage
			SwigFiles, SwigCXXFiles []string
		}
		if err := decoder.Decode(&pkg); err == io.EOF {
			break
		} else if err != nil || pkg.Error != nil {
			return nil, nativeUnavailable("Go selected-input metadata is malformed")
		}
		if pkg.Module != nil && pkg.Module.Replace != nil && pkg.Module.Replace.Version == "" && !nativeContained(root, pkg.Module.Replace.Dir) {
			return nil, nativeUnavailable("a local Go dependency escapes the commit")
		}
		if !nativeContained(root, pkg.Dir) {
			continue
		}
		if len(pkg.CgoFiles)+len(pkg.CFiles)+len(pkg.CXXFiles)+len(pkg.MFiles)+len(pkg.HFiles)+len(pkg.FFiles)+len(pkg.SFiles)+len(pkg.SwigFiles)+len(pkg.SwigCXXFiles)+len(pkg.SysoFiles) > 0 {
			return &WorktreeGoSourceClosure{Reason: goSourceNativeUnsupported, Residual: goSourceRuntimeResidual}, nativeUnavailable("native compiler input closure is unsupported")
		}
		dirs[pkg.Dir] = true
		if pkg.Module != nil && nativeContained(root, pkg.Module.Dir) {
			modules[pkg.Module.Dir] = true
			if pkg.Module.Main && pkg.Module.Dir == root {
				modulePath = pkg.Module.Path
			}
		}
	}
	if len(dirs) == 0 || modulePath == "" {
		return nil, nativeUnavailable("Go metadata selected no root module packages")
	}
	rooted, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rooted.Close() }()
	selectors := []string{}
	for directory := range dirs {
		rel, err := filepath.Rel(root, directory)
		if err != nil {
			return nil, err
		}
		selectors = append(selectors, filepath.ToSlash(rel))
	}
	for module := range modules {
		rel, err := filepath.Rel(root, module)
		if err != nil {
			return nil, err
		}
		selectors = append(selectors, filepath.ToSlash(filepath.Join(rel, "go.mod")), filepath.ToSlash(filepath.Join(rel, "go.sum")))
	}
	status, err := runner.Run(ctx, root, append([]string{"status", "--porcelain=v1", "--ignored=matching", "--untracked-files=all", "-z", "--"}, selectors...)...)
	if err != nil || len(status) != 0 {
		return nil, nativeUnavailable("a package tree contains changed, untracked or ignored inputs")
	}
	index, err := runner.Run(ctx, root, append([]string{"ls-files", "-v", "-z", "--"}, selectors...)...)
	if err != nil {
		return nil, err
	}
	for _, row := range strings.Split(string(index), "\x00") {
		if len(row) > 0 && (row[0] == 'S' || row[0] >= 'a' && row[0] <= 'z') {
			return nil, nativeUnavailable("a package tree has index-hidden inputs")
		}
	}
	tree, err := runner.Run(ctx, root, append([]string{"ls-tree", "-r", "-t", "-z", commit, "--"}, selectors...)...)
	if err != nil {
		return nil, err
	}
	objects, names := map[string]bool{".": true}, []string{}
	for _, row := range strings.Split(strings.TrimSuffix(string(tree), "\x00"), "\x00") {
		header, name, ok := strings.Cut(row, "\t")
		parts := strings.Fields(header)
		if !ok || len(parts) != 3 || len(objects) >= 65536 {
			return nil, nativeUnavailable("package trees have no bounded HEAD inventory")
		}
		objects[name] = true
		if parts[1] == "blob" {
			names = append(names, name)
		} else if parts[1] != "tree" {
			return nil, nativeUnavailable("package tree Git objects are unsupported")
		}
	}
	for directory := range dirs {
		rel, _ := filepath.Rel(root, directory)
		if err := fs.WalkDir(rooted.FS(), rel, func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if name == ".git" && entry.IsDir() {
				return fs.SkipDir
			}
			if name != ".git" && !objects[name] {
				return nativeUnavailable("package tree contains an unpinned filesystem entry")
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	slices.Sort(names)
	identities := make([][2]string, 0, len(names))
	for _, name := range names {
		file, _, err := nativeGitFile(ctx, runner, root, commit, name)
		if err != nil {
			return nil, err
		}
		content, err := rooted.ReadFile(name)
		if err != nil || nativeDigest(content) != file.SHA256 {
			return nil, nativeUnavailable("Go selected input differs from its pinned blob: " + name)
		}
		identities = append(identities, [2]string{name, file.BlobOID})
	}
	raw, err := json.Marshal(identities)
	if err != nil {
		return nil, err
	}
	return &WorktreeGoSourceClosure{SubjectRef: "commit:" + commit, ModulePath: modulePath, SelectedCount: len(names), Digest: nativeDigest(raw), Complete: true, Residual: goSourceRuntimeResidual}, nil
}

// Only the observing invocation fixes selection; legacy commands keep their
// execution semantics and cannot acquire these producer facts.
func worktreeGoSuiteEnvironment() (environment, fixed []string) {
	fixed = []string{
		"GOFLAGS=-mod=readonly", "GOENV=off", "GOWORK=off", "GOTOOLCHAIN=local",
		"GOMAXPROCS=4", "CGO_ENABLED=1", "GOOS=" + runtime.GOOS,
		"GOARCH=" + runtime.GOARCH, "GOEXPERIMENT=", "GO111MODULE=on",
	}
	for _, inherited := range os.Environ() {
		key, _, _ := strings.Cut(inherited, "=")
		if !slices.ContainsFunc(fixed, func(value string) bool { return strings.HasPrefix(value, key+"=") }) {
			environment = append(environment, inherited)
		}
	}
	return append(environment, fixed...), fixed
}
