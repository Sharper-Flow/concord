package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// recordedPayloadVersion is the version the event was written with, before
// any upcaster moved it.
func recordedPayloadVersion(event Event) int {
	if event.replaySourcePayloadVersion != 0 {
		return event.replaySourcePayloadVersion
	}
	return event.PayloadVersion
}

// nextSessionRelocationStampSQL allocates the session's next vacate stamp.
// The latest vacate event of a session is always retained, as the latest
// request of its key or the latest landing of its work item, so the maximum
// stored stamp is the count of the session's vacate events so far.
const nextSessionRelocationStampSQL = `(SELECT COALESCE(max(m),0)+1 FROM (
	SELECT max(stamp) AS m FROM session_vacate_requests WHERE session_ref=?
	UNION ALL SELECT max(landing_stamp) FROM session_relocations WHERE session_ref=?))`

func sessionRelocationWriteFailure(err error) error {
	return wrapFailure(KindUnavailable, "fold_event", "cannot record the session relocation facts", true, "retry once the database is writable", err)
}

// writeSessionVacateRequestTx makes the event the latest request of its work
// item, session, Project, and source directory, and counts it.
func writeSessionVacateRequestTx(ctx context.Context, tx *sql.Tx, event Event, p sessionVacatedPayload) error {
	var destination any
	if raw, ok := payloadMember(event.Payload, "destination_directory"); ok {
		destination = raw
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_vacate_requests(work_id,session_ref,project_id,source_directory,destination_directory,payload_version,stamp)
		VALUES(?,?,?,?,?,?,`+nextSessionRelocationStampSQL+`)
		ON CONFLICT(work_id,session_ref,project_id,source_directory) DO UPDATE SET destination_directory=excluded.destination_directory,payload_version=excluded.payload_version,stamp=excluded.stamp`,
		event.SubjectID, p.SessionRef, p.ProjectID, p.SourceDirectory, destination, recordedPayloadVersion(event), p.SessionRef, p.SessionRef); err != nil {
		return sessionRelocationWriteFailure(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_relocations(work_id,session_ref,request_count) VALUES(?,?,1)
		ON CONFLICT(work_id,session_ref) DO UPDATE SET request_count=request_count+1`, event.SubjectID, p.SessionRef); err != nil {
		return sessionRelocationWriteFailure(err)
	}
	return nil
}

// writeSessionVacateLandingTx counts a verified vacate landing and stamps it,
// so every earlier request of the work item and session reads as landed.
func writeSessionVacateLandingTx(ctx context.Context, tx *sql.Tx, event Event, sessionRef string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_relocations(work_id,session_ref,landing_count,landing_stamp) VALUES(?,?,1,`+nextSessionRelocationStampSQL+`)
		ON CONFLICT(work_id,session_ref) DO UPDATE SET landing_count=landing_count+1,landing_stamp=excluded.landing_stamp`,
		event.SubjectID, sessionRef, sessionRef, sessionRef); err != nil {
		return sessionRelocationWriteFailure(err)
	}
	return nil
}

// writeSessionClaimLandingTx counts a claim landing for its work item and
// session and, when it names a claim generation, for that generation.
func writeSessionClaimLandingTx(ctx context.Context, tx *sql.Tx, event Event) error {
	sessionRef, _ := payloadMember(event.Payload, "session_ref")
	projectID, _ := payloadMember(event.Payload, "project_id")
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_relocations(work_id,session_ref,claim_landing_count) VALUES(?,?,1)
		ON CONFLICT(work_id,session_ref) DO UPDATE SET claim_landing_count=claim_landing_count+1`, event.SubjectID, sessionRef); err != nil {
		return sessionRelocationWriteFailure(err)
	}
	claimOpID, ok := payloadMember(event.Payload, "claim_op_id")
	if !ok {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_claim_landings(work_id,session_ref,project_id,claim_op_id,landing_count) VALUES(?,?,?,?,1)
		ON CONFLICT(work_id,session_ref,project_id,claim_op_id) DO UPDATE SET landing_count=landing_count+1`,
		event.SubjectID, sessionRef, projectID, claimOpID); err != nil {
		return sessionRelocationWriteFailure(err)
	}
	return nil
}

// payloadMember reads one string member the payload records. A member the
// payload omits or records as null reports false, matching json_extract.
func payloadMember(payload []byte, name string) (string, bool) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(payload, &members); err != nil {
		return "", false
	}
	var value *string
	if err := json.Unmarshal(members[name], &value); err != nil || value == nil {
		return "", false
	}
	return *value, true
}

// sessionVacateRequest is one recorded vacate request with its recorded
// payload version and its stamp among the session's vacate events.
type sessionVacateRequest struct {
	Payload        sessionVacatedPayload
	PayloadVersion int
	Stamp          int64
}

const sessionVacateRequestColumns = `work_id, project_id, session_ref, source_directory, COALESCE(destination_directory,''), payload_version, stamp`

func scanSessionVacateRequest(row *sql.Row) (sessionVacateRequest, error) {
	var r sessionVacateRequest
	p := &r.Payload
	err := row.Scan(&p.WorkID, &p.ProjectID, &p.SessionRef, &p.SourceDirectory, &p.DestinationDirectory, &r.PayloadVersion, &r.Stamp)
	return r, err
}
