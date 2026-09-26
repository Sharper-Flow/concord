package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shippedV11290Migration104SQL is the migration 104 text release v11.29.0
// carried: its worktree_id CHECK capped the composed key at 128 characters
// while live stores reach 170 (domain 386), so the backfill aborted on real
// data and those stores pinned at schema 103. Its checksum must equal the
// shipped variant recorded for migration 104, or the fixture no longer
// reproduces the database class it stands for.
const shippedV11290Migration104SQL = `
CREATE TABLE worktree_occupancy (
    worktree_id           TEXT    NOT NULL,
    session_ref           TEXT    NOT NULL,
    recorded_at           TEXT    NOT NULL,
    host_pid              INTEGER,
    host_pid_start        INTEGER,
    has_process_identity  INTEGER NOT NULL CHECK(has_process_identity IN (0,1)),
    CHECK(length(worktree_id) BETWEEN 2 AND 128),
    CHECK(length(session_ref) BETWEEN 2 AND 128),
    CHECK(host_pid IS NULL OR host_pid > 0),
    CHECK(host_pid_start IS NULL OR host_pid_start >= 0),
    CHECK((has_process_identity = 1) OR (host_pid IS NULL AND host_pid_start IS NULL)),
    PRIMARY KEY (worktree_id, session_ref)
);

-- Copy every legacy occupant into the new table. has_process_identity stays 0
-- because no host pid was recorded: liveness never releases a legacy row, and
-- release routes are session_vacate or operator-approved removal only.
INSERT INTO worktree_occupancy (worktree_id, session_ref, recorded_at, host_pid, host_pid_start, has_process_identity)
SELECT
    e.set_id || ':' || e.project_id || ':' || e.claim_op_id,
    e.occupant_session_ref,
    COALESCE(e.verified_at, e.reclaimed_at, '1970-01-01T00:00:00Z'),
    NULL,
    NULL,
    0
FROM worktree_entries e
WHERE e.occupant_session_ref <> '';

CREATE INDEX worktree_occupancy_session ON worktree_occupancy (session_ref);
CREATE INDEX worktree_occupancy_process ON worktree_occupancy (has_process_identity, host_pid);

-- The table is fold-only: every read and write goes through the event log
-- and the fold handlers. Inserts and deletes fire only when fold_guard is
-- active and the row identity matches an event the fold just appended.
CREATE TRIGGER worktree_occupancy_guard_insert BEFORE INSERT ON worktree_occupancy FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'worktree_occupancy is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER worktree_occupancy_guard_update BEFORE UPDATE ON worktree_occupancy FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'worktree_occupancy is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER worktree_occupancy_guard_delete BEFORE DELETE ON worktree_occupancy FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'worktree_occupancy is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;

-- Drop the column this table replaces. The fold and read paths use the new
-- table; the legacy column held a single session and cannot admit a second.
ALTER TABLE worktree_entries DROP COLUMN occupant_session_ref;
`

func TestShippedMigration104TextMatchesVariantChecksum(t *testing.T) {
	t.Parallel()
	sum := sha256.Sum256([]byte(shippedV11290Migration104SQL))
	got := hex.EncodeToString(sum[:])
	variants := migrationShippedVariantChecksums[104]
	if len(variants) != 1 || variants[0] != got {
		t.Fatalf("shipped 104 fixture checksum %q, variant table records %v", got, variants)
	}
}

// liveShapeClaimOpID reproduces the longest claim_op_id shape on the operator
// store: a worktree-retarget operation id carrying a sha256 digest.
const liveShapeClaimOpID = "sha256:3a398c44bb4e536fd226ecc68124c95427cecf2ae8e39b7e3b3da7d4e8f6010e:worktree-retarget:work-d40734d2346c6407fa12e00c"

const (
	liveShapeSetID     = "wts:work-d40734d2346c6407fa12e00c"
	liveShapeProjectID = "pokeedge-backend"
	liveShapeWorkID    = "work-d40734d2346c6407fa12e00c"
	liveShapePath      = "/wt/live-shape"
)

func liveShapeWorktreeID() string {
	return liveShapeSetID + ":" + liveShapeProjectID + ":" + liveShapeClaimOpID
}

// migrateV103 seeds a schema-103 database with the manifest rows every real
// store of that generation recorded.
func migrateV103(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:103] {
		if err := applyMigration(context.Background(), db, migration); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(context.Background(), `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-09-25T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// occupancyBoundAccepts reports whether a fold-guarded insert of a
// keyLength-char worktree_id fits the table's CHECK.
func occupancyBoundAccepts(ctx context.Context, t *testing.T, db *sql.DB, keyLength int) bool {
	t.Helper()
	key := strings.Repeat("k", keyLength-4) + ":sep"
	if len(key) != keyLength {
		t.Fatalf("test key length %d, want %d", len(key), keyLength)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO worktree_occupancy(worktree_id,session_ref,recorded_at,host_pid,host_pid_start,has_process_identity) VALUES(?,?,?,?,NULL,0)`,
		key, fmt.Sprintf("ses-%d", keyLength), "2026-09-26T00:00:00Z", nil)
	if _, derr := db.ExecContext(ctx, `DELETE FROM fold_guard WHERE active = 1`); derr != nil {
		t.Fatal(derr)
	}
	return err == nil
}

