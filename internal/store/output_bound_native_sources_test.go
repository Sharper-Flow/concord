package store

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const nativeWorktreesFile = "internal/store/worktrees.go"

func init() {
	branch := goPatternBound(nativeWorktreesFile, "worktreeBranchPattern")
	path := nativePathMaxProbeDerived()
	content := nativeInspectContentDerived()
	for _, prefix := range []string{"/$defs/worktree_inspect_result", "/$defs/worktree_inspect_result/oneOf/0"} {
		registerOutputBound(prefix+"/properties/branch/maxLength", branch)
		registerOutputBound(prefix+"/properties/path/maxLength", path)
		registerOutputBound(prefix+"/properties/content/maxLength", content)
	}
}

func nativeFuncDecl(rt *ledgerRuntime, file, name string) (*ast.FuncDecl, error) {
	goS, err := rt.loadGo(file)
	if err != nil {
		return nil, err
	}
	fns := ledgerFuncDeclsNamed(goS.file, name)
	if len(fns) != 1 || fns[0].Body == nil {
		return nil, fmt.Errorf("bound: %s is missing, ambiguous or bodyless in %s", name, file)
	}
	return fns[0], nil
}

func nativeStringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	return value, err == nil
}

func nativeIntLit(e ast.Expr) (int64, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return 0, false
	}
	value, err := strconv.ParseInt(lit.Value, 0, 64)
	return value, err == nil
}

func nativeCalls(node ast.Node, callee string) []*ast.CallExpr {
	var found []*ast.CallExpr
	ast.Inspect(node, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && ledgerCalleeMatches(call, callee) {
			found = append(found, call)
		}
		return true
	})
	return found
}

func nativeSelectorName(e ast.Expr) (string, bool) {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return "", false
	}
	return sel.Sel.Name, true
}

// Shared with the mutation-document digest proof.
func nativeASCIIWidth(chars int64, describe string) evaluatedBound {
	return evaluatedBound{kind: "derived", describe: describe, constraints: []boundConstraint{
		{unit: boundCharacters, value: chars, from: describe},
		{unit: boundBytes, value: chars, from: describe + " (ASCII)"},
	}}
}

func nativeDigestShape(rt *ledgerRuntime, file, function string) (int64, error) {
	fn, err := nativeFuncDecl(rt, file, function)
	if err != nil {
		return 0, err
	}
	var literals []string
	ast.Inspect(fn, func(n ast.Node) bool {
		if e, ok := n.(ast.Expr); ok {
			if lit, ok := nativeStringLit(e); ok {
				literals = append(literals, lit)
			}
		}
		return true
	})
	if len(nativeCalls(fn, "sha256.Sum256")) != 1 || len(nativeCalls(fn, "hex.EncodeToString")) != 1 || len(literals) != 1 || literals[0] != "sha256:" {
		return 0, fmt.Errorf("bound: %s:%s no longer has the fixed sha256-prefixed digest body", file, function)
	}
	return int64(len("sha256:")) + 2*32, nil
}

func nativeProbeGatePos(fn *ast.FuncDecl, op string) (token.Pos, error) {
	found := token.NoPos
	var problem error
	ast.Inspect(fn, func(n ast.Node) bool {
		if found != token.NoPos || problem != nil {
			return false
		}
		branch, ok := n.(*ast.IfStmt)
		if !ok || branch.Init == nil || branch.Cond == nil || branch.Body == nil {
			return true
		}
		init, ok := branch.Init.(*ast.AssignStmt)
		if !ok || len(init.Lhs) != 1 || len(init.Rhs) != 1 {
			return true
		}
		errIdent, ok := init.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		call, ok := init.Rhs[0].(*ast.CallExpr)
		if !ok || !ledgerCalleeMatches(call, "probeWorktreeReachable") || len(call.Args) != 4 {
			return true
		}
		gateOp, ok := nativeStringLit(call.Args[2])
		if !ok || gateOp != op {
			return true
		}
		be, ok := branch.Cond.(*ast.BinaryExpr)
		if !ok || be.Op != token.NEQ {
			return true
		}
		left, leftOK := be.X.(*ast.Ident)
		right, rightOK := be.Y.(*ast.Ident)
		if !leftOK || !rightOK || left.Name != errIdent.Name || right.Name != "nil" {
			return true
		}
		returns := false
		ast.Inspect(branch.Body, func(m ast.Node) bool {
			if _, ok := m.(*ast.ReturnStmt); ok {
				returns = true
			}
			return true
		})
		if !returns {
			problem = fmt.Errorf("bound: the %s probe gate no longer returns on error", op)
			return false
		}
		found = branch.Pos()
		return true
	})
	if problem != nil {
		return token.NoPos, problem
	}
	if found == token.NoPos {
		return token.NoPos, fmt.Errorf("bound: no error-propagating probeWorktreeReachable(%q) gate remains", op)
	}
	return found, nil
}

