package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Refine exit requires a passing report covering the recorded Review candidate.

func refineProofStartRefine(t *testing.T, fixture workflowReturnRouteFixture, workID string) {
	t.Helper()
	reviewGateStartStep(t, fixture.store, workID, "refine", "start_refine", fixture.owner)
}

// refineProofCurrentFixture seeds the refine-proof work on the family's
// current definition with verification, review, and artifact evidence bound.
func refineProofCurrentFixture(t *testing.T, workID, definitionRef string) workflowReturnRouteFixture {
	t.Helper()
	return seedWorkflowReturnRouteFixtureWithDefinition(t, workID, mustBuiltinDefinition(t, definitionRef), "refine", []string{"verification"}, []string{"verification", "review", "artifact"})
}

// Leaving execution requires a recorded ready Review of the claimed candidate.
func refineProofAcceptReadyReview(t *testing.T, fixture workflowReturnRouteFixture, workID string) {
	t.Helper()
	reviewSubjectClaim(t, fixture.store, workID)
	f := workContextFixture{store: fixture.store, workID: workID, owner: fixture.owner, operator: fixture.operator}
	acceptCurrentReviewForTest(t, f, "ready_for_final_proof")
}

// refineProofCandidateFixture seeds the refine-proof work without a
// pre-bound verification, so every bound verification a test reads is one it
// produced.
func refineProofCandidateFixture(t *testing.T, workID, definitionRef string) workflowReturnRouteFixture {
	t.Helper()
	return seedWorkflowReturnRouteFixtureWithDefinition(t, workID, mustBuiltinDefinition(t, definitionRef), "refine", []string{"verification"}, []string{"review", "artifact"})
}

// refineProofCandidateRun binds a coordinator's report for a recorded commit.
func refineProofCandidateRun(t *testing.T, s *Store, workID, name, head string) {
	t.Helper()
	report := VerificationReport{Source: "local", Commit: head, Result: "pass", RunRef: "run:" + name}
	if err := runVerdictActionAs(t, s, workID, "bind_evidence", mustJSONValue(map[string]any{"evidence_kind": "verification", "immutable_subject_ref": "commit:" + head, "verification_report": report}), 0, reviewGateAcceptor(workID)); err != nil {
		t.Fatalf("bind report %s: %v", name, err)
	}
}

// refineProofCandidate is the accepted Review's integrated candidate commit.
func refineProofCandidate(t *testing.T, s *Store, workID string) string {
	t.Helper()
	candidate := issue1013Pin(t, s, workID).Review.Candidate
	if candidate == nil {
		t.Fatal("no integrated candidate after the accepted Review")
	}
	return candidate.Commit
}

func refineProofDelivery(t *testing.T, s *Store, workID string) error {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	actor := reviewGateAcceptor(workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	payload := json.RawMessage(`{"delivery_artifact":"artifact:refine-proof-` + workID + `","delivery_state":"asserted"}`)
	_, err = applyWorkflowActionRawTx(context.Background(), tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		EvidenceRefs: actionEvidenceRefs("record_delivery", payload), WorkID: workID, ExpectedVersion: version, ActionID: "record_delivery", Payload: payload, Actor: actor,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64) + fmt.Sprint(version), IdempotencyIdentity: "record_delivery-refine-proof-" + workID + "-" + fmt.Sprint(version), OperationID: "record_delivery-refine-proof-" + workID + "-" + fmt.Sprint(version),
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "record_delivery-refine-proof-" + workID + "-" + fmt.Sprint(version), RequestID: "request:refine-proof-" + workID, ContractDigest: testManifestDigest, Now: time.Unix(9, 0).UTC(),
	})
	if err != nil {
		return err
	}
	return tx.Commit()
}

func refineProofRequireMissingEvidence(t *testing.T, err error, wantContains string) {
	t.Helper()
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || failure.Kind != KindMissingEvidence {
		t.Fatalf("record_delivery = %v, want a missing-evidence refusal", err)
	}
	if !strings.Contains(failure.Detail, wantContains) {
		t.Fatalf("refusal %q does not name %q", failure.Detail, wantContains)
	}
}

// refineProofBindVerification binds one verification evidence. The fold
// validates the binding's producer operation, so the caller passes a
// producerRunRef that names a completed durable operation for this work:
// the verify run's own operation ref, or a seeded workflow.test authority.
func refineProofBindVerification(t *testing.T, s *Store, workID, ref, producerRunRef string) {
	t.Helper()
	ownerRef, err := WorkflowActorRef(reviewGateAcceptor(workID))
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEventWithActor(workID+":refine-proof-bind:"+ref, WorkflowEvidenceBound, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1, "evidence_kind": "verification",
		"immutable_subject_ref": ref, "producer_id": "principal/operator", "producer_run_ref": producerRunRef,
		"producer_watermark": "request/verify", "observed_at": "2026-09-29T00:00:00Z",
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("bind the verification evidence: %v", err)
	}
}

