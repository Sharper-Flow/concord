package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

// seedCheckpointFailedReviewCorrection seeds a break_fix item at its verify
// checkpoint over the accepted repair candidate the return-route precedent
// produces. One review attempt fails, and
// its terminal derives the failure record (CD-0198 D5). A later Review
// completes with correction_required and stays unaccepted. The failure lives in
// that Review disposition, never in a non-ok verdict (CD-0242 D8). It returns
// the failed attempt, its epoch, the log position after the failure record,
// and the completed Review attempt with its epoch.
func seedCheckpointFailedReviewCorrection(t *testing.T, workID string) (fixture workflowReturnRouteFixture, failedID string, failedEpoch, failedSeq int64, reviewID string, reviewEpoch int64) {
	t.Helper()
	j := pReturnReviewFixture(t, workID, "workflow.break_fix", "")
	fixture = workflowReturnRouteFixture{store: j.s, owner: j.fixture.owner, operator: j.fixture.operator}
	parkWorkflowInstanceAt(t, j.s, workID, "verify")
	failedID = "attempt:" + workID + ":failed-review"
	failedEpoch = dispatchCheckpointReviewAttempt(t, fixture, workID, failedID)
	failCheckpointReviewAttempt(t, fixture.store, workID, failedID)
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT effect_seq FROM work_items WHERE id=?`, workID).Scan(&failedSeq); err != nil {
		t.Fatal(err)
	}
	reviewID = "attempt:" + workID + ":correction-review"
	reviewEpoch = dispatchCheckpointReviewAttempt(t, fixture, workID, reviewID)
	completeCheckpointReviewAttempt(t, fixture.store, workID, reviewID, "correction_required")
	return fixture, failedID, failedEpoch, failedSeq, reviewID, reviewEpoch
}

// workerAttemptPopulation counts the work item's materialized worker attempts
// and its dispatch events, so a test can prove request_correction minted none.
func workerAttemptPopulation(t *testing.T, s *Store, workID string) (attempts, dispatches int64) {
	t.Helper()
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempts WHERE work_id=?`, workID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, WorkerDispatched).Scan(&dispatches); err != nil {
		t.Fatal(err)
	}
	return attempts, dispatches
}

