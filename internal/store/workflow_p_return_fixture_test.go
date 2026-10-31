package store

import (
	"encoding/json"
	"testing"
)

// A clean observed Git subject precedes the ordinary Review action.
func pReturnReviewFixture(t *testing.T, id, family, disposition string) *candidateJourney {
	t.Helper()
	effect := "execution"
	if family == "workflow.break_fix" {
		effect = "repair"
	}
	f := seedWorkflowReturnRouteFixture(t, id, family, effect)
	t.Cleanup(func() { _ = f.store.Close() })
	j := pReturnClaim(t, f, id)
	pReturnProduce(t, j, "initial")
	if disposition != "" {
		acceptCurrentReviewForTest(t, j.fixture, disposition)
	}
	return j
}

func pReturnClaim(t *testing.T, f workflowReturnRouteFixture, id string) *candidateJourney {
	t.Helper()
	repo, _ := reviewSubjectClaim(t, f.store, id)
	wf := workContextFixture{store: f.store, workID: id, owner: f.owner, operator: f.operator}
	return &candidateJourney{fixture: wf, s: f.store, workID: id, repo: repo}
}

func pReturnProduce(t *testing.T, j *candidateJourney, label string) {
	t.Helper()
	conformanceImplementChange(t, j, label)
}

func pReviewCorrectionPayload(t *testing.T) json.RawMessage {
	t.Helper()
	return mustJSONValue(map[string]any{"diagnosis": "the accepted current Review requires correction", "strategy": "perform the declared corrective work and obtain applicable Review", "predicate_ids": []string{"predicate:return-route"}, "evidence_refs": []string{"evidence:return-route-verification"}})
}
