package store

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// Exact vendor input, not a policy installed or loaded by Concord:
// https://git.launchpad.net/ubuntu/+source/apparmor/plain/profiles/apparmor/profiles/extras/bwrap-userns-restrict?h=applied/4.0.1really4.0.1-0ubuntu0.24.04.9
const nativeIsolationVendorProfileDigest = "sha256:11d39094f044f0cda0febb3ad517b830301da6b2ce929664af09ee9e4dd264f9"

var nativeIsolationProfileInclude = regexp.MustCompile(`(?m)^\s*#?include\s+(if exists\s+)?[<"]([^>"\n]+)[>"]`)

func nativeIsolationProfileFiles() ([]NativeOracleFile, error) {
	const profile = "/etc/apparmor.d/bwrap-userns-restrict"
	raw, err := os.ReadFile(profile)
	if err != nil || nativeDigest(raw) != nativeIsolationVendorProfileDigest {
		return nil, fmt.Errorf("exact pinned vendor AppArmor profile is missing or changed")
	}
	for _, local := range []string{"bwrap", "bwrap-userns-restrict", "unpriv_bwrap"} {
		if _, err := os.Lstat("/etc/apparmor.d/local/" + local); !os.IsNotExist(err) {
			return nil, fmt.Errorf("local AppArmor override is present or unreadable")
		}
	}
	// Active policy is established by the supervisor's pre-release /proc
	// label observation, not by a policy filename or a privileged profile-list
	// read. This identity alone never supplies established enforcement.
	root, err := os.OpenRoot("/etc/apparmor.d")
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	p := nativeIsolationProfileClosure{root: root, seen: map[string]bool{}, files: []NativeOracleFile{{Path: "isolation-required-profile", SHA256: nativeDigest([]byte(nativeIsolationProfile))}}}
	for _, name := range []string{profile, "/etc/apparmor.d/abi/4.0"} {
		if err := p.visit(name, false); err != nil {
			return nil, err
		}
	}
	return p.files, nil
}

type nativeIsolationProfileClosure struct {
	root  *os.Root
	seen  map[string]bool
	files []NativeOracleFile
}

func (p *nativeIsolationProfileClosure) visit(name string, optional bool) error {
	if p.seen[name] {
		return nil
	}
	if len(p.seen) >= 256 || !nativeContained("/etc/apparmor.d", name) {
		return fmt.Errorf("AppArmor include escapes bounded policy closure")
	}
	p.seen[name] = true
	info, err := p.root.Stat(strings.TrimPrefix(name, "/etc/apparmor.d/"))
	if os.IsNotExist(err) {
		if !optional {
			return fmt.Errorf("required AppArmor include is missing: %s", name)
		}
		p.files = append(p.files, NativeOracleFile{Path: name, SHA256: nativeDigest([]byte("absent"))})
		return nil
	}
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 || info.Mode()&0022 != 0 {
		return fmt.Errorf("AppArmor input is not root-owned and immutable to the workload: %s", name)
	}
	if info.IsDir() {
		return p.directory(name)
	}
	return p.file(name)
}

func (p *nativeIsolationProfileClosure) directory(name string) error {
	entries, err := fs.ReadDir(p.root.FS(), strings.TrimPrefix(name, "/etc/apparmor.d/"))
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || strings.HasSuffix(entry.Name(), "~") {
			continue
		}
		names = append(names, entry.Name())
		if err := p.visit(filepath.Join(name, entry.Name()), false); err != nil {
			return err
		}
	}
	p.files = append(p.files, NativeOracleFile{Path: name, SHA256: nativeDigest([]byte(workflowJSON(names)))})
	return nil
}

func (p *nativeIsolationProfileClosure) file(name string) error {
	f, err := nativeIsolationFile(name)
	if err != nil {
		return err
	}
	p.files = append(p.files, f)
	data, err := p.root.ReadFile(strings.TrimPrefix(name, "/etc/apparmor.d/"))
	if err != nil {
		return err
	}
	for _, match := range nativeIsolationProfileInclude.FindAllSubmatch(data, -1) {
		if err := p.visit(filepath.Join("/etc/apparmor.d", string(match[2])), len(match[1]) != 0); err != nil {
			return err
		}
	}
	return nil
}
