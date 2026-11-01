package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWorkflowLawRevisionSameIDAmendmentRemainsCompatible(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	repo := initKnowledgeRepo(t)
	seedWork(t, s, "law-amendment")
	home := anchorCommittedLawHome(t, s, repo)
	// The committed folder carries the amended spec:stable revision while the
	// pin below names the revision the contract approved, so the check sees a
	// same-ID amendment, never a deletion.
	commitWorkflowFixtureLaw(t, home, "spec:stable", fixtureLawFolderLaw("spec:stable"))
	insertLawRevisionFixture(t, s, "law-amendment", "spec:stable", "sha256:"+strings.Repeat("a", 64))
	ctx, err := s.EstablishKnowledgeSourceSetProofForProject(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}

	got, err := findStaleWorkflowLawRevision(ctx, s.DatabaseForTesting(), "p", "l", "law-amendment", 1, []string{"spec:stable"})
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
	repo := initKnowledgeRepo(t)
	seedWork(t, s, "law-deletion")
	home := anchorCommittedLawHome(t, s, repo)
	oldHash := "sha256:" + strings.Repeat("a", 64)
	// The accepted law is the committed file, and the pin names the revision
	// the contract approved.
	commitWorkflowFixtureLaw(t, home, "spec:old", fixtureLawFolderLaw("spec:old"))
	insertLawRevisionFixture(t, s, "law-deletion", "spec:old", oldHash)
	// The law retires by deleting its committed file: folder membership is
	// the acceptance authority, so the deletion is the retirement.
	removeWorkflowFixtureLaw(t, home, "spec:old")
	ctx, err := s.EstablishKnowledgeSourceSetProofForProject(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}

	got, err := findStaleWorkflowLawRevision(ctx, s.DatabaseForTesting(), "p", "l", "law-deletion", 1, []string{"spec:old"})
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
	repo := initKnowledgeRepo(t)
	seedWork(t, s, "law-legacy")
	home := anchorCommittedLawHome(t, s, repo)
	commitWorkflowFixtureLaw(t, home, "spec:legacy", fixtureLawFolderLaw("spec:legacy"))
	insertLawRevisionFixture(t, s, "law-legacy", "spec:legacy", "sha256:"+strings.Repeat("a", 64))
	// The legacy consumer approved before revision pins existed: its contract
	// carries no pin row.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM workflow_contract_law_revisions; DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	ctx, err := s.EstablishKnowledgeSourceSetProofForProject(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := findStaleWorkflowLawRevision(ctx, s.DatabaseForTesting(), "p", "l", "law-legacy", 1, []string{"spec:legacy"}); err != nil || got != nil {
		t.Fatalf("accepted legacy unpinned law = %+v err=%v, want compatible", got, err)
	}
	// The unpinned consumer's law then retires through the ordinary deletion,
	// and the check fails closed without inventing a pin.
	removeWorkflowFixtureLaw(t, home, "spec:legacy")
	ctx, err = s.EstablishKnowledgeSourceSetProofForProject(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	got, err := findStaleWorkflowLawRevision(ctx, s.DatabaseForTesting(), "p", "l", "law-legacy", 1, []string{"spec:legacy"})
	var failure *Failure
	if got != nil || !failureAs(err, &failure) || failure.Kind != KindProjectionNotFound || len(failure.CandidateIDs) != 1 || failure.CandidateIDs[0] != "spec:legacy" {
		t.Fatalf("legacy deletion diagnosis = %+v err=%v, want projection_not_found without an invented pin", got, err)
	}
}

