package store

import (
	"database/sql"
	"encoding/json"
)

// A delivery unchanged from the job's base must prove its declared outcome.
// Review dispositions use their own acceptance rules instead of claiming delivery.
func guardAcceptWorkerResultChanges(g *workflowActionGuardContext, fields map[string]json.RawMessage) error {
	attemptID := workflowFieldStringDefault(fields, "attempt_id", "")
	var raw []byte
	err := g.tx.QueryRowContext(g.ctx, `SELECT terminal_payload FROM worker_attempts
		WHERE work_id=? AND terminal_kind=? AND attempt_id=?
		AND lifecycle_state='completed'`, g.request.WorkID, WorkerCompleted, attemptID).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return workflowProjectionError(err, "cannot read the completed worker result")
	}
	var completed WorkerCompletedPayload
	if err := json.Unmarshal(raw, &completed); err != nil {
		return newFailure(KindInvariantViolation, "workflow_action", "the completed worker result is malformed", false, "reconcile the invalid retained worker terminal fact before retrying")
	}
	if completed.Review != nil || completed.WorkerJob == nil || completed.ObservedDelivery == nil || completed.ObservedDelivery.State != "clean" {
		return nil
	}
	var base string
	err = g.tx.QueryRowContext(g.ctx, `SELECT COALESCE(json_extract(payload,'$.worker_subject_commit'),'')
		FROM worker_authorizations WHERE work_id=? AND json_extract(payload,'$.worker_job.job_id')=?
		ORDER BY seq LIMIT 1`, g.request.WorkID, completed.WorkerJob.JobID).Scan(&base)
	if err != nil {
		return workflowProjectionError(err, "cannot read the job's base subject")
	}
	if base == "" || base != completed.ObservedDelivery.Commit {
		return nil
	}
	oracles, err := readWorkerJobOraclesTx(g.ctx, g.tx, g.request.WorkID)
	if err != nil {
		return err
	}
	oracle := oracles[*completed.WorkerJob]
	if proven, err := unchangedJobOutcomeProven(g, &completed, oracle); err != nil {
		return err
	} else if proven {
		return nil
	}
	return newFailure(KindInvalidOperation, "workflow_action", "accept_worker_result: the job has no changed paths and lacks fresh passing evidence that its declared outcome already holds", false,
		"bind_evidence for each independently executed control run and retry; otherwise reject_worker_result and dispatch a correction whose report proves every declared job control passes on the unchanged subject, or that delivers the outcome a failing control shows unmet; no edit is required when the outcome already holds")
}

func unchangedJobOutcomeProven(g *workflowActionGuardContext, completed *WorkerCompletedPayload, oracle *AcceptanceOracle) (bool, error) {
	if oracle == nil || len(oracle.Controls) == 0 {
		return false, nil
	}
	var worker string
	if err := g.tx.QueryRowContext(g.ctx, `SELECT COALESCE(json_extract(dispatch_payload,'$.lane_actor_ref'),'') FROM worker_attempts WHERE work_id=? AND attempt_id=? AND dispatch_payload IS NOT NULL`, g.request.WorkID, completed.AttemptID).Scan(&worker); err != nil {
		return false, workflowProjectionError(err, "cannot read the result's producing actor")
	}
	controls := oracleControlIndex(oracle)
	covered := map[string]bool{}
	for _, receipt := range inFlightOracleReceipts(completed) {
		if receipt.Result != OracleReceiptResultPass || receipt.SubjectCommit != completed.ObservedDelivery.Commit {
			continue
		}
		if err := validateWorkerOracleReceiptJoins(&receipt, controls); err != nil {
			return false, err
		}
		producer, err := workerOracleReceiptProducerTx(g.ctx, g.tx, g.request.WorkID, &receipt, oracle, completed.ObservedDelivery.Commit)
		if err != nil {
			return false, err
		}
		for _, id := range receipt.ControlIDs {
			control := controls[id]
			qualified, err := unchangedJobControlProven(g, completed, worker, receipt, control, producer)
			if err != nil {
				return false, err
			}
			if qualified {
				covered[id] = true
			}
		}
	}
	return len(covered) == len(controls), nil
}

func unchangedJobControlProven(g *workflowActionGuardContext, completed *WorkerCompletedPayload, worker string, receipt WorkerOracleReceipt, control OracleControl, producer *WorktreeVerifyResult) (bool, error) {
	if !oracleReceiptCoversControl(receipt, control) {
		return false, nil
	}
	fresh, err := oracleControlProducerFreshTx(g.ctx, g.tx, g.request.WorkID, receipt.SubjectCommit, control, producer)
	if err != nil || !fresh {
		return false, err
	}
	if control.RequiredEvidenceRole == OracleEvidenceRoleIndependentlyExecuted {
		return unchangedJobIndependentProducer(g, completed, worker, receipt.RunRef, producer)
	}
	return true, nil
}

func unchangedJobIndependentProducer(g *workflowActionGuardContext, completed *WorkerCompletedPayload, worker, runRef string, producer *WorktreeVerifyResult) (bool, error) {
	bound, err := oracleClosureProducerBoundTx(g.ctx, g.tx, g.request.WorkID, runRef, producer.OperationRef)
	if err != nil {
		return false, err
	}
	independent, err := oracleClosureIndependentProducerTx(g.ctx, g.tx, g.request.WorkID, producer, completed, worker)
	if err != nil {
		return false, err
	}
	return bound && independent, nil
}
