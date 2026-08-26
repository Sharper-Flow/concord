package launcher

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const dependencyInventoryPath = "docs/decisions/CD-0014-terminal-launcher-dependencies.v1.json"

type inventoryLicense struct {
	File   string `json:"file"`
	Family string `json:"family"`
	SHA256 string `json:"sha256"`
	Source string `json:"source"`
}

type inventoryEntry struct {
	Module  string             `json:"module"`
	Version string             `json:"version"`
	Role    string             `json:"role"`
	License []inventoryLicense `json:"license"`
}

type reviewedInventory struct {
	Schema          string           `json:"schema"`
	Package         string           `json:"package"`
	Runtime         []inventoryEntry `json:"runtime"`
	TestOnly        []inventoryEntry `json:"test_only"`
	ModuleGraphOnly []inventoryEntry `json:"module_graph_only"`
}

type moduleMetadata struct {
	Path     string
	Version  string
	Dir      string
	Sum      string
	GoModSum string
}

type moduleRequirement struct {
	Path     string
	Version  string
	Indirect bool
}

type moduleFile struct {
	Require []moduleRequirement
}

const (
	moduleCacheLicenseSource = "module-cache"
	repositoryLicenseSource  = "repository"

	dependencyInventoryTempPrefix = "TestDependencyEvidenceWorksWithoutGitCheckoutState"
	hermeticCacheGracePeriod      = 15 * time.Minute
	// Covers one checkout copy and one module cache with headroom.
	hermeticCacheMinFreeBytes uint64 = 512 << 20
)

func offlineGoCommand(args ...string) *exec.Cmd {
	command := exec.Command("go", args...)
	command.Dir = filepath.Join("..", "..")
	command.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off")
	return command
}

