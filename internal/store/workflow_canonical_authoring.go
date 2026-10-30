package store

import (
	"fmt"
)

// Canonical authoring carries a verdicts batch and explicit outcome_predicates.
// Recovery validation and publication share the canonical field projection;
// historical policies and cross-field rules retain their original declarations.

// BuiltinWorkflowCanonicalRecoveryActionDefinitions restates the engine
// recovery list's authoring payloads as the canonical public contract. The
// contract recovery definition drops the paired outcome_kind/outcome_payload
// authoring shape and requires the explicit outcome_predicates array: no
// synthetic single-predicate fallback is authorable (CD-0218 D1/D2). Engine
// admission keeps workflowContractRecoveryActionDefinition unchanged, so a
// retained pin recorded under the paired shape replays through its frozen
// decoder entry.
func BuiltinWorkflowCanonicalRecoveryActionDefinitions() []WorkflowActionDefinition {
	recovery := BuiltinWorkflowRecoveryActionDefinitions()
	canonical := make([]WorkflowActionDefinition, 0, len(recovery)+2)
	for _, action := range recovery {
		if action.ID == "supersede_contract" {
			action.Payload = WorkflowPayloadDefinition{Closed: true, Fields: workflowCanonicalContractRecoveryPayloadFields(action.Payload.Fields)}
		}
		canonical = append(canonical, action)
	}
	return canonical
}

// BuiltinWorkflowDecisionFactActionDefinitions returns the contract actions
// a consequence_v1 live head validates against beyond its declared list: the
// initial approve_contract and the canonical supersede_contract with the
// selected decision fact. Publication offers them beside the declared
// legacy shapes, and the store validates each call against the current
// definition of the family the work item pins.
func BuiltinWorkflowDecisionFactActionDefinitions() []WorkflowActionDefinition {
	var actions []WorkflowActionDefinition
	for _, head := range BuiltinWorkflowDefinitions() {
		if !workflowDeclaresDecisionDomains(head) {
			continue
		}
		approve, err := workflowDecisionFactInitialActionDefinition(head)
		if err != nil {
			panic(fmt.Sprintf("built-in %s declares no usable initial contract approval: %v", head.Ref, err))
		}
		supersede := workflowDecisionFactRecoveryActionDefinition(head)
		supersede.Payload = WorkflowPayloadDefinition{Closed: true, Fields: workflowCanonicalContractRecoveryPayloadFields(supersede.Payload.Fields)}
		actions = append(actions, approve, supersede)
	}
	return actions
}

// workflowCanonicalContractRecoveryPayloadFields filters the supplied
// recovery declaration onto the canonical authoring contract: the paired
// outcome authoring shape drops, the explicit predicate array becomes
// required, and every other field keeps the requiredness and closed enum
// its one declaration owner supplies.
func workflowCanonicalContractRecoveryPayloadFields(fields []WorkflowPayloadField) []WorkflowPayloadField {
	canonical := make([]WorkflowPayloadField, 0, len(fields)-2)
	for _, field := range fields {
		switch field.Name {
		case "outcome_kind", "outcome_payload":
			continue
		case "outcome_predicates":
			field.Required = true
		}
		canonical = append(canonical, field)
	}
	return canonical
}

// WorkflowActionCanonicalCrossFieldRules projects one action's engine
// cross-field declaration onto the exact field set one authoring payload
// declares, so the published rules stay bound to what the live heads and
// the canonical recovery declaration actually name. A rule part that names
// no declared field is not part of that payload's contract and drops; a
// part that partially overlaps the declared set is a structural
// disagreement between the declaration and the payload and refuses. A
// forbids entry naming an undeclared field is vacuous — the closed payload
// refuses the field outright — and drops with the entry.
func WorkflowActionCanonicalCrossFieldRules(actionID string, fields []WorkflowPayloadField) (WorkflowActionCrossField, error) {
	declared := make(map[string]bool, len(fields))
	for _, field := range fields {
		declared[field.Name] = true
	}
	cross := WorkflowActionCrossFieldRules(actionID)
	projected := WorkflowActionCrossField{}
	if len(cross.Combinations) != 0 && len(cross.Alternatives) != 0 {
		return WorkflowActionCrossField{}, fmt.Errorf("action %s declares both cross-field rule families", actionID)
	}
	for _, combination := range cross.Combinations {
		if !declared[combination.Field] {
			continue
		}
		projectedCombination, err := workflowCanonicalCombination(actionID, declared, combination)
		if err != nil {
			return WorkflowActionCrossField{}, err
		}
		projected.Combinations = append(projected.Combinations, projectedCombination)
	}
	for _, group := range cross.Alternatives {
		switch workflowDeclaredFieldCount(declared, group.Fields) {
		case 0:
			continue
		case len(group.Fields):
			projectedGroup := WorkflowPayloadAlternative{Fields: group.Fields, Remedy: group.Remedy}
			for _, name := range group.Forbids {
				if !declared[name] {
					continue
				}
				projectedGroup.Forbids = append(projectedGroup.Forbids, name)
			}
			projected.Alternatives = append(projected.Alternatives, projectedGroup)
		default:
			return WorkflowActionCrossField{}, fmt.Errorf("canonical cross-field projection for %s: alternative group %v partially overlaps the declared fields", actionID, group.Fields)
		}
	}
	for _, match := range cross.KindMatches {
		fieldDeclared, objectDeclared := declared[match.Field], declared[match.Object]
		switch {
		case !fieldDeclared && !objectDeclared:
			continue
		case !fieldDeclared || !objectDeclared || match.Discriminator == "":
			return WorkflowActionCrossField{}, fmt.Errorf("canonical cross-field projection for %s: kind match %s/%s partially overlaps the declared fields", actionID, match.Field, match.Object)
		default:
			projected.KindMatches = append(projected.KindMatches, match)
		}
	}
	return projected, nil
}

func workflowDeclaredFieldCount(declared map[string]bool, names []string) int {
	count := 0
	for _, name := range names {
		if declared[name] {
			count++
		}
	}
	return count
}

func workflowCanonicalCombination(actionID string, declared map[string]bool, combination WorkflowPayloadCombination) (WorkflowPayloadCombination, error) {
	projected := WorkflowPayloadCombination{Field: combination.Field, Value: combination.Value, Remedy: combination.Remedy}
	for _, name := range combination.Requires {
		if !declared[name] {
			return WorkflowPayloadCombination{}, fmt.Errorf("canonical cross-field projection for %s: combination on %s requires undeclared field %s", actionID, combination.Field, name)
		}
		projected.Requires = append(projected.Requires, name)
	}
	for _, name := range combination.Forbids {
		if declared[name] {
			projected.Forbids = append(projected.Forbids, name)
		}
	}
	return projected, nil
}
