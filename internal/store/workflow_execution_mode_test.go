package store

import (
	"bytes"
	"testing"
)

func TestLatestWorkflowDefinitionsCarryTypedExecutionModes(t *testing.T) {
	registry := NewBuiltinWorkflowRegistry()
	for _, definition := range BuiltinWorkflowDefinitions() {
		if definition.Version != 3 {
			t.Fatalf("latest %s version=%d, want 3", definition.Ref, definition.Version)
		}
		for _, action := range definition.ActionDefinitions {
			if !validActionExecutionMode(action.ExecutionMode) {
				t.Fatalf("%s action %s has invalid execution mode %q", definition.Ref, action.ID, action.ExecutionMode)
			}
		}
		for _, version := range []int64{1, 2, 3} {
			if _, ok := registry.Lookup(definition.Ref, version); !ok {
				t.Fatalf("%s v%d is not registered", definition.Ref, version)
			}
		}
	}
}

func TestWorkflowDefinitionEncodingAddsExecutionModeOnlyAtV3(t *testing.T) {
	registry := NewBuiltinWorkflowRegistry()
	v2, _ := registry.Lookup("workflow.implementation", 2)
	v3, _ := registry.Lookup("workflow.implementation", 3)
	v2Canonical, err := CanonicalWorkflowDefinition(v2.Definition)
	if err != nil {
		t.Fatal(err)
	}
	v3Canonical, err := CanonicalWorkflowDefinition(v3.Definition)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(v2Canonical, []byte(`"execution_mode"`)) {
		t.Fatal("v2 canonical definition changed its historical encoding")
	}
	if !bytes.Contains(v3Canonical, []byte(`"execution_mode"`)) {
		t.Fatal("v3 canonical definition omitted typed execution modes")
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

func TestLegacyCustomDefinitionsReceiveCompatibleRuntimeModesWithoutChangingDigest(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		version int64
	}{{"v1", 1}, {"v2", 2}} {
		t.Run(testCase.name, func(t *testing.T) {
			version := testCase.version
			definition := builtinImplementation()
			if version == 2 {
				definition = builtinWorkflowV2(definition)
			}
			definition.AvailableActions[0] = "custom_action"
			definition.StepGraph.Steps[0].Actions[0] = "custom_action"
			definition.ActionDefinitions[0].ID = "custom_action"
			definition.ActionDefinitions[0].ExecutionMode = ""
			legacyDigest, err := WorkflowDefinitionDigest(definition)
			if err != nil {
				t.Fatal(err)
			}
			registered, err := NewWorkflowDefinitionRegistry().Register(definition)
			if err != nil {
				t.Fatal(err)
			}
			if registered.Digest != legacyDigest {
				t.Fatalf("compatible runtime mode changed v%d digest: got %s want %s", version, registered.Digest, legacyDigest)
			}
			if registered.Definition.ActionDefinitions[0].ExecutionMode != ActionAdvance {
				t.Fatalf("legacy custom action mode=%q, want %q", registered.Definition.ActionDefinitions[0].ExecutionMode, ActionAdvance)
			}
		})
	}
}

func TestV1UndeclaredReplayActionUsesFrozenCompatibilityMode(t *testing.T) {
	definition := builtinImplementation()
	mode, ok := workflowActionExecutionMode(definition, "record_report")
	if !ok || mode != ActionAdvance {
		t.Fatalf("v1 undeclared replay action mode=%q found=%t, want %q", mode, ok, ActionAdvance)
	}
}

func TestV3DefinitionRejectsMissingExecutionMode(t *testing.T) {
	definition := BuiltinWorkflowDefinitions()[0]
	definition.ActionDefinitions[0].ExecutionMode = ""
	if err := ValidateWorkflowDefinition(definition); err == nil {
		t.Fatal("v3 definition without execution_mode passed validation")
	}
	if _, err := NewWorkflowDefinitionRegistry().Register(definition); err == nil {
		t.Fatal("v3 definition without execution_mode was registered")
	}
}

func TestHistoricalBuiltinWorkflowDigestsStayPinned(t *testing.T) {
	registry := NewBuiltinWorkflowRegistry()
	want := map[int64]string{
		1: "sha256:964cf4a634cc373dbe38a72a70ebe537941029c7f479ab575f7eadde8672ff37",
		2: "sha256:60c60cb444618dee745eb5572f74c9736bf0b4526950e2dd2c896574f36a77e1",
	}
	for version, expected := range want {
		definition, ok := registry.Lookup("workflow.implementation", version)
		if !ok {
			t.Fatalf("workflow.implementation v%d is not registered", version)
		}
		if definition.Digest != expected {
			t.Fatalf("workflow.implementation v%d digest=%s, want %s", version, definition.Digest, expected)
		}
	}
}
