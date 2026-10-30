package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeWorktreePathCanonicalizesSymlinks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	claimed := filepath.Join(root, "claimed")
	if err := os.Mkdir(claimed, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(claimed, alias); err != nil {
		t.Fatal(err)
	}
	path, err := canonicalNativeWorktreePath(alias)
	if err != nil || path != claimed {
		t.Fatalf("canonical path = %q, %v; want %q", path, err, claimed)
	}
}

func TestNativeClaimedSubjectRefusesMismatch(t *testing.T) {
	j := pReturnReviewFixture(t, "subject-claim-mismatch", "workflow.break_fix", "")
	prepared, err := PrepareWorkflowAcceptanceFreshness(t.Context(), j.s, PrepareWorkflowSubjectRequest{WorkID: j.workID})
	if err != nil || prepared == nil || prepared.candidate == nil {
		t.Fatalf("matching active claim = %+v, %v", prepared, err)
	}
	other := t.TempDir()
	report := currentReviewBlockForTest(t, j.s, j.workID, "ready_for_final_proof")
	if _, err := j.s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE worktree_entries SET path=? WHERE set_id=?; DELETE FROM write_guard`, other, WorktreeSetID(j.workID)); err != nil {
		t.Fatal(err)
	}
	before := outsideRepairDatabaseSnapshot(t, j.s)
	err = recordReviewForTest(t, j.s, j.workID, j.fixture.owner, report)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindStaleRequiresReview {
		t.Fatalf("mismatched qualified subject = %v, want stale_requires_review", err)
	}
	if strings.Contains(failure.Detail, j.repo) || strings.Contains(failure.Detail, other) {
		t.Fatalf("refusal leaked a machine path: %q", failure.Detail)
	}
	if !strings.Contains(failure.Detail, "qualified") || failure.RecoveryAction == "" {
		t.Fatalf("refusal omitted the claim boundary or recovery: %+v", failure)
	}
	if outsideRepairDatabaseSnapshot(t, j.s) != before {
		t.Fatal("refused preparation changed durable state")
	}
}

func TestNativeClaimedSubjectRequiresActiveClaim(t *testing.T) {
	for _, state := range []string{"missing", "reclaimed"} {
		t.Run(state, func(t *testing.T) {
			j := pReturnReviewFixture(t, "subject-claim-"+state, "workflow.break_fix", "")
			report := currentReviewBlockForTest(t, j.s, j.workID, "ready_for_final_proof")
			var err error
			if state == "missing" {
				_, err = j.s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM worktree_entries WHERE set_id=?; DELETE FROM write_guard`, WorktreeSetID(j.workID))
			} else {
				_, err = j.s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE worktree_entries SET state='reclaimed',reclaimed_at='2026-10-01T00:00:00Z' WHERE set_id=?; DELETE FROM write_guard`, WorktreeSetID(j.workID))
			}
			if err != nil {
				t.Fatal(err)
			}
			before := outsideRepairDatabaseSnapshot(t, j.s)
			err = recordReviewForTest(t, j.s, j.workID, j.fixture.owner, report)
			if !hasFailureKind(err, KindStaleRequiresReview) {
				t.Fatalf("subject without active claim = %v, want stale_requires_review", err)
			}
			if outsideRepairDatabaseSnapshot(t, j.s) != before {
				t.Fatal("refused preparation changed durable state")
			}
		})
	}
}
