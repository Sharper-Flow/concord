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

type eventLogAllowance struct {
	path, function, migration string
}

// These functions are migration-only, not file-wide exceptions. applyMigration
// owns the backfill entry points; their nested callers are named beside them.
var eventLogMigrationAllowances = []eventLogAllowance{
	{"internal/store/effect_receipts.go", "backfillEffectReceiptDigests", "145/151: applyMigration / retireEventLog"},
	{"internal/store/event_log_retirement.go", "retireEventLog", "151: applyMigration"},
	{"internal/store/review_migration.go", "readLegacyReviewRecords", "148: backfillWorkflowReviewFacts"},
	{"internal/store/work_context_facts.go", "backfillWorkContextFacts", "148: applyMigration"},
	{"internal/store/workflow_admission_migration.go", "workflowAdmissionFactsMigrationReady", "149: migrations.Applies"},
	{"internal/store/workflow_correction_facts.go", "backfillWorkflowCorrectionFacts", "142: applyMigration"},
	{"internal/store/workflow_delivery_receipts.go", "backfillWorkflowDeliveryAssertionReceipts", "147: applyMigration"},
	{"internal/store/workflow_healthy_frontier.go", "backfillWorkflowHealthySequences", "142: applyMigration"},
}

func TestEventLogReferences(t *testing.T) {
	t.Parallel()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	findings, analyzed := scanEventLogReferences(root, eventLogMigrationAllowances)
	if analyzed < 30 {
		t.Fatalf("event log analysis reached only %d production Go files; discovery is broken", analyzed)
	}
	for _, finding := range findings {
		t.Error(finding)
	}
	t.Logf("scanned %d production Go files with %d exact migration function allowances", analyzed, len(eventLogMigrationAllowances))
}

// Walk source rather than build-selected packages: an inactive build tag must
// not hide a runtime dependency. Only Go test files and testdata are out of scope.
func scanEventLogReferences(root string, allowances []eventLogAllowance) ([]string, int) {
	var findings []string
	seen := make(map[eventLogAllowance]bool)
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
			permitted := eventLogManifestLiterals(rel, file)
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Body != nil {
					for _, allowance := range allowances {
						if allowance.path != rel || allowance.function != fn.Name.Name {
							continue
						}
						ast.Inspect(fn.Body, func(node ast.Node) bool {
							if literal, ok := node.(*ast.BasicLit); ok && eventLogLiteralNamesTable(literal) {
								permitted[literal] = true
								seen[allowance] = true
							}
							return true
						})
					}
				}
			}
			ast.Inspect(file, func(node ast.Node) bool {
				if literal, ok := node.(*ast.BasicLit); ok && eventLogLiteralNamesTable(literal) && !permitted[literal] {
					findings = append(findings, fmt.Sprintf("%s:%d: domain_events reference outside an exact migration allowance", rel, fset.Position(literal.Pos()).Line))
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
	for _, allowance := range allowances {
		if !seen[allowance] {
			findings = append(findings, fmt.Sprintf("%s: stale migration allowance for %s (%s): function no longer names domain_events", allowance.path, allowance.function, allowance.migration))
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

// The manifest is the only package-level exception. Admit direct SQL literals
// in entries 1..150 and the explicit 151 retirement DDL, not arbitrary fields,
// function literals, other declarations, or future migration entries.
func eventLogManifestLiterals(path string, file *ast.File) map[*ast.BasicLit]bool {
	permitted := make(map[*ast.BasicLit]bool)
	if path != "internal/store/schema.go" {
		return permitted
	}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || value.Names[0].Name != "migrations" || len(value.Values) != 1 {
				continue
			}
			list, ok := value.Values[0].(*ast.CompositeLit)
			if !ok {
				continue
			}
			slice, ok := list.Type.(*ast.ArrayType)
			if !ok || slice.Len != nil {
				continue
			}
			element, ok := slice.Elt.(*ast.Ident)
			if !ok || element.Name != "migration" {
				continue
			}
			for _, entry := range list.Elts {
				step, ok := entry.(*ast.CompositeLit)
				if !ok {
					continue
				}
				fields := make(map[string]*ast.BasicLit)
				for _, field := range step.Elts {
					pair, ok := field.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := pair.Key.(*ast.Ident)
					if ok {
						fields[key.Name], _ = pair.Value.(*ast.BasicLit)
					}
				}
				version := fields["Version"]
				if version == nil || version.Kind != token.INT {
					continue
				}
				number, err := strconv.Atoi(version.Value)
				if err != nil {
					continue
				}
				name := ""
				if literal := fields["Name"]; literal != nil {
					name, _ = strconv.Unquote(literal.Value)
				}
				if (number >= 1 && number <= 150) || (number == 151 && name == "event_log_retirement") {
					if sql := fields["SQL"]; sql != nil && sql.Kind == token.STRING {
						permitted[sql] = true
					}
				}
				if number == 1 && name == "domain_events" {
					permitted[fields["Name"]] = true
				}
			}
		}
	}
	return permitted
}

func TestEventLogReferencePropertiesBite(t *testing.T) {
	t.Parallel()
	const allowedPath = "internal/store/work_context_facts.go"
	allowance := eventLogAllowance{allowedPath, "backfillWorkContextFacts", "148: applyMigration"}
	const allowed = `package store; func backfillWorkContextFacts() { _ = "SELECT * FROM domain_events" }`
	const manifest = `package store; var migrations = []migration{{Version: 1, Name: "domain_events", SQL: "CREATE TABLE domain_events"}, {Version: 150, SQL: "SELECT * FROM domain_events"}, {Version: 151, Name: "event_log_retirement", SQL: "DROP TABLE domain_events"}}`
	tests := []struct {
		name, path, source, want string
		allowances               []eventLogAllowance
		missing                  string
	}{
		{name: "live reference", path: "internal/live.go", source: `package live; func read() { _ = "SELECT * FROM domain_events" }`, want: "internal/live.go:1: domain_events reference"},
		{name: "runtime in allowed file", path: allowedPath, source: allowed + `; func live() { _ = "domain_events" }`, allowances: []eventLogAllowance{allowance}, want: allowedPath + ":1: domain_events reference"},
		{name: "package reference in allowed file", path: allowedPath, source: allowed + `; var live = "domain_events"`, allowances: []eventLogAllowance{allowance}, want: allowedPath + ":1: domain_events reference"},
		{name: "stale allowance", path: allowedPath, source: `package store; func backfillWorkContextFacts() {}`, allowances: []eventLogAllowance{allowance}, want: "stale migration allowance"},
		{name: "bodyless allowed function", path: allowedPath, source: `package store; func backfillWorkContextFacts()`, allowances: []eventLogAllowance{allowance}, want: "stale migration allowance"},
		{name: "method is not allowed function", path: allowedPath, source: allowed + `; type store struct{}; func (store) backfillWorkContextFacts() { _ = "domain_events" }`, allowances: []eventLogAllowance{allowance}, want: "domain_events reference"},
		{name: "missing allowed function", allowances: []eventLogAllowance{allowance}, want: "stale migration allowance"},
		{name: "permitted function", path: allowedPath, source: allowed, allowances: []eventLogAllowance{allowance}},
		{name: "permitted manifest", path: "internal/store/schema.go", source: manifest},
		{name: "runtime in schema", path: "internal/store/schema.go", source: manifest + `; func live() { _ = "domain_events" }`, want: "domain_events reference"},
		{name: "package reference in schema", path: "internal/store/schema.go", source: manifest + `; const live = "domain_events"`, want: "domain_events reference"},
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
			findings, _ := scanEventLogReferences(root, tt.allowances)
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
