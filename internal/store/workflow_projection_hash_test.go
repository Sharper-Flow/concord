package store

import (
	"context"
	"testing"
)

func TestWorkflowProjectionHashQuotesSchemaIdentifiers(t *testing.T) {
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
