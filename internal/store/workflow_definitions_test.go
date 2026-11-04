package store

import "testing"

func TestBuiltinWorkflowDefinitionCanonicalDigests(t *testing.T) {
	// The complete canonical identity covers steps and action payloads;
	// intentional definition changes must update these digests.
	want := map[string]string{
		"workflow.implementation": "sha256:5f83d6775d0b72fa0e39070026b80f58c80de9bdfee7c8a98920e61224cc1266",
		"workflow.break_fix":      "sha256:50a83dbe2b0ac92b741cc64f2c4988f5277ba638b72f5b21b5437a8e73377827",
		"workflow.research":       "sha256:23e2aee77d6aeb2a3b1319aa364ecd7512905f33758c8f9e07b6c2ab61f4cc72",
	}
	definitions := BuiltinWorkflowDefinitions()
	if len(definitions) != len(want) {
		t.Fatalf("got %d definitions, want %d", len(definitions), len(want))
	}
	for _, definition := range definitions {
		expected, ok := want[definition.Ref]
		if !ok {
			t.Errorf("unexpected or duplicate definition %s", definition.Ref)
			continue
		}
		delete(want, definition.Ref)
		if err := ValidateWorkflowDefinition(definition); err != nil {
			t.Errorf("%s: %v", definition.Ref, err)
			continue
		}
		digest, err := WorkflowDefinitionDigest(definition)
		if err != nil {
			t.Errorf("%s: %v", definition.Ref, err)
		} else if digest != expected {
			t.Errorf("%s digest = %s, want %s", definition.Ref, digest, expected)
		}
	}
	if len(want) != 0 {
		t.Errorf("missing definitions: %v", want)
	}
}
