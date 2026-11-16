package store

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// No production function may name the retired event log: the migration
// callbacks that read it are removed with the history below the supported
// store floor (CD-0082 D4).
func TestEventLogReferences(t *testing.T) {
	t.Parallel()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	findings, analyzed := scanEventLogReferences(root)
	if analyzed < 30 {
		t.Fatalf("event log analysis reached only %d production Go files; discovery is broken", analyzed)
	}
	for _, finding := range findings {
		t.Error(finding)
	}
	t.Logf("scanned %d production Go files", analyzed)
}

// Walk source rather than build-selected packages: an inactive build tag must
// not hide a runtime dependency. Only Go test files and testdata are out of scope.
func scanEventLogReferences(root string) ([]string, int) {
	var findings []string
	analyzed := 0
	for _, scope := range []string{"cmd", "internal"} {
		count := 0
		err := filepath.WalkDir(filepath.Join(root, scope), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			count++
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				findings = append(findings, rel+": cannot parse: "+err.Error())
				return nil
			}
			ast.Inspect(file, func(node ast.Node) bool {
				if literal, ok := node.(*ast.BasicLit); ok && eventLogLiteralNamesTable(literal) {
					findings = append(findings, fmt.Sprintf("%s:%d: domain_events reference", rel, fset.Position(literal.Pos()).Line))
				}
				return true
			})
			return nil
		})
		analyzed += count
		if err != nil {
			findings = append(findings, scope+": cannot scan production scope: "+err.Error())
		} else if count == 0 {
			findings = append(findings, scope+": no production Go files scanned")
		}
	}
	sort.Strings(findings)
	return findings, analyzed
}

func eventLogLiteralNamesTable(literal *ast.BasicLit) bool {
	if literal.Kind != token.STRING {
		return false
	}
	value, err := strconv.Unquote(literal.Value)
	return err == nil && strings.Contains(strings.ToLower(value), "domain_events")
}

func TestEventLogReferencePropertiesBite(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, path, source, want string
		missing                  string
	}{
		{name: "live reference", path: "internal/live.go", source: `package live; func read() { _ = "SELECT * FROM domain_events" }`, want: "internal/live.go:1: domain_events reference"},
		{name: "retired manifest reference", path: "internal/store/schema.go", source: `package store; var migrations = []migration{{Version: 150, SQL: "SELECT * FROM domain_events"}}`, want: "domain_events reference"},
		{name: "runtime in schema", path: "internal/store/schema.go", source: `package store; func live() { _ = "domain_events" }`, want: "domain_events reference"},
		{name: "package reference in schema", path: "internal/store/schema.go", source: `package store; const live = "domain_events"`, want: "domain_events reference"},
		{name: "future migration", path: "internal/store/schema.go", source: `package store; var migrations = []migration{{Version: 152, SQL: "domain_events"}}`, want: "domain_events reference"},
		{name: "wrong retirement entry", path: "internal/store/schema.go", source: `package store; var migrations = []migration{{Version: 151, Name: "other", SQL: "domain_events"}}`, want: "domain_events reference"},
		{name: "manifest closure", path: "internal/store/schema.go", source: `package store; var migrations = []migration{{Version: 150, Applies: func() { _ = "domain_events" }}}`, want: "domain_events reference"},
		{name: "nested cmd package", path: "cmd/tool/nested/live.go", source: `package live; var table = "domain_events"`, want: "cmd/tool/nested/live.go:1: domain_events reference"},
		{name: "inactive build tag", path: "internal/other_windows.go", source: "//go:build windows\n\npackage other; var table = \"domain_events\"", want: "internal/other_windows.go:3: domain_events reference"},
		{name: "escaped mixed case", path: "internal/live.go", source: `package live; var table = "DOMAIN_\x65VENTS"`, want: "domain_events reference"},
		{name: "migration filename is not authority", path: "internal/other_migration.go", source: `package other; var table = "domain_events"`, want: "domain_events reference"},
		{name: "test source excluded", path: "internal/live_test.go", source: `package live; var table = "domain_events"`},
		{name: "nested testdata excluded", path: "internal/store/testdata/nested/live.go", source: "deliberately invalid Go domain_events"},
		{name: "comments excluded", path: "internal/live.go", source: "package live\n// domain_events\n"},
		{name: "parse failure", path: "internal/live.go", source: "not Go", want: "cannot parse"},
		{name: "empty scope", missing: "empty cmd", want: "cmd: no production Go files scanned"},
		{name: "missing scope", missing: "cmd", want: "cmd: cannot scan production scope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for _, scope := range []string{"cmd", "internal"} {
				if tt.missing == scope {
					continue
				}
				dir := filepath.Join(root, scope)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if tt.missing != "empty "+scope {
					if err := os.WriteFile(filepath.Join(dir, "scope.go"), []byte("package scope"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tt.path != "" {
				path := filepath.Join(root, filepath.FromSlash(tt.path))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tt.source), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			findings, _ := scanEventLogReferences(root)
			if tt.want == "" {
				if len(findings) != 0 {
					t.Fatalf("permitted source rejected: %v", findings)
				}
			} else if len(findings) != 1 || !strings.Contains(findings[0], tt.want) {
				t.Fatalf("want exactly one finding containing %q; got %v", tt.want, findings)
			}
		})
	}
}
