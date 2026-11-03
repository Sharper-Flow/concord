package store

import (
	"context"
	"reflect"
	"testing"
)

func TestWorkRemovalClearsBacklogAlignmentAtBothEnds(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct{ name, removed, survivor string }{
		{"searching item", "align-searcher", "align-related"},
		{"related item", "align-related", "align-searcher"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			seedWork(t, s, "align-searcher")
			seedWork(t, s, "align-related")
			seedBacklogAlignmentRows(t, s, "align-searcher", "align-related")
			before := retirementUpgradeRows(t, s.DatabaseForTesting(), `SELECT searched,outcome,recorded_at,recorded_by FROM workflow_backlog_alignment WHERE work_id='align-searcher' AND related_work_id IS NULL`)
			request := removalTestRequest()
			request.WorkID = tc.removed
			request.OperationID = "remove-align-" + tc.removed
			request.IdempotencyKey = "remove-align-key-" + tc.removed
			request.Reason = "shelved"
			if err := removeWorkForTest(ctx, s, request); err != nil {
				t.Fatalf("remove %s: %v", tc.removed, err)
			}
			var rows int
			if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM workflow_backlog_alignment WHERE work_id=? OR related_work_id=?`, tc.removed, tc.removed).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if rows != 0 {
				t.Fatalf("removing %s left %d alignment rows", tc.removed, rows)
			}
			var survivors int
			if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM work_items WHERE id=?`, tc.survivor).Scan(&survivors); err != nil {
				t.Fatal(err)
			}
			if survivors != 1 {
				t.Fatalf("removal dropped survivor %s", tc.survivor)
			}
			alignment, err := readWorkflowBacklogAlignment(ctx, s.DatabaseForTesting(), tc.survivor)
			if err != nil {
				t.Fatal(err)
			}
			if tc.removed == "align-related" && (alignment == nil || alignment.Outcome != "related_found" || len(alignment.RelatedIDs) != 0 || !alignment.AllMatchesRemoved) {
				t.Fatalf("surviving alignment does not mark removed matches: %+v", alignment)
			}
			if tc.removed == "align-related" {
				after := retirementUpgradeRows(t, s.DatabaseForTesting(), `SELECT searched,outcome,recorded_at,recorded_by FROM workflow_backlog_alignment WHERE work_id='align-searcher' AND related_work_id IS NULL`)
				if !reflect.DeepEqual(before, after) || alignment.Searched != "Searched open work for items covering the same surface." {
					t.Fatalf("declaration changed: before=%v after=%v read=%+v", before, after, alignment)
				}
			}
			if tc.removed == "align-searcher" && alignment != nil {
				t.Fatalf("unrecorded alignment is present: %+v", alignment)
			}
		})
	}
}

func TestBacklogAlignmentPartialRemoval(t *testing.T) {
	s := openTemp(t)
	for _, id := range []string{"align-searcher", "align-related", "align-keep"} {
		seedWork(t, s, id)
	}
	seedBacklogAlignmentRows(t, s, "align-searcher", "align-related", "align-keep")
	before := retirementUpgradeRows(t, s.DatabaseForTesting(), `SELECT * FROM workflow_backlog_alignment WHERE related_work_id IS NULL OR related_work_id='align-keep' ORDER BY related_work_id`)
	request := removalTestRequest()
	request.WorkID = "align-related"
	if err := removeWorkForTest(t.Context(), s, request); err != nil {
		t.Fatal(err)
	}
	after := retirementUpgradeRows(t, s.DatabaseForTesting(), `SELECT * FROM workflow_backlog_alignment ORDER BY related_work_id`)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("surviving rows changed: before=%v after=%v", before, after)
	}
	alignment, err := readWorkflowBacklogAlignment(t.Context(), s.DatabaseForTesting(), "align-searcher")
	if err != nil || alignment == nil || alignment.Outcome != "related_found" || alignment.AllMatchesRemoved || !reflect.DeepEqual(alignment.RelatedIDs, []string{"align-keep"}) {
		t.Fatalf("partial removal alignment=%+v err=%v", alignment, err)
	}
}

func seedBacklogAlignmentRows(t *testing.T, s *Store, workID string, relatedIDs ...string) {
	t.Helper()
	ctx := context.Background()
	const actorRef = "actor:0000000000000000000000000000000000000000000000000000000000000001"
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterWrite(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,'agent','2026-09-19T00:00:00Z')`, actorRef, "principal:alignment-removal", "client:alignment-removal", "agent:alignment-removal", "session:alignment-removal"); err != nil {
		t.Fatal(err)
	}
	searched := "Searched open work for items covering the same surface."
	relatedRows := []any{nil}
	for _, id := range relatedIDs {
		relatedRows = append(relatedRows, id)
	}
	for _, related := range relatedRows {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_backlog_alignment(work_id,related_work_id,searched,outcome,recorded_at,recorded_by) VALUES(?,?,?,'related_found','2026-09-19T00:00:00Z',?)`, workID, related, searched, actorRef); err != nil {
			t.Fatal(err)
		}
	}
	if err := leaveWrite(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
