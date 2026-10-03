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
import sqlite3
import sys
import tempfile
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
        fold_source(entry(109, "CREATE TABLE t (a TEXT);\nALTER TABLE main.t ADD COLUMN b TEXT DEFAULT '';"))
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

# A present field never reads as an absent one. The value parser strips a
# trailing // comment and comma, unquotes only a fully double-quoted literal,
# and leaves any other Go expression as text the vocabulary check refuses.
COMMENTED_ORIGIN_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 110,\n\t\tName: \"quoted_with_comment\",\n"
    '\t\tFoldMaintained: "origin", // first derivation wins\n'
    f"\t\tSQL: `{ADD_COLUMN_SQL}`,\n\t}},\n"
    "}\n"
)
RAW_STRING_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 111,\n\t\tName: \"raw_string\",\n"
    "\t\tFoldMaintained: `origin`,\n"
    "\t\tSQL: `SELECT 1;`,\n\t},\n"
    "}\n"
)
parsed_folds(
    "commented declaration parses",
    COMMENTED_ORIGIN_SOURCE,
    ["origin"],
)
parsed_folds(
    "raw-string declaration stays unparsed text",
    RAW_STRING_SOURCE,
    ["`origin`"],
)

# A trailing comment on a valid origin declaration keeps rule (a) satisfied.
expect_evaluate(
    "commented origin declaration on a column add passes",
    check.migrations(COMMENTED_ORIGIN_SOURCE),
    failures=0,
    breaking=[],
)

# A raw-string value is a present declaration the vocabulary refuses, and on
# a migration that adds no column it does not slip past rule (c) either.
expect_evaluate(
    "raw-string declaration is refused, not read as absent",
    check.migrations(RAW_STRING_SOURCE),
    failures=2,
    breaking=[],
)

# A field whose value starts on a later line is present and refused, never
# read as absent: the comment-only residue after stripping must not become
# the empty declaration.
MULTILINE_INVALID_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 112,\n\t\tName: \"value_on_next_line\",\n"
    "\t\tFoldMaintained: // signed claim\n"
    '\t\t"later",\n'
    "\t\tSQL: `SELECT 1;`,\n\t},\n"
    "}\n"
)
MULTILINE_ORIGIN_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 113,\n\t\tName: \"multiline_origin\",\n"
    "\t\tFoldMaintained:\n"
    '\t\t"origin",\n'
    f"\t\tSQL: `{ADD_COLUMN_SQL}`,\n\t}},\n"
    "}\n"
)
parsed_folds(
    "multiline declarations return the elsewhere sentinel",
    MULTILINE_INVALID_SOURCE,
    [check.FOLD_VALUE_ELSEWHERE],
)
parsed_folds(
    "multiline valid origin also returns the sentinel",
    MULTILINE_ORIGIN_SOURCE,
    [check.FOLD_VALUE_ELSEWHERE],
)
expect_evaluate(
    "multiline invalid value on a no-column migration is refused twice",
    check.migrations(MULTILINE_INVALID_SOURCE),
    failures=2,
    breaking=[],
)
expect_evaluate(
    "multiline valid origin draws only the vocabulary refusal, not rule (a)",
    check.migrations(MULTILINE_ORIGIN_SOURCE),
    failures=1,
    breaking=[],
)

# A block comment ahead of the field name does not hide the declaration.
BLOCK_COMMENT_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 114,\n\t\tName: \"block_comment\",\n"
    '\t\t/* signed claim */ FoldMaintained: "later",\n'
    "\t\tSQL: `SELECT 1;`,\n\t},\n"
    "}\n"
)
parsed_folds(
    "block comment before the field name still parses",
    BLOCK_COMMENT_SOURCE,
    ["later"],
)
expect_evaluate(
    "block-commented invalid value on a no-column migration is refused twice",
    check.migrations(BLOCK_COMMENT_SOURCE),
    failures=2,
    breaking=[],
)

# SQL text never reads as a declaration: the field search runs over the
# declaration block with the SQL raw string's content removed, so an
# origin-shaped line embedded in executed SQL satisfies nothing.
SQL_IMPERSONATION_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 115,\n\t\tName: \"sql_impersonation\",\n"
    "\t\tSQL: `ALTER TABLE existing ADD COLUMN c TEXT NOT NULL DEFAULT '';\n"
    "INSERT INTO notes(body) VALUES('\n"
    '\t\tFoldMaintained: "origin",\n'
    "\t\t');`,\n\t},\n"
    "}\n"
)
expect_evaluate(
    "a declaration-shaped SQL line satisfies nothing",
    check.migrations(SQL_IMPERSONATION_SOURCE),
    failures=1,
    breaking=[],
)

# The combined bypass: a real advance field behind a block comment plus a
# decoy origin line inside the SQL. The real declaration governs, so rule
# (b) fires and the entry breaks an older binary.
COMBINED_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 116,\n\t\tName: \"combined\",\n"
    '\t\t/* signed claim */ FoldMaintained: "advance",\n'
    "\t\tSQL: `ALTER TABLE existing ADD COLUMN c TEXT NOT NULL DEFAULT '';\n"
    "INSERT INTO notes(body) VALUES('\n"
    '\t\tFoldMaintained: "origin",\n'
    "\t\t');`,\n\t},\n"
    "}\n"
)
expect_evaluate(
    "a block-commented advance still forces the breaking declaration",
    check.migrations(COMBINED_SOURCE),
    failures=1,
    breaking=[116],
)

# A field line commented out is absent, not declared.
COMMENTED_OUT_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 117,\n\t\tName: \"commented_out\",\n"
    '\t\t/* FoldMaintained: "origin",\n'
    "\t\t*/\n"
    f"\t\tSQL: `{ADD_COLUMN_SQL}`,\n\t}},\n"
    "}\n"
)
expect_evaluate(
    "a commented-out field line reads as undeclared",
    check.migrations(COMMENTED_OUT_SOURCE),
    failures=1,
    breaking=[],
)

# A raw-string Name cannot impersonate or override the declaration: literal
# contents are one token and never a field.
RAW_NAME_IMPERSONATION_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 118,\n"
    "\t\tName: `probe\n"
    '\t\tFoldMaintained: "origin",\n'
    "\t\t`,\n"
    f"\t\tSQL: `{ADD_COLUMN_SQL}`,\n\t}},\n"
    "}\n"
)
expect_evaluate(
    "a declaration-shaped line inside a raw Name satisfies nothing",
    check.migrations(RAW_NAME_IMPERSONATION_SOURCE),
    failures=1,
    breaking=[],
)
RAW_NAME_OVERRIDE_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 119,\n"
    "\t\tName: `probe\n"
    '\t\tFoldMaintained: "origin",\n'
    "\t\t`,\n"
    '\t\tFoldMaintained: "advance",\n'
    f"\t\tSQL: `{ADD_COLUMN_SQL}`,\n\t}},\n"
    "}\n"
)
expect_evaluate(
    "a real advance behind a decoy raw Name still forces the breaking declaration",
    check.migrations(RAW_NAME_OVERRIDE_SOURCE),
    failures=1,
    breaking=[119],
)

# Field layout the line-anchored search missed: a comment between the field
# name and its colon, and two fields sharing a line.
COMMENT_BEFORE_COLON_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 120,\n\t\tName: \"comment_before_colon\",\n"
    '\t\tFoldMaintained /* signed claim */: "later",\n'
    "\t\tSQL: `SELECT 1;`,\n\t},\n"
    "}\n"
)
INLINE_FIELD_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 121,\n"
    '\t\tName: "inline", FoldMaintained: "later",\n'
    "\t\tSQL: `SELECT 1;`,\n\t},\n"
    "}\n"
)
for name, source in (
    ("comment between name and colon", COMMENT_BEFORE_COLON_SOURCE),
    ("inline field on a shared line", INLINE_FIELD_SOURCE),
):
    parsed_folds(f"{name} parses", source, ["later"])
    expect_evaluate(
        f"{name} is refused, not read as absent",
        check.migrations(source),
        failures=2,
        breaking=[],
    )

# A block comment that begins mid-line and ends beside the field must not
# merge the field away: comments carry no line structure of their own.
COMMENT_MERGE_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 122,\n\t\tName: \"merge\",\n"
    "\t\t/* spans\n"
    '\t\tlines */ FoldMaintained: "later",\n'
    "\t\tSQL: `SELECT 1;`,\n\t},\n"
    "}\n"
)
parsed_folds("field after a merged block comment parses", COMMENT_MERGE_SOURCE, ["later"])
expect_evaluate(
    "field after a merged block comment is refused, not read as absent",
    check.migrations(COMMENT_MERGE_SOURCE),
    failures=2,
    breaking=[],
)

# Breaking gets the same token recognition: a commented true inside a
# breaking migration reads as undeclared and fails closed.
BREAKING_COMMENTED_TRUE_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 123,\n\t\tName: \"commented_true\",\n"
    "\t\tBreaking: /* why */ true,\n"
    "\t\tSQL: `DROP TABLE existing;`,\n\t},\n"
    "}\n"
)
expect_evaluate(
    "a commented-true Breaking declaration still counts",
    check.migrations(BREAKING_COMMENTED_TRUE_SOURCE),
    failures=0,
    breaking=[123],
)

# A field name nested inside a value is part of that value, never a
# declaration: an Applies body or a local struct cannot supply FoldMaintained
# or Breaking, and it cannot override a real migration-level claim.
APPLIES_NESTED_FOLD = (
    "Applies: func(context.Context, queryer) (bool, error) {\n"
    '\t\t\tinner := struct{ FoldMaintained string }{FoldMaintained: "origin"}\n'
    '\t\t\treturn inner.FoldMaintained == "origin", nil\n'
    "\t\t},\n"
)
NESTED_FOLD_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 124,\n\t\tName: \"nested_fold\",\n"
    f"\t\t{APPLIES_NESTED_FOLD}"
    f"\t\tSQL: `{ADD_COLUMN_SQL}`,\n\t}},\n"
    "}\n"
)
expect_evaluate(
    "a fold field nested in Applies satisfies nothing",
    check.migrations(NESTED_FOLD_SOURCE),
    failures=1,
    breaking=[],
)
parsed_folds(
    "a fold field nested in Applies is absent at the migration level",
    NESTED_FOLD_SOURCE,
    [""],
)
NESTED_OVERRIDE_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 125,\n\t\tName: \"nested_override\",\n"
    f"\t\t{APPLIES_NESTED_FOLD}"
    '\t\tFoldMaintained: "advance",\n'
    f"\t\tSQL: `{ADD_COLUMN_SQL}`,\n\t}},\n"
    "}\n"
)
expect_evaluate(
    "a real advance behind a nested decoy still forces the breaking declaration",
    check.migrations(NESTED_OVERRIDE_SOURCE),
    failures=1,
    breaking=[125],
)
NESTED_BREAKING_DROP_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 126,\n\t\tName: \"nested_breaking\",\n"
    "\t\tApplies: func(context.Context, queryer) (bool, error) {\n"
    "\t\t\topts := struct{ Breaking bool }{Breaking: true}\n"
    "\t\t\treturn opts.Breaking, nil\n"
    "\t\t},\n"
    "\t\tSQL: `DROP TABLE existing;`,\n\t},\n"
    "}\n"
)
expect_evaluate(
    "a nested Breaking cannot authorize a drop",
    check.migrations(NESTED_BREAKING_DROP_SOURCE),
    failures=1,
    breaking=[126],
)

# A rune literal containing a quote must not start a string: a single
# unpaired quote rune in an Applies body would otherwise swallow the code up
# to the next real string, hiding every field after it.
RUNE_QUOTE_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 127,\n\t\tName: \"rune_quote\",\n"
    "\t\tApplies: func(context.Context, queryer) (bool, error) {\n"
    "\t\t\tsep := '\"'\n"
    "\t\t\treturn true, nil\n"
    "\t\t},\n"
    '\t\tFoldMaintained: "later",\n'
    "\t\tSQL: `SELECT 1;`,\n\t},\n"
    "}\n"
)
expect_evaluate(
    "a quote rune in an Applies body hides nothing after it",
    check.migrations(RUNE_QUOTE_SOURCE),
    failures=2,
    breaking=[],
)

# SQL comes from the token-recognized SQL field: a commented lookalike line
# supplies nothing and stops mattering.
COMMENTED_SQL_IMPERSONATION_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 128,\n\t\tName: \"commented_sql\",\n"
    "\t\t/* SQL: `SELECT 1;`,\n"
    "\t\t*/\n"
    f"\t\tSQL: `{ADD_COLUMN_SQL}`,\n\t}},\n"
    "}\n"
)
expect_evaluate(
    "a commented SQL line supplies nothing",
    check.migrations(COMMENTED_SQL_IMPERSONATION_SOURCE),
    failures=1,
    breaking=[],
)
COMMENTED_SQL_FALSE_REFUSAL_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 129,\n\t\tName: \"commented_sql_origin\",\n"
    "\t\t/* SQL: `SELECT 1;`,\n"
    "\t\t*/\n"
    '\t\tFoldMaintained: "origin",\n'
    f"\t\tSQL: `{ADD_COLUMN_SQL}`,\n\t}},\n"
    "}\n"
)
expect_evaluate(
    "a commented SQL line draws no false refusal beside a real declaration",
    check.migrations(COMMENTED_SQL_FALSE_REFUSAL_SOURCE),
    failures=0,
    breaking=[],
)

# An SQL field that is not one raw literal is refused rather than read.
CONCATENATED_SQL_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 130,\n\t\tName: \"concatenated_sql\",\n"
    '\t\tSQL: "DROP TABLE existing;" + ";",\n\t},\n'
    "}\n"
)
expect_evaluate(
    "an SQL field that is not one raw literal is refused",
    check.migrations(CONCATENATED_SQL_SOURCE),
    failures=1,
    breaking=[],
)

# The entry splitter walks tokens, so field order inside an entry does not
# affect recognition and a commented entry-shaped block supplies nothing.
NAME_FIRST_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tName: \"m131\",\n\t\tVersion: 131,\n"
    "\t\tSQL: `DROP TABLE existing;`,\n\t},\n"
    "}\n"
)
expect_evaluate(
    "an entry whose Version does not lead still evaluates",
    check.migrations(NAME_FIRST_SOURCE),
    failures=1,
    breaking=[131],
)
COMMENTED_ENTRY_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 132,\n\t\tName: \"m132\",\n"
    "\t\tSQL: `SELECT 1;`,\n\t},\n"
    "\t/* {\n\t\tVersion: 999,\n\t\tName: \"fake\",\n\t\tSQL: `SELECT 1;`,\n\t},\n\t*/\n"
    "}\n"
)
expect_evaluate(
    "an entry-shaped comment supplies nothing",
    check.migrations(COMMENTED_ENTRY_SOURCE),
    failures=0,
    breaking=[],
)
NO_VERSION_SOURCE = (
    "var migrations = []migration{\n"
    "\t{\n\t\tName: \"m133\",\n"
    "\t\tSQL: `SELECT 1;`,\n\t},\n"
    "}\n"
)
expect_evaluate(
    "an entry without a readable Version is refused by name",
    check.migrations(NO_VERSION_SOURCE),
    failures=1,
    breaking=[],
)

# The migrations binding is located by its token sequence, so a decoy
# binding inside a comment or a literal never redirects the walk.
DECOY_PREFIX = (
    "// var migrations = []migration{{Version: 1, Name: \"decoy\", SQL: `SELECT 1;`}}\n"
)
REAL_LIST = (
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 134,\n\t\tName: \"m134\",\n"
    "\t\tSQL: `DROP TABLE existing;`,\n\t},\n"
    "}\n"
)
expect_evaluate(
    "a decoy binding in a comment does not hide the real list",
    check.migrations(DECOY_PREFIX + REAL_LIST),
    failures=1,
    breaking=[134],
)
DECOY_STRING_SOURCE = (
    'const doc = "var migrations = []migration{{Version: 1}}"\n\n' + REAL_LIST
)
expect_evaluate(
    "a decoy binding in a string does not hide the real list",
    check.migrations(DECOY_STRING_SOURCE),
    failures=1,
    breaking=[134],
)

