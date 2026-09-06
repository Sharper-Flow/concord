package store

import (
	"bytes"
	"testing"
)

func TestBuiltinWorkflowDefinitionsCarryTypedExecutionModes(t *testing.T) {
	registry := NewBuiltinWorkflowRegistry()
	for _, definition := range BuiltinWorkflowDefinitions() {
		if definition.Version != 1 {
			t.Fatalf("built-in %s version=%d, want 1", definition.Ref, definition.Version)
		}
		for _, action := range definition.ActionDefinitions {
			if !validActionExecutionMode(action.ExecutionMode) {
				t.Fatalf("%s action %s has invalid execution mode %q", definition.Ref, action.ID, action.ExecutionMode)
			}
		}
		if _, ok := registry.Lookup(definition.Ref, 1); !ok {
			t.Fatalf("%s is not registered", definition.Ref)
		}
	}
}

func TestWorkflowDefinitionEncodingCarriesModesAndProductTruth(t *testing.T) {
	registry := NewBuiltinWorkflowRegistry()
	registered, ok := registry.Lookup("workflow.implementation", 1)
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
		"accept_worker_result":   ActionAdvance,
	}
	for actionID, expected := range want {
		mode, ok := workflowActionExecutionMode(definition, actionID)
		if !ok || mode != expected {
			t.Fatalf("action %s mode=%q found=%t, want %q", actionID, mode, ok, expected)
		}
	}
}

// An action the definition does not declare has no execution mode. The registry
// infers nothing from the action ID.
func TestUndeclaredActionHasNoExecutionMode(t *testing.T) {
	definition := BuiltinWorkflowDefinitions()[0]
	if mode, ok := workflowActionExecutionMode(definition, "record_report"); ok {
		t.Fatalf("undeclared action resolved mode=%q, want no mode", mode)
	}
}

func TestDefinitionRejectsMissingExecutionMode(t *testing.T) {
	definition := BuiltinWorkflowDefinitions()[0]
	definition.ActionDefinitions[0].ExecutionMode = ""
	if err := ValidateWorkflowDefinition(definition); err == nil {
		t.Fatal("definition without execution_mode passed validation")
	}
	if _, err := NewWorkflowDefinitionRegistry().Register(definition); err == nil {
		t.Fatal("definition without execution_mode was registered")
	}
}

// The shipped definition digests are the identity the conformance corpus pins.
// A change here is a change to every pinned scenario.
func TestBuiltinWorkflowDigestsStayPinned(t *testing.T) {
	registry := NewBuiltinWorkflowRegistry()
	want := map[string]string{
		"workflow.implementation":     "sha256:90fed5c22d8493fd4b4d20ecdd28fd4dbccfb8f0aaef4645b8990a704b12ab50",
		"workflow.break_fix":          "sha256:8abed1bdb47f7cac3b6229a5a72fa6afb9cdd2011d7c42da586b6ccd148cec83",
		"workflow.research":           "sha256:63f50830a7fe0d5d6dbf2c801c2f04f5b324ed0c5241292d58cfc0ef4ddddab8",
		"workflow.architecture_spike": "sha256:97d09dd24f80750dfa403ac2ccb9bf17b046cbd04981358b7ebaf1b1076aef5e",
		"workflow.ops_runbook":        "sha256:2e681414f079418a9aa2c7260832837d7ecde0e3974fdd602749d8125072dba6",
		"workflow.static_analysis":    "sha256:dcd49187f4c5f3f3a54aa3a54cf28ce1eae5a4e093203ec761cf314b2b1bdd91",
		"workflow.generic_one_off":    "sha256:8e59cbbe8f20589064a975d8b3935c20edd573d6f6f4f9858dabea96bce6ba80",
	}
	for ref, expected := range want {
		definition, ok := registry.Lookup(ref, 1)
		if !ok {
			t.Fatalf("%s is not registered", ref)
		}
		if definition.Digest != expected {
			t.Fatalf("%s digest=%s, want %s", ref, definition.Digest, expected)
		}
	}
}
