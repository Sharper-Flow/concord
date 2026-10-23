package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// seedCheckpointFailedReviewMismatchOn seeds the same issue 1062 shape on the
// named workflow kind, parked at that kind's verification checkpoint.
func seedCheckpointFailedReviewMismatchOn(t *testing.T, workID, definitionRef, checkpoint string) (workflowReturnRouteFixture, string, int64) {
	t.Helper()
	return seedCheckpointFailedReviewMismatchWith(t, workID, attributionInactiveWorkflowHeadForTest(t, definitionRef), checkpoint)
}

// seedCheckpointFailedReviewMismatchAt seeds the issue 1062 shape on one
// named definition version. The return-to-effect tests pin the version before
// the CD-0205 worker-job lifecycle: their fresh implement dispatch carries no
// worker job, which a job-capable pin refuses; the job-bound return route is
// covered by TestCheckpointCorrectionReturnDispatchesARecordedWorkerJob.
func seedCheckpointFailedReviewMismatchAt(t *testing.T, workID, definitionRef string, version int64, checkpoint string) (workflowReturnRouteFixture, string, int64) {
	t.Helper()
	registered, ok := BuiltinWorkflowRegistry().Lookup(definitionRef, version)
	if !ok {
		t.Fatalf("workflow definition %s@%d is not registered", definitionRef, version)
	}
	return seedCheckpointFailedReviewMismatchWith(t, workID, registered, checkpoint)
}

