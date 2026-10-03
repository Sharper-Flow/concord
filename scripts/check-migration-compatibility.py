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
One deliberate boundary: the legacy statement rules treat a conditional
CREATE (IF NOT EXISTS) as ownership, the repair-migration convention
(migration 60 builds on it), while the FoldMaintained duty holds such a
table to the stricter standard below because its column may land on the
table SQLite retained.
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

Widening a CHECK constraint has one SQLite shape: rename the table away,
recreate it under its old name, copy the rows losslessly, drop the scratch
copy, and recreate every index, trigger, and view the table carried. An
older binary stays correct across such a rebuild, yet the rename and the
drop name objects an older binary knows, so the rebuild reads as breaking
and raises the compatibility floor. evaluate() therefore replays each
migration into a World that is an in-memory SQLite database executing the
statements as they run (CD-0055 D2: the tool that parses SQL owns every
schema fact), and a rebuild stays additive only when a trial that clones
that world, seeds probe rows through the rebuilt table's own constraints,
runs the candidate migration, and compares SQLite-derived facts proves
all of this:

- every schema object outside the rebuilt table returns with its exact
  stored definition (sqlite_schema rows compared byte for byte, so a
  rename's rewrite of an inbound foreign key, trigger body, or view - and
  any un-restored dependent or stranded view - refuses the proof);
- the table's PRAGMA table_xinfo (name, type, notnull, default, pk,
  hidden), foreign_key_list, and index_list/index_xinfo facts are
  unchanged;
- neither side carries AUTOINCREMENT state: the sequence high-water mark
  lives in sqlite_sequence, outside the copied rows, so any
  sqlite_sequence entry for the table on either side refuses;
- the seeded rows return exactly, rowids included: a table that answers
  SELECT rowid proves its copy carried the rowids (a rowid-alias column,
  or a copy that writes rowids by name), and a table with no rowid
  (WITHOUT ROWID, decided by SQLite refusing the select) carries its
  identity in its key; an empty rowid table proves nothing and refuses;
- the stored CREATE definitions differ only inside CHECK constraints,
  which must widen inside the decidable family: same column, one numeric
  bound moved under one monotone operator where the referenced column's
  affinity - read from SQLite by probing a scratch column of the same
  declared type, never re-derived in Python - makes the comparison
  numeric, one equal-bound strictness loosening, or a grown membership
  list. Equality, inequality, and text bounds have acceptance sets the
  proof cannot order: a changed one must repeat its old text. A numeric
  literal reads at its SQLite runtime value, so 9007199254740995.0 and
  9007199254740995 are not one bound;
- the copy statement names every column of the recreated table, in
  order, unquoted, on both sides of the SELECT.

An unknown baseline (a poisoned world), a narrowed or novel CHECK, an
altered column, default, key, or foreign-key shape, unsupported SQL, a
lossy copy, or a dependent that does not return keeps the migration
breaking. The migration may carry nothing beside the rebuild shape:
dependent drops before the rename, the rename, the recreation, one
copy, the scratch drop, then restorations - a write anywhere else can
erase the rows the copy claims to move. A conditional migration, an
unreadable entry, or one statement the replay cannot interpret poisons
the world, and poisoned worlds prove nothing: a proof read from an
uncertain before-state would admit a shape an older binary never saw.

