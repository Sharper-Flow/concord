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
		"workflow.implementation":     "sha256:179f8a9a42bc2ece30d555c02b383da6047882292a8ac45504f918f032d62431",
		"workflow.break_fix":          "sha256:539b390324a29cd2996e1889160bb9a6ce831deb9d029eb5936ea66aa744e898",
		"workflow.research":           "sha256:adeb334ee4eb08e1907b2f36c618d809675a81f325266733142e697a90c108b9",
		"workflow.architecture_spike": "sha256:a6a5f1f88f5b4e546ec566e7175f6700c56e32dc557d183c6538a504b41225ac",
		"workflow.ops_runbook":        "sha256:8f393c2168924b420c54203f057cea79ce00127a497e31cc8b52e5c093d0e03f",
		"workflow.static_analysis":    "sha256:6e63731bb8f245d772eb85ce7de5f4fe23edd766775cfd4cdea7b728c3b8801a",
		"workflow.generic_one_off":    "sha256:ba903197f3786f03e099bfaef35d188b64d6cfc2cc33d9b2ff4a7931b666775f",
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
