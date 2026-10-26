package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type txScopeFinding struct {
	Path       string
	Line       int
	Identifier string
	Property   int
	Message    string
}

func (f txScopeFinding) String() string {
	return f.Path + ":" + strconv.Itoa(f.Line) + ": " + f.Identifier + " (property " + strconv.Itoa(f.Property) + "): " + f.Message
}

type txScopeParam struct {
	name  string
	kind  string
	ident *ast.Ident
}

const (
	txScopeQueryer = "queryer"
	txScopeStore   = "store"
	txScopeTx      = "transaction"
)

func TestTxScope(t *testing.T) {
	t.Parallel()
	root := txScopeRepoRoot()
	findings, analyzed := scanTxScope(root)
	// A scan that reaches no files reports no findings, which would let this
	// assertion pass while proving nothing. The package holds dozens of
	// non-test files; a count this low means discovery broke, not that the
	// package shrank.
	if analyzed < 30 {
		t.Fatalf("transaction scope analysis reached %d files in internal/store and internal/store/internal/sqlitedb; discovery is broken", analyzed)
	}
	for _, finding := range findings {
		t.Errorf("%s", finding)
	}
}

func TestTxScopePropertiesBite(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		property int
		source   string
	}{
		{"transaction handle and store", 1, `package store; func violates(q queryer, s *Store) { _ = q }`},
		{"multi-statement transaction closure", 2, `package store; func violates(s *Store) { s.Transact(nil, func(tx *Transaction) error { x := 1; _ = x; return nil }) }`},
		{"read snapshot captures store", 2, `package store; func violates(s *Store) { s.readSnapshot(nil, func(q queryer) error { return readThroughStore(s) }) }`},
		{"read transaction captures store", 2, `package store; func violates(s *Store) { s.ReadTransaction(nil, func(tx *Transaction) error { return readThroughStore(s) }) }`},
		{"queryer nil comparison", 3, `package store; func violates(q queryer) { if q == nil { return } }`},
		{"query under open cursor", 4, `package store; func violates(q queryer) { rows, _ := q.QueryContext(nil, ""); for rows.Next() { helper(q) } }`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, tt.name+".go", tt.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			findings := scanTxScopeFile(tt.name+".go", file, fset)
			if len(findings) != 1 || findings[0].Property != tt.property {
				t.Fatalf("property %d self-test found %d findings: %v", tt.property, len(findings), findings)
			}
		})
	}
}

func txScopeRepoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}

func TestTxScopeDurableTypesBite(t *testing.T) {
	t.Parallel()
	for _, handle := range []string{"Queryer", "WriteTx", "*WriteTx", "sqlitedb.Queryer", "sqlitedb.WriteTx", "*sqlitedb.WriteTx", "durable.Queryer", "*durable.WriteTx", "*dbsql.Tx"} {
		t.Run(handle, func(t *testing.T) {
			prefix := `package sqlitedb; import ("github.com/sharper-flow/concord/internal/store/internal/sqlitedb"; durable "github.com/sharper-flow/concord/internal/store/internal/sqlitedb"; dbsql "database/sql"); `
			cases := []struct {
				property int
				body     string
			}{
				{1, `func violates(q ` + handle + `, s *Store) { _ = q }`},
				{4, `func violates(q ` + handle + `) { rows, _ := q.QueryContext(nil, ""); for rows.Next() { helper(q) } }`},
			}
			if strings.HasSuffix(handle, "Queryer") {
				cases = append(cases, struct {
					property int
					body     string
				}{3, `func violates(q ` + handle + `) { if nil == q { return } }`})
			}
			for _, fixture := range cases {
				fset := token.NewFileSet()
				file, err := parser.ParseFile(fset, "durable.go", prefix+fixture.body, 0)
				if err != nil {
					t.Fatal(err)
				}
				findings := scanTxScopeFile("internal/store/internal/sqlitedb/durable.go", file, fset)
				if len(findings) != 1 || findings[0].Property != fixture.property {
					t.Fatalf("property %d: want one finding, got %v", fixture.property, findings)
				}
			}
		})
	}
}

