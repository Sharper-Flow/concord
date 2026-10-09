package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"

	"github.com/sharper-flow/concord/internal/store"
)

// payloadVariant is one exact closed contract an action admits. A variant
// carries the store-side payload and the public authoring payload together:
// the pair is what a registering definition declared, so the projected
// alternative never mixes halves of two definitions.
type payloadVariant struct {
	Payload       store.WorkflowPayloadDefinition `json:"payload"`
	PublicPayload store.WorkflowPayloadDefinition `json:"public_payload"`
}

type actionContract struct {
	ID string `json:"id"`
	// Variants are the exact closed payload alternatives the action's
	// current contracts declare: one per distinct (payload, public payload)
	// pair among the current definitions and the engine-owned recovery
	// list. Variants may differ only by whole fields; every field two
	// variants both name must be declared identically, or collection
	// refuses. The published authoring schema offers the variants as
	// alternatives; the store still validates each call against the exact
	// definition version the work item pins.
	Variants []payloadVariant `json:"variants"`
	// LegacyPayloads are the closed shapes retained versions declared that
	// stay authorable because live work items pin those versions. A
	// retained shape is admitted only when every field it shares with a
	// current variant is declared identically; conflicting retained shapes
	// were never part of the current authoring surface and stay
	// unauthorable. The closed-empty shapes are the pre-contract open
	// payload era and keep their historical handling.
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

// canonicalPayload normalizes a payload for comparison: a nil field list
// and an empty field list declare the same contract, and definitions built
// by different eras of the chain builders express both.
func canonicalPayload(payload store.WorkflowPayloadDefinition) store.WorkflowPayloadDefinition {
	if payload.Fields == nil {
		payload.Fields = []store.WorkflowPayloadField{}
	}
	return payload
}

// samePayload reports whether two declarations describe one contract.
func samePayload(a, b store.WorkflowPayloadDefinition) bool {
	return reflect.DeepEqual(canonicalPayload(a), canonicalPayload(b))
}

// conflictingFieldName returns the first field both payloads name with
// different declarations, or "" when every shared field agrees. Variants
// are valid exactly when this is empty: a versioned action contract may
// add or drop whole fields, never redeclare a shared one, so a conflict is
// an authoring error the projection must refuse rather than merge.
func conflictingFieldName(a, b store.WorkflowPayloadDefinition) string {
	for _, left := range a.Fields {
		for _, right := range b.Fields {
			if left.Name == right.Name && !reflect.DeepEqual(left, right) {
				return left.Name
			}
		}
	}
	return ""
}

// collectActionContracts folds the registered definitions into the
// projection's action contracts. Current definitions and the recovery list
// contribute variants; retained versions contribute legacy payloads under
// the compatibility rule above. Order is deterministic: variants keep
// registration order (current families, then the recovery list), legacy
// payloads keep chain order.
func collectActionContracts(current []store.WorkflowDefinition, recovery []store.WorkflowActionDefinition, history []store.WorkflowDefinition) (map[string]actionContract, error) {
	contracts := map[string]actionContract{}
	addCurrent := func(action store.WorkflowActionDefinition) error {
		public := action.Payload
		if action.PublicPayload != nil {
			public = *action.PublicPayload
		}
		variant := payloadVariant{Payload: canonicalPayload(action.Payload), PublicPayload: canonicalPayload(public)}
		contract, ok := contracts[action.ID]
		if !ok {
			contract = actionContract{ID: action.ID, Variants: []payloadVariant{}, LegacyPayloads: []store.WorkflowPayloadDefinition{}}
		}
		for _, existing := range contract.Variants {
			if reflect.DeepEqual(existing, variant) {
				contracts[action.ID] = contract
				return nil
			}
			if field := conflictingFieldName(existing.Payload, variant.Payload); field != "" {
				return fmt.Errorf("action %s has inconsistent current payload contracts: field %s is declared differently across current definitions", action.ID, field)
			}
			if field := conflictingFieldName(existing.PublicPayload, variant.PublicPayload); field != "" {
				return fmt.Errorf("action %s has inconsistent current payload contracts: field %s is declared differently across current public payload contracts", action.ID, field)
			}
		}
		contract.Variants = append(contract.Variants, variant)
		contracts[action.ID] = contract
		return nil
	}
	for _, definition := range current {
		for _, action := range definition.ActionDefinitions {
			if err := addCurrent(action); err != nil {
				return nil, err
			}
		}
	}
	for _, action := range recovery {
		if err := addCurrent(action); err != nil {
			return nil, err
		}
	}
	for _, definition := range history {
		for _, action := range definition.ActionDefinitions {
			contract, ok := contracts[action.ID]
			if !ok || !action.Payload.Closed {
				continue
			}
			seen := false
			for _, variant := range contract.Variants {
				if samePayload(variant.Payload, action.Payload) {
					seen = true
					break
				}
			}
			for _, legacy := range contract.LegacyPayloads {
				if samePayload(legacy, action.Payload) {
					seen = true
					break
				}
			}
			if seen {
				continue
			}
			if len(action.Payload.Fields) != 0 {
				// A retained shape rides the authoring surface only when
				// it is a whole-field variant of every current contract:
				// shared fields declared identically. A conflicting
				// retained shape predates the current contract and was
				// never authorable through it; it stays unauthorable.
				compatible := true
				for _, variant := range contract.Variants {
					if field := conflictingFieldName(variant.Payload, action.Payload); field != "" {
						compatible = false
						break
					}
				}
				if !compatible {
					continue
				}
			}
			contract.LegacyPayloads = append(contract.LegacyPayloads, canonicalPayload(action.Payload))
			contracts[action.ID] = contract
		}
	}
	return contracts, nil
}

func main() {
	contracts, err := collectActionContracts(store.BuiltinWorkflowDefinitions(), store.BuiltinWorkflowRecoveryActionDefinitions(), store.BuiltinWorkflowDefinitionsWithHistory())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ids := make([]string, 0, len(contracts))
	for id := range contracts {
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
		projection.Actions = append(projection.Actions, contracts[id])
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(projection); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
