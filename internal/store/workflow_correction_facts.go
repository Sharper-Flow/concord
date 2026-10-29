package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

type workflowCorrectionCompletionFields struct {
	ActionID             string   `json:"action_id"`
	StepID               string   `json:"step_id"`
	OpenFindingIDs       []string `json:"correction_open_finding_ids"`
	CorrectionDiagnosis  string   `json:"correction_diagnosis"`
	CorrectionStrategy   string   `json:"correction_strategy"`
	CorrectionPredicates []string `json:"correction_predicate_ids"`
	CorrectionEvidence   []string `json:"correction_evidence_refs"`
}

type correctionRecordFact struct {
	Seq      int64                              `json:"seq"`
	SourceID string                             `json:"source_id"`
	Fields   workflowCorrectionCompletionFields `json:"fields"`
}

type correctionStateFact struct {
	Boundary int64                  `json:"boundary"`
	Records  []correctionRecordFact `json:"records"`
}

func readWorkflowCorrectionContext(ctx context.Context, q queryer, workID string) (*WorkflowCorrectionContext, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT state FROM workflow_correction_state WHERE work_id=?`, workID).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, workflowProjectionError(err, "cannot read current correction state")
	}
	var state correctionStateFact
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, newFailure(KindInvariantViolation, "workflow_correction", "current correction state is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	for i := len(state.Records) - 1; i >= 0; i-- {
		record := state.Records[i]
		if record.Seq <= state.Boundary || record.Fields.ActionID != "request_correction" {
			continue
		}
		fields := record.Fields
		return &WorkflowCorrectionContext{Disposition: "verification", PredicateIDs: correctionReferenceStrings(fields.CorrectionPredicates), EvidenceRefs: correctionReferenceStrings(fields.CorrectionEvidence), Diagnosis: fields.CorrectionDiagnosis, Strategy: fields.CorrectionStrategy, SourceEventID: record.SourceID, SourceEventSeq: record.Seq}, nil
	}
	return nil, nil
}

func writeWorkflowCorrectionFacts(ctx context.Context, q migrationRunner, workID string, state correctionStateFact) error {
	_, err := q.ExecContext(ctx, `INSERT INTO workflow_correction_state(work_id,state) SELECT id,? FROM work_items WHERE id=? ON CONFLICT(work_id) DO UPDATE SET state=excluded.state`, workflowJSON(state), workID)
	return workflowProjectionError(err, "cannot record current correction state")
}

func correctionStateForEvent(event Event) (correctionStateFact, bool, error) {
	state := correctionStateFact{Boundary: event.Seq}
	if event.Kind == WorkflowContractSuperseded {
		return state, true, nil
	}
	if event.Kind != WorkflowActionCompleted {
		return state, false, nil
	}
	var completed struct {
		workflowCorrectionCompletionFields
		Review *ReviewBlock `json:"review"`
	}
	if err := json.Unmarshal(event.Payload, &completed); err != nil {
		return state, false, err
	}
	fields := completed.workflowCorrectionCompletionFields
	switch fields.ActionID {
	case "request_correction":
		state.Boundary = 0
		state.Records = []correctionRecordFact{{Seq: event.Seq, SourceID: event.EventID, Fields: fields}}
		return state, true, nil
	case "record_review":
		if completed.Review == nil || completed.Review.Disposition != "ready_for_final_proof" && completed.Review.InputCandidate == completed.Review.OutputCandidate {
			return state, false, nil
		}
		return state, true, nil
	case "record_delivery", "record_finding", "record_decision":
		return state, true, nil
	}
	return state, false, nil
}

func advanceWorkflowCorrectionFactsTx(ctx context.Context, tx *sql.Tx, event Event) error {
	state, changed, err := correctionStateForEvent(event)
	if err != nil || !changed {
		return err
	}
	return writeWorkflowCorrectionFacts(ctx, tx, event.SubjectID, state)
}

// Historical migration reads genuine correction/production events only. It
// never manufactures an accepted Review or a subagent execution outcome.
func backfillWorkflowCorrectionFacts(ctx context.Context, q migrationRunner) error {
	rows, err := q.QueryContext(ctx, `SELECT e.seq,e.subject_id,e.event_id,e.kind,e.payload FROM domain_events e JOIN work_items w ON w.id=e.subject_id WHERE e.subject_type='work_item' AND e.kind IN ('workflow.contract_superseded','workflow.action_completed') ORDER BY e.seq`)
	if err != nil {
		return err
	}
	states := map[string]correctionStateFact{}
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.Seq, &event.SubjectID, &event.EventID, &event.Kind, &event.Payload); err != nil {
			rows.Close()
			return err
		}
		state, changed, err := correctionStateForEvent(event)
		if err != nil {
			rows.Close()
			return err
		}
		if changed {
			states[event.SubjectID] = state
		}
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
	for workID, state := range states {
		if err := writeWorkflowCorrectionFacts(ctx, q, workID, state); err != nil {
			return err
		}
	}
	_, err = q.ExecContext(ctx, `DELETE FROM fold_guard`)
	return err
}
