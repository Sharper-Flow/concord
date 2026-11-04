package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"

	"github.com/sharper-flow/concord/internal/store"
)

// payloadVariant carries an action's store and public payload together.
type payloadVariant struct {
	Payload       store.WorkflowPayloadDefinition `json:"payload"`
	PublicPayload store.WorkflowPayloadDefinition `json:"public_payload"`
}

type actionContract struct {
	ID string `json:"id"`
	// Variants carry the current catalog payload and public payload pair.
	Variants []payloadVariant `json:"variants"`
	// CrossField projects the same action policy core validation uses.
	CrossField store.WorkflowActionCrossField `json:"cross_field"`
}

type workflowOutcomeContract struct {
	Ref                  string   `json:"ref"`
	AllowedKinds         []string `json:"allowed_kinds"`
	AllowedOutcomeTokens []string `json:"allowed_outcome_tokens"`
}

// schemaRefProjection names the live catalog's payload schema refs.
type schemaRefProjection struct {
	Current []string `json:"current"`
}

type contractProjection struct {
	SchemaVersion      string                            `json:"schema_version"`
	Actions            []actionContract                  `json:"actions"`
	CatalogActionCount int                               `json:"catalog_action_count"`
	Workflows          []workflowOutcomeContract         `json:"workflows"`
	Teaching           store.WorkflowContractTeaching    `json:"teaching"`
	SchemaRefs         schemaRefProjection               `json:"schema_refs"`
	ContractUpdates    []store.WorkflowPayloadDefinition `json:"contract_updates"`
}

// canonicalPayload normalizes nil and empty field lists to the same contract.
func canonicalPayload(payload store.WorkflowPayloadDefinition) store.WorkflowPayloadDefinition {
	if payload.Fields == nil {
		payload.Fields = []store.WorkflowPayloadField{}
	}
	return payload
}

// canonicalCrossFieldForAction projects the engine cross-field declaration
// onto the exact field set every authoring payload half of the action
// declares. The projected rules must agree across all
// halves, or the action's core and public contracts would publish
// different rules from one declaration.
func canonicalCrossFieldForAction(actionID string, variants []payloadVariant) (store.WorkflowActionCrossField, error) {
	first := true
	var projected store.WorkflowActionCrossField
	for _, variant := range variants {
		for _, payload := range []store.WorkflowPayloadDefinition{variant.Payload, variant.PublicPayload} {
			cross, err := store.WorkflowActionCanonicalCrossFieldRules(actionID, payload.Fields)
			if err != nil {
				return store.WorkflowActionCrossField{}, err
			}
			if first {
				projected, first = cross, false
				continue
			}
			if !reflect.DeepEqual(projected, cross) {
				return store.WorkflowActionCrossField{}, fmt.Errorf("action %s carries inconsistent canonical cross-field declarations across its authoring payloads", actionID)
			}
		}
	}
	return projected, nil
}

// payloadSchemaRefs collects the distinct schema_ref/item_ref names one
// payload declaration carries, in sorted order.
func payloadSchemaRefs(payload store.WorkflowPayloadDefinition) []string {
	refs := map[string]bool{}
	for _, field := range payload.Fields {
		if field.SchemaRef != "" {
			refs[field.SchemaRef] = true
		}
		if field.ItemRef != "" {
			refs[field.ItemRef] = true
		}
	}
	return sortedRefNames(refs)
}

func sortedRefNames(refs map[string]bool) []string {
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func main() {
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "workflow action projection accepts no arguments")
		os.Exit(2)
	}
	var templates struct {
		Types []struct {
			ID    string `json:"id"`
			Steps []struct {
				Actions []string `json:"actions"`
			} `json:"steps"`
		} `json:"types"`
	}
	data, err := os.ReadFile("contracts/workflow-step-templates.v1.json")
	if err == nil {
		err = json.Unmarshal(data, &templates)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	actionIDs := map[string]bool{"record_intake": true, "record_clarify": true, "pick_type": true, "insert_step": true}
	for _, template := range templates.Types {
		for _, step := range template.Steps {
			for _, id := range step.Actions {
				actionIDs[id] = true
			}
		}
	}
	contracts := make(map[string]actionContract, len(actionIDs))
	refs := map[string]bool{}
	for id := range actionIDs {
		action, err := store.WorkflowPlanActionContract(id)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		public := action.Payload
		if action.PublicPayload != nil {
			public = *action.PublicPayload
		}
		variant := payloadVariant{canonicalPayload(action.Payload), canonicalPayload(public)}
		cross, err := canonicalCrossFieldForAction(id, []payloadVariant{variant})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		contracts[id] = actionContract{ID: id, Variants: []payloadVariant{variant}, CrossField: cross}
		for _, payload := range []store.WorkflowPayloadDefinition{variant.Payload, variant.PublicPayload} {
			for _, ref := range payloadSchemaRefs(payload) {
				refs[ref] = true
			}
		}
	}
	ids := make([]string, 0, len(contracts))
	for id := range contracts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	update := store.WorkflowPlanContractUpdatePayload()
	for _, ref := range payloadSchemaRefs(update) {
		refs[ref] = true
	}
	projection := contractProjection{SchemaVersion: "2.0", Actions: make([]actionContract, 0, len(ids)), CatalogActionCount: store.WorkflowActionCatalogSize(), Teaching: store.WorkflowContractTeachingRules(), SchemaRefs: schemaRefProjection{Current: sortedRefNames(refs)}, ContractUpdates: []store.WorkflowPayloadDefinition{update}}
	for _, template := range templates.Types {
		kinds, tokens := store.WorkflowPlanAllowedPredicates(store.WorkflowStepPlanType(template.ID))
		kindNames := make([]string, 0, len(kinds))
		for _, kind := range kinds {
			kindNames = append(kindNames, string(kind))
		}
		projection.Workflows = append(projection.Workflows, workflowOutcomeContract{Ref: template.ID, AllowedKinds: kindNames, AllowedOutcomeTokens: tokens})
	}
	for _, id := range ids {
		projection.Actions = append(projection.Actions, contracts[id])
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(projection); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