func TestRefineExitRefusesWithoutGreenVerifyRunInEpoch(t *testing.T) {
	const workID = "refine-proof-refusal"
	c := acceptDeliveryUnobservedCandidateAt(t, workID, "workflow.implementation")
	s := c.f.store

	// The retained candidate has no applicable producer receipt.
	refineProofRequireMissingEvidence(t, refineProofDelivery(t, s, workID), "no passing verification report covers the recorded Review candidate")

	// A binding that names a workflow operation id proves nothing. The
	// seeded workflow.test authority carries the binding's producer identity
	// and records the reference among its evidence.
	seedWorkflowAuthority(t, s, "return-authority-refine-proof-"+workID, workID, "principal/operator", "request/verify", []string{"evidence:return-route-verification"})
	refineProofBindVerification(t, s, workID, "evidence:return-route-verification", "return-authority-refine-proof-"+workID)
	refineProofRequireMissingEvidence(t, refineProofDelivery(t, s, workID), "verification does not cover the current integrated candidate")

	// The green run in the epoch admits the exit.
	acceptDeliveryBindFresh(t, c, "exit")
	if err := refineProofDelivery(t, s, workID); err != nil {
		t.Fatalf("record_delivery with the candidate's green run: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
}

func TestRefineExitRefusesAStaleVerifyRun(t *testing.T) {
	const workID = "refine-proof-stale-run"
	c := acceptDeliveryCorrectedCandidateAt(t, workID, "workflow.implementation")
	s := c.f.store
	c.j.bindVerification(t, c.early)
	// The retained report must cover the recorded candidate.
	before := c.j.effects(t)
	refineProofRequireMissingEvidence(t, refineProofDelivery(t, s, workID), "verification does not cover the current integrated candidate")
	if c.j.effects(t) != before {
		t.Fatal("refused stale proof changed durable state")
	}
	reviewGateRequireStep(t, s, workID, "refine")

	refineProofCandidateRun(t, s, workID, "corrected", refineProofCandidate(t, s, workID))
	if err := refineProofDelivery(t, s, workID); err != nil {
		t.Fatalf("record_delivery with corrected candidate proof: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
}

func TestRefineExitRefusesWithoutGreenVerifyRunOnBreakFix(t *testing.T) {
	const workID = "refine-proof-break-fix"
	c := acceptDeliveryUnobservedCandidateAt(t, workID, "workflow.break_fix")
	s := c.f.store
	refineProofRequireMissingEvidence(t, refineProofDelivery(t, s, workID), "no passing verification report covers the recorded Review candidate")
	acceptDeliveryBindFresh(t, c, "exit")
	if err := refineProofDelivery(t, s, workID); err != nil {
		t.Fatalf("record_delivery with the candidate's green run: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
}

func TestRefineExitRefusesFailedReport(t *testing.T) {
	const workID = "refine-proof-failed-report"
	c := acceptDeliveryUnobservedCandidateAt(t, workID, "workflow.implementation")
	commit := refineProofCandidate(t, c.f.store, workID)
	report := VerificationReport{Source: "required_ci", Commit: commit, Result: "fail", RunRef: "run:failed"}
	err := runVerdictActionAs(t, c.f.store, workID, "bind_evidence", mustJSONValue(map[string]any{"evidence_kind": "verification", "immutable_subject_ref": "commit:" + commit, "verification_report": report}), 0, reviewGateAcceptor(workID))
	if err != nil {
		t.Fatal(err)
	}
	refineProofRequireMissingEvidence(t, refineProofDelivery(t, c.f.store, workID), "verification does not cover the current integrated candidate")
	refineProofCandidateRun(t, c.f.store, workID, "passing", commit)
	if err := refineProofDelivery(t, c.f.store, workID); err != nil {
		t.Fatalf("passing replacement report refused: %v", err)
	}
}

func TestProjectWithoutManifestPassesOnGreenRunRefusesWithoutRun(t *testing.T) {
	const workID = "refine-proof-no-manifest"
	c := acceptDeliveryUnobservedCandidateAt(t, workID, "workflow.implementation")
	s := c.f.store

	refineProofRequireMissingEvidence(t, refineProofDelivery(t, s, workID), "no passing verification report covers the recorded Review candidate")

	// A recorded passing report qualifies without a tooling manifest.
	c.j.bindVerification(t, c.j.observeRun(t, "exit"))
	if err := refineProofDelivery(t, s, workID); err != nil {
		t.Fatalf("record_delivery without a manifest: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
}

func TestRefineProofStartRecorded(t *testing.T) {
	const workID = "refine-proof-start-recorded"
	fixture := refineProofCurrentFixture(t, workID, "workflow.implementation")
	refineProofStartRefine(t, fixture, workID)

	// The current refine start bounds the epoch in event history: the start
	// event predates every binding the guard accepts.
	var startSeq int64
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT latest_start_seq FROM workflow_step_epochs WHERE work_id=? AND step_id='refine'`, workID).Scan(&startSeq); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_type='work_item' AND subject_id=? AND kind=? AND effect_seq>?`, workID, WorkflowActionStarted, startSeq).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("%d workflow action starts follow the refine start", count)
	}
}
