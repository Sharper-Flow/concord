#!/usr/bin/env python3
"""Check inverse coverage and authority claims for storage vocabularies."""
from __future__ import annotations

import argparse
import json
import re
import sys
from dataclasses import dataclass
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCHEMA_SOURCE = Path("internal/store/schema.go")
MANIFEST = Path("contracts/storage-vocabulary-authority.v1.json")
AUTHORITIES = {"check", "fk", "registry_trigger", "contract", "go_registry", "open"}
VOCABULARY_RE = re.compile(
    r"(?:^|_)(?:kind|type|class|role|status|state|stage|family|mode|severity|phase|consequence)(?:_|$)",
    re.IGNORECASE,
)
IDENTIFIER_RE = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")
MAX_FINDINGS = 1000


@dataclass(frozen=True)
class Column:
    table: str
    name: str
    declaration: str
    body: str
    line: int


@dataclass(frozen=True)
class Trigger:
    name: str
    table: str
    sql: str
    line: int


def _unquote(identifier: str) -> str:
    value = identifier.strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "`\"":
        return value[1:-1]
    if value.startswith("[") and value.endswith("]"):
        return value[1:-1]
    return value


def _mask_comments(text: str) -> str:
    chars = list(text)
    index = 0
    quote: str | None = None
    while index < len(chars):
        if quote:
            if chars[index] == quote:
                if index + 1 < len(chars) and chars[index + 1] == quote:
                    index += 2
                    continue
                quote = None
            index += 1
            continue
        if chars[index] in "'\"`":
            quote = chars[index]
            index += 1
            continue
        if chars[index : index + 2] == ["-", "-"]:
            while index < len(chars) and chars[index] != "\n":
                chars[index] = " "
                index += 1
            continue
        if chars[index : index + 2] == ["/", "*"]:
            chars[index] = chars[index + 1] = " "
            index += 2
            while index < len(chars) and chars[index : index + 2] != ["*", "/"]:
                if chars[index] != "\n":
                    chars[index] = " "
                index += 1
            if index + 1 < len(chars):
                chars[index] = chars[index + 1] = " "
                index += 2
            continue
        index += 1
    return "".join(chars)


def _matching_parenthesis(text: str, opening: int) -> int:
    depth = 0
    quote: str | None = None
    index = opening
    while index < len(text):
        char = text[index]
        if quote:
            if char == quote:
                if index + 1 < len(text) and text[index + 1] == quote:
                    index += 2
                    continue
                quote = None
            index += 1
            continue
        if char in "'\"`":
            quote = char
        elif char == "(":
            depth += 1
        elif char == ")":
            depth -= 1
            if depth == 0:
                return index
        index += 1
    return -1


def _split_top_level(text: str) -> list[str]:
    parts: list[str] = []
    start = 0
    depth = 0
    quote: str | None = None
    for index, char in enumerate(text):
        if quote:
            if char == quote:
                quote = None
            continue
        if char in "'\"`":
            quote = char
        elif char == "(":
            depth += 1
        elif char == ")":
            depth -= 1
        elif char == "," and depth == 0:
            parts.append(text[start:index].strip())
            start = index + 1
    final = text[start:].strip()
    if final:
        parts.append(final)
    return parts


def _sql_sections(source: str) -> list[tuple[int, str]]:
    sections = list(re.finditer(r"\bSQL\s*:\s*`", source, re.IGNORECASE))
    if not sections:
        return [(1, source)]
    result: list[tuple[int, str]] = []
    for index, match in enumerate(sections):
        end = source.find("`", match.end())
        if end < 0:
            end = len(source)
        result.append((source.count("\n", 0, match.end()) + 1, source[match.end() : end]))
    return result


def _line_number(section_line: int, section: str, offset: int) -> int:
    return section_line + section.count("\n", 0, offset)


