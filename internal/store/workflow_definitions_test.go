package store

import "testing"

func TestBuiltinWorkflowDefinitionCanonicalDigests(t *testing.T) {
	// The complete canonical identity covers steps, routes and action payloads;
	// intentional definition changes must update these digests.
	want := map[string]string{
		"workflow.implementation": "sha256:0de951682b745afd0be8cb1d74a220895f696110d7ff89b12666e1c71b8cc436",
		"workflow.break_fix":      "sha256:e282fcffaff35689262b6f95089cb035a328f423fd1847c73875fc0e95d6ae8c",
		"workflow.research":       "sha256:614d50a3dfdfbdbc76b8d2fed4689af1be1c0a838554148906aba77f5c47dee3",
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
