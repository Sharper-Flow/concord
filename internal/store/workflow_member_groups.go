package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// WorkflowMemberCaller is retained authorization narrowing, not execution
// presence. It deliberately has no live-work join.
type WorkflowMemberCaller struct {
	ActorRef, WorkID, ProjectID, GroupID, MemberID string
}

type workflowMemberActor struct {
	PrincipalRef string `json:"principal_ref"`
	ClientRef    string `json:"client_ref"`
	AgentRef     string `json:"agent_ref"`
	SessionRef   string `json:"session_ref"`
}

func (a workflowMemberActor) actor() WorkflowActor {
	return WorkflowActor{PrincipalRef: a.PrincipalRef, ClientRef: a.ClientRef, AgentRef: a.AgentRef, SessionRef: a.SessionRef, ActorClass: ActorAgent}
}

type workflowMemberBinding struct {
	GroupID   string              `json:"group_id"`
	MemberID  string              `json:"member_id"`
	ProjectID string              `json:"project_id"`
	Actor     workflowMemberActor `json:"actor"`
}

type workflowMemberBase struct {
	Commit          string `json:"commit"`
	VerificationRef string `json:"verification_ref"`
}

type workflowGroupMember struct {
	MemberID string `json:"member_id"`
	WorkerJobBinding
	ProjectID string `json:"project_id"`
}

type workflowMemberGroup struct {
	GroupID            string                `json:"group_id"`
	ControllerActorRef string                `json:"controller_actor_ref"`
	BaseSubject        workflowMemberBase    `json:"base_subject"`
	Permission         string                `json:"permission"`
	ReviewRef          string                `json:"review_ref"`
	Members            []workflowGroupMember `json:"members"`
}

func workflowMemberID(group string, job WorkerJobBinding) string {
	return "member-" + strings.TrimPrefix(reviewDigest(struct {
		Group string
		Job   WorkerJobBinding
	}{group, job}), "sha256:")
}

// A slash cannot occur in an authored job ID. Keep admission keys disjoint
// from job aliases even when a job happens to spell a public group ID.
func workflowMemberGroupAdmissionID(group string) string { return "member-group/" + group }

func memberGroupFailure(detail string) error {
	return newFailure(KindInvalidOperation, "workflow_member_group", detail, false, "use the current Main-owned group and exact ready job bindings; reconcile prior effects before reassignment")
}

