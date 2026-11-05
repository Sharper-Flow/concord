package sqlitedb

import "testing"

func TestSnapshotTransactionDigestUsesSameState(t *testing.T) {
	db, _ := snapshotTestDB(t)
	if _, err := db.Exec(`CREATE TABLE state(value TEXT); INSERT INTO state VALUES('first'); CREATE TABLE store_revision(id INTEGER PRIMARY KEY, revision INTEGER); INSERT INTO store_revision VALUES(1,1)`); err != nil {
		t.Fatal(err)
	}
	want, err := CurrentStateDigest(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE store_revision SET revision=2`); err != nil {
		t.Fatal(err)
	}
	got, err := CurrentStateDigestTx(t.Context(), tx)
	if err != nil || got != want {
		t.Fatalf("transaction digest = %q, want %q: %v", got, want, err)
	}
	if _, err := tx.Exec(`INSERT INTO state VALUES('uncommitted')`); err != nil {
		t.Fatal(err)
	}
	got, err = CurrentStateDigestTx(t.Context(), tx)
	if err != nil || got == want {
		t.Fatalf("uncommitted state did not change transaction digest: %q: %v", got, err)
	}
}