Remaining textual pieces, each a CD-0055 D3 declared exception with the
same failure class (silent corruption invisible where it is authored)
and a named structural end-state: the statement splitter and head-word
recognizers read only enough to route a statement to a rule, and their
miss shape is fail-closed ("unclassified statement" stays breaking); the
CREATE TABLE body reader lifts CHECK texts and compares the remaining
words for equality, and its miss shape refuses the proof - the end-state
that retires both is a SQLite surface that reports CHECK constraints as
catalog facts. The CHECK atom family itself compares only bare-column
atoms against literal bounds, and refuses every other expression to
unchanged text. Go-source reading (the migrations table extractor and
the declaration parsers) parses Go, not SQL, because the checker reads
the shipped source without executing the toolchain.
"""

from __future__ import annotations

import math
import re
import sqlite3
import sys
from fractions import Fraction
from pathlib import Path
from typing import NamedTuple

SCHEMA = Path(__file__).resolve().parent.parent / "internal" / "store" / "schema.go"

# The fold-maintenance rules bind only migrations above this floor. Migration
# 108 was the last fold-maintained column admitted without a declaration; its
# repair, migration 109, is the first version the rules could have bound.
RULE_FLOOR = 108

FOLD_VOCABULARY = ("advance", "origin")
# What fold_maintained returns for a field whose value is not on the field's
# own line: present, unreadable, and outside every vocabulary. Text no Go
# value can carry keeps it from colliding with a real residue.
FOLD_VALUE_ELSEWHERE = "<value on a later line>"
# A migrations list element that is not a composite literal cannot supply a
# Version field to read, and the checker refuses it instead of omitting it.
UNSUPPORTED_ELEMENT = -2

# SQLite's tokenizer admits A-Z, a-z, 0-9, _, $ and every code point at or
# above U+0080 inside a bare identifier, and a bare name does not start with a
# digit. The regex must consume the whole token: a prefix match records table
# a as born where SQLite creates a-with-suffix, and every later reference to a
# then looks owned.
SQL_ID_START = r"[A-Za-z_$\u0080-\U0010FFFF]"
SQL_ID_CONT = r"[A-Za-z0-9_$\u0080-\U0010FFFF]"
# SQLite treats only space, tab, newline, formfeed, and carriage return as
# whitespace; Python's \s, \S, and str.strip() go further and consume or stop
# at identifier characters such as U+00A0. Every SQL token boundary and name
# trim uses these classes so the checker reads the names SQLite executes.
SQL_SPACE = "[ \\t\\n\\f\\r]"
SQL_SP = f"{SQL_SPACE}+"
SQL_SQ = f"{SQL_SPACE}*"
SQL_NOT_SPACE = "[^ \\t\\n\\f\\r]"
SQL_TRIM = " \t\n\f\r"
SQL_REF = (
    rf"(?:\"(?:[^\"]|\"\")*\"|\[[^\]]*\]|`(?:[^`]|``)*`|'(?:[^']|'')*'"
    rf"|{SQL_ID_START}{SQL_ID_CONT}*)"
)
SQL_QUAL = rf"(?:{SQL_REF}(?:{SQL_SQ}\.{SQL_SQ}{SQL_REF})*)"
# SQLite separates two bare tokens with whitespace; a quoted or bracketed
# token carries its own boundary, so keyword and name adjacency needs no
# space on the quoted side.
SQL_NAME_SEP = "(?:%s+|(?=[\"'`[]))" % SQL_SP
SQL_NAME_END = "(?:%s+|(?<=[\"'`\\]]))" % SQL_SP
CREATE_TABLE = re.compile(
    rf"^CREATE{SQL_SP}(?:VIRTUAL{SQL_SP}|TEMP{SQL_SP}|TEMPORARY{SQL_SP})*"
    rf"TABLE(?:{SQL_SP}IF{SQL_SP}NOT{SQL_SP}EXISTS)?"
    rf"{SQL_NAME_SEP}({SQL_QUAL})",
    re.IGNORECASE,
)
DROP_TABLE = re.compile(
    rf"^DROP{SQL_SP}TABLE(?:{SQL_SP}IF{SQL_SP}EXISTS)?{SQL_NAME_SEP}({SQL_QUAL})",
    re.IGNORECASE,
)


def quoted_end(text: str, start: int) -> int:
    """Return the index just past the quoted token opening at start.

    The scan reads the token the way SQLite does: a doubled quote inside a
    double-quoted, single-quoted, or backticked token is one character of
    its content, never a boundary, and a bracket token closes at its first
    right bracket. An unterminated token runs to the text's end, which the
    caller refuses rather than guesses at.
    """
    quote = text[start]
    if quote == "[":
        close = text.find("]", start + 1)
        return len(text) if close < 0 else close + 1
    i, n = start + 1, len(text)
    while i < n:
        if text[i] == quote:
            if i + 1 < n and text[i + 1] == quote:
                i += 2
                continue
            return i + 1
        i += 1
    return n


def sql_parts(ref: str) -> list[str]:
    """Split a table reference on dots, keeping quoted parts atomic.

    "a.b.c" is one name, not a schema and a table: the dot inside the
    quotes belongs to the identifier. Every quoting form SQL_REF accepts
    is atomic here, and a doubled quote inside a quoted part is one
    character, not a boundary.
    """
    parts: list[str] = []
    current: list[str] = []
    i, n = 0, len(ref)
    while i < n:
        ch = ref[i]
        if ch in ('"', "'", "`", "["):
            close = "]" if ch == "[" else ch
            j = i + 1
            while j < n:
                k = ref.find(close, j)
                if k < 0:
                    j = n
                    break
                if close != "]" and k + 1 < n and ref[k + 1] == close:
                    j = k + 2
                    continue
                j = k + 1
                break
            current.append(ref[i:j])
            i = j
        elif ch == ".":
            parts.append("".join(current))
            current = []
            i += 1
        else:
            current.append(ch)
            i += 1
    parts.append("".join(current))
    return [p.strip(SQL_TRIM) for p in parts if p.strip(SQL_TRIM)]


ASCII_CASE_FOLD = str.maketrans(
    "ABCDEFGHIJKLMNOPQRSTUVWXYZ", "abcdefghijklmnopqrstuvwxyz"
)


def fold_ascii(name: str) -> str:
    """Fold identifier case the way SQLite compares names: ASCII only.

    SQLite folds A-Z when it compares identifiers and keeps every
    non-ASCII code point distinct. Python's Unicode lowercasing would
    merge names SQLite keeps apart and hide a pre-existing table.
    """
    return name.translate(ASCII_CASE_FOLD)


def sql_table_key(ref: str, temp: bool = False) -> tuple[str, str]:
    """Return the (schema, table) identity of one SQL table reference.

    An explicit qualifier names its schema; an unqualified reference in a
    CREATE carries temp when the statement says TEMP or TEMPORARY and main
    otherwise. Quoting comes off the name with its escapes resolved, so
    "t", 't', [t], `t`, and t are one table, while main.t and temp.t stay
    distinct identities. Case folds with SQLite's ASCII-only comparison.
    """
    def unquote(part: str) -> str:
        if len(part) >= 2 and part[0] == part[-1] and part[0] in ('"', "'", "`"):
            return part[1:-1].replace(part[0] * 2, part[0])
        if len(part) >= 2 and part[0] == "[" and part[-1] == "]":
            return part[1:-1]
        return part

    parts = sql_parts(ref)
    if len(parts) == 1:
        schema = "temp" if temp else "main"
        name = parts[0]
    else:
        schema, name = parts[0], parts[-1]
    return (fold_ascii(unquote(schema)), fold_ascii(unquote(name)))


def resolve_born(ref: str, born: set[tuple[str, str]]) -> tuple[str, str] | None:
    """Return the born key a table reference resolves to, or None.

    A qualified reference matches only its own schema: dropping
    main.existing does not touch a temp.existing born here. An unqualified
    reference resolves as SQLite resolves it, temp before main.
    """
    schema, name = sql_table_key(ref)
    if len(sql_parts(ref)) >= 2:
        return (schema, name) if (schema, name) in born else None
    for key in (("temp", name), ("main", name)):
        if key in born:
            return key
    return None


def resolves_to_born(ref: str, born: set[tuple[str, str]]) -> bool:
    """Return whether a table reference resolves to a born table."""
    return resolve_born(ref, born) is not None
# One SQL table reference: quoted, bracketed, backticked, string-literal, or
# bare, alone or schema-qualified. Comparisons normalize through sql_table_key.
DROP_INDEX = re.compile(
    rf"^DROP{SQL_SP}(?:INDEX|TRIGGER|VIEW)(?:{SQL_SP}IF{SQL_SP}EXISTS)?"
    rf"{SQL_NAME_SEP}{SQL_QUAL}",
    re.IGNORECASE,
)
ALTER = re.compile(
    rf"^ALTER{SQL_SP}TABLE{SQL_NAME_SEP}({SQL_QUAL}){SQL_NAME_END}([\s\S]*)$",
    re.IGNORECASE,
)
ADD_COLUMN = re.compile(
    rf"^ADD{SQL_NAME_SEP}(?:COLUMN{SQL_NAME_SEP})?{SQL_NOT_SPACE}", re.IGNORECASE
)
INDEX_ON = re.compile(
    rf"^CREATE{SQL_SP}(UNIQUE{SQL_SP})?INDEX(?:{SQL_SP}IF{SQL_SP}NOT{SQL_SP}EXISTS)?"
    rf"{SQL_NAME_SEP}(?:{SQL_REF}){SQL_NAME_END}ON{SQL_NAME_SEP}({SQL_QUAL})",
    re.IGNORECASE,
)
TRIGGER_ON = re.compile(
    rf"^CREATE{SQL_SP}TRIGGER(?:{SQL_SP}IF{SQL_SP}NOT{SQL_SP}EXISTS)?"
    rf"{SQL_NAME_SEP}(?:{SQL_REF}){SQL_NAME_END}(?:BEFORE|AFTER|INSTEAD)"
    rf"[\s\S]*?(?<!{SQL_ID_CONT})ON{SQL_NAME_SEP}({SQL_QUAL})",
    re.IGNORECASE,
)
VIEW_OR_PRAGMA = re.compile(
    rf"^(CREATE{SQL_SP}VIEW|PRAGMA|ANALYZE|REINDEX)(?!{SQL_ID_CONT})", re.IGNORECASE
)
WRITE = re.compile(rf"^(INSERT|UPDATE|DELETE|SELECT|WITH)(?!{SQL_ID_CONT})", re.IGNORECASE)


def first_sql_word(text: str) -> str:
    """Return the first SQL-space-delimited token, for refusal messages."""
    return re.split(SQL_SPACE + "+", text.strip(SQL_TRIM), maxsplit=1)[0].upper()


def statements(sql: str) -> list[str]:
    """Split migration SQL into statements, ignoring comments.

    A statement ends at a semicolon outside a CREATE TRIGGER body. The
    scan is literal-aware: text inside a string or a quoted identifier is
    never a comment marker or a boundary, so '--' in a value cannot
    swallow the rest of a line and a semicolon in a value cannot cut a
    statement, while comments come out wherever they sit outside
    literals. Block structure follows grammar, not spelling: the body of
    a CREATE TRIGGER opens at that statement's first BEGIN, further
    BEGINs count only at the head of a body statement, and an END closes
    only when a semicolon or the statement's end follows it, so columns
    named begin or end inside a body change nothing. A quoted table name
    spelling a keyword is never the keyword: trigger heads are read with
    the quoted segments removed.
    """
    out: list[str] = []
    current: list[str] = []
    head_words: list[str] = []
    word: list[str] = []
    depth = 0
    awaiting_body = False
    stmt_open = False
    i, n = 0, len(sql)

    def trigger_head() -> bool:
        # Only a full CREATE [TEMP|TEMPORARY] TRIGGER head opens a body.
        # The second keyword alone is not enough: a SELECT whose second
        # token is trigger would arm the body and swallow the statements
        # after its begin and end aliases.
        words = head_words
        if not words or words[0] != "CREATE":
            return False
        at = 1
        if at < len(words) and words[at] in ("TEMP", "TEMPORARY"):
            at += 1
        return at < len(words) and words[at] == "TRIGGER"

    def end_closes() -> bool:
        j = i
        while j < n:
            if sql[j] in SQL_TRIM:
                j += 1
                continue
            if sql.startswith("--", j):
                k = sql.find("\n", j)
                j = n if k < 0 else k + 1
                continue
            if sql.startswith("/*", j):
                k = sql.find("*/", j + 2)
                if k < 0:
                    return True
                j = k + 2
                continue
            break
        return j >= n or (j < n and sql[j] == ";")

    while i < n:
        ch = sql[i]
        if ch.isalnum() or ch in "_$" or ord(ch) >= 0x80:
            if not word:
                pass
            word.append(ch)
            i += 1
            continue
        token = "".join(word).upper()
        if token:
            if "(" not in "".join(current):
                head_words.append(token)
            if token == "BEGIN" and (
                awaiting_body or (depth >= 1 and not stmt_open)
            ):
                depth += 1
                awaiting_body = False
            elif token == "END" and depth >= 1 and end_closes():
                depth -= 1
            elif depth == 0 and trigger_head():
                awaiting_body = True
            current.extend(word)
            word = []
            stmt_open = True
        if ch == "'":
            j = i + 1
            while j < n:
                if sql[j] == "'" and j + 1 < n and sql[j + 1] == "'":
                    j += 2
                    continue
                if sql[j] == "'":
                    break
                j += 1
            current.append(sql[i : min(j + 1, n)])
            i = j + 1
            stmt_open = True
            continue
        if ch in ('"', "`", "["):
            j = quoted_end(sql, i)
            current.append(sql[i:j])
            i = j
            stmt_open = True
            continue
        if sql.startswith("--", i):
            j = sql.find("\n", i)
            i = n if j < 0 else j
            continue
        if sql.startswith("/*", i):
            j = sql.find("*/", i + 2)
            i = n if j < 0 else j + 2
            current.append(" ")
            continue
        if ch == ";":
            if depth == 0:
                statement = "".join(current).strip(SQL_TRIM)
                if statement:
                    out.append(statement)
                current = []
                head_words = []
                awaiting_body = False
                stmt_open = False
                i += 1
                continue
            stmt_open = False
            current.append(ch)
            i += 1
            continue
        current.append(ch)
        i += 1
    token = "".join(word).upper()
    if token:
        current.extend(word)
    tail = "".join(current).strip(SQL_TRIM)
    if tail:
        out.append(tail)
    return out


RENAMES = re.compile(
    rf"^RENAME{SQL_SP}(?:TO|AS){SQL_NAME_SEP}({SQL_QUAL})", re.IGNORECASE
)
CONDITIONAL_CREATE = re.compile(
    rf"(?<!{SQL_ID_CONT})IF{SQL_SP}NOT{SQL_SP}EXISTS(?!{SQL_ID_CONT})",
    re.IGNORECASE,
)


def track_born(
    born: set[tuple[str, str]],
    conditional: set[tuple[str, str]],
    statement: str,
    *,
    shadow_safe: bool = False,
    reborn: frozenset[tuple[str, str]] = frozenset(),
) -> tuple | None:
    """Apply one statement's table-lifetime effect to the born sets.

    Returns the event for classification: ("preexisting_drop", ref) when
    the statement drops a table this migration did not create, or None.
    A RENAME moves the born identity to the new name instead of leaving
    a stale one behind. A conditional CREATE joins both sets: born, as
    the repair-migration convention has always claimed, and conditional,
    where the fold-declaration rule holds it to the stricter standard -
    the column may land on the pre-existing table SQLite retained.
    """
    match = CREATE_TABLE.match(statement)
    if match:
        prefix = statement[: match.start(1)]
        temp = bool(re.search(r"\b(?:TEMP|TEMPORARY)\b", prefix, re.IGNORECASE))
        key = sql_table_key(match.group(1), temp)
        born.add(key)
        if CONDITIONAL_CREATE.search(prefix):
            conditional.add(key)
        return None
    match = DROP_TABLE.match(statement)
    if match:
        retired = resolve_born(match.group(1), born)
        if retired is None or retired in reborn:
            return ("preexisting_drop", match.group(1))
        born.discard(retired)
        conditional.discard(retired)
        return None
    match = ALTER.match(statement)
    if match:
        rename = RENAMES.match(match.group(2).strip(SQL_TRIM))
        if rename:
            retired = resolve_born(match.group(1), born)
            if (
                shadow_safe
                and retired is not None
                and retired[0] == "main"
                and len(sql_parts(match.group(1))) < 2
            ):
                # An unqualified source resolves temp before main; a
                # pre-existing temp shadow takes the rename, so the
                # migration's own main identity does not move.
                return None
            if retired is not None and retired not in reborn:
                moved = (retired[0], sql_table_key(rename.group(1))[1])
                born.discard(retired)
                born.add(moved)
                if retired in conditional:
                    conditional.discard(retired)
                    conditional.add(moved)
                return ("rename", match.group(1), rename.group(1))
        return None
    return None


def unquote_name(part: str) -> str:
    """Strip SQL identifier quoting and resolve its doubled escapes."""
    if len(part) >= 2 and part[0] == part[-1] and part[0] in ('"', "'", "`"):
        return part[1:-1].replace(part[0] * 2, part[0])
    if len(part) >= 2 and part[0] == "[" and part[-1] == "]":
        return part[1:-1]
    return part


def bare_name(ref: str) -> str | None:
    """Return one unqualified, unquoted, folded name, or None.

    A single-quoted token is a string, not a name, and a dotted reference
    is qualified, so both refuse: the CHECK and copy-list parsers need
    bare columns exactly.
    """
    text = ref.strip(SQL_TRIM)
    if not text or text[0] == "'":
        return None
    parts = sql_parts(text)
    if len(parts) != 1:
        return None
    return fold_ascii(unquote_name(parts[0]))


def collapse_ws(text: str) -> str:
    """Collapse whitespace runs to single spaces, outside quoted tokens.

    A quoted token keeps every byte: a string, a double-quoted identifier,
    a backticked or bracketed name, and its doubled escapes all survive
    verbatim, whatever whitespace they carry. SQLite reads a double-quoted
    token that names no identifier as the string it spells, so two CHECK
    bodies that differ inside a quoted token are different checks, and a
    normalization that equated them would widen against values SQLite
    never saw.
    """
    out: list[str] = []
    i, n = 0, len(text)
    while i < n:
        ch = text[i]
        if ch in ('"', "'", "`", "["):
            close = "]" if ch == "[" else ch
            j = i + 1
            while j < n:
                k = text.find(close, j)
                if k < 0:
                    j = n
                    break
                if close != "]" and k + 1 < n and text[k + 1] == close:
                    j = k + 2
                    continue
                j = k + 1
                break
            out.append(text[i : min(j, n)])
            i = min(j, n)
            continue
        if ch in SQL_TRIM:
            j = i
            while j < n and text[j] in SQL_TRIM:
                j += 1
            out.append(" ")
            i = j
            continue
        out.append(ch)
        i += 1
    return "".join(out).strip(SQL_TRIM)


def paren_span(text: str, start: int) -> tuple[str, int] | None:
    """Return (inner, index after close) for the group opening at start.

    The scan is literal-aware: a paren inside a string or a quoted
    identifier is a character, and comments hold none. None means the
    group never closes, which the parser refuses rather than guesses.
    """
    depth = 0
    i, n = start, len(text)
    while i < n:
        ch = text[i]
        if ch == "'":
            j = i + 1
            while j < n:
                if text[j] == "'" and j + 1 < n and text[j + 1] == "'":
                    j += 2
                    continue
                if text[j] == "'":
                    break
                j += 1
            i = j + 1
            continue
        if ch in ('"', "`", "["):
            i = quoted_end(text, i)
            continue
        if text.startswith("--", i):
            j = text.find("\n", i)
            i = n if j < 0 else j + 1
            continue
        if text.startswith("/*", i):
            j = text.find("*/", i + 2)
            i = n if j < 0 else j + 2
            continue
        if ch == "(":
            depth += 1
        elif ch == ")":
            depth -= 1
            if depth == 0:
                return text[start + 1 : i], i + 1
        i += 1
    return None


def sql_tokens(text: str) -> list[str]:
    """Split one fragment into refs, strings, words, and punctuation.

    A quoted reference or a string literal is one token; bare identifiers,
    numbers, and single punctuation are their own tokens. Shape
    comparisons rejoin these with single spaces, so fragments the same
    lexer reads the same compare the same whatever spacing the source
    carried.
    """
    out: list[str] = []
    i, n = 0, len(text)
    while i < n:
        ch = text[i]
        if ch in SQL_TRIM:
            i += 1
            continue
        if ch == "'":
            j = i + 1
            while j < n:
                if text[j] == "'" and j + 1 < n and text[j + 1] == "'":
                    j += 2
                    continue
                if text[j] == "'":
                    break
                j += 1
            out.append(text[i : min(j + 1, n)])
            i = j + 1
            continue
        if ch in ('"', "`", "["):
            j = quoted_end(text, i)
            out.append(text[i:j])
            i = j
            continue
        match = re.match(SQL_ID_START + SQL_ID_CONT + "*", text[i:])
        if match:
            out.append(match.group(0))
            i += match.end()
            continue
        match = re.match(r"<=|>=|<>|!=|==|\|\|", text[i:])
        if match:
            out.append(match.group(0))
            i += match.end()
            continue
        match = re.match(r"[0-9][0-9]*(?:\.[0-9]+)?", text[i:])
        signed = re.match(r"[-+][0-9][0-9]*(?:\.[0-9]+)?", text[i:])
        if signed and (
            not out
            or out[-1] in ("(", ",")
            or out[-1] in ("<=", ">=", "<>", "!=", "==", "=", "<", ">")
        ):
            # A sign binds to the number only where no binary minus can
            # stand: after an open paren, a comma, or a comparison.
            out.append(signed.group(0))
            i += signed.end()
            continue
        if match:
            out.append(match.group(0))
            i += match.end()
            continue
        out.append(ch)
        i += 1
    return out


def split_top_level(text: str) -> list[str]:
    """Split on commas at paren depth zero, outside literals."""
    parts: list[str] = []
    current: list[str] = []
    depth = 0
    i, n = 0, len(text)
    while i < n:
        ch = text[i]
        if ch == "'":
            j = i + 1
            while j < n:
                if text[j] == "'" and j + 1 < n and text[j + 1] == "'":
                    j += 2
                    continue
                if text[j] == "'":
                    break
                j += 1
            current.append(text[i : min(j + 1, n)])
            i = j + 1
            continue
        if ch in ('"', "`", "["):
            j = quoted_end(text, i)
            current.append(text[i:j])
            i = j
            continue
        if ch == "(":
            depth += 1
        elif ch == ")":
            depth = max(0, depth - 1)
        if ch == "," and depth == 0:
            piece = "".join(current).strip(SQL_TRIM)
            if piece:
                parts.append(piece)
            current = []
            i += 1
            continue
        current.append(ch)
        i += 1
    tail = "".join(current).strip(SQL_TRIM)
    if tail:
        parts.append(tail)
    return parts


IF_EXISTS_RE = re.compile(rf"IF{SQL_SP}EXISTS", re.IGNORECASE)
IF_NOT_EXISTS_RE = re.compile(rf"IF{SQL_SP}NOT{SQL_SP}EXISTS", re.IGNORECASE)
DROP_TABLE_MAYBE = re.compile(
    rf"^DROP{SQL_SP}TABLE(?:{SQL_SP}IF{SQL_SP}EXISTS)?"
    rf"{SQL_NAME_SEP}({SQL_QUAL})$",
    re.IGNORECASE,
)
DROP_DEPENDENT = re.compile(
    rf"^DROP{SQL_SP}(INDEX|TRIGGER|VIEW)((?:{SQL_SP}IF{SQL_SP}EXISTS)?)"
    rf"{SQL_NAME_SEP}({SQL_QUAL})$",
    re.IGNORECASE,
)
ADD_COLUMN_DEF = re.compile(
    rf"^ADD{SQL_NAME_SEP}(?:COLUMN{SQL_NAME_SEP})?([\s\S]+)$", re.IGNORECASE
)
# Matched against the whitespace-collapsed copy statement: INSERT INTO new
# (columns) SELECT columns FROM scratch. OR IGNORE, a WHERE, or any other
# clause fails the pattern, and a failed pattern fails the proof.
REBUILD_COPY = re.compile(
    rf"^INSERT{SQL_SQ}INTO{SQL_NAME_SEP}({SQL_QUAL}){SQL_NAME_SEP}\("
    rf"([^()]*)\){SQL_NAME_SEP}SELECT{SQL_NAME_SEP}([^()]*)"
    rf"{SQL_SQ}FROM{SQL_NAME_SEP}({SQL_QUAL})$",
    re.IGNORECASE,
)
CHECK_LITERAL = rf"(?:'(?:[^']|'')*'|[+-]?[0-9]+(?:\.[0-9]+)?)"
CHECK_COMPARE = re.compile(
    rf"^({SQL_ID_START}{SQL_ID_CONT}*){SQL_SQ}(<=|>=|<>|!=|==|=|<|>)"
    rf"{SQL_SQ}({CHECK_LITERAL})$",
    re.IGNORECASE,
)
CHECK_MEMBERSHIP = re.compile(
    rf"^({SQL_ID_START}{SQL_ID_CONT}*){SQL_SQ}IN{SQL_SQ}\(([^()]*)\)$",
    re.IGNORECASE,
)
TABLE_HEADS = ("CONSTRAINT", "PRIMARY", "UNIQUE", "FOREIGN", "CHECK")
SQL_WORD = rf"{SQL_ID_START}{SQL_ID_CONT}*"


class CheckBound(NamedTuple):
    """One CHECK literal: kind, SQLite runtime value, and source spelling.

    value is what a comparison runs on: the unquoted text for a text
    literal, and for a numeric literal the exact integer when the literal
    fits int64, otherwise the IEEE-754 double SQLite reads the spelling
    as, carried as that double's exact fraction. literal keeps the source
    spelling, because affinity renders the value: a TEXT-affinity column
    turns 9 into '9' but 9.0 into '9.0', so numerically equal spellings
    accept different rows there.
    """

    kind: str
    value: object
    literal: str


def check_bound(text: str) -> CheckBound | None:
    """Return one literal's proof value, or None outside the family.

    A numeric bound is read the way SQLite reads it: an integer that
    fits int64 stays exact, and every other numeric literal parses to
    its IEEE-754 double. Past 2**53 the spelling and the double part
    company - 9007199254740995.0 reads as 9007199254740996 - so a
    comparison on decimal spellings would call a narrowing an equality
    and admit a rebuild that kills an old-valid row. A literal whose
    double is not finite refuses the family rather than guess.
    """
    text = text.strip(SQL_TRIM)
    if len(text) >= 2 and text[0] == "'" and text[-1] == "'":
        return CheckBound("text", text[1:-1].replace("''", "'"), text)
    if not re.fullmatch(r"[+-]?[0-9]+(?:\.[0-9]+)?", text):
        return None
    if re.fullmatch(r"[+-]?[0-9]+", text):
        value = int(text)
        if -(2**63) <= value < 2**63:
            return CheckBound("number", Fraction(value), text)
        # SQLite converts an integer literal too large for int64 to REAL.
    runtime = float(text)
    if not math.isfinite(runtime):
        return None
    return CheckBound("number", Fraction(runtime), text)


def parse_check_atom(expr: str):
    """Parse one supported CHECK atom, or return None outside the family.

    Supported: one bare column against one literal bound with a monotone
    operator, and one bare column's membership list. Everything else -
    compound expressions, functions, subqueries - is outside the family
    and can only repeat the old text unchanged.
    """
    text = collapse_ws(expr)
    match = CHECK_COMPARE.match(text)
    if match:
        bound = check_bound(match.group(3))
        if bound is None:
            return None
        return ("cmp", fold_ascii(match.group(1)), match.group(2), bound)
    match = CHECK_MEMBERSHIP.match(text)
    if match:
        values = set()
        for piece in match.group(2).split(","):
            bound = check_bound(piece)
            if bound is None:
                return None
            values.add(bound)
        return ("in", fold_ascii(match.group(1)), frozenset(values))
    return None


# Widening compares bounds only over operators whose acceptance sets order
# with the bound. Equality and inequality sets are not order-convex: x = 5
# against x = 3, or x != 5 against x != 3, accept disjoint or incomparable
# sets whatever the bounds do, so a changed equality CHECK can only repeat
# its old text.
MONOTONE_OPERATORS = ("<=", ">=", "<", ">")

# Affinities under which a numeric bound orders the accepted set. The
# comparison rules apply affinity before comparing: a numeric-affinity
# column compares its bound numerically, and a BLOB-affinity column stores
# text and blobs above every number, so each rejects them under either
# bound and the numeric subset ordering holds. A TEXT-affinity column
# applies TEXT affinity to the bound, so n <= 9 compares against '9'
# lexicographically and 9 < 10 proves nothing about '9' versus '10'.
NUMERIC_BOUND_AFFINITIES = frozenset(("INTEGER", "REAL", "NUMERIC", "BLOB"))


def member_covers(old_bound: CheckBound, new_bound: CheckBound, affinity) -> bool:
    """Whether one new member's match set contains one old member's.

    The column and its collation are the same on both sides, so equal
    text covers text. A numeric member matches its runtime value only
    where the column's affinity compares numerically: a TEXT-affinity
    column renders each literal as its own text - 9 becomes '9', but 9.0
    becomes '9.0' - so there, and wherever the column's affinity is
    unknown, only an identically spelled member covers.
    """
    if old_bound.kind != new_bound.kind:
        return False
    if old_bound.kind == "text":
        return old_bound.value == new_bound.value
    if old_bound.literal == new_bound.literal:
        return True
    if affinity not in NUMERIC_BOUND_AFFINITIES:
        return False
    return old_bound.value == new_bound.value


def membership_covers(column: str, old_values, new_values, affinity_of) -> bool:
    """Whether every old member matches under some new member.

    SQLite reads x IN (...) as one equality per element under the
    column's affinity, so the lists compare member by member: a list
    larger than the old one is not yet a superset.
    """
    affinity = affinity_of(column) if affinity_of is not None else None
    return all(
        any(member_covers(bound, other, affinity) for other in new_values)
        for bound in old_values
    )


def atom_widens(old_expr: str, new_expr: str, affinity_of) -> bool | None:
    """Whether new_expr accepts every value old_expr accepts, or None when
    the pair leaves the supported family.

    The comparison's meaning comes from the column the atom names, so the
    proof reads that column's declared-type affinity before it orders any
    bound: affinity_of maps the folded column name to its affinity, and a
    name it cannot resolve refuses. Numeric bounds carry their SQLite
    runtime values, so a REAL bound and an INTEGER bound order the way
    SQLite orders them, rounding and all.
    """
    old = parse_check_atom(old_expr)
    new = parse_check_atom(new_expr)
    if old is None or new is None or old[0] != new[0] or old[1] != new[1]:
        return None
    if old[0] == "in":
        return membership_covers(old[1], old[2], new[2], affinity_of)
    old_op, old_bound = old[2], old[3]
    new_op, new_bound = new[2], new[3]
    if old_op not in MONOTONE_OPERATORS or new_op not in MONOTONE_OPERATORS:
        return None
    upper = old_op in ("<", "<=")
    if upper != (new_op in ("<", "<=")):
        return None
    if old_op == new_op:
        if old_bound == new_bound:
            return True
        # A moved bound orders only where the column's affinity makes the
        # comparison numeric; an unknown column refuses.
        if old_bound.kind != "number" or new_bound.kind != "number":
            return None
    else:
        # One strictness step admits a superset only at one bound and
        # only in the loosening direction: x < a widens to x <= a, and
        # x > a to x >= a. The opposite step cuts the boundary value out
        # and narrows.
        if (old_op, new_op) not in (("<", "<="), (">", ">=")):
            return None
        if old_bound == new_bound:
            return True
        # A loosening whose spellings differ proves its equality the way a
        # moved bound proves its order: only where the affinity compares
        # numerically.
        if old_bound.kind != "number" or new_bound.kind != "number":
            return None
    affinity = affinity_of(old[1]) if affinity_of is not None else None
    if affinity not in NUMERIC_BOUND_AFFINITIES:
        return None
    if old_op == new_op:
        # Same operator: x < a accepts a superset as a grows, x > a as a
        # shrinks. The runtime values order the bounds the way SQLite
        # compares them, so the bound 9007199254740995.0 (the double
        # 9007199254740996) cannot move to 9007199254740995.
        return (
            new_bound.value >= old_bound.value
            if upper
            else new_bound.value <= old_bound.value
        )
    # The loosening step admits a superset only while the inclusive bound
    # matches the strict one at runtime: x < 9007199254740995.0 widens to
    # x <= 9007199254740996, never to x <= 9007199254740995.
    return new_bound.value == old_bound.value


def checks_widen(old_checks, new_checks, affinity_of=None) -> bool:
    """Whether the new CHECK set accepts a superset of the old set.

    Every new check must pair with an old check on the same column: equal
    text, or a widened bound or grown membership inside the family. Old
    checks the new table drops are widenings; any unpaired new check is
    narrowing; any expression outside the family must repeat the old text
    exactly or the pairing refuses.
    """
    unused = [collapse_ws(c) for c in old_checks]
    for new in new_checks:
        text = collapse_ws(new)
        if text in unused:
            unused.remove(text)
            continue
        pair = next(
            (c for c in unused if atom_widens(c, new, affinity_of)), None
        )
        if pair is None:
            return False
        unused.remove(pair)
    return True


class ColumnShape(NamedTuple):
    """One column of a replayed CREATE TABLE: name, non-CHECK text, CHECKs."""

    name: str
    rest: str
    checks: tuple[str, ...]


class TableShape(NamedTuple):
    """One table's replayed definition, CHECKs carried apart for widening."""

    columns: tuple[ColumnShape, ...]
    table_checks: tuple[str, ...]
    table_other: tuple[str, ...]
    tail: str


