package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// closedKnowledgeKinds reads the manifest schema's supported_kinds enum, the
// one authority for the knowledge vocabulary. The store binds its own closed
// set to the same enum, so the agent enum, the store filter, and the schema
// cannot drift apart.
func closedKnowledgeKinds(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "concord-knowledge-index.v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties struct {
			SupportedKinds struct {
				Items struct {
					Enum []string `json:"enum"`
				} `json:"items"`
			} `json:"supported_kinds"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	kinds := append([]string(nil), schema.Properties.SupportedKinds.Items.Enum...)
	if len(kinds) == 0 {
		t.Fatal("manifest schema declares no supported_kinds enum")
	}
	sort.Strings(kinds)
	return kinds
}

// The search kinds filter on the agent surface, translated through the
// agent-side aliases, must name exactly the closed knowledge vocabulary the
// manifest schema declares. A manifest kind the enum omits is indexed but
// unreachable by name, which is how reference and constitution records hid
// from every targeted search.
func TestKnowledgeSearchKindsEnumSpansTheClosedVocabulary(t *testing.T) {
	var document struct {
		Defs map[string]struct {
			Properties map[string]struct {
				Items struct {
					Enum []string `json:"enum"`
				} `json:"items"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal([]byte(GeneratedPayloadSchemaDocument), &document); err != nil {
		t.Fatal(err)
	}
	input, ok := document.Defs["knowledge_search_input"]
	if !ok {
		t.Fatal("knowledge_search_input is not declared")
	}
	enum := input.Properties["kinds"].Items.Enum
	if len(enum) == 0 {
		t.Fatal("knowledge_search_input.kinds declares no enum")
	}
	got := knowledgeKinds(enum)
	sort.Strings(got)
	want := closedKnowledgeKinds(t)
	if len(got) != len(want) {
		t.Fatalf("search kinds enum maps to %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("search kinds enum maps to %v, want %v", got, want)
		}
	}
}

func TestKnowledgeSearchInputAdmitsReferenceAndConstitution(t *testing.T) {
	payload := []byte(`{"product_id":"concord","kinds":["reference","constitution"],"page":{"cursor":null,"limit":5}}`)
	if err := ValidateOperationPayload("concord_knowledge", "search", payload, false); err != nil {
		t.Fatalf("reference and constitution refused by the search input: %v", err)
	}
}