func offlineGoCommandInDir(dir string, args ...string) *exec.Cmd {
	command := exec.Command("go", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off")
	return command
}

func moduleClosure(t *testing.T, args ...string) map[string]moduleMetadata {
	t.Helper()
	command := offlineGoCommand(args...)
	out, err := command.Output()
	if err != nil {
		t.Fatalf("go %v: %v", args, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(out))
	modules := map[string]moduleMetadata{}
	for {
		var pkg struct{ Module *moduleMetadata }
		err := decoder.Decode(&pkg)
		if err == io.EOF {
			return modules
		}
		if err != nil {
			t.Fatalf("decode go %v: %v", args, err)
		}
		if pkg.Module != nil && pkg.Module.Path != "" && pkg.Module.Version != "" {
			modules[pkg.Module.Path] = *pkg.Module
		}
	}
}

func loadReviewedInventory(t *testing.T) reviewedInventory {
	t.Helper()
	root := filepath.Join("..", "..")
	content, err := os.ReadFile(filepath.Join(root, dependencyInventoryPath))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var inventory reviewedInventory
	if err := decoder.Decode(&inventory); err != nil {
		t.Fatalf("decode reviewed dependency inventory: %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("reviewed dependency inventory has trailing JSON: %v", err)
	}
	if inventory.Schema != "concord.bubbletea-dependency-inventory.v1" {
		t.Fatalf("unexpected dependency inventory schema %q", inventory.Schema)
	}
	if inventory.Package != "./internal/launcher/render/bubbletea" {
		t.Fatalf("unexpected dependency inventory package %q", inventory.Package)
	}
	return inventory
}

func inventoryMap(t *testing.T, inventory reviewedInventory) map[string]inventoryEntry {
	t.Helper()
	entries := make(map[string]inventoryEntry, len(inventory.Runtime)+len(inventory.TestOnly)+len(inventory.ModuleGraphOnly))
	for group, values := range map[string][]inventoryEntry{
		"runtime":           inventory.Runtime,
		"test_only":         inventory.TestOnly,
		"module_graph_only": inventory.ModuleGraphOnly,
	} {
		previous := ""
		for _, entry := range values {
			if entry.Module == "" || entry.Version == "" || entry.Role == "" || len(entry.License) == 0 {
				t.Errorf("incomplete %s inventory entry: %#v", group, entry)
			}
			if entry.Module <= previous {
				t.Errorf("%s inventory is not strictly sorted by module: %s", group, entry.Module)
			}
			previous = entry.Module
			if _, exists := entries[entry.Module]; exists {
				t.Errorf("module appears more than once in inventory: %s", entry.Module)
			}
			switch group {
			case "runtime":
				if entry.Role != "runtime direct" && entry.Role != "runtime transitive" {
					t.Errorf("invalid runtime role for %s: %q", entry.Module, entry.Role)
				}
			case "test_only":
				if entry.Role != "test-only transitive" {
					t.Errorf("invalid test-only role for %s: %q", entry.Module, entry.Role)
				}
			case "module_graph_only":
				if entry.Role != "module graph only" {
					t.Errorf("invalid module-graph-only role for %s: %q", entry.Module, entry.Role)
				}
			}
			entries[entry.Module] = entry
		}
	}
	return entries
}

func moduleGraph(t *testing.T, roots map[string]string) map[string]string {
	t.Helper()
	graphModuleDir := t.TempDir()
	graphModPath := filepath.Join(graphModuleDir, "go.mod")
	rootPaths := make([]string, 0, len(roots))
	for path := range roots {
		rootPaths = append(rootPaths, path)
	}
	sort.Strings(rootPaths)
	var graphMod strings.Builder
	graphMod.WriteString("module example.com/concord-dependency-graph\n\ngo 1.26\n\nrequire (\n")
	for _, path := range rootPaths {
		graphMod.WriteString("\t")
		graphMod.WriteString(path)
		graphMod.WriteString(" ")
		graphMod.WriteString(roots[path])
		graphMod.WriteByte('\n')
	}
	graphMod.WriteString(")\n")
	if err := os.WriteFile(graphModPath, []byte(graphMod.String()), 0o644); err != nil {
		t.Fatalf("write temporary graph go.mod: %v", err)
	}
	command := offlineGoCommandInDir(filepath.Join("..", ".."), "mod", "graph", "-modfile", graphModPath)
	out, err := command.Output()
	if err != nil {
		t.Fatalf("offline go mod graph from direct Charm roots: %v", err)
	}
	edges := map[string][]string{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			edges[fields[0]] = append(edges[fields[0]], fields[1])
		}
	}
	seen := make(map[string]bool, len(roots))
	queue := make([]string, 0, len(roots))
	for _, path := range rootPaths {
		root := path + "@" + roots[path]
		seen[root] = true
		queue = append(queue, root)
	}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		for _, dependency := range edges[node] {
			if !seen[dependency] {
				seen[dependency] = true
				queue = append(queue, dependency)
			}
		}
	}
	selected := map[string]string{}
	for node := range seen {
		path, version, ok := splitModuleNode(node)
		if !ok || path == "go" || path == "toolchain" {
			continue
		}
		if current, exists := selected[path]; !exists || moduleVersionLess(current, version) {
			selected[path] = version
		}
	}
	return selected
}

func splitModuleNode(node string) (string, string, bool) {
	separator := strings.LastIndexByte(node, '@')
	if separator <= 0 || separator == len(node)-1 {
		return "", "", false
	}
	return node[:separator], node[separator+1:], true
}

func moduleVersionLess(left, right string) bool {
	leftCore, leftPre := moduleVersionParts(left)
	rightCore, rightPre := moduleVersionParts(right)
	for index := range leftCore {
		if leftCore[index] != rightCore[index] {
			return leftCore[index] < rightCore[index]
		}
	}
	if len(leftPre) == 0 || len(rightPre) == 0 {
		return len(leftPre) > 0 && len(rightPre) == 0
	}
	for index := 0; index < len(leftPre) && index < len(rightPre); index++ {
		leftNumber, leftIsNumber := versionIdentifier(leftPre[index])
		rightNumber, rightIsNumber := versionIdentifier(rightPre[index])
		if leftIsNumber && rightIsNumber && leftNumber != rightNumber {
			return leftNumber < rightNumber
		}
		if leftIsNumber != rightIsNumber {
			return leftIsNumber
		}
		if leftPre[index] != rightPre[index] {
			return leftPre[index] < rightPre[index]
		}
	}
	return len(leftPre) < len(rightPre)
}

func moduleVersionParts(version string) ([3]int, []string) {
	var core [3]int
	version = strings.TrimPrefix(version, "v")
	parts := strings.SplitN(version, "-", 2)
	for index, part := range strings.Split(parts[0], ".") {
		if index == len(core) {
			break
		}
		value, err := strconv.Atoi(part)
		if err != nil {
			return core, []string{version}
		}
		core[index] = value
	}
	if len(parts) == 1 {
		return core, nil
	}
	return core, strings.Split(parts[1], ".")
}

func versionIdentifier(value string) (int, bool) {
	parsed, err := strconv.Atoi(value)
	return parsed, err == nil
}

func graphModuleMetadata(t *testing.T, path, version string, hashes map[string]string) moduleMetadata {
	t.Helper()
	moduleHash := hashes[path+"@"+version]
	goModHash := hashes[path+"@"+version+"/go.mod"]
	if moduleHash == "" || goModHash == "" {
		t.Fatalf("go.sum checksum entries missing for graph-only module %s %s: module=%q go.mod=%q", path, version, moduleHash, goModHash)
	}
	return moduleMetadata{Path: path, Version: version, Sum: moduleHash, GoModSum: goModHash}
}

func directModules(t *testing.T) map[string]string {
	t.Helper()
	out, err := offlineGoCommand("mod", "edit", "-json").Output()
	if err != nil {
		t.Fatalf("go mod edit -json: %v", err)
	}
	var mod moduleFile
	if err := json.Unmarshal(out, &mod); err != nil {
		t.Fatalf("decode go.mod: %v", err)
	}
	direct := make(map[string]string)
	for _, requirement := range mod.Require {
		if !requirement.Indirect {
			direct[requirement.Path] = requirement.Version
		}
	}
	return direct
}

func goSumHashes(t *testing.T) map[string]string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	hashes := map[string]string{}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 {
			hashes[fields[0]+"@"+fields[1]] = fields[2]
		}
	}
	return hashes
}

