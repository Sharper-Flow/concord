package store

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestWorkflowContractUpdateAcceptsPredicateArray(t *testing.T) {
	t.Parallel()
	const workID = "contract-update-predicate-array"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "implement", []string{}, nil)
	contract := pContractSuccessor(t, f.store, workID, "Continue with the corrected contract.")
	if err := reviseContractRecord(t, f.store, workID, f.owner, contract, readWorkVersion(t, f.store, workID)); err != nil {
		t.Fatalf("predicate-array contract update refused: %v", err)
	}
	contract = pContractSuccessor(t, f.store, workID, "Reject an ambiguous outcome shape.")
	contract["outcome_kind"] = "check"
	contract["outcome_payload"] = map[string]any{"kind": "check"}
	version := readWorkVersion(t, f.store, workID)
	if err := reviseContractRecord(t, f.store, workID, f.owner, contract, version); !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("contract update with both outcome shapes = %v, want invalid_payload", err)
	}
	if got := readWorkVersion(t, f.store, workID); got != version {
		t.Fatalf("refused contract update changed work version from %d to %d", version, got)
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
			s, completion := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}, includeVerdict: true, includePremise: true, omitClosure: true})
			actor := workflowActorForRef(t, s, completion.Actor)
			cutoverLawProjection(t, s)
			step := workflowPlanFixtureFrontier(t, s, workID)
			binding, err := readWorkflowArchitectureBinding(t.Context(), s.db, workID, 1)
			if err != nil {
				t.Fatal(err)
			}
			successor := map[string]any{
				"contract_version": 2, "premise": "continue under successor law",
				"outcome_predicates": []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:successor-law", "immutable_subject_ref": "commit:successor-law", "expected_result": "pass"}}},
				"required_evidence":  []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": tc.mandate, "law_modifies": []string{}, "rigor_class": "prototype_internal",
				"decision_domains": []string{}, "architecture_binding": binding,
			}
			version := readWorkVersion(t, s, workID)
			err = reviseContractRecord(t, s, workID, actor, successor, version)
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
				t.Fatalf("ordinary deleted-law contract update: %v", err)
			}
			if got := readWorkVersion(t, s, workID); got != version+1 {
				t.Fatalf("ordinary update work version=%d, want %d", got, version+1)
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
			if got := workflowPlanFixtureFrontier(t, s, workID); got != step {
				t.Fatalf("contract revision reset step %q to %q", step, got)
			}
		})
	}
}

func TestWorkflowLawRevisionDeletedLawRecoveryAtExecution(t *testing.T) {
	t.Parallel()
	const workID = "law-deletion-execution-recovery"
	s := seedLawRevisionPendingReview(t, workID)
	var approvedBy string
	if err := s.db.QueryRow(`SELECT approved_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&approvedBy); err != nil {
		t.Fatal(err)
	}
	actor := workflowActorForRef(t, s, approvedBy)
	cutoverLawProjection(t, s)
	successor := pContractSuccessor(t, s, workID, "continue with current accepted law")
	successor["spec_mandate"] = []string{}
	successor["law_modifies"] = []string{}
	if err := reviseContractRecord(t, s, workID, actor, successor, readWorkVersion(t, s, workID)); err != nil {
		t.Fatalf("deleted-law recovery at execution: %v", err)
	}
	if version, active := activeContractState(t, s, workID); version != 2 || active != 1 {
		t.Fatalf("recovery active contract=%d count=%d, want 2/1", version, active)
	}
	var mandate string
	if err := s.db.QueryRow(`SELECT spec_mandate FROM workflow_contracts WHERE work_id=? AND contract_version=2`, workID).Scan(&mandate); err != nil || mandate != "[]" {
		t.Fatalf("dropped mandate=%q err=%v, want []", mandate, err)
	}
	if got := workflowPlanFixtureFrontier(t, s, workID); got != "implement" {
		t.Fatalf("recovery changed execution step: %q", got)
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
	j := pReturnReviewFixture(t, workID, WorkflowStepPlanChange, "")
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
