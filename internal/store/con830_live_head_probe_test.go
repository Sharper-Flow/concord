package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A warm identity memo cannot replace proof that the configured current
// commit still exists. A negative read has no historical blob proof to
// supply this missing current-source evidence.
func TestCoordinatorCON830WarmTreeCannotProveMissingCurrentCommit(t *testing.T) {
	assertCoordinatorCON830MissingCurrentObject(t, "HEAD^{commit}")
}

func TestCoordinatorCON830WarmTreeCannotProveMissingIntermediateTree(t *testing.T) {
	assertCoordinatorCON830MissingCurrentObject(t, "HEAD:.concord/docs/decisions")
}

func assertCoordinatorCON830MissingCurrentObject(t *testing.T, objectRef string) {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	home := seedAmendmentContextHome(t)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home, home.HomeProjectID)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	req := Q10Request{Product: "amendment-product", KnowledgeID: "CD-9999", IncludeAmendmentContext: true}
	first, err := s.QueryQ10(ctx, req)
	if err != nil || first.Authority != "authoritative" || first.Status != "missing" {
		t.Fatalf("initial complete negative: %+v, %v", first, err)
	}
	raw, err := runGit(ctx, home.RepoPath, "rev-parse", "--verify", objectRef)
	if err != nil {
		t.Fatal(err)
	}
	oid := strings.TrimSpace(string(raw))
	if err := os.Remove(filepath.Join(home.RepoPath, ".git", "objects", oid[:2], oid[2:])); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, home.RepoPath, "cat-file", "-e", oid); err == nil {
		t.Fatalf("fixture did not make %s unreachable", objectRef)
	}
	for _, allowDegraded := range []bool{false, true} {
		req.AmendmentContextAllowDegraded = allowDegraded
		out, err := s.QueryQ10(ctx, req)
		if err == nil && out.Authority == "authoritative" {
			t.Errorf("allow_degraded=%t: cached identity claims authoritative missing despite absent %s: %+v", allowDegraded, objectRef, out)
		}
		if err == nil && len(out.Omissions) == 0 {
			t.Errorf("allow_degraded=%t: absent %s has no omission", allowDegraded, objectRef)
		}
	}
}
