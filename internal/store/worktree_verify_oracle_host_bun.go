package store

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The bun materializer installs one bun.lock closure without running a
// package manager. Bun's own cache keeps unpacked directories, so its bytes
// cannot be rechecked against the lock's tarball integrity. The producer
// instead reads each locked tarball from npm's content-addressed cache,
// proves its sha512 against the lock, verifies the lock against the
// manifest and against every verified archive's own declarations, and
// extracts the tarballs itself into a per-run private node_modules tree. No
// lifecycle script can run because nothing that runs scripts is invoked.

const (
	nativeTargetMaterializerBun = "bun"

	nativeBunMaxPackages     = 8192
	nativeBunMaxEntries      = 524288
	nativeBunMaxFiles        = 262144
	nativeBunMaxBytes        = int64(2) << 30
	nativeBunMaxDepth        = 32
	nativeBunMaxPathBytes    = 1024
	nativeBunMaxTarballBytes = int64(256) << 20
	nativeBunMaxManifest     = 1 << 20
	nativeBunMissingListed   = 8
)

// nativeBunAdmittedVersions is the closed set of bun runtimes whose
// auto-install routing the conformance test proves. Admitting a version is a
// code change that must pass that test.
var nativeBunAdmittedVersions = []string{"1.4.0"}

// nativeBunPackage is one locked registry package at one hoisted key.
type nativeBunPackage struct {
	Key, Name, Version string
	Integrity          []byte
	Deps, OptDeps      map[string]string
	PeerDeps           map[string]string
	OptionalPeers      []string
	OS, CPU            []string
	hasOS, hasCPU      bool
}

// nativeBunLock is one parsed closed bun.lock.
type nativeBunLock struct {
	Root     nativeBunWorkspace
	Packages map[string]nativeBunPackage
}

type nativeBunWorkspace struct {
	Name     string            `json:"name"`
	Deps     map[string]string `json:"dependencies"`
	DevDeps  map[string]string `json:"devDependencies"`
	OptDeps  map[string]string `json:"optionalDependencies"`
	PeerDeps map[string]string `json:"peerDependencies"`
}

func nativeBunUnavailable(cause string) error {
	return nativeTargetUnavailableMaterializer(nativeTargetMaterializerBun, cause)
}

