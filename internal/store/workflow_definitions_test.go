package store

import "testing"

func TestBuiltinWorkflowDefinitionCanonicalDigests(t *testing.T) {
	// The complete canonical identity covers steps and action payloads;
	// intentional definition changes must update these digests.
	want := map[string]string{
		"workflow.implementation": "sha256:a76ce5187a4bcdd1933366e7450ae90551d0fb861f94565a98f1e689ababed56",
		"workflow.break_fix":      "sha256:d91c5fa8d4ca502391429fc73b49245c788d617d8def496aefe18bc011ca31ae",
		"workflow.research":       "sha256:17bd177388a14667ead1e5ee63d87c18715e1e461da98f4173d6a530fec53ae6",
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
