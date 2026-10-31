package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestWorkflowCorrectionAdmissionRejectsNBSPReference(t *testing.T) {
	t.Parallel()
	const legacyReference = "evidence:\u00a0legacy"
	if fault := workflowCorrectionSchemaValuesFaultForAdmission(nil, []string{legacyReference}, nil); fault == "" {
		t.Fatal("new correction admission accepted a non-breaking-space reference")
	}
}

// issue1013SuccessorContract is a complete recovery payload: the correction
// replaces the wrong subject the pinned contract named with the delivered one.
func issue1013SuccessorContract() json.RawMessage {
	return json.RawMessage(`{"contract_version":2,"premise":"corrected predicate subject","outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:e3d7c6e6","expected_result":"pass"}}],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","supersede_reason":"the pinned subject named an unrelated commit","audit_evidence":["evidence:issue1013"]}`)
}

func issue1013Preflight(t *testing.T, s *Store, workID, action string, payload json.RawMessage, actor WorkflowActor) error {
	t.Helper()
	return WorkflowActionPreflightWithRegistry(context.Background(), s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
		WorkID: workID, ActionID: action, Payload: payload, Actor: actor,
	})
}

// Late criterion association agrees across discovery, the work pin, preflight
// and mutation. It closes when every criterion has applicable proof.
func TestIssue1013LateVerdictRecoveryPassesReadOnlyPreflight(t *testing.T) {
	t.Parallel()
	const workID = "issue1013-late-verdict-preflight"
	fixture, ref := seedLateCriterionAssociation(t, workID)
	s, owner := fixture.store, fixture.owner
	for _, predicate := range []string{"predicate:first", "predicate:second"} {
		payload := criterionAssociationPayload(ref, predicate)
		if !issue1013HasIntent(issue1013Pin(t, s, workID), "record_verdict") {
			t.Fatal("missing criterion proof has no late-association intent")
		}
		_, action, err := WorkflowActionDefinitionFor(t.Context(), s, BuiltinWorkflowRegistry(), workID, "record_verdict")
		if err != nil || action.ExecutionMode != ActionHold {
			t.Fatalf("late association discovery=%+v err=%v", action, err)
		}
		before := contractCorrectionEffectSnapshot(t, s, workID)
		if err := issue1013Preflight(t, s, workID, "record_verdict", payload, owner); err != nil {
			t.Fatalf("late association preflight: %v", err)
		}
		if after := contractCorrectionEffectSnapshot(t, s, workID); after != before {
			t.Fatal("read-only late association preflight changed state")
		}
		if err := runVerdictActionAs(t, s, workID, "record_verdict", payload, 0, owner); err != nil {
			t.Fatalf("late association: %v", err)
		}
		if got := currentStep(t, s, workID); got != "complete" {
			t.Fatalf("late association moved to %s", got)
		}
	}
	payload := criterionAssociationPayload(ref, "predicate:first")
	before := contractCorrectionEffectSnapshot(t, s, workID)
	if issue1013HasIntent(issue1013Pin(t, s, workID), "record_verdict") {
		t.Fatal("all criteria proved but the late route remains advertised")
	}
	for _, err := range []error{issue1013Preflight(t, s, workID, "record_verdict", payload, owner), runVerdictActionAs(t, s, workID, "record_verdict", payload, 0, owner)} {
		if !hasFailureKind(err, KindIllegalLifecycleTransition) {
			t.Fatalf("closed late route=%v, want current-step refusal", err)
		}
	}
	if after := contractCorrectionEffectSnapshot(t, s, workID); after != before {
		t.Fatal("closed-route refusals changed durable state")
	}
	if err := issue1013Preflight(t, s, workID, "supersede_contract", issue1013SuccessorContract(), owner); !hasFailureKind(err, KindInvalidOperation) {
		t.Fatalf("unrelated contract recovery became available: %v", err)
	}
}

func TestRequestCorrectionRecordsSourceContext(t *testing.T) {
	const workID = "request-correction-source"
	j := pReturnReviewFixture(t, workID, "workflow.implementation", "correction_required")
	_, action, err := WorkflowActionDefinitionFor(t.Context(), j.s, BuiltinWorkflowRegistry(), workID, "request_correction")
	if err != nil || action.ID != "request_correction" {
		t.Fatalf("resolve corrective return: %+v, %v", action, err)
	}
	payload := pReviewCorrectionPayload(t)
	if err := j.action(t, "request_correction", payload, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	pin := issue1013Pin(t, j.s, workID)
	if pin.Correction == nil {
		t.Fatal("corrective return omitted its source context")
	}
	want := WorkflowCorrectionContext{Disposition: "verification", Diagnosis: "the accepted current Review requires correction", Strategy: "perform the declared corrective work and obtain applicable Review", PredicateIDs: []string{"predicate:return-route"}, EvidenceRefs: []string{"evidence:return-route-verification"}, SourceEventID: pin.Correction.SourceEventID, SourceEventSeq: pin.Correction.SourceEventSeq}
	if want.SourceEventID == "" || want.SourceEventSeq < 1 || !reflect.DeepEqual(*pin.Correction, want) {
		t.Fatalf("correction context=%+v, want %+v with exact durable source", pin.Correction, want)
	}
	var sourceID, sourceDigest string
	if err := j.s.db.QueryRow(`SELECT event_id,payload_digest FROM effect_receipts WHERE subject_id=? AND effect_seq=? AND kind=?`, workID, want.SourceEventSeq, WorkflowActionCompleted).Scan(&sourceID, &sourceDigest); err != nil || sourceID != want.SourceEventID || sourceDigest == "" {
		t.Fatalf("correction source=%q digest=%q err=%v", sourceID, sourceDigest, err)
	}
	if err := payloadschema.Validate("work_pin", mustJSONValue(pin)); err != nil {
		t.Fatalf("corrective work pin violates the response schema: %v", err)
	}
	before := j.effects(t)
	if err := j.action(t, "request_correction", payload, j.fixture.owner); err == nil {
		t.Fatal("a consumed Review admitted another correction")
	}
	if j.effects(t) != before || !reflect.DeepEqual(issue1013Pin(t, j.s, workID).Correction, &want) {
		t.Fatal("refused repeated request changed effects or its exact source")
	}
}

func TestCorrectiveReviewClearsPinContext(t *testing.T) {
	const workID = "work-c1b9adae6631673333fc94b8"
	j := pReturnReviewFixture(t, workID, "workflow.break_fix", "correction_required")
	s, owner := j.s, j.fixture.owner
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), owner); err != nil {
		t.Fatalf("request correction: %v", err)
	}

	pin := issue1013Pin(t, s, workID)
	if pin.Correction == nil {
		t.Fatal("work pin omitted the undischarged correction")
	}
	validatorCorrection, err := readWorkflowCorrectionContext(t.Context(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pin.Correction, validatorCorrection) {
		t.Fatalf("work pin correction=%#v, validator correction=%#v", pin.Correction, validatorCorrection)
	}
	before := j.effects(t)
	if err := j.action(t, "record_delivery", map[string]any{"delivery_artifact": "artifact:uncorrected", "delivery_state": "asserted"}, owner); err == nil {
		t.Fatal("delivery without fresh Review discharged correction")
	}
	if j.effects(t) != before {
		t.Fatal("refused delivery changed durable state")
	}

	pReturnProduce(t, j, "corrective")
	if got := issue1013Pin(t, s, workID).Correction; !reflect.DeepEqual(got, validatorCorrection) {
		t.Fatalf("subject observation alone consumed correction: %+v", got)
	}
	acceptCurrentReviewForTest(t, j.fixture, "ready_for_final_proof")
	pin = issue1013Pin(t, s, workID)
	if pin.Correction != nil {
		t.Fatalf("work pin retained discharged correction: %#v", pin.Correction)
	}
	if correction, err := readWorkflowCorrectionContext(t.Context(), s.db, workID); err != nil || correction != nil {
		t.Fatalf("later correction=%#v err=%v, want none", correction, err)
	}
}

