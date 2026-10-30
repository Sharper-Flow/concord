package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func publicationWireFixture() store.KnowledgePublication {
	return store.KnowledgePublication{KnowledgePublicationRecord: store.KnowledgePublicationRecord{
		ProjectID: "source-project", LocatorID: "source-locator", ServedCommit: strings.Repeat("a", 40),
		OriginIdentity: "sha256:" + strings.Repeat("b", 64), PublicationRef: "refs/heads/main",
		PublicationCommit: strings.Repeat("c", 40), ObservedAt: "2026-10-26T12:00:00Z",
		Basis: "cached_remote_observation", Comparison: "lagging",
	}}
}

func TestKnowledgePublicationQ9Wire(t *testing.T) {
	p := publicationWireFixture()
	meta := store.ResultMeta{QueryID: "PM1.Q9", ContractVersion: "PM1/1.0", Authority: "authoritative", Freshness: store.Freshness{ObservedAt: p.ObservedAt}, OrderingKeys: []string{"knowledge_id"}}
	r := runtime{Tool: "concord_knowledge", Operation: "search"}
	response, err := r.q9(agentcontract.NewBase("publication-q9", r.Tool, r.Operation), store.Q9Result{ResultMeta: meta, IndexWatermark: p.ServedCommit, Publications: []store.KnowledgePublication{p}})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Publications []store.KnowledgePublication `json:"publications"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(payload.Publications, []store.KnowledgePublication{p}) {
		t.Fatalf("publication evidence dropped: %s", response.Result)
	}
	if response.Authority != "authoritative" {
		t.Fatalf("comparison changed read authority: %+v", response)
	}
	if _, err := json.Marshal(response); err != nil {
		t.Fatal(err)
	}
}

func TestKnowledgePublicationQ10Wire(t *testing.T) {
	p := publicationWireFixture()
	meta := store.ResultMeta{QueryID: "PM1.Q10", ContractVersion: "PM1/1.0", Authority: "authoritative", Freshness: store.Freshness{ObservedAt: p.ObservedAt}, OrderingKeys: []string{"knowledge_id"}}
	note := &store.CanonicalNote{HomeProjectID: p.ProjectID, HomeLocatorID: p.LocatorID, NotePath: "law.md", CommitOID: p.ServedCommit, ContentHash: "sha256:" + strings.Repeat("d", 64)}
	r := runtime{Tool: "concord_knowledge", Operation: "resolve_note"}
	response, err := r.q10(agentcontract.NewBase("publication-q10", r.Tool, r.Operation), store.Q10Result{ResultMeta: meta, Status: "canonical", Note: note, Publication: &p})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"publication": p, "commit_oid": note.CommitOID, "content_hash": note.ContentHash, "home_project_id": note.HomeProjectID, "home_locator_id": note.HomeLocatorID} {
		raw, _ := json.Marshal(want)
		if string(payload[key]) != string(raw) {
			t.Errorf("%s = %s, want %s", key, payload[key], raw)
		}
	}
	if _, err := json.Marshal(response); err != nil {
		t.Fatal(err)
	}
}

func TestKnowledgePublicationOperationUsesProductRead(t *testing.T) {
	op, ok := agentcontract.ValidateContractOperation("concord_knowledge", "observe_publication")
	if !ok {
		t.Fatal("explicit publication observation is not registered")
	}
	if op.Kind != agentcontract.OperationRead || op.Capability != agentcontract.Capability("product_read") || op.QueryID != "CD-0020.R1" {
		t.Fatalf("publication observation changed authorization route: %+v", op)
	}
}

func TestKnowledgePublicationWireBasisAndHistoricalSuccess(t *testing.T) {
	for _, basis := range []string{"explicit_remote_observation", "cached_remote_observation", "not_observed", "historical_read"} {
		t.Run(basis, func(t *testing.T) {
			previous := publicationWireFixture().KnowledgePublicationRecord
			previous.Comparison = "match"
			p := store.KnowledgePublication{KnowledgePublicationRecord: store.KnowledgePublicationRecord{ProjectID: previous.ProjectID, LocatorID: previous.LocatorID, Basis: basis, Comparison: "unknown"}}
			if basis == "explicit_remote_observation" || basis == "cached_remote_observation" {
				p.ObservedAt = "2026-10-26T13:00:00Z"
				p.PreviousSuccess = &previous
			}
			meta := store.ResultMeta{QueryID: "PM1.Q10", ContractVersion: "PM1/1.0", Authority: "degraded", Freshness: store.Freshness{ObservedAt: "2026-10-26T14:00:00Z"}, OrderingKeys: []string{"knowledge_id"}, Omissions: []string{"source_unreachable"}}
			r := runtime{Tool: "concord_knowledge", Operation: "resolve_note"}
			response, err := r.q10(agentcontract.NewBase("publication-basis", r.Tool, r.Operation), store.Q10Result{ResultMeta: meta, Status: "canonical", Publication: &p})
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Publication store.KnowledgePublication `json:"publication"`
			}
			if err := json.Unmarshal(response.Result, &payload); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(payload.Publication, p) || response.Authority != "degraded" {
				t.Fatalf("publication basis/history changed authority or latest comparison: %s", response.Result)
			}
			if _, err := json.Marshal(response); err != nil {
				t.Fatal(err)
			}
		})
	}
}
