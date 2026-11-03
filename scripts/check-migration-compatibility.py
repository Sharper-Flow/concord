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
Above JSON_REMOVAL_RULE_FLOOR, a data write that calls json_remove with removal
paths also requires a breaking declaration. Published compatibility declarations
remain immutable, so this responsibility applies only after that boundary.

The classification is deliberately conservative: an unrecognized statement
counts as breaking. Being wrong toward breaking costs a refusal that a newer
binary resolves. Being wrong toward additive lets an old binary write against
a shape it does not know, which is silent corruption.

Fold-maintained columns and per-effect tables are additive shapes this
comparison cannot see.
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
refuse one that contradicts the SQL's shape or its own terms. "advance" also
covers a newly created table the write path maintains per effect: an older
binary never writes its rows, leaving gaps with no convergence. It requires
either a column added to a pre-existing table or a CREATE TABLE statement,
and Breaking: true. "origin" keeps its column-only rule.

A table rebuild has one SQLite shape: rename the table away, recreate it
under its old name, copy the rows, drop the scratch copy, and restore every
index, trigger, and view it carried. The rename and the drop name objects an
older binary knows, so this validator reads every such rebuild as breaking
and the migration must declare Breaking: true. Nothing here proves a rebuild
additive; an additive verdict comes only from creating objects or adding a
nullable or defaulted column.

evaluate() also replays each migration into a World, an in-memory SQLite
database executing the statements as they run (CD-0055 D2: the tool that
parses SQL owns every schema fact). The replay feeds one rule: a CREATE
TABLE over a name the replayed schema already carries as a dependent
object (index, trigger, or view) refuses, because that name stays a
dependent object an older binary knows. A conditional migration, an
unreadable entry, or a statement the replay cannot interpret poisons the
world, and a poisoned world rules nothing.

Remaining textual pieces, each a CD-0055 D3 declared exception with the
same failure class (silent corruption invisible where it is authored) and a
named structural end-state: the statement splitter and head-word recognizers
read only enough to route a statement to a rule, and their miss shape is
fail-closed ("unclassified statement" stays breaking); the end-state that
retires them is a replayed-SQLite surface that reports each statement's
effect, so no statement is routed by its text. Go-source reading (the
migrations table extractor and the declaration parsers) parses Go, not SQL,
because the checker reads the shipped source without executing the toolchain.
"""

from __future__ import annotations

import re
import sqlite3
import sys
from pathlib import Path

from migration_source import UNSUPPORTED_ELEMENT, field_run, migrations

SCHEMA = Path(__file__).resolve().parent.parent / "internal" / "store" / "schema.go"

# The fold-maintenance rules bind only migrations above this floor. Migration
# 108 was the last fold-maintained column admitted without a declaration; its
# repair, migration 109, is the first version the rules could have bound.
RULE_FLOOR = 108

# Published declarations are immutable. Earlier JSON metadata cleanups keep
# their recorded compatibility; data-removal enforcement starts after 174.
JSON_REMOVAL_RULE_FLOOR = 174

FOLD_VOCABULARY = ("advance", "origin")
# What fold_maintained returns for a field whose value is not on the field's
# own line: present, unreadable, and outside every vocabulary. Text no Go
# value can carry keeps it from colliding with a real residue.
FOLD_VALUE_ELSEWHERE = "<value on a later line>"
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
        if retired is None:
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
            if retired is not None:
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
    is qualified, so both refuse.
    """
    text = ref.strip(SQL_TRIM)
    if not text or text[0] == "'":
        return None
    parts = sql_parts(text)
    if len(parts) != 1:
        return None
    return fold_ascii(unquote_name(parts[0]))