def is_bare_word(token: str) -> bool:
    return bool(re.match(SQL_WORD + r"$", token)) and token[0] != "'"


def word_is(token: str, keyword: str) -> bool:
    return is_bare_word(token) and token.upper() == keyword


def parse_column_item(item: str) -> ColumnShape | None:
    """Parse one column definition, or None outside the vocabulary.

    The name leads; CHECK groups are lifted and the remaining constraint
    text is kept as one normalized token join, so old and new shapes
    compare equal only when the words match in the same order.
    """
    toks = sql_tokens(item)
    if len(toks) < 2:
        return None
    name = bare_name(toks[0])
    if name is None or is_bare_word(toks[0]) and toks[0].upper() in TABLE_HEADS:
        return None
    checks: list[str] = []
    rest: list[str] = []
    i = 1
    while i < len(toks):
        if (
            word_is(toks[i], "CHECK")
            and i + 1 < len(toks)
            and toks[i + 1] == "("
        ):
            depth = 1
            j = i + 2
            while j < len(toks) and depth:
                if toks[j] == "(":
                    depth += 1
                elif toks[j] == ")":
                    depth -= 1
                j += 1
            if depth:
                return None
            checks.append(" ".join(toks[i + 2 : j - 1]))
            i = j
            continue
        rest.append(toks[i])
        i += 1
    return ColumnShape(name, " ".join(rest), tuple(checks))


