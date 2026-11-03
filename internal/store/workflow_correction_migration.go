package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

func preflightOrdinaryWorkflowRecords(ctx context.Context, q queryer) error {
	rows, err := q.QueryContext(ctx, `SELECT d.payload,d.assertion_id,d.correction_id,d.correction_occurred_at,d.correction_payload,r.effect_seq,r.actor,r.payload_version,r.payload_digest
FROM workflow_delivery_state d LEFT JOIN effect_receipts r ON r.event_id=d.correction_id AND r.subject_type='work_item' AND r.subject_id=d.work_id AND r.kind='workflow.delivery_corrected'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var payload, assertionID string
		var id, occurred, correction, actor, receiptDigest sql.NullString
		var seq, version sql.NullInt64
		if err := rows.Scan(&payload, &assertionID, &id, &occurred, &correction, &seq, &actor, &version, &receiptDigest); err != nil {
			return err
		}
		var valid bool
		if id.Valid {
			valid = id.String != "" && occurred.Valid && occurred.String != "" && correction.Valid && seq.Valid && seq.Int64 > 0 && actor.Valid && actor.String != "" && version.Valid && version.Int64 > 0
			payload = correction.String
			digest, err := effectPayloadDigest([]byte(payload))
			valid = valid && err == nil && receiptDigest.Valid && receiptDigest.String == digest
		} else {
			valid = !occurred.Valid && !correction.Valid
		}
		var delivery struct {
			Artifact string `json:"delivery_artifact"`
			State    string `json:"delivery_state"`
			TargetID string `json:"target_event_id"`
		}
		if !valid || json.Unmarshal([]byte(payload), &delivery) != nil || !ValidReference(delivery.Artifact) || delivery.State != "asserted" || id.Valid && delivery.TargetID != assertionID {
			return newFailure(KindInvariantViolation, "migrate", "ordinary workflow records require verifiable delivery state and correction receipts; restore a verified snapshot", false, "restore a verified snapshot before retrying the upgrade")
		}
	}
	return rows.Err()
}

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

func writeWorkflowCorrectionFacts(ctx context.Context, q migrationRunner, workID string, state correctionStateFact) error {
	_, err := q.ExecContext(ctx, `INSERT INTO workflow_correction_state(work_id,state) SELECT id,? FROM work_items WHERE id=? ON CONFLICT(work_id) DO UPDATE SET state=excluded.state`, workflowJSON(state), workID)
	return workflowProjectionError(err, "cannot record current correction state")
}

func correctionStateForEvent(event Event) (correctionStateFact, bool, error) {
	state := correctionStateFact{Boundary: event.Seq}
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
