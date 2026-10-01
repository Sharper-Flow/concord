package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A prepared lesson is one commit on a branch, and nothing else tracks it
// until the coordinator's pull request merges. A pushed-then-abandoned
// branch holding a prepared lesson reclaims with no signal, because every
// durable gate sees a clean tree and a durable remote ref. These tests hold
// the two halves of the gate: the audit classifies the shape, and the
// reclaim refuses it even when the branch is pushed.

// The audit read classifies a worktree whose branch carries a lesson record
// shard the default ref does not hold, and only lesson records: a decision
// record the branch adds is not an unpublished lesson.
func TestWorktreeAuditClassifiesUnpublishedLessonWorktrees(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	auditWork(t, s, git, "work-live", true)
	git.addBranchRecordShard("work/work-live", "docs/knowledge/records/lesson-live.json", "lesson")
	git.addBranchRecordShard("work/work-live", "docs/knowledge/records/CD-0099.json", "decision")

	audit, err := s.WorktreeAudit(ctx, WorktreeAuditRequest{ProductID: "product-w", Limit: 100, Runner: git, DefaultRef: "origin/main"})
	if err != nil {
		t.Fatal(err)
	}
	byClass := auditRowsByClass(audit.Drift)
	rows := byClass[WorktreeDriftUnpublishedLesson]
	if len(rows) != 1 {
		t.Fatalf("unpublished-lesson rows=%+v, want exactly one", rows)
	}
	row := rows[0]
	if row.WorkID != "work-live" || row.RecoveryAction != WorktreeRecoveryInspect || !strings.Contains(row.Risk, "1 lesson record(s)") {
		t.Fatalf("unpublished-lesson row=%+v", row)
	}
	if strings.Contains(row.Risk, "CD-0099") || strings.Contains(row.Risk, "2 lesson") {
		t.Fatalf("a non-lesson record shard counted as a lesson: %+v", row)
	}
	for _, other := range audit.Drift {
		if other.Class == WorktreeDriftUnpushedContent || other.Class == WorktreeDriftUncommittedContent {
			t.Fatalf("a clean pushed branch classified as content risk: %+v", other)
		}
	}
}

// The reclaim pass refuses a terminal worktree whose pushed branch carries a
// lesson record the default branch does not hold, the worktree and the
// branch survive, and the same pass reclaims once the lesson merges (the
// shard reaches the default tree).
func TestWorktreeAuditReclaimRefusesPushedUnpublishedLesson(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	auditWork(t, s, git, "work-live", true)
	donePath := auditWork(t, s, git, "work-done", true)
	completeAuditWork(t, s, "work-done")
	// Fully pushed: the durability gate passes on remote reachability alone.
	git.unpushed["work/work-done"] = 0
	git.addBranchRecordShard("work/work-done", "docs/knowledge/records/lesson-stranded.json", "lesson")

	result, err := s.WorktreeAuditReclaim(ctx, WorktreeAuditReclaimRequest{ProductID: "product-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "lesson-refusal-1", Now: time.Unix(40, 0).UTC(), Runner: git, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var row *WorktreeAuditReclaimRow
	for i := range result.Rows {
		if result.Rows[i].WorkID == "work-done" {
			row = &result.Rows[i]
		}
	}
	if row == nil {
		t.Fatalf("the pass skipped the worktree: %+v", result.Rows)
	}
	if row.Outcome != WorktreeAuditRefused || row.RefusalKind != string(KindWorktreeUnpublishedLesson) || !strings.Contains(row.Detail, "lesson-stranded.json") {
		t.Fatalf("refused row=%+v", row)
	}
	if _, present := git.worktrees[donePath]; !present {
		t.Fatal("the refused worktree was removed")
	}
	if _, kept := git.branches["work/work-done"]; !kept {
		t.Fatal("the refused branch was deleted")
	}

	// The lesson merges: the default tree now holds the record shard, and
	// the same pass reclaims.
	git.addBranchRecordShard("work/work-done", "", "")
	again, err := s.WorktreeAuditReclaim(ctx, WorktreeAuditReclaimRequest{ProductID: "product-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "lesson-refusal-2", Now: time.Unix(50, 0).UTC(), Runner: git, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, after := range again.Rows {
		if after.WorkID == "work-done" && after.Outcome != WorktreeAuditReclaimed {
			t.Fatalf("post-merge reclaim refused: %+v", after)
		}
	}
	if _, present := git.worktrees[donePath]; present {
		t.Fatal("the merged worktree was not reclaimed")
	}
}

// A direct reclaim of a pushed branch carrying an unpublished lesson refuses
// with the same typed kind, so the gate does not depend on the audit pass.
func TestReclaimWorktreeRefusesUnpublishedLessonDirectly(t *testing.T) {
	for _, defaultRef := range []string{"origin/main", ""} {
		t.Run("default_ref="+defaultRef, func(t *testing.T) {
			testReclaimWorktreeRefusesUnpublishedLessonDirectly(t, defaultRef)
		})
	}
}

func testReclaimWorktreeRefusesUnpublishedLessonDirectly(t *testing.T, defaultRef string) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	auditWork(t, s, git, "work-direct", true)
	completeAuditWork(t, s, "work-direct")
	git.addBranchRecordShard("work/work-direct", "docs/knowledge/records/lesson-direct.json", "lesson")

	_, err := s.ReclaimWorktree(ctx, WorktreeReclaimRequest{WorkID: "work-direct", ProjectID: "project-w", DefaultRef: defaultRef, PrincipalRef: "principal-1", RequestID: "lesson-direct-1", ExpectedVersion: 4, Now: time.Unix(30, 0).UTC(), Runner: git, RequireTerminal: true})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindWorktreeUnpublishedLesson {
		t.Fatalf("direct reclaim err=%v, want %s", err, KindWorktreeUnpublishedLesson)
	}
}