def parse_table_item(item: str):
    """One body item: a ColumnShape, a ('check', expr), or a ('other', text)."""
    toks = sql_tokens(item)
    if not toks:
        return None
    if not is_bare_word(toks[0]) or toks[0].upper() not in TABLE_HEADS:
        return parse_column_item(item)
    if word_is(toks[0], "CONSTRAINT"):
        if len(toks) < 3:
            return None
        toks = toks[2:]
        if not is_bare_word(toks[0]) or toks[0].upper() not in TABLE_HEADS:
            return None
        toks = toks[1:]
        head = toks[0].upper()
    else:
        head = toks[0].upper()
    if head == "CHECK":
        if len(toks) < 2 or toks[1] != "(":
            return None
        depth = 1
        j = 2
        while j < len(toks) and depth:
            if toks[j] == "(":
                depth += 1
            elif toks[j] == ")":
                depth -= 1
            j += 1
        if depth:
            return None
        return ("check", " ".join(toks[2 : j - 1]))
    if head in ("PRIMARY", "UNIQUE", "FOREIGN"):
        return ("other", " ".join(toks))
    return None


def parse_table_shape(body: str) -> TableShape | None:
    """Parse a CREATE TABLE body, or None outside the known vocabulary."""
    columns: list[ColumnShape] = []
    table_checks: list[str] = []
    table_other: list[str] = []
    for item in split_top_level(body):
        parsed = parse_table_item(item)
        if parsed is None:
            return None
        if isinstance(parsed, ColumnShape):
            columns.append(parsed)
        elif parsed[0] == "check":
            table_checks.append(parsed[1])
        else:
            table_other.append(parsed[1])
    if not columns:
        return None
    return TableShape(
        tuple(columns), tuple(table_checks), tuple(table_other), ""
    )


