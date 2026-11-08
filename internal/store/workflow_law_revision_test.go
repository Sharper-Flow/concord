package store

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

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
			s, actor := seedLawRevisionContract(t, workID, "review")
			if err := runVerdictActionAs(t, s, workID, "confirm_premise", json.RawMessage(`{}`), 0, actor); err != nil {
				t.Fatalf("confirm predecessor premise: %v", err)
			}
			cutoverLawProjection(t, s)
			step := workflowPlanFixtureFrontier(t, s, workID)
			binding, err := readWorkflowArchitectureBinding(t.Context(), s.db, workID, 1)
			if err != nil {
				t.Fatal(err)
			}
			successor := map[string]any{
				"contract_version":     2,
				"legislative_subjects": []string{}, "spec_mandate": tc.mandate, "law_modifies": []string{},
				"architecture_binding": binding,
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
	s, _ := seedLawRevisionContract(t, workID, "implement")
	var approvedBy string
	if err := s.db.QueryRow(`SELECT approved_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&approvedBy); err != nil {
		t.Fatal(err)
	}
	actor := workflowActorForRef(t, s, approvedBy)
	cutoverLawProjection(t, s)
	successor := pContractSuccessor(t, s, workID)
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
	s, _ := seedLawRevisionContract(t, workID, "review")
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

func seedLawRevisionContract(t *testing.T, workID, frontier string) (*Store, WorkflowActor) {
	t.Helper()
	s := openTemp(t)
	seedLawContextFixture(t, s, workID)
	binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainAdditions: []string{}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{"spec:one"}, []string{}, binding)
	moveWorkflowPlanFixtureFrontier(t, s, workID, frontier)
	actor := WorkflowActor{PrincipalRef: "principal:law-context", ClientRef: "client:law-context", AgentRef: "agent:law-context", SessionRef: "session:law-context", ActorClass: ActorAgent}
	return s, actor
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
	s, _ := seedLawRevisionContract(t, workID, "implement")
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
