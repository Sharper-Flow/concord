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

type workflowReturnRouteFixture struct {
	store    *Store
	owner    WorkflowActor
	operator WorkflowActor
}

func attributionInactiveWorkflowHeadForTest(t *testing.T, ref string) RegisteredDefinition {
	t.Helper()
	versions := map[string]int64{
		"workflow.implementation": 30,
		"workflow.break_fix":      27,
		"workflow.research":       17,
	}
	version, ok := versions[ref]
	if !ok {
		t.Fatalf("no pre-attribution head is pinned for %s", ref)
	}
	registered, ok := BuiltinWorkflowRegistry().Lookup(ref, version)
	if !ok {
		t.Fatalf("workflow definition %s@%d is not registered", ref, version)
	}
	return registered
}

func seedWorkflowReturnRouteFixture(t *testing.T, workID, definitionRef, verdictStep string) workflowReturnRouteFixture {
	t.Helper()
	return seedWorkflowReturnRouteFixtureRequiring(t, workID, definitionRef, verdictStep, []string{"verification"}, []string{"verification", "review", "artifact"})
}

func seedWorkflowReturnRouteFixtureRequiring(t *testing.T, workID, definitionRef, verdictStep string, requiredEvidence, boundKinds []string) workflowReturnRouteFixture {
	t.Helper()
	return seedWorkflowReturnRouteFixtureWithDefinition(t, workID, legacyApprovalPolicyDefinitionForRef(t, definitionRef), verdictStep, requiredEvidence, boundKinds)
}

func seedHistoricalWorkflowReturnRouteFixture(t *testing.T, workID, definitionRef string, definitionVersion int64, verdictStep string) workflowReturnRouteFixture {
	t.Helper()
	registered, ok := BuiltinWorkflowRegistry().Lookup(definitionRef, definitionVersion)
	if !ok {
		t.Fatalf("workflow definition %s@%d is not registered", definitionRef, definitionVersion)
	}
	return seedWorkflowReturnRouteFixtureWithDefinition(t, workID, registered, verdictStep, []string{"verification"}, []string{"verification", "review", "artifact"})
}

func seedWorkflowReturnRouteFixtureWithDefinition(t *testing.T, workID string, registered RegisteredDefinition, verdictStep string, requiredEvidence, boundKinds []string, predicates ...map[string]any) workflowReturnRouteFixture {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	operatorRef, err := WorkflowActorRef(operator)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: owner, Now: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	version := int64(4)
	nextEvent := func(event Event) Event {
		version++
		return event
	}
	events := []Event{nextEvent(workflowEventWithActor("return-operator-"+workID, WorkflowActorRecorded, workID, operatorRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"actor_ref": operatorRef, "principal_ref": operator.PrincipalRef, "client_ref": operator.ClientRef,
		"agent_ref": operator.AgentRef, "session_ref": operator.SessionRef, "actor_class": string(operator.ActorClass),
	}))}
	outcomePredicate := map[string]any{
		"predicate_id": "predicate:return-route", "ordinal": 0, "outcome_kind": "check",
		"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:return-route", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
	}
	checkAdmitted := false
	for _, kind := range registered.Definition.OutcomeSchema.AllowedKinds {
		if kind == PredicateCheck {
			checkAdmitted = true
		}
	}
	if !checkAdmitted {
		outcomePredicate["outcome_kind"] = "outcome"
		outcomePredicate["outcome_payload"] = map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}
	}
	if len(predicates) == 0 {
		predicates = []map[string]any{outcomePredicate}
	}
	contractPayload := map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"contract_version": 1, "premise": "deliver the checked change", "outcome_kind": "check",
		"outcome_predicates": predicates,
		"required_evidence":  requiredEvidence, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
		"law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1, "rigor_class": "prototype_internal",
		"consequence_class": "internal_sqlite",
	}
	if registered.Definition.ChangesProductTruth != nil && *registered.Definition.ChangesProductTruth {
		contractPayload["architecture_binding"] = WorkflowArchitectureBinding{
			DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"},
			DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{},
			VerificationObligations: []WorkflowVerificationObligation{},
		}
	}
	contract := workflowEventWithActor("return-contract-"+workID, WorkflowContractApproved, workID, ownerRef, contractPayload)
	events = append(events, nextEvent(withPinnedDecisionFact(t, registered.Definition, contract)))
	for _, kind := range boundKinds {
		evidenceRef := "evidence:return-route-" + kind
		seedWorkflowAuthority(t, s, "return-authority-"+kind+"-"+workID, workID, "principal/return-route-"+kind, "request/return-route-"+kind, []string{evidenceRef})
		events = append(events, nextEvent(workflowEventWithActor("return-evidence-"+kind+"-"+workID, WorkflowEvidenceBound, workID, ownerRef, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1, "evidence_kind": kind,
			"immutable_subject_ref": evidenceRef, "producer_id": "principal/return-route-" + kind, "producer_run_ref": "return-authority-" + kind + "-" + workID,
			"producer_watermark": "request/return-route-" + kind, "observed_at": "2026-09-12T00:00:00Z",
		})))
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 4}}); err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(ctx, s, workID, verdictStep, ownerRef); err != nil {
		t.Fatal(err)
	}
	return workflowReturnRouteFixture{store: s, owner: owner, operator: operator}
}

