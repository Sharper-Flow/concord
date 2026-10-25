package store

import (
	"os"
	"testing"
	"time"
)

func memberReclaimFixture(t *testing.T) (workContextFixture, []SessionWorktreeOwner, *fakeWorktreeGit, []WorktreeEntry) {
	t.Helper()
	f, group, owners := boundMemberClaimFixture(t)
	repo, err := f.store.ProjectCanonicalPath(t.Context(), "project")
	if err != nil {
		t.Fatal(err)
	}
	git := newFakeWorktreeGit(repo)
	git.branches["main"] = group.BaseSubject.Commit
	main, err := activeWorktreeEntryForProject(t.Context(), f.store.db, "test", f.workID, "project")
	if err != nil {
		t.Fatal(err)
	}
	entries := []WorktreeEntry{main}
	git.branches[main.Branch] = group.BaseSubject.Commit
	git.worktrees[main.Path], git.worktreeRepos[main.Path] = main.Branch, repo
	for i, member := range group.Members {
		result, err := f.store.ClaimWorktree(t.Context(), WorktreeClaimRequest{
			OpID: "reclaim-claim-" + member.MemberID, WorkID: f.workID, ProjectID: "project", BaseSHA: group.BaseSubject.Commit,
			PrincipalRef: f.owner.PrincipalRef, RequestID: "reclaim-request-" + member.MemberID,
			SessionRef: owners[i].SessionRef, Owner: owners[i], ExpectedVersion: readWorkVersion(t, f.store, f.workID), Runner: git,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(result.Entry.Path, 0o755); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, result.Entry)
	}
	return f, owners, git, entries
}

func memberReclaimRequest(t *testing.T, f workContextFixture, git *fakeWorktreeGit) WorktreeReclaimRequest {
	t.Helper()
	return WorktreeReclaimRequest{WorkID: f.workID, ProjectID: "project", DefaultRef: "origin/main", PrincipalRef: f.owner.PrincipalRef,
		RequestID: "member-reclaim", ExpectedVersion: readWorkVersion(t, f.store, f.workID), Runner: git, Now: time.Unix(400, 0).UTC()}
}

func TestMemberReclaimOwnClaimAndTransactionReplay(t *testing.T) {
	f, owners, git, entries := memberReclaimFixture(t)
	req := memberReclaimRequest(t, f, git)
	mainProbe, err := f.store.PrepareWorktreeReclaim(t.Context(), req)
	if err != nil || mainProbe.Entry.ClaimOpID != entries[0].ClaimOpID {
		t.Fatalf("unrestricted reclaim must default to integration: %+v %v", mainProbe.Entry, err)
	}
	for i, owner := range owners {
		ctx := WithSessionWorktreeOwner(t.Context(), owner)
		req.ExpectedVersion = readWorkVersion(t, f.store, f.workID)
		probe, err := f.store.PrepareWorktreeReclaim(ctx, req)
		if err != nil || probe.Entry.ClaimOpID != entries[i+1].ClaimOpID {
			t.Fatalf("member %d probed foreign claim: %+v %v", i, probe.Entry, err)
		}
		before := readWorkVersion(t, f.store, f.workID)
		if err := f.store.Transact(ctx, func(tx *Transaction) error {
			_, err := ReclaimWorktreeTx(ctx, tx, req, mainProbe)
			return err
		}); err == nil || !hasFailureKind(err, KindProjectionConflict) {
			t.Fatalf("member consumed integration probe: %v", err)
		}
		if got := readWorkVersion(t, f.store, f.workID); got != before {
			t.Fatal("foreign probe refusal wrote a work version")
		}
		var result WorktreeReclaimResult
		if err := f.store.Transact(ctx, func(tx *Transaction) error {
			var err error
			result, err = ReclaimWorktreeTx(ctx, tx, req, probe)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if result.Entry.ClaimOpID != entries[i+1].ClaimOpID || result.Entry.State != worktreeEntryReclaimed || result.Removal == nil || result.Removal.Path != entries[i+1].Path {
			t.Fatalf("transaction returned another claim: %+v", result)
		}
		if err := f.store.FinishWorktreeNativeRemoval(ctx, git, result.Removal); err != nil {
			t.Fatal(err)
		}
		replay, err := f.store.PrepareWorktreeReclaim(ctx, req)
		if err != nil || !replay.AlreadyConverged || replay.Entry.ClaimOpID != entries[i+1].ClaimOpID {
			t.Fatalf("member replay selected another claim: %+v %v", replay, err)
		}
		version := readWorkVersion(t, f.store, f.workID)
		if err := f.store.Transact(ctx, func(tx *Transaction) error {
			result, err = ReclaimWorktreeTx(ctx, tx, req, replay)
			return err
		}); err != nil || result.Removal != nil || result.Entry.ClaimOpID != entries[i+1].ClaimOpID || readWorkVersion(t, f.store, f.workID) != version {
			t.Fatalf("transaction replay changed scope or wrote twice: %+v %v", result, err)
		}
	}
	main, err := activeWorktreeEntryForProject(t.Context(), f.store.db, "test", f.workID, "project")
	if err != nil || main.ClaimOpID != entries[0].ClaimOpID {
		t.Fatalf("member reclaim changed integration: %+v %v", main, err)
	}
}

func TestMemberLandingKeepsRetainedScopeBeforeWrites(t *testing.T) {
	f, owners, git, entries := memberReclaimFixture(t)
	if err := ApplyOperation(t.Context(), f.store, Operation{Events: []Event{
		{EventID: "foreign-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "foreign-work", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion("work.created"), Payload: jsonRaw(`{"work_kind":"task","title":"Foreign landing target","priority":1}`)},
		{EventID: "foreign-member", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "foreign-work", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}}); err != nil {
		t.Fatal(err)
	}
	foreign, err := f.store.ClaimWorktree(t.Context(), WorktreeClaimRequest{OpID: "foreign-claim", WorkID: "foreign-work", ProjectID: "project", BaseSHA: git.branches["main"], PrincipalRef: f.owner.PrincipalRef, RequestID: "foreign-claim", ExpectedVersion: 2, Runner: git})
	if err != nil {
		t.Fatal(err)
	}
	for i, owner := range owners {
		for _, authenticated := range []bool{false, true} {
			ctx := t.Context()
			if authenticated {
				ctx = WithSessionWorktreeOwner(ctx, owner)
			}
			for _, target := range []struct {
				work  string
				entry WorktreeEntry
			}{{f.workID, entries[0]}, {f.workID, entries[2-i]}, {"foreign-work", foreign.Entry}} {
				before := memberLandingSnapshot(t, f)
				_, err := f.store.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: target.work, SessionRef: owner.SessionRef, LandedDirectory: target.entry.Path, HostPID: os.Getpid()})
				if err == nil {
					t.Fatalf("member %d landed outside its own scope (owner=%v): %+v", i, authenticated, target)
				}
				if after := memberLandingSnapshot(t, f); after != before {
					t.Fatalf("foreign landing wrote state: before=%q after=%q", before, after)
				}
			}
			own := WorktreeClaimLandingRequest{WorkID: f.workID, SessionRef: owner.SessionRef, LandedDirectory: entries[i+1].Path, HostPID: os.Getpid()}
			if _, err := f.store.RecordWorktreeClaimLanding(ctx, own); err != nil {
				t.Fatalf("own landing (owner=%v): %v", authenticated, err)
			}
			if replay, err := f.store.RecordWorktreeClaimLanding(ctx, own); err != nil || !replay.AlreadyRecorded {
				t.Fatalf("own landing replay: %+v %v", replay, err)
			}
		}
	}
}

func memberLandingSnapshot(t *testing.T, f workContextFixture) string {
	t.Helper()
	var snapshot string
	if err := f.store.db.QueryRow(`SELECT json_array((SELECT COUNT(*) FROM effect_receipts),(SELECT json_group_array(json_array(worktree_id,session_ref,recorded_at,host_pid,host_pid_start)) FROM (SELECT * FROM worktree_occupancy ORDER BY worktree_id,session_ref)))`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestMemberAuditReclaimsExactReportedClaim(t *testing.T) {
	for _, lifecycle := range []string{"needed", "completed"} {
		t.Run(lifecycle, func(t *testing.T) {
			f, owners, git, entries := memberReclaimFixture(t)
			setWorkLifecycleForTesting(t, f.store, f.workID, lifecycle)
			// Integration is report-only. One member stays live; its sibling is
			// reclaimable, so substituting either other Project row is visible.
			git.dirty[entries[0].Path] = true
			if _, err := f.store.RecordWorktreeClaimLanding(t.Context(), WorktreeClaimLandingRequest{WorkID: f.workID, SessionRef: owners[0].SessionRef, LandedDirectory: entries[1].Path, HostPID: os.Getpid()}); err != nil {
				t.Fatal(err)
			}
			out, err := f.store.WorktreeAuditReclaim(t.Context(), WorktreeAuditReclaimRequest{ProductID: "product", DefaultRef: "origin/main", PrincipalRef: f.owner.PrincipalRef, RequestID: "member-audit", Runner: git, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			rows := map[string]WorktreeAuditReclaimRow{}
			for _, row := range out.Rows {
				rows[row.Path] = row
			}
			if len(rows) != 2 || rows[entries[1].Path].RefusalKind != string(KindWorktreeOwnershipConflict) || rows[entries[2].Path].Outcome != WorktreeAuditReclaimed {
				t.Fatalf("audit did not act on exactly its reported claims: %+v", out)
			}
			for i, entry := range entries {
				var state string
				if err := f.store.db.QueryRow(`SELECT state FROM worktree_entries WHERE claim_op_id=?`, entry.ClaimOpID).Scan(&state); err != nil {
					t.Fatal(err)
				}
				want := worktreeEntryActive
				if i == 2 {
					want = worktreeEntryReclaimed
				}
				if state != want {
					t.Fatalf("claim %d state=%s want=%s", i, state, want)
				}
			}
			if _, kept := git.worktrees[entries[2].Path]; kept {
				t.Fatal("audit reported success without removing that member")
			}
		})
	}
}
