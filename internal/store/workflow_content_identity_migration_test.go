package store

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

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
