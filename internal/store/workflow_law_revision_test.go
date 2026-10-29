package store

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestWorkflowLawRevisionSameIDAmendmentRemainsCompatible(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "law-amendment")
	insertLawRevisionFixture(t, s, "law-amendment", "spec:stable", "sha256:"+strings.Repeat("a", 64))
	insertAcceptedLaw(t, s, "spec:stable", "sha256:"+strings.Repeat("b", 64))

	got, err := findStaleWorkflowLawRevision(context.Background(), s.DatabaseForTesting(), "p", "l", "law-amendment", 1, []string{"spec:stable"})
	if err != nil {
		t.Fatalf("same-ID amendment check: %v", err)
	}
	if got != nil {
		t.Fatalf("same-ID amendment became stale: %+v", got)
	}
}

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

func TestWorkflowLawRevisionDeletionRefusesPinnedConsumer(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "law-deletion")
	oldHash := "sha256:" + strings.Repeat("a", 64)
	insertLawRevisionFixture(t, s, "law-deletion", "spec:old", oldHash)
	insertAcceptedLaw(t, s, "spec:old", oldHash)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM law_subjects WHERE law_id='spec:old'; DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}

	got, err := findStaleWorkflowLawRevision(context.Background(), s.DatabaseForTesting(), "p", "l", "law-deletion", 1, []string{"spec:old"})
	if err != nil {
		t.Fatalf("deletion check: %v", err)
	}
	if got == nil || got.OldLawID != "spec:old" || got.OldContentHash != oldHash || strings.Join(got.RecoveryActions, ",") != "supersede_contract,terminal_work" {
		t.Fatalf("deletion diagnosis = %+v, want deleted spec:old, its pinned hash, and closed recovery actions", got)
	}
}

func TestWorkflowLawRevisionDeletionFailsClosedForLegacyUnpinnedConsumer(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "law-legacy")
	insertLawRevisionFixture(t, s, "law-legacy", "spec:legacy", "sha256:"+strings.Repeat("a", 64))
	insertAcceptedLaw(t, s, "spec:legacy", "sha256:"+strings.Repeat("a", 64))
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM workflow_contract_law_revisions; DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if got, err := findStaleWorkflowLawRevision(t.Context(), s.db, "p", "l", "law-legacy", 1, []string{"spec:legacy"}); err != nil || got != nil {
		t.Fatalf("accepted legacy unpinned law = %+v err=%v, want compatible", got, err)
	}
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM law_subjects WHERE law_id='spec:legacy'; DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	got, err := findStaleWorkflowLawRevision(context.Background(), s.DatabaseForTesting(), "p", "l", "law-legacy", 1, []string{"spec:legacy"})
	var failure *Failure
	if got != nil || !failureAs(err, &failure) || failure.Kind != KindProjectionNotFound || len(failure.CandidateIDs) != 1 || failure.CandidateIDs[0] != "spec:legacy" {
		t.Fatalf("legacy deletion diagnosis = %+v err=%v, want projection_not_found without an invented pin", got, err)
	}
}

func TestWorkflowLawRevisionMissingPinnedProjectionIsStale(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "law-missing")
	insertLawRevisionFixture(t, s, "law-missing", "spec:missing", "sha256:"+strings.Repeat("a", 64))
	got, err := findStaleWorkflowLawRevision(context.Background(), s.DatabaseForTesting(), "p", "l", "law-missing", 1, []string{"spec:missing"})
	if err != nil || got == nil || got.OldLawID != "spec:missing" || got.OldContentHash != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("missing pinned law diagnosis = %+v err=%v, want stale deleted law and pinned hash", got, err)
	}
}