def create_table_shape(statement: str):
    """Return (key, temp, conditional, shape) for one CREATE TABLE.

    shape None marks a table whose columns the statement does not declare
    (AS SELECT): the table exists, its shape is unknown, and no rebuild
    proof may use it as a baseline. None means the statement leaves the
    recognized vocabulary entirely.
    """
    match = CREATE_TABLE.match(statement)
    if not match:
        return None
    prefix = statement[: match.start(1)]
    temp = bool(re.search(r"\b(?:TEMP|TEMPORARY)\b", prefix, re.IGNORECASE))
    conditional = bool(CONDITIONAL_CREATE.search(prefix))
    rest = statement[match.end(1) :].strip(SQL_TRIM)
    if not rest:
        return None
    key = sql_table_key(match.group(1), temp)
    if rest[0] == "(":
        span = paren_span(rest, 0)
        if span is None:
            return None
        body, end = span
        head = body.strip(SQL_TRIM)
        if re.match(SQL_WORD, head, re.IGNORECASE) and head.split()[0].upper() in (
            "SELECT",
            "WITH",
        ):
            return (key, temp, conditional, None)
        shape = parse_table_shape(body)
        if shape is None:
            return None
        tail = collapse_ws(rest[end:])
        return (key, temp, conditional, shape._replace(tail=tail))
    if re.match(rf"AS{SQL_SQ}", rest, re.IGNORECASE):
        return (key, temp, conditional, None)
    return None


DEPENDENT_CREATE = re.compile(
    rf"^CREATE{SQL_SP}(?:UNIQUE{SQL_SP})?(?:INDEX|TRIGGER|VIEW)"
    rf"(?:{SQL_SP}IF{SQL_SP}NOT{SQL_SP}EXISTS)?{SQL_NAME_SEP}{SQL_QUAL}",
    re.IGNORECASE,
)


def quote_ident(name: str) -> str:
    """Quote one schema object name the way a PRAGMA reference needs it."""
    return '"' + name.replace('"', '""') + '"'


def _master(schema: str) -> str:
    return "sqlite_temp_master" if schema == "temp" else "sqlite_master"


def table_row(db, schema: str, name: str):
    """The (exact name, sql) master row of one table, or None.

    The lookup compares names the way SQLite does, ASCII-only case
    folding, so the folded identity the SQL readers derive finds the row
    and the row's exact spelling serves every PRAGMA and SELECT that
    follows.
    """
    return db.execute(
        f"SELECT name, sql FROM {_master(schema)} "
        "WHERE type='table' AND name = ? COLLATE NOCASE",
        (name,),
    ).fetchone()


def catalog(db) -> list:
    """Every user object of both schemas as (schema, type, name, tbl_name, sql)."""
    rows = []
    for master, schema in (
        ("sqlite_master", "main"),
        ("sqlite_temp_master", "temp"),
    ):
        rows.extend(
            (schema, kind, name, tbl, sql)
            for kind, name, tbl, sql in db.execute(
                f"SELECT type, name, tbl_name, sql FROM {master} "
                "WHERE substr(name, 1, 7) <> 'sqlite_'"
            )
        )
    return sorted(rows, key=repr)


def index_facts(db, exact: str) -> list:
    """Every index of one table with its origin flags and column details."""
    facts = []
    for row in db.execute(f"PRAGMA index_list({quote_ident(exact)})"):
        columns = db.execute(
            f"PRAGMA index_xinfo({quote_ident(row[1])})"
        ).fetchall()
        facts.append((row[1], row[2], row[3], row[4], tuple(columns)))
    return sorted(facts, key=repr)


def row_image(db, exact: str):
    """Every row with its rowid, or without it when the table has none.

    SELECT rowid failing is SQLite's own word that the table is WITHOUT
    ROWID, so the rowid and rowid-less images never mix.
    """
    try:
        return (
            "rowid",
            sorted(
                db.execute(
                    f"SELECT rowid, * FROM {quote_ident(exact)}"
                ).fetchall(),
                key=repr,
            ),
        )
    except sqlite3.Error:
        return (
            "plain",
            sorted(
                db.execute(f"SELECT * FROM {quote_ident(exact)}").fetchall(),
                key=repr,
            ),
        )


def sequence_row(db, exact: str):
    """The sqlite_sequence entry of one table, or None when there is none."""
    try:
        return db.execute(
            "SELECT seq FROM sqlite_sequence WHERE name = ?", (exact,)
        ).fetchone()
    except sqlite3.Error:
        return None


def probed_affinity(db, declared: str) -> str | None:
    """The affinity SQLite itself applies to one declared type.

    A scratch column of the declared type takes a text and then an
    integer, and typeof of each read is the affinity decision. No Python
    re-derivation of the affinity rules can drift from SQLite's here: a
    dotless-i type word is one known drift, where SQLite's ASCII-only
    folding reads a TEXT affinity a Unicode uppercase would call
    INTEGER. INTEGER and NUMERIC affinities answer alike and share the
    NUMERIC answer; every rule that reads an affinity orders only the
    numeric/text/blob/real split, which the probe answers exactly. None
    means the probe could not run, and an unknown affinity refuses every
    widened-bound pairing.
    """
    if not declared.strip(SQL_TRIM):
        return "BLOB"
    try:
        db.execute("DROP TABLE IF EXISTS probe_affinity")
        db.execute(f"CREATE TABLE probe_affinity(col {declared})")
        db.execute("INSERT INTO probe_affinity(col) VALUES('1')")
        text_read = db.execute(
            "SELECT typeof(col) FROM probe_affinity"
        ).fetchone()[0]
        db.execute("DELETE FROM probe_affinity")
        db.execute("INSERT INTO probe_affinity(col) VALUES(1)")
        number_read = db.execute(
            "SELECT typeof(col) FROM probe_affinity"
        ).fetchone()[0]
        db.execute("DROP TABLE probe_affinity")
    except sqlite3.Error:
        return None
    pair = (text_read, number_read)
    if pair == ("text", "text"):
        return "TEXT"
    if pair == ("text", "integer"):
        return "BLOB"
    if pair == ("real", "real"):
        return "REAL"
    if pair[0] in ("integer", "real"):
        return "NUMERIC"
    return None


def probed_affinities(db, xinfo_rows) -> object:
    """An affinity lookup for one table's columns, from SQLite probes."""

    def affinity_of(column: str):
        for row in xinfo_rows:
            if row[6] == 0 and fold_ascii(row[1]) == column:
                return probed_affinity(db, row[2])
        return None

    return affinity_of


# Distinctive rowids for probe rows: fresh implicit rowids start from 1,
# so a copy that reassigns them can never reproduce these by accident.
PROBE_ROWIDS = (842137, 842139)

NUMERIC_TYPE_WORD = re.compile(r"INT|REAL|FLOA|DOUB|NUM|BOOL", re.IGNORECASE)


def probe_value_candidates(declared: str, pk: bool) -> list:
    """SQL literal candidates for one probe column, best first."""
    if NUMERIC_TYPE_WORD.search(declared):
        return ["842137", "1", "0"] if pk else ["1", "0", "7"]
    if not declared.strip(SQL_TRIM):
        return ["'p'", "842137", "1"]
    return ["'p'", "'0'", "'1'"]


def seed_probe_rows(db, exact: str) -> None:
    """Seed probe rows, suspending the table's own triggers.

    The triggers drop and return by their stored definitions, so the
    schema the candidate migration replays against is byte-identical to
    the one the rows were seeded into; a trigger that will not come back
    raises, and the caller refuses the proof. A row no candidate
    satisfies is skipped: a table left empty proves no rowid identity,
    and the caller refuses that proof too.
    """
    triggers = db.execute(
        "SELECT name, sql FROM sqlite_schema WHERE type='trigger' "
        "AND tbl_name = ? COLLATE NOCASE",
        (exact,),
    ).fetchall()
    for name, _ in triggers:
        db.execute(f"DROP TRIGGER {quote_ident(name)}")
    try:
        columns = [
            (row[1], row[2], row[5])
            for row in db.execute(f"PRAGMA table_xinfo({quote_ident(exact)})")
            if row[6] == 0
        ]
        target = quote_ident(exact)
        for rowid in PROBE_ROWIDS:
            for use_rowid in (True, False):
                names = (["rowid"] if use_rowid else []) + [
                    quote_ident(column) for column, _, _ in columns
                ]
                for attempt in range(3):
                    values = ([str(rowid)] if use_rowid else []) + [
                        probe_value_candidates(declared, bool(pk))[attempt]
                        for _, declared, pk in columns
                    ]
                    statement = (
                        f"INSERT INTO {target}({', '.join(names)}) "
                        f"VALUES({', '.join(values)})"
                    )
                    try:
                        db.execute("SAVEPOINT probe_seed")
                        db.execute(statement)
                        db.execute("RELEASE probe_seed")
                        break
                    except sqlite3.Error:
                        db.execute("ROLLBACK TO probe_seed")
                        db.execute("RELEASE probe_seed")
                else:
                    continue
                break
    finally:
        for _, sql in triggers:
            db.execute(sql)


