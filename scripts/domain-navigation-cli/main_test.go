package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/testenv"
)

func TestMain(m *testing.M) {
	dir := testenv.ScrubEnv()
	code := m.Run()
	os.Exit(testenv.Cleanup(dir, code))
}

func TestExtractCommandSpecs(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		want         int
	}{
		{"canonical and alias", "package fixture; var commandSpecs = []commandSpec{{Canonical: \"product-create\", TwoWord: \"product create\", Optional: \"ignored\"}}", 1},
		{"multiline and comment", "package fixture\n// Canonical: \"not a declaration\"\nvar commandSpecs = []commandSpec{\n{Canonical:\n`read`,\n},\n}", 1},
		{"missing", "package fixture; var unrelated = 1", 0},
		{"empty", "package fixture; var commandSpecs = []commandSpec{}", 0},
		{"nonliteral", "package fixture; var commandSpecs = makeCommands()", 0},
		{"computed verb", "package fixture; var commandSpecs = []commandSpec{{Canonical: compute()}}", 0},
		{"duplicate", "package fixture; var commandSpecs = []commandSpec{{Canonical: \"read\"}, {Canonical: \"read\"}}", 0},
		{"empty verb", "package fixture; var commandSpecs = []commandSpec{{TwoWord: \"product create\"}}", 0},
		{"unkeyed", "package fixture; var commandSpecs = []commandSpec{{\"read\"}}", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "commands.go")
			if err := os.WriteFile(path, []byte(tc.source), 0600); err != nil {
				t.Fatal(err)
			}
			verbs, err := extract(path)
			if tc.want == 0 {
				if err == nil {
					t.Fatalf("unsupported source accepted: %+v", verbs)
				}
				return
			}
			if err != nil || len(verbs) != tc.want {
				t.Fatalf("extract = %+v, %v", verbs, err)
			}
			if tc.name == "canonical and alias" && (verbs[0].Canonical != "product-create" || verbs[0].TwoWord != "product create") {
				t.Fatalf("verb identity lost: %+v", verbs[0])
			}
		})
	}
}