func TestTxScopeDurableDiscovery(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for path, source := range map[string]string{
		"internal/store/parent.go":                        `package store; func bad(q queryer) { if q == nil {} }`,
		"internal/store/internal/sqlitedb/durable.go":     `package sqlitedb; func bad(q Queryer) { if q == nil {} }`,
		"internal/store/internal/sqlitedb/ignore_test.go": `this is not production Go`,
	} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	findings, analyzed := scanTxScope(root)
	if analyzed != 2 || len(findings) != 2 {
		t.Fatalf("want two production files and two findings, got %d, %v", analyzed, findings)
	}
	want := []string{"internal/store/internal/sqlitedb/durable.go", "internal/store/parent.go"}
	for i, finding := range findings {
		if finding.Path != want[i] || finding.Property != 3 {
			t.Fatalf("discovery finding %d: got %v, want path %s/property 3", i, finding, want[i])
		}
	}
}

func TestTxScopeUnrelatedQualifiedTypes(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "other.go", `package store; import sqlitedb "example.com/other"; func unrelated(q sqlitedb.Queryer, w *sqlitedb.WriteTx, s *Store) { if q == nil {}; _ = w; _ = s }`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if findings := scanTxScopeFile("internal/store/other.go", file, fset); len(findings) != 0 {
		t.Fatalf("unrelated import treated as durable handles: %v", findings)
	}
}

func TestTxScopeDurablePoolBites(t *testing.T) {
	t.Parallel()
	for _, source := range []string{
		`package sqlitedb; import "database/sql"; func violates(tx *WriteTx, db *sql.DB) { _ = tx }`,
		`package sqlitedb; import dbsql "database/sql"; func violates(q Queryer, db *dbsql.DB) { _ = q }`,
		`package sqlitedb; import "database/sql"; func violates(g *WriteGuard, db *sql.DB) { _ = g }`,
		`package sqlitedb; import "database/sql"; func (g WriteGuard) violates(db *sql.DB) { _ = g }`,
		`package store; import ("database/sql"; "github.com/sharper-flow/concord/internal/store/internal/sqlitedb"); func violates(g sqlitedb.WriteGuard, db *sql.DB) { _ = g }`,
		`package store; import ("database/sql"; durable "github.com/sharper-flow/concord/internal/store/internal/sqlitedb"); func violates(g *durable.WriteGuard, db *sql.DB) { _ = g }`,
	} {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "pool.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		findings := scanTxScopeFile("internal/store/internal/sqlitedb/pool.go", file, fset)
		if len(findings) != 1 || findings[0].Property != 1 {
			t.Fatalf("live durable handle plus pool must refuse: %v", findings)
		}
	}
}

func scanTxScope(root string) ([]txScopeFinding, int) {
	var paths []string
	var findings []txScopeFinding
	for _, dir := range []string{"internal/store", "internal/store/internal/sqlitedb"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			findings = append(findings, txScopeFinding{Path: dir, Line: 1, Identifier: dir, Property: 0, Message: "cannot read package: " + err.Error()})
			continue
		}
		before := len(paths)
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			paths = append(paths, filepath.Join(root, dir, entry.Name()))
		}
		if len(paths) == before {
			findings = append(findings, txScopeFinding{Path: dir, Line: 1, Identifier: dir, Property: 0, Message: "no production files discovered"})
		}
	}
	sort.Strings(paths)

	for _, path := range paths {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			panic(err)
		}
		rel = filepath.ToSlash(rel)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			findings = append(findings, txScopeFinding{Path: rel, Line: 1, Identifier: filepath.Base(path), Property: 0, Message: "cannot parse: " + err.Error()})
			continue
		}
		findings = append(findings, scanTxScopeFile(rel, file, fset)...)
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Path != findings[j].Path {
			return findings[i].Path < findings[j].Path
		}
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Property < findings[j].Property
	})
	return findings, len(paths)
}

