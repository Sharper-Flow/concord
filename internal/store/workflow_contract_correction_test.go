package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func runIssue933OperatorAction(t *testing.T, s *Store, workID, action string, payload json.RawMessage, owner WorkflowActor) error {
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
	_, err = applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64), IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
		ContractDigest: testManifestDigest, Now: time.Unix(20, version).UTC(),
	})
	if err != nil {
		return err
	}
	return tx.Commit()
}

func TestIssue933OperatorActionSupersedesAsCurrentCoordinator(t *testing.T) {
	const workID = "issue933-current-coordinator"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	defer fixture.store.Close()
	successor := returnRouteSuccessorPayload(2, workID, 1)
	if err := runIssue933OperatorAction(t, fixture.store, workID, "supersede_contract", successor, fixture.owner); err != nil {
		t.Fatalf("current-policy coordinator supersession: %v", err)
	}
	if version, active := activeContractState(t, fixture.store, workID); version != 2 || active != 1 {
		t.Fatalf("active contract version=%d count=%d, want 2/1", version, active)
	}
}

func contractCorrectionEffectSnapshot(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var version, events, operations int64
	var step string
	if err := s.db.QueryRow(`SELECT w.version,i.current_step,(SELECT count(*) FROM effect_receipts WHERE subject_id=w.id),(SELECT count(*) FROM durable_operations WHERE work_id=w.id) FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&version, &step, &events, &operations); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("version=%d step=%s events=%d operations=%d", version, step, events, operations)
}

// Successors carry the current authoring shape, including the explicit
// decision-domain fact. The original predicate content is copied, not relabeled.
func pContractSuccessor(t *testing.T, s *Store, id, premise string) map[string]any {
	t.Helper()
	const version = int64(2)
	contract, err := readWorkflowContractVersion(t.Context(), s.db, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	predicates := make([]any, len(contract.OutcomePredicates))
	for i, predicate := range contract.OutcomePredicates {
		var outcome map[string]any
		decoder := json.NewDecoder(strings.NewReader(predicate.OutcomePayload))
		decoder.UseNumber()
		if err := decoder.Decode(&outcome); err != nil {
			t.Fatal(err)
		}
		predicates[i] = map[string]any{"predicate_id": predicate.PredicateID, "ordinal": predicate.Ordinal, "outcome_kind": predicate.OutcomeKind, "outcome_payload": outcome}
	}
	payload := map[string]any{"contract_version": version, "premise": premise, "supersede_reason": "correct the accepted premise", "audit_evidence": []string{"evidence:return-route-artifact"}, "outcome_predicates": predicates, "required_evidence": contract.RequiredEvidence, "route_conventions": contract.RouteConventions, "spec_mandate": contract.SpecMandate, "law_modifies": contract.LawModifies, "rigor_class": contract.RigorClass}
	if contract.DecisionDomains != nil {
		payload["decision_domains"] = contract.DecisionDomains
	}
	if contract.ArchitectureBinding != nil {
		payload["architecture_binding"] = contract.ArchitectureBinding
	}
	return payload
}

func pContractCriterionFixture(t *testing.T, id string) workflowReturnRouteFixture {
	t.Helper()
	f, ref := seedCriterionAssociationAtStep(t, id, mustBuiltinDefinition(t, "workflow.research"), "conclude")
	if err := runVerdictActionAs(t, f.store, id, "record_verdict", criterionAssociationPayload(ref, "predicate:first", "predicate:second"), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	seedComparisonObservation(t, f.store, id)
	return f
}

func TestIssue933LateVerdictRecoveryHoldsStepAndRefusesHealthyReplacement(t *testing.T) {
	t.Parallel()
	const id = "issue933-late-verdict"
	f, ref := seedLateCriterionAssociation(t, id)
	payload := criterionAssociationPayload(ref, "predicate:first")
	if err := runVerdictActionAs(t, f.store, id, "record_verdict", payload, 0, f.owner); err != nil {
		t.Fatal(err)
	}
	reviewGateRequireStep(t, f.store, id, "complete")
	before := contractCorrectionEffectSnapshot(t, f.store, id)
	for _, payload := range []json.RawMessage{payload, criterionAssociationPayload(ref, "predicate:second", "predicate:first")} {
		for _, err := range []error{issue1013Preflight(t, f.store, id, "record_verdict", payload, f.owner), runVerdictActionAs(t, f.store, id, "record_verdict", payload, 0, f.owner)} {
			if !hasFailureKind(err, KindIllegalLifecycleTransition) {
				t.Fatalf("applicable association replacement=%v, want off-step refusal", err)
			}
		}
	}
	if after := contractCorrectionEffectSnapshot(t, f.store, id); after != before {
		t.Fatalf("replacement changed effects: before %s, after %s", before, after)
	}
}

// A premise correction preserves proof whose criterion content is unchanged,
// without rewriting the proof's original contract provenance.
func TestIssue933PremiseRevisionUsesTypedContractSupersession(t *testing.T) {
	t.Parallel()
	const id = "issue933-contract-correction"
	f := pContractCriterionFixture(t, id)
	if !issue1013HasIntent(issue1013Pin(t, f.store, id), "confirm_premise") {
		t.Fatal("proved criteria did not open premise confirmation")
	}
	successor := pContractSuccessor(t, f.store, id, "corrected premise")
	if err := runVerdictActionAs(t, f.store, id, "supersede_contract", mustJSONValue(successor), 0, f.owner); err != nil {
		t.Fatalf("typed premise supersession: %v", err)
	}
	var count, active int
	var premise string
	if err := f.store.db.QueryRow(`SELECT count(*),contract_version,premise FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, id).Scan(&count, &active, &premise); err != nil {
		t.Fatal(err)
	}
	if count != 1 || active != 2 || premise != "corrected premise" {
		t.Fatalf("active contract count=%d version=%d premise=%q", count, active, premise)
	}
	reviewGateRequireStep(t, f.store, id, "conclude")
	verdicts, err := latestWorkflowVerdicts(t.Context(), f.store.db, id, 2)
	if err != nil || len(verdicts) != 2 {
		t.Fatalf("unchanged criterion associations=%+v err=%v", verdicts, err)
	}
	for _, association := range verdicts {
		if association.ContractVersion != 1 {
			t.Fatal("compatible proof provenance was rewritten")
		}
		if err := verifyWorkflowCriterionAssociation(t.Context(), f.store.db, id, association); err != nil {
			t.Fatal(err)
		}
	}
	// The premise revision changed no criterion digest, so the preserved
	// proof still answers the successor and needs no repeated production
	// (CD-0242 D7).
	if !issue1013HasIntent(issue1013Pin(t, f.store, id), "confirm_premise") {
		t.Fatal("unchanged-criterion revision withheld premise confirmation")
	}
	before := contractCorrectionEffectSnapshot(t, f.store, id)
	err = WorkflowActionPreflightWithRegistry(t.Context(), f.store, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{WorkID: id, ExpectedVersion: readWorkVersion(t, f.store, id), ActionID: "confirm_premise", Payload: json.RawMessage(`{}`), Actor: f.owner})
	if err != nil {
		t.Fatalf("premise confirmation on preserved proof: %v", err)
	}
	if contractCorrectionEffectSnapshot(t, f.store, id) != before {
		t.Fatal("confirm_premise preflight changed durable state")
	}
}

