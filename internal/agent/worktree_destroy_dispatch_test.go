package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// CD-0096 D3 Destroy through the agent tool surface against real git.

func authorityInvoke(t *testing.T, s *store.Store, service *Service, grant Authority, operation string, input map[string]any) agentcontract.Envelope {
	t.Helper()
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(input)
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: operation, Input: raw}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatalf("dispatch err=%v", err)
	}
	return response
}

func completeWork(t *testing.T, s *store.Store, workID string) {
	t.Helper()
	payload := `{"from":"needed","to":"completed","reason":"fixture terminal","evidence_refs":["fixture"],"expected_version":3,"resulting_version":4}`
	err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{
		{EventID: workID + "-authority-complete", Kind: "work.transitioned", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(payload)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, workID): 3}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeDestroyDispatchReclaimsMergedTerminalWork(t *testing.T) {
	t.Parallel()
	s, _, _, second, secondGrant, _ := tiersFixture(t)
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-2")
	completeWork(t, s, "work-2")
	vacateLinkedWorktree(t, s, second, secondGrant, worktreePath, "destroy-2-vacate")

	response := authorityInvoke(t, s, second, secondGrant, "worktree_destroy", map[string]any{
		"work_id": "work-2", "expected_version": 4, "default_ref": "main", "idempotency_key": "destroy-2",
	})
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("destroy response=%+v err=%+v", response, response.Error)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree still present at %s", worktreePath)
	}
	if version := workVersion(t, s, "work-2"); version != 5 {
		t.Fatalf("work version=%d, want the reclamation bump", version)
	}
}

func TestWorktreeDestroyDispatchNonTerminalNeedsNoConsent(t *testing.T) {
	t.Parallel()
	s, service, grant, _, _, _ := tiersFixture(t)
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")
	vacateLinkedWorktree(t, s, service, grant, worktreePath, "destroy-nt-vacate")

	// CD-0243 D3/D7 retire ordinary nonterminal consent.
	removed := authorityInvoke(t, s, service, grant, "worktree_destroy", map[string]any{
		"work_id": "work-1", "expected_version": 3, "default_ref": "main", "idempotency_key": "destroy-nt",
	})
	if removed.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("removed response=%+v err=%+v", removed, removed.Error)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree still present at %s", worktreePath)
	}
}

func TestWorktreeDestroyDispatchDiscardsObsoleteContentWithoutConsent(t *testing.T) {
	t.Parallel()
	s, service, grant, _, _, _ := tiersFixture(t)
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")
	completeWork(t, s, "work-1")
	vacateLinkedWorktree(t, s, service, grant, worktreePath, "destroy-dirty-vacate")
	for _, content := range []string{"obsolete first change\n", "obsolete second change\n"} {
		if err := os.WriteFile(filepath.Join(worktreePath, "README.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(t, worktreePath, "add", "README.md")
		gitRun(t, worktreePath, "commit", "-m", "obsolete development change")
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "README.md"), []byte("# dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The safe path refuses the dirty tree and names the destructive route.
	refused := authorityInvoke(t, s, service, grant, "worktree_destroy", map[string]any{
		"work_id": "work-1", "expected_version": 4, "default_ref": "main", "idempotency_key": "destroy-dirty",
	})
	if refused.Outcome != agentcontract.OutcomeError || refused.Error == nil || refused.Error.Kind != "invalid_input" {
		t.Fatalf("refused response=%+v err=%+v, want the dirty-tree refusal", refused, refused.Error)
	}

	removed := authorityInvoke(t, s, service, grant, "worktree_destroy", map[string]any{
		"work_id": "work-1", "expected_version": 4, "default_ref": "main", "destructive": true, "idempotency_key": "destroy-dirty",
	})
	if removed.Outcome != agentcontract.OutcomeOK || removed.Error != nil {
		t.Fatalf("removed response=%+v err=%+v, want removal without consent", removed, removed.Error)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("dirty worktree still present at %s", worktreePath)
	}
}

func TestWorktreeDestroyDispatchKeepsOccupancyRefusalWithoutConsent(t *testing.T) {
	t.Parallel()
	s, service, grant, _, _, _ := tiersFixture(t)
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")

	for _, destructive := range []bool{false, true} {
		refused := authorityInvoke(t, s, service, grant, "worktree_destroy", map[string]any{
			"work_id": "work-1", "expected_version": 3, "default_ref": "main", "destructive": destructive, "idempotency_key": "destroy-stale",
		})
		if refused.Outcome != agentcontract.OutcomeError || refused.Error == nil || refused.Error.Kind != "unauthorized" {
			t.Fatalf("refused response=%+v err=%+v, want occupancy refusal without consent", refused, refused.Error)
		}
		if !strings.Contains(refused.Error.Message, grant.SessionRef) || !strings.Contains(refused.Error.Message, worktreePath) {
			t.Fatalf("refusal %q must name the occupying session and worktree", refused.Error.Message)
		}
		if _, err := os.Stat(worktreePath); err != nil {
			t.Fatalf("occupied worktree must remain: %v", err)
		}
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
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: actor, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
}
