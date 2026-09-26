package store

import (
	"context"
	"database/sql"
	"path/filepath"
)

// SessionVacateTarget is the core-derived destination for a session leaving
// its linked worktree. The destination always comes from the Project locator.
type SessionVacateTarget struct {
	WorkID               string
	ProjectID            string
	SourceDirectory      string
	DestinationDirectory string
	OccupantSessionRef   string
}

// ResolveSessionVacateTargetTx resolves the active worktree at source and the
// registered main checkout for the same Project in one transaction.
func ResolveSessionVacateTargetTx(ctx context.Context, transaction *Transaction, projectID, sourceDirectory string, sessionRef ...string) (SessionVacateTarget, error) {
	var target SessionVacateTarget
	tx, err := transactionSQL(transaction, "session_vacate")
	if err != nil {
		return target, err
	}
	if projectID == "" || sourceDirectory == "" {
		return target, newFailure(KindInvalidOperation, "session_vacate", "vacate requires the resolved Project and linked worktree", false, "run the operation from a linked worktree")
	}
	sourceDirectory = filepath.Clean(sourceDirectory)
	// The source row's session_ref comes from worktree_occupancy (CD-0178
	// D3); the per-session filter narrows the read to the calling session.
	err = tx.QueryRowContext(ctx, `
		SELECT c.work_id, c.project_id, e.path, pl.normalized_value, COALESCE((
			SELECT o.session_ref
			  FROM worktree_occupancy o
			 WHERE o.worktree_id = e.set_id || ':' || e.project_id || ':' || e.claim_op_id
			 ORDER BY o.recorded_at LIMIT 1
		), '')
		FROM worktree_entries e
		JOIN worktree_claims c ON c.op_id=e.claim_op_id
		JOIN project_locators pl ON pl.project_id=c.project_id AND pl.kind='canonical_path'
		WHERE c.project_id=? AND e.path=? AND e.state='active'
		  AND (? = '' OR NOT EXISTS (
			SELECT 1 FROM worktree_occupancy o2
			 WHERE o2.worktree_id = e.set_id || ':' || e.project_id || ':' || e.claim_op_id
		  )
		  OR EXISTS (
			SELECT 1 FROM worktree_occupancy o3
			 WHERE o3.worktree_id = e.set_id || ':' || e.project_id || ':' || e.claim_op_id
			   AND o3.session_ref = ?)
		  )
		ORDER BY pl.locator_id
		LIMIT 1`, projectID, sourceDirectory, firstSessionRef(sessionRef), firstSessionRef(sessionRef)).Scan(&target.WorkID, &target.ProjectID, &target.SourceDirectory, &target.DestinationDirectory, &target.OccupantSessionRef)
	if err == sql.ErrNoRows {
		return target, newFailure(KindProjectionNotFound, "session_vacate", "the session does not run in an active Concord worktree", false, "run the operation from its linked worktree")
	}
	if err != nil {
		return target, wrapFailure(KindUnavailable, "session_vacate", "cannot resolve the session worktree destination", true, "retry once the database is readable", err)
	}
	if filepath.Clean(target.SourceDirectory) == filepath.Clean(target.DestinationDirectory) {
		return target, newFailure(KindInvalidOperation, "session_vacate", "the session is already in the registered main checkout", false, "run the operation from a linked worktree")
	}
	return target, nil
}

type sessionVacatedPayload struct {
	WorkID               string `json:"work_id"`
	ProjectID            string `json:"project_id"`
	SessionRef           string `json:"session_ref"`
	SourceDirectory      string `json:"source_directory"`
	DestinationDirectory string `json:"destination_directory"`
	LandedDirectory      string `json:"landed_directory"`
}

func firstSessionRef(refs []string) string {
	if len(refs) == 0 {
		return ""
	}
	return refs[0]
}

// CountWorkSessionVacatesTx returns how many vacate operations the
// projection already records for one work item and session. A recorded event
// is the durable record of one vacate the core accepted, written before the
// adapter moves the host session; it is not proof the session landed. The
// count is therefore the ordinal of the next relocation request, not a count
// of confirmed host moves. Read-only work resume writes no event and no
// session-directory binding, so the recorded vacate history is the only
// projection that distinguishes repeated vacates of the same work item
// by the same session. It runs inside the caller's transaction so the read
// observes the caller's own uncommitted events.
func CountWorkSessionVacatesTx(ctx context.Context, transaction *Transaction, workID, sessionRef string) (int, error) {
	tx, err := transactionSQL(transaction, "session_vacate")
	if err != nil {
		return 0, err
	}
	var count int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE kind='work.session_vacated' AND subject_id=? AND json_extract(payload,'$.session_ref')=?`, workID, sessionRef).Scan(&count)
	if err != nil {
		return 0, wrapFailure(KindUnavailable, "session_vacate", "cannot count the recorded vacate events", true, "retry once the database is readable", err)
	}
	return count, nil
}

func foldSessionVacated(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p sessionVacatedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.WorkID == "" || p.WorkID != event.SubjectID || p.ProjectID == "" || p.SessionRef == "" || p.SourceDirectory == "" {
		return newFailure(KindInvalidPayload, "fold_event", "session vacated payload is missing required fields", false, "supply work, project, session, and source directory")
	}
	return releaseSessionWorktreeOccupancyTx(ctx, tx, "fold_event", p.WorkID, p.ProjectID, p.SourceDirectory, p.SessionRef)
}
