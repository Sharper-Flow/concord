package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// seedCheckpointFailedReviewMismatch leaves a break-fix item at its verify
// checkpoint with one failed review-lane attempt and a current non-ok verdict
// recorded after that failure, the issue 1062 shape: the failed review is the
// latest worker dispatch behind the verdict, so the accepted-completed-
// delivery correction route finds no accepted delivery and the item wedges.
func seedCheckpointFailedReviewMismatch(t *testing.T, workID string) (workflowReturnRouteFixture, string, int64) {
	t.Helper()
	return seedCheckpointFailedReviewMismatchOn(t, workID, "workflow.break_fix", "verify")
}

// seedCheckpointFailedReviewMismatchOn seeds the same issue 1062 shape on the
// named workflow kind, parked at that kind's verification checkpoint.
func seedCheckpointFailedReviewMismatchOn(t *testing.T, workID, definitionRef, checkpoint string) (workflowReturnRouteFixture, string, int64) {
	t.Helper()
	fixture := seedWorkflowReturnRouteFixtureRequiring(t, workID, definitionRef, checkpoint, []string{"verification"}, []string{"verification", "artifact"})
	attemptID := "attempt:" + workID + ":failed-review"
	epoch := dispatchCheckpointReviewAttempt(t, fixture, workID, checkpoint, attemptID)
	failCheckpointReviewAttempt(t, fixture.store, workID, attemptID)
	reviewer := checkpointReviewReviewer(t, fixture.store, workID)
	if err := runVerdictActionAs(t, fixture.store, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}`), 0, reviewer); err != nil {
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
func assertFailedReviewCorrectionPreservesPinnedState(t *testing.T, s *Store, workID, definitionRef string) {
	t.Helper()
	ctx := context.Background()
	registered, err := BuiltinWorkflowDefinitionForRef(definitionRef)
	if err != nil {
		t.Fatal(err)
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
	if len(verdicts) != 1 || verdicts[0].VerdictKind != "outcome_mismatch" || !verdicts[0].IncomparableWithApproved || !contains(verdicts[0].EvaluationEvidence, "evidence:return-route-verification") {
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
	fixture, attemptID, epoch := seedCheckpointFailedReviewMismatch(t, workID)
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
	if returned == nil || returned.AttemptCount != 1 || returned.PredicateIDs[0] != "predicate:return-route" || returned.EvidenceRefs[0] != "evidence:return-route-verification" || returned.Diagnosis != "the review failed while the delivered subject still mismatches" {
		t.Fatalf("returned correction context = %+v, want the first bounded checkpoint correction", returned)
	}
	if attemptErr := runIssue933OperatorAction(t, s, workID, "request_correction", correction, fixture.owner, fixture.operator); attemptErr == nil {
		t.Fatal("second request_correction admitted before a fresh verdict")
	}
	// The return carries no law, contract, history, or evidence edit: the
	// pinned digest, the one contract, the failure verdict, and the failure
	// record all survive unchanged.
	assertFailedReviewCorrectionPreservesPinnedState(t, s, workID, "workflow.break_fix")

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
	if err := dispatchSameStepAttempt(t, s, workID, "repair", fresh, fixture.owner, false, pin.Correction); err != nil {
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
// InspectWorkflowActionAdmission and the boundary coordinator.
func requestCorrectionPreflightRefusalClass(t *testing.T, s *Store, workID string, actor WorkflowActor) (FailureKind, string) {
	t.Helper()
	payload := json.RawMessage(`{"diagnosis":"the delivered subject still mismatches","strategy":"repair and re-verify","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	err := InspectWorkflowActionAdmission(context.Background(), s, WorkflowActionPreflightRequest{WorkID: workID, ActionID: "request_correction", Payload: payload, Actor: actor})
	if err == nil {
		return "", ""
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("correction preflight error=%v, want a store failure", err)
	}
	return failure.Kind, failure.Detail
}

// TestCheckpointFailedReviewCorrectionPreflightMatchesTheResolver pins the
// transactional-preflight parity the shared prerequisite derivation owes every
// surface: the owning request boundary refuses with the same missing-
// prerequisite class the resolver, the fold guard, and the read-only preflight
// name, never with the generic off-step refusal.
func TestCheckpointFailedReviewCorrectionPreflightMatchesTheResolver(t *testing.T) {
	t.Run("absent verdict", func(t *testing.T) {
		const workID = "checkpoint-preflight-class-verdict"
		fixture, attemptID, epoch := seedCheckpointFailedReview(t, workID)
		if err := runVerdictActionAs(t, fixture.store, workID, "record_worker_failure", json.RawMessage(`{"attempt_id":"`+attemptID+`","attempt_epoch":`+fmt.Sprint(epoch)+`}`), 0, fixture.owner); err != nil {
			t.Fatalf("record_worker_failure refused: %v", err)
		}
		kind, detail := requestCorrectionPreflightRefusalClass(t, fixture.store, workID, fixture.owner)
		if kind != KindInvalidOperation || !strings.Contains(detail, "non-ok verification verdict") {
			t.Fatalf("healthy-verdict preflight refusal = %s %q, want the verdict class", kind, detail)
		}
	})
	t.Run("missing accepted delivery", func(t *testing.T) {
		const workID = "checkpoint-preflight-class-delivery"
		fixture := seedWorkflowReturnRouteFixtureRequiring(t, workID, "workflow.break_fix", "verify", []string{"verification", "review"}, []string{"verification", "artifact"})
		reviewer := checkpointReviewReviewer(t, fixture.store, workID)
		dispatchVerifyReviewAttempt(t, fixture, workID)
		if err := runVerdictActionAs(t, fixture.store, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}`), 0, reviewer); err != nil {
			t.Fatalf("record mismatch verdict: %v", err)
		}
		kind, detail := requestCorrectionPreflightRefusalClass(t, fixture.store, workID, fixture.owner)
		if kind != KindInvalidOperation || !strings.Contains(detail, "accepted worker delivery") {
			t.Fatalf("unaccepted review preflight refusal = %s %q, want the accepted-delivery class", kind, detail)
		}
	})
	t.Run("missing failure disposition", func(t *testing.T) {
		const workID = "checkpoint-preflight-class-disposition"
		fixture, _, _ := seedCheckpointFailedReviewMismatch(t, workID)
		kind, detail := requestCorrectionPreflightRefusalClass(t, fixture.store, workID, fixture.owner)
		resolverKind, resolverDetail := requestCorrectionRefusalClass(t, fixture.store, workID)
		if kind != resolverKind || detail != resolverDetail {
			t.Fatalf("preflight refusal = %s %q, resolver refusal = %s %q, want parity on the issue 1062 mismatch", kind, detail, resolverKind, resolverDetail)
		}
		if kind != KindInvalidOperation || !strings.Contains(detail, "failure record that dispositions the failed review attempt") {
			t.Fatalf("unrecorded failed review preflight refusal = %s %q, want the failure-disposition class", kind, detail)
		}
	})
}

// TestCheckpointFailedReviewCorrectionNamesEachMissingPrerequisite pins the
// three refusal classes the shared prerequisite derivation names: absent
// verdict, missing accepted delivery, and missing failure disposition.
func TestCheckpointFailedReviewCorrectionNamesEachMissingPrerequisite(t *testing.T) {
	t.Run("absent verdict", func(t *testing.T) {
		const workID = "checkpoint-correction-class-verdict"
		fixture, attemptID, epoch := seedCheckpointFailedReview(t, workID)
		if err := runVerdictActionAs(t, fixture.store, workID, "record_worker_failure", json.RawMessage(`{"attempt_id":"`+attemptID+`","attempt_epoch":`+fmt.Sprint(epoch)+`}`), 0, fixture.owner); err != nil {
			t.Fatalf("record_worker_failure refused: %v", err)
		}
		kind, detail := requestCorrectionRefusalClass(t, fixture.store, workID)
		if kind != KindInvalidOperation || !strings.Contains(detail, "non-ok verification verdict") {
			t.Fatalf("healthy-verdict refusal = %s %q, want the verdict class", kind, detail)
		}
	})
	t.Run("missing accepted delivery", func(t *testing.T) {
		const workID = "checkpoint-correction-class-delivery"
		fixture := seedWorkflowReturnRouteFixtureRequiring(t, workID, "workflow.break_fix", "verify", []string{"verification", "review"}, []string{"verification", "artifact"})
		reviewer := checkpointReviewReviewer(t, fixture.store, workID)
		dispatchVerifyReviewAttempt(t, fixture, workID)
		if err := runVerdictActionAs(t, fixture.store, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}`), 0, reviewer); err != nil {
			t.Fatalf("record mismatch verdict: %v", err)
		}
		kind, detail := requestCorrectionRefusalClass(t, fixture.store, workID)
		if kind != KindInvalidOperation || !strings.Contains(detail, "accepted worker delivery") {
			t.Fatalf("unaccepted review refusal = %s %q, want the accepted-delivery class", kind, detail)
		}
	})
	t.Run("missing failure disposition", func(t *testing.T) {
		const workID = "checkpoint-correction-class-disposition"
		fixture, _, _ := seedCheckpointFailedReviewMismatch(t, workID)
		kind, detail := requestCorrectionRefusalClass(t, fixture.store, workID)
		if kind != KindInvalidOperation || !strings.Contains(detail, "failure record that dispositions the failed review attempt") {
			t.Fatalf("unrecorded failed review refusal = %s %q, want the failure-disposition class", kind, detail)
		}
	})
}