def sql_tokens(text: str) -> list[str]:
    """Split one fragment into refs, strings, words, and punctuation.

    A quoted reference or a string literal is one token; bare identifiers,
    numbers, and single punctuation are their own tokens.
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


SQL_WORD = rf"{SQL_ID_START}{SQL_ID_CONT}*"


def is_bare_word(token: str) -> bool:
    return bool(re.match(SQL_WORD + r"$", token)) and token[0] != "'"


def word_is(token: str, keyword: str) -> bool:
    return is_bare_word(token) and token.upper() == keyword


class World:
    """The replayed schema state the dependent-object rule reads names from.

    The world is an in-memory SQLite database that has executed every
    earlier migration's statements, so the dependent-object names the
    classifier reads are ones SQLite itself maintains: a conditional
    CREATE's no-op over an existing name replays exactly as SQLite runs
    it. poisoned marks a state the replay can no longer vouch for - a
    statement SQLite refused, a conditional migration that may not have
    run, an unreadable entry - and a poisoned world rules nothing.
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


def destructive_json_write(statement: str) -> bool:
    """Recognize removal calls in writes, using comment-free SQL tokens."""
    tokens = sql_tokens(statement)
    if not tokens:
        return False
    writes = {"insert", "update", "delete", "replace"}
    operation = fold_ascii(tokens[0])
    operation_at = 0
    if word_is(tokens[0], "WITH"):
        depth = 0
        for at, token in enumerate(tokens[1:], 1):
            if token == "(":
                depth += 1
            elif token == ")":
                depth -= 1
            elif depth == 0 and is_bare_word(token) and fold_ascii(token) in writes | {"select"}:
                operation, operation_at = fold_ascii(token), at
                break
    if operation not in writes:
        return False
    depth = 0
    for at, token in enumerate(tokens[:-1]):
        if token == "(":
            depth += 1
        elif token == ")":
            depth -= 1
        if at < operation_at and depth == 0:
            continue  # CTE names and their column lists are not calls.
        if bare_name(token) != "json_remove" or tokens[at + 1] != "(":
            continue
        if at and (
            tokens[at - 1] == "."
            or any(word_is(tokens[at - 1], word) for word in ("INTO", "REPLACE", "AS"))
        ):
            continue  # Write targets, aliases and CAST type names.
        call_depth = 0
        removal_path = False
        for end in range(at + 1, len(tokens)):
            argument = tokens[end]
            if argument == "(":
                call_depth += 1
            elif argument == ")":
                call_depth -= 1
                if call_depth == 0:
                    # INSERT sources can introduce their own CTE column lists.
                    following = end + 1
                    if following < len(tokens) and word_is(tokens[following], "AS"):
                        following += 1
                        for keyword in ("NOT", "MATERIALIZED"):
                            if following < len(tokens) and word_is(tokens[following], keyword):
                                following += 1
                        if following < len(tokens) and tokens[following] == "(":
                            break
                    if removal_path:
                        return True
                    break
            elif argument == "," and call_depth == 1:
                removal_path = True
    return False


def classify(sql: str, world: World | None = None) -> list[str]:
    """Return the reasons this migration breaks an older binary, if any.

    A table created by this same migration is invisible to an older binary,
    so dropping it, indexing it, or putting a trigger on it breaks nothing.
    A rename-away rebuild reads as breaking: the rename alters and the
    scratch drop drops a table an older binary knows.
    """
    stmts = statements(sql)
    born: set[tuple[str, str]] = set()
    scratch: set[tuple[str, str]] = set()
    reasons: list[str] = []
    for statement in stmts:
        if not statement:
            continue
        event = track_born(born, scratch, statement)
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
                # name an older binary knows stays a dependent object.
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
            if owned is not None:
                continue
            if ADD_COLUMN.match(rest):
                continue
            reasons.append(f"alters the pre-existing table {table}: {first_sql_word(rest)}")
            continue
        match = INDEX_ON.match(statement)
        if match:
            owned = resolve_born(match.group(2), born)
            if match.group(1) and owned is None:
                reasons.append(
                    f"adds a unique index to the pre-existing table {match.group(2)}"
                )
            continue
        match = TRIGGER_ON.match(statement)
        if match:
            owned = resolve_born(match.group(1), born)
            if owned is None:
                reasons.append(
                    f"adds a trigger to the pre-existing table {match.group(1)}"
                )
            continue
        if VIEW_OR_PRAGMA.match(statement) or WRITE.match(statement):
            continue
        reasons.append(f"uses an unclassified statement: {first_sql_word(statement)}")
    return reasons


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


