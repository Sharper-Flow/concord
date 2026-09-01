package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// WorkflowInitializationRequest contains only authenticated identity and the
// registry-selected definition. The caller cannot supply an actor reference or
// a definition digest independently of the immutable registry entry.
type WorkflowInitializationRequest struct {
	WorkID     string
	Definition RegisteredDefinition
	Actor      WorkflowActor
	Now        time.Time
}

// InitializeWorkflowTx records the initial actor tuple and immutable
// definition pin for an already-created work item. It is intentionally a
// workflow-specific transaction seam, not a generic event append operation.
// Capture and revise call it before their owning transaction commits.

// DefaultWorkflowRefForKind names the workflow a captured work item pins when
// the capture names none. Session-prepare reads C19 continuity unconditionally,
// so there is no valid capture without a workflow instance (#650).
func DefaultWorkflowRefForKind(kind string) string {
	switch kind {
	case "task":
		return "workflow.implementation"
	case "bug":
		return "workflow.break_fix"
	case "research":
		return "workflow.research"
	default:
		return "workflow.generic_one_off"
	}
}

func InitializeWorkflowTx(ctx context.Context, transaction *Transaction, request WorkflowInitializationRequest) error {
	tx, err := transactionSQL(transaction, "workflow_initialize")
	if err != nil {
		return err
	}
	if request.Now.IsZero() {
		request.Now = transaction.now()
	}
	return initializeWorkflowRawTx(ctx, tx, request)
}

func initializeWorkflowRawTx(ctx context.Context, tx *sql.Tx, request WorkflowInitializationRequest) error {
	if tx == nil {
		return newFailure(KindInvalidOperation, "workflow_initialize", "transaction is not open", false, "supply an active store transaction")
	}
	if request.WorkID == "" || request.Definition.Definition.Ref == "" {
		return newFailure(KindInvalidOperation, "workflow_initialize", "work ID and registered definition are required", false, "resolve the workflow definition before initialization")
	}
	if request.Now.IsZero() {
		request.Now = nowFromClock(nil)
	}
	if _, err := VerifyWorkflowDefinitionPin(BuiltinWorkflowRegistry(), WorkflowDefinitionPin{Ref: request.Definition.Definition.Ref, Version: request.Definition.Definition.Version, Digest: request.Definition.Digest}); err != nil {
		return err
	}
	actorRef, err := WorkflowActorRef(request.Actor)
	if err != nil {
		return err
	}
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, request.WorkID).Scan(&version); err != nil {
		return newFailure(KindProjectionNotFound, "workflow_initialize", "work item does not exist", false, "create the work item before initializing its workflow")
	}
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_instances WHERE work_id=?`, request.WorkID).Scan(&existing); err != nil {
		return wrapFailure(KindUnavailable, "workflow_initialize", "cannot inspect workflow initialization", true, "retry once the database is readable", err)
	}
	if existing != 0 {
		return newFailure(KindProjectionConflict, "workflow_initialize", "work item already has a workflow instance", false, "use the existing pinned workflow")
	}

	actorPayload, _ := json.Marshal(map[string]any{
		"work_id": request.WorkID, "expected_version": version, "resulting_version": version + 1,
		"actor_ref": actorRef, "principal_ref": request.Actor.PrincipalRef, "client_ref": request.Actor.ClientRef,
		"agent_ref": request.Actor.AgentRef, "session_ref": request.Actor.SessionRef, "actor_class": request.Actor.ActorClass,
	})
	definitionPayload, _ := json.Marshal(map[string]any{
		"work_id": request.WorkID, "expected_version": version + 1, "resulting_version": version + 2,
		"ref": request.Definition.Definition.Ref, "version": request.Definition.Definition.Version,
		"digest": request.Definition.Digest, "work_kind": request.Definition.Definition.WorkKind,
	})
	var guardCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM fold_guard WHERE active=1`).Scan(&guardCount); err != nil {
		return wrapFailure(KindUnavailable, "workflow_initialize", "cannot inspect projection fold guard", true, "retry once the database is readable", err)
	}
	ownedGuard := guardCount == 0
	if ownedGuard {
		if err := enterFold(ctx, tx); err != nil {
			return err
		}
		defer func() { _ = leaveFold(ctx, tx) }()
	}
	_, err = applyWorkflowOperationTx(ctx, tx, Operation{
		Events: []Event{
			{EventID: "workflow-init:" + request.WorkID + ":actor:" + actorRef, Kind: WorkflowActorRecorded, SubjectType: SubjectWorkItem, SubjectID: request.WorkID, Actor: actorRef, OccurredAt: request.Now.UTC(), PayloadVersion: 1, Payload: actorPayload},
			{EventID: "workflow-init:" + request.WorkID + ":definition:" + request.Definition.Definition.Ref, Kind: WorkflowDefinitionSelected, SubjectType: SubjectWorkItem, SubjectID: request.WorkID, Actor: actorRef, OccurredAt: request.Now.UTC(), PayloadVersion: 1, Payload: definitionPayload},
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, request.WorkID): version},
	})
	return err
}
