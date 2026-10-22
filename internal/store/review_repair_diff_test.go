package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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
