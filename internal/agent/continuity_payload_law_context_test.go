package agent

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
)

// Resolved law, Domains and the proposal remain available to resumed sessions.
func TestContinuityPayloadProjectsLawContextAndProposal(t *testing.T) {
	t.Parallel()
	snapshot := store.ContinuitySnapshot{
		WorkID:         "work-1",
		LawContext:     &store.WorkflowLawContext{Laws: []store.WorkflowLawContextLaw{{Roles: []string{"mandated", "modified", "obligation"}, LawID: "spec:one", Kind: "spec", Status: "accepted", Title: "Synthetic test law", Path: ".concord/docs/spec.md", ObligationIDs: []string{"verification"}}}, Domains: []store.WorkflowLawContextDomain{{DomainID: "root", Name: "Root", Purpose: "Product law"}}},
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
	if !ok || constraints == nil || len(constraints) != 0 {
		t.Fatalf("proposal constraints = %#v, want a normalized empty list", proposal["constraints"])
	}
}

// Constitution records are law-bearing and project into law_subjects, so an
// approved contract can mandate one and continuity resolves it with kind
// constitution (store half: internal/store). The payload the dispatched lane
// packet consumes must stay schema-valid for that resolution, not only for
// decision and spec.
func TestContinuityPayloadValidatesResolvedConstitutionAgainstSchema(t *testing.T) {
	t.Parallel()
	snapshot := store.ContinuitySnapshot{
		WorkID:                   "work-constitution",
		ProductIdentity:          []string{"product"},
		WorkflowStep:             "execution",
		SpecMandate:              []string{"const:one"},
		RestartUnavailableReason: "dispatch window closed",
		Boundaries:               []store.ContextBoundary{},
		Watermark:                "seq:1",
		LawContext: &store.WorkflowLawContext{
			Laws:    []store.WorkflowLawContextLaw{{Roles: []string{"mandated"}, LawID: "const:one", Kind: "constitution", Status: "accepted", Title: "Synthetic constitution", Path: ".concord/docs/constitution.md"}},
			Domains: []store.WorkflowLawContextDomain{{DomainID: "root", Name: "Root", Purpose: "Product law"}},
		},
	}
	raw, err := json.Marshal(ContinuityPayload(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("continuity_snapshot", raw); err != nil {
		t.Fatalf("continuity payload carrying a resolved constitution law is not schema-valid: %v", err)
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

func TestContinuityPayloadPreservesStaleLawRevisionAgainstSchema(t *testing.T) {
	t.Parallel()
	snapshot := store.ContinuitySnapshot{
		WorkID: "work-stale-law", ProductIdentity: []string{"product"}, WorkflowStep: "execution",
		SpecMandate:              []string{"source/spec:one"},
		RestartUnavailableReason: "dispatch window closed", Boundaries: []store.ContextBoundary{}, Watermark: "seq:1",
		StaleLawRevision: &store.StaleLawRevision{
			OldLawID: "source/spec:one", OldContentHash: "sha256:" + repeatHex(),
			RecoveryActions: []string{"revise_intent", "terminal_work"},
		},
	}
	payload := ContinuityPayload(snapshot)
	if payload["pinned"].(map[string]any)["stale_law_revision"] != snapshot.StaleLawRevision {
		t.Fatal("continuity lost the recorded stale-law identity or recovery actions")
	}
	raw, _, err := ContinuityCanonicalDocument(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("continuity_snapshot", raw); err != nil {
		t.Fatalf("stale-law continuity document rejected: %v", err)
	}
	if err := agentcontract.ValidateOperationPayload("concord_work_trace", "continuity", raw, true); err != nil {
		t.Fatalf("stale-law continuity result rejected: %v", err)
	}
	payload["pinned"].(map[string]any)["stale_law_revision"] = map[string]any{
		"old_law_id": "source/spec:one", "old_content_hash": "not-a-digest", "recovery_actions": []string{"revise_intent"},
	}
	invalid, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("continuity_snapshot", invalid); err == nil {
		t.Fatal("continuity admitted malformed stale-law identity")
	}
}
