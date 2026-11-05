package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type WorkflowInitializationRequest struct {
	WorkID string
	Actor  WorkflowActor
	Now    time.Time
}

// InitializeWorkflowTx records the actor and opens intake in the caller's transaction.
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
	if request.WorkID == "" {
		return newFailure(KindInvalidOperation, "workflow_initialize", "work ID is required", false, "supply the work item to initialize")
	}
	if request.Now.IsZero() {
		request.Now = nowFromClock(nil)
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
		return newFailure(KindProjectionConflict, "workflow_initialize", "work item already has a workflow instance", false, "use the existing workflow")
	}

	actorPayload, _ := json.Marshal(map[string]any{
		"work_id": request.WorkID, "expected_version": version, "resulting_version": version + 1,
		"actor_ref": actorRef, "principal_ref": request.Actor.PrincipalRef, "client_ref": request.Actor.ClientRef,
		"agent_ref": request.Actor.AgentRef, "session_ref": request.Actor.SessionRef, "actor_class": request.Actor.ActorClass,
	})
	scope := newWriteScope(tx)
	if err := scope.enter(ctx); err != nil {
		return err
	}
	defer func() { _ = scope.close(ctx) }()
	_, err = applyWorkflowOperationTx(ctx, tx, Operation{
		Events: []Event{
			{EventID: "workflow-init:" + request.WorkID + ":actor:" + actorRef, Kind: WorkflowActorRecorded, SubjectType: SubjectWorkItem, SubjectID: request.WorkID, Actor: actorRef, OccurredAt: request.Now.UTC(), PayloadVersion: 1, Payload: actorPayload},
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, request.WorkID): version},
	}, scope)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_instances(work_id,instance_state,execution_actor_ref) VALUES(?,'intake',?)`, request.WorkID, actorRef)
	return workflowProjectionError(err, "cannot initialize workflow intake")
}
