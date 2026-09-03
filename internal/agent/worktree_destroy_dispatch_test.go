package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// CD-0096 D3 Destroy and D5 continuity through the agent tool surface,
// against real git. Destroy reclaims merged terminal work under the unchanged
// git gates and routes every other removal through operator approval; the
// continuity read re-pins the reading session's held verify leases.

func authorityInvoke(t *testing.T, s *store.Store, service *Service, grant Authority, tool, operation string, input map[string]any) Envelope {
	t.Helper()
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(input)
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: tool, Operation: operation, Input: raw}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatalf("dispatch err=%v", err)
	}
	return response
}

func completeWork(t *testing.T, s *store.Store, workID string, version int64) {
	t.Helper()
	payload := `{"from":"needed","to":"completed","reason":"fixture terminal","evidence_refs":["fixture"],"expected_version":` + strconv.FormatInt(version, 10) + `,"resulting_version":` + strconv.FormatInt(version+1, 10) + `}`
	err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{
		{EventID: workID + "-authority-complete", Kind: "work.transitioned", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(payload)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, workID): version}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeDestroyDispatchReclaimsMergedTerminalWork(t *testing.T) {
	s, _, _, second, secondGrant, _ := tiersFixture(t)
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-2")
	completeWork(t, s, "work-2", 3)

	response := authorityInvoke(t, s, second, secondGrant, "concord_work_transition", "worktree_destroy", map[string]any{
		"work_id": "work-2", "expected_version": 4, "default_ref": "main", "idempotency_key": "destroy-2",
	})
	if response.Outcome != OutcomeOK {
		t.Fatalf("destroy response=%+v err=%+v", response, response.Error)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree still present at %s", worktreePath)
	}
	if version := workVersion(t, s, "work-2"); version != 5 {
		t.Fatalf("work version=%d, want the reclamation bump", version)
	}
}

func TestWorktreeDestroyDispatchRoutesNonTerminalThroughApproval(t *testing.T) {
	s, service, grant, _, _, _ := tiersFixture(t)
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")

	refused := authorityInvoke(t, s, service, grant, "concord_work_transition", "worktree_destroy", map[string]any{
		"work_id": "work-1", "expected_version": 3, "default_ref": "main", "idempotency_key": "destroy-nt",
	})
	if refused.Outcome != OutcomeError || refused.Error == nil || refused.Error.Kind != "approval_required" {
		t.Fatalf("refused response=%+v err=%+v, want approval_required", refused, refused.Error)
	}
	challengeRef, ok := refused.Error.Details["approval_ref"].(string)
	if !ok || len(challengeRef) != 64 {
		t.Fatalf("approval_ref=%v", refused.Error.Details["approval_ref"])
	}
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("non-terminal destroy removed the worktree before approval: %v", err)
	}

	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	raw, _ := json.Marshal(map[string]any{"work_id": "work-1", "expected_version": 3, "default_ref": "main", "idempotency_key": "destroy-nt"})
	digest := mutationDigest("concord_work_transition", "worktree_destroy", env, raw)
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": 3}
	env.HostApproval = signedHostApproval(mustKey(t), challengeRef, digest, scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "destroy-nt-1")
	approvedRaw, _ := json.Marshal(map[string]any{"work_id": "work-1", "expected_version": 3, "default_ref": "main", "idempotency_key": "destroy-nt", "approval": map[string]any{"approval_ref": challengeRef}})
	approved, approvalErr := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_destroy", Input: approvedRaw}, env)
	if approvalErr != nil {
		t.Fatal(approvalErr)
	}
	// The approval satisfies the terminal gate; the git gates still run and
	// the clean merged tree passes them.
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved response=%+v err=%+v", approved, approved.Error)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree still present at %s", worktreePath)
	}
}

