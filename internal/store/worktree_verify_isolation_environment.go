package store

import (
	"bytes"
	"debug/elf"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

const nativeIsolationLoopback = "loopback_only_v1"
const nativeIsolationLauncher = "/usr/bin/bwrap"

// The pinned vendor profile declares `profile bwrap /usr/bin/bwrap` without
// complain or unconfined flags. Forks before exec retain that profile; the
// supervisor observes both the launcher and gated init before workload exec.
const nativeIsolationProfile = "bwrap (enforce)"

type nativeIsolationEnvironment struct {
	Policy  string             `json:"policy"`
	Profile string             `json:"profile"`
	Files   []NativeOracleFile `json:"files"`
	Exports []NativeOracleFile `json:"exports"`
}

func validateNativeIsolationPolicy(policy string) error {
	if policy != "" && policy != nativeIsolationLoopback {
		return nativeIsolationUnavailable("unsupported recipe isolation policy")
	}
	return nil
}

func nativeIsolationManifestPolicy(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nativeUnavailable("recipe is not one closed target_runner_v1 manifest")
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] {
			return nativeUnavailable("recipe has duplicate or malformed fields")
		}
		seen[name] = true
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nativeUnavailable("recipe has malformed fields")
		}
		if name == "isolation" {
			var policy string
			if json.Unmarshal(value, &policy) != nil || policy != nativeIsolationLoopback {
				return nativeIsolationUnavailable("present isolation must name the one supported policy")
			}
		}
	}
	return nil
}

func nativeIsolationEnvironmentDigest(e *nativeIsolationEnvironment) string {
	if e == nil {
		return ""
	}
	return nativeDigest([]byte(workflowJSON(e)))
}

// No executable, loader, ldd, or --version subprocess runs in this reader.
// VERSION is emitted as one NUL-terminated `bubblewrap <version>` string by
// upstream 0.12.0's --version branch. Only allocated, non-writable ELF data
// counts; an appended string or a script cannot supply the version proof.
func nativeIsolationLauncherVersion(path string) error {
	if err := validateNativeLauncherFile(path); err != nil {
		return err
	}
	f, err := elf.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if f.Machine != elf.EM_X86_64 || f.Class != elf.ELFCLASS64 || (f.Type != elf.ET_DYN && f.Type != elf.ET_EXEC) {
		return fmt.Errorf("launcher is not a Linux amd64 ELF executable")
	}
	versions := map[string]bool{}
	for _, section := range f.Sections {
		if section.Flags&elf.SHF_ALLOC == 0 || section.Flags&(elf.SHF_WRITE|elf.SHF_EXECINSTR) != 0 || section.Type != elf.SHT_PROGBITS {
			continue
		}
		if section.Size > 16*1024*1024 {
			return fmt.Errorf("launcher constant data exceeds bound")
		}
		data, err := section.Data()
		if err != nil {
			return err
		}
		for _, value := range bytes.Split(data, []byte{0}) {
			if bytes.HasPrefix(value, []byte("bubblewrap ")) {
				versions[string(value)] = true
			}
		}
	}
	if len(versions) != 1 || !versions["bubblewrap 0.12.0"] {
		return fmt.Errorf("launcher lacks static proof of exactly bubblewrap 0.12.0")
	}
	var caps [256]byte
	n, err := unix.Getxattr(path, "security.capability", caps[:])
	if err != nil && err != unix.ENODATA && err != unix.ENOTSUP {
		return err
	}
	if n > 0 {
		return fmt.Errorf("launcher has file capabilities")
	}
	return nil
}

func nativeIsolationFile(path string) (NativeOracleFile, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return NativeOracleFile{}, fmt.Errorf("isolation input is not a readable regular file: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return NativeOracleFile{}, err
	}
	defer f.Close()
	// Runtime binaries (notably Bun) can exceed the source input bound.
	data, err := io.ReadAll(io.LimitReader(f, 512*1024*1024+1))
	if err != nil || len(data) > 512*1024*1024 {
		return NativeOracleFile{}, fmt.Errorf("isolation input exceeds bound: %s", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return NativeOracleFile{}, err
	}
	identity := fmt.Sprintf("%s\x00%o\x00%s", resolved, info.Mode(), nativeDigest(data))
	return NativeOracleFile{Path: path, SHA256: nativeDigest([]byte(identity))}, nil
}

// The cache and preload file affect the outer launcher's host loader even
// though neither is exported to the workload. Their absence is also bound.
func nativeIsolationOptionalFile(path string) (NativeOracleFile, error) {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return NativeOracleFile{Path: path, SHA256: nativeDigest([]byte("absent"))}, nil
	}
	return nativeIsolationFile(path)
}