func nativePathProjectionPos(fn *ast.FuncDecl) token.Pos {
	pos := token.NoPos
	ast.Inspect(fn, func(n ast.Node) bool {
		if pos != token.NoPos {
			return false
		}
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if ok && key.Name == "Path" {
			if name, ok := nativeSelectorName(kv.Value); ok && name == "Path" {
				pos = kv.Pos()
			}
		}
		return true
	})
	return pos
}

func nativeExecGitChdirProof(execRun *ast.FuncDecl) error {
	gitC := false
	ast.Inspect(execRun, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		lhs, ok := assign.Lhs[0].(*ast.Ident)
		if !ok || lhs.Name != "command" {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok || !ledgerCalleeMatches(call, "append") || len(call.Args) != 2 || !call.Ellipsis.IsValid() {
			return true
		}
		lit, ok := call.Args[0].(*ast.CompositeLit)
		if !ok || len(lit.Elts) != 2 {
			return true
		}
		first, firstOK := nativeStringLit(lit.Elts[0])
		dir, dirOK := lit.Elts[1].(*ast.Ident)
		spread, spreadOK := call.Args[1].(*ast.Ident)
		gitC = firstOK && first == "-C" && dirOK && dir.Name == "dir" && spreadOK && spread.Name == "args"
		return true
	})
	if !gitC || len(nativeCalls(execRun, "exec.Command")) == 0 {
		return fmt.Errorf("bound: ExecGitRunner.Run no longer runs git -C dir with variadic argv")
	}
	returnsErr := false
	ast.Inspect(execRun, func(n ast.Node) bool {
		if ret, ok := n.(*ast.ReturnStmt); ok {
			for _, result := range ret.Results {
				if ident, ok := result.(*ast.Ident); ok && ident.Name == "err" {
					returnsErr = true
				}
			}
		}
		return true
	})
	if !returnsErr {
		return fmt.Errorf("bound: ExecGitRunner.Run no longer returns the child error")
	}
	return nil
}

func nativeProbeFirstCommandProof(probe *ast.FuncDecl) error {
	if probe.Body == nil || len(probe.Body.List) == 0 {
		return fmt.Errorf("bound: probeWorktreeReachable has no probe statement")
	}
	first, ok := probe.Body.List[0].(*ast.AssignStmt)
	if !ok || len(first.Rhs) != 1 {
		return fmt.Errorf("bound: probeWorktreeReachable no longer opens with the git probe")
	}
	call, ok := first.Rhs[0].(*ast.CallExpr)
	if !ok || !ledgerCalleeMatches(call, "Run") || len(call.Args) < 3 {
		return fmt.Errorf("bound: probeWorktreeReachable no longer opens with the git probe")
	}
	if name, ok := nativeSelectorName(call.Args[1]); !ok || name != "Path" {
		return fmt.Errorf("bound: the probe no longer runs in entry.Path")
	}
	for _, arg := range call.Args[2:] {
		if lit, ok := nativeStringLit(arg); ok && lit == "rev-parse" {
			return nil
		}
	}
	return fmt.Errorf("bound: the probe no longer runs git rev-parse")
}

func nativePathRouteProof(fn *ast.FuncDecl, op string) error {
	gate, err := nativeProbeGatePos(fn, op)
	if err != nil {
		return err
	}
	projection := nativePathProjectionPos(fn)
	if projection == token.NoPos || gate > projection {
		return fmt.Errorf("bound: %s no longer projects entry.Path behind its %q probe gate", fn.Name.Name, op)
	}
	return nil
}

// A successful git -C probe admits the projected path through the kernel.
func nativePathMaxProbeDerived() outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		width, err := nativePathSuccessfulSyscallWidth(rt)
		if err != nil {
			return evaluatedBound{}, err
		}
		describe := fmt.Sprintf("derived inspect path width: successful git -C probe admits at most syscall.PathMax-1 = %d bytes", width)
		return evaluatedBound{kind: "derived", describe: describe, constraints: []boundConstraint{{unit: boundBytes, value: width, from: describe}}}, nil
	}}
}

