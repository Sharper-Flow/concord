package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
)

func allocateStoreSequence(ctx context.Context, tx *sql.Tx, name string) (int64, error) {
	var seq int64
	err := tx.QueryRowContext(ctx, `UPDATE store_sequences SET last=last+1 WHERE name=? RETURNING last`, name).Scan(&seq)
	if err != nil {
		return 0, wrapFailure(KindInvariantViolation, "append_event", "cannot advance the store sequence", false, "restore the store's sequence state", err)
	}
	return seq, nil
}

func effectCreatesRelationPosition(event Event) (bool, error) {
	if slices.Contains(relationIdentityEventKinds, event.Kind) {
		return true, nil
	}
	if event.Kind != "work.created" {
		return false, nil
	}
	var p struct {
		RaisedFromWorkID string `json:"raised_from_work_id"`
	}
	if err := json.Unmarshal(event.Payload, &p); err != nil {
		return false, err
	}
	return p.RaisedFromWorkID != "", nil
}
