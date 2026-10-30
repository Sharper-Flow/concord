package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordedHistoricalProjectionMigrationPreservesRows(t *testing.T) {
	useStampedBuild(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "routing.db")
	db := openMigratedTo(t, path, 43)
	s := &Store{db: db, path: path}
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
		{EventID: "routing-work", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion("work.created"), Payload: jsonRaw(`{"work_kind":"task","title":"Routing migration","priority":1}`)},
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
	}
	// The old schema's rows are migration inputs, not live event-fold inputs.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at)
		VALUES('product-w','product-w','prototype','operator_only',2,'1970-01-01T00:00:01Z','1970-01-01T00:00:01Z');
		INSERT INTO projects(id,display_name,version,created_at,updated_at)
		VALUES('project-w','project-w',1,'1970-01-01T00:00:01Z','1970-01-01T00:00:01Z');
		INSERT INTO product_projects(product_id,project_id,role) VALUES('product-w','project-w','primary');
		INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,intent_json)
		VALUES('work-w','task','Routing migration','needed',1,2,'1970-01-01T00:00:01Z','1970-01-01T00:00:02Z',
		'{"title":"Routing migration","value_statement":"","kind":"task","priority":1,"urgency":"standard","tags":[],"component_id":"","workflow_type_ref":"","external_ref":""}');
		INSERT INTO work_projects(work_id,project_id,role) VALUES('work-w','project-w','primary');
	`); err != nil {
		t.Fatal(err)
	}
	for i, row := range []struct {
		attempt, model, role, lifecycle, failure string
	}{
		{"attempt-migrated-preferred", "openai/gpt-5.6-luna", "preferred", "dispatched", ""},
		{"attempt-migrated-undeclared", "anthropic/claude-unexpected", "undeclared", "failed", "model_identity"},
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
		// Both model columns hold the recorded routing value. Forward migration
		// must preserve this row without deriving it from the old event.
		if _, err := tx.ExecContext(ctx, `INSERT INTO worker_attempts
			(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,routing_policy_version,routing_policy_digest,resolved_model,resolution_role,fallback_reason,readback_model,packet_schema_version,report_schema_version,lifecycle_state,failure_kind,failure_detail,dispatched_at,completed_at,failed_at)
			VALUES('work-w',?,'research',1,?,'research',?,?,?,?,? ,?,'1.0','1.0',?,?,?, ?,NULL,?)`,
			row.attempt, historicalResearchLaneDigest, historicalRoutingPolicyToken, historicalRoutingPolicyDigest, row.model, row.role, "", row.model, row.lifecycle, row.failure, detail, at.Format(time.RFC3339Nano), failedAt); err != nil {
			t.Fatal(err)
		}
	}
	nativePayload := legacyNativeRunWriterPayload(t, "run-migrated", nil)
	native := legacyNativeRunEvent("native-migrated", nativePayload)
	appendEvent(native)
	var report map[string]any
	if err := json.Unmarshal(nativePayload, &report); err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(nativePayload, &envelope); err != nil {
		t.Fatal(err)
	}
	universe := jsonMember(envelope["observed_universe"])
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_native_runs
		(work_id,run_id,phase,status,event_id,reporting_authority_ref,actor_ref,native_subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,recorded_at,capture_method,observed_universe,freshness_policy_ref,divergence_policy_ref)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		native.SubjectID, report["run_id"], report["phase"], report["status"], native.EventID,
		report["reporting_authority_ref"], report["actor_ref"], report["native_subject_ref"], report["subject_digest"],
		report["evidence_ref"], report["evidence_digest"], report["asserted_at"], native.OccurredAt.Format(time.RFC3339Nano),
		report["capture_method"], universe, report["freshness_policy_ref"], report["divergence_policy_ref"]); err != nil {
		t.Fatal(err)
	}
	if err := bumpVersion(ctx, tx, "work_items", native, int64(report["expected_version"].(float64)), int64(report["resulting_version"].(float64)), "work item"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Seed the readback-era row at its released schema, after routing
	// columns were removed, rather than passing it through a current fold.
	migration := migrations[43]
	if err := applyMigration(ctx, db, migration); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-27T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for i, model := range []string{"", "openai/gpt-5.6-luna"} {
		attempt := []string{"attempt-migrated-digestless-empty", "attempt-migrated-digestless-model"}[i]
		at := time.Unix(int64(40+i), 0).UTC()
		members := map[string]any{"host_provenance": histDeclaredHostProvenance()}
		if model != "" {
			members["readback_model"] = model
		}
		appendEvent(histDispatchEvent(t, attempt+"-dispatch", attempt, 3, at, members))
		if _, err := tx.ExecContext(ctx, `INSERT INTO worker_attempts
			(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,failure_kind,failure_detail,dispatched_at,failed_at)
			VALUES('work-w',?,'research',1,?,'research',?,'1.0','1.0','dispatched','','',?,NULL)`,
			attempt, historicalResearchLaneDigest, model, at.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// All surviving columns, including NULL timestamps and empty failure
	// details, must retain the same bytes through migration.
	attemptBytes := func(attempt string) string {
		t.Helper()
		var row string
		if err := db.QueryRowContext(ctx, `SELECT json_array(work_id,attempt_id,lane_id,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,failure_kind,failure_detail,dispatched_at,completed_at,failed_at) FROM worker_attempts WHERE attempt_id=?`, attempt).Scan(&row); err != nil {
			t.Fatal(err)
		}
		return row
	}
	wantBytes := map[string]string{}
	for _, attempt := range []string{"attempt-migrated-preferred", "attempt-migrated-undeclared", "attempt-migrated-digestless-empty", "attempt-migrated-digestless-model"} {
		wantBytes[attempt] = attemptBytes(attempt)
	}
	log := histEventRows(t, s)
	// CD-0242 D6 retires worker rows at native Task cutover. Check their
	// historical readback before that boundary, not in the current store.
	advanceHistoricalPrefix(t, db, nativeTaskRetirementMigration(t).Version-1)
	assertUniverse := func(stage string) {
		t.Helper()
		var got string
		if err := db.QueryRowContext(ctx, `SELECT observed_universe FROM workflow_native_runs WHERE work_id='work-w' AND run_id='run-migrated' AND phase='start'`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != universe {
			t.Fatalf("%s changed native universe bytes: %q, want %q", stage, got, universe)
		}
	}
	assertUniverse("migration")
	for attempt, row := range wantBytes {
		if got := attemptBytes(attempt); got != row {
			t.Fatalf("migration changed %s row bytes: %s, want %s", attempt, got, row)
		}
	}
	assertHistoricalReceipts(t, db, log)
	if err := migrateRestoredStage(ctx, db); err != nil {
		t.Fatal(err)
	}
	assertUniverse("native Task cutover")
	assertHistoricalReceipts(t, db, log)
	assertEventLogAbsent(t, s)
}
