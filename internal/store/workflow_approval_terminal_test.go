package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func approvalKindEvidenceCount(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=? AND json_extract(payload,'$.evidence_kind')='approval'`, workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func latestApprovalEventActor(t *testing.T, s *Store, workID, kind string) string {
	t.Helper()
	var actor string
	if err := s.db.QueryRow(`SELECT actor FROM effect_receipts WHERE subject_id=? AND kind=? ORDER BY effect_seq DESC LIMIT 1`, workID, kind).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	return actor
}

func approvalPremiseConfirmationRow(t *testing.T, s *Store, workID string, contractVersion int64) (string, bool) {
	t.Helper()
	var actor string
	if err := s.db.QueryRow(`SELECT confirmed_by FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=?`, workID, contractVersion).Scan(&actor); err != nil {
		return "", false
	}
	return actor, true
}

func approvalEventCount(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=?`, workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

type reviewerOperatorCoordinator struct {
	reviewer, operator, coordinator          WorkflowActor
	reviewerRef, operatorRef, coordinatorRef string
}

func recordApprovalActors(t *testing.T, s *Store, workID string) reviewerOperatorCoordinator {
	t.Helper()
	reviewer, operator, coordinator := seedRoutineReviewersForSelection(t, s, workID)
	return reviewerOperatorCoordinator{reviewer, operator, coordinator, mustActorRef(t, reviewer), mustActorRef(t, operator), mustActorRef(t, coordinator)}
}

func approvalPlanAction(t *testing.T, f acceptanceRecoveryFixture, action string, payload json.RawMessage) error {
	t.Helper()
	request := planAdmissionRequest(f.workID, action, readWorkVersion(t, f.store, f.workID), f.owner, payload)
	return planAdmissionDispatch(t, f.store, request)
}

func assertApprovalRefusalUnchanged(t *testing.T, f acceptanceRecoveryFixture, version int64, events int) {
	t.Helper()
	if got := readWorkVersion(t, f.store, f.workID); got != version || approvalEventCount(t, f.store, f.workID) != events {
		t.Fatalf("refused action changed version or receipts: version=%d", got)
	}
	if _, ok := approvalPremiseConfirmationRow(t, f.store, f.workID, 1); ok {
		t.Fatal("refused action recorded a premise confirmation")
	}
	assertNoFoldGuardRows(t, f.store)
}

func TestWorkflowPlanCoordinatorConfirmationAndCompletion(t *testing.T) {
	const workID = "plan-confirmation-terminal"
	f := qCurrentAcceptance(t, workID)
	if step := workflowPlanFixtureFrontier(t, f.store, workID); step != "review" {
		t.Fatalf("frontier=%q, want review", step)
	}
	if count := approvalKindEvidenceCount(t, f.store, workID); count != 0 {
		t.Fatalf("unexpected approval-kind evidence: %d", count)
	}
	ownerRef := mustActorRef(t, f.owner)
	version := readWorkVersion(t, f.store, workID)
	request := planAdmissionRequest(workID, "confirm_premise", version, f.owner, json.RawMessage(`{}`))
	if err := planAdmissionPreflight(t, f.store, request); err != nil {
		t.Fatalf("confirmation preflight: %v", err)
	}
	if err := planAdmissionDispatch(t, f.store, request); err != nil {
		t.Fatalf("confirmation dispatch: %v", err)
	}
	confirmed, ok := approvalPremiseConfirmationRow(t, f.store, workID, 1)
	if !ok || confirmed != ownerRef || latestApprovalEventActor(t, f.store, workID, WorkflowPremiseConfirmed) != ownerRef {
		t.Fatalf("confirmation actor=%q present=%v, want %q", confirmed, ok, ownerRef)
	}
	var digest string
	if err := f.store.db.QueryRow(`SELECT payload_digest FROM effect_receipts WHERE subject_id=? AND kind=? ORDER BY effect_seq DESC LIMIT 1`, workID, WorkflowPremiseConfirmed).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	wantDigest, err := effectPayloadDigest(mustJSONValue(map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": 1, "confirming_actor_ref": ownerRef, "confirmation_authority": "coordinator"}))
	if err != nil || digest != wantDigest {
		t.Fatalf("confirmation receipt=%q, want %q: %v", digest, wantDigest, err)
	}
	if err := approvalPlanAction(t, f, "record_delivery", json.RawMessage(`{"delivery_artifact":"artifact:terminal","delivery_state":"asserted"}`)); err != nil {
		t.Fatalf("record delivery before completion: %v", err)
	}
	review, err := loadWorkflowReviewState(t.Context(), f.store.db, workID)
	if err != nil || review.Accepted == nil || review.Candidate == nil {
		t.Fatalf("read accepted Review: %+v, %v", review, err)
	}
	attempt := mustJSONValue(map[string]any{"step_id": "review", "note": "reviewed", "reviewer": "concord-review", "result": "pass", "commit": review.Candidate.Commit, "link": review.Accepted.ReportRef})
	if err := approvalPlanAction(t, f, "complete_step", attempt); err != nil {
		t.Fatalf("reviewed plan step: %v", err)
	}
	if err := approvalPlanAction(t, f, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`)); err != nil {
		t.Fatalf("confirmed plan completion: %v", err)
	}
	var lifecycle, state string
	if err := f.store.db.QueryRow(`SELECT w.lifecycle,i.instance_state FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&lifecycle, &state); err != nil || lifecycle != "completed" || state != "completed" {
		t.Fatalf("terminal lifecycle=%q instance=%q err=%v", lifecycle, state, err)
	}
	if latestApprovalEventActor(t, f.store, workID, WorkflowCompleted) != ownerRef || approvalKindEvidenceCount(t, f.store, workID) != 0 {
		t.Fatal("completion changed the actor or fabricated approval evidence")
	}
	assertNoFoldGuardRows(t, f.store)
}

func TestWorkflowPlanConfirmationRequiresCriterionProof(t *testing.T) {
	const workID = "plan-confirmation-no-verdict"
	f := qBuildAcceptance(t, workID, "", false)
	version, events := readWorkVersion(t, f.store, workID), approvalEventCount(t, f.store, workID)
	request := planAdmissionRequest(workID, "confirm_premise", version, f.owner, json.RawMessage(`{}`))
	_ = planAdmissionFailure(t, planAdmissionPreflight(t, f.store, request), KindMissingEvidence)
	_ = planAdmissionFailure(t, planAdmissionDispatch(t, f.store, request), KindMissingEvidence)
	assertApprovalRefusalUnchanged(t, f, version, events)
}

// The fold owns the same two terminal facts the admission checks: a raw
// completion event against a completed plan still refuses when the premise
// confirmation or the delivery record is missing.
func TestWorkflowCompletionFoldRequiresRecordedFacts(t *testing.T) {
	for _, fault := range []struct {
		name string
		sql  string
	}{
		{"missing-confirmation", `DELETE FROM workflow_premise_confirmations WHERE work_id=?`},
		{"missing-delivery", `DELETE FROM workflow_delivery_state WHERE work_id=?`},
	} {
		t.Run(fault.name, func(t *testing.T) {
			const workID = "completion-fold-facts"
			s, completion := seedCompletionGateCase(t, workID, completionGateCase{
				requiredEvidence: []string{"verification", "review"}, emptyMandate: true,
				includeVerdict: true, includePremise: true, verdictKind: "ok", omitClosure: true,
			})
			initialVersion := readWorkVersion(t, s, workID)
			completeReviewedPlanForTerminalTest(t, s, workID, workflowActorForRef(t, s, completion.Actor))
			completion = shiftWorkflowTestEventVersions(completion, readWorkVersion(t, s, workID)-initialVersion)
			execInFold(t, s, strings.Replace(fault.sql, "?", "'"+workID+"'", 1))
			err := CompleteWorkflow(t.Context(), s, completion)
			if !hasFailureKind(err, KindMissingEvidence) {
				t.Fatalf("fold completion with %s = %v, want missing_evidence", fault.name, err)
			}
			var state string
			if err := s.db.QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil || state != "running" {
				t.Fatalf("refused fold changed instance state: %q err=%v", state, err)
			}
		})
	}
}

func TestWorkflowPlanCompletionRequiresPlanAndRecordedFacts(t *testing.T) {
	const workID = "plan-completion-facts"
	f := qCurrentAcceptance(t, workID)
	version, events := readWorkVersion(t, f.store, workID), approvalEventCount(t, f.store, workID)
	confirmations := func() int {
		t.Helper()
		var count int
		if err := f.store.db.QueryRow(`SELECT count(*) FROM workflow_premise_confirmations WHERE work_id=?`, workID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	before := confirmations()
	request := planAdmissionRequest(workID, "complete", version, f.owner, json.RawMessage(`{"impact_verdict":"non-breaking"}`))
	_ = planAdmissionFailure(t, planAdmissionPreflight(t, f.store, request), KindIllegalLifecycleTransition)
	_ = planAdmissionFailure(t, planAdmissionDispatch(t, f.store, request), KindIllegalLifecycleTransition)
	completeWorkflowPlanFixture(t, f.store, workID)
	// The fixture seeds both terminal facts; remove the delivery record so
	// completion still refuses (CD-0242 D9) without touching other state.
	if _, err := f.store.db.Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM workflow_delivery_state WHERE work_id=?; DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}
	request = planAdmissionRequest(workID, "complete", version, f.owner, json.RawMessage(`{"impact_verdict":"non-breaking"}`))
	_ = planAdmissionFailure(t, planAdmissionPreflight(t, f.store, request), KindMissingEvidence)
	_ = planAdmissionFailure(t, planAdmissionDispatch(t, f.store, request), KindMissingEvidence)
	if got := readWorkVersion(t, f.store, workID); got != version || approvalEventCount(t, f.store, workID) != events || confirmations() != before {
		t.Fatalf("refused completion changed state: version=%d events=%d confirmations=%d", got, approvalEventCount(t, f.store, workID), confirmations())
	}
	assertNoFoldGuardRows(t, f.store)
}
