package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWorkflowContentIdentityMigration152PreservesRetainedState(t *testing.T) {
	useStampedBuild(t)
	const work = "work-upgrade-from"
	db, path := retirementUpgradeFixture(t)
	advanceHistoricalPrefix(t, db, 151)
	digest := "sha256:" + strings.Repeat("a", 64)
	actor := "actor:" + strings.Repeat("a", 64)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state,execution_actor_ref,started_at) VALUES(?,'workflow.break_fix',26,?,'repair','running',?,?)`, work, digest, actor, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_context_checkpoints(work_id,work_version,checkpoint_sequence,checkpoint_id,step_id,attempt_epoch,active_unit,hypothesis,diagnosis,strategy,touched_refs,evidence_refs,pending_questions,pending_decisions,workflow_ref,workflow_definition_version,workflow_definition_digest,actor_ref,request_id,recorded_at) VALUES(?,1,1,'checkpoint:historical','repair',1,'unit:migration','retained bytes survive','identity counters retire','compare retained columns','["ref:migration"]','["evidence:migration"]','["question:retention"]','[]','workflow.break_fix',26,?,?,'request:checkpoint',?)`, work, digest, actor, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_context_boundaries(work_id,work_version,boundary_sequence,boundary_count,boundary_id,boundary_kind,checkpoint_id,checkpoint_sequence,attempt_epoch,summary,workflow_ref,workflow_definition_version,workflow_definition_digest,actor_ref,request_id,recorded_at) VALUES(?,1,1,1,'boundary:historical','summary','checkpoint:historical',1,1,'Resume from retained evidence','workflow.break_fix',26,?,?,'request:boundary',?)`, work, digest, actor, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO runtime_state_writers(surface,definition_ref,version,digest,binary_version) VALUES('knowledge','',7,'','historical'),('workflow','workflow.break_fix',26,?,'historical')`, digest)
	retirementUpgradeExec(t, db, `INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_version,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,principal_ref,request_id,observed_at,contract_digest,result_kind,result_payload,evidence_refs,changed_refs) VALUES('content-result',1,?,'workflow.break_fix',26,'repair','internal_sqlite',?,'{}','operator','request:content-result',?,?,'completed','{"retained":true,"workflow_definition_version":26}','["evidence:migration"]','["ref:migration"]')`, work, digest, retirementUpgradeTime, testManifestDigest)
	retirementUpgradeExec(t, db, `INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,first_observed_at,last_observed_at) VALUES('operator','transition','workflow_action','content-result',?,'content-result',?,?)`, digest, retirementUpgradeTime, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO project_locators(project_id,locator_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('project-upgrade','historical-home','canonical_path','/synthetic/history','/synthetic/history',?,?)`, retirementUpgradeTime, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO knowledge_index_watermark(home_project_id,home_locator_id,head_ref,scanned_commit_oid,scanned_at,complete) VALUES('project-upgrade','historical-home','HEAD',?,?,1)`, strings.Repeat("a", 40), retirementUpgradeTime)
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	s := &Store{db: db, path: path}
	wantSchema := contentIdentity152Schema(t, openMigratedTo(t, filepath.Join(t.TempDir(), "empty-152.db"), 152))

	historical := contentIdentity151WorkerFixture(t, s.db, work)
	assertHistoricalReceipts(t, s.db, historical)
	want := contentIdentity152Snapshot(t, s.db, true)
	for _, table := range []string{"worker_attempts", "workflow_instances", "workflow_contracts", "workflow_context_checkpoints", "workflow_context_boundaries", "runtime_state_writers", "durable_operations", "worker_authorizations", "worker_attempt_dispositions", "workflow_correction_state", "effect_receipts", "idempotency_records", "project_locators", "knowledge_index_watermark"} {
		if len(want[table]) == 0 {
			t.Fatalf("preservation fixture left %s empty", table)
		}
	}
	var witnessed int
	if err := s.db.QueryRow(`SELECT count(*) FROM worker_attempts WHERE dispatch_seq>0 AND terminal_seq>dispatch_seq AND (lifecycle_state='completed' OR (failure_recorded>0 AND failure_recorded_seq>terminal_seq))`).Scan(&witnessed); err != nil || witnessed < 2 {
		t.Fatalf("terminal/failure coordinate witnesses=%d, err=%v", witnessed, err)
	}
	advanceHistoricalPrefix(t, s.db, 152)
	assertHistoricalReceipts(t, s.db, historical)
	var version152, guards int
	if err := s.db.QueryRow(`SELECT max(version),(SELECT count(*) FROM write_guard) FROM schema_migrations`).Scan(&version152, &guards); err != nil || version152 != 152 || guards != 0 {
		t.Fatalf("migration boundary=%d write guards=%d, err=%v", version152, guards, err)
	}
	if got := contentIdentity152Schema(t, s.db); !reflect.DeepEqual(got, wantSchema) {
		t.Errorf("migration 152 did not restore its constraints, indexes, and triggers\ngot %q\nwant %q", got, wantSchema)
	}
	for table, rows := range contentIdentity152Snapshot(t, s.db, false) {
		if !reflect.DeepEqual(rows, want[table]) {
			t.Errorf("migration 152 changed retained %s rows\ngot %q\nwant %q", table, rows, want[table])
		}
	}
	contentIdentity152AssertSchema(t, s.db)
	var claimDigest string
	if err := s.db.QueryRow(`SELECT workflow_type_digest FROM durable_operations WHERE op_id='content-result'`).Scan(&claimDigest); err != nil || claimDigest != "" {
		t.Fatalf("pre152 result acquired digest=%q, err=%v", claimDigest, err)
	}
	advanceHistoricalPrefix(t, db, 182)
	queries := map[string]string{
		"effect_receipts":     `SELECT * FROM effect_receipts ORDER BY effect_seq`,
		"durable_operations":  `SELECT * FROM durable_operations ORDER BY op_id`,
		"idempotency_records": `SELECT * FROM idempotency_records ORDER BY idempotency_key`,
		"checkpoints":         `SELECT work_id,work_version,checkpoint_sequence,checkpoint_id,step_id,attempt_epoch,active_unit,hypothesis,diagnosis,strategy,touched_refs,evidence_refs,pending_questions,pending_decisions,workflow_ref,actor_ref,request_id,recorded_at FROM workflow_context_checkpoints ORDER BY work_id,checkpoint_sequence`,
		"boundaries":          `SELECT work_id,work_version,boundary_sequence,boundary_count,boundary_id,boundary_kind,checkpoint_id,checkpoint_sequence,attempt_epoch,summary,workflow_ref,actor_ref,request_id,recorded_at FROM workflow_context_boundaries ORDER BY work_id,boundary_sequence`,
		"knowledge writers":   `SELECT * FROM runtime_state_writers WHERE surface='knowledge'`,
	}
	before := map[string][][]any{}
	for name, query := range queries {
		before[name] = retirementUpgradeRows(t, db, query)
	}
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatal(err)
	}
	for name, query := range queries {
		if got := retirementUpgradeRows(t, db, query); !reflect.DeepEqual(got, before[name]) {
			t.Errorf("full upgrade changed %s: before=%v after=%v", name, before[name], got)
		}
	}
	assertHistoricalReceipts(t, db, historical)
	retirementUpgradeHealthy(t, db)
}