// nativeBunStripTrailingCommas removes commas that directly precede a
// closing bracket or brace outside strings. Comments and every other
// non-JSON token are left for the strict decoder to refuse.
func nativeBunStripTrailingCommas(raw []byte) ([]byte, error) {
	out := make([]byte, 0, len(raw))
	inString, escaped := false, false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '/':
			return nil, nativeBunUnavailable("bun.lock carries a comment or non-JSON token")
		case ',':
			j := i + 1
			for j < len(raw) && (raw[j] == ' ' || raw[j] == '\t' || raw[j] == '\n' || raw[j] == '\r') {
				j++
			}
			if j < len(raw) && (raw[j] == '}' || raw[j] == ']') {
				continue
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// nativeBunNamePattern is the npm package-name syntax for one hoisted
// segment: an optional scope and a lowercase URL-safe name.
var nativeBunNamePattern = regexp.MustCompile(`^(@[a-z0-9~-][a-z0-9._~-]*/)?[a-z0-9~-][a-z0-9._~-]*$`)

func nativeBunValidName(name string) bool {
	if !nativeBunNamePattern.MatchString(name) {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "." || part == ".." || part == "node_modules" {
			return false
		}
	}
	return true
}

// nativeBunKeySegments splits one hoisted lock key into package names.
func nativeBunKeySegments(key string) ([]string, bool) {
	parts := strings.Split(key, "/")
	var names []string
	for i := 0; i < len(parts); i++ {
		name := parts[i]
		if strings.HasPrefix(name, "@") {
			if i+1 >= len(parts) {
				return nil, false
			}
			name += "/" + parts[i+1]
			i++
		}
		if !nativeBunValidName(name) {
			return nil, false
		}
		names = append(names, name)
	}
	return names, len(names) > 0
}

// nativeBunSplitSpec splits "name@version" at the version separator.
func nativeBunSplitSpec(spec string) (string, string, bool) {
	at := strings.LastIndex(spec, "@")
	if at <= 0 {
		return "", "", false
	}
	return spec[:at], spec[at+1:], true
}

var nativeBunLockTopKeys = []string{"lockfileVersion", "configVersion", "workspaces", "packages"}

// parseNativeBunLock decodes the closed bun.lock grammar.
func parseNativeBunLock(raw []byte) (nativeBunLock, error) {
	var lock nativeBunLock
	clean, err := nativeBunStripTrailingCommas(raw)
	if err != nil {
		return lock, err
	}
	var top map[string]json.RawMessage
	if err := nativeBunStrictJSON(clean, &top); err != nil {
		return lock, nativeBunUnavailable("bun.lock is not one JSON object")
	}
	for key := range top {
		if !slices.Contains(nativeBunLockTopKeys, key) {
			return lock, nativeBunUnavailable("bun.lock carries the unsupported top-level key " + key)
		}
	}
	var version int
	if json.Unmarshal(top["lockfileVersion"], &version) != nil || version != 1 {
		return lock, nativeBunUnavailable("bun.lock lockfileVersion must equal 1")
	}
	if config, ok := top["configVersion"]; ok {
		if json.Unmarshal(config, &version) != nil || version != 1 {
			return lock, nativeBunUnavailable("bun.lock configVersion must be absent or 1")
		}
	}
	var workspaces map[string]json.RawMessage
	if json.Unmarshal(top["workspaces"], &workspaces) != nil || len(workspaces) != 1 || workspaces[""] == nil {
		return lock, nativeBunUnavailable("bun.lock must hold exactly the root workspace")
	}
	if err := nativeBunStrictJSON(workspaces[""], &lock.Root); err != nil {
		return lock, nativeBunUnavailable("bun.lock root workspace carries unsupported fields")
	}
	var packages map[string][]json.RawMessage
	if json.Unmarshal(top["packages"], &packages) != nil {
		return lock, nativeBunUnavailable("bun.lock packages are not tuples")
	}
	if len(packages) > nativeBunMaxPackages {
		return lock, oracleFailure(KindLimitExceeded, "bun.lock declares more than "+strconv.Itoa(nativeBunMaxPackages)+" packages", "reduce the locked dependency set")
	}
	lock.Packages = make(map[string]nativeBunPackage, len(packages))
	for key, tuple := range packages {
		pkg, err := parseNativeBunPackage(key, tuple)
		if err != nil {
			return lock, err
		}
		lock.Packages[key] = pkg
	}
	return lock, nil
}

func parseNativeBunPackage(key string, tuple []json.RawMessage) (nativeBunPackage, error) {
	pkg := nativeBunPackage{Key: key}
	if _, ok := nativeBunKeySegments(key); !ok {
		return pkg, nativeBunUnavailable("bun.lock key is not a chain of package names: " + key)
	}
	var spec, registry, integrity string
	if len(tuple) != 4 || json.Unmarshal(tuple[0], &spec) != nil || json.Unmarshal(tuple[1], &registry) != nil || json.Unmarshal(tuple[3], &integrity) != nil {
		return pkg, nativeBunUnavailable("bun.lock entry is not a default-registry package tuple: " + key)
	}
	if registry != "" {
		return pkg, nativeBunUnavailable("bun.lock entry names a non-default registry: " + key)
	}
	name, version, ok := nativeBunSplitSpec(spec)
	if !ok || !nativeBunValidName(name) {
		return pkg, nativeBunUnavailable("bun.lock entry has no registry name@version: " + key)
	}
	if _, ok := parseNativeSemver(version); !ok {
		return pkg, nativeBunUnavailable("bun.lock entry version is not SemVer: " + spec)
	}
	pkg.Name, pkg.Version = name, version
	digest, ok := strings.CutPrefix(integrity, "sha512-")
	if !ok {
		return pkg, nativeBunUnavailable("bun.lock entry integrity is not one sha512 value: " + key)
	}
	sum, err := base64.StdEncoding.DecodeString(digest)
	if err != nil || len(sum) != sha512.Size {
		return pkg, nativeBunUnavailable("bun.lock entry integrity is not one sha512 value: " + key)
	}
	pkg.Integrity = sum
	var meta map[string]json.RawMessage
	if json.Unmarshal(tuple[2], &meta) != nil {
		return pkg, nativeBunUnavailable("bun.lock entry metadata is not an object: " + key)
	}
	for field, value := range meta {
		var err error
		switch field {
		case "dependencies":
			err = json.Unmarshal(value, &pkg.Deps)
		case "optionalDependencies":
			err = json.Unmarshal(value, &pkg.OptDeps)
		case "peerDependencies":
			err = json.Unmarshal(value, &pkg.PeerDeps)
		case "optionalPeers":
			err = json.Unmarshal(value, &pkg.OptionalPeers)
		case "os":
			pkg.OS, err = nativeBunPlatformList(value)
			pkg.hasOS = true
		case "cpu":
			pkg.CPU, err = nativeBunPlatformList(value)
			pkg.hasCPU = true
		case "bin":
		default:
			return pkg, nativeBunUnavailable("bun.lock entry metadata carries the unsupported field " + field + ": " + key)
		}
		if err != nil {
			return pkg, nativeBunUnavailable("bun.lock entry metadata field " + field + " is malformed: " + key)
		}
	}
	return pkg, nil
}

func nativeBunPlatformList(raw json.RawMessage) ([]string, error) {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}, nil
	}
	var many []string
	err := json.Unmarshal(raw, &many)
	return many, err
}

