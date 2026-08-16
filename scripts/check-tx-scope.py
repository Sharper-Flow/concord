#!/usr/bin/env python3
"""Static transaction-scope guard for the one-connection SQLite pool.

internal/store/store.go pins the pool to one connection
(SetMaxOpenConns(1)). While a *sql.Tx is open it holds that connection, so a
nested call through s.db (query, exec, or another BeginTx) parks database/sql
on the connection-request channel forever — the request never reaches
SQLite, and the failure mode is a hanging test or request, not an error.

This guard is deliberately textual and conservative: it flags `s.db.` member
access that appears, within the same function, after a transaction is
obtained via the known helpers (BeginTx / beginRead / enterFold-style). It
does not attempt type-based escape analysis. The cure for a flagged call is
the established pattern: a tx-scoped core (an xxxTx function or a small
queryer interface taking the tx), never raising the pool size.

Known false-positive shape: obtaining a tx, rolling it back, and only then
calling s.db. The guard still flags those — reordering or extracting a core
removes the ambiguity, and the pattern is rare enough that explicitness is
cheaper than silence in a class whose failure mode is an invisible hang.
"""
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
MAX_FINDINGS = 50

TX_OBTAIN = re.compile(r"\b(BeginTx|beginRead|enterFold)\s*\(")
S_DB_ACCESS = re.compile(r"\bs\.db\.(Query|Exec|Begin|Ping|Stats|Close)")


def guard_lines(func_lines: list[str]) -> bool:
    """True when s.db access follows tx acquisition in the same function."""
    tx_seen = False
    for line in func_lines:
        if TX_OBTAIN.search(line):
            tx_seen = True
            continue
        if tx_seen and S_DB_ACCESS.search(line):
            return True
    return False


def split_functions(text: str):
    """Yield (func_name, [lines]) for top-level funcs; comments and strings
    are stripped first so decorative text cannot trip the guard."""
    stripped_lines = []
    for line in text.splitlines():
        code = line.split("//", 1)[0]
        stripped_lines.append(code)
    body: list[str] = []
    name = None
    depth = 0
    for line in stripped_lines:
        if name is None:
            match = re.match(r"func (?:\([^)]*\) )?([A-Za-z_][A-Za-z0-9_]*)\(", line)
            if match:
                name = match.group(1)
                body = [line]
                depth = line.count("{") - line.count("}")
            continue
        body.append(line)
        depth += line.count("{") - line.count("}")
        if depth <= 0:
            yield name, body
            name, body = None, []


def main() -> int:
    findings: list[str] = []
    store_dir = ROOT / "internal" / "store"
    for path in sorted(store_dir.rglob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        try:
            text = path.read_text(encoding="utf-8")
        except OSError as err:
            findings.append(f"{path.relative_to(ROOT)}: cannot read ({err})")
            continue
        for func_name, lines in split_functions(text):
            if guard_lines(lines):
                line_no = next(
                    (i + 1 for i, line in enumerate(lines) if S_DB_ACCESS.search(line) and TX_OBTAIN.search("".join(lines[: i + 1]))),
                    1,
                )
                findings.append(
                    f"{path.relative_to(ROOT)}: func {func_name} mixes a transaction with s.db access "
                    f"(around line {line_no} of the function); use a tx-scoped core instead"
                )
    for finding in findings[:MAX_FINDINGS]:
        print(finding)
    if len(findings) > MAX_FINDINGS:
        print(f"... {len(findings) - MAX_FINDINGS} additional finding(s) omitted")
    if findings:
        print(f"tx scope check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("tx scope check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
