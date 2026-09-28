package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// CD-0183 D1: the first workflow_action the store applies to a needed item
// moves it to in_progress and enqueues the Linear issue_update in the same
// transaction, so the item stops reading ready before any external effect.
// The action here is a checkpoint on an internal step: no external-effect
// step has begun, and the lifecycle has still moved.
func TestFirstWorkflowActionStartsLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "first-action-lifecycle"
	setupLinearProduct(t, s, "product")
	setupLinearConnectionResource(t, s, "product", map[string]any{"linear": map[string]any{
		"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key",
		"status_ids": map[string]string{"needed": "state-needed", "in_progress": "state-in-progress", "completed": "state-completed", "cancelled": "state-cancelled", "superseded": "state-superseded"},
	}})
	if _, err := s.SetProductPlanningMode(ctx, "product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	seedLinearWorkItem(t, s, workID, "product-project", "First action lifecycle", "Proves the first action starts the lifecycle")
	for _, state := range []string{LinearLinkUnpublished, LinearLinkPending, LinearLinkConfirmed} {
		if err := s.RecordLinearLink(ctx, workID, "remote-first-action", "", "", "", "", state); err != nil {
			t.Fatal(err)
		}
	}
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
	var updateLifecycle, statusID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.lifecycle'), json_extract(payload,'$.status_id') FROM linear_outbox WHERE work_id=?`, workID).Scan(&updateLifecycle, &statusID); err != nil {
		t.Fatalf("the first action enqueued no Linear issue_update: %v", err)
	}
	if updateLifecycle != "in_progress" || statusID != "state-in-progress" {
		t.Fatalf("linear update = %s/%s, want in_progress/state-in-progress", updateLifecycle, statusID)
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

// TestExecutionStartBackfillMatchesFoldDerivation drives a break_fix workflow
// onto its external-effect repair step, then proves three derivations of the
// execution-start fact agree: the fold's value, the migration backfill's SQL
// over the same log, and a rebuild from the log.
func TestExecutionStartBackfillMatchesFoldDerivation(t *testing.T) {
	t.Parallel()
	const workID = "execution-start-derivation"
	s, _, _, _ := seedCompletedWorkerAtExecution(t, workID)
	var folded string
	if err := s.DatabaseForTesting().QueryRow(`SELECT execution_started_at FROM workflow_instances WHERE work_id=?`, workID).Scan(&folded); err != nil {
		t.Fatal(err)
	}
	if folded == "" {
		t.Fatal("the external-effect step start left execution_started_at unset")
	}

	const backfill = `SELECT coalesce((SELECT MIN(e.occurred_at) FROM domain_events e WHERE e.subject_type='work_item' AND e.subject_id=workflow_instances.work_id AND e.kind='workflow.action_started' AND json_extract(e.payload,'$.step_id') IN ('execution','refine','repair','poc_optional','rollback_optional','analyze','execute')),'') FROM workflow_instances WHERE work_id=?`
	var derived string
	if err := s.DatabaseForTesting().QueryRow(backfill, workID).Scan(&derived); err != nil {
		t.Fatal(err)
	}
	if derived != folded {
		t.Fatalf("backfill derivation %q does not match the folded value %q", derived, folded)
	}

	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	var rebuilt string
	if err := s.DatabaseForTesting().QueryRow(`SELECT execution_started_at FROM workflow_instances WHERE work_id=?`, workID).Scan(&rebuilt); err != nil {
		t.Fatal(err)
	}
	if rebuilt != folded {
		t.Fatalf("rebuild derived %q, want the folded value %q", rebuilt, folded)
	}
}