def _discover(source: str) -> tuple[dict[str, Column], list[Trigger]]:
    tables: dict[str, tuple[str, int]] = {}
    triggers: dict[str, Trigger] = {}
    for section_line, section in _sql_sections(source):
        masked = _mask_comments(section)
        events: list[tuple[int, str, re.Match[str] | None]] = []
        create_re = re.compile(
            r"\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*|\"[^\"]+\"|`[^`]+`)\s*\(",
            re.IGNORECASE,
        )
        for match in create_re.finditer(masked):
            close = _matching_parenthesis(section, match.end() - 1)
            if close >= 0:
                events.append((match.start(), "create", (match, close)))  # type: ignore[arg-type]
        for pattern, kind in (
            (r"\bDROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*|\"[^\"]+\"|`[^`]+`)\s*;", "drop"),
            (r"\bALTER\s+TABLE\s+([A-Za-z_][A-Za-z0-9_]*|\"[^\"]+\"|`[^`]+`)\s+RENAME\s+TO\s+([A-Za-z_][A-Za-z0-9_]*|\"[^\"]+\"|`[^`]+`)\s*;", "rename"),
            (r"\bALTER\s+TABLE\s+([A-Za-z_][A-Za-z0-9_]*|\"[^\"]+\"|`[^`]+`)\s+ADD\s+COLUMN\s+(.+?);", "add"),
        ):
            for match in re.finditer(pattern, masked, re.IGNORECASE | re.DOTALL):
                events.append((match.start(), kind, match))
        events.sort(key=lambda event: event[0])
        for position, kind, raw_match in events:
            match = raw_match
            if kind == "create":
                assert isinstance(match, tuple)
                header, close = match
                table = _unquote(header.group(1))
                body = section[header.end() : close]
                tables[table] = (body, _line_number(section_line, section, position))
            elif kind == "drop":
                assert isinstance(match, re.Match)
                tables.pop(_unquote(match.group(1)), None)
            elif kind == "rename":
                assert isinstance(match, re.Match)
                old = _unquote(match.group(1))
                new = _unquote(match.group(2))
                if old in tables:
                    tables[new] = tables.pop(old)
            else:
                assert isinstance(match, re.Match)
                table = _unquote(match.group(1))
                if table in tables:
                    body, line = tables[table]
                    addition = section[match.start(2) : match.end(2)].strip()
                    tables[table] = (f"{body},\n{addition}", line)

        trigger_start = re.compile(
            r"\bCREATE\s+TRIGGER\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*|\"[^\"]+\"|`[^`]+`).*?\bON\s+([A-Za-z_][A-Za-z0-9_]*|\"[^\"]+\"|`[^`]+`)",
            re.IGNORECASE | re.DOTALL,
        )
        trigger_events: list[tuple[int, str, re.Match[str]]] = []
        trigger_end: dict[str, int] = {}
        for match in trigger_start.finditer(masked):
            end_match = re.search(r"\bEND\s*;", masked[match.start() :], re.IGNORECASE)
            end = match.start() + end_match.end() if end_match else len(section)
            trigger_events.append((match.start(), "create", match))
            name = _unquote(match.group(1))
            trigger_end[name] = end
        for match in re.finditer(
            r"\bDROP\s+TRIGGER\s+(?:IF\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*|\"[^\"]+\"|`[^`]+`)\s*;",
            masked,
            re.IGNORECASE,
        ):
            trigger_events.append((match.start(), "drop", match))
        for position, kind, match in sorted(trigger_events, key=lambda event: event[0]):
            name = _unquote(match.group(1))
            if kind == "drop":
                triggers.pop(name, None)
                continue
            end = trigger_end[name]
            triggers[name] = Trigger(
                name=name,
                table=_unquote(match.group(2)),
                sql=section[match.start() : end],
                line=_line_number(section_line, section, position),
            )

    columns: dict[str, Column] = {}
    for table, (body, line) in tables.items():
        for declaration in _split_top_level(body):
            match = re.match(r"\s*(?:\"([^\"]+)\"|`([^`]+)`|([A-Za-z_][A-Za-z0-9_]*))\b", declaration)
            if not match:
                continue
            name = next(value for value in match.groups() if value is not None)
            if name.upper() in {"PRIMARY", "UNIQUE", "CHECK", "FOREIGN", "CONSTRAINT"}:
                continue
            key = f"{table}.{name}"
            columns[key] = Column(table, name, declaration, body, line)
    return columns, list(triggers.values())


def _checks_for(column: Column) -> bool:
    masked = _mask_comments(column.body)
    for match in re.finditer(r"\bCHECK\s*\(", masked, re.IGNORECASE):
        close = _matching_parenthesis(column.body, match.end() - 1)
        if close >= 0 and re.search(rf"\b{re.escape(column.name)}\b", column.body[match.start() : close + 1], re.IGNORECASE):
            return True
    return False


def _foreign_key_for(column: Column) -> bool:
    if re.search(r"\bREFERENCES\b", column.declaration, re.IGNORECASE):
        return True
    for match in re.finditer(r"\bFOREIGN\s+KEY\s*\(([^)]*)\)", column.body, re.IGNORECASE):
        names = {_unquote(value.strip()) for value in match.group(1).split(",")}
        if column.name in names:
            return True
    return False


def _registry_trigger_for(column: Column, triggers: list[Trigger]) -> bool:
    registry_names = {"work_kinds", "workflow_native_run_statuses"}
    for trigger in triggers:
        if trigger.table != column.table:
            continue
        if not re.search(rf"\b(?:NEW|OLD)\s*\.\s*{re.escape(column.name)}\b", trigger.sql, re.IGNORECASE):
            continue
        if re.search(r"\bNOT\s+IN\s*\(", trigger.sql, re.IGNORECASE):
            return True
        if any(name in trigger.sql for name in registry_names):
            return True
    return False