func TestWorkflowStaleLawBoundaryNamesRecoveryWithoutApproval(t *testing.T) {
	t.Parallel()
	s, _ := seedCompletionGateCase(t, "law-recovery", completionGateCase{requiredEvidence: []string{"verification", "review"}, includeVerdict: true, includePremise: true})
	oldHash := "sha256:" + strings.Repeat("a", 64)
	attachWorkflowLawPin(t, s, "law-recovery", oldHash)
	cutoverLawProjection(t, s)
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), "law-recovery")
	if err != nil {
		t.Fatal(err)
	}
	err = s.Transact(ctx, func(tx *Transaction) error {
		return CheckWorkflowConsequentialBoundaryTx(ctx, tx, "law-recovery")
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindStaleLawRevision {
		t.Fatalf("boundary=%v, want stale_law_revision", err)
	}
	if failure.RecoveryAction != "reconcile_operation" {
		t.Errorf("boundary recovery=%q, want reconcile_operation", failure.RecoveryAction)
	}
	stale := failure.StaleLawRevision
	if stale == nil || stale.OldLawID != "spec:one" || stale.OldContentHash != oldHash || strings.Join(stale.RecoveryActions, ",") != "supersede_contract,terminal_work" {
		t.Fatalf("boundary lost typed recovery choices: %+v", stale)
	}
}

func TestWorkflowLawRevisionRecontractsThroughProductionRoutes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		mandate []string
		refused bool
	}{
		{name: "replace", mandate: []string{"spec:two"}},
		{name: "drop", mandate: []string{}},
		{name: "retain-deleted", mandate: []string{"spec:one"}, refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			workID := "law-recontract-route-" + tc.name
			s, completion := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}, includeVerdict: true, includePremise: true})
			actor := workflowActorForRef(t, s, completion.Actor)
			attachWorkflowLawPin(t, s, workID, "sha256:"+strings.Repeat("a", 64))
			cutoverLawProjection(t, s)
			// At the current fixture's completion step, correction also requires the
			// contradiction recorded after proof (CD-0172 D1).
			recordCompleteStepContradictionObservation(t, s, workID, actor)
			if _, action, err := WorkflowActionDefinitionFor(t.Context(), s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err != nil || action.ID != "supersede_contract" {
				t.Fatalf("discover production deleted-law recovery: action=%+v err=%v", action, err)
			}
			binding, err := readWorkflowArchitectureBinding(t.Context(), s.db, workID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:two','product',?,'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','test'); DELETE FROM write_guard`, binding.HomeDomainID); err != nil {
				t.Fatal(err)
			}
			successor := map[string]any{
				"contract_version": 2, "premise": "continue under successor law",
				"outcome_predicates": []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:successor-law", "immutable_subject_ref": "commit:successor-law", "expected_result": "pass"}}},
				"required_evidence":  []string{"verification", "review"}, "route_conventions": []string{"complete_step_correction"}, "spec_mandate": []string{"spec:two"}, "law_modifies": []string{}, "rigor_class": "prototype_internal",
				"supersede_reason": "move to the accepted successor law", "audit_evidence": []string{"evidence:law-cutover"}, "decision_domains": []string{}, "architecture_binding": binding,
			}
			successor["spec_mandate"] = tc.mandate
			version := readWorkVersion(t, s, workID)
			ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			scope, err := beginWrite(context.Background(), tx)
			if err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			result, err := applyWorkflowActionRawTx(ctx, tx, scope, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
				WorkID: workID, ExpectedVersion: version, ActionID: "supersede_contract", Payload: mustJSONValue(successor), Actor: actor,
				AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), IdempotencyIdentity: "recontract-recover", OperationID: "recontract-recover", PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "recontract-recover", RequestID: "request:recontract-recover", ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 17, 0, 0, 3, 0, time.UTC),
			})
			_ = scope.close(context.Background())
			if tc.refused {
				_ = tx.Rollback()
				var failure *Failure
				if !failureAs(err, &failure) || failure.Kind != KindProjectionNotFound || !strings.Contains(failure.Detail, "spec:one") {
					t.Fatalf("complete successor retaining deleted law = %v, want projection_not_found naming spec:one", err)
				}
				if got := readWorkVersion(t, s, workID); got != version {
					t.Fatalf("refused recovery changed version: %d, want %d", got, version)
				}
				var successorCount, activeVersion int
				if err := s.db.QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND contract_version=2`, workID).Scan(&successorCount); err != nil || successorCount != 0 {
					t.Fatalf("refused recovery installed successor: count=%d err=%v", successorCount, err)
				}
				if err := s.db.QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeVersion); err != nil || activeVersion != 1 {
					t.Fatalf("refused recovery active version=%d err=%v", activeVersion, err)
				}
				return
			}
			if err != nil {
				tx.Rollback()
				t.Fatalf("production stale-contract recovery route: %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if result.ResultingVersion == 0 {
				t.Fatal("successor contract approval did not produce a workflow result")
			}
			pins, err := readWorkflowLawRevisions(t.Context(), s.db, workID, 2)
			if err != nil {
				t.Fatal(err)
			}
			if len(pins) != len(tc.mandate) {
				t.Fatalf("successor pins=%+v, want mandate %v", pins, tc.mandate)
			}
			if len(pins) == 1 && (pins[0].LawID != "spec:two" || pins[0].ContentHash != "sha256:"+strings.Repeat("b", 64)) {
				t.Fatalf("successor contract pins=%+v, want current accepted spec:two", pins)
			}
			var activeCount, oldConfirmationCount, successorConfirmationCount int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeCount); err != nil || activeCount != 1 {
				t.Fatalf("active contract count=%d err=%v, want exactly one", activeCount, err)
			}
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=1`, workID).Scan(&oldConfirmationCount); err != nil || oldConfirmationCount != 1 {
				t.Fatalf("prior premise confirmations=%d err=%v, want one historical confirmation", oldConfirmationCount, err)
			}
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=2`, workID).Scan(&successorConfirmationCount); err != nil || successorConfirmationCount != 0 {
				t.Fatalf("successor premise confirmations=%d err=%v, want zero", successorConfirmationCount, err)
			}
			if got := currentStep(t, s, workID); got != "execution" {
				t.Fatalf("recontracted workflow step=%q, want unchanged execution step", got)
			}
			version = result.ResultingVersion
			tx, err = s.DatabaseForTesting().BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			scope2, err := beginWrite(context.Background(), tx)
			if err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			startResult, err := applyWorkflowActionRawTx(context.Background(), tx, scope2, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: version, ActionID: "start_execution", Payload: mustJSONValue(map[string]any{}), Actor: actor, AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64), IdempotencyIdentity: "recontract-start", OperationID: "recontract-start", PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "recontract-start", RequestID: "request:recontract-start", ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 17, 0, 0, 4, 0, time.UTC)})
			_ = scope2.close(context.Background())
			if err != nil {
				tx.Rollback()
				t.Fatalf("ordinary post-recovery action: %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if startResult.ResultingVersion <= version || currentStep(t, s, workID) != "execution" {
				t.Fatalf("post-recovery action result=%d step=%q", startResult.ResultingVersion, currentStep(t, s, workID))
			}
		})
	}
}

func TestWorkflowLawRevisionRecoveryActionIsStaleOnlyAndApprovalRequired(t *testing.T) {
	t.Parallel()
	staleWork := "law-recovery-action-stale"
	staleStore, _ := seedCompletionGateCase(t, staleWork, completionGateCase{requiredEvidence: []string{"verification", "review"}, omitClosure: true})
	attachWorkflowLawPin(t, staleStore, staleWork, "sha256:"+strings.Repeat("a", 64))
	cutoverLawProjection(t, staleStore)
	_, action, err := WorkflowActionDefinitionFor(context.Background(), staleStore, BuiltinWorkflowRegistry(), staleWork, "supersede_contract")
	if err != nil {
		t.Fatalf("stale recovery action definition: %v", err)
	}
	if action.ID != "supersede_contract" || action.Approval != ActionApprovalRequired {
		t.Fatalf("stale recovery action=%+v, want operator approval-required action", action)
	}

	currentWork := "law-recovery-action-current"
	currentStore := seedLawRevisionPendingReview(t, currentWork)
	if _, _, err := WorkflowActionDefinitionFor(context.Background(), currentStore, BuiltinWorkflowRegistry(), currentWork, "supersede_contract"); err == nil {
		t.Fatal("non-stale workflow exposed the contract recovery action")
	} else {
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind != KindInvalidOperation {
			t.Fatalf("non-stale recovery error=%v, want invalid_operation", err)
		}
	}
}

func TestWorkflowLawRevisionDeletedLawRecoveryAtExecution(t *testing.T) {
	t.Parallel()
	const workID = "law-deletion-execution-recovery"
	s := seedLawRevisionPendingReview(t, workID)
	if _, _, err := WorkflowActionDefinitionFor(t.Context(), s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); !hasFailureKind(err, KindInvalidOperation) {
		t.Fatalf("healthy execution exposed stale recovery: %v", err)
	}
	var approvedBy string
	if err := s.db.QueryRow(`SELECT approved_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&approvedBy); err != nil {
		t.Fatal(err)
	}
	actor := workflowActorForRef(t, s, approvedBy)
	cutoverLawProjection(t, s)
	if _, action, err := WorkflowActionDefinitionFor(t.Context(), s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err != nil || action.ID != "supersede_contract" {
		t.Fatalf("deleted-law recovery at execution unavailable: action=%+v err=%v", action, err)
	}
	successor := pContractSuccessor(t, s, workID, "continue with current accepted law")
	successor["spec_mandate"] = []string{}
	successor["law_modifies"] = []string{}
	if err := issue1013Preflight(t, s, workID, "supersede_contract", mustJSONValue(successor), actor); err != nil {
		t.Fatalf("deleted-law recovery preflight: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "supersede_contract", mustJSONValue(successor), 0, actor); err != nil {
		t.Fatalf("deleted-law recovery at execution: %v", err)
	}
	if version, active := activeContractState(t, s, workID); version != 2 || active != 1 {
		t.Fatalf("recovery active contract=%d count=%d, want 2/1", version, active)
	}
	pins, err := readWorkflowLawRevisions(t.Context(), s.db, workID, 2)
	if err != nil || len(pins) != 0 {
		t.Fatalf("dropped mandate retained pins: %+v err=%v", pins, err)
	}
	if got := currentStep(t, s, workID); got != "execution" {
		t.Fatalf("recovery changed execution step: %q", got)
	}
}

func TestWorkflowLawRevisionCloneRecoveryRefusesDeletedMandate(t *testing.T) {
	t.Parallel()
	workID := "law-recovery-requires-successor-pin"
	s, _ := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}})
	attachWorkflowLawPin(t, s, workID, "sha256:"+strings.Repeat("a", 64))
	cutoverLawProjection(t, s)
	currentVersion := readWorkVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = validateClonedWorkflowContractLawRevisionsTx(t.Context(), tx, workID, []int64{1})
	_ = tx.Rollback()
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "retain a deleted mandated law") {
		t.Fatalf("clone recovery retaining deleted law error=%v, want invalid_payload", err)
	}
	if got := readWorkVersion(t, s, workID); got != currentVersion {
		t.Fatalf("clone validation changed version: %d, want %d", got, currentVersion)
	}
}