// checkpointFailedReviewKinds names both corrected workflow kinds with their
// verification checkpoint and owning external-effect step.
var checkpointFailedReviewKinds = []struct {
	ref        string
	checkpoint string
	effectStep string
	workID     string
}{
	{"workflow.break_fix", "verify", "repair", "consumed-verdict-break-fix"},
	{"workflow.implementation", "acceptance", "execution", "consumed-verdict-implementation"},
}

// TestCheckpointFailedReviewConsumedVerdictCannotReopenCorrection pins the
// stale-negative gate: once the dispositioned failed review's correction has
// returned to the owning step and a fresh delivery is accepted, the original
// verdict and the historical failure record cannot reopen correction. The
// pin, the preflight, the resolver, and the operator action all refuse from
// the one derivation and name the absent fresh verdict.
func TestCheckpointFailedReviewConsumedVerdictCannotReopenCorrection(t *testing.T) {
	for _, tc := range checkpointFailedReviewKinds {
		t.Run(tc.ref, func(t *testing.T) {
			fixture, attemptID, epoch := seedCheckpointFailedReviewMismatchOn(t, tc.workID, tc.ref, tc.checkpoint)
			s := fixture.store
			ctx := context.Background()
			if err := runVerdictActionAs(t, s, tc.workID, "record_worker_failure", json.RawMessage(`{"attempt_id":"`+attemptID+`","attempt_epoch":`+fmt.Sprint(epoch)+`}`), 0, fixture.owner); err != nil {
				t.Fatalf("record_worker_failure refused: %v", err)
			}
			correction := json.RawMessage(`{"diagnosis":"the review failed while the delivered subject still mismatches","strategy":"rebuild and re-verify with a fresh review","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
			if err := runIssue933OperatorAction(t, s, tc.workID, "request_correction", correction, fixture.owner, fixture.operator); err != nil {
				t.Fatalf("first correction after the failure record refused: %v", err)
			}
			if got := currentStep(t, s, tc.workID); got != tc.effectStep {
				t.Fatalf("step after the first correction = %q, want %q", got, tc.effectStep)
			}
			ownerRef, err := WorkflowActorRef(fixture.owner)
			if err != nil {
				t.Fatal(err)
			}
			acceptReturnRouteWorker(t, fixture, tc.workID, ownerRef)
			if got := currentStep(t, s, tc.workID); got != tc.checkpoint {
				t.Fatalf("step after the fresh delivery = %q, want %q", got, tc.checkpoint)
			}

			pin := issue1013Pin(t, s, tc.workID)
			for _, intent := range pin.NextValidIntents {
				if intent.ActionID == "request_correction" {
					t.Fatalf("pin advertises request_correction on a consumed failed review: %+v", intent)
				}
			}
			kind, detail := requestCorrectionRefusalClass(t, s, tc.workID)
			if kind != KindInvalidOperation || !strings.Contains(detail, "current non-ok verification verdict") {
				t.Fatalf("stale-verdict refusal = %s %q, want the verdict class", kind, detail)
			}
			if err := InspectWorkflowActionAdmission(ctx, s, WorkflowActionPreflightRequest{WorkID: tc.workID, ActionID: "request_correction", Payload: correction, Actor: fixture.owner}); err == nil {
				t.Fatal("preflight admitted the consumed failed-review correction")
			}
			if err := runIssue933OperatorAction(t, s, tc.workID, "request_correction", correction, fixture.owner, fixture.operator); err == nil {
				t.Fatal("the stale original verdict admitted a second correction")
			}
			if got := currentStep(t, s, tc.workID); got != tc.checkpoint {
				t.Fatalf("step after the refused correction = %q, want %q", got, tc.checkpoint)
			}
		})
	}
}

// TestCheckpointFailedReviewCorrectionReturnsImplementationToExecution pins
// the amended return on the implementation graph: after the acceptance
// checkpoint's failed review is recorded, the operator-approved correction
// returns to execution, and only the fresh execution-step dispatch carries
// the bounded mismatch context.
func TestCheckpointFailedReviewCorrectionReturnsImplementationToExecution(t *testing.T) {
	const workID = "checkpoint-failed-review-implementation"
	fixture, attemptID, epoch := seedCheckpointFailedReviewMismatchOn(t, workID, "workflow.implementation", "acceptance")
	s := fixture.store
	ctx := context.Background()
	if err := runVerdictActionAs(t, s, workID, "record_worker_failure", json.RawMessage(`{"attempt_id":"`+attemptID+`","attempt_epoch":`+fmt.Sprint(epoch)+`}`), 0, fixture.owner); err != nil {
		t.Fatalf("record_worker_failure refused: %v", err)
	}
	correction := json.RawMessage(`{"diagnosis":"the review failed while the delivered subject still mismatches","strategy":"rebuild the helper and re-verify with a fresh review","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	attemptsBefore, dispatchesBefore := workerAttemptPopulation(t, s, workID)
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", correction, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("request correction after the checkpoint failure record: %v", err)
	}
	if got := currentStep(t, s, workID); got != "execution" {
		t.Fatalf("step after the checkpoint correction = %q, want execution", got)
	}
	// The implementation return mints no attempt either: request_correction
	// only moves the pinned graph, and the execution dispatch that follows
	// owns the fresh identity.
	if attempts, dispatches := workerAttemptPopulation(t, s, workID); attempts != attemptsBefore || dispatches != dispatchesBefore {
		t.Fatalf("request_correction changed the attempt population: attempts %d->%d, dispatches %d->%d", attemptsBefore, attempts, dispatchesBefore, dispatches)
	}
	returned, err := workflowCorrectionContextForDispatch(ctx, s.db, workID, "execution", "attempt:next")
	if err != nil {
		t.Fatal(err)
	}
	if returned == nil || returned.AttemptCount != 1 || returned.PredicateIDs[0] != "predicate:return-route" || returned.EvidenceRefs[0] != "evidence:return-route-verification" || returned.Diagnosis != "the review failed while the delivered subject still mismatches" {
		t.Fatalf("returned correction context = %+v, want the first bounded checkpoint correction", returned)
	}
	if attemptErr := runIssue933OperatorAction(t, s, workID, "request_correction", correction, fixture.owner, fixture.operator); attemptErr == nil {
		t.Fatal("second request_correction admitted before a fresh verdict")
	}
	pin := issue1013Pin(t, s, workID)
	var executionEpoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(json_extract(payload,'$.attempt_epoch')),0) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.step_id')='execution'`, workID, WorkflowActionStarted).Scan(&executionEpoch); err != nil {
		t.Fatal(err)
	}
	fresh := "attempt:" + workID + ":fresh"
	if fresh == attemptID {
		t.Fatal("the fresh attempt identity reuses the dispositioned review attempt")
	}
	if err := dispatchSameStepAttempt(t, s, workID, "execution", fresh, fixture.owner, false, pin.Correction); err != nil {
		t.Fatalf("fresh fenced attempt after the checkpoint correction: %v", err)
	}
	var freshEpoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, fresh).Scan(&freshEpoch); err != nil {
		t.Fatal(err)
	}
	if freshEpoch != executionEpoch+1 {
		t.Fatalf("fresh execution dispatch epoch = %d, want the strictly incremented %d", freshEpoch, executionEpoch+1)
	}

	// A late report for the dispositioned attempt stays fenced on the
	// implementation graph too: the attempt reached its failed terminal
	// state, so neither a late completion report nor a late acceptance can
	// land after the correction returned to execution.
	lateLane := reviewGateLane(t, "review")
	lateCompletion := Event{EventID: "implementation-late-completion-" + workID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(40, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lateLane), ReportSchemaVersion: WorkerReportSchemaVersion})}
	lateErr := ApplyOperation(ctx, s, Operation{Events: []Event{lateCompletion}})
	var lateFailure *Failure
	if !errors.As(lateErr, &lateFailure) || lateFailure.Kind != KindProjectionConflict {
		t.Fatalf("late completion for the dispositioned review attempt = %v, want a projection-conflict fence", lateErr)
	}
	if acceptErr := runVerdictActionAs(t, s, workID, "accept_worker_result", json.RawMessage(`{"attempt_id":"`+attemptID+`","attempt_epoch":`+fmt.Sprint(epoch)+`}`), 0, fixture.owner); acceptErr == nil {
		t.Fatal("late acceptance of the dispositioned review attempt admitted after the return")
	}

	// The return carries no law, contract, history, or evidence edit: the
	// pinned digest, the one contract, the failure verdict, and the failure
	// record all survive unchanged on the implementation graph.
	assertFailedReviewCorrectionPreservesPinnedState(t, s, workID, "workflow.implementation")
}

