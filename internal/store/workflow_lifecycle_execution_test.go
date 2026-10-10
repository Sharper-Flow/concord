package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// CD-0183 D1: the first workflow_action the store applies to a needed item
// moves it to in_progress in the same transaction, so the item stops reading
// ready before any external effect.
// The action here is a checkpoint on an internal step: no external-effect
// step has begun, and the lifecycle has still moved.
func TestFirstWorkflowActionStartsLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "first-action-lifecycle"
	setupProductWithProject(t, s, "product", "product-project")
	seedProjectWorkItem(t, s, workID, "product-project", "First action lifecycle", "Proves the first action starts the lifecycle")
	actor := WorkflowActor{PrincipalRef: "principal:operator", ClientRef: "client:concord-1", AgentRef: "agent:owner", SessionRef: "session:" + workID, ActorClass: ActorAgent}
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return initializeWorkflowRawTx(ctx, tx.tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: actor, Now: time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)})
	}); err != nil {
		t.Fatal(err)
	}

	// The item reads ready before the first action.
	ready, err := s.QueryQ5(ctx, Q5Request{Product: "product", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	readsReady := false
	for _, item := range ready.Items {
		if item.ID == workID {
			readsReady = true
		}
	}
	if !readsReady {
		t.Fatal("the needed workflow item does not read ready, so this test cannot show the move")
	}

	var version int64
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	payload := mustJSONValue(map[string]any{
		"active_unit": "unit:first-action", "hypothesis": "hypothesis:the fault is in the fold",
		"diagnosis": "diagnosis:the lifecycle waits for an external step", "strategy": "strategy:start it at the first action",
		"touched_refs": []string{"ref:workflow"}, "evidence_refs": []string{"evidence:first-action"},
		"pending_questions": []string{}, "pending_decisions": []string{},
	})
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, actionErr := applyWorkflowActionRawTx(ctx, tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "checkpoint_context", Payload: payload, Actor: actor,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("b", 64), IdempotencyIdentity: "first-action-checkpoint",
		OperationID: "first-action-checkpoint", PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition",
		IdempotencyKey: "first-action-checkpoint", RequestID: "request:first-action-checkpoint", ContractDigest: testManifestDigest,
		Now: time.Date(2026, 8, 19, 1, 0, 0, 0, time.UTC),
	})
	if actionErr != nil {
		tx.Rollback()
		t.Fatalf("first workflow action refused: %v", actionErr)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var lifecycle string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "in_progress" {
		t.Fatalf("first action left lifecycle=%q, want in_progress", lifecycle)
	}
	ready, err = s.QueryQ5(ctx, Q5Request{Product: "product", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range ready.Items {
		if item.ID == workID {
			t.Fatal("the started item still reads ready")
		}
	}
}

// TestExecutionStartStepSetMatchesRegistry pins the migration backfill's
// external-effect step-id set to the registered definitions: the set holds
// exactly the step ids the current registry declares external-effect, across
// every registered version. The migration freezes the set, so a registry
// change that moves a step kind must revisit the migration decision.
func TestExecutionStartStepSetMatchesRegistry(t *testing.T) {
	t.Parallel()
	var migrationSQL string
	for _, migration := range migrations {
		if migration.Version == 107 {
			migrationSQL = migration.SQL
		}
	}
	if migrationSQL == "" {
		t.Fatal("migration 107 is missing")
	}
	const anchor = "'$.step_id') IN ("
	start := strings.Index(migrationSQL, anchor)
	if start < 0 {
		t.Fatal("migration 107 carries no step-id IN list")
	}
	start += len(anchor)
	end := strings.Index(migrationSQL[start:], ")")
	if end < 0 {
		t.Fatal("migration 107 step-id IN list is unterminated")
	}
	migrationSet := map[string]bool{}
	for _, raw := range strings.Split(migrationSQL[start:start+end], ",") {
		id := strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), "'"))
		if id != "" {
			migrationSet[id] = true
		}
	}
	registrySet := map[string]bool{}
	for _, definition := range BuiltinWorkflowDefinitions() {
		for _, step := range definition.StepGraph.Steps {
			if step.Kind == WorkflowStepExternalEffect {
				registrySet[step.ID] = true
			}
		}
	}
	for id := range registrySet {
		if !migrationSet[id] {
			t.Errorf("registered external-effect step %q is missing from the migration set", id)
		}
	}
	for id := range migrationSet {
		if !registrySet[id] {
			t.Errorf("migration set names %q, which no registered version declares external-effect", id)
		}
	}
}

