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

// applyWorkflowActionRawTx decides which event a completed action appends across
// three separate branches: the ActionCheckpoint arm returns before the semantic
// switch, "complete" is excluded by a guard, and everything else either matches a
// case arm in workflowSemanticActionEvents or falls to its default and receives a
// generic WorkflowActionCompleted.
//
// That default is silent. An action that should carry a typed event but has no
// case arm still succeeds and still advances the workflow; only the persisted
// event is wrong. These tests bind the declared EventShape to the dispatcher so
// the two cannot disagree, in either direction.

var actionLabel = regexp.MustCompile(`^[a-z_]+$`)

// semanticCaseActions returns the action IDs workflowSemanticActionEvents builds
// a typed event for — the case labels of its switch on request.ActionID.
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
		t.Fatal("workflowSemanticActionEvents is not declared in workflow_dispatch.go; " +
			"update this test to name the function that builds typed action events")
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

func TestTypedEventShapeMatchesTheSemanticDispatcher(t *testing.T) {
	cases := semanticCaseActions(t)
	var undeclared, unbuilt []string
	for action, policy := range builtinActionPolicies {
		switch {
		case policy.EventShape == ActionEventTyped && !cases[action]:
			unbuilt = append(unbuilt, action)
		case policy.EventShape != ActionEventTyped && cases[action]:
			undeclared = append(undeclared, action)
		}
	}
	if len(unbuilt) != 0 {
		sort.Strings(unbuilt)
		t.Errorf("actions declared ActionEventTyped that workflowSemanticActionEvents has no case arm for: %v\n"+
			"these reach its default and append a generic WorkflowActionCompleted instead — the action succeeds and the event is wrong. "+
			"Add the case arm, or declare the action ActionEventGeneric", unbuilt)
	}
	if len(undeclared) != 0 {
		sort.Strings(undeclared)
		t.Errorf("actions with a case arm in workflowSemanticActionEvents but not declared ActionEventTyped: %v\n"+
			"declare them ActionEventTyped in builtinActionPolicies, or remove the arm", undeclared)
	}
}

func TestCheckpointEventShapeMatchesExecutionMode(t *testing.T) {
	cases := semanticCaseActions(t)
	for action, policy := range builtinActionPolicies {
		checkpointShape := policy.EventShape == ActionEventCheckpoint
		checkpointMode := policy.ExecutionMode == ActionCheckpoint
		if checkpointShape != checkpointMode {
			t.Errorf("%s declares EventShape %q with ExecutionMode %q; applyWorkflowActionRawTx appends "+
				"WorkflowActionCheckpointed for exactly the ActionCheckpoint actions, so the two must agree",
				action, policy.EventShape, policy.ExecutionMode)
		}
		if checkpointMode && cases[action] {
			t.Errorf("%s runs in ActionCheckpoint mode and also has a case arm in workflowSemanticActionEvents; "+
				"the checkpoint branch returns before the semantic switch, so that arm is unreachable", action)
		}
	}
}

func TestEveryActionDeclaresOneKnownEventShape(t *testing.T) {
	known := map[ActionEventShape]bool{
		ActionEventCheckpoint: true,
		ActionEventTyped:      true,
		ActionEventCompletion: true,
		ActionEventGeneric:    true,
	}
	var completion []string
	for action, policy := range builtinActionPolicies {
		if !known[policy.EventShape] {
			t.Errorf("%s declares unknown EventShape %q", action, policy.EventShape)
		}
		if policy.EventShape == ActionEventCompletion {
			completion = append(completion, action)
		}
	}
	// applyWorkflowActionRawTx excludes exactly one action from the semantic
	// switch by name. If that guard ever covers a second action, the shape stops
	// being derivable from the identifier and this declaration has to change with it.
	sort.Strings(completion)
	if len(completion) != 1 || completion[0] != "complete" {
		t.Errorf("ActionEventCompletion is held by %v; applyWorkflowActionRawTx guards the semantic switch "+
			`with request.ActionID != "complete", which admits exactly one action`, completion)
	}
}