class World:
    """The replayed schema state a rebuild proof reads its baseline from.

    The world is an in-memory SQLite database that has executed every
    earlier migration's statements, so every schema fact a proof compares
    is one SQLite itself maintains: a rename's reference rewrites, a
    conditional CREATE's no-op over an existing name, and a dependent's
    stored definition all replay exactly as SQLite runs them. poisoned
    marks a state the replay can no longer vouch for - a statement
    SQLite refused, a conditional migration that may not have run, an
    unreadable entry - and a poisoned world proves nothing: a proof read
    from an uncertain before-state would admit a shape an older binary
    never saw.
    """

    def __init__(self) -> None:
        self.db = sqlite3.connect(":memory:")
        self.poisoned = False

    def apply(self, statement: str) -> None:
        if self.poisoned or not statement.strip(SQL_TRIM):
            return
        try:
            self.db.executescript(statement)
        except sqlite3.Error:
            self.poisoned = True

    def resolve(self, ref: str) -> tuple[str, str] | None:
        """Resolve one reference to a replayed table, temp before main."""
        schema, name = sql_table_key(ref)
        if len(sql_parts(ref)) >= 2:
            return (schema, name) if table_row(self.db, schema, name) else None
        for schema in ("temp", "main"):
            if table_row(self.db, schema, name):
                return (schema, name)
        return None

    def plainkey(self, ref: str) -> tuple[str, str]:
        """Resolve one reference's identity without requiring it to exist.

        The rename-away scratch table exists only after the rename, so
        the rebuild scan names it against the before-world, where it is
        absent: this resolves the name temp-before-main the way SQLite
        would.
        """
        schema, name = sql_table_key(ref)
        if len(sql_parts(ref)) < 2 and table_row(self.db, "temp", name):
            return ("temp", name)
        return (schema, name)

    @property
    def objects(self) -> dict:
        """Every dependent object's name under SQLite's one namespace."""
        out: dict = {}
        for master in ("sqlite_master", "sqlite_temp_master"):
            for kind, name in self.db.execute(
                f"SELECT type, name FROM {master} "
                "WHERE type IN ('index','trigger','view') "
                "AND substr(name, 1, 7) <> 'sqlite_'"
            ):
                out[fold_ascii(name)] = kind
        return out


def rebuild_scan(stmts: list[str], world: World):
    """Find a rename-away rebuild and prove or refuse it.

    Returns (consumed, reborn) when the proof holds: the consumed
    statements classify as additive and the reborn name classifies as
    pre-existing. A candidate that fails its proof returns (set(),
    reborn): every statement keeps today's classification, and the
    recreated name still owes the pre-existing rules an older binary's
    knowledge earns. None means no rename-away recreation of a known
    table happens in this migration.

    The proof admits one shape and nothing beside it: dependent drops
    before the rename, the rename, the recreation, one copy, the scratch
    drop, then restorations. A write anywhere else in the migration can
    erase the rows the copy claims to move, and any other statement
    carries no proof, so both refuse. Whether the restorations truly
    restore is not read from the statements at all: the trial replay
    below compares the SQLite catalog before and after the migration.
    """
    if world.poisoned:
        return None
    renames = []
    for i, statement in enumerate(stmts):
        match = ALTER.match(statement)
        if not match:
            continue
        rename = RENAMES.match(match.group(2).strip(SQL_TRIM))
        if not rename:
            continue
        key = world.resolve(match.group(1))
        if key is None:
            continue
        moved = (key[0], sql_table_key(rename.group(1))[1])
        if table_row(world.db, moved[0], moved[1]):
            continue
        renames.append((i, key, moved))
    if not renames:
        return None
    if len(renames) > 1:
        # Two rebuilt identities in one migration cannot be proved apart;
        # both recreated names still owe the pre-existing rules.
        return (set(), frozenset(key for _, key, _ in renames))
    at_rename, key, moved = renames[0]
    reborn = frozenset((key,))
    for j in range(at_rename):
        # Only dropping dependents is provable before the rename; a write
        # here can erase the rows the copy will claim to move, and every
        # other statement is unproved.
        if DROP_DEPENDENT.match(stmts[j]) is None:
            return (set(), reborn)
    # The recreated table must appear under the old name directly after the
    # rename: the scratch carries the only live rows, and nothing between
    # the two statements is provable.
    at_create = None
    for j in range(at_rename + 1, len(stmts)):
        match = CREATE_TABLE.match(stmts[j])
        if match is None:
            return (set(), reborn)
        prefix = stmts[j][: match.start(1)]
        if CONDITIONAL_CREATE.search(prefix):
            return (set(), reborn)
        temp = bool(re.search(r"\b(?:TEMP|TEMPORARY)\b", prefix, re.IGNORECASE))
        if sql_table_key(match.group(1), temp) != key:
            return (set(), reborn)
        at_create = j
        break
    if at_create is None:
        return None
    # After the recreation: exactly one copy, the scratch drop, then the
    # restorations. Anything else in the stretch is unproved.
    at_copy = None
    copy_match = None
    at_drop = None
    for j in range(at_create + 1, len(stmts)):
        statement = stmts[j]
        copy = REBUILD_COPY.match(collapse_ws(statement))
        if copy is not None:
            if at_copy is not None or at_drop is not None:
                return (set(), reborn)
            at_copy, copy_match = j, copy
            continue
        drop = DROP_TABLE_MAYBE.match(statement)
        if drop is not None and world.plainkey(drop.group(1)) == moved:
            if at_drop is not None:
                return (set(), reborn)
            at_drop = j
            continue
        if at_drop is not None and (
            DROP_DEPENDENT.match(statement) is not None
            or DEPENDENT_CREATE.match(statement) is not None
        ):
            # Restorations, and the drop-and-recreation of a view the
            # rename stranded on the scratch name, follow the scratch
            # drop; whether every object truly returns is the catalog
            # comparison below.
            continue
        return (set(), reborn)
    if at_copy is None or at_drop is None:
        return (set(), reborn)
    if not rebuild_proves(world, stmts, key, moved, copy_match):
        return (set(), reborn)
    return set(range(len(stmts))), reborn


def rebuild_proves(world: World, stmts, key, moved, copy_match) -> bool:
    """The SQLite-replay proof for one rename-away rebuild candidate.

    A trial database clones the replayed world, seeds probe rows through
    the rebuilt table's own constraints, snapshots every catalog fact,
    runs the candidate migration, and compares. The proof holds only
    when every schema object outside the table returns byte-identical,
    the table's PRAGMA facts are unchanged, no AUTOINCREMENT state
    exists on either side, the seeded rows return with their rowids, the
    stored definitions differ only by CHECKs that widen inside the
    supported family under SQLite-probed affinity, and the copy names
    every column. Any refusal keeps the migration breaking.
    """
    before_row = table_row(world.db, key[0], key[1])
    if before_row is None:
        return False
    exact, before_sql = before_row
    trial = sqlite3.connect(":memory:")
    world.db.backup(trial)
    try:
        # Probe rows carry identity and losslessness, never validity: the
        # textual family below owns CHECK widening, so the trial reads
        # them through CHECK enforcement the way a recovery tool would.
        trial.executescript("PRAGMA ignore_check_constraints = ON;")
        seed_probe_rows(trial, exact)
        before_catalog = catalog(trial)
        before_xinfo = trial.execute(
            f"PRAGMA table_xinfo({quote_ident(exact)})"
        ).fetchall()
        before_fk = trial.execute(
            f"PRAGMA foreign_key_list({quote_ident(exact)})"
        ).fetchall()
        before_index = index_facts(trial, exact)
        before_sequence = sequence_row(trial, exact)
        before_rows = row_image(trial, exact)
        trial.executescript(";\n".join(stmts) + ";")
    except sqlite3.Error:
        return False
    after_row = table_row(trial, key[0], key[1])
    if after_row is None:
        return False
    _, after_sql = after_row
    if any(fold_ascii(row[2]) == fold_ascii(exact) for row in before_fk):
        # The table carries a self-referencing foreign key: the store runs
        # migrations under immediate foreign-key checking, so a copy that
        # inserts rows referencing later rows fails at runtime, and the
        # shape cannot prove the stored rows' insertion order.
        return False
    try:
        after_catalog = catalog(trial)
        after_xinfo = trial.execute(
            f"PRAGMA table_xinfo({quote_ident(exact)})"
        ).fetchall()
        after_fk = trial.execute(
            f"PRAGMA foreign_key_list({quote_ident(exact)})"
        ).fetchall()
        after_index = index_facts(trial, exact)
        after_sequence = sequence_row(trial, exact)
        after_rows = row_image(trial, exact)
    except sqlite3.Error:
        return False
    if table_row(trial, moved[0], moved[1]):
        return False
    # Every object outside the rebuilt table returns with its exact
    # stored definition: a rename rewrote every inbound foreign key,
    # trigger body, and view onto the scratch name, so an un-restored
    # reference - and any other quiet schema change - refuses here.
    def other_objects(rows):
        return [
            row
            for row in rows
            if not (row[0] == key[0] and row[1] == "table" and fold_ascii(row[2]) == key[1])
        ]

    if other_objects(before_catalog) != other_objects(after_catalog):
        return False
    if before_xinfo != after_xinfo:
        return False
    if before_fk != after_fk:
        return False
    if before_index != after_index:
        return False
    if before_sequence is not None or after_sequence is not None:
        # The AUTOINCREMENT high-water mark lives in sqlite_sequence,
        # outside the copied rows: any such state on either side - a
        # rename leaving it on the scratch name, or a recreation adding
        # one - refuses.
        return False
    if before_rows[0] == "rowid" and not before_rows[1]:
        # An empty rowid table proves nothing about rowid identity.
        return False
    if before_rows != after_rows:
        return False
    old_created = create_table_shape(before_sql)
    new_created = create_table_shape(after_sql)
    if old_created is None or new_created is None:
        return False
    old_shape, new_shape = old_created[3], new_created[3]
    if old_shape is None or new_shape is None:
        return False
    if not shape_widens_only(
        old_shape, new_shape, probed_affinities(trial, before_xinfo)
    ):
        return False
    insert_columns = [bare_name(p) for p in split_top_level(copy_match.group(2))]
    select_columns = [bare_name(p) for p in split_top_level(copy_match.group(3))]
    if None in insert_columns or None in select_columns:
        return False
    if insert_columns != [c.name for c in new_shape.columns]:
        return False
    if select_columns != insert_columns:
        return False
    if world.plainkey(copy_match.group(1)) != key or world.plainkey(
        copy_match.group(4)
    ) != moved:
        return False
    return True