# Version literals follow Go integer semantics: 0-leading octal and the
# 0x prefix parse by their base, and anything that is not one integer
# literal is the named unreadable-version refusal.
def version_of(entry_body: str) -> int:
    source = (
        "var migrations = []migration{\n"
        f"\t{{\n{entry_body}"
        "\t\tSQL: `SELECT 1;`,\n\t},\n"
        "}\n"
    )
    return check.migrations(source)[0][0]


if version_of("\t\tVersion: 0156,\n") != 110:
    FAILURES.append("octal version 0156 must parse as 110")
if version_of("\t\tVersion: 0x6e,\n") != 110:
    FAILURES.append("hex version 0x6e must parse as 110")
if version_of("\t\tVersion: 1_10,\n") != 110:
    FAILURES.append("underscore version 1_10 must parse as 110")
if version_of("\t\tVersion: 110.0,\n") != -1:
    FAILURES.append("float version 110.0 must parse as unreadable")
expect_evaluate(
    "a float Version is refused, not crashed on",
    check.migrations(
        "var migrations = []migration{\n"
        "\t{\n\t\tVersion: 110.0,\n\t\tName: \"m135\",\n"
        "\t\tSQL: `SELECT 1;`,\n\t},\n"
        "}\n"
    ),
    failures=1,
    breaking=[],
)

# A local variable named migrations never redirects the walk: only the
# package-level binding is the migration list.
LOCAL_BINDING_PREFIX = (
    "func seed() []migration {\n"
    "\tvar migrations = []migration{\n"
    "\t\t{Version: 1, Name: \"local\", SQL: `SELECT 1;`},\n"
    "\t}\n"
    "\treturn migrations\n"
    "}\n\n"
)
expect_evaluate(
    "a function-local migrations binding does not hide the package list",
    check.migrations(LOCAL_BINDING_PREFIX + REAL_LIST),
    failures=1,
    breaking=[134],
)
CLOSURE_BINDING_PREFIX = (
    "var seed = func() {\n"
    "\tmigrations := []migration{\n"
    "\t\t{Version: 2, Name: \"closure\", SQL: `SELECT 1;`},\n"
    "\t}\n"
    "\t_ = migrations\n"
    "}\n\n"
)
expect_evaluate(
    "a closure-local migrations binding does not hide the package list",
    check.migrations(CLOSURE_BINDING_PREFIX + REAL_LIST),
    failures=1,
    breaking=[134],
)

# The unreadable-Version refusal names the entry however its Name is
# written; a raw-string Name is still a name.
RAW_NAME_UNREADABLE_FAILURES, _ = check.evaluate(
    check.migrations(
        "var migrations = []migration{\n"
        "\t{\n\t\tVersion: 110.0,\n\t\tName: `raw_name`,\n"
        "\t\tSQL: `SELECT 1;`,\n\t},\n"
        "}\n"
    )
)
if not any("raw_name" in failure for failure in RAW_NAME_UNREADABLE_FAILURES):
    FAILURES.append(
        f"a raw-string Name must appear in the unreadable-Version refusal: "
        f"{RAW_NAME_UNREADABLE_FAILURES}"
    )

# The SQL statement scan is literal-aware: '--' inside a value is not a
# comment and cannot hide the column add that follows it.
STRING_MARKER_SQL = (
    "CREATE TABLE notes (body TEXT);\n"
    "INSERT INTO notes VALUES('--');\n"
    "ALTER TABLE existing ADD COLUMN c TEXT DEFAULT '';"
)
expect_evaluate(
    "a string containing -- hides no column add",
    check.migrations(
        "var migrations = []migration{\n"
        "\t{\n\t\tVersion: 136,\n\t\tName: \"m136\",\n"
        f"\t\tSQL: `{STRING_MARKER_SQL}`,\n\t}},\n"
        "}\n"
    ),
    failures=1,
    breaking=[],
)
expect(
    "a semicolon inside a value cuts no statement",
    "INSERT INTO notes VALUES(';');",
    breaking=False,
)

# Quoted, bracketed, qualified, and commented table references are the
# tables they name: the column-add rules see through the spelling.
for label, ref in (
    ("double-quoted", '"existing"'),
    ("bracketed", "[existing]"),
    ("qualified", "main.existing"),
):
    expect_evaluate(
        f"a {label} table reference still requires the fold declaration",
        check.migrations(
            "var migrations = []migration{\n"
            f"\t{{\n\t\tVersion: 137,\n\t\tName: \"m137\",\n"
            f"\t\tSQL: `ALTER TABLE {ref} ADD COLUMN c TEXT DEFAULT '';`,\n\t}},\n"
            "}\n"
        ),
        failures=1,
        breaking=[],
    )
expect_evaluate(
    "a commented ADD COLUMN keyword still requires the fold declaration",
    check.migrations(
        "var migrations = []migration{\n"
        "\t{\n\t\tVersion: 138,\n\t\tName: \"m138\",\n"
        "\t\tSQL: `ALTER TABLE existing ADD /* why */ COLUMN c TEXT DEFAULT '';`,\n\t}},\n"
        "}\n"
    ),
    failures=1,
    breaking=[],
)
expect_evaluate(
    "a quoted table reference with an origin declaration draws no false refusal",
    check.migrations(
        "var migrations = []migration{\n"
        "\t{\n\t\tVersion: 139,\n\t\tName: \"m139\",\n"
        '\t\tFoldMaintained: "origin",\n'
        "\t\tSQL: `ALTER TABLE \"existing\" ADD COLUMN c TEXT DEFAULT '';`,\n\t}},\n"
        "}\n"
    ),
    failures=0,
    breaking=[],
)

# A column named begin is not a trigger-body opener: the statement after it
# still splits and still requires its own declaration.
expect_evaluate(
    "a begin column name hides no later column add",
    check.migrations(
        "var migrations = []migration{\n"
        "\t{\n\t\tVersion: 140,\n\t\tName: \"m140\",\n"
        "\t\tSQL: `CREATE TABLE notes (begin TEXT);"
        "ALTER TABLE existing ADD COLUMN c TEXT DEFAULT '';`,\n\t}},\n"
        "}\n"
    ),
    failures=1,
    breaking=[],
)

# Schema identity survives: a temp table born here never shadows the main
# table a qualified statement destroys.
expect_evaluate(
    "a temp-born table does not authorize dropping the main one",
    check.migrations(
        "var migrations = []migration{\n"
        "\t{\n\t\tVersion: 141,\n\t\tName: \"m141\",\n"
        "\t\tSQL: `CREATE TEMP TABLE existing (a TEXT);"
        "DROP TABLE main.existing;`,\n\t}},\n"
        "}\n"
    ),
    failures=1,
    breaking=[141],
)

# A table born under quoting or qualification is the table a later bare
# statement names: no false declaration refusal on the same identity.
expect_evaluate(
    "a quoted born table needs no fold declaration for its own column",
    check.migrations(
        "var migrations = []migration{\n"
        "\t{\n\t\tVersion: 142,\n\t\tName: \"m142\",\n"
        "\t\tSQL: `CREATE TABLE \"notes\" (a TEXT);"
        "ALTER TABLE main.notes ADD COLUMN c TEXT DEFAULT '';`,\n\t}},\n"
        "}\n"
    ),
    failures=0,
    breaking=[],
)
expect_evaluate(
    "a qualified reference to a born table needs no fold declaration",
    check.migrations(
        "var migrations = []migration{\n"
        "\t{\n\t\tVersion: 143,\n\t\tName: \"m143\",\n"
        "\t\tSQL: `CREATE TABLE notes (a TEXT);"
        "ALTER TABLE main.notes ADD COLUMN c TEXT DEFAULT '';`,\n\t}},\n"
        "}\n"
    ),
    failures=0,
    breaking=[],
)
expect_evaluate(
    "a bracketed born table is the same identity bare",
    check.migrations(
        "var migrations = []migration{\n"
        "\t{\n\t\tVersion: 144,\n\t\tName: \"m144\",\n"
        "\t\tSQL: `CREATE TABLE [notes] (a TEXT);"
        "DROP TABLE notes;`,\n\t}},\n"
        "}\n"
    ),
    failures=0,
    breaking=[],
)

# A table named like the trigger keyword is not a trigger: its BEGIN column
# opens no block and hides no later statement.
expect_evaluate(
    "a trigger_notes table hides no column add",
    check.migrations(
        "var migrations = []migration{\n"
        "\t{\n\t\tVersion: 145,\n\t\tName: \"m145\",\n"
        "\t\tSQL: `CREATE TABLE trigger_notes (begin TEXT);"
        "ALTER TABLE existing ADD COLUMN c TEXT DEFAULT '';`,\n\t}},\n"
        "}\n"
    ),
    failures=1,
    breaking=[],
)

# TEMP carries its schema in every letter case.
expect_evaluate(
    "a lowercase temp table does not authorize dropping the main one",
    check.migrations(
        "var migrations = []migration{\n"
        "\t{\n\t\tVersion: 146,\n\t\tName: \"m146\",\n"
        "\t\tSQL: `create temp table existing (a TEXT);"
        "DROP TABLE main.existing;`,\n\t}},\n"
        "}\n"
    ),
    failures=1,
    breaking=[146],
)

# A born table that this migration drops is gone: a later statement with the
# same spelling reaches the pre-existing table.
expect_evaluate(
    "a dropped born table no longer shadows the pre-existing one",
    check.migrations(
        "var migrations = []migration{\n"
        "\t{\n\t\tVersion: 147,\n\t\tName: \"m147\",\n"
        "\t\tSQL: `CREATE TEMP TABLE existing (a TEXT);"
        "DROP TABLE existing;"
        "ALTER TABLE existing ADD COLUMN c TEXT DEFAULT '';`,\n\t}},\n"
        "}\n"
    ),
    failures=1,
    breaking=[],
)

# A quoted schema qualifier is still that schema.
expect_evaluate(
    "a quoted schema qualifier names the same schema",
    check.migrations(
        "var migrations = []migration{\n"
        "\t{\n\t\tVersion: 148,\n\t\tName: \"m148\",\n"
        '\t\tSQL: `CREATE TABLE "main".notes (a TEXT);'
        "ALTER TABLE main.notes ADD COLUMN c TEXT DEFAULT '';`,\n\t}},\n"
        "}\n"
    ),
    failures=0,
    breaking=[],
)

# SQLite permits ADD without the COLUMN keyword; the declaration duty holds.
expect_evaluate(
    "ADD without the COLUMN keyword still requires the fold declaration",
    check.migrations(
        "var migrations = []migration{\n"
        "\t{\n\t\tVersion: 149,\n\t\tName: \"m149\",\n"
        "\t\tSQL: `ALTER TABLE existing ADD c TEXT DEFAULT '';`,\n\t}},\n"
        "}\n"
    ),
    failures=1,
    breaking=[],
)