func TestWorktreeDestroyDispatchDestructiveUnderApproval(t *testing.T) {
	s, service, grant, _, _, _ := tiersFixture(t)
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")
	completeWork(t, s, "work-1", 3)
	if err := os.WriteFile(filepath.Join(worktreePath, "README.md"), []byte("# dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The safe path refuses the dirty tree and names the destructive route.
	refused := authorityInvoke(t, s, service, grant, "concord_work_transition", "worktree_destroy", map[string]any{
		"work_id": "work-1", "expected_version": 4, "default_ref": "main", "idempotency_key": "destroy-dirty",
	})
	if refused.Outcome != OutcomeError || refused.Error == nil || refused.Error.Kind != "invalid_input" {
		t.Fatalf("refused response=%+v err=%+v, want the dirty-tree refusal", refused, refused.Error)
	}

	// Destructive intent mints the operator challenge.
	challenge := authorityInvoke(t, s, service, grant, "concord_work_transition", "worktree_destroy", map[string]any{
		"work_id": "work-1", "expected_version": 4, "default_ref": "main", "destructive": true, "idempotency_key": "destroy-dirty",
	})
	if challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("challenge response=%+v err=%+v, want approval_required", challenge, challenge.Error)
	}
	challengeRef, _ := challenge.Error.Details["approval_ref"].(string)

	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	raw, _ := json.Marshal(map[string]any{"work_id": "work-1", "expected_version": 4, "default_ref": "main", "destructive": true, "idempotency_key": "destroy-dirty"})
	digest := mutationDigest("concord_work_transition", "worktree_destroy", env, raw)
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": 4}
	env.HostApproval = signedHostApproval(mustKey(t), challengeRef, digest, scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "destroy-force-1")
	approvedRaw, _ := json.Marshal(map[string]any{"work_id": "work-1", "expected_version": 4, "default_ref": "main", "destructive": true, "idempotency_key": "destroy-dirty", "approval": map[string]any{"approval_ref": challengeRef}})
	approved, approvalErr := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_destroy", Input: approvedRaw}, env)
	if approvalErr != nil {
		t.Fatal(approvalErr)
	}
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved response=%+v err=%+v", approved, approved.Error)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("dirty worktree still present at %s", worktreePath)
	}
}

// seedContinuityWorkflow gives the dispatch fixture's work item a workflow
// instance so the continuity read answers for it.
func seedContinuityWorkflow(t *testing.T, s *store.Store, workID string) {
	t.Helper()
	registered, err := store.BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	actor := store.WorkflowActor{PrincipalRef: "human-1", ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-1", ActorClass: store.ActorAgent}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(context.Background(), tx, store.WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: actor, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
}

func TestContinuityDispatchRePinsHeldLease(t *testing.T) {
	s, service, grant, _, _, _ := tiersFixture(t)
	seedContinuityWorkflow(t, s, "work-1")
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")
	commandJSON, _ := json.Marshal([]string{"go", "test", "./..."})
	stamp := fixedTime().UTC().Format(time.RFC3339Nano)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,outcome) VALUES('own-lease','work-1','project-1',?, 'held',?,?,?,?,?,?, 'running')`,
		worktreePath, grant.ClientRef, grant.AgentRef, grant.SessionRef, grant.PrincipalRef, string(commandJSON), stamp); err != nil {
		t.Fatal(err)
	}

	response := authorityInvoke(t, s, service, grant, "concord_work_trace", "continuity", map[string]any{
		"work_id": "work-1", "page": map[string]any{"limit": 5, "cursor": nil},
	})
	if response.Outcome != OutcomeOK {
		t.Fatalf("continuity response=%+v err=%+v", response, response.Error)
	}
	var payload struct {
		Pinned struct {
			ActiveVerifyLeases []store.ActiveWorktreeVerifyLease `json:"active_verify_leases"`
		} `json:"pinned"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Pinned.ActiveVerifyLeases) != 1 || payload.Pinned.ActiveVerifyLeases[0].LeaseID != "own-lease" {
		t.Fatalf("pinned.active_verify_leases=%+v, want the session's held lease re-pinned", payload.Pinned.ActiveVerifyLeases)
	}
}
