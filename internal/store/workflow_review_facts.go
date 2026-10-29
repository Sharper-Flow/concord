package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
)

// Attempt rows retain exact reports; assignments retain group permission.
// Criteria and design are read afresh, never cached as Review readiness.
type workflowReviewFacts struct {
	Candidate *WorkerReviewCandidate  `json:"candidate"`
	Accepted  *WorkflowAcceptedReview `json:"accepted"`
	Findings  []WorkerReviewFinding   `json:"findings"`
}

func readWorkflowReviewFacts(ctx context.Context, q queryer, workID string) (WorkflowReviewState, error) {
	state := WorkflowReviewState{Findings: []WorkerReviewFinding{}, PublicationOutstanding: true}
	var raw string
	err := q.QueryRowContext(ctx, `SELECT state FROM workflow_review_state WHERE work_id=?`, workID).Scan(&raw)
	if err == sql.ErrNoRows {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	var facts workflowReviewFacts
	if err := json.Unmarshal([]byte(raw), &facts); err != nil {
		return state, err
	}
	state.Candidate, state.Accepted = facts.Candidate, facts.Accepted
	if facts.Findings != nil {
		state.Findings = facts.Findings
	}
	return state, nil
}

func writeWorkflowReviewFacts(ctx context.Context, q migrationRunner, workID string, state WorkflowReviewState) error {
	raw, err := json.Marshal(workflowReviewFacts{Candidate: state.Candidate, Accepted: state.Accepted, Findings: state.Findings})
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO workflow_review_state(work_id,state) VALUES(?,?) ON CONFLICT(work_id) DO UPDATE SET state=excluded.state`, workID, string(raw))
	return err
}

func workflowReviewFindingMap(state WorkflowReviewState) map[string]WorkerReviewFinding {
	findings := make(map[string]WorkerReviewFinding, len(state.Findings))
	for _, finding := range state.Findings {
		findings[finding.FindingID] = finding
	}
	return findings
}

func setWorkflowReviewFindings(state *WorkflowReviewState, findings map[string]WorkerReviewFinding) {
	ids := make([]string, 0, len(findings))
	for id := range findings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	state.Findings = make([]WorkerReviewFinding, 0, len(ids))
	for _, id := range ids {
		state.Findings = append(state.Findings, findings[id])
	}
}

func observeWorkflowReviewReport(state *WorkflowReviewState, report *WorkerReviewBlock) {
	if state.Candidate != nil && *state.Candidate != report.InputCandidate {
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
		// A terminal can reopen a finding, but only exact acceptance closes it.
		if finding.Status == "closed" {
			finding.Status = "open"
		}
		findings[finding.FindingID] = finding
	}
	setWorkflowReviewFindings(state, findings)
}

func acceptWorkflowReviewResult(state *WorkflowReviewState, auth workflowActionCompletedPayload, result WorkerCompletedPayload, candidate *WorkerReviewCandidate, reportRef, actor string) {
	if candidate != nil {
		state.Candidate = candidate
	}
	if result.Review != nil && auth.WorkerCapabilityClass == "review" {
		findings := workflowReviewFindingMap(*state)
		applyWorkflowAcceptedReview(state, WorkflowAcceptedReview{AttemptID: result.AttemptID, ReportRef: reportRef, ReportDigest: reviewDigest(result.Review), ActorRef: actor, Report: *result.Review}, findings)
		setWorkflowReviewFindings(state, findings)
	}
}

// Called after terminal admission. A terminal never installs a candidate or an
// accepted report, even when it claims a repair.
func advanceWorkflowReviewCompletedTx(ctx context.Context, tx *sql.Tx, event Event, result WorkerCompletedPayload) error {
	if result.Review == nil {
		return nil
	}
	// The terminal writer has already persisted this exact admitted result.
	auth, _, err := readReviewAttempt(ctx, tx, event.SubjectID, result.AttemptID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if auth.WorkerCapabilityClass != "review" {
		return nil
	}
	state, err := readWorkflowReviewFacts(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	observeWorkflowReviewReport(&state, result.Review)
	return writeWorkflowReviewFacts(ctx, tx, event.SubjectID, state)
}

// Acceptance order is effect order, including repeated acceptance of an older
// attempt. Latest per-attempt dispositions lose that order and the accumulated
// findings, so this writer advances current facts directly.
func advanceWorkflowReviewAcceptanceTx(ctx context.Context, tx *sql.Tx, event Event, action workflowActionCompletedPayload) error {
	if action.ActionID != "accept_worker_result" {
		return nil
	}
	auth, result, err := readReviewAttempt(ctx, tx, event.SubjectID, action.WorkerAttemptID)
	if err != nil {
		return err
	}
	var reportRef string
	if err := tx.QueryRowContext(ctx, `SELECT terminal_event_id FROM worker_attempts WHERE work_id=? AND attempt_id=?`, event.SubjectID, action.WorkerAttemptID).Scan(&reportRef); err != nil {
		return err
	}
	state, err := readWorkflowReviewFacts(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	candidate, err := workflowAcceptedResultCandidate(ctx, tx, event.SubjectID, auth, result)
	if err != nil {
		return err
	}
	if err := acceptOracleJudgedFindingsTx(ctx, tx, event.SubjectID, auth, result); err != nil {
		return err
	}
	acceptWorkflowReviewResult(&state, auth, result, candidate, reportRef, event.Actor)
	return writeWorkflowReviewFacts(ctx, tx, event.SubjectID, state)
}

// Only the admitted acceptance writer closes judged canonical findings. The
// terminal report itself remains the sole pending proposal, with no queue.
func acceptOracleJudgedFindingsTx(ctx context.Context, tx *sql.Tx, workID string, auth workflowActionCompletedPayload, result WorkerCompletedPayload) error {
	if result.Review == nil {
		return nil
	}
	var judged []WorkerOracleResolution
	for _, resolution := range result.Review.ResolvedFindings {
		if len(resolution.JudgedCaseIDs) != 0 {
			judged = append(judged, resolution)
		}
	}
	if len(judged) == 0 {
		return nil
	}
	if auth.WorkerCapabilityClass != "review" {
		return oracleFindingFailure(KindInvalidPayload, "only a Review lane may propose judged closure", "dispatch a Review lane for the declared judged cases")
	}
	lineage, err := readWorkerOracleFindingLineageTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	for _, resolution := range judged {
		finding := lineage.findings[resolution.FindingID]
		if finding == nil {
			return oracleFindingFailure(KindInvariantViolation, "accepted judged finding is absent", "restore verified finding facts")
		}
		if finding.Closed {
			continue
		}
		finding.Closed, finding.ClosureKind = true, oracleClosureResolution
		if finding.Classification == OracleClassificationOracleDefect {
			finding.ClosureKind = oracleClosureOracleDefect
		}
	}
	return storeOracleFindingFacts(ctx, tx, workID, lineage)
}

// Migration-only selection preserves the old ordered dispatch/terminal/accept
// walk, not a reconstruction from latest dispositions. The caller owns the
// migration transaction and its single fold guard scope.
type workflowReviewBackfillRecord struct {
	workID, kind, actor, ref string
	raw                      []byte
}

func backfillWorkflowReviewFacts(ctx context.Context, q migrationRunner) error {
	rows, err := q.QueryContext(ctx, `SELECT e.subject_id,e.kind,e.actor,e.event_id,e.payload FROM domain_events e JOIN work_items w ON w.id=e.subject_id WHERE e.subject_type='work_item' AND e.kind IN (?,?) ORDER BY e.subject_id,e.seq`, WorkflowActionCompleted, WorkerCompleted)
	if err != nil {
		return err
	}
	var records []workflowReviewBackfillRecord
	for rows.Next() {
		var row workflowReviewBackfillRecord
		if err := rows.Scan(&row.workID, &row.kind, &row.actor, &row.ref, &row.raw); err != nil {
			rows.Close()
			return err
		}
		records = append(records, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for start := 0; start < len(records); {
		workID := records[start].workID
		end := start
		for end < len(records) && records[end].workID == workID {
			end++
		}
		if err := backfillWorkflowReviewWork(ctx, q, workID, records[start:end]); err != nil {
			return err
		}
		start = end
	}
	return nil
}

func backfillWorkflowReviewWork(ctx context.Context, q migrationRunner, workID string, records []workflowReviewBackfillRecord) error {
	state := WorkflowReviewState{Findings: []WorkerReviewFinding{}}
	dispatches := map[string]workflowActionCompletedPayload{}
	completions := map[string]WorkerCompletedPayload{}
	reports := map[string]string{}
	for _, row := range records {
		if row.kind == WorkerCompleted {
			var result WorkerCompletedPayload
			if err := json.Unmarshal(row.raw, &result); err != nil {
				return err
			}
			completions[result.AttemptID] = result
			if result.Review != nil && dispatches[result.AttemptID].WorkerCapabilityClass == "review" {
				reports[result.AttemptID] = row.ref
				observeWorkflowReviewReport(&state, result.Review)
			}
			continue
		}
		var action workflowActionCompletedPayload
		if err := json.Unmarshal(row.raw, &action); err != nil {
			return err
		}
		if action.ActionID == "dispatch_worker" {
			dispatches[action.WorkerAttemptID] = action
			continue
		}
		if action.ActionID == "accept_worker_result" {
			if result, found := completions[action.WorkerAttemptID]; found {
				auth := dispatches[result.AttemptID]
				candidate, err := workflowResultCandidate(ctx, q, workID, auth, result)
				if err != nil {
					return err
				}
				acceptWorkflowReviewResult(&state, auth, result, candidate, reports[result.AttemptID], row.actor)
			}
		}
	}
	if err := writeWorkflowReviewFacts(ctx, q, workID, state); err != nil {
		return err
	}
	return nil
}
