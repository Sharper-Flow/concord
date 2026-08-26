package store

// The store database boundary: raw SQLite reaches Go only inside this package
// (internal/pm1fixture aside). The invariant is enforced structurally, through
// the toolchain's own parser — the CD-0055 D2 authority class, not a textual
// scan. SQL string literals are inspected against the schema's CREATE TABLE
// vocabulary, which is the one part of the invariant no Go syntax node carries.
//
// Production code outside internal/store must not: import database/sql, name
// *sql.DB or *sql.Tx, call BeginTx, touch a .DB / .DatabaseForTesting member,
// or hold literal SQL aimed at a store-owned table. Inside internal/store, a
// Store.DB accessor is forbidden so the handle has no production escape hatch.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type boundaryFinding struct {
	Path    string
	Line    int
	Message string
}

func (f boundaryFinding) String() string {
	return f.Path + ":" + strconv.Itoa(f.Line) + ": " + f.Message
}

var (
	boundaryCreateTableRe = regexp.MustCompile(
		"(?i)\\bCREATE\\s+TABLE\\s+(?:IF\\s+NOT\\s+EXISTS\\s+)?(?:\"([^\"]+)\"|`([^`]+)`|([A-Za-z_][A-Za-z0-9_]*))",
	)
	boundarySQLOperationRe = regexp.MustCompile(`(?i)\b(SELECT|INSERT|UPDATE|DELETE|REPLACE)\b`)
	boundarySQLTableRefRe  = regexp.MustCompile(
		"(?i)\\b(?:FROM|JOIN|INTO|UPDATE|DELETE(?:\\s+FROM)?)\\s+(?:[A-Za-z_][A-Za-z0-9_]*\\.)?[\"`]?([A-Za-z_][A-Za-z0-9_]*)[\"`]?",
	)
)

// boundaryStoreTables derives the store-owned table vocabulary from schema.go
// string literals, lowercased.
func boundaryStoreTables(schemaSource string) map[string]bool {
	tables := map[string]bool{}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "schema.go", schemaSource, 0)
	if err != nil {
		return tables
	}
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		for _, match := range boundaryCreateTableRe.FindAllStringSubmatch(stripSQLComments(value), -1) {
			for _, group := range match[1:] {
				if group != "" {
					tables[strings.ToLower(group)] = true
				}
			}
		}
		return true
	})
	return tables
}

type boundaryScope int

const (
	boundaryScopeOutside boundaryScope = iota
	boundaryScopeStore
	boundaryScopeExempt
)

func boundaryScopeForPath(path string) (boundaryScope, bool) {
	if strings.HasSuffix(path, "_test.go") {
		return 0, false
	}
	switch {
	case strings.HasPrefix(path, "internal/store/"):
		return boundaryScopeStore, true
	case strings.HasPrefix(path, "internal/pm1fixture/"):
		return boundaryScopeExempt, true
	case strings.HasPrefix(path, "cmd/") || strings.HasPrefix(path, "internal/"):
		return boundaryScopeOutside, true
	}
	return 0, false
}

// scanBoundaryFiles checks every file in the map (path relative to the
// repository root) and returns findings for boundary violations.
func scanBoundaryFiles(files map[string]string, tables map[string]bool) []boundaryFinding {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var findings []boundaryFinding
	for _, path := range paths {
		scope, applies := boundaryScopeForPath(path)
		if !applies || scope == boundaryScopeExempt {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, files[path], 0)
		if err != nil {
			findings = append(findings, boundaryFinding{path, 1, "cannot parse: " + err.Error()})
			continue
		}
		if scope == boundaryScopeStore {
			findings = append(findings, scanStoreAccessor(path, file, fset)...)
			continue
		}
		findings = append(findings, scanOutsideFile(path, file, fset, tables)...)
	}
	return findings
}

