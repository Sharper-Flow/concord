package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestRestoreFromVerifiedBackupWhileLiveStoreRefusesStrandedGuard(t *testing.T) {
	path := freshMigratedCLIDatabase(t)
	ctx := context.Background()
	s, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	manifest, err := store.Backup(ctx, s, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	refused, err := store.Open(ctx, path)
	if err == nil {
		_ = refused.Close()
		t.Fatal("ordinary Open admitted the stranded guard")
	}
	var failure *store.Failure
	if !errors.As(err, &failure) || failure.Kind != store.KindInvariantViolation {
		t.Fatalf("Open refusal=%v, want invariant_violation", err)
	}

	destination := filepath.Join(t.TempDir(), "restored.db")
	raw, err := json.Marshal(map[string]string{"source": snapshot, "destination": destination})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"restore"}, bytes.NewReader(raw), &out, &errOut); code != 0 {
		t.Fatalf("restore must not open the refused live store: exit=%d stderr=%s", code, &errOut)
	}
	var report store.RestoreReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("restore report=%s: %v", &out, err)
	}
	if report.SourceSnapshot.SnapshotID != manifest.SnapshotID || report.SourceSnapshot.EventWatermark != manifest.EventWatermark || report.RestoredDatabase.EventWatermark != manifest.EventWatermark || report.RestoredDatabase.SchemaVersion != store.CurrentSchemaVersion() {
		t.Fatalf("restore report=%+v, want snapshot %+v at current schema", report, manifest)
	}
	restored, err := store.Open(ctx, destination)
	if err != nil {
		t.Fatalf("restored current state must open: %v", err)
	}
	defer restored.Close()
	if got, err := restored.Revision(ctx); err != nil || got != manifest.EventWatermark {
		t.Fatalf("restored revision=%d err=%v, want %d", got, err, manifest.EventWatermark)
	}

	out.Reset()
	errOut.Reset()
	raw, err = json.Marshal(map[string]string{"source": snapshot, "destination": path})
	if err != nil {
		t.Fatal(err)
	}
	if code := runWithInput([]string{"restore"}, bytes.NewReader(raw), &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "live database path") || out.Len() != 0 {
		t.Fatalf("live destination refusal: exit=%d stdout=%s stderr=%s", code, &out, &errOut)
	}
	var guards int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM fold_guard`).Scan(&guards); err != nil || guards != 1 {
		t.Fatalf("restore changed the refused live guard: rows=%d err=%v", guards, err)
	}
}
