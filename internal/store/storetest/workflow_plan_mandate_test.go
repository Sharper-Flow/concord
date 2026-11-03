package storetest_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

func TestWorkflowPlanMandateReview(t *testing.T) {
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
	plan.Steps[0].Attempts = []store.WorkflowPlanAttempt{{Note: "planned", CompletedAt: "2026-01-01T00:00:00Z"}}
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
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('work:test','task','test','needed',0,1,'now','now');
INSERT INTO work_projects(work_id,project_id,role) VALUES('work:test','project:test','primary');
INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?1,'principal:test','client:test','agent:test','session:test','operator','now');
INSERT INTO workflow_instances(work_id,instance_state,workflow_type,step_plan) VALUES('work:test','running','change',?2);
INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work:test',1,'test','internal_sqlite','[]','[]','now',?1,'["law:test"]','[]',1,'prototype_internal');
INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('work:test',1,'predicate:test',0,'check','{"kind":"check"}');
DELETE FROM write_guard;`, actorRef, string(planJSON))
	if err != nil {
		t.Fatal(err)
	}
	candidate := store.ReviewCandidate{ProjectID: "project:test", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
	digest := sha256.Sum256([]byte(`{"kind":"check","payload":{"kind":"check"}}`))
	report := store.ReviewBlock{Disposition: "continue", InputCandidate: candidate, OutputCandidate: candidate, Dependencies: store.ReviewDependencies{Criteria: []store.ReviewCriterion{{PredicateID: "predicate:test", ContentDigest: fmt.Sprintf("sha256:%x", digest)}}, ArchitectureNotApplicable: true}, Assessments: []store.ReviewAssessment{}, Findings: []store.ReviewFinding{}, RemainingObligations: []store.ReviewObligation{}}
	payload, err := json.Marshal(map[string]any{"review": report})
	if err != nil {
		t.Fatal(err)
	}
	request := store.WorkflowActionExecutionRequest{WorkID: "work:test", ExpectedVersion: 1, ActionID: "record_review", Payload: payload, Actor: actor, PrincipalRef: actor.PrincipalRef, OperationID: "operation:test", IdempotencyIdentity: "identity:test", AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), ContractDigest: "sha256:" + strings.Repeat("b", 64), RequestID: "request:test"}
	apply := func(req store.WorkflowActionExecutionRequest) error {
		return s.Transact(ctx, func(tx *store.Transaction) error { _, err := store.ApplyWorkflowActionTx(ctx, tx, req); return err })
	}
	err = apply(request)
	var failure *store.Failure
	if !errors.As(err, &failure) || failure.Kind != store.KindMissingEvidence || !strings.Contains(failure.Detail, "law:test") {
		t.Fatalf("unbound review = %v, want missing mandated law", err)
	}
	var version, events int
	if err := db.QueryRowContext(ctx, `SELECT version,(SELECT count(*) FROM durable_operations WHERE work_id='work:test') FROM work_items WHERE id='work:test'`).Scan(&version, &events); err != nil || version != 1 || events != 0 {
		t.Fatalf("refusal effects: version=%d events=%d err=%v", version, events, err)
	}
	bind := request
	bind.ActionID = "bind_evidence"
	bind.Payload = json.RawMessage(`{"evidence_kind":"artifact","immutable_subject_ref":"law:test"}`)
	if err := apply(bind); err != nil {
		t.Fatalf("bind evidence: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work:test'`).Scan(&request.ExpectedVersion); err != nil {
		t.Fatal(err)
	}
	request.OperationID, request.IdempotencyIdentity = "operation:review", "identity:review"
	if err := apply(request); err != nil {
		t.Fatalf("bound review: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1);
UPDATE workflow_contracts SET spec_mandate='["law:test","law:added"]' WHERE work_id='work:test';
INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,home_domain_id,projection_hash) VALUES('work:test',1,'product:test','root','sha256:' || printf('%064d',0));
INSERT INTO workflow_law_addition_reservations(product_id,law_id,owner_work_id,owner_contract_version,home_domain_id) VALUES('product:test','law:added','work:test',1,'root');
INSERT INTO workflow_contract_law_additions(work_id,contract_version,product_id,law_id,home_domain_id,reservation_owner_work_id,reservation_owner_contract_version) VALUES('work:test',1,'product:test','law:added','root','work:test',1);
DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work:test'`).Scan(&request.ExpectedVersion); err != nil {
		t.Fatal(err)
	}
	request.OperationID, request.IdempotencyIdentity = "operation:review-addition", "identity:review-addition"
	if err := apply(request); err != nil {
		t.Fatalf("local review with reserved addition: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work:test'`).Scan(&request.ExpectedVersion); err != nil {
		t.Fatal(err)
	}
	advance := request
	advance.ActionID = "complete_step"
	advance.Payload = json.RawMessage(`{"step_id":"implement","note":"complete"}`)
	err = apply(advance)
	if !errors.As(err, &failure) || failure.Kind != store.KindMissingEvidence || !strings.Contains(failure.Detail, "law:added") {
		t.Fatalf("unbound added law advance = %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_contracts SET superseded_by=1 WHERE work_id='work:test'; DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work:test'`).Scan(&request.ExpectedVersion); err != nil {
		t.Fatal(err)
	}
	request.OperationID, request.IdempotencyIdentity = "operation:no-contract", "identity:no-contract"
	err = apply(request)
	if !errors.As(err, &failure) || failure.Kind != store.KindInvariantViolation || !strings.Contains(failure.Detail, "no active contract") {
		t.Fatalf("absent active contract = %v, want its own contract refusal", err)
	}
}