func licenseFamily(text string) string {
	upper := strings.ToUpper(text)
	switch {
	case strings.Contains(upper, "APACHE LICENSE") && strings.Contains(upper, "VERSION 2.0"):
		return "Apache-2.0"
	case strings.Contains(upper, "REDISTRIBUTION AND USE IN SOURCE AND BINARY FORMS") && strings.Contains(upper, "NEITHER THE NAME"):
		return "BSD-3-Clause"
	case strings.Contains(upper, "PERMISSION IS HEREBY GRANTED") && !strings.Contains(upper, "REDISTRIBUTION AND USE IN SOURCE AND BINARY FORMS"):
		return "MIT"
	default:
		return ""
	}
}

func verifyInventoryEvidence(t *testing.T, inventory map[string]inventoryEntry, modules map[string]moduleMetadata, roles map[string]string, expectedSource string) {
	t.Helper()
	hashes := goSumHashes(t)
	for path, metadata := range modules {
		entry, ok := inventory[path]
		if !ok {
			t.Errorf("derived module missing from reviewed inventory: %s %s", path, metadata.Version)
			continue
		}
		if metadata.Version != entry.Version {
			t.Errorf("reviewed version for %s=%s, derived=%s", path, entry.Version, metadata.Version)
		}
		if roles[path] != entry.Role {
			t.Errorf("reviewed role for %s=%q, derived=%q", path, entry.Role, roles[path])
		}
		if got := hashes[path+"@"+metadata.Version]; got == "" || got != metadata.Sum {
			t.Errorf("go.sum module checksum missing or mismatched for %s %s: derived=%q go.sum=%q", path, metadata.Version, metadata.Sum, got)
		}
		if metadata.GoModSum == "" || hashes[path+"@"+metadata.Version+"/go.mod"] != metadata.GoModSum {
			t.Errorf("go.sum go.mod checksum missing or mismatched for %s %s: derived=%q go.sum=%q", path, metadata.Version, metadata.GoModSum, hashes[path+"@"+metadata.Version+"/go.mod"])
		}
		if expectedSource == moduleCacheLicenseSource {
			if metadata.Dir == "" {
				t.Errorf("module cache directory missing for %s %s", path, metadata.Version)
			}
			if _, err := os.Stat(metadata.Dir); err != nil {
				t.Errorf("module cache entry missing for %s %s: %v", path, metadata.Version, err)
			}
		}
		for _, evidence := range entry.License {
			evidenceSource := evidence.Source
			if evidenceSource == "" && expectedSource == moduleCacheLicenseSource {
				evidenceSource = moduleCacheLicenseSource
			}
			if evidence.File == "" || evidence.Family == "" || evidenceSource != expectedSource || len(evidence.SHA256) != sha256.Size*2 {
				t.Errorf("invalid license evidence for %s: %#v", path, evidence)
				continue
			}
			cleanEvidenceFile := filepath.Clean(evidence.File)
			if filepath.IsAbs(evidence.File) || cleanEvidenceFile == ".." || strings.HasPrefix(cleanEvidenceFile, ".."+string(os.PathSeparator)) {
				t.Errorf("license evidence path escapes its source for %s/%s", path, evidence.File)
				continue
			}
			if _, err := hex.DecodeString(evidence.SHA256); err != nil {
				t.Errorf("invalid license hash for %s/%s: %v", path, evidence.File, err)
				continue
			}
			licensePath := filepath.Join(metadata.Dir, evidence.File)
			if evidenceSource == repositoryLicenseSource {
				licensePath = filepath.Join("..", "..", evidence.File)
			}
			cleanPath, err := filepath.Abs(licensePath)
			if err != nil {
				t.Errorf("invalid license evidence path for %s/%s: %v", path, evidence.File, err)
				continue
			}
			basePath, err := filepath.Abs(filepath.Join("..", ".."))
			if err != nil || (evidence.Source == repositoryLicenseSource && (cleanPath == basePath || !strings.HasPrefix(cleanPath, basePath+string(os.PathSeparator)))) {
				t.Errorf("license evidence path escapes repository for %s/%s", path, evidence.File)
				continue
			}
			content, err := os.ReadFile(licensePath)
			if err != nil {
				t.Errorf("reviewed license file missing for %s: %s: %v", path, evidence.File, err)
				continue
			}
			digest := sha256.Sum256(content)
			actualHash := hex.EncodeToString(digest[:])
			if actualHash != evidence.SHA256 {
				t.Errorf("license hash mismatch for %s/%s: actual=%s reviewed=%s", path, evidence.File, actualHash, evidence.SHA256)
			}
			if actual := licenseFamily(string(content)); actual != evidence.Family {
				t.Errorf("license family mismatch for %s/%s: actual=%q reviewed=%q", path, evidence.File, actual, evidence.Family)
			}
		}
	}
}

