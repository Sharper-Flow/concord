package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// WorkflowStartTakeover pins the epoch and holder observed before a fenced start.
type WorkflowStartTakeover struct {
	HeldEpoch      int64
	HolderActorRef string
}

func WorkflowActionOpensHeldAttempt(action WorkflowActionDefinition) bool {
	return action.ExecutionMode == ActionFenced
}

func WorkflowStartTakeoverRead(ctx context.Context, s *Store, action WorkflowActionDefinition, request WorkflowActionPreflightRequest) (*WorkflowStartTakeover, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "workflow_action", "store is not open", false, "open the authority database")
	}
	readTx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot open the takeover read transaction", true, "retry once the store is readable", err)
	}
	defer func() { _ = readTx.Rollback() }()
	return workflowStartTakeover(ctx, readTx, action, request.WorkID, DeriveWorkflowActorRef(request.Actor.PrincipalRef, request.Actor.ClientRef, request.Actor.AgentRef, request.Actor.SessionRef))
}

func workflowStartTakeover(ctx context.Context, q queryer, action WorkflowActionDefinition, workID, callerActorRef string) (*WorkflowStartTakeover, error) {
	if !WorkflowActionOpensHeldAttempt(action) {
		return nil, nil
	}
	var step string
	if err := q.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, workflowProjectionError(err, "cannot read the current workflow step for the takeover")
	}
	startEvent, err := readWorkflowActionStartTx(ctx, q, workID, step)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, workflowProjectionError(err, "cannot inspect the latest workflow action start for the takeover")
	}
	var start workflowActionStartedPayload
	if err := json.Unmarshal(startEvent.Payload, &start); err != nil {
		return nil, newFailure(KindInvariantViolation, "workflow_action", "workflow action start payload is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	var left bool
	if err := q.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM workflow_step_epochs WHERE work_id=? AND step_id<>? AND latest_start_seq>?) OR
		EXISTS(SELECT 1 FROM workflow_step_completions WHERE work_id=? AND step_id<>? AND completion_seq>?)`, workID, step, startEvent.Seq, workID, step, startEvent.Seq).Scan(&left); err != nil {
		return nil, workflowProjectionError(err, "cannot inspect whether the held attempt's step was left")
	}
	if left || start.ActorRef == "" || start.ActorRef == callerActorRef {
		return nil, nil
	}
	var vacated bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM session_vacate_requests r JOIN workflow_actors a ON a.actor_ref=?
		WHERE r.work_id=? AND r.request_seq>? AND r.session_ref=a.session_ref)`, start.ActorRef, workID, startEvent.Seq).Scan(&vacated); err != nil {
		return nil, workflowProjectionError(err, "cannot inspect the holder's session vacates")
	}
	if vacated {
		return nil, nil
	}
	return &WorkflowStartTakeover{HeldEpoch: start.AttemptEpoch, HolderActorRef: start.ActorRef}, nil
}

func guardStartTakeover(ctx context.Context, q queryer, action WorkflowActionDefinition, workID, actorRef string, expected *WorkflowStartTakeover) error {
	takeover, err := workflowStartTakeover(ctx, q, action, workID, actorRef)
	if err != nil {
		return err
	}
	if expected == nil {
		if takeover != nil {
			return newFailure(KindOperationConflict, "workflow_action", "the held attempt changed after the takeover read", false, "reread the work item and retry against its current held attempt")
		}
		return nil
	}
	if takeover == nil || *takeover != *expected {
		return newFailure(KindOperationConflict, "workflow_action", "the held attempt changed after the takeover read", false, "reread the work item and retry against its current held attempt")
	}
	return nil
}