func readWorkflowMemberCallerQ(ctx context.Context, q queryer, actor WorkflowActor) (*WorkflowMemberCaller, error) {
	ref, err := WorkflowActorRef(actor)
	if err != nil {
		return nil, err
	}
	caller := &WorkflowMemberCaller{ActorRef: ref}
	err = q.QueryRowContext(ctx, `SELECT work_id,project_id,group_id,member_id FROM workflow_member_callers WHERE actor_ref=?`, ref).Scan(&caller.WorkID, &caller.ProjectID, &caller.GroupID, &caller.MemberID)
	if err == sql.ErrNoRows {
		var narrowedSession bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_member_callers m JOIN workflow_actors a ON a.actor_ref=m.actor_ref WHERE a.session_ref=?)`, actor.SessionRef).Scan(&narrowedSession); err != nil {
			return nil, err
		}
		if narrowedSession {
			return nil, newFailure(KindUnauthorized, "member_scope", "session is bound to a different authenticated member tuple", false, "use the original bound client, agent and session")
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return caller, nil
}

func ReadWorkflowMemberCaller(ctx context.Context, s *Store, actor WorkflowActor) (*WorkflowMemberCaller, error) {
	if s == nil || s.db == nil {
		return nil, memberGroupFailure("store is not open")
	}
	return readWorkflowMemberCallerQ(ctx, s.db, actor)
}

func workflowMemberOwnerActor(ctx context.Context, q queryer, owner SessionWorktreeOwner) (WorkflowActor, error) {
	if err := validateSessionWorktreeOwner(owner); err != nil {
		return WorkflowActor{}, err
	}
	actor := WorkflowActor{ClientRef: owner.ClientRef, AgentRef: owner.AgentRef, SessionRef: owner.SessionRef, ActorClass: ActorAgent}
	if err := q.QueryRowContext(ctx, `SELECT principal_ref FROM agent_clients WHERE client_ref=?`, owner.ClientRef).Scan(&actor.PrincipalRef); err != nil {
		return actor, err
	}
	return actor, ValidateWorkflowActor(actor)
}

func workflowMemberClaimScope(ctx context.Context, q queryer, workID, projectID string, owner SessionWorktreeOwner) (string, error) {
	actor, err := workflowMemberOwnerActor(ctx, q, owner)
	if err != nil {
		return "", err
	}
	caller, err := readWorkflowMemberCallerQ(ctx, q, actor)
	if err != nil || caller == nil {
		return "", err
	}
	if caller.WorkID != workID || caller.ProjectID != projectID {
		return "", memberGroupFailure("retained member caller cannot access another work or Project")
	}
	var binding WorkerJobBinding
	var controller string
	err = q.QueryRowContext(ctx, `SELECT m.job_id,m.job_revision,m.job_digest,g.controller_actor_ref
	 FROM workflow_group_members m JOIN workflow_member_groups g USING(work_id,group_id)
	 WHERE m.work_id=? AND m.project_id=? AND m.group_id=? AND m.member_id=? AND m.actor_ref=?`, workID, projectID, caller.GroupID, caller.MemberID, caller.ActorRef).Scan(&binding.JobID, &binding.Revision, &binding.Digest, &controller)
	if err == sql.ErrNoRows {
		return "", memberGroupFailure("retained member caller has no matching current live binding")
	}
	if err != nil {
		return "", err
	}
	if controller == caller.ActorRef {
		return "", memberGroupFailure("member group controller cannot be its own member")
	}
	if err := workflowMemberRequireMain(ctx, q, workID, controller); err != nil {
		return "", err
	}
	if err := workflowMemberGroupCurrent(ctx, q, workID, caller.GroupID); err != nil {
		return "", err
	}
	job, err := readWorkerJobRevisionTx(ctx, q, workID, binding)
	if err != nil {
		return "", err
	}
	if job.ProjectScope != projectID || caller.MemberID != workflowMemberID(caller.GroupID, binding) {
		return "", memberGroupFailure("live member identity or Project differs from the exact recorded job")
	}
	return caller.MemberID, nil
}

// Group authority follows its qualified integration subject and all exact
// member jobs. Contract counters and effect ordering are not dependencies.
func workflowMemberGroupCurrent(ctx context.Context, q queryer, workID, groupID string) error {
	var baseRaw sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT base_subject FROM workflow_member_groups WHERE work_id=? AND group_id=?`, workID, groupID).Scan(&baseRaw); err != nil {
		return err
	}
	if !baseRaw.Valid {
		return memberGroupFailure("ordinary one-job admission has no member authority")
	}
	var base workflowMemberBase
	if err := json.Unmarshal([]byte(baseRaw.String), &base); err != nil {
		return err
	}
	current, err := workflowMemberIntegrationBase(ctx, q, workID)
	if err != nil {
		return err
	}
	if base.Commit == "" || base.VerificationRef == "" || current.Commit != base.Commit {
		return memberGroupFailure("member group integration base changed")
	}
	facts, err := readWorkflowReviewFacts(ctx, q, workID)
	if err != nil {
		return err
	}
	if facts.Candidate != nil && facts.Candidate.Commit != base.Commit {
		return memberGroupFailure("member group base no longer matches the accumulated candidate")
	}
	var count int
	var changed bool
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(NOT EXISTS(SELECT 1 FROM worker_job_revisions j WHERE j.work_id=m.work_id AND j.job_id=m.job_id AND j.revision=m.job_revision AND j.digest=m.job_digest AND j.project_scope=m.project_id AND j.revision=(SELECT MAX(latest.revision) FROM worker_job_revisions latest WHERE latest.work_id=j.work_id AND latest.job_id=j.job_id))),0) FROM workflow_group_members m WHERE m.work_id=? AND m.group_id=?`, workID, groupID).Scan(&count, &changed); err != nil {
		return err
	}
	if count < 2 || count > 8 || changed {
		return memberGroupFailure("member group no longer binds every current job revision and digest")
	}
	return nil
}

func workflowMemberIntegrationBase(ctx context.Context, q queryer, workID string) (workflowMemberBase, error) {
	var base workflowMemberBase
	var receiptRaw string
	err := q.QueryRowContext(ctx, oracleVerifyReceiptSQL+` AND EXISTS(SELECT 1 FROM worktree_entries e WHERE e.set_id=? AND e.project_id=l.project_id AND e.path=l.path AND e.branch=json_extract(l.result_json,'$.branch') AND e.state='active' AND e.member_id='') ORDER BY d.rowid DESC LIMIT 1`, workID, WorktreeSetID(workID)).Scan(&receiptRaw)
	if err == sql.ErrNoRows {
		return base, memberGroupFailure("assignment_group requires a native-qualified integration base")
	}
	if err != nil {
		return base, err
	}
	receipt, err := decodeOracleVerifyReceipt(receiptRaw, workID)
	if err != nil {
		return base, err
	}
	base.Commit = strings.TrimPrefix(receipt.SubjectRef, "commit:")
	base.VerificationRef = receipt.OperationRef
	return base, nil
}

func confirmWorkflowMemberClaimTx(ctx context.Context, tx *sql.Tx, memberID string, owner SessionWorktreeOwner) error {
	if memberID == "" {
		return nil
	}
	actor, err := workflowMemberOwnerActor(ctx, tx, owner)
	if err != nil {
		return err
	}
	caller, err := readWorkflowMemberCallerQ(ctx, tx, actor)
	if err != nil {
		return err
	}
	if caller == nil || caller.MemberID != memberID {
		return memberGroupFailure("claim confirmation does not belong to the authenticated member actor")
	}
	derived, err := workflowMemberClaimScope(ctx, tx, caller.WorkID, caller.ProjectID, owner)
	if err != nil {
		return err
	}
	if derived != memberID {
		return memberGroupFailure("claim confirmation has no exact member scope")
	}
	result, err := tx.ExecContext(ctx, `UPDATE workflow_group_members SET confirmed=1 WHERE work_id=? AND group_id=? AND member_id=? AND actor_ref=?`, caller.WorkID, caller.GroupID, memberID, caller.ActorRef)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return memberGroupFailure("claim confirmation lost its live actor binding")
	}
	return nil
}

func workflowMemberRequireMain(ctx context.Context, q queryer, workID, ref string) error {
	return workflowRequireMain(ctx, q, workID, ref)
}

func workflowMemberVariant(fields map[string]json.RawMessage) bool {
	return fields["assignment_group"] != nil || fields["member_binding"] != nil
}

func deriveWorkflowMemberGroup(ctx context.Context, q queryer, workID, operationID, actor string, bindings []WorkerJobBinding) (*workflowMemberGroup, error) {
	if err := workflowMemberRequireMain(ctx, q, workID, actor); err != nil {
		return nil, err
	}
	if len(bindings) < 2 || len(bindings) > 8 {
		return nil, memberGroupFailure("assignment_group requires 2 to 8 members")
	}
	group := &workflowMemberGroup{GroupID: "group:" + strings.TrimPrefix(nativeDigest([]byte(workID+"\x00"+operationID)), "sha256:"), ControllerActorRef: actor, Members: []workflowGroupMember{}}
	state, err := loadWorkflowReviewState(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	if err := workflowAdmitReview(state, workflowMemberGroupAdmissionID(group.GroupID)); err != nil {
		return nil, err
	}
	group.Permission = "first_entry"
	if state.Accepted != nil {
		group.Permission = "continue"
		group.ReviewRef = state.Accepted.ReportRef
	}
	// Only an active integration entry can qualify this base. A newer member
	// receipt cannot select the accumulated candidate by arrival order.
	group.BaseSubject, err = workflowMemberIntegrationBase(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	if state.Candidate != nil && state.Candidate.Commit != group.BaseSubject.Commit {
		return nil, memberGroupFailure("qualified integration base does not match the accumulated candidate")
	}
	jobs := make([]*WorkerJobRecordedPayload, 0, len(bindings))
	seen := map[string]bool{}
	for _, binding := range bindings {
		if seen[binding.JobID] {
			return nil, memberGroupFailure("assignment_group repeats a job")
		}
		seen[binding.JobID] = true
		if err := requireWorkerJobRevisionReadyTx(ctx, q, workID, binding); err != nil {
			return nil, err
		}
		job, err := readWorkerJobRevisionTx(ctx, q, workID, binding)
		if err != nil {
			return nil, err
		}
		if job == nil || job.ProjectScope == "" || len(job.PathScope) == 0 {
			return nil, memberGroupFailure("group member needs a recorded Project and explicit independent path scope")
		}
		jobs = append(jobs, job)
		memberID := workflowMemberID(group.GroupID, binding)
		group.Members = append(group.Members, workflowGroupMember{MemberID: memberID, WorkerJobBinding: binding, ProjectID: job.ProjectScope})
	}
	for i, job := range jobs {
		var dependent bool
		ids := make([]string, 0, len(bindings))
		for _, binding := range bindings {
			if binding.JobID != job.JobID {
				ids = append(ids, binding.JobID)
			}
		}
		if err := q.QueryRowContext(ctx, `WITH RECURSIVE dependencies(job_id,revision) AS (
		 SELECT json_extract(value,'$.job_id'),json_extract(value,'$.revision') FROM json_each(?1)
		 UNION SELECT json_extract(p.value,'$.job_id'),json_extract(p.value,'$.revision')
		 FROM dependencies d JOIN worker_job_revisions j ON j.work_id=?2 AND j.job_id=d.job_id AND j.revision=d.revision JOIN json_each(j.prerequisites) p
		) SELECT EXISTS(SELECT 1 FROM dependencies WHERE job_id IN (SELECT value FROM json_each(?3)))`, workflowJSON(job.Prerequisites), workID, workflowJSON(ids)).Scan(&dependent); err != nil {
			return nil, err
		}
		if dependent {
			return nil, memberGroupFailure("dependent jobs cannot be simultaneous siblings")
		}
		for _, other := range jobs[:i] {
			if job.ProjectScope != other.ProjectScope {
				continue
			}
			for _, path := range job.PathScope {
				if workerJobPathsIntersect(other.PathScope, path) {
					return nil, memberGroupFailure("overlapping job path scopes cannot be simultaneous siblings")
				}
			}
		}
	}
	return group, nil
}

func validateWorkflowMemberBinding(ctx context.Context, q queryer, workID, controller string, binding workflowMemberBinding, operationID string) error {
	if err := workflowMemberRequireMain(ctx, q, workID, controller); err != nil {
		return err
	}
	actor := binding.Actor.actor()
	ref, err := WorkflowActorRef(actor)
	if err != nil {
		return err
	}
	if ref == controller {
		return memberGroupFailure("the controller cannot be its own member")
	}
	var principal string
	if err := q.QueryRowContext(ctx, `SELECT principal_ref FROM agent_clients WHERE client_ref=? AND status='active'`, actor.ClientRef).Scan(&principal); err != nil {
		return err
	}
	if principal != actor.PrincipalRef {
		return memberGroupFailure("target actor principal does not match its registered client")
	}
	worker, err := workflowRecordedEvaluationFacts(ctx, q, workID, ref, int64(1)<<62)
	if err != nil {
		return err
	}
	if worker {
		return memberGroupFailure("a dispatched worker cannot become a member caller")
	}
	var recordedController, project, prior string
	var job WorkerJobBinding
	err = q.QueryRowContext(ctx, `SELECT g.controller_actor_ref,m.project_id,COALESCE(m.actor_ref,''),m.job_id,m.job_revision,m.job_digest FROM workflow_group_members m JOIN workflow_member_groups g USING(work_id,group_id) WHERE m.work_id=? AND m.group_id=? AND m.member_id=?`, workID, binding.GroupID, binding.MemberID).Scan(&recordedController, &project, &prior, &job.JobID, &job.Revision, &job.Digest)
	if err == sql.ErrNoRows {
		return memberGroupFailure("member binding does not name a current group member")
	}
	if err != nil {
		return err
	}
	if recordedController != controller || project != binding.ProjectID {
		return memberGroupFailure("member binding controller or Project does not match its group")
	}
	if binding.MemberID != workflowMemberID(binding.GroupID, job) {
		return memberGroupFailure("member identity does not match its recorded job")
	}
	if err := workflowMemberGroupCurrent(ctx, q, workID, binding.GroupID); err != nil {
		return err
	}
	narrowing, err := readWorkflowMemberCallerQ(ctx, q, actor)
	if err != nil {
		return err
	}
	if narrowing != nil && (narrowing.WorkID != workID || narrowing.ProjectID != project || narrowing.GroupID != binding.GroupID || narrowing.MemberID != binding.MemberID) {
		return memberGroupFailure("target actor is already narrowed to another assignment")
	}
	if prior == ref {
		return nil
	}
	var targetBusy bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worktree_occupancy WHERE session_ref=?3)
	 OR EXISTS(SELECT 1 FROM worktree_verify_leases WHERE client_ref=?1 AND agent_ref=?2 AND session_ref=?3 AND (state='held' OR outcome='running'))
	 OR EXISTS(SELECT 1 FROM worker_authorizations a LEFT JOIN worker_attempts t ON t.work_id=a.work_id AND t.attempt_id=a.attempt_id WHERE json_extract(a.payload,'$.actor_ref')=?4 AND (t.attempt_id IS NULL OR t.lifecycle_state IN ('in_flight','dispatched') OR NOT EXISTS(SELECT 1 FROM worker_attempt_dispositions d WHERE d.work_id=t.work_id AND d.attempt_id=t.attempt_id)))`, actor.ClientRef, actor.AgentRef, actor.SessionRef, ref).Scan(&targetBusy); err != nil {
		return err
	}
	if targetBusy {
		return memberGroupFailure("target actor still owns native occupancy, verification, or an unsettled worker effect")
	}
	// Reassignment never abandons a claim, native occupant, unknown launch,
	// unaccepted terminal, or partial effect. Durable effects are conservatively
	// work-wide because they do not yet carry member identity.
	var busy bool
	err = q.QueryRowContext(ctx, `SELECT
	 EXISTS(SELECT 1 FROM worktree_claims WHERE work_id=?1 AND member_id=?2 AND state IN ('pending','verified'))
	 OR EXISTS(SELECT 1 FROM worktree_entries WHERE set_id=?3 AND member_id=?2 AND state='active')
	 OR EXISTS(SELECT 1 FROM worktree_occupancy o JOIN worktree_entries e ON o.worktree_id=e.set_id||':'||e.project_id||':'||e.claim_op_id WHERE e.set_id=?3 AND e.member_id=?2)
	 OR EXISTS(SELECT 1 FROM worktree_verify_leases l WHERE l.work_id=?1 AND (l.state='held' OR l.outcome='running'))
	 OR EXISTS(SELECT 1 FROM worker_authorizations a LEFT JOIN worker_attempts t ON t.work_id=a.work_id AND t.attempt_id=a.attempt_id WHERE a.work_id=?1 AND json_extract(a.payload,'$.worker_job.job_id')=?4 AND (t.attempt_id IS NULL OR t.lifecycle_state IN ('in_flight','dispatched') OR NOT EXISTS(SELECT 1 FROM worker_attempt_dispositions d WHERE d.work_id=t.work_id AND d.attempt_id=t.attempt_id)))
	 OR EXISTS(SELECT 1 FROM durable_operations WHERE work_id=?1 AND op_id<>?5 AND (result_kind IS NULL OR result_kind IN ('pending','partial')))
	 OR EXISTS(SELECT 1 FROM session_vacate_requests WHERE work_id=?1 AND landed=0)
	 OR EXISTS(SELECT 1 FROM project_handoffs WHERE work_id=?1 AND state='recorded')
	 OR EXISTS(SELECT 1 FROM bootstrap_operations WHERE work_id=?1 AND state NOT IN ('completed','rolled_back'))
	 OR EXISTS(SELECT 1 FROM worktree_ref_outcomes WHERE set_id=?3 AND phase NOT IN ('deleted','settled'))`, workID, binding.MemberID, WorktreeSetID(workID), job.JobID, operationID).Scan(&busy)
	if err != nil {
		return err
	}
	if busy {
		return memberGroupFailure("member binding has an active claim or unsettled effect")
	}
	return nil
}

