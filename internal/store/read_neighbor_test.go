package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store/storetest/neighbor"
)

// A fresh writer per row and a 50ms busy timeout distinguish a deferred read
// from a read that queues behind BEGIN IMMEDIATE. The neighbor holds for 1s;
// success must precede its release, not follow a busy-timeout retry.
func TestReadEntryPointsWithWriterNeighbor(t *testing.T) {
	ctx := context.Background()
	s := seedQueryFixture(t)
	seedResearchWork(t, s, "neighbor-research")
	continuityTestWorkflow(t, s, "neighbor-work")
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	insertInvestigationGateObservation(t, s, "neighbor-work", "obs:"+strings.Repeat("3", 16), []string{"root", "blocker"})
	pack := createSimplePack(t, s, "neighbor-pack", "neighbor-research")
	if err := s.Transact(ctx, func(tx *Transaction) error {
		scope, err := beginFold(ctx, tx.tx)
		if err != nil {
			return err
		}
		if _, err := tx.tx.Exec(`INSERT INTO linear_issue_links(work_id,remote_issue_uuid,human_key,url,link_state,created_at,updated_at) VALUES('blocker','neighbor-issue','SYN-1','https://example.invalid/issues/SYN-1','confirmed','2026-08-09','2026-08-09')`); err != nil {
			return err
		}
		return scope.close(ctx)
	}); err != nil {
		t.Fatal(err)
	}
	var watermark int64
	if err := s.db.QueryRow("SELECT max(seq) FROM domain_events").Scan(&watermark); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA busy_timeout=50"); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		name string
		run  func() error
	}{
		{"PM1.Q1", func() error { _, err := s.QueryQ1(ctx, Q1Request{Product: "prod"}); return err }},
		{"PM1.Q2", func() error { _, err := s.QueryQ2(ctx, Q2Request{Product: "prod"}); return err }},
		{"PM1.Q3", func() error { _, err := s.QueryQ3(ctx, Q3Request{Product: "prod"}); return err }},
		{"PM1.Q4", func() error { _, err := s.QueryQ4(ctx, Q4Request{Product: "prod"}); return err }},
		{"PM1.Q5", func() error { _, err := s.QueryQ5(ctx, Q5Request{Product: "prod"}); return err }},
		{"PM1.Q6", func() error { _, err := s.QueryQ6(ctx, Q6Request{Work: "blocker"}); return err }},
		{"PM1.Q7", func() error { _, err := s.QueryQ7(ctx, Q7Request{Work: "blocker"}); return err }},
		{"PM1.Q8", func() error { _, err := s.QueryQ8(ctx, Q8Request{Work: "blocker"}); return err }},
		{"ReadWorkPin", func() error { _, err := ReadWorkPin(ctx, s, "neighbor-work"); return err }},
		{"ReadWorkClosure", func() error { _, err := ReadWorkClosure(ctx, s, "neighbor-work"); return err }},
		{"ReadWorkflowContinuity", func() error {
			_, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: "neighbor-work", Limit: 20})
			return err
		}},
		{"ReadWorkflowOperatorQuestion", func() error { _, err := ReadWorkflowOperatorQuestion(ctx, s, "neighbor-work"); return err }},
		{"requireRecordedInvestigationArtifactSnapshot", func() error { return requireRecordedInvestigationArtifactSnapshot(ctx, s, "neighbor-work") }},
		{"WorkflowFailedWorkerRetryBinding", func() error {
			_, err := WorkflowFailedWorkerRetryBinding(ctx, s, BuiltinWorkflowRegistry(), "neighbor-work")
			return err
		}},
		{"ReadResearchPack", func() error { _, err := ReadResearchPack(ctx, s, pack.PackID, 100); return err }},
		{"ResearchPacksByOwner", func() error { _, err := ResearchPacksByOwner(ctx, s, "neighbor-research", 100); return err }},
		{"ResearchFreshnessForPack", func() error { _, err := ResearchFreshnessForPack(ctx, s, pack.PackID); return err }},
		{"readAppliedFromDB", func() error { _, _, err := readAppliedFromDB(ctx, s.db, s.Path()); return err }},
		{"ReconstructSubjectAt", func() error {
			_, err := ReconstructSubjectAt(ctx, s, VersionRef(SubjectWorkItem, "blocker"), watermark, PurposeAudit)
			return err
		}},
		{"QueryProductRows", func() error { _, err := s.QueryProductRows(ctx, ProductRowRequest{}); return err }},
		{"Resources", func() error { _, err := s.Resources(ctx, ResourcesRequest{ProductID: "prod"}); return err }},
		{"BlockedSessions", func() error { _, err := s.BlockedSessions(ctx, time.Now(), []string{"prod"}, 100); return err }},
		{"QueryLauncherSearch", func() error {
			_, err := s.QueryLauncherSearch(ctx, LauncherSearchRequest{Product: "prod", Query: "blocker"})
			return err
		}},
		{"QueryLauncherProduct", func() error {
			_, err := s.QueryLauncherProduct(ctx, LauncherProductRequest{Product: "prod"})
			return err
		}},
		{"ResolveLauncherWorkProduct", func() error { _, err := s.ResolveLauncherWorkProduct(ctx, "blocker", "proj", "prod"); return err }},
		{"ResolveLauncherLinearIssue", func() error {
			_, err := s.ResolveLauncherLinearIssue(ctx, "SYN-1", "https://example.invalid/issues/SYN-1", "proj", "prod")
			return err
		}},
		{"ResolveLauncherLinearIssueWork", func() error {
			_, err := s.ResolveLauncherLinearIssueWork(ctx, "SYN-1", "https://example.invalid/issues/SYN-1")
			return err
		}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			neighbor.Start(t, s.Path(), neighbor.Writer)
			started := time.Now()
			if err := row.run(); err != nil {
				t.Fatalf("%s under writer neighbor: %v", row.name, err)
			}
			// Reconstruction migrates a separate scratch database after its
			// live snapshot closes. That work is not a wait on this writer.
			if elapsed := time.Since(started); row.name != "ReconstructSubjectAt" && elapsed >= neighbor.WriterHold {
				t.Fatalf("%s waited for writer release: %s", row.name, elapsed)
			}
		})
	}
	t.Run("ValidateBootstrapOrigin", func(t *testing.T) {
		s, git, _ := worktreeFixture(t)
		claim, err := s.ClaimWorktree(ctx, baseClaim(git))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec("PRAGMA busy_timeout=50"); err != nil {
			t.Fatal(err)
		}
		neighbor.Start(t, s.Path(), neighbor.Writer)
		if _, err := s.ValidateBootstrapOrigin(ctx, "project-w", claim.Entry.Path, git); err != nil {
			t.Fatal(err)
		}
	})
}
