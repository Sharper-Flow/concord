package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

// AJ4 — read the contract a correction replaced.
//
// bindAJ4HistoryOfASupersededContract approves contract v1, supersedes it
// with v2 through the operator-approved correction route, then reads
// concord_work_trace.history with contract_version 1. It proves the
// workflow summary answers v1 (not the active v2) with its successor and its
// own verified definition pin, that a version the work never had refuses
// unknown_scope, and that the reads leave the store revision unchanged.
func bindAJ4HistoryOfASupersededContract(t *testing.T, _ jobScenario) jobObservation {
	t.Helper()
	s, service, grant, privateKey := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition", "product_read"})
	version := seedAgentWorkflow(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	for _, actionID := range []string{"record_proposal", "record_alignment"} {
		invokeWorkflowIssue31Action(t, s, service, env, actionID, version, "aj4-history-"+actionID)
		version = workOneVersion(t, s)
	}
	approved, _ := approvedWorkflowContract(t, s, service, grant, privateKey, env, version, workflowContractFieldsForSeededRegistry(t, s), "aj4-history-approve")
	if approved.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("approve_contract = %+v", approved.Error)
	}
	version = recordApprovedWorkDesign(t, s, service, env, "work-1", "aj4-history-design")
	superseded, _ := approvedWorkflowActionWithVersions(t, s, service, grant, privateKey, env, version, "supersede_contract", map[string]any{
		"contract_version":     2,
		"premise":              "corrected premise",
		"architecture_binding": workflowArchitectureBindingForSeededRegistry(t, s),
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:workflow", "expected_result": "pass"},
		}},
		"required_evidence": []string{"verification"},
		"route_conventions": []string{},
		"spec_mandate":      []string{},
		"law_modifies":      []string{},
		"rigor_class":       "prototype_internal",
		"supersede_reason":  "correct the accepted premise",
		"audit_evidence":    []string{"evidence:aj4-history"},
		"decision_domains":  []string{},
		"design_record": map[string]any{
			"approach":     "corrected design",
			"decisions":    []map[string]any{{"id": "decision:corrected", "question": "Which approach?", "choice": "corrected", "rationale": "Matches the approved objective", "rejected": []string{"original"}}},
			"touched_refs": []string{"src/work.go"},
		},
	}, "aj4-history-supersede", map[string]any{"work": version, "contract": 1})
	if superseded.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("supersede_contract = %+v", superseded.Error)
	}

	history := func(contractVersion int) agentcontract.Envelope {
		raw, err := json.Marshal(map[string]any{
			"work_id":          "work-1",
			"record_kinds":     []string{"lifecycle"},
			"contract_version": contractVersion,
		})
		if err != nil {
			t.Fatal(err)
		}
		return dispatchRead(t, s, service, InvokeRequest{Tool: "concord_work_trace", Operation: "history", Input: raw}, env)
	}
	revisionBefore, err := s.Revision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	resp := history(1)
	if resp.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("history contract_version 1 = %+v", resp.Error)
	}
	var page struct {
		Entries []struct {
			Kind string `json:"entry_kind"`
		} `json:"entries"`
		Workflow struct {
			Contract struct {
				Version       int  `json:"version"`
				SupersededBy  *int `json:"superseded_by"`
				DefinitionPin *struct {
					Verified bool `json:"verified"`
				} `json:"definition_pin"`
			} `json:"contract"`
		} `json:"workflow"`
	}
	if err := json.Unmarshal(resp.Result, &page); err != nil {
		t.Fatalf("unmarshal history page: %v", err)
	}

	obs := envelopeToObservation(resp)
	kinds := []any{}
	for _, entry := range page.Entries {
		kinds = append(kinds, entry.Kind)
	}
	contract := page.Workflow.Contract
	obs.Result = map[string]any{
		"entry_kinds":               kinds,
		"selected_contract_version": contract.Version,
		"definition_pin_verified":   contract.DefinitionPin != nil && contract.DefinitionPin.Verified,
	}
	if contract.SupersededBy != nil {
		obs.Result["selected_contract_superseded_by"] = *contract.SupersededBy
	} else {
		obs.Result["selected_contract_superseded_by"] = probedAbsent{}
	}

	refused := history(9)
	obs.Communication["unknown_version_error_kind"] = probedAbsent{}
	if refused.Error != nil {
		obs.Communication["unknown_version_error_kind"] = refused.Error.Kind
	}
	obs.Effects = map[string]any{"history_mutation": probedAbsent{}}
	after, err := s.Revision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after != revisionBefore {
		obs.Effects["history_mutation"] = after - revisionBefore
	}
	return obs
}