func TestSpikeDependencyEvidenceUsesRuntimeAndTestClosures(t *testing.T) {
	inventoryDocument := loadReviewedInventory(t)
	inventory := inventoryMap(t, inventoryDocument)
	runtime := moduleClosure(t, "list", "-deps", "-json", inventoryDocument.Package)
	test := moduleClosure(t, "list", "-deps", "-test", "-json", inventoryDocument.Package)
	testOnly := map[string]moduleMetadata{}
	for path, metadata := range test {
		if _, inRuntime := runtime[path]; !inRuntime {
			testOnly[path] = metadata
		}
	}
	direct := directModules(t)
	roots := map[string]string{}
	for path, version := range direct {
		if strings.HasPrefix(path, "charm.land/") {
			roots[path] = version
		}
	}
	if len(roots) == 0 {
		t.Fatal("go.mod has no direct Charm module roots")
	}
	runtimeDirectRoots := 0
	for _, entry := range inventoryDocument.Runtime {
		if entry.Role != "runtime direct" {
			continue
		}
		runtimeDirectRoots++
		if roots[entry.Module] != entry.Version {
			t.Errorf("reviewed runtime direct module is not an exact direct Charm root: %s %s", entry.Module, entry.Version)
		}
	}
	if len(roots) != runtimeDirectRoots {
		t.Errorf("direct Charm root count=%d, reviewed runtime direct count=%d", len(roots), runtimeDirectRoots)
	}
	graph := moduleGraph(t, roots)
	hashes := goSumHashes(t)
	graphOnly := map[string]moduleMetadata{}
	for path, version := range graph {
		if hashes[path+"@"+version] == "" || hashes[path+"@"+version+"/go.mod"] == "" {
			continue
		}
		if _, inRuntime := runtime[path]; inRuntime {
			continue
		}
		if _, inTestOnly := testOnly[path]; inTestOnly {
			continue
		}
		graphOnly[path] = graphModuleMetadata(t, path, version, hashes)
	}
	if len(inventory) != len(runtime)+len(testOnly)+len(graphOnly) {
		t.Fatalf("reviewed module count=%d derived runtime=%d test-only=%d graph-only=%d", len(inventory), len(runtime), len(testOnly), len(graphOnly))
	}
	derived := make(map[string]moduleMetadata, len(runtime)+len(testOnly)+len(graphOnly))
	for path, metadata := range runtime {
		derived[path] = metadata
	}
	for path, metadata := range testOnly {
		derived[path] = metadata
	}
	for path, metadata := range graphOnly {
		derived[path] = metadata
		if strings.HasPrefix(path, "modernc.org/") {
			t.Errorf("unrelated SQLite module entered launcher graph-only inventory: %s", path)
		}
	}
	for path, entry := range inventory {
		if _, ok := derived[path]; !ok {
			t.Errorf("reviewed inventory module is outside both adapter closures: %s %s (%s)", path, entry.Version, entry.Role)
		}
	}
	roles := map[string]string{}
	for path := range runtime {
		if _, already := roles[path]; already {
			t.Errorf("module appears in both runtime and test-only closures: %s", path)
		}
		entry, ok := inventory[path]
		if !ok {
			t.Errorf("runtime module missing from reviewed inventory: %s", path)
			continue
		}
		roles[path] = entry.Role
	}
	for path := range testOnly {
		entry, ok := inventory[path]
		if !ok {
			t.Errorf("test-only module missing from reviewed inventory: %s", path)
			continue
		}
		roles[path] = entry.Role
	}
	for path := range graphOnly {
		entry, ok := inventory[path]
		if !ok {
			t.Errorf("graph-only module missing from reviewed inventory: %s", path)
			continue
		}
		roles[path] = entry.Role
	}
	verifyInventoryEvidence(t, inventory, runtime, roles, moduleCacheLicenseSource)
	verifyInventoryEvidence(t, inventory, testOnly, roles, moduleCacheLicenseSource)
	verifyInventoryEvidence(t, inventory, graphOnly, roles, repositoryLicenseSource)

	for path, entry := range inventory {
		if entry.Role == "runtime direct" {
			if direct[path] != entry.Version {
				t.Errorf("direct dependency mismatch for %s: go.mod=%q reviewed=%q", path, direct[path], entry.Version)
			}
		} else if entry.Role == "runtime transitive" {
			if _, ok := direct[path]; ok {
				t.Errorf("runtime transitive inventory module is direct in go.mod: %s", path)
			}
		}
	}

	decisionPath := filepath.Join("..", "..", "docs", "decisions", "CD-0014-terminal-launcher-rendering.md")
	decision, err := os.ReadFile(decisionPath)
	if err != nil {
		t.Fatal(err)
	}
	decisionText := string(decision)
	artifact, err := os.ReadFile(filepath.Join("..", "..", dependencyInventoryPath))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(artifact)
	artifactHash := hex.EncodeToString(digest[:])
	if !strings.Contains(decisionText, "./CD-0014-terminal-launcher-dependencies.v1.json") || !strings.Contains(decisionText, artifactHash) {
		t.Errorf("CD-0014 does not bind the reviewed dependency artifact hash %s", artifactHash)
	}
	if strings.Contains(decisionText, "git diff -- go.sum") || strings.Contains(decisionText, "newly checksummed") {
		t.Error("CD-0014 still treats git diff or newly added checksums as dependency inventory authority")
	}
	if !strings.Contains(decisionText, "19 runtime modules, zero test-only modules, and 4 module-graph-only") {
		t.Error("CD-0014 does not state the three-way closure/graph inventory counts")
	}
	if !strings.Contains(decisionText, "graph evidence only") || !strings.Contains(decisionText, "checked-in") || !strings.Contains(decisionText, "bounded evidence") {
		t.Error("CD-0014 does not explain the module-graph-only inventory role")
	}
	for _, entry := range inventoryDocument.Runtime {
		if entry.Role == "runtime direct" && !strings.Contains(decisionText, "`"+entry.Module+" "+entry.Version+"`") {
			t.Errorf("CD-0014 decision omits direct dependency %s %s", entry.Module, entry.Version)
		}
	}
}

