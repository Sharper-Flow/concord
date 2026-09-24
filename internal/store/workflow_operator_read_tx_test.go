package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

// CD-0173 D2: the comparison-work count and the investigation-ref reads
// answer one question and must run in one read transaction. The operator
// question reader therefore opens its own read-only snapshot and every read
// below flows through it; no read may bypass the transaction through the
// store handle. This structural check fails when a read is reattached to
// s.db, because a behavioral test cannot interleave a writer between two
// reads of one serialized call.
func TestReadWorkflowOperatorQuestionReadsOneTransaction(t *testing.T) {
	t.Parallel()
	_, thisFile, _, _ := runtime.Caller(0)
	sourcePath := filepath.Join(filepath.Dir(thisFile), "workflow_operator.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, sourcePath, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "ReadWorkflowOperatorQuestion" {
			body = fn.Body
			break
		}
	}
	if body == nil {
		t.Fatal("ReadWorkflowOperatorQuestion not found in internal/store/workflow_operator.go")
	}
	var dbRefs int
	var beginTxReceiverPos token.Pos
	opensReadOnlyTx := false
	ast.Inspect(body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.SelectorExpr:
			if x, ok := n.X.(*ast.Ident); ok && x.Name == "s" && n.Sel.Name == "db" {
				dbRefs++
			}
		case *ast.CallExpr:
			sel, ok := n.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "BeginTx" {
				return true
			}
			if inner, ok := sel.X.(*ast.SelectorExpr); ok {
				if x, ok := inner.X.(*ast.Ident); ok && x.Name == "s" && inner.Sel.Name == "db" {
					beginTxReceiverPos = inner.Pos()
				}
			}
			for _, arg := range n.Args {
				var lit *ast.CompositeLit
				switch a := arg.(type) {
				case *ast.CompositeLit:
					lit = a
				case *ast.UnaryExpr:
					if a.Op == token.AND {
						if c, ok := a.X.(*ast.CompositeLit); ok {
							lit = c
						}
					}
				}
				if lit == nil {
					continue
				}
				if sel, ok := lit.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "TxOptions" {
					for _, elt := range lit.Elts {
						kv, ok := elt.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "ReadOnly" {
							opensReadOnlyTx = true
						}
					}
				}
			}
		}
		return true
	})
	if !opensReadOnlyTx {
		t.Error("ReadWorkflowOperatorQuestion must open one read-only transaction for its reads (CD-0173 D2)")
	}
	allowed := 1 // the open guard is the only other permitted use
	if beginTxReceiverPos.IsValid() {
		allowed++ // the transaction is opened from the store handle itself
	}
	if dbRefs > allowed {
		t.Errorf("ReadWorkflowOperatorQuestion references s.db %d times; only the open guard and the BeginTx receiver are permitted, every read must flow through the transaction", dbRefs)
	}
}

// Every caller of the investigation-artifact precondition must pass one
// transaction, never the pooled store handle, so its Product count and its
// ref reads share one snapshot (CD-0173 D2).
func TestInvestigationArtifactPreconditionNeverReadsThroughTheStoreHandle(t *testing.T) {
	t.Parallel()
	_, thisFile, _, _ := runtime.Caller(0)
	sourcePath := filepath.Join(filepath.Dir(thisFile), "workflow_operator.go")
	file, err := parser.ParseFile(token.NewFileSet(), sourcePath, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		fn, ok := call.Fun.(*ast.Ident)
		if !ok || fn.Name != "requireRecordedInvestigationArtifact" || len(call.Args) < 2 {
			return true
		}
		calls++
		if sel, ok := call.Args[1].(*ast.SelectorExpr); ok && sel.Sel.Name == "db" {
			t.Errorf("requireRecordedInvestigationArtifact is called with the store handle at offset %d; pass a transaction", call.Pos())
		}
		return true
	})
	if calls == 0 {
		t.Fatal("no requireRecordedInvestigationArtifact call found in internal/store/workflow_operator.go")
	}
}
