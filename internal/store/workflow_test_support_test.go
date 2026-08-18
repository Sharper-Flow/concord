package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func legacyImplementationDigest(t *testing.T) string {
	t.Helper()
	definition, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 1)
	if !ok {
		t.Fatal("legacy implementation definition is not registered")
	}
	return definition.Digest
}

// applyWorkflowTestOperation models the owning workflow route for white-box
// fold tests. Production callers must use the dispatcher or initialization and
// completion entry points; generic ApplyOperation intentionally rejects the
// reserved event families.
func applyWorkflowTestOperation(ctx context.Context, s *Store, operation Operation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := enterFold(ctx, tx); err != nil {
		tx.Rollback()
		return err
	}
	_, err = applyWorkflowOperationTx(ctx, tx, operation)
	_ = leaveFold(ctx, tx)
	if err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// replayWorkflowAuthority is the corpus-only authority fixture seam. It uses
// the production durable claim/completion protocol so condition folds can
// validate evidence against a real completed operation; it never inserts a
// durable_operations row directly.
func replayWorkflowAuthority(ctx context.Context, s *Store, opID, workID, principal, requestID string, evidence []string) error {
	digest := "sha256:" + strings.Repeat("a", 64)
	workflowTypeRef, workflowTypeVersion, stepID := "workflow.test", 1, "evidence"
	_ = s.DatabaseForTesting().QueryRowContext(ctx, `SELECT definition_ref,definition_version,current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&workflowTypeRef, &workflowTypeVersion, &stepID)
	if stepID == "start" {
		if definition, definitionErr := BuiltinWorkflowDefinitionForRef(workflowTypeRef); definitionErr == nil {
			stepID = definition.Definition.StepGraph.StartStep
		}
	}
	claimed, err := ClaimStep(ctx, s, ClaimRequest{
		OpID: opID, WorkID: workID, WorkflowTypeRef: workflowTypeRef, WorkflowTypeVersion: workflowTypeVersion,
		StepID: stepID, StepKind: StepInternalSQLite, AcceptedInputsDigest: digest,
		AcceptedScopeSnapshot: `{}`, PrincipalRef: principal, Tool: "workflow-corpus", IdempotencyKey: opID,
		RequestID: requestID, ObservedAt: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC), ContractVersion: "2.0.0",
	})
	if err != nil {
		return err
	}
	_, err = CompleteStep(ctx, s, CompleteRequest{
		OpID: opID, AttemptEpoch: claimed.AttemptEpoch, ResultKind: ResultCompleted,
		EvidenceRefs: evidence, PrincipalRef: principal, Tool: "workflow-corpus", IdempotencyKey: opID,
		RequestID: requestID, ObservedAt: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC),
		CompletedAt: ptrTime(time.Date(2026, 8, 9, 0, 0, 1, 0, time.UTC)),
	})
	return err
}

func ptrTime(value time.Time) *time.Time { return &value }
