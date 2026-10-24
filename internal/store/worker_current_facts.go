package store

import (
	"context"
	"database/sql"
)

const workerCurrentFactsMigration = `
INSERT OR IGNORE INTO fold_guard(active) VALUES(1);
ALTER TABLE worker_job_revisions ADD COLUMN recorded_payload TEXT CHECK(recorded_payload IS NULL OR json_valid(recorded_payload));
ALTER TABLE worker_attempts ADD COLUMN dispatch_payload TEXT CHECK(dispatch_payload IS NULL OR json_valid(dispatch_payload));
ALTER TABLE worker_attempts ADD COLUMN dispatch_event_id TEXT;
ALTER TABLE worker_attempts ADD COLUMN dispatch_actor TEXT;
ALTER TABLE worker_attempts ADD COLUMN dispatch_payload_version INTEGER;
ALTER TABLE worker_attempts ADD COLUMN dispatch_seq INTEGER;
ALTER TABLE worker_attempts ADD COLUMN terminal_payload TEXT CHECK(terminal_payload IS NULL OR json_valid(terminal_payload));
ALTER TABLE worker_attempts ADD COLUMN terminal_event_id TEXT;
ALTER TABLE worker_attempts ADD COLUMN terminal_payload_version INTEGER;
ALTER TABLE worker_attempts ADD COLUMN terminal_kind TEXT;
ALTER TABLE worker_attempts ADD COLUMN failure_recorded INTEGER NOT NULL DEFAULT 0 CHECK(failure_recorded>=0);
ALTER TABLE workflow_instances ADD COLUMN result_disposition TEXT NOT NULL DEFAULT '';
CREATE TABLE worker_authorizations (
 work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
 attempt_id TEXT NOT NULL,
 seq INTEGER NOT NULL,
 payload TEXT NOT NULL CHECK(json_valid(payload)),
 PRIMARY KEY(work_id,attempt_id)
);
CREATE TABLE workflow_step_epochs (
 work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
 step_id TEXT NOT NULL,
 attempt_epoch INTEGER NOT NULL,
 PRIMARY KEY(work_id,step_id)
);
UPDATE worker_job_revisions AS j SET recorded_payload=(SELECT payload FROM domain_events e
 WHERE e.subject_type='work_item' AND e.subject_id=j.work_id AND e.kind='worker.job_recorded'
 AND json_extract(e.payload,'$.job_id')=j.job_id AND json_extract(e.payload,'$.revision')=j.revision
 ORDER BY e.seq DESC LIMIT 1);
UPDATE worker_attempts AS a SET (dispatch_payload,dispatch_event_id,dispatch_actor,dispatch_seq,dispatch_payload_version)=(SELECT payload,event_id,actor,seq,payload_version FROM domain_events e
 WHERE e.subject_type='work_item' AND e.subject_id=a.work_id AND e.kind='worker.dispatched'
 AND json_extract(e.payload,'$.attempt_id')=a.attempt_id ORDER BY e.seq DESC LIMIT 1);
UPDATE worker_attempts AS a SET (terminal_payload,terminal_event_id,terminal_payload_version,terminal_kind)=(SELECT payload,event_id,payload_version,kind FROM domain_events e
 WHERE e.subject_type='work_item' AND e.subject_id=a.work_id AND e.kind IN ('worker.completed','worker.failed')
 AND json_extract(e.payload,'$.attempt_id')=a.attempt_id ORDER BY e.seq DESC LIMIT 1);
UPDATE worker_attempts AS a SET failure_recorded=(SELECT count(*) FROM domain_events e
 WHERE e.subject_type='work_item' AND e.subject_id=a.work_id AND e.kind='workflow.action_completed'
 AND json_extract(e.payload,'$.action_id')='record_worker_failure' AND json_extract(e.payload,'$.worker_attempt_id')=a.attempt_id);
INSERT INTO worker_authorizations(work_id,attempt_id,seq,payload)
 SELECT subject_id,json_extract(payload,'$.worker_attempt_id'),seq,payload FROM domain_events e
 WHERE subject_type='work_item' AND kind='workflow.action_completed' AND json_extract(payload,'$.action_id')='dispatch_worker'
 AND EXISTS(SELECT 1 FROM work_items WHERE id=e.subject_id)
 AND json_extract(payload,'$.worker_attempt_id') IS NOT NULL
 AND json_extract(payload,'$.worker_attempt_id')<>''
 AND seq=(SELECT max(d.seq) FROM domain_events d WHERE d.subject_type=e.subject_type AND d.subject_id=e.subject_id AND d.kind=e.kind
 AND json_extract(d.payload,'$.action_id')='dispatch_worker' AND json_extract(d.payload,'$.worker_attempt_id')=json_extract(e.payload,'$.worker_attempt_id'));
INSERT INTO workflow_step_epochs(work_id,step_id,attempt_epoch)
 SELECT subject_id,json_extract(payload,'$.step_id'),json_extract(payload,'$.attempt_epoch') FROM domain_events e
 WHERE subject_type='work_item' AND kind='workflow.action_started'
 AND EXISTS(SELECT 1 FROM work_items WHERE id=e.subject_id)
 AND seq=(SELECT max(d.seq) FROM domain_events d WHERE d.subject_type=e.subject_type AND d.subject_id=e.subject_id AND d.kind=e.kind
 AND json_extract(d.payload,'$.step_id')=json_extract(e.payload,'$.step_id'));
UPDATE workflow_instances AS w SET result_disposition=COALESCE((SELECT json_extract(payload,'$.action_id') FROM domain_events e
 WHERE e.subject_type='work_item' AND e.subject_id=w.work_id AND e.kind='workflow.action_completed'
 AND json_extract(payload,'$.action_id') IN ('accept_worker_result','accept_worker_evidence','reject_worker_result','record_worker_failure') ORDER BY seq DESC LIMIT 1),'');
CREATE TRIGGER worker_authorizations_guard_insert BEFORE INSERT ON worker_authorizations BEGIN SELECT RAISE(ABORT, 'worker_authorizations is fold-only') WHERE NOT EXISTS(SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER worker_authorizations_guard_update BEFORE UPDATE ON worker_authorizations BEGIN SELECT RAISE(ABORT, 'worker_authorizations is fold-only') WHERE NOT EXISTS(SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER worker_authorizations_guard_delete BEFORE DELETE ON worker_authorizations BEGIN SELECT RAISE(ABORT, 'worker_authorizations is fold-only') WHERE NOT EXISTS(SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_step_epochs_guard_insert BEFORE INSERT ON workflow_step_epochs BEGIN SELECT RAISE(ABORT, 'workflow_step_epochs is fold-only') WHERE NOT EXISTS(SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_step_epochs_guard_update BEFORE UPDATE ON workflow_step_epochs BEGIN SELECT RAISE(ABORT, 'workflow_step_epochs is fold-only') WHERE NOT EXISTS(SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_step_epochs_guard_delete BEFORE DELETE ON workflow_step_epochs BEGIN SELECT RAISE(ABORT, 'workflow_step_epochs is fold-only') WHERE NOT EXISTS(SELECT 1 FROM fold_guard WHERE active=1); END;
DELETE FROM fold_guard;
`

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
