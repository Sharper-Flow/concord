package store

import (
	"testing"
	"time"
)

func TestSmallFixPreparationDepthNeedsNoArchitectureDocument(t *testing.T) {
	for _, investigate := range []bool{false, true} {
		name := "bounded-diagnosis"
		if investigate {
			name = "additional-investigation"
		}
		t.Run(name, func(t *testing.T) {
			s := openTemp(t)
			const workID = "small-fix-preparation"
			seedWork(t, s, workID)
			seedWorkflowLaw(t, s)
			owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
			if err := s.Transact(t.Context(), func(tx *Transaction) error {
				return InitializeWorkflowTx(t.Context(), tx, WorkflowInitializationRequest{WorkID: workID, Actor: owner, Now: time.Unix(1, 0).UTC()})
			}); err != nil {
				t.Fatal(err)
			}
			seedWorkflowPlanFrontier(t, s, workID, WorkflowStepPlanFix, "diagnose")
			act := func(action string, fields map[string]any) {
				t.Helper()
				if err := runVerdictActionAs(t, s, workID, action, mustJSONValue(fields), 0, owner); err != nil {
					t.Fatalf("%s: %v", action, err)
				}
			}
			if investigate {
				act("checkpoint_context", map[string]any{
					"active_unit": "unit:repair", "hypothesis": "The documented value disagrees with the unchanged implementation.",
					"diagnosis": "Inspect the implementation and its caller before correcting the comment.", "strategy": "Keep the existing behavior and correct only the factual comment.",
					"touched_refs": []string{"pkg/value.go"}, "evidence_refs": []string{"reading:value"}, "pending_questions": []string{}, "pending_decisions": []string{},
				})
			}
			act("record_diagnosis", map[string]any{
				"reproduction": "The comment says eight; the function and check return seven.", "searched": "No related correction exists in the isolated backlog.",
				"outcome": "none_found", "root_cause": "The factual comment is incorrect; the existing architecture and behavior need no change.",
			})
			if workflowPlanFixtureFrontier(t, s, workID) != "diagnose" {
				t.Fatal("recording diagnosis advanced the frontier")
			}
			act("complete_step", map[string]any{"step_id": "diagnose", "note": "diagnosis completed"})
			act("approve_contract", map[string]any{
				"premise": "Correct the factual comment without changing behavior.", "decision_domains": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
				"outcome_predicates":   []map[string]any{{"predicate_id": "predicate:comment", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:comment", "immutable_subject_ref": "commit:comment", "expected_result": "pass"}}},
				"architecture_binding": WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}},
			})
			if workflowPlanFixtureFrontier(t, s, workID) != "plan" {
				t.Fatal("approving the contract advanced the plan frontier")
			}
			act("record_plan", map[string]any{
				"outcomes": "The comment matches the existing behavior.", "approach": "Correct the comment without changing the function.",
				"decisions": []map[string]any{{"id": "decision:scope", "question": "What needs correction?", "choice": "The comment", "rationale": "The implementation and check agree.", "rejected": []string{"Change the implementation"}}},
				"touches":   []string{"pkg/value.go"}, "merge_split": "One bounded fix", "risks": "The comment could misstate behavior again.",
			})
			if workflowPlanFixtureFrontier(t, s, workID) != "plan" {
				t.Fatal("recording the plan advanced the frontier")
			}
			act("complete_step", map[string]any{"step_id": "plan", "note": "plan completed"})
			if workflowPlanFixtureFrontier(t, s, workID) != "implement" {
				t.Fatal("small fix required an architecture document before implementation")
			}
			design, err := readCurrentWorkflowDesign(t.Context(), s.db, workID)
			if err != nil || design != nil {
				t.Fatalf("small fix acquired a redundant architecture document: %+v, %v", design, err)
			}
			var checkpoints int
			if err := s.db.QueryRow(`SELECT count(*) FROM workflow_context_checkpoints WHERE work_id=?`, workID).Scan(&checkpoints); err != nil {
				t.Fatal(err)
			}
			if (checkpoints == 1) != investigate {
				t.Fatalf("optional preparation checkpoint count=%d, additional investigation=%v", checkpoints, investigate)
			}
		})
	}
}
