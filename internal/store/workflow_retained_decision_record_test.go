package store

import (
	"encoding/json"
	"testing"
	"time"
)

func resolutionEvent(id, kind, workID, actor string, version int64, values map[string]any) Event {
	return workflowTypedEvent(id, kind, workID, actor, time.Unix(9, 0).UTC(), version, values)
}

func assertNoFoldGuardRows(t *testing.T, s *Store) {
	t.Helper()
	var guards int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM write_guard`).Scan(&guards); err != nil {
		t.Fatal(err)
	}
	if guards != 0 {
		t.Fatalf("%d write guard rows survived a refused operation", guards)
	}
}

func TestWorkflowHistoricalDecisionRecordFold(t *testing.T) {
	for _, shape := range []string{"fields", "checkpoint", "top"} {
		t.Run(shape, func(t *testing.T) {
			ctx := t.Context()
			s := openTemp(t)
			seedWork(t, s, "decision-history")
			fields := map[string]any{"action_id": "record_decision", "question": "adopt the delivered change?", "options_considered": []string{"adopt", "reject"}, "decision": "accepted_decision", "rationale": "the verdict holds", "consequences": []string{"the change ships"}, "inputs": []string{"the delivered evidence"}, "poc_findings": "none required"}
			var payload any = fields
			if shape == "fields" {
				payload = map[string]any{"action_id": "record_decision", "fields": fields}
			} else if shape == "checkpoint" {
				payload = map[string]any{"checkpoint": fields}
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
				t.Fatal(err)
			}
			event := Event{SubjectID: "decision-history", OccurredAt: time.Unix(9, 0).UTC()}
			if err := foldWorkflowDecisionRecord(ctx, tx, event, raw); err != nil {
				t.Fatal(err)
			}
			var question, decision string
			if err := tx.QueryRowContext(ctx, `SELECT question,decision FROM workflow_decision_records WHERE work_id=?`, event.SubjectID).Scan(&question, &decision); err != nil {
				t.Fatal(err)
			}
			if question != fields["question"] || decision != fields["decision"] {
				t.Fatalf("decision=%q/%q", question, decision)
			}
		})
	}
}

func TestWorkflowAcceptanceDeliverablesRequireCriterionAssociation(t *testing.T) {
	for _, associated := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "associated"}[associated], func(t *testing.T) {
			workID := map[bool]string{false: "deliverables-missing", true: "deliverables-associated"}[associated]
			s, _, _, _, _ := engineeringConfirmationFixture(t, workID, associated)
			tx, err := s.DatabaseForTesting().BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = workflowConfirmationDeliverablesTx(t.Context(), tx, WorkflowStepPlanResearch, workID)
			if associated && err != nil || !associated && !hasFailureKind(err, KindMissingEvidence) {
				t.Fatalf("associated=%v: %v", associated, err)
			}
		})
	}
}
