package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestApplyOperationRefusesACommittedFoldGuardStrand seeds the stranded
// guard row directly, the only way one can exist, and asserts the public
// append route at the contract's semantics: the collision is an invariant
// failure naming the offline recovery route, the strand is not absorbed,
// and no projection row is folded while it guards the database.
func TestApplyOperationRefusesACommittedFoldGuardStrand(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	err := ApplyOperation(ctx, s, Operation{
		Events:           []Event{locatorProductEvent("p-strand"), locatorProjectEvent("pr-strand"), locatorMembershipEvent("p-strand", "pr-strand")},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "p-strand"): 0, VersionRef(SubjectProject, "pr-strand"): 0},
	})
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("strand error = %v, want a typed failure", err)
	}
	if failure.Kind != KindInvariantViolation {
		t.Fatalf("strand kind = %s, want %s", failure.Kind, KindInvariantViolation)
	}
	if failure.RetrySafe {
		t.Fatal("strand reports retry_safe")
	}
	if !strings.Contains(failure.RecoveryAction, "recover-fold-guard") {
		t.Fatalf("recovery action %q does not name the offline recovery route", failure.RecoveryAction)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM fold_guard`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("the refused fold must not absorb the strand, got %d rows", count)
	}
	var folded int
	if err := s.db.QueryRow(`SELECT count(*) FROM products WHERE id='p-strand'`).Scan(&folded); err != nil {
		t.Fatal(err)
	}
	if folded != 0 {
		t.Fatal("a projection row folded while a stranded guard owns the fold window")
	}
}
