package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Delivery needs an accepted ready Review of the current integrated candidate
// and depended-on revisions. Dispatch order alone cannot invalidate Review.

func reviewGateLane(t *testing.T, capabilityClass string) LaneDefinition {
	t.Helper()
	for _, lane := range BuiltinLaneDefinitions() {
		if lane.CapabilityClass == capabilityClass {
			return lane
		}
	}
	t.Fatalf("no builtin lane carries capability class %q", capabilityClass)
	return LaneDefinition{}
}

// reviewGateRunAttempt records the attempt's dispatch_worker authorization,
// worker.dispatched evidence, and completed report; it returns the attempt's
// recorded authorization epoch, which later accepts and rejects must name.
func reviewGateRunAttempt(t *testing.T, s *Store, workID, attemptID, stepID string, lane LaneDefinition, ownerRef string, at int64) int64 {
	t.Helper()
	recordWorkerFixtureDispatchAuthorization(t, s, workID, attemptID, stepID, ownerRef)
	// The authorization just opened the step's newest action start, so the
	// latest start epoch is this attempt's recorded authorization epoch.
	epoch := latestStepStartEpoch(t, s, workID, stepID)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
		EventID: "review-gate-dispatch-" + attemptID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: ownerRef, OccurredAt: time.Unix(at, 0).UTC(), PayloadVersion: 2,
		Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion}),
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		workerCompleteEventForLane(workID, "review-gate-completed-"+attemptID, attemptID, lane, time.Unix(at+1, 0).UTC(), workerFixtureDirectory(t, s, workID)),
	}}); err != nil {
		t.Fatal(err)
	}
	return epoch
}

func reviewGateStartStep(t *testing.T, s *Store, workID, stepID, startAction string, actor WorkflowActor) {
	t.Helper()
	// CD-0198 D3: current-head fixtures derive the step start from the
	// step's qualifying checkpoint; released pins keep the callable start
	// the caller named. The pinned definition decides, so one helper serves
	// both sides of the derived-start boundary. The derivation pair — the
	// checkpoint that its own derived start opened — binds the fence from
	// the calling actor, exactly as the callable start did.
	entry, err := VerifyWorkflowInstanceDefinition(context.Background(), s, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		t.Fatal(err)
	}
	action := startAction
	if !definitionStepAllows(entry.Definition, stepID, startAction) {
		for candidate, step := range workflowDerivedStartCheckpoints {
			if step == stepID && definitionStepAllows(entry.Definition, stepID, candidate) {
				action = candidate
				break
			}
		}
	}
	if err := runVerdictActionAs(t, s, workID, action, json.RawMessage(`{}`), 0, actor); err != nil {
		t.Fatalf("start %s at %s: %v", action, stepID, err)
	}
	reviewGateStartedEpoch(t, s, workID, action)
}

func reviewGateStartedEpoch(t *testing.T, s *Store, workID, action string) int64 {
	t.Helper()
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionStarted, action).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
}

func reviewGateRequireStep(t *testing.T, s *Store, workID, want string) {
	t.Helper()
	if got := currentStep(t, s, workID); got != want {
		t.Fatalf("current step = %q, want %q", got, want)
	}
}

func reviewGateAcceptResult(t *testing.T, s *Store, workID, attemptID string, epoch int64, acceptor WorkflowActor) error {
	t.Helper()
	payload := json.RawMessage(`{"attempt_id":"` + attemptID + `","attempt_epoch":` + fmt.Sprint(epoch) + `}`)
	return runVerdictActionAs(t, s, workID, "accept_worker_result", payload, 0, acceptor)
}

func reviewGateRecordDelivery(t *testing.T, s *Store, workID string, acceptor WorkflowActor) error {
	t.Helper()
	payload := json.RawMessage(`{"delivery_artifact":"artifact:review-gate-` + workID + `","delivery_state":"asserted"}`)
	return runVerdictActionAs(t, s, workID, "record_delivery", payload, 0, acceptor)
}

