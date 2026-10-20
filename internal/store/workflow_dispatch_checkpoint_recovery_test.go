package store

// The registry checkpoint predicate and the admission model must admit
// dispatch_worker steps of the cross_authority and internal_sqlite kinds the
// same way external_effect steps are admitted, the exclusion shapes stay
// excluded, and the availability answers at one synthetic research instance
// must agree with the work pin and the supersede_contract resolution on
// every worker-dispatch state.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

const dispatchCorrectionMissingStep = "no-such-dispatch-step"

func TestDispatchCheckpointCorrectionAcrossKinds(t *testing.T) {
	counts := map[WorkflowStepKind]int{}
	for _, definition := range BuiltinWorkflowDefinitionsWithHistory() {
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

// seedDispatchCorrectionFixture parks the eligibility fixture at the
// worker-dispatch step on the case's pinned definition: version 0 selects the
// retained research@19 head, and any other version resolves that registered
// research pin directly.
func seedDispatchCorrectionFixture(t *testing.T, workID string, version int64) crossAttemptFixture {
	t.Helper()
	if version == 0 {
		version = 19
	}
	registered, ok := BuiltinWorkflowRegistry().Lookup("workflow.research", version)
	if !ok {
		t.Fatalf("workflow.research v%d is not registered", version)
	}
	f := crossAttemptFixture{
		fixture:      seedWorkflowReturnRouteFixtureWithDefinition(t, workID, registered, "investigate", []string{}, []string{}),
		workID:       workID,
		step:         "investigate",
		definition:   registered.Definition,
		researchLane: reviewGateLane(t, "research"),
		reviewLane:   reviewGateLane(t, "review"),
		attemptR:     "attempt:" + workID + ":1",
		attemptV:     "attempt:" + workID + ":2",
	}
	f.s = f.fixture.store
	f.dispatcher = f.fixture.owner
	return f
}

func TestDispatchCheckpointCorrectionAvailability(t *testing.T) {
	// The two raw-failure states run on the historical research@16 pin:
	// below the CD-0198 D5 derivation gate the terminal failure lands
	// without a workflow record, so the unrecorded state stays reachable
	// there. Current pins derive the failure record atomically at the
	// authenticated terminal, so a failed attempt is never unrecorded on them.
	cases := []struct {
		name         string
		workID       string
		version      int64
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
			name: "unrecorded failed attempt", workID: "dispatch-correction-failed-attempt", version: 16,
			available: false, model: admissionModelState{contracts: 1, dispatched: true, attempt: "failed"}, modelApplies: true,
			build: func(t *testing.T, f crossAttemptFixture) {
				f.dispatch(t, f.attemptR, f.researchLane, f.dispatcher)
				failAbandonedWorkerAttempt(t, f.s, f.workID, f.attemptR)
			},
		},
		{
			name: "recorded failure", workID: "dispatch-correction-recorded-failure", version: 16,
			available: true, model: admissionModelState{contracts: 1, dispatched: true, attempt: "failure_recorded"}, modelApplies: true,
			build: func(t *testing.T, f crossAttemptFixture) {
				f.dispatch(t, f.attemptR, f.researchLane, f.dispatcher)
				failAbandonedWorkerAttempt(t, f.s, f.workID, f.attemptR)
				applyRecordWorkerFailureForTest(t, f.s, f.workID, f.fixture.owner, f.attemptR,
					crossAttemptDispatchEpoch(t, f.s, f.workID, f.attemptR),
					readWorkVersion(t, f.s, f.workID), "record-failure-"+f.workID)
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
			f := seedDispatchCorrectionFixture(t, tc.workID, tc.version)
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

// seedResearchWalledCorrection builds the synthetic walled history on one
// research@19 instance: R1 research and the V1 no_ship review complete and R1
// is rejected, then the corrected R2 research and the V2 no_ship review
// complete and R2 is rejected. It returns the fixture before supersession
// with the wall armed: four non-progress attempts, an escalated standing
// rejection correction, and both reviews undispositioned.
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
	nonProgress, nonProgressErr := workflowNonProgressAttemptCount(context.Background(), f.s.DatabaseForTesting(), workID, "test")
	if nonProgressErr != nil || nonProgress != 4 {
		t.Fatalf("non-progress attempts after two rejected cycles = %d, %v, want 4", nonProgress, nonProgressErr)
	}
	correction, correctionErr := workflowCorrectionContextForDispatch(context.Background(), f.s.DatabaseForTesting(), workID, f.step, "")
	if correctionErr != nil || correction == nil || correction.Disposition != "rejected" || correction.AttemptCount != 4 || !correction.Escalated {
		t.Fatalf("standing correction after two rejected cycles = %#v, %v, want an escalated rejected correction counting 4 attempts", correction, correctionErr)
	}
	if verdicts := f.eventCount(t, WorkflowVerdictRecorded, ""); verdicts != 0 {
		t.Fatalf("verdicts after two rejected cycles = %d, want no workflow verdicts", verdicts)
	}
	return f
}

// supersedeResearchWalledCorrection runs the genuine changed-requirement
// supersession behind the armed wall: the pin offers supersede_contract, the
// full successor passes the admission preflight at the current version, and
// the existing test operator authority lands contract 2 as the single active
// contract. Research changes no Product truth, so the successor omits the
// architecture binding its seed only carried for truth-changing families.
func supersedeResearchWalledCorrection(t *testing.T, f crossAttemptFixture) {
	t.Helper()
	pin := issue1013Pin(t, f.s, f.workID)
	if !workPinContainsAction(pin.NextValidIntents, "supersede_contract") {
		t.Fatalf("work pin behind the walled correction offers no supersede intent: %#v", pin.NextValidIntents)
	}
	successor := json.RawMessage(`{"contract_version":2,"premise":"close the investigation as a source-only report that records its unknowns","outcome_predicates":[{"predicate_id":"predicate:return-route","ordinal":0,"outcome_kind":"outcome","outcome_payload":{"kind":"outcome","allowed":["report_recorded"]}}],"required_evidence":["artifact"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","predecessor_contract_versions":[1],"supersede_reason":"two rejected research rounds and refusing no_ship reviews disproved the original premise; the requirement changed to a closed source-only report with recorded unknowns","audit_evidence":["evidence:` + f.workID + `-walled-supersede"]}`)
	if err := WorkflowActionPreflightWithRegistry(context.Background(), f.s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{WorkID: f.workID, ExpectedVersion: pin.Version, StepID: f.step, ActionID: "supersede_contract", Payload: successor, Actor: f.fixture.owner}); err != nil {
		t.Fatalf("full supersede successor preflight at the walled step: %v", err)
	}
	if err := runIssue933OperatorAction(t, f.s, f.workID, "supersede_contract", successor, f.fixture.owner, f.fixture.operator); err != nil {
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

// walledResearchAssertUndispositioned holds the two historical no_ship review
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
		t.Fatalf("disposition completions on the historical review attempts %s and %s = %d, want zero — both reviews stay undispositioned", f.attemptV, reviewV2, dispositions)
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
	nonProgress                 int64
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
	var nonProgressErr error
	snapshot.nonProgress, nonProgressErr = workflowNonProgressAttemptCount(context.Background(), f.s.DatabaseForTesting(), f.workID, "test")
	if nonProgressErr != nil {
		t.Fatal(nonProgressErr)
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
	if after.pin != before.pin || after.step != before.step || after.activeVersion != before.activeVersion || after.activeCount != before.activeCount || after.nonProgress != before.nonProgress || after.counters != before.counters {
		t.Fatalf("the rebuild moved durable state: before %+v %+v %+v, after %+v %+v %+v", before.counters, before.pin, before.nonProgress, after.counters, after.pin, after.nonProgress)
	}
}

// walledResearchAssertRefused classifies one refused dispatch as the
// convergence wall's missing-evidence refusal and holds the durable effects.
func walledResearchAssertRefused(t *testing.T, f crossAttemptFixture, err error, before walledResearchCounters, stage string) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindMissingEvidence {
		t.Fatalf("%s = %v, want the convergence wall's missing-evidence refusal", stage, err)
	}
	if !strings.Contains(failure.Detail, "4 non-progress attempts since the applicable nonprogress anchor") || !strings.Contains(failure.Detail, "(step "+f.step+")") {
		t.Fatalf("%s refusal detail = %q, want the counted population at the walled step", stage, failure.Detail)
	}
	if after := walledResearchReadCounters(t, f); after != before {
		t.Fatalf("%s changed durable effects: before %+v, after %+v", stage, before, after)
	}
}

// TestResearchWalledCorrectionRecovery walks the walled research step through
// its genuine recovery: the two rejected cycles arm the wall, the supersession
// buys one fresh corrected research authorization from approach_changed, the
// wall re-arms behind that authorization before and after materialization,
// and a projection rebuild preserves every recorded byte and counter.
func TestResearchWalledCorrectionRecovery(t *testing.T) {
	t.Parallel()
	const workID = "research-walled-correction-recovery"
	f := seedResearchWalledCorrection(t, workID)
	attemptR2, attemptV2 := "attempt:"+workID+":3", "attempt:"+workID+":4"
	attemptW, refusedResearch, refusedReview := "attempt:"+workID+":5", "attempt:"+workID+":6", "attempt:"+workID+":7"
	settled, rejected := []string{f.attemptR, f.attemptV, attemptR2, attemptV2}, []string{f.attemptR, attemptR2}
	before := walledResearchCapture(t, f, settled, nil, rejected)

	// The supersession moves the active contract to version 2 and nothing
	// else: no recorded byte, no wall counter, and no standing correction
	// changes with it.
	supersedeResearchWalledCorrection(t, f)
	afterSupersede := walledResearchCapture(t, f, settled, nil, rejected)
	walledResearchAssertSame(t, before, afterSupersede, false)
	if afterSupersede.nonProgress != 4 {
		t.Fatalf("non-progress attempts after the supersession = %d, want 4", afterSupersede.nonProgress)
	}
	if issue1013Pin(t, f.s, workID).Correction == nil {
		t.Fatal("the supersession consumed the standing rejection correction")
	}
	var supersessionSeq int64
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT seq FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=?`, workID, WorkflowContractSuperseded).Scan(&supersessionSeq); err != nil {
		t.Fatal(err)
	}

	// The changed approach buys exactly one fresh corrected research
	// authorization, and its recorded basis names the actual supersession at
	// the next fenced epoch.
	if err := dispatchCorrectionAuthorize(t, f, attemptW, f.researchLane, f.dispatcher); err != nil {
		t.Fatalf("authorize the fresh corrected research dispatch behind the wall: %v", err)
	}
	if epochW := crossAttemptDispatchEpoch(t, f.s, workID, attemptW); epochW != crossAttemptDispatchEpoch(t, f.s, workID, attemptV2)+1 {
		t.Fatalf("the fresh authorization's epoch = %d, want the next epoch after the review", epochW)
	}
	var rawConvergence []byte
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.retry_convergence') FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, attemptW).Scan(&rawConvergence); err != nil {
		t.Fatal(err)
	}
	var convergence WorkflowRetryConvergence
	if err := json.Unmarshal(rawConvergence, &convergence); err != nil {
		t.Fatal(err)
	}
	if !convergence.valid() || convergence.Basis != "approach_changed" || convergence.SupersededSeq != supersessionSeq {
		t.Fatalf("recorded convergence = %+v, want approach_changed at supersession seq %d", convergence, supersessionSeq)
	}
	windowAttempts, windowErr := workflowCorrectionAttemptCount(context.Background(), f.s.DatabaseForTesting(), workID, int64(1)<<62, "test")
	if windowErr != nil || windowAttempts != 5 {
		t.Fatalf("correction window attempts after the fresh authorization = %d, %v, want the four counted attempts plus the one authorized identity", windowAttempts, windowErr)
	}
	if got, nonProgressErr := workflowNonProgressAttemptCount(context.Background(), f.s.DatabaseForTesting(), workID, "test"); nonProgressErr != nil || got != 4 {
		t.Fatalf("non-progress attempts after the fresh authorization = %d, %v, want 4 — the authorization renews no window", got, nonProgressErr)
	}
	if issue1013Pin(t, f.s, workID).Correction == nil {
		t.Fatal("the authorization alone consumed the standing rejection correction")
	}

	// Half-materialized path: behind the consumed basis the next corrected
	// research dispatch refuses at the wall before any worker evidence
	// exists, and the refusal records nothing.
	refusedCounters := walledResearchReadCounters(t, f)
	walledResearchAssertRefused(t, f, dispatchCorrectionAuthorize(t, f, refusedResearch, f.researchLane, f.dispatcher), refusedCounters, "the pre-materialization research dispatch")

	// Full path: the authorized attempt materializes, consumes the standing
	// correction, and the wall re-arms — the next review dispatch refuses
	// without any effect either.
	f.materializeDispatch(t, attemptW, f.researchLane, f.dispatcher)
	if issue1013Pin(t, f.s, workID).Correction != nil {
		t.Fatal("the materialized dispatch left the rejection correction standing")
	}
	refusedCounters = walledResearchReadCounters(t, f)
	_, reviewErr := dispatchJoinAttempt(context.Background(), t, f.s, workID, readWorkVersion(t, f.s, workID), f.dispatcher, joinPacketFor(t, f.s, workID, f.step, refusedReview, f.reviewLane.ID, f.reviewLane.Version, f.reviewLane.Digest))
	walledResearchAssertRefused(t, f, reviewErr, refusedCounters, "the post-materialization review dispatch")

	final := walledResearchCapture(t, f, settled, []string{attemptW}, rejected)
	walledResearchAssertSame(t, before, final, false)
	if final.counters.dispatches != 5 || final.counters.authorizations != 5 || final.counters.reports != 4 || final.counters.attempts != 5 || final.counters.verdicts != 0 {
		t.Fatalf("final effect counters = %+v, want four settled dispatch cycles plus the one basis-buying attempt and no verdicts", final.counters)
	}
	if err := RebuildFromLog(context.Background(), f.s); err != nil {
		t.Fatalf("rebuild the projections from the log: %v", err)
	}
	walledResearchAssertSame(t, final, walledResearchCapture(t, f, settled, []string{attemptW}, rejected), true)
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

// walledEvaluationCheckpoint is the corrected producer round the checkpoint
// evaluation branches continue from: the accepted corrected research result
// with its non-progress anchor, the registered report evidence, and the
// independent registered reviewer that judged the round.
type walledEvaluationCheckpoint struct {
	f         crossAttemptFixture
	acceptSeq int64
	reportRef string
	reviewRef string
	reviewer  WorkflowActor
	settled   []string
	rejected  []string
}

// seedWalledEvaluationCheckpoint walks the armed walled research step through
// its corrected producer round: the supersession buys one fresh research
// authorization, the truthful corrected result is accepted at investigate,
// its report is registered at findings, and an independent review completes
// at conclude and is accepted as held evidence with the named verdict. Every
// epoch and version comes from the persisted record, never a guessed pin.
func seedWalledEvaluationCheckpoint(t *testing.T, workID, reviewVerdict string) walledEvaluationCheckpoint {
	t.Helper()
	ctx := context.Background()
	f := seedResearchWalledCorrection(t, workID)
	attemptR2, attemptV2 := "attempt:"+workID+":3", "attempt:"+workID+":4"
	attemptR3, attemptV3 := "attempt:"+workID+":5", "attempt:"+workID+":6"
	epochV2 := crossAttemptDispatchEpoch(t, f.s, workID, attemptV2)
	settled, rejected := []string{f.attemptR, f.attemptV, attemptR2, attemptV2}, []string{f.attemptR, attemptR2}
	before := walledResearchCapture(t, f, settled, nil, rejected)
	supersedeResearchWalledCorrection(t, f)
	if anchor, err := workflowNonProgressWindowAnchor(ctx, f.s.DatabaseForTesting(), workID, "test", 0); err != nil || anchor != 0 {
		t.Fatalf("non-progress anchor after the supersession = %d, %v, want 0 — the supersession renews no window", anchor, err)
	}
	if err := dispatchCorrectionAuthorize(t, f, attemptR3, f.researchLane, f.dispatcher); err != nil {
		t.Fatalf("authorize the fresh corrected research dispatch: %v", err)
	}
	if epochR3 := crossAttemptDispatchEpoch(t, f.s, workID, attemptR3); epochR3 != epochV2+1 {
		t.Fatalf("the fresh authorization's epoch = %d, want the next epoch after the review %d", epochR3, epochV2+1)
	}
	f.materializeDispatch(t, attemptR3, f.researchLane, f.dispatcher)
	f.completeReport(t, attemptR3, f.researchLane, 41)
	if _, err := invokeWorkflowActionForCD0059(ctx, t, f.s, f.acceptAttemptRequest(t, readWorkVersion(t, f.s, workID), "accept_worker_result", attemptR3, crossAttemptDispatchEpoch(t, f.s, workID, attemptR3), "walled-eval", 42)); err != nil {
		t.Fatalf("accept the corrected research result: %v", err)
	}
	if step := currentStep(t, f.s, workID); step != "findings" {
		t.Fatalf("current step after the productive acceptance = %q, want findings", step)
	}
	var acceptSeq int64
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT seq FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='accept_worker_result' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, attemptR3).Scan(&acceptSeq); err != nil {
		t.Fatal(err)
	}
	if anchor, err := workflowNonProgressWindowAnchor(ctx, f.s.DatabaseForTesting(), workID, "test", 0); err != nil || anchor != acceptSeq {
		t.Fatalf("non-progress anchor after the acceptance = %d, %v, want the productive accept event seq %d", anchor, err, acceptSeq)
	}
	if nonProgress, err := workflowNonProgressAttemptCount(ctx, f.s.DatabaseForTesting(), workID, "test"); err != nil || nonProgress != 0 {
		t.Fatalf("non-progress attempts after the acceptance = %d, %v, want 0", nonProgress, err)
	}
	walledResearchAssertSame(t, before, walledResearchCapture(t, f, settled, nil, rejected), false)
	reportHash := sha256.Sum256(crossAttemptEventPayload(t, f.s, workID, WorkerCompleted, attemptR3))
	reportRef := fmt.Sprintf("evidence:sha256:%x", reportHash)
	if err := runVerdictActionAs(t, f.s, workID, "record_report", json.RawMessage(`{"evidence_kind":"artifact","immutable_subject_ref":"`+reportRef+`"}`), 0, f.fixture.owner); err != nil {
		t.Fatalf("register the corrected research report at findings: %v", err)
	}
	if step := currentStep(t, f.s, workID); step != "conclude" {
		t.Fatalf("current step after the report = %q, want conclude", step)
	}
	// A productive producer round leaves no standing correction, so the
	// corrected dispatch authorizer's standing-context requirement cannot
	// hold at the conclude checkpoint; the review packet is built here so it
	// declares the active contract's actual outcome predicates the review
	// evidence ties — a packet without the typed field refuses the tie as
	// undeclared — and materializeDispatch records the lane actor.
	// The epoch is step-local: the allocator opens it from the dispatched
	// step's own start history (workflowActionStartEpochForDispatch), so the
	// conclude review opens conclude's first epoch.
	w := f
	w.step = "conclude"
	_, lastConcludeEpoch, _, startErr := latestWorkflowActionStartAt(ctx, f.s.DatabaseForTesting(), workID, "conclude", 0)
	if startErr != nil {
		t.Fatal(startErr)
	}
	projection, projectionErr := ReadWorkflowProjection(ctx, f.s, WorkflowReadRequest{WorkID: workID})
	if projectionErr != nil {
		t.Fatal(projectionErr)
	}
	if projection.Contract == nil || len(projection.Contract.OutcomePredicates) == 0 {
		t.Fatalf("active contract declares no outcome predicates the review dispatch could declare: %+v", projection.Contract)
	}
	predicates := make([]map[string]any, 0, len(projection.Contract.OutcomePredicates))
	for _, predicate := range projection.Contract.OutcomePredicates {
		predicates = append(predicates, map[string]any{
			"predicate_id":    predicate.PredicateID,
			"ordinal":         predicate.Ordinal,
			"outcome_kind":    predicate.OutcomeKind,
			"outcome_payload": json.RawMessage(predicate.OutcomePayload),
		})
	}
	reviewPacket := joinPacketFor(t, f.s, workID, w.step, attemptV3, f.reviewLane.ID, f.reviewLane.Version, f.reviewLane.Digest)
	reviewPacket["inputs"].(map[string]any)["outcome_predicates"] = predicates
	if _, dispatchErr := dispatchJoinAttempt(ctx, t, f.s, workID, readWorkVersion(t, f.s, workID), f.dispatcher, reviewPacket); dispatchErr != nil {
		t.Fatalf("authorize the conclude review dispatch: %v", dispatchErr)
	}
	w.materializeDispatch(t, attemptV3, f.reviewLane, f.dispatcher)
	if epochV3 := crossAttemptDispatchEpoch(t, f.s, workID, attemptV3); epochV3 != lastConcludeEpoch+1 {
		t.Fatalf("the conclude review's dispatch epoch = %d, want the next epoch conclude's own start history opens (%d)", epochV3, lastConcludeEpoch+1)
	}
	reviewEvent := reviewLaneVerdictCompleteEvent(workID, "walled-eval-review-completed-"+attemptV3, attemptV3, f.reviewLane, reviewVerdict, time.Unix(43, 0).UTC(), workerFixtureDirectory(t, f.s, workID))
	var reviewPayload WorkerCompletedPayload
	if err := json.Unmarshal(reviewEvent.Payload, &reviewPayload); err != nil {
		t.Fatal(err)
	}
	reviewPayload.EvidenceOrigin = WorkerEvidenceReported
	reviewPayload.Evidence = reportedLaneEvidenceForTest(f.reviewLane, reviewPayload.Review)
	for i := range reviewPayload.Evidence {
		reviewPayload.Evidence[i].Detail = "reviewed " + reportRef + " against active predicate:return-route (contract 2)"
		reviewPayload.Evidence[i].PredicateIDs = []string{"predicate:return-route"}
	}
	if reviewVerdict == "ship" {
		reviewPayload.Review.Findings = []WorkerReviewFinding{}
	}
	reviewEvent.Payload = mustJSONValue(reviewPayload)
	if err := ApplyOperation(ctx, f.s, Operation{Events: []Event{reviewEvent}}); err != nil {
		t.Fatalf("record the %s review report: %v", reviewVerdict, err)
	}
	var recordedReview WorkerCompletedPayload
	if err := json.Unmarshal(crossAttemptEventPayload(t, f.s, workID, WorkerCompleted, attemptV3), &recordedReview); err != nil {
		t.Fatal(err)
	}
	matched := false
	for _, evidence := range recordedReview.Evidence {
		if evidence.Detail == "reviewed "+reportRef+" against active predicate:return-route (contract 2)" && slices.Contains(evidence.PredicateIDs, "predicate:return-route") {
			matched = true
		}
	}
	if !matched {
		t.Fatal("checkpoint review does not cite the exact current report and active predicate")
	}
	if _, err := invokeWorkflowActionForCD0059(ctx, t, f.s, f.acceptAttemptRequest(t, readWorkVersion(t, f.s, workID), "accept_worker_evidence", attemptV3, crossAttemptDispatchEpoch(t, f.s, workID, attemptV3), "walled-review", 44)); err != nil {
		t.Fatalf("accept the completed review as held evidence: %v", err)
	}
	if step := currentStep(t, f.s, workID); step != "conclude" {
		t.Fatalf("current step after the review evidence acceptance = %q, want conclude held", step)
	}
	var bound int64
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.evidence_kind')='review' AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, attemptV3).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound != 1 {
		t.Fatalf("review evidence bindings for %s = %d, want the acceptance's one bound review report", attemptV3, bound)
	}
	if anchor, err := workflowNonProgressWindowAnchor(ctx, f.s.DatabaseForTesting(), workID, "test", 0); err != nil || anchor != acceptSeq {
		t.Fatalf("non-progress anchor after the review acceptance = %d, %v, want the producer accept %d — no delivery or producer acceptance substitution", anchor, err, acceptSeq)
	}
	return walledEvaluationCheckpoint{f: f, acceptSeq: acceptSeq, reportRef: reportRef, reviewRef: attemptV3, reviewer: checkpointReviewReviewer(t, f.s, workID), settled: append(settled, attemptR3, attemptV3), rejected: rejected}
}

// walledCheckpointVerificationRequests reads the verification window's actual
// request population the way workflowCorrectionContextForDispatch derives it:
// request_correction completions after the active contract's comparable-healthy
// baseline through the current prefix. The raw lifetime count is not the
// window — the baseline owner decides which requests a packet's count carries.
func walledCheckpointVerificationRequests(t *testing.T, f crossAttemptFixture) int64 {
	t.Helper()
	ctx := context.Background()
	var prefix int64
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type='work_item' AND subject_id=?`, f.workID).Scan(&prefix); err != nil {
		t.Fatal(err)
	}
	lastHealthySeq, err := workflowCorrectionActiveHealthyBaseline(ctx, f.s.DatabaseForTesting(), f.workID, prefix, "test")
	if err != nil {
		t.Fatal(err)
	}
	var requests int64
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='request_correction' AND seq>? AND seq<=?`, string(SubjectWorkItem), f.workID, WorkflowActionCompleted, lastHealthySeq, prefix).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	return requests
}

// TestResearchWalledRecoveryCheckpointEvaluation pins the evaluation sequence
// past the wall: the truthful corrected producer result
// is accepted at investigate, its report is registered at findings, an
// independent review is accepted as held evidence at conclude, and the
// predicate verdict then routes either the healthy premise completion or the
// unhealthy evidence-bearing correction back to investigate without
// re-dispositioning the accepted result or minting a worker.
func TestResearchWalledRecoveryCheckpointEvaluation(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		t.Parallel()
		const workID = "research-walled-evaluation-healthy"
		c := seedWalledEvaluationCheckpoint(t, workID, "ship")
		f := c.f
		if err := runVerdictActionAs(t, f.s, workID, "record_verdict", json.RawMessage(`{"contract_version":2,"verdicts":[{"predicate_id":"predicate:return-route","verdict_kind":"ok","evaluation_evidence":["`+c.reportRef+`","`+c.reviewRef+`"]}]}`), 0, c.reviewer); err != nil {
			t.Fatalf("record the healthy predicate verdict: %v", err)
		}
		attemptsBefore, dispatchesBefore := workerAttemptPopulation(t, f.s, workID)
		if err := runIssue933OperatorAction(t, f.s, workID, "confirm_premise", json.RawMessage(`{"contract_version":2}`), f.fixture.owner, f.fixture.operator); err != nil {
			t.Fatalf("confirm the corrected premise: %v", err)
		}
		if step := currentStep(t, f.s, workID); step != "complete" {
			t.Fatalf("current step after the premise confirmation = %q, want complete", step)
		}
		if err := runIssue933OperatorAction(t, f.s, workID, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`), f.fixture.owner, f.fixture.operator); err != nil {
			t.Fatalf("complete the healthy research item: %v", err)
		}
		var state string
		if err := f.s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != "completed" {
			t.Fatalf("instance state after the healthy completion = %q, want completed", state)
		}
		if attempts, dispatches := workerAttemptPopulation(t, f.s, workID); attempts != attemptsBefore || dispatches != dispatchesBefore {
			t.Fatalf("the healthy completion minted workers: attempts %d->%d, dispatches %d->%d", attemptsBefore, attempts, dispatchesBefore, dispatches)
		}
		final := walledResearchCapture(t, f, c.settled, nil, c.rejected)
		if err := RebuildFromLog(context.Background(), f.s); err != nil {
			t.Fatalf("rebuild the projections from the log: %v", err)
		}
		walledResearchAssertSame(t, final, walledResearchCapture(t, f, c.settled, nil, c.rejected), true)
	})
	t.Run("unhealthy", func(t *testing.T) {
		t.Parallel()
		const workID = "research-walled-evaluation-unhealthy"
		c := seedWalledEvaluationCheckpoint(t, workID, "no_ship")
		f := c.f
		if err := runVerdictActionAs(t, f.s, workID, "record_verdict", json.RawMessage(`{"contract_version":2,"verdicts":[{"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["`+c.reportRef+`","`+c.reviewRef+`"],"incomparable_with_approved":true}]}`), 0, c.reviewer); err != nil {
			t.Fatalf("record the mismatch predicate verdict: %v", err)
		}
		requestsBefore := checkpointCorrectionRequestOpen(t, f.s, workID)
		attemptsBefore, dispatchesBefore := workerAttemptPopulation(t, f.s, workID)
		correction := json.RawMessage(`{"diagnosis":"the corrected research report still does not satisfy the approved outcome","strategy":"return to investigate and re-produce the corrected report","predicate_ids":["predicate:return-route"],"evidence_refs":["` + c.reportRef + `"]}`)
		if err := runIssue933OperatorAction(t, f.s, workID, "request_correction", correction, f.fixture.owner, f.fixture.operator); err != nil {
			t.Fatalf("request correction behind the mismatch verdict: %v", err)
		}
		if step := currentStep(t, f.s, workID); step != "investigate" {
			t.Fatalf("current step after the correction request = %q, want investigate", step)
		}
		if got := checkpointCorrectionRequestOpen(t, f.s, workID); got != requestsBefore+1 {
			t.Fatalf("recorded correction requests = %d, want exactly the one new request (before %d)", got, requestsBefore)
		}
		returned := issue1013Pin(t, f.s, workID).Correction
		if returned == nil || returned.Disposition != "verification" || returned.AttemptCount != requestsBefore+1 {
			t.Fatalf("verification correction after step re-entry = %#v, want %d recorded requests", returned, requestsBefore+1)
		}
		// Neither the supersession nor the corrective step re-entry renewed the
		// window: the anchor stays at the productive acceptance, and the only
		// counted attempt since it is the refusing review itself.
		if anchor, err := workflowNonProgressWindowAnchor(context.Background(), f.s.DatabaseForTesting(), workID, "test", 0); err != nil || anchor != c.acceptSeq {
			t.Fatalf("non-progress anchor after the request = %d, %v, want the producer accept %d", anchor, err, c.acceptSeq)
		}
		if nonProgress, err := workflowNonProgressAttemptCount(context.Background(), f.s.DatabaseForTesting(), workID, "test"); err != nil || nonProgress != 1 {
			t.Fatalf("non-progress attempts after the request = %d, %v, want only the refusing review", nonProgress, err)
		}
		if attempts, dispatches := workerAttemptPopulation(t, f.s, workID); attempts != attemptsBefore || dispatches != dispatchesBefore {
			t.Fatalf("request_correction minted attempts: attempts %d->%d, dispatches %d->%d", attemptsBefore, attempts, dispatchesBefore, dispatches)
		}
		if results := f.rejectableResults(t, 0); len(results) != 0 {
			t.Fatalf("rejectable results after the return = %#v, want none — the accepted result is not reopened for rejection", results)
		}
		if rejections := f.eventCount(t, WorkflowActionCompleted, "reject_worker_result"); rejections != 2 {
			t.Fatalf("rejection completions after the return = %d, want only the two walled ones", rejections)
		}
		// The verification window the request opened is the baseline-owned
		// population, and it matches the pin's derived count.
		if requests := walledCheckpointVerificationRequests(t, f); requests != requestsBefore+1 {
			t.Fatalf("verification window request population at the request = %d, want the baseline-owned %d", requests, requestsBefore+1)
		}
		// One real synthetic failed cycle on the returned investigate step: the
		// corrected R4 research dispatch consumes the standing verification
		// context, the V4 review refuses no_ship, and the exact R4 result is
		// rejected. The latest correction is then an ordinary rejected-result
		// correction — the state a supersession is admitted on.
		attemptR4, attemptV4 := "attempt:"+workID+":7", "attempt:"+workID+":8"
		if err := dispatchCorrectionAuthorize(t, f, attemptR4, f.researchLane, f.dispatcher); err != nil {
			t.Fatalf("authorize the corrected research cycle on the returned step: %v", err)
		}
		f.materializeDispatch(t, attemptR4, f.researchLane, f.dispatcher)
		f.completeReport(t, attemptR4, f.researchLane, 45)
		f.dispatch(t, attemptV4, f.reviewLane, f.dispatcher)
		f.completeReviewNoShip(t, attemptV4, 46)
		if _, err := f.invokeReject(t, readWorkVersion(t, f.s, workID), attemptR4, crossAttemptDispatchEpoch(t, f.s, workID, attemptR4), "r4", 47); err != nil {
			t.Fatalf("reject the second corrected research result: %v", err)
		}
		// The old no_ship checkpoint review counts one, and the rejected R4 and
		// its no_ship V4 review count two more: three non-progress attempts at
		// the walled step.
		if nonProgress, err := workflowNonProgressAttemptCount(context.Background(), f.s.DatabaseForTesting(), workID, "test"); err != nil || nonProgress != 3 {
			t.Fatalf("non-progress attempts after the rejected cycle = %d, %v, want the checkpoint review, the rejected research, and the no_ship review", nonProgress, err)
		}
		if requests := walledCheckpointVerificationRequests(t, f); requests != requestsBefore+1 {
			t.Fatalf("verification window request population after the rejected cycle = %d, want the unchanged %d", requests, requestsBefore+1)
		}
		beforeSupersede := walledResearchReadCounters(t, f)
		settled, rejected := c.settled, c.rejected
		settled = append(settled, attemptR4, attemptV4)
		rejected = append(rejected, attemptR4)
		successor := json.RawMessage(`{"contract_version":3,"premise":"close a source-only report that enumerates its sources and unknowns","outcome_predicates":[{"predicate_id":"predicate:return-route","ordinal":0,"outcome_kind":"outcome","outcome_payload":{"kind":"outcome","allowed":["report_recorded"]}}],"required_evidence":["artifact"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","predecessor_contract_versions":[2],"supersede_reason":"the mismatch revealed that the requirement omitted an explicit source inventory","audit_evidence":["` + c.reportRef + `"]}`)
		if err := runIssue933OperatorAction(t, f.s, workID, "supersede_contract", successor, f.fixture.owner, f.fixture.operator); err != nil {
			t.Fatalf("supersede the walled step at its genuinely rejected correction: %v", err)
		}
		var activeCount, activeVersion int64
		if err := f.s.DatabaseForTesting().QueryRow(`SELECT count(*), COALESCE(MAX(contract_version),0) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeCount, &activeVersion); err != nil || activeCount != 1 || activeVersion != 3 {
			t.Fatalf("active contracts after the supersession = %d at v%d, %v, want exactly contract 3", activeCount, activeVersion, err)
		}
		after := walledResearchReadCounters(t, f)
		if after.version != beforeSupersede.version+1 {
			t.Fatalf("work version after the supersession = %d, want exactly %d+1 — the supersession's one operator action moves only the version", after.version, beforeSupersede.version)
		}
		expected := beforeSupersede
		expected.version = after.version
		if after != expected {
			t.Fatalf("the supersession changed durable effects beyond the one version: before %+v, after %+v", beforeSupersede, after)
		}
		if nonProgress, err := workflowNonProgressAttemptCount(context.Background(), f.s.DatabaseForTesting(), workID, "test"); err != nil || nonProgress != 3 {
			t.Fatalf("non-progress attempts after the supersession = %d, %v, want the unchanged 3", nonProgress, err)
		}
		if requests := walledCheckpointVerificationRequests(t, f); requests != requestsBefore+1 {
			t.Fatalf("verification window request population after the supersession = %d, want the unchanged %d", requests, requestsBefore+1)
		}
		final := walledResearchCapture(t, f, settled, nil, rejected)
		if err := RebuildFromLog(context.Background(), f.s); err != nil {
			t.Fatalf("rebuild the projections from the log: %v", err)
		}
		walledResearchAssertSame(t, final, walledResearchCapture(t, f, settled, nil, rejected), true)
	})
}
