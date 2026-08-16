#!/usr/bin/env python3
"""Tests for scripts/check-tx-scope.py — the guard must catch the class it
exists for and stay quiet on the established safe patterns."""
import importlib.util
import sys
import tempfile
from pathlib import Path

spec = importlib.util.spec_from_file_location(
    "check_tx_scope", Path(__file__).with_name("check-tx-scope.py")
)
guard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guard)


def guard_one(source: str) -> bool:
    for _name, lines in guard.split_functions(source):
        if guard.guard_lines(lines):
            return True
    return False


def test_nested_sdb_after_begintx_is_flagged() -> None:
    source = """package store

func (s *Store) Broken(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT 1`).Scan(&n); err != nil {
		return err
	}
	return tx.Commit()
}
"""
    assert guard_one(source), "nested s.db after BeginTx must be flagged"


def test_nested_sdb_after_beginread_is_flagged() -> None:
    source = """package store

func (s *Store) BrokenRead(ctx context.Context) error {
	tx, err := beginRead(ctx, s, "q")
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := s.db.QueryContext(ctx, `SELECT 1`)
	_ = rows
	return nil
}
"""
    assert guard_one(source), "nested s.db after beginRead must be flagged"


def test_tx_scoped_core_is_clean() -> None:
    source = """package store

func (s *Store) Fixed(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := someCoreTx(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func someCoreTx(ctx context.Context, tx *sql.Tx) error {
	return tx.QueryRowContext(ctx, `SELECT 1`).Scan(new(int))
}
"""
    assert not guard_one(source), "tx-scoped core must not be flagged"


def test_sequential_separate_transactions_are_clean() -> None:
    source = """package store

func (s *Store) Sequential(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := tx.Commit(); err != nil {
		return err
	}
	var n int
	return s.db.QueryRowContext(ctx, `SELECT 1`).Scan(&n)
}
"""
    # Sequential is still flagged by policy: the guard is textual and cannot
    # prove the tx ended. This documents the conservative choice.
    assert guard_one(source), "guard is conservative by design"


def test_no_tx_is_clean() -> None:
    source = """package store

func (s *Store) Plain(ctx context.Context) error {
	var n int
	return s.db.QueryRowContext(ctx, `SELECT 1`).Scan(&n)
}
"""
    assert not guard_one(source), "s.db without any tx must not be flagged"


def test_enterfold_is_flagged() -> None:
    source = """package store

func foldThing(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := enterFold(ctx, tx); err != nil {
		return err
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT 1`).Scan(&n); err != nil {
		return err
	}
	return nil
}
"""
    assert guard_one(source), "s.db inside a fold must be flagged"


def test_comment_text_cannot_trip_the_guard() -> None:
    source = """package store

func (s *Store) Commented(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// a comment mentioning s.db.QueryRowContext is not code
	return tx.Commit()
}
"""
    assert not guard_one(source), "comments must be stripped before checking"


def test_full_repo_passes() -> None:
    # The repository's own non-test store sources must be clean; this is the
    # CI assertion.
    with tempfile.TemporaryDirectory() as tmp:
        code = guard.main()
    assert code == 0


if __name__ == "__main__":
    failures = 0
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            try:
                fn()
                print(f"ok  {name}")
            except AssertionError as err:
                failures += 1
                print(f"FAIL {name}: {err}")
    sys.exit(1 if failures else 0)
