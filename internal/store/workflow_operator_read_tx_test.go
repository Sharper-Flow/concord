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
// answer one question and must run in one read transaction. ReadWorkPin
// opens the one read-only snapshot for the pin read, and every read below
// it flows through that snapshot; no read may bypass the
// transaction through the store handle. This structural check fails when a
// read is reattached to s.db, because a behavioral test cannot interleave a
// writer between two reads of one serialized call. It also fails when the
// workflow projection reads the question on any queryer but the one its
// caller's snapshot supplied.
func TestReadWorkflowOperatorQuestionReadsOneTransaction(t *testing.T) {
	t.Parallel()
	_, thisFile, _, _ := runtime.Caller(0)
	storeDir := filepath.Dir(thisFile)

	ownerPath := filepath.Join(storeDir, "workpin.go")
	fset := token.NewFileSet()
	ownerFile, err := parser.ParseFile(fset, ownerPath, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ownerBody *ast.BlockStmt
	for _, decl := range ownerFile.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "ReadWorkPin" {
			ownerBody = fn.Body
			break
		}
	}
	if ownerBody == nil {
		t.Fatal("ReadWorkPin not found in internal/store/workpin.go")
	}
	readBegins, dbRefs := operatorQuestionReadSnapshotCounts(ownerBody)
	if readBegins != 1 {
		t.Errorf("ReadWorkPin must open exactly one read-only transaction for its reads (CD-0173 D2); got %d", readBegins)
	}
	allowed := 1 + readBegins // the open guard and the read owner's database argument
	if dbRefs > allowed {
		t.Errorf("ReadWorkPin references s.db %d times; only the open guard and the beginReadTx argument are permitted, every read must flow through the transaction", dbRefs)
	}

	projectionPath := filepath.Join(storeDir, "workflow_read.go")
	projectionFile, err := parser.ParseFile(fset, projectionPath, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	readers := map[string]*ast.FuncDecl{}
	for _, decl := range projectionFile.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			readers[fn.Name.Name] = fn
		}
	}
	// Both edges must carry the caller's original queryer binding. Neither
	// reader may replace it, use the pool, or open another snapshot.
	for _, edge := range []struct{ caller, callee string }{
		{"readWorkflowProjectionTx", "readWorkflowProjectionContractTx"},
		{"readWorkflowProjectionContractTx", "workflowOperatorQuestionTx"},
	} {
		caller := readers[edge.caller]
		if caller == nil {
			t.Fatalf("%s not found in internal/store/workflow_read.go", edge.caller)
		}
		reads, foreignReads := operatorQuestionQueryerEdgeCounts(caller, edge.callee)
		if reads < 1 {
			t.Errorf("%s must pass its original queryer to %s (CD-0173 D2); found %d such calls", edge.caller, edge.callee, reads)
		}
		if foreignReads != 0 {
			t.Errorf("%s has %d queryer substitutions or reads outside its caller's snapshot (CD-0173 D2)", edge.caller, foreignReads)
		}
	}
}

func operatorQuestionQueryerEdgeCounts(caller *ast.FuncDecl, callee string) (int, int) {
	if caller.Type.Params == nil || len(caller.Type.Params.List) < 2 || len(caller.Type.Params.List[1].Names) != 1 {
		return 0, 1
	}
	queryer := caller.Type.Params.List[1].Names[0]
	if queryer.Obj == nil {
		return 0, 1
	}
	isQueryer := func(expr ast.Expr) bool {
		ident, ok := expr.(*ast.Ident)
		return ok && ident.Obj == queryer.Obj
	}
	reads, foreignReads := 0, 0
	ast.Inspect(caller.Body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.AssignStmt:
			for _, target := range n.Lhs {
				if isQueryer(target) {
					foreignReads++
				}
			}
		case *ast.RangeStmt:
			if isQueryer(n.Key) || isQueryer(n.Value) {
				foreignReads++
			}
		case *ast.UnaryExpr:
			if n.Op == token.AND && isQueryer(n.X) {
				foreignReads++
			}
		case *ast.SelectorExpr:
			if n.Sel.Name == "db" {
				foreignReads++
			}
		case *ast.CallExpr:
			fn, ok := n.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			switch fn.Name {
			case "ReadWorkPin", "beginReadTx":
				foreignReads++
			default:
				if fn.Name == callee || fn.Name == "workflowOperatorQuestionTx" || fn.Name == "workflowOperatorQuestionForDefinitionTx" {
					if len(n.Args) < 2 || !isQueryer(n.Args[1]) {
						foreignReads++
					} else if fn.Name == callee {
						reads++
					}
				}
			}
		}
		return true
	})
	return reads, foreignReads
}

