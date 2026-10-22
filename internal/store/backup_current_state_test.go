package store

import (
	"context"
	"database/sql"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seedBackupCurrentState(t *testing.T, s *Store) {
	t.Helper()
	seedWork(t, s, "snapshot-active-work")
	_, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at)
		VALUES('snapshot-active-work','attempt:snapshot','implement',1,?,'engineering','','2.0','2.0','in_flight','2026-10-22T00:00:00Z');
		DELETE FROM fold_guard`, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,first_observed_at,last_observed_at)
		VALUES('principal:snapshot','snapshot-tool','test','snapshot-key','digest','snapshot-op','2026-10-22T00:00:00Z','2026-10-22T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
}

func TestBackupRestorePreservesMaterializedCurrentState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	seedBackupCurrentState(t, s)
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := Backup(ctx, s, snapshot); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored.db")
	if _, err := RestoreBackup(ctx, snapshot, destination); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(driverName, readOnlyDataSource(destination))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, query := range []string{
		`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id='attempt:snapshot'`,
		`SELECT canonical_digest FROM idempotency_records WHERE idempotency_key='snapshot-key'`,
	} {
		if got, want := captureEvidenceRows(t, db, query), captureEvidenceRows(t, s.db, query); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("restored current rows = %v, want %v (%s)", got, want, query)
		}
	}
}

func TestRestoreStageRejectsCurrentStateTampering(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, mutation string }{
		{"active attempt lost", `INSERT INTO fold_guard VALUES(1); DELETE FROM worker_attempts; DELETE FROM fold_guard`},
		{"active attempt changed", `INSERT INTO fold_guard VALUES(1); UPDATE worker_attempts SET dispatched_at='2026-10-23T00:00:00Z'; DELETE FROM fold_guard`},
		{"direct authority lost", `DELETE FROM idempotency_records`},
		{"direct authority changed", `UPDATE idempotency_records SET canonical_digest='tampered'`},
		{"schema changed", `DROP INDEX worker_attempts_work`},
		{"new current table", `CREATE TABLE snapshot_unexpected(value TEXT)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTemp(t)
			defer s.Close()
			seedBackupCurrentState(t, s)
			snapshot := filepath.Join(t.TempDir(), "snapshot.db")
			manifest, err := Backup(ctx, s, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			stage := filepath.Join(t.TempDir(), "stage.db")
			if err := runOnlineRestore(ctx, snapshot, stage); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open(driverName, dataSourceName(stage))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(tc.mutation); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := verifyRestoredStage(ctx, stage, manifest); err == nil {
				t.Fatal("restore verifier accepted changed current state")
			} else {
				assertFailureKind(t, err, KindInvariantViolation)
			}
		})
	}
}

func TestBackupOccupancyPreservesCurrentRowsAndRefusesLoss(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, git, _ := worktreeFixture(t)
	defer s.Close()
	if _, err := s.ClaimWorktree(ctx, baseClaim(git)); err != nil {
		t.Fatal(err)
	}
	worktreeID := worktreeOccupancyID(WorktreeSetID("work-w"), "project-w", "wt-op-1")
	// Current occupancy is authority even without an event that recreates it.
	if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1);
