package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sharper-flow/concord/internal/testenv"
)

func TestExtractEarlyDispatch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		source  string
		want    []string
		wantErr bool
	}{
		{"guarded route", "package fixture\nfunc run(args []string) {\n\tif len(args) > 0 && args[0] == \"repair\" {\n\t\t_ = args\n\t}\n}", []string{"repair"}, false},
		{"literal first", "package fixture\nfunc run(args []string) {\n\tif len(args) > 0 && \"zl\" == args[0] {\n\t\t_ = args\n\t}\n}", []string{"zl"}, false},
		{"guard after comparison", "package fixture\nfunc run(args []string) {\n\tif args[0] == \"ci-wait\" && len(args) > 0 {\n\t\t_ = args\n\t}\n}", []string{"ci-wait"}, false},
		{"two routes", "package fixture\nfunc run(args []string) {\n\tif len(args) > 0 && args[0] == \"session\" {\n\t\t_ = args\n\t}\n\tif len(args) > 0 && args[0] == \"launcher\" {\n\t\t_ = args\n\t}\n}", []string{"launcher", "session"}, false},
		{"exact-length flag is not a command", "package fixture\nfunc run(args []string) {\n\tif len(args) == 1 && args[0] == \"--version\" {\n\t\t_ = args\n\t}\n}", nil, false},
		{"unguarded comparison", "package fixture\nfunc run(args []string) {\n\tif args[0] == \"solo\" {\n\t\t_ = args\n\t}\n}", nil, false},
		{"switch case", "package fixture\nfunc run(args []string) {\n\tswitch {\n\tcase len(args) == 1 && args[0] == \"--list\":\n\t\t_ = args\n\t}\n}", nil, false},
		{"index one is not a command", "package fixture\nfunc run(args []string) {\n\tif len(args) > 1 && args[1] == \"nested\" {\n\t\t_ = args\n\t}\n}", nil, false},
		{"duplicate token", "package fixture\nfunc run(args []string) {\n\tif len(args) > 0 && args[0] == \"repair\" {\n\t\t_ = args\n\t}\n\tif len(args) > 0 && args[0] == \"repair\" {\n\t\t_ = args\n\t}\n}", nil, true},
		{"missing file", "", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "commands.go")
			if err := os.WriteFile(path, []byte(tc.source), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := extractEarlyDispatch(path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("unsupported source accepted: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("extractEarlyDispatch = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("extractEarlyDispatch = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("extractEarlyDispatch = %+v, want %+v", got, tc.want)
				}
			}
		})
	}
}

func TestEarlyDispatchSurfaceOfConcordMain(t *testing.T) {
	path := filepath.Join("..", "..", "cmd", "concord", "main.go")
	got, err := extractEarlyDispatch(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ci-wait", "continuity-block", "host-lease", "host-leases", "launcher",
		"recover-fold-guard", "repair", "session", "upgrade", "zl"}
	if len(got) != len(want) {
		t.Fatalf("early dispatch surface = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("early dispatch surface = %+v, want %+v", got, want)
		}
	}
}

func TestExtractImports(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sources map[string]string
		files   []string
		want    map[string]fileImports
		wantErr bool
	}{
		{
			name:  "plain imports",
			files: []string{"a.go"},
			sources: map[string]string{
				"a.go": "package fixture\n\nimport (\n\t\"fmt\"\n\t\"example.invalid/lib\"\n)\n",
			},
			want: map[string]fileImports{"a.go": {Package: "fixture", Imports: []string{"example.invalid/lib", "fmt"}}},
		},
		{
			name:  "named blank and dotted imports stay package-level",
			files: []string{"a.go"},
			sources: map[string]string{
				"a.go": "package fixture\n\nimport (\n\t_ \"example.invalid/side\"\n\t. \"example.invalid/dot\"\n\talias \"example.invalid/alias\"\n)\n",
			},
			want: map[string]fileImports{"a.go": {Package: "fixture", Imports: []string{"example.invalid/alias", "example.invalid/dot", "example.invalid/side"}}},
		},
		{
			name:    "no imports",
			files:   []string{"a.go"},
			sources: map[string]string{"a.go": "package fixture\n"},
			want:    map[string]fileImports{"a.go": {Package: "fixture", Imports: []string{}}},
		},
		{
			name:    "unparsable file refuses",
			files:   []string{"a.go"},
			sources: map[string]string{"a.go": "not go source"},
			wantErr: true,
		},
		{
			name:  "unselected Go file cannot break observation",
			files: []string{"a.go"},
			sources: map[string]string{
				"a.go":       "package fixture\n",
				"ignored.go": "not go source",
			},
			want: map[string]fileImports{"a.go": {Package: "fixture", Imports: []string{}}},
		},
		{
			name:    "missing root refuses",
			sources: nil,
			wantErr: true,
		},
		{
			name:    "missing selected file refuses",
			files:   []string{"missing.go"},
			wantErr: true,
		},
		{
			name:    "escaping selected file refuses",
			files:   []string{"../outside.go"},
			wantErr: true,
		},
		{
			name: "empty selected universe",
			want: map[string]fileImports{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for name, source := range tc.sources {
				if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "missing root refuses" {
				root = filepath.Join(root, "absent")
			}
			got, err := extractImports(root, tc.files)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("unsupported source accepted: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("extractImports = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("extractImports = %+v, want %+v", got, tc.want)
			}
			for path, want := range tc.want {
				got := got[path]
				if got.Package != want.Package || !slices.Equal(got.Imports, want.Imports) {
					t.Fatalf("extractImports[%s] = %+v, want %+v", path, got, want)
				}
			}
		})
	}
}

func TestImportsCoverConcordMain(t *testing.T) {
	got, err := extractImports(filepath.Join("..", ".."), []string{"cmd/concord/main.go"})
	if err != nil {
		t.Fatal(err)
	}
	main, ok := got["cmd/concord/main.go"]
	if !ok {
		t.Fatalf("cmd/concord/main.go missing from observed imports: %d files", len(got))
	}
	if main.Package != "main" {
		t.Fatalf("cmd/concord/main.go package = %q", main.Package)
	}
	found := false
	for _, path := range main.Imports {
		if path == "github.com/sharper-flow/concord/internal/store" {
			found = true
		}
	}
	if !found {
		t.Fatalf("cmd/concord/main.go does not observe internal/store: %+v", main.Imports)
	}
}

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
