package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

type correctionRecordFact struct {
	Seq      int64                              `json:"seq"`
	SourceID string                             `json:"source_id"`
	JobKey   string                             `json:"job_key"`
	Fields   workflowCorrectionCompletionFields `json:"fields"`
}

type checksRepairFact struct {
	Binding         WorkerJobBinding `json:"binding"`
	NamedKeys       []string         `json:"named_keys"`
	ContractVersion int64            `json:"contract_version"`
	AcceptanceSeq   int64            `json:"acceptance_seq"`
	ConfirmAttempt  string           `json:"confirm_attempt"`
	Confirmed       bool             `json:"confirmed"`
	Voided          bool             `json:"voided"`
}

type correctionStateFact struct {
	Boundary   int64                       `json:"boundary"`
	Unresolved map[string]bool             `json:"unresolved"`
	Records    []correctionRecordFact      `json:"records"`
	Repairs    map[string]checksRepairFact `json:"repairs"`
}

func newWorkflowCorrectionState() *workflowCorrectionHistory {
	return &workflowCorrectionHistory{
		unresolved: map[string]bool{}, dispatchJob: map[string]string{}, materialized: map[string]bool{},
		obligations: map[string]workflowJobObligationState{}, blockingReview: map[string]bool{},
		reviewDisposition: map[string]string{}, checksRepairs: map[string]*workflowChecksRepairState{},
	}
}

