package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// AJ4 — read the contract a correction replaced.
//
// bindAJ4HistoryOfASupersededContract approves contract v1, supersedes it
// with v2 through the operator-approved correction route, then reads
// concord_work_trace.history with contract_version 1. It proves the
// workflow summary answers v1 (not the active v2) with its successor and its
// own verified definition pin, that the approval and supersession events
// name the versions they made, that a version the work never had refuses
// unknown_scope, and that the reads append nothing to the log.
func bindAJ4HistoryOfASupersededContract(t *testing.T, _ jobScenario) jobObservation {
	t.Helper()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "product_read"})
	version := seedAgentWorkflow(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	for _, actionID := range []string{"record_proposal", "record_alignment", "record_design"} {
		invokeWorkflowIssue31Action(t, s, service, env, actionID, version, "aj4-history-"+actionID)
		version = workOneVersion(t, s)
	}
	approved, version := approvedWorkflowContract(t, s, service, grant, privateKey, env, version, workflowContractFieldsFixture(), "aj4-history-approve")
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approve_contract = %+v", approved.Error)
	}
	superseded, _ := approvedWorkflowActionWithVersions(t, s, service, grant, privateKey, env, version, "supersede_contract", map[string]any{
		"contract_version":     2,
		"premise":              "corrected premise",
		"architecture_binding": workflowArchitectureBindingFixture(),
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
	if superseded.Outcome != OutcomeOK {
		t.Fatalf("supersede_contract = %+v", superseded.Error)
	}

	history := func(contractVersion int) Envelope {
		raw, err := json.Marshal(map[string]any{
			"work_id":          "work-1",
			"event_kinds":      []string{store.WorkflowContractApproved, store.WorkflowContractSuperseded},
			"contract_version": contractVersion,
		})
		if err != nil {
			t.Fatal(err)
		}
		return dispatchRead(t, s, service, InvokeRequest{Tool: "concord_work_trace", Operation: "history", Input: raw}, env)
	}
	eventsBefore := domainEventCountForTest(t, s)
	resp := history(1)
	if resp.Outcome != OutcomeOK {
		t.Fatalf("history contract_version 1 = %+v", resp.Error)
	}
	var page struct {
		Events []struct {
			Kind                    string `json:"kind"`
			ContractVersion         *int   `json:"contract_version"`
			PreviousContractVersion *int   `json:"previous_contract_version"`
		} `json:"events"`
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
	events := []any{}
	for _, event := range page.Events {
		entry := event.Kind
		if event.ContractVersion != nil {
			entry += ":" + strconv.Itoa(*event.ContractVersion)
		}
		if event.PreviousContractVersion != nil {
			entry += "<-" + strconv.Itoa(*event.PreviousContractVersion)
		}
		events = append(events, entry)
	}
	contract := page.Workflow.Contract
	obs.Result = map[string]any{
		"contract_events":           events,
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
	if after := domainEventCountForTest(t, s); after != eventsBefore {
		obs.Effects["history_mutation"] = after - eventsBefore
	}
	return obs
}
