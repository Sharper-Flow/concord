package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The read-scoped prover pool owns the pass's Git subprocesses: one live
// batch process per distinct repository, independent of how many sources,
// roots, or endpoint specs the pass proves. Growing the spec population
// two-hundred-fold must not start another process, and a second source over
// the same repository must reuse the first source's process.
func TestCON830ProverProcessesBoundedByRepositories(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home := refinementTestStore(t)
	defer s.Close()
	second := seedRefinementSecondSource(t, s, "prover-bound", "prover-bound-loc")
	commit := firstCommitOID(t, s, home)
	subjectPaths := func(projectID, locatorID string, limit int) []refinementSubjectRef {
		t.Helper()
		rows, err := s.DatabaseForTesting().QueryContext(ctx, `SELECT law_id, path, content_hash FROM law_subjects WHERE home_project_id=? AND home_locator_id=? LIMIT ?`, projectID, locatorID, limit)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		subjects := make([]refinementSubjectRef, 0)
		for rows.Next() {
			var subject refinementSubjectRef
			if err := rows.Scan(&subject.lawID, &subject.path, &subject.hash); err != nil {
				t.Fatal(err)
			}
			subjects = append(subjects, subject)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return subjects
	}
	primary := subjectPaths(home.HomeProjectID, home.HomeLocatorID, 32)
	external := subjectPaths(second.HomeProjectID, second.HomeLocatorID, 32)
	if len(primary) == 0 || len(external) == 0 {
		t.Fatalf("fixture carried no subjects: primary=%d external=%d", len(primary), len(external))
	}
	pool := newGitProverPool(ctx)
	defer pool.close()
	// Repeated verification of two sources over one repository, plus a
	// third home record over that same repository, keeps one process.
	sameRepoPeer := home
	sameRepoPeer.HomeLocatorID = "prover-same-repo-locator"
	for i := 0; i < 3; i++ {
		for _, source := range []KnowledgeHome{home, sameRepoPeer} {
			if _, _, err := validateKnowledgeHomeProven(ctx, s.DatabaseForTesting(), pool, source, true, "PM1.Q10"); err != nil {
				t.Fatalf("round %d: %v", i, err)
			}
		}
		if got := pool.processCount(); got != 1 {
			t.Fatalf("round %d: %d git processes for one repository, want 1", i, got)
		}
	}
	// A second repository adds exactly one process, once.
	for i := 0; i < 3; i++ {
		if _, _, err := validateKnowledgeHomeProven(ctx, s.DatabaseForTesting(), pool, second, true, "PM1.Q10"); err != nil {
			t.Fatalf("second round %d: %v", i, err)
		}
		if got := pool.processCount(); got != 2 {
			t.Fatalf("second round %d: %d git processes for two repositories, want 2", i, got)
		}
	}
	// Spec proof over both repositories stays at two processes while the
	// spec population grows by two hundred synthetic endpoints (degraded
	// allowed: absent endpoints are named findings, never refusals).
	specs := make([]refinementObjectSpec, 0, len(primary)+len(external)+200)
	for _, subject := range primary {
		specs = append(specs, refinementObjectSpec{label: home.HomeProjectID + "/" + home.HomeLocatorID, repo: home.RepoPath, commit: commit, path: subject.path, hash: subject.hash})
	}
	for _, subject := range external {
		specs = append(specs, refinementObjectSpec{label: second.HomeProjectID + "/" + second.HomeLocatorID, repo: second.RepoPath, commit: firstCommitOID(t, s, second), path: subject.path, hash: subject.hash})
	}
	for i := 0; i < 200; i++ {
		specs = append(specs, refinementObjectSpec{label: home.HomeProjectID + "/" + home.HomeLocatorID, repo: home.RepoPath, commit: commit, path: fmt.Sprintf(".concord/docs/decisions/synthetic-%04d.md", i), hash: "sha256:" + strings.Repeat("0", 64)})
	}
	findings, err := refinementVerifyObjectSpecs(ctx, pool, specs, true, "PM1.Q10.amendment_context")
	if err != nil {
		t.Fatalf("degraded spec proof refused: %v", err)
	}
	syntheticFindings := 0
	for _, finding := range findings {
		if strings.Contains(finding, "synthetic-") {
			syntheticFindings++
		}
	}
	if syntheticFindings != 200 {
		t.Fatalf("synthetic absent endpoints produced %d findings, want 200: %v", syntheticFindings, findings)
	}
	if got := pool.processCount(); got != 2 {
		t.Fatalf("%d git processes after %d specs across two repositories, want 2", got, len(specs))
	}
}

// Warm proof cannot survive root-tree loss: after an authoritative
// contextual read, deleting the head commit's root tree object must keep
// both the strict and the explicitly degraded read from claiming an
// authoritative no-amendments negative, because no live traversal can
// reach any projected content any more.
func TestCON830WarmRootTreeLossCannotProveAuthoritative(t *testing.T) {
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
	raw, err := runGit(ctx, home.RepoPath, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	oid := strings.TrimSpace(string(raw))
	if err := os.Remove(filepath.Join(home.RepoPath, ".git", "objects", oid[:2], oid[2:])); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, home.RepoPath, "cat-file", "-e", oid); err == nil {
		t.Fatalf("fixture did not make the root tree %s unreachable", oid)
	}
	for _, allowDegraded := range []bool{false, true} {
		req.AmendmentContextAllowDegraded = allowDegraded
		out, err := s.QueryQ10(ctx, req)
		if err == nil && out.Authority == "authoritative" {
			t.Errorf("allow_degraded=%t: cached identity claims authoritative missing despite the absent root tree: %+v", allowDegraded, out)
		}
		if err == nil && len(out.Omissions) == 0 {
			t.Errorf("allow_degraded=%t: absent root tree has no omission: omissions=%v", allowDegraded, out.Omissions)
		}
	}
}
