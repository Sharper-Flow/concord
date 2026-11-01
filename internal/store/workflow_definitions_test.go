package store

import "testing"

func TestBuiltinWorkflowDefinitionCanonicalDigests(t *testing.T) {
	// The complete canonical identity covers steps, routes and action payloads;
	// intentional definition changes must update these digests.
	want := map[string]string{
		"workflow.implementation": "sha256:6be7ed08473901cdd13a7a39cfdecc28154213603a618cac86c2e234a681aa7f",
		"workflow.break_fix":      "sha256:587af15ced4b0b295341fad71a696befb4445e35741a9034f1a951e9078d1be9",
		"workflow.research":       "sha256:a4e299e75b26865219f2a790d6c3c53e221df256ffaa91ed4d3ac5ec4c797a08",
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
