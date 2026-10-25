package store

import "testing"

func TestResearchCompositionReachesReview(t *testing.T) {
	s := openTemp(t)
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
	lane := reviewGateLane(t, "review")
	const attempt = "attempt:research-review"
	packet := joinPacketFor(t, s, workID, "findings", attempt, lane.ID, lane.Digest)
	if _, err := dispatchJoinAttempt(t.Context(), t, s, workID, readWorkVersion(t, s, workID), livenessActor(), packet); err != nil {
		t.Fatalf("research cannot authorize its Review after findings: %v", err)
	}
	var authorized int
	if err := s.db.QueryRow(`SELECT count(*) FROM worker_authorizations WHERE work_id=? AND attempt_id=? AND json_extract(payload,'$.worker_lane_id')='review'`, workID, attempt).Scan(&authorized); err != nil || authorized != 1 {
		t.Fatalf("research Review authorization=%d, err=%v", authorized, err)
	}
}