func copyCheckout(t *testing.T, source, destination string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}

func makeTreeWritable(root string) {
	_ = filepath.Walk(root, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		mode := info.Mode().Perm()
		if info.IsDir() {
			mode |= 0o700
		} else {
			mode |= 0o600
		}
		_ = os.Chmod(path, mode)
		return nil
	})
}

func sweepStaleDependencyInventoryCaches(now time.Time) {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	cutoff := now.Add(-hermeticCacheGracePeriod)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), dependencyInventoryTempPrefix) {
			continue
		}
		path := filepath.Join(os.TempDir(), entry.Name())
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		makeTreeWritable(path)
		_ = os.RemoveAll(path)
	}
}

func tempFilesystemFreeBytes() (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(os.TempDir(), &stat); err != nil {
		return 0, err
	}
	if stat.Bavail <= 0 || stat.Bsize <= 0 {
		return 0, nil
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}

func requireHermeticCacheSpace(t *testing.T) {
	t.Helper()
	freeBytes, err := tempFilesystemFreeBytes()
	if err != nil {
		t.Skipf("unable to measure free space on temp filesystem: %v", err)
	}
	if freeBytes < hermeticCacheMinFreeBytes {
		t.Skipf("temp filesystem has %d bytes free, below the %d-byte threshold for the hermetic dependency-inventory cache", freeBytes, hermeticCacheMinFreeBytes)
	}
}

func TestSweepStaleDependencyInventoryCaches(t *testing.T) {
	now := time.Now()
	stale, err := os.MkdirTemp(os.TempDir(), dependencyInventoryTempPrefix+"-stale-")
	if err != nil {
		t.Fatal(err)
	}
	makeTreeWritable(stale)
	t.Cleanup(func() {
		makeTreeWritable(stale)
		_ = os.RemoveAll(stale)
	})
	readonlyDir := filepath.Join(stale, "readonly")
	if err := os.Mkdir(readonlyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	readonlyFile := filepath.Join(readonlyDir, "cache.zip")
	if err := os.WriteFile(readonlyFile, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(readonlyDir, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(readonlyFile, 0o400); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-(hermeticCacheGracePeriod + time.Hour))
	if err := os.Chtimes(readonlyFile, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(readonlyDir, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	fresh, err := os.MkdirTemp(os.TempDir(), dependencyInventoryTempPrefix+"-fresh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		makeTreeWritable(fresh)
		_ = os.RemoveAll(fresh)
	})

	sweepStaleDependencyInventoryCaches(now)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale cache still exists after sweep: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh concurrent cache was swept: %v", err)
	}
}

func TestDependencyEvidenceWorksWithoutGitCheckoutState(t *testing.T) {
	if os.Getenv("CONCORD_DEPENDENCY_INVENTORY_CLEAN_CHECKOUT") == "1" {
		t.Skip("clean-checkout child process")
	}
	sweepStaleDependencyInventoryCaches(time.Now())
	requireHermeticCacheSpace(t)
	source, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	clean := t.TempDir()
	copyCheckout(t, source, clean)
	fakeBin := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "git"), []byte("#!/bin/sh\nexit 97\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	freshModuleCache := t.TempDir()
	t.Cleanup(func() { makeTreeWritable(freshModuleCache) })
	compile := exec.Command("go", "test", "./internal/launcher/render/bubbletea")
	compile.Dir = clean
	compile.Env = append(os.Environ(),
		"GOTOOLCHAIN=local",
		"GOMODCACHE="+freshModuleCache,
	)
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("launcher runtime compile failed with fresh module cache: %v\n%s", err, output)
	}
	for _, module := range []string{
		"github.com/aymanbagabas/go-udiff@v0.4.1",
		"github.com/charmbracelet/x/exp/golden@v0.0.0-20250806222409-83e3a29d542f",
		"github.com/dustin/go-humanize@v1.0.1",
		"golang.org/x/exp@v0.0.0-20231006140011-7918f672742d",
	} {
		if _, err := os.Stat(filepath.Join(freshModuleCache, module)); !os.IsNotExist(err) {
			t.Fatalf("runtime-only launcher compile populated graph-only module cache entry %s: %v", module, err)
		}
	}
	command := exec.Command("go", "test", "./internal/launcher", "-run", "^TestSpikeDependencyEvidenceUsesRuntimeAndTestClosures$", "-count=1")
	command.Dir = clean
	command.Env = append(os.Environ(),
		"CONCORD_DEPENDENCY_INVENTORY_CLEAN_CHECKOUT=1",
		"GOTOOLCHAIN=local",
		"GOMODCACHE="+freshModuleCache,
		"GOPROXY=off",
		"GOSUMDB=off",
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("inventory test failed in clean checkout without git: %v\n%s", err, output)
	}
}
