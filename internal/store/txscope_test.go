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
	root := txScopeRepoRoot()
	findings, analyzed := scanTxScope(root)
	// A scan that reaches no files reports no findings, which would let this
	// assertion pass while proving nothing. The package holds dozens of
	// non-test files; a count this low means discovery broke, not that the
	// package shrank.
	if analyzed < 30 {
		t.Fatalf("transaction scope analysis reached %d files in internal/store; discovery is broken", analyzed)
	}
	for _, finding := range findings {
		t.Errorf("%s", finding)
	}
}

func TestTxScopePropertiesBite(t *testing.T) {
	tests := []struct {
		name     string
		property int
		source   string
	}{
		{"transaction handle and store", 1, `package store; func violates(q queryer, s *Store) { _ = q }`},
		{"multi-statement transaction closure", 2, `package store; func violates(s *Store) { s.Transact(nil, func(tx *Transaction) error { x := 1; _ = x; return nil }) }`},
		{"queryer nil comparison", 3, `package store; func violates(q queryer) { if q == nil { return } }`},
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

func scanTxScope(root string) ([]txScopeFinding, int) {
	storeDir := filepath.Join(root, "internal", "store")
	entries, err := os.ReadDir(storeDir)
	if err != nil {
		return []txScopeFinding{{Path: "internal/store", Line: 1, Identifier: "store", Property: 0, Message: "cannot read package: " + err.Error()}}, 0
	}

	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		paths = append(paths, filepath.Join(storeDir, entry.Name()))
	}
	sort.Strings(paths)

	var findings []txScopeFinding
	for _, path := range paths {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			findings = append(findings, txScopeFinding{Path: filepath.ToSlash(filepath.Join("internal/store", filepath.Base(path))), Line: 1, Identifier: filepath.Base(path), Property: 0, Message: "cannot parse: " + err.Error()})
			continue
		}
		rel := filepath.ToSlash(filepath.Join("internal/store", filepath.Base(path)))
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
	var inspect func(ast.Node, []string)
	inspect = func(node ast.Node, enclosingStores []string) {
		if node == nil {
			return
		}
		switch n := node.(type) {
		case *ast.FuncDecl:
			params := txScopeParams(n.Recv, n.Type.Params)
			findings = append(findings, scanTxScopeFunction(path, n.Body, params, fset)...)
			stores := txScopeStoreNames(params)
			for _, decl := range n.Body.List {
				inspect(decl, stores)
			}
			return
		case *ast.FuncLit:
			params := txScopeParams(nil, n.Type.Params)
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
			findings = append(findings, txScopeFinding{path, fset.Position(param.ident.Pos()).Line, param.ident.Name, 1, "transaction-scoped function receives a *Store; use a tx-scoped core with only the live transaction handle"})
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
			findings = append(findings, txScopeFinding{path, fset.Position(ident.Pos()).Line, ident.Name, 1, "transaction-scoped function references a *Store value; use tx-scoped query helpers instead"})
		}
		return true
	})
	findings = append(findings, scanTxScopeNilChecks(path, body, params, fset)...)
	return findings
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
	return ok && selector.Sel.Name == "Transact"
}

func txScopeParams(recv *ast.FieldList, params *ast.FieldList) []txScopeParam {
	var result []txScopeParam
	for _, fields := range []*ast.FieldList{recv, params} {
		if fields == nil {
			continue
		}
		for _, field := range fields.List {
			kind := txScopeTypeKind(field.Type)
			for _, name := range field.Names {
				result = append(result, txScopeParam{name: name.Name, kind: kind, ident: name})
			}
		}
	}
	return result
}

func txScopeTypeKind(expr ast.Expr) string {
	if ident, ok := expr.(*ast.Ident); ok && ident.Name == "queryer" {
		return txScopeQueryer
	}
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return ""
	}
	if ident, ok := star.X.(*ast.Ident); ok {
		switch ident.Name {
		case "Store":
			return txScopeStore
		case "Transaction":
			return txScopeTx
		}
	}
	selector, ok := star.X.(*ast.SelectorExpr)
	if ok {
		selectorBase, baseOK := selector.X.(*ast.Ident)
		if baseOK && selectorBase.Name == "sql" && selector.Sel.Name == "Tx" {
			return txScopeTx
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
