package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// siblingProjectFixture registers project-b as a second member Project of
// work-w with its own repository, mirroring the cross-Project shape the host
// drives: one work item, two Project worktrees, two repositories. The work
// item's version advances to 3.
func siblingProjectFixture(t *testing.T, s *Store, git *fakeWorktreeGit) {
	t.Helper()
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("product-b"), locatorProjectEvent("project-b"), locatorMembershipEvent("product-b", "project-b")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-b"): 0, VersionRef(SubjectProject, "project-b"): 0}}); err != nil {
		t.Fatal(err)
	}
	repoB := t.TempDir()
	git.addRepository(repoB)
	if err := s.AddProjectLocator(ctx, "project-b", ProjectLocator{ID: "path-b", Kind: LocatorCanonicalPath, Value: repoB}, 1); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{
		EventID: "work-w-membership-b", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1,
		Payload: jsonRaw(`{"memberships":[{"project_id":"project-w","role":"primary"},{"project_id":"project-b","role":"secondary"}],"expected_version":2,"resulting_version":3}`),
	}}}); err != nil {
		t.Fatal(err)
	}
}

func siblingClaim(git *fakeWorktreeGit, opID string, expectedVersion int64) WorktreeClaimRequest {
	req := baseClaim(git)
	req.OpID = opID
	req.ProjectID = "project-b"
	req.RequestID = "req-" + opID
	req.ExpectedVersion = expectedVersion
	return req
}

func worktreeEntriesByProject(t *testing.T, s *Store, workID string) map[string]WorktreeEntry {
	t.Helper()
	entries, err := s.WorktreeEntries(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	byProject := map[string]WorktreeEntry{}
	for _, entry := range entries {
		byProject[entry.ProjectID] = entry
	}
	return byProject
}

// A session holding one Project worktree of a work item may claim a sibling
// Project of the same work item. The claim is admitted and the commit leaves
// the source occupancy row unchanged: only a verified landing may move the
// projection, so a refused move strands nothing.
func TestClaimWorktreeAdmitsSiblingProjectOccupancy(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	siblingProjectFixture(t, s, git)
	ctx := context.Background()

	reqA := baseClaim(git)
	reqA.ExpectedVersion = 3
	reqA.SessionRef = "ses-1"
	if _, err := s.ClaimWorktree(ctx, reqA); err != nil {
		t.Fatal(err)
	}

	reqB := siblingClaim(git, "wt-op-2", 4)
	reqB.SessionRef = "ses-1"
	if _, err := s.ClaimWorktree(ctx, reqB); err != nil {
		t.Fatalf("sibling Project claim refused: %v", err)
	}

	byProject := worktreeEntriesByProject(t, s, "work-w")
	if byProject["project-w"].State != worktreeEntryActive || byProject["project-w"].OccupantSessionRef != "ses-1" {
		t.Fatalf("source entry=%+v, want active and still occupied by ses-1", byProject["project-w"])
	}
	if byProject["project-b"].State != worktreeEntryActive || byProject["project-b"].OccupantSessionRef != "ses-1" {
		t.Fatalf("sibling entry=%+v, want active and occupied by ses-1", byProject["project-b"])
	}
}

// Another work item's occupied worktree still refuses the claim. The new work
// item has no landing record that could make a host move true, so the
// ownership conflict stands and names session_vacate.
func TestClaimWorktreeStillRefusesForeignWorkOccupancy(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()

	req := baseClaim(git)
	req.SessionRef = "ses-1"
	if _, err := s.ClaimWorktree(ctx, req); err != nil {
		t.Fatal(err)
	}

	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: "work-x-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "work-x", Actor: "operator", OccurredAt: time.Unix(4, 0).UTC(), PayloadVersion: 2, Payload: jsonRaw(`{"work_kind":"task","title":"Foreign Work","priority":1}`)},
		{EventID: "work-x-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "work-x", Actor: "operator", OccurredAt: time.Unix(5, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-w","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-x"): 0}}); err != nil {
		t.Fatal(err)
	}

	foreign := baseClaim(git)
	foreign.WorkID = "work-x"
	foreign.OpID = "wt-op-x"
	foreign.RequestID = "req-x"
	foreign.SessionRef = "ses-1"
	_, err := s.ClaimWorktree(ctx, foreign)
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindWorktreeOwnershipConflict {
		t.Fatalf("err=%v, want %s", err, KindWorktreeOwnershipConflict)
	}
	if !strings.Contains(failure.RecoveryAction, "session_vacate") {
		t.Fatalf("recovery=%q, want the remedy to name session_vacate", failure.RecoveryAction)
	}
	byProject := worktreeEntriesByProject(t, s, "work-w")
	if byProject["project-w"].OccupantSessionRef != "ses-1" {
		t.Fatalf("source entry=%+v, want the refused claim to leave the occupancy recorded", byProject["project-w"])
	}
}
