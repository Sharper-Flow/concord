package store

import (
	"strings"
	"testing"
)

func TestAdmissionEffectSequenceLogBlind(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "admission-work")
	var eventID string
	var want int64
	if err := s.db.QueryRow(`SELECT event_id,effect_seq FROM effect_receipts WHERE subject_type='work_item' AND subject_id='admission-work' ORDER BY effect_seq DESC LIMIT 1`).Scan(&eventID, &want); err != nil {
		t.Fatal(err)
	}
	hideEffectLog(t, s)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	got, err := eventSequenceTx(t.Context(), tx, eventID)
	if err != nil || got != want {
		t.Fatalf("effect sequence without the log = %d, %v; want %d", got, err, want)
	}
	_, err = eventSequenceTx(t.Context(), tx, "missing-effect")
	assertFailureKind(t, err, KindUnavailable)
}

func TestAdmissionUnsettledWorkerLogBlind(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "admission-work")
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1);
INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at,completed_at)
VALUES('admission-work','attempt','lane',1,?,'implementation','model','1.0','1.0','completed','now','now');
ALTER TABLE domain_events RENAME TO admission_hidden_log;`, "sha256:"+strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"completed", "failed", "dispatched"} {
		if _, err := tx.Exec(`UPDATE worker_attempts SET lifecycle_state=?,
completed_at=CASE WHEN ?='completed' THEN 'now' END,
failed_at=CASE WHEN ?='failed' THEN 'now' END,
failure_kind=CASE WHEN ?='failed' THEN 'abandoned' ELSE '' END`, state, state, state, state); err != nil {
			t.Fatal(err)
		}
		for _, action := range []string{"accept_worker_result", "accept_worker_evidence", "reject_worker_result", "record_worker_failure", "record_worker_job"} {
			if _, err := tx.Exec(`DELETE FROM worker_attempt_dispositions`); err != nil {
				t.Fatal(err)
			}
			unsettled, err := workflowUnsettledWorkerAttempt(t.Context(), tx, "admission-work", "test", 0)
			if err != nil || !unsettled {
				t.Fatalf("undisposed %s attempt before %s = %t, %v", state, action, unsettled, err)
			}
			payload := []byte(`{"action_id":"` + action + `","worker_attempt_id":"attempt"}`)
			if err := writeWorkerAuthorizationTx(t.Context(), tx, Event{SubjectID: "admission-work", Seq: 10, EventID: action, Payload: payload}, workflowActionCompletedPayload{ActionID: action, WorkerAttemptID: "attempt"}); err != nil {
				t.Fatal(err)
			}
			for _, before := range []int64{0, 10, 11} {
				want := before == 10 || state == "dispatched" || state == "failed" && action != "record_worker_failure"
				unsettled, err = workflowUnsettledWorkerAttempt(t.Context(), tx, "admission-work", "test", before)
				if err != nil || unsettled != want {
					t.Fatalf("%s attempt after %s before %d = %t, %v; want %t", state, action, before, unsettled, err, want)
				}
			}
		}
	}
}