func scanTxScopeFile(path string, file *ast.File, fset *token.FileSet) []txScopeFinding {
	var findings []txScopeFinding
	aliases := txSubprocessImportAliases(file)
	var inspect func(ast.Node, []string)
	inspect = func(node ast.Node, enclosingStores []string) {
		if node == nil {
			return
		}
		switch n := node.(type) {
		case *ast.FuncDecl:
			params := txScopeParams(n.Recv, n.Type.Params, aliases)
			findings = append(findings, scanTxScopeFunction(path, n.Body, params, fset)...)
			stores := txScopeStoreNames(params)
			if n.Body != nil {
				for _, decl := range n.Body.List {
					inspect(decl, stores)
				}
			}
			return
		case *ast.FuncLit:
			params := txScopeParams(nil, n.Type.Params, aliases)
			findings = append(findings, scanTxScopeFunction(path, n.Body, params, fset)...)
			stores := append(append([]string(nil), enclosingStores...), txScopeStoreNames(params)...)
			for _, decl := range n.Body.List {
				inspect(decl, stores)
			}
			return
		case *ast.CallExpr:
			if txScopeIsTransactCall(n) {
				for _, arg := range n.Args {
					if lit, ok := arg.(*ast.FuncLit); ok {
						findings = append(findings, scanTxScopeClosure(path, lit, enclosingStores, fset)...)
					}
				}
			}
		}
		ast.Inspect(node, func(child ast.Node) bool {
			if child == node {
				return true
			}
			inspect(child, enclosingStores)
			return false
		})
	}
	for _, decl := range file.Decls {
		inspect(decl, nil)
	}
	return findings
}

func scanTxScopeFunction(path string, body *ast.BlockStmt, params []txScopeParam, fset *token.FileSet) []txScopeFinding {
	if body == nil {
		return nil
	}
	var findings []txScopeFinding
	transactionScoped := false
	stores := map[string]bool{}
	for _, param := range params {
		if param.kind == txScopeQueryer || param.kind == txScopeTx {
			transactionScoped = true
		}
		if param.kind == txScopeStore {
			stores[param.name] = true
		}
	}
	if !transactionScoped {
		return scanTxScopeNilChecks(path, body, params, fset)
	}
	for _, param := range params {
		if param.kind == txScopeStore {
			findings = append(findings, txScopeFinding{path, fset.Position(param.ident.Pos()).Line, param.ident.Name, 1, "transaction-scoped function receives a *Store or *sql.DB; use a tx-scoped core with only the live transaction handle"})
		}
	}
	ast.Inspect(body, func(node ast.Node) bool {
		if node == nil {
			return false
		}
		if _, nested := node.(*ast.FuncLit); nested {
			return false
		}
		ident, ok := node.(*ast.Ident)
		if ok && stores[ident.Name] {
			findings = append(findings, txScopeFinding{path, fset.Position(ident.Pos()).Line, ident.Name, 1, "transaction-scoped function references a *Store or *sql.DB value; use tx-scoped query helpers instead"})
		}
		return true
	})
	findings = append(findings, scanTxScopeNilChecks(path, body, params, fset)...)
	findings = append(findings, scanTxScopeCursorQueries(path, body, params, fset)...)
	return findings
}

func scanTxScopeCursorQueries(path string, body *ast.BlockStmt, params []txScopeParam, fset *token.FileSet) []txScopeFinding {
	handles := txScopeQueryHandleNames(params)
	cursors := txScopeCursorNames(body, handles)
	if len(cursors) == 0 {
		return nil
	}
	var findings []txScopeFinding
	seen := make(map[token.Pos]bool)
	ast.Inspect(body, func(node ast.Node) bool {
		loop, ok := node.(*ast.ForStmt)
		if !ok || txScopeLoopCursor(loop.Cond, cursors) == "" || loop.Body == nil {
			return true
		}
		ast.Inspect(loop.Body, func(child ast.Node) bool {
			if child == nil {
				return false
			}
			if _, nested := child.(*ast.FuncLit); nested {
				return false
			}
			call, ok := child.(*ast.CallExpr)
			if !ok || !txScopeCallUsesQueryHandle(call, handles) || seen[call.Pos()] {
				return true
			}
			seen[call.Pos()] = true
			findings = append(findings, txScopeFinding{path, fset.Position(call.Pos()).Line, txScopeCallIdentifier(call), 4, "query handle is used while a query cursor is open; materialize the cursor before the nested query"})
			return true
		})
		return true
	})
	return findings
}

