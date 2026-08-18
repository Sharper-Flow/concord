#!/usr/bin/env python3
"""Reject raw SQLite access outside the internal/store boundary.

This is a deliberately conservative lexical check. It does not attempt Go type
checking or SQL parsing; it rejects the direct escape hatches that make the
store boundary observable and uses the schema's CREATE TABLE vocabulary to
catch literal SQL aimed at store-owned tables.
"""

from __future__ import annotations

import re
import sys
from dataclasses import dataclass
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MAX_FINDINGS = 200

CREATE_TABLE = re.compile(
    r"\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?"
    r"(?:\"([^\"]+)\"|`([^`]+)`|([A-Za-z_][A-Za-z0-9_]*))",
    re.IGNORECASE,
)
SQL_OPERATION = re.compile(r"\b(SELECT|INSERT|UPDATE|DELETE|REPLACE)\b", re.IGNORECASE)
SQL_TABLE_REFERENCE = re.compile(
    r"\b(?:FROM|JOIN|INTO|UPDATE|DELETE(?:\s+FROM)?)\s+"
    r'(?:[A-Za-z_][A-Za-z0-9_]*\.)?["`]?([A-Za-z_][A-Za-z0-9_]*)["`]?',
    re.IGNORECASE,
)
DB_TYPE = re.compile(r"\*\s*sql\s*\.\s*DB\b")
TX_TYPE = re.compile(r"\*\s*sql\s*\.\s*Tx\b")
DATABASE_ACCESSOR = re.compile(r"\.\s*(?:DB|DatabaseForTesting)\b")
BEGIN_TX_CALL = re.compile(r"\bBeginTx\s*\(")
STORE_DB_METHOD = re.compile(
    r"\bfunc\s*\(\s*[A-Za-z_][A-Za-z0-9_]*\s+\*?Store\s*\)\s*DB\s*\(",
)
IMPORT_DECL = re.compile(
    r"\bimport\s*(?:\((?P<block>.*?)\)|(?P<single>(?:(?:[A-Za-z_][A-Za-z0-9_]*|\.)\s+)?\"(?:\\.|[^\"\\])*\"))",
    re.DOTALL,
)


@dataclass(frozen=True)
class GoString:
    value: str
    line: int
    offset: int


def _line_at(text: str, offset: int) -> int:
    return text.count("\n", 0, offset) + 1


def extract_go_strings(text: str) -> list[GoString]:
    """Extract interpreted and raw Go strings, ignoring comments and runes."""
    strings: list[GoString] = []
    index = 0
    while index < len(text):
        if text.startswith("//", index):
            newline = text.find("\n", index + 2)
            index = len(text) if newline < 0 else newline + 1
            continue
        if text.startswith("/*", index):
            end = text.find("*/", index + 2)
            index = len(text) if end < 0 else end + 2
            continue
        if text[index] == "`":
            start = index
            end = text.find("`", index + 1)
            if end < 0:
                break
            strings.append(GoString(text[index + 1 : end], _line_at(text, start), start))
            index = end + 1
            continue
        if text[index] == "'":
            index += 1
            escaped = False
            while index < len(text):
                if text[index] == "'" and not escaped:
                    index += 1
                    break
                if text[index] == "\\" and not escaped:
                    escaped = True
                else:
                    escaped = False
                index += 1
            continue
        if text[index] == '"':
            start = index
            index += 1
            value_start = index
            escaped = False
            while index < len(text):
                char = text[index]
                if char == '"' and not escaped:
                    strings.append(GoString(text[value_start:index], _line_at(text, start), start))
                    index += 1
                    break
                if char == "\\" and not escaped:
                    escaped = True
                else:
                    escaped = False
                index += 1
            continue
        index += 1
    return strings


def mask_comments_preserving_literals(text: str) -> str:
    """Mask Go comments while retaining literals for import extraction."""
    chars = list(text)
    index = 0
    while index < len(text):
        if text.startswith("//", index):
            end = text.find("\n", index + 2)
            end = len(text) if end < 0 else end
            for position in range(index, end):
                chars[position] = " "
            index = end
            continue
        if text.startswith("/*", index):
            end = text.find("*/", index + 2)
            end = len(text) if end < 0 else end + 2
            for position in range(index, end):
                if text[position] != "\n":
                    chars[position] = " "
            index = end
            continue
        if text[index] == "`":
            end = text.find("`", index + 1)
            index = len(text) if end < 0 else end + 1
            continue
        if text[index] in ('"', "'"):
            quote = text[index]
            index += 1
            escaped = False
            while index < len(text):
                if text[index] == quote and not escaped:
                    index += 1
                    break
                if text[index] == "\\" and not escaped:
                    escaped = True
                else:
                    escaped = False
                index += 1
            continue
        index += 1
    return "".join(chars)


def database_sql_import_lines(text: str) -> list[int]:
    """Return lines whose import declaration names database/sql."""
    masked_comments = mask_comments_preserving_literals(text)
    lines: list[int] = []
    for declaration in IMPORT_DECL.finditer(masked_comments):
        for literal in extract_go_strings(declaration.group(0)):
            if literal.value == "database/sql":
                lines.append(_line_at(text, declaration.start() + literal.offset))
    return lines