func TestWorkflowActionPreflightNamesEveryMissingAndUndeclaredField(t *testing.T) {
	t.Parallel()
	payload := json.RawMessage(`{"predicate_ids":["predicate:primary"],"evidence_refs":["evidence:x"],"zz_extra":true}`)
	want := `workflow action payload field "diagnosis" is required for action "request_correction"; workflow action payload field "strategy" is required for action "request_correction"; workflow action payload field "zz_extra" is not declared for action "request_correction"`
	for range 50 {
		err := validateWorkflowActionPayload(WorkflowDefinition{}, "request_correction", payload)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidPayload || failure.Detail != want || failure.RecoveryAction != "supply every required registered action field and use only fields declared by the pinned definition" {
			t.Fatalf("structural preflight=%v, want deterministic complete refusal", err)
		}
	}
}

func TestPReviewCorrectionAdmitsAdvertisedEvidence(t *testing.T) {
	j := pReturnReviewFixture(t, "p-return-advertised-evidence", "workflow.break_fix", "correction_required")
	entry := mustBuiltinDefinition(t, "workflow.break_fix")
	correction, _, err := workflowCorrectionRequestAdmissionForRegistry(t.Context(), j.s.db, j.workID, entry.Definition, "repair", "test", 0, BuiltinWorkflowRegistry())
	if err != nil || correction == nil {
		t.Fatalf("advertised correction=%+v err=%v", correction, err)
	}
	// The context's predicate IDs bind the approved contract; the correction
	// payload itself cites the work's durably bound evidence, not the
	// accepted report's event reference.
	if len(correction.PredicateIDs) != 1 || correction.PredicateIDs[0] != "predicate:return-route" {
		t.Fatalf("advertised predicates=%v, want the approved predicate", correction.PredicateIDs)
	}
	payload := pReviewCorrectionPayload(t)
	if err := issue1013Preflight(t, j.s, j.workID, "request_correction", payload, j.fixture.owner); err != nil {
		t.Fatalf("correction with durably bound evidence refused: %v", err)
	}
	if err := j.action(t, "request_correction", payload, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	reviewGateRequireStep(t, j.s, j.workID, "repair")
}

func TestPReturnRouteCurrentReviewFixture(t *testing.T) {
	j := pReturnReviewFixture(t, "p-return-current-review", "workflow.break_fix", "correction_required")
	pin, err := ReadWorkPin(t.Context(), j.s, j.workID)
	if err != nil || pin.Review == nil || pin.Review.Accepted == nil || pin.Step != "repair" || !pin.Review.DependenciesCurrent || pin.Review.Candidate == nil {
		t.Fatalf("current accepted Review fixture=%+v err=%v", pin, err)
	}
}

func TestIssue1062PersistentMismatchReturnsImplementationAcceptanceToRefine(t *testing.T) {
	testWorkflowReturnRoute(t, "return-route-implementation", "workflow.implementation", "acceptance", "execution")
}
func TestIssue1062PersistentMismatchReturnsBreakFixVerificationToRefine(t *testing.T) {
	testWorkflowReturnRoute(t, "return-route-break-fix", "workflow.break_fix", "verify", "repair")
}

func testWorkflowReturnRoute(t *testing.T, id, family, verdictStep, effect string) {
	t.Helper()
	j := pReturnReviewFixture(t, id, family, "correction_required")
	entry := mustBuiltinDefinition(t, family)
	if workflowUnhealthyVerdictRouteTarget(entry.Definition, verdictStep) != effect {
		t.Fatal("checkpoint recovery lost its producer target")
	}
	for i := 0; i < 2; i++ {
		before := j.effects(t)
		err := j.action(t, "record_delivery", map[string]any{"delivery_artifact": "artifact:must-not-ship", "delivery_state": "asserted"}, j.fixture.owner)
		if !hasFailureKind(err, KindMissingEvidence) || !strings.Contains(err.Error(), "ready_for_final_proof") || j.effects(t) != before {
			t.Fatalf("blocking Review delivery=%v", err)
		}
		err = j.action(t, "complete", map[string]any{"impact_verdict": "non-breaking"}, j.fixture.owner)
		if !hasFailureKind(err, KindMissingEvidence) || !strings.Contains(err.Error(), "ready_for_final_proof") || j.effects(t) != before {
			t.Fatalf("blocking Review completion=%v", err)
		}
		if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
			t.Fatal(err)
		}
		reviewGateRequireStep(t, j.s, id, effect)
		pReturnProduce(t, j, fmt.Sprintf("correction-%d", i))
		acceptCurrentReviewForTest(t, j.fixture, j.repo, "correction_required")
	}
	acceptCurrentReviewForTest(t, j.fixture, j.repo, "ready_for_final_proof")
	pin := issue1013Pin(t, j.s, id)
	if pin.Review == nil || workflowAdmitReview(*pin.Review, "") != nil {
		t.Fatal("fresh accepted readiness did not settle the Review boundary")
	}
	if countWorkflowCompletionEvents(t, j.s, id) != 0 {
		t.Fatal("persistent correction completed the work")
	}
	reviewGateRequireStep(t, j.s, id, effect)
	proof := j.observeRun(t, "final-proof")
	if err := j.action(t, "bind_evidence", map[string]any{"evidence_kind": "verification", "evidence_ref": proof.OperationRef}, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	if err := reviewGateRecordDelivery(t, j.s, id, j.fixture.owner); err != nil {
		t.Fatalf("fresh ready Review and producer receipt did not admit final proof: %v", err)
	}
	reviewGateRequireStep(t, j.s, id, "refine")
}

func testPReviewCorrection(t *testing.T, id, family, effect string) {
	t.Helper()
	j := pReturnReviewFixture(t, id, family, "correction_required")
	entry := mustBuiltinDefinition(t, family)
	pin := issue1013Pin(t, j.s, id)
	if pin.Review == nil || pin.Review.Accepted == nil || !issue1013HasIntent(pin, "request_correction") {
		t.Fatalf("accepted Review has no correction route: %+v", pin)
	}
	before := j.effects(t)
	payload := pReviewCorrectionPayload(t)
	if err := issue1013Preflight(t, j.s, id, "request_correction", payload, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	if j.effects(t) != before {
		t.Fatal("preflight changed durable state")
	}
	if err := j.action(t, "request_correction", payload, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	reviewGateRequireStep(t, j.s, id, effect)
	correction, err := workflowCorrectionContextForDispatch(t.Context(), j.s.db, id, effect, "attempt:next")
	if err != nil || correction == nil || len(correction.PredicateIDs) != 1 || correction.PredicateIDs[0] != "predicate:return-route" {
		t.Fatalf("correction context=%+v err=%v", correction, err)
	}
	version, digest := reviewGateInstancePin(t, j.s, id)
	if version != entry.Definition.Version || digest != entry.Digest {
		t.Fatal("correction changed the current definition pin")
	}
	before = j.effects(t)
	if err := issue1013Preflight(t, j.s, id, "request_correction", payload, j.fixture.owner); err == nil {
		t.Fatal("repeated correction passed preflight before a fresh Review")
	}
	if j.effects(t) != before {
		t.Fatal("refused repeated correction changed durable state")
	}
}

func TestNonOKVerdictCorrectionReturnsImplementationToExecution(t *testing.T) {
	testPReviewCorrection(t, "return-route-verdict-correction", "workflow.implementation", "execution")
}
func TestNonOKVerdictCorrectionReturnsBreakFixToRepair(t *testing.T) {
	testPReviewCorrection(t, "return-route-break-fix-correction", "workflow.break_fix", "repair")
}

func TestVerdictCorrectionRequiresAcceptedWorkerDelivery(t *testing.T) {
	j := pReturnReviewFixture(t, "return-route-requires-accepted-delivery", "workflow.implementation", "")
	attempt, epoch := completeCurrentReviewForTest(t, j.fixture, j.repo, "correction_required")
	before := j.effects(t)
	if err := issue1013Preflight(t, j.s, j.workID, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err == nil {
		t.Fatal("an unaccepted Review authorized correction")
	}
	if j.effects(t) != before {
		t.Fatal("refused correction changed durable state")
	}
	if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	if err := issue1013Preflight(t, j.s, j.workID, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
		t.Fatalf("accepted current Review did not authorize correction: %v", err)
	}
}

func prepareVerdictCorrectionFixture(t *testing.T, id string) workflowReturnRouteFixture {
	t.Helper()
	j := pReturnReviewFixture(t, id, "workflow.implementation", "correction_required")
	return workflowReturnRouteFixture{store: j.s, owner: j.fixture.owner, operator: j.fixture.operator}
}

func TestVerdictCorrectionRefusesIncompleteAuthority(t *testing.T) {
	cases := []struct {
		name, payload string
		kind          FailureKind
		detail        string
	}{
		{"missing fields", `{}`, KindInvalidPayload, `is required for action "request_correction"`},
		{"diagnosis absent", `{"strategy":"strategy","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`, KindInvalidPayload, `field "diagnosis" is required`},
		{"strategy absent", `{"diagnosis":"diagnosis","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`, KindInvalidPayload, `field "strategy" is required`},
		{"predicate outside active contract", `{"diagnosis":"diagnosis","strategy":"strategy","predicate_ids":["predicate:outside"],"evidence_refs":["evidence:return-route-verification"]}`, KindInvalidPayload, "without a current non-ok verdict"},
		{"evidence is not bound", `{"diagnosis":"diagnosis","strategy":"strategy","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:not-bound"]}`, KindMissingEvidence, "not durably bound"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "return-route-refusal-" + strings.ReplaceAll(tc.name, " ", "-")
			f := prepareVerdictCorrectionFixture(t, id)
			before := contractCorrectionEffectSnapshot(t, f.store, id)
			err := issue1013Preflight(t, f.store, id, "request_correction", json.RawMessage(tc.payload), f.owner)
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != tc.kind || !strings.Contains(failure.Detail, tc.detail) {
				t.Fatalf("correction refusal=%v, want %s containing %q", err, tc.kind, tc.detail)
			}
			if contractCorrectionEffectSnapshot(t, f.store, id) != before {
				t.Fatal("refused correction changed durable state")
			}
		})
	}
}

// Main's engineering correction is not an operator-verdict substitution.
func TestVerdictCorrectionUsesMainAcceptedReviewWithoutOperatorSubstitution(t *testing.T) {
	const id = "return-route-without-operator"
	f := prepareVerdictCorrectionFixture(t, id)
	payload := json.RawMessage(`{"diagnosis":"diagnosis","strategy":"strategy","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	if err := runVerdictActionAs(t, f.store, id, "request_correction", payload, 0, f.owner); err != nil {
		t.Fatalf("Main correction from accepted Review: %v", err)
	}
	reviewGateRequireStep(t, f.store, id, "execution")
}

// Only the current family definition can supply executable correction authority.
func TestCurrentBreakFixPinSupportsReviewCorrection(t *testing.T) {
	testPReviewCorrection(t, "return-route-current-break-fix", "workflow.break_fix", "repair")
	if _, ok := BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 7); ok {
		t.Fatal("retired break-fix definition remains registered")
	}
}

func runCorrectionActionWithoutOperator(s *Store, workID string, owner WorkflowActor, payload json.RawMessage) error {
	ctx := context.Background()
	var version int64
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		return err
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	op := "correction-without-operator-" + workID
	_, err = applyWorkflowActionRawTx(ctx, tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: version, ActionID: "request_correction", Payload: payload, Actor: owner, AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64), IdempotencyIdentity: op, OperationID: op, PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: op, RequestID: "request:" + op, ContractDigest: testManifestDigest, Now: time.Unix(20, version).UTC()})
	return err
}

func TestVerdictCorrectionSequenceNeedsConjunctiveHealthyVerdicts(t *testing.T) {
	const id = "return-route-conjunctive-sequence"
	f, ref := seedLateCriterionAssociation(t, id)
	if err := runVerdictActionAs(t, f.store, id, "record_verdict", criterionAssociationPayload(ref, "predicate:first"), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	predicates, err := workflowActiveContractPredicateIDs(t.Context(), f.store.db, id, "test")
	if err != nil {
		t.Fatal(err)
	}
	verdicts, err := latestWorkflowVerdicts(t.Context(), f.store.db, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(predicates) != 2 || len(verdicts) != 1 {
		t.Fatalf("partial proof predicates=%v associations=%+v", predicates, verdicts)
	}
	if !issue1013HasIntent(issue1013Pin(t, f.store, id), "record_verdict") {
		t.Fatal("partial proof closed missing-proof route")
	}
	if err := runVerdictActionAs(t, f.store, id, "record_verdict", criterionAssociationPayload(ref, "predicate:second"), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	verdicts, err = latestWorkflowVerdicts(t.Context(), f.store.db, id, 1)
	if err != nil || len(verdicts) != 2 || issue1013HasIntent(issue1013Pin(t, f.store, id), "record_verdict") {
		t.Fatalf("complete conjunction=%+v err=%v", verdicts, err)
	}
}

func TestNonOKVerdictCorrectionReturnsTerminalImplementationToExecution(t *testing.T) {
	j := pReturnReviewFixture(t, "return-route-terminal-verdict-correction", "workflow.implementation", "correction_required")
	owner, err := WorkflowActorRef(j.fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(t.Context(), j.s, j.workID, "release", owner); err != nil {
		t.Fatal(err)
	}
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
		t.Fatalf("terminal correction from accepted Review: %v", err)
	}
	reviewGateRequireStep(t, j.s, j.workID, "execution")
}

func acceptReturnRouteWorker(t *testing.T, f workflowReturnRouteFixture, id string) WorkflowActor {
	return acceptReturnRouteWorkerLabeled(t, f, id, "")
}

func acceptReturnRouteWorkerLabeled(t *testing.T, f workflowReturnRouteFixture, id, label string) WorkflowActor {
	t.Helper()
	j := pReturnClaim(t, f, id)
	pReturnProduce(t, j, "producer"+label)
	acceptCurrentReviewForTest(t, j.fixture, j.repo, "ready_for_final_proof")
	return f.owner
}

func countWorkflowCompletionEvents(t *testing.T, s *Store, id string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, id, WorkflowCompleted).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func dispatchVerifyReviewAttempt(t *testing.T, f workflowReturnRouteFixture, id string) {
	t.Helper()
	seedCheckpointReviewAcceptedDelivery(t, f.store, id, f.owner, checkpointEffectStep(t, f.store, id, "verify"), "verify")
	lane := reviewGateLane(t, "review")
	laneVersion, laneDigest := registeredLaneIdentity(t, "review")
	attempt := "attempt:" + id + ":verify-review"
	packet := joinPacketFor(t, f.store, id, "verify", attempt, "review", laneVersion, laneDigest)
	if _, err := dispatchJoinAttempt(context.Background(), t, f.store, id, readWorkVersion(t, f.store, id), f.owner, packet); err != nil {
		t.Fatalf("review dispatch at the verify checkpoint refused: %v", err)
	}
	var digest string
	if err := f.store.db.QueryRow(`SELECT json_extract(payload,'$.worker_packet_digest') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, id, WorkflowActionCompleted).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	dispatch := Event{EventID: "verify-review-dispatch-" + id, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: id, Actor: "worker:test", OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerDispatched), Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attempt, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, PacketDigest: digest})}
	if err := f.store.Transact(context.Background(), func(tx *Transaction) error {
		prepared, err := PrepareLaneActorDispatch(context.Background(), tx, dispatch, f.owner.PrincipalRef, f.owner.ClientRef)
		if err != nil {
			return err
		}
		_, err = AppendLaneActorDispatchTx(context.Background(), tx, prepared)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), f.store, Operation{Events: []Event{workerCompleteEventForLane(id, "verify-review-completed-"+id, attempt, lane, time.Unix(31, 0).UTC(), workerFixtureDirectory(t, f.store, id))}}); err != nil {
		t.Fatal(err)
	}
}

