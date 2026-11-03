package store

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestWorkflowPlanActionDispatch(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	seedStepWork(t, s, "work-plan-dispatch")
	ctx := t.Context()
	if err := s.Transact(ctx, func(tx *Transaction) error {
		scope := newWriteScope(tx.tx)
		if err := scope.enter(ctx); err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO workflow_instances(work_id,instance_state) VALUES('work-plan-dispatch','intake')`); err != nil {
			return err
		}
		return scope.close(ctx)
	}); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work-plan-dispatch'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	seq := 0
	apply := func(action, payload string, expected int64) (WorkflowActionExecutionResult, error) {
		seq++
		r := WorkflowActionExecutionRequest{WorkID: "work-plan-dispatch", ExpectedVersion: expected, ActionID: action, Payload: json.RawMessage(payload), Actor: stepFixtureActor(), OperationID: fmt.Sprintf("plan-op-%d", seq), IdempotencyIdentity: fmt.Sprintf("plan-key-%d", seq), PrincipalRef: "principal-1", RequestID: fmt.Sprintf("request-%d", seq), AcceptedInputsDigest: testDigest("inputs"), ContractDigest: testManifestDigest, Now: time.Unix(100, 0).UTC()}
		var result WorkflowActionExecutionResult
		err := s.Transact(ctx, func(tx *Transaction) error {
			var err error
			result, err = ApplyWorkflowActionTx(ctx, tx, r)
			return err
		})
		return result, err
	}
	intake := `{"outcome":"proceed","open_questions_found":false}`
	result, err := apply("record_intake", intake, version)
	if err != nil {
		t.Fatal(err)
	}
	old := version
	version = result.ResultingVersion
	var beforePrelude string
	if err := s.db.QueryRowContext(ctx, `SELECT prelude_state FROM workflow_instances WHERE work_id='work-plan-dispatch'`).Scan(&beforePrelude); err != nil {
		t.Fatal(err)
	}
	if _, err := apply("record_intake", intake, old); err == nil {
		t.Fatal("stale intake accepted")
	}
	if _, err := apply("record_intake", intake, version); err == nil {
		t.Fatal("duplicate intake accepted")
	}
	var storedVersion, operationCount int64
	var prelude string
	if err := s.db.QueryRowContext(ctx, `SELECT w.version,i.prelude_state,(SELECT count(*) FROM durable_operations WHERE work_id=w.id) FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id='work-plan-dispatch'`).Scan(&storedVersion, &prelude, &operationCount); err != nil {
		t.Fatal(err)
	}
	if storedVersion != version || operationCount != 1 || prelude != beforePrelude {
		t.Fatalf("refused intake changed state: version=%d operations=%d prelude=%q", storedVersion, operationCount, prelude)
	}
	if _, err := apply("pick_type", `{"type":"change","reason":"initial selection","unexpected":true}`, version); err == nil {
		t.Fatal("undeclared field accepted")
	}
	result, err = apply("pick_type", `{"type":"change","reason":"initial selection"}`, version)
	if err != nil {
		t.Fatal(err)
	}
	version = result.ResultingVersion
	var state string
	if err := s.db.QueryRowContext(ctx, `SELECT instance_state FROM workflow_instances WHERE work_id='work-plan-dispatch'`).Scan(&state); err != nil || state != "running" {
		t.Fatalf("type pick left instance_state=%q: %v", state, err)
	}
	if _, err := apply("pick_type", `{"type":"change","reason":"same type again"}`, version); err == nil {
		t.Fatal("re-picking the current type accepted")
	}
	plan, err := readWorkflowPlanTx(ctx, s.db, "work-plan-dispatch")
	if err != nil || plan == nil {
		t.Fatalf("read plan: %v", err)
	}
	frontier := WorkflowStepPlanFrontier(*plan)
	if len(frontier) == 0 {
		t.Fatal("missing initial frontier")
	}
	step := frontier[0].ID
	payload, _ := json.Marshal(map[string]string{"step_id": step, "note": "completed step"})
	result, err = apply("complete_step", string(payload), version)
	if err != nil {
		t.Fatal(err)
	}
	version = result.ResultingVersion
	plan, err = readWorkflowPlanTx(ctx, s.db, "work-plan-dispatch")
	if err != nil {
		t.Fatal(err)
	}
	frontier = WorkflowStepPlanFrontier(*plan)
	if len(frontier) == 0 || frontier[0].ID == step {
		t.Fatalf("frontier after completion: %+v", frontier)
	}
	if completed, _ := stepByID(plan.Steps, step); len(completed.Attempts) != 1 || completed.Attempts[0].CompletionSeq <= 0 {
		t.Fatalf("completed attempt lacks its completion sequence: %+v", completed)
	}
	var beforePlan string
	if err := s.db.QueryRowContext(ctx, `SELECT step_plan FROM workflow_instances WHERE work_id='work-plan-dispatch'`).Scan(&beforePlan); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"start_execution", "checkpoint_execution"} {
		result, err = apply(action, `{}`, version)
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		version = result.ResultingVersion
		var afterPlan string
		if err := s.db.QueryRowContext(ctx, `SELECT step_plan FROM workflow_instances WHERE work_id='work-plan-dispatch'`).Scan(&afterPlan); err != nil || afterPlan != beforePlan {
			t.Fatalf("%s changed plan: %v", action, err)
		}
	}
	var stored int64
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work-plan-dispatch'`).Scan(&stored); err != nil || stored != version {
		t.Fatalf("version %d want %d: %v", stored, version, err)
	}
}