func TestLiveShape170CharKeyMigrates104And105(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := migrateV103(t, filepath.Join(t.TempDir(), "concord-v103.db"))
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,reclaimed_at,git_facts,occupant_session_ref) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		liveShapeSetID, liveShapeProjectID, liveShapeClaimOpID, "work/a", "a1", liveShapePath, "/repo", "active", "2026-09-24T10:00:00Z", nil, "{}", "ses-live"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate a live-shaped v103 store: %v", err)
	}
	var applied int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version IN (104,105)`).Scan(&applied); err != nil || applied != 2 {
		t.Fatalf("migrations 104 and 105 applied=%d err=%v", applied, err)
	}
	var identity int
	if err := db.QueryRowContext(ctx, `SELECT has_process_identity FROM worktree_occupancy WHERE worktree_id=?`, liveShapeWorktreeID()).Scan(&identity); err != nil {
		t.Fatalf("the 170-char live-shape key is missing after migration: %v", err)
	}
	if identity != 0 {
		t.Fatalf("backfilled row has_process_identity=%d, want the legacy shape", identity)
	}
	if !occupancyBoundAccepts(ctx, t, db, 386) {
		t.Fatal("a 386-char worktree_id (the composed domain bound) was refused after migration 105")
	}
	if occupancyBoundAccepts(ctx, t, db, 513) {
		t.Fatal("a 513-char worktree_id was accepted; the CHECK bound must still exist")
	}
}

func TestShipped104StoreMigratesToWidened105(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := migrateV103(t, filepath.Join(t.TempDir(), "concord-shipped104.db"))
	t.Cleanup(func() { _ = db.Close() })
	// A database that applied the shipped 104 succeeded because its keys fit
	// the 128 bound, so seed that short-key shape before applying the text.
	if _, err := db.ExecContext(ctx, `INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,reclaimed_at,git_facts,occupant_session_ref) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		"set-short", "project-short", "claim-short", "work/b", "b1", "/wt/short", "/repo", "active", "2026-09-24T11:00:00Z", nil, "{}", "ses-shipped"); err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(ctx, db, migration{Version: 104, Name: "worktree_occupancy_table", SQL: shippedV11290Migration104SQL}); err != nil {
		t.Fatalf("apply the shipped 104 text: %v", err)
	}
	variants := migrationShippedVariantChecksums[104]
	if len(variants) != 1 {
		t.Fatal("no shipped variant recorded for 104")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, 104, "worktree_occupancy_table", variants[0], "2026-09-25T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate a shipped-104 store: %v", err)
	}
	if !occupancyBoundAccepts(ctx, t, db, 386) {
		t.Fatal("a 386-char worktree_id was refused after migration 105 widened a shipped-104 table")
	}
	var identity int
	if err := db.QueryRowContext(ctx, `SELECT has_process_identity FROM worktree_occupancy WHERE worktree_id='set-short:project-short:claim-short'`).Scan(&identity); err != nil || identity != 0 {
		t.Fatalf("legacy row survived the rebuild: identity=%d err=%v", identity, err)
	}
}

func TestRuntimeOccupancyInsertLongKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concord-runtime.db")
	db := migrateV103(t, path)
	if _, err := db.ExecContext(ctx, `INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,reclaimed_at,git_facts,occupant_session_ref) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		liveShapeSetID, liveShapeProjectID, liveShapeClaimOpID, "work/a", "a1", liveShapePath, "/repo", "active", "2026-09-24T10:00:00Z", nil, "{}", "ses-live"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open the migrated store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("product-live"), locatorProjectEvent(liveShapeProjectID), locatorMembershipEvent("product-live", liveShapeProjectID)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-live"): 0, VersionRef(SubjectProject, liveShapeProjectID): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: liveShapeWorkID + "-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: liveShapeWorkID, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: jsonRaw(`{"work_kind":"task","title":"Live shape","priority":1}`)},
		{EventID: liveShapeWorkID + "-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: liveShapeWorkID, Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"` + liveShapeProjectID + `","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, liveShapeWorkID): 0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{
		WorkID: liveShapeWorkID,
		// A session other than the backfilled legacy occupant, so the
		// landing records a fresh transfer instead of replaying.
		SessionRef:      "ses-second",
		LandedDirectory: liveShapePath,
		HostPID:         os.Getpid(),
		Now:             time.Unix(10, 0).UTC(),
	}); err != nil {
		t.Fatalf("landing for a 170-char worktree key: %v", err)
	}
	var hasIdentity int
	var hostPID int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT has_process_identity, COALESCE(host_pid,0) FROM worktree_occupancy WHERE worktree_id=? AND session_ref='ses-second'`, liveShapeWorktreeID()).Scan(&hasIdentity, &hostPID); err != nil {
		t.Fatalf("occupancy row for the long key: %v", err)
	}
	if hasIdentity != 1 || hostPID != int64(os.Getpid()) {
		t.Fatalf("occupancy row identity=%d pid=%d, want the landing's process identity", hasIdentity, hostPID)
	}
}