func nativeBunStrictJSON(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return errors.New("trailing input")
	}
	return nil
}

// nativeBunPlatformMatch decides one os or cpu constraint for the release
// platform. A list matches when it names the host value, or names only
// negations without the host's own negation.
func nativeBunPlatformMatch(present bool, values []string, host string) bool {
	if !present {
		return true
	}
	if slices.Contains(values, "!"+host) {
		return false
	}
	if slices.Contains(values, host) {
		return true
	}
	return !slices.ContainsFunc(values, func(v string) bool { return !strings.HasPrefix(v, "!") })
}

func (p nativeBunPackage) selected() bool {
	return nativeBunPlatformMatch(p.hasOS, p.OS, "linux") && nativeBunPlatformMatch(p.hasCPU, p.CPU, "x64")
}

// nativeBunManifest is the closed set of package.json fields the
// materializer reads; every field that changes resolution beyond them
// refuses.
type nativeBunManifest struct {
	Name     string
	Deps     map[string]string
	DevDeps  map[string]string
	OptDeps  map[string]string
	PeerDeps map[string]string
}

var nativeBunManifestRefused = []string{"workspaces", "overrides", "resolutions", "catalog", "catalogs", "patchedDependencies"}

func parseNativeBunManifest(raw []byte) (nativeBunManifest, error) {
	var m nativeBunManifest
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return m, nativeBunUnavailable("package.json is not one JSON object")
	}
	for _, field := range nativeBunManifestRefused {
		if _, ok := fields[field]; ok {
			return m, nativeBunUnavailable("package.json declares the unsupported field " + field)
		}
	}
	for field, target := range map[string]any{"name": &m.Name, "dependencies": &m.Deps, "devDependencies": &m.DevDeps, "optionalDependencies": &m.OptDeps, "peerDependencies": &m.PeerDeps} {
		if value, ok := fields[field]; ok && json.Unmarshal(value, target) != nil {
			return m, nativeBunUnavailable("package.json field " + field + " is malformed")
		}
	}
	return m, nil
}

// nativeBunInstall is one package the materializer extracts: its hoisted
// directory under node_modules and its verified lock entry.
type nativeBunInstall struct {
	Dir string
	Pkg nativeBunPackage
}