def strip_sql_comments(sql: str) -> str:
    """Remove SQL comments while preserving newlines for bounded diagnostics."""
    result: list[str] = []
    index = 0
    quote: str | None = None
    while index < len(sql):
        char = sql[index]
        if quote:
            result.append(char)
            if char == quote:
                if index + 1 < len(sql) and sql[index + 1] == quote:
                    result.append(sql[index + 1])
                    index += 2
                    continue
                quote = None
            index += 1
            continue
        if char in ("'", '"'):
            quote = char
            result.append(char)
            index += 1
            continue
        if sql.startswith("--", index):
            newline = sql.find("\n", index + 2)
            if newline < 0:
                result.append(" " * (len(sql) - index))
                break
            result.append(" " * (newline - index))
            result.append("\n")
            index = newline + 1
            continue
        if sql.startswith("/*", index):
            end = sql.find("*/", index + 2)
            if end < 0:
                result.append(" " * (len(sql) - index))
                break
            comment = sql[index : end + 2]
            result.extend("\n" if char == "\n" else " " for char in comment)
            index = end + 2
            continue
        result.append(char)
        index += 1
    return "".join(result)


def mask_comments_and_strings(text: str) -> str:
    """Mask Go comments and literals, retaining line/column positions."""
    chars = list(text)
    index = 0
    while index < len(text):
        if text.startswith("//", index):
            end = text.find("\n", index + 2)
            end = len(text) if end < 0 else end
            for position in range(index, end):
                chars[position] = " "
            index = end
            continue
        if text.startswith("/*", index):
            end = text.find("*/", index + 2)
            end = len(text) if end < 0 else end + 2
            for position in range(index, end):
                if text[position] != "\n":
                    chars[position] = " "
            index = end
            continue
        if text[index] == "`":
            end = text.find("`", index + 1)
            end = len(text) if end < 0 else end + 1
            for position in range(index, end):
                if text[position] != "\n":
                    chars[position] = " "
            index = end
            continue
        if text[index] == '"':
            index += 1
            escaped = False
            while index < len(text):
                if text[index] == '"' and not escaped:
                    index += 1
                    break
                if text[index] == "\\" and not escaped:
                    escaped = True
                else:
                    escaped = False
                if text[index] != "\n":
                    chars[index] = " "
                index += 1
            continue
        if text[index] == "'":
            index += 1
            escaped = False
            while index < len(text):
                if text[index] == "'" and not escaped:
                    index += 1
                    break
                if text[index] == "\\" and not escaped:
                    escaped = True
                else:
                    escaped = False
                if text[index] != "\n":
                    chars[index] = " "
                index += 1
            continue
        index += 1
    return "".join(chars)


def schema_tables(root: Path) -> set[str]:
    schema = root / "internal" / "store" / "schema.go"
    text = schema.read_text(encoding="utf-8")
    tables: set[str] = set()
    for literal in extract_go_strings(text):
        for match in CREATE_TABLE.finditer(strip_sql_comments(literal.value)):
            tables.add(next(group for group in match.groups() if group is not None).lower())
    return tables


def production_files(root: Path) -> list[Path]:
    paths: list[Path] = []
    for directory in (root / "cmd", root / "internal"):
        if not directory.exists():
            continue
        for path in directory.rglob("*.go"):
            relative = path.relative_to(root)
            if path.name.endswith("_test.go"):
                continue
            if relative.parts[:2] in (("internal", "store"), ("internal", "pm1fixture")):
                continue
            paths.append(path)
    return sorted(paths)


def finding(path: Path, root: Path, line: int, message: str) -> str:
    return f"{path.relative_to(root)}:{line}: {message}"


def scan(root: Path = ROOT) -> list[str]:
    findings: list[str] = []
    tables = schema_tables(root)

    for path in production_files(root):
        text = path.read_text(encoding="utf-8")
        masked = mask_comments_and_strings(text)
        for line in database_sql_import_lines(text):
            findings.append(finding(path, root, line, "database/sql import outside internal/store"))
        for match in DB_TYPE.finditer(masked):
            findings.append(finding(path, root, _line_at(text, match.start()), "raw *sql.DB type outside internal/store"))
        for match in TX_TYPE.finditer(masked):
            findings.append(finding(path, root, _line_at(text, match.start()), "raw *sql.Tx type outside internal/store"))
        for match in DATABASE_ACCESSOR.finditer(masked):
            findings.append(finding(path, root, _line_at(text, match.start()), "raw database accessor identifier outside exempt paths"))
        for match in BEGIN_TX_CALL.finditer(masked):
            findings.append(finding(path, root, _line_at(text, match.start()), "direct BeginTx call outside internal/store"))
        for literal in extract_go_strings(text):
            sql = strip_sql_comments(literal.value)
            reported: set[tuple[int, str]] = set()
            for operation in SQL_OPERATION.finditer(sql):
                operation_name = operation.group(1).upper()
                statement_end = sql.find(";", operation.end())
                if statement_end < 0:
                    statement_end = len(sql)
                for table in SQL_TABLE_REFERENCE.finditer(sql, operation.start(), statement_end):
                    table_name = table.group(1).lower()
                    if table_name not in tables:
                        continue
                    key = (table.start(1), table_name)
                    if key in reported:
                        continue
                    reported.add(key)
                    line = literal.line + sql.count("\n", 0, table.start(1))
                    findings.append(
                        finding(path, root, line, f"literal SQL {operation_name} targets store table {table.group(1)}")
                    )

    for path in sorted((root / "internal" / "store").rglob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        text = path.read_text(encoding="utf-8")
        masked = mask_comments_and_strings(text)
        for match in STORE_DB_METHOD.finditer(masked):
            findings.append(finding(path, root, _line_at(text, match.start()), "Store.DB method must be removed"))

    return findings


def main() -> int:
    findings = scan()
    for item in findings[:MAX_FINDINGS]:
        print(item)
    if len(findings) > MAX_FINDINGS:
        print(f"... {len(findings) - MAX_FINDINGS} additional finding(s) omitted")
    if findings:
        print(f"store boundary check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("store boundary check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
