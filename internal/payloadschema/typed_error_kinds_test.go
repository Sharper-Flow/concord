package payloadschema

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestTypedErrorVocabularyMatchesCanonicalSchema(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "agent-tool-envelope.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Defs struct {
			TypedError struct {
				Properties struct {
					Kind struct {
						Enum []string `json:"enum"`
					} `json:"kind"`
				} `json:"properties"`
			} `json:"typedError"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	declared := document.Defs.TypedError.Properties.Kind.Enum
	if len(declared) == 0 || !slices.Equal(TypedErrorKinds(), declared) {
		t.Fatalf("vocabulary = %v, schema = %v", TypedErrorKinds(), declared)
	}
	for _, kind := range declared {
		if !TypedErrorKindAllowed(kind) {
			t.Errorf("schema kind %q is rejected", kind)
		}
	}
}

func TestTypedErrorVocabularyRejectsUndeclaredKinds(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"", "not_a_declared_kind", " invalid_input", "invalid_input ", "INVALID_INPUT", "invalid_input\x00"} {
		if TypedErrorKindAllowed(kind) {
			t.Errorf("undeclared kind %q is accepted", kind)
		}
	}
	for _, kind := range TypedErrorKinds() {
		if TypedErrorKindAllowed(strings.ToUpper(kind)) {
			t.Errorf("case-changed kind %q is accepted", kind)
		}
	}
}

func TestTypedErrorVocabularyListsAreIndependent(t *testing.T) {
	t.Parallel()
	first, second := TypedErrorKinds(), TypedErrorKinds()
	if len(first) == 0 || !slices.Equal(first, second) {
		t.Fatalf("lists differ: %v and %v", first, second)
	}
	original := second[0]
	first[0] = "not_a_declared_kind"
	if second[0] != original || !slices.Equal(TypedErrorKinds(), second) {
		t.Fatal("modifying a returned list changed another vocabulary list")
	}
	if !TypedErrorKindAllowed(original) || TypedErrorKindAllowed(first[0]) {
		t.Fatal("modifying a returned list changed vocabulary membership")
	}
}