def discover(root: Path, schema_path: Path | None = None) -> list[Column]:
    path = schema_path or root / SCHEMA_SOURCE
    source = path.read_text(encoding="utf-8")
    columns, _ = _discover(source)
    return sorted((column for column in columns.values() if VOCABULARY_RE.search(column.name)), key=lambda value: (value.table, value.name))


def _load_json(path: Path, findings: list[str]) -> object | None:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        findings.append(f"manifest: invalid JSON: {exc}")
        return None


def _symbol_exists(root: Path, symbol: str) -> bool:
    if not symbol or not re.search(r"[A-Za-z_]", symbol):
        return False
    pattern = re.compile(rf"\b{re.escape(symbol)}\b")
    for path in (root / "internal").rglob("*"):
        if path.is_file():
            try:
                if pattern.search(path.read_text(encoding="utf-8")):
                    return True
            except (OSError, UnicodeDecodeError):
                continue
    return False


def _entry_map(manifest: object, findings: list[str]) -> dict[tuple[str, str], dict[str, object]]:
    if not isinstance(manifest, dict) or not isinstance(manifest.get("entries"), list):
        findings.append("manifest: entries must be an array")
        return {}
    entries: dict[tuple[str, str], dict[str, object]] = {}
    for index, entry in enumerate(manifest["entries"]):
        if not isinstance(entry, dict):
            findings.append(f"manifest.entries[{index}]: entry must be an object")
            continue
        table, column = entry.get("table"), entry.get("column")
        if not isinstance(table, str) or not isinstance(column, str):
            findings.append(f"manifest.entries[{index}]: table and column must be strings")
            continue
        key = (table, column)
        if key in entries:
            findings.append(f"manifest-duplicate: {table}.{column}")
        entries[key] = entry
    return entries


def check(root: Path, schema_path: Path | None = None, manifest_path: Path | None = None) -> list[str]:
    findings: list[str] = []
    try:
        columns = discover(root, schema_path)
        source = (schema_path or root / SCHEMA_SOURCE).read_text(encoding="utf-8")
        _, triggers = _discover(source)
    except (OSError, UnicodeDecodeError) as exc:
        findings.append(f"schema: unable to read source: {exc}")
        return findings
    manifest = _load_json(manifest_path or root / MANIFEST, findings)
    entries = _entry_map(manifest, findings)
    live = {(column.table, column.name): column for column in columns}
    for key in sorted(set(live) - set(entries)):
        findings.append(f"undeclared: {key[0]}.{key[1]}")
    for key, entry in sorted(entries.items()):
        if key not in live:
            findings.append(f"stale: {key[0]}.{key[1]} is not a live vocabulary column")
            continue
        authority = entry.get("authority")
        if authority not in AUTHORITIES:
            findings.append(f"authority: {key[0]}.{key[1]} has invalid authority {authority!r}")
            continue
        column = live[key]
        if authority == "check" and not _checks_for(column):
            findings.append(f"false-claim: {key[0]}.{key[1]} claims check but the live DDL has no CHECK")
        elif authority == "fk" and not _foreign_key_for(column):
            findings.append(f"false-claim: {key[0]}.{key[1]} claims fk but the live DDL has no foreign key")
        elif authority == "registry_trigger" and not _registry_trigger_for(column, triggers):
            findings.append(f"false-claim: {key[0]}.{key[1]} claims registry_trigger but no matching registry trigger exists")
        elif authority == "contract":
            validator = entry.get("validator")
            if not isinstance(validator, str) or not validator:
                findings.append(f"binding: {key[0]}.{key[1]} contract authority must name a validator")
            elif not (root / validator).is_file():
                findings.append(f"binding: {key[0]}.{key[1]} validator does not exist: {validator}")
        elif authority == "go_registry":
            symbol = entry.get("symbol")
            if not isinstance(symbol, str) or not symbol:
                findings.append(f"binding: {key[0]}.{key[1]} go_registry authority must name a symbol")
            elif not _symbol_exists(root, symbol):
                findings.append(f"binding: {key[0]}.{key[1]} Go symbol does not exist: {symbol}")
        elif authority == "open":
            rationale = entry.get("rationale")
            if not isinstance(rationale, str) or len(rationale.strip()) < 20:
                findings.append(f"rationale: {key[0]}.{key[1]} open authority needs at least 20 characters")
    return findings


def report(findings: list[str]) -> int:
    for finding in findings[:MAX_FINDINGS]:
        print(finding)
    if len(findings) > MAX_FINDINGS:
        print(f"... {len(findings) - MAX_FINDINGS} additional finding(s) omitted")
    if findings:
        print(f"vocabulary authority check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("vocabulary authority check passed")
    return 0


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", type=Path, default=ROOT, help="repository root")
    parser.add_argument("--schema", type=Path, help="schema source override for tests")
    parser.add_argument("--manifest", type=Path, help="manifest path override for tests")
    args = parser.parse_args(argv)
    return report(check(args.root.resolve(), args.schema, args.manifest))


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
