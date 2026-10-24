package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewRepairDiffKeepsInputBWhileCheckoutChanges(t *testing.T) {
	j, _ := descendantReviewFixture(t, "repair-diff-input")
	const jobID = "job:repair-diff-implementation"
	if err := recordWorkerJobFieldsForTest(t, j.s, j.workID, j.fixture.owner, map[string]any{
		"job_id": jobID, "objective": "Produce the bounded diff input.", "stopping_condition": "The committed input can be reviewed.",
		"path_scope": []string{"internal/store", "seed.txt"}, "checks": []string{}, "ready": true, "readiness_evidence": []string{"evidence:ready"},
	}); err != nil {
		t.Fatal(err)
	}
	views, err := readWorkerJobRevisions(t.Context(), j.s.db, j.workID)
	if err != nil {
		t.Fatal(err)
	}
	var job WorkerJobBinding
	for _, view := range views {
		if view.Binding.JobID == jobID {
			job = view.Binding
		}
	}
	if err := descendantDispatch(t, j, "implement", "repair-diff-implementation", job, nil); err != nil {
		t.Fatal(err)
	}
	j.laneDispatch(t, "repair-diff-implementation", "implementation", &job)
	dir := filepath.Join(j.repo, "internal", "store")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "change.txt"), []byte("input B\n"), 0600); err != nil {
		t.Fatal(err)
	}
	b := j.commit(t, "worker@example.invalid", "input-b")
	j.completeLane(t, "repair-diff-implementation", "implementation", &job, nil)
	j.observeRun(t, "input-b")
	if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": "repair-diff-implementation", "attempt_epoch": j.dispatchEpoch(t, "repair-diff-implementation")}, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	req := workerDiffDispatch(t, j, "repair-diff")
	before, err := j.s.ReadWorkerDiff(context.Background(), req)
	if err != nil || before.SubjectCommit != b {
		t.Fatalf("input diff: %+v %v", before, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "change.txt"), []byte("uncommitted repair\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, commit := range []bool{false, true} {
		if commit {
			if err := os.WriteFile(filepath.Join(dir, "change.txt"), []byte("output C\n"), 0600); err != nil {
				t.Fatal(err)
			}
			j.commit(t, "reviewer@example.invalid", "output-c")
		}
		after, err := j.s.ReadWorkerDiff(context.Background(), req)
		if err != nil || after.SubjectCommit != b || after.ContentHash != before.ContentHash || !bytes.Equal(after.Data, before.Data) {
			t.Fatalf("mutable checkout replaced the pinned B diff: %+v %v", after, err)
		}
	}
}

func TestReviewRepairDispatchDoesNotResetAccumulatedDiffBase(t *testing.T) {
	db := workerFoundationCurrentDB(t)
	foundation := strings.Repeat("a", 40)
	for i, id := range []string{"implement", "review"} {
		subject := foundation
		if id == "review" {
			subject = strings.Repeat("b", 40)
		}
		foundationAuthorizationForTest(t, db, int64(2+i*2), int64(i+1), id, id, subject, "claim")
	}
	base, err := firstWorkerEditingSubject(context.Background(), db, "work", "claim", 5)
	if err != nil || base != foundation {
		t.Fatalf("Review reset the accumulated solution diff to its repair input: base=%q err=%v", base, err)
	}
}
