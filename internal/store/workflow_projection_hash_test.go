package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/sharper-flow/concord/internal/store/storetest/projection"
)

// WorkflowProjectionHash returns the canonical projection digest through the
// leaf test-support oracle, so store tests and consumer-package tests compare
// the complete persisted projection before and after replay. It is test
// support: no production caller exists.
func WorkflowProjectionHash(ctx context.Context, s *Store) (string, error) {
	if s == nil || s.db == nil {
		return "", newFailure(KindUnavailable, "workflow_projection_hash", "store is not open", false, "open the authority database")
	}
	return projection.Hash(ctx, s.DatabaseForTesting())
}

func TestWorkflowProjectionHashQuotesSchemaIdentifiers(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	if _, err := s.DatabaseForTesting().Exec(`ALTER TABLE workflow_actors ADD COLUMN "hostile"", value; --" TEXT DEFAULT 'safe'`); err != nil {
		t.Fatal(err)
	}

	first, err := WorkflowProjectionHash(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	second, err := WorkflowProjectionHash(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("projection hash is not deterministic for quoted identifiers: %s != %s", first, second)
	}
}

func TestWorkflowProjectionHashMovesWithARestoredWorkflowRow(t *testing.T) {
	t.Parallel()
	// This tests the hash encoding, not workflow admission or event authority.
	db, err := sql.Open(driverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, table := range projection.Tables() {
		if _, err := db.Exec(`CREATE TABLE "` + table + `" (value TEXT)`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO workflow_instances(value) VALUES('baseline')`); err != nil {
		t.Fatal(err)
	}
	baseline, err := projection.Hash(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workflow_instances SET value='changed'`); err != nil {
		t.Fatal(err)
	}
	changed, err := projection.Hash(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if changed == baseline {
		t.Fatal("a changed workflow row left the projection hash unchanged")
	}
	if _, err := db.Exec(`UPDATE workflow_instances SET value='baseline'`); err != nil {
		t.Fatal(err)
	}
	restored, err := projection.Hash(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if restored != baseline {
		t.Fatalf("restoring the workflow row moved the projection hash: baseline=%s restored=%s", baseline, restored)
	}
}