// Every correction consumes the accepted Review that admitted it. Each later
// correction needs a fresh corrective delivery and a fresh accepted
// correction_required Review; a consumed Review never admits another.
func TestRepeatedVerificationCorrectionRequiresFreshVerdict(t *testing.T) {
	t.Parallel()
	const workID = "verification-wall-fourth-request"
	j := pReturnReviewFixture(t, workID, "workflow.implementation", "correction_required")
	for cycle := 1; cycle <= 4; cycle++ {
		if cycle > 1 {
			pReturnProduce(t, j, fmt.Sprintf("correction-%d", cycle))
			acceptCurrentReviewForTest(t, j.fixture, "correction_required")
		}
		if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
			t.Fatalf("cycle %d correction request: %v", cycle, err)
		}
		reviewGateRequireStep(t, j.s, workID, "execution")
		pin := issue1013Pin(t, j.s, workID)
		if pin.Correction == nil || len(pin.Correction.PredicateIDs) != 1 || pin.Correction.PredicateIDs[0] != "predicate:return-route" {
			t.Fatalf("correction after cycle %d = %#v, want the current predicate source", cycle, pin.Correction)
		}
	}
	before := j.effects(t)
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err == nil {
		t.Fatal("request_correction reused a consumed Review")
	}
	if j.effects(t) != before {
		t.Fatal("refused correction changed durable state")
	}
}

func issue1013StartRepair(t *testing.T, s *Store, workID string, owner WorkflowActor, expectedVersion, attemptEpoch int64) {
	t.Helper()
	// CD-0198 D3: on a derived-start pin the callable start is gone, and the
	// following dispatch opens the fresh epoch itself, so no raw start is
	// crafted here.
	entry, err := VerifyWorkflowInstanceDefinition(context.Background(), s, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if workflowDerivedStepStartActive(entry.Definition) {
		return
	}
	actorRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	start := workflowEventWithActor("issue1013-start-"+workID+"-"+fmt.Sprint(attemptEpoch), WorkflowActionStarted, workID, actorRef, map[string]any{
		"work_id": workID, "expected_version": expectedVersion, "resulting_version": expectedVersion + 1, "step_id": "repair",
		"action_id": "start_repair", "attempt_epoch": attemptEpoch, "accepted_inputs_digest": "sha256:" + strings.Repeat("b", 64),
		"idempotency_identity": "issue1013-start:" + workID + ":" + fmt.Sprint(attemptEpoch), "actor_ref": actorRef,
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{start}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expectedVersion}}); err != nil {
		t.Fatalf("start correction attempt %d: %v", attemptEpoch, err)
	}
}

func issue1013Pin(t *testing.T, s *Store, workID string) WorkPin {
	t.Helper()
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatalf("read work pin: %v", err)
	}
	return pin
}

// completeStepSuccessorPayload is a typed successor whose outcome predicates
// repeat the predecessor's byte for byte. The complete-step cut must not
// depend on a predicate payload change: the route exists because durable
// evidence disproved the premise, not because the predicate shape moved. The
// required evidence names every kind the predecessor bound, so the successor
// refuses completion until each kind is freshly bound after the supersession.
func completeStepSuccessorPayload(workID string) json.RawMessage {
	binding := `{"home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}`
	return json.RawMessage(`{"contract_version":2,"premise":"the delivered subject the durable evidence names","outcome_predicates":[{"predicate_id":"predicate:return-route","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:return-route","immutable_subject_ref":"commit:` + workID + `","expected_result":"pass"}}],"required_evidence":["verification","review","artifact"],"route_conventions":["complete_step_correction"],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","decision_domains":[],"supersede_reason":"durable evidence contradicted the approved premise","audit_evidence":["evidence:complete-correction-` + workID + `"],"architecture_binding":` + binding + `}`)
}

// seedCompleteStepCorrection drives a real pinned workflow instance to its
// completion step under an approved contract with a recorded healthy verdict,
// then records one same-work observation through the public generic operation
// route. The observation postdates the verdict, so the complete-step
// correction route's contradiction-record condition holds.
func seedCompleteStepCorrection(t *testing.T, workID, definitionRef, verdictStep, completeStep string) (workflowReturnRouteFixture, WorkflowActor) {
	t.Helper()
	fixture, reviewer := seedCompleteStep(t, workID, definitionRef, verdictStep, completeStep)
	recordCompleteStepContradictionObservation(t, fixture.store, workID, fixture.owner)
	return fixture, reviewer
}

// seedCompleteStep leaves the contradiction record unrecorded, so refusal
// fixtures can supply exactly the conditions under test.
func seedCompleteStep(t *testing.T, workID, definitionRef, verdictStep, completeStep string) (workflowReturnRouteFixture, WorkflowActor) {
	t.Helper()
	effectStep := workflowUnhealthyVerdictRouteTarget(mustBuiltinDefinition(t, definitionRef).Definition, verdictStep)
	fixture := seedWorkflowReturnRouteFixture(t, workID, definitionRef, effectStep)
	if stepDeclaresAction(mustBuiltinDefinition(t, definitionRef).Definition, effectStep, "checkpoint_repair") {
		if err := runVerdictActionAs(t, fixture.store, workID, "checkpoint_repair", json.RawMessage(`{}`), 0, fixture.owner); err != nil {
			t.Fatalf("start the repair pass: %v", err)
		}
	}
	reviewer := completeCompleteStepReview(t, fixture, workID, verdictStep, completeStep)
	return fixture, reviewer
}

// completeCompleteStepReview records the reviewer actor, the current proof
// association under the active contract, and the premise confirmation that
// parks the instance on the pinned completion step, and returns the reviewer
// for later verdict actions.
func completeCompleteStepReview(t *testing.T, fixture workflowReturnRouteFixture, workID, verdictStep, completeStep string) WorkflowActor {
	t.Helper()
	s := fixture.store
	reviewer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/reviewer", SessionRef: "session/" + workID + "-reviewer", ActorClass: ActorAgent}
	reviewerRef, err := WorkflowActorRef(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	reviewerEvent := workflowEventWithActor("complete-correction-reviewer-"+workID, WorkflowActorRecorded, workID, reviewerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"actor_ref": reviewerRef, "principal_ref": reviewer.PrincipalRef, "client_ref": reviewer.ClientRef,
		"agent_ref": reviewer.AgentRef, "session_ref": reviewer.SessionRef, "actor_class": "agent",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{reviewerEvent}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("record the reviewer actor: %v", err)
	}
	wf := workContextFixture{store: s, workID: workID, owner: fixture.owner, operator: fixture.operator}
	qAssociateCurrentProofAt(t, wf, verdictStep, "", true)
	if err := pConfirmReceiptPremise(t, wf); err != nil {
		t.Fatalf("confirm the premise: %v", err)
	}
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != completeStep {
		t.Fatalf("step before correction = %q, want %q", step, completeStep)
	}
	return reviewer
}

// recordCompleteStepContradictionObservation records the contradiction
// observation on the stranded work item through the public generic operation
// route, the route concord_work_define.observation_record drives. The pinned
// completion step declares no evidence-binding action, so workflow-scoped
// event families are unreachable there and the observation is the durable
// contradiction record a public route can produce.
func recordCompleteStepContradictionObservation(t *testing.T, s *Store, workID string, owner WorkflowActor) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"observation_id": "obs:" + strings.Repeat("c", 16),
		"statement":      "Durable evidence contradicts the approved contract premise at the completion step.",
		"refs":           []string{"work:" + workID},
		"tags":           []string{"correction"},
	})
	if err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: "complete-correction-observation-" + workID, Kind: WorkObservationRecorded, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: owner.PrincipalRef, OccurredAt: time.Unix(50, 0).UTC(), PayloadVersion: 1, Payload: payload}
	version := readWorkVersion(t, s, workID)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("record the contradiction observation: %v", err)
	}
}

func completeStepInstancePin(t *testing.T, s *Store, workID string) (step, state, definitionRef, definitionDigest string) {
	t.Helper()
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step,instance_state,definition_ref,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&step, &state, &definitionRef, &definitionDigest); err != nil {
		t.Fatal(err)
	}
	return step, state, definitionRef, definitionDigest
}

