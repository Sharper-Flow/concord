package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRecordedRoutingDispatchMigrationAndReplayAgree(t *testing.T) {
	useStampedBuild(t)
	requireHistoricalResearchLane(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "routing.db")
	db := openMigratedTo(t, path, 43)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	prerequisites := []Event{
		locatorProductEvent("product-w"), locatorProjectEvent("project-w"), locatorMembershipEvent("product-w", "project-w"),
		{EventID: "routing-work", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: jsonRaw(`{"work_kind":"task","title":"Routing replay","priority":1}`)},
		{EventID: "routing-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-w","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	appendEvent := func(event Event) {
		t.Helper()
		if _, err := tx.ExecContext(ctx, `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`, event.EventID, event.Kind, string(event.SubjectType), event.SubjectID, event.Actor, event.OccurredAt.Format(time.RFC3339Nano), event.PayloadVersion, string(event.Payload)); err != nil {
			t.Fatal(err)
		}
	}
	for _, event := range prerequisites {
		appendEvent(event)
		if err := foldRegisteredEvent(ctx, tx, event); err != nil {
			t.Fatal(err)
		}
	}
	for i, row := range []struct {
		attempt, model, role, lifecycle, failure string
	}{
		{"attempt-migrated-preferred", "openai/gpt-5.6-luna", "preferred", "dispatched", ""},
		{"attempt-migrated-undeclared", "anthropic/claude-unexpected", "undeclared", "failed", WorkerFailureModelIdentity},
	} {
		at := time.Unix(int64(30+i), 0).UTC()
		extra := map[string]any{
			"routing_policy_version": historicalRoutingPolicyToken, "routing_policy_digest": historicalRoutingPolicyDigest,
			"resolved_model": row.model, "resolution_role": row.role, "fallback_reason": "",
		}
		var failedAt any
		detail := ""
		if row.lifecycle == "failed" {
			detail = "model outside the declared set"
			extra["terminal"], extra["terminal_failure_kind"], extra["terminal_detail"] = "failed", row.failure, detail
			failedAt = at.Format(time.RFC3339Nano)
		}
		appendEvent(histDispatchEvent(t, row.attempt+"-dispatch", row.attempt, 2, at, extra))
		// The released routing fold seeds both model columns from the same
		// recorded value. Forward migration and replay must preserve its row.
		if _, err := tx.ExecContext(ctx, `INSERT INTO worker_attempts
			(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,routing_policy_version,routing_policy_digest,resolved_model,resolution_role,fallback_reason,readback_model,packet_schema_version,report_schema_version,lifecycle_state,failure_kind,failure_detail,dispatched_at,completed_at,failed_at)
			VALUES('work-w',?,'research',1,?,'research',?,?,?,?,? ,?,'1.0','1.0',?,?,?, ?,NULL,?)`,
			row.attempt, historicalResearchLaneDigest, historicalRoutingPolicyToken, historicalRoutingPolicyDigest, row.model, row.role, "", row.model, row.lifecycle, row.failure, detail, at.Format(time.RFC3339Nano), failedAt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db, path: path}
	want := map[string]histAttempt{}
	for _, attempt := range []string{"attempt-migrated-preferred", "attempt-migrated-undeclared"} {
		want[attempt] = histAttemptRow(t, db, attempt)
	}
	log := histEventRows(t, s)
	if err := migrateRestoredStage(ctx, db); err != nil {
		t.Fatal(err)
	}
	for attempt, row := range want {
		if got := histAttemptRow(t, db, attempt); got != row {
			t.Fatalf("migration changed %s: %+v, want %+v", attempt, got, row)
		}
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	for attempt, row := range want {
		if got := histAttemptRow(t, db, attempt); got != row {
			t.Fatalf("replay disagrees with migration for %s: %+v, want %+v", attempt, got, row)
		}
	}
	if !reflect.DeepEqual(log, histEventRows(t, s)) {
		t.Fatal("migration or replay changed the recorded event rows")
	}
}
