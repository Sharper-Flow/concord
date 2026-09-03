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
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

SCHEMA = Path(__file__).resolve().parent.parent / "internal" / "store" / "schema.go"

# A migration entry: its version, then everything up to the next entry.
# gofmt aligns the values in a struct literal, so the run of spaces after a
# field name depends on the longest field name present in that literal. Every
# field pattern here tolerates that alignment rather than pinning one width.
ENTRY = re.compile(r"\n\t\{\n\t\tVersion:\s+(\d+),")
DECLARES_BREAKING = re.compile(r"^\t\tBreaking:\s+true,$", re.MULTILINE)

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


def main() -> int:
    source = SCHEMA.read_text()
    entries = migrations(source)
    if not entries:
        print("check-migration-compatibility: no migrations found", file=sys.stderr)
        return 1

    failures: list[str] = []
    breaking_versions: list[int] = []
    for version, entry, sql in entries:
        declared = bool(DECLARES_BREAKING.search(entry))
        reasons = classify(sql)
        if reasons:
            breaking_versions.append(version)
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
