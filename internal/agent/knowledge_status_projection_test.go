package agent

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// Law search exposes accepted law, without a replacement relationship.
func TestKnowledgeSearchProjectsAcceptedLawStatus(t *testing.T) {
	t.Parallel()
	meta := store.ResultMeta{QueryID: "PM1.Q9", ContractVersion: "PM1/1.0", Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
	result := store.Q9Result{ResultMeta: meta, IndexWatermark: "commit", Items: []store.KnowledgeItem{
		{ID: "CD-0001", Kind: "decision", OutcomeTag: "accepted", NotePath: ".concord/docs/decisions/CD-0001.md"},
	}}
	response, err := (runtime{Tool: "concord_knowledge", Operation: "search"}).q9(NewBase("q9-status", "concord_knowledge", "search"), result)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil || len(page.Items) != 1 {
		t.Fatalf("search page = %s err=%v", raw, err)
	}
	if got := page.Items[0]["status"]; got != "accepted" {
		t.Fatalf("search item status = %v, want accepted; item=%s", got, raw)
	}
	if _, present := page.Items[0]["successor_id"]; present {
		t.Fatalf("law search item projected a successor: %s", raw)
	}
}

// A work-note outcome is not a law status: the search projection omits
// status instead of emitting a value the closed enum refuses.
func TestKnowledgeSearchOmitsNonLawOutcomeTags(t *testing.T) {
	t.Parallel()
	meta := store.ResultMeta{QueryID: "PM1.Q9", ContractVersion: "PM1/1.0", Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
	result := store.Q9Result{ResultMeta: meta, IndexWatermark: "commit", Items: []store.KnowledgeItem{
		{ID: "work-1", Kind: "work_note", OutcomeTag: "shipped", NotePath: "notes/work-1.md", SuccessorID: "work-2"},
	}}
	response, err := (runtime{Tool: "concord_knowledge", Operation: "search"}).q9(NewBase("q9-worknote", "concord_knowledge", "search"), result)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil || len(page.Items) != 1 {
		t.Fatalf("search page = %s err=%v", raw, err)
	}
	if _, present := page.Items[0]["status"]; present {
		t.Fatalf("work-note search item projected a non-law status: %s", raw)
	}
	if got := page.Items[0]["successor_id"]; got != "work-2" {
		t.Fatalf("search item successor_id = %v, want work-2; item=%s", got, raw)
	}
}

// A work note whose free outcome tag names a law status is still not law
// (CD-0020 D3): the projection drops the tag instead of emitting a law
// status for it.
func TestKnowledgeSearchOmitsWorkNoteTagsThatNameLawStatuses(t *testing.T) {
	t.Parallel()
	for _, tag := range []string{"accepted", "superseded", "published"} {
		meta := store.ResultMeta{QueryID: "PM1.Q9", ContractVersion: "PM1/1.0", Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
		result := store.Q9Result{ResultMeta: meta, IndexWatermark: "commit", Items: []store.KnowledgeItem{
			{ID: "work-1", Kind: "work_note", OutcomeTag: tag, NotePath: "notes/work-1.md"},
		}}
		response, err := (runtime{Tool: "concord_knowledge", Operation: "search"}).q9(NewBase("q9-worknote-law-tag", "concord_knowledge", "search"), result)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(response.Result)
		if err != nil {
			t.Fatal(err)
		}
		var page struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(raw, &page); err != nil || len(page.Items) != 1 {
			t.Fatalf("search page = %s err=%v", raw, err)
		}
		if _, present := page.Items[0]["status"]; present {
			t.Fatalf("work-note tag %q projected a law status: %s", tag, raw)
		}
	}
}

// A canonical law result carries accepted status beside its locator, while
// state keeps its PM1 Q10 meaning.
func TestKnowledgeResolveNoteProjectsAcceptedLawStatus(t *testing.T) {
	t.Parallel()
	meta := store.ResultMeta{QueryID: "PM1.Q10", ContractVersion: "PM1/1.0", Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
	note := &store.CanonicalNote{HomeProjectID: "home", HomeLocatorID: "locator", NotePath: ".concord/docs/decisions/CD-0001.md", Commit: "c0ffee", ContentHash: "sha256:deadbeef"}
	result := store.Q10Result{ResultMeta: meta, Status: "canonical", Note: note, Result: &store.Q10Payload{Status: "canonical", Note: note, LawStatus: "accepted"}}
	response, err := (runtime{Tool: "concord_knowledge", Operation: "resolve_note"}).q10(NewBase("q10-status", "concord_knowledge", "resolve_note"), result)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		State   string  `json:"state"`
		Status  *string `json:"status"`
		Locator *string `json:"locator"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.State != "canonical" || payload.Locator == nil {
		t.Fatalf("resolve_note = %s, want canonical state with locator", raw)
	}
	if payload.Status == nil || *payload.Status != "accepted" {
		t.Fatalf("resolve_note status = %s, want accepted", raw)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, present := fields["successor_id"]; present {
		t.Fatalf("law resolve_note projected a successor: %s", raw)
	}
}

// A resolve_note answer without a law status, such as not_compacted,
// projects state alone and stays shape-compatible with PM1 Q10.
func TestKnowledgeResolveNoteWithoutLawStatusOmitsTheField(t *testing.T) {
	t.Parallel()
	meta := store.ResultMeta{QueryID: "PM1.Q10", ContractVersion: "PM1/1.0", Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
	result := store.Q10Result{ResultMeta: meta, Status: "not_compacted", Result: &store.Q10Payload{Status: "not_compacted"}}
	response, err := (runtime{Tool: "concord_knowledge", Operation: "resolve_note"}).q10(NewBase("q10-nostatus", "concord_knowledge", "resolve_note"), result)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["state"] != "not_compacted" {
		t.Fatalf("resolve_note = %s, want not_compacted state", raw)
	}
	if _, present := payload["status"]; present {
		t.Fatalf("resolve_note projected a law status without one: %s", raw)
	}
}

func TestKnowledgeWireRejectsRetiredLawStatusAndSuccessor(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"accepted", "superseded", "published"} {
		for _, successor := range []bool{false, true} {
			item := map[string]any{"knowledge_id": "CD-0001", "kind": "decision", "locator": "law.md", "status": status}
			note := map[string]any{"state": "canonical", "status": status}
			if successor {
				item["successor_id"], note["successor_id"] = "CD-0002", "CD-0002"
			}
			for name, payload := range map[string]any{"knowledge_page": map[string]any{"items": []any{item}}, "canonical_note_result": note} {
				raw, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				valid := !successor && (status == "accepted" || (name == "canonical_note_result" && status == "published"))
				if err := ValidatePayloadSchema(name, raw); (err == nil) != valid {
					t.Fatalf("%s status=%q successor=%v: err=%v, want valid=%v", name, status, successor, err, valid)
				}
			}
		}
	}
	for name, payload := range map[string]string{
		"knowledge_page":        `{"items":[{"knowledge_id":"work-1","kind":"note","locator":"note.md","successor_id":"work-2"}]}`,
		"canonical_note_result": `{"state":"canonical","successor_id":"work-2"}`,
	} {
		if err := ValidatePayloadSchema(name, json.RawMessage(payload)); err != nil {
			t.Fatalf("%s rejected work-note successor: %v", name, err)
		}
	}
	if err := ValidatePayloadSchema("knowledge_page", json.RawMessage(`{"items":[{"knowledge_id":"lesson-1","kind":"lesson","locator":"lesson.md","status":"published"}]}`)); err != nil {
		t.Fatalf("knowledge page rejected published non-law record: %v", err)
	}
}
