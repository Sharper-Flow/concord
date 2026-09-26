package agent

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// A superseded law must not read as binding: the agent search projection
// carries the record's law status and successor the store already holds.
func TestKnowledgeSearchProjectsLawStatusAndSuccessor(t *testing.T) {
	t.Parallel()
	meta := store.ResultMeta{QueryID: "PM1.Q9", ContractVersion: "PM1/1.0", Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
	result := store.Q9Result{ResultMeta: meta, IndexWatermark: "commit", Items: []store.KnowledgeItem{
		{ID: "CD-0001", Kind: "decision", OutcomeTag: "superseded", NotePath: "docs/decisions/CD-0001.md"},
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
	if got := page.Items[0]["status"]; got != "superseded" {
		t.Fatalf("search item status = %v, want superseded; item=%s", got, raw)
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

// A canonical resolve_note result carries the record's law status and
// successor beside the locator, and state keeps its PM1 Q10 meaning.
func TestKnowledgeResolveNoteProjectsLawStatusAndSuccessor(t *testing.T) {
	t.Parallel()
	meta := store.ResultMeta{QueryID: "PM1.Q10", ContractVersion: "PM1/1.0", Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
	note := &store.CanonicalNote{HomeProjectID: "home", HomeLocatorID: "locator", NotePath: "docs/decisions/CD-0001.md", Commit: "c0ffee", ContentHash: "sha256:deadbeef"}
	result := store.Q10Result{ResultMeta: meta, Status: "canonical", Note: note, Result: &store.Q10Payload{Status: "canonical", Note: note, LawStatus: "superseded", SuccessorID: "CD-0002"}}
	response, err := (runtime{Tool: "concord_knowledge", Operation: "resolve_note"}).q10(NewBase("q10-status", "concord_knowledge", "resolve_note"), result)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		State       string  `json:"state"`
		Status      *string `json:"status"`
		SuccessorID string  `json:"successor_id"`
		Locator     *string `json:"locator"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.State != "canonical" || payload.Locator == nil {
		t.Fatalf("resolve_note = %s, want canonical state with locator", raw)
	}
	if payload.Status == nil || *payload.Status != "superseded" {
		t.Fatalf("resolve_note status = %s, want superseded", raw)
	}
	if payload.SuccessorID != "CD-0002" {
		t.Fatalf("resolve_note successor_id = %q, want CD-0002", raw)
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