func txScopeQueryHandleNames(params []txScopeParam) map[string]bool {
	handles := make(map[string]bool)
	for _, param := range params {
		if param.kind == txScopeQueryer || param.kind == txScopeTx {
			handles[param.name] = true
		}
	}
	return handles
}

func txScopeCursorNames(body *ast.BlockStmt, handles map[string]bool) map[string]bool {
	cursors := make(map[string]bool)
	ast.Inspect(body, func(node ast.Node) bool {
		assign, ok := node.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for index, rhs := range assign.Rhs {
			call, ok := rhs.(*ast.CallExpr)
			if !ok || !txScopeQueryRowsCall(call, handles) || index >= len(assign.Lhs) {
				continue
			}
			if ident, ok := assign.Lhs[index].(*ast.Ident); ok {
				cursors[ident.Name] = true
			}
		}
		return true
	})
	return cursors
}

func txScopeQueryRowsCall(call *ast.CallExpr, handles map[string]bool) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "QueryContext" {
		return false
	}
	receiver, ok := selector.X.(*ast.Ident)
	return ok && handles[receiver.Name]
}

func txScopeLoopCursor(condition ast.Expr, cursors map[string]bool) string {
	call, ok := condition.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return ""
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Next" {
		return ""
	}
	receiver, ok := selector.X.(*ast.Ident)
	if !ok || !cursors[receiver.Name] {
		return ""
	}
	return receiver.Name
}

func txScopeCallUsesQueryHandle(call *ast.CallExpr, handles map[string]bool) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if ok && (selector.Sel.Name == "QueryContext" || selector.Sel.Name == "QueryRowContext") {
		if receiver, receiverOK := selector.X.(*ast.Ident); receiverOK && handles[receiver.Name] {
			return true
		}
	}
	for _, arg := range call.Args {
		found := false
		ast.Inspect(arg, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if ok && handles[ident.Name] {
				found = true
				return false
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

func txScopeCallIdentifier(call *ast.CallExpr) string {
	if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
		return selector.Sel.Name
	}
	if ident, ok := call.Fun.(*ast.Ident); ok {
		return ident.Name
	}
	return "call"
}

func scanTxScopeNilChecks(path string, body *ast.BlockStmt, params []txScopeParam, fset *token.FileSet) []txScopeFinding {
	queryers := map[string]bool{}
	for _, param := range params {
		if param.kind == txScopeQueryer {
			queryers[param.name] = true
		}
	}
	var findings []txScopeFinding
	ast.Inspect(body, func(node ast.Node) bool {
		binary, ok := node.(*ast.BinaryExpr)
		if !ok || binary.Op != token.EQL {
			return true
		}
		left, lok := binary.X.(*ast.Ident)
		right, rok := binary.Y.(*ast.Ident)
		if lok && queryers[left.Name] && rok && right.Name == "nil" {
			findings = append(findings, txScopeFinding{path, fset.Position(left.Pos()).Line, left.Name, 3, "queryer parameter compared with nil; guard the live handle in the owning method instead"})
		}
		if rok && queryers[right.Name] && lok && left.Name == "nil" {
			findings = append(findings, txScopeFinding{path, fset.Position(right.Pos()).Line, right.Name, 3, "queryer parameter compared with nil; guard the live handle in the owning method instead"})
		}
		return true
	})
	return findings
}

func scanTxScopeClosure(path string, lit *ast.FuncLit, stores []string, fset *token.FileSet) []txScopeFinding {
	var findings []txScopeFinding
	line := fset.Position(lit.Pos()).Line
	if lit.Body == nil || len(lit.Body.List) != 1 {
		findings = append(findings, txScopeFinding{path, line, "Transact", 2, "Transact closure must contain exactly one return statement; extract coordination into a tx-taking wrapper"})
		return findings
	}
	ret, ok := lit.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		findings = append(findings, txScopeFinding{path, line, "Transact", 2, "Transact closure must contain exactly one return statement; extract coordination into a tx-taking wrapper"})
		return findings
	}
	call, ok := ret.Results[0].(*ast.CallExpr)
	if !ok {
		findings = append(findings, txScopeFinding{path, line, "Transact", 2, "Transact closure must return a single function call; extract coordination into a tx-taking wrapper"})
		return findings
	}
	for _, name := range stores {
		var captured *ast.Ident
		ast.Inspect(lit.Body, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if ok && ident.Name == name {
				captured = ident
				return false
			}
			return true
		})
		if captured != nil {
			findings = append(findings, txScopeFinding{path, fset.Position(captured.Pos()).Line, name, 2, "Transact closure captures a *Store value; pass the transaction to a tx-taking wrapper instead"})
		}
	}
	_ = call
	return findings
}

func txScopeIsTransactCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && (selector.Sel.Name == "Transact" || selector.Sel.Name == "TransactDurable" || selector.Sel.Name == "readSnapshot" || selector.Sel.Name == "ReadTransaction")
}

func txScopeParams(recv *ast.FieldList, params *ast.FieldList, aliases map[string]string) []txScopeParam {
	var result []txScopeParam
	for _, fields := range []*ast.FieldList{recv, params} {
		if fields == nil {
			continue
		}
		for _, field := range fields.List {
			kind := txScopeTypeKind(field.Type, aliases)
			for _, name := range field.Names {
				result = append(result, txScopeParam{name: name.Name, kind: kind, ident: name})
			}
		}
	}
	return result
}

func txScopeTypeKind(expr ast.Expr, aliases map[string]string) string {
	if ident, ok := expr.(*ast.Ident); ok && (ident.Name == "queryer" || ident.Name == "Queryer") {
		return txScopeQueryer
	}
	if ident, ok := expr.(*ast.Ident); ok && (ident.Name == "WriteTx" || ident.Name == "WriteGuard") {
		return txScopeTx
	}
	if selector, ok := expr.(*ast.SelectorExpr); ok {
		if base, ok := selector.X.(*ast.Ident); ok && aliases[base.Name] == "github.com/sharper-flow/concord/internal/store/internal/sqlitedb" {
			switch selector.Sel.Name {
			case "Queryer":
				return txScopeQueryer
			case "WriteTx", "WriteGuard":
				return txScopeTx
			}
		}
	}
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return ""
	}
	if ident, ok := star.X.(*ast.Ident); ok {
		switch ident.Name {
		case "Store":
			return txScopeStore
		case "Transaction", "writeTx", "WriteTx", "WriteGuard":
			return txScopeTx
		}
	}
	selector, ok := star.X.(*ast.SelectorExpr)
	if ok {
		selectorBase, baseOK := selector.X.(*ast.Ident)
		if baseOK {
			if selectorBase.Name == "sql" || aliases[selectorBase.Name] == "database/sql" {
				switch selector.Sel.Name {
				case "Tx":
					return txScopeTx
				case "DB":
					return txScopeStore
				}
			}
			if aliases[selectorBase.Name] == "github.com/sharper-flow/concord/internal/store/internal/sqlitedb" && (selector.Sel.Name == "WriteTx" || selector.Sel.Name == "WriteGuard") {
				return txScopeTx
			}
		}
	}
	return ""
}

func txScopeStoreNames(params []txScopeParam) []string {
	var names []string
	for _, param := range params {
		if param.kind == txScopeStore {
			names = append(names, param.name)
		}
	}
	return names
}
