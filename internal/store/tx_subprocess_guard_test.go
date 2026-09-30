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

// CD-0195 D2: no write transaction stays open across a subprocess, a network
// call, or a large filesystem operation. This guard is the structural half
// of that rule, beside txscope_test.go: it fails when a function that holds
// a store write transaction reaches GitRunner.Run/RunStdin, exec.Command*,
// or os.RemoveAll through a package-local call. It scans internal/store and
// internal/agent, the two packages that open store transactions.

type txSubprocessFinding struct {
	Path       string
	Line       int
	Identifier string
	Message    string
}

func (f txSubprocessFinding) String() string {
	return f.Path + ":" + strconv.Itoa(f.Line) + ": " + f.Identifier + ": " + f.Message
}

func TestTxSubprocessGuard(t *testing.T) {
	t.Parallel()
	root := txSubprocessRepoRoot()
	findings, analyzed := scanTxSubprocess(root)
	// A scan that reaches no files reports no findings, which would let this
	// assertion pass while proving nothing. The two packages hold far more
	// non-test files than this bound; a count below it means discovery broke.
	if analyzed < 40 {
		t.Fatalf("transaction subprocess analysis reached %d files across internal/store and internal/agent; discovery is broken", analyzed)
	}
	for _, finding := range findings {
		t.Errorf("%s", finding)
	}
}

func TestTxSubprocessGuardBites(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
	}{
		{"transaction parameter reaches a git runner", `package store
func violates(tx *sql.Tx, r GitRunner) { r.Run(nil, "dir", "status") }`},
		{"queryer parameter reaches a git runner", `package store
func violates(q queryer, r GitRunner) { r.RunStdin(nil, "dir", nil, "patch-id") }`},
		{"Tx-suffixed function removes a directory", `package store
func foldTx() { os.RemoveAll("path") }`},
		{"Transact closure spawns a subprocess", `package store
func plans(s *Store) { s.Transact(nil, func(tx *Transaction) error { exec.Command("git", "status"); return nil }) }`},
		{"agent effect closure reaches a git runner", `package store
func plan() { _ = func(tx *store.Transaction) error { _, err := runner.Run(nil, "dir", "worktree", "remove"); return err } }`},
		{"nested cleanup in a transaction function", `package store
func holdTx(tx *sql.Tx) { defer func() { os.RemoveAll("path") }(); _ = tx }`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, tt.name+".go", tt.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			findings := scanTxSubprocessFile(tt.name+".go", file, fset)
			if len(findings) != 1 {
				t.Fatalf("self-test found %d findings, want exactly one: %v", len(findings), findings)
			}
		})
	}
	negative := []struct {
		name   string
		source string
	}{
		{"plain function may run git", `package store
func probe(r GitRunner) { r.Run(nil, "dir", "status") }`},
		{"command run with no arguments is not a git runner call", `package store
func holdTx(cmd *exec.Cmd) { _ = cmd.Run() }`},
	}
	for _, tt := range negative {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, tt.name+".go", tt.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			if findings := scanTxSubprocessFile(tt.name+".go", file, fset); len(findings) != 0 {
				t.Fatalf("non-transaction scope flagged: %v", findings)
			}
		})
	}
}

func txSubprocessRepoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}

func scanTxSubprocess(root string) ([]txSubprocessFinding, int) {
	dirs := []string{filepath.Join(root, "internal", "store"), filepath.Join(root, "internal", "agent")}
	var paths []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return []txSubprocessFinding{{Path: filepath.ToSlash(dir), Line: 1, Identifier: dir, Message: "cannot read package: " + err.Error()}}, 0
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(paths)
	var findings []txSubprocessFinding
	for _, path := range paths {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			findings = append(findings, txSubprocessFinding{Path: filepath.ToSlash(path), Line: 1, Identifier: filepath.Base(path), Message: "cannot parse: " + err.Error()})
			continue
		}
		findings = append(findings, scanTxSubprocessFile(filepath.ToSlash(path), file, fset)...)
	}
	return findings, len(paths)
}

