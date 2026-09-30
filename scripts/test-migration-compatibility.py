#!/usr/bin/env python3
"""Tests for check-migration-compatibility.py.

The validator decides whether a migration may be declared additive, and an
additive declaration is what lets an older binary open a newer database. A
wrong "additive" verdict is silent corruption, so the classifier's boundary
cases are pinned here rather than left to the live schema, which happens to
exercise only some of them.

The FoldMaintained rules above RULE_FLOOR are pinned here too. A declaration
is the author's signed claim beside the SQL, and the checker's whole authority
over it is these cases: the closed vocabulary, the required declaration on a
pre-existing column add, the breaking consequence of "advance", and the
refusal of a declaration with nothing to describe.
"""

from __future__ import annotations

import importlib.util
import sys
from pathlib import Path

SCRIPTS = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location(
    "check_migration_compatibility", SCRIPTS / "check-migration-compatibility.py"
)
assert spec and spec.loader
check = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check)

FAILURES: list[str] = []


def expect(name: str, sql: str, breaking: bool) -> None:
    reasons = check.classify(sql)
    if bool(reasons) != breaking:
        want = "breaking" if breaking else "additive"
        FAILURES.append(f"{name}: classified {reasons or 'additive'}, want {want}")


def expect_entries(name: str, source: str, versions: list[int]) -> None:
    found = [version for version, _, _ in check.migrations(source)]
    if found != versions:
        FAILURES.append(f"{name}: parsed versions {found}, want {versions}")


# --- FoldMaintained declaration parsing and rules ----------------------------
# Entries are built in the shipped struct-literal shape so the declaration
# regexes run against the text a real schema.go carries.

ADD_COLUMN_SQL = "ALTER TABLE existing ADD COLUMN c TEXT NOT NULL DEFAULT '';"


def entry(version: int, sql: str, breaking: bool = False, fold: str | None = None) -> str:
    fields = [f"Version: {version}", f'Name: "m{version}"']
    if breaking:
        fields.append("Breaking: true")
    if fold is not None:
        fields.append(f'FoldMaintained: "{fold}"')
    fields.append(f"SQL: `{sql}`")
    body = "".join(f"\n\t\t{field}," for field in fields)
    return f"\t{{{body}\n\t}},\n"


def fold_source(*entries: str) -> str:
    return "var migrations = []migration{\n" + "".join(entries) + "}\n"


def parsed_folds(name: str, source: str, want: list[str]) -> None:
    found = [check.fold_maintained(entry) for _, entry, _ in check.migrations(source)]
    if found != want:
        FAILURES.append(f"{name}: parsed FoldMaintained {found}, want {want}")


def expect_evaluate(
    name: str, entries: list[tuple[int, str, str]], failures: int, breaking: list[int]
) -> None:
    got_failures, got_breaking = check.evaluate(entries)
    if len(got_failures) != failures:
        FAILURES.append(
            f"{name}: {len(got_failures)} failure(s) {got_failures}, want {failures}"
        )
    if got_breaking != breaking:
        FAILURES.append(f"{name}: breaking {got_breaking}, want {breaking}")


# The declaration parses beside the other fields, tolerates gofmt alignment,
# and its absence reads as the empty declaration.
parsed_folds(
    "fold declarations parse with alignment",
    fold_source(
        entry(1, "SELECT 1;"),
        entry(2, "SELECT 1;", fold="origin"),
        entry(3, "SELECT 1;", breaking=True, fold="advance"),
    ),
    ["", "origin", "advance"],
)

# Rule (a): above the floor, an ADD COLUMN on a pre-existing table must carry
# a FoldMaintained declaration. Undeclared, the checker refuses rather than
# guess which fold class the writer belongs to.
expect_evaluate(
    "undeclared fold-maintained column above the floor",
    check.migrations(fold_source(entry(109, ADD_COLUMN_SQL))),
    failures=1,
    breaking=[],
)

# Migrations at or below the floor predate the field and keep the older
# contract: migration 108 itself stays untouched.
expect_evaluate(
    "floor version keeps the older contract",
    check.migrations(fold_source(entry(108, ADD_COLUMN_SQL))),
    failures=0,
    breaking=[],
)

# A column added to a table the same migration creates needs no declaration:
# no older fold generation ever wrote that table.
expect_evaluate(
    "column on a table born here needs no declaration",
    check.migrations(
        fold_source(entry(109, "CREATE TABLE t (a TEXT);\nALTER TABLE t ADD COLUMN b TEXT DEFAULT '';"))
    ),
    failures=0,
    breaking=[],
)

