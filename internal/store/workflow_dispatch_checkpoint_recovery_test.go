package store

// The registry checkpoint predicate and the admission model must admit
// dispatch_worker steps of the cross_authority and internal_sqlite kinds the
// same way external_effect steps are admitted, the exclusion shapes stay
// excluded, and the availability answers at one synthetic research instance
// must agree with the work pin and the supersede_contract resolution on
// every worker-dispatch state.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

const dispatchCorrectionMissingStep = "no-such-dispatch-step"

func TestDispatchCheckpointCorrectionAcrossKinds(t *testing.T) {
	counts := map[WorkflowStepKind]int{}
	for _, definition := range BuiltinWorkflowDefinitions() {
		if workflowFamilyArchived(definition.Ref) {
			continue
		}
		if workflowContractCorrectionCheckpoint(definition, dispatchCorrectionMissingStep) {
			t.Fatalf("%s v%d: nonexistent step resolved as a correction checkpoint", definition.Ref, definition.Version)
		}
		for _, step := range definition.StepGraph.Steps {
			if stepDeclaresAction(definition, step.ID, "complete") {
				// The reserved complete-step correction gate owns these
				// steps; this check asserts neither state for them.
				continue
			}
			if !slices.Contains(step.Actions, "dispatch_worker") {
				dispatchCorrectionAssertExcluded(t, definition, step)
				continue
			}
			switch step.Kind {
			case WorkflowStepCrossAuthority, WorkflowStepInternalSQLite, WorkflowStepExternalEffect:
				if !workflowContractCorrectionCheckpoint(definition, step.ID) {
					t.Fatalf("%s v%d step %s (%s) declares dispatch_worker but is not a correction checkpoint", definition.Ref, definition.Version, step.ID, step.Kind)
				}
				if !admissionContractCorrection(definition, admissionModelState{step: step.ID, contracts: 1}) {
					t.Fatalf("%s v%d step %s (%s): admission model refuses contract correction before dispatch", definition.Ref, definition.Version, step.ID, step.Kind)
				}
				counts[step.Kind]++
			case WorkflowStepHumanCheckpoint:
				// Registered human_checkpoint dispatch steps resolve through
				// the same structure: the complete gate owns its steps above,
				// so the checkpoint answer here is the evaluator route target
				// or the confirm_premise gate on a live step.
				expected := !slices.Contains(definition.StepGraph.TerminalSteps, step.ID) &&
					(workflowUnhealthyVerdictRouteTarget(definition, step.ID) != "" || slices.Contains(step.Actions, "confirm_premise"))
				if workflowContractCorrectionCheckpoint(definition, step.ID) != expected {
					t.Fatalf("%s v%d step %s (%s) resolves as a correction checkpoint = %v, want the structure's %v", definition.Ref, definition.Version, step.ID, step.Kind, !expected, expected)
				}
				if admissionContractCorrection(definition, admissionModelState{step: step.ID, contracts: 1}) != expected {
					t.Fatalf("%s v%d step %s (%s): admission model disagrees with the structure's %v", definition.Ref, definition.Version, step.ID, step.Kind, expected)
				}
				counts[step.Kind]++
			}
		}
	}
	for _, kind := range []WorkflowStepKind{WorkflowStepCrossAuthority, WorkflowStepInternalSQLite, WorkflowStepExternalEffect, WorkflowStepHumanCheckpoint} {
		if counts[kind] == 0 {
			t.Fatalf("no retained registered dispatch step of kind %s was counted: %v", kind, counts)
		}
	}
	dispatchCorrectionAssertProbeExclusions(t)
}

