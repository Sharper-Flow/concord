package store

import (
	"bytes"
	"testing"
)

func TestBuiltinWorkflowDefinitionsCarryTypedExecutionModes(t *testing.T) {
	registry := NewBuiltinWorkflowRegistry()
	for _, definition := range BuiltinWorkflowDefinitions() {
		if definition.Version != authoredWorkflowDefinitionVersion {
			t.Fatalf("built-in %s version=%d, want %d", definition.Ref, definition.Version, authoredWorkflowDefinitionVersion)
		}
		for _, action := range definition.ActionDefinitions {
			if !validActionExecutionMode(action.ExecutionMode) {
				t.Fatalf("%s action %s has invalid execution mode %q", definition.Ref, action.ID, action.ExecutionMode)
			}
		}
		if _, ok := registry.Lookup(definition.Ref, definition.Version); !ok {
			t.Fatalf("%s is not registered", definition.Ref)
		}
	}
}

func TestWorkflowDefinitionEncodingCarriesModesAndProductTruth(t *testing.T) {
	registry := NewBuiltinWorkflowRegistry()
	registered, ok := registry.Lookup("workflow.implementation", authoredWorkflowDefinitionVersion)
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

// Every registered definition version keeps the digest recorded here. This is
// the cross-release guard for issue #861: mutating a released version in place
// changes its digest and fails this test naming the ref and version. Changing a
// family's content means a new authored version plus new golden rows, never an
// edit to an existing row.
func TestBuiltinWorkflowDigestsStayPinned(t *testing.T) {
	registry := NewBuiltinWorkflowRegistry()
	type versionPin struct {
		version int64
		digest  string
	}
	want := map[string][]versionPin{
		"workflow.implementation": {
			{1, "sha256:deaeec1077f5360b23b4c6ca78328d45a620668c503760855ec28e7bf6ecf155"},
			{2, "sha256:e16dfed665a50ece82f33040d2cb0e4a6abfd72dbc5b4743098eab22f0faab89"},
		},
		"workflow.break_fix": {
			{1, "sha256:aefce865f350345dc41fc1e2e988e7d5e246fa7fd560335399cf8c826e4cc35a"},
			{2, "sha256:d7f8d8cc8b951e74751ddafe95c7b9c9d65e606cd73c41b2ceadd5fa2cdf29cb"},
		},
		"workflow.research": {
			{1, "sha256:adeb334ee4eb08e1907b2f36c618d809675a81f325266733142e697a90c108b9"},
			{2, "sha256:7a987b5e2cbc9bafd7a80e92345efa35b331ccaa533aefe4722024485be57e4a"},
		},
		"workflow.architecture_spike": {
			{2, "sha256:7f7a7c0802daf0bed8e65744ef12480f14045f1d8c4048b80d35021448b83c07"},
		},
		"workflow.ops_runbook": {
			{2, "sha256:4b19ba4c81ffcb1fef8f2da8b45c47c9a0121f9531dcffabdd15af25c5b82dab"},
		},
		"workflow.static_analysis": {
			{2, "sha256:161b3b2b85d075b069cb6b9a9dda26cf226c82ba4e7ee5e7946a2792cd76ecac"},
		},
		"workflow.generic_one_off": {
			{1, "sha256:c2b8b4c8ef11b2de08912f7c82faa91dffe6a2fbe4ddcef924ff4b393da578b3"},
			{2, "sha256:273c82c0a0cf6c17d231f1be898ff74c6158f8036985cb3e1666b8f12c1b7895"},
		},
	}
	for ref, pins := range want {
		for _, pin := range pins {
			definition, ok := registry.Lookup(ref, pin.version)
			if !ok {
				t.Fatalf("%s version=%d is not registered", ref, pin.version)
			}
			if definition.Digest != pin.digest {
				t.Fatalf("%s version=%d digest=%s, want %s", ref, pin.version, definition.Digest, pin.digest)
			}
			if err := registry.Verify(ref, pin.version, pin.digest); err != nil {
				t.Fatalf("%s version=%d pinned digest does not verify: %v", ref, pin.version, err)
			}
		}
	}
}
