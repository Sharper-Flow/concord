package store

import (
	"context"
	"encoding/json"
)

// The pre-cutover walk is an independent migration parity oracle, never a live
// fallback. It keeps the old selection and accumulation order verbatim.
func g32LegacyReviewFactsForTest(ctx context.Context, q queryer, workID string) (WorkflowReviewState, error) {
	state := WorkflowReviewState{Findings: []WorkerReviewFinding{}, PublicationOutstanding: true}
	rows, err := q.QueryContext(ctx, `SELECT seq,kind,actor,event_id,payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind IN (?,?) AND (?=0 OR seq<?) ORDER BY seq`, workID, WorkflowActionCompleted, WorkerCompleted, 0, 0)
	if err != nil {
		return state, err
	}
	type row struct {
		seq              int64
		kind, actor, ref string
		raw              []byte
	}
	var records []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.seq, &r.kind, &r.actor, &r.ref, &r.raw); err != nil {
			rows.Close()
			return state, err
		}
		records = append(records, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return state, err
	}
	reports := map[string]row{}
	completions := map[string]WorkerCompletedPayload{}
	dispatches := map[string]workflowActionCompletedPayload{}
	findings := map[string]WorkerReviewFinding{}
	for _, r := range records {
		if r.kind == WorkerCompleted {
			var result WorkerCompletedPayload
			if err := json.Unmarshal(r.raw, &result); err != nil {
				return state, err
			}
			completions[result.AttemptID] = result
			if result.Review != nil && dispatches[result.AttemptID].WorkerCapabilityClass == "review" {
				reports[result.AttemptID] = r
				if state.Candidate != nil && *state.Candidate != result.Review.InputCandidate {
					continue
				}
				for _, f := range result.Review.Findings {
					if prev, ok := findings[f.FindingID]; ok {
						if prev.Oracle != nil {
							f.Oracle = prev.Oracle
						}
						if prev.Status == "open" {
							f.InScope = f.InScope || prev.InScope
							f.Blocking = f.Blocking || prev.Blocking
							f.Status = "open"
						}
					}
					if f.Status == "closed" {
						f.Status = "open"
					}
					findings[f.FindingID] = f
				}
			}
			continue
		}
		var action workflowActionCompletedPayload
		if err := json.Unmarshal(r.raw, &action); err != nil {
			return state, err
		}
		if action.ActionID == "dispatch_worker" {
			dispatches[action.WorkerAttemptID] = action
			continue
		}
		if action.ActionID != "accept_worker_result" {
			continue
		}
		result, found := completions[action.WorkerAttemptID]
		if !found {
			continue
		}
		candidate, err := workflowAcceptedResultCandidate(ctx, q, workID, dispatches[result.AttemptID], result)
		if err != nil {
			return state, err
		}
		if candidate != nil {
			state.Candidate = candidate
		}
		if result.Review != nil && dispatches[result.AttemptID].WorkerCapabilityClass == "review" {
			applyWorkflowAcceptedReview(&state, WorkflowAcceptedReview{AttemptID: result.AttemptID, ReportRef: reports[action.WorkerAttemptID].ref, ReportDigest: reviewDigest(result.Review), ActorRef: r.actor, Report: *result.Review}, findings)
		}
	}
	setWorkflowReviewFindings(&state, findings)
	return state, nil
}
