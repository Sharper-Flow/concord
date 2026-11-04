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

func TestWorkflowPlanDeliveryAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, review, proof string
		required            bool
		want                store.FailureKind
	}{
		{"missing-review", "missing", "", true, store.KindMissingEvidence},
		{"stale-review", "stale", "", true, store.KindStaleRequiresReview},
		{"missing-proof", "ready", "", true, store.KindMissingEvidence},
		{"failed-proof", "ready", "fail", true, store.KindMissingEvidence},
		{"legacy-binding", "ready", "legacy", true, store.KindMissingEvidence},
		{"wrong-candidate", "ready", "other", true, store.KindStaleRequiresReview},
		{"passing-proof", "ready", "pass", true, ""},
		{"no-verification-required", "ready", "", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			plan.Steps[1].Attempts = []store.WorkflowPlanAttempt{{Note: "implemented", CompletedAt: "2026-01-01T00:00:00Z", Reviewer: "reviewer:test", Result: "pass", Commit: "commit:test", Link: "link:test"}}
			planJSON, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			actor := store.WorkflowActor{PrincipalRef: "principal:test", ClientRef: "client:test", AgentRef: "agent:test", SessionRef: "session:test", ActorClass: "operator"}
			actorRef, err := store.WorkflowActorRef(actor)
			if err != nil {
				t.Fatal(err)
			}
			required := `[]`
			if tc.required {
				required = `["verification"]`
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
INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work:test',1,'test','internal_sqlite',?3,'[]','now',?1,'[]','[]',1,'prototype_internal');
INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('work:test',1,'predicate:test',0,'check','{"check_ref":"check:test","expected_result":"pass","immutable_subject_ref":"commit:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","kind":"check"}');
DELETE FROM write_guard;`, actorRef, string(planJSON), required)
			if err != nil {
				t.Fatal(err)
			}
			candidate := store.ReviewCandidate{ProjectID: "project:test", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
			if tc.review != "missing" {
				digest := sha256.Sum256([]byte(`{"kind":"check","payload":{"check_ref":"check:test","expected_result":"pass","immutable_subject_ref":"commit:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","kind":"check"}}`))
				report := store.ReviewBlock{Disposition: "ready_for_final_proof", InputCandidate: candidate, OutputCandidate: candidate, Dependencies: store.ReviewDependencies{Criteria: []store.ReviewCriterion{{PredicateID: "predicate:test", ContentDigest: fmt.Sprintf("sha256:%x", digest)}}, ArchitectureNotApplicable: true}, Assessments: []store.ReviewAssessment{}, Findings: []store.ReviewFinding{}, RemainingObligations: []store.ReviewObligation{}}
				if tc.review == "stale" {
					candidate.Commit = strings.Repeat("c", 40)
				}
				facts, _ := json.Marshal(map[string]any{"candidate": candidate, "accepted": store.WorkflowAcceptedReview{ReportRef: "review:fixture", ReportDigest: "sha256:" + strings.Repeat("d", 64), ActorRef: actorRef, Report: report}, "findings": []any{}})
				if _, err := db.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1); INSERT INTO workflow_review_state(work_id,state) VALUES('work:test',?1); DELETE FROM write_guard;`, string(facts)); err != nil {
					t.Fatal(err)
				}
			}
			if tc.proof != "" {
				bindingFields := map[string]any{"evidence_kind": "verification", "immutable_subject_ref": "report:test"}
				if tc.proof != "legacy" {
					commit, result := strings.Repeat("a", 40), tc.proof
					if tc.proof == "other" {
						commit, result = strings.Repeat("c", 40), "pass"
					}
					bindingFields["verification_report"] = store.VerificationReport{Source: "local", Commit: commit, Result: result, RunRef: "run:synthetic"}
				}
				binding, _ := json.Marshal(bindingFields)
				if _, err := db.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1); INSERT INTO workflow_evidence_bindings(work_id,binding_seq,binding_id,actor,payload) VALUES('work:test',1,'binding:test',?1,?2); DELETE FROM write_guard;`, actorRef, string(binding)); err != nil {
					t.Fatal(err)
				}
			}
			req := store.WorkflowActionExecutionRequest{WorkID: "work:test", ExpectedVersion: 1, ActionID: "record_delivery", Payload: json.RawMessage(`{"delivery_artifact":"artifact:test","delivery_state":"asserted"}`), Actor: actor, PrincipalRef: actor.PrincipalRef, OperationID: "operation:test", IdempotencyIdentity: "identity:test", AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), ContractDigest: "sha256:" + strings.Repeat("b", 64), RequestID: "request:test"}
			err = s.Transact(ctx, func(tx *store.Transaction) error { _, err := store.ApplyWorkflowActionTx(ctx, tx, req); return err })
			var failure *store.Failure
			if tc.want == "" && err != nil {
				t.Fatalf("delivery: %v", err)
			}
			if tc.want == "" {
				var lifecycle, state string
				if err := db.QueryRowContext(ctx, `SELECT w.version,w.lifecycle,i.instance_state FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id='work:test'`).Scan(&req.ExpectedVersion, &lifecycle, &state); err != nil || lifecycle != "in_progress" || state != "running" {
					t.Fatalf("delivery changed lifecycle: %s %s %v", lifecycle, state, err)
				}
				req.OperationID, req.IdempotencyIdentity = "operation:replacement", "identity:replacement"
				req.Payload = json.RawMessage(`{"delivery_artifact":"artifact:replacement","delivery_state":"asserted"}`)
				if err := s.Transact(ctx, func(tx *store.Transaction) error { _, err := store.ApplyWorkflowActionTx(ctx, tx, req); return err }); err != nil {
					t.Fatalf("replacement delivery: %v", err)
				}
			}
			if tc.want != "" && (!errors.As(err, &failure) || failure.Kind != tc.want) {
				t.Fatalf("delivery = %v, want %s", err, tc.want)
			}
			if tc.want != "" {
				var version, count int
				if err := db.QueryRowContext(ctx, `SELECT version,(SELECT count(*) FROM durable_operations WHERE work_id='work:test') FROM work_items WHERE id='work:test'`).Scan(&version, &count); err != nil || version != 1 || count != 0 {
					t.Fatalf("refusal effects: version=%d count=%d err=%v", version, count, err)
				}
			}
			if tc.name == "missing-proof" {
				bind := req
				bind.ActionID, bind.OperationID, bind.IdempotencyIdentity = "bind_evidence", "operation:bind", "identity:bind"
				bind.Payload = json.RawMessage(`{"evidence_kind":"verification","immutable_subject_ref":"report:bound","verification_report":{"source":"local","commit":"` + strings.Repeat("a", 40) + `","result":"pass","run_ref":"run:synthetic"}}`)
				if err := s.Transact(ctx, func(tx *store.Transaction) error { _, err := store.ApplyWorkflowActionTx(ctx, tx, bind); return err }); err != nil {
					t.Fatalf("recovery binding: %v", err)
				}
				if err := db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work:test'`).Scan(&req.ExpectedVersion); err != nil {
					t.Fatal(err)
				}
				if err := s.Transact(ctx, func(tx *store.Transaction) error { _, err := store.ApplyWorkflowActionTx(ctx, tx, req); return err }); err != nil {
					t.Fatalf("delivery after binding: %v", err)
				}
			}
		})
	}
}
