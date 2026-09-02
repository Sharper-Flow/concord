package store

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// CD-0096 D1/D2/D3/D5 store coverage: the retarget derives the canonical
// worktree from identity only, persists the session's effective target, tiers
// cross-worktree authority behind a typed ownership conflict, and fails
// closed on stale target versions.

func retargetOwner(client, agent, session string) SessionWorktreeOwner {
	return SessionWorktreeOwner{ClientRef: client, AgentRef: agent, SessionRef: session}
}

func retargetRequest(git *fakeWorktreeGit, owner SessionWorktreeOwner, workID string, expectedWork, expectedTarget int64) WorktreeRetargetRequest {
	return WorktreeRetargetRequest{
		Owner: owner, WorkID: workID,
		ExpectedWorkVersion:   expectedWork,
		ExpectedTargetVersion: expectedTarget,
		PrincipalRef:          "principal-1", RequestID: "req-rt-1", OpID: "rt-op-" + workID,
		Now: time.Unix(10, 0).UTC(), Runner: git,
	}
}

func canonicalTargetPath(s *Store, projectID, workID string) string {
	return filepath.Join(filepath.Dir(s.Path()), "worktrees", projectID, workID)
}

// seedRetargetWork adds one work item with a primary membership in the
// worktree fixture's Project.
func seedRetargetWork(t *testing.T, s *Store, workID string) {
	t.Helper()
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		{EventID: workID + "-rt-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: jsonRaw(`{"work_kind":"task","title":"Retarget ` + workID + `","priority":1}`)},
		{EventID: workID + "-rt-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-w","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}})
	if err != nil {
		t.Fatal(err)
	}
}

// seedRetargetLifecycle moves a fixture work item to a terminal lifecycle.
func seedRetargetLifecycle(t *testing.T, s *Store, workID, to string, expected int64) {
	t.Helper()
	payload := `{"from":"needed","to":"` + to + `","reason":"fixture terminal","evidence_refs":["fixture"],"expected_version":` + strconv.FormatInt(expected, 10) + `,"resulting_version":` + strconv.FormatInt(expected+1, 10) + `}`
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		{EventID: workID + "-rt-" + to, Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(payload)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expected}})
	if err != nil {
		t.Fatal(err)
	}
}