func TestWorkflowLawRevisionAllowsTerminalLifecycleMutationAfterCutover(t *testing.T) {
	t.Parallel()
	workID := "law-terminal-route"
	s, _ := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}})
	attachWorkflowLawPin(t, s, workID, "sha256:"+strings.Repeat("a", 64))
	cutoverLawProjection(t, s)
	version := readWorkVersion(t, s, workID)
	var currentLifecycle string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&currentLifecycle); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workTransitionEvent("law-terminal-cancel", workID, currentLifecycle, "cancelled", version, version+1)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("terminal lifecycle route after cutover: %v", err)
	}
	var lifecycle string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil || lifecycle != "cancelled" {
		t.Fatalf("terminal lifecycle=%q err=%v, want cancelled", lifecycle, err)
	}
}

func TestWorkflowLawRevisionCutoverCommitsBeforeCrossConnectionAcceptance(t *testing.T) {
	t.Parallel()
	workID := "law-cross-connection-order"
	s1 := seedLawRevisionPendingReview(t, workID)
	acceptancePayload := mustJSONValue(lawRevisionAcceptancePayload(t, s1, workID))
	s2, err := Open(context.Background(), s1.Path())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	locked := make(chan struct{})
	release := make(chan struct{})
	cutoverErr := make(chan error, 1)
	crossVersion := readWorkVersion(t, s1, workID)
	go func() {
		tx, beginErr := s1.DatabaseForTesting().BeginTx(context.Background(), nil)
		if beginErr != nil {
			cutoverErr <- beginErr
			return
		}
		if foldErr := enterWrite(context.Background(), tx); foldErr != nil {
			tx.Rollback()
			cutoverErr <- foldErr
			return
		}
		if _, execErr := tx.Exec(`DELETE FROM law_domain_homes WHERE law_id='spec:one'; DELETE FROM law_subjects WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one'`); execErr != nil {
			tx.Rollback()
			cutoverErr <- execErr
			return
		}
		close(locked)
		<-release
		_ = leaveWrite(context.Background(), tx)
		cutoverErr <- tx.Commit()
	}()
	select {
	case <-locked:
	case err := <-cutoverErr:
		t.Fatalf("cutover before lock: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("cutover did not reach its transaction lock")
	}

	acceptanceDone := make(chan error, 1)
	acceptanceStarted := make(chan struct{})
	go func() {
		owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
		close(acceptanceStarted)
		tx, beginErr := s2.DatabaseForTesting().BeginTx(context.Background(), nil)
		if beginErr != nil {
			acceptanceDone <- beginErr
			return
		}
		_, actionErr := applyWorkflowActionRawTx(context.Background(), tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: crossVersion, ActionID: "record_review", Payload: acceptancePayload, Actor: owner, AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), IdempotencyIdentity: "cross-accept", OperationID: "cross-accept", PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "cross-accept", RequestID: "request:cross-accept", ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 17, 0, 0, 4, 0, time.UTC)})
		if actionErr == nil {
			tx.Rollback()
			acceptanceDone <- nil
			return
		}
		tx.Rollback()
		acceptanceDone <- actionErr
	}()
	select {
	case <-acceptanceStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("cross-connection acceptance did not start before cutover release")
	}
	close(release)
	if err := <-cutoverErr; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-acceptanceDone:
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind != KindStaleLawRevision {
			t.Fatalf("cross-connection acceptance=%v, want stale_law_revision after committed cutover", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cross-connection acceptance did not finish")
	}
}

