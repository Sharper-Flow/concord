package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// CD-0096 part 3a (#689): the Inspect and Verify tiers through the agent
// surface against real git. A session reads and verifies another work item's
// active worktree in the same Project: inspection is read-only and never
// moves the persistent target; verification holds the exclusive lease and
// refuses typed completion when tracked files changed.

func tiersInvoke(t *testing.T, s *store.Store, service *Service, grant Authority, tool, operation string, input map[string]any) Envelope {
	t.Helper()
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(input)
	request := InvokeRequest{Tool: tool, Operation: operation, Input: raw}
	response, err := Dispatch(context.Background(), s, service, request, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatalf("dispatch err=%v", err)
	}
	return response
}

// tiersFixture binds session-1 to work-1's canonical worktree and session-2
// to work-2's, both inside project-1, so session-1 exercises cross-worktree
// tiers against work-2's tree.
func tiersFixture(t *testing.T) (*store.Store, *Service, Authority, *Service, Authority, string) {
	t.Helper()
	s, service, grant, repoRoot := retargetDispatchFixture(t)
	if response := retargetInvoke(t, s, service, grant, map[string]any{
		"work_id": "work-1", "expected_version": 2, "expected_target_version": 0, "idempotency_key": "tiers-rt-1",
	}); response.Outcome != OutcomeOK {
		t.Fatalf("bind work-1 response=%+v err=%+v", response, response.Error)
	}
	second, _, secondGrant := newAuthorizedService(t, s, "client-2", "human-2", []Capability{"work_transition", "product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	if response := retargetInvoke(t, s, second, secondGrant, map[string]any{
		"work_id": "work-2", "expected_version": 2, "expected_target_version": 0, "idempotency_key": "tiers-rt-2",
	}); response.Outcome != OutcomeOK {
		t.Fatalf("bind work-2 response=%+v err=%+v", response, response.Error)
	}
	return s, service, grant, second, secondGrant, repoRoot
}

func tiersInspect(t *testing.T, s *store.Store, service *Service, grant Authority, workID, mode, path string) (Envelope, map[string]any) {
	t.Helper()
	input := map[string]any{"work_id": workID, "mode": mode}
	if path != "" {
		input["path"] = path
	}
	response := tiersInvoke(t, s, service, grant, "concord_work_browse", "worktree_inspect", input)
	var payload map[string]any
	if response.Outcome == OutcomeOK {
		if err := json.Unmarshal(response.Result, &payload); err != nil {
			t.Fatalf("inspect result=%s", response.Result)
		}
	}
	return response, payload
}

func TestWorktreeInspectReadsSameProjectWorktreeWithoutMovingTarget(t *testing.T) {
	s, service, grant, _, secondGrant, _ := tiersFixture(t)
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-2")

	// Tracked change in the other session's worktree, made directly on disk.
	if err := os.WriteFile(filepath.Join(worktreePath, "README.md"), []byte("# fixture\ninspected\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	response, payload := tiersInspect(t, s, service, grant, "work-2", "status", "")
	if response.Outcome != OutcomeOK {
		t.Fatalf("inspect response=%+v err=%+v", response, response.Error)
	}
	if response.QueryID != "CD-0096.R1" {
		t.Fatalf("query_id=%q, want CD-0096.R1", response.QueryID)
	}
	if payload["path"] != worktreePath || payload["branch"] != "work/work-2" {
		t.Fatalf("payload=%+v, want the identity-derived worktree", payload)
	}
	if !strings.Contains(payload["content"].(string), "README.md") {
		t.Fatalf("status=%v, want the modified file visible", payload["content"])
	}

	diff, diffPayload := tiersInspect(t, s, service, grant, "work-2", "diff", "")
	if diff.Outcome != OutcomeOK || !strings.Contains(diffPayload["content"].(string), "inspected") {
		t.Fatalf("diff response=%+v payload=%+v", diff, diffPayload)
	}
	file, filePayload := tiersInspect(t, s, service, grant, "work-2", "file", "README.md")
	if file.Outcome != OutcomeOK || !strings.Contains(filePayload["content"].(string), "inspected") {
		t.Fatalf("file response=%+v payload=%+v", file, filePayload)
	}

	// The persistent effective target of neither session moved.
	target, found, err := s.SessionWorktreeTarget(context.Background(), store.SessionWorktreeOwner{ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef})
	if err != nil || !found || target.WorkID != "work-1" {
		t.Fatalf("session-1 target=%+v found=%v err=%v, want its own binding unchanged", target, found, err)
	}
	secondTarget, found, err := s.SessionWorktreeTarget(context.Background(), store.SessionWorktreeOwner{ClientRef: secondGrant.ClientRef, AgentRef: secondGrant.AgentRef, SessionRef: secondGrant.SessionRef})
	if err != nil || !found || secondTarget.WorkID != "work-2" {
		t.Fatalf("session-2 target=%+v found=%v err=%v, want its own binding unchanged", secondTarget, found, err)
	}
	var leases int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_verify_leases`).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if leases != 0 {
		t.Fatalf("leases=%d, want the Inspect tier lease-free", leases)
	}
}

func TestWorktreeInspectRefusesWithoutActiveEntry(t *testing.T) {
	s, service, grant, _, _, _ := tiersFixture(t)
	// A third work item exists in the same Project with no worktree.
	events := []store.Event{
		{EventID: "tiers-work-3-create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-3", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Tier Three","priority":1}`)},
		{EventID: "tiers-work-3-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-3", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-3"): 0}}); err != nil {
		t.Fatal(err)
	}
	response, _ := tiersInspect(t, s, service, grant, "work-3", "status", "")
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "unknown_scope" {
		t.Fatalf("response=%+v, want typed unknown_scope for a worktree-less item", response)
	}
}