// The complete-step route admits operator-approved contract correction when
// the pinned completion step holds a nonterminal break-fix item with one
// active contract, no unsettled attempt, and a durable post-verdict
// contradiction observation. The supersession preserves predecessor
// contracts, verdicts, attempt history, and the pinned definition, returns
// the instance to repair, and cuts the historical verdicts off the
// successor's predicates, so fresh delivery and a fresh verdict are the only
// path back to completion (CD-0166).
func TestCompleteStepContractCorrectionReturnsBreakFixToRepair(t *testing.T) {
	t.Parallel()
	const workID = "complete-correction-breakfix"
	ctx := context.Background()
	fixture, reviewer := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
	s := fixture.store

	_, action, err := WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "supersede_contract")
	if err != nil {
		t.Fatalf("discover contract correction at the complete step: %v", err)
	}
	if action.ID != "supersede_contract" {
		t.Fatalf("correction action = %#v, want supersede_contract", action)
	}
	if err := issue1013Preflight(t, s, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner); err != nil {
		t.Fatalf("read-only preflight refused the complete-step correction: %v", err)
	}
	pin := issue1013Pin(t, s, workID)
	if !issue1013HasIntent(pin, "supersede_contract") {
		t.Fatalf("work pin omitted the correction intent at the complete step: %#v", pin.NextValidIntents)
	}
	if marshaled, marshalErr := json.Marshal(pin); marshalErr != nil {
		t.Fatal(marshalErr)
	} else if validErr := payloadschema.Validate("work_pin", marshaled); validErr != nil {
		t.Fatalf("complete-step correction pin does not satisfy the closed response schema: %v", validErr)
	}

	var verdicts, predecessorPredicates, starts int
	if err := s.DatabaseForTesting().QueryRow(`SELECT (SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?), (SELECT count(*) FROM workflow_contract_predicates WHERE work_id=? AND contract_version=1), (SELECT count(*) FROM workflow_step_epochs WHERE work_id=?)`, workID, WorkflowVerdictRecorded, workID, workID).Scan(&verdicts, &predecessorPredicates, &starts); err != nil {
		t.Fatal(err)
	}
	_, _, definitionRef, definitionDigest := completeStepInstancePin(t, s, workID)

	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner); err != nil {
		t.Fatalf("supersede the contract at the complete step: %v", err)
	}

	var activeCount, activeVersion, supersededBy int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT superseded_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&supersededBy); err != nil {
		t.Fatal(err)
	}
	if activeCount != 1 || activeVersion != 2 || supersededBy != 2 {
		t.Fatalf("contracts after supersession: active=%d version=%d predecessor superseded_by=%d", activeCount, activeVersion, supersededBy)
	}
	var verdictsAfter, predecessorPredicatesAfter, startsAfter int
	if err := s.DatabaseForTesting().QueryRow(`SELECT (SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?), (SELECT count(*) FROM workflow_contract_predicates WHERE work_id=? AND contract_version=1), (SELECT count(*) FROM workflow_step_epochs WHERE work_id=?)`, workID, WorkflowVerdictRecorded, workID, workID).Scan(&verdictsAfter, &predecessorPredicatesAfter, &startsAfter); err != nil {
		t.Fatal(err)
	}
	if verdictsAfter != verdicts || predecessorPredicatesAfter != predecessorPredicates || startsAfter != starts {
		t.Fatalf("supersession disturbed history: verdicts %d->%d predecessor predicates %d->%d starts %d->%d", verdicts, verdictsAfter, predecessorPredicates, predecessorPredicatesAfter, starts, startsAfter)
	}
	step, state, refAfter, digestAfter := completeStepInstancePin(t, s, workID)
	if refAfter != definitionRef || digestAfter != definitionDigest {
		t.Fatalf("pinned definition changed: %s (%s) -> %s (%s)", definitionRef, definitionDigest, refAfter, digestAfter)
	}
	if step != "repair" || state != "running" {
		t.Fatalf("instance after correction: step=%q state=%q, want repair/running", step, state)
	}

	// Historical healthy verdicts never satisfy the successor: identical
	// predicate payloads carry no verdict across this supersession.
	satisfied, err := latestWorkflowVerdicts(ctx, s.db, workID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(satisfied) != 0 {
		t.Fatalf("historical verdicts satisfied the successor: %#v", satisfied)
	}
	cutoff, cutoffErr := workflowCompleteStepCorrectionEvidenceCutoff(ctx, s.db, workID, 2)
	if cutoffErr != nil {
		t.Fatal(cutoffErr)
	}
	if cutoff == 0 {
		t.Fatal("the corrected successor derived no evidence cutoff")
	}

	// The returned instance runs the ordinary route: fresh delivery through
	// the repair and refine external effects, a fresh verdict under the
	// successor, and only then the completion step again.
	workerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	entry, entryErr := VerifyWorkflowInstanceDefinition(ctx, s, BuiltinWorkflowRegistry(), workID)
	if entryErr != nil {
		t.Fatal(entryErr)
	}
	if workflowDerivedStepStartActive(entry.Definition) {
		if err := runVerdictActionAs(t, s, workID, "checkpoint_repair", json.RawMessage(`{}`), 0, fixture.owner); err != nil {
			t.Fatalf("the correction re-entry derives the repair start: %v", err)
		}
	} else {
		issue1013StartRepair(t, s, workID, fixture.owner, readWorkVersion(t, s, workID), latestStepStartEpoch(t, s, workID, "repair")+1)
	}
	epoch := latestStepStartEpoch(t, s, workID, "repair")
	deliverAt := func(stepID string, attemptEpoch int64) {
		t.Helper()
		version := readWorkVersion(t, s, workID)
		delivery := workflowEventWithActor("complete-correction-redelivery-"+workID+"-"+stepID, WorkflowActionCompleted, workID, workerRef, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1,
			"step_id": stepID, "action_id": "record_delivery", "attempt_epoch": attemptEpoch,
			"delivery_artifact": "evidence:complete-correction-" + workID, "delivery_state": "asserted",
			"result_evidence_refs": []string{"evidence:complete-correction-" + workID}, "changed_refs": []string{workID},
			"actor_ref": workerRef,
		})
		if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{delivery}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
			t.Fatalf("record the fresh delivery at %s: %v", stepID, err)
		}
	}
	deliverAt("repair", epoch)
	deliverAt("refine", epoch+1)
	deliverAt("delivery", epoch+2)
	if step, _, _, _ := completeStepInstancePin(t, s, workID); step != "verify" {
		t.Fatalf("step after the fresh deliveries = %q, want verify", step)
	}

	// The successor inherits no predecessor evidence either. Every
	// predecessor binding is still on the log, yet the requirement scan cuts
	// them all: the outstanding set names every required kind, the premise
	// confirmation refuses, and completion refuses at its evidence clause
	// rather than reaching its verdict clause.
	pinned, pinnedOK := BuiltinWorkflowRegistry().Current(definitionRef)
	if !pinnedOK {
		t.Fatalf("pinned definition %s is not registered", definitionRef)
	}
	outstanding, outstandingErr := outstandingWorkflowEvidenceRequirementsForWork(ctx, s.db, workID, pinned.Definition)
	if outstandingErr != nil {
		t.Fatal(outstandingErr)
	}
	if len(outstanding) != 3 {
		t.Fatalf("outstanding requirements with only predecessor evidence = %v, want verification, review, and artifact", outstanding)
	}
	for _, requirement := range outstanding {
		if requirement.Kind != "verification" && requirement.Kind != "review" && requirement.Kind != "artifact" {
			t.Fatalf("outstanding requirement = %#v, want a required evidence kind", requirement)
		}
	}
	debtPin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	requireWorkPinOutstandingKinds(t, &debtPin, []string{"artifact", "review", "verification"})
	wf := workContextFixture{store: s, workID: workID, owner: fixture.owner, operator: fixture.operator}
	var repo string
	if err := s.db.QueryRow(`SELECT path FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(workID)).Scan(&repo); err != nil {
		t.Fatal(err)
	}
	var failure *Failure
	if err := pConfirmReceiptPremise(t, wf); err == nil {
		t.Fatal("confirm_premise passed with only predecessor evidence")
	} else if !errors.As(err, &failure) || failure.Kind != KindMissingEvidence {
		t.Fatalf("confirm_premise with only predecessor evidence = %v, want missing-evidence", err)
	}
	operatorRef, operatorRefErr := WorkflowActorRef(fixture.operator)
	if operatorRefErr != nil {
		t.Fatal(operatorRefErr)
	}
	reviewerRef, reviewerRefErr := WorkflowActorRef(reviewer)
	if reviewerRefErr != nil {
		t.Fatal(reviewerRefErr)
	}
	completion := workflowEventWithActor("complete-correction-early-"+workID, WorkflowCompleted, workID, operatorRef, map[string]any{
		"work_id": workID, "expected_version": readWorkVersion(t, s, workID), "resulting_version": readWorkVersion(t, s, workID) + 1,
		"terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": reviewerRef, "premise_confirmed": true,
		"evidence_count": 3, "changed_refs_digest": "sha256:" + strings.Repeat("a", 64), "impact_verdict": "non-breaking",
	})
	completion.PayloadVersion = 2
	if err := CompleteWorkflow(ctx, s, completion); err == nil {
		t.Fatal("completion passed with only predecessor evidence")
	} else if !errors.As(err, &failure) || failure.Kind != KindMissingEvidence || failure.Clause != 1 {
		t.Fatalf("completion with only predecessor evidence = %v, want the clause-1 evidence refusal", err)
	}

	// Fresh proof under the successor is a new producer receipt on the
	// current candidate, a fresh accepted Review, its association, and fresh
	// required-kind bindings, all recorded after the cutoff: that is the fresh
	// evidence the path back to completion requires (CD-0172 D4, CD-0242 D8).
	journey := &candidateJourney{fixture: wf, s: s, workID: workID, repo: repo, acceptor: fixture.owner}
	run := journey.observeRun(t, "q-proof-successor")
	journey.bindVerification(t, run)
	qAcceptCriterionReview(t, wf, run.OperationRef)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", qAssociationPayload(t, acceptanceRecoveryFixture{store: s, workID: workID, receipt: run.OperationRef}), 0, fixture.owner); err != nil {
		t.Fatalf("associate the successor's fresh proof: %v", err)
	}
	journey.bindVerification(t, run)
	if err := runVerdictActionAs(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"artifact","immutable_subject_ref":"evidence:complete-correction-`+workID+`"}`), 0, fixture.owner); err != nil {
		t.Fatalf("bind the successor's fresh artifact: %v", err)
	}
	outstanding, outstandingErr = outstandingWorkflowEvidenceRequirementsForWork(ctx, s.db, workID, pinned.Definition)
	if outstandingErr != nil {
		t.Fatal(outstandingErr)
	}
	if len(outstanding) != 0 {
		t.Fatalf("outstanding requirements after the successor verdict = %v, want none", outstanding)
	}
	debtPin, err = ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	requireWorkPinOutstandingKinds(t, &debtPin, []string{})
	satisfied, err = latestWorkflowVerdicts(ctx, s.db, workID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(satisfied) != 1 || satisfied[0].ContractVersion != 2 {
		t.Fatalf("fresh verdicts = %#v, want one successor verdict", satisfied)
	}
	if err := pConfirmReceiptPremise(t, wf); err != nil {
		t.Fatalf("confirm the successor premise after the fresh bindings: %v", err)
	}
	if step, _, _, _ := completeStepInstancePin(t, s, workID); step != "complete" {
		t.Fatalf("step after the successor premise = %q, want complete", step)
	}
	if step, state, ref, digest := completeStepInstancePin(t, s, workID); step != "complete" || state != "running" || ref != definitionRef || digest != definitionDigest {
		t.Fatalf("instance = %s/%s %s (%s), want the corrected completion step under its original pin", step, state, ref, digest)
	}
}

