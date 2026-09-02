package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// CD-0096 part 2 (#689): the in-session retarget surface. The session names
// its current work item; Concord derives, creates or adopts the canonical
// worktree and persists the session's effective target. The host process
// directory the envelope carries never changes.

func retargetDispatchFixture(t *testing.T) (*store.Store, *Service, Authority, string) {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	events := []store.Event{
		{EventID: "wt-retarget-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"WT Retarget","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "wt-retarget-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"WT Retarget Project"}`)},
		{EventID: "wt-retarget-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"retarget fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "wt-retarget-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Retarget One","priority":1}`)},
		{EventID: "wt-retarget-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		{EventID: "wt-retarget-work-2", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Retarget Two","priority":1}`)},
		{EventID: "wt-retarget-work-2-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-2"): 0}}); err != nil {
		t.Fatal(err)
	}

	repoRoot := t.TempDir()
	gitRun(t, repoRoot, "init", "-b", "main")
	gitRun(t, repoRoot, "config", "user.email", "concord@example.invalid")
	gitRun(t, repoRoot, "config", "user.name", "Concord Retarget Test")
	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("# fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repoRoot, "add", "README.md")
	gitRun(t, repoRoot, "commit", "-m", "fixture base")
	if err := s.AddProjectLocator(ctx, "project-1", store.ProjectLocator{ID: "path-1", Kind: store.LocatorCanonicalPath, Value: repoRoot}, 1); err != nil {
		t.Fatal(err)
	}

	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []Capability{"work_transition", "product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	return s, service, grant, repoRoot
}

func retargetInvoke(t *testing.T, s *store.Store, service *Service, grant Authority, input map[string]any) Envelope {
	t.Helper()
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(input)
	request := InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_retarget", Input: raw}
	response, err := Dispatch(context.Background(), s, service, request, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatalf("dispatch err=%v", err)
	}
	return response
}

func TestWorktreeRetargetBindsAndPersistsThroughToolSurface(t *testing.T) {
	s, service, grant, repoRoot := retargetDispatchFixture(t)
	canonical := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")

	response := retargetInvoke(t, s, service, grant, map[string]any{
		"work_id": "work-1", "expected_version": 2, "expected_target_version": 0, "idempotency_key": "rt-1",
	})
	if response.Outcome != OutcomeOK {
		t.Fatalf("response=%+v err=%+v", response, response.Error)
	}

	// Later operations resolve through the persisted effective target.
	target, found, err := s.SessionWorktreeTarget(context.Background(), store.SessionWorktreeOwner{ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef})
	if err != nil || !found {
		t.Fatalf("target=%+v found=%v err=%v", target, found, err)
	}
	if target.Path != canonical || target.Branch != "work/work-1" || target.TargetVersion != 1 {
		t.Fatalf("target=%+v, want the identity-derived canonical worktree", target)
	}
	if !strings.Contains(gitRun(t, repoRoot, "worktree", "list"), canonical) {
		t.Fatalf("native worktree missing at %s", canonical)
	}

	// The binding survives the turn: a later re-assert through the same
	// surface advances the target version and adopts the verified worktree.
	again := retargetInvoke(t, s, service, grant, map[string]any{
		"work_id": "work-1", "expected_version": 3, "expected_target_version": 1, "idempotency_key": "rt-2",
	})
	if again.Outcome != OutcomeOK {
		t.Fatalf("re-assert response=%+v err=%+v", again, again.Error)
	}
	if got := strings.Count(gitRun(t, repoRoot, "worktree", "list"), canonical); got != 1 {
		t.Fatalf("worktree lines=%d, want exactly one native worktree", got)
	}
	_, _, read := s.SessionWorktreeTarget(context.Background(), store.SessionWorktreeOwner{ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef})
	if read != nil {
		t.Fatalf("read err=%v", read)
	}
}

func TestWorktreeRetargetRefusesCrossWorkWithTypedOwnershipConflict(t *testing.T) {
	s, service, grant, _ := retargetDispatchFixture(t)
	if response := retargetInvoke(t, s, service, grant, map[string]any{
		"work_id": "work-1", "expected_version": 2, "expected_target_version": 0, "idempotency_key": "rt-1",
	}); response.Outcome != OutcomeOK {
		t.Fatalf("bind response=%+v err=%+v", response, response.Error)
	}

	// The session's current item is work-1; naming work-2 is a takeover
	// without authority, refused typed with owner and recovery action.
	refused := retargetInvoke(t, s, service, grant, map[string]any{
		"work_id": "work-2", "expected_version": 2, "expected_target_version": 1, "idempotency_key": "rt-2",
	})
	if refused.Outcome != OutcomeError || refused.Error == nil {
		t.Fatalf("response=%+v, want typed refusal", refused)
	}
	if refused.Error.Kind != "operation_conflict" {
		t.Fatalf("error.kind=%q, want operation_conflict", refused.Error.Kind)
	}
	if !strings.Contains(refused.Error.Message, "work-1") || !strings.Contains(refused.Error.Message, "session-1") || !strings.Contains(refused.Error.Message, "takeover") {
		t.Fatalf("error.message=%q, want owner identity and takeover naming", refused.Error.Message)
	}
	if refused.Error.RecoveryAction.Kind != "contact_operator" {
		t.Fatalf("recovery=%q, want contact_operator", refused.Error.RecoveryAction.Kind)
	}
	// The binding is unchanged.
	target, found, err := s.SessionWorktreeTarget(context.Background(), store.SessionWorktreeOwner{ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef})
	if err != nil || !found || target.WorkID != "work-1" {
		t.Fatalf("target=%+v found=%v err=%v, want the original binding", target, found, err)
	}
}

func TestWorktreeRetargetStaleTargetVersionFailsClosed(t *testing.T) {
	s, service, grant, _ := retargetDispatchFixture(t)
	if response := retargetInvoke(t, s, service, grant, map[string]any{
		"work_id": "work-1", "expected_version": 2, "expected_target_version": 0, "idempotency_key": "rt-1",
	}); response.Outcome != OutcomeOK {
		t.Fatalf("bind response=%+v err=%+v", response, response.Error)
	}

	stale := retargetInvoke(t, s, service, grant, map[string]any{
		"work_id": "work-1", "expected_version": 3, "expected_target_version": 0, "idempotency_key": "rt-2",
	})
	if stale.Outcome != OutcomeError || stale.Error == nil || stale.Error.Kind != "version_conflict" {
		t.Fatalf("response=%+v, want stale version_conflict", stale)
	}
}

func TestWorktreeRetargetSecondSessionRefusesWithTypedOwnershipConflict(t *testing.T) {
	s, service, grant, _ := retargetDispatchFixture(t)
	if response := retargetInvoke(t, s, service, grant, map[string]any{
		"work_id": "work-1", "expected_version": 2, "expected_target_version": 0, "idempotency_key": "rt-1",
	}); response.Outcome != OutcomeOK {
		t.Fatalf("bind response=%+v err=%+v", response, response.Error)
	}

	// A second session in the same Product adopts no one else's worktree.
	second, _, secondGrant := newAuthorizedService(t, s, "client-2", "human-2", []Capability{"work_transition", "product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	refused := retargetInvoke(t, s, second, secondGrant, map[string]any{
		"work_id": "work-1", "expected_version": 3, "expected_target_version": 0, "idempotency_key": "rt-second-1",
	})
	if refused.Outcome != OutcomeError || refused.Error == nil {
		t.Fatalf("response=%+v, want typed refusal", refused)
	}
	if refused.Error.Kind != "operation_conflict" {
		t.Fatalf("error.kind=%q, want operation_conflict", refused.Error.Kind)
	}
	if !strings.Contains(refused.Error.Message, "session-1") {
		t.Fatalf("error.message=%q, want the holding session named", refused.Error.Message)
	}
}

func TestWorktreeRetargetReplayIsIdempotent(t *testing.T) {
	s, service, grant, repoRoot := retargetDispatchFixture(t)
	input := map[string]any{"work_id": "work-1", "expected_version": 2, "expected_target_version": 0, "idempotency_key": "rt-replay"}
	first := retargetInvoke(t, s, service, grant, input)
	if first.Outcome != OutcomeOK {
		t.Fatalf("first response=%+v err=%+v", first, first.Error)
	}
	replay := retargetInvoke(t, s, service, grant, input)
	if replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("replay response=%+v replayed=%v, want idempotent replay", replay, replay.Replayed)
	}
	canonical := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")
	if got := strings.Count(gitRun(t, repoRoot, "worktree", "list"), canonical); got != 1 {
		t.Fatalf("worktree lines=%d, want exactly one native worktree", got)
	}
}
