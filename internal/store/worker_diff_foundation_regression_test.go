package store

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadWorkerDiffFailedEditorSupersessionRetainsCommittedChange(t *testing.T) {
	f := seedWorkContextFixture(t, "diff-failed-editor-supersession")
	t.Cleanup(func() { f.store.Close() })
	repo, base := reviewSubjectClaim(t, f.store, f.workID)
	var branch string
	if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(f.workID)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "branch", "-m", branch)
	j := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: repo}
	job := reviewSubjectReadyRevision(t, f)
	if err := descendantDispatch(t, j, "implement", "failed-editor", job, nil); err != nil {
		t.Fatal(err)
	}
	j.laneDispatch(t, "failed-editor", "implementation", &job)
	dir := filepath.Join(repo, "internal", "store")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "change.txt"), []byte("committed before the report failed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	head := j.commit(t, "worker@example.invalid", "retained-change")
	lane := reviewGateLane(t, "implementation")
	if err := s2WorkerFailedLive(t, j.s, s2WorkerFailedAttributedEvent(j.workID, "failed-editor", "failed-editor-terminal", preferredModelForLane(lane), WorkerFailureInvalidReport, "the editor committed but supplied no report frame", time.Unix(30, 0).UTC())); err != nil {
		t.Fatal(err)
	}
	j.observeRun(t, "failed-editor-change")
	reviewSubjectSupersedeContract(t, j.s, j.workID)
	acceptCurrentReviewForTest(t, f, repo, "continue")
	convergenceJob := reviewSubjectReadyRevision(t, f)
	if err := descendantDispatch(t, j, "implement", "convergence-editor", convergenceJob, nil); err != nil {
		t.Fatal(err)
	}
	j.laneDispatch(t, "convergence-editor", "implementation", &convergenceJob)
	j.completeLane(t, "convergence-editor", "implementation", &convergenceJob, nil)
	reviewSubjectAccept(t, j.s, j.workID, "convergence-editor")
	j.observeRun(t, "retained-change")
	req := workerDiffDispatch(t, j, "review-retained-change")
	before := j.effects(t)
	page, err := j.s.ReadWorkerDiff(t.Context(), req)
	if err != nil || page.BaseCommit != base || page.SubjectCommit != head || !bytes.Contains(page.Data, []byte("+committed before the report failed")) {
		t.Fatalf("Review lost the failed editor's committed change: base=%s subject=%s total_bytes=%d data=%q err=%v; want base=%s subject=%s and the committed delta", page.BaseCommit, page.SubjectCommit, page.TotalBytes, page.Data, err, base, head)
	}
	if after := j.effects(t); after != before {
		t.Fatal("diff read changed workflow or evidence state")
	}
}
