package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRecordedInvestigationArtifactRequiresResolvableDomainAndWorkRefs(t *testing.T) {
	s := openTemp(t)
	workID := "investigation-gate-work"
	otherWorkID := "investigation-gate-other"
	seedWork(t, s, workID)
	seedWork(t, s, otherWorkID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)

	insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("1", 16), []string{workID, "root"})
	if err := requireRecordedInvestigationArtifact(context.Background(), s.DatabaseForTesting(), workID); err == nil {
		t.Fatal("observation accepted the owning work item as the comparison ref")
	}
	insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("2", 16), []string{"missing-domain", otherWorkID})
	if err := requireRecordedInvestigationArtifact(context.Background(), s.DatabaseForTesting(), workID); err == nil {
		t.Fatal("observation accepted an unknown Domain ref")
	}
	insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("3", 16), []string{"root", otherWorkID})
	if err := requireRecordedInvestigationArtifact(context.Background(), s.DatabaseForTesting(), workID); err != nil {
		t.Fatalf("resolvable investigation artifact refused: %v", err)
	}
}

func TestPendingQuestionsRequireBoundResearchRevision(t *testing.T) {
	s := openTemp(t)
	workID := "investigation-gate-research"
	actor, version := continuityTestWorkflow(t, s, workID)
	_, err := continuityAction(t, s, workID, version, "checkpoint_context", "investigation-gate-checkpoint", map[string]any{
		"active_unit":       "unit:implementation",
		"hypothesis":        "hypothesis:one",
		"diagnosis":         "diagnosis:one",
		"strategy":          "strategy:one",
		"touched_refs":      []string{"ref:file"},
		"evidence_refs":     []string{"evidence:one"},
		"pending_questions": []string{"question:technical"},
		"pending_decisions": []string{},
	}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireResearchForPendingQuestions(context.Background(), s.DatabaseForTesting(), workID); err == nil {
		t.Fatal("approval admitted recorded pending questions without research")
	} else {
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindMissingEvidence {
			t.Fatalf("pending-question refusal=%v, want missing evidence", err)
		} else if !strings.Contains(failure.RecoveryAction, "research_bindings") || !strings.Contains(failure.RecoveryAction, "approving action") {
			t.Fatalf("pending-question recovery=%q, want research_bindings on the approving action", failure.RecoveryAction)
		}
	}

	pack := createSimplePack(t, s, "investigation-gate-research", workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	err = BindResearchRelianceTx(context.Background(), tx, workID, []ResearchBindingDeclaration{{PackID: pack.PackID, Revision: 1, UseRole: UseDecisionBasis, Required: true}}, time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC))
	leaveErr := leaveFold(context.Background(), tx)
	if err == nil {
		err = leaveErr
	}
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := requireResearchForPendingQuestions(context.Background(), s.DatabaseForTesting(), workID); err != nil {
		t.Fatalf("bound research revision did not admit approval: %v", err)
	}
}

func TestApproveContractResearchBindingTakesEffectBeforeItsGate(t *testing.T) {
	s := openTemp(t)
	workID := "investigation-gate-same-action"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	actor := WorkflowActor{PrincipalRef: "principal:investigation", ClientRef: "client:investigation", AgentRef: "agent:investigation", SessionRef: "session:investigation", ActorClass: ActorAgent}
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(context.Background(), tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: actor, Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	version := int64(4)
	for _, action := range []string{"record_proposal", "record_discovery", "record_design"} {
		version = issue31WorkflowAction(t, s, workID, version, action, "same-action-"+action, actor)
	}
	version, err = continuityAction(t, s, workID, version, "checkpoint_context", "same-action-checkpoint", map[string]any{
		"active_unit":       "unit:implementation",
		"hypothesis":        "hypothesis:one",
		"diagnosis":         "diagnosis:one",
		"strategy":          "strategy:one",
		"touched_refs":      []string{"ref:file"},
		"evidence_refs":     []string{"evidence:one"},
		"pending_questions": []string{"question:technical"},
		"pending_decisions": []string{},
	}, actor)
	if err != nil {
		t.Fatal(err)
	}
	pack := createSimplePack(t, s, "same-action", workID)
	approval := json.RawMessage(`{"spec_mandate":[],"law_modifies":[],"architecture_binding":{"domain_registry_content_hash":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}}`)
	if err := applyInvestigationGateApproval(t, s, workID, version, "same-action-refusal", actor, approval, nil); err == nil {
		t.Fatal("approve_contract admitted pending questions without research_bindings")
	} else {
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindMissingEvidence {
			t.Fatalf("pending-question refusal=%v, want missing evidence", err)
		}
	}
	if err := applyInvestigationGateApproval(t, s, workID, version, "same-action-approval", actor, approval, []ResearchBindingDeclaration{{PackID: pack.PackID, Revision: 1, UseRole: UseDecisionBasis, Required: true}}); err != nil {
		t.Fatalf("approve_contract with same-action research_bindings refused: %v", err)
	}
	var consumers int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM active_research_consumers WHERE consumer_work_id=? AND pack_id=? AND revision=?`, workID, pack.PackID, 1).Scan(&consumers); err != nil {
		t.Fatal(err)
	}
	if consumers != 1 {
		t.Fatalf("same-action research binding count=%d, want 1", consumers)
	}
}

func applyInvestigationGateApproval(t *testing.T, s *Store, workID string, version int64, operationID string, actor WorkflowActor, payload json.RawMessage, bindings []ResearchBindingDeclaration) error {
	t.Helper()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	_, actionErr := applyWorkflowActionRawTx(context.Background(), tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "approve_contract", Payload: testApprovalPayload("approve_contract", payload), Actor: actor,
		AcceptedInputsDigest: "sha256:investigation-gate", IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID,
		RequestID: "request:" + operationID, ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC), ResearchBindings: bindings,
	})
	if leaveErr := leaveFold(context.Background(), tx); actionErr == nil {
		actionErr = leaveErr
	}
	if actionErr != nil {
		_ = tx.Rollback()
		return actionErr
	}
	return tx.Commit()
}

func insertInvestigationGateObservation(t *testing.T, s *Store, workID, observationID string, refs []string) {
	t.Helper()
	encoded, err := json.Marshal(refs)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	_, err = tx.ExecContext(context.Background(), `INSERT INTO work_observations(observation_id,work_id,statement,refs,tags,recorded_at) VALUES(?,?,?,?,?,?)`, observationID, workID, "technical investigation", string(encoded), `[]`, time.Now().UTC().Format(time.RFC3339Nano))
	leaveErr := leaveFold(context.Background(), tx)
	if err == nil {
		err = leaveErr
	}
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestOperatorQuestionWithheldForAnyApprovalRequiredAction(t *testing.T) {
	s := openTemp(t)
	workID := "investigation-gate-question"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	ctx := context.Background()

	// With no observation the path produces no question, whatever the
	// approval-required action is. The verify step's confirm_premise question
	// and the planning step's approve_contract question take the same gate.
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := WorkflowDefinitionDigest(registered.Definition)
	if err != nil {
		t.Fatal(err)
	}
	definition := WorkflowReadDefinition{Ref: registered.Definition.Ref, Version: registered.Definition.Version, Digest: digest}
	if q, _, err := workflowOperatorQuestionTx(ctx, s.DatabaseForTesting(), workID, "verify", 1, definition, WorkflowReadContract{}); err != nil {
		t.Fatalf("question read failed: %v", err)
	} else if q != nil {
		t.Fatal("confirm_premise question admitted without an investigation artifact")
	}
	if q, _, err := workflowOperatorQuestionTx(ctx, s.DatabaseForTesting(), workID, "planning", 1, definition, WorkflowReadContract{}); err != nil {
		t.Fatalf("question read failed: %v", err)
	} else if q != nil {
		t.Fatal("approve_contract question admitted without an investigation artifact")
	}

	// With a resolvable artifact the same questions are admitted.
	insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("4", 16), []string{"root", "investigation-gate-other-work"})
	if _, _, err := workflowOperatorQuestionTx(ctx, s.DatabaseForTesting(), workID, "verify", 1, definition, WorkflowReadContract{}); err != nil {
		t.Fatalf("question refused after investigation: %v", err)
	}
}
