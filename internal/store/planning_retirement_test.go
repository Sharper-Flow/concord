package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// retiredPlanningEvents is one historical event of each kind CD-0213 D8
// retires. Each payload matches the schema the kind was appended under, so a
// log written before the retirement still decodes.
func retiredPlanningEvents(productID, initiativeID, childID string) []Event {
	entry := func(id, kind string, expected int64) Event {
		return operationEvent(id, kind, SubjectWorkItem, initiativeID, map[string]any{
			"child_work_id": childID, "position": 0, "required": true,
			"expected_version": expected, "resulting_version": expected + 1,
		})
	}
	return []Event{
		operationEvent("retired-planning-mode", "product.planning_mode_set", SubjectProduct, productID, map[string]any{
			"product_id": productID, "planning_mode": "linear_enabled", "reason": "historical",
			"expected_version": 2, "resulting_version": 3,
		}),
		entry("retired-entry-added", "initiative_entry.added", 2),
		entry("retired-entry-reordered", "initiative_entry.reordered", 3),
		entry("retired-entry-requiredness", "initiative_entry.requiredness_changed", 4),
		operationEvent("retired-narrative", "initiative.narrative_revised", SubjectWorkItem, initiativeID, map[string]any{
			"narrative": "historical narrative", "reason": "historical",
			"expected_version": 5, "resulting_version": 6,
		}),
		entry("retired-entry-removed", "initiative_entry.removed", 6),
	}
}

// seedRetiredPlanningHistory builds a store whose log carries a historical
// initiative capture the way a pre-retirement binary wrote it: the events
// land in domain_events directly and RebuildFromLog folds them, because the
// live append seam refuses a new initiative capture under CD-0213 D4.
func seedRetiredPlanningHistory(t *testing.T) (*Store, []Event) {
	t.Helper()
	s := openTemp(t)
	seedWork(t, s, "retired-child")
	historical := []Event{
		func() Event {
			created := operationEvent("create-retired-initiative", "work.created", SubjectWorkItem, "retired-initiative", map[string]any{
				"work_kind": "initiative", "title": "Historical initiative", "priority": 0,
			})
			created.PayloadVersion = 2
			return created
		}(),
		operationEvent("membership-retired-initiative", "work_project.added", SubjectWorkItem, "retired-initiative", map[string]any{
			"work_id": "retired-initiative", "project_id": "project", "role": "primary", "reason": "test",
			"expected_version": 1, "resulting_version": 2,
		}),
	}
	insertHistoricalEvents(t, s.DatabaseForTesting(), historical)
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("replay of a historical initiative capture must still fold: %v", err)
	}
	return s, retiredPlanningEvents("product", "retired-initiative", "retired-child")
}

