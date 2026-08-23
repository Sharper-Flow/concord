package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

const testManifestDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

// workflowFixtureRef names the test-only workflow family that fold, projection,
// and supersession fixtures pin. It carries the implementation step graph so
// step IDs stay familiar, and a work kind without Product-truth authority so a
// contract fixture stays about the mechanism under test rather than about
// architecture binding. Version 2 adds the worker action pair, which gives the
// registry a real supersession to prove against; the shipped built-ins carry
// exactly one version each.
const workflowFixtureRef = "workflow.test_fixture"

// workflowFixtureWorkKind is the payload spelling of the fixture family's work
// kind.
const workflowFixtureWorkKind = "static_analysis"

// The fixture family registers at test-binary initialization so cross-process
// race workers, which re-enter the binary without running a helper, resolve the
// same pins as their parent.
var workflowFixtureVersions = registerWorkflowFixtureFamily()

func registerWorkflowFixtureFamily() []RegisteredDefinition {
	registry := BuiltinWorkflowRegistry()
	registered := make([]RegisteredDefinition, 0, 2)
	for _, definition := range []WorkflowDefinition{
		workflowFixtureShape(builtinImplementation(), 1),
		workflowFixtureShape(withWorkerActions(builtinImplementation()), 2),
	} {
		entry, err := registry.Register(definition)
		if err != nil {
			panic(err)
		}
		registered = append(registered, entry)
	}
	return registered
}

func workflowFixtureDefinition(t *testing.T, version int64) RegisteredDefinition {
	t.Helper()
	if version < 1 || version > int64(len(workflowFixtureVersions)) {
		t.Fatalf("workflow fixture v%d is not registered", version)
	}
	return workflowFixtureVersions[version-1]
}

func workflowFixtureShape(definition WorkflowDefinition, version int64) WorkflowDefinition {
	definition.Ref = workflowFixtureRef
	definition.Version = version
	definition.WorkKind = WorkKindStaticAnalysis
	changesProductTruth := false
	definition.ChangesProductTruth = &changesProductTruth
	return definition
}

func workflowFixtureDigest(t *testing.T) string {
	t.Helper()
	return workflowFixtureDefinition(t, 1).Digest
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
		RequestID: requestID, ObservedAt: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC), ContractDigest: testManifestDigest,
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
