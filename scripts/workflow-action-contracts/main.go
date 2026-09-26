package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"

	"github.com/sharper-flow/concord/internal/store"
)

type actionContract struct {
	ID             string                            `json:"id"`
	Payload        store.WorkflowPayloadDefinition   `json:"payload"`
	PublicPayload  store.WorkflowPayloadDefinition   `json:"public_payload"`
	LegacyPayloads []store.WorkflowPayloadDefinition `json:"legacy_payloads"`
}

type workflowOutcomeContract struct {
	Ref                    string   `json:"ref"`
	AllowedKinds           []string `json:"allowed_kinds"`
	AllowedOutcomeTokens   []string `json:"allowed_outcome_tokens"`
	DecisionRecordRequired bool     `json:"decision_record_required"`
}

type contractProjection struct {
	SchemaVersion string                    `json:"schema_version"`
	Actions       []actionContract          `json:"actions"`
	Workflows     []workflowOutcomeContract `json:"workflows"`
}

func main() {
	payloads := map[string]actionContract{}
	addAction := func(action store.WorkflowActionDefinition) {
		publicPayload := action.Payload
		if action.PublicPayload != nil {
			publicPayload = *action.PublicPayload
		}
		contract := actionContract{ID: action.ID, Payload: action.Payload, PublicPayload: publicPayload, LegacyPayloads: []store.WorkflowPayloadDefinition{}}
		if previous, ok := payloads[action.ID]; ok && !reflect.DeepEqual(previous, contract) {
			fmt.Fprintf(os.Stderr, "action %s has inconsistent current payload contracts\n", action.ID)
			os.Exit(1)
		}
		payloads[action.ID] = contract
	}
	for _, definition := range store.BuiltinWorkflowDefinitions() {
		for _, action := range definition.ActionDefinitions {
			addAction(action)
		}
	}
	for _, action := range store.BuiltinWorkflowRecoveryActionDefinitions() {
		addAction(action)
	}
	for _, definition := range store.BuiltinWorkflowDefinitionsWithHistory() {
		for _, action := range definition.ActionDefinitions {
			contract, ok := payloads[action.ID]
			if !ok || !action.Payload.Closed || len(action.Payload.Fields) != 0 {
				continue
			}
			if reflect.DeepEqual(contract.Payload, action.Payload) {
				continue
			}
			seen := false
			for _, legacy := range contract.LegacyPayloads {
				if reflect.DeepEqual(legacy, action.Payload) {
					seen = true
					break
				}
			}
			if !seen {
				contract.LegacyPayloads = append(contract.LegacyPayloads, action.Payload)
				payloads[action.ID] = contract
			}
		}
	}
	ids := make([]string, 0, len(payloads))
	for id := range payloads {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	definitions := store.BuiltinWorkflowDefinitions()
	refs := make([]string, 0, len(definitions))
	byRef := map[string]store.WorkflowDefinition{}
	for _, definition := range definitions {
		if _, ok := byRef[definition.Ref]; ok {
			fmt.Fprintf(os.Stderr, "workflow %s has inconsistent outcome schema contracts\n", definition.Ref)
			os.Exit(1)
		}
		byRef[definition.Ref] = definition
		refs = append(refs, definition.Ref)
	}
	sort.Strings(refs)
	workflows := make([]workflowOutcomeContract, 0, len(refs))
	for _, ref := range refs {
		definition := byRef[ref]
		kinds := make([]string, 0, len(definition.OutcomeSchema.AllowedKinds))
		for _, kind := range definition.OutcomeSchema.AllowedKinds {
			kinds = append(kinds, string(kind))
		}
		tokens := definition.OutcomeSchema.AllowedOutcomeTokens
		if tokens == nil {
			tokens = []string{}
		}
		workflows = append(workflows, workflowOutcomeContract{
			Ref:                    ref,
			AllowedKinds:           kinds,
			AllowedOutcomeTokens:   tokens,
			DecisionRecordRequired: definition.OutcomeSchema.DecisionRecordRequired,
		})
	}
	projection := contractProjection{SchemaVersion: "1.0", Actions: make([]actionContract, 0, len(ids)), Workflows: workflows}
	for _, id := range ids {
		projection.Actions = append(projection.Actions, payloads[id])
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(projection); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
