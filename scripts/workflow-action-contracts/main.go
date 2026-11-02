package main

import (
	"encoding/json"
	"fmt"
	"io"
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
	// pair among the live definition heads and the engine-owned recovery
	// list. Variants may differ only by whole fields; every field two
	// variants both name must be declared identically, or collection
	// refuses. The published authoring schema offers the variants as
	// alternatives; the store still validates each call against the exact
	// definition version the work item pins.
	Variants []payloadVariant `json:"variants"`
	// CrossField projects the engine registry's cross-field declaration for
	// this action — the same single declaration workflowActionCrossField
	// resolves for core validation. It rides the action contract, never the
	// digest-covered payload definition (CON-412).
	CrossField store.WorkflowActionCrossField `json:"cross_field"`
}

type workflowOutcomeContract struct {
	Ref                    string   `json:"ref"`
	AllowedKinds           []string `json:"allowed_kinds"`
	AllowedOutcomeTokens   []string `json:"allowed_outcome_tokens"`
	DecisionRecordRequired bool     `json:"decision_record_required"`
}

// schemaRefProjection names the payload schema refs the registry declares,
// split by decoding owner. Current refs belong to live authoring and resolve
// through the public authoring document. Historical refs belong to retained
// registry versions and resolve through the frozen historical decoder
// document, so a later public authoring change cannot break historical
// payload validation.
type schemaRefProjection struct {
	Current []string `json:"current"`
}

type contractProjection struct {
	SchemaVersion   string                            `json:"schema_version"`
	Actions         []actionContract                  `json:"actions"`
	Workflows       []workflowOutcomeContract         `json:"workflows"`
	Teaching        store.WorkflowContractTeaching    `json:"teaching"`
	SchemaRefs      schemaRefProjection               `json:"schema_refs"`
	ContractUpdates []store.WorkflowPayloadDefinition `json:"contract_updates"`
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

// schemaIdentityDrift reports whether two declarations of one field differ
// only in the generated schema identity they declare. Versioned decoder
// identities are the sanctioned case: current heads of different generations
// declare the same wire field against different decoder identities, and the
// projection publishes the declarations as the closed variants it already
// carries. Any other difference stays a conflict.
func schemaIdentityDrift(left, right store.WorkflowPayloadField) bool {
	strippedLeft, strippedRight := left, right
	strippedLeft.SchemaRef, strippedRight.SchemaRef = "", ""
	strippedLeft.ItemRef, strippedRight.ItemRef = "", ""
	return reflect.DeepEqual(strippedLeft, strippedRight) && (left.SchemaRef != right.SchemaRef || left.ItemRef != right.ItemRef)
}

// conflictingSharedField returns the first shared field two current payload
// declarations carry with a difference that is not a schema-identity drift.
// Schema-identity drift keeps both variants: the store validates each call
// against the exact definition version the work item pins, and the frozen
// historical decoder owns the replay side of each identity.
func conflictingSharedField(a, b store.WorkflowPayloadDefinition) string {
	for _, left := range a.Fields {
		for _, right := range b.Fields {
			if left.Name == right.Name && !reflect.DeepEqual(left, right) && !schemaIdentityDrift(left, right) {
				return left.Name
			}
		}
	}
	return ""
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

// collectActionContracts folds the live definition heads and the canonical
// recovery list into the projection's action contracts. Nothing else
// contributes: retired published actions (CD-0218 D1/D2) and every retained
// version's historical payload layout stay out of the authoring surface,
// whose replay side is owned by the frozen historical decoder. Cross-field
// rules project onto each action's declared fields, so
// a canonical head whose payload narrows a rule's subject no longer
// publishes the vacuous half of the engine declaration. Order is
// deterministic: variants keep registration order (current families, then
// the recovery list).
func collectActionContracts(current []store.WorkflowDefinition, recovery []store.WorkflowActionDefinition) (map[string]actionContract, error) {
	contracts := map[string]actionContract{}
	addCurrent := func(action store.WorkflowActionDefinition) error {
		public := action.Payload
		if action.PublicPayload != nil {
			public = *action.PublicPayload
		}
		variant := payloadVariant{Payload: canonicalPayload(action.Payload), PublicPayload: canonicalPayload(public)}
		contract, ok := contracts[action.ID]
		if !ok {
			contract = actionContract{ID: action.ID, Variants: []payloadVariant{}}
		}
		for _, existing := range contract.Variants {
			if reflect.DeepEqual(existing, variant) {
				contracts[action.ID] = contract
				return nil
			}
			if field := conflictingSharedField(existing.Payload, variant.Payload); field != "" {
				return fmt.Errorf("action %s has inconsistent current payload contracts: field %s is declared differently across current definitions", action.ID, field)
			}
			if field := conflictingSharedField(existing.PublicPayload, variant.PublicPayload); field != "" {
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
	ids := make([]string, 0, len(contracts))
	for id := range contracts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		contract := contracts[id]
		cross, err := canonicalCrossFieldForAction(id, contract.Variants)
		if err != nil {
			return nil, err
		}
		contract.CrossField = cross
		contracts[id] = contract
	}
	return contracts, nil
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

// collectSchemaRefs splits the registry's declared payload schema refs by
// decoding owner: current definitions and the engine recovery list declare
// the refs the public payload document must resolve.
func collectSchemaRefs(current []store.WorkflowDefinition, recovery []store.WorkflowActionDefinition) schemaRefProjection {
	currentRefs := map[string]bool{}
	for _, definition := range current {
		for _, action := range definition.ActionDefinitions {
			for _, ref := range payloadSchemaRefs(action.Payload) {
				currentRefs[ref] = true
			}
		}
	}
	for _, action := range recovery {
		for _, ref := range payloadSchemaRefs(action.Payload) {
			currentRefs[ref] = true
		}
	}
	return schemaRefProjection{Current: sortedRefNames(currentRefs)}
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--canonical-definition" {
		var definition store.WorkflowDefinition
		decoder := json.NewDecoder(os.Stdin)
		if err := decoder.Decode(&definition); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			fmt.Fprintln(os.Stderr, "canonical definition requires one JSON object")
			os.Exit(1)
		}
		canonical, err := store.CanonicalWorkflowDefinition(definition)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		digest, err := store.WorkflowDefinitionDigest(definition)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		// Keep the canonical manifest order; the digest is not hashed.
		fmt.Printf("%s,\"digest\":%q}\n", canonical[:len(canonical)-1], digest)
		return
	}
	definitions := store.BuiltinWorkflowDefinitions()
	recovery := append(store.BuiltinWorkflowRecoveryActionDefinitions(), store.BuiltinWorkflowDecisionFactActionDefinitions()...)
	contracts, err := collectActionContracts(definitions, recovery)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ids := make([]string, 0, len(contracts))
	for id := range contracts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
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
	projection := contractProjection{SchemaVersion: "2.0", Actions: make([]actionContract, 0, len(ids)), Workflows: workflows, Teaching: store.WorkflowContractTeachingRules(), SchemaRefs: collectSchemaRefs(definitions, recovery)}
	for _, definition := range definitions {
		payload := store.WorkflowContractUpdatePayload(definition)
		if !slicesContainsPayload(projection.ContractUpdates, payload) {
			projection.ContractUpdates = append(projection.ContractUpdates, payload)
		}
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

func slicesContainsPayload(values []store.WorkflowPayloadDefinition, payload store.WorkflowPayloadDefinition) bool {
	for _, value := range values {
		if reflect.DeepEqual(value, payload) {
			return true
		}
	}
	return false
}
