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
// or os.RemoveAll, directly or through any chain of package-local calls.
// It scans internal/store and internal/agent, the two packages that open
// store transactions. Receiver method calls stay untraced: a selector call
// on a non-import receiver cannot resolve without type information, so the
// direct-call rule still covers those at the call site.

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
		{"TransactDurable closure spawns a subprocess", `package store
func plans(s *Store) { s.TransactDurable(nil, func(tx *Transaction) error { exec.Command("git", "status"); return nil }) }`},
		{"write transaction reaches a git runner", `package store
func violates(tx *writeTx, r GitRunner) { r.Run(nil, "dir", "status") }`},
		{"agent effect closure reaches a git runner", `package store
func plan() { _ = func(tx *store.Transaction) error { _, err := runner.Run(nil, "dir", "worktree", "remove"); return err } }`},
		{"nested cleanup in a transaction function", `package store
func holdTx(tx *sql.Tx) { defer func() { os.RemoveAll("path") }(); _ = tx }`},
		{"two-hop transitive reach through package-local calls", `package store
func holdTx(q queryer, r GitRunner) { firstHop(r); _ = q }
func firstHop(r GitRunner) { secondHop(r) }
func secondHop(r GitRunner) { r.Run(nil, "dir", "status") }`},
		{"Transact closure reaches a git runner through a helper", `package store
func plans(s *Store) { s.Transact(nil, func(tx *Transaction) error { return commitHelper() }) }
func commitHelper() error { exec.Command("git", "status"); return nil }`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, tt.name+".go", tt.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			graph := newTxSubprocessGraph([]parsedTxSubprocessFile{{Package: "store", Path: tt.name + ".go", FileSet: fset, File: file}})
			findings := graph.scan()
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
		{"helper may run git when no caller holds a transaction", `package store
func probe() { firstHop(runner) }
func firstHop(r GitRunner) { secondHop(r) }
func secondHop(r GitRunner) { r.Run(nil, "dir", "status") }
var runner GitRunner`},
		{"two-hop chain that never reaches a forbidden call", `package store
func holdTx(q queryer) { firstHopSafe(); _ = q }
func firstHopSafe() { secondHopSafe() }
func secondHopSafe() { println("sql only") }`},
	}
	for _, tt := range negative {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, tt.name+".go", tt.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			graph := newTxSubprocessGraph([]parsedTxSubprocessFile{{Package: "store", Path: tt.name + ".go", FileSet: fset, File: file}})
			if findings := graph.scan(); len(findings) != 0 {
				t.Fatalf("non-transaction scope flagged: %v", findings)
			}
		})
	}
}

func txSubprocessRepoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}

type parsedTxSubprocessFile struct {
	Package string
	Path    string
	FileSet *token.FileSet
	File    *ast.File
}

func scanTxSubprocess(root string) ([]txSubprocessFinding, int) {
	dirs := []string{filepath.Join(root, "internal", "store"), filepath.Join(root, "internal", "agent")}
	packages := map[string]string{}
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
			packages[filepath.Join(dir, entry.Name())] = filepath.Base(dir)
		}
	}
	sort.Strings(paths)
	var parsed []parsedTxSubprocessFile
	for _, path := range paths {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return []txSubprocessFinding{{Path: filepath.ToSlash(path), Line: 1, Identifier: filepath.Base(path), Message: "cannot parse: " + err.Error()}}, len(paths)
		}
		parsed = append(parsed, parsedTxSubprocessFile{Package: packages[path], Path: filepath.ToSlash(path), FileSet: fset, File: file})
	}
	graph := newTxSubprocessGraph(parsed)
	return graph.scan(), len(paths)
}

// txSubprocessFunc is one named package-local function in the call graph.
type txSubprocessFunc struct {
	Package string
	Name    string
	Path    string
	Line    int
	// holding reports that the function itself holds a store transaction
	// while its body runs: a queryer or transaction parameter, or the Tx
	// suffix this repository reserves for tx-scoped cores.
	holding bool
	// txHolding reports that some region of the body runs inside a
	// transaction the function opened: a Transact closure, or a function
	// literal whose parameters carry a transaction handle.
	txHolding bool
	// calls are the package-local calls made anywhere in the body.
	calls []txSubprocessEdge
	// txCalls are the package-local calls made from a body region that runs
	// inside an open transaction.
	txCalls []txSubprocessEdge
	// directForbidden reports that the body makes a forbidden call at any
	// depth, in any frame. Such a function is a forbidden target for every
	// caller's reach walk.
	directForbidden bool
	// directInTxFrames reports a forbidden call made while a transaction is
	// open in this function's own frame or inside a transaction region it
	// opens.
	directInTxFrames bool
}