// These rows are migration inputs, never current dispatches or reports. Keep
// their historical payload strings intact, including retired keys and spacing.
func contentIdentity151WorkerFixture(t *testing.T, db *sql.DB, work string) []histEventRow {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	var seq int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(effect_seq),0) FROM effect_receipts`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(31, 0).UTC().Format(time.RFC3339Nano)
	digest := "sha256:" + strings.Repeat("a", 64)
	var events []histEventRow
	for _, state := range []string{"completed", "failed"} {
		attempt := "attempt:content-" + state
		authorization := `{ "action_id": "dispatch_worker", "worker_attempt_id": "` + attempt + `", "step_id": "repair", "attempt_epoch": 1 }`
		dispatch := `{ "lane_version": 19, "attempt_id": "` + attempt + `", "lane_id": "implementation", "retained": true }`
		terminal := "{\n  \"workflow_definition_version\": 26, \"attempt_id\": \"" + attempt + "\", \"retained\": true\n}"
		action := "accept_worker_result"
		var completedAt, failedAt any = stamp, nil
		failureKind, failureDetail := "", ""
		failureRecorded := 0
		var failureSeq int64
		firstAcceptance, firstPayload := seq+4, ""
		if state == "failed" {
			action = "record_worker_failure"
			completedAt, failedAt = nil, stamp
			failureKind, failureDetail = "worker_error", "the bounded worker failed"
			failureRecorded, failureSeq = 1, seq+4
			firstAcceptance, firstPayload = 0, "{}"
		}
		disposition := `{ "action_id": "` + action + `", "worker_attempt_id": "` + attempt + `", "evidence_refs": ["evidence:migration"] }`
		if state == "completed" {
			firstPayload = disposition
		}
		batch := []histEventRow{
			{Seq: seq + 1, EventID: "content-" + state + "-authorization", Kind: "workflow.action_completed", Payload: authorization},
			{Seq: seq + 2, EventID: "content-" + state + "-dispatch", Kind: "worker.dispatched", Payload: dispatch},
			{Seq: seq + 3, EventID: "content-" + state + "-terminal", Kind: "worker." + state, Payload: terminal},
			{Seq: seq + 4, EventID: "content-" + state + "-disposition", Kind: "workflow.action_completed", Payload: disposition},
		}
		for i := range batch {
			e := &batch[i]
			e.SubjectType, e.SubjectID, e.Actor, e.OccurredAt, e.Version = "work_item", work, "actor:historical-content", stamp, 1
			if _, err := tx.Exec(`INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest) VALUES(?,?,?,?,?,?,?,?)`, e.EventID, e.Seq, e.Kind, e.SubjectType, e.SubjectID, e.Actor, e.Version, historicalFixturePayloadDigest(t, e.Payload)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tx.Exec(`INSERT INTO worker_attempts
