package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWorkflowContentIdentityMigration152PreservesRetainedState(t *testing.T) {
	const work = "content-identity-migration"
	f := oSeedReviewRoute(t, work, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	ctx := context.Background()
	job := &WorkerJobBinding{JobID: "job:content-identity", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, work, f.owner, job)
	dispatchJobBoundAttempt(t, s, work, "repair", "attempt:content-completed", nil, f.owner, 0, "content-dispatch", job)
	completeAndAcceptAttempt(t, s, work, "attempt:content-completed", reviewGateLane(t, "implementation"), reviewGateAcceptor(work))
	failed := &WorkerJobBinding{JobID: "job:content-failed", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, work, f.owner, failed)
	oDispatchJobBoundAttempt(t, s, work, "attempt:content-failed", nil, f.owner, 0, "content-failed-dispatch", failed)
	failure := s2WorkerFailedAttributedEvent(work, "attempt:content-failed", "content-failed-terminal", preferredModelForLane(reviewGateLane(t, "implementation")), WorkerFailureWorkerError, "the bounded worker failed", time.Unix(31, 0).UTC())
	if err := s2WorkerFailedLive(t, s, failure); err != nil {
		t.Fatal(err)
	}
	version, err := continuityAction(t, s, work, readWorkVersion(t, s, work), "checkpoint_context", "content-checkpoint", map[string]any{
		"active_unit": "unit:migration", "hypothesis": "retained bytes survive", "diagnosis": "identity counters retire", "strategy": "compare every retained column",
		"touched_refs": []string{"internal/store/schema.go"}, "evidence_refs": []string{"evidence:migration"}, "pending_questions": []string{"question:retention"}, "pending_decisions": []string{},
	}, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := continuityAction(t, s, work, version, "cross_context_boundary", "content-boundary", map[string]any{
		"boundary_kind": "summary", "mode": "summary", "checkpoint_id": "content-checkpoint:context-checkpoint", "summary": "Resume from the retained checkpoint and evidence.",
	}, f.owner); err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return recordRuntimeStateWriter(ctx, tx.tx, knowledgeStateWriter(7))
	}); err != nil {
		t.Fatal(err)
	}

	// A pending claim carries real result/evidence bytes before the old schema
	// loses its unrecorded definition digest. Migration must not invent one.
	entry := mustBuiltinDefinition(t, "workflow.break_fix")
	claim := compactionClaimRequest(work)
	claim.OpID, claim.IdempotencyKey, claim.RequestID = "content-claim", "content-claim", "request:content-claim"
	claim.WorkflowTypeRef, claim.WorkflowTypeDigest, claim.StepID = entry.Definition.Ref, entry.Digest, "repair"
	claimed, err := ClaimStepAuthorized(ctx, s, claim, nil)
	if err != nil {
		t.Fatal(err)
	}
	complete := CompleteRequest{
		OpID: claim.OpID, AttemptEpoch: claimed.AttemptEpoch, ResultKind: ResultPending,
		ResultPayload: "{\n  \"retained\": true, \"workflow_definition_version\": 26\n}", EvidenceRefs: []string{"evidence:migration"}, ChangedRefs: []string{"ref:migration"}, ResumeCursor: "cursor:migration",
		PrincipalRef: claim.PrincipalRef, Tool: claim.Tool, IdempotencyKey: "content-pending", RequestID: "request:content-pending", ObservedAt: claim.ObservedAt,
	}
	if _, err := CompleteStep(ctx, s, complete); err != nil {
		t.Fatal(err)
	}
	if _, err := Step(ctx, s, claim.OpID); err != nil {
		t.Fatalf("exact-digest claim before migration: %v", err)
	}
	wantSchema := contentIdentity152Schema(t, s.db)

	// The current route supplies a complete data oracle. The historical store
	// has the actual immutable 151 schema, not a reconstructed current schema.
	path := filepath.Join(t.TempDir(), "content-identity-151.db")
	db := openMigratedTo(t, path, 151)
	rows, err := s.db.Query(`SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT IN ('schema_migrations','write_guard') ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	oldValues := map[string]map[string]string{
		"durable_operations":           {"workflow_type_version": "26"},
		"workflow_contracts":           {"definition_version": "26"},
		"workflow_instances":           {"definition_version": "26"},
		"workflow_context_checkpoints": {"workflow_definition_version": "26"},
		"workflow_context_boundaries":  {"workflow_definition_version": "26"},
		"worker_attempts": {
			"lane_version":     "19",
			"dispatch_payload": `'{ "lane_version": 19, '||substr(dispatch_payload,2)`,
			"terminal_payload": `'{ "workflow_definition_version": 26, '||substr(terminal_payload,2)`,
		},
		"runtime_state_writers": {"version": "CASE WHEN surface='knowledge' THEN knowledge_projection_version ELSE 26 END"},
		// Ordinary dispatch admission lives on the source group owner. Jobs
		// without that admission retain the historical empty defaults.
		"worker_job_revisions": {
			"group_permission": `COALESCE((SELECT g.permission FROM fixture_source.workflow_member_groups g WHERE g.work_id=fixture_source.worker_job_revisions.work_id AND g.group_id=fixture_source.worker_job_revisions.job_id),'')`,
			"group_review_ref": `COALESCE((SELECT g.review_ref FROM fixture_source.workflow_member_groups g WHERE g.work_id=fixture_source.worker_job_revisions.work_id AND g.group_id=fixture_source.worker_job_revisions.job_id),'')`,
		},
	}
	// Historical payloads include retired keys and whitespace. Their receipts
	// must bind those exact inputs at INSERT, without updating immutable receipts.
	rows, err = s.db.Query(`SELECT dispatch_event_id,'{ "lane_version": 19, '||substr(dispatch_payload,2) FROM worker_attempts WHERE dispatch_event_id IS NOT NULL
UNION ALL SELECT terminal_event_id,'{ "workflow_definition_version": 26, '||substr(terminal_payload,2) FROM worker_attempts WHERE terminal_event_id IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	digestCase := "CASE event_id"
	for rows.Next() {
		var id, payload string
		if err := rows.Scan(&id, &payload); err != nil {
			t.Fatal(err)
		}
		digestCase += " WHEN '" + strings.ReplaceAll(id, "'", "''") + "' THEN '" + historicalFixturePayloadDigest(t, payload) + "'"
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	oldValues["effect_receipts"] = map[string]string{"payload_digest": digestCase + " ELSE payload_digest END"}
	copyFixtureDataToPrefix(t, s, db, tables, oldValues, "write_guard")
	s = &Store{db: db, path: path, Clock: s.Clock}
	want := contentIdentity152Snapshot(t, s.db, true)
	for table, rows := range want {
		if len(rows) == 0 {
			t.Fatalf("preservation fixture left %s empty", table)
		}
	}
	var witnessed int
	if err := s.db.QueryRow(`SELECT count(*) FROM worker_attempts WHERE dispatch_seq>0 AND terminal_seq>dispatch_seq AND (lifecycle_state='completed' OR (failure_recorded>0 AND failure_recorded_seq>terminal_seq))`).Scan(&witnessed); err != nil || witnessed < 2 {
		t.Fatalf("terminal/failure coordinate witnesses=%d, err=%v", witnessed, err)
	}
	if err := Migrate(ctx, s.db); err != nil {
		t.Fatal(err)
	}
	if got := contentIdentity152Schema(t, s.db); !reflect.DeepEqual(got, wantSchema) {
		t.Errorf("migration 152 did not restore current constraints, indexes, and triggers\ngot %q\nwant %q", got, wantSchema)
	}
	for table, rows := range contentIdentity152Snapshot(t, s.db, false) {
		if !reflect.DeepEqual(rows, want[table]) {
			t.Errorf("migration 152 changed retained %s rows\ngot %q\nwant %q", table, rows, want[table])
		}
	}
	contentIdentity152AssertSchema(t, s.db)
	var digest string
	if err := s.db.QueryRow(`SELECT workflow_type_digest FROM durable_operations WHERE op_id=?`, claim.OpID).Scan(&digest); err != nil || digest != "" {
		t.Fatalf("pre152 claim acquired digest=%q (current=%q), err=%v", digest, entry.Digest, err)
	}
	beforeRefusal := contentIdentity152Snapshot(t, s.db, false)
	for _, route := range []string{"resume", "complete"} {
		t.Run(route+" refuses unknown claim identity", func(t *testing.T) {
			var err error
			if route == "resume" {
				_, err = Step(ctx, s, claim.OpID)
			} else {
				request := complete
				request.ResultKind, request.IdempotencyKey = ResultCompleted, "content-complete-after-migration"
				_, err = CompleteStep(ctx, s, request)
			}
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != KindInvariantViolation || failure.RecoveryAction != "reread_entities" || !strings.Contains(failure.Detail, "definition") {
				t.Fatalf("%s unknown pre152 identity refusal=%v, want typed definition revalidation", route, err)
			}
			if got := contentIdentity152Snapshot(t, s.db, false); !reflect.DeepEqual(got, beforeRefusal) {
				t.Fatalf("%s refusal changed original operations or evidence", route)
			}
		})
	}
	claim.OpID, claim.IdempotencyKey, claim.RequestID = "content-current-claim", "content-current-claim", "request:content-current-claim"
	current, err := ClaimStepAuthorized(ctx, s, claim, nil)
	if err != nil {
		t.Fatalf("current exact-digest claim refused: %v", err)
	}
	complete.OpID, complete.AttemptEpoch, complete.ResultKind = claim.OpID, current.AttemptEpoch, ResultCompleted
	complete.IdempotencyKey, complete.RequestID = "content-current-complete", "request:content-current-complete"
	if _, err := CompleteStep(ctx, s, complete); err != nil {
		t.Fatalf("current exact-digest completion refused: %v", err)
	}
	if result, err := Step(ctx, s, claim.OpID); err != nil || result.ResultKind != ResultCompleted || !reflect.DeepEqual(result.EvidenceRefs, complete.EvidenceRefs) {
		t.Fatalf("current exact-digest resume=%+v, err=%v", result, err)
	}
	t.Run("workflow writer keyed by ref and digest", func(t *testing.T) {
		// A repeat updates the same identity; independent content and another
		// ref remain distinct without an authored version allocation.
		otherDigest := "sha256:" + strings.Repeat("0", 64)
		if err := s.Transact(ctx, func(tx *Transaction) error {
			for _, pin := range []WorkflowDefinitionPin{
				{Ref: entry.Definition.Ref, Digest: entry.Digest},
				{Ref: entry.Definition.Ref, Digest: entry.Digest},
				{Ref: entry.Definition.Ref, Digest: otherDigest},
				{Ref: "workflow.other", Digest: entry.Digest},
			} {
				if err := recordRuntimeStateWriter(ctx, tx.tx, workflowStateWriter(pin)); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := s.db.QueryRow(`SELECT count(*) FROM runtime_state_writers WHERE surface='workflow' AND knowledge_projection_version=0 AND ((definition_ref=? AND digest IN (?,?)) OR (definition_ref='workflow.other' AND digest=?))`, entry.Definition.Ref, entry.Digest, otherDigest, entry.Digest).Scan(&count); err != nil || count != 3 {
			t.Fatalf("ref+digest writer identities=%d, err=%v", count, err)
		}
	})
}

func contentIdentity152Schema(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT json_array(type,name,tbl_name,sql) FROM sqlite_schema
WHERE tbl_name IN ('worker_attempts','workflow_instances','workflow_context_checkpoints','workflow_context_boundaries','runtime_state_writers') ORDER BY type,name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// Compare all surviving columns, not a hand-picked subset. JSON arrays retain
// SQL NULL versus empty text and the exact payload strings without decoding or
// removing historical keys. The only normalized value is the workflow writer
// counter, which becomes zero; knowledge's format version remains unchanged.
// Claims gain an empty digest, never an inferred definition identity.
func contentIdentity152Snapshot(t *testing.T, db *sql.DB, before bool) map[string][]string {
	t.Helper()
	retired := map[string]string{
		"worker_attempts": "lane_version", "workflow_instances": "definition_version", "workflow_contracts": "definition_version",
		"workflow_context_checkpoints": "workflow_definition_version", "workflow_context_boundaries": "workflow_definition_version",
		"durable_operations": "workflow_type_version",
	}
	tables := []string{"worker_attempts", "workflow_instances", "workflow_contracts", "workflow_context_checkpoints", "workflow_context_boundaries", "runtime_state_writers", "durable_operations", "worker_authorizations", "workflow_evidence_bindings", "worker_attempt_dispositions", "effect_receipts", "idempotency_records"}
	out := make(map[string][]string, len(tables))
	for _, table := range tables {
		rows, err := db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY name`, table)
		if err != nil {
			t.Fatal(err)
		}
		var columns []string
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				t.Fatal(err)
			}
			if column == retired[table] {
				continue
			}
			if table == "runtime_state_writers" && before && column == "version" {
				column = "knowledge_projection_version"
			}
			columns = append(columns, column)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		if table == "durable_operations" && before {
			columns = append(columns, "workflow_type_digest")
		}
		slices.Sort(columns)
		for i, column := range columns {
			switch {
			case table == "runtime_state_writers" && before && column == "knowledge_projection_version":
				columns[i] = `CASE WHEN surface='knowledge' THEN version ELSE 0 END`
			case table == "durable_operations" && before && column == "workflow_type_digest":
				columns[i] = `''`
			default:
				columns[i] = `"` + column + `"`
			}
		}
		rows, err = db.Query(`SELECT json_array(` + strings.Join(columns, ",") + `) FROM "` + table + `" ORDER BY 1`)
		if err != nil {
			t.Fatalf("snapshot %s: %v", table, err)
		}
		out[table] = []string{}
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				t.Fatal(err)
			}
			out[table] = append(out[table], row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func contentIdentity152AssertSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	for table, column := range map[string]string{
		"worker_attempts": "lane_version", "workflow_instances": "definition_version", "workflow_contracts": "definition_version",
		"workflow_context_checkpoints": "workflow_definition_version", "workflow_context_boundaries": "workflow_definition_version",
		"runtime_state_writers": "version", "durable_operations": "workflow_type_version",
	} {
		present, err := columnPresent(t.Context(), db, table, column)
		if err != nil || present {
			t.Errorf("retired counter %s.%s present=%v, err=%v", table, column, present, err)
		}
	}
	for table, index := range map[string]string{
		"worker_attempts": "worker_attempts_work", "workflow_instances": "workflow_instances_state",
		"workflow_context_checkpoints": "workflow_context_checkpoints_latest", "workflow_context_boundaries": "workflow_context_boundaries_history",
	} {
		for _, object := range []string{index, table + "_guard_insert", table + "_guard_update", table + "_guard_delete"} {
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=? AND tbl_name=?`, object, table).Scan(&count); err != nil || count != 1 {
				t.Errorf("missing schema protection %s: count=%d, err=%v", object, count, err)
			}
		}
		for _, statement := range []string{`UPDATE "` + table + `" SET work_id=work_id`, `DELETE FROM "` + table + `"`} {
			if _, err := db.Exec(statement); err == nil || (!strings.Contains(err.Error(), "write-seam-only") && !strings.Contains(err.Error(), "immutable")) {
				t.Fatalf("unguarded %s did not refuse: %v", statement, err)
			}
		}
	}
	for _, statement := range []string{
		`UPDATE worker_attempts SET lifecycle_state='completed',completed_at=NULL`,
		`UPDATE worker_attempts SET lane_digest='unknown'`,
		`UPDATE worker_attempts SET terminal_payload='invalid json'`,
		`UPDATE workflow_instances SET definition_digest='unknown'`,
		`INSERT INTO runtime_state_writers VALUES('workflow','workflow.invalid',1,'digest','binary')`,
		`INSERT INTO runtime_state_writers VALUES('knowledge','',0,'','binary')`,
	} {
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := enterWrite(t.Context(), tx); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(statement)
		_ = tx.Rollback()
		if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
			t.Errorf("current constraint lost for %s: %v", statement, err)
		}
	}
	var violations int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign key violations=%d, err=%v", violations, err)
	}
}
