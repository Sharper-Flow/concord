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
func WorkflowStartTakeoverRead(ctx context.Context, s *Store, action WorkflowActionDefinition, request WorkflowActionPreflightRequest) (*WorkflowStartTakeover, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "workflow_action", "store is not open", false, "open the authority database")
	}
	identity := ""
	if action.RequiredCapability == WorkerDispatchCapability {
		canonical, err := canonicalWorkerWorktreePath(request.SessionWorktree)
		if err != nil {
			return nil, newFailure(KindUnauthorizedDispatch, "worker_dispatch", "host session worktree identity cannot be resolved", false, "refresh the host session boundary")
		}
		identity = workerWorktreeIdentity(canonical)
	}
	readTx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot open the takeover read transaction", true, "retry once the store is readable", err)
	}
	defer func() { _ = readTx.Rollback() }()
	return workflowStartTakeover(ctx, readTx, action, request.WorkID, DeriveWorkflowActorRef(request.Actor.PrincipalRef, request.Actor.ClientRef, request.Actor.AgentRef, request.Actor.SessionRef), workflowDispatchAssignment(request.Payload), identity)
}

// workflowStartTakeover returns nil when no foreign takeover is required: no
// attempt has started on the current entry of the step, the caller holds the
// attempt's writing authority, or the holder relinquished it. The attempt's
// holder is its starter until a lane dispatched after the start takes the
// authority, which the fenced-writer rule then names. A lane relinquishes
// through a recorded result decision, which that rule already reads; a session
// relinquishes through session_vacate after it took the authority. Leaving the
// step ends the attempt, so a start on a re-entered step takes nothing over.
// An unresolved own child that conflicts with this dispatch defers refusal to
// dispatch admission, after exact replay and selected-job readiness.
//
// A worker-lane dispatch takes over only an unfinished lane attempt another
// session dispatched: its worker is running or its result awaits a decision.
// A failed attempt is finished, and the next dispatch is a retry
// whose approval binds the failed attempt.
func workflowStartTakeover(ctx context.Context, q queryer, action WorkflowActionDefinition, workID, callerActorRef string, assignment workerRestartAssignment, identity string) (*WorkflowStartTakeover, error) {
	var step, executionActor string
	if err := q.QueryRowContext(ctx, `SELECT current_step,COALESCE(execution_actor_ref,'') FROM workflow_instances WHERE work_id=?`, workID).Scan(&step, &executionActor); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, workflowProjectionError(err, "cannot read the current workflow step for the takeover")
	}
	startEvent, err := readWorkflowActionStartTx(ctx, q, workID, step, false)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, workflowProjectionError(err, "cannot inspect the latest workflow action start for the takeover")
	}
	startSeq := startEvent.Seq
	var start workflowActionStartedPayload
	if err := json.Unmarshal(startEvent.Payload, &start); err != nil {
		return nil, newFailure(KindInvariantViolation, "workflow_action", "workflow action start payload is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	var left bool
	if err := q.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM workflow_step_epochs WHERE work_id=? AND step_id<>? AND latest_start_seq>?) OR
		EXISTS(SELECT 1 FROM workflow_step_completions WHERE work_id=? AND step_id<>? AND completion_seq>?)`, workID, step, startSeq, workID, step, startSeq).Scan(&left); err != nil {
		return nil, workflowProjectionError(err, "cannot inspect whether the held attempt's step was left")
	}
	if left {
		return nil, nil
	}
	if action.RequiredCapability == WorkerDispatchCapability {
		assignment.stepID = step
		if own, err := workflowUnresolvedOwnWorkerAttempt(ctx, q, workID, callerActorRef, assignment, identity); err != nil || own != "" {
			return nil, err
		}
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
	var vacated bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM session_vacate_requests r JOIN workflow_actors a ON a.actor_ref=?
		WHERE r.work_id=? AND r.request_seq>? AND r.session_ref=a.session_ref)`, holder, workID, max(since, startSeq)).Scan(&vacated); err != nil {
		return nil, workflowProjectionError(err, "cannot inspect the holder's session vacates")
	}
	if vacated {
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
	err := q.QueryRowContext(ctx, `SELECT attempt_id FROM worker_authorizations WHERE work_id=? AND seq>? AND json_extract(payload,'$.step_id')=? AND json_extract(payload,'$.attempt_epoch')=? ORDER BY seq LIMIT 1`, workID, startSeq, step, start.AttemptEpoch).Scan(&attemptID)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, workflowProjectionError(err, "cannot inspect the held lane attempt for the takeover")
	}
	var ended bool
	if err := q.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM worker_attempt_dispositions WHERE work_id=? AND attempt_id=? AND seq>?) OR
		EXISTS(SELECT 1 FROM worker_attempts WHERE work_id=? AND attempt_id=? AND
		 ((terminal_seq>? AND terminal_kind=?) OR (dispatch_seq>? AND json_extract(dispatch_payload,'$.terminal')='failed')))`, workID, attemptID, startSeq, workID, attemptID, startSeq, WorkerFailed, startSeq).Scan(&ended); err != nil {
		return false, workflowProjectionError(err, "cannot inspect whether the held lane attempt ended")
	}
	return !ended, nil
}

// guardStartTakeover checks foreign takeover authority inside the action
// transaction. A takeover needs the exact
// approval minted for the current held attempt; a changed holder or epoch
// invalidates that approval.
func guardStartTakeover(ctx context.Context, q queryer, action WorkflowActionDefinition, workID, actorRef string, assignment workerRestartAssignment, identity string, approved *WorkflowStartTakeover) error {
	takeover, err := workflowStartTakeover(ctx, q, action, workID, actorRef, assignment, identity)
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

func guardWorkerDispatchRestart(ctx context.Context, q queryer, workID, actorRef string, assignment workerRestartAssignment, identity string) error {
	attemptID, err := workflowUnresolvedOwnWorkerAttempt(ctx, q, workID, actorRef, assignment, identity)
	if err != nil {
		return err
	}
	if attemptID != "" {
		return newFailure(KindOperationConflict, "worker_dispatch", "the caller's started worker attempt "+attemptID+" has no terminal outcome", false, "reconcile the original Task through worker_reconcile, or close the exact attempt through admitted worker_abandon before authorizing another attempt")
	}
	return nil
}

// A job's stable identity survives revisions and contracts. Job-less pins bind
// the work's step and lane; an attempt or packet digest is never an assignment.
type workerRestartAssignment struct {
	jobID, stepID, laneID string
}

func (a workerRestartAssignment) same(b workerRestartAssignment) bool {
	if a.jobID != "" || b.jobID != "" {
		return a.jobID != "" && a.jobID == b.jobID
	}
	return a.stepID == "" || b.stepID == "" || a.laneID == "" || b.laneID == "" || (a.stepID == b.stepID && a.laneID == b.laneID)
}

// workflowDispatchAssignment reads the packet's lane and job claims; the step is
// the store's current step. Dispatch admission validates the job claim against
// recorded assignment content.
func workflowDispatchAssignment(payload json.RawMessage) workerRestartAssignment {
	var fields struct {
		Packet struct {
			LaneID string `json:"lane_id"`
			Inputs struct {
				Job struct {
					JobID string `json:"job_id"`
				} `json:"worker_job"`
			} `json:"inputs"`
		} `json:"worker_packet"`
	}
	_ = json.Unmarshal(payload, &fields)
	return workerRestartAssignment{jobID: fields.Packet.Inputs.Job.JobID, laneID: fields.Packet.LaneID}
}

// workflowUnresolvedOwnWorkerAttempt examines every outstanding own start,
// never just the latest epoch. Editing authority survives contract supersession;
// read-only work can coexist with a different assignment on any claim. Missing
// claim identity cannot prove a separate claim; unknown lanes block conservatively.
func workflowUnresolvedOwnWorkerAttempt(ctx context.Context, q queryer, workID, actorRef string, assignment workerRestartAssignment, identity string) (string, error) {
	rows, err := q.QueryContext(ctx, `SELECT a.attempt_id,a.lane_id,a.lane_digest,COALESCE(json_extract(c.payload,'$.worker_worktree_identity'),''),COALESCE(json_extract(c.payload,'$.worker_job.job_id'),''),COALESCE(json_extract(c.payload,'$.step_id'),'') FROM worker_attempts a
	 JOIN worker_authorizations c ON c.work_id=a.work_id AND c.attempt_id=a.attempt_id
	 WHERE a.work_id=? AND a.lifecycle_state='dispatched'
	 AND json_extract(c.start_payload,'$.actor_ref')=? ORDER BY c.seq`, workID, actorRef)
	if err != nil {
		return "", workflowProjectionError(err, "cannot inspect the caller's unresolved started worker attempts")
	}
	defer rows.Close()
	for rows.Next() {
		var attemptID, digest, heldIdentity string
		var held workerRestartAssignment
		if err := rows.Scan(&attemptID, &held.laneID, &digest, &heldIdentity, &held.jobID, &held.stepID); err != nil {
			return "", workflowProjectionError(err, "cannot read the caller's unresolved started worker attempt")
		}
		if _, err := LookupLane(held.laneID, digest); err != nil {
			return attemptID, nil
		}
		if workerLaneEdits(held.laneID, digest) {
			if identity == "" || heldIdentity == "" || identity == heldIdentity {
				return attemptID, nil
			}
		} else if assignment.same(held) {
			return attemptID, nil
		}
	}
	return "", rows.Err()
}
