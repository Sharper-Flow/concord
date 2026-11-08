package store

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/version"
)

func TestResearchRetirementUpgradePreservesVerifiedExportAndSnapshot(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, db := openResearchExportFixture(t)
	before, err := backupCurrentStateDigest(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := BackupDatabase(t.Context(), path, snapshot); err != nil {
		t.Fatal(err)
	}
	report, err := Upgrade(t.Context(), path, nil, UpgradeOptions{SnapshotPath: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(report.Applied, []int{183, 184, 185, 186, 187, 188, 189, 190, 191, 192, 193, 194, 195}) || report.ResearchExport != snapshot+".research" {
		t.Fatalf("upgrade = %+v", report)
	}
	if err := VerifyResearchExport(t.Context(), snapshot, report.ResearchExport); err != nil {
		t.Fatal(err)
	}
	for _, table := range researchExportTables {
		var exists bool
		if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name=?)`, table.name).Scan(&exists); err != nil || exists {
			t.Fatalf("table %s remains: %v", table.name, err)
		}
	}
	verified, err := VerifyBackup(t.Context(), snapshot)
	if err != nil || verified.currentStateDigest != before {
		t.Fatalf("snapshot recovery state = %s, want %s: %v", verified.currentStateDigest, before, err)
	}
	restored := filepath.Join(t.TempDir(), "restored.db")
	if _, err := RestoreBackup(t.Context(), snapshot, restored); err != nil {
		t.Fatalf("snapshot is not recoverable: %v", err)
	}
	if err := VerifyResearchExport(t.Context(), snapshot, restored+".research"); err != nil {
		t.Fatalf("restore did not preserve research: %v", err)
	}
}

func TestResearchRetirementUpgradeRefusesWithoutExport(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, db := openResearchExportFixture(t)
	before, err := backupCurrentStateDigest(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Upgrade(t.Context(), path, nil); err == nil || !strings.Contains(err.Error(), "verified snapshot and export") {
		t.Fatalf("unguarded drop = %v", err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := BackupDatabase(t.Context(), path, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshot+".research", []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Upgrade(t.Context(), path, nil, UpgradeOptions{SnapshotPath: snapshot}); err == nil {
		t.Fatal("failed export permitted drop")
	}
	after, err := backupCurrentStateDigest(t.Context(), db)
	if err != nil || before != after {
		t.Fatalf("refused upgrade changed source: %v", err)
	}
}

func TestResearchRetirementRejectsSourceDriftAndTamperedExport(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, db := openResearchExportFixture(t)
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := BackupDatabase(t.Context(), path, snapshot); err != nil {
		t.Fatal(err)
	}
	ctx, destination, err := prepareResearchRetirement(t.Context(), snapshot, snapshot+".research")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE active_research_revisions SET question='changed' WHERE pack_id='export' AND revision=1`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err == nil || !strings.Contains(err.Error(), "research rows changed") {
		t.Fatalf("source drift permitted drop: %v", err)
	}
	if _, err := db.Exec(`UPDATE active_research_revisions SET question='Question' WHERE pack_id='export' AND revision=1`); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(destination, entries[0].Name())); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err == nil || !strings.Contains(err.Error(), "research export file count") {
		t.Fatalf("tampered export permitted drop: %v", err)
	}
}

func TestResearchRetirementRefusesChangedSnapshot(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, db := openResearchExportFixture(t)
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := BackupDatabase(t.Context(), path, snapshot); err != nil {
		t.Fatal(err)
	}
	ctx, _, err := prepareResearchRetirement(t.Context(), snapshot, snapshot+".research")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshot, []byte("damaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err == nil || !strings.Contains(err.Error(), "snapshot changed") {
		t.Fatalf("damaged rollback snapshot permitted drop: %v", err)
	}
}

func TestResearchRetirementPreservesInitiativeLinkedRowsBeforeOwnerRemoval(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	for _, id := range []string{"owner", "consumer"} {
		t.Run(id, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.db")
			db := openMigratedTo(t, path, 181)
			seedResearchExportFixture(t, db)
			if _, err := db.Exec(`INSERT INTO write_guard VALUES(1); UPDATE work_items SET kind='initiative' WHERE id=?; DELETE FROM write_guard`, id); err != nil {
				t.Fatal(err)
			}
			snapshot := filepath.Join(t.TempDir(), "snapshot.db")
			if _, err := BackupDatabase(t.Context(), path, snapshot); err != nil {
				t.Fatal(err)
			}
			report, err := Upgrade(t.Context(), path, nil, UpgradeOptions{SnapshotPath: snapshot})
			if err != nil {
				t.Fatalf("initiative-linked research could not retire: %v", err)
			}
			if err := VerifyResearchExport(t.Context(), snapshot, report.ResearchExport); err != nil {
				t.Fatalf("initiative retirement lost research: %v", err)
			}
			var remaining int
			if err := db.QueryRow(`SELECT count(*) FROM work_items WHERE id=?`, id).Scan(&remaining); err != nil || remaining != 0 {
				t.Fatalf("initiative remains=%d: %v", remaining, err)
			}
		})
	}
}
