package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func seedHistoricalBackupCLI(t *testing.T, path string) store.Event {
	t.Helper()
	ctx := context.Background()
	seedCLIProduct(t, path, "product-1", "project-1")
	s, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	work := []store.Event{
		{EventID: "historical-backup-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: []byte(`{"work_kind":"task","title":"Historical backup","priority":1}`)},
		{EventID: "historical-backup-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: []byte(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: work, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-1"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	lane := store.BuiltinLaneDefinitions()[0]
	failed := store.Event{
		EventID: "historical-backup-abandoned", Kind: store.WorkerFailed, SubjectType: store.SubjectWorkItem, SubjectID: "work-1",
		Actor: "host:fixture", OccurredAt: legacyWorkerEvidenceSeededTime.Add(time.Second), PayloadVersion: 1,
		Payload: []byte(`{"attempt_id":"attempt-1","readback_model":"","failure_kind":"abandoned","detail":"session ended without a report","observed_session_directories":[]}`),
	}
	seedLegacyWorkerEventRows(t, path, legacyWorkerDispatchEvent(t, "historical-backup-dispatch", lane, "", 3), failed)
	s, err = store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return failed
}

func assertHistoricalBackupEvent(t *testing.T, path string, want store.Event) {
	t.Helper()
	version, payload, actor := storedWorkerEventRow(t, path, want.EventID)
	if version != want.PayloadVersion || string(payload) != string(want.Payload) || actor != want.Actor {
		t.Fatalf("historical event changed: version=%d payload=%s actor=%q", version, payload, actor)
	}
}

func TestHistoricalPayloadBackupRestoreViaCLI(t *testing.T) {
	path := freshMigratedCLIDatabase(t)
	failed := seedHistoricalBackupCLI(t, path)
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	runCLIJSON(t, []string{"backup"}, map[string]any{"destination": snapshot})
	destination := filepath.Join(t.TempDir(), "restored.db")
	raw := runCLIJSON(t, []string{"restore"}, map[string]any{"source": snapshot, "destination": destination})
	var report store.RestoreReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.SourceSnapshot.SnapshotID == "" || report.SourceSnapshot.EventWatermark == 0 || report.SourceSnapshot.IntegrityCheck != "ok" || report.RestoredDatabase.SchemaVersion != store.CurrentSchemaVersion() || report.RestoredDatabase.DBChecksum == "" || report.RestoredDatabase.EventWatermark != report.SourceSnapshot.EventWatermark {
		t.Fatalf("restore did not distinguish verified source and destination inventories: %s", raw)
	}
	assertHistoricalBackupEvent(t, path, failed)
	assertHistoricalBackupEvent(t, destination, failed)
}
