package store

import (
	"context"
	"encoding/json"
)

// Migration 147 reads the historical log before migration 151 removes it.
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
	type legacyAuthorization struct {
		Capability string `json:"worker_capability_class"`
		Subject    string `json:"worker_subject_commit"`
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
			refs[key] = r.ref
			if result.Review != nil && auths[key].Capability == "review" {
				findings := workflowReviewFindingMap(state)
				for _, finding := range result.Review.Findings {
					if old, exists := findings[finding.FindingID]; exists {
						finding.InScope = finding.InScope || old.InScope
						finding.Blocking = finding.Blocking || old.Blocking
						if old.Oracle != nil {
							finding.Oracle = old.Oracle
						}
					}
					finding.Status = "open"
					findings[finding.FindingID] = finding
				}
				setWorkflowReviewFindings(&state, findings)
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
			if action.ID == "accept_worker_result" {
				result, found := results[key]
				auth := auths[key]
				if found && result.Review != nil && auth.Capability == "review" && result.Observed != nil && result.Observed.State == "clean" && result.Review.InputCandidate.Commit == auth.Subject && result.Review.OutputCandidate.Commit == result.Observed.Commit && result.Review.OutputCandidate.Tree == result.Observed.Tree {
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
