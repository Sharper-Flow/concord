package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This test derives the action-to-field reads from dispatcher control flow and
// compares them with the current registry contracts. It contains no duplicate
// expected field inventory. The only exclusions are the singular approval
// compatibility fields and stale-law recovery, whose public contracts are
// explicitly outside issue #776.
func TestDispatcherFieldReadsHaveActionPayloadDeclarations(t *testing.T) {
	files := map[string]*ast.File{}
	fset := token.NewFileSet()
	for _, name := range []string{"workflow_dispatch.go", "workflow_action_guards.go"} {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files[name] = file
	}

	reads := map[string]map[string]bool{}
	add := func(action string, fields map[string]bool) {
		if reads[action] == nil {
			reads[action] = map[string]bool{}
		}
		for field := range fields {
			reads[action][field] = true
		}
	}

	dispatch := workflowTestFunction(t, files["workflow_dispatch.go"], "workflowSemanticActionEvents")
	ast.Inspect(dispatch.Body, func(node ast.Node) bool {
		switchStatement, ok := node.(*ast.SwitchStmt)
		if !ok || workflowTestExpression(switchStatement.Tag) != "request.ActionID" {
			return true
		}
		for _, statement := range switchStatement.Body.List {
			clause, ok := statement.(*ast.CaseClause)
			if !ok {
				continue
			}
			fields := workflowTestFieldReads(clause, "fields")
			for _, expression := range clause.List {
				literal, ok := expression.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				action, err := strconv.Unquote(literal.Value)
				if err == nil {
					add(action, fields)
				}
			}
		}
		return false
	})

	for _, binding := range []struct {
		file    string
		name    string
		actions []string
	}{
		{"workflow_dispatch.go", "workflowCompletionBoundaryPreflight", []string{"complete"}},
		{"workflow_dispatch.go", "workflowCompletionEvent", []string{"complete"}},
		{"workflow_dispatch.go", "workflowContractOutcomePredicates", []string{"approve_contract"}},
		{"workflow_action_guards.go", "guardRecoveryEvidenceBind", []string{"bind_evidence"}},
		{"workflow_action_guards.go", "guardForwardLinkOnly", []string{"link_successor"}},
		{"workflow_action_guards.go", "guardNoRestartDispatch", []string{"cross_context_boundary"}},
		{"workflow_action_guards.go", "lateBindWorkflowEvidenceTx", []string{"complete"}},
	} {
		fields := workflowTestFieldReads(workflowTestFunction(t, files[binding.file], binding.name).Body, "fields")
		for _, action := range binding.actions {
			add(action, fields)
		}
	}

	for _, binding := range []struct {
		file string
		name string
	}{
		{"workflow_dispatch.go", "workflowActionEvidenceRefs"},
		{"workflow_action_guards.go", "appendGenericWorkflowCompletion"},
	} {
		function := workflowTestFunction(t, files[binding.file], binding.name)
		ast.Inspect(function.Body, func(node ast.Node) bool {
			condition, ok := node.(*ast.IfStmt)
			if !ok {
				return true
			}
			action := workflowTestActionEquality(condition.Cond)
			if action != "" {
				add(action, workflowTestFieldReads(condition.Body, "fields"))
			}
			return true
		})
	}

	legacyApprovalFields := map[string]bool{"outcome": true, "outcome_kind": true, "outcome_payload": true, "payload": true}
	for field := range legacyApprovalFields {
		delete(reads["approve_contract"], field)
	}
	delete(reads, "supersede_contract")
	delete(reads, "reject_worker_result")

	declared := map[string]map[string]bool{}
	for _, definition := range BuiltinWorkflowDefinitions() {
		for _, action := range definition.ActionDefinitions {
			if declared[action.ID] == nil {
				declared[action.ID] = map[string]bool{}
			}
			for _, field := range action.Payload.Fields {
				declared[action.ID][field.Name] = true
			}
		}
	}
	var missing []string
	for action, fields := range reads {
		for field := range fields {
			if !declared[action][field] {
				missing = append(missing, action+"."+field)
			}
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		t.Fatalf("dispatcher fields missing from their current action payload contracts: %s", strings.Join(missing, ", "))
	}
}

func workflowTestFunction(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == name {
			return function
		}
	}
	t.Fatalf("function %s is absent", name)
	return nil
}

func workflowTestFieldReads(node ast.Node, object string) map[string]bool {
	fields := map[string]bool{}
	ast.Inspect(node, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.CallExpr:
			identifier, ok := value.Fun.(*ast.Ident)
			fieldReader := ok && (strings.HasPrefix(identifier.Name, "workflowField") || identifier.Name == "workflowAuthenticatedActorField")
			if !fieldReader || len(value.Args) < 2 {
				return true
			}
			root, ok := value.Args[0].(*ast.Ident)
			literal, literalOK := value.Args[1].(*ast.BasicLit)
			if !ok || root.Name != object || !literalOK || literal.Kind != token.STRING {
				return true
			}
			if name, err := strconv.Unquote(literal.Value); err == nil {
				fields[name] = true
			}
		case *ast.IndexExpr:
			root, ok := value.X.(*ast.Ident)
			literal, literalOK := value.Index.(*ast.BasicLit)
			if !ok || root.Name != object || !literalOK || literal.Kind != token.STRING {
				return true
			}
			if name, err := strconv.Unquote(literal.Value); err == nil {
				fields[name] = true
			}
		}
		return true
	})
	return fields
}

func workflowTestExpression(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return workflowTestExpression(value.X) + "." + value.Sel.Name
	}
	return ""
}

func workflowTestActionEquality(expression ast.Expr) string {
	binary, ok := expression.(*ast.BinaryExpr)
	if !ok || binary.Op != token.EQL {
		return ""
	}
	for _, pair := range [][2]ast.Expr{{binary.X, binary.Y}, {binary.Y, binary.X}} {
		if !strings.HasSuffix(workflowTestExpression(pair[0]), ".ActionID") {
			continue
		}
		literal, ok := pair[1].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			continue
		}
		action, _ := strconv.Unquote(literal.Value)
		return action
	}
	return ""
}
