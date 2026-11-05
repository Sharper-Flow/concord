package store

import (
	"context"
	"database/sql"
)

const (
	WorkflowInstancePresent = "present"
	WorkflowInstanceAbsent  = "absent"
)

func readResumeIdentityTx(ctx context.Context, tx *sql.Tx, workID string) ([]string, error) {
	identity := []string{}
	if err := publishedWorkAbsenceQ(ctx, tx, workID, "C19.Resume"); err != nil {
		return nil, err
	}
	exists, err := workExistsCore(ctx, tx, workID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, newFailure(KindProjectionNotFound, "C19.Resume", "work item is not recorded", false, "reread_entities")
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=? ORDER BY pp.product_id LIMIT 65`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "C19.Resume", "cannot read Product identity", true, "retry once the database is readable", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		identity = append(identity, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(identity) > 64 {
		return nil, newFailure(KindLimitExceeded, "C19.Resume", "Product identity exceeds the resume read bound", false, "reduce_limit")
	}
	return identity, nil
}

// SessionResumeRead is the session's identity, workflow and handoff at one store revision.
type SessionResumeRead struct {
	SourceVersionWatermark int64
	WorkID                 string
	ProductIdentity        []string
	Resume                 *WorkflowResumeRead
	WorkflowInstanceAbsent bool
	PendingProjectHandoff  *ProjectHandoff
}

// ReadSessionResume reads all session resume facts in one consistent transaction.
func ReadSessionResume(ctx context.Context, s *Store, workID, projectID string) (SessionResumeRead, error) {
	var out SessionResumeRead
	if s == nil || s.db == nil {
		return out, newFailure(KindUnavailable, "C19.Resume", "store is not open", false, "open the authority database")
	}
	if len(workID) < 2 || len(workID) > 128 || len(projectID) > 128 || (len(projectID) > 0 && len(projectID) < 2) {
		return out, newFailure(KindInvalidPayload, "C19.Resume", "work or project ID is out of bounds", false, "supply bounded work and optional project IDs")
	}
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "C19.Resume", "cannot open a consistent resume read", true, "retry once the database is readable", err)
	}
	defer tx.Rollback()
	return readSessionResumeTx(ctx, tx, workID, projectID)
}

func readSessionResumeTx(ctx context.Context, tx *sql.Tx, workID, projectID string) (SessionResumeRead, error) {
	var out SessionResumeRead
	var err error
	out.SourceVersionWatermark, err = storeRevision(ctx, tx)
	if err != nil {
		return out, err
	}
	identity, err := readResumeIdentityTx(ctx, tx, workID)
	if err != nil {
		return out, err
	}
	out.WorkID, out.ProductIdentity = workID, identity
	var exists bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_instances WHERE work_id=?)`, workID).Scan(&exists)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "C19.Resume", "cannot read workflow instance", true, "retry once the database is readable", err)
	}
	if exists {
		resume, err := readWorkflowResumeTx(ctx, tx, workID)
		if err != nil {
			return out, err
		}
		out.Resume = &resume
	} else {
		out.WorkflowInstanceAbsent = true
	}
	version, err := activeWorkflowContractVersion(ctx, tx, workID, "C19.Resume")
	if err != nil && err != sql.ErrNoRows {
		return out, err
	}
	out.PendingProjectHandoff, err = selectProjectHandoffTx(ctx, tx, workID, projectID, version)
	if err != nil {
		return out, err
	}
	return out, nil
}