// seedBootstrapLaunch writes a bootstrap operation row whose launch state the
// retarget must respect (CD-0096 D6).
func seedBootstrapLaunch(t *testing.T, s *Store, workID, sessionID, launchState string) {
	t.Helper()
	digest := "sha256:" + strings.Repeat("b", 64)
	stamp := time.Unix(1, 0).UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`INSERT INTO bootstrap_operations(idempotency_key,operation_id,request_digest,request_json,product_id,project_id,work_id,repo_path,expected_version,state,launch_state,launch_session_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"boot-"+workID, "op-"+workID, digest, "{}", "product-w", "project-w", workID, "/repo", 2, "completed", launchState, sessionID, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetargetCreatesCanonicalWorktreeAndBindsSession(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	owner := retargetOwner("client-1", "agent-1", "session-1")

	target, err := s.RetargetSessionWorktree(context.Background(), retargetRequest(git, owner, "work-w", 2, 0))
	if err != nil {
		t.Fatal(err)
	}
	if target.State != sessionTargetActive || target.TargetVersion != 1 || target.WorkID != "work-w" || target.ProjectID != "project-w" {
		t.Fatalf("target=%+v", target)
	}
	if target.Branch != "work/work-w" {
		t.Fatalf("branch=%q, want the identity-derived work/<work_id>", target.Branch)
	}
	if target.Path != canonicalTargetPath(s, "project-w", "work-w") {
		t.Fatalf("path=%q, want the locator-derived canonical path", target.Path)
	}
	if branch, ok := git.worktrees[target.Path]; !ok || branch != "work/work-w" {
		t.Fatalf("native worktree missing or on %q", branch)
	}
	entries, err := s.WorktreeEntries(context.Background(), "work-w")
	if err != nil || len(entries) != 1 || entries[0].State != worktreeEntryActive {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}

	// The binding is persistent: a later read resolves through it.
	read, found, err := s.SessionWorktreeTarget(context.Background(), owner)
	if err != nil || !found || read.Path != target.Path || read.TargetVersion != 1 {
		t.Fatalf("read=%+v found=%v err=%v", read, found, err)
	}
}

func TestRetargetReassertSucceedsAndStaleTargetVersionFailsClosed(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	owner := retargetOwner("client-1", "agent-1", "session-1")
	ctx := context.Background()

	if _, err := s.RetargetSessionWorktree(ctx, retargetRequest(git, owner, "work-w", 2, 0)); err != nil {
		t.Fatal(err)
	}
	// A later retarget of the same own item adopts the verified worktree: no
	// second native create, and the target version advances.
	again, err := s.RetargetSessionWorktree(ctx, retargetRequest(git, owner, "work-w", 3, 1))
	if err != nil {
		t.Fatal(err)
	}
	if again.TargetVersion != 2 {
		t.Fatalf("target version=%d, want 2", again.TargetVersion)
	}
	if got := git.countCalls("worktree add"); got != 1 {
		t.Fatalf("native creates=%d, want exactly one", got)
	}

	// Stale target versions fail closed; the binding does not move.
	_, err = s.RetargetSessionWorktree(ctx, retargetRequest(git, owner, "work-w", 4, 1))
	if err == nil || err.(*Failure).Kind != KindVersionConflict {
		t.Fatalf("err=%v, want stale version conflict", err)
	}
	read, found, readErr := s.SessionWorktreeTarget(ctx, owner)
	if readErr != nil || !found || read.TargetVersion != 2 {
		t.Fatalf("read=%+v found=%v err=%v, want binding unchanged", read, found, readErr)
	}
}

// A pin against a session that holds no binding fails closed under CD-0096 D5,
// and it names the absence rather than a version conflict. A version conflict
// must carry the live version the caller should re-read, and there is none
// here: the agent envelope refuses such a refusal at marshal time, so the
// classification would never reach a caller.
func TestRetargetFirstBindingWithNonzeroVersionRefuses(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	owner := retargetOwner("client-1", "agent-1", "session-1")

	_, err := s.RetargetSessionWorktree(context.Background(), retargetRequest(git, owner, "work-w", 2, 3))
	if err == nil || err.(*Failure).Kind != KindProjectionNotFound {
		t.Fatalf("err=%v, want projection_not_found on a pinned version without a binding", err)
	}
	if _, found, readErr := s.SessionWorktreeTarget(context.Background(), owner); readErr != nil || found {
		t.Fatalf("found=%v err=%v, want no binding recorded", found, readErr)
	}
}

func TestRetargetCrossWorkRefusesWithTypedOwnershipConflict(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	seedRetargetWork(t, s, "work-x")
	owner := retargetOwner("client-1", "agent-1", "session-1")
	ctx := context.Background()

	if _, err := s.RetargetSessionWorktree(ctx, retargetRequest(git, owner, "work-w", 2, 0)); err != nil {
		t.Fatal(err)
	}
	// The session's current item is work-w; naming work-x is a takeover.
	_, err := s.RetargetSessionWorktree(ctx, retargetRequest(git, owner, "work-x", 2, 1))
	if err == nil {
		t.Fatal("cross-work retarget must refuse")
	}
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindWorktreeOwnershipConflict {
		t.Fatalf("err=%v, want worktree ownership conflict", err)
	}
	if !strings.Contains(failure.Detail, "work-w") || !strings.Contains(failure.Detail, "takeover") || !strings.Contains(failure.Detail, "session-1") {
		t.Fatalf("detail=%q, want the owner identity and the recovery action", failure.Detail)
	}
	if failure.RecoveryAction == "" {
		t.Fatal("ownership conflict must name a recovery action")
	}
	// The binding is unchanged.
	read, found, readErr := s.SessionWorktreeTarget(ctx, owner)
	if readErr != nil || !found || read.WorkID != "work-w" {
		t.Fatalf("read=%+v found=%v err=%v, want the original binding", read, found, readErr)
	}
}

func TestRetargetSecondSessionRefusesWithTypedOwnershipConflict(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	first := retargetOwner("client-1", "agent-1", "session-1")
	second := retargetOwner("client-1", "agent-1", "session-2")
	ctx := context.Background()

	if _, err := s.RetargetSessionWorktree(ctx, retargetRequest(git, first, "work-w", 2, 0)); err != nil {
		t.Fatal(err)
	}
	_, err := s.RetargetSessionWorktree(ctx, retargetRequest(git, second, "work-w", 2, 0))
	if err == nil {
		t.Fatal("second session retarget must refuse")
	}
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindWorktreeOwnershipConflict {
		t.Fatalf("err=%v, want worktree ownership conflict", err)
	}
	if !strings.Contains(failure.Detail, "session-1") {
		t.Fatalf("detail=%q, want the holding session named", failure.Detail)
	}
	if _, found, readErr := s.SessionWorktreeTarget(ctx, second); readErr != nil || found {
		t.Fatalf("found=%v err=%v, want no binding for the second session", found, readErr)
	}
}

func TestRetargetRefusesTerminalWork(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	seedRetargetLifecycle(t, s, "work-w", "completed", 2)
	_, err := s.RetargetSessionWorktree(context.Background(), retargetRequest(git, retargetOwner("client-1", "agent-1", "session-1"), "work-w", 3, 0))
	if err == nil || err.(*Failure).Kind != KindInvalidTransition {
		t.Fatalf("err=%v, want invalid transition for terminal work", err)
	}
}

func TestRetargetRequiresPrimaryProject(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	// work-y holds only a secondary membership, so no primary Project locates
	// its repository.
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		{EventID: "work-y-rt-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "work-y", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: jsonRaw(`{"work_kind":"task","title":"No Primary","priority":1}`)},
		{EventID: "work-y-rt-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "work-y", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-w","role":"secondary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-y"): 0}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RetargetSessionWorktree(context.Background(), retargetRequest(git, retargetOwner("client-1", "agent-1", "session-1"), "work-y", 2, 0))
	if err == nil || err.(*Failure).Kind != KindUnknownScope {
		t.Fatalf("err=%v, want unknown scope for work without a primary Project", err)
	}
}

func TestRetargetRefusesLiveBootstrapLaunchUnlessSameSession(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	seedBootstrapLaunch(t, s, "work-w", "session-launched", "running")

	_, err := s.RetargetSessionWorktree(ctx, retargetRequest(git, retargetOwner("client-1", "agent-1", "session-1"), "work-w", 2, 0))
	if err == nil {
		t.Fatal("retarget under a live foreign launch must refuse")
	}
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindWorktreeOwnershipConflict {
		t.Fatalf("err=%v, want worktree ownership conflict", err)
	}
	if !strings.Contains(failure.Detail, "session-launched") {
		t.Fatalf("detail=%q, want the launched session named", failure.Detail)
	}

	// The launched session itself converges on the same worktree (CD-0096 D6).
	target, err := s.RetargetSessionWorktree(ctx, retargetRequest(git, retargetOwner("client-2", "agent-2", "session-launched"), "work-w", 2, 0))
	if err != nil {
		t.Fatal(err)
	}
	if target.State != sessionTargetActive || target.TargetVersion != 1 {
		t.Fatalf("target=%+v, want the launched session bound", target)
	}
}

func TestRetargetAdoptsExistingVerifiedWorktree(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	// The bootstrap route claimed the canonical worktree first (CD-0088).
	claim := WorktreeClaimRequest{
		OpID: "boot-op-1", WorkID: "work-w", ProjectID: "project-w",
		Branch: "work/work-w", BaseSHA: git.branches["main"], Path: canonicalTargetPath(s, "project-w", "work-w"),
		PrincipalRef: "principal-boot", RequestID: "req-boot",
		ExpectedVersion: 2, Now: time.Unix(5, 0).UTC(), Runner: git,
	}
	if _, err := s.ClaimWorktree(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	target, err := s.RetargetSessionWorktree(context.Background(), retargetRequest(git, retargetOwner("client-1", "agent-1", "session-1"), "work-w", 3, 0))
	if err != nil {
		t.Fatal(err)
	}
	if target.Path != canonicalTargetPath(s, "project-w", "work-w") {
		t.Fatalf("path=%q, want adoption of the verified worktree", target.Path)
	}
	if got := git.countCalls("worktree add"); got != 1 {
		t.Fatalf("native creates=%d, want the bootstrap create only", got)
	}
	entries, err := s.WorktreeEntries(context.Background(), "work-w")
	if err != nil || len(entries) != 1 || entries[0].ClaimOpID != "boot-op-1" {
		t.Fatalf("entries=%+v err=%v, want the bootstrap claim to remain the owner", entries, err)
	}
}

func TestRetargetRefusesStoredPathDrift(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	// A verified claim pinned outside the locator-derived canonical path.
	stray := WorktreeClaimRequest{
		OpID: "stray-op-1", WorkID: "work-w", ProjectID: "project-w",
		Branch: "work/work-w", BaseSHA: git.branches["main"], Path: filepath.Join(filepath.Dir(s.Path()), "stray-wt"),
		PrincipalRef: "principal-1", RequestID: "req-stray",
		ExpectedVersion: 2, Now: time.Unix(5, 0).UTC(), Runner: git,
	}
	if _, err := s.ClaimWorktree(context.Background(), stray); err != nil {
		t.Fatal(err)
	}
	_, err := s.RetargetSessionWorktree(context.Background(), retargetRequest(git, retargetOwner("client-1", "agent-1", "session-1"), "work-w", 3, 0))
	if err == nil || err.(*Failure).Kind != KindProjectionConflict {
		t.Fatalf("err=%v, want projection conflict for locator drift", err)
	}
}

func TestRetargetNilStoreRefuses(t *testing.T) {
	var s *Store
	if _, err := s.RetargetSessionWorktree(context.Background(), WorktreeRetargetRequest{WorkID: "work-w"}); err == nil || err.(*Failure).Kind != KindUnavailable {
		t.Fatalf("err=%v, want unavailable", err)
	}
	if _, _, err := s.SessionWorktreeTarget(context.Background(), retargetOwner("c1", "a1", "s1")); err == nil || err.(*Failure).Kind != KindUnavailable {
		t.Fatalf("err=%v, want unavailable", err)
	}
}
