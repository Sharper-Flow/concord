package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
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
	return applyWorkflowActionRawTx(ctx, tx, registry, request)
}

// SupersedeWorkflowContract applies a contract revision and its consequential
// reverse-dependent notices in one owning transaction. A consumed active hard
// dependent receives a breaking notice; every other declared dependent receives
// an advisory non-breaking notice.
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
	if _, err := appendEvent(ctx, tx, event, true); err != nil {
		return rollback(err)
	}
	if err := foldRegisteredEvent(ctx, tx, event); err != nil {
		return rollback(err)
	}
	version := *payload.ResultingVersion
	rows, err := tx.QueryContext(ctx, `SELECT work_id,edge_id,edge_class FROM workflow_impact_edges WHERE target_work_id=? AND edge_kind='depends_on' AND edge_class IN ('hard','soft') ORDER BY work_id,edge_id`, event.SubjectID)
	if err != nil {
		return rollback(wrapFailure(KindUnavailable, "supersede_workflow_contract", "cannot inspect hard dependents", true, "retry once the database is readable", err))
	}
	type dependent struct{ workID, edgeID, edgeClass string }
	var dependents []dependent
	for rows.Next() {
		var item dependent
		if err := rows.Scan(&item.workID, &item.edgeID, &item.edgeClass); err != nil {
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
	selected := make(map[string]dependent, len(dependents))
	for _, candidate := range dependents {
		current, exists := selected[candidate.workID]
		if !exists || candidate.edgeClass == "hard" && current.edgeClass != "hard" || candidate.edgeClass == current.edgeClass && candidate.edgeID < current.edgeID {
			selected[candidate.workID] = candidate
		}
	}
	owners := make([]string, 0, len(selected))
	for owner := range selected {
		owners = append(owners, owner)
	}
	sort.Strings(owners)
	for _, owner := range owners {
		item := selected[owner]
		var consumed, active int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contracts WHERE work_id=? AND contract_version=?`, item.workID, payload.PreviousContractVersion).Scan(&consumed); err != nil {
			return rollback(wrapFailure(KindUnavailable, "supersede_workflow_contract", "cannot inspect dependent contract consumption", true, "retry once the database is readable", err))
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_instances WHERE work_id=? AND instance_state NOT IN ('completed','cancelled','superseded')`, item.workID).Scan(&active); err != nil {
			return rollback(wrapFailure(KindUnavailable, "supersede_workflow_contract", "cannot inspect dependent lifecycle", true, "retry once the database is readable", err))
		}
		severity := "non-breaking"
		if item.edgeClass == "hard" && consumed != 0 && active != 0 {
			severity = "breaking"
		}
		noticeID := WorkflowNoticeID(event.SubjectID, payload.NewContractVersion, "workflow_contract", fmt.Sprintf("contract:%d", payload.PreviousContractVersion), item.workID, severity)
		noticePayload := map[string]any{"work_id": event.SubjectID, "expected_version": version, "resulting_version": version + 1, "notice_id": noticeID, "source_contract_version": payload.NewContractVersion, "entity_kind": "workflow_contract", "entity_ref": fmt.Sprintf("contract:%d", payload.PreviousContractVersion), "target_work_id": item.workID, "edge_owner_work_id": item.workID, "edge_id": item.edgeID, "old_hash": nil, "new_hash": nil, "severity": severity}
		raw, _ := json.Marshal(noticePayload)
		notice := Event{EventID: "notice-event:" + noticeID, Kind: WorkflowImpactNoticeRecorded, SubjectType: SubjectWorkItem, SubjectID: event.SubjectID, Actor: event.Actor, OccurredAt: event.OccurredAt, PayloadVersion: 2, Payload: raw}
		if _, err := appendEvent(ctx, tx, notice, true); err != nil {
			return rollback(newFailure(KindOperationConflict, "supersede_workflow_contract", "dependent impact notice conflicted", false, "reconcile_operation"))
		}
		if err := foldRegisteredEvent(ctx, tx, notice); err != nil {
			return rollback(err)
		}
		version++
	}
	if err := leaveFold(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return wrapFailure(KindUnavailable, "supersede_workflow_contract", "cannot commit contract revision", true, "retry once the database is writable", err)
	}
	return nil
}
