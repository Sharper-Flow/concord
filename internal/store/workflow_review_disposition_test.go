package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// jobBoundReviewCompletion reads the worker-job binding the dispatch
// authorization recorded for the attempt and rewrites the completion event's
// payload to carry it. The fold's end-to-end check refuses a worker.completed
// whose worker_job does not match the dispatch's binding, so a refine review
// attempt dispatched under a recorded worker job must complete against the
// same revision. Legacy dispatches return no binding, so the helper is a
// no-op when the dispatch predated the worker-job lifecycle.
func jobBoundReviewCompletion(t *testing.T, event *Event, attemptID string, s *Store) {
	t.Helper()
	binding, err := workflowDispatchedJobForAttempt(context.Background(), s.DatabaseForTesting(), event.SubjectID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if binding == nil {
		return
	}
	var payload WorkerCompletedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	var laneID, laneDigest string
	var laneVersion int64
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT lane_id,lane_version,lane_digest FROM worker_attempts WHERE work_id=? AND attempt_id=?`, event.SubjectID, attemptID).Scan(&laneID, &laneVersion, &laneDigest); err != nil {
		t.Fatal(err)
	}
	lane, err := LookupLane(laneID, laneVersion, laneDigest)
	if err != nil {
		t.Fatal(err)
	}
	payload.WorkerJob = binding
	payload.ReportSchemaVersion = WorkerReportSchemaVersion
	payload.EvidenceOrigin = WorkerEvidenceReported
	payload.Evidence = reportedLaneEvidenceForTest(lane, payload.Review)
	event.PayloadVersion = WorkerEvidenceEventPayloadVersion(WorkerCompleted)
	event.Payload = mustJSONValue(payload)
}

func TestRefineReviewEvidenceDispositionOpensContractCorrection(t *testing.T) {
	for _, ref := range []string{"workflow.break_fix", "workflow.implementation"} {
		t.Run(ref, func(t *testing.T) {
			workID := "review-disposition-" + ref
			definition, err := BuiltinWorkflowDefinitionForRef(ref)
			if err != nil {
				t.Fatal(err)
			}
			fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, definition, "refine", []string{"review"}, []string{"review"})
			s := fixture.store
			var productVersion int64
			if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM products WHERE id='product'`).Scan(&productVersion); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DesignateProductKnowledgeHome(context.Background(), ProductKnowledgeHomeDesignation{
				ProductID: "product", ProjectID: "project", LocatorID: "workflow-law-locator",
				Reason: "synthetic successor contract law home", ExpectedVersion: productVersion,
			}); err != nil {
				t.Fatal(err)
			}
			ownerRef, err := WorkflowActorRef(fixture.owner)
			if err != nil {
				t.Fatal(err)
			}
			at := int64(100)
			epoch := reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
			attemptID := "attempt:" + workID + ":review"
			reviewGateRunAttemptWithVerdict(t, s, workID, attemptID, epoch, reviewGateLane(t, "review"), ownerRef, "ship", at)
			acceptor := reviewGateAcceptor(workID)
			payload := json.RawMessage(fmt.Sprintf(`{"attempt_id":%q,"attempt_epoch":%d}`, attemptID, epoch))
			pin, err := ReadWorkPin(context.Background(), s, workID)
			if err != nil {
				t.Fatal(err)
			}
			if !workPinContainsAction(pin.NextValidIntents, "accept_worker_evidence") || workPinContainsAction(pin.NextValidIntents, "supersede_contract") {
				t.Fatalf("pending review has wrong disposition/correction intents: %#v", pin.NextValidIntents)
			}

			if err := InspectWorkflowActionAdmission(context.Background(), s, WorkflowActionPreflightRequest{
				WorkID: workID, ExpectedVersion: verdictItemVersion(t, s, workID),
				ActionID: "accept_worker_evidence", Payload: payload, Actor: acceptor,
			}); err != nil {
				t.Fatalf("preflight of review disposition without delivery = %v, want admitted non-advancing disposition", err)
			}
			if err := runVerdictActionAs(t, s, workID, "accept_worker_evidence", payload, 0, acceptor); err != nil {
				t.Fatalf("accept completed review without delivery = %v, want accepted evidence without advance", err)
			}
			reviewGateRequireStep(t, s, workID, "refine")

			var deliveries int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.step_id')='refine' AND json_extract(payload,'$.delivery_state')='asserted'`, workID, WorkflowActionCompleted).Scan(&deliveries); err != nil {
				t.Fatal(err)
			}
			if deliveries != 0 {
				t.Fatalf("review disposition asserted %d refinement deliveries", deliveries)
			}

			pin, err = ReadWorkPin(context.Background(), s, workID)
			if err != nil {
				t.Fatal(err)
			}
			if !workPinContainsAction(pin.NextValidIntents, "supersede_contract") {
				t.Fatalf("accepted review leaves operator contract correction hidden: %#v", pin.NextValidIntents)
			}
			if workPinContainsAction(pin.NextValidIntents, "accept_worker_evidence") {
				t.Fatal("a dispositioned review remains offered for evidence acceptance")
			}
			version := verdictItemVersion(t, s, workID)
			if err := runVerdictActionAs(t, s, workID, "accept_worker_evidence", payload, 0, acceptor); err == nil {
				t.Fatal("the same review was dispositioned twice through distinct operations")
			}
			if verdictItemVersion(t, s, workID) != version {
				t.Fatal("refused repeated disposition changed the work")
			}
			if err := RebuildFromLog(context.Background(), s); err != nil {
				t.Fatalf("replay accepted review evidence: %v", err)
			}
			reviewGateRequireStep(t, s, workID, "refine")
			pin, err = ReadWorkPin(context.Background(), s, workID)
			if err != nil || !workPinContainsAction(pin.NextValidIntents, "supersede_contract") {
				t.Fatalf("replay lost contract correction: pin=%#v err=%v", pin, err)
			}
			var originalContract []byte
			if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowContractApproved).Scan(&originalContract); err != nil {
				t.Fatal(err)
			}
			var originalFields, successorFields map[string]json.RawMessage
			if err := json.Unmarshal(originalContract, &originalFields); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(issue1013SuccessorContract(), &successorFields); err != nil {
				t.Fatal(err)
			}
			successorFields["architecture_binding"] = originalFields["architecture_binding"]
			successor := mustJSONValue(successorFields)
			if err := runVerdictActionAs(t, s, workID, "supersede_contract", successor, 0, acceptor); err == nil {
				t.Fatal("evidence disposition supplied operator approval for contract correction")
			}
			if err := issue1013Preflight(t, s, workID, "supersede_contract", successor, fixture.owner); err != nil {
				t.Fatalf("operator correction preflight after review disposition: %v", err)
			}
			if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", successor, fixture.owner, fixture.operator); err != nil {
				t.Fatalf("approved correction after review disposition: %v", err)
			}
			contract, err := s.ActiveWorkflowContract(context.Background(), workID)
			if err != nil || contract.Version != 2 {
				t.Fatalf("successor contract=(%#v,%v)", contract, err)
			}
			if err := RebuildFromLog(context.Background(), s); err != nil {
				t.Fatalf("replay evidence disposition and approved successor: %v", err)
			}
			if err := runVerdictActionAs(t, s, workID, "accept_worker_evidence", payload, 0, acceptor); err == nil {
				t.Fatal("old review was accepted as a successor-contract report")
			}
			// The approved successor owns fresh refinement work, not an epoch
			// reset used to escape an undispositioned report.
			epoch = reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
			version = verdictItemVersion(t, s, workID)

			// An authorization closes correction before native dispatch exists.
			authorization := workflowEventWithActor("new-review-authorization-"+workID, WorkflowActionCompleted, workID, ownerRef, map[string]any{
				"work_id": workID, "expected_version": version, "resulting_version": version + 1,
				"step_id": "refine", "action_id": "dispatch_worker", "attempt_epoch": epoch,
				"worker_attempt_id": "attempt:" + workID + ":new", "actor_ref": ownerRef,
			})
			authorization.OccurredAt = time.Unix(200, 0).UTC()
			authorization.PayloadVersion = 2
			if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{authorization}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
				t.Fatal(err)
			}
			pin, err = ReadWorkPin(context.Background(), s, workID)
			if err != nil || workPinContainsAction(pin.NextValidIntents, "supersede_contract") {
				t.Fatalf("new unused authorization did not close correction: pin=%#v err=%v", pin, err)
			}
			if err := RebuildFromLog(context.Background(), s); err != nil {
				t.Fatalf("later successor epoch changed old review disposition replay: %v", err)
			}
		})
	}
}