// txSubprocessHoldingParams reports whether a parameter list makes a function
// transaction-holding: any queryer, *sql.Tx, or *Transaction handle (the
// agent package names the latter through the store selector).
func txSubprocessHoldingParams(fields *ast.FieldList) bool {
	if fields == nil {
		return false
	}
	for _, field := range fields.List {
		if expr := field.Type; expr != nil {
			if star, ok := expr.(*ast.StarExpr); ok {
				switch inner := star.X.(type) {
				case *ast.Ident:
					if inner.Name == "Transaction" {
						return true
					}
				case *ast.SelectorExpr:
					// *sql.Tx in this package, *store.Transaction in the
					// agent package.
					if inner.Sel.Name == "Transaction" || inner.Sel.Name == "Tx" {
						return true
					}
				}
			}
			if ident, ok := expr.(*ast.Ident); ok && ident.Name == "queryer" {
				return true
			}
		}
	}
	return false
}

func scanTxSubprocessFile(path string, file *ast.File, fset *token.FileSet) []txSubprocessFinding {
	var findings []txSubprocessFinding
	flag := func(call *ast.CallExpr, holding bool) {
		if !holding {
			return
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		switch {
		case (selector.Sel.Name == "Run" || selector.Sel.Name == "RunStdin") && len(call.Args) >= 2:
			findings = append(findings, txSubprocessFinding{Path: path, Line: fset.Position(call.Pos()).Line, Identifier: selector.Sel.Name, Message: "transaction-holding function reaches a git runner; probe before the transaction opens or run the call after commit (CD-0195 D2)"})
		case selector.Sel.Name == "Command" || selector.Sel.Name == "CommandContext":
			if base, ok := selector.X.(*ast.Ident); ok && base.Name == "exec" {
				findings = append(findings, txSubprocessFinding{Path: path, Line: fset.Position(call.Pos()).Line, Identifier: "exec." + selector.Sel.Name, Message: "transaction-holding function spawns a subprocess; move the subprocess outside the transaction (CD-0195 D2)"})
			}
		case selector.Sel.Name == "RemoveAll":
			if base, ok := selector.X.(*ast.Ident); ok && base.Name == "os" {
				findings = append(findings, txSubprocessFinding{Path: path, Line: fset.Position(call.Pos()).Line, Identifier: "os.RemoveAll", Message: "transaction-holding function removes a directory tree; run the bulk removal after commit (CD-0195 D2)"})
			}
		}
	}
	var walk func(node ast.Node, holding bool)
	var walkStmts func(stmts []ast.Stmt, holding bool)
	walkStmts = func(stmts []ast.Stmt, holding bool) {
		for _, stmt := range stmts {
			ast.Inspect(stmt, func(child ast.Node) bool {
				switch n := child.(type) {
				case *ast.FuncLit:
					walk(n, holding)
					return false
				case *ast.CallExpr:
					flag(n, holding)
					if selector, ok := n.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "Transact" {
						for _, arg := range n.Args {
							if lit, ok := arg.(*ast.FuncLit); ok {
								walk(lit, true)
							}
						}
						return false
					}
				}
				return true
			})
		}
	}
	walk = func(node ast.Node, holding bool) {
		if node == nil {
			return
		}
		switch n := node.(type) {
		case *ast.FuncDecl:
			childHolding := holding || strings.HasSuffix(n.Name.Name, "Tx") || txSubprocessHoldingParams(n.Recv) || txSubprocessHoldingParams(n.Type.Params)
			if n.Body != nil {
				walkStmts(n.Body.List, childHolding)
			}
			return
		case *ast.FuncLit:
			childHolding := holding || txSubprocessHoldingParams(n.Type.Params)
			walkStmts(n.Body.List, childHolding)
			return
		case *ast.CallExpr:
			flag(n, holding)
			if selector, ok := n.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "Transact" {
				// A Transact closure holds the store's write transaction for
				// its whole body, whatever its parameters name.
				for _, arg := range n.Args {
					if lit, ok := arg.(*ast.FuncLit); ok {
						walk(lit, true)
					}
				}
				return
			}
		}
		ast.Inspect(node, func(child ast.Node) bool {
			if child == node {
				return true
			}
			switch child.(type) {
			case *ast.FuncDecl, *ast.FuncLit:
				walk(child, holding)
				return false
			}
			walk(child, holding)
			return false
		})
	}
	for _, decl := range file.Decls {
		walk(decl, false)
	}
	return findings
}
