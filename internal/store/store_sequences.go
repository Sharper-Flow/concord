package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

func allocateStoreSequence(ctx context.Context, tx *sql.Tx, name string) (int64, error) {
	seq, err := sqlitedb.AllocateSequence(ctx, tx, name)
	return seq, mechanismFailure(err)
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