// scanOutsideFile applies the production rules for files outside the store.
func scanOutsideFile(path string, file *ast.File, fset *token.FileSet, tables map[string]bool) []boundaryFinding {
	var findings []boundaryFinding

	// database/sql imports under any local name.
	sqlNames := map[string]bool{}
	for _, spec := range file.Imports {
		value, err := strconv.Unquote(spec.Path.Value)
		if err != nil || value != "database/sql" {
			continue
		}
		findings = append(findings, boundaryFinding{
			path, fset.Position(spec.Pos()).Line, "database/sql import outside internal/store",
		})
		name := "sql"
		if spec.Name != nil && spec.Name.Name != "_" && spec.Name.Name != "." {
			name = spec.Name.Name
		}
		sqlNames[name] = true
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SelectorExpr:
			ident, ok := node.X.(*ast.Ident)
			// The conventional database/sql binding is `sql`; aliased
			// bindings are tracked from the import specs. Naming the raw
			// handle type at all outside the store is the violation, with
			// or without the import in the same file.
			sqlBound := ok && (ident.Name == "sql" || sqlNames[ident.Name])
			if sqlBound && (node.Sel.Name == "DB" || node.Sel.Name == "Tx") {
				findings = append(findings, boundaryFinding{
					path, fset.Position(node.Pos()).Line,
					"raw *sql." + node.Sel.Name + " type outside internal/store",
				})
				return true
			}
			if !sqlBound && (node.Sel.Name == "DB" || node.Sel.Name == "DatabaseForTesting") {
				findings = append(findings, boundaryFinding{
					path, fset.Position(node.Pos()).Line,
					"raw database accessor identifier outside exempt paths",
				})
			}
		case *ast.CallExpr:
			name := ""
			switch fun := node.Fun.(type) {
			case *ast.Ident:
				name = fun.Name
			case *ast.SelectorExpr:
				name = fun.Sel.Name
			}
			if name == "BeginTx" {
				findings = append(findings, boundaryFinding{
					path, fset.Position(node.Pos()).Line,
					"direct BeginTx call outside internal/store",
				})
			}
		case *ast.BasicLit:
			if node.Kind != token.STRING {
				return true
			}
			findings = append(findings, scanSQLLiteral(path, node, fset, tables)...)
		}
		return true
	})
	return findings
}

// scanStoreAccessor forbids a Store.DB method inside the store: the raw
// handle has no escape hatch once typed store operations own every seam.
func scanStoreAccessor(path string, file *ast.File, fset *token.FileSet) []boundaryFinding {
	var findings []boundaryFinding
	for _, decl := range file.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok || funcDecl.Recv == nil || funcDecl.Name.Name != "DB" {
			continue
		}
		if len(funcDecl.Recv.List) == 0 {
			continue
		}
		recvType := funcDecl.Recv.List[0].Type
		star, isStar := recvType.(*ast.StarExpr)
		ident, isIdent := recvType.(*ast.Ident)
		if (isStar && isIdentName(star.X, "Store")) || (isIdent && ident.Name == "Store") {
			findings = append(findings, boundaryFinding{
				path, fset.Position(funcDecl.Pos()).Line, "Store.DB method must be removed",
			})
		}
	}
	return findings
}

func isIdentName(expr ast.Expr, name string) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == name
}

// scanSQLLiteral flags literal SQL aimed at store-owned tables. Statement
// bounds end at the first semicolon after the operation; table references
// after a JOIN or inside a subquery count as targets.
func scanSQLLiteral(path string, lit *ast.BasicLit, fset *token.FileSet, tables map[string]bool) []boundaryFinding {
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return nil
	}
	litLine := fset.Position(lit.Pos()).Line
	sql := stripSQLComments(value)

	var findings []boundaryFinding
	reported := map[[2]int]bool{}
	for _, operation := range boundarySQLOperationRe.FindAllStringSubmatchIndex(sql, -1) {
		operationStart, operationEnd := operation[0], operation[1]
		operationName := strings.ToUpper(sql[operationStart:operationEnd])
		statementEnd := strings.IndexByte(sql[operationEnd:], ';')
		if statementEnd < 0 {
			statementEnd = len(sql)
		} else {
			statementEnd += operationEnd
		}
		for _, table := range boundarySQLTableRefRe.FindAllStringSubmatchIndex(sql[operationStart:statementEnd], -1) {
			tableStart := operationStart + table[2]
			tableName := sql[tableStart : operationStart+table[3]]
			key := [2]int{tableStart, len(tableName)}
			if reported[key] {
				continue
			}
			reported[key] = true
			if !tables[strings.ToLower(tableName)] {
				continue
			}
			findings = append(findings, boundaryFinding{
				path, litLine + strings.Count(sql[:tableStart], "\n"),
				"literal SQL " + operationName + " targets store table " + tableName,
			})
		}
	}
	return findings
}

