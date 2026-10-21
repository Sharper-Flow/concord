package store

import (
	"context"
	"database/sql"
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
	if err := enterFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES(?,?,?,'2026-08-19T00:00:00Z')`, from, to, kind); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
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

// TestRetiredPlanningEventsReplay proves history stays replayable after the
// planning-operation retirement: one journal carries the link-written
// relation.added and relation.removed shapes, work.superseded,
// work.reopened_from_superseded, and the retired Domain-observation
// record/dismiss events, and the rebuild folds them to the same projection
// the retired writers produced, without error and without touching the log.
func TestRetiredPlanningEventsReplay(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	for _, id := range []string{"replay-link-a", "replay-link-b", "replay-link-c", "replay-link-d", "replay-successor", "replay-predecessor"} {
		seedWork(t, s, id)
	}
	seedCurrentDomain(t, s, "product", "replay-domain")
	observed := operationEvent("replay-observation-recorded", "domain.observation_recorded", SubjectProduct, "product", map[string]any{
		"observation_id": "dob:9270000000000001", "product_id": "product", "domain_id": "replay-domain",
		"statement": "historical observation recorded by the retired route", "refs": []string{"work:replay-link-a"}, "tags": []string{"historical"},
	})
	dismissed := operationEvent("replay-observation-dismissed", "domain.observation_dismissed", SubjectProduct, "product", map[string]any{
		"observation_id": "dob:9270000000000001", "product_id": "product", "domain_id": "replay-domain",
	})
	journal := []Event{
		relationAddedEvent("replay-link-kept", "blocks", "replay-link-a", "replay-link-b", 2, 3),
		relationAddedEvent("replay-link-removed", "blocks", "replay-link-c", "replay-link-d", 2, 3),
		relationRemovedEvent("replay-link-unlinked", "blocks", "replay-link-c", "replay-link-d", 3, 4),
		workSupersededEvent("replay-superseded", "replay-successor", "replay-predecessor", 2, 3),
		workReopenedFromSupersededEvent("replay-reopened", "replay-predecessor", "replay-successor", 3, 4),
		observed,
		dismissed,
	}
	db := s.DatabaseForTesting()
	insertHistoricalEvents(t, db, journal)
	before := planningEventCount(t, db)
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("RebuildFromLog() over retired planning events error = %v", err)
	}
	if after := planningEventCount(t, db); after != before {
		t.Fatalf("replay changed the log: %d events after rebuild, want %d", after, before)
	}
	// The retired Domain-observation pair records as corpus witnesses: the
	// passing replay above proves the candidate folds both rows unchanged.
	recordHistoricalWitness(t, s, observed.EventID, observed.Kind, observed.PayloadVersion, observed.Payload)
	recordHistoricalWitness(t, s, dismissed.EventID, dismissed.Kind, dismissed.PayloadVersion, dismissed.Payload)
	// The kept link edge survives; the removed edge and the supersession edge
	// the reopen deleted stay gone.
	assertTableCount(t, s, "relations", 1)
	var kept int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM relations WHERE work_id_from='replay-link-a' AND work_id_to='replay-link-b' AND kind='blocks'`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 1 {
		t.Fatalf("kept link edge count = %d, want 1", kept)
	}
	var removed int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM relations WHERE (work_id_from='replay-link-c' AND work_id_to='replay-link-d') OR kind='supersedes'`).Scan(&removed); err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("removed or reopened-over edges survived = %d, want 0", removed)
	}
	// The superseded item is back to needed at v4; the untouched endpoints
	// hold the versions the journal advanced them to.
	assertWorkNeededVersion(t, db, "replay-predecessor", 4)
	assertWorkNeededVersion(t, db, "replay-successor", 2)
	assertWorkNeededVersion(t, db, "replay-link-a", 3)
	assertWorkNeededVersion(t, db, "replay-link-c", 4)
	// The recorded observation is dismissed, still readable for audit, and
	// holds no open row on the Domain.
	var state, dismissedAt string
	if err := db.QueryRowContext(ctx, `SELECT state, coalesce(dismissed_at,'') FROM domain_observations WHERE observation_id='dob:9270000000000001'`).Scan(&state, &dismissedAt); err != nil {
		t.Fatalf("replayed observation must stay readable: %v", err)
	}
	if state != DomainObservationDismissed || dismissedAt == "" {
		t.Fatalf("replayed observation = (%q, %q), want (dismissed, non-empty)", state, dismissedAt)
	}
	var open int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM domain_observations WHERE product_id='product' AND domain_id='replay-domain' AND state=?`, DomainObservationOpen).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Fatalf("open observations after replay = %d, want 0", open)
	}
}

func assertWorkNeededVersion(t *testing.T, db *sql.DB, id string, version int64) {
	t.Helper()
	var gotLifecycle string
	var gotVersion int64
	if err := db.QueryRowContext(context.Background(), `SELECT lifecycle, version FROM work_items WHERE id=?`, id).Scan(&gotLifecycle, &gotVersion); err != nil {
		t.Fatalf("work item %s must stay readable: %v", id, err)
	}
	if gotLifecycle != "needed" || gotVersion != version {
		t.Fatalf("work item %s = (%q, v%d), want (needed, v%d)", id, gotLifecycle, gotVersion, version)
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
			// Historical source-v1 effect control: the cycle guard was first
			// reached under the v1 rules, whose only approval mandate was the
			// plan layer's bare reference. The explicit v1 event keeps the
			// guard assertions against their original source shape; the live
			// v2 destructive class is covered by the consumed-approval
			// boundary elsewhere.
			err := historicalSourceV1OverlapResolution(t, s, "overlap-cycle-"+tc.kind, "cycle-source", "cycle-target", 2, 2, tc.kind, "operator approved terminal resolution", "approval:overlap-cycle", actor)
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
			// Historical source-v1 effect control: the second-successor
			// guards were first reached under the v1 rules, whose only
			// approval mandate was the plan layer's bare reference. The
			// explicit v1 event keeps the guard assertions against their
			// original source shape; the live v2 destructive class is covered
			// by the consumed-approval boundary elsewhere.
			err := historicalSourceV1OverlapResolution(t, s, "overlap-second-"+tc.kind, tc.pairFrom, tc.pairTo, 2, 2, tc.kind, "operator approved terminal resolution", "approval:overlap-second", actor)
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
