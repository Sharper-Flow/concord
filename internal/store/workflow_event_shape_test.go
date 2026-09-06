package store

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
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

// The checkpoint branch of assembleWorkflowActionEventsTx keys on the declared
// EventShape, so an action may append a typed checkpoint and still advance
// (record_decision). What must still agree: every ActionCheckpoint-mode action
// declares the checkpoint shape, since a checkpoint that appends no checkpoint
// event records nothing, and no checkpoint-shape action has a semantic case arm,
// since the checkpoint branch runs before the semantic switch.
func TestCheckpointEventShapeGovernsTheCheckpointBranch(t *testing.T) {
	source, err := os.ReadFile("workflow_action_guards.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(source, []byte("builtinActionPolicies[in.request.ActionID].EventShape == ActionEventCheckpoint {")) {
		t.Fatal("assembleWorkflowActionEventsTx does not key its checkpoint branch on the declared EventShape")
	}
	cases := semanticCaseActions(t)
	for action, policy := range builtinActionPolicies {
		checkpointShape := policy.EventShape == ActionEventCheckpoint
		if policy.ExecutionMode == ActionCheckpoint && !checkpointShape {
			t.Errorf("%s runs in ActionCheckpoint mode but declares EventShape %q; a checkpoint action must append WorkflowActionCheckpointed", action, policy.EventShape)
		}
		if checkpointShape && cases[action] {
			t.Errorf("%s declares the checkpoint EventShape and also has a case arm in workflowSemanticActionEvents; "+
				"the checkpoint branch runs before the semantic switch, so that arm is unreachable", action)
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
