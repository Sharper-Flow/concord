package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// TestFederatedQ9WireCarriesSourceProof pins the agent wire contract for a
// Product-wide answer (CD-0200): each item names its source Project and
// locator beside the revision proof, and the answer carries one watermark
// entry per registered source in both the payload and the envelope.
func TestFederatedQ9WireCarriesSourceProof(t *testing.T) {
	meta := store.ResultMeta{QueryID: "PM1.Q9", ContractVersion: "PM1/1.0", Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
	q := store.Q9Result{
		ResultMeta:     meta,
		IndexWatermark: "home-commit",
		SourceWatermarks: []store.KnowledgeSourceWatermark{
			{ProjectID: "home", LocatorID: "home-loc", Watermark: "home-commit", Authority: "authoritative"},
			{ProjectID: "src", LocatorID: "src-loc", Watermark: "src-commit", Authority: "authoritative"},
		},
		Items: []store.KnowledgeItem{{
			ID: "SRC-LAW", Kind: "decision", NotePath: ".concord/docs/decisions/CD-0999.md",
			HomeProjectID: "src", HomeLocatorID: "src-loc", CommitOID: "src-commit",
			ContentHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}},
	}
	result, err := (runtime{Tool: "concord_knowledge", Operation: "search"}).q9(NewBase("review-q9", "concord_knowledge", "search"), q)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Items   []map[string]any `json:"items"`
		Sources []any            `json:"source_watermarks"`
	}
	if err := json.Unmarshal(result.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Sources) != 2 || len(result.SourceVersionWatermark) != 2 || payload.Items[0]["home_project_id"] != "src" {
		t.Fatalf("federated source proof dropped: result=%s watermarks=%+v", result.Result, result.SourceVersionWatermark)
	}
}

// TestSingleSourceQ9WireKeepsOneWatermark pins the single-source wire shape:
// no source_watermarks payload key and the one-entry envelope watermark the
// contract always carried (CD-0200 single-source rule).
func TestSingleSourceQ9WireKeepsOneWatermark(t *testing.T) {
	meta := store.ResultMeta{QueryID: "PM1.Q9", ContractVersion: "PM1/1.0", Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
	q := store.Q9Result{
		ResultMeta:     meta,
		IndexWatermark: "home-commit",
		Items: []store.KnowledgeItem{{
			ID: "HOME-LAW", Kind: "decision", NotePath: ".concord/docs/decisions/CD-0998.md",
			HomeProjectID: "home", HomeLocatorID: "home-loc", CommitOID: "home-commit",
			ContentHash: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		}},
	}
	result, err := (runtime{Tool: "concord_knowledge", Operation: "search"}).q9(NewBase("solo-q9", "concord_knowledge", "search"), q)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SourceVersionWatermark) != 1 {
		t.Fatalf("single-source watermarks = %+v, want one entry", result.SourceVersionWatermark)
	}
	if strings.Contains(string(result.Result), "source_watermarks") {
		t.Fatalf("single-source answer carries a federated payload key: %s", result.Result)
	}
}

// A single-source answer keeps the wire shape it always had (CD-0200
// single-source rule): no source_watermarks payload key, one envelope
// watermark entry, and no per-item source fields, because every record
// resolves against the one source the envelope watermark already proves.
func TestSingleSourceQ9WireOmitsSourceFields(t *testing.T) {
	meta := store.ResultMeta{QueryID: "PM1.Q9", ContractVersion: "PM1/1.0", Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
	q := store.Q9Result{
		ResultMeta:     meta,
		IndexWatermark: "home-commit",
		Items: []store.KnowledgeItem{{
			ID: "HOME-LAW", Kind: "decision", NotePath: ".concord/docs/decisions/CD-0998.md",
			HomeProjectID: "home", HomeLocatorID: "home-loc", CommitOID: "home-commit",
			ContentHash: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		}},
	}
	result, err := (runtime{Tool: "concord_knowledge", Operation: "search"}).q9(NewBase("solo-q9-wire", "concord_knowledge", "search"), q)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(result.Result, &payload); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"home_project_id", "home_locator_id"} {
		if _, present := payload.Items[0][field]; present {
			t.Fatalf("single-source output adds the previously absent %s field: %s", field, result.Result)
		}
	}
}