func readWorkflowCorrectionFacts(ctx context.Context, q queryer, workID, subject string) (*workflowCorrectionHistory, error) {
	h := newWorkflowCorrectionState()
	var raw string
	err := q.QueryRowContext(ctx, `SELECT state FROM workflow_correction_state WHERE work_id=?`, workID).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return nil, wrapFailure(KindUnavailable, subject, "cannot read current correction state", true, "retry once the workflow projection is readable", err)
	}
	if err == nil {
		var state correctionStateFact
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			return nil, newFailure(KindInvariantViolation, subject, "current correction state is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
		}
		h.boundary = state.Boundary
		if state.Unresolved != nil {
			h.unresolved = state.Unresolved
		}
		for _, record := range state.Records {
			h.records = append(h.records, workflowCorrectionRecordState{seq: record.Seq, sourceID: record.SourceID, fields: record.Fields, jobKey: record.JobKey})
		}
		for key, repair := range state.Repairs {
			h.checksRepairs[key] = &workflowChecksRepairState{binding: repair.Binding, namedKeys: repair.NamedKeys, contractVersion: repair.ContractVersion, acceptanceSeq: repair.AcceptanceSeq, confirmAttempt: repair.ConfirmAttempt, confirmed: repair.Confirmed, voided: repair.Voided}
		}
	}
	// Immutable job obligations and attempt identities already have current
	// owners. Keep only mutable debt and repair decisions in this projection.
	rows, err := q.QueryContext(ctx, `SELECT recorded_payload FROM worker_job_revisions WHERE work_id=?`, workID)
	if err != nil {
		return nil, workflowProjectionError(err, "cannot read current correction obligations")
	}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			rows.Close()
			return nil, err
		}
		var job WorkerJobRecordedPayload
		if err := json.Unmarshal([]byte(payload), &job); err != nil {
			rows.Close()
			return nil, err
		}
		key := workerJobKey(job.JobID, job.Revision)
		h.obligations[key] = workflowJobObligationState{base: workerJobObligationKey(job), oracle: job.AcceptanceOracle, contractVersion: job.ContractVersion}
		if job.ChecksRepair != nil && h.checksRepairs[key] == nil {
			named := make([]string, 0, len(job.ChecksRepair.Revisions))
			for _, entry := range job.ChecksRepair.Revisions {
				named = append(named, workerJobKey(entry.WorkerJob.JobID, entry.WorkerJob.Revision))
			}
			h.checksRepairs[key] = &workflowChecksRepairState{binding: WorkerJobBinding{JobID: job.JobID, Revision: job.Revision, Digest: job.Digest}, namedKeys: named, contractVersion: job.ContractVersion}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = q.QueryContext(ctx, `SELECT attempt_id,seq,payload FROM worker_authorizations WHERE work_id=? ORDER BY seq`, workID)
	if err != nil {
		return nil, workflowProjectionError(err, "cannot read current correction authorizations")
	}
	for rows.Next() {
		var attempt, payload string
		var seq int64
		if err := rows.Scan(&attempt, &seq, &payload); err != nil {
			rows.Close()
			return nil, err
		}
		var authorization workflowActionCompletedPayload
		if err := json.Unmarshal([]byte(payload), &authorization); err != nil {
			rows.Close()
			return nil, err
		}
		if authorization.WorkerJob != nil {
			h.bind(attempt, *authorization.WorkerJob)
		}
		h.completions = append(h.completions, workflowCorrectionDispatchCompletion{seq: seq, attempt: attempt})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = q.QueryContext(ctx, `SELECT attempt_id,dispatch_payload,terminal_payload FROM worker_attempts WHERE work_id=?`, workID)
	if err != nil {
		return nil, workflowProjectionError(err, "cannot read current correction attempts")
	}
	defer rows.Close()
	for rows.Next() {
		var attempt string
		var dispatch, terminal sql.NullString
		if err := rows.Scan(&attempt, &dispatch, &terminal); err != nil {
			return nil, err
		}
		h.materialized[attempt] = dispatch.Valid
		if dispatch.Valid {
			var payload WorkerDispatchedPayload
			if err := json.Unmarshal([]byte(dispatch.String), &payload); err != nil {
				return nil, err
			}
			if payload.WorkerJob != nil {
				h.bind(attempt, *payload.WorkerJob)
			}
		}
		if terminal.Valid {
			var payload struct {
				Review *WorkerReviewBlock `json:"review"`
			}
			if err := json.Unmarshal([]byte(terminal.String), &payload); err != nil {
				return nil, err
			}
			if payload.Review != nil {
				h.reviewDisposition[attempt] = payload.Review.Disposition
				h.blockingReview[attempt] = payload.Review.Disposition == "correction_required" || payload.Review.Disposition == "revision_required"
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return h, nil
}

func writeWorkflowCorrectionFacts(ctx context.Context, q migrationRunner, workID string, h *workflowCorrectionHistory) error {
	state := correctionStateFact{Boundary: h.boundary, Unresolved: h.unresolved, Repairs: map[string]checksRepairFact{}}
	// The latest direction for each job remains applicable if that job fails
	// again. Unbound rejections also have a step/action-specific consumer.
	seen := map[string]bool{}
	for i := len(h.records) - 1; i >= 0; i-- {
		r := h.records[i]
		key := "job:" + r.jobKey
		if r.jobKey == "" {
			key = "unbound:" + r.fields.StepID + ":" + r.fields.ActionID
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		state.Records = append(state.Records, correctionRecordFact{Seq: r.seq, SourceID: r.sourceID, JobKey: r.jobKey, Fields: r.fields})
	}
	for i, j := 0, len(state.Records)-1; i < j; i, j = i+1, j-1 {
		state.Records[i], state.Records[j] = state.Records[j], state.Records[i]
	}
	for key, r := range h.checksRepairs {
		state.Repairs[key] = checksRepairFact{Binding: r.binding, NamedKeys: r.namedKeys, ContractVersion: r.contractVersion, AcceptanceSeq: r.acceptanceSeq, ConfirmAttempt: r.confirmAttempt, Confirmed: r.confirmed, Voided: r.voided}
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	// Standalone worker receipts do not create workflow state without its owner.
	_, err = q.ExecContext(ctx, `INSERT INTO workflow_correction_state(work_id,state) SELECT id,? FROM work_items WHERE id=? ON CONFLICT(work_id) DO UPDATE SET state=excluded.state`, string(raw), workID)
	return workflowProjectionError(err, "cannot record current correction state")
}

func advanceWorkflowCorrectionFactsTx(ctx context.Context, tx *sql.Tx, event Event) error {
	h, err := readWorkflowCorrectionFacts(ctx, tx, event.SubjectID, "fold_event")
	if err != nil {
		return err
	}
	var fields struct {
		AttemptID       string            `json:"attempt_id"`
		WorkerAttemptID string            `json:"worker_attempt_id"`
		ActionID        string            `json:"action_id"`
		WorkerJob       *WorkerJobBinding `json:"worker_job"`
	}
	if err := json.Unmarshal(event.Payload, &fields); err != nil {
		return err
	}
	row := workflowCorrectionHistoryRow{seq: event.Seq, kind: event.Kind, dispatchAttempt: fields.AttemptID, actionAttempt: fields.WorkerAttemptID, actionID: fields.ActionID, payload: string(event.Payload), eventID: event.EventID}
	if fields.WorkerJob != nil {
		row.jobID, row.jobRevision, row.jobDigest = fields.WorkerJob.JobID, fields.WorkerJob.Revision, fields.WorkerJob.Digest
	}
	if err := h.observe(row, "fold_event"); err != nil {
		return err
	}
	return writeWorkflowCorrectionFacts(ctx, tx, event.SubjectID, h)
}

func applyAcceptWorkerResultCorrectionTx(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCompletedPayload) error {
	if p.ActionID != "accept_worker_result" {
		return nil
	}
	return advanceWorkflowCorrectionFactsTx(ctx, tx, event)
}

func backfillWorkflowCorrectionFacts(ctx context.Context, q migrationRunner) error {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT e.subject_id FROM domain_events e JOIN work_items w ON w.id=e.subject_id WHERE e.subject_type='work_item' ORDER BY e.subject_id`)
	if err != nil {
		return err
	}
	var workIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		workIDs = append(workIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `INSERT OR IGNORE INTO fold_guard(active) VALUES(1)`); err != nil {
		return err
	}
	for _, id := range workIDs {
		h, err := readWorkflowCorrectionStateForMigration(ctx, q, id, int64(1)<<62, "migrate")
		if err != nil {
			return err
		}
		if err := writeWorkflowCorrectionFacts(ctx, q, id, h); err != nil {
			return err
		}
	}
	_, err = q.ExecContext(ctx, `DELETE FROM fold_guard`)
	return err
}
