package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInvestigationArtifactPreconditionNeverReadsThroughTheStoreHandle(t *testing.T) {
	t.Parallel()
	_, thisFile, _, _ := runtime.Caller(0)
	for _, name := range []string{"workflow_event_constructors.go", "workflow_admission.go"} {
		calls := 0
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(filepath.Dir(thisFile), name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
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
				t.Errorf("%s: investigation precondition reads through the store handle at offset %d", name, call.Pos())
			}
			return true
		})
		if calls == 0 {
			t.Errorf("%s: no investigation precondition call found", name)
		}
	}
}
