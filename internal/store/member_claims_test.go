package store

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func boundMemberClaimFixture(t *testing.T) (workContextFixture, workflowMemberGroup, []SessionWorktreeOwner) {
	t.Helper()
	f, jobs := memberGroupFixture(t)
	result, err := memberGroupAction(t, f, f.owner, map[string]any{"assignment_group": map[string]any{"members": jobs}})
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Group workflowMemberGroup `json:"assignment_group"`
	}
	if err := json.Unmarshal(result.Result, &output); err != nil {
		t.Fatal(err)
	}
	var base string
	if err := f.store.db.QueryRow(`SELECT base_subject FROM workflow_member_groups WHERE work_id=? AND group_id=?`, f.workID, output.Group.GroupID).Scan(&base); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(base), &output.Group.BaseSubject); err != nil {
		t.Fatal(err)
	}
	owners := make([]SessionWorktreeOwner, len(output.Group.Members))
	for i, member := range output.Group.Members {
		owners[i] = SessionWorktreeOwner{ClientRef: f.owner.ClientRef, AgentRef: "agent/member", SessionRef: "session/" + member.MemberID}
		binding := workflowMemberBinding{GroupID: output.Group.GroupID, MemberID: member.MemberID, ProjectID: member.ProjectID, Actor: workflowMemberActor{PrincipalRef: f.owner.PrincipalRef, ClientRef: owners[i].ClientRef, AgentRef: owners[i].AgentRef, SessionRef: owners[i].SessionRef}}
		if _, err := memberGroupAction(t, f, f.owner, map[string]any{"member_binding": binding}); err != nil {
			t.Fatal(err)
		}
	}
	return f, output.Group, owners
}

func TestMemberClaimsDeriveIsolatedNativeIdentity(t *testing.T) {
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
	for i, member := range group.Members {
		req := WorktreeClaimRequest{OpID: "claim-" + member.MemberID, WorkID: f.workID, ProjectID: member.ProjectID, BaseSHA: group.BaseSubject.Commit, PrincipalRef: f.owner.PrincipalRef, RequestID: "request-" + member.MemberID, SessionRef: owners[i].SessionRef, Owner: owners[i], ExpectedVersion: readWorkVersion(t, f.store, f.workID), Runner: git, Now: time.Unix(200, 0).UTC()}
		claimed, err := f.store.ClaimWorktree(t.Context(), req)
		if err != nil {
			t.Fatalf("claim member %d: %v", i, err)
		}
		wantBranch := "work/" + f.workID + "--" + member.MemberID
		wantPath := filepath.Join(filepath.Dir(f.store.Path()), "worktrees", member.ProjectID, f.workID+"--"+member.MemberID)
		if claimed.Entry.MemberID != member.MemberID || claimed.Entry.Branch != wantBranch || claimed.Entry.Path != wantPath {
			t.Fatalf("wrong derived member identity: %+v", claimed.Entry)
		}
		var confirmed bool
		if err := f.store.db.QueryRow(`SELECT confirmed FROM workflow_group_members WHERE work_id=? AND group_id=? AND member_id=?`, f.workID, group.GroupID, member.MemberID).Scan(&confirmed); err != nil || !confirmed {
			t.Fatalf("claim did not confirm own authenticated binding: %v, %v", confirmed, err)
		}
		replayed, err := f.store.ClaimWorktree(t.Context(), req)
		if err != nil || !replayed.Reconciled || replayed.Entry.ClaimOpID != claimed.Entry.ClaimOpID {
			t.Fatalf("member claim exact replay: %+v %v", replayed, err)
		}
		ctx := WithSessionWorktreeOwner(t.Context(), owners[i])
		resumed, err := f.store.ResumeWorktreeLocation(ctx, "product", member.ProjectID, f.workID)
		if err != nil || resumed.Path != wantPath {
			t.Fatalf("own member resume selected another scope: %+v %v", resumed, err)
		}
		if _, err := activeWorktreeEntryForProject(ctx, f.store.db, "test", "foreign-work", member.ProjectID); err == nil {
			t.Fatal("member scope reached foreign work")
		}
		before := len(git.calls)
		wrong := req
		wrong.Owner = owners[1-i]
		if _, _, err := f.store.PrepareWorktreeClaimNative(t.Context(), wrong); err == nil || !hasFailureKind(err, KindUnauthorized) {
			t.Fatalf("wrong authenticated session admitted: %v", err)
		}
		if len(git.calls) != before {
			t.Fatal("identity refusal reached native Git")
		}
		wrong = req
		wrong.Owner = SessionWorktreeOwner{}
		if _, _, err := f.store.PrepareWorktreeClaimNative(t.Context(), wrong); err == nil || !hasFailureKind(err, KindUnauthorized) {
			t.Fatalf("missing authenticated member tuple selected integration: %v", err)
		}
		wrong = req
		wrong.BaseSHA = strings.Repeat("0", 40)
		if _, _, err := f.store.PrepareWorktreeClaimNative(t.Context(), wrong); err == nil {
			t.Fatal("caller replaced the group's pinned integration base")
		}
	}
	unchanged, err := activeWorktreeEntryForProject(t.Context(), f.store.db, "test", f.workID, "project")
	if err != nil || unchanged.ClaimOpID != main.ClaimOpID || unchanged.Path != main.Path {
		t.Fatalf("member claims changed integration: %+v %v", unchanged, err)
	}
	entries, err := f.store.WorktreeEntries(t.Context(), f.workID)
	if err != nil || len(entries) != 3 {
		t.Fatalf("one set must retain integration plus two isolated members: %+v %v", entries, err)
	}
}
