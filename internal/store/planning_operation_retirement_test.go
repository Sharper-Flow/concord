package store

import (
	"context"
	"errors"
	"testing"
)

// requireOverlapOwnerRefusal asserts the error is the typed refusal of the
// overlap resolution owner itself, not a generic fold or transport failure.
func requireOverlapOwnerRefusal(t *testing.T, err error, wantKind FailureKind, wantDetail string) *Failure {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want a typed store failure", err)
	}
	if failure.Kind != wantKind || failure.Op != "workflow_domain_overlap" || failure.Detail != wantDetail {
		t.Fatalf("refusal = (%s, %s, %q), want (%s, workflow_domain_overlap, %q)",
			failure.Kind, failure.Op, failure.Detail, wantKind, wantDetail)
	}
	return failure
}

// seedOverlapGuardRelation places one pre-existing relation edge in the
// projection so the overlap resolution fold evaluates its guards against a
// real graph. It opens its own transaction and must never run inside one,
// because the store pools a single connection.
func seedOverlapGuardRelation(t *testing.T, s *Store, from, to, kind string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterWrite(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES(?,?,?,'2026-08-19T00:00:00Z')`, from, to, kind); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveWrite(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// assertOverlapResolutionRefusalWroteNothing proves the refused resolution
// rolled back: the pre-seeded guard edge is the only relation, and no
// resolution row or event landed.
func assertOverlapResolutionRefusalWroteNothing(t *testing.T, s *Store, eventID string) {
	t.Helper()
	db := s.DatabaseForTesting()
	var relations int
	if err := db.QueryRow(`SELECT count(*) FROM relations`).Scan(&relations); err != nil {
		t.Fatal(err)
	}
	if relations != 1 {
		t.Fatalf("relations after the refused resolution = %d, want the 1 pre-seeded guard edge", relations)
	}
	var resolutions int
	if err := db.QueryRow(`SELECT count(*) FROM workflow_overlap_resolutions`).Scan(&resolutions); err != nil {
		t.Fatal(err)
	}
	var events int
	if err := db.QueryRow(`SELECT count(*) FROM domain_events WHERE event_id=?`, eventID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if resolutions != 0 || events != 0 {
		t.Fatalf("refused resolution wrote resolutions=%d events=%d, want none", resolutions, events)
	}
}

// TestOverlapTerminalResolutionRejectsCycle proves the overlap resolution
// owner refuses a terminal resolution whose own relation would close a cycle,
// for both terminal kinds, and leaves the projection untouched.
func TestOverlapTerminalResolutionRejectsCycle(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, kind string }{
		{name: "supersedes", kind: ResolutionSupersedes},
		{name: "merged_into", kind: ResolutionMergedInto},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, actor := seedOverlapProjection(t, "cycle-source", "cycle-target", false)
			seedOverlapGuardRelation(t, s, "cycle-target", "cycle-source", tc.kind)
			err := resolveApprovedOverlap(t, s, "overlap-cycle-"+tc.kind, "cycle-source", "cycle-target", 2, 2, tc.kind, "operator approved terminal resolution", actor)
			failure := requireOverlapOwnerRefusal(t, err, KindCycleDetected, "overlap resolution would create a relation cycle")
			if len(failure.Violations) != 1 || failure.Violations[0] != tc.kind+":cycle-source->cycle-target" {
				t.Fatalf("cycle violations = %v, want [%s:cycle-source->cycle-target]", failure.Violations, tc.kind)
			}
			assertOverlapResolutionRefusalWroteNothing(t, s, "overlap-cycle-"+tc.kind)
		})
	}
}

// TestOverlapTerminalResolutionRejectsSecondSuccessor proves the overlap
// resolution owner refuses a second successor: a superseded target cannot
// take another successor, and merged work cannot take another target, for
// both terminal kinds, with the projection untouched.
func TestOverlapTerminalResolutionRejectsSecondSuccessor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, kind, pairFrom, pairTo, guardFrom, guardTo, extraWork string
		wantKind                                                    FailureKind
		wantDetail                                                  string
	}{
		{
			name: "supersedes", kind: ResolutionSupersedes,
			pairFrom: "ss-second", pairTo: "ss-target", guardFrom: "ss-first", guardTo: "ss-target", extraWork: "ss-first",
			wantKind: KindSupersessionSecondSuccessor, wantDetail: "supersession target already has a direct successor",
		},
		{
			name: "merged_into", kind: ResolutionMergedInto,
			pairFrom: "mi-source", pairTo: "mi-other", guardFrom: "mi-source", guardTo: "mi-target", extraWork: "mi-target",
			wantKind: KindRelationConflict, wantDetail: "merged work already has a direct target",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, actor := seedOverlapProjection(t, tc.pairFrom, tc.pairTo, false)
			seedWork(t, s, tc.extraWork)
			seedOverlapGuardRelation(t, s, tc.guardFrom, tc.guardTo, tc.kind)
			err := resolveApprovedOverlap(t, s, "overlap-second-"+tc.kind, tc.pairFrom, tc.pairTo, 2, 2, tc.kind, "operator approved terminal resolution", actor)
			_ = requireOverlapOwnerRefusal(t, err, tc.wantKind, tc.wantDetail)
			assertOverlapResolutionRefusalWroteNothing(t, s, "overlap-second-"+tc.kind)
		})
	}
}

// TestOverlapBlocksRemovesWorkFromReady proves that
// the blocks row resolve_overlap writes removes the blocked item from
// ready while the blocker is needed, and a terminal blocker releases it.
func TestOverlapBlocksRemovesWorkFromReady(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, actor := seedOverlapProjection(t, "ready-blocker", "ready-blocked", false)
	if err := s.Transact(ctx, func(tx *Transaction) error {
		_, err := ResolveWorkflowDomainOverlapTx(ctx, tx, WorkflowDomainOverlapResolutionRequest{
			EventID: "ready-blocks", FromWorkID: "ready-blocker", ToWorkID: "ready-blocked",
			FromExpectedVersion: 2, ToExpectedVersion: 2, FromContractVersion: 1, ToContractVersion: 1,
			ResolutionKind: ResolutionBlocks, Reason: "operator sequenced the pair",
			Actor: actor,
		})
		return err
	}); err != nil {
		t.Fatalf("blocks resolution rejected: %v", err)
	}
	var resolutionID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT resolution_id FROM relations WHERE work_id_from='ready-blocker' AND work_id_to='ready-blocked' AND kind='blocks'`).Scan(&resolutionID); err != nil {
		t.Fatalf("the blocks resolution must commit its gating relation: %v", err)
	}
	if resolutionID != "ready-blocks" {
		t.Fatalf("blocks relation resolution_id = %q, want ready-blocks", resolutionID)
	}
	setWorkLifecycleForTesting(t, s, "ready-blocker", "needed")
	setWorkLifecycleForTesting(t, s, "ready-blocked", "needed")
	readyIDs := func() map[string]bool {
		t.Helper()
		ready, err := s.QueryQ5(ctx, Q5Request{Product: "product", Project: "project"})
		if err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for _, item := range ready.Items {
			ids[item.ID] = true
		}
		return ids
	}
	if ids := readyIDs(); ids["ready-blocked"] || !ids["ready-blocker"] {
		t.Fatalf("ready IDs = %v, want ready-blocked gated out and ready-blocker ready", ids)
	}
	blocked, err := s.QueryQ4(ctx, Q4Request{Product: "product", Project: "project", Limit: 1, Depth: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked.Items) != 1 || blocked.Items[0].ID != "ready-blocked" || len(blocked.Items[0].Blockers) != 1 || blocked.Items[0].Blockers[0].ID != "ready-blocker" || len(blocked.Items[0].Blockers[0].Blockers) != 0 {
		t.Fatalf("blocked = %#v, want only ready-blocked with ready-blocker as its sole leaf blocker", blocked)
	}
	setWorkTerminalForTesting(t, s, "ready-blocker")
	// The terminal blocker leaves ready itself, but its gate is released: the
	// blocked item is ready again.
	if ids := readyIDs(); !ids["ready-blocked"] {
		t.Fatalf("ready IDs after the blocker went terminal = %v, want ready-blocked released", ids)
	}
}