func TestVerdictCorrectionAdmitsReviewEvidenceAccept(t *testing.T) {
	j := pReturnReviewFixture(t, "return-route-review-evidence-correction", "workflow.break_fix", "")
	attempt, epoch := completeCurrentReviewForTest(t, j.fixture, j.repo, "correction_required")
	if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
		t.Fatalf("correction after exact Review acceptance: %v", err)
	}
	reviewGateRequireStep(t, j.s, j.workID, "repair")
}

func TestVerdictCorrectionRefusesUnacceptedReviewDelivery(t *testing.T) {
	j := pReturnReviewFixture(t, "return-route-unaccepted-review-delivery", "workflow.break_fix", "")
	completeCurrentReviewForTest(t, j.fixture, j.repo, "correction_required")
	before := j.effects(t)
	entry := mustBuiltinDefinition(t, "workflow.break_fix")
	available, _, err := workflowCorrectionRequestAdmissionStateForRegistry(t.Context(), j.s.db, j.workID, entry.Definition, "repair", "return_route_test", 0, BuiltinWorkflowRegistry())
	if err != nil || available {
		t.Fatalf("unaccepted Review correction available=%t err=%v", available, err)
	}
	err = issue1013Preflight(t, j.s, j.workID, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner)
	if err == nil || j.effects(t) != before {
		t.Fatalf("unaccepted Review correction=%v", err)
	}
}
