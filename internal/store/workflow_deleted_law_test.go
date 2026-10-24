package store

import (
	"context"
	"strings"
	"testing"
)

func TestWorkflowLawRevisionDeletedPinnedLawIsStale(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "deleted-law-pin")
	hash := "sha256:" + strings.Repeat("a", 64)
	insertLawRevisionFixture(t, s, "deleted-law-pin", "spec:deleted", hash)
	got, err := findStaleWorkflowLawRevision(context.Background(), s.db, "p", "l", "deleted-law-pin", 1, []string{"spec:deleted"})
	if err != nil || got == nil || got.OldLawID != "spec:deleted" || got.OldContentHash != hash {
		t.Fatalf("deleted pinned law diagnosis=%+v err=%v, want deleted law ID and pinned hash", got, err)
	}
	if strings.Join(got.RecoveryActions, ",") != staleLawRecoveryActions {
		t.Fatalf("deleted pinned law recovery=%v", got.RecoveryActions)
	}
}