// stripSQLComments blanks SQL comments while preserving offsets and newlines
// so positions stay meaningful. Quote-aware for doubled ” / "" escapes.
func stripSQLComments(sql string) string {
	var out strings.Builder
	blank := func(section string) {
		for i := 0; i < len(section); i++ {
			if section[i] == '\n' {
				out.WriteByte('\n')
			} else {
				out.WriteByte(' ')
			}
		}
	}
	quote := byte(0)
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case quote != 0:
			out.WriteByte(c)
			if c == quote {
				if i+1 < len(sql) && sql[i+1] == quote {
					out.WriteByte(sql[i+1])
					i += 2
					continue
				}
				quote = 0
			}
			i++
		case c == '\'' || c == '"':
			quote = c
			out.WriteByte(c)
			i++
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			newline := strings.IndexByte(sql[i:], '\n')
			if newline < 0 {
				blank(sql[i:])
				i = len(sql)
				continue
			}
			blank(sql[i : i+newline])
			out.WriteByte('\n')
			i += newline + 1
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			end := strings.Index(sql[i:], "*/")
			if end < 0 {
				blank(sql[i:])
				i = len(sql)
				continue
			}
			blank(sql[i : i+end+2])
			i += end + 2
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String()
}

// TestStoreBoundaryRepoIsClean scans the real repository production tree and
// asserts the boundary holds everywhere.
func TestStoreBoundaryRepoIsClean(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))

	files := map[string]string{}
	for _, directory := range []string{filepath.Join(root, "cmd"), filepath.Join(root, "internal")} {
		err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files[filepath.ToSlash(relative)] = string(content)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", directory, err)
		}
	}

	schema, err := os.ReadFile(filepath.Join(root, "internal", "store", "schema.go"))
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	tables := boundaryStoreTables(string(schema))
	if len(tables) == 0 {
		t.Fatal("no store tables derived from schema.go")
	}

	for _, finding := range scanBoundaryFiles(files, tables) {
		t.Errorf("boundary violation: %s", finding)
	}
}

