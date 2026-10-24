package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// The verify lease carries the event-order position at which its run started
// (the integration settlement): the highest committed event sequence of this
// work item, recorded by the acquire INSERT in its own transaction. The
// attribution reader treats a NULL watermark as an unavailable population, so
// leases recorded before the column keep NULL and the migration must not
// backfill them.

// TestVerifyLeaseWatermarkMigrationIsAdditive pins the declaration bits: a
// nullable column an older binary never names is non-breaking, and the
// acquire INSERT writes the value once per lease with first write winning
// (the origin class migration 125 declared on this table).
func TestVerifyLeaseWatermarkMigrationIsAdditive(t *testing.T) {
	t.Parallel()
	found := false
	for _, m := range migrations {
		if m.Name != "verify_lease_event_watermark" {
			continue
		}
		found = true
		if m.Breaking || m.FoldMaintained != "origin" {
			t.Fatalf("watermark migration must stay additive with an origin declaration: breaking=%v fold=%q", m.Breaking, m.FoldMaintained)
		}
	}
	if !found {
		t.Fatal("verify_lease_event_watermark migration is missing")
	}
}

// TestVerifyLeaseWatermarkMigrationKeepsOldLeasesNull proves the forward
// migration from the previous version: the column arrives nullable and a
// lease recorded before it keeps its NULL watermark.
func TestVerifyLeaseWatermarkMigrationKeepsOldLeasesNull(t *testing.T) {
	t.Parallel()
	index := -1
	for i, m := range migrations {
		if m.Name == "verify_lease_event_watermark" {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("verify_lease_event_watermark migration is missing")
	}
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "watermark.db"), index)
	ctx := context.Background()
	if present, err := columnPresent(ctx, db, "worktree_verify_leases", "event_watermark"); err != nil || present {
		t.Fatalf("fixture already has watermark before its forward migration: present=%v err=%v", present, err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('work-wm','task','Watermark','in_progress',1,1,'2026-10-14T00:00:00Z','2026-10-14T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,outcome) VALUES('lease-old','work-wm','project-wm','/repo/wt-old','released','client-old','agent-old','session-old','principal-old','["true"]','2026-10-14T00:00:00Z','completed')`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("forward migration: %v", err)
	}

	var nullable int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('worktree_verify_leases') WHERE name='event_watermark' AND "notnull"=0`).Scan(&nullable); err != nil || nullable != 1 {
		t.Fatalf("event_watermark nullable column absent: count=%d err=%v", nullable, err)
	}
	var watermark sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT event_watermark FROM worktree_verify_leases WHERE lease_id='lease-old'`).Scan(&watermark); err != nil {
		t.Fatal(err)
	}
	if watermark.Valid {
		t.Fatalf("pre-migration lease carries watermark %d, want NULL", watermark.Int64)
	}
}

// TestAcquireVerifyLeaseRecordsWorkEventWatermark proves the acquire INSERT
// snapshots this work item's highest committed event sequence in its own
// transaction, and that an event committed after the acquire never moves the
// recorded value.
func TestAcquireVerifyLeaseRecordsWorkEventWatermark(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	entry := claimFixtureWorktree(t, s, git)
	ctx := context.Background()

	workEvents := `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type='work_item' AND subject_id='work-w'`
	var before int64
	if err := s.db.QueryRow(workEvents).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before == 0 {
		t.Fatal("the fixture recorded no work item events; the watermark test cannot run")
	}

	req := verifyRequest(git, "lease-watermark", []string{"true"}, nil)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := acquireVerifyLeaseTx(ctx, tx, req, entry, workflowJSON(req.Command), req.Now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var watermark sql.NullInt64
	if err := s.db.QueryRow(`SELECT event_watermark FROM worktree_verify_leases WHERE lease_id=?`, req.LeaseID).Scan(&watermark); err != nil {
		t.Fatal(err)
	}
	if !watermark.Valid || watermark.Int64 != before {
		t.Fatalf("lease watermark=%v, want the acquire-time seq %d", watermark, before)
	}

	// A membership event committed after the acquire sits above the
	// watermark and must not move the recorded value.
	var version int64
	if err := s.db.QueryRow(`SELECT version FROM work_items WHERE id='work-w'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	payload := `{"memberships":[{"project_id":"project-w","role":"primary"}],"expected_version":` + strconv.FormatInt(version, 10) + `,"resulting_version":` + strconv.FormatInt(version+1, 10) + `}`
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{
		EventID: "work-wm-after-acquire", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "work-w",
		Actor: "operator", OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion("work.memberships_replaced"),
		Payload: jsonRaw(payload),
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-w"): version}}); err != nil {
		t.Fatal(err)
	}

	var after int64
	if err := s.db.QueryRow(workEvents).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT event_watermark FROM worktree_verify_leases WHERE lease_id=?`, req.LeaseID).Scan(&watermark); err != nil {
		t.Fatal(err)
	}
	if after <= before {
		t.Fatalf("the later event did not advance the work log: before=%d after=%d", before, after)
	}
	if !watermark.Valid || watermark.Int64 != before {
		t.Fatalf("watermark moved to %v, want the acquire-time seq %d", watermark, before)
	}
}
