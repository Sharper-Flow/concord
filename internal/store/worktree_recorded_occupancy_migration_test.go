package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRecordedOccupancyMigration89BackfillAndReleasedRow(t *testing.T) {
	useStampedBuild(t)
	ctx := context.Background()
	events := []Event{
		locatorProductEvent("product-w"), locatorProjectEvent("project-w"), locatorMembershipEvent("product-w", "project-w"),
		workCreatedEvent("work-w", "migration-create-work"),
		operationEvent("migration-work-project", "work_project.added", SubjectWorkItem, "work-w", map[string]any{"work_id": "work-w", "project_id": "project-w", "role": "primary", "reason": "migration fixture", "expected_version": 1, "resulting_version": 2}),
		operationEvent("migration-worktree", "work.worktree_created", SubjectWorkItem, "work-w", map[string]any{"set_id": WorktreeSetID("work-w"), "project_id": "project-w", "claim_op_id": "wt-op-1", "branch": "work/work-w", "base_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "path": t.TempDir(), "repository_id": "repo-fixture", "git_facts": map[string]any{}, "expected_version": 2, "resulting_version": 3}),
	}
	path := filepath.Join(t.TempDir(), "migration-occupancy.db")
	db := openMigratedTo(t, path, 88)
	s := &Store{db: db, path: path}
	defer s.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	scope, err := beginFold(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "project.locator_added" {
			event.PayloadVersion = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`, event.EventID, event.Kind, event.SubjectType, event.SubjectID, event.Actor, event.OccurredAt.Format(time.RFC3339Nano), event.PayloadVersion, string(event.Payload)); err != nil {
			t.Fatal(err)
		}
		if event.Kind == "project.locator_added" {
			// Seed the recorded writer's projection, before its generation column existed.
			p, err := decodeProjectLocator(event)
			if err != nil {
				t.Fatal(err)
			}
			stamp := event.OccurredAt.Format(time.RFC3339Nano)
			if _, err := tx.ExecContext(ctx, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, p.LocatorID, p.ProjectID, p.Kind, p.Value, p.NormalizedValue, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if err := bumpVersion(ctx, tx, "projects", event, p.ExpectedVersion, p.ResultingVersion, "Project"); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if event.Kind != "work.worktree_created" {
			if event.Kind == "work.created" {
				seedPreQ7WorkCreated(t, tx, event)
				continue
			}
			if err := foldRegisteredEvent(ctx, tx, event); err != nil {
				t.Fatal(err)
			}
			continue
		}
		var p worktreeCreatedPayload
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			t.Fatal(err)
		}
		// The scalar-era writer created an empty occupant, with no member in
		// its retained payload. Migration 89, not replay, supplies the backfill.
		if _, err := tx.ExecContext(ctx, `INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts,occupant_session_ref) VALUES(?,?,?,?,?,?,?,'active',?,?,'')`, p.SetID, p.ProjectID, p.ClaimOpID, p.Branch, p.BaseSHA, p.Path, p.RepositoryID, event.OccurredAt.Format(time.RFC3339Nano), string(p.GitFacts)); err != nil {
			t.Fatal(err)
		}
		if err := bumpVersion(ctx, tx, "work_items", event, p.ExpectedVersion, p.ResultingVersion, "work item"); err != nil {
			t.Fatal(err)
		}
	}
	const session = "ses-unrecorded"
	actor := DeriveWorkflowActorRef("principal:m89", "client:m89", "agent:m89", session)
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,'agent',?)`, actor, "principal:m89", "client:m89", "agent:m89", session, time.Unix(11, 0).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES('migration-driver','work.transitioned','work_item','work-w',?,?,1,?)`, actor, time.Unix(11, 0).UTC().Format(time.RFC3339Nano), `{"from":"needed","to":"in_progress","reason":"Synthetic migration driver","expected_version":3,"resulting_version":4}`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE work_items SET lifecycle='in_progress',version=4 WHERE id='work-w'`); err != nil {
		t.Fatal(err)
	}
	if err := scope.close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	migration := migrations[88]
	if migration.Version != 89 {
		t.Fatal("fixture must apply the occupancy backfill")
	}
	if err := applyMigration(ctx, db, migration); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), time.Unix(12, 0).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	var backfilled string
	if err := db.QueryRowContext(ctx, `SELECT occupant_session_ref FROM worktree_entries WHERE claim_op_id='wt-op-1'`).Scan(&backfilled); err != nil || backfilled != session {
		t.Fatalf("migration 89 did not backfill the synthetic session: %q: %v", backfilled, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1); UPDATE worktree_entries SET occupant_session_ref='' WHERE claim_op_id='wt-op-1' AND occupant_session_ref=?; DELETE FROM fold_guard`, session); err != nil {
		t.Fatal(err)
	}
	before := histEventRows(t, s)
	if err := migrateRestoredStage(ctx, db); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM worktree_occupancy`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration restored a released scalar occupant: rows=%d: %v", count, err)
	}
	if !reflect.DeepEqual(before, histEventRows(t, s)) {
		t.Fatal("migration rewrote retained event bytes")
	}
}
