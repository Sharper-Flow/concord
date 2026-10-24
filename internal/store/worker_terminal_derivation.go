package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// ApplyWorkerEvidenceOperationTx appends and folds one live worker-evidence
// event inside the caller's durable transaction. It is the store-owned ingest
// seam for the worker-evidence verbs that carry terminal outcomes: the
// terminal worker fact and the workflow record the engine derives from it
// commit in one transaction, so a caller cannot land the terminal fact and
// lose the workflow record it owes. Replay never reaches this seam: rebuilt
// journals fold their stored events unchanged.
//
// The seam is also the one frontier-attribution and delivery-
// observation owner. It derives consumed_context_frontier from the attempt's
// own dispatch authorization, attaches the sealed preparation's delivery
// observation after revalidating every sampled identity with SQL only, and
// refuses a completed implementation attempt that arrived without one.
func ApplyWorkerEvidenceOperationTx(ctx context.Context, transaction *Transaction, event Event) (ApplyOperationResult, error) {
	tx, err := transactionSQL(transaction, "worker_evidence")
	if err != nil {
		return ApplyOperationResult{}, err
	}
	scope := transaction.fold
	if scope == nil {
		scope = newFoldScope(tx)
	}
	enriched := event
	if event.Kind == WorkerCompleted || event.Kind == WorkerFailed {
		enriched, err = enrichWorkerTerminalEventTx(ctx, tx, event)
		if err != nil {
			return ApplyOperationResult{}, err
		}
	}
	if event.Kind == WorkerFailed {
		// the terminal failure's attribution observation derives
		// its core identity here, inside the same authenticated transaction
		// that consumes the signed assertion. A caller-authored origin or
		// identity refuses with zero effects.
		enriched, err = enrichWorkerFailedAttributionTx(ctx, tx, enriched)
		if err != nil {
			return ApplyOperationResult{}, err
		}
	}
	events := []Event{enriched}
	derived, err := deriveWorkflowWorkerFailureEventsTx(ctx, tx, enriched)
	if err != nil {
		return ApplyOperationResult{}, err
	}
	events = append(events, derived...)
	return applyWorkflowOperationTx(ctx, tx, Operation{Events: events}, scope)
}

// deriveWorkflowWorkerFailureEventsTx derives the workflow failure record a
// dispatched attempt's terminal failed event owes (CD-0198 D5). The record is
// the same workflow.action.completed event a record_worker_failure call
// appended, derived from the attempt's own terminal event exactly once: the
// correction diagnosis fills from the failure detail the attempt already
// carries and the strategy from the standing default, which are the values
// the correction context applies when the caller omits them, so the derived
// record carries no correction fields and every reader stays unchanged. The
// derivation is structural, never prose: it derives only when the attempt's
// authorized dispatch window still names the instance's current step at the
// window's own epoch, no failure record exists yet for the attempt, and the
// pinned definition derives the record. A terminal event outside those
// conditions lands unchanged, and the engine's failure-recovery route stays
// its disposition.
func deriveWorkflowWorkerFailureEventsTx(ctx context.Context, tx *sql.Tx, failed Event) ([]Event, error) {
	if failed.Kind != WorkerFailed || failed.SubjectType != SubjectWorkItem || failed.SubjectID == "" {
		return nil, nil
	}
	var payload WorkerFailedPayload
	if err := decodeClosedWorkerPayload(failed, &payload); err != nil {
		return nil, err
	}
	if payload.AttemptID == "" {
		return nil, nil
	}
	entry, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, BuiltinWorkflowRegistry(), failed.SubjectID)
	if err != nil {
		return nil, err
	}
	if !workflowDerivedWorkerFailureActive(entry.Definition) {
		return nil, nil
	}
	window, err := FindAuthorizedDispatchWindowTx(ctx, tx, failed.SubjectID, payload.AttemptID)
	if err != nil {
		var failure *Failure
		if failureAs(err, &failure) && failure.Kind == KindUnauthorizedDispatch {
			// No authorized workflow window: the attempt was never opened by
			// a dispatch_worker action on this work item, so no workflow step
			// holds it. The terminal event lands without a derived record.
			return nil, nil
		}
		return nil, err
	}
	var currentStep string
	if err := tx.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, failed.SubjectID).Scan(&currentStep); err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_fail_derive", "cannot read the current workflow step", true, "retry once the workflow projection is readable", err)
	}
	if window.StepID != currentStep {
		// The step moved on: the failure is dispositionable only through the
		// engine's failure-recovery route, exactly as before the derivation.
		return nil, nil
	}
	recorded, err := workerRecordedFailureCountTx(ctx, tx, failed.SubjectID, payload.AttemptID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_fail_derive", "cannot inspect recorded worker failures", true, "retry once the workflow projection is readable", err)
	}
	if recorded != 0 {
		return nil, nil
	}
	startEpoch, found, err := latestWorkflowActionStartEpochOnly(ctx, tx, failed.SubjectID, currentStep)
	if err != nil {
		return nil, err
	}
	if !found || startEpoch != window.AttemptEpoch {
		// A newer fenced attempt superseded this failure's epoch at the step;
		// the derived record would refuse the same fence a manual record
		// refuses, so the terminal event lands without one.
		return nil, nil
	}
	dispatchActor, err := workerAuthorizationActorTx(ctx, tx, failed.SubjectID, payload.AttemptID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, wrapFailure(KindUnavailable, "worker_fail_derive", "cannot read the dispatch authorization actor", true, "retry once the workflow projection is readable", err)
	}
	if dispatchActor == "" {
		return nil, nil
	}
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, failed.SubjectID).Scan(&version); err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_fail_derive", "cannot read the work version", true, "retry once the work item is readable", err)
	}
	eventID := failed.EventID + workflowDerivedWorkerFailureEventIDSuffix
	values := map[string]any{
		"work_id": failed.SubjectID, "expected_version": version, "resulting_version": version + 1,
		"step_id": window.StepID, "action_id": "record_worker_failure", "attempt_epoch": window.AttemptEpoch,
		"worker_attempt_id": payload.AttemptID, "result_evidence_refs": []string{"evidence:" + eventID},
		"changed_refs": []string{failed.SubjectID}, "actor_ref": dispatchActor,
		// the terminal owns the observation; the derived record
		// refers to the terminal attribution by the failed event's id.
		"failure_attribution_ref": failed.EventID,
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return nil, workflowProjectionError(err, "cannot encode the derived worker failure record")
	}
	registration, ok := registeredEventKind(WorkflowActionCompleted)
	if !ok {
		return nil, newFailure(KindInvariantViolation, "worker_fail_derive", "workflow action completed is not a registered event kind", false, "repair the event registry")
	}
	return []Event{{
		EventID: eventID, Kind: WorkflowActionCompleted, SubjectType: SubjectWorkItem, SubjectID: failed.SubjectID,
		Actor: dispatchActor, OccurredAt: failed.OccurredAt.UTC(), PayloadVersion: registration.CurrentVersion, Payload: raw,
	}}, nil
}

