package store

import "testing"

func pReturnClaim(t *testing.T, f workflowReturnRouteFixture, id string) *candidateJourney {
	t.Helper()
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, "fixture.txt", "fixture\n")
	commitKnowledgeRepo(t, repo, "fixture")
	wf := workContextFixture{store: f.store, workID: id, owner: f.owner, operator: f.operator}
	return &candidateJourney{fixture: wf, s: f.store, workID: id, repo: repo}
}

func pReturnProduce(t *testing.T, j *candidateJourney, label string) {
	t.Helper()
	conformanceImplementChange(t, j, label)
}
