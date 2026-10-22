package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func recordedOccupancyFixture(t *testing.T, marker string) *Store {
	t.Helper()
	s, _, _ := worktreeFixture(t)
	insertRecordedOccupancyCreation(t, s, marker)
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return s
}

// insertRecordedOccupancyCreation appends the fixture's historical creation
// event without folding it, so later release events fold in the same replay
// pass that creates the marked occupant row.
func insertRecordedOccupancyCreation(t *testing.T, s *Store, marker string) {
	t.Helper()
	creation := map[string]any{
		"expected_version": 2, "resulting_version": 3,
		"set_id": WorktreeSetID("work-w"), "project_id": "project-w", "claim_op_id": "wt-op-1",
		"branch": claimBranch(), "base_sha": strings.Repeat("a", 40), "path": claimPath(s),
		"repository_id": "repo-fixture", "git_facts": map[string]any{},
	}
	if marker != "absent" {
		creation["occupant_session_ref"] = marker
	}
	raw, err := json.Marshal(creation)
	if err != nil {
		t.Fatal(err)
	}
	insertHistoricalEvent(t, s, Event{
		EventID: "recorded-occupancy-creation", Kind: "work.worktree_created", SubjectType: SubjectWorkItem,
		SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(10, 0).UTC(), PayloadVersion: 1, Payload: raw,
	})
}

