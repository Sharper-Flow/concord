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

// semanticCaseActions returns typed action IDs from both branches of the plan
// dispatcher. The AST check also verifies that the plan-control map feeds the
// typed-event branch and that its fallback reaches the semantic dispatcher.
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
	var dispatch *ast.BlockStmt
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "workflowPlanActionEventsTx" {
			dispatch = fn.Body
		}
	}
	if dispatch == nil {
		t.Fatal("plan action dispatcher not found")
	}
	var planMap *ast.CompositeLit
	var branch *ast.IfStmt
	ast.Inspect(dispatch, func(node ast.Node) bool {
		if assign, ok := node.(*ast.AssignStmt); ok && len(assign.Lhs) == 1 && len(assign.Rhs) == 1 {
			if id, ok := assign.Lhs[0].(*ast.Ident); ok && id.Name == "planKinds" {
				planMap, _ = assign.Rhs[0].(*ast.CompositeLit)
			}
		}
		if stmt, ok := node.(*ast.IfStmt); ok && stmt.Init != nil {
			if init, ok := stmt.Init.(*ast.AssignStmt); ok && len(init.Lhs) == 2 && len(init.Rhs) == 1 {
				if index, ok := init.Rhs[0].(*ast.IndexExpr); ok {
					name, nameOK := index.X.(*ast.Ident)
					kind, kindOK := init.Lhs[0].(*ast.Ident)
					present, presentOK := init.Lhs[1].(*ast.Ident)
					cond, condOK := stmt.Cond.(*ast.Ident)
					if nameOK && name.Name == "planKinds" && exprText(index.Index) == "request.ActionID" && kindOK && kind.Name == "kind" && presentOK && condOK && cond.Name == present.Name {
						branch = stmt
					}
				}
			}
		}
		return true
	})
	hasCall := func(node ast.Node, name string) bool {
		found := false
		ast.Inspect(node, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == name {
					found = true
				}
			}
			return true
		})
		return found
	}
	if planMap == nil || branch == nil || !hasCall(branch.Body, "workflowTypedEvent") || !hasCall(dispatch, "assemblePlanWorkflowActionEventsTx") {
		t.Fatal("plan-control map is not connected to the typed event and semantic fallback branches")
	}
	typedKind := false
	ast.Inspect(branch.Body, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok && len(call.Args) > 1 {
			if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == "workflowTypedEvent" {
				if kind, ok := call.Args[1].(*ast.Ident); ok && kind.Name == "kind" {
					typedKind = true
				}
			}
		}
		return true
	})
	if !typedKind {
		t.Fatal("plan-control branch does not use the mapped typed event kind")
	}
	guards, err := parser.ParseFile(fset, "workflow_action_guards.go", nil, 0)
	if err != nil {
		t.Fatalf("parse workflow_action_guards.go: %v", err)
	}
	var assembly *ast.BlockStmt
	for _, decl := range guards.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "assemblePlanWorkflowActionEventsTx" {
			assembly = fn.Body
		}
	}
	if assembly == nil || !hasCall(assembly, "workflowSemanticActionEvents") {
		t.Fatal("plan action assembly does not call the semantic dispatcher")
	}
	for _, element := range planMap.Elts {
		entry, ok := element.(*ast.KeyValueExpr)
		if !ok {
			t.Fatal("plan-control map has a non-keyed entry")
		}
		key, ok := entry.Key.(*ast.BasicLit)
		if !ok || key.Kind != token.STRING {
			t.Fatal("plan-control action key is not a string literal")
		}
		value, err := strconv.Unquote(key.Value)
		if err != nil || !actionLabel.MatchString(value) {
			t.Fatalf("invalid plan-control action key %q", key.Value)
		}
		actions[value] = true
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
	t.Parallel()
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
		t.Errorf("typed actions missing from the plan and semantic dispatchers: %v", unbuilt)
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
	t.Parallel()
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
	t.Parallel()
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

// TestEvidenceBinderCaseArmMatchesTheProducerTable retired with the fixed
// definitions' reachability walk (CON-1574, workflow-engine-contract
// §§2.1/12): the dispatcher case arm is a literal list with no producer
// table to mirror.
