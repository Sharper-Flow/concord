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
	"strings"
	"testing"
)

const dependencyInventoryPath = "docs/decisions/CD-0014-terminal-launcher-dependencies.v1.json"

type inventoryLicense struct {
	File   string `json:"file"`
	Family string `json:"family"`
	SHA256 string `json:"sha256"`
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

func offlineGoCommand(args ...string) *exec.Cmd {
	command := exec.Command("go", args...)
	command.Dir = filepath.Join("..", "..")
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

func selectedModules(t *testing.T) map[string]moduleMetadata {
	t.Helper()
	out, err := offlineGoCommand("list", "-m", "-json", "all").Output()
	if err != nil {
		t.Fatalf("go list -m -json all: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(out))
	modules := map[string]moduleMetadata{}
	for {
		var module moduleMetadata
		err := decoder.Decode(&module)
		if err == io.EOF {
			return modules
		}
		if err != nil {
			t.Fatalf("decode go list -m -json all: %v", err)
		}
		if module.Path != "" && module.Version != "" {
			modules[module.Path] = module
		}
	}
}

func moduleGraph(t *testing.T, roots map[string]bool) map[string]bool {
	t.Helper()
	out, err := offlineGoCommand("mod", "graph").Output()
	if err != nil {
		t.Fatalf("go mod graph: %v", err)
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
	for root := range roots {
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
	return seen
}

func downloadedModule(t *testing.T, path, version string) moduleMetadata {
	t.Helper()
	out, err := offlineGoCommand("mod", "download", "-json", path+"@"+version).Output()
	if err != nil {
		t.Fatalf("go mod download -json %s@%s: %v", path, version, err)
	}
	var module moduleMetadata
	if err := json.Unmarshal(out, &module); err != nil {
		t.Fatalf("decode go mod download %s@%s: %v", path, version, err)
	}
	if module.Path != path || module.Version != version || module.Dir == "" || module.Sum == "" {
		t.Fatalf("incomplete downloaded module metadata: %#v", module)
	}
	return module
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
		if len(fields) == 3 && !strings.HasSuffix(fields[1], "/go.mod") {
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

func verifyInventoryEvidence(t *testing.T, inventory map[string]inventoryEntry, modules map[string]moduleMetadata, roles map[string]string) {
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
		if metadata.Dir == "" {
			t.Errorf("module cache directory missing for %s %s", path, metadata.Version)
		}
		if _, err := os.Stat(metadata.Dir); err != nil {
			t.Errorf("module cache entry missing for %s %s: %v", path, metadata.Version, err)
		}
		if got := hashes[path+"@"+metadata.Version]; got == "" || got != metadata.Sum {
			t.Errorf("go.sum checksum missing or mismatched for %s %s: go list=%q go.sum=%q", path, metadata.Version, metadata.Sum, got)
		}
		for _, evidence := range entry.License {
			if evidence.File == "" || evidence.Family == "" || len(evidence.SHA256) != sha256.Size*2 {
				t.Errorf("invalid license evidence for %s: %#v", path, evidence)
				continue
			}
			if _, err := hex.DecodeString(evidence.SHA256); err != nil {
				t.Errorf("invalid license hash for %s/%s: %v", path, evidence.File, err)
				continue
			}
			licensePath := filepath.Join(metadata.Dir, evidence.File)
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
	roots := map[string]bool{}
	for path, version := range direct {
		if strings.HasPrefix(path, "charm.land/") {
			roots[path+"@"+version] = true
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
		if !roots[entry.Module+"@"+entry.Version] {
			t.Errorf("reviewed runtime direct module is not an exact direct Charm root: %s %s", entry.Module, entry.Version)
		}
	}
	if len(roots) != runtimeDirectRoots {
		t.Errorf("direct Charm root count=%d, reviewed runtime direct count=%d", len(roots), runtimeDirectRoots)
	}
	graph := moduleGraph(t, roots)
	selected := selectedModules(t)
	graphOnly := map[string]moduleMetadata{}
	for path, selectedModule := range selected {
		if selectedModule.Sum == "" || selectedModule.Dir == "" {
			continue
		}
		version := selectedModule.Version
		if !graph[path+"@"+version] {
			continue
		}
		if _, inRuntime := runtime[path]; inRuntime {
			continue
		}
		if _, inTestOnly := testOnly[path]; inTestOnly {
			continue
		}
		graphOnly[path] = downloadedModule(t, path, version)
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
	verifyInventoryEvidence(t, inventory, runtime, roles)
	verifyInventoryEvidence(t, inventory, testOnly, roles)
	verifyInventoryEvidence(t, inventory, graphOnly, roles)

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
	if !strings.Contains(decisionText, "fetched/checksummed module metadata not linked into the") {
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

func TestDependencyEvidenceWorksWithoutGitCheckoutState(t *testing.T) {
	if os.Getenv("CONCORD_DEPENDENCY_INVENTORY_CLEAN_CHECKOUT") == "1" {
		t.Skip("clean-checkout child process")
	}
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
	command := exec.Command("go", "test", "./internal/launcher", "-run", "^TestSpikeDependencyEvidenceUsesRuntimeAndTestClosures$", "-count=1")
	command.Dir = clean
	command.Env = append(os.Environ(),
		"CONCORD_DEPENDENCY_INVENTORY_CLEAN_CHECKOUT=1",
		"GOTOOLCHAIN=local",
		"GOPROXY=off",
		"GOSUMDB=off",
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("inventory test failed in clean checkout without git: %v\n%s", err, output)
	}
}