(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,failure_kind,failure_detail,dispatched_at,completed_at,failed_at,dispatch_payload,dispatch_event_id,dispatch_actor,dispatch_payload_version,dispatch_seq,terminal_payload,terminal_event_id,terminal_payload_version,terminal_kind,failure_recorded,terminal_seq,terminal_occurred_at,failure_recorded_seq)
VALUES(?,?,'implementation',19,?,'implementation','model:historical','2.0','2.0',?,?,?,?,?,?,?,?,?,1,?,?,?,1,?,?,?,?,?)`,
			work, attempt, digest, state, failureKind, failureDetail, stamp, completedAt, failedAt,
			dispatch, batch[1].EventID, batch[1].Actor, batch[1].Seq, terminal, batch[2].EventID, batch[2].Kind, failureRecorded, batch[2].Seq, stamp, failureSeq); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO worker_authorizations(work_id,attempt_id,seq,payload,event_id,contract_version) VALUES(?,?,?,?,?,1)`, work, attempt, batch[0].Seq, authorization, batch[0].EventID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO worker_attempt_dispositions(work_id,attempt_id,action_id,seq,event_id,payload,first_acceptance_seq,first_acceptance_payload) VALUES(?,?,?,?,?,?,?,?)`, work, attempt, action, batch[3].Seq, batch[3].EventID, disposition, firstAcceptance, firstPayload); err != nil {
			t.Fatal(err)
		}
		if state == "failed" {
			correction := workflowJSON(map[string]any{"boundary": 0, "records": []any{map[string]any{"seq": batch[3].Seq, "source_id": batch[3].EventID, "fields": map[string]any{"action_id": action, "worker_attempt_id": attempt, "diagnosis": "the bounded worker failed", "strategy": "retain the failure and evidence"}}}})
			if _, err := tx.Exec(`INSERT INTO workflow_correction_state(work_id,state) VALUES(?,?) ON CONFLICT(work_id) DO UPDATE SET state=excluded.state`, work, correction); err != nil {
				t.Fatal(err)
			}
		}
		events = append(events, batch...)
		seq += 4
	}
	if _, err := tx.Exec(`UPDATE store_sequences SET last=? WHERE name='effect'`, seq); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE work_items SET effect_seq=? WHERE id=?`, seq, work); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE workflow_instances SET result_disposition='record_worker_failure' WHERE work_id=?`, work); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return events
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
	tables := []string{"worker_attempts", "workflow_instances", "workflow_contracts", "workflow_context_checkpoints", "workflow_context_boundaries", "runtime_state_writers", "durable_operations", "worker_authorizations", "workflow_evidence_bindings", "worker_attempt_dispositions", "workflow_correction_state", "effect_receipts", "idempotency_records", "project_locators", "knowledge_index_watermark"}
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
			t.Errorf("migration 152 constraint lost for %s: %v", statement, err)
		}
	}
	var violations int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign key violations=%d, err=%v", violations, err)
	}
}