// dispatchCorrectionAssertExcluded holds the exclusions the checkpoint
// predicate must preserve on registered steps: a step without dispatch_worker
// is a correction checkpoint only through the confirm_premise gate or an
// evaluator route.
func dispatchCorrectionAssertExcluded(t *testing.T, definition WorkflowDefinition, step WorkflowStep) {
	t.Helper()
	if step.Kind == WorkflowStepHumanCheckpoint && slices.Contains(step.Actions, "confirm_premise") {
		return
	}
	if workflowUnhealthyVerdictRouteTarget(definition, step.ID) != "" {
		return
	}
	if workflowContractCorrectionCheckpoint(definition, step.ID) {
		t.Fatalf("%s v%d step %s (%s) without dispatch_worker, confirm_premise, or an evaluator route resolved as a correction checkpoint", definition.Ref, definition.Version, step.ID, step.Kind)
	}
}

// dispatchCorrectionProbeDefinition is a synthetic definition carrying the
// exclusion shapes the registered history does not: a terminal dispatch step
// that declares no complete action, a human checkpoint dispatch step without
// confirm_premise, and a plain internal step without dispatch_worker.
func dispatchCorrectionProbeDefinition() WorkflowDefinition {
	return WorkflowDefinition{
		Ref:     "workflow.correction_probe",
		Version: 1,
		StepGraph: WorkflowStepGraph{
			StartStep:     "producing",
			TerminalSteps: []string{"sealed"},
			Steps: []WorkflowStep{
				{ID: "producing", Kind: WorkflowStepInternalSQLite, Actions: []string{"record_note"}},
				{ID: "quiet", Kind: WorkflowStepHumanCheckpoint, Actions: []string{"dispatch_worker"}},
				{ID: "sealed", Kind: WorkflowStepInternalSQLite, Actions: []string{"dispatch_worker"}},
			},
		},
	}
}

func dispatchCorrectionAssertProbeExclusions(t *testing.T) {
	t.Helper()
	probe := dispatchCorrectionProbeDefinition()
	for _, stepID := range []string{"producing", "quiet", "sealed", dispatchCorrectionMissingStep} {
		if workflowContractCorrectionCheckpoint(probe, stepID) {
			t.Fatalf("probe step %q resolved as a correction checkpoint", stepID)
		}
		if admissionContractCorrection(probe, admissionModelState{step: stepID, contracts: 1}) {
			t.Fatalf("probe step %q: admission model admits contract correction", stepID)
		}
	}
}

// seedDispatchCorrectionFixture parks the current research fixture at investigate.
func seedDispatchCorrectionFixture(t *testing.T, workID string) crossAttemptFixture {
	t.Helper()
	return seedCrossAttemptFixture(t, workID)
}

