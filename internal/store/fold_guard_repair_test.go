package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

type foldRowReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func foldGuardRows(t *testing.T, q foldRowReader) int {
	t.Helper()
	var n int
	if err := q.QueryRowContext(context.Background(), `SELECT count(*) FROM fold_guard`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func openRepairStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), copyTestDatabase(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedRepairProduct(t *testing.T, s *Store) {
	t.Helper()
	if err := ApplyOperation(context.Background(), s, Operation{
		Events: []Event{
			operationEvent("repair-product", "product.created", SubjectProduct, "prod", map[string]any{
				"display_name": "Product", "stage_maturity": "prototype", "stage_audience_commitment": "operator_only",
			}),
			operationEvent("repair-project", "project.created", SubjectProject, "proj", map[string]any{"display_name": "Project"}),
			operationEvent("repair-membership", "product_project.added", SubjectProduct, "prod", map[string]any{
				"product_id": "prod", "project_id": "proj", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2,
			}),
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// A nested fold must keep the guard row until the outer scope closes, and the
// outer close must be the one that re-enables projection protection.
func TestFoldScopeNestingKeepsGuardUntilOuterClose(t *testing.T) {
	t.Parallel()
	s := openRepairStore(t)
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	outer, err := beginWrite(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if got := foldGuardRows(t, tx); got != 1 {
		t.Fatalf("guard rows after the outer fold = %d, want 1", got)
	}
	// A nested fold opens a second level on the same transaction-owned scope,
	// exactly as the mutation seams do.
	if err := outer.enter(ctx); err != nil {
		t.Fatal(err)
	}
	if got := foldGuardRows(t, tx); got != 1 {
		t.Fatalf("guard rows after the nested fold = %d, want 1", got)
	}
	if err := outer.close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := foldGuardRows(t, tx); got != 1 {
		t.Fatalf("nested close dropped the guard: rows = %d, want 1", got)
	}
	if err := outer.close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := foldGuardRows(t, tx); got != 0 {
		t.Fatalf("guard rows after the outer close = %d, want 0", got)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('x','X','prototype','operator_only',1,'now','now')`); err == nil {
		t.Fatal("a direct projection write succeeded with no fold scope open")
	}
}

// The transaction seam must reuse the region's scope, so a mutation nested in
// a fold region depth-counts instead of colliding, and the guard survives
// until the region's own level closes.
func TestFoldScopeNestsThroughTransactionSeam(t *testing.T) {
	t.Parallel()
	s := openRepairStore(t)
	seedRepairProduct(t, s)
	ctx := context.Background()
	if err := s.Transact(ctx, func(tx *Transaction) error {
		scope, err := beginWrite(ctx, tx.tx)
		if err != nil {
			return err
		}
		defer func() { _ = scope.close(ctx) }()
		tx.write = scope
		if _, err := ApplyOperationTx(ctx, tx, Operation{
			Events:           []Event{operationEvent("repair-nested", "product.renamed", SubjectProduct, "prod", map[string]any{"display_name": "Nested"})},
			ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "prod"): 2},
		}); err != nil {
			return err
		}
		if got := foldGuardRows(t, tx.tx); got != 1 {
			t.Fatalf("guard rows while the region stays open = %d, want 1", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := foldGuardRows(t, s.DatabaseForTesting()); got != 0 {
		t.Fatalf("committed guard rows = %d, want 0", got)
	}
}

// The guard row lives inside the transaction, so a rollback leaves no
// committed guard behind.
func TestFoldScopeRollbackLeavesNoCommittedGuard(t *testing.T) {
	t.Parallel()
	s := openRepairStore(t)
	seedRepairProduct(t, s)
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := beginWrite(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyOperationTx(ctx, &Transaction{tx: tx, write: scope}, Operation{
		Events:           []Event{operationEvent("repair-rollback", "product.renamed", SubjectProduct, "prod", map[string]any{"display_name": "Rolled"})},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "prod"): 2},
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := foldGuardRows(t, s.DatabaseForTesting()); got != 0 {
		t.Fatalf("committed guard rows after rollback = %d, want 0", got)
	}
}

// A guard uniqueness collision means a guard row committed outside any fold
// scope. It is an invariant failure with offline recovery guidance, not a
// retryable unavailability.
func TestFoldGuardCollisionIsInvariantFailure(t *testing.T) {
	t.Parallel()
	s := openRepairStore(t)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`)
	})
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = beginWrite(ctx, tx)
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("beginWrite error = %v, want a typed failure", err)
	}
	if failure.Kind != KindInvariantViolation {
		t.Fatalf("collision kind = %s, want %s", failure.Kind, KindInvariantViolation)
	}
	if failure.RetrySafe {
		t.Fatal("collision reports retry_safe")
	}
	if !strings.Contains(failure.RecoveryAction, "concord restore") || !strings.Contains(failure.RecoveryAction, "new path") {
		t.Fatalf("recovery action %q does not name verified backup restore into a new path", failure.RecoveryAction)
	}
}

// Ordinary use must refuse before anything reads or writes a database that
// carries a committed fold_guard row.
func TestOpenRefusesCommittedFoldGuard(t *testing.T) {
	t.Parallel()
	s := openRepairStore(t)
	path := s.Path()
	seedRepairProduct(t, s)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), path)
	if err == nil {
		_ = reopened.Close()
		t.Fatal("Open admitted a database with a committed fold_guard row")
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvariantViolation {
		t.Fatalf("Open error = %v, want %s", err, KindInvariantViolation)
	}
	if !strings.Contains(failure.RecoveryAction, "concord restore") || !strings.Contains(failure.RecoveryAction, "new path") {
		t.Fatalf("recovery action %q does not name verified backup restore into a new path", failure.RecoveryAction)
	}
}