// verifyNativeBunLock proves manifest freshness, the closed range grammar,
// edge identity and range satisfaction, and required-edge closure. It
// returns the selected packages in deterministic install order.
func verifyNativeBunLock(manifest nativeBunManifest, lock nativeBunLock) ([]nativeBunInstall, error) {
	root := lock.Root
	if manifest.Name != root.Name || !maps.Equal(manifest.Deps, root.Deps) || !maps.Equal(manifest.DevDeps, root.DevDeps) || !maps.Equal(manifest.OptDeps, root.OptDeps) || !maps.Equal(manifest.PeerDeps, root.PeerDeps) {
		return nil, nativeBunUnavailable("bun.lock root workspace differs from package.json: the lock is stale")
	}
	type edge struct {
		from     string
		deps     map[string]string
		required bool
	}
	edges := []edge{{"", root.Deps, true}, {"", root.DevDeps, true}, {"", root.OptDeps, false}}
	keys := slices.Sorted(maps.Keys(lock.Packages))
	var installs []nativeBunInstall
	for _, key := range keys {
		pkg := lock.Packages[key]
		if !pkg.selected() {
			continue
		}
		edges = append(edges, edge{key, pkg.Deps, true}, edge{key, pkg.OptDeps, false})
		segments, _ := nativeBunKeySegments(key)
		installs = append(installs, nativeBunInstall{Dir: "node_modules/" + strings.Join(segments, "/node_modules/"), Pkg: pkg})
	}
	for _, e := range edges {
		for _, name := range slices.Sorted(maps.Keys(e.deps)) {
			spec := e.deps[name]
			target, rangeText := name, spec
			if alias, ok := strings.CutPrefix(spec, "npm:"); ok {
				aliasName, aliasRange, ok := nativeBunSplitSpec(alias)
				if !ok || !nativeBunValidName(aliasName) {
					return nil, nativeBunUnavailable("dependency alias is malformed: " + name + "@" + spec)
				}
				target, rangeText = aliasName, aliasRange
			}
			r, ok := parseNativeSemverRange(rangeText)
			if !ok {
				return nil, nativeBunUnavailable("dependency specifier is outside the supported range grammar: " + name + "@" + spec)
			}
			key, found := nativeBunResolve(lock, e.from, name)
			if !found {
				if e.required {
					return nil, nativeBunUnavailable("required dependency " + name + " of " + nativeBunFrom(e.from) + " resolves to no installable lock entry")
				}
				continue
			}
			pkg := lock.Packages[key]
			v, _ := parseNativeSemver(pkg.Version)
			if pkg.Name != target || !r.satisfies(v) {
				return nil, nativeBunUnavailable("dependency " + name + "@" + spec + " of " + nativeBunFrom(e.from) + " resolves to " + pkg.Name + "@" + pkg.Version)
			}
		}
	}
	return installs, nil
}

func nativeBunFrom(key string) string {
	if key == "" {
		return "the root workspace"
	}
	return key
}

// nativeBunResolve applies Node lookup over the hoisted key chain: the
// nearest enclosing node_modules that holds an installed (platform-selected)
// entry of the name.
func nativeBunResolve(lock nativeBunLock, from, name string) (string, bool) {
	segments, _ := nativeBunKeySegments(from)
	for {
		key := strings.Join(append(slices.Clone(segments), name), "/")
		if pkg, ok := lock.Packages[key]; ok && pkg.selected() {
			return key, true
		}
		if len(segments) == 0 {
			return "", false
		}
		segments = segments[:len(segments)-1]
	}
}

// nativeBunArchiveManifest is the subset of an archive's package.json that
// reconciliation compares with the lock.
type nativeBunArchiveManifest struct {
	Name         string                     `json:"name"`
	Version      string                     `json:"version"`
	Deps         map[string]string          `json:"dependencies"`
	OptDeps      map[string]string          `json:"optionalDependencies"`
	PeerDeps     map[string]string          `json:"peerDependencies"`
	PeerDepsMeta map[string]json.RawMessage `json:"peerDependenciesMeta"`
}

// reconcileNativeBunArchive proves the lock entry declares exactly what the
// integrity-verified archive declares, so a lock cannot drop a transitive
// dependency or edit a range.
func reconcileNativeBunArchive(pkg nativeBunPackage, raw []byte) error {
	var a nativeBunArchiveManifest
	if json.Unmarshal(raw, &a) != nil {
		return nativeBunUnavailable("package archive package.json is malformed: " + pkg.Key)
	}
	if a.Name != pkg.Name || a.Version != pkg.Version {
		return nativeBunUnavailable("package archive is " + a.Name + "@" + a.Version + ", not the locked " + pkg.Name + "@" + pkg.Version)
	}
	deps := maps.Clone(a.Deps)
	for name := range a.OptDeps {
		delete(deps, name)
	}
	var optionalPeers []string
	for name, meta := range a.PeerDepsMeta {
		var m struct {
			Optional bool `json:"optional"`
		}
		if json.Unmarshal(meta, &m) == nil && m.Optional {
			optionalPeers = append(optionalPeers, name)
		}
	}
	slices.Sort(optionalPeers)
	lockPeers := slices.Sorted(slices.Values(pkg.OptionalPeers))
	peersOK := true
	for name, value := range pkg.PeerDeps {
		archived, ok := a.PeerDeps[name]
		_, metaOnly := a.PeerDepsMeta[name]
		if !(ok && archived == value || !ok && metaOnly && value == "*") {
			peersOK = false
		}
	}
	for name := range a.PeerDeps {
		if _, ok := pkg.PeerDeps[name]; !ok {
			peersOK = false
		}
	}
	if !maps.Equal(deps, pkg.Deps) || !maps.Equal(a.OptDeps, pkg.OptDeps) || !peersOK || !slices.Equal(optionalPeers, lockPeers) {
		return nativeBunUnavailable("lock does not match package archive: " + pkg.Key)
	}
	return nil
}