// TestCheckpointFailedReviewCorrectionSecondCycleCountsPriorRequests pins the
// CD-0164 counting parity the stale admission broke: after the first
// failed-review correction returns and a fresh delivery verifies non-ok
// again, the reopened correction counts the prior request, and the admitted
// count and the recorded dispatch context agree.
func TestCheckpointFailedReviewCorrectionSecondCycleCountsPriorRequests(t *testing.T) {
	const workID = "checkpoint-correction-second-cycle"
	fixture, attemptID, epoch := seedCheckpointFailedReviewMismatch(t, workID)
	s := fixture.store
	ctx := context.Background()
	if err := runVerdictActionAs(t, s, workID, "record_worker_failure", json.RawMessage(`{"attempt_id":"`+attemptID+`","attempt_epoch":`+fmt.Sprint(epoch)+`}`), 0, fixture.owner); err != nil {
		t.Fatalf("record_worker_failure refused: %v", err)
	}
	correction := json.RawMessage(`{"diagnosis":"the review failed while the delivered subject still mismatches","strategy":"rebuild the helper and re-verify with a fresh review","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", correction, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("first correction refused: %v", err)
	}
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	reviewer := acceptReturnRouteWorker(t, fixture, workID, ownerRef)
	if got := currentStep(t, s, workID); got != "verify" {
		t.Fatalf("step after the fresh delivery = %q, want verify", got)
	}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}`), 0, reviewer); err != nil {
		t.Fatalf("fresh mismatch verdict refused: %v", err)
	}
	entry, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	admission, missing, err := workflowCorrectionRequestAdmission(ctx, s.db, workID, entry.Definition, "verify", "correction_parity_test", 0)
	if err != nil {
		t.Fatal(err)
	}
	if admission == nil || admission.AttemptCount != 2 {
		t.Fatalf("admission after the fresh verdict = %+v (missing %q), want the second correction carrying the prior request", admission, missing)
	}
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", correction, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("second correction refused: %v", err)
	}
	if got := currentStep(t, s, workID); got != "repair" {
		t.Fatalf("step after the second correction = %q, want repair", got)
	}
	returned, err := workflowCorrectionContextForDispatch(ctx, s.db, workID, "repair", "attempt:next")
	if err != nil {
		t.Fatal(err)
	}
	if returned == nil || returned.AttemptCount != admission.AttemptCount {
		t.Fatalf("dispatch context = %+v, want parity with the admitted count %d", returned, admission.AttemptCount)
	}
	// The re-entry dispatch opens the owning step's next epoch: the accepted
	// delivery journey started repair at epoch 1, so the post-correction
	// dispatch must mint a strictly incremented epoch and a fresh identity,
	// never a reuse of the delivered attempt or of the dispositioned review.
	var repairEpoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(json_extract(payload,'$.attempt_epoch')),0) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.step_id')='repair'`, workID, WorkflowActionStarted).Scan(&repairEpoch); err != nil {
		t.Fatal(err)
	}
	cycle2 := "attempt:" + workID + ":cycle2"
	if cycle2 == attemptID || cycle2 == "attempt:"+workID {
		t.Fatal("the re-entry attempt identity reuses a prior attempt")
	}
	if err := dispatchSameStepAttempt(t, s, workID, "repair", cycle2, fixture.owner, false, returned); err != nil {
		t.Fatalf("re-entry fenced attempt after the second correction: %v", err)
	}
	var cycle2Epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, cycle2).Scan(&cycle2Epoch); err != nil {
		t.Fatal(err)
	}
	if cycle2Epoch != repairEpoch+1 {
		t.Fatalf("re-entry dispatch epoch = %d, want the strictly incremented %d", cycle2Epoch, repairEpoch+1)
	}
}

