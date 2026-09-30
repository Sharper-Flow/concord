#!/usr/bin/env python3
"""Refuse a migration whose Breaking declaration its statements contradict.

A database records, per applied migration, whether that migration left an
older binary able to operate it. The highest breaking version applied is the
compatibility floor, and internal/store/schema.go refuses a binary below it.
The floor is only as trustworthy as the declaration, and a declaration a human
sets by hand drifts from the SQL beside it with nothing to catch the drift.

This validator classifies each migration's statements and compares the result
with its declared Breaking field. A migration that removes or renames a schema
object, or constrains an existing one, must declare Breaking: true. One that
only creates objects or adds a nullable or defaulted column must not.

The classification is deliberately conservative: an unrecognized statement
counts as breaking. Being wrong toward breaking costs a refusal that a newer
binary resolves. Being wrong toward additive lets an old binary write against
a shape it does not know, which is silent corruption.

Fold-maintained columns are the additive shape this comparison cannot see.
SQLite reads an added column's rows as its constant default and rejects a
non-constant one, so every ADD COLUMN classifies as additive; whether the
column stays true is decided by the Go fold writer, which the SQL shape does
not show. A fold generation from before the migration never runs that writer,
so under a rolling upgrade (CD-0111 D3) a column the fold advances drifts
behind the log with no convergence, the way migration 108's last_activity_at
did. The migration struct therefore carries a FoldMaintained declaration, and
for every migration above RULE_FLOOR this validator holds that declaration to
its closed vocabulary and to its consequences. The declaration is the
author's signed claim beside the SQL: this checker cannot derive it, only
refuse one that contradicts the SQL's shape or its own terms.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

SCHEMA = Path(__file__).resolve().parent.parent / "internal" / "store" / "schema.go"

# The fold-maintenance rules bind only migrations above this floor. Migration
# 108 was the last fold-maintained column admitted without a declaration; its
# repair, migration 109, is the first version the rules could have bound.
RULE_FLOOR = 108

# A migration entry: its version, then everything up to the next entry.
# gofmt aligns the values in a struct literal, so the run of spaces after a
# field name depends on the longest field name present in that literal. Every
# field pattern here tolerates that alignment rather than pinning one width.
ENTRY = re.compile(r"\n\t\{\n\t\tVersion:\s+(\d+),")
DECLARES_BREAKING = re.compile(r"^\t\tBreaking:\s+true,$", re.MULTILINE)
DECLARES_FOLD = re.compile(r"^\t\tFoldMaintained:[ \t]*(.*)$", re.MULTILINE)
FOLD_VOCABULARY = ("advance", "origin")
# What fold_maintained returns for a field whose value is not on the field's
# own line: present, unreadable, and outside every vocabulary. Text no Go
# value can carry keeps it from colliding with a real residue.
FOLD_VALUE_ELSEWHERE = "<value on a later line>"

CREATE_TABLE = re.compile(
    r"^CREATE\s+(?:VIRTUAL\s+|TEMP\s+|TEMPORARY\s+)*TABLE(?:\s+IF\s+NOT\s+EXISTS)?"
    r"\s+([A-Za-z_][A-Za-z0-9_]*)",
    re.IGNORECASE,
)
DROP_TABLE = re.compile(
    r"^DROP\s+TABLE(?:\s+IF\s+EXISTS)?\s+([A-Za-z_][A-Za-z0-9_]*)", re.IGNORECASE
)
DROP_INDEX = re.compile(
    r"^DROP\s+(?:INDEX|TRIGGER|VIEW)(?:\s+IF\s+EXISTS)?\s+([A-Za-z_][A-Za-z0-9_]*)",
    re.IGNORECASE,
)
ALTER = re.compile(
    r"^ALTER\s+TABLE\s+([A-Za-z_][A-Za-z0-9_]*)\s+([\s\S]*)$", re.IGNORECASE
)
ADD_COLUMN = re.compile(r"^ADD\s+COLUMN\b", re.IGNORECASE)
INDEX_ON = re.compile(
    r"^CREATE\s+(UNIQUE\s+)?INDEX(?:\s+IF\s+NOT\s+EXISTS)?\s+\S+\s+ON\s+([A-Za-z_][A-Za-z0-9_]*)",
    re.IGNORECASE,
)
TRIGGER_ON = re.compile(
    r"^CREATE\s+TRIGGER(?:\s+IF\s+NOT\s+EXISTS)?\s+\S+\s+(?:BEFORE|AFTER|INSTEAD)"
    r"[\s\S]*?\bON\s+([A-Za-z_][A-Za-z0-9_]*)",
    re.IGNORECASE,
)
VIEW_OR_PRAGMA = re.compile(r"^(CREATE\s+VIEW|PRAGMA|ANALYZE|REINDEX)\b", re.IGNORECASE)
WRITE = re.compile(r"^(INSERT|UPDATE|DELETE|SELECT|WITH)\b", re.IGNORECASE)


def statements(sql: str) -> list[str]:
    """Split migration SQL into statements, ignoring comments.

    CREATE TRIGGER bodies contain semicolons, so a bare split would cut them
    apart. A statement therefore ends at a semicolon that is not inside a
    BEGIN...END block.
    """
    sql = re.sub(r"--[^\n]*", "", sql)
    out: list[str] = []
    current: list[str] = []
    depth = 0
    for token in re.split(r"(\bBEGIN\b|\bEND\b|;)", sql, flags=re.IGNORECASE):
        upper = token.upper().strip()
        if upper == "BEGIN":
            depth += 1
        elif upper == "END":
            depth = max(0, depth - 1)
        elif token == ";" and depth == 0:
            statement = "".join(current).strip()
            if statement:
                out.append(statement)
            current = []
            continue
        current.append(token)
    tail = "".join(current).strip()
    if tail:
        out.append(tail)
    return out


def classify(sql: str) -> list[str]:
    """Return the reasons this migration breaks an older binary, if any.

    A table created by this same migration is invisible to an older binary, so
    dropping it, indexing it, or putting a trigger on it breaks nothing.
    """
    born: set[str] = set()
    reasons: list[str] = []
    for statement in statements(sql):
        if not statement:
            continue
        match = CREATE_TABLE.match(statement)
        if match:
            born.add(match.group(1).lower())
            continue
        match = DROP_TABLE.match(statement)
        if match:
            if match.group(1).lower() not in born:
                reasons.append(f"drops the pre-existing table {match.group(1)}")
            continue
        match = DROP_INDEX.match(statement)
        if match:
            continue
        match = ALTER.match(statement)
        if match:
            table, rest = match.group(1), match.group(2).strip()
            if table.lower() in born:
                continue
            if ADD_COLUMN.match(rest):
                continue
            reasons.append(f"alters the pre-existing table {table}: {rest.split()[0].upper()}")
            continue
        match = INDEX_ON.match(statement)
        if match:
            if match.group(1) and match.group(2).lower() not in born:
                reasons.append(
                    f"adds a unique index to the pre-existing table {match.group(2)}"
                )
            continue
        match = TRIGGER_ON.match(statement)
        if match:
            if match.group(1).lower() not in born:
                reasons.append(
                    f"adds a trigger to the pre-existing table {match.group(1)}"
                )
            continue
        if VIEW_OR_PRAGMA.match(statement) or WRITE.match(statement):
            continue
        reasons.append(f"uses an unclassified statement: {statement.split()[0].upper()}")
    return reasons


SQL_LITERAL = re.compile(r"\n\t\tSQL:\s+`([\s\S]*?)`,\n", re.MULTILINE)


def fold_maintained(entry: str) -> str:
    """Return the entry's FoldMaintained declaration, or "" when the field
    is absent.

    The value is the field's Go expression: a trailing // comment and comma
    come off, and only a fully double-quoted literal unquotes. Any other
    form the line carries, such as a raw string or a concatenated
    expression, stays as unparsed text and fails the vocabulary check, so a
    present field never reads as an absent one. A field whose value starts
    on a later line returns FOLD_VALUE_ELSEWHERE: the checker refuses the
    multiline form rather than guess at it, so the declaration keeps its
    value on the field's own line. The declaration is a signed human claim
    (see the migration struct), so a genuinely missing line is an empty
    declaration rather than a parse error.
    """
    match = DECLARES_FOLD.search(entry)
    if not match:
        return ""
    value = re.sub(r"//.*$", "", match.group(1)).strip().rstrip(",").strip()
    if not value:
        return FOLD_VALUE_ELSEWHERE
    if len(value) >= 2 and value.startswith('"') and value.endswith('"'):
        return value[1:-1]
    return value


def adds_preexisting_column(sql: str) -> bool:
    """Return whether any statement adds a column to a table the migration
    did not create.

    A FoldMaintained declaration describes exactly this shape: a column on a
    table an older binary's fold generation already writes.
    """
    born: set[str] = set()
    for statement in statements(sql):
        match = CREATE_TABLE.match(statement)
        if match:
            born.add(match.group(1).lower())
            continue
        match = ALTER.match(statement)
        if match and match.group(1).lower() not in born:
            if ADD_COLUMN.match(match.group(2).strip()):
                return True
    return False


def migrations(source: str) -> list[tuple[int, str, str]]:
    """Return each migration's version, its declaration block, and its SQL.

    The declaration block carries the Breaking field. The SQL is the raw string
    literal alone: the Go field lines around it are not statements, and feeding
    them to the classifier would report every migration as unclassifiable.
    """
    start = source.index("var migrations = []migration{")
    parts = ENTRY.split(source[start:])
    out: list[tuple[int, str, str]] = []
    for i in range(1, len(parts), 2):
        version, entry = int(parts[i]), parts[i + 1]
        literal = SQL_LITERAL.search(entry)
        out.append((version, entry, literal.group(1) if literal else ""))
    return out


def evaluate(entries: list[tuple[int, str, str]]) -> tuple[list[str], list[int]]:
    """Compare every entry's declarations with what its SQL does.

    Returns the refusal messages and the versions whose SQL, or whose advance
    declaration, breaks an older binary. Migrations at or below RULE_FLOOR
    predate the FoldMaintained field and are held to the statement rules
    alone.
    """
    failures: list[str] = []
    breaking_versions: list[int] = []
    for version, entry, sql in entries:
        declared = bool(DECLARES_BREAKING.search(entry))
        fold = fold_maintained(entry)
        reasons = classify(sql)
        if version > RULE_FLOOR:
            adds_column = adds_preexisting_column(sql)
            if adds_column and not fold:
                failures.append(
                    f"migration {version} adds a column to a pre-existing table "
                    'without a FoldMaintained declaration; declare "advance" '
                    'with Breaking: true, or declare "origin"'
                )
            if fold and not adds_column:
                failures.append(
                    f'migration {version} declares FoldMaintained "{fold}", but '
                    "adds no column to a pre-existing table; remove the declaration"
                )
            if fold and fold not in FOLD_VOCABULARY:
                failures.append(
                    f'migration {version} declares FoldMaintained "{fold}"; the '
                    'vocabulary is "advance" or "origin"'
                )
            if fold == "advance":
                reasons.append(
                    "advances a fold-maintained column, which a rolling upgrade "
                    "leaves unconverged (CD-0111 D3)"
                )
        if reasons and not declared:
            failures.append(
                f"migration {version} must declare Breaking: true; it "
                + "; ".join(reasons)
            )
        if declared and not reasons:
            failures.append(
                f"migration {version} declares Breaking: true, but every statement "
                "only creates a schema object or adds a column; remove the "
                "declaration or state why the classification is wrong"
            )
        if reasons:
            breaking_versions.append(version)
    return failures, breaking_versions


def main() -> int:
    source = SCHEMA.read_text()
    entries = migrations(source)
    if not entries:
        print("check-migration-compatibility: no migrations found", file=sys.stderr)
        return 1

    failures, breaking_versions = evaluate(entries)
    for failure in failures:
        print(f"check-migration-compatibility: {failure}", file=sys.stderr)
    if failures:
        return 1

    floor = max(breaking_versions) if breaking_versions else 0
    print(
        f"check-migration-compatibility: {len(entries)} migrations, "
        f"{len(breaking_versions)} breaking, compatibility floor {floor}"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
