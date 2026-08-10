package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// ReplaceWorkflowOutcome is the semantic outcome-revision boundary. Outcome
// replacement is only legal while planning is still authoritative; execution
// must use contract supersession instead.
func ReplaceWorkflowOutcome(ctx context.Context, s *Store, workID string) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "replace_workflow_outcome", "store is not open", false, "open the authority database")
	}
	var step string
	if err := s.db.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		return wrapFailure(KindUnavailable, "replace_workflow_outcome", "cannot read workflow step", true, "retry once the workflow is readable", err)
	}
	if step != "planning" {
		return newFailure(KindInvalidTransition, "replace_workflow_outcome", "approved outcome cannot be replaced after execution begins", false, "supersede the contract during planning")
	}
	return nil
}

// ReplaceWorkflowCheckTx is the evaluator-owned check replacement boundary.
// It delegates to the authenticated verdict route, preserving actor fencing
// and evidence validation instead of inventing a second check mutation path.
func ReplaceWorkflowCheckTx(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, request WorkflowActionExecutionRequest) (WorkflowActionExecutionResult, error) {
	request.ActionID = "record_verdict"
	return ApplyWorkflowActionTx(ctx, tx, registry, request)
}

// SupersedeWorkflowContract applies a contract revision and its consequential
// dependent notices in one owning transaction. A consumed active hard
// dependent receives a breaking notice; unconsumed dependents do not receive a
// fabricated impact.
func SupersedeWorkflowContract(ctx context.Context, s *Store, event Event) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "supersede_workflow_contract", "store is not open", false, "open the authority database")
	}
	if event.Kind != WorkflowContractSuperseded {
		return newFailure(KindInvalidOperation, "supersede_workflow_contract", "contract revision entry point requires workflow.contract_superseded", false, "supply the typed supersession event")
	}
	var payload workflowContractSupersededPayload
	if err := decodeWorkflowPayload(event, &payload); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "supersede_workflow_contract", "cannot begin contract revision", true, "retry once the database is writable", err)
	}
	rollback := func(cause error) error { _ = tx.Rollback(); return cause }
	if err := enterFold(ctx, tx); err != nil {
		return rollback(err)
	}
	defer func() { _ = leaveFold(ctx, tx) }()
	if _, err := appendEvent(ctx, tx, event, true); err != nil {
		return rollback(err)
	}
	if err := foldRegisteredEvent(ctx, tx, event); err != nil {
		return rollback(err)
	}
	version := *payload.ResultingVersion
	rows, err := tx.QueryContext(ctx, `SELECT edge_id,target_work_id,severity FROM workflow_impact_edges WHERE work_id=? AND edge_kind='depends_on' AND edge_class='hard' ORDER BY edge_id`, event.SubjectID)
	if err != nil {
		return rollback(wrapFailure(KindUnavailable, "supersede_workflow_contract", "cannot inspect hard dependents", true, "retry once the database is readable", err))
	}
	type dependent struct{ edgeID, target, severity string }
	var dependents []dependent
	for rows.Next() {
		var item dependent
		if err := rows.Scan(&item.edgeID, &item.target, &item.severity); err != nil {
			rows.Close()
			return rollback(wrapFailure(KindUnavailable, "supersede_workflow_contract", "cannot read hard dependent", true, "retry once the database is readable", err))
		}
		dependents = append(dependents, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return rollback(wrapFailure(KindUnavailable, "supersede_workflow_contract", "cannot scan hard dependents", true, "retry once the database is readable", err))
	}
	rows.Close()
	for _, item := range dependents {
		var consumed, active int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contracts WHERE work_id=? AND contract_version=?`, item.target, payload.PreviousContractVersion).Scan(&consumed); err != nil {
			return rollback(wrapFailure(KindUnavailable, "supersede_workflow_contract", "cannot inspect dependent contract consumption", true, "retry once the database is readable", err))
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_instances WHERE work_id=? AND instance_state NOT IN ('completed','cancelled','superseded')`, item.target).Scan(&active); err != nil {
			return rollback(wrapFailure(KindUnavailable, "supersede_workflow_contract", "cannot inspect dependent lifecycle", true, "retry once the database is readable", err))
		}
		if consumed == 0 || active == 0 {
			continue
		}
		noticeID := WorkflowNoticeID(event.SubjectID, payload.NewContractVersion, "workflow_contract", fmt.Sprintf("contract:%d", payload.PreviousContractVersion), item.target, "breaking")
		noticePayload := map[string]any{"work_id": event.SubjectID, "expected_version": version, "resulting_version": version + 1, "notice_id": noticeID, "source_contract_version": payload.NewContractVersion, "entity_kind": "workflow_contract", "entity_ref": fmt.Sprintf("contract:%d", payload.PreviousContractVersion), "target_work_id": item.target, "edge_id": item.edgeID, "old_hash": nil, "new_hash": nil, "severity": "breaking"}
		raw, _ := json.Marshal(noticePayload)
		notice := Event{EventID: "notice-event:" + noticeID, Kind: WorkflowImpactNoticeRecorded, SubjectType: SubjectWorkItem, SubjectID: event.SubjectID, Actor: event.Actor, OccurredAt: event.OccurredAt, PayloadVersion: 1, Payload: raw}
		if _, err := appendEvent(ctx, tx, notice, true); err != nil {
			return rollback(newFailure(KindOperationConflict, "supersede_workflow_contract", "dependent impact notice conflicted", false, "reconcile_operation"))
		}
		if err := foldRegisteredEvent(ctx, tx, notice); err != nil {
			return rollback(err)
		}
		version++
	}
	if err := tx.Commit(); err != nil {
		return wrapFailure(KindUnavailable, "supersede_workflow_contract", "cannot commit contract revision", true, "retry once the database is writable", err)
	}
	return nil
}
