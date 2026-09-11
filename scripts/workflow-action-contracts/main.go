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

type contractProjection struct {
	SchemaVersion string           `json:"schema_version"`
	Actions       []actionContract `json:"actions"`
}

func main() {
	payloads := map[string]actionContract{}
	for _, definition := range store.BuiltinWorkflowDefinitions() {
		for _, action := range definition.ActionDefinitions {
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
	projection := contractProjection{SchemaVersion: "1.0", Actions: make([]actionContract, 0, len(ids))}
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
