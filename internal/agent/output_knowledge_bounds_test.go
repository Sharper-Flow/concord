package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
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

// The store boundary, not the tool surface, owns the retained research
// content: internal/store validateFinding/validateSource/normalizeRevision
// require non-empty fields only, and the final research DDL carries
// CHECK(length(...) > 0) with no upper bound, so a store writer can retain
// identifiers and text above every published output maximum. The result
// schema of concord_work_trace.research is a projection of that store and
// must admit the readback.
func TestResearchOutputAdmitsRetainedStoreBounds(t *testing.T) {
	t.Parallel()
	s, _, _, _ := researchSurfaceFixture(t)
	ctx := context.Background()

	var (
		bigQuestion  = strings.Repeat("q", 2049)
		bigSourceID  = strings.Repeat("s", 129)
		bigTitle     = strings.Repeat("t", 513)
		bigFindingID = strings.Repeat("f", 129)
		bigStatement = strings.Repeat("a", 8193)
		// Exactly 256 Unicode code points: the store scope guard counts code
		// points (utf8.RuneCountInString <= 256), so this multibyte ID sits on
		// the admitted boundary while spanning 768 bytes.
		bigScopeID = strings.Repeat("\u754c", 256)
	)
	if _, err := createResearchPack(ctx, s, store.CreateResearchPackRequest{
		PackID: "bounds-pack", OwnerWorkID: "work-1",
		Revision: store.ResearchRevisionInput{
			Question: bigQuestion, ScopeIn: json.RawMessage(`[]`), ScopeOut: json.RawMessage(`[]`),
			DoneWhen: json.RawMessage(`[]`), Method: "store readback fixture",
		},
	}); err != nil {
		t.Fatalf("store must retain a question above the output maximum: %v", err)
	}
	if err := recordResearchSource(ctx, s, store.ResearchSourceRequest{
		PackID: "bounds-pack", ExpectedVersion: 1,
		Source: store.ResearchSource{
			SourceID: bigSourceID, Kind: store.SourceCode, Locator: "internal/store/research_reads.go",
			Title: bigTitle, PublisherOrAuthor: "concord", AccessedAt: "2026-01-01T00:00:00Z",
		},
	}); err != nil {
		t.Fatalf("store must retain an oversized source ID and title: %v", err)
	}
	if err := recordResearchFinding(ctx, s, store.ResearchFindingRequest{
		PackID: "bounds-pack", ExpectedVersion: 2,
		Finding: store.ResearchFinding{
			FindingID: bigFindingID, Kind: store.FindingObservation, Statement: bigStatement,
			Confidence: store.ConfidenceHigh, Freshness: store.ResearchCurrent, Status: store.FindingActive,
			SourceIDs: []string{bigSourceID},
			Scopes:    store.ResearchScopes{Mode: "explicit", DomainIDs: []string{bigScopeID}},
		},
	}); err != nil {
		t.Fatalf("store must retain an oversized finding and its 256-code-point scope: %v", err)
	}

	pack, err := s.ReadResearchPack(ctx, store.ResearchReadRequest{PackID: "bounds-pack"})
	if err != nil {
		t.Fatalf("read back the retained pack: %v", err)
	}
	if pack.PackID != "bounds-pack" || pack.OwnerWorkID != "work-1" || pack.CurrentRevision != 1 || len(pack.Revisions) != 1 {
		t.Fatalf("unexpected pack shape: %+v", pack)
	}
	revision := pack.Revisions[0]
	if revision.Question != bigQuestion {
		t.Fatalf("question did not survive readback exactly: %d code points", len([]rune(revision.Question)))
	}
	if len(revision.Findings) != 1 || len(revision.Sources) != 1 {
		t.Fatalf("readback lost the finding or source: %+v", revision)
	}
	finding, source := revision.Findings[0], revision.Sources[0]
	if finding.FindingID != bigFindingID || finding.Statement != bigStatement {
		t.Fatal("finding identity and statement must survive readback exactly")
	}
	if finding.Scopes.Mode != "explicit" || len(finding.Scopes.DomainIDs) != 1 || finding.Scopes.DomainIDs[0] != bigScopeID {
		t.Fatalf("scope did not survive readback exactly: %+v", finding.Scopes)
	}
	if source.SourceID != bigSourceID || source.Title != bigTitle {
		t.Fatal("source identity and title must survive readback exactly")
	}
	if len(finding.SourceIDs) != 1 || finding.SourceIDs[0] != bigSourceID {
		t.Fatalf("citation did not survive readback exactly: %+v", finding.SourceIDs)
	}

	raw, err := json.Marshal(pack)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= 51200 {
		t.Fatalf("the retained readback must sit below the 51200-byte ceiling, is %d bytes", len(raw))
	}
	if err := agentcontract.ValidateOperationPayload("concord_work_trace", "research", raw, true); err != nil {
		t.Fatalf("the research result schema must admit the retained store readback: %v", err)
	}
}

// The authored-input side keeps its own bounds: tool callers write research
// content through the input schemas, and those guards must stay narrow even
// once the result side admits the full retained readback.
func TestResearchOutputBoundsKeepAuthoredInputNarrow(t *testing.T) {
	t.Parallel()
	document := payloadschema.Document()
	definitions := document["$defs"].(map[string]any)
	for _, probe := range []struct{ definition, field, value string }{
		{"research_source_input", "source_id", strings.Repeat("s", 129)},
		{"research_finding_input", "finding_id", strings.Repeat("f", 129)},
		{"research_finding_input", "statement", strings.Repeat("a", 8193)},
		{"research_revision_input", "question", strings.Repeat("q", 2049)},
	} {
		node := definitions[probe.definition].(map[string]any)["properties"].(map[string]any)[probe.field].(map[string]any)
		if err := payloadschema.ValidateValue(probe.value, node, document, "$"); err == nil {
			t.Errorf("%s.%s must keep its authored-input bound: admitted %d code points", probe.definition, probe.field, len([]rune(probe.value)))
		}
	}
}
