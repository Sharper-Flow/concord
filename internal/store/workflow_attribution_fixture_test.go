package store

import (
	"encoding/json"
	"fmt"
	"testing"
)

func outsideRepairDatabaseSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	raw, err := json.Marshal(retirementUpgradeSnapshot(t, s.db))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func outsideRepairSettleAction(t *testing.T, s *Store, work string, actor WorkflowActor, kind string) {
	t.Helper()
	ref, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := readWorkflowPlanTx(t.Context(), s.db, work)
	if err != nil || plan == nil {
		t.Fatalf("settle fixture plan: %v %+v", err, plan)
	}
	frontier := WorkflowStepPlanFrontier(*plan)
	if len(frontier) == 0 {
		t.Fatal("settle fixture has no frontier step")
	}
	version := readWorkVersion(t, s, work)
	startFields := map[string]any{"work_id": work, "expected_version": version, "resulting_version": version + 1,
		"step_id": frontier[0].ID, "action_id": "approve_contract", "attempt_epoch": 1,
		"accepted_inputs_digest": "sha256:settle-" + work, "idempotency_identity": "settle-" + work + "-start", "actor_ref": ref, "execution_model": "model:fixture"}
	startEvent := workflowEventWithActor(fmt.Sprintf("%s-settle-start-%d", work, version), WorkflowActionStarted, work, ref, startFields)
	if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{startEvent}, ExpectedVersions: workVersion(work, version)}); err != nil {
		t.Fatal(err)
	}
	version = readWorkVersion(t, s, work)
	fields := map[string]any{"work_id": work, "expected_version": version, "resulting_version": version + 1,
		"step_id": frontier[0].ID, "attempt_epoch": 1, "actor_ref": ref}
	if kind == WorkflowActionFailed {
		fields["failure_kind"], fields["recoverable"] = "timeout", false
	} else {
		fields["action_id"] = "record_plan"
	}
	event := workflowEventWithActor(fmt.Sprintf("%s-settle-%d", work, version), kind, work, ref, fields)
	if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion(work, version)}); err != nil {
		t.Fatal(err)
	}
}

func terminalCleanupSQL(t *testing.T, s *Store, queries ...string) {
	t.Helper()
	if err := s.Transact(t.Context(), func(transaction *Transaction) error {
		scope := newWriteScope(transaction.tx)
		if err := scope.enter(t.Context()); err != nil {
			return err
		}
		for _, query := range queries {
			if _, err := transaction.tx.ExecContext(t.Context(), query); err != nil {
				return fmt.Errorf("fixture %s: %w", query, err)
			}
		}
		return scope.close(t.Context())
	}); err != nil {
		t.Fatal(err)
	}
}

func lawUpgradeSnapshot(t *testing.T, s *Store) map[string][]string {
	t.Helper()
	rows, err := s.db.Query(`SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name<>'schema_migrations' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	return sixClassSnapshot(t, s.db, tables...)
}
