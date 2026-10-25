package store

import (
	"context"
	"database/sql"
)

type workflowAssignmentGroup struct {
	GroupID       string
	Permission    string
	ReviewRef     string
	CorrectionRef string
}

// The assignment owns its current admission, independently of launch identity
// and revision content. Reading absence of a report cannot create a group.
func loadWorkflowAssignmentGroups(ctx context.Context, q queryer, workID string, state *WorkflowReviewState) error {
	state.Groups = map[string]workflowAssignmentGroup{}
	rows, err := q.QueryContext(ctx, `SELECT g.group_id,g.permission,g.review_ref,COALESCE(m.job_id,'') FROM workflow_member_groups g LEFT JOIN workflow_group_members m USING(work_id,group_id) WHERE g.work_id=? ORDER BY g.recorded_seq,g.group_id,m.member_id`, workID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, job string
		var group workflowAssignmentGroup
		if err := rows.Scan(&id, &group.Permission, &group.ReviewRef, &job); err != nil {
			rows.Close()
			return err
		}
		if job == "" {
			// No roster means an ordinary one-job admission. Legacy rows may
			// lack a controller, native base and effect sequence.
			state.Groups[id] = group
			continue
		}
		group.GroupID = id
		state.Groups[workflowMemberGroupAdmissionID(id)] = group
		state.Groups[job] = group
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = q.QueryContext(ctx, `SELECT j.job_id,j.correction_ref FROM worker_job_revisions j WHERE j.work_id=? AND j.revision=(SELECT MAX(latest.revision) FROM worker_job_revisions latest WHERE latest.work_id=j.work_id AND latest.job_id=j.job_id) AND j.correction_ref<>'' ORDER BY j.job_id`, workID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, correction string
		if err := rows.Scan(&id, &correction); err != nil {
			rows.Close()
			return err
		}
		group := state.Groups[id]
		group.CorrectionRef = correction
		state.Groups[id] = group
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	state.ExecutionProgressed = len(state.Groups) != 0
	state.ContinueConsumed, state.GroupID = false, ""
	if state.Accepted != nil {
		for id, group := range state.Groups {
			if group.Permission == "continue" && group.ReviewRef == state.Accepted.ReportRef {
				state.ContinueConsumed, state.GroupID = true, id
				if group.GroupID != "" {
					state.GroupID = group.GroupID
				}
			}
		}
	}
	return nil
}

func recordWorkflowAssignmentGroup(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCompletedPayload) error {
	if p.WorkerCapabilityClass != "implementation" || p.WorkerJob == nil {
		return nil
	}
	state, err := loadWorkflowReviewState(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	correctionRef, err := workflowDeclaredReviewCorrection(ctx, tx, event.SubjectID, p.WorkerAttemptID, &state)
	if err != nil {
		return err
	}
	if correctionRef == "" {
		if err := workflowAdmitReview(state, p.WorkerJob.JobID); err != nil {
			return err
		}
	}
	group, exists := state.Groups[p.WorkerJob.JobID]
	if correctionRef != "" {
		group.ReviewRef, group.CorrectionRef = state.Accepted.ReportRef, correctionRef
	} else if !exists || state.Accepted != nil && state.Candidate != nil && state.Accepted.Report.OutputCandidate == *state.Candidate && state.Accepted.ReportRef != group.ReviewRef {
		group = workflowAssignmentGroup{Permission: "first_entry"}
		if state.Accepted != nil {
			group.Permission, group.ReviewRef = "continue", state.Accepted.ReportRef
		}
	}
	groupID := group.GroupID
	if groupID == "" {
		groupID = p.WorkerJob.JobID
	}
	// Ordinary admissions have no roster and make no claim about a qualified
	// native base. Permission and Review identity have exactly one owner.
	result, err := tx.ExecContext(ctx, `INSERT INTO workflow_member_groups(work_id,group_id,controller_actor_ref,permission,review_ref,recorded_seq) VALUES(?,?,?,?,?,?) ON CONFLICT(work_id,group_id) DO UPDATE SET permission=excluded.permission,review_ref=excluded.review_ref WHERE ?<>'' OR NOT EXISTS(SELECT 1 FROM workflow_group_members m WHERE m.work_id=excluded.work_id AND m.group_id=excluded.group_id)`, event.SubjectID, groupID, event.Actor, group.Permission, group.ReviewRef, event.Seq, group.GroupID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return memberGroupFailure("ordinary job identity collides with an existing member group")
	}
	_, err = tx.ExecContext(ctx, `UPDATE worker_job_revisions SET correction_ref=? WHERE work_id=? AND job_id=? AND revision=?`, group.CorrectionRef, event.SubjectID, p.WorkerJob.JobID, p.WorkerJob.Revision)
	return err
}

// Compare only jobs present at Review dispatch. A later assignment is not a
// dependency of that Review, but replacing a reviewed job withdraws its authority.
func loadWorkflowReviewJobChanges(ctx context.Context, q queryer, workID string, state *WorkflowReviewState) error {
	state.StaleJobs = nil
	if state.Accepted == nil {
		return nil
	}
	rows, err := q.QueryContext(ctx, `SELECT current.job_id,COALESCE(m.group_id,'')
FROM worker_job_revisions current
JOIN worker_authorizations a ON a.work_id=current.work_id AND a.attempt_id=?
JOIN worker_job_revisions reviewed ON reviewed.work_id=current.work_id AND reviewed.job_id=current.job_id
LEFT JOIN workflow_group_members m ON m.work_id=current.work_id AND m.job_id=current.job_id
WHERE current.work_id=?
AND current.revision=(SELECT MAX(j.revision) FROM worker_job_revisions j WHERE j.work_id=current.work_id AND j.job_id=current.job_id)
AND reviewed.revision=(SELECT MAX(j.revision) FROM worker_job_revisions j WHERE j.work_id=current.work_id AND j.job_id=current.job_id AND j.recorded_seq<a.seq)
AND current.digest<>reviewed.digest`, state.Accepted.AttemptID, workID)
	if err != nil {
		return err
	}
	defer rows.Close()
	state.StaleJobs = map[string]bool{}
	for rows.Next() {
		var job, group string
		if err := rows.Scan(&job, &group); err != nil {
			return err
		}
		state.StaleJobs[job] = true
		if group != "" {
			state.StaleJobs[workflowMemberGroupAdmissionID(group)] = true
		}
	}
	return rows.Err()
}

func workflowDeclaredReviewCorrection(ctx context.Context, q queryer, workID, attempt string, state *WorkflowReviewState) (string, error) {
	if state == nil || state.Accepted == nil || state.Candidate == nil || state.Accepted.Report.OutputCandidate != *state.Candidate || !state.DependenciesCurrent || state.Accepted.Report.Disposition != "correction_required" {
		return "", nil
	}
	correction, err := workflowCorrectionOpenRecord(ctx, q, workID, attempt, "workflow_correction")
	if err != nil || correction == nil {
		return "", err
	}
	if _, err := workflowCorrectionContextForRecord(ctx, q, workID, "", correction); err != nil {
		return "", err
	}
	if correction.fields.ActionID == "request_correction" {
		return correction.sourceID, nil
	}
	return "", nil
}