type txSubprocessEdge struct {
	// target names a package-local function: a bare identifier resolves in
	// the same package, a selector on a scanned-package import resolves in
	// that package.
	target string
	// crossPackage names the scanned package a qualified selector resolves
	// into; empty for same-package identifiers.
	crossPackage string
}

type txSubprocessGraph struct {
	functions map[string]map[string]*txSubprocessFunc // package -> name -> func
	order     []*txSubprocessFunc
}

// newTxSubprocessGraph builds the package-local call graph for the parsed
// files. Pass 1 declares every named function so edges resolve regardless of
// file order; pass 2 classifies bodies and links calls between the scanned
// packages.
func newTxSubprocessGraph(files []parsedTxSubprocessFile) *txSubprocessGraph {
	g := &txSubprocessGraph{functions: map[string]map[string]*txSubprocessFunc{}}
	for _, pf := range files {
		if g.functions[pf.Package] == nil {
			g.functions[pf.Package] = map[string]*txSubprocessFunc{}
		}
		for _, decl := range pf.File.Decls {
			decl, ok := decl.(*ast.FuncDecl)
			if !ok || decl.Body == nil {
				continue
			}
			if _, seen := g.functions[pf.Package][decl.Name.Name]; seen {
				continue
			}
			fn := &txSubprocessFunc{
				Package: pf.Package,
				Name:    decl.Name.Name,
				Path:    pf.Path,
				Line:    pf.FileSet.Position(decl.Pos()).Line,
				holding: strings.HasSuffix(decl.Name.Name, "Tx") || txSubprocessHoldingParams(decl.Recv) || txSubprocessHoldingParams(decl.Type.Params),
			}
			g.functions[pf.Package][fn.Name] = fn
			g.order = append(g.order, fn)
		}
	}
	for _, pf := range files {
		aliases := txSubprocessImportAliases(pf.File)
		for _, decl := range pf.File.Decls {
			decl, ok := decl.(*ast.FuncDecl)
			if !ok || decl.Body == nil {
				continue
			}
			fn := g.functions[pf.Package][decl.Name.Name]
			if fn == nil {
				continue
			}
			g.classifyBody(fn, pf.Package, aliases, decl.Body)
		}
	}
	return g
}

// scan reports one finding per transaction-scoped function whose body, or
// any transaction region it opens, reaches a forbidden call directly or
// through package-local calls.
func (g *txSubprocessGraph) scan() []txSubprocessFinding {
	forbidden := map[string]bool{}
	for _, fn := range g.order {
		if fn.directForbidden {
			forbidden[g.key(fn.Package, fn.Name)] = true
		}
	}
	var findings []txSubprocessFinding
	flag := func(fn *txSubprocessFunc, target string, hops int) {
		findings = append(findings, txSubprocessFinding{
			Path:       fn.Path,
			Line:       fn.Line,
			Identifier: fn.Name,
			Message:    "transaction-holding function reaches " + target + " through " + strconv.Itoa(hops) + " package-local call(s); probe before the transaction opens or run the call after commit (CD-0195 D2)",
		})
	}
	for _, fn := range g.order {
		if fn.directInTxFrames {
			flag(fn, "a forbidden call", 0)
			continue
		}
		if fn.holding {
			if target, hops := g.reachesForbidden(fn.Package, fn.calls, forbidden); target != "" {
				flag(fn, target, hops)
				continue
			}
		}
		if fn.txHolding {
			if target, hops := g.reachesForbidden(fn.Package, fn.txCalls, forbidden); target != "" {
				flag(fn, target, hops)
			}
		}
	}
	return findings
}

func (g *txSubprocessGraph) key(pkg, name string) string { return pkg + "." + name }

// reachesForbidden walks package-local call edges breadth-first and returns
// the first forbidden callee it reaches, with the hop count.
func (g *txSubprocessGraph) reachesForbidden(pkg string, edges []txSubprocessEdge, forbidden map[string]bool) (string, int) {
	visited := map[string]bool{g.key(pkg, ""): true}
	type item struct {
		pkg  string
		name string
		hops int
	}
	queue := make([]item, 0, len(edges))
	enqueue := func(fromPkg string, call txSubprocessEdge, hops int) {
		target := call.crossPackage
		if target == "" {
			target = fromPkg
		}
		queue = append(queue, item{pkg: target, name: call.target, hops: hops})
	}
	for _, call := range edges {
		enqueue(pkg, call, 1)
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		key := g.key(current.pkg, current.name)
		if visited[key] {
			continue
		}
		visited[key] = true
		if forbidden[key] {
			return key, current.hops
		}
		callee := g.functions[current.pkg][current.name]
		if callee == nil {
			continue
		}
		for _, call := range callee.calls {
			enqueue(current.pkg, call, current.hops+1)
		}
	}
	return "", 0
}

