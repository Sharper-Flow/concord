package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

type auditPinDriftRunner struct {
	GitRunner
	afterRemoval func()
}

func (r auditPinDriftRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	output, err := r.GitRunner.Run(ctx, dir, args...)
	if err == nil && len(args) >= 2 && args[0] == "worktree" && args[1] == "remove" {
		r.afterRemoval()
	}
	return output, err
}

func auditManagedPinFixture(t *testing.T) (*Store, *fakeWorktreeGit, string, int64) {
	t.Helper()
	s, git, repo := worktreeFixture(t)
	runKnowledgeGit(t, repo, "init", "--initial-branch=main")
	runKnowledgeGit(t, repo, "config", "user.email", "test@example.invalid")
	runKnowledgeGit(t, repo, "config", "user.name", "Concord Test")
	writeKnowledgeFile(t, repo, ".concord/docs/knowledge/domain-registry.json", fixtureLawFolderRegistry)
	commitKnowledgeRepo(t, repo, "fixture registry")
	bindWorkflowFixtureKnowledgeSource(t, s, KnowledgeHome{HomeProjectID: "project-w", HomeLocatorID: "path-w", RepoPath: repo, HeadRef: "HEAD"})
	path := auditWork(t, s, git, "work-done", true)
	completeAuditWork(t, s, "work-done")
	registered, err := BuiltinWorkflowRegistry().Register(BuiltinWorkflowDefinitions()[0])
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), "work-done")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: "work-done", Definition: registered, Actor: WorkflowActor{PrincipalRef: "principal/fixture", ClientRef: "client/fixture", AgentRef: "agent/fixture", SessionRef: "session/fixture", ActorClass: ActorAgent}, Now: time.Unix(35, 0).UTC()})
	}); err != nil {
		t.Fatal(err)
	}
	before, err := currentWorkVersion(context.Background(), s.db, "work-done")
	if err != nil {
		t.Fatal(err)
	}
	return s, git, path, before
}

func TestWorktreeAuditReclaimVersionUsesRowTransaction(t *testing.T) {
	s, git, _, before := auditManagedPinFixture(t)
	runner := auditPinDriftRunner{GitRunner: git, afterRemoval: func() {
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
			UPDATE work_items SET version=version+1 WHERE id='work-done'; DELETE FROM write_guard`); err != nil {
			t.Fatal(err)
		}
	}}
	result, err := s.WorktreeAuditReclaim(context.Background(), WorktreeAuditReclaimRequest{ProductID: "product-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "audit-pin-drift", Now: time.Unix(40, 0).UTC(), Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0].Outcome != WorktreeAuditReclaimed || result.Rows[0].Version != before+1 {
		t.Fatalf("audit must report its own committed version %d, not the later writer: %+v", before+1, result.Rows)
	}
	if pin := result.Rows[0].WorkPin; pin == nil || pin.WorkID != "work-done" || pin.Version != before+1 {
		t.Fatalf("audit must retain its transaction's pin: %+v", pin)
	}
	current, err := currentWorkVersion(context.Background(), s.db, "work-done")
	if err != nil || current != before+2 {
		t.Fatalf("later writer did not run: version=%d err=%v", current, err)
	}
}

func TestWorktreeAuditPinFailureRollsBackBeforeNativeRemoval(t *testing.T) {
	s, git, path, before := auditManagedPinFixture(t)
	if _, err := s.DatabaseForTesting().Exec(`CREATE TRIGGER unreadable_reclaimed_pin AFTER UPDATE OF state ON worktree_entries
		WHEN NEW.state <> 'active'
		BEGIN UPDATE workflow_instances SET definition_ref='workflow.unavailable_fixture' WHERE work_id='work-done'; END`); err != nil {
		t.Fatal(err)
	}
	result, err := s.WorktreeAuditReclaim(context.Background(), WorktreeAuditReclaimRequest{ProductID: "product-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "audit-pin-refusal", Now: time.Unix(40, 0).UTC(), Runner: git})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0].Outcome != WorktreeAuditRefused || result.Rows[0].Version != 0 || !strings.Contains(result.Rows[0].Detail, "current workflow definition is unavailable") {
		t.Fatalf("an unreadable post-state must refuse its row: %+v", result.Rows)
	}
	if _, present := git.worktrees[path]; !present {
		t.Fatal("pin failure must roll back before native removal")
	}
	version, err := currentWorkVersion(context.Background(), s.db, "work-done")
	if err != nil || version != before {
		t.Fatalf("pin failure did not roll back the row: version=%d error=%v", version, err)
	}
}