// Raw historical fixtures must carry the committed occupancy effect before
// recovery compares their projections with the replay output. This seed is
// independent of replay and requires an existing matching destination row.
func seedRecordedLandingOccupancy(t *testing.T, s *Store, event Event) {
	t.Helper()
	var p legacySessionClaimLandedPayload
	if err := json.Unmarshal(event.Payload, &p); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	scope, err := beginFold(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	var claim string
	if err := tx.QueryRowContext(ctx, `SELECT claim_op_id FROM worktree_entries WHERE set_id=? AND project_id=? AND path=?`, WorktreeSetID(p.WorkID), p.ProjectID, p.LandedDirectory).Scan(&claim); err != nil {
		t.Fatal(err)
	}
	destination := worktreeOccupancyID(WorktreeSetID(p.WorkID), p.ProjectID, claim)
	result, err := tx.ExecContext(ctx, `UPDATE worktree_occupancy SET recorded_at=? WHERE worktree_id=? AND session_ref=? AND has_process_identity=0`, event.OccurredAt.Format(time.RFC3339Nano), destination, p.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("fixture needs a recorded destination occupant: rows=%d err=%v", n, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM worktree_occupancy WHERE session_ref=? AND worktree_id<>?`, p.SessionRef, destination); err != nil {
		t.Fatal(err)
	}
	if err := scope.close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func appendRecordedOccupancyRelease(t *testing.T, s *Store, id, session string, scalar, incarnation bool) Event {
	t.Helper()
	payload := map[string]any{"set_id": WorktreeSetID("work-w"), "project_id": "project-w", "claim_op_id": "wt-op-1"}
	key := "session_ref"
	if scalar {
		key = "occupant_session_ref"
	}
	payload[key] = session
	if incarnation {
		payload["claim_incarnation"] = 1
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: id, Kind: "work.worktree_occupancy_released", SubjectType: SubjectWorkItem,
		SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: 1, Payload: raw}
	insertHistoricalEvent(t, s, event)
	return event
}

func assertRecordedOccupancyRefusal(t *testing.T, s *Store, event Event) {
	t.Helper()
	before, events := histProjectionRows(t, s.db), histEventRows(t, s)
	err := RebuildFromLog(context.Background(), s)
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || failure.EventID != event.EventID || failure.Sequence == 0 || failure.Stage != "fold" {
		t.Fatalf("expected attributed fold refusal, got %v", err)
	}
	if !reflect.DeepEqual(before, histProjectionRows(t, s.db)) || !reflect.DeepEqual(events, histEventRows(t, s)) {
		t.Fatal("refused occupancy replay changed projections or retained events")
	}
}

// recordedOccupancySessionRefs reads the surviving occupancy sessions in a
// stable order.
func recordedOccupancySessionRefs(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT session_ref FROM worktree_occupancy ORDER BY session_ref`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			t.Fatal(err)
		}
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// assertRecordedOccupancyReplayConverged replays the retained log twice,
// requires the exact surviving occupancy sessions and byte-identical
// retained events, and requires backup and restore to agree on the same
// occupancy and retained-byte result.
func assertRecordedOccupancyReplayConverged(t *testing.T, s *Store, wantSessions []string) {
	t.Helper()
	before := histEventRows(t, s)
	for range 2 {
		if err := RebuildFromLog(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		if got := recordedOccupancySessionRefs(t, s.db); !reflect.DeepEqual(got, wantSessions) {
			t.Fatalf("replay occupancy=%v, want %v", got, wantSessions)
		}
		if !reflect.DeepEqual(before, histEventRows(t, s)) {
			t.Fatal("replay changed retained bytes")
		}
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := Backup(context.Background(), s, snapshot); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored.db")
	if _, err := RestoreBackup(context.Background(), snapshot, restored); err != nil {
		t.Fatal(err)
	}
	restoredDB, err := sql.Open(driverName, dataSourceName(restored))
	if err != nil {
		t.Fatal(err)
	}
	defer restoredDB.Close()
	restoredStore := &Store{db: restoredDB}
	if got := recordedOccupancySessionRefs(t, restoredDB); !reflect.DeepEqual(got, wantSessions) {
		t.Fatalf("restored occupancy=%v, want %v", got, wantSessions)
	}
	if got := histEventRows(t, restoredStore); !reflect.DeepEqual(before, got) {
		t.Fatal("restored retained events diverge from the original log")
	}
}

func TestRecordedOccupancyMissingMarkerAdmitsBothReleaseSchemas(t *testing.T) {
	for _, scalar := range []bool{true, false} {
		for _, incarnation := range []bool{false, true} {
			t.Run(fmt.Sprintf("scalar-%t-incarnation-%t", scalar, incarnation), func(t *testing.T) {
				s := recordedOccupancyFixture(t, "absent")
				defer s.Close()
				appendRecordedOccupancyRelease(t, s, "unrecorded-release", "ses-unrecorded", scalar, incarnation)
				before := histEventRows(t, s)
				for range 2 {
					if err := RebuildFromLog(context.Background(), s); err != nil {
						t.Fatal(err)
					}
					var count int
					if err := s.db.QueryRow(`SELECT count(*) FROM worktree_occupancy`).Scan(&count); err != nil || count != 0 {
						t.Fatalf("replay invented occupancy: %d: %v", count, err)
					}
					if !reflect.DeepEqual(before, histEventRows(t, s)) {
						t.Fatal("replay changed retained bytes")
					}
				}
				snapshot := filepath.Join(t.TempDir(), "snapshot.db")
				if _, err := Backup(context.Background(), s, snapshot); err != nil {
					t.Fatal(err)
				}
				if _, err := RestoreBackup(context.Background(), snapshot, filepath.Join(t.TempDir(), "restored.db")); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// recordedVacatePlannedMain is the synthetic planned registered main
// checkout the released v1 vacate writers recorded in their destination and
// landed members. It is the planned path, never landing evidence, and no
// fixture reads a live host path through it.
const recordedVacatePlannedMain = "/workspace/main"

func appendRecordedOccupancyVacate(t *testing.T, s *Store, id, session string) Event {
	t.Helper()
	raw, err := json.Marshal(sessionVacatedPayload{
		WorkID: "work-w", ProjectID: "project-w", SessionRef: session, SourceDirectory: claimPath(s),
		DestinationDirectory: recordedVacatePlannedMain, LandedDirectory: recordedVacatePlannedMain,
	})
	if err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: id, Kind: "work.session_vacated", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: session, OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: 1, Payload: raw}
	insertHistoricalEvent(t, s, event)
	return event
}

// A recorded v1 vacate is evidence its release committed: the first vacate
// against an explicit empty marker member and the second vacate by a
// distinct session against an empty recorded source each replay their own
// released-target effects.
func TestRecordedOccupancyVacateAdmitsReleasedEmptyTargets(t *testing.T) {
	for _, marker := range []string{"absent", ""} {
		t.Run("marker-"+marker, func(t *testing.T) {
			s := recordedOccupancyFixture(t, marker)
			defer s.Close()
			appendRecordedOccupancyVacate(t, s, "unrecorded-vacate", "ses-unrecorded")
			if err := RebuildFromLog(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			appendRecordedOccupancyVacate(t, s, "different-unrecorded-vacate", "ses-different")
			assertRecordedOccupancyReplayConverged(t, s, nil)
		})
	}
}

func TestRecordedOccupancyUnreproducibleRowRefusesRebuild(t *testing.T) {
	s := recordedOccupancyFixture(t, "")
	defer s.Close()
	worktreeID := worktreeOccupancyID(WorktreeSetID("work-w"), "project-w", "wt-op-1")
	if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO worktree_occupancy(worktree_id,session_ref,recorded_at,has_process_identity) VALUES(?,?,'synthetic',0); DELETE FROM fold_guard`, worktreeID, "ses-unlogged"); err != nil {
		t.Fatal(err)
	}
	before, events := histProjectionRows(t, s.db), histEventRows(t, s)
	err := RebuildFromLog(context.Background(), s)
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || failure.Kind != KindProjectionConflict || !strings.Contains(failure.Detail, worktreeID) || !strings.Contains(failure.Detail, "ses-unlogged") {
		t.Fatalf("missing typed occupancy evidence refusal: %v", err)
	}
	if !reflect.DeepEqual(before, histProjectionRows(t, s.db)) || !reflect.DeepEqual(events, histEventRows(t, s)) {
		t.Fatal("unreproducible occupancy refusal changed the store")
	}
}

// Two distinct unknown sessions vacate the same empty recorded source in one
// replay pass. Each six-member v1 vacate is evidence its own release
// committed, so neither consumes a lifetime allowance the other needs.
func TestRecordedOccupancyVacateAdmitsTwoDistinctUnknownSessions(t *testing.T) {
	s := recordedOccupancyFixture(t, "absent")
	defer s.Close()
	appendRecordedOccupancyVacate(t, s, "first-unknown-vacate", "ses-unrecorded")
	appendRecordedOccupancyVacate(t, s, "second-unknown-vacate", "ses-different")
	assertRecordedOccupancyReplayConverged(t, s, nil)
}

// A creation's occupant row was already released before these historical
// releases, so its creation marker no longer describes an occupant: the
// scalar and the row-set release of the empty recorded source each replay
// their own committed effect. Creation and releases fold in one replay pass,
// the way the released history was written.
func TestRecordedOccupancyReleasedMarkerRowAdmitsScalarAndRowSet(t *testing.T) {
	for _, scalar := range []bool{true, false} {
		t.Run(fmt.Sprintf("scalar-%t", scalar), func(t *testing.T) {
			s, _, _ := worktreeFixture(t)
			defer s.Close()
			insertRecordedOccupancyCreation(t, s, "ses-recorded")
			appendRecordedOccupancyRelease(t, s, "recorded-occupant-release", "ses-recorded", false, false)
			appendRecordedOccupancyRelease(t, s, "released-marker-release", "ses-unrecorded", scalar, false)
			assertRecordedOccupancyReplayConverged(t, s, nil)
		})
	}
}

// A v1 vacate released the session's rows on fold. The first recorded
// row-set release after it names a row the vacate already deleted, and the
// committed release event is its own evidence, not a duplicate release.
func TestRecordedOccupancyFirstRowSetReleaseAfterVacateAdmits(t *testing.T) {
	s := recordedOccupancyFixture(t, "absent")
	defer s.Close()
	appendRecordedOccupancyVacate(t, s, "first-vacate", "ses-unrecorded")
	appendRecordedOccupancyRelease(t, s, "first-row-set-after-vacate", "ses-unrecorded", false, false)
	assertRecordedOccupancyReplayConverged(t, s, nil)
}