func TestIssue933CorrectionPreflightAfterContractSupersession(t *testing.T) {
	t.Parallel()
	const id = "issue933-correction-preflight"
	j := pReturnReviewFixture(t, id, "workflow.implementation", "correction_required")
	owner, err := WorkflowActorRef(j.fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(t.Context(), j.s, id, "acceptance", owner); err != nil {
		t.Fatal(err)
	}
	successor := pContractSuccessor(t, j.s, id, "corrected premise")
	if err := runVerdictActionAs(t, j.s, id, "supersede_contract", mustJSONValue(successor), 0, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	payload := pReviewCorrectionPayload(t)
	before := j.effects(t)
	if err := WorkflowActionPreflightWithRegistry(ctx, j.s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{WorkID: id, ActionID: "request_correction", Payload: payload, Actor: j.fixture.owner}); err != nil {
		t.Fatalf("public correction preflight after supersession: %v", err)
	}
	_, action, err := WorkflowActionDefinitionFor(ctx, j.s, BuiltinWorkflowRegistry(), id, "request_correction")
	if err != nil || action.ID != "request_correction" {
		t.Fatalf("public correction discovery=%+v err=%v", action, err)
	}
	if j.effects(t) != before {
		t.Fatal("public read paths changed durable state")
	}
	if err := j.action(t, "request_correction", payload, j.fixture.owner); err != nil {
		t.Fatalf("public correction after supersession: %v", err)
	}
	reviewGateRequireStep(t, j.s, id, "execution")
}

func TestWorkflowActionPreflightResolvesContractCorrectionAtCheckpoint(t *testing.T) {
	t.Parallel()
	const id = "preflight-supersede-at-checkpoint"
	f := pContractCriterionFixture(t, id)
	payload := mustJSONValue(pContractSuccessor(t, f.store, id, "corrected premise"))
	request := WorkflowActionPreflightRequest{WorkID: id, ExpectedVersion: readWorkVersion(t, f.store, id), StepID: "conclude", ActionID: "supersede_contract", Payload: payload, Actor: f.owner}
	before := contractCorrectionEffectSnapshot(t, f.store, id)
	if err := WorkflowActionPreflightWithRegistry(t.Context(), f.store, BuiltinWorkflowRegistry(), request); err != nil {
		t.Fatalf("contract correction preflight: %v", err)
	}
	request.ActionID = "undeclared_action"
	request.Payload = json.RawMessage(`{}`)
	if err := WorkflowActionPreflightWithRegistry(t.Context(), f.store, BuiltinWorkflowRegistry(), request); !hasFailureKind(err, KindIllegalLifecycleTransition) {
		t.Fatalf("undeclared action preflight=%v", err)
	}
	if contractCorrectionEffectSnapshot(t, f.store, id) != before {
		t.Fatal("preflight changed effects")
	}
}

func TestVerdictOmittingContractVersionResolvesActiveContract(t *testing.T) {
	t.Parallel()
	const id = "verdict-version-default-supersede"
	f, ref := seedCriterionAssociationAtStep(t, id, mustBuiltinDefinition(t, "workflow.research"), "investigate")
	successor := pContractSuccessor(t, f.store, id, "corrected premise")
	predicates := successor["outcome_predicates"].([]any)
	successor["outcome_predicates"] = append(predicates, map[string]any{"predicate_id": "predicate:added", "ordinal": 2, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}})
	if err := runVerdictActionAs(t, f.store, id, "supersede_contract", mustJSONValue(successor), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	before := contractCorrectionEffectSnapshot(t, f.store, id)
	// The supersession stales nothing by itself (CD-0242 D7); a verdict
	// before research production is refused because investigate does not
	// declare it.
	if err := runVerdictActionAs(t, f.store, id, "record_verdict", criterionAssociationPayload(ref, "predicate:added"), 0, f.owner); !hasFailureKind(err, KindIllegalLifecycleTransition) {
		t.Fatalf("successor association before research production=%v, want the off-step refusal", err)
	}
	if contractCorrectionEffectSnapshot(t, f.store, id) != before {
		t.Fatal("stale successor association changed durable state")
	}
	if err := runVerdictActionAs(t, f.store, id, "record_finding", json.RawMessage(`{}`), 0, f.owner); err != nil {
		t.Fatalf("successor research production: %v", err)
	}
	if err := runVerdictActionAs(t, f.store, id, "record_report", json.RawMessage(`{"evidence_kind":"artifact","evidence_ref":"evidence:return-route-artifact"}`), 0, f.owner); err != nil {
		t.Fatalf("successor research report: %v", err)
	}
	if err := runVerdictActionAs(t, f.store, id, "record_verdict", criterionAssociationPayload(ref, "predicate:first", "predicate:second", "predicate:added"), 0, f.owner); err != nil {
		t.Fatalf("association without contract_version: %v", err)
	}
	var recorded int64
	if err := f.store.db.QueryRow(`SELECT contract_version FROM workflow_predicate_verdicts WHERE work_id=? ORDER BY verdict_seq DESC LIMIT 1`, id).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 2 {
		t.Fatalf("association contract_version=%d, want 2", recorded)
	}
	associations, err := latestWorkflowVerdicts(t.Context(), f.store.db, id, 2)
	if err != nil || len(associations) != 3 {
		t.Fatalf("successor conjunction=%+v err=%v", associations, err)
	}
}

// A recorded upstream-revision obligation keeps contract repair reachable.
func TestCurrentReviewKeepsContractCorrectionAfterProductiveAccept(t *testing.T) {
	const id = "correction-open-after-productive-accept"
	j := pReturnReviewFixture(t, id, "workflow.break_fix", "revision_required")
	owner, err := WorkflowActorRef(j.fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(t.Context(), j.s, id, "refine", owner); err != nil {
		t.Fatal(err)
	}
	reviewGateStartStep(t, j.s, id, "refine", "start_refine", j.fixture.owner)
	completeCurrentReviewForTest(t, j.fixture, "revision_required")
	successor := pContractSuccessor(t, j.s, id, "corrected premise after accepted Review")
	if err := runVerdictActionAs(t, j.s, id, "supersede_contract", mustJSONValue(successor), 0, j.fixture.owner); err != nil {
		t.Fatalf("supersede after productive acceptance: %v", err)
	}
}