def adds_preexisting_column(sql: str) -> bool:
    """Return whether any statement adds a column to a table the migration
    did not create.

    A FoldMaintained declaration describes exactly this shape: a column on a
    table an older binary's fold generation already writes.
    """
    stmts = statements(sql)
    born: set[tuple[str, str]] = set()
    conditional: set[tuple[str, str]] = set()
    for statement in stmts:
        track_born(born, conditional, statement, shadow_safe=True)
        match = ALTER.match(statement)
        if match:
            ref = match.group(1)
            key = resolve_born(ref, born)
            # A qualified reference owns exactly its schema. An unqualified
            # one resolves temp before main at runtime, so this migration's
            # own main table does not own it: a pre-existing temp shadow
            # takes the statement, and connection-local tables are invisible
            # here. Only a temp-born name is provably owned unqualified.
            provably_owned = key is not None and (
                len(sql_parts(ref)) >= 2 or key[0] == "temp"
            )
            if provably_owned and key not in conditional:
                continue
            if ADD_COLUMN.match(match.group(2).strip(SQL_TRIM)):
                return True
    return False


def sql_field_sound(entry: str) -> bool:
    """Return whether the SQL field is absent or exactly one raw literal."""
    run = field_run(entry, "SQL")
    return run is None or (len(run) == 1 and run[0].kind == "raw")


def evaluate(entries: list[tuple[int, str, str]]) -> tuple[list[str], list[int]]:
    """Compare every entry's declarations with what its SQL does.

    Returns the refusal messages and the versions whose SQL, or whose advance
    declaration, breaks an older binary. Migrations at or below RULE_FLOOR
    predate the FoldMaintained field and are held to the statement rules
    alone. A World replays each migration's schema effect as it runs, so
    the dependent-object rule reads names SQLite itself carries; a
    conditional migration, an unreadable entry, or one statement the
    replay cannot interpret poisons that world, and a poisoned world
    rules nothing.
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
            # cannot vouch for the schema its successors are ruled against.
            world.poisoned = True
        reasons = classify(sql, world)
        if version > JSON_REMOVAL_RULE_FLOOR and any(
            destructive_json_write(statement) for statement in statements(sql)
        ):
            reasons.append("removes JSON members in a data-write expression")
        if version > RULE_FLOOR:
            adds_column = adds_preexisting_column(sql)
            creates_table = any(CREATE_TABLE.match(statement) for statement in statements(sql))
            if adds_column and not fold:
                failures.append(
                    f"migration {version} adds a column to a pre-existing table "
                    'without a FoldMaintained declaration; declare "advance" '
                    'with Breaking: true, declare "origin", or qualify the '
                    "reference with its schema so the migration owns it"
                )
            if fold and not adds_column and not (fold == "advance" and creates_table):
                missing_shape = (
                    "adds no column to a pre-existing table and creates no table"
                    if fold == "advance"
                    else "adds no column to a pre-existing table"
                )
                failures.append(
                    f'migration {version} declares FoldMaintained "{fold}", but '
                    f"{missing_shape}; remove the declaration"
                )
            if fold and fold not in FOLD_VOCABULARY:
                failures.append(
                    f'migration {version} declares FoldMaintained "{fold}"; the '
                    'vocabulary is "advance" or "origin"'
                )
            if fold == "advance":
                reasons.append(
                    "advances fold-maintained state, which a rolling upgrade "
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
        # The world replays what ran, not what was allowed: the
        # dependent-object rule reads this state as its baseline.
        for statement in statements(sql):
            world.apply(statement)
    return failures, breaking_versions


def main() -> int:
    # newline="" keeps the file's own characters: Go raw-string semantics
    # discard carriage returns at value derivation (see migration_source.sql_literal), and
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
