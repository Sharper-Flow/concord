package store

import (
	"context"
	"testing"
)

func TestResearchRelianceMissingRevisionIsProjectionNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "consumer")
	pack := createSimplePack(t, s, "missing-revision", "owner")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	err = BindResearchRelianceTx(ctx, tx, "consumer", []ResearchBindingDeclaration{{PackID: pack.PackID, Revision: 999, UseRole: UseContext, Required: true}}, s.now())
	assertFailureKind(t, err, KindProjectionNotFound)
	var pins int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM active_research_consumers`).Scan(&pins); err != nil || pins != 0 {
		t.Fatalf("pins=%d err=%v", pins, err)
	}
}

func TestResearchRelianceExactRedeclarationPreservesPin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "consumer")
	pack := createSimplePack(t, s, "exact-pin", "owner")
	declarations := []ResearchBindingDeclaration{{PackID: pack.PackID, Revision: 1, UseRole: UseContext, Required: true}}
	for range 2 {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := BindResearchRelianceTx(ctx, tx, "consumer", declarations, s.now()); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	var pins, version int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM active_research_consumers`).Scan(&pins); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT expected_version FROM active_research_packs WHERE pack_id=?`, pack.PackID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if pins != 1 || version != 2 {
		t.Fatalf("pins=%d version=%d, want 1/2", pins, version)
	}
}
