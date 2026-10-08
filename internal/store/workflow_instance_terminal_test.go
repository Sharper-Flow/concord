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
	selected := workflowEvent(workID+"-definition", WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "ref": registered.Definition.Ref, "version": registered.Definition.Version, "digest": registered.Digest, "work_kind": string(registered.Definition.WorkKind)})
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

// Migration 74 closes the instances that earlier terminal folds left live,
// with the state the fold now records and the item's terminal time as the
// stamp. A log rebuild reproduces the same row, so the repaired projection
// and the replayed projection agree.
func TestMigrationClosesInstancesOfTerminalWorkItems(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// The event log must replay under the current registry, so it is
	// authored by the current write path on a full-schema sibling store and
	// copied verbatim. The fixture itself stops before 75: the write path
	// now ends in the initiative invariant validator, whose projection
	// tables belong to migration 120, so a v74 fixture cannot run it.
	sibling := openTemp(t)
	defer sibling.Close()
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}

	orphans := map[string]string{"orphan-cancelled": "cancelled", "orphan-completed": "completed", "orphan-superseded": "superseded"}
	terminal := map[string]string{}
	for workID, lifecycle := range orphans {
		_, version := startWorkflowPinnedTo(t, sibling, workID, definition)
		switch lifecycle {
		case "superseded":
			seedWork(t, sibling, workID+"-successor")
			if err := applyWorkEvent(t, sibling, workSupersededEvent(workID+"-supersede", workID+"-successor", workID, version, version+1), workVersion(workID, version)); err != nil {
				t.Fatal(err)
			}
		case "completed":
			// The historical orphan migration 75 repairs predates the
			// CD-0183 gate, so the recorded transition folds under the
			// replay context a rebuild gives it, exactly as the log that
			// survives from an earlier release does.
			event := workTransitionEvent(workID+"-end", workID, "in_progress", "completed", version, version+1)
			if err := ApplyOperation(workflowReplayContext(context.Background()), sibling, Operation{Events: []Event{event}, ExpectedVersions: workVersion(workID, version)}); err != nil {
				t.Fatal(err)
			}
			assertFoldGuardEmpty(t, sibling)
		default:
			// The action start above moves the item to in_progress (CD-0183
			// D1), so the terminal transition starts from there.
			if err := applyWorkEvent(t, sibling, workTransitionEvent(workID+"-end", workID, "in_progress", lifecycle, version, version+1), workVersion(workID, version)); err != nil {
				t.Fatal(err)
			}
		}
		var lifecycleAt, terminalAt string
		if err := sibling.DatabaseForTesting().QueryRow(`SELECT lifecycle,coalesce(terminal_time,'') FROM work_items WHERE id=?`, workID).Scan(&lifecycleAt, &terminalAt); err != nil {
			t.Fatal(err)
		}
		if lifecycleAt != lifecycle || terminalAt == "" {
			t.Fatalf("sibling %s lifecycle=%q terminal_time=%q", workID, lifecycleAt, terminalAt)
		}
		terminal[workID] = terminalAt
	}

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
	if err := ensureInstallationKey(ctx, db); err != nil {
		t.Fatal(err)
	}

	// Reproduce the state an earlier binary left: the item is terminal, the
	// instance is still live, and the log holds the history a rebuild
	// replays. Guarded direct SQL writes the projection rows; the log is
	// the sibling's, copied in order.
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for workID, lifecycle := range orphans {
		if _, err := db.ExecContext(ctx, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES(?,?,?,?,?,?,?,?,?)`,
			workID, "task", "Orphan "+workID, lifecycle, 1, 2, terminal[workID], terminal[workID], terminal[workID]); err != nil {
			t.Fatalf("seed %s: %v", workID, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state,execution_model) VALUES(?,?,?,?,?,'running','single_actor')`,
			workID, definition.Definition.Ref, definition.Definition.Version, definition.Digest, "proposal"); err != nil {
			t.Fatalf("seed instance %s: %v", workID, err)
		}
	}
	siblingRows, err := sibling.DatabaseForTesting().Query(`SELECT event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload FROM domain_events ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	var events [][]any
	for siblingRows.Next() {
		var eventID, kind, subjectType, subjectID, actor, occurredAt, payload string
		var payloadVersion int
		if err := siblingRows.Scan(&eventID, &kind, &subjectType, &subjectID, &actor, &occurredAt, &payloadVersion, &payload); err != nil {
			siblingRows.Close()
			t.Fatal(err)
		}
		events = append(events, []any{eventID, kind, subjectType, subjectID, actor, occurredAt, payloadVersion, payload})
	}
	if err := errors.Join(siblingRows.Err(), siblingRows.Close()); err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if _, err := db.ExecContext(ctx, `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`, event...); err != nil {
			t.Fatalf("copy event %v: %v", event[0], err)
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

	s := &Store{db: db, path: path}
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
