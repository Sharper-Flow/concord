package storetest_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

func TestWorkflowPlanMandateCompletion(t *testing.T) {
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
	for i := range plan.Steps {
		attempt := store.WorkflowPlanAttempt{Note: "completed", CompletedAt: "2026-01-01T00:00:00Z"}
		if plan.Steps[i].Reviewed {
			attempt.Reviewer, attempt.Result, attempt.Commit, attempt.Link = "reviewer:test", "pass", "commit:test", "link:test"
		}
		plan.Steps[i].Attempts = []store.WorkflowPlanAttempt{attempt}
	}
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
INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project:completion','test',1,'now','now');
INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product:completion','test','prototype','operator_only',1,'now','now');
INSERT INTO product_projects(product_id,project_id,role) VALUES('product:completion','project:completion','primary');
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('work:completion','task','test','in_progress',0,1,'now','now');
INSERT INTO work_projects(work_id,project_id,role) VALUES('work:completion','project:completion','primary');
INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?1,'principal:test','client:test','agent:test','session:test','operator','now');
INSERT INTO workflow_instances(work_id,instance_state,workflow_type,step_plan) VALUES('work:completion','running','change',?2);
INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work:completion',1,'test','internal_sqlite','["verification"]','[]','now',?1,'["law:unbound"]','[]',1,'prototype_internal');
INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('work:completion',1,'predicate:test',0,'check','{"kind":"check","check_ref":"check:test","immutable_subject_ref":"commit:test","expected_result":"pass"}');
INSERT INTO workflow_premise_confirmations(work_id,contract_version,confirmed_by,confirmed_at) VALUES('work:completion',1,?1,'now');
INSERT INTO workflow_delivery_state(work_id,assertion_seq,assertion_id,actor,occurred_at,payload_version,payload) VALUES('work:completion',1,'delivery:completion',?1,'now',1,'{"delivery_artifact":"https://example.com/delivery","delivery_state":"asserted"}');
DELETE FROM write_guard;`, actorRef, string(planJSON))
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"impact_verdict":"non-breaking"}`)
	preflight := store.WorkflowActionPreflightRequest{WorkID: "work:completion", ExpectedVersion: 1, ActionID: "complete", Payload: payload, Actor: actor}
	if _, err := store.WorkflowActionDefinitionFor(ctx, s, preflight); err != nil {
		t.Fatalf("completion preflight: %v", err)
	}
	req := store.WorkflowActionExecutionRequest{WorkID: preflight.WorkID, ExpectedVersion: 1, ActionID: "complete", Payload: payload, Actor: actor, PrincipalRef: actor.PrincipalRef, OperationID: "operation:completion", IdempotencyIdentity: "identity:completion", AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), ContractDigest: "sha256:" + strings.Repeat("b", 64), RequestID: "request:completion"}
	apply := func(r store.WorkflowActionExecutionRequest) error {
		return s.Transact(ctx, func(tx *store.Transaction) error { _, err := store.ApplyWorkflowActionTx(ctx, tx, r); return err })
	}
	refused := func(label string, r store.WorkflowActionExecutionRequest) {
		t.Helper()
		if err := apply(r); err == nil {
			t.Fatalf("%s completion was accepted", label)
		}
		var version, operations int
		if err := db.QueryRowContext(ctx, `SELECT version,(SELECT count(*) FROM durable_operations WHERE work_id='work:completion') FROM work_items WHERE id='work:completion'`).Scan(&version, &operations); err != nil || version != 1 || operations != 0 {
			t.Fatalf("%s refusal changed state: version=%d operations=%d err=%v", label, version, operations, err)
		}
	}
	stale := req
	stale.ExpectedVersion = 2
	refused("stale version", stale)
	invalidActor := req
	invalidActor.Actor = store.WorkflowActor{}
	refused("invalid actor", invalidActor)
	plan.Steps[len(plan.Steps)-1].Attempts = nil
	incompleteJSON, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET step_plan=?1 WHERE work_id='work:completion'; DELETE FROM write_guard;`, string(incompleteJSON)); err != nil {
		t.Fatal(err)
	}
	refused("incomplete plan", req)
	if _, err := db.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET step_plan=?1 WHERE work_id='work:completion'; UPDATE workflow_contracts SET superseded_by=1 WHERE work_id='work:completion'; DELETE FROM write_guard;`, string(planJSON)); err != nil {
		t.Fatal(err)
	}
	refused("no active contract", req)
	if _, err := db.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_contracts SET superseded_by=NULL WHERE work_id='work:completion'; DELETE FROM write_guard;`); err != nil {
		t.Fatal(err)
	}
	if err := apply(req); err != nil {
		t.Fatalf("completion: %v", err)
	}
	var lifecycle, state string
	if err := db.QueryRowContext(ctx, `SELECT w.lifecycle,i.instance_state FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id='work:completion'`).Scan(&lifecycle, &state); err != nil || lifecycle != "completed" || state != "completed" {
		t.Fatalf("terminal state %q %q: %v", lifecycle, state, err)
	}
	var completion string
	if err := db.QueryRowContext(ctx, `SELECT completion_payload FROM workflow_instances WHERE work_id='work:completion'`).Scan(&completion); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(completion), &fields); err != nil {
		t.Fatal(err)
	}
	if value, ok := fields["final_verdict_kind"]; ok {
		t.Fatalf("completion invented final verdict: %s", value)
	}
	verdict, err := store.ReadWorkflowVerdict(ctx, s, "work:completion")
	if err != nil || verdict == nil {
		t.Fatalf("read completion metadata: %v", err)
	}
	visible, err := json.Marshal(verdict)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(visible), "final_verdict_kind") {
		t.Fatalf("completion read invented final verdict: %s", visible)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET completion_payload=json_set(completion_payload,'$.final_verdict_kind','outcome_mismatch') WHERE work_id='work:completion'; DELETE FROM write_guard;`); err != nil {
		t.Fatal(err)
	}
	verdict, err = store.ReadWorkflowVerdict(ctx, s, "work:completion")
	if err != nil || verdict == nil || verdict.FinalVerdictKind != "outcome_mismatch" {
		t.Fatalf("historical completion verdict: %+v, %v", verdict, err)
	}
}
