package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

type occupancyReplayClaim struct {
	creationSeq int64
	worktreeID  string
}

type occupancyReplayRelease struct {
	unrecordedSession string
	released          map[string]bool
}

// Only serial recorded folds populate this state. A recorded release that
// actually deleted a row does not consume the unrecorded-session allowance.
func occupancyReplayReleaseTx(ctx context.Context, tx *sql.Tx, event Event, setID, projectID, claimOpID string) (*occupancyReplayRelease, error) {
	state, replay := ctx.Value(workflowReplayContextKey{}).(*workflowReplayState)
	if !replay || event.replaySourcePayloadVersion != 1 || event.Seq <= 0 {
		return nil, nil
	}
	var creationSeq int64
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT seq,payload FROM domain_events WHERE kind='work.worktree_created' AND subject_type='work_item' AND subject_id=? AND json_extract(payload,'$.claim_op_id')=? AND seq<? ORDER BY seq DESC LIMIT 1`, event.SubjectID, claimOpID, event.Seq).Scan(&creationSeq, &raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "fold_event", "cannot read the governing occupancy creation", true, "retry once the retained log is readable", err)
	}
	var members map[string]json.RawMessage
	var creation worktreeCreatedPayload
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &creation); err != nil {
		return nil, err
	}
	if _, present := members["occupant_session_ref"]; present || creation.SetID != setID || creation.ProjectID != projectID {
		return nil, nil
	}
	worktreeID := worktreeOccupancyID(setID, projectID, claimOpID)
	key := occupancyReplayClaim{creationSeq, worktreeID}
	if state.occupancyReleases == nil {
		state.occupancyReleases = map[occupancyReplayClaim]*occupancyReplayRelease{}
	}
	release := state.occupancyReleases[key]
	if release == nil {
		release = &occupancyReplayRelease{released: map[string]bool{}}
		state.occupancyReleases[key] = release
	}
	return release, nil
}

func admitUnrecordedOccupancyReleaseTx(ctx context.Context, tx *sql.Tx, event Event, setID, projectID, claimOpID, sessionRef string, scalar bool) (bool, error) {
	release, err := occupancyReplayReleaseTx(ctx, tx, event, setID, projectID, claimOpID)
	if err != nil || release == nil {
		return false, err
	}
	if scalar {
		var others int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM worktree_occupancy WHERE worktree_id=? AND session_ref<>?`, worktreeOccupancyID(setID, projectID, claimOpID), sessionRef).Scan(&others); err != nil {
			return false, err
		}
		if others != 0 {
			return false, nil
		}
	}
	if (release.unrecordedSession != "" && release.unrecordedSession != sessionRef) || (!scalar && release.released[sessionRef]) {
		return false, nil
	}
	release.unrecordedSession = sessionRef
	release.released[sessionRef] = true
	return true, nil
}

func noteRecordedOccupancyReleaseTx(ctx context.Context, tx *sql.Tx, event Event, setID, projectID, claimOpID, sessionRef string) error {
	release, err := occupancyReplayReleaseTx(ctx, tx, event, setID, projectID, claimOpID)
	if err == nil && release != nil {
		release.released[sessionRef] = true
	}
	return err
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