// A workflow instance that already recorded completion while the work item
// stayed nonterminal is the stranded regression shape. Correction at the
// complete step reopens it: the return sets the instance running on the
// external-effect step under the unchanged pinned definition.
func TestCompleteStepContractCorrectionReopensCompletedInstance(t *testing.T) {
	t.Parallel()
	const workID = "complete-correction-reopen"
	fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
	s := fixture.store
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET instance_state='completed' WHERE work_id=?; DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := WorkflowActionDefinitionFor(context.Background(), s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err != nil {
		t.Fatalf("discovery refused the completed-instance correction: %v", err)
	}
	pin := issue1013Pin(t, s, workID)
	if len(pin.NextValidIntents) != 1 || pin.NextValidIntents[0].ActionID != "supersede_contract" {
		t.Fatalf("completed nonterminal instance intents = %#v, want only supersede_contract", pin.NextValidIntents)
	}
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner); err != nil {
		t.Fatalf("supersede on the completed instance: %v", err)
	}
	step, state, _, _ := completeStepInstancePin(t, s, workID)
	if step != "repair" || state != "running" {
		t.Fatalf("instance after correction: step=%q state=%q, want repair/running", step, state)
	}
}

// TestCompleteStepContractCorrectionReturnsImplementationToExecution proves
// the implementation mapping of the same route: the release step returns to
// execution under the unchanged pinned definition.
func TestCompleteStepContractCorrectionReturnsImplementationToExecution(t *testing.T) {
	t.Parallel()
	const workID = "complete-correction-implementation"
	fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.implementation", "acceptance", "release")
	s := fixture.store
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner); err != nil {
		t.Fatalf("supersede the contract at the release step: %v", err)
	}
	step, state, _, _ := completeStepInstancePin(t, s, workID)
	if step != "execution" || state != "running" {
		t.Fatalf("instance after correction: step=%q state=%q, want execution/running", step, state)
	}
}

// Every complete-step refusal fires before any effect: missing durable
// contradiction evidence, an unsettled worker attempt, a terminal work item,
// an unsupported workflow shape, a missing operator approval, and a stale
// work version.
func TestCompleteStepContractCorrectionRefusals(t *testing.T) {
	t.Parallel()

	t.Run("missing contradiction evidence", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-no-evidence"
		fixture, _ := seedCompleteStep(t, workID, "workflow.break_fix", "verify", "complete")
		err := issue1013Preflight(t, fixture.store, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != "contract recovery is available only for a stale workflow contract" {
			t.Fatalf("preflight without contradiction evidence = %v, want the stale-contract refusal", err)
		}
		if _, _, err := WorkflowActionDefinitionFor(context.Background(), fixture.store, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err == nil {
			t.Fatal("discovery admitted correction without contradiction evidence")
		}
	})

	t.Run("observation before the verdict", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-pre-verdict-observation"
		fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
		recordCompleteStepContradictionObservation(t, fixture.store, workID, fixture.owner)
		completeCompleteStepReview(t, fixture, workID, "verify", "complete")
		err := issue1013Preflight(t, fixture.store, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != "contract recovery is available only for a stale workflow contract" {
			t.Fatalf("preflight with only a pre-verdict observation = %v, want the stale-contract refusal", err)
		}
	})

	t.Run("terminal work item", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-terminal"
		fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
		if _, err := fixture.store.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE work_items SET lifecycle='completed' WHERE id=?; DELETE FROM write_guard`, workID); err != nil {
			t.Fatal(err)
		}
		_, _, err := WorkflowActionDefinitionFor(context.Background(), fixture.store, BuiltinWorkflowRegistry(), workID, "supersede_contract")
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != "contract recovery is available only for a stale workflow contract" {
			t.Fatalf("discovery on a terminal item = %v, want the shared admission's stale-contract refusal", err)
		}
	})

	t.Run("unsupported workflow shape", func(t *testing.T) {
		t.Parallel()
		// CD-0242 D14 removes retired definitions from the registry. The
		// unsupported family now refuses before a recovery shape can be pinned.
		_, err := BuiltinWorkflowDefinitionForRef("workflow.static_analysis")
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindDefinitionDigestMismatch || !strings.Contains(failure.Detail, "workflow type reference is not registered") {
			t.Fatalf("resolve the static-analysis shape = %v, want the retired-family refusal", err)
		}
	})

	// consequence_v1 records an engineering successor decision without an
	// operator approval. Classified law-change consent stays covered by the
	// law-change suite.
	t.Run("routine authority without operator approval", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-no-approval"
		fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
		version := readWorkVersion(t, fixture.store, workID)
		ctx, err := fixture.store.EstablishKnowledgeSourceSetProof(t.Context(), workID)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := fixture.store.DatabaseForTesting().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		operationID := "complete-correction-no-approval"
		_, err = applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: version, ActionID: "supersede_contract", Payload: completeStepSuccessorPayload(workID), Actor: fixture.owner,
			AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64), IdempotencyIdentity: operationID, OperationID: operationID,
			PrincipalRef: fixture.owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
			ContractDigest: testManifestDigest, Now: time.Unix(30, 0).UTC(),
		})
		if err != nil {
			t.Fatalf("routine complete-step correction without approval = %v, want admitted", err)
		}
	})

	t.Run("stale work version", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-stale-version"
		fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
		version := readWorkVersion(t, fixture.store, workID)
		tx, err := fixture.store.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		operationID := "complete-correction-stale-version"
		_, err = applyWorkflowActionRawTx(context.Background(), tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: version + 1, ActionID: "supersede_contract", Payload: completeStepSuccessorPayload(workID), Actor: fixture.owner,
			AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64), IdempotencyIdentity: operationID, OperationID: operationID,
			PrincipalRef: fixture.owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
			ContractDigest: testManifestDigest, Now: time.Unix(31, 0).UTC(),
		})
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindVersionConflict {
			t.Fatalf("stale-version correction = %v, want version_conflict", err)
		}
	})

	t.Run("missing declared supersession event", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-missing-supersession"
		fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
		if _, err := fixture.store.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_contracts SET route_conventions='["complete_step_correction"]' WHERE work_id=? AND contract_version=1; DELETE FROM write_guard`, workID); err != nil {
			t.Fatal(err)
		}
		_, err := workflowCompleteStepCorrectionEvidenceCutoff(context.Background(), fixture.store.db, workID, 1)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvariantViolation {
			t.Fatalf("missing declared supersession = %v, want invariant_violation", err)
		}
	})
}

