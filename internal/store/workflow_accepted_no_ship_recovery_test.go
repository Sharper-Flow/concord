package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func historicalReviewAcceptor(t *testing.T, s *Store, workID string) WorkflowActor {
	t.Helper()
	acceptor := reviewGateAcceptor(workID)
	acceptorRef, err := WorkflowActorRef(acceptor)
	if err != nil {
		t.Fatal(err)
	}
	version := verdictItemVersion(t, s, workID)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEventWithActor("historical-acceptor", WorkflowActorRecorded, workID, acceptorRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"actor_ref": acceptorRef, "principal_ref": acceptor.PrincipalRef, "client_ref": acceptor.ClientRef,
		"agent_ref": acceptor.AgentRef, "session_ref": acceptor.SessionRef, "actor_class": string(acceptor.ActorClass),
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	return acceptor
}

func TestHistoricalAcceptedNoShipReviewWithoutRejectionRecovers(t *testing.T) {
	for _, pin := range []struct {
		ref     string
		version int64
		target  string
	}{
		{"workflow.implementation", 19, "execution"},
		{"workflow.break_fix", 13, "repair"},
	} {
		t.Run(pin.ref, func(t *testing.T) {
			const workID = "historical-accepted-no-ship"
			ctx := context.Background()
			registered, ok := BuiltinWorkflowRegistry().Lookup(pin.ref, pin.version)
			if !ok {
				t.Fatalf("definition %s v%d is not registered", pin.ref, pin.version)
			}
			fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, registered, "refine", []string{"verification"}, []string{"verification", "review", "artifact"})
			s := fixture.store
			deliveriesBefore := reviewGateCountDeliveries(t, s, workID)
			ownerRef, err := WorkflowActorRef(fixture.owner)
			if err != nil {
				t.Fatal(err)
			}
			epoch := reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
			attemptID := "attempt:" + workID + ":review"
			reviewGateRunAttemptWithVerdict(t, s, workID, attemptID, epoch, reviewGateLane(t, "review"), ownerRef, "no_ship", 100)
			acceptor := historicalReviewAcceptor(t, s, workID)
			// Replay the historical advancing acceptance without rewriting its
			// disposition or adding a rejection or delivery assertion.
			reviewGateSeedAcceptAtRefine(t, s, workID, attemptID, epoch, acceptor)
			reviewGateRequireStep(t, s, workID, "delivery")
			var rejections int
			if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='reject_worker_result'`, workID, WorkflowActionCompleted).Scan(&rejections); err != nil || rejections != 0 {
				t.Fatalf("rejection count = %d, error %v; want no rejection", rejections, err)
			}
			workPin, err := ReadWorkPin(ctx, s, workID)
			if err != nil {
				t.Fatal(err)
			}
			if !workPinContainsAction(workPin.NextValidIntents, "request_correction") {
				t.Fatalf("historical no_ship acceptance hides request_correction: %#v", workPin.NextValidIntents)
			}
			if workPinContainsAction(workPin.NextValidIntents, "record_delivery") {
				t.Fatal("historical no_ship acceptance offers delivery")
			}
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			state, _, loadErr := loadWorkflowAdmissionStateTx(ctx, tx, workID, registered.Definition, "delivery", "test")
			_ = tx.Rollback()
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if state.ReviewDebt != ReviewDebtOutstanding || state.ReadyReviewAttemptID != "" {
				t.Fatalf("accepted no_ship debt/readiness = %s/%s", state.ReviewDebt, state.ReadyReviewAttemptID)
			}
			if !workflowAdmit(registered.Definition, state, "request_correction").Admitted {
				t.Fatal("shared admission refuses the historical corrective return")
			}
			payload := reviewGateCorrectionPayload("evidence:return-route-verification")
			if err := InspectWorkflowActionAdmission(ctx, s, WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: workPin.Version, ActionID: "request_correction", Payload: payload, Actor: fixture.owner}); err != nil {
				t.Fatalf("correction preflight disagrees with the work pin: %v", err)
			}
			if err := reviewGateRecordDelivery(t, s, workID, reviewGateAcceptor(workID)); err == nil {
				t.Fatal("delivery behind accepted no_ship was admitted")
			}
			for _, invalid := range []struct {
				name    string
				payload json.RawMessage
				kind    FailureKind
			}{
				{"unbound evidence", reviewGateCorrectionPayload("evidence:not-bound"), KindMissingEvidence},
				{"unknown predicate", json.RawMessage(`{"diagnosis":"review refused the result","strategy":"repair it","predicate_ids":["predicate:unknown"],"evidence_refs":["evidence:return-route-verification"]}`), KindInvalidPayload},
			} {
				err := runIssue933OperatorAction(t, s, workID, "request_correction", invalid.payload, fixture.owner, fixture.operator)
				var failure *Failure
				if err == nil || !failureAs(err, &failure) || failure.Kind != invalid.kind {
					t.Fatalf("%s correction = %v, want %s", invalid.name, err, invalid.kind)
				}
			}
			if err := runCorrectionActionWithoutOperator(s, workID, fixture.owner, payload); err == nil {
				t.Fatal("correction without operator authority was admitted")
			}
			if err := runIssue933OperatorAction(t, s, workID, "request_correction", payload, fixture.owner, fixture.operator); err != nil {
				t.Fatalf("historical evidence-bearing corrective return: %v", err)
			}
			reviewGateRequireStep(t, s, workID, pin.target)
			pinnedVersion, digest := reviewGateInstancePin(t, s, workID)
			if pinnedVersion != pin.version || digest != registered.Digest {
				t.Fatalf("definition pin changed: v%d %s", pinnedVersion, digest)
			}
			if count := reviewGateCountDeliveries(t, s, workID); count != deliveriesBefore {
				t.Fatalf("correction changed delivery count from %d to %d", deliveriesBefore, count)
			}
			correction, err := workflowCorrectionContext(ctx, s.db, workID, pin.target)
			if err != nil || correction == nil || correction.AttemptCount != 1 || correction.Disposition != "verification" || correction.FailedAttemptID != "" {
				t.Fatalf("recorded correction = %#v, error %v", correction, err)
			}
			if err := runIssue933OperatorAction(t, s, workID, "request_correction", payload, fixture.owner, fixture.operator); err == nil || !strings.Contains(err.Error(), "non-ok verification verdict") {
				t.Fatalf("consumed return = %v, want no repeated correction", err)
			}
		})
	}
}

func TestHistoricalReviewGateDoesNotBorrowUnacceptedNoShipReport(t *testing.T) {
	for _, verdict := range []string{"ship", "", "unaccepted_no_ship"} {
		name := verdict
		if name == "" {
			name = "absent"
		}
		t.Run(name, func(t *testing.T) {
			const workID = "historical-review-no-debt"
			fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, "workflow.implementation", 19, "refine")
			s := fixture.store
			ownerRef, err := WorkflowActorRef(fixture.owner)
			if err != nil {
				t.Fatal(err)
			}
			epoch := reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
			attemptID := "attempt:" + workID + ":review"
			switch verdict {
			case "":
				reviewGateRunAttempt(t, s, workID, attemptID, "refine", epoch, reviewGateLane(t, "review"), ownerRef, 100)
			case "ship":
				reviewGateRunAttemptWithVerdict(t, s, workID, attemptID, epoch, reviewGateLane(t, "review"), ownerRef, verdict, 100)
			default:
				reviewGateRunAttemptWithVerdict(t, s, workID, attemptID, epoch, reviewGateLane(t, "review"), ownerRef, "no_ship", 100)
				attemptID = "attempt:" + workID + ":implementation"
				reviewGateRunAttempt(t, s, workID, attemptID, "refine", epoch, reviewGateLane(t, "implementation"), ownerRef, 102)
			}
			reviewGateSeedAcceptAtRefine(t, s, workID, attemptID, epoch, historicalReviewAcceptor(t, s, workID))
			reviewGateRequireStep(t, s, workID, "delivery")
			pin, err := ReadWorkPin(context.Background(), s, workID)
			if err != nil {
				t.Fatal(err)
			}
			if workPinContainsAction(pin.NextValidIntents, "request_correction") {
				t.Fatal("a settling review or unaccepted no_ship report opened gate correction")
			}
			if err := runIssue933OperatorAction(t, s, workID, "request_correction", reviewGateCorrectionPayload("evidence:return-route-verification"), fixture.owner, fixture.operator); err == nil {
				t.Fatal("execution admitted correction without an accepted no_ship or rejection")
			}
		})
	}
}

func acceptedNoShipRepairToRefine(t *testing.T, fixture workflowReturnRouteFixture, workID, ownerRef, attemptID string, at int64) {
	t.Helper()
	s := fixture.store
	epoch := reviewGateStartStep(t, s, workID, "execution", "start_execution", fixture.owner)
	reviewGateRunAttempt(t, s, workID, attemptID, "execution", epoch, reviewGateLane(t, "implementation"), ownerRef, at)
	if err := reviewGateAcceptResult(t, s, workID, attemptID, epoch, reviewGateAcceptor(workID)); err != nil {
		t.Fatalf("accept the corrective implementation: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "refine")
}

func TestAcceptedNoShipReviewCorrectionCounting(t *testing.T) {
	const workID = "accepted-no-ship-correction-count"
	ctx := context.Background()
	fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, "workflow.implementation", 19, "refine")
	s := fixture.store
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	acceptor := historicalReviewAcceptor(t, s, workID)
	for cycle := int64(1); cycle <= 4; cycle++ {
		if cycle > 1 {
			acceptedNoShipRepairToRefine(t, fixture, workID, ownerRef, fmt.Sprintf("attempt:%s:repair-%d", workID, cycle), 95+cycle*10)
		}
		epoch := reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
		attemptID := fmt.Sprintf("attempt:%s:review-%d", workID, cycle)
		reviewGateRunAttemptWithVerdict(t, s, workID, attemptID, epoch, reviewGateLane(t, "review"), ownerRef, "no_ship", 100+cycle*10)
		reviewGateSeedAcceptAtRefine(t, s, workID, attemptID, epoch, acceptor)
		if err := runIssue933OperatorAction(t, s, workID, "request_correction", reviewGateCorrectionPayload("evidence:return-route-verification"), fixture.owner, fixture.operator); err != nil {
			t.Fatalf("correction %d: %v", cycle, err)
		}
		correction, err := workflowCorrectionContext(ctx, s.db, workID, "execution")
		if err != nil || correction == nil || correction.AttemptCount != cycle || correction.Escalated != (cycle > workflowCorrectionAttemptLimit) {
			t.Fatalf("correction %d count/escalation = %#v, error %v", cycle, correction, err)
		}
	}
	binding, err := WorkflowFailedWorkerRetryBinding(ctx, s, nil, workID)
	if err != nil || binding == nil || binding.CorrectionAttempts != 4 || binding.ContractVersion != 1 || binding.FailedAttemptID != "" || binding.FailedAttemptEpoch != 0 {
		t.Fatalf("escalated retry binding = %#v, error %v", binding, err)
	}
	registered, _ := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 19)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	state, _, loadErr := loadWorkflowAdmissionStateTx(ctx, tx, workID, registered.Definition, "execution", "test")
	_ = tx.Rollback()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	decision := workflowAdmit(registered.Definition, state, "dispatch_worker")
	if decision.Admitted || !decision.ApprovalRequired || decision.Failure == nil || decision.Failure.Kind != KindApprovalRequired {
		t.Fatalf("escalated dispatch admission = %#v, want retry approval required", decision)
	}
}

func TestAcceptedNoShipReviewDebtSettlesAfterFreshReview(t *testing.T) {
	const workID = "accepted-no-ship-settlement"
	ctx := context.Background()
	fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, "workflow.implementation", 19, "refine")
	s := fixture.store
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	acceptor := historicalReviewAcceptor(t, s, workID)
	epoch := reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
	noShipID := "attempt:" + workID + ":no-ship"
	reviewGateRunAttemptWithVerdict(t, s, workID, noShipID, epoch, reviewGateLane(t, "review"), ownerRef, "no_ship", 100)
	reviewGateSeedAcceptAtRefine(t, s, workID, noShipID, epoch, acceptor)
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", reviewGateCorrectionPayload("evidence:return-route-verification"), fixture.owner, fixture.operator); err != nil {
		t.Fatal(err)
	}
	acceptedNoShipRepairToRefine(t, fixture, workID, ownerRef, "attempt:"+workID+":repair", 150)
	epoch = reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
	shipID := "attempt:" + workID + ":ship"
	reviewGateRunAttemptWithVerdict(t, s, workID, shipID, epoch, reviewGateLane(t, "review"), ownerRef, "ship", 200)
	if err := acceptRefineResult(t, s, workID, shipID, epoch, acceptor); err != nil {
		t.Fatalf("fresh settling review: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
	pin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if workPinContainsAction(pin.NextValidIntents, "request_correction") || !workPinContainsAction(pin.NextValidIntents, "record_delivery") {
		t.Fatalf("settled gate intents = %#v", pin.NextValidIntents)
	}
}