# Round-eleven boundaries: trigger grammar from tokens, conditional-create
# ownership under the fold duty, rename lifetime, dotted quoted names, and
# the quoted column without the COLUMN keyword.
for label, sql, failures, breaking in (
    (
        "a quoted table named trigger hides no column add",
        'CREATE TABLE "trigger" (begin TEXT);'
        "ALTER TABLE existing ADD COLUMN c TEXT DEFAULT '';",
        1,
        [],
    ),
    (
        "a bracketed table named trigger hides no column add",
        "CREATE TABLE [trigger] (begin TEXT);"
        "ALTER TABLE existing ADD COLUMN c TEXT DEFAULT '';",
        1,
        [],
    ),
    (
        "a begin column inside a real trigger body hides no later add",
        "CREATE TABLE notes (begin TEXT);"
        "CREATE TRIGGER g AFTER INSERT ON notes FOR EACH ROW BEGIN "
        "SELECT begin FROM notes; END;"
        "ALTER TABLE existing ADD COLUMN c TEXT DEFAULT '';",
        1,
        [],
    ),
    (
        "a conditional create claims no fold ownership",
        "CREATE TABLE IF NOT EXISTS existing (a TEXT);"
        "ALTER TABLE existing ADD COLUMN c TEXT DEFAULT '';",
        1,
        [],
    ),
    (
        "a rename retires the born identity",
        "CREATE TEMP TABLE existing (a TEXT);"
        "ALTER TABLE existing RENAME TO staging;"
        "ALTER TABLE existing ADD COLUMN c TEXT DEFAULT '';",
        1,
        [],
    ),
    (
        "dots inside quoted names keep identities apart",
        'CREATE TABLE "a.b.c" (a TEXT);'
        'ALTER TABLE "a.x.c" ADD COLUMN c TEXT DEFAULT '';',
        1,
        [],
    ),
    (
        "a quoted column without the COLUMN keyword still requires the declaration",
        'ALTER TABLE existing ADD "c" TEXT DEFAULT \'\';',
        1,
        [],
    ),
    (
        "a trigger column name admits no body",
        "CREATE TABLE notes (trigger TEXT, begin TEXT);"
        "ALTER TABLE existing ADD COLUMN c TEXT DEFAULT '';",
        1,
        [],
    ),
    (
        "a table named trigger admits no body",
        "CREATE TABLE trigger (begin TEXT);"
        "ALTER TABLE existing ADD COLUMN c TEXT DEFAULT '';",
        1,
        [],
    ),
    (
        "select aliases named trigger, begin, and end open no body",
        "CREATE TABLE scratch (trigger TEXT, b TEXT);"
        "SELECT trigger, b AS begin, trigger AS end FROM scratch;"
        "ALTER TABLE existing ADD COLUMN c TEXT DEFAULT '';",
        1,
        [],
    ),
    (
        "select aliases hide no destructive statement",
        "CREATE TABLE scratch (trigger TEXT, b TEXT);"
        "SELECT trigger, b AS begin, trigger AS end FROM scratch;"
        "DROP TABLE existing;",
        1,
        [150],
    ),
    (
        "a comment after a trigger END closes the body",
        "CREATE TABLE notes (a TEXT);"
        "CREATE TRIGGER g AFTER INSERT ON notes FOR EACH ROW BEGIN "
        "SELECT 1; END /* guard */;"
        "ALTER TABLE existing ADD COLUMN c TEXT DEFAULT '';",
        1,
        [],
    ),
    (
        "a line comment after a trigger END closes the body",
        "CREATE TABLE notes (a TEXT);"
        "CREATE TRIGGER g AFTER INSERT ON notes FOR EACH ROW BEGIN "
        "SELECT 1; END -- guard\n;"
        "ALTER TABLE existing ADD COLUMN c TEXT DEFAULT '';",
        1,
        [],
    ),
    (
        "spaces around the qualifier dot keep the reference qualified",
        "CREATE TABLE main (a TEXT);"
        "ALTER TABLE main . existing ADD COLUMN c TEXT DEFAULT '';",
        1,
        [],
    ),
    (
        "an escaped-quote name is its own identity",
        'CREATE TABLE "a""b" (a TEXT);'
        "ALTER TABLE a ADD COLUMN c TEXT DEFAULT '';",
        1,
        [],
    ),
    (
        "an escaped-quote name is one identity under its own form",
        'CREATE TABLE "a""b" (a TEXT);'
        'ALTER TABLE main."a""b" ADD COLUMN c TEXT DEFAULT \'\';',
        0,
        [],
    ),
    (
        "dots inside single-quoted names keep identities apart",
        "CREATE TABLE 'a.b.c' (a TEXT);"
        "ALTER TABLE 'a.x.c' ADD COLUMN c TEXT DEFAULT '';",
        1,
        [],
    ),
    (
        "a single-quoted name is the same identity as its quoted form",
        "CREATE TABLE 'a.b.c' (a TEXT);"
        'ALTER TABLE main."a.b.c" ADD COLUMN c TEXT DEFAULT \'\';',
        0,
        [],
    ),
    (
        "a string-literal table name is the same identity as its bare form",
        "CREATE TABLE 'existing' (a TEXT);"
        "ALTER TABLE main.existing ADD COLUMN c TEXT DEFAULT '';",
        0,
        [],
    ),
    (
        "SQLite case folding keeps non-ASCII name variants apart",
        'CREATE TABLE "Ä" (a TEXT);'
        'ALTER TABLE "ä" ADD COLUMN c TEXT DEFAULT \'\';',
        1,
        [],
    ),
    (
        "the non-ASCII variants collide in neither direction",
        'CREATE TABLE "ä" (a TEXT);'
        'ALTER TABLE "Ä" ADD COLUMN c TEXT DEFAULT \'\';',
        1,
        [],
    ),
    (
        "a mixed-quoting non-ASCII pair stays distinct",
        "CREATE TABLE [K] (a TEXT);"
        "ALTER TABLE 'k' ADD COLUMN c TEXT DEFAULT '';",
        1,
        [],
    ),
    (
        "ASCII case equivalence survives the SQLite folding rule",
        'CREATE TABLE "Kept" (a TEXT);'
        "ALTER TABLE main.kept ADD COLUMN c TEXT DEFAULT '';",
        0,
        [],
    ),
    (
        "a bare unicode suffix hides no column add",
        "CREATE TABLE a\u2603 (a TEXT);"
        "ALTER TABLE a ADD COLUMN c TEXT DEFAULT 0;",
        1,
        [],
    ),
    (
        "a bare emoji suffix hides no column add",
        "CREATE TABLE a\U0001F600 (a TEXT);"
        "ALTER TABLE a ADD COLUMN c TEXT DEFAULT 0;",
        1,
        [],
    ),
    (
        "a combining-mark name is one identifier",
        "CREATE TABLE a\u0308 (a TEXT);"
        "ALTER TABLE a ADD COLUMN c TEXT DEFAULT 0;",
        1,
        [],
    ),
    (
        "a bare unicode name is one identity under its own form",
        "CREATE TABLE a\u2603 (a TEXT);"
        "ALTER TABLE main.a\u2603 ADD COLUMN c TEXT DEFAULT 0;",
        0,
        [],
    ),
    (
        "a carriage return inside a raw literal hides no column add",
        "CREATE TABLE a\rb (a TEXT);"
        "ALTER TABLE a ADD COLUMN c TEXT DEFAULT 0;",
        1,
        [],
    ),
    (
        "a carriage-return name drop classifies as destructive",
        "CREATE TABLE a\rb (a TEXT);"
        "DROP TABLE a;",
        1,
        [150],
    ),
    (
        "a leading unicode space hides no column add",
        "CREATE TABLE \u00a0a (a TEXT);"
        "ALTER TABLE a ADD COLUMN c TEXT DEFAULT 0;",
        1,
        [],
    ),
    (
        "a trailing unicode space hides no column add",
        "CREATE TABLE a\u00a0 (a TEXT);"
        "ALTER TABLE a ADD COLUMN c TEXT DEFAULT 0;",
        1,
        [],
    ),
    (
        "an em-space sibling name hides no column add",
        "CREATE TABLE \u2003a (a TEXT);"
        "ALTER TABLE a ADD COLUMN c TEXT DEFAULT 0;",
        1,
        [],
    ),
    (
        "a narrow-no-break-space sibling name hides no column add",
        "CREATE TABLE \u202fa (a TEXT);"
        "ALTER TABLE a ADD COLUMN c TEXT DEFAULT 0;",
        1,
        [],
    ),
    (
        "a unicode-space sibling drop classifies as destructive",
        "CREATE TABLE \u00a0a (a TEXT);"
        "DROP TABLE a;",
        1,
        [150],
    ),
    (
        "a string-literal table reference still requires the declaration",
        "ALTER TABLE 'existing' ADD COLUMN c TEXT DEFAULT '';",
        1,
        [],
    ),
    (
        "an unqualified reference does not claim a main-born table",
        "CREATE TABLE a (v TEXT);"
        "ALTER TABLE a ADD COLUMN c TEXT DEFAULT 0;",
        1,
        [],
    ),
    (
        "an explicit schema claims the born table",
        "CREATE TABLE a (v TEXT);"
        "ALTER TABLE main.a ADD COLUMN c TEXT DEFAULT 0;",
        0,
        [],
    ),
    (
        "a temp-born table owns its unqualified reference",
        "CREATE TEMP TABLE a (v TEXT);"
        "ALTER TABLE a ADD COLUMN c TEXT DEFAULT 0;",
        0,
        [],
    ),
    (
        "a quoted name may follow the TABLE keyword directly",
        'ALTER TABLE"existing" ADD COLUMN c TEXT DEFAULT 0;',
        1,
        [],
    ),
    (
        "a quoted name may precede the ADD keyword directly",
        'ALTER TABLE "existing"ADD COLUMN c TEXT DEFAULT 0;',
        1,
        [],
    ),
    (
        "a bracketed name may follow the TABLE keyword directly",
        "ALTER TABLE[existing] ADD COLUMN c TEXT DEFAULT 0;",
        1,
        [],
    ),
    (
        "a string-literal name may follow the TABLE keyword directly",
        "ALTER TABLE'existing' ADD COLUMN c TEXT DEFAULT 0;",
        1,
        [],
    ),
    (
        "a quoted column name may follow ADD directly",
        'ALTER TABLE existing ADD"c" TEXT DEFAULT 0;',
        1,
        [],
    ),
    (
        "a bracketed column name may follow ADD directly",
        "ALTER TABLE existing ADD[c] TEXT DEFAULT 0;",
        1,
        [],
    ),
    (
        "a string-literal column name may follow ADD directly",
        "ALTER TABLE existing ADD'c' TEXT DEFAULT 0;",
        1,
        [],
    ),
    (
        "a quoted column name may follow the COLUMN keyword directly",
        'ALTER TABLE existing ADD COLUMN"c" TEXT DEFAULT 0;',
        1,
        [],
    ),
    (
        "an empty quoted table name is a table reference",
        'ALTER TABLE "" ADD COLUMN c TEXT DEFAULT 0;',
        1,
        [],
    ),
    (
        "adjacent quoted names keep their identities",
        'CREATE TABLE"t" (a TEXT);'
        'ALTER TABLE main."t"ADD COLUMN c TEXT DEFAULT 0;',
        0,
        [],
    ),
    (
        "an unqualified rename transfers no main ownership",
        "CREATE TABLE a (v TEXT);"
        "ALTER TABLE a RENAME TO b;"
        "ALTER TABLE main.b ADD COLUMN c TEXT DEFAULT 0;",
        1,
        [],
    ),
    (
        "a qualified rename transfers ownership",
        "CREATE TABLE a (v TEXT);"
        "ALTER TABLE main.a RENAME TO b;"
        "ALTER TABLE main.b ADD COLUMN c TEXT DEFAULT 0;",
        0,
        [],
    ),
):
    expect_evaluate(
        label,
        check.migrations(
            "var migrations = []migration{\n"
            f"\t{{\n\t\tVersion: 150,\n\t\tName: \"m150\",\n"
            f"\t\tSQL: `{sql}`,\n\t}},\n"
            "}\n"
        ),
        failures=failures,
        breaking=breaking,
    )

# The source reader preserves the file's own characters. A universal-newline
# read would erase a raw literal's carriage return before classification and
# pass a name Go never executes, so the read and the value derivation carry
# the language rule between them.
with tempfile.NamedTemporaryFile(
    "w", encoding="utf-8", newline="", delete=False
) as handle:
    handle.write(
        fold_source(
            entry(
                110,
                "CREATE TABLE a\rb (a TEXT);"
                "ALTER TABLE a ADD COLUMN c TEXT DEFAULT 0;",
            )
        )
    )
    cr_path = Path(handle.name)
try:
    with open(cr_path, encoding="utf-8", newline="") as handle:
        cr_source = handle.read()
    expect_evaluate(
        "a carriage-return source keeps its characters through the read",
        check.migrations(cr_source),
        failures=1,
        breaking=[],
    )
finally:
    cr_path.unlink()

# Every SQLite whitespace character after a trigger END closes the body.
# The body's tail scan uses the shared whitespace definition, so a formfeed
# cannot hold the body open and swallow the statements that follow it.
for label, gap in [
    ("space", " "),
    ("tab", "\t"),
    ("newline", "\n"),
    ("formfeed", "\f"),
    ("carriage return", "\r"),
]:
    expect_evaluate(
        f"a {label} after a trigger END closes the body",
        check.migrations(
            "var migrations = []migration{\n"
            "\t{\n\t\tVersion: 150,\n\t\tName: \"m150\",\n"
            f"\t\tSQL: `CREATE TABLE notes (a TEXT);"
            f"CREATE TRIGGER g AFTER INSERT ON notes FOR EACH ROW BEGIN "
            f"SELECT 1; END{gap};"
            f"ALTER TABLE existing ADD COLUMN c TEXT DEFAULT '';`,\n\t}},\n"
            "}\n"
        ),
        failures=1,
        breaking=[],
    )
    expect_evaluate(
        f"a {label} after a trigger END hides no destructive statement",
        check.migrations(
            "var migrations = []migration{\n"
            "\t{\n\t\tVersion: 150,\n\t\tName: \"m150\",\n"
            f"\t\tSQL: `CREATE TABLE notes (a TEXT);"
            f"CREATE TRIGGER g AFTER INSERT ON notes FOR EACH ROW BEGIN "
            f"SELECT 1; END{gap};"
            f"DROP TABLE existing;`,\n\t}},\n"
            "}\n"
        ),
        failures=1,
        breaking=[150],
    )

# The declaration duty survives a Breaking declaration that the recognized
# shape makes pointless: the unclassified-statement reason can no longer
# mask the missing FoldMaintained refusal.
expect_evaluate(
    "breaking masks no declaration duty on an adjacent quoted name",
    check.migrations(
        fold_source(
            entry(
                150,
                'ALTER TABLE"existing" ADD COLUMN c TEXT DEFAULT 0;',
                breaking=True,
            )
        )
    ),
    failures=2,
    breaking=[],
)
expect_evaluate(
    "origin satisfies the duty on a quoted column name after ADD",
    check.migrations(
        fold_source(
            entry(
                150,
                'ALTER TABLE existing ADD"c" TEXT DEFAULT 0;',
                fold="origin",
            )
        )
    ),
    failures=0,
    breaking=[],
)
expect_evaluate(
    "advance with breaking satisfies the duty on a quoted column name",
    check.migrations(
        fold_source(
            entry(
                150,
                'ALTER TABLE existing ADD"c" TEXT DEFAULT 0;',
                breaking=True,
                fold="advance",
            )
        )
    ),
    failures=0,
    breaking=[150],
)
expect_evaluate(
    "origin satisfies the duty on an empty quoted table name",
    check.migrations(
        fold_source(
            entry(150, 'ALTER TABLE "" ADD COLUMN c TEXT DEFAULT 0;', fold="origin")
        )
    ),
    failures=0,
    breaking=[],
)
expect_evaluate(
    "origin satisfies the duty on an adjacent quoted name",
    check.migrations(
        fold_source(
            entry(
                150,
                'ALTER TABLE"existing" ADD COLUMN c TEXT DEFAULT 0;',
                fold="origin",
            )
        )
    ),
    failures=0,
    breaking=[],
)
expect_evaluate(
    "advance with breaking satisfies the duty on an adjacent quoted name",
    check.migrations(
        fold_source(
            entry(
                150,
                'ALTER TABLE"existing" ADD COLUMN c TEXT DEFAULT 0;',
                breaking=True,
                fold="advance",
            )
        )
    ),
    failures=0,
    breaking=[150],
)

