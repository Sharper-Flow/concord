package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func runIssue933OperatorAction(t *testing.T, s *Store, workID, action string, payload json.RawMessage, owner WorkflowActor, operator WorkflowActor) error {
	t.Helper()
	version := verdictItemVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	operationID := fmt.Sprintf("issue933-%s-%s-%d", action, workID, version)
	_, err = applyWorkflowActionRawTx(context.Background(), tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: owner, OperatorActor: &operator,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64), IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
		ContractDigest: testManifestDigest, Now: time.Unix(20, version).UTC(),
	})
	if err != nil {
		return err
	}
	return tx.Commit()
}

// produceContractCorrectionWorker drives the fixture's execution producer
// through the real dispatch and acceptance actions. Only the lane's dispatch
// and completed report are simulated; the engine owns the production event.
func produceContractCorrectionWorker(t *testing.T, s *Store, workID, label string, owner WorkflowActor) {
	t.Helper()
	ctx := context.Background()
	if got := currentStep(t, s, workID); got != "execution" {
		t.Fatalf("producer step = %q, want execution", got)
	}
	var definitionRef string
	var definitionVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version FROM workflow_instances WHERE work_id=?`, workID).Scan(&definitionRef, &definitionVersion); err != nil {
		t.Fatal(err)
	}
	if definitionRef != workflowFixtureRef || definitionVersion != 2 {
		t.Fatalf("production journey pin = %s@%d, want %s@2", definitionRef, definitionVersion, workflowFixtureRef)
	}
	definition := workflowFixtureDefinition(t, definitionVersion).Definition
	assertStale := func(stage, step string, want bool) {
		t.Helper()
		got, err := workflowArtifactStale(ctx, s.DatabaseForTesting(), workID, definition, step, "workflow_action")
		if err != nil {
			t.Fatalf("%s: read artifact freshness: %v", stage, err)
		}
		if got != want {
			t.Fatalf("%s: artifact stale = %t, want %t", stage, got, want)
		}
	}
	lane := reviewGateLane(t, "implementation")
	attemptID := "attempt:" + workID + ":" + label
	// The claim can change the work version, so build the packet afterward.
	dispatchSessionWorktree(t, s, workID)
	packet := joinPacketFor(t, s, workID, "execution", attemptID, lane.ID, lane.Version, lane.Digest)
	correction, err := workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, "execution", attemptID)
	if err != nil {
		t.Fatalf("read producing worker correction: %v", err)
	}
	if correction != nil {
		packet["inputs"].(map[string]any)["correction"] = correction
	}
	if _, err := dispatchJoinAttempt(ctx, t, s, workID, verdictItemVersion(t, s, workID), owner, packet); err != nil {
		t.Fatalf("dispatch producing worker: %v", err)
	}
	assertStale("dispatch alone", "execution", correction != nil)
	var packetDigest string
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_packet_digest'),json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, attemptID).Scan(&packetDigest, &epoch); err != nil {
		t.Fatal(err)
	}
	dispatch := Event{
		EventID: "correction-dispatch-" + workID + "-" + label, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: "worker:test", OccurredAt: time.Now().UTC(), PayloadVersion: 2,
		Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, PacketDigest: packetDigest}),
	}
	if err := s.Transact(ctx, func(transaction *Transaction) error {
		prepared, err := PrepareLaneActorDispatch(ctx, transaction, dispatch, owner.PrincipalRef, owner.ClientRef)
		if err != nil {
			return err
		}
		_, err = AppendLaneActorDispatchTx(ctx, transaction, prepared)
		return err
	}); err != nil {
		t.Fatalf("producing lane dispatch: %v", err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{workerCompleteEventForLane(workID, "correction-completed-"+workID+"-"+label, attemptID, lane, time.Now().UTC())}}); err != nil {
		t.Fatalf("complete producing worker: %v", err)
	}
	assertStale("completed but unaccepted worker", "execution", correction != nil)
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", json.RawMessage(fmt.Sprintf(`{"attempt_id":%q,"attempt_epoch":%d}`, attemptID, epoch)), 0, owner); err != nil {
		t.Fatalf("accept producing worker: %v", err)
	}
	if got := currentStep(t, s, workID); got != "acceptance" {
		t.Fatalf("step after production = %q, want acceptance on the pinned fixture", got)
	}
	assertStale("accepted production", "acceptance", false)
}

func contractCorrectionEffectSnapshot(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var version, events, operations int64
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT w.version,i.current_step,(SELECT count(*) FROM domain_events WHERE subject_id=w.id),(SELECT count(*) FROM durable_operations WHERE work_id=w.id) FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&version, &step, &events, &operations); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("version=%d step=%s events=%d operations=%d", version, step, events, operations)
}

func TestIssue933LateVerdictRecoveryHoldsStepAndRefusesHealthyReplacement(t *testing.T) {
	t.Parallel()
	const workID = "issue933-late-verdict"
	s, owner, _ := seedItemAtAcceptance(t, workID, false)
	reviewer := verdictReviewer(t, s, workID)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","verdict_kind":"outcome_mismatch","incomparable_with_approved":true}`), 0, reviewer); err != nil {
		t.Fatalf("record mismatch verdict: %v", err)
	}
	operator := operatorVerdictActor(t, workID)
	var verdictEvidence string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.evaluation_evidence[0]') FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowVerdictRecorded).Scan(&verdictEvidence); err != nil {
		t.Fatal(err)
	}
	beforeCorrection := contractCorrectionEffectSnapshot(t, s, workID)
	healthy := json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","verdict_kind":"ok"}`)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", healthy, 0, reviewer); !hasFailureKind(err, KindStaleRequiresReview) {
		t.Fatalf("healthy verdict before production = %v, want stale_requires_review", err)
	}
	if err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":1}`), owner, operator); !hasFailureKind(err, KindStaleRequiresReview) {
		t.Fatalf("premise confirmation before production = %v, want stale_requires_review", err)
	}
	if got := contractCorrectionEffectSnapshot(t, s, workID); got != beforeCorrection {
		t.Fatalf("stale refusals changed effects: before %s, after %s", beforeCorrection, got)
	}
	correction := json.RawMessage(`{"diagnosis":"the verdict found a stale artifact","strategy":"repeat execution with a fresh worker","predicate_ids":["predicate:primary"],"evidence_refs":["` + verdictEvidence + `"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", correction, owner, operator); err != nil {
		t.Fatalf("request verdict correction: %v", err)
	}
	produceContractCorrectionWorker(t, s, workID, "fresh", owner)
	if err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":1}`), owner, operator); err != nil {
		t.Fatalf("confirm premise: %v", err)
	}
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "release" {
		t.Fatalf("step after premise confirmation = %q, want release", step)
	}

	if err := runVerdictActionAs(t, s, workID, "record_verdict", healthy, 0, reviewer); err != nil {
		t.Fatalf("late healthy verdict recovery: %v", err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "release" {
		t.Fatalf("step after late verdict = %q, want release", step)
	}
	before := contractCorrectionEffectSnapshot(t, s, workID)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", healthy, 0, reviewer); err == nil {
		t.Fatal("healthy comparable verdict replacement succeeded")
	}
	after := contractCorrectionEffectSnapshot(t, s, workID)
	if after != before {
		t.Fatalf("refused healthy replacement changed effects: before %s, after %s", before, after)
	}
}

