package agent

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// The dispatched lane packet reads the pinned continuity, so the resolved
// contract-bound law and Domains and the recorded proposal ride the pinned
// projection when present, and stay absent when the contract binds none.
func TestContinuityPayloadProjectsLawContextAndProposal(t *testing.T) {
	t.Parallel()
	snapshot := store.ContinuitySnapshot{
		WorkID:         "work-1",
		LawContext:     &store.WorkflowLawContext{Laws: []store.WorkflowLawContextLaw{{Role: "modified", LawID: "spec:one", Kind: "spec", Status: "accepted", Title: "Synthetic test law", Path: "docs/spec.md"}}, Domains: []store.WorkflowLawContextDomain{{DomainID: "root", Name: "Root", Purpose: "Product law"}}},
		ProposalRecord: &store.WorkflowProposalRecord{WorkVersion: 2, Problem: "Workers receive bare law IDs", UserOutcomes: []string{"Workers read the binding law"}, Constraints: nil},
	}
	payload := ContinuityPayload(snapshot)
	pinned, ok := payload["pinned"].(map[string]any)
	if !ok {
		t.Fatalf("pinned payload type = %T", payload["pinned"])
	}
	lawContext, ok := pinned["law_context"].(*store.WorkflowLawContext)
	if !ok || lawContext == nil {
		t.Fatalf("law_context payload type = %T", pinned["law_context"])
	}
	if len(lawContext.Laws) != 1 || lawContext.Laws[0].LawID != "spec:one" {
		t.Fatalf("law_context laws = %+v", lawContext.Laws)
	}
	proposal, ok := pinned["proposal_record"].(map[string]any)
	if !ok {
		t.Fatalf("proposal_record payload type = %T", pinned["proposal_record"])
	}
	if proposal["problem"] != "Workers receive bare law IDs" {
		t.Fatalf("proposal problem = %v", proposal["problem"])
	}
	constraints, ok := proposal["constraints"].([]string)
	if !ok || len(constraints) != 0 {
		t.Fatalf("proposal constraints = %#v, want a normalized empty list", proposal["constraints"])
	}
}

func TestContinuityPayloadOmitsUnboundLawContext(t *testing.T) {
	t.Parallel()
	payload := ContinuityPayload(store.ContinuitySnapshot{})
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("law_context")) || bytes.Contains(raw, []byte("proposal_record")) {
		t.Fatalf("unbound law context or proposal leaked into the payload: %s", raw)
	}
}
