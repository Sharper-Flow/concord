package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

type legacyReviewJob struct {
	JobID    string `json:"job_id"`
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
}

type legacyReviewAuthorization struct {
	Capability string           `json:"worker_capability_class"`
	Job        *legacyReviewJob `json:"worker_job"`
	Retained   *struct {
		State struct {
			Candidate ReviewCandidate `json:"candidate"`
		} `json:"state"`
		Input ReviewCandidate `json:"input"`
	} `json:"worker_retained_review"`
}

type legacyReviewResult struct {
	Attempt         string            `json:"attempt_id"`
	Job             *legacyReviewJob  `json:"worker_job"`
	ContextFindings []json.RawMessage `json:"context_findings"`
	Review          *ReviewBlock      `json:"review"`
	Observed        *struct {
		Commit string `json:"commit"`
		Tree   string `json:"tree"`
		State  string `json:"state"`
	} `json:"observed_delivery"`
}

type legacyReviewRecord struct {
	work, kind, actor, ref string
	seq                    int64
	raw                    []byte
}

func readLegacyReviewRecords(ctx context.Context, q migrationRunner) ([]legacyReviewRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT e.subject_id,e.kind,e.actor,e.event_id,e.seq,e.payload FROM domain_events e JOIN work_items w ON w.id=e.subject_id WHERE e.subject_type='work_item' AND e.kind IN ('workflow.action_completed','worker.completed') ORDER BY e.subject_id,e.seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []legacyReviewRecord
	for rows.Next() {
		var r legacyReviewRecord
		if err := rows.Scan(&r.work, &r.kind, &r.actor, &r.ref, &r.seq, &r.raw); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// Migration 148 reads the historical log before migration 151 removes it.
// Only the fields needed to retain accepted subjects and findings are decoded.
func backfillWorkflowReviewFacts(ctx context.Context, q migrationRunner) error {
	records, err := readLegacyReviewRecords(ctx, q)
	if err != nil {
		return err
	}
	states := map[string]WorkflowReviewState{}
	auths := map[string]legacyReviewAuthorization{}
	results := map[string]legacyReviewResult{}
	refs := map[string]string{}
	for _, r := range records {
		state := states[r.work]
		keyPrefix := r.work + "\x00"
		if r.kind == "worker.completed" {
			var result legacyReviewResult
			if err := json.Unmarshal(r.raw, &result); err != nil {
				return err
			}
			if err := retainLegacyReviewSources(ctx, q, r.work, &state, result, r.ref, r.seq); err != nil {
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
				legacyReviewAuthorization
			}
			if err := json.Unmarshal(r.raw, &action); err != nil {
				return err
			}
			key := keyPrefix + action.Attempt
			if action.ID == "dispatch_worker" {
				auths[key] = action.legacyReviewAuthorization
			}
			if result, found := results[key]; found && action.ID == "accept_worker_result" {
				if err := acceptLegacyReviewResult(ctx, q, r.work, r.actor, &state, result, auths[key], refs[key]); err != nil {
					return err
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

// Preserve minting coordinates and the exact recorded job's owner inventory
// before the log retires. Later reports cannot reassign a retained local ID.
func retainLegacyReviewSources(ctx context.Context, q migrationRunner, work string, state *WorkflowReviewState, result legacyReviewResult, ref string, seq int64) error {
	if result.Review == nil || len(result.Review.Findings) == 0 {
		return nil
	}
	oracle, err := legacyReviewSourceOracle(ctx, q, work, result.Job)
	if err != nil {
		return err
	}
	if state.findingSources == nil {
		state.findingSources = map[string]reviewFindingSource{}
	}
	for index, finding := range result.Review.Findings {
		if _, retained := state.findingSources[finding.FindingID]; retained || finding.FindingID == "" {
			continue
		}
		ordinal := len(result.ContextFindings) + index
		source := reviewFindingSource{CanonicalID: fmt.Sprintf("finding:%d:%d", seq, ordinal), DomainID: "unresolved", EventID: ref, Kind: "worker.completed", Seq: seq, Ordinal: ordinal, Oracle: oracle}
		if finding.Oracle != nil && oracle != nil {
			for _, owner := range oracle.Owners {
				if owner.OwnerID == finding.Oracle.OwnerID && owner.DomainID != "" {
					source.DomainID = owner.DomainID
				}
			}
		}
		state.findingSources[finding.FindingID] = source
	}
	return nil
}

func legacyReviewSourceOracle(ctx context.Context, q queryer, work string, job *legacyReviewJob) (*AcceptanceOracle, error) {
	if job == nil {
		return nil, nil
	}
	var raw string
	err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(recorded_payload,'$.acceptance_oracle'),'null') FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=? AND digest=?`, work, job.JobID, job.Revision, job.Digest).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var oracle *AcceptanceOracle
	if err := json.Unmarshal([]byte(raw), &oracle); err != nil {
		return nil, err
	}
	return oracle, nil
}

func acceptLegacyReviewResult(ctx context.Context, q migrationRunner, work, actor string, state *WorkflowReviewState, result legacyReviewResult, auth legacyReviewAuthorization, ref string) error {
	if result.Review != nil && auth.Capability == "review" {
		findings := workflowReviewFindingMap(*state)
		// Historical acceptance only applies proposed closures. Observation
		// already merged findings; accepting an older report cannot reopen them.
		for _, finding := range result.Review.Findings {
			if finding.Status == "closed" {
				previous := findings[finding.FindingID]
				previous.Status, previous.ClosureRefs = "closed", finding.ClosureRefs
				findings[finding.FindingID] = previous
			}
		}
		state.Accepted = &WorkflowAcceptedReview{ReportRef: ref, ReportDigest: reviewDigest(result.Review), ActorRef: actor, Report: *result.Review}
		candidate := result.Review.OutputCandidate
		state.Candidate = &candidate
		state.DependenciesCurrent = state.Dependencies != nil && sameReviewDependencies(result.Review.Dependencies, *state.Dependencies)
		setWorkflowReviewFindings(state, findings)
		return nil
	}
	if result.Observed == nil || result.Observed.State != "clean" {
		return nil
	}
	project := ""
	if auth.Job != nil {
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(recorded_payload,'$.project_scope'),'') FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=? AND digest=? AND recorded_payload IS NOT NULL`, work, auth.Job.JobID, auth.Job.Revision, auth.Job.Digest).Scan(&project); err != nil {
			return err
		}
	}
	state.Candidate = &ReviewCandidate{ProjectID: project, Commit: result.Observed.Commit, Tree: result.Observed.Tree}
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
				finding.Status = "open"
			}
		}
		if finding.Status == "closed" {
			finding.Status = "open"
		}
		findings[finding.FindingID] = finding
	}
	setWorkflowReviewFindings(state, findings)
}