func TestRefineReviewEvidenceDispositionRefusesInvalidAttempts(t *testing.T) {
	for _, scenario := range []string{"wrong-attempt", "wrong-epoch", "delivery-fields", "implementation", "verification", "uncompleted", "self-acceptance"} {
		t.Run(scenario, func(t *testing.T) {
			workID := "review-disposition-negative-" + scenario
			definition, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
			if err != nil {
				t.Fatal(err)
			}
			fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, definition, "refine", []string{"review"}, []string{"review"})
			s := fixture.store
			ownerRef, err := WorkflowActorRef(fixture.owner)
			if err != nil {
				t.Fatal(err)
			}
			epoch := reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
			attemptID := "attempt:" + workID + ":report"
			capability := "review"
			if scenario == "implementation" || scenario == "verification" {
				capability = scenario
			}
			reviewGateRunAttemptWithVerdict(t, s, workID, attemptID, epoch, reviewGateLane(t, capability), ownerRef, "ship", 100)
			if scenario == "uncompleted" {
				if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE worker_attempts SET lifecycle_state='dispatched',completed_at=NULL WHERE attempt_id=?; DELETE FROM fold_guard`, attemptID); err != nil {
					t.Fatal(err)
				}
			}
			acceptor := reviewGateAcceptor(workID)
			if scenario == "self-acceptance" {
				acceptor = fixture.owner
			}
			fields := map[string]any{"attempt_id": attemptID, "attempt_epoch": epoch}
			if scenario == "wrong-attempt" {
				fields["attempt_id"] = "attempt:another-work"
			}
			if scenario == "wrong-epoch" {
				fields["attempt_epoch"] = epoch + 1
			}
			if scenario == "delivery-fields" {
				fields["delivery_artifact"] = "artifact:unapproved"
				fields["delivery_state"] = "asserted"
			}
			payload := mustJSONValue(fields)
			version := verdictItemVersion(t, s, workID)
			if err := runVerdictActionAs(t, s, workID, "accept_worker_evidence", payload, 0, acceptor); err == nil {
				t.Fatalf("%s evidence acceptance was admitted", scenario)
			}
			if verdictItemVersion(t, s, workID) != version {
				t.Fatal("refused evidence acceptance changed the work")
			}
			reviewGateRequireStep(t, s, workID, "refine")
		})
	}
}

func TestRefineReviewEvidenceAcceptancePreservesVerdictDebt(t *testing.T) {
	const workID = "review-evidence-debt"
	ctx := context.Background()
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, definition, "refine", []string{"review"}, []string{"review"})
	s := fixture.store
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	epoch := reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
	for i, verdict := range []string{"no_ship", "ship"} {
		attemptID := "attempt:" + workID + ":" + verdict
		reviewGateRunAttemptWithVerdict(t, s, workID, attemptID, epoch, reviewGateLane(t, "review"), ownerRef, verdict, int64(100+i*10))
		payload := mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": epoch})
		if err := runVerdictActionAs(t, s, workID, "accept_worker_evidence", payload, 0, reviewGateAcceptor(workID)); err != nil {
			t.Fatal(err)
		}
		outstanding, err := workflowPostRejectionReviewOutstanding(ctx, s.db, workID, definition.Definition, "review_evidence_test")
		if err != nil || outstanding != (verdict == "no_ship") {
			t.Fatalf("%s evidence acceptance debt=(%v,%v)", verdict, outstanding, err)
		}
		reviewGateRequireStep(t, s, workID, "refine")
		if err := reviewGateRecordDelivery(t, s, workID, reviewGateAcceptor(workID)); err == nil {
			t.Fatal("review evidence acceptance bypassed the delivery verification proof")
		}
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("replay sequential review evidence dispositions: %v", err)
	}
	outstanding, err := workflowPostRejectionReviewOutstanding(ctx, s.db, workID, definition.Definition, "review_evidence_test")
	if err != nil || outstanding {
		t.Fatalf("replay did not preserve settled review debt: (%v,%v)", outstanding, err)
	}
}

func TestRefineReviewEvidenceCorrectionHoldsUndispositionedAuthorization(t *testing.T) {
	for _, recordedFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("recorded-failure-%v", recordedFailure), func(t *testing.T) {
			workID := fmt.Sprintf("review-evidence-unsettled-%v", recordedFailure)
			definition, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
			if err != nil {
				t.Fatal(err)
			}
			fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, definition, "refine", []string{"review"}, []string{"review"})
			s := fixture.store
			ownerRef, err := WorkflowActorRef(fixture.owner)
			if err != nil {
				t.Fatal(err)
			}
			epoch := reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
			if recordedFailure {
				failedID := "attempt:" + workID + ":failed"
				failedEpoch := dispatchCheckpointReviewAttempt(t, fixture, workID, "refine", failedID)
				epoch = failedEpoch
				failCheckpointReviewAttempt(t, s, workID, failedID)
				if err := runVerdictActionAs(t, s, workID, "record_worker_failure", mustJSONValue(map[string]any{"attempt_id": failedID, "attempt_epoch": failedEpoch}), 0, fixture.owner); err != nil {
					t.Fatal(err)
				}
			}
			reviewID := "attempt:" + workID + ":accepted"
			reviewGateRunAttemptWithVerdict(t, s, workID, reviewID, epoch, reviewGateLane(t, "review"), ownerRef, "ship", 100)
			if err := runVerdictActionAs(t, s, workID, "accept_worker_evidence", mustJSONValue(map[string]any{"attempt_id": reviewID, "attempt_epoch": epoch}), 0, reviewGateAcceptor(workID)); err != nil {
				t.Fatal(err)
			}
			pin := issue1013Pin(t, s, workID)
			if !issue1013HasIntent(pin, "supersede_contract") {
				t.Fatal("settled review did not open correction")
			}
			version := verdictItemVersion(t, s, workID)
			opening := workflowEventWithActor("review-evidence-unsettled-opening-"+workID, WorkflowActionCompleted, workID, ownerRef, map[string]any{
				"work_id": workID, "expected_version": version, "resulting_version": version + 1,
				"step_id": "refine", "action_id": "dispatch_worker", "attempt_epoch": epoch,
				"worker_attempt_id": "attempt:" + workID + ":pending", "actor_ref": ownerRef,
			})
			opening.PayloadVersion = 2
			if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{opening}, ExpectedVersions: workVersion(workID, version)}); err != nil {
				t.Fatal(err)
			}
			pin = issue1013Pin(t, s, workID)
			if issue1013HasIntent(pin, "supersede_contract") {
				t.Fatal("undispositioned same-pass authorization left contract correction open")
			}
			if err := RebuildFromLog(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			if issue1013HasIntent(issue1013Pin(t, s, workID), "supersede_contract") {
				t.Fatal("replay reopened correction over an undispositioned authorization")
			}
		})
	}
}

func TestRefineReviewEvidenceCorrectionClosesOnFencedDispatch(t *testing.T) {
	for _, ref := range []string{"workflow.break_fix", "workflow.implementation"} {
		t.Run(ref, func(t *testing.T) {
			workID := "review-evidence-fenced-" + ref
			definition, err := BuiltinWorkflowDefinitionForRef(ref)
			if err != nil {
				t.Fatal(err)
			}
			fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, definition, "refine", []string{"review"}, []string{"review"})
			s := fixture.store
			reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
			reviewID := "attempt:" + workID + ":review"
			epoch := dispatchCheckpointReviewAttempt(t, fixture, workID, "refine", reviewID)
			completion := reviewLaneVerdictCompleteEvent(workID, "fenced-review-completion-"+workID, reviewID, reviewGateLane(t, "review"), "ship", time.Unix(100, 0).UTC())
			jobBoundReviewCompletion(t, &completion, reviewID, s)
			if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completion}}); err != nil {
				t.Fatal(err)
			}
			if err := runVerdictActionAs(t, s, workID, "accept_worker_evidence", mustJSONValue(map[string]any{"attempt_id": reviewID, "attempt_epoch": epoch}), 0, reviewGateAcceptor(workID)); err != nil {
				t.Fatal(err)
			}
			if err := RebuildFromLog(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			if !issue1013HasIntent(issue1013Pin(t, s, workID), "supersede_contract") {
				t.Fatal("fenced review acceptance did not open correction")
			}
			laneVersion, laneDigest := registeredLaneIdentity(t, "review")
			packet := joinPacketFor(t, s, workID, "refine", "attempt:"+workID+":pending", "review", laneVersion, laneDigest)
			attachReadyWorkerJobIfJobCapable(t, s, workID, "refine", fixture.owner, packet)
			if _, err := dispatchJoinAttempt(context.Background(), t, s, workID, verdictItemVersion(t, s, workID), fixture.owner, packet); err != nil {
				t.Fatal(err)
			}
			if issue1013HasIntent(issue1013Pin(t, s, workID), "supersede_contract") {
				t.Fatal("fenced authorization did not close correction")
			}
			if err := RebuildFromLog(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			if issue1013HasIntent(issue1013Pin(t, s, workID), "supersede_contract") {
				t.Fatal("replay reopened correction after a fenced authorization")
			}
		})
	}
}
