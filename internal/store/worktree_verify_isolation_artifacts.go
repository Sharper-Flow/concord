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
)

// Derive host exports from the same verified archives before the immutable
// environment is sealed. Binding libraries only after the lease starts would
// allow changed addon libraries to reuse stale dispatch readiness. This uses
// the existing in-process materializer; it runs no package manager or workload.
func qualifyNativeIsolationArtifacts(ctx context.Context, t *nativeTargetOraclePlan, raw map[string][]byte) (returnErr error) {
	if t.Recipe.Isolation == "" {
		return nil
	}
	if t.Environment.Isolation == nil {
		return nativeIsolationUnavailable("required isolation identity is missing")
	}
	if t.Recipe.Materializer != nativeTargetMaterializerBun {
		return nativeIsolationBindTargetExports(t, "")
	}
	root, err := os.MkdirTemp("", "concord-native-export-inventory-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(root); err != nil && returnErr == nil {
			returnErr = nativeIsolationUnavailable(err.Error())
		}
	}()
	cwd := filepath.Join(root, "snapshot", filepath.FromSlash(t.Recipe.Root))
	if err := os.MkdirAll(cwd, 0700); err != nil {
		return err
	}
	for _, name := range []string{t.Recipe.Pin.DepManifest, t.Recipe.Pin.DepLock} {
		if len(raw[name]) == 0 {
			return nativeIsolationUnavailable("pinned dependency bytes are missing from export qualification")
		}
		if err := os.WriteFile(filepath.Join(cwd, name), raw[name], 0600); err != nil {
			return err
		}
	}
	if err := materializeNativeBunRun(ctx, t, root, cwd); err != nil {
		return err
	}
	return nativeIsolationBindTargetExports(t, cwd)
}

func nativeIsolationExportPaths(t *nativeTargetOraclePlan, cwd string) ([]string, error) {
	roots := []string{t.Environment.Bun, t.Environment.Node, t.Environment.Bash}
	if t.Recipe.GitMetadata != "" {
		if t.Environment.Git == "" {
			return nil, nativeIsolationUnavailable("candidate Git metadata has no pinned runtime")
		}
		roots = append(roots, t.Environment.Git)
	}
	var private []string
	if t.Recipe.Materializer == nativeTargetMaterializerBun {
		dependencies := filepath.Join(cwd, "node_modules")
		private = append(private, dependencies)
		addons, err := nativeIsolationBunELFRoots(t, cwd)
		if err != nil {
			return nil, err
		}
		roots = append(roots, addons...)
	}
	files, err := nativeIsolationELFClosure(roots, private...)
	if err != nil {
		return nil, nativeIsolationUnavailable(err.Error())
	}
	files = slices.DeleteFunc(files, func(name string) bool {
		for _, root := range private {
			if nativeContained(root, name) {
				return true
			}
		}
		return false
	})
	locale, err := nativeIsolationLocaleFiles()
	if err != nil {
		return nil, err
	}
	files = append(files, locale...)
	if _, err := os.Stat("/etc/localtime"); err == nil {
		files = append(files, "/etc/localtime")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	slices.Sort(files)
	return slices.Compact(files), nil
}

func nativeIsolationBunELFRoots(t *nativeTargetOraclePlan, cwd string) ([]string, error) {
	root, err := os.OpenRoot(cwd)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	manifest, err := root.ReadFile(t.Recipe.Pin.DepManifest)
	if err != nil {
		return nil, err
	}
	lock, err := root.ReadFile(t.Recipe.Pin.DepLock)
	if err != nil {
		return nil, err
	}
	installs, err := nativeBunPlanInstalls(manifest, lock)
	if err != nil {
		return nil, err
	}
	var roots []string
	for _, pkg := range installs {
		files, err := nativeIsolationPackageELFs(root, pkg.Dir)
		if err != nil {
			return nil, err
		}
		for _, name := range files {
			roots = append(roots, filepath.Join(cwd, filepath.FromSlash(name)))
		}
	}
	return roots, nil
}

func nativeIsolationPackageELFs(root *os.Root, base string) ([]string, error) {
	raw, err := root.ReadFile(path.Join(base, "package.json"))
	if err != nil {
		return nil, err
	}
	selected, err := nativeIsolationPackageSupportsGlibc(raw)
	if err != nil {
		return nil, err
	}
	if !selected {
		return nil, nil
	}
	var roots []string
	if err := fs.WalkDir(root.FS(), base, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && name != base && d.Name() == "node_modules" {
			return fs.SkipDir
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		f, err := root.Open(name)
		if err != nil {
			return err
		}
		var magic [4]byte
		_, _ = f.Read(magic[:])
		_ = f.Close()
		if bytes.Equal(magic[:], []byte{0x7f, 'E', 'L', 'F'}) {
			roots = append(roots, name)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return roots, nil
}

// The verified original archive may contain optional binaries for another
// libc: Bun's lock platform metadata only selects OS/CPU. The supported host
// loader is glibc. This excludes only explicit incompatible archive metadata;
// it does not infer a platform from a package name or ELF filename.
func nativeIsolationPackageSupportsGlibc(raw []byte) (bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return false, nativeIsolationUnavailable("verified package manifest is not one object")
	}
	value, present := fields["libc"]
	if !present {
		return true, nil
	}
	values, err := nativeBunPlatformList(value)
	if err != nil {
		return false, nativeIsolationUnavailable("unsupported archive libc metadata")
	}
	for _, v := range values {
		if v != "glibc" && v != "musl" && v != "!glibc" && v != "!musl" {
			return false, nativeIsolationUnavailable("unsupported archive libc selector: " + v)
		}
	}
	return nativeBunPlatformMatch(true, values, "glibc"), nil
}

func nativeIsolationBindTargetExports(t *nativeTargetOraclePlan, cwd string) error {
	paths, err := nativeIsolationExportPaths(t, cwd)
	if err != nil {
		return err
	}
	files, err := nativeIsolationExportIdentities(paths)
	if err != nil {
		return err
	}
	t.Environment.Isolation.Exports = files
	return nil
}

func nativeIsolationExportIdentities(paths []string) ([]NativeOracleFile, error) {
	var files []NativeOracleFile
	for _, name := range paths {
		file, err := nativeIsolationFile(name)
		if err != nil {
			return nil, nativeIsolationUnavailable(err.Error())
		}
		files = append(files, file)
	}
	return files, nil
}
