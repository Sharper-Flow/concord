package store

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestWorkflowContractRecoveryPayloadAcceptsPredicateArray(t *testing.T) {
	t.Parallel()
	raw := mustJSONValue(map[string]any{
		"contract_version": 2, "premise": "continue with the corrected contract",
		"outcome_predicates": []map[string]any{{"predicate_id": "predicate:first", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:first", "immutable_subject_ref": "commit:first", "expected_result": "pass"}}},
		"required_evidence":  []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
		"rigor_class": "prototype_internal", "supersede_reason": "correct the accepted premise", "audit_evidence": []string{"evidence:audit"},
	})
	if err := validateWorkflowContractRecoveryPayload(WorkflowDefinition{}, raw); err != nil {
		t.Fatalf("predicate-array successor payload refused: %v", err)
	}
	legacyAndArray := mustJSONValue(map[string]any{
		"contract_version": 2, "premise": "ambiguous outcome shape", "outcome_predicates": []map[string]any{{"predicate_id": "predicate:first", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check"}}},
		"outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check"}, "required_evidence": []string{}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
		"rigor_class": "prototype_internal", "supersede_reason": "ambiguous", "audit_evidence": []string{"evidence:audit"},
	})
	if err := validateWorkflowContractRecoveryPayload(WorkflowDefinition{}, legacyAndArray); err == nil {
		t.Fatal("successor payload accepted both outcome shapes")
	}
}

func TestWorkflowPredicateHistoryCompatibilityRequiresUnchangedPayload(t *testing.T) {
	t.Parallel()
	history := workflowContractPredicateHistoryData{
		1: {"predicate:first": {PredicateID: "predicate:first", OutcomeKind: "check", OutcomePayload: `{"kind":"check","expected_result":"pass"}`}},
		2: {"predicate:first": {PredicateID: "predicate:first", OutcomeKind: "check", OutcomePayload: `{"expected_result":"pass","kind":"check"}`}},
	}
	verdict := workflowVerdictRecordedPayload{PredicateID: "predicate:first"}
	if !workflowPredicateHistoryCompatible(history, 1, 2, verdict.PredicateID, verdict) {
		t.Fatal("canonical-equivalent predicate payload was refused")
	}
	history[2]["predicate:first"] = WorkflowReadPredicate{PredicateID: "predicate:first", OutcomeKind: "check", OutcomePayload: `{"kind":"check","expected_result":"fail"}`}
	if workflowPredicateHistoryCompatible(history, 1, 2, verdict.PredicateID, verdict) {
		t.Fatal("changed predicate payload remained compatible")
	}
}

func TestWorkflowLawRevisionRecontractsThroughProductionRoutes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		mandate []string
		refused bool
	}{{"replace", []string{"spec:two"}, false}, {"drop", []string{}, false}, {"retain-deleted", []string{"spec:one"}, true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			workID := "law-recontract-route-" + tc.name
			s, completion := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}, includeVerdict: true, includePremise: true})
			actor := workflowActorForRef(t, s, completion.Actor)
			cutoverLawProjection(t, s)
			recordCompleteStepContradictionObservation(t, s, workID, actor)
			if _, action, err := WorkflowActionDefinitionFor(t.Context(), s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err != nil || action.ID != "supersede_contract" {
				t.Fatalf("discover correction: action=%+v err=%v", action, err)
			}
			binding, err := readWorkflowArchitectureBinding(t.Context(), s.db, workID, 1)
			if err != nil {
				t.Fatal(err)
			}
			successor := map[string]any{
				"contract_version": 2, "premise": "continue under successor law",
				"outcome_predicates": []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:successor-law", "immutable_subject_ref": "commit:successor-law", "expected_result": "pass"}}},
				"required_evidence":  []string{"verification", "review"}, "route_conventions": []string{"complete_step_correction"}, "spec_mandate": tc.mandate, "law_modifies": []string{}, "rigor_class": "prototype_internal",
				"supersede_reason": "move to the accepted successor law", "audit_evidence": []string{"evidence:law-cutover"}, "decision_domains": []string{}, "architecture_binding": binding,
			}
			version := readWorkVersion(t, s, workID)
			err = runVerdictActionAs(t, s, workID, "supersede_contract", mustJSONValue(successor), 0, actor)
			if tc.refused {
				var failure *Failure
				if !failureAs(err, &failure) || failure.Kind != KindProjectionNotFound || !strings.Contains(failure.Detail, "spec:one") {
					t.Fatalf("successor retaining deleted law = %v", err)
				}
				if got := readWorkVersion(t, s, workID); got != version {
					t.Fatalf("refused correction changed version: %d", got)
				}
				var count int
				if err := s.db.QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND contract_version=2`, workID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("refused correction wrote successor: %d %v", count, err)
				}
				if got, active := activeContractState(t, s, workID); got != 1 || active != 1 {
					t.Fatalf("refused correction changed active contract: %d/%d", got, active)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var mandateJSON string
			if err := s.db.QueryRow(`SELECT spec_mandate FROM workflow_contracts WHERE work_id=? AND contract_version=2`, workID).Scan(&mandateJSON); err != nil {
				t.Fatal(err)
			}
			var mandated []string
			if err := json.Unmarshal([]byte(mandateJSON), &mandated); err != nil || !reflect.DeepEqual(mandated, tc.mandate) {
				t.Fatalf("mandate=%s err=%v, want %v", mandateJSON, err, tc.mandate)
			}
			if got, active := activeContractState(t, s, workID); got != 2 || active != 1 {
				t.Fatalf("corrected contract: %d/%d", got, active)
			}
			var oldConfirmations, newConfirmations int
			if err := s.db.QueryRow(`SELECT count(*) FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=1`, workID).Scan(&oldConfirmations); err != nil || oldConfirmations != 1 {
				t.Fatalf("prior confirmations=%d err=%v", oldConfirmations, err)
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=2`, workID).Scan(&newConfirmations); err != nil || newConfirmations != 0 {
				t.Fatalf("new confirmations=%d err=%v", newConfirmations, err)
			}
			if got := currentStep(t, s, workID); got != "execution" {
				t.Fatalf("corrected step=%q", got)
			}
			version = readWorkVersion(t, s, workID)
			if err := runVerdictActionAs(t, s, workID, "start_execution", mustJSONValue(map[string]any{}), 0, actor); err != nil {
				t.Fatal(err)
			}
			if readWorkVersion(t, s, workID) <= version || currentStep(t, s, workID) != "execution" {
				t.Fatal("ordinary action did not advance the corrected work")
			}
		})
	}
}