func issue1013HasIntent(pin WorkPin, actionID string) bool {
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == actionID {
			return true
		}
	}
	return false
}

// A correction must be representable in the response before its effect commits.
func TestRequestCorrectionRefusesSchemaBreakingRefs(t *testing.T) {
	for _, tc := range []struct{ name, field, value string }{
		{"predicate slash", "predicate_ids", "predicate:primary/slash"},
		{"evidence whitespace", "evidence_refs", "evidence:review with spaces"},
		{"evidence length", "evidence_refs", "evidence:" + strings.Repeat("a", 129)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workID := "correction-charset-" + strings.ReplaceAll(tc.name, " ", "-")
			j := pReturnReviewFixture(t, workID, "workflow.implementation", "correction_required")
			var payload map[string]any
			if err := json.Unmarshal(pReviewCorrectionPayload(t), &payload); err != nil {
				t.Fatal(err)
			}
			payload[tc.field] = []string{tc.value}
			before := j.effects(t)
			err := j.action(t, "request_correction", payload, j.fixture.owner)
			if !hasFailureKind(err, KindInvalidPayload) {
				t.Fatalf("correction with %s=%q: %v, want invalid_payload", tc.field, tc.value, err)
			}
			if j.effects(t) != before || issue1013Pin(t, j.s, workID).Correction != nil {
				t.Fatal("invalid correction changed effects or acquired a source")
			}
			if err := payloadschema.Validate("work_pin", mustJSONValue(issue1013Pin(t, j.s, workID))); err != nil {
				t.Fatalf("refused correction left a malformed response: %v", err)
			}
		})
	}
}

// latestStepStartEpoch reads the newest workflow action start epoch the step
// recorded, so record payloads can bind the exact epoch the fold will check.
func latestStepStartEpoch(t *testing.T, s *Store, workID, stepID string) int64 {
	t.Helper()
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(attempt_epoch),0) FROM workflow_step_epochs WHERE work_id=? AND step_id=?`, workID, stepID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
}

func TestCorrectiveReviewAllowsFreshCorrectionSource(t *testing.T) {
	const workID = "correction-count-accept-reset"
	j := pReturnReviewFixture(t, workID, "workflow.break_fix", "correction_required")
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	source := issue1013Pin(t, j.s, workID).Correction
	if source == nil || source.SourceEventID == "" || source.SourceEventSeq < 1 {
		t.Fatalf("first correction has no source: %+v", source)
	}
	pReturnProduce(t, j, "corrective")
	acceptCurrentReviewForTest(t, j.fixture, "ready_for_final_proof")
	pin := issue1013Pin(t, j.s, workID)
	if pin.Correction != nil {
		t.Fatalf("corrective Review left correction context: %#v", pin.Correction)
	}
	if step := currentStep(t, j.s, workID); step != "repair" {
		t.Fatalf("Review advanced the step to %q, want repair", step)
	}
	pReturnProduce(t, j, "later-defect")
	acceptCurrentReviewForTest(t, j.fixture, "correction_required")
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	pin = issue1013Pin(t, j.s, workID)
	if pin.Correction == nil || pin.Correction.SourceEventID == source.SourceEventID || pin.Correction.SourceEventSeq <= source.SourceEventSeq || pin.Correction.Diagnosis != source.Diagnosis || !reflect.DeepEqual(pin.Correction.PredicateIDs, source.PredicateIDs) {
		t.Fatalf("later correction=%+v, want fresh identity for the same declared direction", pin.Correction)
	}
}

// A scope-preserving contract revision cannot erase a genuine corrective request.
func TestCorrectionSourceSurvivesContractSupersession(t *testing.T) {
	const workID = "correction-count-supersede"
	j := pReturnReviewFixture(t, workID, "workflow.break_fix", "correction_required")
	s, owner := j.s, j.fixture.owner
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), owner); err != nil {
		t.Fatal(err)
	}
	pin := issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.SourceEventID == "" || pin.Correction.SourceEventSeq < 1 {
		t.Fatalf("correction before supersession = %#v, want the genuine request source", pin.Correction)
	}
	source := *pin.Correction
	var sourceDigest string
	if err := s.db.QueryRow(`SELECT payload_digest FROM effect_receipts WHERE event_id=? AND effect_seq=?`, source.SourceEventID, source.SourceEventSeq).Scan(&sourceDigest); err != nil {
		t.Fatal(err)
	}
	projection, err := ReadWorkflowProjection(t.Context(), s, WorkflowReadRequest{WorkID: workID})
	if err != nil || projection.Contract == nil {
		t.Fatalf("current contract=%+v err=%v", projection.Contract, err)
	}
	contract := projection.Contract
	predicates := make([]map[string]any, len(contract.OutcomePredicates))
	for i, predicate := range contract.OutcomePredicates {
		predicates[i] = map[string]any{"predicate_id": predicate.PredicateID, "ordinal": predicate.Ordinal, "outcome_kind": predicate.OutcomeKind, "outcome_payload": json.RawMessage(predicate.OutcomePayload)}
	}
	successor, err := json.Marshal(map[string]any{
		"contract_version": contract.Version + 1, "premise": "The same approved outcome needs a corrected approach.",
		"outcome_predicates": predicates, "required_evidence": contract.RequiredEvidence,
		"route_conventions": contract.RouteConventions, "spec_mandate": contract.SpecMandate, "law_modifies": contract.LawModifies,
		"rigor_class": contract.RigorClass, "architecture_binding": contract.ArchitectureBinding, "decision_domains": []string{},
		"supersede_reason": "correct the approach without changing the approved outcome", "audit_evidence": source.EvidenceRefs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, s, workID, "supersede_contract", successor, 0, owner); err != nil {
		t.Fatalf("supersede the contract across the correction sequence: %v", err)
	}
	var activeVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeVersion); err != nil {
		t.Fatal(err)
	}
	if int64(activeVersion) != contract.Version+1 {
		t.Fatalf("active contract version = %d, want %d", activeVersion, contract.Version+1)
	}
	pin = issue1013Pin(t, s, workID)
	if !reflect.DeepEqual(pin.Correction, &source) {
		t.Errorf("correction across supersession = %#v, want the same genuine request", pin.Correction)
	}
	var afterDigest string
	if err := s.db.QueryRow(`SELECT payload_digest FROM effect_receipts WHERE event_id=? AND effect_seq=?`, source.SourceEventID, source.SourceEventSeq).Scan(&afterDigest); err != nil || afterDigest != sourceDigest {
		t.Fatalf("supersession changed the source receipt: %q -> %q, %v", sourceDigest, afterDigest, err)
	}
	pReturnProduce(t, j, "successor-correction")
	acceptCurrentReviewForTest(t, j.fixture, "correction_required")
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), owner); err != nil {
		t.Fatalf("fresh corrective return under successor: %v", err)
	}
	pin = issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.SourceEventID == source.SourceEventID || pin.Correction.SourceEventSeq <= source.SourceEventSeq {
		t.Fatalf("successor correction=%+v, want fresh request identity", pin.Correction)
	}
}

// laterOrdinarySuccessorPayload is the typed successor a later ordinary
// supersession approves after the complete-step correction: identical
// predicate payloads, no reserved convention, version 3.
func laterOrdinarySuccessorPayload(workID string) json.RawMessage {
	binding := `{"home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}`
	return json.RawMessage(`{"contract_version":3,"premise":"the delivered subject the durable evidence names","outcome_predicates":[{"predicate_id":"predicate:return-route","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:return-route","immutable_subject_ref":"commit:` + workID + `","expected_result":"pass"}}],"required_evidence":["verification","review","artifact"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","decision_domains":[],"architecture_binding":` + binding + `,"supersede_reason":"the corrected contract premise moved again","audit_evidence":["evidence:later-successor-` + workID + `"]}`)
}