func TestDispatchCheckpointCorrectionAvailability(t *testing.T) {
	cases := []struct {
		name         string
		workID       string
		available    bool
		model        admissionModelState
		modelApplies bool
		build        func(t *testing.T, f crossAttemptFixture)
	}{
		{
			name: "before dispatch", workID: "dispatch-correction-before-dispatch",
			available: true, model: admissionModelState{contracts: 1}, modelApplies: true,
		},
		{
			name: "authorization without materialization", workID: "dispatch-correction-authorized-only",
			available: false, modelApplies: false,
			build: func(t *testing.T, f crossAttemptFixture) {
				f.authorizeDispatch(t, f.attemptR, f.researchLane, f.dispatcher)
			},
		},
		{
			name: "live materialized worker", workID: "dispatch-correction-live-worker",
			available: false, model: admissionModelState{contracts: 1, dispatched: true}, modelApplies: true,
			build: func(t *testing.T, f crossAttemptFixture) {
				f.dispatch(t, f.attemptR, f.researchLane, f.dispatcher)
			},
		},
		{
			name: "unaccepted completed result", workID: "dispatch-correction-completed-result",
			available: false, model: admissionModelState{contracts: 1, dispatched: true, attempt: "completed"}, modelApplies: true,
			build: func(t *testing.T, f crossAttemptFixture) {
				f.dispatch(t, f.attemptR, f.researchLane, f.dispatcher)
				f.completeReport(t, f.attemptR, f.researchLane, 21)
			},
		},
		{
			name: "recorded failure", workID: "dispatch-correction-recorded-failure",
			available: true, model: admissionModelState{contracts: 1, dispatched: true, attempt: "failure_recorded"}, modelApplies: true,
			build: func(t *testing.T, f crossAttemptFixture) {
				f.dispatch(t, f.attemptR, f.researchLane, f.dispatcher)
				failure := workerFailedContextFindingsEvent(f.workID, "failed-"+f.workID, f.attemptR, preferredModelForLane(f.researchLane), WorkerFailureWorkerError, nil, WorkerEvidenceEventPayloadVersion(WorkerFailed))
				if err := s2WorkerFailedLive(t, f.s, withWorkerFailureObservation(t, failure)); err != nil {
					t.Fatalf("record authenticated failed terminal: %v", err)
				}
			},
		},
		{
			name: "rejected completed result", workID: "dispatch-correction-rejected-result",
			available: true, model: admissionModelState{contracts: 1, dispatched: true, attempt: "rejected"}, modelApplies: true,
			build: func(t *testing.T, f crossAttemptFixture) {
				f.dispatch(t, f.attemptR, f.researchLane, f.dispatcher)
				f.completeReport(t, f.attemptR, f.researchLane, 21)
				if _, err := f.invokeReject(t, readWorkVersion(t, f.s, f.workID), f.attemptR,
					crossAttemptDispatchEpoch(t, f.s, f.workID, f.attemptR), "", 30); err != nil {
					t.Fatalf("reject the completed research result: %v", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := seedDispatchCorrectionFixture(t, tc.workID)
			if tc.build != nil {
				tc.build(t, f)
			}
			available, err := workflowContractCorrectionAvailable(context.Background(), f.s.DatabaseForTesting(), f.workID, f.definition, f.step, "test")
			if err != nil {
				t.Fatalf("read dispatch checkpoint correction availability: %v", err)
			}
			if available != tc.available {
				t.Fatalf("dispatch checkpoint correction available = %v, want %v", available, tc.available)
			}
			_, _, definitionErr := WorkflowActionDefinitionFor(context.Background(), f.s, BuiltinWorkflowRegistry(), f.workID, "supersede_contract")
			if (definitionErr == nil) != tc.available {
				t.Fatalf("supersede_contract resolution (err=%v) disagrees with availability %v", definitionErr, tc.available)
			}
			pin, err := ReadWorkPin(context.Background(), f.s, f.workID)
			if err != nil {
				t.Fatal(err)
			}
			intent := false
			for _, candidate := range pin.NextValidIntents {
				if candidate.ActionID == "supersede_contract" && candidate.ReasonCode == "operator_contract_correction" {
					intent = true
				}
			}
			if intent != tc.available {
				t.Fatalf("work pin supersede intent = %v, want agreement with availability %v", intent, tc.available)
			}
			if !tc.available {
				if preflightErr := WorkflowActionPreflightWithRegistry(context.Background(), f.s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
					WorkID: f.workID, ExpectedVersion: pin.Version, ActionID: "supersede_contract", Actor: f.fixture.owner,
				}); !hasFailureKind(preflightErr, KindInvalidOperation) {
					t.Fatalf("payload-free supersede_contract preflight = %v, want the stale-contract recovery refusal", preflightErr)
				}
			}
			if tc.modelApplies {
				tc.model.step = f.step
				if got := admissionContractCorrection(f.definition, tc.model); got != tc.available {
					t.Fatalf("admission model at state %s answered %v, want %v", tc.model, got, tc.available)
				}
			}
		})
	}
}

// dispatchCorrectionAuthorize authorizes one corrected worker attempt the way
// a host worker would: the session worktree claim precedes packet
// construction, the packet consumes the standing correction context, and the
// join refusal returns for the caller to classify.
func dispatchCorrectionAuthorize(t *testing.T, f crossAttemptFixture, attemptID string, lane LaneDefinition, dispatcher WorkflowActor) error {
	t.Helper()
	dispatchSessionWorktree(t, f.s, f.workID)
	packet := joinPacketFor(t, f.s, f.workID, f.step, attemptID, lane.ID, lane.Version, lane.Digest)
	correction, err := workflowCorrectionContextForDispatch(context.Background(), f.s.DatabaseForTesting(), f.workID, f.step, attemptID)
	if err != nil {
		return err
	}
	if correction == nil {
		t.Fatalf("no standing correction context for the corrected dispatch of %s", attemptID)
	}
	packet["inputs"].(map[string]any)["correction"] = correction
	_, err = dispatchJoinAttempt(context.Background(), t, f.s, f.workID, readWorkVersion(t, f.s, f.workID), dispatcher, packet)
	return err
}

// seedResearchWalledCorrection builds two rejected producer rounds with
// corrective Review reports. Both Reviews remain undispositioned.
func seedResearchWalledCorrection(t *testing.T, workID string) crossAttemptFixture {
	t.Helper()
	f := seedCrossAttemptFixture(t, workID)
	f.buildTwoAttemptHistory(t)
	attemptR2, attemptV2 := "attempt:"+workID+":3", "attempt:"+workID+":4"
	if _, err := f.invokeReject(t, readWorkVersion(t, f.s, workID), f.attemptR, crossAttemptDispatchEpoch(t, f.s, workID, f.attemptR), "", 30); err != nil {
		t.Fatalf("reject the first research result: %v", err)
	}
	if err := dispatchCorrectionAuthorize(t, f, attemptR2, f.researchLane, f.dispatcher); err != nil {
		t.Fatalf("authorize the corrected research retry: %v", err)
	}
	f.materializeDispatch(t, attemptR2, f.researchLane, f.dispatcher)
	f.completeReport(t, attemptR2, f.researchLane, 31)
	f.dispatch(t, attemptV2, f.reviewLane, f.dispatcher)
	f.completeReviewNoShip(t, attemptV2, 32)
	if _, err := f.invokeReject(t, readWorkVersion(t, f.s, workID), attemptR2, crossAttemptDispatchEpoch(t, f.s, workID, attemptR2), "r2", 33); err != nil {
		t.Fatalf("reject the corrected research result: %v", err)
	}
	correction, correctionErr := workflowCorrectionContextForDispatch(context.Background(), f.s.DatabaseForTesting(), workID, f.step, "")
	if correctionErr != nil || correction == nil || correction.Disposition != "rejected" || correction.FailedAttemptID != attemptR2 {
		t.Fatalf("standing correction after two rejected cycles = %#v, %v, want the latest rejected source", correction, correctionErr)
	}
	if verdicts := f.eventCount(t, WorkflowVerdictRecorded, ""); verdicts != 0 {
		t.Fatalf("verdicts after two rejected cycles = %d, want no workflow verdicts", verdicts)
	}
	return f
}

// supersedeResearchWalledCorrection runs the genuine changed-requirement
// supersession behind the armed wall: the pin offers supersede_contract, the
// full successor passes the admission preflight at the current version, and
// Main lands contract 2 as the single active
// contract. Research changes no Product truth, so the successor omits the
// architecture binding its seed only carried for truth-changing families.
func supersedeResearchWalledCorrection(t *testing.T, f crossAttemptFixture) {
	t.Helper()
	pin := issue1013Pin(t, f.s, f.workID)
	if !workPinContainsAction(pin.NextValidIntents, "supersede_contract") {
		t.Fatalf("work pin behind the walled correction offers no supersede intent: %#v", pin.NextValidIntents)
	}
	successor := json.RawMessage(`{"contract_version":2,"premise":"close the investigation as a source-only report that records its unknowns","outcome_predicates":[{"predicate_id":"predicate:return-route","ordinal":0,"outcome_kind":"outcome","outcome_payload":{"kind":"outcome","allowed":["report_recorded"]}}],"required_evidence":["artifact"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"decision_domains":[],"rigor_class":"prototype_internal","predecessor_contract_versions":[1],"supersede_reason":"two rejected research rounds disproved the original premise; the requirement changed to a closed source-only report with recorded unknowns","audit_evidence":["evidence:` + f.workID + `-walled-supersede"]}`)
	if err := WorkflowActionPreflightWithRegistry(context.Background(), f.s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{WorkID: f.workID, ExpectedVersion: pin.Version, StepID: f.step, ActionID: "supersede_contract", Payload: successor, Actor: f.fixture.owner}); err != nil {
		t.Fatalf("full supersede successor preflight at the walled step: %v", err)
	}
	if err := runVerdictActionAs(t, f.s, f.workID, "supersede_contract", successor, 0, f.fixture.owner); err != nil {
		t.Fatalf("supersede the research contract behind the wall: %v", err)
	}
	var activeCount, activeVersion int64
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT count(*), COALESCE(MAX(contract_version),0) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, f.workID).Scan(&activeCount, &activeVersion); err != nil || activeCount != 1 || activeVersion != 2 {
		t.Fatalf("active contracts after the supersession = %d at v%d, %v, want exactly contract 2", activeCount, activeVersion, err)
	}
}

