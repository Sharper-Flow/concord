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
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// CD-0096 D3: the Inspect and Verify tiers through the agent surface against
// real git. A session reads and verifies another work item's active worktree
// in the same Project: inspection is read-only, and verification holds the
// exclusive lease and refuses typed completion when tracked files changed.

func tiersRepoFixture(t *testing.T) (*store.Store, *Service, Authority, string) {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	events := []store.Event{
		{EventID: "wt-tiers-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"WT Tiers","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "wt-tiers-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"WT Tiers Project"}`)},
		{EventID: "wt-tiers-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"tiers fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "wt-tiers-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Tiers One","priority":1}`)},
		{EventID: "wt-tiers-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		{EventID: "wt-tiers-work-2", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Tiers Two","priority":1}`)},
		{EventID: "wt-tiers-work-2-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-2"): 0}}); err != nil {
		t.Fatal(err)
	}

	repoRoot := t.TempDir()
	gitRun(t, repoRoot, "init", "-b", "main")
	gitRun(t, repoRoot, "config", "user.email", "concord@example.invalid")
	gitRun(t, repoRoot, "config", "user.name", "Concord Tiers Test")
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

// tiersFixture claims work-1's canonical worktree under session-1 and
// work-2's under session-2, both inside project-1, so session-1 exercises
// cross-worktree tiers against work-2's tree.
func tiersFixture(t *testing.T) (*store.Store, *Service, Authority, *Service, Authority, string) {
	t.Helper()
	s, service, grant, repoRoot := tiersRepoFixture(t)
	baseSHA := gitRun(t, repoRoot, "rev-parse", "HEAD")
	worktreeRoot := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1")
	if response := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_claim", map[string]any{
		"work_id": "work-1", "project_id": "project-1", "branch": "work/work-1", "base_sha": baseSHA, "path": filepath.Join(worktreeRoot, "work-1"), "expected_version": 2, "idempotency_key": "tiers-claim-1",
	}); response.Outcome != OutcomeOK {
		t.Fatalf("claim work-1 response=%+v err=%+v", response, response.Error)
	}
	second, _, secondGrant := newAuthorizedService(t, s, "client-2", "human-2", []Capability{"work_transition", "product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	if response := tiersInvoke(t, s, second, secondGrant, "concord_work_transition", "worktree_claim", map[string]any{
		"work_id": "work-2", "project_id": "project-1", "branch": "work/work-2", "base_sha": baseSHA, "path": filepath.Join(worktreeRoot, "work-2"), "expected_version": 2, "idempotency_key": "tiers-claim-2",
	}); response.Outcome != OutcomeOK {
		t.Fatalf("claim work-2 response=%+v err=%+v", response, response.Error)
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

func TestWorktreeInspectReadsSameProjectWorktree(t *testing.T) {
	s, service, grant, _, _, _ := tiersFixture(t)
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

	// The Inspect tier takes no lease.
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
	s, service, grant, _, secondGrant, _ := tiersFixture(t)
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-2")

	// A held lease from session-2's in-flight verify of the same worktree.
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
	if refused.Error.Kind != "resource_busy" || refused.Error.RetrySafe != true || refused.Error.RecoveryAction.Kind != "retry_same_request" {
		t.Fatalf("error=%+v, want retry-safe resource_busy with retry_same_request (#736)", refused.Error)
	}
	if !strings.Contains(refused.Error.Message, secondGrant.SessionRef) {
		t.Fatalf("error.message=%q, want the holding session named", refused.Error.Message)
	}
}