// TestStoreBoundaryFixtureViolations covers every violation shape: raw
// handles, aliased imports, accessors (including method values), direct
// BeginTx, literal SQL against owned tables through joins and subqueries, SQL
// comments, and the Store.DB escape hatch — plus the allowed scopes
// (store-owned code, tests, pm1fixture) staying silent.
func TestStoreBoundaryFixtureViolations(t *testing.T) {
	const schema = "package store\nvar schema = `CREATE TABLE widgets (id INTEGER);`\n"
	const bt = "`"
	tables := boundaryStoreTables(schema)

	cases := []struct {
		name     string
		files    map[string]string
		contains []string
		count    int
		line     int
	}{
		{
			name:     "raw sql.DB type",
			files:    map[string]string{"cmd/main.go": "package main\nvar db *sql.DB\n"},
			contains: []string{"raw *sql.DB type"},
			count:    1,
		},
		{
			name:     "raw sql.Tx type",
			files:    map[string]string{"internal/agent/use.go": "package agent\nvar tx *sql.Tx\n"},
			contains: []string{"raw *sql.Tx type"},
			count:    1,
		},
		{
			name: "database/sql import under alias",
			files: map[string]string{"cmd/main.go": "package main\n" +
				"import sqlpkg \"database/sql\"\n" +
				"func open() { _, _ = sqlpkg.Open(\"sqlite\", \"\") }\n"},
			contains: []string{"database/sql import"},
			count:    1,
		},
		{
			name:     "database accessor identifiers",
			files:    map[string]string{"internal/agent/use.go": "package agent\nfunc f(s *Store) { s.DB(); s.DatabaseForTesting() }\n"},
			contains: []string{"raw database accessor identifier"},
			count:    2,
		},
		{
			name:     "database accessor method value without call",
			files:    map[string]string{"cmd/main.go": "package main\nfunc f(s *Store) { get := s.DatabaseForTesting; _ = get }\n"},
			contains: []string{"raw database accessor identifier"},
			count:    1,
		},
		{
			name:     "direct BeginTx call",
			files:    map[string]string{"internal/agent/use.go": "package agent\nfunc f(db *sql.DB) { db.BeginTx(nil, nil) }\n"},
			contains: []string{"direct BeginTx"},
			count:    2, // raw *sql.DB type + BeginTx
		},
		{
			name: "literal SQL operations against owned table",
			files: map[string]string{"internal/agent/use.go": "package agent\n" +
				"var a = " + bt + "SELECT id FROM widgets" + bt + "\n" +
				"var b = " + bt + "INSERT INTO widgets(id) VALUES (1)" + bt + "\n" +
				"var c = " + bt + "UPDATE widgets SET id = 2" + bt + "\n" +
				"var d = " + bt + "DELETE FROM widgets WHERE id = 2" + bt + "\n" +
				"var e = " + bt + "REPLACE INTO widgets(id) VALUES (3)" + bt + "\n"},
			contains: []string{"literal SQL"},
			count:    5,
		},
		{
			name: "owned tables after joins and inside subqueries",
			files: map[string]string{"internal/agent/use.go": "package agent\n" +
				"var joined = " + bt + "SELECT other.id FROM other JOIN widgets ON widgets.id = other.id" + bt + "\n" +
				"var nested = " + bt + "UPDATE other SET id = (SELECT max(id) FROM widgets)" + bt + "\n"},
			contains: []string{"literal SQL"},
			count:    2,
		},
		{
			name: "multiline SQL with SQL comments reports the table line",
			files: map[string]string{"cmd/main.go": "package agent\n" +
				"var query = " + bt + "SELECT id\n" +
				"-- SELECT id FROM widgets\n" +
				"FROM\n" +
				"  widgets" + bt + "\n" +
				"var prose = " + bt + "/* INSERT INTO widgets(id) VALUES (1) */" + bt + "\n"},
			contains: []string{"literal SQL SELECT targets store table widgets"},
			count:    1,
			line:     5,
		},
		{
			name:  "store-owned code is allowed",
			files: map[string]string{"internal/store/owned.go": "package store\nimport \"database/sql\"\nfunc owned(db *sql.DB) { db.BeginTx(nil, nil); _ = db.Query(`SELECT id FROM widgets`) }\n"},
			count: 0,
		},
		{
			name:     "Store.DB method is forbidden",
			files:    map[string]string{"internal/store/owned.go": "package store\nfunc (s *Store) DB() *sql.DB { return nil }\n"},
			contains: []string{"Store.DB method"},
			count:    1,
		},
		{
			name:  "test files are allowed",
			files: map[string]string{"internal/agent/use_test.go": "package agent\nfunc testOnly(db *sql.DB) { db.BeginTx(nil, nil); _ = db.Query(`SELECT id FROM widgets`) }\n"},
			count: 0,
		},
		{
			name:  "pm1fixture is allowed",
			files: map[string]string{"internal/pm1fixture/fixture.go": "package pm1fixture\nfunc fixture(db *sql.DB) { db.BeginTx(nil, nil); _ = db.Query(`SELECT id FROM widgets`) }\n"},
			count: 0,
		},
		{
			name: "go comments and prose strings do not trip structural rules",
			files: map[string]string{"internal/agent/use.go": "package agent\n" +
				"// *sql.DB; *sql.Tx; s.DB(); s.DatabaseForTesting(); db.BeginTx(nil, nil)\n" +
				"/* SELECT id FROM widgets; INSERT INTO widgets(id) VALUES (1) */\n" +
				"var prose = \"not code: *sql.DB *sql.Tx .DB() BeginTx(\"\n"},
			count: 0,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			findings := scanBoundaryFiles(testCase.files, tables)
			if len(findings) != testCase.count {
				t.Fatalf("want %d findings, got %d: %v", testCase.count, len(findings), findings)
			}
			for _, needle := range testCase.contains {
				if !containsMessage(findings, needle) {
					t.Fatalf("no finding contains %q: %v", needle, findings)
				}
			}
			if testCase.line != 0 {
				for _, finding := range findings {
					if strings.Contains(finding.Message, "literal SQL") && finding.Line != testCase.line {
						t.Fatalf("literal SQL finding on line %d, want %d", finding.Line, testCase.line)
					}
				}
			}
		})
	}
}

func containsMessage(findings []boundaryFinding, needle string) bool {
	for _, finding := range findings {
		if strings.Contains(finding.Message, needle) {
			return true
		}
	}
	return false
}
