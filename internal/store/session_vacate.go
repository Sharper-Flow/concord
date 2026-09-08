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
}

// ResolveSessionVacateTargetTx resolves the active worktree at source and the
// registered main checkout for the same Project in one transaction.
func ResolveSessionVacateTargetTx(ctx context.Context, transaction *Transaction, projectID, sourceDirectory string) (SessionVacateTarget, error) {
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
		SELECT c.work_id, c.project_id, e.path, pl.normalized_value
		FROM worktree_entries e
		JOIN worktree_claims c ON c.op_id=e.claim_op_id
		JOIN project_locators pl ON pl.project_id=c.project_id AND pl.kind='canonical_path'
		WHERE c.project_id=? AND e.path=? AND e.state='active'
		ORDER BY pl.locator_id
		LIMIT 1`, projectID, sourceDirectory).Scan(&target.WorkID, &target.ProjectID, &target.SourceDirectory, &target.DestinationDirectory)
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

func foldSessionVacated(_ context.Context, _ *sql.Tx, _ Event) error {
	return nil
}
