package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// A work item that reaches a terminal lifecycle closes its workflow instance
// in the same fold. Cancelled and superseded mirror the lifecycle. A completed
// target whose workflow instance still runs is refused (CD-0183 D4): the
// lifecycle cannot bypass the workflow's completion gate, whose remedy names
// the workflow_action complete action.
func TestTerminalLifecycleClosesTheWorkflowInstance(t *testing.T) {
	t.Parallel()
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
			selected := workflowEvent(workID+"-definition", WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "ref": registered.Definition.Ref, "digest": registered.Digest, "work_kind": string(registered.Definition.WorkKind)})
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

// CD-0183 D4: a completed lifecycle target whose workflow instance has not
// reached a terminal state is refused with a typed refusal whose remedy names
// the workflow completion action.
func TestTerminalLifecycleRefusesCompletedWhileWorkflowLive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	workID := "terminal-completed-live"
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	selected := workflowEvent(workID+"-definition", WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "ref": registered.Definition.Ref, "digest": registered.Digest, "work_kind": string(registered.Definition.WorkKind)})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{selected}, ExpectedVersions: workVersion(workID, 2)}); err != nil {
		t.Fatal(err)
	}
	err = applyWorkEvent(t, s, workTransitionEvent(workID+"-complete", workID, "needed", "completed", 3, 4), workVersion(workID, 3))
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindNotTerminal {
		t.Fatalf("completed on external evidence error=%v, want %s", err, KindNotTerminal)
	}
	if !strings.Contains(failure.RecoveryAction, "workflow_action complete") {
		t.Fatalf("recovery action = %q, want the workflow completion action", failure.RecoveryAction)
	}
	var lifecycle, state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT w.lifecycle, i.instance_state FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&lifecycle, &state); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "needed" || state != "planned" {
		t.Fatalf("refused transition changed state: lifecycle=%q instance=%q, want needed/planned", lifecycle, state)
	}
}

// The workflow completion's own lifecycle move keeps the instance record the
// completion fold wrote: the terminal-lifecycle close admits an item whose
// instance is already terminal, and stamps nothing (CD-0183 D3).
func TestTerminalLifecycleLeavesACompletedInstanceAlone(t *testing.T) {
	t.Parallel()
	workID := "completed-then-lifecycle"
	s, completion := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}, includeSpec: true, includeVerdict: true, includePremise: true, verdictKind: "ok"})
	if err := CompleteWorkflow(context.Background(), s, completion); err != nil {
		t.Fatalf("workflow completion refused: %v", err)
	}
	var lifecycle, state string
	var completedAt *string
	if err := s.DatabaseForTesting().QueryRow(`SELECT w.lifecycle, i.instance_state, i.completed_at FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&lifecycle, &state, &completedAt); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "completed" {
		t.Fatalf("lifecycle = %q after the workflow completion, want completed", lifecycle)
	}
	if state != "completed" {
		t.Fatalf("instance_state = %q after the workflow completion, want completed", state)
	}
	if completedAt == nil || *completedAt == "" {
		t.Fatal("the completion left no instance stamp")
	}
}

// Migration 75 closes instances that terminal work items left live and uses
// each item's terminal time as the completion stamp.
func TestMigrationClosesInstancesOfTerminalWorkItems(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}

	orphans := map[string]string{"orphan-cancelled": "cancelled", "orphan-completed": "completed", "orphan-superseded": "superseded"}
	const terminalAt = "2026-09-06T01:00:00Z"

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
	if _, err := db.ExecContext(ctx, `INSERT INTO agent_installation_keys(key_name,key_bytes,created_at) VALUES('cursor',zeroblob(32),'2026-09-06T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	// Reproduce the migration input directly: terminal work with a live instance.
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for workID, lifecycle := range orphans {
		if _, err := db.ExecContext(ctx, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES(?,?,?,?,?,?,?,?,?)`,
			workID, "task", "Orphan "+workID, lifecycle, 1, 2, terminalAt, terminalAt, terminalAt); err != nil {
			t.Fatalf("seed %s: %v", workID, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state,execution_model) VALUES(?,?,?,?,?,'running','single_actor')`,
			workID, definition.Definition.Ref, 1, definition.Digest, "proposal"); err != nil {
			t.Fatalf("seed instance %s: %v", workID, err)
		}
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
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
}
