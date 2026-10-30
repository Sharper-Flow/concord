package store

import (
	"context"
	"testing"
	"time"
)

func seedWorktreeContinuityWorkflow(t *testing.T, s *Store, workID string) {
	t.Helper()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	actor := WorkflowActor{PrincipalRef: "principal-1", ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-1", ActorClass: ActorAgent}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: actor, Now: time.Unix(5, 0).UTC()}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestContinuityRePinsHeldLease(t *testing.T) {
	t.Parallel()
	s, _, repo := worktreeFixture(t)
	gitRunStore(t, repo, "init", "-b", "main")
	gitRunStore(t, repo, "-c", "user.name=Synthetic Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "standalone continuity source")
	seedWorktreeContinuityWorkflow(t, s, "work-w")
	owner := SessionWorktreeOwner{ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-1"}
	stamp := time.Unix(25, 0).UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec(`INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,outcome) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, "lease-1", "work-w", "project-w", "/worktrees/project-w/work-w", "held", owner.ClientRef, owner.AgentRef, owner.SessionRef, "principal-1", `["go","test","./..."]`, stamp, "running"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := ReadWorkflowContinuity(t.Context(), s, ContinuityRequest{Work: "work-w", Limit: 5, Owner: &owner})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.ActiveVerifyLeases) != 1 || snapshot.ActiveVerifyLeases[0].LeaseID != "lease-1" || len(snapshot.ActiveVerifyLeases[0].Command) != 3 {
		t.Fatalf("active_verify_leases=%+v", snapshot.ActiveVerifyLeases)
	}
	plain, err := ReadWorkflowContinuity(t.Context(), s, ContinuityRequest{Work: "work-w", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.ActiveVerifyLeases) != 0 {
		t.Fatalf("plain snapshot leases=%d, want work-keyed projection", len(plain.ActiveVerifyLeases))
	}
}