// walledResearchCounters is the durable effect population a refused dispatch
// must leave untouched and a projection rebuild must reproduce.
type walledResearchCounters struct {
	version, dispatches, authorizations, reports, attempts, verdicts int64
}

func walledResearchReadCounters(t *testing.T, f crossAttemptFixture) walledResearchCounters {
	t.Helper()
	counters := walledResearchCounters{version: readWorkVersion(t, f.s, f.workID), dispatches: f.eventCount(t, WorkerDispatched, ""), authorizations: f.eventCount(t, WorkflowActionCompleted, "dispatch_worker"), reports: f.eventCount(t, WorkerCompleted, ""), verdicts: f.eventCount(t, WorkflowVerdictRecorded, "")}
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT count(DISTINCT attempt_id) FROM worker_attempts WHERE work_id=?`, f.workID).Scan(&counters.attempts); err != nil {
		t.Fatal(err)
	}
	return counters
}

// walledResearchAssertUndispositioned holds the two corrective Review
// attempts to zero disposition completions: their recorded reports stay
// unaccepted, unrejected, and free of a recorded worker failure.
func walledResearchAssertUndispositioned(t *testing.T, f crossAttemptFixture) {
	t.Helper()
	reviewV2 := "attempt:" + f.workID + ":4"
	var dispositions int64
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.worker_attempt_id') IN (?,?) AND json_extract(payload,'$.action_id') IN ('accept_worker_result','accept_worker_evidence','reject_worker_result','record_worker_failure')`, f.workID, WorkflowActionCompleted, f.attemptV, reviewV2).Scan(&dispositions); err != nil {
		t.Fatal(err)
	}
	if dispositions != 0 {
		t.Fatalf("disposition completions on the Review attempts %s and %s = %d, want zero — both reviews stay undispositioned", f.attemptV, reviewV2, dispositions)
	}
}