func insertHistoricalEvents(t *testing.T, db *sql.DB, events []Event) {
	t.Helper()
	for _, event := range events {
		if _, err := db.ExecContext(context.Background(), `INSERT INTO domain_events(event_id, kind, subject_type, subject_id, actor, occurred_at, payload_version, payload) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			event.EventID, event.Kind, string(event.SubjectType), event.SubjectID, event.Actor, event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), event.PayloadVersion, string(event.Payload)); err != nil {
			t.Fatalf("seed historical %s: %v", event.Kind, err)
		}
	}
}

func planningEventCount(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var count int64
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM domain_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertRetiredPlanningRefusal(t *testing.T, err error) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "CD-0213") {
		t.Fatalf("error = %v, want an invalid_operation refusal naming CD-0213", err)
	}
}

func TestRetiredInitiativeExecutionRefusalNamesOperation(t *testing.T) {
	t.Parallel()
	s, _ := seedRetiredPlanningHistory(t)
	err := s.RequireActionableWork(context.Background(), "retired-initiative", "worktree_claim")
	assertRetiredPlanningRefusal(t, err)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Op != "worktree_claim" || strings.Contains(failure.Detail, "event kind") {
		t.Fatalf("execution refusal must name its operation, not an event kind: %v", err)
	}
}

// TestRetiredPlanningEventsReplayWithoutInitiativeState proves CD-0213 D8:
// a log that carries every retired planning kind still rebuilds, the rebuilt
// projection holds no Initiative membership, and each retired event still
// advances its subject version, so a later historical event on the same
// subject folds at the version it was written against.
func TestRetiredPlanningEventsReplayWithoutInitiativeState(t *testing.T) {
	t.Parallel()
	s, events := seedRetiredPlanningHistory(t)
	ctx := context.Background()
	db := s.DatabaseForTesting()
	for _, event := range events {
		if _, err := db.ExecContext(ctx, `INSERT INTO domain_events(event_id, kind, subject_type, subject_id, actor, occurred_at, payload_version, payload) VALUES(?, ?, ?, ?, 'operator', '2026-09-23T00:00:00Z', 1, ?)`,
			event.EventID, event.Kind, string(event.SubjectType), event.SubjectID, string(event.Payload)); err != nil {
			t.Fatalf("seed historical %s: %v", event.Kind, err)
		}
	}
	later := workTransitionEvent("retired-later-transition", "retired-initiative", "needed", "in_progress", 7, 8)
	if _, err := db.ExecContext(ctx, `INSERT INTO domain_events(event_id, kind, subject_type, subject_id, actor, occurred_at, payload_version, payload) VALUES(?, ?, ?, ?, 'operator', '2026-09-24T00:00:00Z', 1, ?)`,
		later.EventID, later.Kind, string(later.SubjectType), later.SubjectID, string(later.Payload)); err != nil {
		t.Fatalf("seed later transition: %v", err)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("RebuildFromLog() over retired planning events error = %v", err)
	}
	// Every retired kind is a corpus witness under the recorder rule: the log
	// rows predate the retirement, and this passing replay proves the
	// candidate still folds each one unchanged.
	for _, event := range events {
		recordHistoricalWitness(t, s, event.EventID, event.Kind, event.PayloadVersion, event.Payload)
	}
	var includes int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM relations WHERE kind='includes'`).Scan(&includes); err != nil {
		t.Fatal(err)
	}
	if includes != 0 {
		t.Fatalf("rebuilt projection holds %d includes relations, want none", includes)
	}
	var kind, lifecycle string
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT kind, lifecycle, version FROM work_items WHERE id='retired-initiative'`).Scan(&kind, &lifecycle, &version); err != nil {
		t.Fatalf("historical initiative work item must stay readable: %v", err)
	}
	if kind != "initiative" || lifecycle != "in_progress" || version != 8 {
		t.Fatalf("historical initiative = (%s, %s, v%d), want (initiative, in_progress, v8)", kind, lifecycle, version)
	}
	var productVersion int64
	if err := db.QueryRowContext(ctx, `SELECT version FROM products WHERE id='product'`).Scan(&productVersion); err != nil {
		t.Fatal(err)
	}
	if productVersion != 3 {
		t.Fatalf("Product version after the retired planning-mode event = %d, want 3", productVersion)
	}
}

// TestRetiredPlanningEventsRefuseNewAppends proves CD-0213 D8: no route can
// append a retired planning kind, and the refusal names the decision.
func TestRetiredPlanningEventsRefuseNewAppends(t *testing.T) {
	t.Parallel()
	s, events := seedRetiredPlanningHistory(t)
	for _, event := range events {
		err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}})
		assertRetiredPlanningRefusal(t, err)
	}
}

// TestRetiredInitiativeCaptureRefusedAndWritesNothing proves CD-0213 D4 on
// the generic capture route: a raw work.created whose payload classifies as
// initiative refuses at the append seam, a payload recorded at an older
// version refuses through the same upcast classification, and a refused
// capture leaves no domain_events row behind.
func TestRetiredInitiativeCaptureRefusedAndWritesNothing(t *testing.T) {
	t.Parallel()
	s, _ := seedRetiredPlanningHistory(t)
	db := s.DatabaseForTesting()
	captures := []Event{
		func() Event {
			v3 := operationEvent("new-initiative-capture", "work.created", SubjectWorkItem, "new-initiative", map[string]any{
				"work_kind": "initiative", "title": "New initiative", "priority": 0,
			})
			v3.PayloadVersion = 3
			return v3
		}(),
		func() Event {
			// The v1 payload shape: the kind travels in "kind", so only the
			// prepared upcast payload can classify the capture.
			v1 := operationEvent("old-version-initiative-capture", "work.created", SubjectWorkItem, "old-initiative", map[string]any{
				"kind": "initiative", "title": "Old initiative", "priority": 0,
			})
			v1.PayloadVersion = 1
			return v1
		}(),
	}
	for _, capture := range captures {
		before := planningEventCount(t, db)
		err := ApplyOperation(context.Background(), s, Operation{
			Events:           []Event{capture},
			ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, capture.SubjectID): 0},
		})
		assertRetiredPlanningRefusal(t, err)
		if after := planningEventCount(t, db); after != before {
			t.Fatalf("refused capture %s wrote %d log rows, want none", capture.EventID, after-before)
		}
	}
}

// TestRetiredInitiativeLifecycleRefusedAndWritesNothing proves CD-0213 D4 on
// the generic lifecycle route: a new transition on an existing historical
// initiative refuses by the subject's stored kind, and the refusal leaves no
// domain_events row behind. The historical record itself stays readable.
func TestRetiredInitiativeLifecycleRefusedAndWritesNothing(t *testing.T) {
	t.Parallel()
	s, _ := seedRetiredPlanningHistory(t)
	ctx := context.Background()
	db := s.DatabaseForTesting()
	before := planningEventCount(t, db)
	err := ApplyOperation(ctx, s, Operation{
		Events:           []Event{workTransitionEvent("new-initiative-transition", "retired-initiative", "needed", "in_progress", 2, 3)},
		ExpectedVersions: workVersion("retired-initiative", 2),
	})
	assertRetiredPlanningRefusal(t, err)
	if after := planningEventCount(t, db); after != before {
		t.Fatalf("refused lifecycle wrote %d log rows, want none", after-before)
	}
	var kind, lifecycle string
	if err := db.QueryRowContext(ctx, `SELECT kind, lifecycle FROM work_items WHERE id='retired-initiative'`).Scan(&kind, &lifecycle); err != nil {
		t.Fatal(err)
	}
	if kind != "initiative" || lifecycle != "needed" {
		t.Fatalf("historical initiative = (%s, %s), want (initiative, needed) after the refusal", kind, lifecycle)
	}
}

func TestRetiredInitiativeSelectionAfterMigration124(t *testing.T) {
	t.Parallel()
	for _, blockedInitiative := range []bool{false, true} {
		name := "ready-initiative"
		if blockedInitiative {
			name = "blocked-initiative"
		}
		t.Run(name, func(t *testing.T) {
			s, _ := seedRetiredPlanningHistory(t)
			ctx := context.Background()
			seedWork(t, s, "live-blocker")
			seedWork(t, s, "live-blocked")
			historical := []Event{
				relationAddedEvent("historical-initiative-block", "blocks", "retired-initiative", "retired-child", 2, 3),
				relationAddedEvent("historical-live-block", "blocks", "retired-child", "live-blocked", 2, 3),
				relationAddedEvent("historical-second-initiative-block", "blocks", "retired-initiative", "live-blocked", 3, 4),
			}
			if blockedInitiative {
				historical = append(historical, relationAddedEvent("historical-blocked-initiative", "blocks", "live-blocker", "retired-initiative", 2, 3))
			}
			insertHistoricalEvents(t, s.DatabaseForTesting(), historical)
			if err := RebuildFromLog(ctx, s); err != nil {
				t.Fatal(err)
			}
			// Restore the pre-retirement schema with the open Initiative and its
			// historical edges already present, then run the real migration.
			if err := undoMigration124(t, ctx, s.DatabaseForTesting()); err != nil {
				t.Fatal(err)
			}
			if err := applyMigration(ctx, s.DatabaseForTesting(), migrations[planningRetirementMigrationIndex(t)]); err != nil {
				t.Fatal(err)
			}

			ready, err := s.QueryQ5(ctx, Q5Request{Product: "product", Project: "project"})
			if err != nil {
				t.Fatal(err)
			}
			readyIDs := map[string]bool{}
			for _, item := range ready.Items {
				readyIDs[item.ID] = true
			}
			if len(readyIDs) != 2 || !readyIDs["retired-child"] || !readyIDs["live-blocker"] {
				t.Errorf("ready IDs = %v, want only retired-child and live-blocker", readyIDs)
			}
			blocked, err := s.QueryQ4(ctx, Q4Request{Product: "product", Project: "project", Limit: 1, Depth: 3})
			if err != nil {
				t.Fatal(err)
			}
			if len(blocked.Items) != 1 || blocked.Items[0].ID != "live-blocked" || len(blocked.Items[0].Blockers) != 1 || blocked.Items[0].Blockers[0].ID != "retired-child" || len(blocked.Items[0].Blockers[0].Blockers) != 0 || blocked.NextCursor != nil {
				t.Errorf("blocked = %#v, want only live-blocked with retired-child as its sole leaf blocker", blocked)
			}
			initiativeOnly, err := s.QueryQ4(ctx, Q4Request{Product: "product", Work: "retired-initiative"})
			if err != nil || len(initiativeOnly.Items) != 0 {
				t.Errorf("explicit Initiative blocked selection = %#v, %v; want empty", initiativeOnly, err)
			}
			list, err := s.QueryQ3(ctx, Q3Request{Product: "product"})
			if err != nil {
				t.Fatal(err)
			}
			if len(list.Items) != 4 {
				t.Errorf("list lost historical work: %v", list.Items)
			}
			foundInitiative := false
			for _, item := range list.Items {
				if item.ID == "retired-initiative" {
					foundInitiative = item.Kind == "initiative" && item.Lifecycle == "needed"
				}
				if item.ID == "retired-initiative" && (item.Ready || item.Blocked || item.Active) {
					t.Errorf("historical Initiative carries actionable flags: %#v", item)
				}
				if item.ID == "retired-child" && (!item.Ready || item.Blocked) {
					t.Errorf("historical edge still gates child: %#v", item)
				}
			}
			if !foundInitiative {
				t.Error("list must retain the historical needed Initiative")
			}
			snapshot, err := s.QueryQ2(ctx, Q2Request{Product: "product"})
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.LifecycleCounts["needed"] != 4 || snapshot.DerivedCounts.Ready != 2 || snapshot.DerivedCounts.Blocked != 1 {
				t.Errorf("snapshot counts = %v, %#v; want 4 historical needed, 2 actionable ready, 1 blocked", snapshot.LifecycleCounts, snapshot.DerivedCounts)
			}
			for _, item := range snapshot.Items {
				if item.Kind == "initiative" && (item.Ready || item.Blocked || item.Active) {
					t.Errorf("snapshot offers historical Initiative: %#v", item)
				}
			}
			scope, err := s.QueryQ6(ctx, Q6Request{Product: "product", Work: "retired-initiative"})
			if err != nil || scope.Work == nil || scope.Work.Kind != "initiative" || scope.Work.Ready || scope.Work.Blocked || scope.Work.Active {
				t.Errorf("Initiative scope = %#v, %v; want readable, non-actionable record", scope, err)
			}
			projectScope, err := s.QueryQ6(ctx, Q6Request{Product: "product", Project: "project"})
			if err != nil {
				t.Fatal(err)
			}
			if len(projectScope.Items) != 4 {
				t.Errorf("Project scope lost historical work: %v", projectScope.Items)
			}
			for _, item := range projectScope.Items {
				if item.Kind == "initiative" && (item.Ready || item.Blocked || item.Active) {
					t.Errorf("Project scope offers historical Initiative: %#v", item)
				}
				if item.ID == "retired-child" && (!item.Ready || item.Blocked) {
					t.Errorf("Project scope still gates child: %#v", item)
				}
			}
			relations, err := s.QueryQ8(ctx, Q8Request{Work: "retired-initiative", RelationKinds: []string{"blocks"}, Direction: "outgoing"})
			if err != nil || len(relations.Edges) != 2 {
				t.Errorf("historical relations = %#v, %v; want both outgoing blocks readable", relations, err)
			}
		})
	}
}

func TestRetiredInitiativeActiveSelection(t *testing.T) {
	t.Parallel()
	s, _ := seedRetiredPlanningHistory(t)
	ctx := context.Background()
	insertHistoricalEvents(t, s.DatabaseForTesting(), []Event{
		workTransitionEvent("historical-active-initiative", "retired-initiative", "needed", "in_progress", 2, 3),
		workTransitionEvent("historical-active-child", "retired-child", "needed", "in_progress", 2, 3),
	})
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	assertIDs := func(ids []string, err error) {
		t.Helper()
		if err != nil || len(ids) != 1 || ids[0] != "retired-child" {
			t.Errorf("active IDs = %v, %v; want only retired-child", ids, err)
		}
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		ids, err := ActiveWorkIDsTx(ctx, tx, "product")
		assertIDs(ids, err)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.QueryQ2(ctx, Q2Request{Product: "product"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LifecycleCounts["in_progress"] != 2 || snapshot.DerivedCounts.Active != 1 {
		t.Errorf("active snapshot = %v, %#v; want 2 historical in_progress and 1 actionable active", snapshot.LifecycleCounts, snapshot.DerivedCounts)
	}
	for _, item := range snapshot.Items {
		if item.Kind == "initiative" && item.Active {
			t.Errorf("historical Initiative is active: %#v", item)
		}
	}
}