func appendWorkflowMemberCompletion(in workflowActionAssemblyInput, fields map[string]json.RawMessage, events []Event, values map[string]any) ([]Event, error) {
	if !workflowMemberVariant(fields) {
		return events, nil
	}
	if fields["assignment_group"] != nil {
		authored, err := decodeClosedWorkerJobObject[struct {
			Members []WorkerJobBinding `json:"members"`
		}](fields["assignment_group"], "assignment_group requires one closed member roster")
		if err != nil {
			return events, err
		}
		group, err := deriveWorkflowMemberGroup(in.ctx, in.tx, in.request.WorkID, in.request.OperationID, in.eventActor, authored.Members)
		if err != nil {
			return events, err
		}
		values["assignment_group"] = group
		return events, nil
	}
	binding, err := decodeClosedWorkerJobObject[workflowMemberBinding](fields["member_binding"], "member_binding requires one closed actor binding")
	if err != nil {
		return events, err
	}
	if err := validateWorkflowMemberBinding(in.ctx, in.tx, in.request.WorkID, in.eventActor, *binding, in.request.OperationID); err != nil {
		return events, err
	}
	prefix, err := workflowActorRecordPrefixTx(in.ctx, in.tx, binding.Actor.actor())
	if err != nil {
		return events, err
	}
	if prefix != nil {
		events = append(events, prefix.event(in.request.OperationID+":member-actor", in.request.WorkID, in.request.Now, in.request.ExpectedVersion+int64(len(events))))
	}
	values["member_binding"] = binding
	return events, nil
}