INSERT INTO worktree_occupancy(worktree_id,session_ref,recorded_at,has_process_identity) VALUES(?,'session:current-occupant','2026-10-22T00:00:00Z',0);
DELETE FROM fold_guard`, worktreeID); err != nil {
		t.Fatal(err)
	}
	query := `SELECT * FROM worktree_occupancy WHERE session_ref='session:current-occupant'`
	want := captureEvidenceRows(t, s.db, query)
	if len(want) != 1 {
		t.Fatalf("fixture must carry the current occupant: %v", want)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	manifest, err := Backup(ctx, s, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(t.TempDir(), "stage.db")
	if err := runOnlineRestore(ctx, snapshot, stage); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(driverName, dataSourceName(stage))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO fold_guard VALUES(1); DELETE FROM worktree_occupancy; DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyRestoredStage(ctx, stage, manifest); err == nil {
		t.Fatal("current-state verification accepted lost occupancy")
	} else {
		assertFailureKind(t, err, KindInvariantViolation)
	}
	destination := filepath.Join(t.TempDir(), "restored.db")
	if _, err := RestoreBackup(ctx, snapshot, destination); err != nil {
		t.Fatal(err)
	}
	restored, err := sql.Open(driverName, readOnlyDataSource(destination))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	for _, current := range []*sql.DB{s.db, restored} {
		if got := captureEvidenceRows(t, current, query); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("backup/restore changed current occupancy: got %v want %v", got, want)
		}
	}
}

func TestBackupCurrentStateDigestIgnoresRowOrderNotValues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	if _, err := s.db.Exec(`CREATE TABLE "current ""state"(value COLLATE NOCASE)`); err != nil {
		t.Fatal(err)
	}
	values := []any{nil, "a", "A", "a\x00b", "a\x00c", []byte{}, []byte{0, 255}, int64(1), float64(1), math.Inf(1), "duplicate", "duplicate"}
	replaceRows := func(order []int) {
		t.Helper()
		if _, err := s.db.Exec(`DELETE FROM "current ""state"`); err != nil {
			t.Fatal(err)
		}
		for _, i := range order {
			if _, err := s.db.Exec(`INSERT INTO "current ""state" VALUES(?)`, values[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	digest := func() string {
		t.Helper()
		got, err := backupCurrentStateDigest(ctx, s.db)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	rng := rand.New(rand.NewPCG(1, 2))
	replaceRows(rng.Perm(len(values)))
	want := digest()
	for range 20 {
		replaceRows(rng.Perm(len(values)))
		if got := digest(); got != want {
			t.Fatalf("insertion order changed current-state digest: %s != %s", got, want)
		}
	}
	for i := range values {
		order := rng.Perm(len(values))
		for j, index := range order {
			if index == i {
				order = append(order[:j], order[j+1:]...)
				break
			}
		}
		replaceRows(order)
		if digest() == want {
			t.Fatalf("removing value %T:%v did not change digest", values[i], values[i])
		}
	}
}

func TestBackupRestoreDoesNotRequireHistoryTable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	seedBackupCurrentState(t, s)
	if _, err := s.db.Exec(`DROP TABLE domain_events`); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := Backup(ctx, s, snapshot); err != nil {
		t.Fatalf("backup without history: %v", err)
	}
	if _, err := VerifyBackup(ctx, snapshot); err != nil {
		t.Fatalf("verify without history: %v", err)
	}
	destination := filepath.Join(t.TempDir(), "restored.db")
	if _, err := RestoreBackup(ctx, snapshot, destination); err != nil {
		t.Fatalf("restore without history: %v", err)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(driverName, readOnlyDataSource(destination))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	want, err := backupCurrentStateDigest(ctx, s.db)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := backupCurrentStateDigest(ctx, db); err != nil || got != want {
		t.Fatalf("history-free restored state = %q err=%v, want %q", got, err, want)
	}
}

func TestBackupEventWatermarkIsInventoryOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	seedBackupCurrentState(t, s)
	var want int64
	if err := s.db.QueryRow(`SELECT MAX(seq) FROM domain_events`).Scan(&want); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	manifest, err := Backup(ctx, s, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.EventWatermark != want || manifest.SourceEventMaxSeq != want || want == 0 {
		t.Fatalf("backup event inventory = %d/%d, want %d", manifest.EventWatermark, manifest.SourceEventMaxSeq, want)
	}
	manifest.EventWatermark = want + 100
	if err := writeBackupManifest(snapshot, manifest); err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyBackup(ctx, snapshot)
	if err != nil || verified.EventWatermark != want {
		t.Fatalf("verified inventory = %d err=%v, want recomputed %d", verified.EventWatermark, err, want)
	}
	stage := filepath.Join(t.TempDir(), "stage.db")
	if err := runOnlineRestore(ctx, snapshot, stage); err != nil {
		t.Fatal(err)
	}
	got, err := verifyRestoredStage(ctx, stage, manifest)
	if err != nil || got.EventWatermark != want {
		t.Fatalf("stage inventory = %d err=%v, want %d without comparing metadata", got.EventWatermark, err, want)
	}
}