// TestCompleteStepCorrectionCutoffPersistsAcrossLaterSuccessor proves the
// correction cut survives the route marker. A later ordinary supersession of
// the corrected contract inherits neither the pre-correction healthy verdict
// nor the pre-correction evidence bindings, however identical the predicate
// payloads remain: the cutoff is ancestry state, not a property of the active
// contract row (CD-0172 D4).
func TestCompleteStepCorrectionCutoffPersistsAcrossLaterSuccessor(t *testing.T) {
	t.Parallel()
	const workID = "complete-correction-cutoff-persists"
	ctx := context.Background()
	fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
	s := fixture.store

	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner); err != nil {
		t.Fatalf("supersede the contract at the complete step: %v", err)
	}
	cutoffV2, cutoffErr := workflowCompleteStepCorrectionEvidenceCutoff(ctx, s.db, workID, 2)
	if cutoffErr != nil {
		t.Fatal(cutoffErr)
	}
	if cutoffV2 == 0 {
		t.Fatal("the corrected successor derived no evidence cutoff")
	}

	// A later ordinary supersession at the returned repair step retires the
	// corrected contract without the reserved convention.
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", laterOrdinarySuccessorPayload(workID), fixture.owner); err != nil {
		t.Fatalf("ordinary supersession of the corrected contract: %v", err)
	}
	var activeVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeVersion); err != nil {
		t.Fatal(err)
	}
	if activeVersion != 3 {
		t.Fatalf("active contract version = %d, want 3", activeVersion)
	}

	cutoffV3, cutoffErr := workflowCompleteStepCorrectionEvidenceCutoff(ctx, s.db, workID, 3)
	if cutoffErr != nil {
		t.Fatal(cutoffErr)
	}
	if cutoffV3 != cutoffV2 {
		t.Fatalf("cutoff moved across the ordinary successor: %d -> %d", cutoffV2, cutoffV3)
	}

	// The pre-correction healthy verdict satisfies nothing: the successor's
	// predicate history is byte-identical, yet no verdict is admitted.
	satisfied, err := latestWorkflowVerdicts(ctx, s.db, workID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(satisfied) != 0 {
		t.Fatalf("ordinary successor inherited pre-correction verdicts: %#v", satisfied)
	}

	// The pre-correction bindings satisfy nothing either: every kind the
	// predecessor bound stays outstanding under the ordinary successor.
	pinned, pinnedOK := completeStepPinnedDefinition(t, s, workID)
	if !pinnedOK {
		t.Fatal("pinned break-fix definition is not registered")
	}
	outstanding, outstandingErr := outstandingWorkflowEvidenceRequirementsForWork(ctx, s.db, workID, pinned.Definition)
	if outstandingErr != nil {
		t.Fatal(outstandingErr)
	}
	if len(outstanding) != 3 {
		t.Fatalf("outstanding requirements under the ordinary successor = %v, want verification, review, and artifact", outstanding)
	}
}

func completeStepPinnedDefinition(t *testing.T, s *Store, workID string) (RegisteredDefinition, bool) {
	t.Helper()
	var definitionRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&definitionRef); err != nil {
		t.Fatal(err)
	}
	return BuiltinWorkflowRegistry().Current(definitionRef)
}

// staleLawSuccessorPayload mandates a current accepted law. The composed
// successor derives current pins without a relation to the deleted law.
func staleLawSuccessorPayload(workID string) json.RawMessage {
	binding := `{"home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}`
	return json.RawMessage(`{"contract_version":2,"premise":"the delivered subject under the successor law","outcome_predicates":[{"predicate_id":"predicate:return-route","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:return-route","immutable_subject_ref":"commit:` + workID + `","expected_result":"pass"}}],"required_evidence":["verification","review","artifact"],"route_conventions":["complete_step_correction"],"spec_mandate":["spec:two"],"law_modifies":[],"rigor_class":"prototype_internal","decision_domains":[],"architecture_binding":` + binding + `,"supersede_reason":"the approved premise moved to the successor law","audit_evidence":["evidence:complete-correction-` + workID + `"]}`)
}

// TestCompleteStepCorrectionStaleLawStaysBehindTheGate proves the stale-law
// recovery route does not bypass the complete-step admission. With the active
// contract's pinned law deleted but no durable contradiction record,
// discovery and preflight refuse; the route opens only when the contradiction
// observation postdates the verdict (CD-0172 D1).
func TestCompleteStepCorrectionStaleLawStaysBehindTheGate(t *testing.T) {
	t.Parallel()
	const workID = "complete-correction-stale-law-gate"
	fixture, _ := seedCompleteStep(t, workID, "workflow.break_fix", "verify", "complete")
	s := fixture.store
	attachWorkflowLawPin(t, s, workID, "sha256:"+strings.Repeat("a", 64))
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		UPDATE workflow_contracts SET spec_mandate='["spec:one"]' WHERE work_id=? AND contract_version=1;
		DELETE FROM law_domain_homes WHERE law_id='spec:one';
		DELETE FROM law_subjects WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one';
		INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:two','spec','accepted','.concord/docs/spec-two.md','Synthetic successor law','sha256:`+strings.Repeat("b", 64)+`','test');
		INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid,product_wide_rationale) VALUES('project','workflow-law-locator','spec:two','product','root','sha256:`+strings.Repeat("b", 64)+`','test','');
		DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}

	err := issue1013Preflight(t, s, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != "contract recovery is available only for a stale workflow contract" {
		t.Fatalf("stale-law supersession without the contradiction record = %v, want the complete-step gate refusal", err)
	}
	if _, _, err := WorkflowActionDefinitionFor(context.Background(), s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err == nil {
		t.Fatal("discovery admitted the complete-step correction without the contradiction record")
	}

	// Recording the durable contradiction opens the shared gate; the stale-law
	// condition neither excuses the reserved convention nor the state gate,
	// and the successor still pins the accepted successor law.
	recordCompleteStepContradictionObservation(t, s, workID, fixture.owner)
	if err := issue1013Preflight(t, s, workID, "supersede_contract", staleLawSuccessorPayload(workID), fixture.owner); err != nil {
		t.Fatalf("preflight refused the complete-step correction behind a stale law: %v", err)
	}
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", staleLawSuccessorPayload(workID), fixture.owner); err != nil {
		t.Fatalf("supersede behind a stale law through the gate: %v", err)
	}
	if step, state, _, _ := completeStepInstancePin(t, s, workID); step != "repair" || state != "running" {
		t.Fatalf("instance after correction: step=%q state=%q, want repair/running", step, state)
	}
}