// nativeBunCacheContent is the npm cacache content path for one sha512.
func nativeBunCacheContent(cacheDir string, sum []byte) string {
	h := hex.EncodeToString(sum)
	return filepath.Join(cacheDir, "content-v2", "sha512", h[:2], h[2:4], h[4:])
}

// nativeBunCacheDir is the npm cache of the Concord process's own home.
// npm configuration is user configuration and is never consulted.
func nativeBunCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nativeBunUnavailable("no home directory for the npm cache: " + err.Error())
	}
	return filepath.Join(home, ".npm", "_cacache"), nil
}

// nativeBunMissing names the selected packages absent from the cache, with
// the host recovery that warms them outside the oracle.
func nativeBunMissing(cacheDir string, installs []nativeBunInstall) error {
	var missing []string
	count := 0
	for _, in := range installs {
		if _, err := os.Stat(nativeBunCacheContent(cacheDir, in.Pkg.Integrity)); err != nil {
			count++
			if len(missing) < nativeBunMissingListed {
				missing = append(missing, in.Pkg.Name+"@"+in.Pkg.Version)
			}
		}
	}
	if count == 0 {
		return nil
	}
	return nativeBunUnavailable(strconv.Itoa(count) + " locked packages are missing from the npm cache: " + strings.Join(missing, ", ") +
		"; warm it outside the oracle with `npm cache add --ignore-scripts " + strings.Join(missing, " ") + "`, then request explicit native preparation")
}

// nativeBunBudget bounds one run's whole extraction.
type nativeBunBudget struct {
	entries, files int
	bytes          int64
}

func nativeBunLimit(what string) error {
	return oracleFailure(KindLimitExceeded, "bun materialization exceeds the "+what+" bound", "reduce the locked dependency set")
}

// materializeNativeBunTree reads, verifies, reconciles, and extracts every
// selected package into dest (the producer-owned node_modules parent).
func materializeNativeBunTree(ctx context.Context, cacheDir, dest string, installs []nativeBunInstall) error {
	if err := nativeBunMissing(cacheDir, installs); err != nil {
		return err
	}
	rooted, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer func() { _ = rooted.Close() }()
	if err := rooted.MkdirAll("node_modules", 0755); err != nil {
		return err
	}
	budget := &nativeBunBudget{}
	for _, in := range installs {
		if err := ctx.Err(); err != nil {
			return err
		}
		tarball, err := nativeBunReadCached(cacheDir, in.Pkg)
		if err != nil {
			return err
		}
		manifest, err := extractNativeBunPackage(ctx, rooted, in.Dir, tarball, budget)
		if err != nil {
			return err
		}
		if err := reconcileNativeBunArchive(in.Pkg, manifest); err != nil {
			return err
		}
	}
	return nil
}

// nativeBunReadCached reads one cached tarball once and proves its bytes
// against the lock integrity. The returned bytes are the ones extracted.
func nativeBunReadCached(cacheDir string, pkg nativeBunPackage) ([]byte, error) {
	file, err := os.Open(nativeBunCacheContent(cacheDir, pkg.Integrity))
	if err != nil {
		return nil, nativeBunUnavailable("locked package is missing from the npm cache: " + pkg.Name + "@" + pkg.Version)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, nativeBunMaxTarballBytes+1))
	if err != nil {
		return nil, nativeBunUnavailable("cached package is unreadable: " + pkg.Name + "@" + pkg.Version)
	}
	if int64(len(data)) > nativeBunMaxTarballBytes {
		return nil, nativeBunLimit("compressed-tarball")
	}
	sum := sha512.Sum512(data)
	if !bytes.Equal(sum[:], pkg.Integrity) {
		return nil, nativeBunUnavailable("corrupt cache entry: " + pkg.Name + "@" + pkg.Version + " does not match its locked sha512")
	}
	return data, nil
}

