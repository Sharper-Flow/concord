package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestProjectRepositoryModeOperations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedSchemaEvolutionBase(t, s)
	previous := assertRepositoryMode(t, s, "schema-project", "off", 1)

	for i, mode := range []string{"on", "off", "on"} {
		version := int64(i + 1)
		result, err := s.SetProjectRepositoryMode(ctx, ProjectRepositoryModeChange{ProjectID: "schema-project", RepositoryMode: mode, ExpectedVersion: version})
		if err != nil || len(result.EventIDs) != 1 {
			t.Fatalf("set %s: result=%+v err=%v", mode, result, err)
		}
		updated := assertRepositoryMode(t, s, "schema-project", mode, version+1)
		if updated == previous {
			t.Fatalf("updated_at did not change on set %s: %s", mode, updated)
		}
		previous = updated
		var kind, actor string
		if err := s.db.QueryRow(`SELECT kind, actor FROM effect_receipts WHERE event_id=?`, result.EventIDs[0]).Scan(&kind, &actor); err != nil || kind != "project.repository_mode_set" || actor != "operator" {
			t.Fatalf("receipt kind=%q actor=%q err=%v", kind, actor, err)
		}
	}
	q, err := s.QueryQ1(ctx, Q1Request{Product: "schema-product"})
	if err != nil || len(q.Projects) != 1 || q.Projects[0].RepositoryMode != "on" {
		t.Fatalf("membership read=%+v err=%v", q.Projects, err)
	}
	projects, err := s.ProjectsForProduct(ctx, "schema-product")
	if err != nil || len(projects) != 1 || projects[0].RepositoryMode != "on" {
		t.Fatalf("ProjectsForProduct=%+v err=%v", projects, err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{workCreatedEvent("mode-work", "mode-work-create"), operationEvent("mode-work-membership", "work_project.added", SubjectWorkItem, "mode-work", map[string]any{"work_id": "mode-work", "project_id": "schema-project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "mode-work"): 0}}); err != nil {
		t.Fatal(err)
	}
	projects, err = s.ProjectsForWork(ctx, "mode-work")
	if err != nil || len(projects) != 1 || projects[0].RepositoryMode != "on" {
		t.Fatalf("ProjectsForWork=%+v err=%v", projects, err)
	}
	work, err := s.QueryQ3(ctx, Q3Request{Product: "schema-product", WorkIDs: []string{"mode-work"}})
	if err != nil || len(work.Items) != 1 || len(work.Items[0].Projects) != 1 || work.Items[0].Projects[0].RepositoryMode != "on" {
		t.Fatalf("work membership read=%+v err=%v", work.Items, err)
	}
	_, err = s.SetProjectRepositoryMode(ctx, ProjectRepositoryModeChange{ProjectID: "schema-project", RepositoryMode: "bad", ExpectedVersion: 4})
	assertFailureKind(t, err, KindInvalidOperation)
	_, err = s.SetProjectRepositoryMode(ctx, ProjectRepositoryModeChange{ProjectID: "schema-project", RepositoryMode: "off", ExpectedVersion: 3})
	assertFailureKind(t, err, KindVersionConflict)
	invalid := operationEvent("invalid-mode", "project.repository_mode_set", SubjectProject, "schema-project", map[string]any{"repository_mode": "bad"})
	err = ApplyOperation(ctx, s, Operation{Events: []Event{invalid}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "schema-project"): 4}})
	assertFailureKind(t, err, KindInvalidPayload)
	rename := operationEvent("mode-rename", "project.renamed", SubjectProject, "schema-project", map[string]any{"display_name": "Renamed"})
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{rename}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "schema-project"): 4}}); err != nil {
		t.Fatal(err)
	}
	assertRepositoryMode(t, s, "schema-project", "on", 5)
}

func TestProjectRepositoryModeSQLDefault(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('raw','Raw',1,'t','t')`); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := tx.QueryRow(`SELECT repository_mode FROM projects WHERE id='raw'`).Scan(&mode); err != nil || mode != "off" {
		t.Fatalf("default=%q err=%v", mode, err)
	}
	if _, err := tx.Exec(`UPDATE projects SET repository_mode='invalid' WHERE id='raw'`); err == nil {
		t.Fatal("invalid mode passed SQL constraint")
	}
}

func TestProjectRepositoryModeBackupUpgrade(t *testing.T) {
	useStampedBuild(t)
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "source.db")
	db := openMigratedTo(t, source, migrationPrefixBefore(t, "project_repository_mode"))
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES (1); INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product','Product','prototype','operator_only',1,'t','t'); INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('old','Old',1,'t','t'); INSERT INTO product_projects(product_id,project_id,role) VALUES('product','old','primary'); DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(root, "snapshot.db")
	manifest, err := BackupDatabase(ctx, source, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 190 {
		t.Fatalf("snapshot schema=%d", manifest.SchemaVersion)
	}
	destination := filepath.Join(root, "restored.db")
	report, err := RestoreBackup(ctx, snapshot, destination)
	if err != nil {
		t.Fatal(err)
	}
	if report.RestoredDatabase.SchemaVersion != CurrentSchemaVersion() {
		t.Fatalf("restored schema=%d", report.RestoredDatabase.SchemaVersion)
	}
	restored, err := Open(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	assertRepositoryMode(t, restored, "old", "off", 1)
}

func assertRepositoryMode(t *testing.T, s *Store, id, want string, wantVersion int64) string {
	t.Helper()
	var got, updated string
	var version int64
	if err := s.db.QueryRow(`SELECT repository_mode,version,updated_at FROM projects WHERE id=?`, id).Scan(&got, &version, &updated); err != nil {
		t.Fatal(err)
	}
	if got != want || version != wantVersion {
		t.Fatalf("mode=%q version=%d, want %q version=%d", got, version, want, wantVersion)
	}
	return updated
}