// walledResearchSnapshot captures the pin, step, definition digest, active
// contract, wall counters, and recorded event bytes that the supersession,
// the consumed basis, the refused dispatches, and a projection rebuild must
// preserve.
type walledResearchSnapshot struct {
	counters                    walledResearchCounters
	pin, step, definitionDigest string
	activeVersion, activeCount  int64
	bytes                       map[string][]byte
}

func walledResearchCapture(t *testing.T, f crossAttemptFixture, settled, authorized, rejected []string) walledResearchSnapshot {
	t.Helper()
	walledResearchAssertUndispositioned(t, f)
	snapshot := walledResearchSnapshot{counters: walledResearchReadCounters(t, f), bytes: map[string][]byte{}}
	snapshot.pin = string(mustJSONValue(issue1013Pin(t, f.s, f.workID)))
	snapshot.step = currentStep(t, f.s, f.workID)
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT i.definition_digest,COALESCE(MAX(c.contract_version),0),count(c.contract_version) FROM workflow_instances i LEFT JOIN workflow_contracts c ON c.work_id=i.work_id AND c.superseded_by IS NULL WHERE i.work_id=?`, f.workID).Scan(&snapshot.definitionDigest, &snapshot.activeVersion, &snapshot.activeCount); err != nil {
		t.Fatal(err)
	}
	for _, attemptID := range settled {
		snapshot.bytes["authorized:"+attemptID] = crossAttemptEventPayload(t, f.s, f.workID, WorkflowActionCompleted, attemptID)
		snapshot.bytes["dispatched:"+attemptID] = crossAttemptEventPayload(t, f.s, f.workID, WorkerDispatched, attemptID)
		snapshot.bytes["completed:"+attemptID] = crossAttemptEventPayload(t, f.s, f.workID, WorkerCompleted, attemptID)
	}
	for _, attemptID := range authorized {
		snapshot.bytes["authorized:"+attemptID] = crossAttemptEventPayload(t, f.s, f.workID, WorkflowActionCompleted, attemptID)
		snapshot.bytes["dispatched:"+attemptID] = crossAttemptEventPayload(t, f.s, f.workID, WorkerDispatched, attemptID)
	}
	for _, attemptID := range rejected {
		var payload []byte
		if queryErr := f.s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='reject_worker_result' AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq`, f.workID, WorkflowActionCompleted, attemptID).Scan(&payload); queryErr != nil {
			t.Fatalf("read the rejection completion of %s: %v", attemptID, queryErr)
		}
		snapshot.bytes["rejected:"+attemptID] = payload
	}
	var supersededPayload []byte
	if supersededErr := f.s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, f.workID, WorkflowContractSuperseded).Scan(&supersededPayload); supersededErr == nil {
		snapshot.bytes["superseded"] = supersededPayload
	} else if supersededErr != sql.ErrNoRows {
		t.Fatal(supersededErr)
	}
	return snapshot
}