func nativePathSuccessfulSyscallWidth(rt *ledgerRuntime) (int64, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return 0, fmt.Errorf("bound: syscall.PathMax applies only on linux/amd64")
	}
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return 0, fmt.Errorf("bound: cannot resolve GOROOT: %w", err)
	}
	goRoot := strings.TrimSpace(string(out))
	if goRoot == "" {
		return 0, fmt.Errorf("bound: the go toolchain reports no GOROOT")
	}
	ztypes := filepath.Join("src", "syscall", "ztypes_linux_amd64.go")
	raw, err := os.ReadFile(filepath.Join(goRoot, ztypes))
	if err != nil {
		return 0, err
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), ztypes, raw, parser.SkipObjectResolution)
	if err != nil {
		return 0, err
	}
	pathMax, found := int64(0), false
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if ok && len(value.Names) == 1 && len(value.Values) == 1 && value.Names[0].Name == "PathMax" {
				pathMax, found = nativeIntLit(value.Values[0])
			}
		}
	}
	if !found || pathMax != 0x1000 {
		return 0, fmt.Errorf("bound: toolchain no longer pins syscall.PathMax at 0x1000")
	}
	runner, err := nativeFuncDecl(rt, "internal/store/project_locator.go", "Run")
	if err != nil {
		return 0, err
	}
	if err := nativeExecGitChdirProof(runner); err != nil {
		return 0, err
	}
	probe, err := nativeFuncDecl(rt, nativeWorktreesFile, "probeWorktreeReachable")
	if err != nil {
		return 0, err
	}
	if err := nativeProbeFirstCommandProof(probe); err != nil {
		return 0, err
	}
	inspect, err := nativeFuncDecl(rt, nativeWorktreesFile, "InspectWorktree")
	if err != nil {
		return 0, err
	}
	if err := nativePathRouteProof(inspect, "worktree_inspect"); err != nil {
		return 0, err
	}
	return pathMax - 1, nil
}

// The production caller omits MaxBytes; Inspect clamps every content assignment.
func nativeInspectContentDerived() outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		inspect, err := nativeFuncDecl(rt, nativeWorktreesFile, "InspectWorktree")
		if err != nil {
			return evaluatedBound{}, err
		}
		fallbackFound, bounded, unbounded := false, 0, 0
		ast.Inspect(inspect, func(n ast.Node) bool {
			if branch, ok := n.(*ast.IfStmt); ok && branch.Cond != nil {
				if be, ok := branch.Cond.(*ast.BinaryExpr); ok && be.Op == token.LEQ {
					if ident, ok := be.X.(*ast.Ident); ok && ident.Name == "maxBytes" {
						if value, ok := nativeIntLit(be.Y); ok && value == 0 {
							ast.Inspect(branch.Body, func(m ast.Node) bool {
								assign, ok := m.(*ast.AssignStmt)
								if ok && len(assign.Lhs) == 1 && len(assign.Rhs) == 1 && ledgerExprText(assign.Lhs[0]) == "maxBytes" && ledgerExprText(assign.Rhs[0]) == "defaultWorktreeInspectBytes" {
									fallbackFound = true
								}
								return true
							})
						}
					}
				}
			}
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range assign.Lhs {
				if name, ok := nativeSelectorName(lhs); !ok || name != "Content" {
					continue
				}
				if len(assign.Rhs) == 1 {
					if call, ok := assign.Rhs[0].(*ast.CallExpr); ok && ledgerCalleeMatches(call, "boundText") && len(call.Args) == 2 && ledgerExprText(call.Args[1]) == "maxBytes" {
						bounded++
						continue
					}
				}
				unbounded++
			}
			return true
		})
		if !fallbackFound || bounded == 0 || unbounded != 0 {
			return evaluatedBound{}, fmt.Errorf("bound: InspectWorktree must use the default and boundText: %d bounded, %d other assignments", bounded, unbounded)
		}
		reader, err := nativeFuncDecl(rt, nativeWorktreesFile, "boundText")
		if err != nil {
			return evaluatedBound{}, err
		}
		sliced := false
		ast.Inspect(reader, func(n ast.Node) bool {
			if slice, ok := n.(*ast.SliceExpr); ok && slice.Low == nil && !slice.Slice3 && slice.High != nil && ledgerExprText(slice.High) == "maxBytes" {
				sliced = true
			}
			return true
		})
		if !sliced {
			return evaluatedBound{}, fmt.Errorf("bound: boundText no longer slices text to [:maxBytes]")
		}
		callerSites := 0
		for _, dir := range []string{"internal/agent", "internal/store"} {
			entries, err := os.ReadDir(filepath.Join(rt.root, filepath.Clean(dir)))
			if err != nil {
				return evaluatedBound{}, err
			}
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
					continue
				}
				name := filepath.ToSlash(filepath.Join(dir, entry.Name()))
				goS, err := rt.loadGo(name)
				if err != nil {
					continue
				}
				for _, decl := range goS.file.Decls {
					fn, ok := decl.(*ast.FuncDecl)
					if !ok {
						continue
					}
					for _, call := range nativeCalls(fn, "InspectWorktree") {
						callerSites++
						var request *ast.CompositeLit
						for _, arg := range call.Args {
							if lit, ok := arg.(*ast.CompositeLit); ok {
								request = lit
							}
						}
						if request == nil {
							return evaluatedBound{}, fmt.Errorf("bound: InspectWorktree call in %s:%s has no request literal", name, fn.Name.Name)
						}
						for _, elt := range request.Elts {
							if kv, ok := elt.(*ast.KeyValueExpr); ok && ledgerExprText(kv.Key) == "MaxBytes" {
								return evaluatedBound{}, fmt.Errorf("bound: InspectWorktree caller in %s sets MaxBytes", name)
							}
						}
					}
				}
			}
		}
		if callerSites != 1 {
			return evaluatedBound{}, fmt.Errorf("bound: %d production InspectWorktree call sites found, want one", callerSites)
		}
		goS, err := rt.loadGo(nativeWorktreesFile)
		if err != nil {
			return evaluatedBound{}, err
		}
		width, err := rt.evalNumeric(goS, nil, &ast.Ident{Name: "defaultWorktreeInspectBytes"})
		if err != nil {
			return evaluatedBound{}, err
		}
		describe := fmt.Sprintf("derived inspect content: boundText under defaultWorktreeInspectBytes publishes %d bytes", width)
		return evaluatedBound{kind: "derived", describe: describe, constraints: []boundConstraint{{unit: boundBytes, value: width, from: describe}}}, nil
	}}
}

