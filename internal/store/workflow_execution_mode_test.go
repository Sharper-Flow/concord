package store

import (
	"bytes"
	"testing"
)

func TestBuiltinWorkflowDefinitionsCarryTypedExecutionModes(t *testing.T) {
	t.Parallel()
	registry := NewBuiltinWorkflowRegistry()
	for _, definition := range BuiltinWorkflowDefinitions() {
		for _, action := range definition.ActionDefinitions {
			if !validActionExecutionMode(action.ExecutionMode) {
				t.Fatalf("%s action %s has invalid execution mode %q", definition.Ref, action.ID, action.ExecutionMode)
			}
		}
		if _, ok := registry.Current(definition.Ref); !ok {
			t.Fatalf("%s is not registered", definition.Ref)
		}
	}
}

func TestWorkflowDefinitionEncodingCarriesModesAndProductTruth(t *testing.T) {
	t.Parallel()
	registry := NewBuiltinWorkflowRegistry()
	registered, ok := registry.Current("workflow.implementation")
	if !ok {
		t.Fatal("implementation definition is not registered")
	}
	canonical, err := CanonicalWorkflowDefinition(registered.Definition)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(canonical, []byte(`"execution_mode"`)) {
		t.Fatal("canonical definition omitted typed execution modes")
	}
	if !bytes.Contains(canonical, []byte(`"changes_product_truth"`)) {
		t.Fatal("canonical definition omitted product-truth classification")
	}
}

func TestWorkflowExecutionModesPreserveCurrentTransitionSemantics(t *testing.T) {
	t.Parallel()
	definition := BuiltinWorkflowDefinitions()[0]
	want := map[string]ActionExecutionMode{
		"record_proposal":        ActionAdvance,
		"approve_contract":       ActionAdvance,
		"start_execution":        ActionFenced,
		"checkpoint_execution":   ActionCheckpoint,
		"bind_evidence":          ActionHold,
		"record_verdict":         ActionHold,
		"confirm_premise":        ActionAdvance,
		"complete":               ActionHold,
		"checkpoint_context":     ActionHold,
		"cross_context_boundary": ActionHold,
		"record_delivery":        ActionAdvance,
		"checkpoint_refine":      ActionCheckpoint,
		"record_review":          ActionHold,
	}
	for actionID, expected := range want {
		mode, ok := workflowActionExecutionMode(definition, actionID)
		if !ok || mode != expected {
			t.Fatalf("action %s mode=%q found=%t, want %q", actionID, mode, ok, expected)
		}
	}
	// CD-0242 D6 retires worker-result acceptance and failure actions.
	for _, actionID := range []string{"accept_worker_result", "record_worker_failure"} {
		if _, ok := workflowActionExecutionMode(definition, actionID); ok {
			t.Fatalf("retired action %s still has an execution mode", actionID)
		}
	}
	// CD-0198 D3: the current head derives the refine start, so the
	// callable start_refine is no longer a declared action; the released
	// pins keep theirs.
	if _, ok := workflowActionExecutionMode(definition, "start_refine"); ok {
		t.Fatal("the current head still declares start_refine; the derived-start pin omits it")
	}
}

// An action the definition does not declare has no execution mode. The registry
// infers nothing from the action ID.
func TestUndeclaredActionHasNoExecutionMode(t *testing.T) {
	t.Parallel()
	definition := BuiltinWorkflowDefinitions()[0]
	if mode, ok := workflowActionExecutionMode(definition, "record_report"); ok {
		t.Fatalf("undeclared action resolved mode=%q, want no mode", mode)
	}
}

func TestDefinitionRejectsMissingExecutionMode(t *testing.T) {
	t.Parallel()
	definition := BuiltinWorkflowDefinitions()[0]
	definition.ActionDefinitions[0].ExecutionMode = ""
	if err := ValidateWorkflowDefinition(definition); err == nil {
		t.Fatal("definition without execution_mode passed validation")
	}
	if _, err := NewWorkflowDefinitionRegistry().Register(definition); err == nil {
		t.Fatal("definition without execution_mode was registered")
	}
}
