package store

import (
	"context"
	"testing"
)

// TestApplyOperationToleratesACommittedFoldGuardStrand reproduces the wedge
// observed on 2026-09-21: a process that died between enterFold and leaveFold
// left the guard row committed, and every later fold refused until an agent
// deleted the row by hand. enterFold and leaveFold share one transaction, so
// a committed row can never belong to a live fold; applying an operation must
// ignore the strand rather than refuse.
func TestApplyOperationToleratesACommittedFoldGuardStrand(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	err := ApplyOperation(ctx, s, Operation{
		Events:           []Event{locatorProductEvent("p-strand"), locatorProjectEvent("pr-strand"), locatorMembershipEvent("p-strand", "pr-strand")},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "p-strand"): 0, VersionRef(SubjectProject, "pr-strand"): 0},
	})
	if err != nil {
		t.Fatalf("a committed fold_guard strand must not wedge a fold: %v", err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM fold_guard`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("the fold's leaveFold must absorb the strand, got %d rows", count)
	}
	var productName string
	if err := s.db.QueryRow(`SELECT display_name FROM products WHERE id='p-strand'`).Scan(&productName); err != nil {
		t.Fatalf("the folded product row is missing: %v", err)
	}
	if productName != "p-strand" {
		t.Fatalf("unexpected product display name %q", productName)
	}
}