// classifyBody walks one function body. It records every package-local call
// and every forbidden call, splitting calls that run inside a transaction
// region (a Transact closure or a literal with transaction parameters) from
// calls in the function's own frame.
func (g *txSubprocessGraph) classifyBody(fn *txSubprocessFunc, pkg string, aliases map[string]string, body *ast.BlockStmt) {
	var walk func(node ast.Node, holding bool)
	walk = func(node ast.Node, holding bool) {
		if node == nil {
			return
		}
		switch n := node.(type) {
		case *ast.FuncLit:
			walk(n.Body, holding || txSubprocessHoldingParams(n.Type.Params))
			return
		case *ast.CallExpr:
			if selector, ok := n.Fun.(*ast.SelectorExpr); ok && (selector.Sel.Name == "Transact" || selector.Sel.Name == "TransactDurable") {
				for _, arg := range n.Args {
					if lit, ok := arg.(*ast.FuncLit); ok {
						fn.txHolding = true
						walk(lit, true)
					}
				}
			}
			target, cross := g.resolveCallee(pkg, n.Fun, aliases)
			if txSubprocessForbiddenCall(n) {
				fn.directForbidden = true
				if holding {
					fn.directInTxFrames = true
				}
			} else if target != "" {
				call := txSubprocessEdge{target: target, crossPackage: cross}
				fn.calls = append(fn.calls, call)
				if holding {
					fn.txCalls = append(fn.txCalls, call)
				}
			}
			walk(n.Fun, holding)
			for _, arg := range n.Args {
				walk(arg, holding)
			}
			return
		}
		ast.Inspect(node, func(child ast.Node) bool {
			if child == node {
				return true
			}
			if _, isDecl := child.(*ast.FuncDecl); isDecl {
				return false
			}
			walk(child, holding)
			return false
		})
	}
	walk(body, fn.holding)
}

// resolveCallee names the package-local function a call expression invokes:
// a bare identifier in the same package, or a selector on an import of one
// of the scanned packages. Receiver method calls resolve to nothing.
func (g *txSubprocessGraph) resolveCallee(pkg string, fun ast.Expr, aliases map[string]string) (string, string) {
	switch expr := fun.(type) {
	case *ast.Ident:
		if _, isFunc := g.functions[pkg][expr.Name]; isFunc {
			return expr.Name, ""
		}
	case *ast.SelectorExpr:
		base, ok := expr.X.(*ast.Ident)
		if !ok {
			return "", ""
		}
		if path, isImport := aliases[base.Name]; isImport {
			for candidate := range g.functions {
				if strings.HasSuffix(path, "/internal/"+candidate) {
					if _, isFunc := g.functions[candidate][expr.Sel.Name]; isFunc {
						return expr.Sel.Name, candidate
					}
				}
			}
		}
	}
	return "", ""
}

// txSubprocessForbiddenCall reports whether a call expression is one of the
// subprocess, command spawn, or bulk removal forms CD-0195 D2 forbids inside
// an open transaction.
func txSubprocessForbiddenCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch {
	case (selector.Sel.Name == "Run" || selector.Sel.Name == "RunStdin") && len(call.Args) >= 2:
		return true
	case selector.Sel.Name == "Command" || selector.Sel.Name == "CommandContext":
		if base, ok := selector.X.(*ast.Ident); ok && base.Name == "exec" {
			return true
		}
	case selector.Sel.Name == "RemoveAll":
		if base, ok := selector.X.(*ast.Ident); ok && base.Name == "os" {
			return true
		}
	}
	return false
}

func txSubprocessImportAliases(file *ast.File) map[string]string {
	aliases := map[string]string{}
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		var name string
		if imp.Name != nil {
			name = imp.Name.Name
		} else {
			name = path[strings.LastIndex(path, "/")+1:]
		}
		aliases[name] = path
	}
	return aliases
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
					if inner.Name == "Transaction" || inner.Name == "writeTx" {
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