func TestWorkflowLawRevisionCutoverCommitsBeforeCrossProcessAcceptance(t *testing.T) {
	t.Parallel()
	workID := "law-cross-process-order"
	s := seedLawRevisionPendingReview(t, workID)
	version := readWorkVersion(t, s, workID)

	cutover := exec.Command(os.Args[0], "-test.run=^TestWorkflowLawRevisionCrossProcessWorker$", "-test.v=false")
	cutover.Env = append(os.Environ(), "TEST_CONCORD_LAW_RACE_ROLE=cutover", "TEST_CONCORD_LAW_RACE_DB="+s.Path(), "TEST_CONCORD_LAW_RACE_WORK="+workID)
	cutoverOut, err := cutover.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cutoverIn, err := cutover.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cutover.Stderr = os.Stderr
	if err := cutover.Start(); err != nil {
		t.Fatal(err)
	}
	cutoverScanner := bufio.NewScanner(cutoverOut)
	t.Cleanup(func() { _ = cutover.Process.Kill() })
	if !cutoverScanner.Scan() || cutoverScanner.Text() != "cutover=locked" {
		t.Fatalf("cutover worker did not establish its transaction lock: %q", cutoverScanner.Text())
	}

	acceptance := exec.Command(os.Args[0], "-test.run=^TestWorkflowLawRevisionCrossProcessWorker$", "-test.v=false")
	acceptance.Env = append(os.Environ(), "TEST_CONCORD_LAW_RACE_ROLE=acceptance", "TEST_CONCORD_LAW_RACE_DB="+s.Path(), "TEST_CONCORD_LAW_RACE_WORK="+workID, fmt.Sprintf("TEST_CONCORD_LAW_RACE_VERSION=%d", version))
	acceptanceOut, err := acceptance.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	acceptance.Stderr = os.Stderr
	if err := acceptance.Start(); err != nil {
		t.Fatal(err)
	}
	acceptanceScanner := bufio.NewScanner(acceptanceOut)
	t.Cleanup(func() { _ = acceptance.Process.Kill() })
	if !acceptanceScanner.Scan() || acceptanceScanner.Text() != "acceptance=ready" {
		t.Fatalf("acceptance worker did not become ready before cutover commit: %q", acceptanceScanner.Text())
	}

	if _, err := fmt.Fprintln(cutoverIn, "release"); err != nil {
		t.Fatal(err)
	}
	if !cutoverScanner.Scan() || cutoverScanner.Text() != "cutover=committed" {
		t.Fatalf("cutover worker did not commit: %q", cutoverScanner.Text())
	}
	if err := cutover.Wait(); err != nil {
		t.Fatalf("cutover worker: %v", err)
	}
	if !acceptanceScanner.Scan() || acceptanceScanner.Text() != "acceptance=stale_law_revision" {
		t.Fatalf("acceptance worker result = %q, want stale_law_revision", acceptanceScanner.Text())
	}
	if err := acceptance.Wait(); err != nil {
		t.Fatalf("acceptance worker: %v", err)
	}
}

