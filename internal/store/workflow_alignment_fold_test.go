package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAlignmentFoldCombinations(t *testing.T) {
	folds := []struct {
		name string
		kind string
		fold func(context.Context, *sql.Tx, Event) error
	}{
		{"backlog", WorkflowBacklogAlignmentRecorded, foldWorkflowBacklogAlignmentRecorded},
		{"diagnosis", WorkflowDiagnosisRecorded, foldWorkflowDiagnosisRecorded},
	}
	cases := []struct {
		name, outcome string
		related       bool
		wantError     bool
	}{
		{"none_found_with_ids", "none_found", true, true},
		{"related_found_with_ids", "related_found", true, false},
		{"related_found_without_ids", "related_found", false, true},
	}
	for _, f := range folds {
		for _, c := range cases {
			t.Run(f.name+"/"+c.name, func(t *testing.T) {
				s := openTemp(t)
				seedWork(t, s, "work")
				seedWork(t, s, "related")
				version := readWorkVersion(t, s, "work")
				payload := map[string]any{
					"work_id": "work", "expected_version": version, "resulting_version": version + 1,
					"searched": "searched backlog", "outcome": c.outcome,
					"reproduction": "reproduced defect", "root_cause": "identified cause",
				}
				if c.related {
					payload["related_ids"] = []string{"related"}
				}
				// The standalone event has no diagnosis fields.
				if f.kind == WorkflowBacklogAlignmentRecorded {
					delete(payload, "reproduction")
					delete(payload, "root_cause")
				}
				encoded, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				ctx := context.Background()
				tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err := tx.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
					t.Fatal(err)
				}
				actor := DeriveWorkflowActorRef("principal", "client", "agent", "session")
				if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal','client','agent','session','agent','now')`, actor); err != nil {
					t.Fatal(err)
				}
				event := Event{Kind: f.kind, SubjectType: SubjectWorkItem, SubjectID: "work", Actor: actor, OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: encoded}
				err = f.fold(ctx, tx, event)
				if c.wantError && err == nil {
					t.Error("fold accepted invalid outcome and related_ids")
				}
				if !c.wantError && err != nil {
					t.Errorf("fold refused valid outcome and related_ids: %v", err)
				}
				var count int
				if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_backlog_alignment WHERE work_id=?`, "work").Scan(&count); err != nil {
					t.Fatal(err)
				}
				if c.wantError && count != 0 {
					t.Errorf("invalid fold wrote %d alignment rows, want 0", count)
				}
				if !c.wantError && count != 2 {
					t.Errorf("valid fold wrote %d alignment rows, want 2", count)
				}
			})
		}
	}
}

func TestAlignmentTeachingRule(t *testing.T) {
	rule := WorkflowContractTeachingRules().AlignmentCombinationRule
	if !strings.Contains(rule, "record_diagnosis") || strings.Contains(rule, "record_alignment") {
		t.Errorf("alignment teaching names the wrong action: %q", rule)
	}
}
