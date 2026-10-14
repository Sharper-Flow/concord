package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func recordedOccupancyFixture(t *testing.T, marker string) *Store {
	t.Helper()
	s, _, _ := worktreeFixture(t)
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
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return s
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

func appendRecordedOccupancyVacate(t *testing.T, s *Store, id, session string) Event {
	t.Helper()
	raw, err := json.Marshal(sessionVacatedPayload{WorkID: "work-w", ProjectID: "project-w", SessionRef: session, SourceDirectory: claimPath(s)})
	if err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: id, Kind: "work.session_vacated", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: session, OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: 1, Payload: raw}
	insertHistoricalEvent(t, s, event)
	return event
}

func TestRecordedOccupancyVacateUsesMarkerAndDistinctSessionBound(t *testing.T) {
	for _, marker := range []string{"absent", ""} {
		t.Run("marker-"+marker, func(t *testing.T) {
			s := recordedOccupancyFixture(t, marker)
			defer s.Close()
			first := appendRecordedOccupancyVacate(t, s, "unrecorded-vacate", "ses-unrecorded")
			if marker != "absent" {
				assertRecordedOccupancyRefusal(t, s, first)
				return
			}
			if err := RebuildFromLog(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			second := appendRecordedOccupancyVacate(t, s, "different-unrecorded-vacate", "ses-different")
			assertRecordedOccupancyRefusal(t, s, second)
		})
	}
}

func TestRecordedOccupancyUnreproducibleRowRefusesRebuildAndBackup(t *testing.T) {
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
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := Backup(context.Background(), s, snapshot); err == nil {
		t.Fatal("backup promoted unreproducible occupancy")
	}
	if _, err := os.Stat(snapshot); !os.IsNotExist(err) {
		t.Fatalf("refused backup left a snapshot: %v", err)
	}
}
