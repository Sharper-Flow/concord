package store

import (
	"context"
	"strings"
	"testing"
)

// TestWorkflowLawRevisionDeletedPinnedLawIsStale proves a contract pin whose
// law is no longer a committed law file resolves stale with the pinned hash.
// The law home is a real committed folder that does not carry the pinned ID,
// and the pre-transaction proof binds that folder before the transaction-
// scoped staleness read.
func TestWorkflowLawRevisionDeletedPinnedLawIsStale(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	repo := initKnowledgeRepo(t)
	home := KnowledgeHome{HomeProjectID: "p", HomeLocatorID: "l", RepoPath: repo, HeadRef: "HEAD"}
	// The committed law folder carries a registry and no spec:deleted: the
	// pinned ID is not a current law file.
	writeFixtureLawSource(t, home)
	seedEventDerivedLocator(t, s, "p", "l", repo)
	// Designate the committed folder as the Product's knowledge home, so the
	// per-project verification resolves and binds it.
	authorizeKnowledgeProductHome(t, s, "anchor-product-p", home, "p")
	seedWork(t, s, "deleted-law-pin")
	hash := "sha256:" + strings.Repeat("a", 64)
	insertLawRevisionFixture(t, s, "deleted-law-pin", "spec:deleted", hash)
	ctx, err := s.EstablishKnowledgeSourceSetProofForProject(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	got, err := findStaleWorkflowLawRevision(ctx, s.db, "p", "l", "deleted-law-pin", 1, []string{"spec:deleted"})
	if err != nil || got == nil || got.OldLawID != "spec:deleted" || got.OldContentHash != hash {
		t.Fatalf("deleted pinned law diagnosis=%+v err=%v, want deleted law ID and pinned hash", got, err)
	}
	if strings.Join(got.RecoveryActions, ",") != staleLawRecoveryActions {
		t.Fatalf("deleted pinned law recovery=%v", got.RecoveryActions)
	}
}