func TestWorkflowLawRevisionCrossProcessWorker(t *testing.T) {
	t.Parallel()
	role := os.Getenv("TEST_CONCORD_LAW_RACE_ROLE")
	if role == "" {
		return
	}
	if role == "acceptance" {
		fmt.Println("acceptance=ready")
	}
	s, err := Open(context.Background(), os.Getenv("TEST_CONCORD_LAW_RACE_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	workID := os.Getenv("TEST_CONCORD_LAW_RACE_WORK")

	switch role {
	case "cutover":
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := enterWrite(context.Background(), tx); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.Exec(`DELETE FROM law_domain_homes WHERE law_id='spec:one'; DELETE FROM law_subjects WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one'`); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		fmt.Println("cutover=locked")
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := leaveWrite(context.Background(), tx); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		fmt.Println("cutover=committed")
	case "acceptance":
		acceptancePayload := mustJSONValue(lawRevisionAcceptancePayload(t, s, workID))
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
		_, actionErr := applyWorkflowActionRawTx(context.Background(), tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: mustEnvInt64(t, "TEST_CONCORD_LAW_RACE_VERSION"), ActionID: "record_review", Payload: acceptancePayload, Actor: owner, AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), IdempotencyIdentity: "cross-process-accept", OperationID: "cross-process-accept", PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "cross-process-accept", RequestID: "request:cross-process-accept", ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 17, 0, 0, 4, 0, time.UTC)})
		tx.Rollback()
		var failure *Failure
		if !failureAs(actionErr, &failure) || failure.Kind != KindStaleLawRevision {
			t.Fatalf("acceptance error = %v, want stale_law_revision", actionErr)
		}
		fmt.Println("acceptance=stale_law_revision")
	default:
		t.Fatalf("unknown race worker role %q", role)
	}
}

