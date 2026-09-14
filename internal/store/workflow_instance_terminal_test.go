package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// A work item that reaches a terminal lifecycle closes its workflow instance
// in the same fold. Cancelled and superseded mirror the lifecycle. An item
// completed on external evidence never ran its completion gate, so its
// instance records the abandoned workflow as cancelled while the item's
// lifecycle carries the outcome.
func TestTerminalLifecycleClosesTheWorkflowInstance(t *testing.T) {
	cases := []struct {
		name          string
		terminal      func(t *testing.T, s *Store, workID string)
		instanceState string
	}{
		{name: "cancelled", instanceState: "cancelled", terminal: func(t *testing.T, s *Store, workID string) {
			if err := applyWorkEvent(t, s, workTransitionEvent(workID+"-cancel", workID, "needed", "cancelled", 3, 4), workVersion(workID, 3)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "completed on external evidence", instanceState: "cancelled", terminal: func(t *testing.T, s *Store, workID string) {
			if err := applyWorkEvent(t, s, workTransitionEvent(workID+"-complete", workID, "needed", "completed", 3, 4), workVersion(workID, 3)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "superseded", instanceState: "superseded", terminal: func(t *testing.T, s *Store, workID string) {
			seedWork(t, s, workID+"-successor")
			event := workSupersededEvent(workID+"-supersede", workID+"-successor", workID, 3, 4)
			if err := applyWorkEvent(t, s, event, map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 3}); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			workID := "terminal-" + tc.name
			s := openTemp(t)
			seedWork(t, s, workID)
			seedWorkflowLaw(t, s)
			registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
			if err != nil {
				t.Fatal(err)
			}
			selected := workflowEvent(workID+"-definition", WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "ref": registered.Definition.Ref, "version": registered.Definition.Version, "digest": registered.Digest, "work_kind": string(registered.Definition.WorkKind)})
			if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{selected}, ExpectedVersions: workVersion(workID, 2)}); err != nil {
				t.Fatal(err)
			}
			var before string
			if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if before != "planned" {
				t.Fatalf("instance_state before the terminal fold = %q, want planned", before)
			}

			tc.terminal(t, s, workID)

			var lifecycle, state string
			var completedAt *string
			if err := s.DatabaseForTesting().QueryRow(`SELECT w.lifecycle, i.instance_state, i.completed_at FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&lifecycle, &state, &completedAt); err != nil {
				t.Fatal(err)
			}
			if !isTerminalLifecycle(lifecycle) {
				t.Fatalf("lifecycle = %q, want a terminal lifecycle", lifecycle)
			}
			if state != tc.instanceState {
				t.Errorf("instance_state = %q, want %q after the item reached %q", state, tc.instanceState, lifecycle)
			}
			if completedAt == nil || *completedAt == "" {
				t.Errorf("completed_at is empty; the terminal fold must stamp the instance")
			}
		})
	}
}

// An instance that already completed its workflow keeps that record when the
// item's lifecycle reaches a terminal state afterwards. The terminal fold
// closes only a live instance.
func TestTerminalLifecycleLeavesACompletedInstanceAlone(t *testing.T) {
	workID := "completed-then-lifecycle"
	s, completion := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}, includeSpec: true, includeVerdict: true, includePremise: true, verdictKind: "ok"})
	if err := CompleteWorkflow(context.Background(), s, completion); err != nil {
		t.Fatalf("workflow completion refused: %v", err)
	}
	var version int64
	var completedAt string
	if err := s.DatabaseForTesting().QueryRow(`SELECT w.version, i.completed_at FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&version, &completedAt); err != nil {
		t.Fatal(err)
	}
	if err := applyWorkEvent(t, s, workTransitionEvent(workID+"-cancel", workID, "in_progress", "cancelled", version, version+1), workVersion(workID, version)); err != nil {
		t.Fatal(err)
	}
	var state, after string
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state, completed_at FROM workflow_instances WHERE work_id=?`, workID).Scan(&state, &after); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || after != completedAt {
		t.Errorf("instance = (%q, %q), want the completed record (%q, %q) untouched", state, after, "completed", completedAt)
	}
}

// Migration 74 closes the instances that earlier terminal folds left live,
// with the state the fold now records and the item's terminal time as the
// stamp. A log rebuild reproduces the same row, so the repaired projection
// and the replayed projection agree.
func TestMigrationClosesInstancesOfTerminalWorkItems(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concord-orphans.db")
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	// The backfill under test is version 75; later migrations are irrelevant
	// to it, so the seeded database stops just before 75.
	for _, migration := range migrations {
		if migration.Version >= 75 {
			break
		}
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-09-06T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	s := &Store{db: db, path: path}
	if err := ensureInstallationKey(ctx, db); err != nil {
		t.Fatal(err)
	}
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}

	orphans := map[string]string{"orphan-cancelled": "cancelled", "orphan-completed": "completed", "orphan-superseded": "superseded"}
	for workID, lifecycle := range orphans {
		_, version := startWorkflowPinnedTo(t, s, workID, definition)
		switch lifecycle {
		case "superseded":
			seedWork(t, s, workID+"-successor")
			if err := applyWorkEvent(t, s, workSupersededEvent(workID+"-supersede", workID+"-successor", workID, version, version+1), workVersion(workID, version)); err != nil {
				t.Fatal(err)
			}
		default:
			if err := applyWorkEvent(t, s, workTransitionEvent(workID+"-end", workID, "needed", lifecycle, version, version+1), workVersion(workID, version)); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Reproduce the state an earlier binary left: the item is terminal and
	// the instance is still live.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET instance_state='running', completed_at=NULL`); err != nil {
		t.Fatal(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var live int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_instances WHERE instance_state='running'`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != len(orphans) {
		t.Fatalf("seeded %d live instances, want %d", live, len(orphans))
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migration over orphaned instances: %v", err)
	}
	assertOrphansClosed := func(phase string) {
		t.Helper()
		for workID, lifecycle := range orphans {
			var state, completedAt, terminalTime string
			if err := db.QueryRowContext(ctx, `SELECT i.instance_state, coalesce(i.completed_at,''), coalesce(w.terminal_time,'') FROM workflow_instances i JOIN work_items w ON w.id=i.work_id WHERE i.work_id=?`, workID).Scan(&state, &completedAt, &terminalTime); err != nil {
				t.Fatal(err)
			}
			want := "cancelled"
			if lifecycle == "superseded" {
				want = "superseded"
			}
			if state != want {
				t.Errorf("%s: %s instance_state = %q, want %q for lifecycle %q", phase, workID, state, want, lifecycle)
			}
			if completedAt == "" || completedAt != terminalTime {
				t.Errorf("%s: %s completed_at = %q, want the item's terminal time %q", phase, workID, completedAt, terminalTime)
			}
		}
	}
	assertOrphansClosed("after migration")
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("rebuild after migration: %v", err)
	}
	assertOrphansClosed("after rebuild")
}
