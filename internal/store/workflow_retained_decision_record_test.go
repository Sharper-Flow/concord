package store

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func resolutionEvent(id, kind, workID, actor string, version int64, values map[string]any) Event {
	return workflowTypedEvent(id, kind, workID, actor, time.Unix(9, 0).UTC(), version, values)
}

func TestWorkflowRetainedDecisionRecordRead(t *testing.T) {
	ctx := t.Context()
	s := openTemp(t)
	for _, id := range []string{"decision-a", "decision-b"} {
		seedWork(t, s, id)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, record := range []struct {
		work, question, decision string
		at                       int64
		supersedes, supersededBy any
	}{
		{"decision-a", "z question", "insufficient_evidence", 9, "earlier", "later"},
		{"decision-b", "other question", "accepted_decision", 8, nil, nil},
		{"decision-a", "a question", "accepted_decision", 9, nil, nil},
		{"decision-a", "first question", "accepted_decision", 8, nil, nil},
	} {
		fields := map[string]any{"action_id": "record_decision", "question": record.question, "options_considered": []string{"one", "two"}, "decision": record.decision, "rationale": "because", "consequences": []string{"result"}, "inputs": []string{"evidence"}, "poc_findings": "findings", "supersedes": record.supersedes, "superseded_by": record.supersededBy}
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if err := foldWorkflowDecisionRecord(ctx, tx, Event{SubjectID: record.work, OccurredAt: time.Unix(record.at, 0).UTC()}, raw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, detail string
		ids          []string
		present      bool
	}{
		{"item-full", "full", []string{"decision-b", "decision-a"}, true},
		{"item-summary", "summary", []string{"decision-a", "decision-b"}, false},
		{"broad-full", "full", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := s.QueryQ3(ctx, Q3Request{Product: "product", WorkIDs: tc.ids, Detail: tc.detail})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Items) != 2 {
				t.Fatalf("items: %+v", result.Items)
			}
			for _, item := range result.Items {
				data, err := json.Marshal(item)
				if err != nil {
					t.Fatal(err)
				}
				var shape map[string]json.RawMessage
				if err := json.Unmarshal(data, &shape); err != nil {
					t.Fatal(err)
				}
				raw, found := shape["decision_records"]
				if found != tc.present {
					t.Fatalf("decision_records present = %v, want %v: %s", found, tc.present, data)
				}
				if !found {
					continue
				}
				var got []map[string]any
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatal(err)
				}
				makeRecord := func(question, decision, at string, supersedes, supersededBy any) map[string]any {
					return map[string]any{"work_id": item.ID, "question": question, "options_considered": []any{"one", "two"}, "decision": decision, "rationale": "because", "consequences": []any{"result"}, "inputs": []any{"evidence"}, "poc_findings": "findings", "supersedes": supersedes, "superseded_by": supersededBy, "recorded_at": at}
				}
				want := []map[string]any{makeRecord("other question", "accepted_decision", "1970-01-01T00:00:08Z", nil, nil)}
				if item.ID == "decision-a" {
					want = []map[string]any{makeRecord("first question", "accepted_decision", "1970-01-01T00:00:08Z", nil, nil), makeRecord("a question", "accepted_decision", "1970-01-01T00:00:09Z", nil, nil), makeRecord("z question", "insufficient_evidence", "1970-01-01T00:00:09Z", "earlier", "later")}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("records = %#v, want %#v", got, want)
				}
			}
		})
	}
	// The schema admits any JSON array, so a null element is the stored
	// shape the reader itself must refuse rather than decode as "".
	setColumn := func(t *testing.T, column, value string) {
		t.Helper()
		tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_decision_records SET `+column+`=? WHERE work_id='decision-a' AND question='first question'`, value); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM write_guard`); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	for _, column := range []string{"options_considered", "consequences", "inputs"} {
		t.Run("malformed-"+column, func(t *testing.T) {
			var original string
			if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT `+column+` FROM workflow_decision_records WHERE work_id='decision-a' AND question='first question'`).Scan(&original); err != nil {
				t.Fatal(err)
			}
			setColumn(t, column, `["kept",null]`)
			_, err := s.QueryQ3(ctx, Q3Request{Product: "product", WorkIDs: []string{"decision-a"}, Detail: "full"})
			setColumn(t, column, original)
			if !hasFailureKind(err, KindInvariantViolation) {
				t.Fatalf("malformed stored array: err = %v, want invariant_violation", err)
			}
		})
	}
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
