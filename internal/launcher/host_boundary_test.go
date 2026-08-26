package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// multiplexerNeedles are assembled from fragments so this file does not match
// its own scan. CD-0078 D1 forbids multiplexer knowledge in the launcher and
// the command package; "screen" is deliberately absent because Screen is a
// launcher domain type.
var multiplexerNeedles = []string{
	"zel" + "lij",
	"tm" + "ux",
	"wez" + "term",
	"byo" + "bu",
	"multi" + "plexer",
}

// TestLauncherAndCommandCarryNoMultiplexerKnowledge proves CD-0078 D1
// structurally. Terminal placement belongs to the host, so no launcher or
// command source file may name a multiplexer.
func TestLauncherAndCommandCarryNoMultiplexerKnowledge(t *testing.T) {
	packages := listPackages(t, "./internal/launcher/...", "./cmd/concord")
	if len(packages) == 0 {
		t.Fatal("go list returned no launcher or command packages")
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