func resolveNativeIsolationEnvironment(e nativeTargetEnvironment) (*nativeIsolationEnvironment, error) {
	if err := validateNativeIsolationCredentials(os.Getuid(), os.Geteuid(), os.Getgid(), os.Getegid()); err != nil {
		return nil, err
	}
	if err := nativeIsolationLauncherVersion(nativeIsolationLauncher); err != nil {
		return nil, nativeIsolationUnavailable(err.Error())
	}
	// Python's stdlib, venv mutation and dlopen/data closure require their own
	// inventory. Do not expose an interpreter prefix or shared cache to guess it.
	if e.Python != "" || e.UV != "" {
		return nil, nativeIsolationUnavailable("Python/uv runtime data export is not qualified")
	}
	p := &nativeIsolationEnvironment{Policy: nativeIsolationLoopback, Profile: nativeIsolationProfile}
	p.Files = append(p.Files, NativeOracleFile{Path: "isolation-user", SHA256: nativeDigest([]byte(fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())))})
	if raw, err := os.ReadFile("/etc/ld.so.preload"); err == nil && len(bytes.TrimSpace(raw)) != 0 {
		return nil, nativeIsolationUnavailable("host loader preload closure is unsupported")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, nativeIsolationUnavailable(err.Error())
	}
	for _, key := range []string{"PATH", "LANG", "LC_ALL"} {
		value, present := os.LookupEnv(key)
		p.Files = append(p.Files, NativeOracleFile{Path: "isolation-inherited-" + key, SHA256: nativeDigest([]byte(fmt.Sprintf("%t\x00%s", present, value)))})
	}
	profileFiles, err := nativeIsolationProfileFiles()
	if err != nil {
		return nil, nativeIsolationUnavailable(err.Error())
	}
	p.Files = append(p.Files, profileFiles...)
	files, err := nativeIsolationELFClosure([]string{nativeIsolationLauncher, e.Bun, e.Node, e.Bash})
	if err != nil {
		return nil, nativeIsolationUnavailable(err.Error())
	}
	for _, name := range files {
		f, err := nativeIsolationFile(name)
		if err != nil {
			return nil, nativeIsolationUnavailable(err.Error())
		}
		p.Files = append(p.Files, f)
	}
	for _, name := range []string{"/etc/ld.so.cache", "/etc/ld.so.preload"} {
		f, err := nativeIsolationOptionalFile(name)
		if err != nil {
			return nil, nativeIsolationUnavailable(err.Error())
		}
		p.Files = append(p.Files, f)
	}
	supervisor, err := nativeIsolationSupervisorIdentity()
	if err != nil {
		return nil, nativeIsolationUnavailable(err.Error())
	}
	p.Files = append(p.Files, supervisor...)
	slices.SortFunc(p.Files, func(a, b NativeOracleFile) int { return strings.Compare(a.Path, b.Path) })
	return p, nil
}

func validateNativeIsolationCredentials(uid, euid, gid, egid int) error {
	if uid == 0 || uid != euid || gid != egid {
		return nativeIsolationUnavailable("requires an ordinary unprivileged user identity")
	}
	return nil
}

func nativeIsolationEnvironmentUnchanged(e nativeTargetEnvironment) error {
	if e.Isolation == nil || len(e.Isolation.Exports) == 0 {
		return nativeIsolationUnavailable("required environment has no isolation identity")
	}
	current, err := resolveNativeIsolationEnvironment(e)
	if err != nil {
		return err
	}
	var paths []string
	for _, file := range e.Isolation.Exports {
		paths = append(paths, file.Path)
	}
	current.Exports, err = nativeIsolationExportIdentities(paths)
	if err != nil {
		return err
	}
	if nativeIsolationEnvironmentDigest(current) != nativeIsolationEnvironmentDigest(e.Isolation) {
		return nativeIsolationUnavailable("launcher, runtime, profile or supervisor identity changed")
	}
	return nil
}

