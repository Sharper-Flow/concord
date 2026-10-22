package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

func runIssue933OperatorAction(t *testing.T, s *Store, workID, action string, payload json.RawMessage, owner WorkflowActor, operator WorkflowActor) error {
	t.Helper()
	ctx, err := prepareTerminalTestContext(context.Background(), s, workID, action)
	if err != nil {
		return err
	}
	version := readWorkVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	operationID := fmt.Sprintf("issue933-%s-%s-%d", action, workID, version)
	_, err = applyWorkflowActionRawTx(ctx, tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
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
	if _, err := dispatchJoinAttempt(ctx, t, s, workID, readWorkVersion(t, s, workID), owner, packet); err != nil {
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
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{workerCompleteEventForLane(workID, "correction-completed-"+workID+"-"+label, attemptID, lane, time.Now().UTC(), workerFixtureDirectory(t, s, workID))}}); err != nil {
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
	fixture, ref := seedLateCriterionAssociation(t, workID)
	s, owner := fixture.store, fixture.owner
	payload := criterionAssociationPayload(ref, "predicate:first")
	if err := runVerdictActionAs(t, s, workID, "record_verdict", payload, 0, owner); err != nil {
		t.Fatalf("late criterion association: %v", err)
	}
	if step := currentStep(t, s, workID); step != "complete" {
		t.Fatalf("association moved to %s instead of holding complete", step)
	}
	before := contractCorrectionEffectSnapshot(t, s, workID)
	// Another criterion is still missing: the off-step route stays open, but
	// cannot be used to replace an already applicable association in a batch.
	for _, payload := range []json.RawMessage{payload, criterionAssociationPayload(ref, "predicate:second", "predicate:first")} {
		for _, err := range []error{issue1013Preflight(t, s, workID, "record_verdict", payload, owner), runVerdictActionAs(t, s, workID, "record_verdict", payload, 0, owner)} {
			if !hasFailureKind(err, KindIllegalLifecycleTransition) {
				t.Fatalf("applicable association replacement=%v, want off-step refusal", err)
			}
		}
	}
	after := contractCorrectionEffectSnapshot(t, s, workID)
	if after != before {
		t.Fatalf("refused association replacement changed effects: before %s, after %s", before, after)
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
	question, err := readOperatorQuestionFromWorkPinForTest(context.Background(), s, workID)
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
	var binding *WorkflowRetryApprovalBinding
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		var txErr error
		binding, txErr = WorkflowFailedWorkerRetryBindingTx(context.Background(), tx, nil, workID)
		return txErr
	}); err != nil {
		t.Fatalf("public correction path after contract supersession: %v", err)
	} else if binding != nil {
		t.Fatalf("correction path returned a retry binding without a failed worker: %#v", binding)
	}
	question, err = readOperatorQuestionFromWorkPinForTest(context.Background(), s, workID)
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
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"verdicts":[{"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}]}`), 0, reviewer); err != nil {
		t.Fatalf("record return-route mismatch verdict: %v", err)
	}
	seedComparisonObservation(t, s, workID)
	question, err := readOperatorQuestionFromWorkPinForTest(context.Background(), s, workID)
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
	if err := WorkflowActionPreflightWithRegistry(ctx, s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
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

	version := readWorkVersion(t, s, workID)
	payload := json.RawMessage(`{"contract_version":2,"premise":"corrected premise","outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:` + workID + `","expected_result":"pass"}}],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","supersede_reason":"correct the accepted premise","audit_evidence":["evidence:preflight"]}`)
	request := WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: version, StepID: "acceptance", ActionID: "supersede_contract", Payload: payload, Actor: owner}
	if err := WorkflowActionPreflightWithRegistry(context.Background(), s, BuiltinWorkflowRegistry(), request); err != nil {
		t.Fatalf("contract correction preflight: %v", err)
	}

	request.ActionID = "undeclared_action"
	request.Payload = json.RawMessage(`{}`)
	if err := WorkflowActionPreflightWithRegistry(context.Background(), s, BuiltinWorkflowRegistry(), request); err == nil {
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

// An escalated correction window keeps the contract-correction route open
// across a productive acceptance. The failure mode this pins: a supersession
// forces every worker job to re-record, the reworded revision is a different
// obligation whose acceptance cannot discharge the recorded debt (that rule
// is deliberate), and once correction closed after the acceptance no
// supersession could follow the last dispatch — so the escalated next
// dispatch had no convergence basis and the work item could never dispatch
// its next lane. The escape stays open exactly while the window is escalated.
func TestEscalatedWindowKeepsContractCorrectionAfterProductiveAccept(t *testing.T) {
	const id = "correction-open-after-productive-accept"
	f := seedWorkflowReturnRouteFixture(t, id, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	acceptor := reviewGateAcceptor(id)
	job := &WorkerJobBinding{JobID: "job:escalated-corrective", Revision: 1}

	// Three failed dispatches of the same revision escalate the window.
	for i := 1; i <= 3; i++ {
		attempt := fmt.Sprintf("attempt:%s:%d", id, i)
		pin := issue1013Pin(t, s, id)
		issue1013StartRepair(t, s, id, f.owner, pin.Version, latestStepStartEpoch(t, s, id, "repair")+1)
		pin = issue1013Pin(t, s, id)
		var correction *WorkflowCorrectionContext
		if pin.Correction != nil && pin.Correction.AttemptCount >= 1 {
			correction = pin.Correction
		}
		dispatchJobBoundAttempt(t, s, id, "repair", attempt, correction, f.owner, pin.Version, fmt.Sprintf("escalated-window-%d", i), job)
		s2WorkerFailedAttributedLive(t, s, id, attempt, "corrective objective remains unsatisfied")
	}
	pin := issue1013Pin(t, s, id)
	if pin.Correction == nil || !pin.Correction.Escalated {
		t.Fatalf("correction after three failed dispatches = %#v, want the escalated window", pin.Correction)
	}

	// The pre-acceptance supersession is the sanctioned convergence basis.
	// The fixture definition is Product-changing, so each successor carries
	// the active contract's architecture binding.
	var bindingRaw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind='workflow.contract_approved' ORDER BY seq DESC LIMIT 1`, id).Scan(&bindingRaw); err != nil {
		t.Fatal(err)
	}
	var approved struct {
		ArchitectureBinding json.RawMessage `json:"architecture_binding"`
	}
	if err := json.Unmarshal([]byte(bindingRaw), &approved); err != nil {
		t.Fatal(err)
	}
	successor := func(version int64, reason string) json.RawMessage {
		payload := map[string]any{
			"contract_version": version, "premise": "corrected premise " + strconv.FormatInt(version, 10),
			"outcome_predicates": []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check",
				"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + id, "expected_result": "pass"}}},
			"required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{},
			"law_modifies": []string{}, "rigor_class": "prototype_internal", "supersede_reason": reason,
			"audit_evidence":       []string{"evidence:escalated-window-correction"},
			"architecture_binding": approved.ArchitectureBinding,
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if err := runIssue933OperatorAction(t, s, id, "supersede_contract", successor(2, "approach changed after the failures"), f.owner, operatorVerdictActor(t, id)); err != nil {
		t.Fatalf("supersede while escalated before acceptance: %v", err)
	}

	// The supersession forces a re-recorded revision; its reworded objective
	// is a different obligation, so its acceptance is productive for the
	// step while the earlier debt survives by design.
	if err := recordWorkerJobFieldsForTest(t, s, id, f.owner, map[string]any{
		"job_id": job.JobID, "objective": "corrected corrective objective under the successor contract",
		"stopping_condition": "the recorded checks pass with no unresolved reference",
		"path_scope":         []string{"internal/store"}, "checks": []string{"go test ./internal/store/"},
		"reserved_integration": "integration evidence binds at the parent effect step",
		"ready":                true, "readiness_evidence": []string{"evidence:coordinator-ready"},
	}); err != nil {
		t.Fatalf("record reworded revision: %v", err)
	}
	revisions, err := readWorkerJobRevisions(context.Background(), s.db, id)
	if err != nil {
		t.Fatal(err)
	}
	replacement := revisions[len(revisions)-1].Binding
	pin = issue1013Pin(t, s, id)
	issue1013StartRepair(t, s, id, f.owner, pin.Version, latestStepStartEpoch(t, s, id, "repair")+1)
	pin = issue1013Pin(t, s, id)
	dispatchJobBoundAttempt(t, s, id, "repair", "attempt:"+id+":4", pin.Correction, f.owner, pin.Version, "escalated-window-4", &replacement)
	completeAndAcceptAttempt(t, s, id, "attempt:"+id+":4", BuiltinLaneDefinitions()[0], acceptor)

	// The regression: correction must stay open after the productive
	// acceptance while the window is escalated, so a fresh supersession can
	// follow the last dispatch and arm the next lane's convergence basis.
	if err := runIssue933OperatorAction(t, s, id, "supersede_contract", successor(3, "stage admission after the accepted delivery"), f.owner, operatorVerdictActor(t, id)); err != nil {
		t.Fatalf("supersede after a productive acceptance on the escalated window: %v", err)
	}
}