func TestWorktreeVerifyRunsUnderLeaseThroughToolSurface(t *testing.T) {
	s, service, grant, _, _, _ := tiersFixture(t)
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-2")

	response := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": "work-2", "command": []string{"true"}, "idempotency_key": "tiers-verify-1",
	})
	if response.Outcome != OutcomeOK {
		t.Fatalf("verify response=%+v err=%+v", response, response.Error)
	}
	var result struct {
		ExitCode            int    `json:"exit_code"`
		Path                string `json:"path"`
		TrackedFilesChanged bool   `json:"tracked_files_changed"`
		LeaseID             string `json:"lease_id"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.TrackedFilesChanged || result.Path != worktreePath || result.LeaseID == "" {
		t.Fatalf("result=%+v", result)
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM worktree_verify_leases WHERE lease_id=?`, result.LeaseID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "released" {
		t.Fatalf("lease state=%q, want released", state)
	}

	// Exact replay returns the recorded result without a second run.
	replay := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": "work-2", "command": []string{"true"}, "idempotency_key": "tiers-verify-1",
	})
	if replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("replay=%+v replayed=%v", replay, replay.Replayed)
	}
}

func TestWorktreeVerifyRefusesTrackedFileMutation(t *testing.T) {
	s, service, grant, _, _, _ := tiersFixture(t)
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-2")

	refused := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": "work-2", "command": []string{"sh", "-c", "echo mutated >> README.md"}, "idempotency_key": "tiers-verify-mut",
	})
	if refused.Outcome != OutcomeError || refused.Error == nil {
		t.Fatalf("response=%+v, want typed refusal", refused)
	}
	if refused.Error.Kind != "operation_conflict" {
		t.Fatalf("error.kind=%q, want operation_conflict", refused.Error.Kind)
	}
	if !strings.Contains(refused.Error.Message, worktreePath) || !strings.Contains(refused.Error.Message, "tracked files changed") {
		t.Fatalf("error.message=%q, want the worktree and the change named", refused.Error.Message)
	}
	if refused.Error.RecoveryAction.Kind != "reconcile_operation" {
		t.Fatalf("recovery=%q, want reconcile_operation", refused.Error.RecoveryAction.Kind)
	}
	var held int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_verify_leases WHERE state='held'`).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held != 0 {
		t.Fatalf("held leases=%d, want the refusal to release its lease", held)
	}
}

func TestWorktreeVerifyConcurrentLeaseRefusesTyped(t *testing.T) {
	s, service, grant, second, secondGrant, _ := tiersFixture(t)
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-2")

	// A held lease from session-2's in-flight verify of the same worktree.
	if response := retargetInvoke(t, s, second, secondGrant, map[string]any{
		"work_id": "work-2", "expected_version": 3, "expected_target_version": 1, "idempotency_key": "tiers-rt-2b",
	}); response.Outcome != OutcomeOK {
		t.Fatalf("re-assert response=%+v err=%+v", response, response.Error)
	}
	commandJSON, _ := json.Marshal([]string{"go", "test", "./..."})
	stamp := fixedTime().UTC().Format(time.RFC3339Nano)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,outcome) VALUES('foreign-lease','work-2','project-1',?, 'held',?,?,?,?,?,?, 'running')`,
		worktreePath, secondGrant.ClientRef, secondGrant.AgentRef, secondGrant.SessionRef, secondGrant.PrincipalRef, string(commandJSON), stamp); err != nil {
		t.Fatal(err)
	}

	refused := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": "work-2", "command": []string{"go", "test", "./..."}, "idempotency_key": "tiers-verify-contended",
	})
	if refused.Outcome != OutcomeError || refused.Error == nil {
		t.Fatalf("response=%+v, want typed refusal", refused)
	}
	if refused.Error.Kind != "operation_conflict" || refused.Error.RetrySafe != true {
		t.Fatalf("error=%+v, want retry-safe operation_conflict", refused.Error)
	}
	if !strings.Contains(refused.Error.Message, secondGrant.SessionRef) {
		t.Fatalf("error.message=%q, want the holding session named", refused.Error.Message)
	}
}
