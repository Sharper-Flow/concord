#!/usr/bin/env python3
"""Enforce the CD-0002 durable-tier binding rules as executable law.

CD-0002 binds the durable git tier: markdown only, distillation rather than
state dumps, a note the size of a page. Until CD-0069 nothing checked those
rules, and the predecessor system drifted into committing megabyte JSON state
blobs into git history with nothing failing. This validator is the detective
layer of the CD-0069 enforcement stack: it binds every writer, including
manual commits, at the merge chokepoint.

The bounds are not restated here. They live once in
docs/durable-tier-budget.v1.json and this script reads them, so the budget
file, the future producer-side parse, and the AJ6 scenario extension all
enforce the same numbers.

Scope, per CD-0069:

- docs/work/ and docs/lessons/ (the compaction-note roots): markdown only,
  each note at or under max_note_bytes, no fenced JSON block over
  max_fenced_json_bytes. Zero tolerance; the tier starts empty and no ratchet
  baseline exists. An allowance is permission for one named note to exceed
  the byte bound while an issue tracks it, never permission to be non-markdown
  or to embed a state dump.
- docs/decisions/: decision records are a different artifact class (CD-0013
  is accepted law at 34.5 KB), so no byte bound applies. The markdown-only
  rule is enforced as a declared inventory: every non-markdown artifact is
  enumerated with a reason, and any new one fails until acknowledged. Drift
  becomes a reviewable manifest diff instead of silent accretion.
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
BUDGET = ROOT / "docs/durable-tier-budget.v1.json"
MAX_FINDINGS = 200
FENCE_RE = re.compile(r"^```[ \t]*([A-Za-z0-9_-]*)[ \t]*$")


def load_budget(findings: list[str]) -> dict | None:
    try:
        budget = json.loads(BUDGET.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        findings.append(f"budget is unreadable: {exc}")
        return None
    if not isinstance(budget, dict):
        findings.append("budget must be a JSON object")
        return None
    if budget.get("schema_version") != "1.0":
        findings.append("budget: schema_version must be 1.0")
        return None
    roots = budget.get("note_roots")
    if not isinstance(roots, list) or not roots or not all(
        isinstance(root, str) and root.startswith("docs/") for root in roots
    ):
        findings.append("budget: note_roots must be a non-empty array of docs/ paths")
        return None
    for key in ("max_note_bytes", "max_fenced_json_bytes"):
        value = budget.get(key)
        if not isinstance(value, int) or isinstance(value, bool) or value < 1:
            findings.append(f"budget: {key} must be a positive integer")
            return None
    allowances = budget.get("note_allowances")
    if not isinstance(allowances, list) or not all(
        isinstance(entry, dict)
        and isinstance(entry.get("path"), str)
        and entry.get("path").endswith(".md")
        and isinstance(entry.get("state"), str)
        and (entry.get("state") != "outstanding" or isinstance(entry.get("issue"), int))
        and isinstance(entry.get("reason"), str)
        for entry in allowances
    ):
        findings.append(
            "budget: note_allowances entries need path (.md), state, reason, and an issue when outstanding"
        )
        return None
    inventory = budget.get("non_markdown_inventory")
    if not isinstance(inventory, list) or not all(
        isinstance(entry, dict)
        and isinstance(entry.get("path"), str)
        and entry.get("path").startswith("docs/decisions/")
        and not entry.get("path").endswith(".md")
        and isinstance(entry.get("reason"), str)
        and len(entry["reason"]) >= 12
        for entry in inventory
    ):
        findings.append(
            "budget: non_markdown_inventory entries need a docs/decisions/ non-markdown path and a reason"
        )
        return None
    return budget


def iter_markdown(root: Path) -> list[Path]:
    if not root.is_dir():
        return []
    return sorted(path for path in root.rglob("*.md") if path.is_file())


def iter_non_markdown(root: Path) -> list[Path]:
    if not root.is_dir():
        return []
    return sorted(path for path in root.rglob("*") if path.is_file() and path.suffix != ".md")


def fenced_json_blocks(text: str) -> list[str]:
    blocks: list[str] = []
    current: list[str] | None = None
    for line in text.splitlines():
        fence = FENCE_RE.match(line)
        if fence and current is None:
            if "json" in fence.group(1).lower():
                current = []
            continue
        if line.strip() == "```" and current is not None:
            blocks.append("\n".join(current))
            current = None
            continue
        if current is not None:
            current.append(line)
    # An unterminated fence still carries everything after it. Ignoring the
    # trailing block would let a note escape the bound by omitting one line, and
    # would put this layer out of step with the producer-side parse.
    if current is not None:
        blocks.append("\n".join(current))
    return blocks


def main() -> int:
    findings: list[str] = []
    budget = load_budget(findings)
    if budget is None:
        for finding in findings[:MAX_FINDINGS]:
            print(finding)
        return 1

    max_note_bytes = budget["max_note_bytes"]
    max_fenced_json_bytes = budget["max_fenced_json_bytes"]
    allowed_sizes = {entry["path"]: entry for entry in budget["note_allowances"]}

    for root_name in budget["note_roots"]:
        root = ROOT / root_name
        rel_root = Path(root_name)
        for path in iter_non_markdown(root):
            findings.append(f"{path.relative_to(ROOT)}: durable note root {rel_root} permits markdown only")
        for path in iter_markdown(root):
            rel = path.relative_to(ROOT).as_posix()
            size = path.stat().st_size
            if size > max_note_bytes and rel not in allowed_sizes:
                findings.append(
                    f"{rel}: {size} bytes exceeds the durable note bound {max_note_bytes} (CD-0002: a page, not a state dump)"
                )
            for block in fenced_json_blocks(path.read_text(encoding="utf-8", errors="replace")):
                if len(block.encode("utf-8")) > max_fenced_json_bytes:
                    findings.append(
                        f"{rel}: fenced JSON block of {len(block.encode('utf-8'))} bytes exceeds {max_fenced_json_bytes}; distill, do not embed state"
                    )

    inventoried = {entry["path"] for entry in budget["non_markdown_inventory"]}
    for path in iter_non_markdown(ROOT / "docs/decisions"):
        rel = path.relative_to(ROOT).as_posix()
        if rel not in inventoried:
            findings.append(
                f"{rel}: non-markdown artifact in docs/decisions is not in the budget inventory; acknowledge it with a reason or remove it"
            )
    declared_missing = sorted(
        path for path in inventoried if not (ROOT / path).is_file()
    )
    for path in declared_missing:
        findings.append(f"{path}: inventoried artifact is gone; remove the inventory entry")

    for finding in findings[:MAX_FINDINGS]:
        print(finding)
    if findings:
        print(f"{len(findings)} durable-tier finding(s); see CD-0069 and docs/durable-tier-budget.v1.json")
        return 1
    print("durable tier satisfies the CD-0002 binding rules")
    return 0


if __name__ == "__main__":
    sys.exit(main())
