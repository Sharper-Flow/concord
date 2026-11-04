package store

import (
	"encoding/json"
	"testing"
)

func seedLateCriterionAssociation(t *testing.T, workID string) (workflowReturnRouteFixture, string) {
	t.Helper()
	return seedCriterionAssociationAtStep(t, workID, "conclude")
}

func seedCriterionAssociationAtStep(t *testing.T, workID, step string) (workflowReturnRouteFixture, string) {
	t.Helper()
	var predicates []map[string]any
	for ordinal, id := range []string{"predicate:first", "predicate:second"} {
		predicates = append(predicates, map[string]any{"predicate_id": id, "ordinal": ordinal, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}})
	}
	route := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanResearch, step, []string{}, []string{"artifact"}, predicates...)
	t.Cleanup(func() { _ = route.store.Close() })
	if step != "conclude" {
		return route, ""
	}
	f := workContextFixture{store: route.store, workID: workID, owner: route.owner, operator: route.operator}
	proof := qAssociateCurrentProofAt(t, f, "conclude", "", false)
	return route, proof.receipt
}

func criterionAssociationPayload(ref string, predicates ...string) json.RawMessage {
	entries := make([]map[string]any, 0, len(predicates))
	for _, predicate := range predicates {
		entries = append(entries, map[string]any{"predicate_id": predicate, "receipt_refs": []string{ref}})
	}
	return mustJSONValue(map[string]any{"verdicts": entries})
}
