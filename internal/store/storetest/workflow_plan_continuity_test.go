package storetest_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

func TestWorkflowPlanCheckpointContinuity(t *testing.T) {
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	db := s.DatabaseForTesting()
	if err := pm1fixture.SeedProductAndProject(ctx, s, "product:continuity", "project:continuity"); err != nil {
		t.Fatal(err)
	}
	if err := pm1fixture.SeedWorkItem(ctx, s, "project:continuity", "work:continuity", "Continuity", 0); err != nil {
		t.Fatal(err)
	}
	repo, _, err := pm1fixture.DomainEvidenceRepo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1);
INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('locator:continuity','project:continuity','canonical_path',?1,?1,'now','now');
INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('product:continuity','project:continuity','locator:continuity');
DELETE FROM write_guard`, repo); err != nil {
		t.Fatal(err)
	}
	actor := store.WorkflowActor{PrincipalRef: "principal:test", ClientRef: "client:test", AgentRef: "agent:test", SessionRef: "session:test", ActorClass: "operator"}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work:continuity", Actor: actor, Now: time.Unix(100, 0).UTC()})
	}); err != nil {
		t.Fatal(err)
	}
	seq := 0
	apply := func(action string, fields any) {
		t.Helper()
		seq++
		var version int64
		if err := db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work:continuity'`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		req := store.WorkflowActionExecutionRequest{WorkID: "work:continuity", ExpectedVersion: version, ActionID: action, Payload: payload, Actor: actor, PrincipalRef: actor.PrincipalRef, OperationID: fmt.Sprintf("operation:%d", seq), IdempotencyIdentity: fmt.Sprintf("identity:%d", seq), RequestID: fmt.Sprintf("request:%d", seq), AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), ContractDigest: "sha256:" + strings.Repeat("b", 64), Now: time.Unix(100+int64(seq), 0).UTC()}
		if err := s.Transact(ctx, func(tx *store.Transaction) error { _, err := store.ApplyWorkflowActionTx(ctx, tx, req); return err }); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	apply("record_intake", map[string]any{"outcome": "proceed", "open_questions_found": false})
	apply("pick_type", map[string]any{"type": "change", "reason": "bounded change"})
	ctx, err = s.EstablishKnowledgeSourceSetProof(ctx, "work:continuity")
	if err != nil {
		t.Fatal(err)
	}
	apply("record_plan", map[string]any{"outcomes": "Working continuity", "approach": "Preserve context", "decisions": []any{map[string]any{"id": "decision:test", "question": "What to preserve?", "choice": "Context", "rationale": "Continuity", "rejected": []string{}}}, "touches": []string{"path:test"}, "merge_split": "One change", "risks": "Stored context"})
	apply("approve_contract", map[string]any{"premise": "bounded change", "outcome_predicates": []any{map[string]any{"predicate_id": "predicate:test", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:test", "immutable_subject_ref": "commit:test", "expected_result": "pass"}}}, "spec_mandate": []string{}, "law_modifies": []string{}, "decision_domains": []string{}, "architecture_binding": map[string]any{"home_domain_id": pm1fixture.SingleDomainRootID, "affected_domain_ids": []string{pm1fixture.SingleDomainRootID}, "domain_modifies": []string{}, "domain_relation_modifies": []any{}, "law_additions": []any{}, "verification_obligations": []any{}}})
	apply("complete_step", map[string]any{"step_id": "plan", "note": "planned"})
	apply("start_execution", map[string]any{})
	apply("record_work_context", map[string]any{})
	apply("checkpoint_context", map[string]any{"checkpoint_id": "checkpoint:test", "active_unit": "implementation", "hypothesis": "hypothesis", "diagnosis": "diagnosis", "strategy": "strategy", "touched_refs": []string{"path:test"}, "evidence_refs": []string{"evidence:test"}, "pending_questions": []string{}, "pending_decisions": []string{}})
	apply("cross_context_boundary", map[string]any{"boundary_kind": "summary", "mode": "summary", "checkpoint_id": "checkpoint:test", "summary": "Continue the work."})
	var checkpoints, boundaries int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM workflow_context_checkpoints WHERE work_id='work:continuity'),(SELECT count(*) FROM workflow_context_boundaries WHERE work_id='work:continuity')`).Scan(&checkpoints, &boundaries); err != nil || checkpoints != 1 || boundaries != 1 {
		t.Fatalf("continuity rows: %d %d %v", checkpoints, boundaries, err)
	}
	snapshot, err := store.ReadWorkflowContinuity(ctx, s, store.ContinuityRequest{Work: "work:continuity"})
	if err != nil || snapshot.LatestCheckpoint == nil || snapshot.LatestCheckpoint.Hypothesis != "hypothesis" || snapshot.BoundaryCount != 1 || len(snapshot.Boundaries) != 1 || snapshot.Boundaries[0].Summary != "Continue the work." {
		t.Fatalf("continuity: %+v, %v", snapshot, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE workflow_context_checkpoints SET hypothesis='changed' WHERE work_id='work:continuity'`); err == nil {
		t.Fatal("checkpoint update bypassed immutability")
	}
}
