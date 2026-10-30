package store

import "testing"

func TestResearchCompositionReachesReview(t *testing.T) {
	s := openTemp(t)
	t.Cleanup(func() { _ = s.Close() })
	const workID = "research-review-reachable"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	registered, err := LiveWorkflowDefinitionForRef("workflow.research")
	if err != nil {
		t.Fatal(err)
	}
	definition := registered.Definition
	livenessInitialize(t, s, workID, definition)
	seedWorkflowAuthority(t, s, "research-review-authority", workID, livenessActor().PrincipalRef, "request/liveness", []string{livenessSubjectRef})
	ordinal := 0
	apply := func(actionID string, variant map[string]string) {
		t.Helper()
		action, ok := livenessActionDefinition(definition, actionID)
		if !ok {
			t.Fatalf("research does not declare %s", actionID)
		}
		payload, err := livenessPayload(definition, action, variant)
		if err != nil {
			t.Fatal(err)
		}
		if err := livenessApply(t.Context(), s, workID, livenessMove{action: actionID, payload: payload}, ordinal); err != nil {
			t.Fatalf("research prerequisite %s: %v", actionID, err)
		}
		ordinal++
	}
	apply("frame_research", nil)
	apply("approve_contract", nil)
	for _, kind := range definition.RequiredEvidenceKinds {
		apply("bind_evidence", map[string]string{"evidence_kind": string(kind)})
	}
	apply("record_finding", nil)
	if step := readInstanceStep(t, s, workID); step != "findings" {
		t.Fatalf("research prerequisites reached %q, want findings", step)
	}
	apply("record_report", nil)
	if step := readInstanceStep(t, s, workID); step != "conclude" {
		t.Fatalf("research report reached %q, want conclude", step)
	}
	repo, _ := reviewSubjectClaim(t, s, workID)
	qualifyReviewSubjectForTest(t, s, workID, repo)
	report := currentReviewBlockForTest(t, s, workID, "", "ready_for_final_proof")
	if err := recordReviewForTest(t, s, workID, livenessActor(), report); err != nil {
		t.Fatalf("research cannot record Review after findings: %v", err)
	}
	state, err := loadWorkflowReviewState(t.Context(), s.db, workID)
	if err != nil || state.Accepted == nil || state.Accepted.ReportDigest != reviewDigest(report) {
		t.Fatalf("recorded research Review = %+v, %v", state.Accepted, err)
	}
}
