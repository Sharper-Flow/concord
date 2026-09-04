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
		"cross_context_boundary": ActionAdvance,
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
		"workflow.implementation":     "sha256:b5386551391c54dd6521ba2f46ac5a6de5701d96ccfb9f7be4eb0e218b848dc4",
		"workflow.break_fix":          "sha256:67969df4130be5d45f7d33d82bf3f3a0950836f9d6b5a67aeabb7a068ed8df0b",
		"workflow.research":           "sha256:299cbfd6ba89c27fcd748b69b3c1a2b674e998674d98d51d22255c0574d3fc66",
		"workflow.architecture_spike": "sha256:f54520ef28174d9212a9adace8e49d814aa096e2c75f9d7d38682ac50979deda",
		"workflow.ops_runbook":        "sha256:0e520c182c5d6b3aabacdbe4049dff9e5f00433937ccd9bb7943d61ada2dc56a",
		"workflow.static_analysis":    "sha256:0654940af9ce6cac0b93f3f3333ad1e2fcb34cfd58f29d9835655b2209ff7742",
		"workflow.generic_one_off":    "sha256:d3715162c04ea7456c6f8977b7f95c56449130ba781472c111400372196a690f",
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
