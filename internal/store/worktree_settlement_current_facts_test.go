package store

import (
	"reflect"
	"testing"
)

func TestWorktreeSettlementSequenceUsesCurrentCounts(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	if _, err := s.ClaimWorktree(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	removal := &WorktreeNativeRemoval{WorkID: req.WorkID, ProjectID: req.ProjectID, ClaimOpID: req.OpID, PrincipalRef: req.PrincipalRef}
	const branch = "work/settlement_count"
	read := func() (any, error) {
		var count int
		err := s.Transact(t.Context(), func(transaction *Transaction) error {
			var err error
			count, err = worktreeRefSettlementSequence(t.Context(), transaction.tx, req.WorkID, req.ProjectID, req.OpID, branch)
			return err
		})
		return count, err
	}
	assertCount := func(want int) {
		t.Helper()
		got, err := read()
		if err != nil || got != want {
			t.Fatalf("settlement sequence=%v err=%v, want %d", got, err, want)
		}
	}
	settle := func(phase, reason string) {
		t.Helper()
		if err := s.Transact(t.Context(), func(transaction *Transaction) error {
			return settleWorktreeRefOutcomeTx(t.Context(), transaction.tx, removal, WorktreeNativeOutcome{Branch: branch, Phase: phase, Reason: reason})
		}); err != nil {
			t.Fatal(err)
		}
	}
	assertCount(0)
	settle(WorktreeRefPhasePlanned, "first")
	assertCount(1)
	settle(WorktreeRefPhasePlanned, "first")
	assertCount(1) // A semantic no-op records no owned identity.
	settle(WorktreeRefPhasePlanned, "new observation")
	for _, phase := range []string{WorktreeRefPhaseCheckoutProvenAbsent, WorktreeRefPhaseDeleted, WorktreeRefPhaseRestorationOwed, WorktreeRefPhaseRestored} {
		settle(phase, phase)
	}
	assertCount(6)
	removal.ClaimIncarnation = 1
	settle(WorktreeRefPhasePlanned, "reopened claim")
	assertCount(7)
	// A settlement with an unrelated identity does not consume a sequence
	// from this ref's owned prefix.
	if err := ApplyOperation(t.Context(), s, Operation{Events: []Event{operationEvent("unrelated-settlement-identity", "work.worktree_removal_settled", SubjectWorkItem, req.WorkID, map[string]any{
		"set_id": WorktreeSetID(req.WorkID), "project_id": req.ProjectID, "claim_op_id": req.OpID, "claim_incarnation": 1,
		"branch": branch, "phase": WorktreeRefPhasePlanned, "reason": "separate identity",
	})}}); err != nil {
		t.Fatal(err)
	}
	assertCount(7)
	settle(WorktreeRefPhasePlanned, "next owned observation")
	assertCount(8)
	if err := s.Transact(t.Context(), func(transaction *Transaction) error {
		return settleWorktreeRefOutcomeTx(t.Context(), transaction.tx, removal, WorktreeNativeOutcome{Branch: "work/settlementXcount", Phase: WorktreeRefPhasePlanned, Reason: "another ref"})
	}); err != nil {
		t.Fatal(err)
	}
	assertCount(8) // The underscore is a literal, not a LIKE wildcard.
	base := worktreeRefSettlementEventBase(req.WorkID, req.ProjectID, req.OpID, branch)
	for _, id := range []string{base + ">planned:6:i1", base + "planned>planned:7:i1"} {
		var exists bool
		if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM domain_events WHERE event_id=?)`, id).Scan(&exists); err != nil || !exists {
			t.Fatalf("published identity %q exists=%t err=%v", id, exists, err)
		}
	}
	var legacyCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM domain_events WHERE event_id>=? AND event_id<?`, base, base+"\xff").Scan(&legacyCount); err != nil {
		t.Fatal(err)
	}
	if legacyCount != 8 {
		t.Fatalf("owned prefix count=%d, want 8", legacyCount)
	}
	queries := []string{`SELECT json_array(claim_incarnation,settlement_count) FROM worktree_ref_outcomes ORDER BY set_id,project_id,claim_op_id,claim_incarnation,branch`}
	want := workerFactsQueryRows(t, s, queries)
	repopulateSessionWindowCurrentFactsForTest(t, s)
	if got := workerFactsQueryRows(t, s, queries); !reflect.DeepEqual(got, want) {
		t.Fatalf("settlement backfill=%v, want folded counts %v", got, want)
	}
	assertCount(8)
	assertWorkerFactsLogBlind(t, s, map[string]func() (any, error){"G38-settlement-count": read})
}