func writeWorkflowMemberCompletionTx(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCompletedPayload) error {
	if p.AssignmentGroup == nil && p.MemberBinding == nil {
		return nil
	}
	if p.ActionID != "record_worker_job" || p.AssignmentGroup != nil && p.MemberBinding != nil {
		return memberGroupFailure("member facts require one record_worker_job variant")
	}
	operationID := strings.TrimSuffix(event.EventID, ":completed")
	if p.AssignmentGroup != nil {
		bindings := make([]WorkerJobBinding, 0, len(p.AssignmentGroup.Members))
		for _, member := range p.AssignmentGroup.Members {
			bindings = append(bindings, member.WorkerJobBinding)
		}
		derived, err := deriveWorkflowMemberGroup(ctx, tx, event.SubjectID, operationID, event.Actor, bindings)
		if err != nil {
			return err
		}
		if reviewDigest(derived) != reviewDigest(p.AssignmentGroup) {
			return memberGroupFailure("member group differs from core-derived admission")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO workflow_member_groups(work_id,group_id,controller_actor_ref,base_subject,permission,review_ref,recorded_seq) VALUES(?,?,?,?,?,?,?)`, event.SubjectID, derived.GroupID, event.Actor, workflowJSON(derived.BaseSubject), derived.Permission, derived.ReviewRef, event.Seq)
		if err != nil {
			return err
		}
		for _, member := range derived.Members {
			if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_group_members(work_id,group_id,member_id,job_id,job_revision,job_digest,project_id) VALUES(?,?,?,?,?,?,?)`, event.SubjectID, derived.GroupID, member.MemberID, member.JobID, member.Revision, member.Digest, member.ProjectID); err != nil {
				return err
			}
		}
		return nil
	}
	binding := *p.MemberBinding
	if err := validateWorkflowMemberBinding(ctx, tx, event.SubjectID, event.Actor, binding, operationID); err != nil {
		return err
	}
	ref, err := WorkflowActorRef(binding.Actor.actor())
	if err != nil {
		return err
	}
	if err := requireActor(ctx, tx, ref); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_member_callers(actor_ref,work_id,project_id,group_id,member_id) VALUES(?,?,?,?,?) ON CONFLICT(actor_ref) DO NOTHING`, ref, event.SubjectID, binding.ProjectID, binding.GroupID, binding.MemberID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE workflow_group_members SET confirmed=CASE WHEN actor_ref=? THEN confirmed ELSE 0 END,actor_ref=? WHERE work_id=? AND group_id=? AND member_id=?`, ref, ref, event.SubjectID, binding.GroupID, binding.MemberID)
	return err
}
