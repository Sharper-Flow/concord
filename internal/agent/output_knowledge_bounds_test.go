package agent

import (
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestDomainOutputsAdmitKnowledgeIdentifiers(t *testing.T) {
	t.Parallel()
	document := payloadschema.Document()
	definitions := document["$defs"].(map[string]any)
	for _, id := range []string{"x", "law with spaces", strings.Repeat("界", 256)} {
		for _, probe := range []struct {
			definition string
			value      map[string]any
		}{
			{"domain_summary", map[string]any{
				"domain_id": id, "parent_domain_id": "parent", "name": "Domain",
				"purpose": "Synthetic domain", "status": "current", "home_domain": false,
			}},
			{"domain_summary", map[string]any{
				"domain_id": "child", "parent_domain_id": id, "name": "Domain",
				"purpose": "Synthetic domain", "status": "current", "home_domain": false,
			}},
			{"domain_law_record", map[string]any{
				"law_id": id, "kind": "decision", "title": "Synthetic law",
				"path": "docs/law.md", "content_hash": "sha256:" + strings.Repeat("a", 64),
				"applies_to": []any{id},
			}},
			{"domain_relation", map[string]any{
				"kind": "depends_on", "source_domain_id": id, "target_domain_id": "target",
				"state": "active", "governing_law_ids": []any{id},
			}},
			{"domain_relation", map[string]any{
				"kind": "depends_on", "source_domain_id": "source", "target_domain_id": id,
				"state": "active", "governing_law_ids": []any{id},
			}},
		} {
			if err := payloadschema.ValidateValue(probe.value, definitions[probe.definition].(map[string]any), document, "$"); err != nil {
				t.Errorf("%s must admit a knowledge identifier with %d code points: %v", probe.definition, len([]rune(id)), err)
			}
		}
	}
	localID := definitions["id"].(map[string]any)
	for _, value := range []string{"law with spaces", strings.Repeat("x", 129)} {
		if err := payloadschema.ValidateValue(value, localID, document, "$"); err == nil {
			t.Errorf("the local id primitive must remain narrow: accepted %q", value)
		}
	}
}

func TestDrivingSessionOutputAdmitsStoredCharacterBound(t *testing.T) {
	t.Parallel()
	document := payloadschema.Document()
	definitions := document["$defs"].(map[string]any)
	value := map[string]any{
		"session_ref": strings.Repeat("界", 128), "last_action_id": "start_execution",
		"last_acted_at": "2026-01-01T00:00:00Z",
	}
	if err := payloadschema.ValidateValue(value, definitions["work_pin_driving_session"].(map[string]any), document, "$"); err != nil {
		t.Fatalf("stored session references count characters, not workflow-reference bytes: %v", err)
	}
	if err := payloadschema.ValidateValue(value["session_ref"], definitions["reference"].(map[string]any), document, "$"); err == nil {
		t.Fatal("the workflow reference input must retain its separate byte bound")
	}
}
