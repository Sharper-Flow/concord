package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupOlderSchemaWithAcceptedImplementationJob(t *testing.T) {
	useStampedBuild(t)
	ctx := t.Context()
	root := t.TempDir()
	source := filepath.Join(root, "source.db")
	db := openMigratedTo(t, source, 135)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO fold_guard VALUES(1)`)
	exec(`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('work-backup','task','Backup witness','needed',0,1,?,?)`, retirementUpgradeTime, retirementUpgradeTime)
	exec(`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project-backup','Backup project',1,?,?)`, retirementUpgradeTime, retirementUpgradeTime)
	exec(`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product-backup','Backup product','prototype','operator_only',1,?,?)`, retirementUpgradeTime, retirementUpgradeTime)
	exec(`INSERT INTO product_projects VALUES('product-backup','project-backup','primary')`)
	exec(`INSERT INTO work_projects VALUES('work-backup','project-backup','primary')`)
	job := map[string]any{"job_id": "job:backup", "revision": 1, "project_scope": "project-backup"}
	digest := "sha256:" + strings.Repeat("d", 64)
	exec(`INSERT INTO worker_job_revisions(work_id,job_id,revision,contract_version,objective,stopping_condition,project_scope,path_scope,predicate_ids,checks,prerequisites,unresolved_refs,digest,state,recorded_at) VALUES('work-backup',?,1,1,'Preserve candidate','Candidate restored',?,'[]','[]','[]','[]','[]',?,'recorded',?)`, job["job_id"], job["project_scope"], digest, retirementUpgradeTime)
	delivery := map[string]any{"state": "clean", "commit": strings.Repeat("a", 40), "tree": strings.Repeat("b", 40)}
	events := []struct {
		kind string
		body any
	}{
		{"worker.job_recorded", job},
		{WorkflowActionCompleted, map[string]any{"step_id": "execution", "action_id": "dispatch_worker", "worker_attempt_id": "attempt:backup", "worker_capability_class": "implementation", "worker_job": map[string]any{"job_id": job["job_id"], "revision": 1, "digest": digest}}},
		{"worker.completed", map[string]any{"attempt_id": "attempt:backup", "observed_delivery": delivery}},
		{WorkflowActionCompleted, map[string]any{"step_id": "execution", "action_id": "accept_worker_result", "worker_attempt_id": "attempt:backup"}},
	}
	for i, event := range events {
		exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,'work_item','work-backup','operator',?,1,?)`, fmt.Sprintf("backup-event-%d", i), event.kind, retirementUpgradeTime, string(mustJSONValue(event.body)))
	}
	exec(`DELETE FROM fold_guard`)
	before, err := backupCurrentStateDigest(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(root, "snapshot.db")
	manifest, err := BackupDatabase(ctx, source, snapshot)
	if err != nil {
		t.Fatalf("backup with accepted implementation job: %v", err)
	}
	if manifest.SchemaVersion != 135 || manifest.currentStateDigest != before {
		t.Fatalf("snapshot changed source state: %+v", manifest)
	}
	restored := filepath.Join(root, "restored.db")
	if _, err := RestoreBackup(ctx, snapshot, restored); err != nil {
		t.Fatal(err)
	}
	restoredDB, err := sql.Open(driverName, readOnlyDataSource(restored))
	if err != nil {
		t.Fatal(err)
	}
	defer restoredDB.Close()
	state, err := readWorkflowReviewFacts(ctx, restoredDB, "work-backup")
	want := ReviewCandidate{ProjectID: "project-backup", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
	if err != nil || state.Candidate == nil || *state.Candidate != want {
		t.Fatalf("restored candidate = %+v, %v; want %+v", state, err, want)
	}
	if got := manifestMax(t, source); got != 135 {
		t.Fatalf("backup migrated live source to %d", got)
	}
	sourceDB, err := sql.Open(driverName, readOnlyDataSource(source))
	if err != nil {
		t.Fatal(err)
	}
	defer sourceDB.Close()
	if after, err := backupCurrentStateDigest(ctx, sourceDB); err != nil || after != before {
		t.Fatalf("backup changed live source state: %s, %v; want %s", after, err, before)
	}
	verified, err := VerifyBackup(ctx, snapshot)
	if err != nil || verified.DBChecksum != manifest.DBChecksum {
		t.Fatalf("snapshot changed after restore: %+v, %v", verified, err)
	}
}