// extractNativeBunPackage writes one verified tarball into dir under the
// rooted destination and returns the archive's package.json bytes. Only
// regular files and directories are admitted; every name is canonical,
// contained, free of node_modules segments, and unique, so packages cannot
// overlap whatever their extraction order. Every bound is checked before the
// work it limits.
func extractNativeBunPackage(ctx context.Context, rooted *os.Root, dir string, tarball []byte, budget *nativeBunBudget) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(tarball))
	if err != nil {
		return nil, nativeBunUnavailable("cached package is not a gzip tarball: " + dir)
	}
	tr := tar.NewReader(zr)
	refuse := func(cause string) error {
		return nativeBunUnavailable("package archive " + dir + " " + cause)
	}
	top := ""
	kinds := map[string]byte{}
	var manifest []byte
	if err := nativeBunExtractionPathBounds(dir, strings.Count(dir, "/")+1); err != nil {
		return nil, err
	}
	if err := rooted.MkdirAll(dir, 0755); err != nil {
		return nil, refuse("cannot be placed: " + err.Error())
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, refuse("is malformed: " + err.Error())
		}
		budget.entries++
		if budget.entries > nativeBunMaxEntries {
			return nil, nativeBunLimit("tar-entry")
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		rel, target, err := nativeBunArchivePath(hdr, dir, &top, refuse)
		if err != nil {
			return nil, err
		}
		if rel == "" {
			continue
		}
		isDir := hdr.Typeflag == tar.TypeDir
		if err := nativeBunClaimArchivePath(rel, isDir, kinds, refuse); err != nil {
			return nil, err
		}
		if isDir {
			if err := rooted.MkdirAll(target, 0755); err != nil {
				return nil, refuse("directory cannot be written: " + err.Error())
			}
			continue
		}
		data, err := writeNativeBunArchiveFile(rooted, target, rel, hdr, tr, budget, refuse)
		if err != nil {
			return nil, err
		}
		if rel == "package.json" {
			manifest = data
		}
	}
	if manifest == nil {
		return nil, refuse("holds no package.json")
	}
	return manifest, nil
}

func nativeBunArchivePath(hdr *tar.Header, dir string, top *string, refuse func(string) error) (string, string, error) {
	name := hdr.Name
	isDir := hdr.Typeflag == tar.TypeDir
	switch hdr.Typeflag {
	case tar.TypeReg, tar.TypeDir:
	default:
		return "", "", refuse("holds an unsupported entry type (link, device, FIFO, or sparse): " + name)
	}
	if isDir {
		name = strings.TrimSuffix(name, "/")
	}
	if name == "" || strings.ContainsAny(name, "\\\x00") || path.IsAbs(name) || path.Clean(name) != name {
		return "", "", refuse("holds a non-canonical entry name: " + hdr.Name)
	}
	segments := strings.Split(name, "/")
	if slices.ContainsFunc(segments, func(s string) bool { return s == "" || s == "." || s == ".." }) {
		return "", "", refuse("holds a traversing entry name: " + hdr.Name)
	}
	if *top == "" {
		*top = segments[0]
	} else if segments[0] != *top {
		return "", "", refuse("holds more than one top directory")
	}
	rest := segments[1:]
	if len(rest) == 0 {
		if !isDir {
			return "", "", refuse("holds a file in place of its top directory")
		}
		return "", "", nil
	}
	if slices.Contains(rest, "node_modules") {
		return "", "", refuse("bundles a node_modules path: " + hdr.Name)
	}
	rel := strings.Join(rest, "/")
	target := dir + "/" + rel
	if err := nativeBunExtractionPathBounds(target, len(rest)+strings.Count(dir, "/")+1); err != nil {
		return "", "", err
	}
	return rel, target, nil
}