# The advance/origin split. "origin" sets the column once and first
# derivation wins, so the backfill and RebuildFromLog agree and the step
# stays additive.
expect_evaluate(
    "origin column stays additive",
    check.migrations(fold_source(entry(109, ADD_COLUMN_SQL, fold="origin"))),
    failures=0,
    breaking=[],
)
expect_evaluate(
    "origin column must not declare breaking",
    check.migrations(fold_source(entry(109, ADD_COLUMN_SQL, fold="origin", breaking=True))),
    failures=1,
    breaking=[],
)

# Rule (b): "advance" moves the column as later events apply, so an older
# fold generation leaves it unconverged (CD-0111 D3) and the entry breaks an
# older binary whether or not Breaking is declared.
expect_evaluate(
    "advance column without Breaking fails",
    check.migrations(fold_source(entry(109, ADD_COLUMN_SQL, fold="advance"))),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "advance column declared breaking passes",
    check.migrations(fold_source(entry(109, ADD_COLUMN_SQL, fold="advance", breaking=True))),
    failures=0,
    breaking=[109],
)

# Rule (c): a declaration without the SQL shape it describes is refused.
expect_evaluate(
    "declaration on a migration that adds no column",
    check.migrations(fold_source(entry(109, "SELECT 1;", fold="origin"))),
    failures=1,
    breaking=[],
)

# Rule (d): the vocabulary is closed.
expect_evaluate(
    "value outside the vocabulary",
    check.migrations(fold_source(entry(109, ADD_COLUMN_SQL, fold="later"))),
    failures=1,
    breaking=[],
)


# A new table is invisible to an older binary, however constrained it is.
expect(
    "new table with constraints",
    "CREATE TABLE t (a TEXT NOT NULL CHECK(a <> ''), b TEXT REFERENCES u(id));",
    breaking=False,
)
expect("new index", "CREATE INDEX t_a ON t(a);", breaking=False)
expect(
    "added column with default",
    "ALTER TABLE existing ADD COLUMN c TEXT NOT NULL DEFAULT '';",
    breaking=False,
)

# Objects created and destroyed inside one migration never existed for an
# older binary, so the table-rebuild scaffolding is not itself breaking.
expect(
    "temp scaffolding dropped in the same migration",
    "CREATE TEMP TABLE t_backup AS SELECT * FROM t;\nDROP TABLE t_backup;",
    breaking=False,
)
expect(
    "index and trigger on a table born here",
    "CREATE TABLE t (a TEXT);\n"
    "CREATE UNIQUE INDEX t_a ON t(a);\n"
    "CREATE TRIGGER t_guard BEFORE INSERT ON t FOR EACH ROW "
    "BEGIN SELECT RAISE(ABORT, 'no'); END;",
    breaking=False,
)

# Removing or constraining something an older binary already names.
expect("dropped table", "DROP TABLE existing;", breaking=True)
expect("dropped column", "ALTER TABLE existing DROP COLUMN c;", breaking=True)
expect("renamed table", "ALTER TABLE existing RENAME TO other;", breaking=True)
expect(
    "unique index on an existing table",
    "CREATE UNIQUE INDEX existing_a ON existing(a);",
    breaking=True,
)
expect(
    "trigger on an existing table",
    "CREATE TRIGGER existing_guard BEFORE INSERT ON existing FOR EACH ROW "
    "BEGIN SELECT RAISE(ABORT, 'no'); END;",
    breaking=True,
)

# An unrecognized statement is breaking. The classifier must never call
# something additive because it failed to understand it.
expect("unknown statement", "VACUUM INTO 'copy.db';", breaking=True)

# A trigger body holds semicolons. Splitting on them naively cuts the body
# apart and turns the tail into an unclassified statement.
expect(
    "trigger body does not fragment",
    "CREATE TABLE t (a TEXT);\n"
    "CREATE TRIGGER t_guard BEFORE INSERT ON t FOR EACH ROW BEGIN "
    "SELECT RAISE(ABORT, 'no'); SELECT 1; END;",
    breaking=False,
)

# gofmt aligns struct literal values by the longest field name present, so the
# entry parser must not depend on one column width.
expect_entries(
    "aligned and unaligned entries both parse",
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 1,\n\t\tName:    \"one\",\n\t\tSQL: `SELECT 1;`,\n\t},\n"
    "\t{\n\t\tVersion:  2,\n\t\tName:     \"two\",\n\t\tBreaking: true,\n"
    "\t\tSQL:      `SELECT 1;`,\n\t},\n}\n",
    [1, 2],
)


def main() -> int:
    for failure in FAILURES:
        print(f"test-migration-compatibility: {failure}", file=sys.stderr)
    if FAILURES:
        print(
            f"test-migration-compatibility: {len(FAILURES)} failure(s)", file=sys.stderr
        )
        return 1
    print("test-migration-compatibility: all classifier cases passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