// workflowDerivedWorkerFailureEventIDSuffix pairs the derived workflow
// failure record with the terminal event it derives from. The pair is the
// fold's structural admission: an event carrying the suffix folds only when
// the terminal worker.failed fact it names exists earlier in effect order and
// its attempt window matches the record's step and epoch.
const workflowDerivedWorkerFailureEventIDSuffix = ":workflow-failure"

// workflowDerivedWorkerFailureActive reports whether a pinned definition
// derives the workflow failure record from the attempt's own terminal event
// (CD-0198 D5). Earlier versions keep the callable record and the behavior
// and digest they shipped with.
func workflowDerivedWorkerFailureActive(definition WorkflowDefinition) bool {
	return workflowCapabilities(definition).has(WorkflowCapabilityDerivedWorkerFailure)
}

// workflowDerivedWorkerFailurePairAdmitsFold reports whether one
// record_worker_failure completion is the derivation pair of a terminal
// worker.failed event (CD-0198 D5). The check mirrors the CD-0198 D3
// derived-start pair: the pin must derive the record, the completing event's
// id must name a terminal worker.failed fact recorded earlier in effect order,
// that event's attempt must be the record's attempt, and the attempt's
// authorized dispatch window must name the record's step and epoch. A
// caller-authored event carrying the suffix without its terminal event
// refuses here, so the pair admits the engine's derivation alone.
func workflowDerivedWorkerFailurePairAdmitsFold(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, event Event, payload workflowActionCompletedPayload) (bool, error) {
	if !workflowDerivedWorkerFailureActive(definition) || payload.ActionID != "record_worker_failure" {
		return false, nil
	}
	if !strings.HasSuffix(event.EventID, workflowDerivedWorkerFailureEventIDSuffix) {
		return false, nil
	}
	if event.Seq <= 0 {
		return false, nil
	}
	terminalEventID := strings.TrimSuffix(event.EventID, workflowDerivedWorkerFailureEventIDSuffix)
	_, attemptID, err := readWorkerTerminalSourceBefore(ctx, tx, event.SubjectID, terminalEventID, WorkerFailed, event.Seq)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, workflowProjectionError(err, "cannot read the derived failure's terminal event")
	}
	if attemptID == "" || attemptID != payload.WorkerAttemptID {
		return false, nil
	}
	window, err := FindAuthorizedDispatchWindowTx(ctx, tx, event.SubjectID, payload.WorkerAttemptID)
	if err != nil {
		var failure *Failure
		if failureAs(err, &failure) && failure.Kind == KindUnauthorizedDispatch {
			return false, nil
		}
		return false, err
	}
	return window.StepID == payload.StepID && window.AttemptEpoch == payload.AttemptEpoch, nil
}

// readWorkerTerminalSourceBefore resolves the exact current terminal identity.
// A zero bound reads current state; a positive bound is strictly exclusive.
func readWorkerTerminalSourceBefore(ctx context.Context, q queryer, workID, eventID, kind string, beforeSeq int64) (payload, attemptID string, err error) {
	var sourceAttempt string
	err = q.QueryRowContext(ctx, `SELECT terminal_payload,COALESCE(json_extract(terminal_payload,'$.attempt_id'),''),attempt_id
FROM worker_attempts WHERE work_id=? AND terminal_kind=? AND terminal_event_id=?
AND terminal_seq>0 AND (?=0 OR terminal_seq<?)`, workID, kind, eventID, beforeSeq, beforeSeq).Scan(&payload, &sourceAttempt, &attemptID)
	if err == nil && (sourceAttempt == "" || sourceAttempt != attemptID) {
		err = newFailure(KindInvariantViolation, "worker_terminal_source", "terminal payload attempt_id does not match its owning attempt", false, "reconcile_operation")
	}
	return payload, attemptID, err
}

// latestWorkflowActionStartEpochOnly reads the latest start epoch at a step
// without the sequence the full reader returns.
func latestWorkflowActionStartEpochOnly(ctx context.Context, q queryer, workID, stepID string) (int64, bool, error) {
	_, epoch, found, err := latestWorkflowActionStartAt(ctx, q, workID, stepID, 0)
	return epoch, found, err
}
