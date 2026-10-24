package store

import (
	"context"
	"database/sql"
)

func sessionRelocationWriteFailure(err error) error {
	return wrapFailure(KindUnavailable, "fold_event", "cannot record the session relocation state", true, "retry once the database is writable", err)
}

// writeSessionVacateRequestTx makes a landing-verified request the latest
// request of its work item, session, Project, and source directory, pending
// until a verified landing completes it. The writing effect's sequence orders
// requests across work items.
func writeSessionVacateRequestTx(ctx context.Context, tx *sql.Tx, event Event, p sessionVacatedPayload) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_vacate_requests(work_id,session_ref,project_id,source_directory,destination_directory,request_seq,landed)
		VALUES(?,?,?,?,?,?,0)
		ON CONFLICT(work_id,session_ref,project_id,source_directory) DO UPDATE SET destination_directory=excluded.destination_directory,request_seq=excluded.request_seq,landed=0`,
		event.SubjectID, p.SessionRef, p.ProjectID, p.SourceDirectory, p.DestinationDirectory, event.Seq); err != nil {
		return sessionRelocationWriteFailure(err)
	}
	return nil
}

// writeSessionVacateLandingTx completes every request of the work item and
// session: a verified landing follows each of them.
func writeSessionVacateLandingTx(ctx context.Context, tx *sql.Tx, event Event, sessionRef string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE session_vacate_requests SET landed=1 WHERE work_id=? AND session_ref=?`, event.SubjectID, sessionRef); err != nil {
		return sessionRelocationWriteFailure(err)
	}
	return nil
}

// writeSessionClaimLandingTx records that the session landed in the claim
// generation the landing names. A missing claim identity proves no placement.
func writeSessionClaimLandingTx(ctx context.Context, tx *sql.Tx, event Event, p sessionClaimLandedPayload) error {
	if p.ClaimOpID == "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO session_claim_landings(work_id,session_ref,project_id,claim_op_id) VALUES(?,?,?,?)`,
		event.SubjectID, p.SessionRef, p.ProjectID, p.ClaimOpID); err != nil {
		return sessionRelocationWriteFailure(err)
	}
	return nil
}

// sessionVacateRequest is one standing vacate request and whether a verified
// landing completed it.
type sessionVacateRequest struct {
	Payload sessionVacatedPayload
	Landed  bool
}

const sessionVacateRequestColumns = `work_id, project_id, session_ref, source_directory, destination_directory, landed`

func scanSessionVacateRequest(row *sql.Row) (sessionVacateRequest, error) {
	var r sessionVacateRequest
	p := &r.Payload
	err := row.Scan(&p.WorkID, &p.ProjectID, &p.SessionRef, &p.SourceDirectory, &p.DestinationDirectory, &r.Landed)
	return r, err
}
