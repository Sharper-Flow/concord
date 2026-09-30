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
