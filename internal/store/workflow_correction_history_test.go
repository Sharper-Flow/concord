package store

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"
)

func correctionHistoryDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open(driverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE domain_events(seq INTEGER PRIMARY KEY,subject_type TEXT,subject_id TEXT,kind TEXT,payload BLOB,event_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func correctionFixtureTables(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE work_items(id TEXT PRIMARY KEY);
INSERT INTO work_items VALUES('work-history');
CREATE TABLE workflow_correction_state(work_id TEXT PRIMARY KEY,state TEXT);`); err != nil {
		t.Fatal(err)
	}
}

func applyCorrectionFixture(t *testing.T, db *sql.DB, seq int64, eventID, kind string, payload any) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := advanceWorkflowCorrectionFactsTx(t.Context(), tx, Event{Seq: seq, EventID: eventID, SubjectID: "work-history", Kind: kind, Payload: mustJSONValue(payload)}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// The fold consumes admitted completion facts; action admission is tested separately.
func correctionHistoryFixture(t *testing.T) (*sql.DB, func(string, any) int64) {
	t.Helper()
	db := correctionHistoryDatabase(t)
	correctionFixtureTables(t, db)
	var seq int64
	return db, func(kind string, payload any) int64 {
		t.Helper()
		seq++
		applyCorrectionFixture(t, db, seq, fmt.Sprintf("history-e%d", seq), kind, payload)
		return seq
	}
}

func correctionHistoryRequest(label string) map[string]any {
	return map[string]any{"action_id": "request_correction", "correction_diagnosis": "diagnosis:" + label, "correction_strategy": "strategy:" + label, "correction_predicate_ids": []string{"predicate:one"}, "correction_evidence_refs": []string{"evidence:" + label}}
}

func TestCorrectionHistoryLatestUnresolvedRecord(t *testing.T) {
	db, appendEvent := correctionHistoryFixture(t)
	appendEvent(WorkflowActionCompleted, correctionHistoryRequest("older"))
	seq := appendEvent(WorkflowActionCompleted, correctionHistoryRequest("newer"))
	want := &WorkflowCorrectionContext{Disposition: "verification", Diagnosis: "diagnosis:newer", Strategy: "strategy:newer", PredicateIDs: []string{"predicate:one"}, EvidenceRefs: []string{"evidence:newer"}, SourceEventID: fmt.Sprintf("history-e%d", seq), SourceEventSeq: seq}
	got, err := readWorkflowCorrectionContext(t.Context(), db, "work-history")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("latest genuine direction=%+v, %v, want %+v", got, err, want)
	}
	appendEvent(WorkflowEvidenceBound, progressEvidence("artifact", "evidence:unrelated"))
	if got, err := readWorkflowCorrectionContext(t.Context(), db, "work-history"); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("unrelated evidence changed correction=%+v, %v", got, err)
	}
}

func TestCorrectionHistoryOrdinaryProductionConsumption(t *testing.T) {
	for _, tc := range []struct {
		name, action, disposition string
		changed, consumed         bool
	}{
		{name: "unchanged continuing Review", action: "record_review", disposition: "continue"},
		{name: "changed continuing Review", action: "record_review", disposition: "continue", changed: true, consumed: true},
		{name: "ready Review", action: "record_review", disposition: "ready_for_final_proof", consumed: true},
		{name: "delivery", action: "record_delivery", consumed: true},
		{name: "finding", action: "record_finding", consumed: true},
		{name: "decision", action: "record_decision", consumed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, appendEvent := correctionHistoryFixture(t)
			appendEvent(WorkflowActionCompleted, correctionHistoryRequest("standing"))
			before, err := readWorkflowCorrectionContext(t.Context(), db, "work-history")
			if err != nil || before == nil {
				t.Fatalf("standing correction=%+v, %v", before, err)
			}
			payload := map[string]any{"action_id": tc.action}
			if tc.action == "record_review" {
				report := reviewBlockFixture()
				report.Disposition = tc.disposition
				report.Findings = []ReviewFinding{}
				if tc.changed {
					report.OutputCandidate.Commit = "cccccccccccccccccccccccccccccccccccccccc"
				}
				payload["review"] = report
			}
			appendEvent(WorkflowActionCompleted, payload)
			after, err := readWorkflowCorrectionContext(context.Background(), db, "work-history")
			if err != nil || (after == nil) != tc.consumed || !tc.consumed && !reflect.DeepEqual(after, before) {
				t.Fatalf("after %s: correction=%+v, %v, consumed=%v", tc.name, after, err, tc.consumed)
			}
		})
	}
}
