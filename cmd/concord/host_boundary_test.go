package main

import (
	"encoding/json"
	"io"
	"os"
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
		err = decoder.Decode(&pkg)
		if err == io.EOF {
			return packages
		}
		if err != nil {
			t.Fatalf("decode go list %v: %v", args, err)
		}
		packages = append(packages, pkg)
	}
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

// multiplexerNeedles are assembled from fragments so this file does not match
// its own scan. "screen" is ambiguous with ordinary display terminology.
var multiplexerNeedles = []string{
	"zel" + "lij",
	"tm" + "ux",
	"wez" + "term",
	"byo" + "bu",
	"multi" + "plexer",
}

// TestLauncherAndCommandCarryNoMultiplexerKnowledge proves CD-0078 D1
// structurally over the command package.
func TestLauncherAndCommandCarryNoMultiplexerKnowledge(t *testing.T) {
	packages := listPackages(t, "./cmd/concord")
	if len(packages) == 0 {
		t.Fatal("go list returned no command packages")
	}
	scanned := 0
	for _, pkg := range packages {
		for _, path := range packageFiles(pkg) {
			if filepath.Base(path) == "host_boundary_test.go" {
				continue
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			scanned++
			lowered := strings.ToLower(string(body))
			for _, needle := range multiplexerNeedles {
				if strings.Contains(lowered, needle) {
					t.Errorf("%s names multiplexer %q; CD-0078 D1 places terminal placement in the host", path, needle)
				}
			}
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no files; the boundary proof would pass vacuously")
	}
}
