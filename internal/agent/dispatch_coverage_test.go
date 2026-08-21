package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"testing"
)

// The manifest declares a closed operation set and scripts/generate-agent-contracts.py
// renders it into ContractOperations. The adapter derives its tool surface from that
// table, so a manifest operation reaches TypeScript without an edit. The Go runtime
// does not: mutate and read name each operation in a hand-written case label, and the
// only thing that observes an unhandled operation is a default arm at call time.
//
// These tests supply the missing proposition — that the declared set and the dispatched
// set are the same set. They read case labels rather than invoking dispatch because the
// arms carry effects; the claim under test is coverage, not behaviour.

// dispatchRoots names the functions that route a contract operation to its
// implementation, and the file each is declared in. mutate and read hold the
// primary switches. mutateCompaction is separate because mutate routes to it by
// tool name rather than by operation ID, so the compaction operations are named
// there and nowhere else.
var dispatchRoots = map[string]string{
	"mutate":           "mutations.go",
	"mutateCompaction": "mutations.go",
	"read":             "runtime.go",
}

// operationID matches the tool.operation shape used for contract operation IDs.
// Bare tool names, error strings, and schema names do not match.
var operationID = regexp.MustCompile(`^concord_[a-z_]+\.[a-z_]+$`)

// dispatchedOperationIDs returns every operation ID a dispatch root selects on,
// mapped to the root that selects it. An ID counts as dispatched when it appears
// as a case-clause label or as an operand of a string equality comparison — the
// two forms the routing uses.
func dispatchedOperationIDs(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	parsed := map[string]*ast.File{}
	for _, file := range dispatchRoots {
		if _, done := parsed[file]; done {
			continue
		}
		file0, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		parsed[file] = file0
	}

	dispatched := map[string]string{}
	for root, file := range dispatchRoots {
		body := functionBody(parsed[file], root)
		if body == nil {
			t.Fatalf("dispatch root %s is not declared in %s; update dispatchRoots to name the function that replaced it", root, file)
		}
		record := func(expr ast.Expr) {
			lit, ok := expr.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil || !operationID.MatchString(value) {
				return
			}
			dispatched[value] = root
		}
		ast.Inspect(body, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.CaseClause:
				for _, expr := range typed.List {
					record(expr)
				}
			case *ast.BinaryExpr:
				if typed.Op == token.EQL || typed.Op == token.NEQ {
					record(typed.X)
					record(typed.Y)
				}
			}
			return true
		})
	}
	return dispatched
}

func functionBody(file *ast.File, name string) *ast.BlockStmt {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn.Body
		}
	}
	return nil
}

func TestEveryContractOperationReachesADispatchArm(t *testing.T) {
	dispatched := dispatchedOperationIDs(t)
	var missing []string
	for _, operation := range ContractOperations {
		if _, ok := dispatched[operation.ID]; !ok {
			missing = append(missing, operation.ID)
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		t.Fatalf("manifest operations no dispatch root selects on: %v\n"+
			"contracts/agent-tool-surface.v1.json declares these; mutate (mutations.go) or read (runtime.go) must handle them, "+
			"or they reach the default arm and fail at call time with an unimplemented-operation error", missing)
	}
}

func TestEveryDispatchArmNamesAContractOperation(t *testing.T) {
	declared := map[string]bool{}
	for _, operation := range ContractOperations {
		declared[operation.ID] = true
	}
	var orphaned []string
	for id, root := range dispatchedOperationIDs(t) {
		if !declared[id] {
			orphaned = append(orphaned, id+" (in "+root+")")
		}
	}
	if len(orphaned) != 0 {
		sort.Strings(orphaned)
		t.Fatalf("dispatch arms for operations the manifest does not declare: %v\n"+
			"these are unreachable — invoke rejects an unknown operation before dispatch. "+
			"Remove the arm, or add the operation to contracts/agent-tool-surface.v1.json and regenerate", orphaned)
	}
}
