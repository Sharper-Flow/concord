package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestReclaimWorktreeKeepsRefusingDifferentClaimGeneration(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	path := auditWork(t, s, git, "work-othergen", true)
	completeAuditWork(t, s, "work-othergen")
	event := Event{
		EventID: reclaimedEventID("work-othergen", "project-w", "wt-work-othergen", 0),
		Kind:    "work.worktree_reclaimed", SubjectType: SubjectWorkItem, SubjectID: "work-othergen",
		Actor: "principal:operator", OccurredAt: time.Unix(35, 0).UTC(), PayloadVersion: 1,
		Payload: []byte(fmt.Sprintf(`{"expected_version":3,"resulting_version":4,"set_id":%q,"project_id":"project-w","claim_op_id":"wt-a-different-generation","git_facts":{"already_absent":true}}`, WorktreeSetID("work-othergen"))),
	}
	if _, err := appendInTx(t, s, event); err != nil {
		t.Fatal(err)
	}
	_, err := s.ReclaimWorktree(context.Background(), WorktreeReclaimRequest{
		WorkID: "work-othergen", ProjectID: "project-w", PrincipalRef: "principal-1", RequestID: "conflict-othergen",
		ExpectedVersion: 4, Now: time.Unix(40, 0).UTC(), Runner: git, RequireTerminal: true,
	})
	assertFailureKind(t, err, KindIdempotencyConflict)
	if _, kept := git.worktrees[path]; !kept {
		t.Fatal("refused reclaim must keep the worktree")
	}
}

func TestReclaimWorktreeKeepsRefusingDifferentKind(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	path := auditWork(t, s, git, "work-otherkind", true)
	completeAuditWork(t, s, "work-otherkind")
	event := Event{
		EventID: reclaimedEventID("work-otherkind", "project-w", "wt-work-otherkind", 0),
		Kind:    "work.worktree_occupancy_released", SubjectType: SubjectWorkItem, SubjectID: "work-otherkind",
		Actor: "principal:operator", OccurredAt: time.Unix(35, 0).UTC(), PayloadVersion: 2,
		Payload: []byte(`{"set_id":"wts:work-otherkind","project_id":"project-w","claim_op_id":"wt-work-otherkind","session_ref":"ses-1"}`),
	}
	if _, err := appendInTx(t, s, event); err != nil {
		t.Fatal(err)
	}
	_, err := s.ReclaimWorktree(context.Background(), WorktreeReclaimRequest{
		WorkID: "work-otherkind", ProjectID: "project-w", PrincipalRef: "principal-1", RequestID: "conflict-otherkind",
		ExpectedVersion: 4, Now: time.Unix(40, 0).UTC(), Runner: git, RequireTerminal: true,
	})
	assertFailureKind(t, err, KindIdempotencyConflict)
	if _, kept := git.worktrees[path]; !kept {
		t.Fatal("refused reclaim must keep the worktree")
	}
}
