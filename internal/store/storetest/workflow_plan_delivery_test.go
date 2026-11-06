package storetest_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

func TestWorkflowPlanDeliveryAdmission(t *testing.T) {
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	plan, err := store.WorkflowStepPlanTemplate(store.WorkflowStepPlanChange)
	if err != nil {
		t.Fatal(err)
	}
	plan.Steps[0].Attempts = []store.WorkflowPlanAttempt{{Note: "clarified", CompletedAt: "2026-01-01T00:00:00Z"}}
	plan.Steps[1].Attempts = []store.WorkflowPlanAttempt{{Note: "planned", CompletedAt: "2026-01-01T00:00:00Z"}}
	plan.Steps[2].Attempts = []store.WorkflowPlanAttempt{{Note: "implemented", CompletedAt: "2026-01-01T00:00:00Z", Reviewer: "concord-review", Result: "pass", Commit: "commit:test", Link: "link:test"}}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	actor := store.WorkflowActor{PrincipalRef: "principal:test", ClientRef: "client:test", AgentRef: "agent:test", SessionRef: "session:test", ActorClass: "operator"}
	actorRef, err := store.WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	db := s.DatabaseForTesting()
	_, err = db.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1);
INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project:test','test',1,'now','now');
INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product:test','test','prototype','operator_only',1,'now','now');
INSERT INTO product_projects(product_id,project_id,role) VALUES('product:test','project:test','primary');
INSERT INTO work_items(id,kind,title,lifecycle,version,created_at,updated_at) VALUES('work:test','task','test','needed',1,'now','now');
INSERT INTO work_projects(work_id,project_id,role) VALUES('work:test','project:test','primary');
INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?1,'principal:test','client:test','agent:test','session:test','operator','now');
INSERT INTO workflow_instances(work_id,instance_state,workflow_type,step_plan) VALUES('work:test','running','change',?2);
INSERT INTO workflow_contracts(work_id,contract_version,consequence_class,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version) VALUES('work:test',1,'internal_sqlite','now',?1,'[]','[]',1);
DELETE FROM write_guard;`, actorRef, string(planJSON))
	if err != nil {
		t.Fatal(err)
	}
	req := store.WorkflowActionExecutionRequest{WorkID: "work:test", ExpectedVersion: 1, ActionID: "record_delivery", Payload: json.RawMessage(`{"delivery_artifact":"artifact:test","delivery_state":"asserted"}`), Actor: actor, PrincipalRef: actor.PrincipalRef, OperationID: "operation:test", IdempotencyIdentity: "identity:test", AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), ContractDigest: "sha256:" + strings.Repeat("b", 64), RequestID: "request:test"}
	apply := func() error {
		return s.Transact(ctx, func(tx *store.Transaction) error { _, err := store.ApplyWorkflowActionTx(ctx, tx, req); return err })
	}
	if err := apply(); err != nil {
		t.Fatalf("delivery: %v", err)
	}
	var lifecycle, state, payload string
	if err := db.QueryRowContext(ctx, `SELECT w.version,w.lifecycle,i.instance_state,d.payload FROM work_items w JOIN workflow_instances i ON i.work_id=w.id JOIN workflow_delivery_state d ON d.work_id=w.id WHERE w.id='work:test'`).Scan(&req.ExpectedVersion, &lifecycle, &state, &payload); err != nil || lifecycle != "in_progress" || state != "running" || !strings.Contains(payload, "artifact:test") {
		t.Fatalf("delivery state: version=%d lifecycle=%s state=%s payload=%s err=%v", req.ExpectedVersion, lifecycle, state, payload, err)
	}
	firstVersion := req.ExpectedVersion
	req.OperationID, req.IdempotencyIdentity = "operation:replacement", "identity:replacement"
	req.Payload = json.RawMessage(`{"delivery_artifact":"artifact:replacement","delivery_state":"asserted"}`)
	if err := apply(); err != nil {
		t.Fatalf("replacement delivery: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT w.version,d.payload FROM work_items w JOIN workflow_delivery_state d ON d.work_id=w.id WHERE w.id='work:test'`).Scan(&req.ExpectedVersion, &payload); err != nil || req.ExpectedVersion <= firstVersion || !strings.Contains(payload, "artifact:replacement") {
		t.Fatalf("replacement state: version=%d payload=%s err=%v", req.ExpectedVersion, payload, err)
	}
}
