package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"testing"
)

var actionLabel = regexp.MustCompile(`^[a-z_]+$`)

func semanticCaseActions(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "workflow_dispatch.go", nil, 0)
	if err != nil {
		t.Fatalf("parse workflow_dispatch.go: %v", err)
	}
	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "workflowSemanticActionEvents" {
			body = fn.Body
		}
	}
	if body == nil {
		t.Fatal("workflowSemanticActionEvents is not declared in workflow_dispatch.go")
	}
	actions := map[string]bool{}
	ast.Inspect(body, func(node ast.Node) bool {
		stmt, ok := node.(*ast.SwitchStmt)
		if !ok || exprText(stmt.Tag) != "request.ActionID" {
			return true
		}
		for _, item := range stmt.Body.List {
			clause, ok := item.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, expr := range clause.List {
				lit, ok := expr.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(lit.Value)
				if err == nil && actionLabel.MatchString(value) {
					actions[value] = true
				}
			}
		}
		return true
	})
	if len(actions) == 0 {
		t.Fatal("found no case labels on request.ActionID in workflowSemanticActionEvents")
	}
	return actions
}

func exprText(expr ast.Expr) string {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	ident, ok := selector.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return ident.Name + "." + selector.Sel.Name
}

func TestActionShapeTypedSemanticMatchesDispatcher(t *testing.T) {
	cases := semanticCaseActions(t)
	var unbuilt, undeclared []string
	for action, policy := range builtinActionPolicies {
		if policy.EventShape == shapeTypedSemantic && !cases[action] {
			unbuilt = append(unbuilt, action)
		}
	}
	for action := range cases {
		policy, ok := builtinActionPolicies[action]
		if !ok || policy.EventShape != shapeTypedSemantic {
			undeclared = append(undeclared, action)
		}
	}
	if len(unbuilt) != 0 {
		sort.Strings(unbuilt)
		t.Errorf("actions declared shapeTypedSemantic without a workflowSemanticActionEvents case arm: %v", unbuilt)
	}
	if len(undeclared) != 0 {
		sort.Strings(undeclared)
		t.Errorf("workflowSemanticActionEvents case arms not declared shapeTypedSemantic: %v", undeclared)
	}
}

func TestActionShapeCheckpointMatchesExecutionMode(t *testing.T) {
	cases := semanticCaseActions(t)
	for action, policy := range builtinActionPolicies {
		checkpointShape := policy.EventShape == shapeCheckpoint
		checkpointMode := policy.ExecutionMode == ActionCheckpoint
		if checkpointShape != checkpointMode {
			t.Errorf("%s declares EventShape %q with ExecutionMode %q; checkpoint shape must match ActionCheckpoint mode", action, policy.EventShape, policy.ExecutionMode)
		}
		if checkpointMode && cases[action] {
			t.Errorf("%s is ActionCheckpoint and also has a semantic case arm; the arm is unreachable", action)
		}
	}
}

func TestActionShapePartition(t *testing.T) {
	expected := map[actionEventShape]int{
		shapeCheckpoint:    7,
		shapeTypedSemantic: 21,
		shapeCompletion:    1,
		shapeGeneric:       18,
	}
	counts := map[actionEventShape]int{}
	for action, policy := range builtinActionPolicies {
		if _, ok := expected[policy.EventShape]; !ok {
			t.Errorf("%s declares unknown EventShape %q", action, policy.EventShape)
			continue
		}
		counts[policy.EventShape]++
	}
	if len(builtinActionPolicies) != 47 {
		t.Errorf("builtinActionPolicies contains %d actions; expected 47", len(builtinActionPolicies))
	}
	for shape, want := range expected {
		if got := counts[shape]; got != want {
			t.Errorf("EventShape %q has %d actions; expected %d", shape, got, want)
		}
	}
	if counts[shapeCheckpoint]+counts[shapeTypedSemantic]+counts[shapeCompletion]+counts[shapeGeneric] != len(builtinActionPolicies) {
		t.Errorf("event-shape counts do not partition builtinActionPolicies: counts=%v actions=%d", counts, len(builtinActionPolicies))
	}
	completion := []string{}
	for action, policy := range builtinActionPolicies {
		if policy.EventShape == shapeCompletion {
			completion = append(completion, action)
		}
	}
	sort.Strings(completion)
	if len(completion) != 1 || completion[0] != "complete" {
		t.Errorf("shapeCompletion is held by %v; applyWorkflowActionRawTx guards only action complete", completion)
	}
}
