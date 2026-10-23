package store

import (
	"database/sql"
	"encoding/json"
)

// A clean first result at its dispatch commit must prove the job's declared
// outcome already holds; editing capability alone does not require an edit.
// Later attempts may deliver a retained draft from an earlier failed attempt;
// without a recorded job base, commit equality cannot judge those corrections.
func guardAcceptWorkerResultChanges(g *workflowActionGuardContext, fields map[string]json.RawMessage) error {
	attemptID := workflowFieldStringDefault(fields, "attempt_id", "")
	var raw []byte
	err := g.tx.QueryRowContext(g.ctx, `SELECT e.payload FROM domain_events e
		JOIN worker_attempts a ON a.work_id=e.subject_id AND a.attempt_id=json_extract(e.payload,'$.attempt_id')
		WHERE e.subject_type=? AND e.subject_id=? AND e.kind=? AND a.attempt_id=?
		AND a.capability_class='implementation' AND a.lifecycle_state='completed'
		ORDER BY e.seq DESC LIMIT 1`, string(SubjectWorkItem), g.request.WorkID, WorkerCompleted, attemptID).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return workflowProjectionError(err, "cannot read the completed editing result")
	}
	var completed WorkerCompletedPayload
	if err := json.Unmarshal(raw, &completed); err != nil {
		return newFailure(KindInvariantViolation, "workflow_action", "the completed editing result is malformed", false, "rebuild workflow projections from the event log")
	}
	if completed.WorkerJob == nil || completed.ObservedDelivery == nil || completed.ObservedDelivery.State != "clean" {
		return nil
	}
	window, err := findAuthorizedDispatchWindowCore(g.ctx, g.tx, g.request.WorkID, attemptID)
	if err != nil {
		return err
	}
	if window.SubjectCommit == "" || window.SubjectCommit != completed.ObservedDelivery.Commit {
		return nil
	}
	var firstAttempt string
	err = g.tx.QueryRowContext(g.ctx, `SELECT a.attempt_id FROM domain_events e
		JOIN worker_attempts a ON a.work_id=e.subject_id AND a.attempt_id=json_extract(e.payload,'$.worker_attempt_id')
		WHERE e.subject_type=? AND e.subject_id=? AND e.kind=?
		AND json_extract(e.payload,'$.action_id')='dispatch_worker'
		AND json_extract(e.payload,'$.worker_job.job_id')=? AND a.capability_class='implementation'
		ORDER BY e.seq LIMIT 1`, string(SubjectWorkItem), g.request.WorkID, WorkflowActionCompleted, completed.WorkerJob.JobID).Scan(&firstAttempt)
	if err != nil {
		return workflowProjectionError(err, "cannot read the editing job's first authorization")
	}
	if firstAttempt != attemptID {
		return nil
	}
	lineage, err := readWorkerOracleFindingLineageTx(g.ctx, g.tx, g.request.WorkID)
	if err != nil {
		return err
	}
	oracle := lineage.oracleForBinding(*completed.WorkerJob)
	if proven, err := unchangedJobOutcomeProven(g, &completed, oracle); err != nil {
		return err
	} else if proven {
		return nil
	}
	return newFailure(KindInvalidOperation, "workflow_action", "accept_worker_result: the job has no changed paths and lacks fresh passing evidence that its declared outcome already holds", false,
		"report fresh passing executions of every declared job control on the unchanged subject with the required evidence roles; if the outcome does not hold, reject_worker_result and correct the unmet outcome, not the absence of an edit")
}

func unchangedJobOutcomeProven(g *workflowActionGuardContext, completed *WorkerCompletedPayload, oracle *AcceptanceOracle) (bool, error) {
	if oracle == nil || len(oracle.Controls) == 0 {
		return false, nil
	}
	var worker string
	if err := g.tx.QueryRowContext(g.ctx, `SELECT json_extract(payload,'$.lane_actor_ref') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=?`, string(SubjectWorkItem), g.request.WorkID, WorkerDispatched, completed.AttemptID).Scan(&worker); err != nil {
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
			if !oracleReceiptCoversControl(receipt, control) {
				continue
			}
			fresh, err := oracleControlProducerFreshTx(g.ctx, g.tx, g.request.WorkID, receipt.SubjectCommit, control, producer)
			if err != nil {
				return false, err
			}
			if !fresh {
				continue
			}
			if control.RequiredEvidenceRole == OracleEvidenceRoleIndependentlyExecuted {
				bound, err := oracleClosureProducerBoundTx(g.ctx, g.tx, g.request.WorkID, receipt.RunRef, producer.OperationRef)
				if err != nil {
					return false, err
				}
				independent, err := oracleClosureIndependentProducerTx(g.ctx, g.tx, g.request.WorkID, producer, completed, worker)
				if err != nil {
					return false, err
				}
				if !bound || !independent {
					continue
				}
			}
			covered[id] = true
		}
	}
	return len(covered) == len(controls), nil
}
