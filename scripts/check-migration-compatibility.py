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
"""

from __future__ import annotations

import re
import sys
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

SQL_REF = r"(?:\"(?:[^\"]|\"\")+\"|\[[^\]]+\]|`[^`]+`|'(?:[^']|'')+'|[A-Za-z_][\w$]*)"
SQL_QUAL = rf"(?:{SQL_REF}(?:\s*\.\s*{SQL_REF})*)"
CREATE_TABLE = re.compile(
    rf"^CREATE\s+(?:VIRTUAL\s+|TEMP\s+|TEMPORARY\s+)*TABLE(?:\s+IF\s+NOT\s+EXISTS)?"
    rf"\s+({SQL_QUAL})",
    re.IGNORECASE,
)
DROP_TABLE = re.compile(
    rf"^DROP\s+TABLE(?:\s+IF\s+EXISTS)?\s+({SQL_QUAL})", re.IGNORECASE
)


def sql_parts(ref: str) -> list[str]:
    """Split a table reference on dots, keeping quoted parts atomic.

    "a.b.c" is one name, not a schema and a table: the dot inside the
    quotes belongs to the identifier.
    """
    parts: list[str] = []
    current: list[str] = []
    i, n = 0, len(ref)
    while i < n:
        ch = ref[i]
        if ch in ('"', "`", "["):
            close = "]" if ch == "[" else ch
            j = ref.find(close, i + 1)
            j = n if j < 0 else j + 1
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
    return [p.strip() for p in parts if p.strip()]


def sql_table_key(ref: str, temp: bool = False) -> tuple[str, str]:
    """Return the (schema, table) identity of one SQL table reference.

    An explicit qualifier names its schema; an unqualified reference in a
    CREATE carries temp when the statement says TEMP or TEMPORARY and main
    otherwise. Quoting comes off the name, so "t", [t], `t`, and t are one
    table, while main.t and temp.t stay distinct identities.
    """
    def unquote(part: str) -> str:
        if len(part) >= 2 and part[0] == part[-1] and part[0] in ('"', "`"):
            return part[1:-1]
        if len(part) >= 2 and part[0] == "[" and part[-1] == "]":
            return part[1:-1]
        return part

    parts = sql_parts(ref)
    if len(parts) == 1:
        schema = "temp" if temp else "main"
        name = parts[0]
    else:
        schema, name = parts[0], parts[-1]
    return (unquote(schema).lower(), unquote(name).lower())


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
# One SQL table reference: quoted, bracketed, backticked, or bare, alone or
# schema-qualified. Comparisons normalize through sql_name.
DROP_INDEX = re.compile(
    rf"^DROP\s+(?:INDEX|TRIGGER|VIEW)(?:\s+IF\s+EXISTS)?\s+{SQL_QUAL}",
    re.IGNORECASE,
)
ALTER = re.compile(
    rf"^ALTER\s+TABLE\s+({SQL_QUAL})\s+([\s\S]*)$", re.IGNORECASE
)
ADD_COLUMN = re.compile(r"^ADD\s+(?:COLUMN\s+)?\S", re.IGNORECASE)
INDEX_ON = re.compile(
    rf"^CREATE\s+(UNIQUE\s+)?INDEX(?:\s+IF\s+NOT\s+EXISTS)?\s+\S+\s+ON\s+({SQL_QUAL})",
    re.IGNORECASE,
)
TRIGGER_ON = re.compile(
    rf"^CREATE\s+TRIGGER(?:\s+IF\s+NOT\s+EXISTS)?\s+\S+\s+(?:BEFORE|AFTER|INSTEAD)"
    rf"[\s\S]*?\bON\s+({SQL_QUAL})",
    re.IGNORECASE,
)
VIEW_OR_PRAGMA = re.compile(r"^(CREATE\s+VIEW|PRAGMA|ANALYZE|REINDEX)\b", re.IGNORECASE)
WRITE = re.compile(r"^(INSERT|UPDATE|DELETE|SELECT|WITH)\b", re.IGNORECASE)


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
    word_start = 0
    word: list[str] = []
    depth = 0
    awaiting_body = False
    stmt_open = False
    i, n = 0, len(sql)

    def trigger_head() -> bool:
        # The statement's second keyword decides: CREATE [TEMP|TEMPORARY]
        # TRIGGER opens a body; CREATE TABLE names a table, whatever the
        # table or a column is called.
        words = head_words
        at = 1
        if at < len(words) and words[at] in ("TEMP", "TEMPORARY"):
            at += 1
        return at < len(words) and words[at] == "TRIGGER"

    def end_closes() -> bool:
        j = i
        while j < n:
            if sql[j] in " \t\r\n":
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
        if ch.isalnum() or ch in "_$":
            if not word:
                word_start = i
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
            close = "]" if ch == "[" else ch
            j = sql.find(close, i + 1)
            j = n - 1 if j < 0 else j
            current.append(sql[i : j + 1])
            i = j + 1
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
                statement = "".join(current).strip()
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
    tail = "".join(current).strip()
    if tail:
        out.append(tail)
    return out


RENAMES = re.compile(rf"^RENAME\s+(?:TO|AS)\s+({SQL_QUAL})", re.IGNORECASE)
CONDITIONAL_CREATE = re.compile(r"\bIF\s+NOT\s+EXISTS\b", re.IGNORECASE)


def track_born(
    born: set[tuple[str, str]],
    conditional: set[tuple[str, str]],
    statement: str,
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
        rename = RENAMES.match(match.group(2).strip())
        if rename:
            retired = resolve_born(match.group(1), born)
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


def classify(sql: str) -> list[str]:
    """Return the reasons this migration breaks an older binary, if any.

    A table created by this same migration is invisible to an older binary, so
    dropping it, indexing it, or putting a trigger on it breaks nothing.
    """
    born: set[tuple[str, str]] = set()
    scratch: set[tuple[str, str]] = set()
    reasons: list[str] = []
    for statement in statements(sql):
        if not statement:
            continue
        event = track_born(born, scratch, statement)
        if event and event[0] == "preexisting_drop":
            reasons.append(f"drops the pre-existing table {event[1]}")
            continue
        if event is not None or CREATE_TABLE.match(statement) or DROP_TABLE.match(statement):
            continue
        match = DROP_INDEX.match(statement)
        if match:
            continue
        match = ALTER.match(statement)
        if match:
            table, rest = match.group(1), match.group(2).strip()
            if resolves_to_born(table, born):
                continue
            if ADD_COLUMN.match(rest):
                continue
            reasons.append(f"alters the pre-existing table {table}: {rest.split()[0].upper()}")
            continue
        match = INDEX_ON.match(statement)
        if match:
            if match.group(1) and not resolves_to_born(match.group(2), born):
                reasons.append(
                    f"adds a unique index to the pre-existing table {match.group(2)}"
                )
            continue
        match = TRIGGER_ON.match(statement)
        if match:
            if not resolves_to_born(match.group(1), born):
                reasons.append(
                    f"adds a trigger to the pre-existing table {match.group(1)}"
                )
            continue
        if VIEW_OR_PRAGMA.match(statement) or WRITE.match(statement):
            continue
        reasons.append(f"uses an unclassified statement: {statement.split()[0].upper()}")
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


def adds_preexisting_column(sql: str) -> bool:
    """Return whether any statement adds a column to a table the migration
    did not create.

    A FoldMaintained declaration describes exactly this shape: a column on a
    table an older binary's fold generation already writes.
    """
    born: set[tuple[str, str]] = set()
    conditional: set[tuple[str, str]] = set()
    for statement in statements(sql):
        track_born(born, conditional, statement)
        match = ALTER.match(statement)
        if match:
            key = resolve_born(match.group(1), born)
            if key is not None and key not in conditional:
                continue
            if ADD_COLUMN.match(match.group(2).strip()):
                return True
    return False


def sql_literal(entry: str) -> str:
    """Return the SQL field's content, or "" when the field is absent.

    The value comes from the token-recognized SQL field, so a commented or
    nested lookalike cannot supply it; a field whose value is anything but
    one raw string literal also reads as empty and sql_field_sound refuses
    the entry separately.
    """
    run = field_run(entry, "SQL")
    if run and len(run) == 1 and run[0].kind == "raw":
        return run[0].text[1:-1]
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


def migrations(source: str) -> list[tuple[int, str, str]]:
    """Return each migration's version, its entry text, and its SQL.

    Entries are brace groups of the migrations literal, found by walking
    tokens: a comment or a literal cannot open or close a group, and field
    order inside an entry does not affect recognition. The literal itself
    is located by its token sequence (var migrations = []migration {), so
    a decoy binding inside a comment or a literal never redirects the
    walk. The SQL is the token-recognized SQL field's raw string content,
    and declaration searches tokenize the same entry, where a raw literal
    is one token, so SQL content can never read as a Go field in either
    direction.

    An entry whose Version field is missing or not one Go integer literal
    parses with version -1; evaluate refuses it by name.
    """
    tokens = go_tokens(source)
    opener = migrations_binding(tokens)
    out: list[tuple[int, str, str]] = []
    depth = 1
    entry_start: int | None = None
    for tok in tokens[opener + 1 :]:
        if tok.kind == "punct" and tok.text == "{":
            if depth == 1:
                entry_start = tok.at + 1
            depth += 1
            continue
        if tok.kind == "punct" and tok.text == "}":
            depth -= 1
            if depth == 1 and entry_start is not None:
                entry = source[entry_start:tok.at]
                version_run = field_run(entry, "Version")
                version = -1
                if version_run and len(version_run) == 1 and version_run[0].kind == "number":
                    version = go_int(version_run[0].text) or -1
                out.append((version, entry, sql_literal(entry)))
                entry_start = None
            if depth == 0:
                break
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
        if version < 0:
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
            failures.append(
                f"migration {version} carries an SQL field that is not one raw "
                "string literal; the checker refuses to read it"
            )
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