func TestIssue933PremiseRevisionUsesTypedContractSupersession(t *testing.T) {
	t.Parallel()
	const workID = "issue933-contract-correction"
	s, owner, _ := seedItemAtAcceptance(t, workID, false)
	reviewer := verdictReviewer(t, s, workID)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","verdict_kind":"ok"}`), 0, reviewer); err != nil {
		t.Fatalf("record compatible verdict: %v", err)
	}
	// The operator question gate requires an investigation observation naming
	// a current Domain and another work item before it offers the question.
	seedComparisonObservation(t, s, workID)
	question, err := ReadWorkflowOperatorQuestion(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if question == nil || question.ActionID != "confirm_premise" {
		t.Fatalf("operator question = %+v, want confirm_premise", question)
	}
	var reviseAction string
	for _, choice := range question.Choices {
		if choice.ID == "revise" {
			reviseAction = choice.ActionID
		}
	}
	if reviseAction != "supersede_contract" {
		t.Fatalf("revise choice action = %q, want supersede_contract", reviseAction)
	}

	operator := operatorVerdictActor(t, workID)
	successor := json.RawMessage(`{"contract_version":2,"premise":"corrected premise","outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:` + workID + `","expected_result":"pass"}}],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","supersede_reason":"correct the accepted premise","audit_evidence":["evidence:issue933-correction"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", successor, owner, operator); err != nil {
		t.Fatalf("supersede contract at premise checkpoint: %v", err)
	}
	var currentVersion, activeVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&currentVersion); err != nil {
		t.Fatal(err)
	}
	if currentVersion != 1 {
		t.Fatalf("active contract count = %d, want 1", currentVersion)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeVersion); err != nil {
		t.Fatal(err)
	}
	if activeVersion != 2 {
		t.Fatalf("active contract version = %d, want 2", activeVersion)
	}
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "acceptance" {
		t.Fatalf("step after contract correction = %q, want acceptance", step)
	}
	var verdicts []workflowVerdictRecordedPayload
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	verdicts, err = latestWorkflowVerdicts(context.Background(), tx, workID, 2)
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if len(verdicts) != 1 || verdicts[0].PredicateID != "predicate:primary" {
		t.Fatalf("compatible verdicts after correction = %+v, want the preserved primary verdict", verdicts)
	}
	if binding, err := WorkflowFailedWorkerRetryBinding(context.Background(), s, nil, workID); err != nil {
		t.Fatalf("public correction path after contract supersession: %v", err)
	} else if binding != nil {
		t.Fatalf("correction path returned a retry binding without a failed worker: %#v", binding)
	}
	question, err = ReadWorkflowOperatorQuestion(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if question == nil || question.PremiseSummary != "corrected premise" {
		t.Fatalf("post-correction question = %+v, want corrected premise", question)
	}
}

func TestIssue933CorrectionPreflightAfterContractSupersession(t *testing.T) {
	t.Parallel()
	const workID = "issue933-correction-preflight"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "execution")
	s, owner := fixture.store, fixture.owner
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	reviewer := acceptReturnRouteWorker(t, fixture, workID, ownerRef)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}`), 0, reviewer); err != nil {
		t.Fatalf("record return-route mismatch verdict: %v", err)
	}
	seedComparisonObservation(t, s, workID)
	question, err := ReadWorkflowOperatorQuestion(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if question == nil || question.ActionID != "confirm_premise" {
		t.Fatalf("operator question = %+v, want confirm_premise", question)
	}
	successor := json.RawMessage(`{"contract_version":2,"premise":"corrected premise","outcome_predicates":[{"predicate_id":"predicate:return-route","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:return-route","immutable_subject_ref":"commit:` + workID + `","expected_result":"pass"}}],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","architecture_binding":{"domain_registry_content_hash":"sha256:` + strings.Repeat("b", 64) + `","home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]},"supersede_reason":"correct the accepted premise","audit_evidence":["evidence:issue933-correction"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", successor, owner, fixture.operator); err != nil {
		t.Fatalf("supersede contract at premise checkpoint: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	payload := json.RawMessage(`{"diagnosis":"the delivered subject still fails","strategy":"repeat the external effect","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	if err := InspectWorkflowActionAdmission(ctx, s, WorkflowActionPreflightRequest{
		WorkID: workID, ActionID: "request_correction", Payload: payload, Actor: owner,
	}); err != nil {
		t.Fatalf("public correction preflight after contract supersession: %v", err)
	}
	_, action, err := WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "request_correction")
	if err != nil {
		t.Fatalf("public correction action after contract supersession: %v", err)
	}
	if action.ID != "request_correction" {
		t.Fatalf("public correction action = %q, want request_correction", action.ID)
	}
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", payload, owner, fixture.operator); err != nil {
		t.Fatalf("public correction path after contract supersession: %v", err)
	}
}

