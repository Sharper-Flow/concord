package store

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewRepairDiffKeepsInputBWhileCheckoutChanges(t *testing.T) {
	j, _ := descendantReviewFixture(t, "repair-diff-input")
	b := workerDiffCommit(t, j, "input-b", "input B\n")
	req := workerDiffDispatch(t, j, "repair-diff")
	before, err := j.s.ReadWorkerDiff(context.Background(), req)
	if err != nil || before.SubjectCommit != b {
		t.Fatalf("input diff: %+v %v", before, err)
	}
	if err := os.WriteFile(filepath.Join(j.repo, "internal", "store", "change.txt"), []byte("uncommitted repair\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, commit := range []bool{false, true} {
		if commit {
			workerDiffCommit(t, j, "output-c", "output C\n")
		}
		after, err := j.s.ReadWorkerDiff(context.Background(), req)
		if err != nil || after.SubjectCommit != b || after.ContentHash != before.ContentHash || !bytes.Equal(after.Data, before.Data) {
			t.Fatalf("mutable checkout replaced the pinned B diff: %+v %v", after, err)
		}
	}
}

func TestReviewRepairDispatchDoesNotResetAccumulatedDiffBase(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE domain_events(seq INTEGER PRIMARY KEY,subject_type TEXT,subject_id TEXT,kind TEXT,payload TEXT); CREATE TABLE worker_attempts(work_id TEXT,attempt_id TEXT,lane_id TEXT,lane_version INTEGER,lane_digest TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, seq := range []int{1, 3} {
		if _, err := db.Exec(`INSERT INTO domain_events VALUES(?,'work_item','work',?,?)`, seq, WorkflowContractApproved, `{"contract_version":1}`); err != nil {
			t.Fatal(err)
		}
	}
	foundation := strings.Repeat("a", 40)
	for i, id := range []string{"implement", "review"} {
		version, digest := registeredLaneIdentity(t, id)
		subject := foundation
		if id == "review" {
			subject = strings.Repeat("b", 40)
		}
		payload := map[string]any{"action_id": "dispatch_worker", "worker_lane_id": id, "worker_lane_version": version, "worker_lane_digest": digest, "worker_worktree_identity": "claim", "worker_subject_commit": subject}
		if _, err := db.Exec(`INSERT INTO domain_events VALUES(?,'work_item','work',?,?)`, 2+i*2, WorkflowActionCompleted, string(mustJSONValue(payload))); err != nil {
			t.Fatal(err)
		}
	}
	base, err := firstWorkerEditingSubject(context.Background(), db, "work", "claim", 5)
	if err != nil || base != foundation {
		t.Fatalf("Review reset the accumulated solution diff to its repair input: base=%q err=%v", base, err)
	}
}
