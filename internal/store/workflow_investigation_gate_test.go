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
	if q, err := workflowOperatorQuestionTx(ctx, s.DatabaseForTesting(), workID, "verify", 1, definition, WorkflowReadContract{}); err != nil {
		t.Fatalf("question read failed: %v", err)
	} else if q != nil {
		t.Fatal("confirm_premise question admitted without an investigation artifact")
	}
	if q, err := workflowOperatorQuestionTx(ctx, s.DatabaseForTesting(), workID, "planning", 1, definition, WorkflowReadContract{}); err != nil {
		t.Fatalf("question read failed: %v", err)
	} else if q != nil {
		t.Fatal("approve_contract question admitted without an investigation artifact")
	}

	// With a resolvable artifact the same questions are admitted.
	insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("4", 16), []string{"root", "investigation-gate-other-work"})
	if _, err := workflowOperatorQuestionTx(ctx, s.DatabaseForTesting(), workID, "verify", 1, definition, WorkflowReadContract{}); err != nil {
		t.Fatalf("question refused after investigation: %v", err)
	}
}
