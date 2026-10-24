package store

import (
	"context"
	"database/sql"
)

type workflowAssignmentGroup struct {
	Permission    string
	ReviewRef     string
	CorrectionRef string
}

// The assignment owns its current admission, independently of launch identity
// and revision content. Reading absence of a report cannot create a group.
func loadWorkflowAssignmentGroups(ctx context.Context, q queryer, workID string, state *WorkflowReviewState) error {
	state.Groups = map[string]workflowAssignmentGroup{}
	rows, err := q.QueryContext(ctx, `SELECT j.job_id,j.group_permission,j.group_review_ref,j.correction_ref FROM worker_job_revisions j WHERE j.work_id=? AND j.revision=(SELECT MAX(latest.revision) FROM worker_job_revisions latest WHERE latest.work_id=j.work_id AND latest.job_id=j.job_id) AND (j.group_permission<>'' OR j.correction_ref<>'') ORDER BY j.job_id`, workID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var group workflowAssignmentGroup
		if err := rows.Scan(&id, &group.Permission, &group.ReviewRef, &group.CorrectionRef); err != nil {
			rows.Close()
			return err
		}
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
			}
		}
	}
	return nil
}

func recordWorkflowAssignmentGroup(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCompletedPayload) error {
	if p.WorkerCapabilityClass != "implementation" || p.WorkerJob == nil {
		return nil
	}
	state, err := loadWorkflowReviewState(ctx, tx, event.SubjectID, event.Seq)
	if err != nil {
		return err
	}
	correctionRef, err := workflowDeclaredReviewCorrection(ctx, tx, event.SubjectID, p.StepID, p.WorkerAttemptID, &state)
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
	_, err = tx.ExecContext(ctx, `UPDATE worker_job_revisions SET group_permission=?,group_review_ref=?,correction_ref=? WHERE work_id=? AND job_id=? AND revision=?`, group.Permission, group.ReviewRef, group.CorrectionRef, event.SubjectID, p.WorkerJob.JobID, p.WorkerJob.Revision)
	return err
}

func workflowDeclaredReviewCorrection(ctx context.Context, q queryer, workID, step, attempt string, state *WorkflowReviewState) (string, error) {
	if state == nil || state.Accepted == nil || state.Candidate == nil || state.Accepted.Report.OutputCandidate != *state.Candidate || !state.DependenciesCurrent || state.Accepted.Report.Disposition != "correction_required" {
		return "", nil
	}
	correction, err := workflowCorrectionContextForDispatch(ctx, q, workID, step, attempt)
	if err != nil || correction == nil {
		return "", err
	}
	var action string
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(payload,'$.action_id'),'') FROM domain_events WHERE event_id=? AND subject_id=?`, correction.SourceEventID, workID).Scan(&action); err != nil {
		return "", err
	}
	if action == "request_correction" {
		return correction.SourceEventID, nil
	}
	return "", nil
}
