package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// CD-0096 part 3b (#689): the Take over and Destroy tiers and the session
// continuity re-pin through the agent tool surface, against real git. An
// active owner blocks a takeover until it releases or the operator approves
// the override; destroy reclaims merged terminal work under the unchanged
// git gates and routes every other removal through operator approval.

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

func takeoverInput(workID string, work, target int64, key string) map[string]any {
	return map[string]any{"work_id": workID, "expected_version": work, "expected_target_version": target, "idempotency_key": key}
}

func sessionOwnerOf(grant Authority) store.SessionWorktreeOwner {
	return store.SessionWorktreeOwner{ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef}
}

func countActiveHolders(t *testing.T, s *store.Store, workID string) int64 {
	t.Helper()
	var count int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM session_worktree_targets WHERE work_id=? AND state='active'`, workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
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

func TestWorktreeReleaseDispatchReleasesBinding(t *testing.T) {
	s, _, _, second, secondGrant, _ := tiersFixture(t)
	owner := sessionOwnerOf(secondGrant)

	response := authorityInvoke(t, s, second, secondGrant, "concord_work_transition", "worktree_release", map[string]any{
		"work_id": "work-2", "expected_target_version": 1, "idempotency_key": "rel-2",
	})
	if response.Outcome != OutcomeOK {
		t.Fatalf("release response=%+v err=%+v", response, response.Error)
	}
	target, found, err := s.SessionWorktreeTarget(context.Background(), owner)
	if err != nil || !found {
		t.Fatalf("target=%+v found=%v err=%v", target, found, err)
	}
	if target.State != "released" || target.TargetVersion != 2 {
		t.Fatalf("target=%+v, want released at version 2", target)
	}
	if count := countActiveHolders(t, s, "work-2"); count != 0 {
		t.Fatalf("work-2 active holders=%d, want none", count)
	}
}

func TestWorktreeTakeoverDispatchNamesOwnerMintsChallengeAndTransfers(t *testing.T) {
	s, _, firstGrant, second, secondGrant, _ := tiersFixture(t)
	secondOwner := sessionOwnerOf(secondGrant)

	// Refused without release or operator approval: the challenge names the
	// owner identity and the recovery action (CD-0096 D3 Take over).
	refused := authorityInvoke(t, s, second, secondGrant, "concord_work_transition", "worktree_takeover", takeoverInput("work-1", 3, 1, "tk-1"))
	if refused.Outcome != OutcomeError || refused.Error == nil || refused.Error.Kind != "approval_required" {
		t.Fatalf("refused response=%+v err=%+v, want approval_required", refused, refused.Error)
	}
	ownerLabel, _ := refused.Error.Details["takeover_owner"].(string)
	if !strings.Contains(ownerLabel, firstGrant.ClientRef) {
		t.Fatalf("takeover_owner=%q, want the active holder's identity", ownerLabel)
	}
	challengeRef, ok := refused.Error.Details["approval_ref"].(string)
	if !ok || len(challengeRef) != 64 {
		t.Fatalf("approval_ref=%v, want a minted challenge", refused.Error.Details["approval_ref"])
	}
	if count := countActiveHolders(t, s, "work-1"); count != 1 {
		t.Fatalf("refusal changed holders=%d", count)
	}

	// The operator approves the exact override; the transfer lands.
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(secondGrant, scopeVersion)
	raw, _ := json.Marshal(takeoverInput("work-1", 3, 1, "tk-1"))
	digest := mutationDigest("concord_work_transition", "worktree_takeover", env, raw)
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": 3, "target": 1}
	env.HostApproval = signedHostApproval(mustKey(t), challengeRef, digest, scope, versions, secondGrant.SessionRef, secondGrant.AgentRef, secondGrant.Worktree, fixedTime(), "takeover-override-1")

	approvedRaw, _ := json.Marshal(map[string]any{"work_id": "work-1", "expected_version": 3, "expected_target_version": 1, "idempotency_key": "tk-1", "approval": map[string]any{"approval_ref": challengeRef}})
	approved, err := Dispatch(context.Background(), s, second, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_takeover", Input: approvedRaw}, env)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved response=%+v err=%+v", approved, approved.Error)
	}

	target, found, err := s.SessionWorktreeTarget(context.Background(), secondOwner)
	if err != nil || !found {
		t.Fatalf("taker target=%+v found=%v err=%v", target, found, err)
	}
	if target.WorkID != "work-1" || target.State != "active" {
		t.Fatalf("taker target=%+v, want active on work-1", target)
	}
	firstTarget, _, err := s.SessionWorktreeTarget(context.Background(), sessionOwnerOf(firstGrant))
	if err != nil {
		t.Fatal(err)
	}
	if firstTarget.State != "released" {
		t.Fatalf("prior holder target=%+v, want released", firstTarget)
	}
	if count := countActiveHolders(t, s, "work-1"); count != 1 {
		t.Fatalf("work-1 active holders=%d, want exactly one", count)
	}
}

func TestWorktreeTakeoverAfterReleaseNeedsNoApproval(t *testing.T) {
	s, _, _, second, secondGrant, _ := tiersFixture(t)
	if response := authorityInvoke(t, s, second, secondGrant, "concord_work_transition", "worktree_release", map[string]any{
		"work_id": "work-2", "expected_target_version": 1, "idempotency_key": "rel-2",
	}); response.Outcome != OutcomeOK {
		t.Fatalf("release response=%+v err=%+v", response, response.Error)
	}

	response := authorityInvoke(t, s, second, secondGrant, "concord_work_transition", "worktree_takeover", takeoverInput("work-2", 3, 2, "tk-after-release"))
	if response.Outcome != OutcomeOK {
		t.Fatalf("takeover response=%+v err=%+v", response, response.Error)
	}
	target, _, err := s.SessionWorktreeTarget(context.Background(), sessionOwnerOf(secondGrant))
	if err != nil || target.WorkID != "work-2" || target.TargetVersion != 3 {
		t.Fatalf("target=%+v err=%v, want the released worktree re-bound at version 3", target, err)
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

func TestContinuityDispatchRePinsTargetAndFailsClosedOnStalePin(t *testing.T) {
	s, service, grant, _, _, _ := tiersFixture(t)
	seedContinuityWorkflow(t, s, "work-1")

	response := authorityInvoke(t, s, service, grant, "concord_work_trace", "continuity", map[string]any{
		"work_id": "work-1", "page": map[string]any{"limit": 5, "cursor": nil}, "expected_target_version": 1,
	})
	if response.Outcome != OutcomeOK {
		t.Fatalf("continuity response=%+v err=%+v", response, response.Error)
	}
	var payload struct {
		Pinned struct {
			EffectiveTarget *store.SessionWorktreeTarget `json:"effective_target"`
		} `json:"pinned"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Pinned.EffectiveTarget == nil || payload.Pinned.EffectiveTarget.WorkID != "work-1" || payload.Pinned.EffectiveTarget.TargetVersion != 1 {
		t.Fatalf("pinned.effective_target=%+v, want the session binding re-pinned", payload.Pinned.EffectiveTarget)
	}

	stale := authorityInvoke(t, s, service, grant, "concord_work_trace", "continuity", map[string]any{
		"work_id": "work-1", "page": map[string]any{"limit": 5, "cursor": nil}, "expected_target_version": 5,
	})
	if stale.Outcome != OutcomeError || stale.Error == nil || stale.Error.Kind != "version_conflict" {
		t.Fatalf("stale response=%+v err=%+v, want version_conflict", stale, stale.Error)
	}
}