// walledResearchAssertSame holds the recorded bytes and the definition digest
// fixed, and with full also the pin, step, active contract, and wall counters
// a projection rebuild must reproduce.
func walledResearchAssertSame(t *testing.T, before, after walledResearchSnapshot, full bool) {
	t.Helper()
	for key, want := range before.bytes {
		if got := after.bytes[key]; string(got) != string(want) {
			t.Fatalf("recorded %s bytes moved: before %s, after %s", key, want, got)
		}
	}
	if after.definitionDigest != before.definitionDigest {
		t.Fatalf("definition digest moved: before %s, after %s", before.definitionDigest, after.definitionDigest)
	}
	if !full {
		return
	}
	if after.pin != before.pin || after.step != before.step || after.activeVersion != before.activeVersion || after.activeCount != before.activeCount || after.counters != before.counters {
		t.Fatalf("the rebuild moved durable state: before %+v %+v, after %+v %+v", before.counters, before.pin, after.counters, after.pin)
	}
}

// walledResearchAssertRefused checks launch safety and zero durable effects.
func walledResearchAssertRefused(t *testing.T, f crossAttemptFixture, err error, before walledResearchCounters, stage string) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindOperationConflict {
		t.Fatalf("%s = %v, want the active launch window refusal", stage, err)
	}
	if after := walledResearchReadCounters(t, f); after != before {
		t.Fatalf("%s changed durable effects: before %+v, after %+v", stage, before, after)
	}
}