// Only explicit host loader roots are searched. LD_* is absent from the
// stage environment; the host loader cache itself is never exported. An
// unresolved SONAME refuses rather than binding a library directory.
func nativeIsolationELFClosure(roots []string, privateRoots ...string) ([]string, error) {
	search := []string{"/lib/x86_64-linux-gnu", "/usr/lib/x86_64-linux-gnu", "/lib64", "/usr/lib64", "/lib", "/usr/lib"}
	cache, err := nativeIsolationHostLoaderCache()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var visit func(string, []string) error
	visit = func(name string, inherited []string) error {
		if name == "" || seen[name] {
			return nil
		}
		if !filepath.IsAbs(name) || filepath.Clean(name) != name || len(seen) >= 256 {
			return fmt.Errorf("unbounded or nonabsolute ELF input: %s", name)
		}
		seen[name] = true
		f, err := elf.Open(name)
		if err != nil {
			return fmt.Errorf("ELF input %s: %w", name, err)
		}
		defer f.Close()
		if f.Machine != elf.EM_X86_64 || f.Class != elf.ELFCLASS64 {
			return fmt.Errorf("unsupported ELF architecture: %s", name)
		}
		for _, program := range f.Progs {
			if program.Type == elf.PT_INTERP {
				data, err := io.ReadAll(io.LimitReader(program.Open(), 4097))
				if err != nil || len(data) > 4096 || len(data) < 2 || data[len(data)-1] != 0 {
					return fmt.Errorf("invalid ELF interpreter: %s", name)
				}
				if err := nativeIsolationLibraryAllowed(string(data[:len(data)-1]), privateRoots); err != nil {
					return err
				}
				if err := visit(string(data[:len(data)-1]), nil); err != nil {
					return err
				}
			}
		}
		runpath, err := f.DynString(elf.DT_RUNPATH)
		if err != nil {
			return err
		}
		rpath, err := f.DynString(elf.DT_RPATH)
		if err != nil {
			return err
		}
		paths := rpath
		if len(runpath) != 0 {
			paths = runpath
		}
		var ownDirs []string
		for _, list := range paths {
			for _, dir := range strings.Split(list, ":") {
				dir = strings.ReplaceAll(strings.ReplaceAll(dir, "${ORIGIN}", filepath.Dir(name)), "$ORIGIN", filepath.Dir(name))
				if !filepath.IsAbs(dir) || strings.Contains(dir, "$") {
					return fmt.Errorf("unsupported ELF search path: %s", name)
				}
				ownDirs = append(ownDirs, filepath.Clean(dir))
			}
		}
		dirs := append(slices.Clone(ownDirs), inherited...)
		if len(runpath) != 0 {
			dirs = append(slices.Clone(inherited), ownDirs...)
		}
		needed, err := f.ImportedLibraries()
		if err != nil {
			return err
		}
		for _, lib := range needed {
			if strings.ContainsAny(lib, "/$") {
				return fmt.Errorf("unsupported ELF dependency: %s", lib)
			}
			found := ""
			fromDefault := false
			for i, dir := range append(slices.Clone(dirs), search...) {
				candidate := filepath.Join(dir, lib)
				if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
					found = candidate
					fromDefault = i >= len(dirs)
					break
				}
			}
			if found == "" {
				return fmt.Errorf("missing ELF dependency %s for %s", lib, name)
			}
			if fromDefault {
				if err := nativeIsolationCacheAgrees(cache[lib], found); err != nil {
					return err
				}
			}
			if err := nativeIsolationLibraryAllowed(found, privateRoots); err != nil {
				return err
			}
			next := inherited
			if len(runpath) == 0 {
				next = append(slices.Clone(ownDirs), inherited...)
			}
			if err := visit(found, next); err != nil {
				return fmt.Errorf("ELF dependency %s for %s: %w", lib, name, err)
			}
		}
		return nil
	}
	for _, root := range roots {
		if err := visit(root, nil); err != nil {
			return nil, err
		}
	}
	files := make([]string, 0, len(seen))
	for name := range seen {
		files = append(files, name)
	}
	slices.Sort(files)
	return files, nil
}

// An archive's RPATH must not select an arbitrary host-home or configuration
// file. Additional non-system libraries must stay in its verified private tree.
func nativeIsolationLibraryAllowed(name string, privateRoots []string) error {
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		return err
	}
	system := func(path string) bool {
		for _, root := range []string{"/lib", "/lib64", "/usr/lib", "/usr/lib64"} {
			if nativeContained(root, path) {
				return true
			}
		}
		return false
	}
	if system(name) && system(resolved) {
		return nil
	}
	for _, root := range privateRoots {
		canonical, err := filepath.EvalSymlinks(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if nativeContained(root, name) && nativeContained(canonical, resolved) {
			return nil
		}
	}
	return fmt.Errorf("ELF library escapes qualified runtime roots: %s", name)
}