func seedLawRevisionPendingReview(t *testing.T, workID string) *Store {
	t.Helper()
	j := pReturnReviewFixture(t, workID, "workflow.implementation", "")
	f := j.fixture
	if _, err := f.store.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_contracts SET spec_mandate='["spec:one"]' WHERE work_id=? AND superseded_by IS NULL; DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}
	attachWorkflowLawPin(t, f.store, workID, "sha256:"+strings.Repeat("a", 64))
	acceptCurrentReviewForTest(t, f, j.repo, "continue")
	return f.store
}

func lawRevisionAcceptancePayload(t *testing.T, s *Store, workID string) map[string]any {
	t.Helper()
	state, err := readWorkflowReviewFacts(t.Context(), s.db, workID)
	if err != nil || state.Accepted == nil {
		t.Fatalf("read retained Review payload: accepted=%+v err=%v", state.Accepted, err)
	}
	return map[string]any{"review": state.Accepted.Report}
}

func mustEnvInt64(t *testing.T, name string) int64 {
	t.Helper()
	var value int64
	if _, err := fmt.Sscan(os.Getenv(name), &value); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return value
}

func attachWorkflowLawPin(t *testing.T, s *Store, workID, hash string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES(?,?,?,?); DELETE FROM write_guard`, workID, 1, "spec:one", hash); err != nil {
		t.Fatal(err)
	}
}

func cutoverLawProjection(t *testing.T, s *Store) {
	t.Helper()
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM law_domain_homes WHERE law_id='spec:one'; DELETE FROM law_subjects WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id=?`, "spec:one"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator',?,'spec','accepted','.concord/docs/spec-two.md','Synthetic successor law','sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','test')`, "spec:two"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowLawRevisionPinsFoldWithoutConsultingCurrentLaw(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	workID := "law-pinned-rebuild"
	seedWork(t, s, workID)
	seedEventDerivedLocator(t, s, "p", "l", t.TempDir())
	insertAcceptedLaw(t, s, "spec:one", "sha256:"+strings.Repeat("a", 64))
	actor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/runner", "session/law-pinned")
	events := []Event{
		workflowEventWithActor("law-pinned-actor", WorkflowActorRecorded, workID, actor, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": actor, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/runner", "session_ref": "session/law-pinned", "actor_class": "agent"}),
		workflowEventWithActor("law-pinned-definition", WorkflowDefinitionSelected, workID, actor, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": workflowFixtureRef, "version": 1, "digest": workflowFixtureDigest(t), "work_kind": workflowFixtureWorkKind}),
	}
	contract := workflowEventWithActor("law-pinned-contract", WorkflowContractApproved, workID, actor, map[string]any{"work_id": workID, "expected_version": 4, "resulting_version": 5, "contract_version": 1, "premise": "pin the accepted law", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:law-pin", "immutable_subject_ref": "commit:law-pin", "expected_result": "pass"}, "required_evidence": []string{}, "route_conventions": []string{}, "spec_mandate": []string{"spec:one"}, "law_revisions": []WorkflowLawRevision{{LawID: "spec:one", ContentHash: "sha256:" + strings.Repeat("a", 64)}}, "law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"})
	events = append(events, contract)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := s.DatabaseForTesting().QueryRow(`SELECT content_hash FROM workflow_contract_law_revisions WHERE work_id=?`, workID).Scan(&hash); err != nil || hash != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("folded law pin = %q err=%v", hash, err)
	}
}

func TestWorkflowLawRevisionKeepsRawCompletionButRefusesWorkflowAcceptanceAfterCutover(t *testing.T) {
	t.Parallel()
	workID := "law-stale-in-flight"
	s := seedLawRevisionPendingReview(t, workID)
	acceptancePayload := mustJSONValue(lawRevisionAcceptancePayload(t, s, workID))
	var definitionRef string
	var definitionDigest string
	if err := s.db.QueryRow(`SELECT definition_ref,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&definitionRef, &definitionDigest); err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimStepAuthorized(context.Background(), s, ClaimRequest{
		OpID: "law-stale-in-flight-op", WorkID: workID, WorkflowTypeRef: definitionRef, WorkflowTypeDigest: definitionDigest,
		StepID: "execution", StepKind: StepExternalEffect, AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64),
		AcceptedScopeSnapshot: `{}`, PrincipalRef: "principal/in-flight", Tool: "workflow-test", IdempotencyKey: "law-stale-in-flight-claim",
		RequestID: "request:law-stale-in-flight", ContractDigest: testManifestDigest, ObservedAt: time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC),
	}, nil)
	if err != nil {
		t.Fatalf("claim before cutover: %v", err)
	}
	cutoverLawProjection(t, s)

	if _, err = CompleteStep(context.Background(), s, CompleteRequest{
		OpID: claim.OpID, AttemptEpoch: claim.AttemptEpoch, ResultKind: ResultCompleted, PrincipalRef: "principal/in-flight", Tool: "workflow-test",
		IdempotencyKey: "law-stale-in-flight-complete", RequestID: "request:law-stale-in-flight-complete", ObservedAt: time.Date(2026, 8, 17, 0, 0, 1, 0, time.UTC),
	}); err != nil {
		t.Fatalf("raw durable completion must remain recordable after cutover: %v", err)
	}
	var resultKind string
	if err := s.DatabaseForTesting().QueryRow(`SELECT result_kind FROM durable_operations WHERE op_id=? AND attempt_epoch=?`, claim.OpID, claim.AttemptEpoch).Scan(&resultKind); err != nil || resultKind != string(ResultCompleted) {
		t.Fatalf("raw completion result=%q err=%v, want completed", resultKind, err)
	}
	var failure *Failure
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	currentVersion := readWorkVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := beginWrite(context.Background(), tx)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	_, err = applyWorkflowActionRawTx(context.Background(), tx, scope, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: currentVersion, ActionID: "record_review", Payload: acceptancePayload, Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), IdempotencyIdentity: "accept-stale", OperationID: "accept-stale", PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "accept-stale", RequestID: "request:accept-stale", ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 17, 0, 0, 2, 0, time.UTC),
	})
	_ = scope.close(context.Background())
	_ = tx.Rollback()
	if !failureAs(err, &failure) || failure.Kind != KindStaleLawRevision {
		t.Fatalf("workflow result acceptance error=%+v, want stale_law_revision", err)
	}
}

func insertLawRevisionFixture(t *testing.T, s *Store, workID, lawID, hash string) {
	t.Helper()
	anchorHomePair(t, s, "p", "l")
	specMandate := "[\"" + lawID + "\"]"
	actorRef := "actor:" + strings.Repeat("a", 64)
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal/law','client/law','agent/law','session/law','agent','now')`, actorRef); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'law test','internal_sqlite','[]','[]','now',?,?, '[]',1,'prototype_internal'); INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:primary',0,'check','{"kind":"check"}')`, workID, actorRef, specMandate, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES(?,?,?,?)`, workID, 1, lawID, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
}

func insertAcceptedLaw(t *testing.T, s *Store, lawID, hash string) {
	t.Helper()
	anchorHomePair(t, s, "p", "l")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('p','l',?,'spec','accepted','.concord/docs/law.md',?,?, 'commit'); DELETE FROM write_guard`, lawID, lawID, hash); err != nil {
		t.Fatal(err)
	}
}

// The read form and the mutation form agree: an active peer that shares the
// item's Domain and law refuses neither (CD-0222 D6).
func TestWorkflowLawRevisionStalenessAdmitsASharedDomainPeer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := seedOverlapProjection(t, "overlap-left", "overlap-right", true)

	readErr := func() error {
		tx, beginErr := s.DatabaseForTesting().BeginTx(ctx, nil)
		if beginErr != nil {
			return beginErr
		}
		defer tx.Rollback()
		return checkWorkflowLawRevisionStalenessTx(ctx, tx, "overlap-left")
	}()
	if readErr != nil {
		t.Fatalf("read form refused beside a shared-Domain peer: %v", readErr)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return CheckWorkflowConsequentialBoundaryTx(ctx, tx, "overlap-left")
	}); err != nil {
		t.Fatalf("mutation form refused beside a shared-Domain peer: %v", err)
	}
}
