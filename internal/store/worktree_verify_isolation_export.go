package store

import (
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type nativeIsolationMount struct {
	Source string
	Target string
	Write  bool
}

// The stage owner supplies only producer-owned paths. Mounts beneath the
// snapshot are forbidden: no setup operation can traverse candidate symlinks.
// Mutable generated data is reached through the already verified producer
// .svelte-kit link, never a writable source bind.
func nativeTargetIsolationExport(t *nativeTargetOraclePlan, root, snapshot, cwd string, argv []string) (*nativeStageIsolation, error) {
	if t.Recipe.Isolation == "" {
		if t.Environment.Isolation != nil {
			return nil, nativeIsolationUnavailable("legacy recipe carries a foreign isolation environment")
		}
		return nil, nil
	}
	if err := validateNativeIsolationPolicy(t.Recipe.Isolation); err != nil {
		return nil, err
	}
	if err := nativeIsolationEnvironmentUnchanged(t.Environment); err != nil {
		return nil, err
	}
	return nativeIsolationBuildExport(t, root, snapshot, cwd, argv)
}

func nativeIsolationBuildExport(t *nativeTargetOraclePlan, root, snapshot, cwd string, argv []string) (*nativeStageIsolation, error) {
	if !filepath.IsAbs(root) || !nativeContained(root, snapshot) || !nativeContained(snapshot, cwd) {
		return nil, nativeIsolationUnavailable("export paths do not realize producer scratch")
	}
	identity := filepath.Join(root, "identity")
	if err := os.MkdirAll(identity, 0700); err != nil {
		return nil, err
	}
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	home := filepath.Join(root, "home")
	const localName = "local" + "host"
	for name, body := range map[string]string{
		"passwd":        "sandbox:x:" + uid + ":" + gid + ":sandbox:" + home + ":/nonexistent\n",
		"group":         "sandbox:x:" + gid + ":\n",
		"hosts":         net.IPv4(127, 0, 0, 1).String() + " " + localName + " sandbox\n" + net.IPv6loopback.String() + " " + localName + " sandbox\n",
		"nsswitch.conf": "passwd: files\ngroup: files\nhosts: files\n",
	} {
		if err := os.WriteFile(filepath.Join(identity, name), []byte(body), 0600); err != nil {
			return nil, err
		}
	}
	var mounts []nativeIsolationMount
	for _, name := range []string{"passwd", "group", "hosts", "nsswitch.conf"} {
		mounts = append(mounts, nativeIsolationMount{Source: filepath.Join(identity, name), Target: "/etc/" + name})
	}
	// Every supported stage shares these finite output slots. There is no
	// writable mount of root, snapshot, node_modules or a shared cache.
	for _, name := range []string{"home", "tmp", "reports", "generated", "bun-cache"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		mounts = append(mounts, nativeIsolationMount{Source: dir, Target: dir, Write: true})
	}
	for _, name := range []string{"vitest-plan.json", "vitest-runner.mjs"} {
		p := filepath.Join(root, name)
		if t.Recipe.Runner == nativeTargetRunnerVitest {
			mounts = append(mounts, nativeIsolationMount{Source: p, Target: p})
		}
	}
	for _, dir := range []string{nativeBunBinDir(root), nativeTargetNodeBinDir(root)} {
		if _, err := os.Stat(dir); err == nil {
			mounts = append(mounts, nativeIsolationMount{Source: dir, Target: dir})
		}
	}
	files, err := nativeIsolationExportPaths(t, cwd)
	if err != nil {
		return nil, nativeIsolationUnavailable(err.Error())
	}
	observed, err := nativeIsolationExportIdentities(files)
	if err != nil {
		return nil, err
	}
	if workflowJSON(observed) != workflowJSON(t.Environment.Isolation.Exports) {
		return nil, nativeIsolationUnavailable("runtime exports differ from the prepared environment")
	}
	for _, name := range files {
		resolved, err := filepath.EvalSymlinks(name)
		if err != nil {
			return nil, err
		}
		// Materialize each alias as an exact read-only file. No parent host
		// directory or ambient loader cache comes with it.
		mounts = append(mounts, nativeIsolationMount{Source: resolved, Target: name})
	}
	mounts = append(mounts, nativeIsolationMount{Source: snapshot, Target: snapshot})
	return nativeIsolationAssemble(root, snapshot, cwd, argv, mounts, t.Environment.Isolation)
}

func nativeIsolationLocaleFiles() ([]string, error) {
	locale := os.Getenv("LC_ALL")
	if locale == "" {
		locale = os.Getenv("LANG")
	}
	switch locale {
	case "", "C", "POSIX":
		return nil, nil
	case "C.UTF-8", "C.utf8":
		var files []string
		err := filepath.WalkDir("/usr/lib/locale/C.utf8", func(name string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("locale input is nonregular")
			}
			files = append(files, name)
			return nil
		})
		if err != nil {
			return nil, nativeIsolationUnavailable("consumed locale data is unavailable: " + err.Error())
		}
		return files, nil
	default:
		if strings.ContainsAny(locale, "/\\\x00") {
			return nil, nativeIsolationUnavailable("consumed locale is not a bounded locale name")
		}
		const archive = "/usr/lib/locale/locale-archive"
		if info, err := os.Stat(archive); err != nil || !info.Mode().IsRegular() {
			return nil, nativeIsolationUnavailable("consumed locale archive is unavailable")
		}
		return []string{archive}, nil
	}
}