func nativeBunExtractionPathBounds(target string, depth int) error {
	if depth > nativeBunMaxDepth {
		return nativeBunLimit("path-depth")
	}
	if len(target) > nativeBunMaxPathBytes {
		return nativeBunLimit("path-length")
	}
	return nil
}

func nativeBunClaimArchivePath(rel string, isDir bool, kinds map[string]byte, refuse func(string) error) error {
	rest := strings.Split(rel, "/")
	for i := 1; i < len(rest); i++ {
		ancestor := strings.Join(rest[:i], "/")
		if kinds[ancestor] == 'f' {
			return refuse("holds a file and a directory at one path: " + ancestor)
		}
		kinds[ancestor] = 'd'
	}
	if _, seen := kinds[rel]; seen {
		if !isDir || kinds[rel] != 'd' {
			return refuse("holds a duplicate or conflicting entry: " + rel)
		}
	}
	if isDir {
		kinds[rel] = 'd'
	} else {
		kinds[rel] = 'f'
	}
	return nil
}

func writeNativeBunArchiveFile(rooted *os.Root, target, rel string, hdr *tar.Header, tr *tar.Reader, budget *nativeBunBudget, refuse func(string) error) ([]byte, error) {
	budget.files++
	if budget.files > nativeBunMaxFiles {
		return nil, nativeBunLimit("file-count")
	}
	if hdr.Size < 0 || hdr.Size > nativeBunMaxBytes-budget.bytes {
		return nil, nativeBunLimit("extracted-size")
	}
	budget.bytes += hdr.Size
	if err := rooted.MkdirAll(path.Dir(target), 0755); err != nil {
		return nil, refuse("directory cannot be written: " + err.Error())
	}
	mode := os.FileMode(0644)
	if hdr.Mode&0111 != 0 {
		mode = 0755
	}
	out, err := rooted.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return nil, refuse("entry cannot be written: " + err.Error())
	}
	// The create mode passes through the process umask; the declared
	// mode is set on the open file itself.
	if err := out.Chmod(mode); err != nil {
		_ = out.Close()
		return nil, refuse("entry cannot be written: " + err.Error())
	}
	var keep bytes.Buffer
	var w io.Writer = out
	if rel == "package.json" {
		if hdr.Size > nativeBunMaxManifest {
			_ = out.Close()
			return nil, refuse("package.json exceeds 1 MiB")
		}
		w = io.MultiWriter(out, &keep)
	}
	_, copyErr := io.CopyN(w, tr, hdr.Size)
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		return nil, refuse("entry cannot be written: " + errors.Join(copyErr, closeErr).Error())
	}
	return keep.Bytes(), nil
}

// nativeBunTreeDigest fingerprints one materialized node_modules tree: every
// directory and regular file with its path, type, and mode, and each file's
// sha256. A symlink or any other entry type refuses.
func nativeBunTreeDigest(dir string) ([]byte, error) {
	rooted, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nativeBunUnavailable("materialized node_modules is missing")
	}
	defer func() { _ = rooted.Close() }()
	var lines []string
	walkErr := fs.WalkDir(rooted.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return nativeBunUnavailable("materialized node_modules is unreadable: " + name)
		}
		info, err := d.Info()
		if err != nil {
			return nativeBunUnavailable("materialized node_modules is unreadable: " + name)
		}
		switch {
		case d.IsDir():
			lines = append(lines, "d "+info.Mode().Perm().String()+" "+name)
		case d.Type().IsRegular():
			data, err := rooted.ReadFile(name)
			if err != nil {
				return nativeBunUnavailable("materialized node_modules is unreadable: " + name)
			}
			sum := sha256.Sum256(data)
			lines = append(lines, "f "+info.Mode().Perm().String()+" "+hex.EncodeToString(sum[:])+" "+name)
		default:
			return nativeBunUnavailable("materialized node_modules holds a symlink or nonregular entry: " + name)
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return []byte(strings.Join(lines, "\n")), nil
}

// nativeBunPlanInstalls parses and verifies the dependency inputs under one
// root and returns the install list. Plan time and run time call it on the
// same pinned bytes.
func nativeBunPlanInstalls(manifestRaw, lockRaw []byte) ([]nativeBunInstall, error) {
	manifest, err := parseNativeBunManifest(manifestRaw)
	if err != nil {
		return nil, err
	}
	lock, err := parseNativeBunLock(lockRaw)
	if err != nil {
		return nil, err
	}
	return verifyNativeBunLock(manifest, lock)
}