// The research correction preserves its exact source and recorded history
// through supersession, launch admission, and materialization.
func TestResearchWalledCorrectionRecovery(t *testing.T) {
	t.Parallel()
	const workID = "research-walled-correction-recovery"
	f := seedResearchWalledCorrection(t, workID)
	attemptR2, attemptV2 := "attempt:"+workID+":3", "attempt:"+workID+":4"
	attemptW, refusedResearch, refusedReview := "attempt:"+workID+":5", "attempt:"+workID+":6", "attempt:"+workID+":7"
	settled, rejected := []string{f.attemptR, f.attemptV, attemptR2, attemptV2}, []string{f.attemptR, attemptR2}
	before := walledResearchCapture(t, f, settled, nil, rejected)

	// Supersession moves the active contract to version 2 without changing
	// historical bytes or consuming the standing correction.
	supersedeResearchWalledCorrection(t, f)
	afterSupersede := walledResearchCapture(t, f, settled, nil, rejected)
	walledResearchAssertSame(t, before, afterSupersede, false)
	if issue1013Pin(t, f.s, workID).Correction == nil {
		t.Fatal("the supersession consumed the standing rejection correction")
	}

	// The next corrected research authorization opens a fresh fenced epoch.
	if err := dispatchCorrectionAuthorize(t, f, attemptW, f.researchLane, f.dispatcher); err != nil {
		t.Fatalf("authorize the fresh corrected research dispatch behind the wall: %v", err)
	}
	if epochW := crossAttemptDispatchEpoch(t, f.s, workID, attemptW); epochW != crossAttemptDispatchEpoch(t, f.s, workID, attemptV2)+1 {
		t.Fatalf("the fresh authorization's epoch = %d, want the next epoch after the review", epochW)
	}
	if issue1013Pin(t, f.s, workID).Correction == nil {
		t.Fatal("the authorization alone consumed the standing rejection correction")
	}

	// A pending authorization prevents another launch before materialization.
	refusedCounters := walledResearchReadCounters(t, f)
	walledResearchAssertRefused(t, f, dispatchCorrectionAuthorize(t, f, refusedResearch, f.researchLane, f.dispatcher), refusedCounters, "the pre-materialization research dispatch")

	// Materialization consumes the correction and resolves the launch outcome.
	// A subsequent review can authorize without a retry budget.
	f.materializeDispatch(t, attemptW, f.researchLane, f.dispatcher)
	if issue1013Pin(t, f.s, workID).Correction != nil {
		t.Fatal("the materialized dispatch left the rejection correction standing")
	}
	_, reviewErr := dispatchJoinAttempt(context.Background(), t, f.s, workID, readWorkVersion(t, f.s, workID), f.dispatcher, joinPacketFor(t, f.s, workID, f.step, refusedReview, f.reviewLane.ID, f.reviewLane.Version, f.reviewLane.Digest))
	if reviewErr != nil {
		t.Fatalf("post-materialization review dispatch: %v", reviewErr)
	}
	f.materializeDispatch(t, refusedReview, f.reviewLane, f.dispatcher)
	f.completeReviewNoShip(t, refusedReview, 34)
	settled = append(settled, refusedReview)

	final := walledResearchCapture(t, f, settled, []string{attemptW}, rejected)
	walledResearchAssertSame(t, before, final, false)
	if final.counters.dispatches != 6 || final.counters.authorizations != 6 || final.counters.reports != 5 || final.counters.attempts != 6 || final.counters.verdicts != 0 {
		t.Fatalf("final effect counters = %+v, want the four historical cycles, fresh research and review, and no verdicts", final.counters)
	}
	for _, attemptID := range rejected {
		var completedSeq int64
		if err := f.s.DatabaseForTesting().QueryRow(`SELECT seq FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=?`, workID, WorkerCompleted, attemptID).Scan(&completedSeq); err != nil {
			t.Fatal(err)
		}
		epoch := crossAttemptDispatchEpoch(t, f.s, workID, attemptID)
		if crossAttemptResultsContain(f.rejectableResults(t, completedSeq), attemptID, epoch) {
			t.Fatalf("future completion authorized rejection of %s before its report existed", attemptID)
		}
		if !crossAttemptResultsContain(f.rejectableResults(t, f.rejectionSequence(t, attemptID)), attemptID, epoch) {
			t.Fatalf("later history invalidated the eligible rejection of %s", attemptID)
		}
	}
}