func TestNativePathSuccessfulPathProofMutations(t *testing.T) {
	t.Parallel()
	runner := `package store
func (ExecGitRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	command := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", command...)
	stdout, _, err := runBoundedGitOutput(ctx, cmd)
	return stdout, err
}`
	probe := `package store
func probeWorktreeReachable(ctx context.Context, runner GitRunner, op string, entry WorktreeEntry) error {
	branchOut, err := runner.Run(ctx, entry.Path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil { return wrapFailure(err) }
	return nil
}`
	route := `package store
func inspectWitness(ctx context.Context, runner GitRunner, req WorktreeInspectRequest) (WorktreeInspectResult, error) {
	if err := probeWorktreeReachable(ctx, runner, "worktree_inspect", entry); err != nil {
		return WorktreeInspectResult{}, err
	}
	result := WorktreeInspectResult{WorkID: req.WorkID, Path: entry.Path}
	return result, nil
}`
	cases := []struct {
		name, function, source, from, to string
		check                            func(*ast.FuncDecl) error
	}{
		{"runner without -C", "Run", runner, `[]string{"-C", dir}`, `[]string{dir}`, nativeExecGitChdirProof},
		{"runner directory off argv", "Run", runner, `[]string{"-C", dir}`, `[]string{}`, nativeExecGitChdirProof},
		{"runner swallowing error", "Run", runner, "return stdout, err", "return stdout, nil", nativeExecGitChdirProof},
		{"probe fixed directory", "probeWorktreeReachable", probe, "runner.Run(ctx, entry.Path", `runner.Run(ctx, "."`, nativeProbeFirstCommandProof},
		{"probe other command", "probeWorktreeReachable", probe, `"rev-parse"`, `"status"`, nativeProbeFirstCommandProof},
		{"route swallowing error", "inspectWitness", route, "return WorktreeInspectResult{}, err", "_ = err", func(fn *ast.FuncDecl) error { return nativePathRouteProof(fn, "worktree_inspect") }},
		{"route projects before gate", "inspectWitness", route, "if err := probeWorktreeReachable", "result := WorktreeInspectResult{Path: entry.Path}; if err := probeWorktreeReachable", func(fn *ast.FuncDecl) error { return nativePathRouteProof(fn, "worktree_inspect") }},
		{"route omits path", "inspectWitness", route, "WorkID: req.WorkID, Path: entry.Path", "WorkID: req.WorkID", func(fn *ast.FuncDecl) error { return nativePathRouteProof(fn, "worktree_inspect") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Count(tc.source, tc.from) != 1 {
				t.Fatal("mutation must target one occurrence")
			}
			for _, mutate := range []bool{false, true} {
				source := tc.source
				if mutate {
					source = strings.Replace(source, tc.from, tc.to, 1)
				}
				rt := newLedgerRuntime(ledgerRepoRoot(t))
				if err := rt.parseGoSource("path_witness.go", source); err != nil {
					t.Fatal(err)
				}
				fn, err := nativeFuncDecl(rt, "path_witness.go", tc.function)
				if err != nil {
					t.Fatal(err)
				}
				err = tc.check(fn)
				if !mutate && err != nil {
					t.Fatalf("live shape must prove: %v", err)
				}
				if mutate && err == nil {
					t.Fatal("mutated shape must refuse")
				}
			}
		})
	}
}
