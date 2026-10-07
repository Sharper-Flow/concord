package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestEmptyInitiativeEntryReadsReturnArrays(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedInitiativeForNarrative(t, s)
	entries, err := s.ReadInitiativeEntries(ctx, "initiative")
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := json.Marshal(entries); err != nil || string(raw) != "[]" {
		t.Errorf("empty entries=%s err=%v, want []", raw, err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	entries, err = readInitiativeEntriesTx(ctx, tx, "initiative")
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := json.Marshal(entries); err != nil || string(raw) != "[]" {
		t.Errorf("empty transaction entries=%s err=%v, want []", raw, err)
	}
}