func TestWorkflowActionPreflightResolvesContractCorrectionAtCheckpoint(t *testing.T) {
	t.Parallel()
	const workID = "preflight-supersede-at-checkpoint"
	s, owner, _ := seedItemAtAcceptance(t, workID, false)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","verdict_kind":"ok"}`), 0, verdictReviewer(t, s, workID)); err != nil {
		t.Fatalf("record compatible verdict: %v", err)
	}

	version := verdictItemVersion(t, s, workID)
	payload := json.RawMessage(`{"contract_version":2,"premise":"corrected premise","outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:` + workID + `","expected_result":"pass"}}],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","supersede_reason":"correct the accepted premise","audit_evidence":["evidence:preflight"]}`)
	request := WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: version, StepID: "acceptance", ActionID: "supersede_contract", Payload: payload, Actor: owner}
	if err := InspectWorkflowActionAdmission(context.Background(), s, request); err != nil {
		t.Fatalf("contract correction preflight: %v", err)
	}

	request.ActionID = "undeclared_action"
	request.Payload = json.RawMessage(`{}`)
	if err := InspectWorkflowActionAdmission(context.Background(), s, request); err == nil {
		t.Fatal("undeclared action passed preflight")
	}
}

// A verdict that omits contract_version must resolve the active contract,
// not contract 1. After any supersession a caller that does not name a
// version records against the successor, so verdict and confirmation read
// one approval set instead of deadlocking between versions.
func TestVerdictOmittingContractVersionResolvesActiveContract(t *testing.T) {
	t.Parallel()
	const workID = "verdict-version-default-supersede"
	s, owner, _ := seedItemAtAcceptance(t, workID, false)
	reviewer := verdictReviewer(t, s, workID)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","verdict_kind":"ok"}`), 0, reviewer); err != nil {
		t.Fatalf("record compatible verdict: %v", err)
	}
	seedComparisonObservation(t, s, workID)
	operator := operatorVerdictActor(t, workID)
	successor := json.RawMessage(`{"contract_version":2,"premise":"corrected premise","outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:` + workID + `","expected_result":"pass"}},{"predicate_id":"predicate:added","ordinal":1,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:added","immutable_subject_ref":"commit:` + workID + `-added","expected_result":"pass"}}],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","supersede_reason":"add a successor predicate","audit_evidence":["evidence:verdict-default"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", successor, owner, operator); err != nil {
		t.Fatalf("supersede contract: %v", err)
	}
	before := contractCorrectionEffectSnapshot(t, s, workID)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"predicate_id":"predicate:added","verdict_kind":"ok"}`), 0, reviewer); !hasFailureKind(err, KindStaleRequiresReview) {
		t.Fatalf("successor healthy verdict before production = %v, want stale_requires_review", err)
	}
	if got := contractCorrectionEffectSnapshot(t, s, workID); got != before {
		t.Fatalf("stale successor refusal changed effects: before %s, after %s", before, got)
	}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"predicate_id":"predicate:added","verdict_kind":"outcome_mismatch"}`), 0, reviewer); err != nil {
		t.Fatalf("record unhealthy successor verdict without contract_version: %v", err)
	}
	var verdictEvidence string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.evaluation_evidence[0]') FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowVerdictRecorded).Scan(&verdictEvidence); err != nil {
		t.Fatal(err)
	}
	correction := json.RawMessage(`{"diagnosis":"the successor requires a fresh artifact","strategy":"repeat execution under contract 2","predicate_ids":["predicate:added"],"evidence_refs":["` + verdictEvidence + `"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", correction, owner, operator); err != nil {
		t.Fatalf("request successor correction: %v", err)
	}
	produceContractCorrectionWorker(t, s, workID, "successor", owner)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"predicate_id":"predicate:added","verdict_kind":"ok"}`), 0, reviewer); err != nil {
		t.Fatalf("verdict without contract_version must resolve the active contract: %v", err)
	}
	var recorded int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.contract_version') FROM domain_events WHERE subject_id=? AND kind='workflow.verdict_recorded' ORDER BY seq DESC LIMIT 1`, workID).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 2 {
		t.Fatalf("verdict contract_version=%d, want 2", recorded)
	}
}