func TestWorkflowLawRevisionMissingPinnedProjectionIsStale(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	repo := initKnowledgeRepo(t)
	seedWork(t, s, "law-missing")
	// The anchored home commits only its registry: the pin names a law the
	// folder never accepted.
	anchorCommittedLawHome(t, s, repo)
	insertLawRevisionFixture(t, s, "law-missing", "spec:missing", "sha256:"+strings.Repeat("a", 64))
	ctx, err := s.EstablishKnowledgeSourceSetProofForProject(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	got, err := findStaleWorkflowLawRevision(ctx, s.DatabaseForTesting(), "p", "l", "law-missing", 1, []string{"spec:missing"})
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
			if len(pins) == 1 && (pins[0].LawID != "spec:two" || pins[0].ContentHash != workflowFixtureLawPin(t, workflowLawFixtureRepo(t, s), "spec:two")) {
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
			startCtx, err := s.PrepareWorkflowActionProof(context.Background(), "start_execution", workID)
			if err != nil {
				t.Fatal(err)
			}
			tx, err = s.DatabaseForTesting().BeginTx(startCtx, nil)
			if err != nil {
				t.Fatal(err)
			}
			scope2, err := beginWrite(startCtx, tx)
			if err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			startResult, err := applyWorkflowActionRawTx(startCtx, tx, scope2, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: version, ActionID: "start_execution", Payload: mustJSONValue(map[string]any{}), Actor: actor, AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64), IdempotencyIdentity: "recontract-start", OperationID: "recontract-start", PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "recontract-start", RequestID: "request:recontract-start", ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 17, 0, 0, 4, 0, time.UTC)})
			_ = scope2.close(startCtx)
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
	if action.ID != "supersede_contract" {
		t.Fatalf("stale recovery action=%+v, want supersede_contract", action)
	}

	currentWork := "law-recovery-action-current"
	currentFixture := closureBindingFixture(t, currentWork, "release")
	currentStore := currentFixture.store
	attachWorkflowLawPin(t, currentStore, currentWork, "sha256:"+strings.Repeat("a", 64))
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
	// The staleness read consumes committed law, so the pre-transaction proof
	// is verified before the raw read opens.
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkWorkflowLawRevisionStalenessTx(ctx, s.db, workID); err != nil {
		t.Fatalf("healthy execution has a stale law pin: %v", err)
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
	// The clone wall reads committed law, so the proof is verified after the
	// cutover and before the transaction opens.
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
	if err != nil {
		t.Fatal(err)
	}
	currentVersion := readWorkVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = validateClonedWorkflowContractLawRevisionsTx(ctx, tx, workID, []int64{1})
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

func seedLawRevisionPendingReview(t *testing.T, workID string) *Store {
	t.Helper()
	j := pReturnReviewFixture(t, workID, "workflow.implementation", "")
	f := j.fixture
	if _, err := f.store.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_contracts SET spec_mandate='["spec:one"]' WHERE work_id=? AND superseded_by IS NULL; DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}
	attachWorkflowLawPin(t, f.store, workID, "sha256:"+strings.Repeat("a", 64))
	if err := runVerdictActionAs(t, f.store, workID, "bind_evidence", mustJSONValue(map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": "spec:one"}), 0, f.owner); err != nil {
		t.Fatalf("bind mandated law before Review: %v", err)
	}
	acceptCurrentReviewForTest(t, f, "continue")
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

func attachWorkflowLawPin(t *testing.T, s *Store, workID, hash string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES(?,?,?,?); DELETE FROM write_guard`, workID, 1, "spec:one", hash); err != nil {
		t.Fatal(err)
	}
}

// cutoverLawProjection retires the mandated committed law and enacts its
// successor through the ordinary pull-request route: the committed file is
// deleted and the successor law file lands in the same commit series, so the
// next verified read carries the cutover.
func cutoverLawProjection(t *testing.T, s *Store) {
	t.Helper()
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"}
	removeCommittedFixtureLaw(t, home)
	commitFixtureLawFile(t, home, "spec:two", fixtureLawFolderLaw("spec:two"))
}

func TestWorkflowLawRevisionPinsFoldWithoutConsultingCurrentLaw(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	workID := "law-pinned-rebuild"
	seedWork(t, s, workID)
	// The anchor home carries a real committed spec:one at a revision the pin
	// below does not name, so the fold demonstrably consults neither the
	// committed folder nor its digests.
	repo := initKnowledgeRepo(t)
	home := KnowledgeHome{HomeProjectID: "p", HomeLocatorID: "l", RepoPath: repo, HeadRef: "HEAD"}
	commitWorkflowFixtureLaw(t, home, "spec:one", fixtureLawFolderLaw("spec:one"))
	seedEventDerivedLocator(t, s, "p", "l", repo)
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

// A proof read before a law cutover never feeds a later transaction: the
// transaction revalidates the committed law content the snapshot was read
// from and refuses once the cutover moved it.
func TestWorkflowLawRevisionRefusesProofReadBeforeCutover(t *testing.T) {
	t.Parallel()
	const workID = "law-proof-before-cutover"
	s := seedLawRevisionPendingReview(t, workID)
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
	if err != nil {
		t.Fatal(err)
	}
	cutoverLawProjection(t, s)
	err = checkWorkflowLawRevisionStalenessTx(ctx, s.db, workID)
	requireFailureKind(t, err, KindInvalidOperation, "proof read before the cutover")
	if !strings.Contains(err.Error(), "moved its committed law content after its verification") {
		t.Fatalf("stale proof refusal does not name the moved law content: %v", err)
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
	// The acceptance verifies the committed law folder before its transaction
	// opens, so the verification reads the committed cutover.
	acceptanceCtx, err := prepareTerminalTestContext(context.Background(), s, workID, "record_review")
	if err != nil {
		t.Fatal(err)
	}

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
	_, err = applyWorkflowActionRawTx(acceptanceCtx, tx, scope, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: currentVersion, ActionID: "record_review", Payload: acceptancePayload, Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), IdempotencyIdentity: "accept-stale", OperationID: "accept-stale", PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "accept-stale", RequestID: "request:accept-stale", ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 17, 0, 0, 2, 0, time.UTC),
	})
	_ = scope.close(context.Background())
	_ = tx.Rollback()
	if !failureAs(err, &failure) || failure.Kind != KindStaleLawRevision {
		t.Fatalf("workflow result acceptance error=%+v, want stale_law_revision", err)
	}
}

// anchorCommittedLawHome seeds the project and canonical-path locator pair
// ("p"/"l") the raw staleness reads name against a real committed law-folder
// home, and designates that home as the anchor Product's knowledge home, so a
// pre-transaction proof binds the folder the check reads. Call it after
// seedWork: the work fixture must create the shared Project first.
func anchorCommittedLawHome(t *testing.T, s *Store, repo string) KnowledgeHome {
	t.Helper()
	home := KnowledgeHome{HomeProjectID: "p", HomeLocatorID: "l", RepoPath: repo, HeadRef: "HEAD"}
	// A designated home's law reads need its committed Domain registry.
	writeFixtureLawSource(t, home)
	seedEventDerivedLocator(t, s, "p", "l", repo)
	authorizeKnowledgeProductHome(t, s, "anchor-product-p", home, "p")
	return home
}

// insertLawRevisionFixture records one approved contract and its law revision
// pin for a seeded work item. The caller anchors the committed law home the
// mandate resolves against.
func insertLawRevisionFixture(t *testing.T, s *Store, workID, lawID, hash string) {
	t.Helper()
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
