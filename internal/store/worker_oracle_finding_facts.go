package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// The explicit nil SourceOracle shadows the embedded read model's field when
// encoding. Only its immutable job binding belongs in the stored aggregate;
// worker_job_revisions owns the oracle bytes.
type oracleFindingFact struct {
	*WorkerOpenOracleFinding
	SourceOracle *AcceptanceOracle `json:"SourceOracle,omitempty"`
	SourceJob    *WorkerJobBinding `json:"source_job,omitempty"`
}

// One current aggregate per work keeps closed identities addressable and
// receipts active only until the latest contract supersession.
func readWorkerOracleFindingLineageTx(ctx context.Context, q queryer, workID string) (*workerOracleFindingLineage, error) {
	lineage := &workerOracleFindingLineage{findings: map[string]*WorkerOpenOracleFinding{}, retainedReceipts: make([]WorkerOracleReceipt, 0, 8)}
	var findings, receipts []byte
	err := q.QueryRowContext(ctx, `SELECT oracle_capable,findings,receipts FROM worker_oracle_finding_facts WHERE work_id=?`, workID).Scan(&lineage.oracleCapable, &findings, &receipts)
	if err == sql.ErrNoRows {
		return lineage, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_oracle_findings", "cannot read the ranked-finding lineage", true, "retry once the finding facts are readable", err)
	}
	var ordered []*oracleFindingFact
	if err := json.Unmarshal(findings, &ordered); err != nil {
		return nil, oracleFindingFailure(KindInvariantViolation, "stored ranked-finding facts are malformed", "restore verified finding facts")
	}
	if err := json.Unmarshal(receipts, &lineage.retainedReceipts); err != nil {
		return nil, oracleFindingFailure(KindInvariantViolation, "stored oracle receipt facts are malformed", "restore verified finding facts")
	}
	var oracles map[WorkerJobBinding]*AcceptanceOracle
	if len(ordered) > 0 {
		oracles, err = readWorkerJobOraclesTx(ctx, q, workID)
		if err != nil {
			return nil, err
		}
	}
	for _, fact := range ordered {
		if fact == nil || fact.WorkerOpenOracleFinding == nil || fact.FindingID == "" || lineage.findings[fact.FindingID] != nil {
			return nil, oracleFindingFailure(KindInvariantViolation, "stored ranked-finding identities are malformed", "restore verified finding facts")
		}
		finding := fact.WorkerOpenOracleFinding
		setOracleFindingSource(finding, fact.SourceJob, oracles)
		lineage.findings[finding.FindingID] = finding
		lineage.openOrder = append(lineage.openOrder, finding.FindingID)
	}
	return lineage, nil
}

func storeOracleFindingFacts(ctx context.Context, q migrationRunner, workID string, lineage *workerOracleFindingLineage) error {
	ordered := make([]oracleFindingFact, 0, len(lineage.openOrder))
	for _, id := range lineage.openOrder {
		finding := lineage.findings[id]
		ordered = append(ordered, oracleFindingFact{WorkerOpenOracleFinding: finding, SourceJob: finding.sourceJob})
	}
	findings, err := json.Marshal(ordered)
	if err != nil {
		return err
	}
	receipts, err := json.Marshal(lineage.retainedReceipts)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO worker_oracle_finding_facts(work_id,oracle_capable,findings,receipts) VALUES(?,?,?,?) ON CONFLICT(work_id) DO UPDATE SET oracle_capable=excluded.oracle_capable,findings=excluded.findings,receipts=excluded.receipts`, workID, lineage.oracleCapable, string(findings), string(receipts))
	return err
}

// writeOracleFindingFactsTx runs only after admission. Completion validation
// reads the prior aggregate, before this effect's findings and receipts land.
// Contract supersession must call this helper in the same write transaction.
func writeOracleFindingFactsTx(ctx context.Context, tx *sql.Tx, event Event) error {
	lineage, err := readWorkerOracleFindingLineageTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	oracles, err := readWorkerJobOraclesTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	actors := map[string]string{}
	if event.Kind == WorkerCompleted {
		var report WorkerCompletedPayload
		if err := json.Unmarshal(event.Payload, &report); err != nil {
			return err
		}
		var actor string
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(json_extract(dispatch_payload,'$.lane_actor_ref'),'') FROM worker_attempts WHERE work_id=? AND attempt_id=? AND dispatch_payload IS NOT NULL`, event.SubjectID, report.AttemptID).Scan(&actor)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		actors[report.AttemptID] = actor
	}
	if err := reduceOracleFindingEffect(lineage, event, oracles, actors); err != nil {
		return err
	}
	return storeOracleFindingFacts(ctx, tx, event.SubjectID, lineage)
}

// backfillOracleFindingFacts is migration-only. Its caller owns one guard
// scope and transaction. Close reads before querying descriptors or writing
// facts: the store has only one pooled connection.
func backfillOracleFindingFacts(ctx context.Context, q migrationRunner) error {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT e.subject_id FROM domain_events e JOIN work_items w ON w.id=e.subject_id WHERE e.subject_type=? AND e.kind IN (?,?,?)`, SubjectWorkItem, WorkerCompleted, WorkerDispatched, WorkflowContractSuperseded)
	if err != nil {
		return err
	}
	var workIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		workIDs = append(workIDs, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, workID := range workIDs {
		if err := backfillOracleFindingWork(ctx, q, workID); err != nil {
			return err
		}
	}
	return nil
}

func backfillOracleFindingWork(ctx context.Context, q migrationRunner, workID string) error {
	oracles, err := readWorkerJobOraclesTx(ctx, q, workID)
	if err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, `SELECT seq,event_id,kind,payload FROM domain_events WHERE subject_type=? AND subject_id=? AND kind IN (?,?,?) ORDER BY seq`, SubjectWorkItem, workID, WorkerCompleted, WorkerDispatched, WorkflowContractSuperseded)
	if err != nil {
		return err
	}
	lineage := &workerOracleFindingLineage{findings: map[string]*WorkerOpenOracleFinding{}, retainedReceipts: make([]WorkerOracleReceipt, 0, 8)}
	actors := map[string]string{}
	for rows.Next() {
		event := Event{SubjectID: workID, SubjectType: SubjectWorkItem}
		if err := rows.Scan(&event.Seq, &event.EventID, &event.Kind, &event.Payload); err != nil {
			_ = rows.Close()
			return err
		}
		if err := reduceOracleFindingEffect(lineage, event, oracles, actors); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return storeOracleFindingFacts(ctx, q, workID, lineage)
}
