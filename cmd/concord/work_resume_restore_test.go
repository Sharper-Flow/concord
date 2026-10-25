package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestWorkResumeRestoresMissingWorktree(t *testing.T) {
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	t.Setenv("CONCORD_DB_PATH", dbPath)
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	origin, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(origin.Entry.Path, "committed.txt")
	if err := os.WriteFile(tracked, []byte("surviving committed work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOutput(t, origin.Entry.Path, "add", "committed.txt")
	gitOutput(t, origin.Entry.Path, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture work")
	tip := strings.TrimSpace(gitOutput(t, repo, "rev-parse", "refs/heads/"+origin.Entry.Branch))
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='domain_events'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("current-state journey must have no retired log: count=%d err=%v", count, err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	var manifest store.BackupManifest
	if err := json.Unmarshal(runCLIJSON(t, []string{"backup"}, map[string]any{"destination": snapshot}), &manifest); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restoredDB := filepath.Join(filepath.Dir(dbPath), "restored.db")
	var restoredReport store.RestoreReport
	if err := json.Unmarshal(runCLIJSON(t, []string{"restore"}, map[string]any{"source": snapshot, "destination": restoredDB}), &restoredReport); err != nil {
		t.Fatal(err)
	}
	if restoredReport.SourceSnapshot.SnapshotID != manifest.SnapshotID || restoredReport.RestoredDatabase.SchemaVersion != store.CurrentSchemaVersion() {
		t.Fatalf("restore did not preserve the current snapshot: %+v", restoredReport)
	}
	s = mustOpenStore(t, restoredDB)
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='domain_events'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("restore recreated retired history: count=%d err=%v", count, err)
	}
	// The database snapshot contains no uncommitted filesystem bytes.
	if err := os.WriteFile(tracked, []byte("unbacked dirty replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	untracked := filepath.Join(origin.Entry.Path, "unbacked.txt")
	if err := os.WriteFile(untracked, []byte("unbacked new file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(origin.Entry.Path); err != nil {
		t.Fatal(err)
	}
	code, output, stderr := resumeCLI(t, s, repo, origin.WorkID)
	if code != 0 {
		t.Fatalf("resume code=%d stderr=%q", code, stderr)
	}
	if got, err := os.ReadFile(tracked); err != nil || string(got) != "surviving committed work\n" {
		t.Fatalf("resume did not restore committed work: %q %v", got, err)
	}
	if _, err := os.Stat(untracked); !os.IsNotExist(err) {
		t.Fatalf("resume claimed recovery of unbacked dirty bytes: %v", err)
	}
	raw, err := json.Marshal(output.Worktree)
	if err != nil {
		t.Fatal(err)
	}
	var restored map[string]any
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	warning, _ := restored["restoration_warning"].(string)
	if restored["restored"] != true || restored["restored_tip"] != tip || !strings.Contains(strings.ToLower(warning), "uncommitted changes") {
		t.Fatalf("resume omitted restoration facts and loss warning: %s", raw)
	}
	entry, err := s.ResumeWorktreeLocation(context.Background(), "product-wl", "project-wl", origin.WorkID)
	if err != nil || entry.ClaimOpID != origin.Entry.ClaimOpID {
		t.Fatalf("restoration replaced the claim: %+v %v", entry, err)
	}
}