func operatorQuestionReadSnapshotCounts(body *ast.BlockStmt) (int, int) {
	readBegins, dbRefs := 0, 0
	ast.Inspect(body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.SelectorExpr:
			if x, ok := n.X.(*ast.Ident); ok && x.Name == "s" && n.Sel.Name == "db" {
				dbRefs++
			}
		case *ast.CallExpr:
			fn, ok := n.Fun.(*ast.Ident)
			if !ok || fn.Name != "beginReadTx" || len(n.Args) != 2 {
				return true
			}
			if inner, ok := n.Args[1].(*ast.SelectorExpr); ok {
				if x, ok := inner.X.(*ast.Ident); ok && x.Name == "s" && inner.Sel.Name == "db" {
					readBegins++
				}
			}
		}
		return true
	})
	return readBegins, dbRefs
}

func TestOperatorQuestionReadSnapshotGuardBites(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body string
		valid      bool
	}{
		{"one snapshot", `if s.db == nil { return }; tx, _ := beginReadTx(ctx, s.db); tx.QueryRowContext(ctx, "SELECT 1")`, true},
		{"pool read", `if s.db == nil { return }; tx, _ := beginReadTx(ctx, s.db); s.db.QueryRowContext(ctx, "SELECT 1"); _ = tx`, false},
		{"two snapshots", `if s.db == nil { return }; beginReadTx(ctx, s.db); beginReadTx(ctx, s.db)`, false},
		{"no snapshot", `if s.db == nil { return }; s.db.QueryRowContext(ctx, "SELECT 1")`, false},
		{"wrong database", `if s.db == nil { return }; beginReadTx(ctx, other)`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package store; func read() {"+tt.body+"}", 0)
			if err != nil {
				t.Fatal(err)
			}
			body := file.Decls[0].(*ast.FuncDecl).Body
			begins, refs := operatorQuestionReadSnapshotCounts(body)
			valid := begins == 1 && refs <= 1+begins
			if valid != tt.valid {
				t.Fatalf("begins=%d pool references=%d: valid=%t, want %t", begins, refs, valid, tt.valid)
			}
		})
	}
}

func TestOperatorQuestionQueryerEdgeGuardBites(t *testing.T) {
	t.Parallel()
	for _, edge := range []struct{ caller, callee string }{
		{"readWorkflowProjectionTx", "readWorkflowProjectionContractTx"},
		{"readWorkflowProjectionContractTx", "workflowOperatorQuestionTx"},
	} {
		t.Run(edge.caller, func(t *testing.T) {
			call := edge.callee + `(ctx, q)`
			for _, tt := range []struct {
				name, body string
				valid      bool
			}{
				{"original queryer", call, true},
				{"missing edge", `_ = q`, false},
				{"pool argument", edge.callee + `(ctx, s.db)`, false},
				{"other queryer", edge.callee + `(ctx, other)`, false},
				{"missing argument", edge.callee + `(ctx)`, false},
				{"reassigned queryer", `q = other; ` + call, false},
				{"redeclared queryer", `q, err := other, nil; _ = err; ` + call, false},
				{"shadowed queryer", `{ q := other; ` + call + ` }`, false},
				{"range replacement", `for q = range others { ` + call + ` }`, false},
				{"queryer address escapes", `replace(&q); ` + call, false},
				{"extra pool read", `s.db.QueryRowContext(ctx, "SELECT 1"); ` + call, false},
				{"new snapshot", `beginReadTx(ctx, other); ` + call, false},
				{"nested pin snapshot", `ReadWorkPin(ctx, s, workID); ` + call, false},
				{"extra foreign question read", call + `; workflowOperatorQuestionForDefinitionTx(ctx, other)`, false},
			} {
				t.Run(tt.name, func(t *testing.T) {
					source := "package store; func " + edge.caller + "(ctx context.Context, q queryer) {" + tt.body + "}"
					file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, 0)
					if err != nil {
						t.Fatal(err)
					}
					reads, foreignReads := operatorQuestionQueryerEdgeCounts(file.Decls[0].(*ast.FuncDecl), edge.callee)
					valid := reads >= 1 && foreignReads == 0
					if valid != tt.valid {
						t.Fatalf("same-queryer calls=%d foreign reads=%d: valid=%t, want %t", reads, foreignReads, valid, tt.valid)
					}
				})
			}
		})
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