def shape_widens_only(old: TableShape, new: TableShape, affinity_of) -> bool:
    """Whether new is old with only the CHECK family widened.

    Every column keeps its name and every non-CHECK word of its
    definition, the non-CHECK table constraints and the tail (STRICT,
    WITHOUT ROWID) repeat, and each column's CHECK set and the
    table-level CHECK set widen under checks_widen with the affinity the
    probe read from SQLite.
    """
    if len(old.columns) != len(new.columns):
        return False
    for old_column, new_column in zip(old.columns, new.columns):
        if old_column.name != new_column.name:
            return False
        if old_column.rest != new_column.rest:
            return False
    for old_column, new_column in zip(old.columns, new.columns):
        if not checks_widen(old_column.checks, new_column.checks, affinity_of):
            return False
    if old.table_other != new.table_other or old.tail != new.tail:
        return False
    return checks_widen(old.table_checks, new.table_checks, affinity_of)

def classify(sql: str, world: World | None = None) -> list[str]:
    """Return the reasons this migration breaks an older binary, if any.

    A table created by this same migration is invisible to an older binary,
    so dropping it, indexing it, or putting a trigger on it breaks nothing -
    unless the name is a rebuilt identity: a rename-away recreation frees a
    name an older binary knows, so the born rules stop applying to it and
    the pre-existing rules take over (reborn). With a replayed world, a
    rebuild whose shape proof holds classifies as additive; an unproved
    statement keeps the classification below, and the proof refuses on an
    unknown baseline, a narrowed or novel CHECK, an altered column,
    default, key, or foreign-key shape, an inbound reference from another
    table, an unproved write, unsupported SQL, a lossy copy, or a
    dependent that does not return.
    """
    stmts = statements(sql)
    scanned = rebuild_scan(stmts, world) if world is not None else None
    consumed = scanned[0] if scanned else set()
    reborn = scanned[1] if scanned else frozenset()
    born: set[tuple[str, str]] = set()
    scratch: set[tuple[str, str]] = set()
    reasons: list[str] = []
    for i, statement in enumerate(stmts):
        if not statement:
            continue
        if i in consumed:
            track_born(born, scratch, statement, reborn=reborn)
            continue
        event = track_born(born, scratch, statement, reborn=reborn)
        if event and event[0] == "preexisting_drop":
            reasons.append(f"drops the pre-existing table {event[1]}")
            continue
        if CREATE_TABLE.match(statement):
            match = CREATE_TABLE.match(statement)
            prefix = statement[: match.start(1)]
            temp = bool(re.search(r"\b(?:TEMP|TEMPORARY)\b", prefix, re.IGNORECASE))
            key = sql_table_key(match.group(1), temp)
            if (
                world is not None
                and not world.poisoned
                and key[0] == "main"
                and key[1] in world.objects
            ):
                # SQLite's one namespace: a conditional CREATE over the
                # dependent object's name is the no-op SQLite runs and an
                # unconditional one the error it raises. Either way the
                # name an older binary knows stays a dependent object, and
                # no table can be proved under it.
                reasons.append(
                    f"re-creates the pre-existing dependent object {match.group(1)} as a table"
                )
            continue
        if event is not None or DROP_TABLE.match(statement):
            continue
        match = DROP_INDEX.match(statement)
        if match:
            continue
        match = ALTER.match(statement)
        if match:
            table, rest = match.group(1), match.group(2).strip(SQL_TRIM)
            owned = resolve_born(table, born)
            if owned is not None and owned not in reborn:
                continue
            if ADD_COLUMN.match(rest):
                continue
            reasons.append(f"alters the pre-existing table {table}: {first_sql_word(rest)}")
            continue
        match = INDEX_ON.match(statement)
        if match:
            owned = resolve_born(match.group(2), born)
            if match.group(1) and (owned is None or owned in reborn):
                reasons.append(
                    f"adds a unique index to the pre-existing table {match.group(2)}"
                )
            continue
        match = TRIGGER_ON.match(statement)
        if match:
            owned = resolve_born(match.group(1), born)
            if owned is None or owned in reborn:
                reasons.append(
                    f"adds a trigger to the pre-existing table {match.group(1)}"
                )
            continue
        if VIEW_OR_PRAGMA.match(statement) or WRITE.match(statement):
            continue
        reasons.append(f"uses an unclassified statement: {first_sql_word(statement)}")
    return reasons


class Tok(NamedTuple):
    kind: str  # ident, string, raw, rune, number, punct, newline
    text: str
    at: int  # offset of the token's first byte in the scanned source


GO_IDENT = re.compile(r"[A-Za-z_][A-Za-z0-9_]*")
GO_NUMBER = re.compile(r"\d[\w.]*")
# Integer literal forms of the Go spec: decimal, 0-leading octal, and the
# 0x, 0o, and 0b prefixes, with underscores between digits. Anything else
# a number token can carry (a float, an exponent) is not a version.
GO_INT = re.compile(r"^(0[xX][0-9a-fA-F_]+|0[bB][01_]+|0[oO][0-7_]+|0[0-7_]*|[1-9][0-9_]*|0)$")


def go_int(text: str) -> int | None:
    """Parse one Go integer literal, or None when the text is not one."""
    if not GO_INT.match(text):
        return None
    digits = text.replace("_", "")
    lowered = digits.lower()
    if lowered.startswith("0x"):
        return int(digits, 16)
    if lowered.startswith("0b"):
        return int(digits, 2)
    if lowered.startswith("0o"):
        return int(digits, 8)
    if digits.startswith("0") and len(digits) > 1:
        return int(digits, 8)
    return int(digits, 10)


def go_tokens(region: str) -> list[Tok]:
    """Tokenize the region's Go code enough to find keyed struct fields.

    Comments produce no tokens, string and raw literals are each one token,
    and newlines survive as tokens. Field recognition therefore never
    depends on how source is laid out on lines: text inside a literal or a
    comment cannot pose as a field, a comment cannot hide one wherever it
    sits, and two fields on one line are still two fields.
    """
    out: list[Tok] = []
    i, n = 0, len(region)
    while i < n:
        ch = region[i]
        if ch == "\n":
            out.append(Tok("newline", "\n", i))
            i += 1
        elif ch in " \t\r":
            i += 1
        elif region.startswith("//", i):
            end = region.find("\n", i)
            i = n if end < 0 else end
        elif region.startswith("/*", i):
            end = region.find("*/", i + 2)
            i = n if end < 0 else end + 2
        elif ch == '"':
            j = i + 1
            while j < n and region[j] != '"':
                j += 2 if region[j] == "\\" else 1
            j = min(j, n - 1)
            out.append(Tok("string", region[i : j + 1], i))
            i = j + 1
        elif ch == "`":
            end = region.find("`", i + 1)
            end = n - 1 if end < 0 else end
            out.append(Tok("raw", region[i : end + 1], i))
            i = end + 1
        elif ch == "'":
            j = i + 1
            while j < n and region[j] != "'":
                j += 2 if region[j] == "\\" else 1
            j = min(j, n - 1)
            out.append(Tok("rune", region[i : j + 1], i))
            i = j + 1
        else:
            match = GO_IDENT.match(region, i)
            kind = "ident"
            if not match:
                match = GO_NUMBER.match(region, i)
                kind = "number"
            if match:
                out.append(Tok(kind, match.group(0), i))
                i = match.end()
            else:
                out.append(Tok("punct", ch, i))
                i += 1
    return out


def field_run(entry: str, name: str) -> list[Tok] | None:
    """Return the value tokens of the entry's keyed field `name`.

    None means the entry never declares the field at the migration
    literal's own level: a field name inside a value, such as a local
    struct or an Applies function body, sits at deeper bracket depth and
    is part of that value, never a declaration. The run spans from after
    the colon to the comma that closes the value at bracket depth zero,
    so a function-valued field's internal commas stay inside it. A run
    holding only a newline means the value starts on a later line than
    its colon.
    """
    tokens = go_tokens(entry)
    depth = 0
    for i, tok in enumerate(tokens):
        if tok.kind == "punct":
            if tok.text in "([{":
                depth += 1
            elif tok.text in ")]}":
                depth = max(0, depth - 1)
            continue
        if depth != 0 or tok.kind != "ident" or tok.text != name:
            continue
        if i + 1 >= len(tokens) or tokens[i + 1].text != ":":
            continue
        run: list[Tok] = []
        value_depth = 0
        for value in tokens[i + 2 :]:
            if value.kind == "newline" and not run:
                return [value]
            if value.kind == "punct":
                if value.text in "([{":
                    value_depth += 1
                elif value.text in ")]}":
                    value_depth -= 1
                elif value.text == "," and value_depth <= 0:
                    break
            run.append(value)
        return run
    return None


