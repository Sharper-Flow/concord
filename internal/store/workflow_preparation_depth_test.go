package store

import (
	"strings"
	"testing"
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
			seedIssue31DomainRegistry(t, s)
			definition, err := LiveWorkflowDefinitionForRef("workflow.break_fix")
			if err != nil {
				t.Fatal(err)
			}
			livenessInitialize(t, s, workID, definition.Definition)
			act := func(action string, fields map[string]any) {
				t.Helper()
				if err := runApprovalActionTx(t, s, BuiltinWorkflowRegistry(), workID, action, livenessActor(), nil, "", "", string(mustJSONValue(fields))); err != nil {
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
			if readInstanceStep(t, s, workID) != "planning" {
				t.Fatal("agent-selected preparation depth did not preserve the required diagnosis exit")
			}
			act("approve_contract", map[string]any{
				"premise": "Correct the factual comment without changing behavior.", "decision_domains": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
				"outcome_predicates":   []map[string]any{{"predicate_id": "predicate:comment", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:comment", "immutable_subject_ref": "commit:comment", "expected_result": "pass"}}},
				"architecture_binding": WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}},
			})
			if readInstanceStep(t, s, workID) != "repair" {
				t.Fatal("small fix required an architecture document before entering repair")
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