func TestWorkflowLawRevisionAllowsTerminalLifecycleMutationAfterCutover(t *testing.T) {
	t.Parallel()
	workID := "law-terminal-route"
	s, _ := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}})
	cutoverLawProjection(t, s)
	version := readWorkVersion(t, s, workID)
	var currentLifecycle string
	if err := s.db.QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&currentLifecycle); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workTransitionEvent("law-terminal-cancel", workID, currentLifecycle, "cancelled", version, version+1)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	var lifecycle string
	if err := s.db.QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil || lifecycle != "cancelled" {
		t.Fatalf("lifecycle=%q err=%v", lifecycle, err)
	}
}

func seedLawRevisionPendingReview(t *testing.T, workID string) *Store {
	t.Helper()
	j := pReturnReviewFixture(t, workID, "workflow.implementation", "")
	f := j.fixture
	if _, err := f.store.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_contracts SET spec_mandate='["spec:one"]' WHERE work_id=? AND superseded_by IS NULL; DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, f.store, workID, "bind_evidence", mustJSONValue(map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": "spec:one"}), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	acceptCurrentReviewForTest(t, f, "continue")
	return f.store
}

func lawRevisionAcceptancePayload(t *testing.T, s *Store, workID string) map[string]any {
	t.Helper()
	state, err := readWorkflowReviewFacts(t.Context(), s.db, workID)
	if err != nil || state.Accepted == nil {
		t.Fatalf("accepted=%+v err=%v", state.Accepted, err)
	}
	return map[string]any{"review": state.Accepted.Report}
}

func cutoverLawProjection(t *testing.T, s *Store) {
	t.Helper()
	home := lawChangeFixtureHome(t, s)
	removeCommittedFixtureLaw(t, home)
	commitFixtureLawFile(t, home, "spec:two", fixtureLawFolderLaw("spec:two"))
}

func TestWorkflowLawRevisionRefusesProofReadBeforeCutover(t *testing.T) {
	t.Parallel()
	const workID = "law-proof-before-cutover"
	s := seedLawRevisionPendingReview(t, workID)
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
	if err != nil {
		t.Fatal(err)
	}
	cutoverLawProjection(t, s)
	_, err = lawSnapshotForSource(ctx, lawChangeFixtureHome(t, s), "test_snapshot_consistency")
	requireFailureKind(t, err, KindInvalidOperation, "proof read before the cutover")
	if !strings.Contains(err.Error(), "moved its committed law content after its verification") {
		t.Fatalf("stale proof refusal: %v", err)
	}
}