// assertFailedReviewCorrectionPreservesPinnedState pins what the checkpoint
// correction must leave exactly as it found it: the pinned definition digest
// identity, the single active contract, the failed attempt's failure
// history, and the absence of any authored verdict.
func assertFailedReviewCorrectionPreservesPinnedState(t *testing.T, s *Store, workID, definitionRef string) {
	t.Helper()
	ctx := context.Background()
	registered, ok := BuiltinWorkflowRegistry().Current(definitionRef)
	if !ok {
		t.Fatalf("workflow definition %s is not registered", definitionRef)
	}
	var digest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest != registered.Digest {
		t.Fatalf("pinned definition = %s, want the current %s", digest, registered.Digest)
	}
	var contracts int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&contracts); err != nil {
		t.Fatal(err)
	}
	if contracts != 1 {
		t.Fatalf("workflow contracts after the correction = %d, want the one approved contract", contracts)
	}
	var failures int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, WorkerFailed).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures != 1 {
		t.Fatalf("worker failure history after the correction = %d, want the one failed review", failures)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	verdicts, err := latestWorkflowVerdicts(ctx, tx, workID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(verdicts) != 0 {
		t.Fatalf("verdicts after the correction = %+v, want none: the failure lives in the Review disposition", verdicts)
	}
}

func requestCorrectionRefusalClass(t *testing.T, s *Store, workID string) (FailureKind, string) {
	t.Helper()
	_, _, err := WorkflowActionDefinitionFor(context.Background(), s, BuiltinWorkflowRegistry(), workID, "request_correction")
	if err == nil {
		return "", ""
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("correction resolution error=%v, want a store failure", err)
	}
	return failure.Kind, failure.Detail
}

// TestCheckpointFailedReviewCorrectionReturnsBreakFixToRepair pins the
// CD-0143 D1 and CD-0193 D1 return from the verify checkpoint. A failed review
// attempt derives its own failure record (CD-0198 D5), and that disposition
// never reads as an accepted delivery. The correction is admitted only by a
// current accepted correction_required Review (CD-0242 D8). It returns
// break_fix to repair along the declared route, and only the fresh repair
// dispatch carries the bounded correction context.
func TestCheckpointFailedReviewCorrectionReturnsBreakFixToRepair(t *testing.T) {
	const workID = "checkpoint-failed-review-correction"
	fixture, attemptID, epoch, failedSeq, reviewID, reviewEpoch := seedCheckpointFailedReviewCorrection(t, workID)
	s := fixture.store
	ctx := context.Background()

	if records := s2DerivedFailureRecords(t, s, workID, attemptID); len(records) != 1 || records[0]["step"] != "verify" || records[0]["epoch"] != epoch {
		t.Fatalf("checkpoint failure records = %+v, want one derived at verify epoch %d", records, epoch)
	}
	_, accepted, err := workflowAcceptedWorkerDelivery(ctx, s.db, workID, failedSeq, "checkpoint_correction_test")
	if err != nil {
		t.Fatal(err)
	}
	if accepted {
		t.Fatal("the checkpoint failure disposition reads as an accepted worker delivery")
	}
	if kind, detail := requestCorrectionRefusalClass(t, s, workID); kind == "" {
		t.Fatalf("request_correction resolved before Main accepted the correction_required Review (%q)", detail)
	}
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", json.RawMessage(`{"attempt_id":"`+reviewID+`","attempt_epoch":`+fmt.Sprint(reviewEpoch)+`}`), 0, fixture.owner); err != nil {
		t.Fatalf("Main acceptance of the correction_required Review: %v", err)
	}
	if pin := issue1013Pin(t, s, workID); pin.Review == nil || pin.Review.Accepted == nil || pin.Review.Accepted.Report.Disposition != "correction_required" || !issue1013HasIntent(pin, "request_correction") {
		t.Fatalf("accepted correction_required Review opened no correction route: %+v", pin)
	}

	correction := json.RawMessage(`{"diagnosis":"the review failed while the delivered subject still mismatches","strategy":"rebuild the helper and re-verify with a fresh review","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	attemptsBefore, dispatchesBefore := workerAttemptPopulation(t, s, workID)
	if err := runVerdictActionAs(t, s, workID, "request_correction", correction, 0, fixture.owner); err != nil {
		t.Fatalf("request correction after the accepted correction_required Review: %v", err)
	}
	if got := currentStep(t, s, workID); got != "repair" {
		t.Fatalf("step after the checkpoint correction = %q, want repair", got)
	}
	// request_correction returns along the pinned graph; it never mints a
	// worker attempt or a dispatch window. Only the normal execution-step
	// dispatch that follows mints a fresh attempt identity.
	if attempts, dispatches := workerAttemptPopulation(t, s, workID); attempts != attemptsBefore || dispatches != dispatchesBefore {
		t.Fatalf("request_correction changed the attempt population: attempts %d->%d, dispatches %d->%d", attemptsBefore, attempts, dispatchesBefore, dispatches)
	}

	// The fresh attempt context at the owning execution step carries the
	// bounded mismatch and consumes the recorded request.
	returned, err := workflowCorrectionContextForDispatch(ctx, s.db, workID, "repair", "attempt:next")
	if err != nil {
		t.Fatal(err)
	}
	if returned == nil || returned.PredicateIDs[0] != "predicate:return-route" || returned.EvidenceRefs[0] != "evidence:return-route-verification" || returned.Diagnosis != "the review failed while the delivered subject still mismatches" {
		t.Fatalf("returned correction context = %+v, want the first bounded checkpoint correction", returned)
	}
	if attemptErr := runVerdictActionAs(t, s, workID, "request_correction", correction, 0, fixture.owner); attemptErr == nil {
		t.Fatal("second request_correction admitted before a fresh Review")
	}
	// The return carries no law, contract, history, or evidence edit: the
	// pinned digest, the one contract, and the failure record survive
	// unchanged, and no verdict was authored.
	assertFailedReviewCorrectionPreservesPinnedState(t, s, workID, "workflow.break_fix")

	// The return reopens the owning execution step's normal dispatch: a fresh
	// fenced attempt mints a new identity and a strictly incremented epoch,
	// never a reuse of the dispositioned review attempt.
	pin := issue1013Pin(t, s, workID)
	var repairEpoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(attempt_epoch),0) FROM workflow_step_epochs WHERE work_id=? AND step_id='repair'`, workID).Scan(&repairEpoch); err != nil {
		t.Fatal(err)
	}
	fresh := "attempt:" + workID + ":fresh"
	if fresh == attemptID {
		t.Fatal("the fresh attempt identity reuses the dispositioned review attempt")
	}
	implement := laneForID(t)
	packet := joinPacketFor(t, s, workID, "repair", fresh, "implement", implement.Digest)
	packet["inputs"].(map[string]any)["correction"] = pin.Correction
	attachReadyWorkerJobIfJobCapable(t, s, workID, "repair", fixture.owner, packet)
	if _, err := dispatchJoinAttempt(ctx, t, s, workID, readWorkVersion(t, s, workID), fixture.owner, packet); err != nil {
		t.Fatalf("fresh fenced attempt after the checkpoint correction: %v", err)
	}
	var freshEpoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM worker_authorizations WHERE work_id=? AND attempt_id=?`, workID, fresh).Scan(&freshEpoch); err != nil {
		t.Fatal(err)
	}
	if freshEpoch != repairEpoch+1 {
		t.Fatalf("fresh dispatch epoch = %d, want the strictly incremented %d", freshEpoch, repairEpoch+1)
	}

	// A late report for the dispositioned attempt stays fenced: the attempt
	// reached its failed terminal state, so neither a late completion report
	// nor a late acceptance can land after the correction returned.
	lateLane := reviewGateLane(t, "review")
	lateCompletion := workerCompleteEventForLane(workID, "checkpoint-late-completion-"+workID, attemptID, lateLane, time.Unix(40, 0).UTC(), "")
	lateErr := ApplyOperation(ctx, s, Operation{Events: []Event{lateCompletion}})
	var lateFailure *Failure
	if !errors.As(lateErr, &lateFailure) || lateFailure.Kind != KindProjectionConflict {
		t.Fatalf("late completion for the dispositioned attempt = %v, want a projection-conflict fence", lateErr)
	}
	if acceptErr := runVerdictActionAs(t, s, workID, "accept_worker_result", json.RawMessage(`{"attempt_id":"`+attemptID+`","attempt_epoch":`+fmt.Sprint(epoch)+`}`), 0, fixture.owner); acceptErr == nil {
		t.Fatal("late acceptance of the dispositioned attempt admitted after the return")
	}
}
