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
	err = tx.QueryRowContext(ctx, `
		SELECT c.work_id, c.project_id, e.path, pl.normalized_value, e.occupant_session_ref
		FROM worktree_entries e
		JOIN worktree_claims c ON c.op_id=e.claim_op_id
		JOIN project_locators pl ON pl.project_id=c.project_id AND pl.kind='canonical_path'
		WHERE c.project_id=? AND e.path=? AND e.state='active'
		  AND (? = '' OR e.occupant_session_ref = '' OR e.occupant_session_ref = ?)
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
	var occupant string
	err := tx.QueryRowContext(ctx, `SELECT occupant_session_ref FROM worktree_entries WHERE set_id=? AND project_id=? AND path=? AND state='active'`, WorktreeSetID(p.WorkID), p.ProjectID, filepath.Clean(p.SourceDirectory)).Scan(&occupant)
	if err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "fold_event", "the vacated worktree is not active", false, "vacate the active linked worktree")
	}
	if err != nil {
		return err
	}
	if occupant == "" {
		return nil
	}
	if occupant != p.SessionRef {
		return newFailure(KindWorktreeOwnershipConflict, "fold_event", "session vacate does not own the recorded worktree occupancy", false, "vacate from the session recorded as the occupant")
	}
	_, err = tx.ExecContext(ctx, `UPDATE worktree_entries SET occupant_session_ref='' WHERE set_id=? AND project_id=? AND path=? AND state='active' AND occupant_session_ref=?`, WorktreeSetID(p.WorkID), p.ProjectID, filepath.Clean(p.SourceDirectory), p.SessionRef)
	return err
}