def fold_maintained(entry: str) -> str:
    """Return the entry's FoldMaintained declaration, or "" when the field
    is absent.

    The field is recognized as Go tokens, never as a text shape: comments
    and literal contents cannot supply or hide it. Only a value that is
    exactly one interpreted string literal unquotes; any other expression,
    such as a raw string, a constant, or a concatenation, stays as unparsed
    text and fails the vocabulary check, so a present field never reads as
    an absent one. A value that starts on a later line returns
    FOLD_VALUE_ELSEWHERE: the checker refuses the multiline form rather
    than guess at it, so the declaration keeps its value on the field's
    own line. The declaration is a signed human claim (see the migration
    struct), so a genuinely missing field is an empty declaration rather
    than a parse error.
    """
    run = field_run(entry, "FoldMaintained")
    if run is None:
        return ""
    if not run or run[0].kind == "newline":
        return FOLD_VALUE_ELSEWHERE
    if len(run) == 1 and run[0].kind == "string":
        return run[0].text[1:-1]
    return " ".join(tok.text for tok in run)


def declares_breaking(entry: str) -> bool:
    """Return whether the entry declares Breaking as the literal true.

    The same token recognition governs this field: a true inside a comment
    or a literal is not a declaration, and any value other than the single
    identifier true reads as undeclared, which fails closed.
    """
    run = field_run(entry, "Breaking")
    return bool(run) and len(run) == 1 and run[0].kind == "ident" and run[0].text == "true"


def adds_preexisting_column(sql: str, world: World | None = None) -> bool:
    """Return whether any statement adds a column to a table the migration
    did not create.

    A FoldMaintained declaration describes exactly this shape: a column on a
    table an older binary's fold generation already writes. A rebuilt name
    is such a table: the migration freed a name an older binary knows and
    created a table under it again, so the fold duty reaches it too.
    """
    stmts = statements(sql)
    scanned = rebuild_scan(stmts, world) if world is not None else None
    reborn = scanned[1] if scanned else frozenset()
    born: set[tuple[str, str]] = set()
    conditional: set[tuple[str, str]] = set()
    for statement in stmts:
        track_born(born, conditional, statement, shadow_safe=True, reborn=reborn)
        match = ALTER.match(statement)
        if match:
            ref = match.group(1)
            key = resolve_born(ref, born)
            # A qualified reference owns exactly its schema. An unqualified
            # one resolves temp before main at runtime, so this migration's
            # own main table does not own it: a pre-existing temp shadow
            # takes the statement, and connection-local tables are invisible
            # here. A reborn name is pre-existing for this duty. Only a
            # temp-born, unrebuilt name is provably owned unqualified.
            provably_owned = key is not None and key not in reborn and (
                len(sql_parts(ref)) >= 2 or key[0] == "temp"
            )
            if provably_owned and key not in conditional:
                continue
            if ADD_COLUMN.match(match.group(2).strip(SQL_TRIM)):
                return True
    return False


def sql_literal(entry: str) -> str:
    """Return the SQL field's content, or "" when the field is absent.

    The value comes from the token-recognized SQL field, so a commented or
    nested lookalike cannot supply it; a field whose value is anything but
    one raw string literal also reads as empty and sql_field_sound refuses
    the entry separately. The content is the Go raw-string value: the
    language discards carriage returns inside raw string literals, so the
    checker discards them too and classifies the SQL that actually runs.
    """
    run = field_run(entry, "SQL")
    if run and len(run) == 1 and run[0].kind == "raw":
        return run[0].text[1:-1].replace("\r", "")
    return ""


def sql_field_sound(entry: str) -> bool:
    """Return whether the SQL field is absent or exactly one raw literal."""
    run = field_run(entry, "SQL")
    return run is None or (len(run) == 1 and run[0].kind == "raw")


def migrations_binding(tokens: list[Tok]) -> int:
    """Return the index of the package-level migrations literal's opening
    brace token.

    The binding is the token sequence var migrations = []migration followed
    by an opening brace, at package scope: bracket depth zero. A lookalike
    sequence inside a comment, a literal, or a function body is never these
    tokens at this scope and cannot redirect the walk to a decoy list.
    """
    keys = ("var", "migrations", "=", "[", "]", "migration")
    depth = 0
    for i, tok in enumerate(tokens):
        if i + len(keys) < len(tokens):
            window = tokens[i : i + len(keys)]
            if (
                depth == 0
                and all(
                    t.kind in ("ident", "punct") and t.text == key
                    for t, key in zip(window, keys)
                )
                and tokens[i + len(keys)].kind == "punct"
                and tokens[i + len(keys)].text == "{"
            ):
                return i + len(keys)
        if tok.kind == "punct":
            if tok.text in "([{":
                depth += 1
            elif tok.text in ")]}":
                depth = max(0, depth - 1)
    raise ValueError("no migrations binding in source")


def brace_close(sig: list[Tok], open_at: int) -> int:
    """Return the index of the bracket that closes sig[open_at], or -1."""
    depth = 0
    for i in range(open_at, len(sig)):
        tok = sig[i]
        if tok.kind == "punct" and tok.text in "([{":
            depth += 1
        elif tok.kind == "punct" and tok.text in ")]}":
            depth -= 1
            if depth == 0:
                return i
    return -1


def migrations(source: str) -> list[tuple[int, str, str]]:
    """Return each migration's version, its entry text, and its SQL.

    A list element is one complete expression, split at top-level commas
    over every bracket type. Only a migration composite literal is a
    readable entry: the elided-type group, or the type name beside one
    group that ends the element. A call, a selector, a method call on a
    literal, or any other expression is refused whole, never mined for a
    nested literal that is not the element itself. Classification reads
    significant tokens, so a comment in the element changes nothing.

    Field recognition inside an entry is the token walk in field_run: a
    comment or a literal cannot supply or hide a field, and field order
    does not affect recognition. The SQL is the token-recognized SQL
    field's raw string content. An entry whose Version field is missing
    or not one Go integer literal parses with version -1; evaluate
    refuses it by name. A non-literal element parses with version -2.
    """
    tokens = go_tokens(source)
    opener = migrations_binding(tokens)
    out: list[tuple[int, str, str]] = []
    depth = 1
    element: list[Tok] = []

    def flush() -> None:
        nonlocal element
        sig = [t for t in element if t.kind != "newline"]
        element = []
        if not sig:
            return
        open_at = 0
        if (
            sig[0].kind == "ident"
            and sig[0].text == "migration"
            and len(sig) > 1
            and sig[1].text == "{"
        ):
            open_at = 1
        if open_at == 1 or sig[0].text == "{":
            close = brace_close(sig, open_at)
            if close == len(sig) - 1:
                entry = source[sig[open_at].at + 1 : sig[close].at]
                version_run = field_run(entry, "Version")
                version = -1
                if (
                    version_run
                    and len(version_run) == 1
                    and version_run[0].kind == "number"
                ):
                    version = go_int(version_run[0].text) or -1
                out.append((version, entry, sql_literal(entry)))
                return
        end = sig[-1].at + len(sig[-1].text)
        out.append((UNSUPPORTED_ELEMENT, source[sig[0].at : end], ""))

    for tok in tokens[opener + 1 :]:
        if tok.kind == "punct" and tok.text in ")]}":
            depth -= 1
            if depth == 0:
                flush()
                break
        if tok.kind == "punct" and tok.text == "," and depth == 1:
            flush()
            continue
        if tok.kind == "punct" and tok.text in "([{":
            depth += 1
        element.append(tok)
    return out


def evaluate(entries: list[tuple[int, str, str]]) -> tuple[list[str], list[int]]:
    """Compare every entry's declarations with what its SQL does.

    Returns the refusal messages and the versions whose SQL, or whose advance
    declaration, breaks an older binary. Migrations at or below RULE_FLOOR
    predate the FoldMaintained field and are held to the statement rules
    alone. A World replays each migration's schema effect as it runs, so
    a later rebuild can prove itself against the schema its predecessors
    produced; a conditional migration, an unreadable entry, or one
    statement the replay cannot interpret poisons that world, and a
    poisoned world proves nothing.
    """
    failures: list[str] = []
    breaking_versions: list[int] = []
    world = World()
    for version, entry, sql in entries:
        if version == UNSUPPORTED_ELEMENT:
            world.poisoned = True
            label = " ".join(entry.split())[:60]
            failures.append(
                f"migration list element {label} is not a composite literal; "
                "the checker refuses to evaluate it"
            )
            continue
        if version < 0:
            world.poisoned = True
            name_run = field_run(entry, "Name")
            label = "<unnamed>"
            if name_run and len(name_run) == 1 and name_run[0].kind in ("string", "raw"):
                label = name_run[0].text[1:-1]
            failures.append(
                f"migration entry {label} carries no readable Version field; "
                "the checker refuses to evaluate it"
            )
            continue
        declared = declares_breaking(entry)
        fold = fold_maintained(entry)
        if not sql_field_sound(entry):
            world.poisoned = True
            failures.append(
                f"migration {version} carries an SQL field that is not one raw "
                "string literal; the checker refuses to read it"
            )
        if field_run(entry, "Applies") is not None:
            # A conditional migration may not have run, so the world
            # cannot vouch for the schema its successors prove against.
            world.poisoned = True
        reasons = classify(sql, world)
        if version > RULE_FLOOR:
            adds_column = adds_preexisting_column(sql, world)
            if adds_column and not fold:
                failures.append(
                    f"migration {version} adds a column to a pre-existing table "
                    'without a FoldMaintained declaration; declare "advance" '
                    'with Breaking: true, declare "origin", or qualify the '
                    "reference with its schema so the migration owns it"
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
        # The world replays what ran, not what was allowed: the proof
        # for a later rebuild reads this state as its baseline.
        for statement in statements(sql):
            world.apply(statement)
    return failures, breaking_versions


def main() -> int:
    # newline="" keeps the file's own characters: Go raw-string semantics
    # discard carriage returns at value derivation (see sql_literal), and
    # universal-newline translation would erase the evidence first.
    with open(SCHEMA, encoding="utf-8", newline="") as handle:
        source = handle.read()
    entries = migrations(source)
    if not entries:
        print("check-migration-compatibility: no migrations found", file=sys.stderr)
        return 1

    eval_failures, breaking_versions = evaluate(entries)
    failures = list(eval_failures)
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