// TestCompleteStepCorrectionRecoversDuplicateProjection proves a duplicated
// contract projection at the pinned complete step keeps an exit: the
// complete-step correction admits it once the contradiction record exists,
// the successor names every active version, and the instance returns to its
// external-effect step under one active contract (CD-0203 D1). Before the
// contradiction record, the shared gate refuses as for one contract. The
// loader's fold matches the abstract model before and after the recovery.
func TestCompleteStepCorrectionRecoversDuplicateProjection(t *testing.T) {
	t.Parallel()
	const workID = "complete-correction-duplicate"
	fixture, _ := seedCompleteStep(t, workID, "workflow.break_fix", "verify", "complete")
	s := fixture.store
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	seedDuplicateContractProjection(t, s, workID, ownerRef)
	seedWorkflowLawProjection(t, s)
	var activeCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if activeCount != 2 {
		t.Fatalf("duplicate fixture active contracts = %d, want 2", activeCount)
	}
	def := pinnedWorkflowDefinitionForTest(t, s, workID)
	model := admissionModelState{step: "complete", contracts: 2, verdict: "ok", artifact: true}
	conformanceCheckpoint(t, s, BuiltinWorkflowRegistry(), workID, def, model, "duplicate at the complete step")
	if issue1013HasIntent(issue1013Pin(t, s, workID), "supersede_contract") {
		t.Fatal("duplicate complete-step pin advertises supersession before the contradiction record")
	}
	duplicateRecovery := json.RawMessage(strings.Replace(string(completeStepSuccessorPayload(workID)), `"contract_version":2,`, `"contract_version":3,"predecessor_contract_versions":[1,2],`, 1))
	err = issue1013Preflight(t, s, workID, "supersede_contract", duplicateRecovery, fixture.owner)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != "contract recovery is available only for a stale workflow contract" {
		t.Fatalf("duplicate recovery without the contradiction record = %v, want the complete-step gate refusal", err)
	}

	recordCompleteStepContradictionObservation(t, s, workID, fixture.owner)
	model.observed = true
	conformanceCheckpoint(t, s, BuiltinWorkflowRegistry(), workID, def, model, "duplicate with the contradiction record")
	if !issue1013HasIntent(issue1013Pin(t, s, workID), "supersede_contract") {
		t.Fatal("duplicate complete-step pin withholds the admitted correction")
	}
	if err := issue1013Preflight(t, s, workID, "supersede_contract", duplicateRecovery, fixture.owner); err != nil {
		t.Fatalf("preflight refused the duplicate complete-step correction: %v", err)
	}
	// consequence_v1 supersedes through attributed supersession, not
	// verified operator approval (CD-0172).
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", duplicateRecovery, fixture.owner); err != nil {
		t.Fatalf("duplicate complete-step correction: %v", err)
	}
	if step, state, _, _ := completeStepInstancePin(t, s, workID); step != "repair" || state != "running" {
		t.Fatalf("instance after duplicate correction: step=%q state=%q, want repair/running", step, state)
	}
	var active []int64
	rows, err := s.DatabaseForTesting().Query(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			t.Fatal(err)
		}
		active = append(active, version)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0] != 3 {
		t.Fatalf("active contracts after duplicate correction = %v, want [3]", active)
	}
	model = admissionSuccessor(def, model, "supersede_contract")
	// The single successor restores the seeded journey's facets the
	// duplicates hid: its started delivery, the accepted current Review of
	// the delivered candidate, and the engineering successor's coordinator
	// confirmation.
	model.started = true
	model.review = admissionModelReview{candidate: true, accepted: "ready_for_final_proof", current: true}
	conformanceCheckpoint(t, s, BuiltinWorkflowRegistry(), workID, def, model, "after the duplicate correction")
}

func TestCompleteStepCorrectionAdmissionUsesOnlyPriorHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, test := range []struct {
		name              string
		observationBefore bool
		laterKind         string
		wantBefore        bool
		wantLive          bool
	}{
		{"later verdict", true, WorkflowVerdictRecorded, true, false},
		{"later observation", false, WorkObservationRecorded, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			const workID = "correction-history-boundary"
			fixture, _ := seedCompleteStep(t, workID, "workflow.break_fix", "verify", "complete")
			s := fixture.store
			db := s.DatabaseForTesting()
			if test.observationBefore {
				recordCompleteStepContradictionObservation(t, s, workID, fixture.owner)
			}
			definition, ok := completeStepPinnedDefinition(t, s, workID)
			if !ok {
				t.Fatal("missing pinned definition")
			}
			if available, err := workflowCompleteStepCorrectionAvailable(ctx, db, workID, definition.Definition, "complete", "test", 0); err != nil || available != test.wantBefore {
				t.Fatalf("admission before current update = %t,%v; want %t", available, err, test.wantBefore)
			}
			var beforeSeq int64
			if test.laterKind == WorkObservationRecorded {
				recordCompleteStepContradictionObservation(t, s, workID, fixture.owner)
				if err := db.QueryRow(`SELECT MAX(recorded_seq) FROM work_observations WHERE work_id=?`, workID).Scan(&beforeSeq); err != nil {
					t.Fatal(err)
				}
			} else {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				scope, err := beginWrite(ctx, tx)
				if err != nil {
					t.Fatal(err)
				}
				event := Event{EventID: "later-history", Kind: test.laterKind, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: fixture.owner.PrincipalRef, OccurredAt: time.Date(2026, 10, 13, 0, 0, 1, 0, time.UTC), PayloadVersion: 1, Payload: json.RawMessage(`{}`)}
				beforeSeq, err = appendPreparedEvent(ctx, tx, event)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(`UPDATE workflow_predicate_verdicts SET verdict_seq=? WHERE work_id=?`, beforeSeq, workID); err != nil {
					t.Fatal(err)
				}
				if err := scope.close(ctx); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			for _, boundary := range []struct {
				seq  int64
				want bool
			}{{beforeSeq, test.wantBefore}, {beforeSeq + 1, test.wantLive}, {0, test.wantLive}} {
				available, err := workflowCompleteStepCorrectionAvailable(ctx, db, workID, definition.Definition, "complete", "test", boundary.seq)
				if err != nil || available != boundary.want {
					t.Errorf("admission before sequence %d = %t, error = %v; want %t", boundary.seq, available, err, boundary.want)
				}
			}
		})
	}
}

func TestCompleteStepCorrectionKeepsSuccessorVerdictsFresh(t *testing.T) {
	t.Parallel()
	const workID = "complete-correction-fresh-verdicts"
	ctx := context.Background()
	fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
	s := fixture.store
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner); err != nil {
		t.Fatalf("supersede the contract at the complete step: %v", err)
	}
	step, state, _, _ := completeStepInstancePin(t, s, workID)
	if step != "repair" || state != "running" {
		t.Fatalf("instance after correction: step=%q state=%q, want repair/running", step, state)
	}
	var activeVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeVersion); err != nil {
		t.Fatal(err)
	}
	if activeVersion != 2 {
		t.Fatalf("active contract version after correction = %d, want 2", activeVersion)
	}
	satisfied, err := latestWorkflowVerdicts(ctx, s.db, workID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(satisfied) != 0 {
		t.Fatalf("successor inherited pre-correction verdicts: %#v", satisfied)
	}
}

