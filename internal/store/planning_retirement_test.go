package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func insertHistoricalEvents(t *testing.T, db *sql.DB, events []Event) {
	t.Helper()
	for _, event := range events {
		if _, err := db.ExecContext(context.Background(), `INSERT INTO domain_events(event_id, kind, subject_type, subject_id, actor, occurred_at, payload_version, payload) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			event.EventID, event.Kind, string(event.SubjectType), event.SubjectID, event.Actor, event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), event.PayloadVersion, string(event.Payload)); err != nil {
			t.Fatalf("seed historical %s: %v", event.Kind, err)
		}
	}
}

func assertRetiredPlanningRefusal(t *testing.T, err error) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "CD-0213") {
		t.Fatalf("error = %v, want an invalid_operation refusal naming CD-0213", err)
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
			s := openTemp(t)
			ctx := context.Background()
			seedWork(t, s, "retired-child")
			seedWork(t, s, "retired-initiative")
			seedWork(t, s, "live-blocker")
			seedWork(t, s, "live-blocked")
			// Migration 124 removes includes, not work items or blocks edges.
			// Seed exactly the surviving current rows to exercise selection.
			if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE work_items SET kind='initiative' WHERE id='retired-initiative'; DELETE FROM write_guard`); err != nil {
				t.Fatal(err)
			}
			seedOverlapGuardRelation(t, s, "retired-initiative", "retired-child", "blocks")
			seedOverlapGuardRelation(t, s, "retired-child", "live-blocked", "blocks")
			seedOverlapGuardRelation(t, s, "retired-initiative", "live-blocked", "blocks")
			if blockedInitiative {
				seedOverlapGuardRelation(t, s, "live-blocker", "retired-initiative", "blocks")
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
