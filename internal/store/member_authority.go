package store

import (
	"context"
	"encoding/json"
)

// MemberOperationScope intersects retained member authority with the host's
// already authorized invocation. It never grants a client capability.
type MemberOperationScope struct {
	WorkID, ProjectID, Operation, ActionID, AttemptID string
	Read                                              bool
}

func (s *Store) CheckMemberOperation(ctx context.Context, actor WorkflowActor, request MemberOperationScope) error {
	return checkMemberOperation(ctx, s.db, actor, request)
}

func CheckMemberOperationTx(ctx context.Context, tx *Transaction, actor WorkflowActor, request MemberOperationScope) error {
	q, err := transactionSQL(tx, "member_scope")
	if err != nil {
		return err
	}
	return checkMemberOperation(ctx, q, actor, request)
}

// CheckMemberMutationTx rechecks narrowing after acquiring the mutation's
// transaction, including specialized envelopes outside the common effect planner.
func CheckMemberMutationTx(ctx context.Context, tx *Transaction, actor WorkflowActor, operation string) error {
	q, err := transactionSQL(tx, "member_scope")
	if err != nil {
		return err
	}
	member, err := readWorkflowMemberCallerQ(ctx, q, actor)
	if err != nil || member == nil {
		return err
	}
	switch operation {
	case "workflow_action", "worktree_claim", "worktree_verify", "worktree_reclaim", "session_vacate", "worker_reconcile", "worker_abandon":
		return nil
	}
	return newFailure(KindUnauthorized, "member_scope", "member caller cannot acquire Main mutation authority", false, "return the action to the group controller")
}

func checkMemberOperation(ctx context.Context, q queryer, actor WorkflowActor, request MemberOperationScope) error {
	member, err := readWorkflowMemberCallerQ(ctx, q, actor)
	if err != nil || member == nil {
		return err
	}
	refuse := func() error {
		return newFailure(KindUnauthorized, "member_scope", "member caller is restricted to its own recorded assignment", false, "return Main-only or foreign-scope work to the group controller")
	}
	if request.Read && request.Operation == "concord_product_view.resolve" {
		return nil
	}
	if request.Operation == "concord_work_transition.session_vacate" && request.WorkID == "" {
		request.WorkID = member.WorkID
	}
	if request.WorkID != member.WorkID || request.ProjectID != member.ProjectID {
		return refuse()
	}
	if request.Read {
		return nil
	}
	switch request.Operation {
	case "concord_work_transition.worktree_claim", "concord_work_transition.worktree_verify", "concord_work_transition.session_vacate", "concord_work_transition.worktree_reclaim":
		return nil
	case "concord_work_transition.workflow_action":
		if request.ActionID == "dispatch_worker" {
			return nil
		}
	case "concord_work_transition.worker_abandon", "concord_work_transition.worker_reconcile":
		var own bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worker_authorizations WHERE work_id=? AND attempt_id=? AND json_extract(payload,'$.actor_ref')=?)`, member.WorkID, request.AttemptID, member.ActorRef).Scan(&own); err != nil {
			return err
		}
		if own {
			return nil
		}
	}
	return refuse()
}

func guardWorkflowMemberAction(ctx context.Context, q queryer, request WorkflowActionExecutionRequest) error {
	member, err := readWorkflowMemberCallerQ(ctx, q, request.Actor)
	if err != nil || member == nil {
		return err
	}
	if request.ActionID != "dispatch_worker" || request.WorkID != member.WorkID {
		return newFailure(KindUnauthorized, "member_scope", "member caller cannot perform Main workflow actions", false, "return the action to the group controller")
	}
	owner := SessionWorktreeOwner{ClientRef: request.Actor.ClientRef, AgentRef: request.Actor.AgentRef, SessionRef: request.Actor.SessionRef}
	if _, err := workflowMemberClaimScope(ctx, q, member.WorkID, member.ProjectID, owner); err != nil {
		return err
	}
	var job WorkerJobBinding
	var confirmed bool
	if err := q.QueryRowContext(ctx, `SELECT job_id,job_revision,job_digest,confirmed FROM workflow_group_members WHERE work_id=? AND group_id=? AND member_id=? AND actor_ref=?`, member.WorkID, member.GroupID, member.MemberID, member.ActorRef).Scan(&job.JobID, &job.Revision, &job.Digest, &confirmed); err != nil {
		return err
	}
	var fields struct {
		Packet struct {
			Inputs struct {
				Job WorkerJobBinding `json:"worker_job"`
			} `json:"inputs"`
		} `json:"worker_packet"`
	}
	if err := json.Unmarshal(request.Payload, &fields); err != nil {
		return err
	}
	if !confirmed || fields.Packet.Inputs.Job != job {
		return newFailure(KindUnauthorizedDispatch, "worker_dispatch", "member dispatch requires its confirmed claim and exact assigned job", false, "claim the assigned member worktree and dispatch only its recorded job")
	}
	entry, err := activeWorktreeEntryForProject(WithSessionWorktreeOwner(ctx, owner), q, "worker_dispatch", member.WorkID, member.ProjectID)
	if err != nil {
		return err
	}
	canonical, err := canonicalWorkerWorktreePath(request.SessionWorktree)
	claimed, claimErr := canonicalWorkerWorktreePath(entry.Path)
	if err != nil || claimErr != nil || canonical != claimed {
		return newFailure(KindUnauthorizedDispatch, "worker_dispatch", "member caller is not in its own claimed worktree", false, "resume the assigned member worktree before dispatch")
	}
	return nil
}

func requireNonMemberMain(ctx context.Context, q queryer, actor string) error {
	var member bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_member_callers WHERE actor_ref=?)`, actor).Scan(&member)
	if err != nil {
		return err
	}
	if member {
		return newFailure(KindUnauthorized, "member_scope", "a retained member caller is not Main", false, "return the action to the group controller")
	}
	return nil
}
