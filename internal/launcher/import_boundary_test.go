package launcher

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type listedPackage struct {
	ImportPath   string
	Dir          string
	GoFiles      []string
	CgoFiles     []string
	TestGoFiles  []string
	XTestGoFiles []string
}

func listPackages(t *testing.T, args ...string) []listedPackage {
	t.Helper()
	command := exec.Command("go", append([]string{"list", "-json"}, args...)...)
	command.Dir = filepath.Join("..", "..")
	out, err := command.Output()
	if err != nil {
		t.Fatalf("go list %v: %v", args, err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(out)))
	var packages []listedPackage
	for {
		var pkg listedPackage
		err := decoder.Decode(&pkg)
		if err == io.EOF {
			return packages
		}
		if err != nil {
			t.Fatalf("decode go list %v: %v", args, err)
		}
		packages = append(packages, pkg)
	}
}

func corePackages(t *testing.T) []listedPackage {
	t.Helper()
	var core []listedPackage
	for _, pkg := range listPackages(t, "./internal/launcher/...") {
		if pkg.ImportPath == "github.com/sharper-flow/concord/internal/launcher/render/bubbletea" || strings.Contains(pkg.ImportPath, "/render/bubbletea/") {
			continue
		}
		core = append(core, pkg)
	}
	return core
}

func packageFiles(pkg listedPackage) []string {
	files := append([]string{}, pkg.GoFiles...)
	files = append(files, pkg.CgoFiles...)
	files = append(files, pkg.TestGoFiles...)
	files = append(files, pkg.XTestGoFiles...)
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, filepath.Join(pkg.Dir, file))
	}
	return paths
}

func isCharmPath(path string) bool {
	return strings.HasPrefix(path, "charm.land/") || strings.HasPrefix(path, "github.com/charmbracelet/")
}

func TestLauncherCoreBoundaryUsesASTAndGoList(t *testing.T) {
	core := corePackages(t)
	if len(core) == 0 {
		t.Fatal("go list returned no launcher core packages")
	}
	corePaths := make([]string, 0, len(core))
	for _, pkg := range core {
		corePaths = append(corePaths, pkg.ImportPath)
		for _, file := range packageFiles(pkg) {
			astFile, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parse %s: %v", file, err)
			}
			for _, imported := range astFile.Imports {
				path := strings.Trim(imported.Path.Value, `"`)
				if isCharmPath(path) {
					t.Errorf("core file %s directly imports renderer dependency %s", file, path)
				}
			}
		}
	}
	command := exec.Command("go", append([]string{"list", "-deps", "-json"}, corePaths...)...)
	command.Dir = filepath.Join("..", "..")
	depsOut, err := command.Output()
	if err != nil {
		t.Fatalf("go list core dependency closure: %v", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(depsOut)))
	for {
		var pkg listedPackage
		err := decoder.Decode(&pkg)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if isCharmPath(pkg.ImportPath) {
			t.Errorf("core dependency closure reaches Charm package %s", pkg.ImportPath)
		}
	}
}

func TestRendererClosureExcludesStoreDomainAndQuery(t *testing.T) {
	packages := listPackages(t, "-deps", "./internal/launcher/render/bubbletea")
	for _, pkg := range packages {
		path := pkg.ImportPath
		if path == "github.com/sharper-flow/concord/internal/store" || strings.Contains(path, "/internal/domain/") || strings.Contains(path, "/internal/query/") {
			t.Errorf("renderer closure reaches forbidden domain package %s", path)
		}
	}
}
