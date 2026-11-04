package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

func TestWorkflowPlanProjectionRetiredDefinitionSelection(t *testing.T) {
	ctx := t.Context()
	db, err := sqlitedb.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `CREATE TABLE work_items(id TEXT PRIMARY KEY, version INTEGER, updated_at TEXT); INSERT INTO work_items VALUES('work',1,'')`); err != nil {
		t.Fatal(err)
	}
	before, after := int64(1), int64(2)
	raw, err := json.Marshal(workflowDefinitionSelectedPayload{WorkflowVersionFields: WorkflowVersionFields{WorkID: "work", ExpectedVersion: &before, ResultingVersion: &after}, Ref: "retired.definition", Digest: "sha256:retired", WorkKind: "research"})
	if err != nil {
		t.Fatal(err)
	}
	event := Event{SubjectType: SubjectWorkItem, SubjectID: "work", Payload: raw, OccurredAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := foldWorkflowDefinitionSelected(ctx, tx, event, nil); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work'`).Scan(&version); err != nil || version != after {
		t.Fatalf("version = %d, want %d: %v", version, after, err)
	}
	if err := foldWorkflowDefinitionSelected(ctx, tx, event, nil); err == nil {
		t.Fatal("stale historical event did not refuse")
	}
}

func TestWorkflowPlanProjectionStartKeepsPlan(t *testing.T) {
	ctx := t.Context()
	db, err := sqlitedb.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `
CREATE TABLE work_items(id TEXT PRIMARY KEY,version INTEGER,updated_at TEXT,lifecycle TEXT,version_actions_floor INTEGER DEFAULT 0,transition_from TEXT,transition_reason TEXT,transition_actor TEXT,transition_evidence_refs TEXT,transition_seq INTEGER,transition_version INTEGER);
CREATE TABLE workflow_instances(work_id TEXT PRIMARY KEY,workflow_type TEXT,step_plan TEXT,instance_state TEXT,execution_actor_ref TEXT,execution_model TEXT,started_at TEXT,execution_started_at TEXT,last_checkpoint_at TEXT);
CREATE TABLE workflow_actors(actor_ref TEXT PRIMARY KEY,actor_class TEXT,session_ref TEXT);
CREATE TABLE workflow_step_epochs(work_id TEXT,step_id TEXT,attempt_epoch INTEGER,latest_start_seq INTEGER,latest_start_event_id TEXT,latest_start_payload TEXT,PRIMARY KEY(work_id,step_id));
CREATE TABLE workflow_coordinator_actions(work_id TEXT,actor_ref TEXT,last_effect_seq INTEGER,last_action_id TEXT,last_acted_at TEXT,PRIMARY KEY(work_id,actor_ref));
CREATE TABLE work_version_actions(work_id TEXT,resulting_version INTEGER,action_id TEXT,session_ref TEXT,effect_seq INTEGER);
CREATE TABLE workflow_completed_exits(work_id TEXT,definition_ref TEXT,step_id TEXT,action_id TEXT,exit_seq INTEGER,PRIMARY KEY(work_id,definition_ref,step_id,action_id));
CREATE TABLE workflow_phase_exits(work_id TEXT,action_id TEXT,exit_seq INTEGER,actor TEXT,step_id TEXT,contract_version INTEGER,record_version INTEGER,PRIMARY KEY(work_id,action_id));
CREATE TABLE workflow_contracts(work_id TEXT,contract_version INTEGER,superseded_by INTEGER);
CREATE TABLE workflow_checkpoints(work_id TEXT,checkpoint_id TEXT,step_id TEXT,step_kind TEXT,attempt_epoch INTEGER,accepted_inputs_digest TEXT,result_evidence_refs TEXT,resume_cursor TEXT,idempotency_identity TEXT,actor_ref TEXT,request_id TEXT,recorded_at TEXT);
INSERT INTO work_items(id,version,updated_at,lifecycle) VALUES('work',1,'','needed');
INSERT INTO workflow_actors VALUES('actor:example','agent','session');`)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := WorkflowStepPlanTemplate(WorkflowStepPlanChange)
	if err != nil {
		t.Fatal(err)
	}
	plan.Steps[0].Attempts = []WorkflowPlanAttempt{{Note: "done", CompletedAt: "2026-01-01T00:00:00Z"}}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO workflow_instances(work_id,workflow_type,step_plan,instance_state,execution_model) VALUES('work','change',?,'running','')`, string(encoded)); err != nil {
		t.Fatal(err)
	}
	before, after := int64(1), int64(2)
	payload, err := json.Marshal(workflowActionStartedPayload{WorkflowVersionFields: WorkflowVersionFields{WorkID: "work", ExpectedVersion: &before, ResultingVersion: &after}, StepID: "implement", ActionID: "start_execution", AttemptEpoch: 1, AcceptedInputsDigest: "digest", IdempotencyIdentity: "identity", ActorRef: "actor:example"})
	if err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: "start", Kind: WorkflowActionStarted, SubjectType: SubjectWorkItem, SubjectID: "work", Actor: "actor:example", Seq: 10, Payload: payload, OccurredAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := foldWorkflowActionStarted(ctx, tx, event, nil); err != nil {
		t.Fatal(err)
	}
	var got, state string
	if err := tx.QueryRowContext(ctx, `SELECT step_plan,instance_state FROM workflow_instances WHERE work_id='work'`).Scan(&got, &state); err != nil {
		t.Fatal(err)
	}
	if got != string(encoded) || state != "running" {
		t.Fatalf("plan or state changed: plan=%s state=%s", got, state)
	}
	var epoch int64
	if err := tx.QueryRowContext(ctx, `SELECT attempt_epoch FROM workflow_step_epochs WHERE work_id='work' AND step_id='implement'`).Scan(&epoch); err != nil || epoch != 1 {
		t.Fatalf("epoch=%d err=%v", epoch, err)
	}
	before, after = 2, 3
	payload, err = json.Marshal(workflowActionCheckpointedPayload{WorkflowVersionFields: WorkflowVersionFields{WorkID: "work", ExpectedVersion: &before, ResultingVersion: &after}, StepID: "implement", StepKind: "external_effect", AttemptEpoch: 1, ActorRef: "actor:example", RequestID: "request", CheckpointPayload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	event.EventID, event.Kind, event.Seq, event.Payload = "checkpoint", WorkflowActionCheckpointed, 11, payload
	if err := foldWorkflowActionCheckpointed(ctx, tx, event, nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT step_plan FROM workflow_instances WHERE work_id='work'`).Scan(&got); err != nil || got != string(encoded) {
		t.Fatalf("checkpoint changed plan: %s: %v", got, err)
	}
	before, after = 3, 4
	payload, err = json.Marshal(workflowActionCompletedPayload{WorkflowVersionFields: WorkflowVersionFields{WorkID: "work", ExpectedVersion: &before, ResultingVersion: &after}, StepID: "implement", ActionID: "start_execution", AttemptEpoch: 1, ActorRef: "actor:example", ResultEvidenceRefs: []string{}, ChangedRefs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	event.EventID, event.Kind, event.Seq, event.Payload = "done", WorkflowActionCompleted, 12, payload
	if err := foldWorkflowActionCompleted(ctx, tx, event, nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT step_plan FROM workflow_instances WHERE work_id='work'`).Scan(&got); err != nil || got != string(encoded) {
		t.Fatalf("action completion changed plan: %s: %v", got, err)
	}
}
