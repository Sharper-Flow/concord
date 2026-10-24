package store

import (
	"context"
	"database/sql"
)

// Authorization can precede an attempt row. Keep that current descriptor keyed
// by its attempt, rather than treating native dispatch as authorization.
func writeWorkerAuthorizationTx(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCompletedPayload) error {
	if p.ActionID == "dispatch_worker" && p.WorkerAttemptID != "" {
		_, err := tx.ExecContext(ctx, `INSERT INTO worker_authorizations(work_id,attempt_id,seq,payload) VALUES(?,?,?,?) ON CONFLICT(work_id,attempt_id) DO UPDATE SET seq=excluded.seq,payload=excluded.payload`, event.SubjectID, p.WorkerAttemptID, event.Seq, string(event.Payload))
		return err
	}
	if p.ActionID == "record_worker_failure" {
		if _, err := tx.ExecContext(ctx, `UPDATE worker_attempts SET failure_recorded=failure_recorded+1 WHERE work_id=? AND attempt_id=?`, event.SubjectID, p.WorkerAttemptID); err != nil {
			return err
		}
	}
	switch p.ActionID {
	case "accept_worker_result", "accept_worker_evidence", "reject_worker_result", "record_worker_failure":
		_, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET result_disposition=? WHERE work_id=?`, p.ActionID, event.SubjectID)
		return err
	}
	return nil
}

func writeWorkerDispatchFactsTx(ctx context.Context, tx *sql.Tx, event Event, attempt string) error {
	_, err := tx.ExecContext(ctx, `UPDATE worker_attempts SET dispatch_payload=?,dispatch_event_id=?,dispatch_actor=?,dispatch_seq=?,dispatch_payload_version=? WHERE work_id=? AND attempt_id=?`, string(event.Payload), event.EventID, event.Actor, event.Seq, event.PayloadVersion, event.SubjectID, attempt)
	return err
}

func writeWorkerTerminalFactsTx(ctx context.Context, tx *sql.Tx, event Event, attempt string) error {
	_, err := tx.ExecContext(ctx, `UPDATE worker_attempts SET terminal_payload=?,terminal_event_id=?,terminal_payload_version=?,terminal_kind=? WHERE work_id=? AND attempt_id=?`, string(event.Payload), event.EventID, event.PayloadVersion, event.Kind, event.SubjectID, attempt)
	return err
}

func workerAuthorizationActorTx(ctx context.Context, q queryer, work, attempt string) (string, error) {
	var actor string
	err := q.QueryRowContext(ctx, `SELECT json_extract(payload,'$.actor_ref') FROM worker_authorizations WHERE work_id=? AND attempt_id=?`, work, attempt).Scan(&actor)
	return actor, err
}

func workerRecordedFailureCountTx(ctx context.Context, q queryer, work, attempt string) (int64, error) {
	var count int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(failure_recorded),0) FROM worker_attempts WHERE work_id=? AND attempt_id=?`, work, attempt).Scan(&count)
	return count, err
}

func readWorkerDispatchFactTx(ctx context.Context, q queryer, work, attempt string) (Event, error) {
	event := Event{Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: work}
	err := q.QueryRowContext(ctx, `SELECT dispatch_event_id,dispatch_actor,dispatch_payload,dispatch_payload_version,dispatch_seq FROM worker_attempts WHERE work_id=? AND attempt_id=? AND dispatch_payload IS NOT NULL`, work, attempt).Scan(&event.EventID, &event.Actor, &event.Payload, &event.PayloadVersion, &event.Seq)
	return event, err
}

func workerDispatchPacketDigestTx(ctx context.Context, q queryer, work, attempt string) (string, error) {
	var digest string
	err := q.QueryRowContext(ctx, `SELECT json_extract(dispatch_payload,'$.packet_digest') FROM worker_attempts WHERE work_id=? AND attempt_id=? AND dispatch_payload IS NOT NULL`, work, attempt).Scan(&digest)
	return digest, err
}

func workerCoordinatorSessionTx(ctx context.Context, q queryer, work, attempt string) (string, error) {
	var session string
	err := q.QueryRowContext(ctx, `SELECT a.session_ref FROM worker_authorizations d JOIN workflow_actors a ON a.actor_ref=json_extract(d.payload,'$.actor_ref') WHERE d.work_id=? AND d.attempt_id=?`, work, attempt).Scan(&session)
	return session, err
}

func workerExecutorAtSequenceTx(ctx context.Context, q queryer, work, attempt string, seq int64) (string, error) {
	var executor string
	err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(dispatch_payload,'$.lane_actor_ref'),'') FROM worker_attempts WHERE work_id=? AND attempt_id=? AND dispatch_seq=? UNION ALL SELECT COALESCE(json_extract(payload,'$.lane_actor_ref'),'') FROM worker_authorizations WHERE work_id=? AND attempt_id=? AND seq=?`, work, attempt, seq, work, attempt, seq).Scan(&executor)
	return executor, err
}
