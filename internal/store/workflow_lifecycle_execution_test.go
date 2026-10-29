package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
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
	_, actionErr := applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
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

// TestExecutionStartStepSetMatchesRegistry checks that the migration backfill
// covers every current external-effect step. CD-0242 D14 retains no historical
// definitions, so the frozen migration may also name retired steps.
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
}

// TestExecutionStartBackfillMatchesFoldDerivation proves the execution-start
// fold agrees with migration 107's backfill. The second external-effect
// start carries an earlier occurred_at than the first, because appendEvent
// orders by seq and never requires timestamps to rise with it.
func TestExecutionStartBackfillMatchesFoldDerivation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const workID = "execution-start-derivation"
	s, executorRef, _ := seedOrdinaryExecutorAtExecution(t, workID)
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	// Two ordinary starts precede this restart; occurrence time does not
	// replace their sequence order.
	restart := workflowEventWithActor("restart-"+workID, WorkflowActionStarted, workID, executorRef, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 3, "accepted_inputs_digest": "sha256:" + strings.Repeat("b", 64), "idempotency_identity": "restart:" + workID, "actor_ref": executorRef, "execution_model": "model:fixture"})
	restart.OccurredAt = restart.OccurredAt.Add(-time.Hour)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{restart}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	executionStart := func(db *sql.DB, label string) string {
		t.Helper()
		var value sql.NullString
		if err := db.QueryRow(`SELECT execution_started_at FROM workflow_instances WHERE work_id=?`, workID).Scan(&value); err != nil {
			t.Fatal(err)
		}
		if !value.Valid || value.String == "" {
			t.Fatalf("%s left execution_started_at unset", label)
		}
		return value.String
	}
	folded := executionStart(s.db, "the fold")
	if want := restart.OccurredAt.Add(time.Hour).UTC().Format(time.RFC3339Nano); folded != want {
		t.Fatalf("fold kept %q, want the first start in log order %q", folded, want)
	}

	// CD-0242 D2 retires replay construction, not the one-time backfill.
	// Only parent data crosses into the genuine 106 schema. The source starts
	// are authored independently of execution_started_at and epoch payload rows.
	path := filepath.Join(t.TempDir(), "execution-start-106.db")
	db := openMigratedTo(t, path, 106)
	copyFixtureDataToPrefix(t, s, db, []string{"products", "projects", "product_projects", "work_items", "work_projects", "workflow_actors", "workflow_instances"}, map[string]map[string]string{
		"products":           {"planning_mode": "'local_only'"},
		"workflow_instances": {"definition_version": "1"},
	}, "fold_guard")
	start := func(id, action, identity string, expected, epoch int64) Event {
		return workflowEventWithActor(id, WorkflowActionStarted, workID, executorRef, map[string]any{
			"work_id": workID, "expected_version": expected, "resulting_version": expected + 1,
			"step_id": "execution", "action_id": action, "attempt_epoch": epoch,
			"accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": identity,
			"actor_ref": executorRef, "execution_model": "model:fixture",
		})
	}
	corpus := []Event{
		start("start-"+workID+"-1", "start_execution", "start-"+workID+"-1", 9, 1),
		start("start-"+workID+"-2", "start_execution", "start-"+workID+"-2", 10, 2),
		restart,
	}
	var currentStarts int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, WorkflowActionStarted).Scan(&currentStarts); err != nil || currentStarts != len(corpus) {
		t.Fatalf("current start population=%d,%v; corpus=%d", currentStarts, err, len(corpus))
	}
	for _, event := range corpus {
		var seq int64
		var digest string
		if err := s.db.QueryRow(`SELECT effect_seq,payload_digest FROM effect_receipts WHERE event_id=?`, event.EventID).Scan(&seq, &digest); err != nil {
			t.Fatal(err)
		}
		if digest != historicalFixturePayloadDigest(t, string(event.Payload)) {
			t.Fatalf("explicit source %s differs from fresh-route payload", event.EventID)
		}
		if _, err := db.Exec(`INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?,?)`, seq, event.EventID, event.Kind, event.SubjectType, event.SubjectID, event.Actor, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.PayloadVersion, string(event.Payload)); err != nil {
			t.Fatal(err)
		}
	}
	before := histEventRows(t, &Store{db: db})
	for _, row := range before {
		t.Logf("migration107 source seq=%d id=%s kind=%s actor=%s time=%s digest=%s", row.Seq, row.EventID, row.Kind, row.Actor, row.OccurredAt, historicalFixturePayloadDigest(t, row.Payload))
	}
	advanceHistoricalPrefix(t, db, 107)
	if backfilled := executionStart(db, "migration107"); backfilled != folded {
		t.Fatalf("migration107 first-sequence selection = %q, want fresh-route first start %q", backfilled, folded)
	}
	if got := histEventRows(t, &Store{db: db}); !reflect.DeepEqual(got, before) {
		t.Fatal("execution-start migration changed source bytes, actors, sequence or occurrence times")
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if backfilled := executionStart(db, "upgraded migration107"); backfilled != folded {
		t.Fatalf("migration backfill derived %q, want the folded value %q", backfilled, folded)
	}
	assertHistoricalReceipts(t, db, before)
	assertEventLogAbsent(t, &Store{db: db, path: path})
	queries := []string{`SELECT execution_started_at FROM workflow_instances WHERE work_id='execution-start-derivation'`}
	assertCorpusSelectionRed(t, historicalFactCorpus{
		events: corpus,
		parents: func(t *testing.T, prefix *Store, guard string) {
			copyFixtureDataToPrefix(t, s, prefix.db, []string{"products", "projects", "product_projects", "work_items", "work_projects", "workflow_actors", "workflow_instances"}, map[string]map[string]string{
				"products":           {"planning_mode": "'local_only'"},
				"workflow_instances": {"definition_version": "1"},
			}, guard)
		},
	}, 107, "ORDER BY e.seq", "ORDER BY e.seq DESC", queries, workerFactsQueryRows(t, s, queries))
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
	if err := enterWrite(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_items(id, kind, title, lifecycle, priority, urgency, version, intent_json, created_at, updated_at) VALUES(?, 'task', ?, 'needed', 0, 'standard', 1, ?, '2026-09-09T00:00:00Z', '2026-09-09T00:00:00Z')`, workID, title, `{"title":"`+title+`","value_statement":"`+valueStatement+`","kind":"task","priority":0,"urgency":"standard"}`); err != nil {
		t.Fatalf("seed work item %s: %v", workID, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_projects(work_id, project_id, role) VALUES(?, ?, 'primary')`, workID, projectID); err != nil {
		t.Fatalf("seed work membership %s: %v", workID, err)
	}
	if err := leaveWrite(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