func nativeIsolationAssemble(root, snapshot, cwd string, workload []string, mounts []nativeIsolationMount, environment *nativeIsolationEnvironment) (*nativeStageIsolation, error) {
	args := []string{"--unshare-user", "--unshare-pid", "--unshare-net", "--unshare-ipc", "--unshare-uts", "--disable-userns", "--assert-userns-disabled", "--new-session", "--cap-drop", "ALL", "--hostname", "sandbox"}
	// The empty-root skeleton is built before any untrusted directory is
	// mounted. No following operation may create a destination beneath one.
	// Default /tmp is a fresh per-stage tmpfs mounted first: producer scratch
	// usually lies beneath it, and an earlier stage's writable slot must
	// never hold the parents of later mount destinations.
	dirs := map[string]bool{"/proc": true, "/dev": true}
	seen := map[string]nativeIsolationMount{}
	var unique []nativeIsolationMount
	for _, m := range mounts {
		if !filepath.IsAbs(m.Source) || !filepath.IsAbs(m.Target) || filepath.Clean(m.Target) != m.Target || m.Target == "/" || slices.Contains([]string{"/usr", "/lib", "/lib64", "/opt", "/home", "/etc", "/run", "/var", "/tmp"}, m.Target) {
			return nil, nativeIsolationUnavailable("export contains a broad or nonabsolute mount")
		}
		if nativeContained(snapshot, m.Target) && m.Target != snapshot {
			return nil, nativeIsolationUnavailable("mount destination crosses the read-only snapshot")
		}
		info, err := os.Lstat(m.Source)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return nil, nativeIsolationUnavailable("export source is missing, symlinked or nonregular")
		}
		if m.Write && (!nativeContained(root, m.Source) || nativeContained(snapshot, m.Source)) {
			return nil, nativeIsolationUnavailable("writable export is outside private scratch")
		}
		if m.Write && (m.Source != m.Target || !slices.Contains([]string{filepath.Join(root, "home"), filepath.Join(root, "tmp"), filepath.Join(root, "reports"), filepath.Join(root, "generated"), filepath.Join(root, "bun-cache")}, m.Source)) {
			return nil, nativeIsolationUnavailable("writable export is not one finite producer state slot")
		}
		if !m.Write && info.IsDir() && !slices.Contains([]string{snapshot, nativeBunBinDir(root), nativeTargetNodeBinDir(root)}, m.Source) {
			return nil, nativeIsolationUnavailable("read-only directory export is not a verified producer input tree")
		}
		if previous, ok := seen[m.Target]; ok {
			if previous != m {
				return nil, nativeIsolationUnavailable("conflicting export destinations")
			}
			continue
		}
		seen[m.Target] = m
		for dir := filepath.Dir(m.Target); dir != "/"; dir = filepath.Dir(dir) {
			dirs[dir] = true
		}
		unique = append(unique, m)
	}
	for _, m := range unique {
		for _, other := range unique {
			if m.Target != other.Target && nativeContained(m.Target, other.Target) {
				return nil, nativeIsolationUnavailable("nested export destinations are unsupported")
			}
		}
	}
	delete(dirs, "/tmp")
	var ordered []string
	for dir := range dirs {
		ordered = append(ordered, dir)
	}
	slices.Sort(ordered)
	args = append(args, "--tmpfs", "/tmp")
	for _, dir := range ordered {
		args = append(args, "--dir", dir)
	}
	args = append(args, "--proc", "/proc", "--dev", "/dev")
	var inventory []NativeOracleFile
	for _, m := range unique {
		flag := "--ro-bind"
		if m.Write {
			flag = "--bind"
		} else {
			info, _ := os.Stat(m.Source)
			if info.Mode().IsRegular() {
				f, err := nativeIsolationFile(m.Source)
				if err != nil {
					return nil, err
				}
				inventory = append(inventory, f)
			}
		}
		args = append(args, flag, m.Source, m.Target)
	}
	args = append(args, "--remount-ro", "/", "--chdir", cwd, "--")
	args = append(args, workload...)
	// Normalize only the producer root, not arbitrary path-like recipe text.
	shape := strings.ReplaceAll(workflowJSON(mounts), root, "$scratch")
	fingerprint := nativeDigest([]byte(nativeIsolationEnvironmentDigest(environment) + "\x00" + shape + "\x00" + strings.ReplaceAll(workflowJSON(inventory), root, "$scratch")))
	p := &nativeStageIsolation{Launcher: nativeIsolationLauncher, Argv: args, Fingerprint: fingerprint, Profile: nativeIsolationProfile}
	if err := validateNativeIsolation(p, workload); err != nil {
		return nil, nativeIsolationUnavailable(err.Error())
	}
	return p, nil
}