// seedOffShapeCompletedInstance parks a completed workflow instance on a
// verified break-fix step whose pinned shape does not carry the complete-step
// correction route, and deletes the active contract's pinned law so the
// stale-law recovery is the admission under test.
func seedOffShapeCompletedInstance(t *testing.T, workID string) workflowReturnRouteFixture {
	t.Helper()
	fixture, _ := seedCompleteStep(t, workID, "workflow.break_fix", "verify", "complete")
	attachWorkflowLawPin(t, fixture.store, workID, "sha256:"+strings.Repeat("a", 64))
	if _, err := fixture.store.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step='verify', instance_state='completed' WHERE work_id=?;
		UPDATE workflow_contracts SET spec_mandate='["spec:one"]' WHERE work_id=? AND contract_version=1;
		DELETE FROM law_domain_homes WHERE law_id='spec:one';
		DELETE FROM law_subjects WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one';
		INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:two','spec','accepted','.concord/docs/spec-two.md','Synthetic successor law','sha256:`+strings.Repeat("b", 64)+`','test');
		INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid,product_wide_rationale) VALUES('project','workflow-law-locator','spec:two','product','root','sha256:`+strings.Repeat("b", 64)+`','test','');
		DELETE FROM write_guard`, workID, workID); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// offShapeStaleLawSuccessorPayload is the stale-law successor without the
// reserved convention: the shape under test carries no complete-step
// correction route, so a shape-compliant successor declares no route.
func offShapeStaleLawSuccessorPayload(workID string) json.RawMessage {
	return json.RawMessage(strings.Replace(string(staleLawSuccessorPayload(workID)), `"route_conventions":["complete_step_correction"]`, `"route_conventions":[]`, 1))
}

// TestCompleteStepCorrectionReadOnlyPreflightRequiresReservedRoute pins the
// shared admission the read-only surface once skipped: a completed nonterminal
// instance at the pinned complete step whose successor omits the reserved
// complete_step_correction convention refuses at the read-only preflight
// exactly as the transaction preflight, guard, and fold refuse, and no effect
// records (CD-0172).
func TestCompleteStepCorrectionReadOnlyPreflightRequiresReservedRoute(t *testing.T) {
	t.Parallel()
	const workID = "complete-correction-missing-marker"
	fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
	s := fixture.store
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET instance_state='completed' WHERE work_id=?; DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}
	withoutRoute := json.RawMessage(strings.Replace(string(completeStepSuccessorPayload(workID)), `"route_conventions":["complete_step_correction"]`, `"route_conventions":[]`, 1))
	if !strings.Contains(string(withoutRoute), `"route_conventions":[]`) {
		t.Fatal("the missing-marker payload kept the reserved convention")
	}
	err := issue1013Preflight(t, s, workID, "supersede_contract", withoutRoute, fixture.owner)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidPayload || failure.Detail != "complete-step correction requires complete_step_correction in fields.route_conventions" {
		t.Fatalf("read-only preflight without the reserved route = %v, want the reserved-route refusal", err)
	}
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", withoutRoute, fixture.owner); err == nil {
		t.Fatal("the owning action admitted a successor without the reserved route")
	}
	var activeVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeVersion); err != nil {
		t.Fatal(err)
	}
	if activeVersion != 1 {
		t.Fatalf("active contract version = %d, want the predecessor 1", activeVersion)
	}
	if step, state, _, _ := completeStepInstancePin(t, s, workID); step != "complete" || state != "completed" {
		t.Fatalf("instance moved without effect: step=%q state=%q, want complete/completed", step, state)
	}
}

// TestCompletedInstanceOffShapeSupersessionRefusesBeforeEffect pins the
// completed-instance boundary every admission surface shares: a completed
// workflow instance whose pinned shape does not carry the complete-step
// correction route refuses supersession at discovery, the read-only preflight,
// the transaction admission, the owning mutation, and the live fold, while
// the same historical event still replays and the instance stays closed with
// its contract active (CD-0172).
func TestCompletedInstanceOffShapeSupersessionRefusesBeforeEffect(t *testing.T) {
	t.Parallel()

	const sharedDetail = "a completed workflow instance supersedes its contract only on the pinned complete-step correction shape"

	t.Run("discovery", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-offshape-discovery"
		fixture := seedOffShapeCompletedInstance(t, workID)
		_, _, err := WorkflowActionDefinitionFor(context.Background(), fixture.store, BuiltinWorkflowRegistry(), workID, "supersede_contract")
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != sharedDetail {
			t.Fatalf("discovery on the off-shape completed instance = %v, want the shape refusal", err)
		}
	})

	t.Run("read-only preflight", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-offshape-preflight"
		fixture := seedOffShapeCompletedInstance(t, workID)
		err := issue1013Preflight(t, fixture.store, workID, "supersede_contract", offShapeStaleLawSuccessorPayload(workID), fixture.owner)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != sharedDetail {
			t.Fatalf("read-only preflight on the off-shape completed instance = %v, want the shape refusal", err)
		}
	})

	t.Run("transaction admission", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-offshape-admission"
		fixture := seedOffShapeCompletedInstance(t, workID)
		err := WorkflowActionPreflightWithRegistry(context.Background(), fixture.store, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
			WorkID: workID, ActionID: "supersede_contract", Payload: offShapeStaleLawSuccessorPayload(workID), Actor: fixture.owner,
		})
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != sharedDetail {
			t.Fatalf("transaction admission on the off-shape completed instance = %v, want the shape refusal", err)
		}
	})

	t.Run("mutation", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-offshape-mutation"
		fixture := seedOffShapeCompletedInstance(t, workID)
		err := runIssue933OperatorAction(t, fixture.store, workID, "supersede_contract", offShapeStaleLawSuccessorPayload(workID), fixture.owner)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != sharedDetail {
			t.Fatalf("mutation on the off-shape completed instance = %v, want the shape refusal", err)
		}
		var activeVersion int
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeVersion); err != nil {
			t.Fatal(err)
		}
		if activeVersion != 1 {
			t.Fatalf("active contract version after the refused mutation = %d, want the predecessor 1", activeVersion)
		}
	})

	t.Run("live fold refuses", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-offshape-fold"
		fixture := seedOffShapeCompletedInstance(t, workID)
		successor := &workflowContractApprovedPayload{ContractVersion: 2, RouteConventions: []string{}}
		tx, txErr := fixture.store.DatabaseForTesting().BeginTx(context.Background(), nil)
		if txErr != nil {
			t.Fatal(txErr)
		}
		defer tx.Rollback()
		err := foldCompleteStepContractCorrectionTx(context.Background(), tx, Event{SubjectID: workID}, successor)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvariantViolation || failure.Detail != sharedDetail {
			t.Fatalf("live fold of the off-shape completed supersession = %v, want the shape refusal", err)
		}
	})
}

// TestCompletedInstanceDuplicateRecoveryStaysOffThePin keeps the duplicate
// recovery exception off a completed instance: the work pin and discovery
// refuse when the duplicated projection sits on a completed instance whose
// pinned shape does not carry the complete-step correction route (CD-0172).
func TestCompletedInstanceDuplicateRecoveryStaysOffThePin(t *testing.T) {
	t.Parallel()
	const workID = "complete-correction-duplicate-offshape"
	fixture, _ := seedCompleteStep(t, workID, "workflow.break_fix", "verify", "complete")
	s := fixture.store
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	seedDuplicateContractProjection(t, s, workID, ownerRef)
	var activeCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if activeCount != 2 {
		t.Fatalf("off-shape duplicate fixture active contracts = %d, want 2", activeCount)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET current_step='verify', instance_state='completed' WHERE work_id=?; DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}
	pin := issue1013Pin(t, s, workID)
	if issue1013HasIntent(pin, "supersede_contract") {
		t.Fatalf("completed off-shape pin advertises duplicate recovery: %#v", pin.NextValidIntents)
	}
	if _, _, err := WorkflowActionDefinitionFor(context.Background(), s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err == nil {
		t.Fatal("discovery offered duplicate recovery on the completed off-shape instance")
	}
}

// foldCompleteStepContractCorrectionTx resolves the builtin registry for
// direct callers. The core keeps the route marker and
// admission checks together before it returns the pinned instance to its
// external-effect step, and the scoped supersession fold carries the
// transaction's registry to the same core. The caller holds the fold
// transaction.
func foldCompleteStepContractCorrectionTx(ctx context.Context, tx *sql.Tx, event Event, successor *workflowContractApprovedPayload) error {
	return foldCompleteStepContractCorrectionTxForRegistry(ctx, tx, BuiltinWorkflowRegistry(), event, successor)
}