func reviewGateRejectResult(t *testing.T, s *Store, workID, attemptID string, epoch int64, actor WorkflowActor) {
	t.Helper()
	payload := json.RawMessage(`{"attempt_id":"` + attemptID + `","attempt_epoch":` + fmt.Sprint(epoch) + `,"diagnosis":"the review rejects the refined result","strategy":"implement the review findings and resubmit","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	if err := runVerdictActionAs(t, s, workID, "reject_worker_result", payload, 0, actor); err != nil {
		t.Fatalf("reject worker result %s: %v", attemptID, err)
	}
}

func reviewGateRefineFixture(t *testing.T, workID, family string) *candidateJourney {
	t.Helper()
	j := pReturnReviewFixture(t, workID, family, "ready_for_final_proof")
	j.acceptor = j.fixture.owner
	j.bindVerification(t, j.observeRun(t, "execution-integration"))
	if err := reviewGateRecordDelivery(t, j.s, workID, j.fixture.owner); err != nil {
		t.Fatalf("deliver reviewed execution: %v", err)
	}
	reviewGateRequireStep(t, j.s, workID, "refine")
	reviewGateStartStep(t, j.s, workID, "refine", "start_refine", j.fixture.owner)
	return j
}

func reviewGateDriveRejection(t *testing.T, j *candidateJourney) {
	t.Helper()
	attempt, epoch := reviewGateCompleteReview(t, j, "correction_required")
	reviewGateRejectResult(t, j.s, j.workID, attempt, epoch, j.fixture.owner)
	attempt, epoch = reviewGateCompleteReview(t, j, "continue")
	if err := reviewGateAcceptResult(t, j.s, j.workID, attempt, epoch, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
}

// Re-record the same obligation after rejection; a new unrelated job would
// leave the rejected revision required and unsatisfied at whole-work delivery.
func reviewGateCompleteReview(t *testing.T, j *candidateJourney, disposition string) (string, int64) {
	t.Helper()
	job := WorkerJobBinding{JobID: "job:review-gate"}
	recordWorkerJobRevisionForTest(t, j.s, j.workID, j.fixture.owner, &job)
	attempt := fmt.Sprintf("review:gate:%d", job.Revision)
	if err := descendantDispatch(t, j, "review", attempt, job, nil); err != nil {
		t.Fatal(err)
	}
	j.laneDispatch(t, attempt, "review", &job)
	j.completeReview(t, attempt, job, currentReviewBlockForTest(t, j.s, j.workID, attempt, disposition))
	return attempt, reviewSubjectWindow(t, j.s, j.workID, attempt).AttemptEpoch
}

// Test setup, including verification, finishes before this effect snapshot.
func reviewGateRefusal(t *testing.T, j *candidateJourney, route deliveryEntryRoute, attempt string, epoch int64, kind FailureKind, detail string) {
	t.Helper()
	version := readWorkVersion(t, j.s, j.workID)
	deliveries := reviewGateCountDeliveries(t, j.s, j.workID)
	var before int
	if err := j.s.db.QueryRow(`SELECT count(*) FROM domain_events`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	err := route.exit(t, j.s, j.workID, attempt, epoch, j.fixture.owner, nil, false)
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || failure.Kind != kind || !strings.Contains(failure.Detail, detail) {
		t.Fatalf("%s exit = %v, want %s naming %q", route.name, err, kind, detail)
	}
	var after int
	if err := j.s.db.QueryRow(`SELECT count(*) FROM domain_events`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before || readWorkVersion(t, j.s, j.workID) != version || reviewGateCountDeliveries(t, j.s, j.workID) != deliveries {
		t.Fatal("refused delivery changed events, work version, or delivery assertions")
	}
	reviewGateRequireStep(t, j.s, j.workID, "refine")
}

func reviewGateExitWithCurrentReview(t *testing.T, j *candidateJourney, route deliveryEntryRoute) {
	t.Helper()
	attempt, epoch := reviewGateCompleteReview(t, j, "ready_for_final_proof")
	pending := issue1013Pin(t, j.s, j.workID)
	if workPinContainsAction(pending.NextValidIntents, "record_delivery") || !workPinContainsAction(pending.NextValidIntents, "accept_worker_result") {
		t.Fatalf("unaccepted current Review intents = %+v, want acceptance without delivery", pending.NextValidIntents)
	}
	if route.name == "record_delivery" {
		if err := reviewGateAcceptResult(t, j.s, j.workID, attempt, epoch, j.fixture.owner); err != nil {
			t.Fatal(err)
		}
		reviewGateRequireStep(t, j.s, j.workID, "refine")
	}
	j.bindVerification(t, j.observeRun(t, "refine-integration"))
	if err := route.exit(t, j.s, j.workID, attempt, epoch, j.fixture.owner, nil, false); err != nil {
		t.Fatalf("%s after current Review: %v", route.name, err)
	}
	reviewGateRequireStep(t, j.s, j.workID, "delivery")
	pin := issue1013Pin(t, j.s, j.workID)
	if !workPinContainsAction(pin.NextValidIntents, "record_delivery") {
		t.Fatalf("current Review did not restore delivery: %+v", pin.NextValidIntents)
	}
	if err := reviewGateRecordDelivery(t, j.s, j.workID, j.fixture.owner); err != nil {
		t.Fatalf("gate crossing after current Review: %v", err)
	}
	reviewGateRequireStep(t, j.s, j.workID, "verify")
}

func reviewGateAcceptor(workID string) WorkflowActor {
	return WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/review-gate-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
}

func TestPostRejectionReviewGateHoldsBreakFixDelivery(t *testing.T) {
	for _, route := range deliveryEntryRoutes() {
		t.Run(route.name, func(t *testing.T) {
			j := reviewGateRefineFixture(t, "review-gate-break-fix", "workflow.break_fix")
			reviewGateDriveRejection(t, j)
			attempt, epoch := reviewGateCompleteReview(t, j, "ready_for_final_proof")
			pReturnProduce(t, j, "repair")
			kind, detail := KindMissingEvidence, "accepted Review on the current candidate"
			if route.name == "accept_worker_result" {
				kind, detail = KindStaleRequiresReview, "newer accepted candidate replaced this Review's input"
			}
			reviewGateRefusal(t, j, route, attempt, epoch, kind, detail)
			pin := issue1013Pin(t, j.s, j.workID)
			if workPinContainsAction(pin.NextValidIntents, "record_delivery") {
				t.Fatalf("pin offered delivery behind unreviewed repair: %+v", pin.NextValidIntents)
			}
			// CD-0242 D5 keeps payload-blind acceptance as a hold. The
			// exact stale acceptance above still refuses without any effect.
			if !workPinContainsAction(pin.NextValidIntents, "accept_worker_result") {
				t.Fatal("pin hid Main's result-disposition hold")
			}
			if err := j.action(t, "request_correction", reviewGateCorrectionPayload("evidence:return-route-verification"), j.fixture.owner); err == nil {
				t.Fatal("refine admitted correction without an accepted correction-required Review")
			}
			reviewGateExitWithCurrentReview(t, j, route)
		})
	}
}

func TestDeliveryGateDeclaresCorrectiveReturn(t *testing.T) {
	for _, family := range []string{"workflow.implementation", "workflow.break_fix"} {
		entry := mustBuiltinDefinition(t, family)
		gate := workflowStep(entry.Definition, "delivery")
		if !workflowStepIsDeliveryGate(gate) || len(gate.Actions) != 4 || gate.Actions[0] != "record_delivery" || gate.Actions[1] != "request_correction" {
			t.Fatalf("%s gate actions=%v", family, gate.Actions)
		}
		if workflowCorrectionReturnTarget(entry.Definition, "delivery") == "" {
			t.Fatalf("%s gate has no producer return", family)
		}
	}
	// The registry exposes only the current gate as executable authority.
	if _, ok := BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 13); ok {
		t.Fatal("retired closed gate remains registered")
	}
}

// An unaccepted Review of the earlier candidate cannot authorize a candidate
// changed by a later accepted implementation, regardless of dispatch order.
func TestPostRejectionReviewGateRefusesPreRejectionReviewAccept(t *testing.T) {
	for _, route := range deliveryEntryRoutes() {
		t.Run(route.name, func(t *testing.T) {
			j := reviewGateRefineFixture(t, "review-gate-stale-review", "workflow.break_fix")
			early, epoch := reviewGateCompleteReview(t, j, "ready_for_final_proof")
			before := issue1013Pin(t, j.s, j.workID).Review.Candidate
			reviewGateDriveRejection(t, j)
			pReturnProduce(t, j, "repair")
			after := issue1013Pin(t, j.s, j.workID).Review.Candidate
			if before == nil || after == nil || *before == *after {
				t.Fatal("repair did not replace the integrated candidate")
			}
			kind, detail := KindMissingEvidence, "accepted Review on the current candidate"
			if route.name == "accept_worker_result" {
				kind, detail = KindStaleRequiresReview, "newer accepted candidate replaced this Review's input"
			}
			reviewGateRefusal(t, j, route, early, epoch, kind, detail)
			reviewGateExitWithCurrentReview(t, j, route)
		})
	}
}

// An accepted Review becomes inapplicable when its candidate changes. A later
// completed Review with unchanged inputs does not invalidate it by chronology.
func TestPostRejectionReviewGateRefusesReviewDispatchedBeforeRepair(t *testing.T) {
	for _, route := range deliveryEntryRoutes() {
		t.Run(route.name, func(t *testing.T) {
			j := reviewGateRefineFixture(t, "review-gate-review-before-repair", "workflow.break_fix")
			reviewGateDriveRejection(t, j)
			early, epoch := reviewGateCompleteReview(t, j, "continue")
			if err := reviewGateAcceptResult(t, j.s, j.workID, early, epoch, j.fixture.owner); err != nil {
				t.Fatal(err)
			}
			early, epoch = reviewGateCompleteReview(t, j, "ready_for_final_proof")
			pReturnProduce(t, j, "repair")
			kind, detail := KindMissingEvidence, "accepted Review on the current candidate"
			if route.name == "accept_worker_result" {
				kind, detail = KindStaleRequiresReview, "newer accepted candidate replaced this Review's input"
			}
			reviewGateRefusal(t, j, route, early, epoch, kind, detail)
			reviewGateExitWithCurrentReview(t, j, route)
		})
	}
	t.Run("later launch keeps unchanged Review applicable", func(t *testing.T) {
		j := reviewGateRefineFixture(t, "review-gate-unchanged", "workflow.break_fix")
		before := issue1013Pin(t, j.s, j.workID).Review
		attempt, epoch := reviewGateCompleteReview(t, j, "ready_for_final_proof")
		after := issue1013Pin(t, j.s, j.workID).Review
		if before == nil || after == nil || before.Accepted.ReportRef != after.Accepted.ReportRef || *before.Candidate != *after.Candidate || !after.DependenciesCurrent {
			t.Fatal("unchanged launch invalidated accepted Review")
		}
		if err := workflowAdmitReview(*after, ""); err != nil {
			t.Fatalf("unchanged launch invalidated readiness: %v", err)
		}
		// The new job still needs its own disposition before whole-work
		// delivery, independently of the standing Review's applicability.
		if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}, j.fixture.owner); err != nil {
			t.Fatal(err)
		}
		reviewGateRequireStep(t, j.s, j.workID, "refine")
		j.bindVerification(t, j.observeRun(t, "unchanged-proof"))
		if err := reviewGateRecordDelivery(t, j.s, j.workID, j.fixture.owner); err != nil {
			t.Fatalf("unchanged accepted Review did not authorize delivery: %v", err)
		}
		reviewGateRequireStep(t, j.s, j.workID, "delivery")
	})
	t.Run("Review corrective output needs no further Review", func(t *testing.T) {
		j := reviewGateRefineFixture(t, "review-gate-corrective-output", "workflow.break_fix")
		job := reviewSubjectReadyRevision(t, j.fixture)
		const attempt = "review:corrective-output"
		if err := descendantDispatch(t, j, "review", attempt, job, nil); err != nil {
			t.Fatal(err)
		}
		j.laneDispatch(t, attempt, "review", &job)
		report := currentReviewBlockForTest(t, j.s, j.workID, attempt, "ready_for_final_proof")
		path := filepath.Join(j.repo, "internal", "store", job.JobID, "value.go")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package fixture\nconst Value = 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		output := j.commit(t, "reviewer@example.invalid", "correct the scoped candidate")
		report.OutputCandidate.Commit, report.OutputCandidate.Tree = output, reviewObservedTree(t, j.repo, output)
		j.completeReview(t, attempt, job, report)
		if err := reviewGateAcceptResult(t, j.s, j.workID, attempt, j.dispatchEpoch(t, attempt), j.fixture.owner); err != nil {
			t.Fatal(err)
		}
		pin := issue1013Pin(t, j.s, j.workID)
		if pin.Review.Candidate.Commit != output || pin.Review.Accepted.Report.OutputCandidate.Commit != output || !pin.Review.DependenciesCurrent {
			t.Fatalf("corrective Review did not bind its output: %+v", pin.Review)
		}
		j.bindVerification(t, j.observeRun(t, "corrective-output-proof"))
		if err := reviewGateRecordDelivery(t, j.s, j.workID, j.fixture.owner); err != nil {
			t.Fatalf("corrective output required another Review: %v", err)
		}
		reviewGateRequireStep(t, j.s, j.workID, "delivery")
	})
}

// TestCurrentDeliveryGateAdmitsEvidenceBearingReturn exercises an admitted
// request_correction on the current break-fix and implementation gates: a
// parked gate with an outstanding post-rejection review offers the
// evidence-bearing return, and the fold returns the item to the correction
// target step for a fresh review.
func TestCurrentDeliveryGateAdmitsEvidenceBearingReturn(t *testing.T) {
	for _, tc := range []struct {
		definitionRef string
		targetStep    string
	}{
		{definitionRef: "workflow.break_fix", targetStep: "repair"},
		{definitionRef: "workflow.implementation", targetStep: "execution"},
	} {
		t.Run(tc.definitionRef, func(t *testing.T) {
			const workID = "current-gate-return"
			ctx := context.Background()
			j := reviewGateRefineFixture(t, workID, tc.definitionRef)
			s := j.s
			fixture := j.fixture
			reviewGateDriveRejection(t, j)
			pReturnProduce(t, j, "repair")
			parkWorkflowInstanceAt(t, s, workID, "delivery")
			reviewGateRequireStep(t, s, workID, "delivery")

			current := mustBuiltinDefinition(t, tc.definitionRef)
			pinnedVersion, pinnedDigest := reviewGateInstancePin(t, s, workID)
			if pinnedVersion != current.Definition.Version || pinnedDigest != current.Digest {
				t.Fatalf("pinned definition = v%d %s, want v%d %s", pinnedVersion, pinnedDigest, current.Definition.Version, current.Digest)
			}

			// The pin advertises the corrective return, not the delivery exit.
			pin, pinErr := ReadWorkPin(ctx, s, workID)
			if pinErr != nil {
				t.Fatal(pinErr)
			}
			if workPinContainsAction(pin.NextValidIntents, "record_delivery") {
				t.Fatalf("parked gate offered record_delivery: %#v", pin.NextValidIntents)
			}
			returnIntent := ""
			for _, intent := range pin.NextValidIntents {
				if intent.ActionID == "request_correction" {
					returnIntent = intent.ReasonCode
				}
			}
			if returnIntent != "delivery_gate_correction" {
				t.Fatalf("gate correction intent = %q, want delivery_gate_correction", returnIntent)
			}

			// The evidence-bearing return is admitted and returns the item to
			// the correction target step for a fresh review.
			deliveries := reviewGateCountDeliveries(t, s, workID)
			if err := runVerdictActionAs(t, s, workID, "request_correction", reviewGateCorrectionPayload("evidence:return-route-verification"), 0, fixture.owner); err != nil {
				t.Fatalf("evidence-bearing corrective return on the current gate: %v", err)
			}
			reviewGateRequireStep(t, s, workID, tc.targetStep)
			if count := reviewGateCountDeliveries(t, s, workID); count != deliveries {
				t.Fatalf("corrective return changed delivery assertions from %d to %d", deliveries, count)
			}
		})
	}
}

// TestCurrentDeliveryGateReturnsWhenSuccessorStalesAcceptedReview: a
// successor contract that changes a criterion leaves the accepted Review
// inapplicable, so the parked gate offers only the evidence-bearing return.
func TestCurrentDeliveryGateReturnsWhenSuccessorStalesAcceptedReview(t *testing.T) {
	for _, family := range []string{"workflow.break_fix", "workflow.implementation"} {
		t.Run(family, func(t *testing.T) {
			j := pReturnReviewFixture(t, "current-gate-return", family, "ready_for_final_proof")
			owner, err := WorkflowActorRef(j.fixture.owner)
			if err != nil {
				t.Fatal(err)
			}
			// Park at the gate, then change the contract through its fold owner.
			if err := advanceWorkflowTestInstanceToStep(t.Context(), j.s, j.workID, "delivery", owner); err != nil {
				t.Fatal(err)
			}
			successor := pContractSuccessor(t, j.s, j.workID, "changed criterion")
			delete(successor, "audit_evidence")
			delete(successor, "supersede_reason")
			successor["outcome_kind"] = "check"
			predicates := successor["outcome_predicates"].([]any)
			predicates[0].(map[string]any)["outcome_payload"].(map[string]any)["expected_result"] = "fail"
			version := readWorkVersion(t, j.s, j.workID)
			successor["law_revisions"], successor["law_boundary_version"], successor["consequence_class"] = []WorkflowLawRevision{}, 1, "internal_sqlite"
			event := workflowEventWithActor("p-gate-successor", WorkflowContractSuperseded, j.workID, owner, map[string]any{"work_id": j.workID, "expected_version": version, "resulting_version": version + 1, "previous_contract_version": 1, "new_contract_version": 2, "supersede_reason": "changed criterion", "audit_evidence": []string{"evidence:return-route-artifact"}, "successor_contract": successor})
			event.PayloadVersion = 1
			event = withPinnedDecisionFact(t, pinnedWorkflowDefinitionForTest(t, j.s, j.workID), event)
			if err := applyWorkflowTestOperation(t.Context(), j.s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, j.workID): version}}); err != nil {
				t.Fatal(err)
			}
			pin := issue1013Pin(t, j.s, j.workID)
			if pin.Review == nil || pin.Review.DependenciesCurrent || issue1013HasIntent(pin, "record_delivery") {
				t.Fatalf("stale gate offered delivery: %+v", pin.NextValidIntents)
			}
			if !issue1013HasIntent(pin, "request_correction") {
				t.Fatalf("stale gate hides corrective return: %+v", pin.NextValidIntents)
			}
			var reason string
			for _, intent := range pin.NextValidIntents {
				if intent.ActionID == "request_correction" {
					reason = intent.ReasonCode
				}
			}
			if reason != "delivery_gate_correction" {
				t.Fatalf("gate correction intent=%q, want delivery_gate_correction", reason)
			}
			payload := pReviewCorrectionPayload(t)
			deliveries := reviewGateCountDeliveries(t, j.s, j.workID)
			if err := j.action(t, "request_correction", payload, j.fixture.owner); err != nil {
				t.Fatalf("evidence-bearing current gate return: %v", err)
			}
			effect := "execution"
			if family == "workflow.break_fix" {
				effect = "repair"
			}
			reviewGateRequireStep(t, j.s, j.workID, effect)
			if count := reviewGateCountDeliveries(t, j.s, j.workID); count != deliveries {
				t.Fatalf("corrective return changed asserted deliveries from %d to %d", deliveries, count)
			}
		})
	}
}
