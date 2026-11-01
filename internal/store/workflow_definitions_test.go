package store

import "testing"

func TestBuiltinWorkflowDefinitionCanonicalDigests(t *testing.T) {
	// The complete canonical identity covers steps, routes and action payloads;
	// intentional definition changes must update these digests.
	want := map[string]string{
		"workflow.implementation": "sha256:4a8482030e531fdbd2908159497e41a6e6b512665f8dde1afd123aee7b1e216c",
		"workflow.break_fix":      "sha256:970a721a90e9536e93dd5bc30180828a98da44186c40894b6a13fa3cedcb7335",
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
