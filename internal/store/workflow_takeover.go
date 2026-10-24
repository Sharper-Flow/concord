package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
)

// WorkflowStartTakeover names the attempt a fresh fenced start would take
// from its holder. CD-0013 criterion 25 admits that takeover only with an
// approval bound to the held epoch and the holder; CD-0008 D4 forbids
// deciding that an attempt is stuck from liveness, so the holder's own
// recorded relinquishment is the only release.
type WorkflowStartTakeover struct {
	HeldEpoch      int64
	HolderActorRef string
}

// WorkflowActionOpensHeldAttempt reports whether an action opens a fresh
// fenced attempt on its step that another actor may hold: its execution mode
// is fenced, so its fold records the started event that opens the next
// attempt epoch.
func WorkflowActionOpensHeldAttempt(action WorkflowActionDefinition) bool {
	return action.ExecutionMode == ActionFenced
}

// WorkflowStartTakeoverRead opens its own read transaction around the single
// implementation (the store connection invariant).
func WorkflowStartTakeoverRead(ctx context.Context, s *Store, action WorkflowActionDefinition, workID, callerActorRef string) (*WorkflowStartTakeover, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "workflow_action", "store is not open", false, "open the authority database")
	}
	readTx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot open the takeover read transaction", true, "retry once the store is readable", err)
	}
	defer func() { _ = readTx.Rollback() }()
	return workflowStartTakeover(ctx, readTx, action, workID, callerActorRef)
}

// workflowStartTakeover returns nil when the caller may start freely: no
// attempt has started on the current entry of the step, the caller holds the
// attempt's writing authority, or the holder relinquished it. The attempt's
// holder is its starter until a lane dispatched after the start takes the
// authority, which the fenced-writer rule then names. A lane relinquishes
// through a recorded result decision, which that rule already reads; a session
// relinquishes through session_vacate after it took the authority. Leaving the
// step ends the attempt, so a start on a re-entered step takes nothing over.
//
// A worker-lane dispatch takes over only an unfinished lane attempt another
// session dispatched: its worker is running or its result awaits a decision.
// A failed attempt is finished, and the next dispatch is a CD-0148 retry
// whose approval binds the failed attempt.
func workflowStartTakeover(ctx context.Context, q queryer, action WorkflowActionDefinition, workID, callerActorRef string) (*WorkflowStartTakeover, error) {
	var step, executionActor string
	if err := q.QueryRowContext(ctx, `SELECT current_step,COALESCE(execution_actor_ref,'') FROM workflow_instances WHERE work_id=?`, workID).Scan(&step, &executionActor); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, workflowProjectionError(err, "cannot read the current workflow step for the takeover")
	}
	var startSeq int64
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT seq,payload FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.step_id')=? ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkflowActionStarted, step).Scan(&startSeq, &raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, workflowProjectionError(err, "cannot inspect the latest workflow action start for the takeover")
	}
	var start workflowActionStartedPayload
	if err := json.Unmarshal([]byte(raw), &start); err != nil {
		return nil, newFailure(KindInvariantViolation, "workflow_action", "workflow action start payload is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	var left int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind IN (?,?) AND seq>? AND json_extract(payload,'$.step_id')<>?`, string(SubjectWorkItem), workID, WorkflowActionStarted, WorkflowActionCompleted, startSeq, step).Scan(&left); err != nil {
		return nil, workflowProjectionError(err, "cannot inspect whether the held attempt's step was left")
	}
	if left > 0 {
		return nil, nil
	}
	if action.RequiredCapability == WorkerDispatchCapability {
		held, err := workflowLaneAttemptHeldFrom(ctx, q, workID, step, startSeq, start, callerActorRef)
		if err != nil || !held {
			return nil, err
		}
	}
	holder, _, since, err := workflowFencedWriter(ctx, q, workID, executionActor, math.MaxInt64)
	if err != nil {
		return nil, err
	}
	if since < startSeq {
		holder = start.ActorRef
	}
	if holder == "" || holder == callerActorRef {
		return nil, nil
	}
	var vacated int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM domain_events e JOIN workflow_actors a ON a.actor_ref=?
		WHERE e.subject_type=? AND e.subject_id=? AND e.kind='work.session_vacated' AND e.seq>?
		AND json_extract(e.payload,'$.session_ref')=a.session_ref`, holder, string(SubjectWorkItem), workID, max(since, startSeq)).Scan(&vacated); err != nil {
		return nil, workflowProjectionError(err, "cannot inspect the holder's session vacates")
	}
	if vacated > 0 {
		return nil, nil
	}
	return &WorkflowStartTakeover{HeldEpoch: start.AttemptEpoch, HolderActorRef: holder}, nil
}

// workflowLaneAttemptHeldFrom reports whether the step's latest start is a
// lane dispatch whose attempt another session dispatched and that has not
// ended through a recorded result decision, a terminal failure, or a
// born-failed dispatch.
func workflowLaneAttemptHeldFrom(ctx context.Context, q queryer, workID, step string, startSeq int64, start workflowActionStartedPayload, callerActorRef string) (bool, error) {
	if start.ActorRef == callerActorRef {
		return false, nil
	}
	var attemptID string
	err := q.QueryRowContext(ctx, `SELECT json_extract(payload,'$.worker_attempt_id') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND seq>? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.step_id')=? AND json_extract(payload,'$.attempt_epoch')=? AND json_extract(payload,'$.worker_attempt_id') IS NOT NULL ORDER BY seq LIMIT 1`, string(SubjectWorkItem), workID, WorkflowActionCompleted, startSeq, step, start.AttemptEpoch).Scan(&attemptID)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, workflowProjectionError(err, "cannot inspect the held lane attempt for the takeover")
	}
	var ended int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND seq>? AND (
		(kind=? AND json_extract(payload,'$.action_id') IN (`+workerAttemptDisposalActions+`) AND json_extract(payload,'$.worker_attempt_id')=?)
		OR (kind=? AND json_extract(payload,'$.attempt_id')=?)
		OR (kind=? AND json_extract(payload,'$.attempt_id')=? AND json_extract(payload,'$.terminal')='failed'))`, string(SubjectWorkItem), workID, startSeq, WorkflowActionCompleted, attemptID, WorkerFailed, attemptID, WorkerDispatched, attemptID).Scan(&ended); err != nil {
		return false, workflowProjectionError(err, "cannot inspect whether the held lane attempt ended")
	}
	return ended == 0, nil
}

// guardStartTakeover rereads the held attempt inside the action transaction.
// A takeover needs the exact approval minted for that held attempt; an
// approval minted for a held attempt that has since changed authorizes
// nothing.
func guardStartTakeover(ctx context.Context, q queryer, action WorkflowActionDefinition, workID, actorRef string, approved *WorkflowStartTakeover) error {
	takeover, err := workflowStartTakeover(ctx, q, action, workID, actorRef)
	if err != nil {
		return err
	}
	if approved == nil {
		if takeover != nil {
			return newFailure(KindApprovalRequired, "workflow_action", "this fenced start would take over an attempt its holder has not relinquished", false, "obtain operator approval bound to the held attempt epoch and holder, or have the holder relinquish it")
		}
		return nil
	}
	if takeover == nil || *takeover != *approved {
		return newFailure(KindApprovalInvalid, "workflow_action", "the held attempt changed after the takeover approval challenge", false, "reread the work item and request a fresh approval")
	}
	return nil
}
