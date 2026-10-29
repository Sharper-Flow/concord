package store

import (
	"context"
	"encoding/json"
)

// Migration 148 reads the historical log before migration 151 removes it.
// Only the fields needed to retain accepted subjects and findings are decoded.
func backfillWorkflowReviewFacts(ctx context.Context, q migrationRunner) error {
	rows, err := q.QueryContext(ctx, `SELECT e.subject_id,e.kind,e.actor,e.event_id,e.payload FROM domain_events e JOIN work_items w ON w.id=e.subject_id WHERE e.subject_type='work_item' AND e.kind IN ('workflow.action_completed','worker.completed') ORDER BY e.subject_id,e.seq`)
	if err != nil {
		return err
	}
	type record struct {
		work, kind, actor, ref string
		raw                    []byte
	}
	var records []record
	for rows.Next() {
		var r record
		if err := rows.Scan(&r.work, &r.kind, &r.actor, &r.ref, &r.raw); err != nil {
			rows.Close()
			return err
		}
		records = append(records, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	type legacyJob struct {
		JobID    string `json:"job_id"`
		Revision int64  `json:"revision"`
		Digest   string `json:"digest"`
	}
	type legacyAuthorization struct {
		Capability string     `json:"worker_capability_class"`
		Job        *legacyJob `json:"worker_job"`
		Retained   *struct {
			State struct {
				Candidate ReviewCandidate `json:"candidate"`
			} `json:"state"`
			Input ReviewCandidate `json:"input"`
		} `json:"worker_retained_review"`
	}
	type legacyResult struct {
		Attempt  string       `json:"attempt_id"`
		Review   *ReviewBlock `json:"review"`
		Observed *struct {
			Commit string `json:"commit"`
			Tree   string `json:"tree"`
			State  string `json:"state"`
		} `json:"observed_delivery"`
	}
	states := map[string]WorkflowReviewState{}
	auths := map[string]legacyAuthorization{}
	results := map[string]legacyResult{}
	refs := map[string]string{}
	for _, r := range records {
		state := states[r.work]
		keyPrefix := r.work + "\x00"
		if r.kind == "worker.completed" {
			var result legacyResult
			if err := json.Unmarshal(r.raw, &result); err != nil {
				return err
			}
			key := keyPrefix + result.Attempt
			results[key] = result
			auth := auths[key]
			if result.Review != nil && auth.Capability == "review" {
				refs[key] = r.ref
				observeLegacyReviewReport(&state, result.Review, auth.Retained != nil && state.Candidate != nil && *state.Candidate == auth.Retained.State.Candidate && result.Review.InputCandidate == auth.Retained.Input)
			}
		} else {
			var action struct {
				ID      string `json:"action_id"`
				Attempt string `json:"worker_attempt_id"`
				legacyAuthorization
			}
			if err := json.Unmarshal(r.raw, &action); err != nil {
				return err
			}
			key := keyPrefix + action.Attempt
			if action.ID == "dispatch_worker" {
				auths[key] = action.legacyAuthorization
			}
			if result, found := results[key]; found && action.ID == "accept_worker_result" {
				auth := auths[key]
				var candidate *ReviewCandidate
				if result.Review != nil && auth.Capability == "review" {
					output := result.Review.OutputCandidate
					candidate = &output
				} else if result.Observed != nil && result.Observed.State == "clean" {
					project := ""
					if auth.Job != nil {
						if err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(recorded_payload,'$.project_scope'),'') FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=? AND digest=? AND recorded_payload IS NOT NULL`, r.work, auth.Job.JobID, auth.Job.Revision, auth.Job.Digest).Scan(&project); err != nil {
							return err
						}
					}
					candidate = &ReviewCandidate{ProjectID: project, Commit: result.Observed.Commit, Tree: result.Observed.Tree}
				}
				if candidate != nil {
					state.Candidate = candidate
				}
				if result.Review != nil && auth.Capability == "review" {
					applyWorkflowAcceptedReview(&state, WorkflowAcceptedReview{ReportRef: refs[key], ReportDigest: reviewDigest(result.Review), ActorRef: r.actor, Report: *result.Review}, workflowReviewFindingMap(state))
				}
			}
		}
		states[r.work] = state
	}
	for work, state := range states {
		if err := writeWorkflowReviewFacts(ctx, q, work, state); err != nil {
			return err
		}
	}
	return nil
}

// observeLegacyReviewReport keeps the historical terminal walk: a report on a
// different input accumulates findings only through its retained base, a
// terminal never closes a finding, and only acceptance installs a candidate.
func observeLegacyReviewReport(state *WorkflowReviewState, report *ReviewBlock, retainedBase bool) {
	if state.Candidate != nil && *state.Candidate != report.InputCandidate && !retainedBase {
		return
	}
	findings := workflowReviewFindingMap(*state)
	for _, finding := range report.Findings {
		if previous, found := findings[finding.FindingID]; found {
			if previous.Oracle != nil {
				finding.Oracle = previous.Oracle
			}
			if previous.Status == "open" {
				finding.InScope = finding.InScope || previous.InScope
				finding.Blocking = finding.Blocking || previous.Blocking
			}
		}
		finding.Status = "open"
		findings[finding.FindingID] = finding
	}
	setWorkflowReviewFindings(state, findings)
}