# Every top-level list element is accounted for. A named migration value is
# not a composite literal the checker can read, so it is refused rather than
# silently omitted from the four declaration rules.
expect_evaluate(
    "a named migration element is refused, never omitted",
    check.migrations(
        "var hidden = migration{Version: 110, Name: \"hidden_column\", "
        "SQL: `ALTER TABLE existing ADD COLUMN c TEXT DEFAULT 0;`, "
        "FoldMaintained: \"advance\"};\n"
        "var migrations = []migration{\n"
        "\t{\n\t\tVersion: 109,\n\t\tName: \"safe\",\n"
        "\t\tSQL: `SELECT 1;`,\n\t},\n"
        "\thidden,\n"
        "}\n"
    ),
    failures=1,
    breaking=[],
)
expect_evaluate(
    "a mixed literal and reference list keeps both elements visible",
    check.migrations(
        "var tail = migration{Version: 110, Name: \"tail\", "
        "SQL: `SELECT 2;`};\n"
        "var migrations = []migration{\n"
        "\t{\n\t\tVersion: 109,\n\t\tName: \"head\",\n"
        "\t\tSQL: `SELECT 1;`,\n\t},\n"
        "\ttail,\n"
        "}\n"
    ),
    failures=1,
    breaking=[],
)
expect_evaluate(
    "a wrapper call around a literal is refused whole",
    check.migrations(
        "var migrations = []migration{\n"
        "\trewrite(migration{Version: 110, Name: \"wrapped\", "
        "SQL: `SELECT 1;`}),\n"
        "}\n"
    ),
    failures=1,
    breaking=[],
)
expect_evaluate(
    "a mixed literal and call list keeps both elements visible",
    check.migrations(
        "var migrations = []migration{\n"
        "\t{\n\t\tVersion: 109,\n\t\tName: \"head\",\n"
        "\t\tSQL: `SELECT 1;`,\n\t},\n"
        "\trewrite(migration{Version: 110, Name: \"wrapped\", "
        "SQL: `SELECT 2;`}),\n"
        "}\n"
    ),
    failures=1,
    breaking=[],
)
expect_entries(
    "a wrapper call parses as an unsupported entry",
    "var migrations = []migration{\n"
    "\trewrite(migration{Version: 110, Name: \"wrapped\", SQL: `SELECT 1;`}),\n"
    "}\n",
    [-2],
)
expect_evaluate(
    "a suffix call on a literal is refused whole",
    check.migrations(
        "var migrations = []migration{\n"
        "\tmigration{Version: 110, Name: \"suffix\", "
        "SQL: `SELECT 1;`}.rewrite(),\n"
        "}\n"
    ),
    failures=1,
    breaking=[],
)
expect_evaluate(
    "a mixed literal and suffix call keep both elements visible",
    check.migrations(
        "var migrations = []migration{\n"
        "\t{\n\t\tVersion: 109,\n\t\tName: \"head\",\n"
        "\t\tSQL: `SELECT 1;`,\n\t},\n"
        "\tmigration{Version: 110, Name: \"suffix\", "
        "SQL: `SELECT 2;`}.rewrite(),\n"
        "}\n"
    ),
    failures=1,
    breaking=[],
)
expect_entries(
    "a suffix call parses as an unsupported entry",
    "var migrations = []migration{\n"
    "\tmigration{Version: 110, Name: \"suffix\", SQL: `SELECT 1;`}.rewrite(),\n"
    "}\n",
    [-2],
)
expect_entries(
    "a comment between the type name and brace parses",
    "var migrations = []migration{\n"
    "\tmigration /* claim */ {Version: 110, Name: \"typed\", SQL: `SELECT 1;`},\n"
    "}\n",
    [110],
)
expect_entries(
    "a named element parses as an unsupported entry",
    "var hidden = migration{Version: 110, Name: \"hidden_column\", SQL: `SELECT 1;`};\n"
    "var migrations = []migration{\n"
    "\t{\n\t\tVersion: 109,\n\t\tName: \"safe\",\n"
    "\t\tSQL: `SELECT 1;`,\n\t},\n"
    "\thidden,\n"
    "}\n",
    [109, -2],
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
expect(
    "dropped table with a distinct single-quoted name",
    "CREATE TABLE 'a.b.c' (a TEXT);DROP TABLE 'a.x.c';",
    breaking=True,
)
expect(
    "dropped own single-quoted born table",
    "CREATE TABLE 'a.b.c' (a TEXT);DROP TABLE 'a.b.c';",
    breaking=False,
)
expect(
    "dropped table with a distinct non-ASCII name",
    'CREATE TABLE "Ä" (a TEXT);DROP TABLE "ä";',
    breaking=True,
)
expect(
    "dropped own non-ASCII born table",
    'CREATE TABLE "Ä" (a TEXT);DROP TABLE "Ä";',
    breaking=False,
)
expect(
    "dropped table with a bare unicode sibling",
    "CREATE TABLE a\u2603 (a TEXT);DROP TABLE a;",
    breaking=True,
)
expect(
    "dropped own bare unicode born table",
    "CREATE TABLE a\u2603 (a TEXT);DROP TABLE a\u2603;",
    breaking=False,
)
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


# --- CON-488: same-shape CHECK-widening rebuilds -----------------------------
# Widening a CHECK in SQLite needs the rebuild shape: rename the table away,
# recreate it under its old name, copy the rows losslessly, drop the scratch
# copy, and recreate every index, trigger, and view the table carried. The
# classifier admits that shape as additive only against a structurally proved
# before-world; everything it cannot prove stays breaking. The fixture table
# is WITHOUT ROWID because a plain rowid table's implicit rowids are identity
# the column copy does not carry: rows the old shape addressed at their
# rowids would answer at fresh ones, so only a rowid-alias column or a
# WITHOUT ROWID shape proves the copy lossless (pinned in the identity
# regressions below).

REBUILD_TRIGGER_SQL = (
    "CREATE TRIGGER attempts_guard BEFORE INSERT ON attempts FOR EACH ROW "
    "BEGIN SELECT RAISE(ABORT, 'attempts is fold-only') "
    "WHERE NEW.id = 'forbidden'; END;"
)

REBUILD_BASE_SQL = (
    "CREATE TABLE attempts ("
    "id TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'open' "
    "CHECK(state IN ('open','closed')), "
    "n INTEGER NOT NULL CHECK(n >= 0), PRIMARY KEY(id)) WITHOUT ROWID;"
    "CREATE INDEX attempts_state ON attempts(state);"
    + REBUILD_TRIGGER_SQL
)

REBUILD_CREATE_TEMPLATE = (
    "CREATE TABLE attempts ("
    "id TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'open' {state}, "
    "n INTEGER NOT NULL {n}, PRIMARY KEY(id)) WITHOUT ROWID;"
)

REBUILD_COPY = (
    "INSERT INTO attempts (id, state, n) "
    "SELECT id, state, n FROM attempts_v108;"
)


def rebuild_sql(
    *,
    state_check="CHECK(state IN ('open','closed','archived'))",
    n_check="CHECK(n >= 0)",
    create=REBUILD_CREATE_TEMPLATE,
    copy=REBUILD_COPY,
    index_sql="CREATE INDEX attempts_state ON attempts(state);",
    trigger_sql=REBUILD_TRIGGER_SQL,
    prelude="DROP TRIGGER IF EXISTS attempts_guard;",
    after_rename=(),
    after_copy=(),
    extra=(),
):
    parts = [
        prelude,
        "ALTER TABLE attempts RENAME TO attempts_v108;",
        *after_rename,
        create.format(state=state_check, n=n_check),
        copy,
        *after_copy,
        "DROP TABLE attempts_v108;",
        index_sql,
        trigger_sql,
        *extra,
    ]
    return "\n".join(part for part in parts if part)


def rebuild_entries(*, base=REBUILD_BASE_SQL, base_version=108, rebuild=None,
                    rebuild_version=109):
    sql = rebuild_sql() if rebuild is None else rebuild
    entries = [entry(base_version, base)] if base else []
    entries.append(entry(rebuild_version, sql))
    return check.migrations(fold_source(*entries))


expect_evaluate(
    "a supported CHECK-widening rebuild stays additive",
    rebuild_entries(),
    failures=0,
    breaking=[],
)
expect_evaluate(
    "a supported numeric bound widening stays additive",
    rebuild_entries(rebuild=rebuild_sql(n_check="CHECK(n >= -5)")),
    failures=0,
    breaking=[],
)
expect_evaluate(
    "a removed CHECK widens",
    rebuild_entries(rebuild=rebuild_sql(n_check="")),
    failures=0,
    breaking=[],
)
expect_evaluate(
    "an unchanged same-shape rebuild stays additive",
    rebuild_entries(
        rebuild=rebuild_sql(
            state_check="CHECK(state IN ('open','closed'))",
            n_check="CHECK(n >= 0)",
        )
    ),
    failures=0,
    breaking=[],
)


# Fail closed: narrowing, novelty, and shape the supported family cannot prove.
expect_evaluate(
    "a narrowed CHECK bound stays breaking",
    rebuild_entries(rebuild=rebuild_sql(n_check="CHECK(n >= 10)")),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "a novel CHECK stays breaking",
    rebuild_entries(
        rebuild=rebuild_sql(
            state_check=(
                "CHECK(state IN ('open','closed','archived')), "
                "CHECK(length(state) > 0)"
            )
        )
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "a compound CHECK widening stays breaking",
    rebuild_entries(
        rebuild=rebuild_sql(
            n_check=(
                "CHECK((n >= 0 AND state IN ('open')) OR "
                "(n >= -1 AND state IN ('closed','archived')))"
            )
        )
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "an altered column default stays breaking",
    rebuild_entries(
        rebuild=rebuild_sql(
            create=REBUILD_CREATE_TEMPLATE.replace(
                "DEFAULT 'open'", "DEFAULT 'archived'")
        )
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "an altered column type stays breaking",
    rebuild_entries(
        rebuild=rebuild_sql(
            create=REBUILD_CREATE_TEMPLATE.replace(
                "n INTEGER NOT NULL {n}", "n TEXT NOT NULL {n}")
        )
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "an added column stays breaking",
    rebuild_entries(
        rebuild=rebuild_sql(
            create=REBUILD_CREATE_TEMPLATE.replace(
                "PRIMARY KEY(id)) WITHOUT ROWID;",
                "probe TEXT NOT NULL DEFAULT '', PRIMARY KEY(id)) WITHOUT ROWID;",
            ),
            copy=(
                "INSERT INTO attempts (id, state, n, probe) "
                "SELECT id, state, n, '' FROM attempts_v108;"
            ),
        )
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "a reordered column stays breaking",
    rebuild_entries(
        rebuild=rebuild_sql(
            create=(
                "CREATE TABLE attempts ("
                "n INTEGER NOT NULL CHECK(n >= 0), id TEXT NOT NULL, "
                "state TEXT NOT NULL DEFAULT 'open' "
                "CHECK(state IN ('open','closed','archived')), "
                "PRIMARY KEY(id)) WITHOUT ROWID;"
            ),
            copy=(
                "INSERT INTO attempts (n, id, state) "
                "SELECT n, id, state FROM attempts_v108;"
            ),
        )
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "a filtered copy stays breaking",
    rebuild_entries(
        rebuild=rebuild_sql(
            copy=(
                "INSERT INTO attempts (id, state, n) "
                "SELECT id, state, n FROM attempts_v108 WHERE n > 0;"
            )
        )
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "an INSERT OR IGNORE copy stays breaking",
    rebuild_entries(
        rebuild=rebuild_sql(
            copy=(
                "INSERT OR IGNORE INTO attempts (id, state, n) "
                "SELECT id, state, n FROM attempts_v108;"
            )
        )
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "a short copy list stays breaking",
    rebuild_entries(
        rebuild=rebuild_sql(
            copy=(
                "INSERT INTO attempts (id, state) "
                "SELECT id, state FROM attempts_v108;"
            )
        )
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "a computed copy stays breaking",
    rebuild_entries(
        rebuild=rebuild_sql(
            copy=(
                "INSERT INTO attempts (id, state, n) "
                "SELECT substr(id, 1, 8), state, n FROM attempts_v108;"
            )
        )
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "an unrestored index stays breaking",
    rebuild_entries(rebuild=rebuild_sql(index_sql="")),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "an unrestored trigger stays breaking",
    rebuild_entries(rebuild=rebuild_sql(trigger_sql="")),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "an extra trigger stays breaking",
    rebuild_entries(
        rebuild=rebuild_sql(
            extra=(
                "CREATE TRIGGER attempts_probe AFTER INSERT ON attempts "
                "FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'no'); END;",
            )
        )
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "an un-dropped view stays breaking",
    rebuild_entries(
        base=(
            REBUILD_BASE_SQL
            + "CREATE VIEW attempts_open AS SELECT id FROM attempts "
            "WHERE state = 'open';"
        )
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "an unknown baseline stays breaking",
    rebuild_entries(base=None),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "a conditional baseline stays breaking",
    rebuild_entries(
        base=(
            "CREATE TABLE IF NOT EXISTS attempts ("
            "id TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'open' "
            "CHECK(state IN ('open','closed')), "
            "n INTEGER NOT NULL CHECK(n >= 0), PRIMARY KEY(id));"
        )
    ),
    failures=1,
    breaking=[109],
)


# --- CON-488 counterexample regressions --------------------------------------
# Each case here failed against the first cut of the proof: it read a
# strictness step as widening in the narrowing direction, admitted equality
# and text-bound changes whose acceptance sets it cannot order, let writes
# around the copy erase the rows the copy claims to move, ignored inbound
# foreign keys and triggers that a rename rewrites onto the scratch name,
# and missed view references written as string literals.

# n >= 0 to n > 0 cuts the boundary value out: an old-valid write dies.
expect_evaluate(
    "a strictness-narrowed CHECK stays breaking",
    rebuild_entries(rebuild=rebuild_sql(n_check="CHECK(n > 0)")),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "an upper strictness narrowing stays breaking",
    rebuild_entries(
        base=REBUILD_BASE_SQL.replace("CHECK(n >= 0)", "CHECK(n <= 10)"),
        rebuild=rebuild_sql(n_check="CHECK(n < 10)"),
    ),
    failures=1,
    breaking=[109],
)
# The true strictness direction admits exactly the old set plus the boundary.
expect_evaluate(
    "an equal-bound strictness widening stays additive",
    rebuild_entries(
        base=REBUILD_BASE_SQL.replace("CHECK(n >= 0)", "CHECK(n > 0)"),
        rebuild=rebuild_sql(n_check="CHECK(n >= 0)"),
    ),
    failures=0,
    breaking=[],
)
expect_evaluate(
    "an upper strictness widening stays additive",
    rebuild_entries(
        base=REBUILD_BASE_SQL.replace("CHECK(n >= 0)", "CHECK(n < 10)"),
        rebuild=rebuild_sql(n_check="CHECK(n <= 10)"),
    ),
    failures=0,
    breaking=[],
)
# Equality and inequality sets are not order-convex: x = 5 against x = 3,
# or x != 5 against x != 3, accept disjoint or incomparable sets whatever
# the bounds do.
expect_evaluate(
    "a changed equality CHECK stays breaking",
    rebuild_entries(
        base=REBUILD_BASE_SQL.replace("CHECK(n >= 0)", "CHECK(n = 5)"),
        rebuild=rebuild_sql(n_check="CHECK(n = 3)"),
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "a changed inequality CHECK stays breaking",
    rebuild_entries(
        base=REBUILD_BASE_SQL.replace("CHECK(n >= 0)", "CHECK(n != 5)"),
        rebuild=rebuild_sql(n_check="CHECK(n != 3)"),
    ),
    failures=1,
    breaking=[109],
)
# Text bounds compare under the column's collation, which the proof cannot
# read; only a verbatim repeat is decidable.
expect_evaluate(
    "a changed text-bound CHECK stays breaking",
    rebuild_entries(
        base=REBUILD_BASE_SQL.replace(
            "CHECK(state IN ('open','closed'))", "CHECK(state < 'm')"
        ),
        rebuild=rebuild_sql(state_check="CHECK(state < 'z')"),
    ),
    failures=1,
    breaking=[109],
)


# --- CON-488 affinity regressions --------------------------------------------
# A bound's meaning comes from the column the CHECK names: SQLite applies
# the column's declared-type affinity to the bound before comparing, so a
# TEXT-affinity column reads n <= 9 against '9' lexicographically, and the
# numeric order 9 < 10 proves nothing ('2' passes n <= 9 and dies on
# n <= 10). The proof therefore binds each atom to its referenced column
# and orders a moved bound only where that column's affinity makes the
# comparison numeric.

TEXT_N_BASE = REBUILD_BASE_SQL.replace(
    "n INTEGER NOT NULL CHECK(n >= 0)", "n TEXT NOT NULL CHECK(n <= 9)"
)
TEXT_N_TEMPLATE = REBUILD_CREATE_TEMPLATE.replace(
    "n INTEGER NOT NULL {n}", "n TEXT NOT NULL {n}"
)
expect_evaluate(
    "a numeric bound widening on a TEXT column stays breaking",
    rebuild_entries(
        base=TEXT_N_BASE,
        rebuild=rebuild_sql(
            create=TEXT_N_TEMPLATE, n_check="CHECK(n <= 10)"
        ),
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "a numeric bound move on a TEXT column stays breaking in both directions",
    rebuild_entries(
        base=TEXT_N_BASE.replace("CHECK(n <= 9)", "CHECK(n >= 5)"),
        rebuild=rebuild_sql(
            create=TEXT_N_TEMPLATE, n_check="CHECK(n >= 0)"
        ),
    ),
    failures=1,
    breaking=[109],
)
# The bound itself never moves in a strictness step, so the loosening needs
# no order at all: it stays decidable for every affinity.
expect_evaluate(
    "an equal-bound strictness loosening on a TEXT column stays additive",
    rebuild_entries(
        base=TEXT_N_BASE.replace("CHECK(n <= 9)", "CHECK(n < 9)"),
        rebuild=rebuild_sql(
            create=TEXT_N_TEMPLATE, n_check="CHECK(n <= 9)"
        ),
    ),
    failures=0,
    breaking=[],
)
expect_evaluate(
    "an equal-bound text loosening on a TEXT column stays additive",
    rebuild_entries(
        base=REBUILD_BASE_SQL.replace(
            "CHECK(state IN ('open','closed'))", "CHECK(state < 'm')"
        ),
        rebuild=rebuild_sql(state_check="CHECK(state <= 'm')"),
    ),
    failures=0,
    breaking=[],
)
expect_evaluate(
    "a bound type change on a numeric column stays breaking",
    rebuild_entries(rebuild=rebuild_sql(n_check="CHECK(n >= '0')")),
    failures=1,
    breaking=[109],
)
# A column with no declared type has BLOB affinity: text and blobs sort
# above every number, so either bound rejects them and the numeric
# subset ordering holds.
expect_evaluate(
    "a numeric bound widening on an untyped column stays additive",
    rebuild_entries(
        base=REBUILD_BASE_SQL.replace(
            "n INTEGER NOT NULL CHECK(n >= 0)", "n CHECK(n <= 9)"
        ),
        rebuild=rebuild_sql(
            create=REBUILD_CREATE_TEMPLATE.replace(
                "n INTEGER NOT NULL {n}", "n {n}"
            ),
            n_check="CHECK(n <= 10)",
        ),
    ),
    failures=0,
    breaking=[],
)
# A growing membership list stays a superset of the listed values whatever
# affinity compares the elements.
expect_evaluate(
    "a growing numeric membership on a TEXT column stays additive",
    rebuild_entries(
        base=TEXT_N_BASE.replace("CHECK(n <= 9)", "CHECK(n IN (1,2))"),
        rebuild=rebuild_sql(
            create=TEXT_N_TEMPLATE, n_check="CHECK(n IN (1,2,3))"
        ),
    ),
    failures=0,
    breaking=[],
)
# Table-level CHECKs bind through the same column lookup.
TABLE_N_BASE = REBUILD_BASE_SQL.replace(
    "n INTEGER NOT NULL CHECK(n >= 0), PRIMARY KEY(id)) WITHOUT ROWID;",
    "n TEXT NOT NULL, PRIMARY KEY(id), CHECK(n <= 9)) WITHOUT ROWID;",
)
TABLE_N_TEMPLATE = REBUILD_CREATE_TEMPLATE.replace(
    "n INTEGER NOT NULL {n}, PRIMARY KEY(id)) WITHOUT ROWID;",
    "n TEXT NOT NULL, PRIMARY KEY(id), {n}) WITHOUT ROWID;",
)
expect_evaluate(
    "a numeric bound widening on a TEXT table CHECK stays breaking",
    rebuild_entries(
        base=TABLE_N_BASE,
        rebuild=rebuild_sql(
            create=TABLE_N_TEMPLATE, n_check="CHECK(n <= 10)"
        ),
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "a numeric bound widening on an INTEGER table CHECK stays additive",
    rebuild_entries(
        base=TABLE_N_BASE.replace(
            "n TEXT NOT NULL", "n INTEGER NOT NULL"
        ).replace("CHECK(n <= 9)", "CHECK(n >= 0)"),
        rebuild=rebuild_sql(
            create=TABLE_N_TEMPLATE.replace(
                "n TEXT NOT NULL", "n INTEGER NOT NULL"
            ),
            n_check="CHECK(n >= -5)",
        ),
    ),
    failures=0,
    breaking=[],
)
# A column-level CHECK can name another column, so the atom binds to the
# column it references, not the column that carries it.
CROSS_CREATE = (
    "CREATE TABLE attempts ("
    "id TEXT NOT NULL {id}, state TEXT NOT NULL DEFAULT 'open' {state}, "
    "{n}, PRIMARY KEY(id)) WITHOUT ROWID;"
)
expect_evaluate(
    "a cross-column widening over a TEXT column stays breaking",
    rebuild_entries(
        base=REBUILD_BASE_SQL.replace(
            "id TEXT NOT NULL,", 'id TEXT NOT NULL CHECK(n <= 5),'
        ).replace("n INTEGER NOT NULL CHECK(n >= 0)", "n TEXT NOT NULL"),
        rebuild=rebuild_sql(
            create=CROSS_CREATE.replace(
                "{id}", "CHECK(n <= 10)"
            ).replace("{n},", "n TEXT NOT NULL,"),
            n_check="",
        ),
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "a cross-column widening over an INTEGER column stays additive",
    rebuild_entries(
        base=REBUILD_BASE_SQL.replace(
            "id TEXT NOT NULL,", "id TEXT NOT NULL CHECK(n <= 5),"
        ),
        rebuild=rebuild_sql(
            create=CROSS_CREATE.replace("{id}", "CHECK(n <= 10)"),
            n_check="n INTEGER NOT NULL CHECK(n >= 0)",
        ),
    ),
    failures=0,
    breaking=[],
)


# --- CON-488 numeric-literal regressions --------------------------------------
# A numeric bound is proved at its SQLite runtime value, not its decimal
# spelling: an integer literal that fits int64 is exact, and every other
# numeric literal is the IEEE-754 double SQLite reads it as. Past 2**53 the
# spelling and the double part company - 9007199254740995.0 rounds up to
# 9007199254740996 - so a spelling-level comparison calls a narrowing an
# equality and admits a rebuild that kills an old-valid row. Membership has
# the same trap through affinity: a TEXT-affinity column renders each literal
# as its own text, so 9 and 9.0 are different members there whatever their
# numeric equality.

expect_evaluate(
    "a REAL-to-INTEGER bound move at the precision boundary stays breaking",
    rebuild_entries(
        base=REBUILD_BASE_SQL.replace(
            "CHECK(n >= 0)", "CHECK(n <= 9007199254740995.0)"
        ),
        rebuild=rebuild_sql(n_check="CHECK(n <= 9007199254740995)"),
    ),
    failures=1,
    breaking=[109],
)

TABLE_REAL_BASE = REBUILD_BASE_SQL.replace(
    "n INTEGER NOT NULL CHECK(n >= 0), PRIMARY KEY(id)) WITHOUT ROWID;",
    "n INTEGER NOT NULL, PRIMARY KEY(id), CHECK(n <= 9007199254740995.0)) "
    "WITHOUT ROWID;",
)
TABLE_REAL_TEMPLATE = REBUILD_CREATE_TEMPLATE.replace(
    "n INTEGER NOT NULL {n}, PRIMARY KEY(id)) WITHOUT ROWID;",
    "n INTEGER NOT NULL, PRIMARY KEY(id), {n}) WITHOUT ROWID;",
)
expect_evaluate(
    "a REAL-to-INTEGER table CHECK move at the precision boundary stays breaking",
    rebuild_entries(
        base=TABLE_REAL_BASE,
        rebuild=rebuild_sql(
            create=TABLE_REAL_TEMPLATE, n_check="CHECK(n <= 9007199254740995)"
        ),
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "a REAL-to-INTEGER strictness move at the precision boundary stays breaking",
    rebuild_entries(
        base=REBUILD_BASE_SQL.replace(
            "CHECK(n >= 0)", "CHECK(n < 9007199254740995.0)"
        ),
        rebuild=rebuild_sql(n_check="CHECK(n <= 9007199254740995)"),
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "a REAL-to-INTEGER membership move at the precision boundary stays breaking",
    rebuild_entries(
        base=REBUILD_BASE_SQL.replace(
            "CHECK(n >= 0)", "CHECK(n IN (9007199254740995.0, 5))"
        ),
        rebuild=rebuild_sql(n_check="CHECK(n IN (9007199254740995, 5, 6))"),
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "a same-value REAL re-spelling on a TEXT column stays breaking",
    rebuild_entries(
        base=TEXT_N_BASE,
        rebuild=rebuild_sql(create=TEXT_N_TEMPLATE, n_check="CHECK(n <= 9.0)"),
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "a same-value REAL re-spelling in a TEXT membership stays breaking",
    rebuild_entries(
        base=TEXT_N_BASE.replace("CHECK(n <= 9)", "CHECK(n IN (9))"),
        rebuild=rebuild_sql(
            create=TEXT_N_TEMPLATE, n_check="CHECK(n IN (9.0, 10))"
        ),
    ),
    failures=1,
    breaking=[109],
)

# The sound directions stay provable: widening the INTEGER spelling to the
# REAL one admits only values the old bound refused, and a REAL bound may
# move to the INTEGER spelling its double equals.
expect_evaluate(
    "an INTEGER-to-REAL bound widening at the precision boundary stays additive",
    rebuild_entries(
        base=REBUILD_BASE_SQL.replace(
            "CHECK(n >= 0)", "CHECK(n <= 9007199254740995)"
        ),
        rebuild=rebuild_sql(n_check="CHECK(n <= 9007199254740995.0)"),
    ),
    failures=0,
    breaking=[],
)
expect_evaluate(
    "a runtime-equal REAL-to-INTEGER bound move stays additive",
    rebuild_entries(
        base=REBUILD_BASE_SQL.replace(
            "CHECK(n >= 0)", "CHECK(n <= 9007199254740995.0)"
        ),
        rebuild=rebuild_sql(n_check="CHECK(n <= 9007199254740996)"),
    ),
    failures=0,
    breaking=[],
)
expect_evaluate(
    "a runtime-equal membership move at the precision boundary stays additive",
    rebuild_entries(
        base=REBUILD_BASE_SQL.replace(
            "CHECK(n >= 0)", "CHECK(n IN (9007199254740995.0, 5))"
        ),
        rebuild=rebuild_sql(n_check="CHECK(n IN (9007199254740996, 5, 6))"),
    ),
    failures=0,
    breaking=[],
)

# A write anywhere beside the rebuild shape is unproved: before the rename
# it erases the rows the copy will claim to move losslessly.
expect_evaluate(
    "a delete before the rename stays breaking",
    rebuild_entries(rebuild=rebuild_sql(prelude="DELETE FROM attempts;")),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "an update before the rename stays breaking",
    rebuild_entries(
        rebuild=rebuild_sql(
            prelude="UPDATE attempts SET state = 'open' WHERE id = 'x';"
        )
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "a pragma before the rename stays breaking",
    rebuild_entries(rebuild=rebuild_sql(prelude="PRAGMA foreign_keys = OFF;")),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "a write between the rename and the recreation stays breaking",
    rebuild_entries(rebuild=rebuild_sql(after_rename=("DELETE FROM attempts_v108;",))),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "a write between the copy and the drop stays breaking",
    rebuild_entries(rebuild=rebuild_sql(after_copy=("DELETE FROM attempts_v108;",))),
    failures=1,
    breaking=[109],
)
# A write that never names the rebuilt table still erases it through a
# trigger on the table it does name.
MEDIATED_BASE = (
    REBUILD_BASE_SQL
    + "CREATE TABLE attempts_log (id TEXT PRIMARY KEY);"
    + "CREATE TRIGGER attempts_log_guard AFTER INSERT ON attempts_log "
    "FOR EACH ROW BEGIN DELETE FROM attempts; END;"
)
expect_evaluate(
    "a trigger-mediated write before the rename stays breaking",
    rebuild_entries(
        base=MEDIATED_BASE,
        rebuild=rebuild_sql(prelude="INSERT INTO attempts_log VALUES ('x');"),
    ),
    failures=1,
    breaking=[109],
)

# A rename rewrites every inbound foreign key and trigger body onto the
# scratch name, and nothing restores them: an inbound reference keeps the
# rebuild breaking.
expect_evaluate(
    "an inbound foreign key stays breaking",
    rebuild_entries(
        base=(
            REBUILD_BASE_SQL
            + "CREATE TABLE attempts_children ("
            "id TEXT PRIMARY KEY, attempts_id TEXT NOT NULL "
            "REFERENCES attempts(id));"
        )
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "an inbound trigger on another table stays breaking",
    rebuild_entries(
        base=(
            REBUILD_BASE_SQL
            + "CREATE TABLE attempts_log (id TEXT PRIMARY KEY);"
            + "CREATE TRIGGER attempts_log_guard AFTER INSERT ON attempts_log "
            "FOR EACH ROW BEGIN INSERT INTO attempts VALUES ('x'); END;"
        )
    ),
    failures=1,
    breaking=[109],
)

# SQLite accepts a string literal where an identifier is expected, and a
# rename rewrites FROM 'attempts' onto the scratch name, so a single-quoted
# reference is a reference: the rebuild must restore such a view.
QUOTED_VIEW_SQL = (
    "CREATE VIEW attempts_open AS SELECT id FROM 'attempts' "
    "WHERE state = 'open';"
)
expect_evaluate(
    "a single-quoted view reference stays breaking",
    rebuild_entries(base=REBUILD_BASE_SQL + QUOTED_VIEW_SQL),
    failures=1,
    breaking=[109],
)
QUOTED_VIEW_RESTORE_SQL = "\n".join(
    (
        "DROP TRIGGER IF EXISTS attempts_guard;",
        "DROP VIEW IF EXISTS attempts_open;",
        "ALTER TABLE attempts RENAME TO attempts_v108;",
        REBUILD_CREATE_TEMPLATE.format(
            state="CHECK(state IN ('open','closed','archived'))",
            n="CHECK(n >= 0)",
        ),
        REBUILD_COPY,
        "DROP TABLE attempts_v108;",
        "CREATE INDEX attempts_state ON attempts(state);",
        REBUILD_TRIGGER_SQL,
        QUOTED_VIEW_SQL,
    )
)
expect_evaluate(
    "a restored single-quoted view stays additive",
    rebuild_entries(
        base=REBUILD_BASE_SQL + QUOTED_VIEW_SQL,
        rebuild=QUOTED_VIEW_RESTORE_SQL,
    ),
    failures=0,
    breaking=[],
)


def applies_entry(version, sql):
    return (
        f"\t{{\n\t\tVersion: {version},\n\t\tName: \"m{version}\",\n"
        "\t\tApplies: func(ctx context.Context, q queryer) (bool, error) "
        "{ return false, nil },\n"
        f"\t\tSQL: `{sql}`,\n\t}},\n"
    )


expect_evaluate(
    "a rebuild after a conditional migration stays breaking",
    check.migrations(
        fold_source(applies_entry(108, "SELECT 1;"), entry(109, rebuild_sql()))
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "a stray write to the rebuilt table stays breaking",
    rebuild_entries(
        rebuild=rebuild_sql(
            extra=("UPDATE attempts SET state = 'archived' WHERE id = 'x';",)
        )
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "a second rename in one migration stays breaking",
    rebuild_entries(
        rebuild=rebuild_sql(
            extra=("ALTER TABLE attempts RENAME TO attempts_again;",)
        )
    ),
    failures=1,
    breaking=[109],
)
expect_evaluate(
    "a column added to a rebuilt table owes the fold declaration",
    rebuild_entries(
        rebuild=rebuild_sql(
            n_check="CHECK(n >= 10)",
            extra=(
                "ALTER TABLE main.attempts ADD COLUMN probe TEXT NOT NULL "
                "DEFAULT '';",
                "CREATE INDEX attempts_probe ON attempts(probe);",
            ),
        )
    ),
    failures=2,
    breaking=[109],
)


# --- CON-488 identity and replay regressions ---------------------------------
# Five counterexamples the first proof admitted and SQLite convicted. Each
# names an identity or replay fact the shape comparison cannot see: a quoted
# type word changes which comparisons a bound orders, a rename rewrites
# inbound references onto the scratch name, a conditional CREATE over an
# existing dependent is the no-op SQLite runs, an AUTOINCREMENT high-water
# mark lives outside the copied rows, and a rowid table without a rowid
# alias answers copied rows at fresh implicit rowids.

def expect_world(name: str, base: str, sql: str, breaking: bool) -> None:
    world = check.World()
    for statement in check.statements(base):
        world.apply(statement)
    reasons = check.classify(sql, world)
    if bool(reasons) != breaking:
        want = "breaking" if breaking else "additive"
        FAILURES.append(f"{name}: classified {reasons or 'additive'}, want {want}")


def small_rebuild(old_columns: str, new_columns: str) -> str:
    return (
        "ALTER TABLE t RENAME TO scratch; "
        f"CREATE TABLE t ({new_columns}); "
        "INSERT INTO t (id, n) SELECT id, n FROM scratch; "
        "DROP TABLE scratch;"
    )


def small_base(columns: str) -> str:
    return f"CREATE TABLE t ({columns});"


# A quoted type word is the name it quotes: SQLite applies TEXT affinity to
# n "TEXT", so n <= 9 reads against '9' lexicographically and the numeric
# order 9 < 10 proves nothing. The moved bound refuses; the unchanged one
# still admits, which is what ties the refusal to the affinity and not to
# the quoting.
QUOTED_TYPE_BASE = small_base('id INTEGER PRIMARY KEY, n "TEXT" CHECK(n <= 9)')
expect_world(
    "a quoted type name applies its affinity: a moved bound stays breaking",
    QUOTED_TYPE_BASE,
    small_rebuild(
        'id INTEGER PRIMARY KEY, n "TEXT" CHECK(n <= 9)',
        'id INTEGER PRIMARY KEY, n "TEXT" CHECK(n <= 10)',
    ),
    breaking=True,
)
expect_world(
    "a quoted type name with an unchanged bound stays additive",
    QUOTED_TYPE_BASE,
    small_rebuild(
        'id INTEGER PRIMARY KEY, n "TEXT" CHECK(n <= 9)',
        'id INTEGER PRIMARY KEY, n "TEXT" CHECK(n <= 9)',
    ),
    breaking=False,
)
# A quoted column name rides the same rule: the affinity reads the name the
# quotes carry, not the quoting.
expect_world(
    "a quoted column name applies its affinity: a moved bound stays breaking",
    small_base('id INTEGER PRIMARY KEY, "n" TEXT CHECK(n <= 9)'),
    small_rebuild(
        'id INTEGER PRIMARY KEY, "n" TEXT CHECK(n <= 9)',
        'id INTEGER PRIMARY KEY, "n" TEXT CHECK(n <= 10)',
    ),
    breaking=True,
)
# SQLite conviction: the copy itself dies, because '5' <= '10' is false
# under the TEXT affinity the quoted spelling applies.
connection = sqlite3.connect(":memory:")
try:
    connection.executescript(QUOTED_TYPE_BASE)
    connection.execute("INSERT INTO t (id, n) VALUES (1, '5')")
    try:
        connection.executescript(
            small_rebuild(
                'id INTEGER PRIMARY KEY, n "TEXT" CHECK(n <= 9)',
                'id INTEGER PRIMARY KEY, n "TEXT" CHECK(n <= 10)',
            )
        )
    except sqlite3.IntegrityError:
        pass
    else:
        FAILURES.append(
            "the quoted-type rebuild copied a row SQLite refuses "
            "('5' <= '10' is false as text)"
        )
finally:
    connection.close()

# A rename rewrites every inbound foreign key onto the new name. The replay
# carries a foreign reference only as stale text, so a rename an inbound
# foreign key can see poisons the world: the rebuild loses its baseline and
# keeps the breaking classification.
RENAMED_FK_BASE = (
    "CREATE TABLE parent (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0)); "
    "CREATE TABLE child (pid INTEGER REFERENCES parent(id)); "
    "ALTER TABLE parent RENAME TO t;"
)
expect_world(
    "a rename under an inbound foreign key keeps the rebuild breaking",
    RENAMED_FK_BASE,
    small_rebuild(
        "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0)",
        "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1)",
    ),
    breaking=True,
)
# SQLite conviction: child's reference followed parent to t, the rebuild's
# rename drove it onto scratch, and the scratch drop stranded it.
connection = sqlite3.connect(":memory:")
try:
    connection.executescript(RENAMED_FK_BASE)
    connection.executescript(
        "INSERT INTO t (id, n) VALUES (1, 0);"
        "INSERT INTO child (pid) VALUES (1);"
    )
    connection.executescript(
        small_rebuild(
            "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0)",
            "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1)",
        )
    )
    stranded = connection.execute("PRAGMA foreign_key_check").fetchall()
    if not stranded:
        FAILURES.append("the renamed-FK rebuild left no stranded reference")
finally:
    connection.close()

# A conditional CREATE over an existing dependent is the no-op SQLite runs:
# the old definition survives, so a view that reads t keeps reading t even
# after a conditional re-declaration over other. The rebuild must restore
# the definition SQLite kept, and one that does not stays breaking.
CONDITIONAL_VIEW_BASE = (
    "CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0)); "
    "CREATE TABLE other (id INTEGER PRIMARY KEY); "
    "CREATE VIEW v AS SELECT id FROM t; "
    "CREATE VIEW IF NOT EXISTS v AS SELECT id FROM other;"
)
expect_world(
    "a conditional view no-op keeps the old definition the rebuild must restore",
    CONDITIONAL_VIEW_BASE,
    small_rebuild(
        "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0)",
        "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1)",
    ),
    breaking=True,
)
expect_world(
    "a rebuild that drops and restores the no-op view stays additive",
    CONDITIONAL_VIEW_BASE,
    "\n".join(
        (
            "DROP VIEW IF EXISTS v;",
            "ALTER TABLE t RENAME TO scratch;",
            "CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1));",
            "INSERT INTO t (id, n) SELECT id, n FROM scratch;",
            "DROP TABLE scratch;",
            "CREATE VIEW v AS SELECT id FROM t;",
        )
    ),
    breaking=False,
)
# SQLite conviction: without the restore, v still names the table the
# rename drove onto scratch, and the drop killed it.
connection = sqlite3.connect(":memory:")
try:
    connection.executescript(CONDITIONAL_VIEW_BASE)
    connection.execute("INSERT INTO t (id, n) VALUES (1, 0)")
    connection.executescript(
        small_rebuild(
            "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0)",
            "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1)",
        )
    )
    try:
        connection.execute("SELECT * FROM v").fetchall()
    except sqlite3.OperationalError:
        pass
    else:
        FAILURES.append("the conditional no-op view survived the rebuild")
finally:
    connection.close()

# The same namespace rule holds a trigger: a conditional re-declaration over
# a trigger that exists on another table is the no-op SQLite runs, so the
# guard never lands on the rebuilt table and no restore is owed.
CONDITIONAL_TRIGGER_BASE = (
    "CREATE TABLE a (id INTEGER PRIMARY KEY); "
    "CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0)); "
    "CREATE TRIGGER guard AFTER INSERT ON a BEGIN SELECT RAISE(ABORT, 'no'); END; "
    "CREATE TRIGGER IF NOT EXISTS guard AFTER INSERT ON t "
    "BEGIN SELECT RAISE(ABORT, 'no'); END;"
)
expect_world(
    "a conditional trigger no-op lands on neither table",
    CONDITIONAL_TRIGGER_BASE,
    small_rebuild(
        "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0)",
        "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1)",
    ),
    breaking=False,
)
connection = sqlite3.connect(":memory:")
try:
    connection.executescript(CONDITIONAL_TRIGGER_BASE)
    connection.executescript(
        small_rebuild(
            "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0)",
            "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1)",
        )
    )
    if connection.execute(
        "SELECT 1 FROM sqlite_master WHERE name = 'guard'"
    ).fetchone() is None:
        FAILURES.append("the no-op trigger vanished")
    try:
        connection.execute("INSERT INTO a (id) VALUES (2)")
    except sqlite3.IntegrityError:
        pass
    else:
        FAILURES.append("the no-op trigger stopped guarding its own table")
    connection.execute("INSERT INTO t (id, n) VALUES (1, 0)")
finally:
    connection.close()

# An AUTOINCREMENT column's high-water mark lives in sqlite_sequence, not
# in the rows: the rename moves the sequence entry onto the scratch name
# and the scratch drop deletes it. The copy restores every row and still
# resets the next implicit id, so the column refuses the proof.
AUTOINCREMENT_BASE = small_base(
    "id INTEGER PRIMARY KEY AUTOINCREMENT, n INTEGER CHECK(n >= 0)"
)
expect_world(
    "an AUTOINCREMENT column keeps the rebuild breaking",
    AUTOINCREMENT_BASE,
    small_rebuild(
        "id INTEGER PRIMARY KEY AUTOINCREMENT, n INTEGER CHECK(n >= 0)",
        "id INTEGER PRIMARY KEY AUTOINCREMENT, n INTEGER CHECK(n >= -1)",
    ),
    breaking=True,
)
# SQLite conviction: rows 1 and 100 in, 100 deleted, the sequence holds
# 100; after the proved-shape copy it holds 1 and the next id is 2, not
# 101 - the reuse the refusal prevents.
connection = sqlite3.connect(":memory:")
try:
    connection.executescript(AUTOINCREMENT_BASE)
    connection.executescript(
        "INSERT INTO t (id, n) VALUES (1, 0);"
        "INSERT INTO t (id, n) VALUES (100, 0);"
        "DELETE FROM t WHERE id = 100;"
    )
    before = connection.execute(
        "SELECT seq FROM sqlite_sequence WHERE name = 't'"
    ).fetchone()[0]
    connection.executescript(
        small_rebuild(
            "id INTEGER PRIMARY KEY AUTOINCREMENT, n INTEGER CHECK(n >= 0)",
            "id INTEGER PRIMARY KEY AUTOINCREMENT, n INTEGER CHECK(n >= -1)",
        )
    )
    after = connection.execute(
        "SELECT seq FROM sqlite_sequence WHERE name = 't'"
    ).fetchone()[0]
    connection.execute("INSERT INTO t (n) VALUES (0)")
    next_id = connection.execute("SELECT max(id) FROM t").fetchone()[0]
    if not (before == 100 and after == 1 and next_id == 2):
        FAILURES.append(
            "AUTOINCREMENT state: "
            f"before {before}, after {after}, next id {next_id}"
        )
finally:
    connection.close()

# A rowid table without a rowid alias answers copied rows at fresh implicit
# rowids: a row the old shape addressed as 42 the new shape answers at 1.
# Only an alias column the copy writes by name proves the copy lossless.
ROWID_BASE = small_base("id TEXT PRIMARY KEY, n INTEGER CHECK(n >= 0)")
expect_world(
    "a rowid table without an alias keeps the rebuild breaking",
    ROWID_BASE,
    small_rebuild(
        "id TEXT PRIMARY KEY, n INTEGER CHECK(n >= 0)",
        "id TEXT PRIMARY KEY, n INTEGER CHECK(n >= -1)",
    ),
    breaking=True,
)
# SQLite conviction: rowid 42 comes back as 1.
connection = sqlite3.connect(":memory:")
try:
    connection.executescript(ROWID_BASE)
    connection.execute("INSERT INTO t (rowid, id, n) VALUES (42, 'old', 0)")
    connection.executescript(
        small_rebuild(
            "id TEXT PRIMARY KEY, n INTEGER CHECK(n >= 0)",
            "id TEXT PRIMARY KEY, n INTEGER CHECK(n >= -1)",
        )
    )
    rowid = connection.execute("SELECT rowid FROM t").fetchone()[0]
    if rowid != 1:
        FAILURES.append(f"implicit rowid moved to {rowid}, expected 1")
finally:
    connection.close()

# The documented alias spellings prove the copy lossless. Inline
# INTEGER PRIMARY KEY and INTEGER PRIMARY KEY ASC alias the rowid, and a
# single-column table-level PRIMARY KEY over the INTEGER column aliases it
# with ASC, DESC, or neither; the inline DESC spelling is the documented
# exception and aliases nothing, so it refuses.
expect_world(
    "an inline rowid-alias widening stays additive",
    small_base("id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0)"),
    small_rebuild(
        "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0)",
        "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1)",
    ),
    breaking=False,
)
expect_world(
    "an inline ASC alias widening stays additive",
    small_base("id INTEGER PRIMARY KEY ASC, n INTEGER CHECK(n >= 0)"),
    small_rebuild(
        "id INTEGER PRIMARY KEY ASC, n INTEGER CHECK(n >= 0)",
        "id INTEGER PRIMARY KEY ASC, n INTEGER CHECK(n >= -1)",
    ),
    breaking=False,
)
expect_world(
    "a table-level DESC alias widening stays additive",
    small_base("id INTEGER, n INTEGER CHECK(n >= 0), PRIMARY KEY(id DESC)"),
    small_rebuild(
        "id INTEGER, n INTEGER CHECK(n >= 0), PRIMARY KEY(id DESC)",
        "id INTEGER, n INTEGER CHECK(n >= -1), PRIMARY KEY(id DESC)",
    ),
    breaking=False,
)
expect_world(
    "an inline DESC alias is no alias: the rebuild stays breaking",
    small_base("id INTEGER PRIMARY KEY DESC, n INTEGER CHECK(n >= 0)"),
    small_rebuild(
        "id INTEGER PRIMARY KEY DESC, n INTEGER CHECK(n >= 0)",
        "id INTEGER PRIMARY KEY DESC, n INTEGER CHECK(n >= -1)",
    ),
    breaking=True,
)
# SQLite conviction for the admitted alias shape: rowids the operator chose
# survive the copy, and old-shaped writes keep working.
connection = sqlite3.connect(":memory:")
try:
    connection.executescript(
        small_base("id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0)")
    )
    connection.executescript(
        "INSERT INTO t (id, n) VALUES (5, 0); INSERT INTO t (id, n) VALUES (42, 0);"
    )
    connection.executescript(
        small_rebuild(
            "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0)",
            "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1)",
        )
    )
    rows = connection.execute("SELECT rowid, id FROM t ORDER BY rowid").fetchall()
    if rows != [(5, 5), (42, 42)]:
        FAILURES.append(f"alias rowids not preserved: {rows}")
    connection.execute("INSERT INTO t (n) VALUES (0)")
    next_id = connection.execute("SELECT max(id) FROM t").fetchone()[0]
    if next_id != 43:
        FAILURES.append(f"alias next id {next_id}, expected 43")
finally:
    connection.close()


# The rebuild shape must survive real SQLite: rows are preserved, old-shaped
# reads and writes keep working, the widened CHECK accepts what the old one
# refused, and the restored trigger still guards writes.
REBUILD_ROUNDTRIP_BASE = (
    "CREATE TABLE attempts ("
    "id TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'open' "
    "CHECK(state IN ('open','closed')), "
    "n INTEGER NOT NULL CHECK(n >= 0), PRIMARY KEY(id)) WITHOUT ROWID;"
    "CREATE INDEX attempts_state ON attempts(state);"
    + REBUILD_TRIGGER_SQL
)


def rebuild_roundtrip(name, *, rebuild, probes, insert=(
        "INSERT INTO attempts (id, state, n) VALUES ('a', 'open', 1), "
        "('b', 'closed', 0);"), base=REBUILD_ROUNDTRIP_BASE):
    connection = sqlite3.connect(":memory:")
    try:
        connection.executescript(base)
        connection.execute(insert)
        connection.executescript(rebuild)
        probes(connection, name)
    finally:
        connection.close()


def widening_probes(connection, name):
    rows = connection.execute(
        "SELECT id, state, n FROM attempts ORDER BY id;"
    ).fetchall()
    if rows != [("a", "open", 1), ("b", "closed", 0)]:
        FAILURES.append(f"{name}: rows not preserved: {rows}")
    connection.execute("SELECT id FROM attempts;").fetchall()
    connection.execute(
        "INSERT INTO attempts (id, state, n) VALUES ('c', 'open', 2);")
    if connection.execute(
        "SELECT 1 FROM pragma_index_list('attempts') "
        "WHERE name = 'attempts_state'"
    ).fetchone() is None:
        FAILURES.append(f"{name}: index not restored")
    try:
        connection.execute(
            "INSERT INTO attempts (id, state, n) VALUES ('d', 'archived', 3);")
    except sqlite3.IntegrityError as err:
        FAILURES.append(f"{name}: widened CHECK refused: {err}")
    try:
        connection.execute(
            "INSERT INTO attempts (id, state, n) VALUES "
            "('forbidden', 'open', 4);")
    except sqlite3.IntegrityError as err:
        if "fold-only" not in str(err):
            FAILURES.append(f"{name}: trigger error changed: {err}")
    else:
        FAILURES.append(f"{name}: restored trigger did not guard")


rebuild_roundtrip(
    "a widening rebuild preserves rows and dependents in SQLite",
    rebuild=rebuild_sql(),
    probes=widening_probes,
)


def narrowing_probes(connection, name):
    # This is why the classifier refuses a narrowed rebuild: SQLite applies
    # it happily, the rows survive, and a write the old shape allowed dies.
    try:
        connection.execute(
            "INSERT INTO attempts (id, state, n) VALUES ('z', 'open', 1);")
    except sqlite3.IntegrityError:
        return
    FAILURES.append(f"{name}: narrowed CHECK admitted an old-shape write")


rebuild_roundtrip(
    "a narrowed rebuild makes old-shape writes fail in SQLite",
    rebuild=rebuild_sql(n_check="CHECK(n >= 10)"),
    probes=narrowing_probes,
    insert=(
        "INSERT INTO attempts (id, state, n) VALUES ('a', 'open', 10), "
        "('b', 'closed', 11);"
    ),
)


def strictness_narrowing_probes(connection, name):
    # The coordinator's counterexample: >= 0 to > 0 drops the boundary
    # value, so a write the old shape accepted dies on the new table.
    try:
        connection.execute(
            "INSERT INTO attempts (id, state, n) VALUES ('z', 'open', 0);")
    except sqlite3.IntegrityError:
        return
    FAILURES.append(
        f"{name}: strictness-narrowed CHECK admitted an old-valid write")


rebuild_roundtrip(
    "a strictness-narrowed rebuild makes an old-valid write fail in SQLite",
    rebuild=rebuild_sql(n_check="CHECK(n > 0)"),
    probes=strictness_narrowing_probes,
    insert=(
        "INSERT INTO attempts (id, state, n) VALUES ('a', 'open', 1), "
        "('b', 'closed', 2);"
    ),
)


def quoted_view_probes(connection, name):
    rows = connection.execute(
        "SELECT id, state, n FROM attempts ORDER BY id;").fetchall()
    if rows != [("a", "open", 1), ("b", "closed", 0)]:
        FAILURES.append(f"{name}: rows not preserved: {rows}")
    if connection.execute(
        "SELECT id FROM attempts_open ORDER BY id;").fetchall() != [("a",)]:
        FAILURES.append(f"{name}: restored view does not read")


rebuild_roundtrip(
    "a restored single-quoted view reads in SQLite",
    rebuild=QUOTED_VIEW_RESTORE_SQL,
    probes=quoted_view_probes,
    base=REBUILD_ROUNDTRIP_BASE + QUOTED_VIEW_SQL,
)


# The affinity trap is a database fact, not a parser quirk: under TEXT
# affinity SQLite reads the bound as text, so the coordinator's rebuild
# cannot even copy the old rows, and the classifier's refusal matches it.
TEXT_ROUNDTRIP_BASE = (
    "CREATE TABLE attempts ("
    "id TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'open' "
    "CHECK(state IN ('open','closed')), "
    "n TEXT NOT NULL CHECK(n <= 9), PRIMARY KEY(id)) WITHOUT ROWID;"
    "CREATE INDEX attempts_state ON attempts(state);"
    + REBUILD_TRIGGER_SQL
)
TEXT_N_ROUNDTRIP_CREATE = REBUILD_CREATE_TEMPLATE.replace(
    "n INTEGER NOT NULL {n}", "n TEXT NOT NULL {n}"
)
TEXT_WIDEN_REBUILD = rebuild_sql(
    create=TEXT_N_ROUNDTRIP_CREATE, n_check="CHECK(n <= 10)"
)
connection = sqlite3.connect(":memory:")
try:
    connection.executescript(TEXT_ROUNDTRIP_BASE)
    connection.execute(
        "INSERT INTO attempts (id, state, n) VALUES ('a', 'open', '2');")
    try:
        connection.executescript(TEXT_WIDEN_REBUILD)
    except sqlite3.IntegrityError as err:
        if "CHECK" not in str(err):
            FAILURES.append(
                f"the TEXT-affinity rebuild failed for the wrong reason: {err}"
            )
    else:
        FAILURES.append(
            "the refused TEXT-affinity rebuild copied an old value in SQLite"
        )
finally:
    connection.close()


# The additive TEXT loosening survives real SQLite: rows written under the
# old shape read back unchanged, old-shaped writes keep working, and the
# boundary value the strict form refused is the only new acceptance.
def text_loosen_probes(connection, name):
    rows = connection.execute(
        "SELECT id, state, n FROM attempts ORDER BY id;"
    ).fetchall()
    if rows != [("a", "open", "2"), ("b", "closed", "8")]:
        FAILURES.append(f"{name}: rows not preserved: {rows}")
    connection.execute(
        "INSERT INTO attempts (id, state, n) VALUES ('c', 'open', '9');")
    connection.execute(
        "INSERT INTO attempts (id, state, n) VALUES ('d', 'closed', '4');")
    try:
        connection.execute(
            "INSERT INTO attempts (id, state, n) VALUES "
            "('e', 'open', '95');")
    except sqlite3.IntegrityError:
        return
    FAILURES.append(
        f"{name}: loosened CHECK admitted past its bound: '95' <= '9'")


rebuild_roundtrip(
    "a TEXT-affinity strictness loosening preserves rows in SQLite",
    rebuild=rebuild_sql(
        create=TEXT_N_ROUNDTRIP_CREATE, n_check="CHECK(n <= 9)"
    ),
    probes=text_loosen_probes,
    base=TEXT_ROUNDTRIP_BASE.replace("CHECK(n <= 9)", "CHECK(n < 9)"),
    insert=(
        "INSERT INTO attempts (id, state, n) VALUES ('a', 'open', '2'), "
        "('b', 'closed', '8');"),
)

# The precision trap is a database fact, not a parser quirk: the old REAL
# bound rounds up to the double 9007199254740996, so 9007199254740996 is
# old-valid, and a rebuild to the INTEGER spelling 9007199254740995 cannot
# even copy that row. The classifier refuses exactly this pair.
PRECISION_NARROW_BASE = REBUILD_ROUNDTRIP_BASE.replace(
    "CHECK(n >= 0)", "CHECK(n <= 9007199254740995.0)"
)
connection = sqlite3.connect(":memory:")
try:
    connection.executescript(PRECISION_NARROW_BASE)
    try:
        connection.execute(
            "INSERT INTO attempts (id, state, n) "
            "VALUES ('a', 'open', 9007199254740996);"
        )
    except sqlite3.IntegrityError as err:
        FAILURES.append(
            "the REAL bound refused the value its double admits "
            f"(9007199254740996): {err}"
        )
    else:
        try:
            connection.executescript(
                rebuild_sql(n_check="CHECK(n <= 9007199254740995)")
            )
        except sqlite3.IntegrityError as err:
            if "check" not in str(err).lower():
                FAILURES.append(
                    "the precision-narrowed rebuild failed for the wrong "
                    f"reason: {err}"
                )
        else:
            FAILURES.append(
                "the refused REAL-to-INTEGER rebuild copied the old-valid "
                "row 9007199254740996 in SQLite"
            )
finally:
    connection.close()


# The sound direction survives SQLite: widening the INTEGER spelling to the
# REAL double admits only values the old bound refused, old-shaped rows and
# writes keep working, the boundary value 9007199254740996 is the new
# acceptance, and the restored index and trigger still hold.
def precision_widen_probes(connection, name):
    rows = connection.execute(
        "SELECT id, state, n FROM attempts ORDER BY id;"
    ).fetchall()
    if rows != [("a", "open", 9007199254740995)]:
        FAILURES.append(f"{name}: rows not preserved: {rows}")
        return
    connection.execute(
        "INSERT INTO attempts (id, state, n) "
        "VALUES ('b', 'open', 9007199254740995);"
    )
    connection.execute(
        "INSERT INTO attempts (id, state, n) "
        "VALUES ('c', 'closed', 9007199254740996);"
    )
    if connection.execute(
        "SELECT 1 FROM pragma_index_list('attempts') "
        "WHERE name = 'attempts_state'"
    ).fetchone() is None:
        FAILURES.append(f"{name}: index not restored")
    try:
        connection.execute(
            "INSERT INTO attempts (id, state, n) "
            "VALUES ('forbidden', 'open', 1);"
        )
    except sqlite3.IntegrityError as err:
        if "fold-only" not in str(err):
            FAILURES.append(f"{name}: trigger error changed: {err}")
    else:
        FAILURES.append(f"{name}: restored trigger did not guard")
    try:
        connection.execute(
            "INSERT INTO attempts (id, state, n) "
            "VALUES ('d', 'open', 9007199254740997);"
        )
    except sqlite3.IntegrityError:
        return
    FAILURES.append(f"{name}: widened CHECK admitted past its bound")


rebuild_roundtrip(
    "a precision-boundary widening preserves rows and dependents in SQLite",
    rebuild=rebuild_sql(n_check="CHECK(n <= 9007199254740995.0)"),
    probes=precision_widen_probes,
    insert=(
        "INSERT INTO attempts (id, state, n) "
        "VALUES ('a', 'open', 9007199254740995);"
    ),
    base=REBUILD_ROUNDTRIP_BASE.replace(
        "CHECK(n >= 0)", "CHECK(n <= 9007199254740995)"
    ),
)


# --- CON-488 retry-11: view and quoted-token regressions ---------------------
# Three unsafe additive admissions the earlier proof carried, each with the
# SQLite fact that convicted it: a view created before its table vanished
# from the replay, DROP TABLE removed surviving views so a conditional
# CREATE VIEW pretended restoration, and whitespace inside double-quoted
# CHECK tokens collapsed, so a narrowed string constraint proved equal.

# SQLite stores CREATE VIEW as text and parses it at query time, so a view
# may precede its table. Nothing attaches such a view to the table, so the
# rename strands it on the scratch name and the scratch drop leaves it
# broken: the rebuild owes it a drop and a restoration.
FORWARD_VIEW_BASE = (
    "CREATE VIEW v AS SELECT id FROM t;"
    "CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0));"
)
expect_world(
    "a view born before its table keeps an unrestored rebuild breaking",
    FORWARD_VIEW_BASE,
    small_rebuild(
        "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0)",
        "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1)",
    ),
    breaking=True,
)
expect_world(
    "a view born before its table, dropped and restored, stays additive",
    FORWARD_VIEW_BASE,
    "\n".join(
        (
            "DROP VIEW IF EXISTS v;",
            "ALTER TABLE t RENAME TO scratch;",
            "CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1));",
            "INSERT INTO t (id, n) SELECT id, n FROM scratch;",
            "DROP TABLE scratch;",
            "CREATE VIEW v AS SELECT id FROM t;",
        )
    ),
    breaking=False,
)
# SQLite conviction: the unrestored forward view reads the dropped scratch
# name after the rebuild.
connection = sqlite3.connect(":memory:")
try:
    connection.executescript(FORWARD_VIEW_BASE)
    connection.executescript(
        small_rebuild(
            "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0)",
            "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1)",
        )
    )
    try:
        connection.execute("SELECT * FROM v").fetchall()
    except sqlite3.OperationalError:
        pass
    else:
        FAILURES.append("the forward view survived the rebuild unbroken")
finally:
    connection.close()

# DROP TABLE leaves surviving views standing, so a conditional CREATE VIEW
# over the stranded view is the no-op SQLite runs: even when it repeats the
# view's exact old text it restores nothing.
CONDITIONAL_REBUILD_BASE = (
    "CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0));"
    "CREATE VIEW IF NOT EXISTS v AS SELECT id FROM t;"
)
CONDITIONAL_REBUILD = "\n".join(
    (
        "ALTER TABLE t RENAME TO scratch;",
        "CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1));",
        "INSERT INTO t (id, n) SELECT id, n FROM scratch;",
        "DROP TABLE scratch;",
        "CREATE VIEW IF NOT EXISTS v AS SELECT id FROM t;",
    )
)
expect_world(
    "a conditional re-creation over a stranded view stays breaking",
    CONDITIONAL_REBUILD_BASE,
    CONDITIONAL_REBUILD,
    breaking=True,
)
expect_world(
    "a stranded view dropped after the scratch drop stays additive",
    CONDITIONAL_REBUILD_BASE,
    "\n".join(
        (
            "ALTER TABLE t RENAME TO scratch;",
            "CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1));",
            "INSERT INTO t (id, n) SELECT id, n FROM scratch;",
            "DROP TABLE scratch;",
            "DROP VIEW IF EXISTS v;",
            "CREATE VIEW IF NOT EXISTS v AS SELECT id FROM t;",
        )
    ),
    breaking=False,
)
# SQLite conviction: after the conditional no-op the view still names the
# dropped scratch name.
connection = sqlite3.connect(":memory:")
try:
    connection.executescript(CONDITIONAL_REBUILD_BASE)
    connection.execute("INSERT INTO t (id, n) VALUES (1, 0)")
    connection.executescript(CONDITIONAL_REBUILD)
    try:
        connection.execute("SELECT * FROM v").fetchall()
    except sqlite3.OperationalError:
        pass
    else:
        FAILURES.append("the conditional re-creation revived the stranded view")
finally:
    connection.close()

# A restored view must repeat its definition byte for byte: a difference
# inside a double-quoted token is a different view text in sqlite_master.
expect_world(
    "a restored view differing inside a quoted token is no restoration",
    CONDITIONAL_REBUILD_BASE,
    "\n".join(
        (
            "ALTER TABLE t RENAME TO scratch;",
            "CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1));",
            "INSERT INTO t (id, n) SELECT id, n FROM scratch;",
            "DROP TABLE scratch;",
            "DROP VIEW IF EXISTS v;",
            'CREATE VIEW IF NOT EXISTS v AS SELECT "id" FROM t;',
        )
    ),
    breaking=True,
)

# A view naming nothing the rebuild touched must survive untouched.
OTHER_TABLE_VIEW_BASE = (
    "CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0));"
    "CREATE TABLE other (id INTEGER PRIMARY KEY);"
    "CREATE VIEW other_open AS SELECT id FROM other;"
)
expect_world(
    "an untouched view on another table survives the rebuild additively",
    OTHER_TABLE_VIEW_BASE,
    small_rebuild(
        "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0)",
        "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1)",
    ),
    breaking=False,
)
expect_world(
    "a dropped-and-unrestored unrelated view keeps the rebuild breaking",
    OTHER_TABLE_VIEW_BASE,
    "DROP VIEW other_open;\n"
    + small_rebuild(
        "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0)",
        "id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1)",
    ),
    breaking=True,
)

# SQLite's one namespace holds tables and dependent names together: a
# rename cannot land on a surviving view's name, and a view cannot take
# the rebuilt table's own name.
expect_world(
    "a rename onto a surviving view's name keeps the rebuild breaking",
    "CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0));"
    "CREATE VIEW reserved AS SELECT id FROM t;",
    "\n".join(
        (
            "ALTER TABLE t RENAME TO reserved;",
            "CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1));",
            "INSERT INTO t (id, n) SELECT id, n FROM reserved;",
            "DROP TABLE reserved;",
        )
    ),
    breaking=True,
)
expect_world(
    "a view over the rebuilt table's own name is no restoration",
    CONDITIONAL_REBUILD_BASE,
    "\n".join(
        (
            "ALTER TABLE t RENAME TO scratch;",
            "CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1));",
            "INSERT INTO t (id, n) SELECT id, n FROM scratch;",
            "DROP TABLE scratch;",
            "DROP VIEW IF EXISTS v;",
            "CREATE VIEW t AS SELECT id FROM v;",
        )
    ),
    breaking=True,
)
# A conditional table creation over a view's name is the no-op SQLite runs,
# so it builds no table a later rename-rebuild can prove against: the view
# survives as a view, and the rebuild of a nonexistent table is refused.
expect_world(
    "a conditional table creation over a view's name proves no rebuild",
    "CREATE VIEW t AS SELECT id FROM base;"
    "CREATE TABLE base (id INTEGER PRIMARY KEY);",
    "\n".join(
        (
            "CREATE TABLE IF NOT EXISTS t "
            "(id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0));",
            "ALTER TABLE t RENAME TO scratch;",
            "CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1));",
            "INSERT INTO t (id, n) SELECT id, n FROM scratch;",
            "DROP TABLE scratch;",
        )
    ),
    breaking=True,
)

# Whitespace inside a double-quoted token is data. SQLite reads a
# double-quoted token that names no identifier as the string it spells, so
# "a  b" and "a b" are different constraints and neither proves the other.
QUOTED_TOKEN_BASE = small_base(
    'id INTEGER PRIMARY KEY, n TEXT CHECK(n = "a  b")'
)
expect_world(
    'a double-quoted CHECK token narrowed inside its quotes stays breaking',
    QUOTED_TOKEN_BASE,
    small_rebuild(
        'id INTEGER PRIMARY KEY, n TEXT CHECK(n = "a  b")',
        'id INTEGER PRIMARY KEY, n TEXT CHECK(n = "a b")',
    ),
    breaking=True,
)
expect_world(
    "an identical double-quoted CHECK token still proves equal",
    QUOTED_TOKEN_BASE,
    small_rebuild(
        'id INTEGER PRIMARY KEY, n TEXT CHECK(n = "a  b")',
        'id INTEGER PRIMARY KEY, n TEXT CHECK(n = "a  b")',
    ),
    breaking=False,
)
# SQLite conviction: the narrowed rebuild cannot even copy the old row,
# because 'a  b' fails CHECK(n = "a b") under the string fallback.
connection = sqlite3.connect(":memory:")
try:
    connection.executescript(QUOTED_TOKEN_BASE)
    connection.execute("INSERT INTO t (id, n) VALUES (1, 'a  b')")
    try:
        connection.executescript(
            small_rebuild(
                'id INTEGER PRIMARY KEY, n TEXT CHECK(n = "a  b")',
                'id INTEGER PRIMARY KEY, n TEXT CHECK(n = "a b")',
            )
        )
    except sqlite3.IntegrityError:
        pass
    else:
        FAILURES.append('the narrowed quoted-token rebuild copied "a  b"')
finally:
    connection.close()

# The restored forward view survives real SQLite: rows are preserved in
# their old shape, old-shaped reads and writes keep working, the widened
# CHECK admits the new state, and the view reads again.
FORWARD_ROUNDTRIP_BASE = (
    "CREATE VIEW attempts_open AS SELECT id FROM attempts WHERE state = 'open';"
    + REBUILD_ROUNDTRIP_BASE
)


def forward_view_probes(connection, name):
    rows = connection.execute(
        "SELECT id, state, n FROM attempts ORDER BY id;"
    ).fetchall()
    if rows != [("a", "open", 1), ("b", "closed", 0)]:
        FAILURES.append(f"{name}: rows not preserved: {rows}")
        return
    if connection.execute(
        "SELECT id FROM attempts_open ORDER BY id;"
    ).fetchall() != [("a",)]:
        FAILURES.append(f"{name}: restored forward view does not read")
    connection.execute(
        "INSERT INTO attempts (id, state, n) VALUES ('c', 'open', 2);"
    )
    try:
        connection.execute(
            "INSERT INTO attempts (id, state, n) VALUES ('d', 'archived', 3);"
        )
    except sqlite3.IntegrityError as err:
        FAILURES.append(f"{name}: widened CHECK refused the new state: {err}")


rebuild_roundtrip(
    "a restored forward view reads and old-shaped writes keep working",
    rebuild="\n".join(
        (
            "DROP VIEW IF EXISTS attempts_open;",
            "DROP TRIGGER IF EXISTS attempts_guard;",
            "ALTER TABLE attempts RENAME TO attempts_v108;",
            REBUILD_CREATE_TEMPLATE.format(
                state="CHECK(state IN ('open','closed','archived'))",
                n="CHECK(n >= 0)",
            ),
            REBUILD_COPY,
            "DROP TABLE attempts_v108;",
            "CREATE INDEX attempts_state ON attempts(state);",
            REBUILD_TRIGGER_SQL,
            "CREATE VIEW attempts_open AS "
            "SELECT id FROM attempts WHERE state = 'open';",
        )
    ),
    probes=forward_view_probes,
    base=FORWARD_ROUNDTRIP_BASE,
)

# --- CON-488 retry-12: escaped tokens and self-referencing renames ------------
# Three unsafe additive admissions the earlier proof carried, each with the
# SQLite fact that convicted it. A doubled quote inside a quoted identifier is
# one character of its name, but the token scanners split it apart, so a
# changed column name and declared type parsed identical to the old one and
# the copy wrote the string literal 'p' where the old value was. An escaped
# table name never met the reference scan's raw-text eye, so a stranded view
# proved restored. And a rename rewrites the renamed table's own foreign key
# onto the new name, which the replay carried as its old text and a later
# proof read as a baseline the real database no longer had.

# A column renamed through an escaped token: "p""q" (the column p"q) and
# "p" "q" (the column p with a quoted type word) split into the same token
# stream, so the proof must refuse what the parser cannot tell apart.
ESCAPED_COLUMN_BASE = small_base(
    'id INTEGER PRIMARY KEY, "p""q" TEXT NOT NULL, n INTEGER CHECK(n >= 0)'
)
ESCAPED_COLUMN_ATTACK = "\n".join(
    (
        "ALTER TABLE t RENAME TO scratch;",
        'CREATE TABLE t (id INTEGER PRIMARY KEY, "p" "q" TEXT NOT NULL, '
        "n INTEGER CHECK(n >= -1));",
        'INSERT INTO t (id, p, n) SELECT id, "p", n FROM scratch;',
        "DROP TABLE scratch;",
    )
)
expect_world(
    "a column renamed through an escaped token stays breaking",
    ESCAPED_COLUMN_BASE,
    ESCAPED_COLUMN_ATTACK,
    breaking=True,
)
# The copy names the new column p, which the scratch table does not carry:
# SQLite falls back to reading "p" as the string literal it spells, so the
# rebuild runs, the column is renamed, and every old value is lost.
connection = sqlite3.connect(":memory:")
try:
    connection.executescript(ESCAPED_COLUMN_BASE)
    connection.execute('INSERT INTO t (id, "p""q", n) VALUES (1, \'real\', 5)')
    connection.executescript(ESCAPED_COLUMN_ATTACK)
    columns = [row[1] for row in connection.execute("PRAGMA table_info(t)")]
    if columns != ["id", "p", "n"]:
        FAILURES.append(
            f"the escaped-token attack kept its columns: {columns}")
    rows = connection.execute("SELECT id, p, n FROM t").fetchall()
    if rows != [(1, "p", 5)]:
        FAILURES.append(
            f"the escaped-token attack kept an old value: {rows}")
finally:
    connection.close()
# The same escape on both sides keeps one identity and still widens.
expect_world(
    "an identically escaped column still widens additively",
    ESCAPED_COLUMN_BASE,
    "\n".join(
        (
            "ALTER TABLE t RENAME TO scratch;",
            'CREATE TABLE t (id INTEGER PRIMARY KEY, "p""q" TEXT NOT NULL, '
            "n INTEGER CHECK(n >= -1));",
            'INSERT INTO t (id, "p""q", n) SELECT id, "p""q", n FROM scratch;',
            "DROP TABLE scratch;",
        )
    ),
    breaking=False,
)

# A semicolon or a comment marker inside an escaped token is content: it
# cuts no statement, opens no comment, and a rebuild carrying the token
# still proves its shape.
ESCAPED_MARKER_BASE = small_base(
    'id INTEGER PRIMARY KEY, d TEXT NOT NULL DEFAULT "a"";--b", '
    "n INTEGER CHECK(n >= 0)"
)
if len(check.statements(
    'CREATE TABLE t (d TEXT NOT NULL DEFAULT "a"";--b");'
)) != 1:
    FAILURES.append("a semicolon inside an escaped token split a statement")
expect_world(
    "an escaped semicolon inside a default cuts no statement",
    ESCAPED_MARKER_BASE,
    "\n".join(
        (
            "ALTER TABLE t RENAME TO scratch;",
            'CREATE TABLE t (id INTEGER PRIMARY KEY, '
            'd TEXT NOT NULL DEFAULT "a"";--b", n INTEGER CHECK(n >= -1));',
            "INSERT INTO t (id, d, n) SELECT id, d, n FROM scratch;",
            "DROP TABLE scratch;",
        )
    ),
    breaking=False,
)

# A table named through an escaped spelling strands a view that names it the
# same way: SQLite rewrites the view onto the scratch name at the rename, so
# the rebuild owes the view a drop and a restoration.
ESCAPED_TABLE_BASE = (
    'CREATE TABLE "t""x" (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0)); '
    'CREATE VIEW v AS SELECT id FROM "t""x";'
)
ESCAPED_TABLE_REBUILD = "\n".join(
    (
        'ALTER TABLE "t""x" RENAME TO scratch;',
        'CREATE TABLE "t""x" (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1));',
        'INSERT INTO "t""x" (id, n) SELECT id, n FROM scratch;',
        "DROP TABLE scratch;",
    )
)
expect_world(
    "an escaped view reference strands like any other",
    ESCAPED_TABLE_BASE,
    ESCAPED_TABLE_REBUILD,
    breaking=True,
)
expect_world(
    "an escaped view reference dropped and restored stays additive",
    ESCAPED_TABLE_BASE,
    "\n".join(
        (
            "DROP VIEW IF EXISTS v;",
            'ALTER TABLE "t""x" RENAME TO scratch;',
            'CREATE TABLE "t""x" (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1));',
            'INSERT INTO "t""x" (id, n) SELECT id, n FROM scratch;',
            "DROP TABLE scratch;",
            'CREATE VIEW v AS SELECT id FROM "t""x";',
        )
    ),
    breaking=False,
)
# SQLite conviction: the unrestored view names the dropped scratch name.
connection = sqlite3.connect(":memory:")
try:
    connection.executescript(ESCAPED_TABLE_BASE)
    connection.executescript(ESCAPED_TABLE_REBUILD)
    try:
        connection.execute("SELECT * FROM v").fetchall()
    except sqlite3.OperationalError:
        pass
    else:
        FAILURES.append("the escaped-name view survived the rebuild unbroken")
finally:
    connection.close()
# An inbound trigger that names the table through an escaped spelling is an
# inbound reference the rename rewrites onto the scratch name.
expect_world(
    "an inbound trigger through an escaped spelling keeps the rebuild breaking",
    'CREATE TABLE "t""x" (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0)); '
    "CREATE TABLE log (id INTEGER PRIMARY KEY); "
    'CREATE TRIGGER g AFTER INSERT ON log BEGIN DELETE FROM "t""x"; END;',
    ESCAPED_TABLE_REBUILD,
    breaking=True,
)
# Escaped spellings ride the whole proof: a backticked name with a doubled
# backtick is one identifier, in the rename, the references, and the proof.
expect_world(
    "a backticked escaped name proves its rebuild",
    "CREATE TABLE `a``b` (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0));",
    "\n".join(
        (
            "ALTER TABLE `a``b` RENAME TO scratch;",
            "CREATE TABLE `a``b` (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1));",
            "INSERT INTO `a``b` (id, n) SELECT id, n FROM scratch;",
            "DROP TABLE scratch;",
        )
    ),
    breaking=False,
)

# SQLite rewrites a renamed table's own foreign key onto the new name, and
# the replay carries the old text: a prior self-referencing rename leaves a
# fictitious baseline, and a proof read from it admits a rebuild whose
# foreign key targets a table that no longer exists.
SELF_FK_RENAME_BASE = (
    "CREATE TABLE parent (id INTEGER PRIMARY KEY, "
    "pid INTEGER REFERENCES parent(id), n INTEGER CHECK(n >= 0)); "
    "ALTER TABLE parent RENAME TO t;"
)
SELF_FK_FICTITIOUS_REBUILD = "\n".join(
    (
        "ALTER TABLE t RENAME TO scratch;",
        "CREATE TABLE t (id INTEGER PRIMARY KEY, "
        "pid INTEGER REFERENCES parent(id), n INTEGER CHECK(n >= -1));",
        "INSERT INTO t (id, pid, n) SELECT id, pid, n FROM scratch;",
        "DROP TABLE scratch;",
    )
)
expect_world(
    "a prior self-referencing rename poisons the baseline",
    SELF_FK_RENAME_BASE,
    SELF_FK_FICTITIOUS_REBUILD,
    breaking=True,
)
# SQLite conviction: migrations run inside one transaction with foreign keys
# enforced, and the copy against the vanished parent table cannot even run.
connection = sqlite3.connect(":memory:")
try:
    connection.executescript(SELF_FK_RENAME_BASE)
    connection.execute("INSERT INTO t (id, pid, n) VALUES (1, NULL, 0)")
    connection.commit()
    connection.execute("PRAGMA foreign_keys = ON")
    if connection.execute("PRAGMA foreign_keys").fetchone() != (1,):
        FAILURES.append("the conviction could not enable foreign keys")
    try:
        connection.executescript(SELF_FK_FICTITIOUS_REBUILD)
    except sqlite3.OperationalError:
        pass
    else:
        FAILURES.append(
            "the fictitious-baseline rebuild copied rows against a "
            "nonexistent parent table"
        )
finally:
    connection.close()
# A self-referencing table rebuilt without any prior rename fails closed
# too: the copy through a self-reference is lossless only when the stored
# rows satisfy the key, and that is data state the shape cannot prove.
expect_world(
    "a self-referencing table keeps its rebuild breaking",
    "CREATE TABLE t (id INTEGER PRIMARY KEY, "
    "pid INTEGER REFERENCES t(id), n INTEGER CHECK(n >= 0));",
    "\n".join(
        (
            "ALTER TABLE t RENAME TO scratch;",
            "CREATE TABLE t (id INTEGER PRIMARY KEY, "
            "pid INTEGER REFERENCES t(id), n INTEGER CHECK(n >= -1));",
            "INSERT INTO t (id, pid, n) SELECT id, pid, n FROM scratch;",
            "DROP TABLE scratch;",
        )
    ),
    breaking=True,
)
# A rename that rewrites nothing stays faithful: the table and its trigger
# name only other tables, the replay carries them truly, and a later
# rebuild of a different table against that world still proves.
expect_world(
    "a rebuild beside a clean prior rename stays additive",
    "CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0)); "
    "CREATE TRIGGER g AFTER INSERT ON t BEGIN SELECT 1; END; "
    "ALTER TABLE t RENAME TO u; "
    "CREATE TABLE other (id INTEGER PRIMARY KEY, m INTEGER CHECK(m >= 0));",
    "\n".join(
        (
            "ALTER TABLE other RENAME TO other_scratch;",
            "CREATE TABLE other (id INTEGER PRIMARY KEY, m INTEGER CHECK(m >= -1));",
            "INSERT INTO other (id, m) SELECT id, m FROM other_scratch;",
            "DROP TABLE other_scratch;",
        )
    ),
    breaking=False,
)

# The reference scan reads whole tokens at their unquoted value, the way
# SQLite rewrites them: a value string that merely contains the name is not
# a reference, so a view carrying one is not stranded by the rename.
expect_world(
    "a view naming the table only inside a longer string stays additive",
    "CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= 0)); "
    "CREATE VIEW notes AS SELECT 'see t here' AS label;",
    "\n".join(
        (
            "ALTER TABLE t RENAME TO scratch;",
            "CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER CHECK(n >= -1));",
            "INSERT INTO t (id, n) SELECT id, n FROM scratch;",
            "DROP TABLE scratch;",
        )
    ),
    breaking=False,
)


# The shipped manifest keeps its recorded compatibility floor: migration 111
# widens a compound CHECK the supported family cannot prove, so it stays
# breaking, and the live check passes without weakening any guard.
with open(check.SCHEMA, encoding="utf-8", newline="") as handle:
    live_source = handle.read()
live_failures, live_breaking = check.evaluate(check.migrations(live_source))
if live_failures:
    FAILURES.append(f"live manifest drew refusals: {live_failures}")
if max(live_breaking, default=0) != 111:
    FAILURES.append(
        f"live compatibility floor moved: {max(live_breaking, default=0)}")
if 110 not in live_breaking or 111 not in live_breaking:
    FAILURES.append(
        f"shipped rebuild migrations left breaking: {live_breaking}")


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
