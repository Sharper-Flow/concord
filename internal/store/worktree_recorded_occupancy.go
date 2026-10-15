package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// admitRecordedOccupancyReleaseTx admits one recorded historical release
// during serial replay. The committed event is evidence its target was
// released by the writer's own transaction, so admission re-derives from the
// retained log alone: the governing creation's identity, never its creation
// occupant member, no lifetime session count, and no liveness observation.
// The recorded kind and shape own the target: exclusive kinds refuse while
// another session's row stands, and a row-set release owns only the row it
// names.
func admitRecordedOccupancyReleaseTx(ctx context.Context, tx *sql.Tx, event Event, setID, projectID, claimOpID, sessionRef string) (bool, error) {
	if !isWorkflowReplay(ctx) || event.replaySourcePayloadVersion != 1 || event.Seq <= 0 {
		return false, nil
	}
	// Exclusivity is a property of the recorded event, never a caller knob.
	var exclusive bool
	switch {
	case event.Kind == "work.session_vacated" && event.recordedPayloadShape == recordedShapeCurrent:
		exclusive = true
	case event.Kind == "work.worktree_occupancy_released" && event.recordedPayloadShape == recordedShapeScalarOccupancyRelease:
		exclusive = true
	case event.Kind == "work.worktree_occupancy_released" && event.recordedPayloadShape == recordedShapeCurrent:
		exclusive = false
	default:
		return false, nil
	}
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE kind='work.worktree_created' AND subject_type='work_item' AND subject_id=? AND json_extract(payload,'$.claim_op_id')=? AND seq<? ORDER BY seq DESC LIMIT 1`, event.SubjectID, claimOpID, event.Seq).Scan(&raw)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, wrapFailure(KindUnavailable, "fold_event", "cannot read the governing occupancy creation", true, "retry once the retained log is readable", err)
	}
	var creation worktreeCreatedPayload
	if err := json.Unmarshal(raw, &creation); err != nil {
		return false, err
	}
	if creation.SetID != setID || creation.ProjectID != projectID {
		return false, nil
	}
	if exclusive {
		var others int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM worktree_occupancy WHERE worktree_id=? AND session_ref<>?`, worktreeOccupancyID(setID, projectID, claimOpID), sessionRef).Scan(&others); err != nil {
			return false, err
		}
		if others != 0 {
			return false, nil
		}
	}
	return true, nil
}

type occupancyRebuildRow struct {
	worktreeID, sessionRef, recordedAt string
	hostPID, hostPIDStart              sql.NullInt64
	hasProcessIdentity                 int
}

func readOccupancyRebuildEvidence(ctx context.Context, q queryer) ([]occupancyRebuildRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT worktree_id,session_ref,recorded_at,host_pid,host_pid_start,has_process_identity FROM worktree_occupancy ORDER BY worktree_id,session_ref`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var evidence []occupancyRebuildRow
	for rows.Next() {
		var row occupancyRebuildRow
		if err := rows.Scan(&row.worktreeID, &row.sessionRef, &row.recordedAt, &row.hostPID, &row.hostPIDStart, &row.hasProcessIdentity); err != nil {
			return nil, err
		}
		evidence = append(evidence, row)
	}
	return evidence, rows.Err()
}

// Pre-rebuild rows are evidence only: none are restored or used by event folds.
// Compare inside the rebuild transaction so any missing row rolls it all back.
func verifyOccupancyRebuildEvidence(ctx context.Context, tx *sql.Tx, before []occupancyRebuildRow) error {
	after, err := readOccupancyRebuildEvidence(ctx, tx)
	if err != nil {
		return err
	}
	reproduced := make(map[occupancyRebuildRow]bool, len(after))
	for _, row := range after {
		reproduced[row] = true
	}
	for _, row := range before {
		if !reproduced[row] {
			return newFailure(KindProjectionConflict, "rebuild_from_log", fmt.Sprintf("occupancy for worktree %q session %q cannot be reproduced from retained events", row.worktreeID, row.sessionRef), false, "restore a snapshot with reproducible occupancy")
		}
	}
	return nil
}