func seedCheckpointFailedReviewMismatchWith(t *testing.T, workID string, registered RegisteredDefinition, checkpoint string) (workflowReturnRouteFixture, string, int64) {
	t.Helper()
	fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, registered, checkpoint, []string{"verification"}, []string{"verification", "artifact"})
	seedCheckpointReviewAcceptedDelivery(t, fixture.store, workID, fixture.owner, checkpointEffectStep(t, fixture.store, workID, checkpoint), checkpoint)
	attemptID := "attempt:" + workID + ":failed-review"
	epoch := dispatchCheckpointReviewAttempt(t, fixture, workID, checkpoint, attemptID)
	failCheckpointReviewAttempt(t, fixture.store, workID, attemptID)
	reviewer := checkpointReviewReviewer(t, fixture.store, workID)
	if err := runVerdictActionAs(t, fixture.store, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"verdicts":[{"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}]}`), 0, reviewer); err != nil {
		t.Fatalf("record mismatch verdict after the failed review: %v", err)
	}
	return fixture, attemptID, epoch
}

// workerAttemptPopulation counts the work item's materialized worker attempts
// and its dispatch events, so a test can prove request_correction minted none.
func workerAttemptPopulation(t *testing.T, s *Store, workID string) (attempts, dispatches int64) {
	t.Helper()
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempts WHERE work_id=?`, workID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkerDispatched).Scan(&dispatches); err != nil {
		t.Fatal(err)
	}
	return attempts, dispatches
}

// assertFailedReviewCorrectionPreservesPinnedState pins what the checkpoint
// correction must leave exactly as it found it: the pinned definition digest
// and version, the single active contract, the failure verdict with its bound
// evidence, and the failed attempt's failure history.
func assertFailedReviewCorrectionPreservesPinnedState(t *testing.T, s *Store, workID, definitionRef string, definitionVersion int64) {
	t.Helper()
	ctx := context.Background()
	registered, ok := BuiltinWorkflowRegistry().Lookup(definitionRef, definitionVersion)
	if !ok {
		t.Fatalf("workflow definition %s@%d is not registered", definitionRef, definitionVersion)
	}
	var digest string
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_digest,definition_version FROM workflow_instances WHERE work_id=?`, workID).Scan(&digest, &version); err != nil {
		t.Fatal(err)
	}
	if digest != registered.Digest || version != registered.Definition.Version {
		t.Fatalf("pinned definition = v%d %s, want the released v%d %s", version, digest, registered.Definition.Version, registered.Digest)
	}
	var contracts int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&contracts); err != nil {
		t.Fatal(err)
	}
	if contracts != 1 {
		t.Fatalf("workflow contracts after the correction = %d, want the one approved contract", contracts)
	}
	var failures int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkerFailed).Scan(&failures); err != nil {
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
	if len(verdicts) != 1 || verdicts[0].VerdictKind != "outcome_mismatch" || !verdicts[0].IncomparableWithApproved || !slices.Contains(verdicts[0].EvaluationEvidence, "evidence:return-route-verification") {
		t.Fatalf("latest verdicts after the correction = %+v, want the bound mismatch verdict with its evidence", verdicts)
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

// TestCheckpointFailedReviewCorrectionReturnsBreakFixToRepair pins the amended
// CD-0143 D1 and CD-0193 D1 return: after the latest review attempt a worker
// dispatched at the verify checkpoint failed and the exact hold-mode failure
// record dispositions it, a current non-ok verdict admits the operator-
// approved evidence-bearing correction, the pinned graph returns break_fix to
// repair, the failure disposition never reads as an accepted delivery, and
// only the fresh execution-step dispatch carries the bounded mismatch context.
func TestCheckpointFailedReviewCorrectionReturnsBreakFixToRepair(t *testing.T) {
	const workID = "checkpoint-failed-review-correction"
	fixture, attemptID, epoch := seedCheckpointFailedReviewMismatchAt(t, workID, "workflow.break_fix", 19, "verify")
	s := fixture.store
	ctx := context.Background()

	kind, detail := requestCorrectionRefusalClass(t, s, workID)
	if kind != KindInvalidOperation || !strings.Contains(detail, "failure record that dispositions the failed review attempt") {
		t.Fatalf("refusal before the record = %s %q, want the failure-disposition class", kind, detail)
	}

	if err := runVerdictActionAs(t, s, workID, "record_worker_failure", json.RawMessage(`{"attempt_id":"`+attemptID+`","attempt_epoch":`+fmt.Sprint(epoch)+`}`), 0, fixture.owner); err != nil {
		t.Fatalf("record_worker_failure at the verify checkpoint refused: %v", err)
	}

	// The failure disposition is not an accepted success: the primary
	// accepted-completed-delivery route stays closed behind the same verdict.
	var verdictSeq int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowVerdictRecorded).Scan(&verdictSeq); err != nil {
		t.Fatal(err)
	}
	_, accepted, err := workflowAcceptedWorkerDelivery(ctx, s.db, workID, verdictSeq, "checkpoint_correction_test")
	if err != nil {
		t.Fatal(err)
	}
	if accepted {
		t.Fatal("the checkpoint failure disposition reads as an accepted worker delivery")
	}

	correction := json.RawMessage(`{"diagnosis":"the review failed while the delivered subject still mismatches","strategy":"rebuild the helper and re-verify with a fresh review","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	attemptsBefore, dispatchesBefore := workerAttemptPopulation(t, s, workID)
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", correction, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("request correction after the checkpoint failure record: %v", err)
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
	if attemptErr := runIssue933OperatorAction(t, s, workID, "request_correction", correction, fixture.owner, fixture.operator); attemptErr == nil {
		t.Fatal("second request_correction admitted before a fresh verdict")
	}
	// The return carries no law, contract, history, or evidence edit: the
	// pinned digest, the one contract, the failure verdict, and the failure
	// record all survive unchanged.
	assertFailedReviewCorrectionPreservesPinnedState(t, s, workID, "workflow.break_fix", 19)

	// The return reopens the owning execution step's normal dispatch: a fresh
	// fenced attempt mints a new identity and a strictly incremented epoch,
	// never a reuse of the dispositioned review attempt.
	pin := issue1013Pin(t, s, workID)
	var repairEpoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(json_extract(payload,'$.attempt_epoch')),0) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.step_id')='repair'`, workID, WorkflowActionStarted).Scan(&repairEpoch); err != nil {
		t.Fatal(err)
	}
	fresh := "attempt:" + workID + ":fresh"
	if fresh == attemptID {
		t.Fatal("the fresh attempt identity reuses the dispositioned review attempt")
	}
	if err := dispatchSameStepAttempt(t, s, workID, "repair", fresh, fixture.owner, pin.Correction); err != nil {
		t.Fatalf("fresh fenced attempt after the checkpoint correction: %v", err)
	}
	var freshEpoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, fresh).Scan(&freshEpoch); err != nil {
		t.Fatal(err)
	}
	if freshEpoch != repairEpoch+1 {
		t.Fatalf("fresh dispatch epoch = %d, want the strictly incremented %d", freshEpoch, repairEpoch+1)
	}

	// A late report for the dispositioned attempt stays fenced: the attempt
	// reached its failed terminal state, so neither a late completion report
	// nor a late acceptance can land after the correction returned.
	lateLane := reviewGateLane(t, "review")
	lateCompletion := Event{EventID: "checkpoint-late-completion-" + workID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(40, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lateLane), ReportSchemaVersion: WorkerReportSchemaVersion})}
	lateErr := ApplyOperation(ctx, s, Operation{Events: []Event{lateCompletion}})
	var lateFailure *Failure
	if !errors.As(lateErr, &lateFailure) || lateFailure.Kind != KindProjectionConflict {
		t.Fatalf("late completion for the dispositioned attempt = %v, want a projection-conflict fence", lateErr)
	}
	if acceptErr := runVerdictActionAs(t, s, workID, "accept_worker_result", json.RawMessage(`{"attempt_id":"`+attemptID+`","attempt_epoch":`+fmt.Sprint(epoch)+`}`), 0, fixture.owner); acceptErr == nil {
		t.Fatal("late acceptance of the dispositioned attempt admitted after the return")
	}
}

// requestCorrectionPreflightRefusalClass classifies the refusal the owning
// transactional preflight returns for request_correction: the surface behind
// WorkflowActionPreflightWithRegistry and the boundary coordinator.
