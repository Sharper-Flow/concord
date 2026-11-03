package store

import (
	"encoding/json"
	"testing"
)

func TestWorkflowCompletionIgnoresRetiredContractClauses(t *testing.T) {
	for _, fault := range []struct {
		name string
		sql  string
	}{
		{"required_evidence", `UPDATE workflow_contracts SET required_evidence='["native_run"]' WHERE work_id=?`},
		{"mandated_law", `UPDATE workflow_contracts SET spec_mandate='["law:absent"]',law_boundary_version=1 WHERE work_id=?`},
		{"verdict", `DELETE FROM workflow_predicate_verdicts WHERE work_id=?`},
		{"premise", `DELETE FROM workflow_premise_confirmations WHERE work_id=?`},
		{"changed_criteria_and_review", `UPDATE workflow_contract_predicates SET outcome_payload=json_set(outcome_payload,'$.expected_result','fail') WHERE work_id=?`},
	} {
		t.Run(fault.name, func(t *testing.T) {
			id := "retired-completion-" + fault.name
			f := qCurrentAcceptanceWithDelivery(t, id, "https://example.com/delivery/retired")
			s := f.store
			completeWorkflowPlanFixture(t, s, id)
			if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(fault.sql, id); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`DELETE FROM write_guard`); err != nil {
				t.Fatal(err)
			}
			if err := f.run(t.Context(), "complete", "complete", readWorkVersion(t, s, id), json.RawMessage(`{"impact_verdict":"non-breaking"}`), nil); err != nil {
				t.Fatalf("completion rechecked retired %s clause: %v", fault.name, err)
			}
			var lifecycle string
			if err := s.db.QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, id).Scan(&lifecycle); err != nil || lifecycle != "completed" {
				t.Fatalf("lifecycle=%q err=%v", lifecycle, err)
			}
		})
	}
}

func TestWorkflowCompletionIgnoresRetiredPayloadDrift(t *testing.T) {
	f := qCurrentAcceptanceWithDelivery(t, "completion-payload-drift", "https://example.com/delivery/drift")
	completeWorkflowPlanFixture(t, f.store, f.workID)
	if err := f.run(t.Context(), "complete", "complete", f.version(t.Context(), t), json.RawMessage(`{"impact_verdict":"non-breaking","evidence_commit":"commit:before","current_commit":"commit:after","payload":{"evidence_commit":"commit:before","current_commit":"commit:after","staleness":{"drifted":true,"severity":"block"}}}`), nil); err != nil {
		t.Fatalf("completion rechecked retired payload drift: %v", err)
	}
}

func TestWorkflowCompletionIgnoresRetiredModifyingScope(t *testing.T) {
	const id = "completion-modifying-scope"
	const target = "completion-outside-candidates"
	f := qCurrentAcceptanceWithDelivery(t, id, "https://example.com/delivery/scope")
	s := f.store
	completeWorkflowPlanFixture(t, s, id)
	seedWork(t, s, target)
	var candidates int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_candidate_sets WHERE work_id=? AND candidate_kind='work_item' AND candidate_ref=?`, id, target).Scan(&candidates); err != nil || candidates != 0 {
		t.Fatalf("outside target candidates=%d err=%v", candidates, err)
	}
	execInFold(t, s, `INSERT INTO workflow_impact_edges(work_id,edge_id,edge_kind,edge_class,target_work_id,target_kind,severity,recorded_at) VALUES('completion-modifying-scope','outside-modifying-edge','modifies','none','completion-outside-candidates','work_item','informational','now')`)
	if err := f.run(t.Context(), "complete", "complete", f.version(t.Context(), t), json.RawMessage(`{"impact_verdict":"non-breaking"}`), nil); err != nil {
		t.Fatalf("completion rechecked modifying-edge scope: %v", err)
	}
	var lifecycle string
	if err := s.db.QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, id).Scan(&lifecycle); err != nil || lifecycle != "completed" {
		t.Fatalf("lifecycle=%q err=%v", lifecycle, err)
	}
	delivery, err := workflowDeliveryAssertionRead(t.Context(), s.db, id)
	if err != nil || delivery == nil || delivery.Artifact != "https://example.com/delivery/scope" {
		t.Fatalf("delivery=%+v err=%v", delivery, err)
	}
}
