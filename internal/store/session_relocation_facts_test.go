package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

func inRelocationTx(t *testing.T, s *Store, fn func(*Transaction, *sql.Tx) error) error {
	t.Helper()
	return s.Transact(t.Context(), func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "relocation_test")
		if err != nil {
			return err
		}
		return fn(transaction, tx)
	})
}

func TestSessionClaimLandingReadersIgnoreTheLog(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	bindSessionToProjectWorktree(t, f.store, f.targetTree, f.targetSession)
	var firstClaim string
	if err := f.store.db.QueryRow(`SELECT claim_op_id FROM worktree_entries WHERE path=? AND state='active'`, filepath.Clean(f.targetTree)).Scan(&firstClaim); err != nil {
		t.Fatal(err)
	}
	terminalCleanupSQL(t, f.store, `DELETE FROM worktree_occupancy`)
	replaceHostWorktreeGeneration(t, f.store, f.targetTree, "relocation-op-2")
	assertEventLogAbsent(t, f.store)
	if err := inRelocationTx(t, f.store, func(_ *Transaction, tx *sql.Tx) error {
		placement, err := verifiedSessionPlacementTx(t.Context(), tx, f.workID, f.targetSession)
		if err != nil || placement != "" {
			t.Fatalf("placement=%q err=%v, want no placement before the new landing", placement, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	bindSessionToProjectWorktree(t, f.store, f.targetTree, f.targetSession)
	assertEventLogAbsent(t, f.store)
	if err := inRelocationTx(t, f.store, func(_ *Transaction, tx *sql.Tx) error {
		placement, err := verifiedSessionPlacementTx(t.Context(), tx, f.workID, f.targetSession)
		if err != nil || placement != f.targetProject {
			t.Fatalf("placement=%q err=%v, want %s", placement, err, f.targetProject)
		}
		for _, claim := range []string{firstClaim, "relocation-op-2"} {
			var count int
			err := tx.QueryRowContext(t.Context(), `SELECT count(*) FROM session_claim_landings WHERE work_id=? AND session_ref=? AND claim_op_id=?`, f.workID, f.targetSession, claim).Scan(&count)
			if err != nil || count != 1 {
				t.Fatalf("claim %s landing rows=%d err=%v", claim, count, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func relocationFactRows(t *testing.T, s *Store) []string {
	t.Helper()
	var out []string
	for _, query := range []string{
		`SELECT 'v|'||work_id||'|'||session_ref||'|'||project_id||'|'||source_directory||'|'||destination_directory||'|'||request_seq||'|'||landed FROM session_vacate_requests ORDER BY work_id, session_ref, project_id, source_directory`,
		`SELECT 'c|'||work_id||'|'||session_ref||'|'||project_id||'|'||claim_op_id FROM session_claim_landings ORDER BY work_id, session_ref, project_id, claim_op_id`,
	} {
		rows, err := s.db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			out = append(out, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestSessionRelocationBackfillPreservesRecordedFacts(t *testing.T) {
	t.Parallel()
	request := func(id, work string) Event {
		e := corpusEvent(id, "work.session_vacated", work, map[string]any{"work_id": work, "project_id": "project-w", "session_ref": "ses-x", "source_directory": "/data/tree", "destination_directory": "/data/main"})
		e.PayloadVersion = 2
		return e
	}
	landing := func(id, work string) Event {
		return corpusEvent(id, "work.session_vacate_landed", work, map[string]any{"work_id": work, "project_id": "project-w", "session_ref": "ses-x", "source_directories": []string{"/data/tree"}, "destination_directory": "/data/main", "landed_directory": "/data/main", "host_pid": 7, "host_pid_start": 9})
	}
	claim := corpusEvent("claim-landed", "work.session_claim_landed", "work-w", map[string]any{"work_id": "work-w", "project_id": "project-w", "claim_op_id": "claim-1", "session_ref": "ses-x", "source_directories": []string{}, "landed_directory": "/data/tree", "host_pid": 7, "host_pid_start": 9})
	claim.PayloadVersion = 2
	oldRequest := request("excluded-v1", "work-w")
	oldRequest.PayloadVersion = 1
	unbound := corpusEvent("excluded-unbound", "work.session_claim_landed", "work-w", map[string]any{"work_id": "work-w", "project_id": "project-w", "session_ref": "ses-old", "landed_directory": "/data/tree"})
	c := historicalFactCorpus{events: []Event{claim, request("request-1", "work-w"), landing("landing-1", "work-w"), request("request-2", "work-w"), request("other-request", "work-other"), landing("other-landing", "work-other"), oldRequest, unbound}, parents: func(*testing.T, *Store, string) {}}
	s := c.prefix(t, 140)
	before := histEventRows(t, s)
	want := []string{"v|work-other|ses-x|project-w|/data/tree|/data/main|5|1", "v|work-w|ses-x|project-w|/data/tree|/data/main|4|0", "c|work-w|ses-x|project-w|claim-1"}
	advanceHistoricalPrefix(t, s.db, 141)
	if got := relocationFactRows(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("migration141 facts=%v, want %v", got, want)
	}
	if got := histEventRows(t, s); !reflect.DeepEqual(got, before) {
		t.Fatal("migration changed historical source bytes or coordinates")
	}
	if err := Migrate(context.Background(), s.db); err != nil {
		t.Fatal(err)
	}
	assertHistoricalReceipts(t, s.db, before)
	assertEventLogAbsent(t, s)
	if got := relocationFactRows(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("retained facts=%v, want %v", got, want)
	}
	queries := []string{`SELECT json_array(work_id,session_ref,project_id,source_directory,destination_directory,landed) FROM session_vacate_requests ORDER BY work_id,session_ref,project_id,source_directory`, `SELECT json_array(work_id,session_ref,project_id,claim_op_id) FROM session_claim_landings ORDER BY work_id,session_ref,project_id,claim_op_id`}
	assertCorpusSelectionRed(t, c, 141, "SELECT max(o.seq)", "SELECT min(o.seq)", queries, workerFactsQueryRows(t, s, queries))
}