// TestExecutionStartBackfillMatchesFoldDerivation proves three derivations of
// the execution-start fact agree: the fold's value, migration 107's backfill
// over the same log, and a rebuild from the log. The second external-effect
// start carries an earlier occurred_at than the first, because appendEvent
// orders by seq and never requires timestamps to rise with it.
func TestExecutionStartBackfillMatchesFoldDerivation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const workID = "execution-start-derivation"
	s, workerRef := seedDispatchedWorkerAtExecution(t, workID)
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	restart := workflowEventWithActor("restart-"+workID, WorkflowActionStarted, workID, workerRef, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 2, "accepted_inputs_digest": "sha256:" + strings.Repeat("b", 64), "idempotency_identity": "restart:" + workID, "actor_ref": workerRef, "execution_model": preferredModelForLane(BuiltinLaneDefinitions()[0])})
	restart.OccurredAt = restart.OccurredAt.Add(-time.Hour)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{restart}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	executionStart := func(label string) string {
		t.Helper()
		var value sql.NullString
		if err := s.DatabaseForTesting().QueryRow(`SELECT execution_started_at FROM workflow_instances WHERE work_id=?`, workID).Scan(&value); err != nil {
			t.Fatal(err)
		}
		if !value.Valid || value.String == "" {
			t.Fatalf("%s left execution_started_at unset", label)
		}
		return value.String
	}
	folded := executionStart("the fold")
	if want := restart.OccurredAt.Add(time.Hour).UTC().Format(time.RFC3339Nano); folded != want {
		t.Fatalf("fold kept %q, want the first start in log order %q", folded, want)
	}

	if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO fold_guard(active) VALUES (1); UPDATE workflow_instances SET execution_started_at=NULL WHERE work_id=?; DELETE FROM fold_guard;`, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(migration107Backfill(t)); err != nil {
		t.Fatal(err)
	}
	if backfilled := executionStart("the migration backfill"); backfilled != folded {
		t.Fatalf("migration backfill derived %q, want the folded value %q", backfilled, folded)
	}

	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	if rebuilt := executionStart("the rebuild"); rebuilt != folded {
		t.Fatalf("rebuild derived %q, want the folded value %q", rebuilt, folded)
	}
}

// migration107Backfill returns migration 107's text after its ALTER TABLE, so
// the derivation test runs the shipped backfill rather than a copy of it.
func migration107Backfill(t *testing.T) string {
	t.Helper()
	const alter = "ALTER TABLE workflow_instances ADD COLUMN execution_started_at TEXT;"
	for _, migration := range migrations {
		if migration.Version != 107 {
			continue
		}
		index := strings.Index(migration.SQL, alter)
		if index < 0 {
			t.Fatal("migration 107 carries no execution_started_at ALTER TABLE")
		}
		return migration.SQL[index+len(alter):]
	}
	t.Fatal("migration 107 is missing")
	return ""
}

// seedProjectWorkItem seeds a needed task with a title, a value statement, and
// primary membership in one Project.
func seedProjectWorkItem(t *testing.T, s *Store, workID, projectID, title, valueStatement string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_items(id, kind, title, lifecycle, priority, urgency, version, intent_json, created_at, updated_at) VALUES(?, 'task', ?, 'needed', 0, 'standard', 1, ?, '2026-09-09T00:00:00Z', '2026-09-09T00:00:00Z')`, workID, title, `{"title":"`+title+`","value_statement":"`+valueStatement+`","kind":"task","priority":0,"urgency":"standard"}`); err != nil {
		t.Fatalf("seed work item %s: %v", workID, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_projects(work_id, project_id, role) VALUES(?, ?, 'primary')`, workID, projectID); err != nil {
		t.Fatalf("seed work membership %s: %v", workID, err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
