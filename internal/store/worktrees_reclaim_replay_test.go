package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// noGenerationReclaimPayload builds the payload-v1 reclaim body the
// pre-generation writer recorded: no claim generation field.
func noGenerationReclaimPayload(expected, resulting int64) string {
	return fmt.Sprintf(`{"expected_version":%d,"resulting_version":%d,"set_id":%q,"project_id":"project-w","git_facts":{"already_absent":true}}`, expected, resulting, WorktreeSetID("work-w"))
}

func noGenerationReclaimEvent(eventID string, expected, resulting int64, at time.Time) Event {
	return Event{
		EventID:     eventID,
		Kind:        "work.worktree_reclaimed",
		SubjectType: SubjectWorkItem, SubjectID: "work-w",
		Actor: "principal-1", OccurredAt: at, PayloadVersion: 1,
		Payload: jsonRaw(noGenerationReclaimPayload(expected, resulting)),
	}
}

// TestPreGenerationReclaimReplayFoldsAndLiveAppendRefuses seeds the claimed →
// reclaimed → claimed → reclaimed sequence the pre-generation log holds: both
// reclaims carry no claim generation. The rebuild must fold both reclaims in
// log order, while the live path must keep refusing the ambiguous fresh
// append once an earlier generation sits reclaimed.
func TestPreGenerationReclaimReplayFoldsAndLiveAppendRefuses(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()

	// Generation one: claimed, then reclaimed by a payload-v1 event with no
	// claim generation. No earlier generation sits reclaimed, so the live
	// append folds, as the historical writer's append did.
	if _, err := s.ClaimWorktree(ctx, baseClaim(git)); err != nil {
		t.Fatal(err)
	}
	first := noGenerationReclaimEvent("legacy-reclaim-1", 3, 4, time.Unix(20, 0).UTC())
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{first}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-w"): 3}}); err != nil {
		t.Fatalf("first no-generation reclaim append failed: %v", err)
	}

	// Generation two: claimed while generation one sits reclaimed in
	// worktree_claims.
	second := baseClaim(git)
	second.OpID = "wt-op-2"
	second.ExpectedVersion = 4
	if _, err := s.ClaimWorktree(ctx, second); err != nil {
		t.Fatal(err)
	}

	// The live path refuses the ambiguous fresh append: the payload names no
	// generation, and the reclaimed earlier generation cannot be told apart
	// from the active one by ordering.
	ambiguous := noGenerationReclaimEvent("legacy-reclaim-2", 5, 6, time.Unix(30, 0).UTC())
	err := ApplyOperation(ctx, s, Operation{Events: []Event{ambiguous}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-w"): 5}})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindProjectionConflict || !strings.Contains(failure.Detail, "cannot target the active worktree") {
		t.Fatalf("ambiguous live no-generation append err=%v, want a projection conflict naming the active worktree", err)
	}
	if entries, err := s.WorktreeEntries(ctx, "work-w"); err != nil || len(entries) != 1 || entries[0].ClaimOpID != "wt-op-2" || entries[0].State != worktreeEntryActive {
		t.Fatalf("refused append changed the projection: entries=%+v err=%v", entries, err)
	}

	// The historical log holds the same event the live append refused. Record
	// it the way the pre-generation writer did, as a log row the projection
	// never folded, then rebuild: the fold must accept it in sequence order.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,1,?)`,
		"legacy-reclaim-2", "work.worktree_reclaimed", string(SubjectWorkItem), "work-w", "principal-1", time.Unix(30, 0).UTC().Format(time.RFC3339Nano), noGenerationReclaimPayload(5, 6)); err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("rebuild must fold pre-generation reclaims: %v", err)
	}
	entries, err := s.WorktreeEntries(ctx, "work-w")
	if err != nil || len(entries) != 1 || entries[0].ClaimOpID != "wt-op-2" || entries[0].State != worktreeEntryReclaimed {
		t.Fatalf("entries after rebuild=%+v err=%v", entries, err)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-w'`).Scan(&version); err != nil || version != 6 {
		t.Fatalf("work version after rebuild=%d err=%v, want 6", version, err)
	}
	// The claim row is operational state: the live reclaim of generation one
	// marked it reclaimed, and replaying the log's reclaims must not regress
	// generation two's still-verified operational claim.
	for opID, wantState := range map[string]string{"wt-op-1": worktreeStateReclaimed, "wt-op-2": worktreeStateVerified} {
		var state string
		if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM worktree_claims WHERE op_id=?`, opID).Scan(&state); err != nil || state != wantState {
			t.Fatalf("claim %s state=%q err=%v, want %q", opID, state, err, wantState)
		}
	}
}
