package store

import (
	"context"
	"strings"
	"testing"
)

func TestWorkKindPoliciesAndDatabaseRegistry(t *testing.T) {
	s := openTemp(t)
	db := s.DatabaseForTesting()
	ctx := context.Background()
	want := map[string][4]int{
		"task":       {1, 1, 1, 1},
		"bug":        {1, 1, 1, 1},
		"decision":   {1, 1, 1, 1},
		"research":   {1, 1, 1, 1},
		"other":      {1, 1, 1, 1},
		"initiative": {1, 1, 0, 0},
		"epic":       {0, 0, 0, 0},
	}
	rows, err := db.QueryContext(ctx, `SELECT kind,stored,fold_create,fold_revise,agent_capture FROM work_kinds`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var values [4]int
		if err := rows.Scan(&kind, &values[0], &values[1], &values[2], &values[3]); err != nil {
			t.Fatal(err)
		}
		if values != want[kind] {
			t.Fatalf("registry row %q = %v, want %v", kind, values, want[kind])
		}
		delete(want, kind)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(want) != 0 {
		t.Fatalf("missing registry rows: %v", want)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, `DELETE FROM fold_guard`)
	insert := `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES(?,?,?,?,1,1,?,?,NULL)`
	if _, err := db.ExecContext(ctx, insert, "stored-bug", "bug", "Bug", "needed", "now", "now"); err != nil {
		t.Fatalf("stored bug rejected: %v", err)
	}
	for _, kind := range []string{"problem", "epic"} {
		if _, err := db.ExecContext(ctx, insert, "invalid-"+kind, kind, kind, "needed", "now", "now"); err == nil || !strings.Contains(err.Error(), "work_items kind is not a stored work kind") {
			t.Fatalf("undeclared or retired kind %q error = %v", kind, err)
		}
	}
}

func TestWorkKindFoldPoliciesRejectRetiredAndAllowInitiative(t *testing.T) {
	if !WorkKindFoldCreateAllowed("initiative") || WorkKindFoldReviseAllowed("initiative") {
		t.Fatal("initiative policy does not distinguish create from revise")
	}
	if WorkKindFoldCreateAllowed("epic") || WorkKindStored("epic") {
		t.Fatal("epic policy permits storage")
	}
}

func TestVocabularyRegistriesAreImmutable(t *testing.T) {
	db := openTemp(t).DatabaseForTesting()
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		query   string
		message string
	}{
		{"insert work kind", `INSERT INTO work_kinds(kind,stored,fold_create,fold_revise,agent_capture) VALUES('new',1,1,1,1)`, "work_kinds registry is immutable"},
		{"update work kind", `UPDATE work_kinds SET stored=0 WHERE kind='task'`, "work_kinds registry is immutable"},
		{"delete work kind", `DELETE FROM work_kinds WHERE kind='task'`, "work_kinds registry is immutable"},
		{"insert native status", `INSERT INTO workflow_native_run_statuses(phase,status,failure) VALUES('start','new',0)`, "workflow_native_run_statuses registry is immutable"},
		{"update native status", `UPDATE workflow_native_run_statuses SET failure=1 WHERE phase='start' AND status='started'`, "workflow_native_run_statuses registry is immutable"},
		{"delete native status", `DELETE FROM workflow_native_run_statuses WHERE phase='start' AND status='started'`, "workflow_native_run_statuses registry is immutable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.ExecContext(ctx, tc.query); err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("registry mutation error = %v, want %q", err, tc.message)
			}
		})
	}
}

func TestReconstructionScratchWorkUsesStoredOperatorKind(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := insertScratchWork(ctx, tx, "scratch-endpoint"); err != nil {
		t.Fatalf("insert reconstruction scratch work: %v", err)
	}
	var kind string
	if err := tx.QueryRowContext(ctx, `SELECT kind FROM work_items WHERE id='scratch-endpoint'`).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "task" {
		t.Fatalf("scratch work kind = %q, want task", kind)
	}
}