// nativeBunBinDir holds the per-run bun link that precedes the inherited
// PATH, so a harness's bare bun is the fingerprinted executable.
func nativeBunBinDir(root string) string { return filepath.Join(root, "bin") }

// nativeBunInstallCacheDir is the private install cache that must stay
// empty: bun writes any auto-installed package there.
func nativeBunInstallCacheDir(root string) string { return filepath.Join(root, "bun-cache") }

// nativeBunAutoloadedName reports the files bun loads by discovery: bunfig
// runner configuration and .env process environment.
func nativeBunAutoloadedName(name string) bool {
	return name == "bunfig.toml" || name == ".bunfig.toml" || name == ".env" || strings.HasPrefix(name, ".env.")
}

// nativeBunHiddenTree returns the first node_modules directory under dir, or
// "" when none exists.
func nativeBunHiddenTree(fsys fs.FS, dir string) string {
	found := ""
	_ = fs.WalkDir(fsys, dir, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && d.Name() == "node_modules" {
			found = name
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// materializeNativeBunRun re-derives the install list from the snapshot's
// dependency inputs, proven equal to the pinned bytes, extracts the verified
// tree into the manifest root, and creates the bun link and the empty
// private install cache.
func materializeNativeBunRun(ctx context.Context, t *nativeTargetOraclePlan, root, cwd string) error {
	snapshot, err := os.OpenRoot(filepath.Join(root, "snapshot"))
	if err != nil {
		return err
	}
	defer func() { _ = snapshot.Close() }()
	raw := map[string][]byte{}
	for _, f := range t.DependencyFiles {
		data, err := snapshot.ReadFile(f.Path)
		if err != nil {
			return nativeBunUnavailable("dependency input is missing from the snapshot: " + f.Path)
		}
		if nativeDigest(data) != f.SHA256 {
			return nativeUnavailable("candidate dependency inputs differ from pinned preparation")
		}
		raw[path.Base(f.Path)] = data
	}
	installs, err := nativeBunPlanInstalls(raw["package.json"], raw["bun.lock"])
	if err != nil {
		return err
	}
	if err := materializeNativeBunTree(ctx, t.Environment.NPMCacheDir, cwd, installs); err != nil {
		return err
	}
	if err := os.Mkdir(nativeBunBinDir(root), 0700); err != nil {
		return err
	}
	if err := os.Symlink(t.Environment.Bun, filepath.Join(nativeBunBinDir(root), "bun")); err != nil {
		return err
	}
	return os.Mkdir(nativeBunInstallCacheDir(root), 0700)
}

// nativeBunRunnerParts fingerprints the bun closure: the resolved bun file,
// the bin link target, and the materialized node_modules inventory. It
// refuses unless the private install cache exists and is empty.
func nativeBunRunnerParts(t *nativeTargetOraclePlan, root string) ([][]byte, error) {
	binary, err := os.ReadFile(t.Environment.Bun)
	if err != nil {
		return nil, nativeBunUnavailable("pinned bun is unreadable")
	}
	if bins, err := os.ReadDir(nativeBunBinDir(root)); err != nil || len(bins) != 1 {
		return nil, nativeBunUnavailable("the per-run bin directory must hold only the pinned bun link")
	}
	link, err := os.Readlink(filepath.Join(nativeBunBinDir(root), "bun"))
	if err != nil || link != t.Environment.Bun {
		return nil, nativeBunUnavailable("the per-run bun link no longer names the pinned bun")
	}
	entries, err := os.ReadDir(nativeBunInstallCacheDir(root))
	if err != nil {
		return nil, nativeBunUnavailable("the private bun install cache is missing")
	}
	if len(entries) > 0 {
		return nil, nativeBunUnavailable("the run auto-installed packages into the private bun install cache: " + entries[0].Name())
	}
	tree, err := nativeBunTreeDigest(filepath.Join(root, "snapshot", filepath.FromSlash(t.Recipe.Root), "node_modules"))
	if err != nil {
		return nil, err
	}
	return [][]byte{[]byte("bun\x00"), binary, []byte("bun-link\x00" + link), []byte("node_modules\x00"), tree}, nil
}
