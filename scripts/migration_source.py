"""Read Go migration literals and their exact SQL source spans."""
from __future__ import annotations

import re
from typing import NamedTuple


# A migrations list element that is not a composite literal cannot supply a
# Version field to read, and the checker refuses it instead of omitting it.
UNSUPPORTED_ELEMENT = -2


class Tok(NamedTuple):
    kind: str  # ident, string, raw, rune, number, punct, newline
    text: str
    at: int  # character offset in the scanned source


class MigrationEntry(NamedTuple):
    version: int
    text: str
    at: int


class MigrationSQL(NamedTuple):
    sql: str
    span: tuple[int, int]  # SQL content's character offsets, excluding backticks


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


def sql_literal(entry: str) -> str:
    """Return the SQL field's content, or "" when the field is absent.

    The value comes from the token-recognized SQL field, so a commented or
    nested lookalike cannot supply it; a field whose value is anything but
    one raw string literal also reads as empty. The content is the Go
    raw-string value: the language discards carriage returns inside raw
    string literals, so the reader discards them too.
    """
    run = field_run(entry, "SQL")
    if run and len(run) == 1 and run[0].kind == "raw":
        return run[0].text[1:-1].replace("\r", "")
    return ""


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


def _migration_entries(source: str) -> list[MigrationEntry]:
    """Read complete migration elements, retaining their source offsets.

    A list element is one complete expression, split at top-level commas
    over every bracket type. Only a migration composite literal is a
    readable entry: the elided-type group, or the type name beside one
    group that ends the element. A call, a selector, a method call on a
    literal, or any other expression is refused whole, never mined for a
    nested literal that is not the element itself. Classification reads
    significant tokens, so a comment in the element changes nothing.

    Field recognition inside an entry is the token walk in field_run: a
    comment or a literal cannot supply or hide a field, and field order
    does not affect recognition. An entry whose Version field is missing
    or not one Go integer literal parses with version -1. A non-literal
    element parses with version -2.
    """
    tokens = go_tokens(source)
    opener = migrations_binding(tokens)
    out: list[MigrationEntry] = []
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
                at = sig[open_at].at + 1
                entry = source[at : sig[close].at]
                version_run = field_run(entry, "Version")
                version = -1
                if (
                    version_run
                    and len(version_run) == 1
                    and version_run[0].kind == "number"
                ):
                    version = go_int(version_run[0].text) or -1
                out.append(MigrationEntry(version, entry, at))
                return
        end = sig[-1].at + len(sig[-1].text)
        out.append(MigrationEntry(UNSUPPORTED_ELEMENT, source[sig[0].at : end], sig[0].at))

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


def migrations(source: str) -> list[tuple[int, str, str]]:
    """Return each migration's version, its entry text, and its SQL value."""
    return [
        (entry.version, entry.text, sql_literal(entry.text) if entry.version != UNSUPPORTED_ELEMENT else "")
        for entry in _migration_entries(source)
    ]


def migration_sql(source: str, version: int, name: str) -> MigrationSQL | None:
    """Find a named migration's SQL value and its exact span in source."""
    try:
        entries = _migration_entries(source)
    except ValueError:
        return None
    for entry in entries:
        if entry.version != version:
            continue
        name_run = field_run(entry.text, "Name")
        if not (
            name_run
            and len(name_run) == 1
            and name_run[0].kind in ("string", "raw")
            and name_run[0].text[1:-1].replace("\r", "") == name
        ):
            continue
        run = field_run(entry.text, "SQL")
        if not run or len(run) != 1 or run[0].kind != "raw":
            return None
        start = entry.at + run[0].at + 1
        end = entry.at + run[0].at + len(run[0].text) - 1
        return MigrationSQL(sql_literal(entry.text), (start, end))
    return None
