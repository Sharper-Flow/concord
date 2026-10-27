package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Production, host observation and Main acceptance establish the candidate
// before Review can authorize its next execution group or final proof.
func pReturnReviewFixture(t *testing.T, id, family, disposition string) *candidateJourney {
	t.Helper()
	effect := "execution"
	if family == "workflow.break_fix" {
		effect = "repair"
	}
	// Pin the indexed law and package ownership before Review captures them.
	// The return-route law has a complete empty executable-check set.
	var product string
	f := seedWorkflowReturnRouteFixtureWithSetup(t, id, legacyApprovalPolicyDefinitionForRef(t, family), effect, []string{"verification"}, []string{"verification", "review", "artifact"}, func(s *Store) {
		product = footprintProofKnowledge(t, s, id)
	})
	t.Cleanup(func() { _ = f.store.Close() })
	j := pReturnClaim(t, f, id)
	pReturnProduce(t, j, "initial")
	footprintProofRecordPath(t, j.s, id, product, ".concord/docs/decisions/CD-0003-empty.md")
	if disposition != "" {
		acceptCurrentReviewForTest(t, j.fixture, j.repo, disposition)
	}
	return j
}

func pReturnClaim(t *testing.T, f workflowReturnRouteFixture, id string) *candidateJourney {
	t.Helper()
	repo, _ := reviewSubjectClaim(t, f.store, id)
	var branch string
	if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(id)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "branch", "-m", branch)
	wf := workContextFixture{store: f.store, workID: id, owner: f.owner, operator: f.operator}
	return &candidateJourney{fixture: wf, s: f.store, workID: id, repo: repo}
}

func pReturnProduce(t *testing.T, j *candidateJourney, label string) {
	t.Helper()
	job := reviewSubjectReadyRevision(t, j.fixture)
	attempt := "attempt:" + j.workID + ":" + label
	if err := descendantDispatch(t, j, "implement", attempt, job, nil); err != nil {
		t.Fatal(err)
	}
	j.laneDispatch(t, attempt, "implementation", &job)
	path := filepath.Join(j.repo, "internal", "store", job.JobID, "value.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package fixture\n// "+attempt+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	j.commit(t, "worker@example.invalid", "produce bounded implementation")
	j.completeLane(t, attempt, "implementation", &job, nil)
	window := reviewSubjectWindow(t, j.s, j.workID, attempt)
	if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": window.AttemptEpoch}, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	j.observe(t, "accepted-"+label)
}

func pReviewCorrectionPayload(t *testing.T) json.RawMessage {
	t.Helper()
	return mustJSONValue(map[string]any{"diagnosis": "the accepted current Review requires correction", "strategy": "perform the declared corrective work and obtain applicable Review", "predicate_ids": []string{"predicate:return-route"}, "evidence_refs": []string{"evidence:return-route-verification"}})
}