// checkpointCorrectionRequestOpen counts the recorded request_correction
// completions, so a refusal test can prove the refused request wrote nothing.
func checkpointCorrectionRequestOpen(t *testing.T, s *Store, workID string) int64 {
	t.Helper()
	var requests int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='request_correction'`, workID, WorkflowActionCompleted).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	return requests
}

// runCheckpointOperatorAction runs one operator-decision action with an
// optional verified operator identity, so the approval-wall tests can ask
// what the failed-review return does without one and with the wrong one.
func runCheckpointOperatorAction(t *testing.T, s *Store, workID, action, key string, payload json.RawMessage, owner WorkflowActor, operator *WorkflowActor) error {
	t.Helper()
	version := verdictItemVersion(t, s, workID)
	operationID := action + "-" + workID + "-" + key + "-" + fmt.Sprint(version)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := applyWorkflowActionRawTx(context.Background(), tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: owner, OperatorActor: operator,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64), IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
		ContractDigest: testManifestDigest, Now: time.Unix(21, version).UTC(),
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// TestCheckpointFailedReviewCorrectionRequiresExactOperatorApproval pins the
// operator approval wall the failed-review return rides: the admitted shape
// still refuses without the verified operator identity, refuses a non-operator
// identity and the invoking agent relabeling itself, and admits only the
// exact operator approval, which returns to repair and mints no attempt.
func TestCheckpointFailedReviewCorrectionRequiresExactOperatorApproval(t *testing.T) {
	const workID = "checkpoint-correction-operator"
	fixture, attemptID, epoch := seedCheckpointFailedReviewMismatch(t, workID)
	s := fixture.store
	if err := runVerdictActionAs(t, s, workID, "record_worker_failure", json.RawMessage(`{"attempt_id":"`+attemptID+`","attempt_epoch":`+fmt.Sprint(epoch)+`}`), 0, fixture.owner); err != nil {
		t.Fatalf("record_worker_failure refused: %v", err)
	}
	correction := json.RawMessage(`{"diagnosis":"the review failed while the delivered subject still mismatches","strategy":"rebuild the helper and re-verify with a fresh review","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)

	noOperator := runCheckpointOperatorAction(t, s, workID, "request_correction", "no-operator", correction, fixture.owner, nil)
	var approvalFailure *Failure
	if !errors.As(noOperator, &approvalFailure) || approvalFailure.Kind != KindApprovalRequired || !strings.Contains(approvalFailure.Detail, "verified operator approval identity") {
		t.Fatalf("correction without an operator identity = %v, want the operator approval wall", noOperator)
	}
	selfApproval := runCheckpointOperatorAction(t, s, workID, "request_correction", "self-operator", correction, fixture.owner, &fixture.owner)
	var relabelFailure *Failure
	if !errors.As(selfApproval, &relabelFailure) || relabelFailure.Kind != KindUnauthorized {
		t.Fatalf("invoking agent relabeled as the operator = %v, want an unauthorized refusal", selfApproval)
	}
	if got := currentStep(t, s, workID); got != "verify" {
		t.Fatalf("step after the refused approvals = %q, want verify", got)
	}
	if got := checkpointCorrectionRequestOpen(t, s, workID); got != 0 {
		t.Fatalf("refused approvals recorded %d correction requests, want none", got)
	}

	if err := runCheckpointOperatorAction(t, s, workID, "request_correction", "exact-operator", correction, fixture.owner, &fixture.operator); err != nil {
		t.Fatalf("exact operator approval refused: %v", err)
	}
	if got := currentStep(t, s, workID); got != "repair" {
		t.Fatalf("step after the approved correction = %q, want repair", got)
	}
	if attempts, dispatches := workerAttemptPopulation(t, s, workID); attempts != 1 || dispatches != 1 {
		t.Fatalf("request_correction changed the attempt population: attempts=%d dispatches=%d, want the one failed review", attempts, dispatches)
	}
}

// TestCheckpointFailedReviewCorrectionRefusesUnboundPredicatesAndEvidence
// pins the payload gates on the failed-review path: a predicate without a
// current non-ok verdict, unbound evidence, and an incomplete disposition are
// refused, and no refused request records a correction.
func TestCheckpointFailedReviewCorrectionRefusesUnboundPredicatesAndEvidence(t *testing.T) {
	const workID = "checkpoint-correction-unbound"
	fixture, attemptID, epoch := seedCheckpointFailedReviewMismatch(t, workID)
	s := fixture.store
	if err := runVerdictActionAs(t, s, workID, "record_worker_failure", json.RawMessage(`{"attempt_id":"`+attemptID+`","attempt_epoch":`+fmt.Sprint(epoch)+`}`), 0, fixture.owner); err != nil {
		t.Fatalf("record_worker_failure refused: %v", err)
	}
	unboundPredicate := json.RawMessage(`{"diagnosis":"the review failed","strategy":"rebuild and re-verify","predicate_ids":["predicate:elsewhere"],"evidence_refs":["evidence:return-route-verification"]}`)
	errPredicate := runCheckpointOperatorAction(t, s, workID, "request_correction", "unbound-predicate", unboundPredicate, fixture.owner, &fixture.operator)
	var predicateFailure *Failure
	if !errors.As(errPredicate, &predicateFailure) || predicateFailure.Kind != KindInvalidPayload || !strings.Contains(predicateFailure.Detail, "without a current non-ok verdict") {
		t.Fatalf("unbound predicate = %v, want the verdict-class payload refusal", errPredicate)
	}
	unboundEvidence := json.RawMessage(`{"diagnosis":"the review failed","strategy":"rebuild and re-verify","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:unbound"]}`)
	errEvidence := runCheckpointOperatorAction(t, s, workID, "request_correction", "unbound-evidence", unboundEvidence, fixture.owner, &fixture.operator)
	var evidenceFailure *Failure
	if !errors.As(errEvidence, &evidenceFailure) || evidenceFailure.Kind != KindMissingEvidence || !strings.Contains(evidenceFailure.Detail, "not durably bound") {
		t.Fatalf("unbound evidence = %v, want the evidence-binding refusal", errEvidence)
	}
	incomplete := json.RawMessage(`{"diagnosis":"the review failed","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	errIncomplete := runCheckpointOperatorAction(t, s, workID, "request_correction", "incomplete", incomplete, fixture.owner, &fixture.operator)
	var payloadFailure *Failure
	if !errors.As(errIncomplete, &payloadFailure) || payloadFailure.Kind != KindInvalidPayload || !strings.Contains(payloadFailure.Detail, "requires diagnosis, strategy") {
		t.Fatalf("incomplete disposition = %v, want the payload-completeness refusal", errIncomplete)
	}
	if got := checkpointCorrectionRequestOpen(t, s, workID); got != 0 {
		t.Fatalf("refused requests recorded %d correction requests, want none", got)
	}
	if got := currentStep(t, s, workID); got != "verify" {
		t.Fatalf("step after the refused requests = %q, want verify", got)
	}
}

// seedCheckpointCorrectionSecondCycle runs the shared two-request cycle the
// counting regressions read: the dispositioned failed review corrects once, a
// fresh delivery is accepted behind a new verdict, and the admitted second
// request counts the first. The returned reviewer recorded that verdict.
func seedCheckpointCorrectionSecondCycle(t *testing.T, workID string) (workflowReturnRouteFixture, WorkflowActor) {
	t.Helper()
	fixture, attemptID, epoch := seedCheckpointFailedReviewMismatch(t, workID)
	s := fixture.store
	if err := runVerdictActionAs(t, s, workID, "record_worker_failure", json.RawMessage(`{"attempt_id":"`+attemptID+`","attempt_epoch":`+fmt.Sprint(epoch)+`}`), 0, fixture.owner); err != nil {
		t.Fatalf("record_worker_failure refused: %v", err)
	}
	correction := json.RawMessage(`{"diagnosis":"the review failed while the delivered subject still mismatches","strategy":"rebuild the helper and re-verify with a fresh review","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", correction, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("first correction refused: %v", err)
	}
	if got := currentStep(t, s, workID); got != "repair" {
		t.Fatalf("step after the first correction = %q, want repair", got)
	}
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	reviewer := acceptReturnRouteWorker(t, fixture, workID, ownerRef)
	if got := currentStep(t, s, workID); got != "verify" {
		t.Fatalf("step after the fresh delivery = %q, want verify", got)
	}
	return fixture, reviewer
}

// TestCheckpointCorrectionDispatchCountsIncomparableOKVerdicts pins the
// CD-0164 D4 window against the incomparable ok verdict: an ok verdict that
// is incomparable with the approved result does not end the unresolved
// correction sequence (CD-0143 D3), so the second admitted request counts the
// first, and the recorded dispatch context reports the admitted count instead
// of a window the incomparable ok reset.
func TestCheckpointCorrectionDispatchCountsIncomparableOKVerdicts(t *testing.T) {
	const workID = "checkpoint-correction-incomparable-ok"
	fixture, reviewer := seedCheckpointCorrectionSecondCycle(t, workID)
	s := fixture.store
	ctx := context.Background()
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"ok","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}`), 0, reviewer); err != nil {
		t.Fatalf("incomparable ok verdict refused: %v", err)
	}
	entry, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	admission, missing, err := workflowCorrectionRequestAdmission(ctx, s.db, workID, entry.Definition, "verify", "incomparable_ok_test", 0)
	if err != nil {
		t.Fatal(err)
	}
	if admission == nil || admission.AttemptCount != 2 || admission.Escalated {
		t.Fatalf("admission after the incomparable ok verdict = %+v (missing %q), want the second request counting the first", admission, missing)
	}
	correction := json.RawMessage(`{"diagnosis":"the incomparable result still mismatches","strategy":"rebuild the helper and re-verify with a fresh review","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", correction, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("second correction refused: %v", err)
	}
	if got := currentStep(t, s, workID); got != "repair" {
		t.Fatalf("step after the second correction = %q, want repair", got)
	}
	returned, err := workflowCorrectionContextForDispatch(ctx, s.db, workID, "repair", "attempt:next")
	if err != nil {
		t.Fatal(err)
	}
	if returned == nil || returned.AttemptCount != admission.AttemptCount || returned.Escalated != admission.Escalated {
		t.Fatalf("dispatch context = %+v, want the admitted count %d without an incomparable-ok reset", returned, admission.AttemptCount)
	}
}

// TestCheckpointCorrectionDispatchCountsMixedPredicateWindows pins the
// conjunctive owner behind the dispatched window: one predicate's comparable
// ok verdict beside another predicate's stale non-ok verdict is not a
// comparable-healthy set, so neither the admission nor the recorded dispatch
// context may open a fresh window at the lone ok verdict.
func TestCheckpointCorrectionDispatchCountsMixedPredicateWindows(t *testing.T) {
	const workID = "checkpoint-correction-mixed-predicates"
	fixture, reviewer := seedCheckpointCorrectionSecondCycle(t, workID)
	s := fixture.store
	ctx := context.Background()
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:second',1,'check','{"kind":"check","check_ref":"check:second","immutable_subject_ref":"commit:second","expected_result":"pass"}')`, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"ok","evaluation_evidence":["evidence:return-route-verification"]}`), 0, reviewer); err != nil {
		t.Fatalf("comparable ok verdict refused: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:second","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}`), 0, reviewer); err != nil {
		t.Fatalf("stale second-predicate verdict refused: %v", err)
	}
	entry, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	admission, missing, err := workflowCorrectionRequestAdmission(ctx, s.db, workID, entry.Definition, "verify", "mixed_predicates_test", 0)
	if err != nil {
		t.Fatal(err)
	}
	if admission == nil || admission.AttemptCount != 2 || admission.Escalated {
		t.Fatalf("admission after the mixed verdict set = %+v (missing %q), want the second request counting the first", admission, missing)
	}
	correction := json.RawMessage(`{"diagnosis":"the second predicate still mismatches","strategy":"rebuild the helper and re-verify both predicates","predicate_ids":["predicate:second"],"evidence_refs":["evidence:return-route-verification"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", correction, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("second correction refused: %v", err)
	}
	if got := currentStep(t, s, workID); got != "repair" {
		t.Fatalf("step after the second correction = %q, want repair", got)
	}
	returned, err := workflowCorrectionContextForDispatch(ctx, s.db, workID, "repair", "attempt:next")
	if err != nil {
		t.Fatal(err)
	}
	if returned == nil || returned.AttemptCount != admission.AttemptCount || returned.Escalated != admission.Escalated {
		t.Fatalf("dispatch context = %+v, want the admitted count %d without a lone-ok reset", returned, admission.AttemptCount)
	}
}
