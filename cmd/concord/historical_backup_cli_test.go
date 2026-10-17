package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/version"
)

func seedHistoricalBackupCLI(t *testing.T, path string, invalid bool) store.Event {
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
	if invalid {
		bad := failed
		bad.EventID = "historical-backup-invalid"
		bad.Payload = []byte(strings.Replace(string(failed.Payload), `"observed_session_directories":[]`, `"observed_session_directories":null`, 1))
		seedLegacyWorkerEventRows(t, path, bad)
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

func assertNoHistoricalSnapshot(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "backups"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("refused snapshot left files behind: %v", entries)
	}
}

func historicalRefusalSequence(t *testing.T, path string) int64 {
	t.Helper()
	db, err := sql.Open("sqlite", store.SQLiteFileURI(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var seq int64
	if err := db.QueryRow(`SELECT seq FROM domain_events WHERE event_id='historical-backup-invalid'`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return seq
}

func assertHistoricalRefusalAttribution(t *testing.T, path, diagnostic string) {
	t.Helper()
	for _, field := range []string{`event_id="historical-backup-invalid"`, fmt.Sprintf("sequence=%d ", historicalRefusalSequence(t, path)), `event_kind="worker.failed"`, "payload_version=1", `subject_type="work_item"`, `subject_id="work-1"`, `stage="decode"`} {
		if !strings.Contains(diagnostic, field) {
			t.Errorf("refusal %q does not report %s", diagnostic, field)
		}
	}
}

func TestHistoricalPayloadBackupRestoreViaCLI(t *testing.T) {
	path := freshMigratedCLIDatabase(t)
	failed := seedHistoricalBackupCLI(t, path, false)
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

func TestHistoricalPayloadRestoreRefusalAttribution(t *testing.T) {
	path := freshMigratedCLIDatabase(t)
	failed := seedHistoricalBackupCLI(t, path, false)
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	runCLIJSON(t, []string{"backup"}, map[string]any{"destination": snapshot})
	manifest, err := store.VerifyBackup(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	// Model a structurally verified snapshot whose writer accepted a payload
	// this binary cannot replay. The manifest inventories the fixture bytes.
	failed.EventID = "historical-backup-invalid"
	failed.Payload = []byte(strings.Replace(string(failed.Payload), `"observed_session_directories":[]`, `"observed_session_directories":null`, 1))
	seedLegacyWorkerEventRows(t, snapshot, failed)
	raw, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	manifest.DBChecksum = fmt.Sprintf("%x", sha256.Sum256(raw))
	manifest.EventWatermark = historicalRefusalSequence(t, snapshot)
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshot+".manifest.json", manifestRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.VerifyBackup(context.Background(), snapshot); err != nil {
		t.Fatalf("fixture must pass snapshot inventory verification: %v", err)
	}
	destination := filepath.Join(t.TempDir(), "restored.db")
	input, err := json.Marshal(map[string]string{"source": snapshot, "destination": destination})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"restore"}, bytes.NewReader(input), &out, &errOut); code != 1 {
		t.Fatalf("restore exit=%d, want 1; stderr=%s", code, errOut.String())
	}
	assertHistoricalRefusalAttribution(t, snapshot, errOut.String())
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("refused restore left a destination: %v", err)
	}
	after, err := os.ReadFile(snapshot)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatalf("refused restore changed source snapshot: %v", err)
	}
}

func TestHistoricalPayloadUpgradeSnapshotQualification(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		name := "replayable"
		if invalid {
			name = "unreplayable"
		}
		t.Run(name, func(t *testing.T) {
			previous := version.Value
			version.Value = "v-test"
			t.Cleanup(func() { version.Value = previous })
			path, root := cliStoreRoot(t)
			failed := seedHistoricalBackupCLI(t, path, invalid)
			pendingBreakingStore(t, path, true)
			before, err := store.PlanUpgradeReadiness(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			code, out, errOut := runUpgradeStdin(t, `{}`)
			after, err := store.PlanUpgradeReadiness(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			if invalid {
				if code != 1 || !strings.Contains(errOut, "requires the recorded host session observation") || !reflect.DeepEqual(before.AppliedVersions, after.AppliedVersions) {
					t.Fatalf("unreplayable rollback must prevent migration: code=%d error=%s before=%+v after=%+v", code, errOut, before, after)
				}
				assertNoHistoricalSnapshot(t, root)
				assertHistoricalRefusalAttribution(t, path, errOut)
			} else {
				if code != 0 || len(after.PendingBreaking) != 0 {
					t.Fatalf("replayable historical rollback must permit upgrade: code=%d error=%s after=%+v", code, errOut, after)
				}
				var report struct {
					Backup string `json:"backup"`
				}
				if err := json.Unmarshal([]byte(out), &report); err != nil || report.Backup == "" {
					t.Fatalf("upgrade backup report: %s: %v", out, err)
				}
				restored := filepath.Join(t.TempDir(), "rollback.db")
				rollback, err := store.RestoreBackup(context.Background(), report.Backup, restored)
				if err != nil || rollback.SourceSnapshot.SchemaVersion != before.SchemaVersion || rollback.RestoredDatabase.SchemaVersion != store.CurrentSchemaVersion() {
					t.Fatalf("pre-upgrade historical snapshot must restore: %+v: %v", rollback, err)
				}
				assertHistoricalBackupEvent(t, restored, failed)
			}
			assertHistoricalBackupEvent(t, path, failed)
		})
	}
}

func TestHistoricalPayloadRepairSnapshotQualification(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		name := "replayable"
		if invalid {
			name = "unreplayable"
		}
		t.Run(name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "installer-ran")
			fixture := newRepairFixture(t, "#!/bin/sh\ntouch \""+marker+"\"\nexit 0\n")
			path := filepath.Join(fixture.dataRoot, "store.db")
			failed := seedHistoricalBackupCLI(t, path, invalid)
			code, out, errOut := runRepairStdin(t, `{"artifact_dir":"`+filepath.ToSlash(fixture.artifactDir)+`"}`)
			_, markerErr := os.Stat(marker)
			if invalid {
				if code != 1 || !strings.Contains(errOut.String(), "requires the recorded host session observation") || !os.IsNotExist(markerErr) {
					t.Fatalf("unreplayable rollback must prevent installer execution: code=%d error=%s marker=%v", code, errOut, markerErr)
				}
				assertNoHistoricalSnapshot(t, fixture.dataRoot)
				assertHistoricalRefusalAttribution(t, path, errOut.String())
			} else if code != 0 || markerErr != nil || !strings.Contains(out.String(), "database backup at") {
				t.Fatalf("replayable historical rollback must permit repair: code=%d error=%s marker=%v", code, errOut, markerErr)
			}
			assertHistoricalBackupEvent(t, path, failed)
		})
	}
}
