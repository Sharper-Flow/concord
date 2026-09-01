#!/usr/bin/env python3
"""Validate a maturity-readiness manifest defined by CD-0091.

A manifest records a Product's distance from one maturity rung. It reuses the
floor-readiness state model and the shared `evidence_anchors` machinery: a
satisfied item binds a typed executable anchor that resolves, an outstanding
item names a public tracking issue, and an unmeasured or out-of-scope item
carries a reason.

Unlike the floor manifest, this validator does not bind condition titles to
sentences in the source document. CD-0091 states each rung bar as prose rather
than an itemised floor, so `source` records provenance only.
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))

from evidence_anchors import check_anchor  # noqa: E402

MANIFEST_GLOB = "maturity-readiness*.json"
MANIFEST = ROOT / "docs/maturity-readiness.v1.json"
SCHEMA_VERSION = "1.0"
RUNGS = {"alpha": "a", "beta": "b", "production": "p"}
STATES = ("satisfied", "outstanding", "unmeasured", "out_of_scope")

ALLOWED_ROOT = {"schema_version", "rung", "source", "conditions", "items"}
ALLOWED_SOURCE = {"path", "section"}
ALLOWED_CONDITION = {"id", "ordinal", "title"}
ALLOWED_ITEM = {"id", "condition", "title", "requirement", "state", "evidence", "issue", "reason"}
REQUIRED_ITEM = {"id", "condition", "title", "requirement", "state"}

CONDITION_ID = re.compile(r"^[abp][0-9]{1,2}$")
ITEM_ID = re.compile(r"^([abp][0-9]{1,2})-[a-z0-9]+(?:-[a-z0-9]+)*$")
MAX_ITEMS = 500
MAX_EVIDENCE = 32


def bounded_text(value: object, minimum: int, maximum: int) -> bool:
    return isinstance(value, str) and value == value.strip() and minimum <= len(value) <= maximum


def safe_repository_path(value: object) -> bool:
    return (
        isinstance(value, str)
        and not value.startswith("/")
        and ".." not in Path(value).parts
    )


def validate_source(source: object, expect_prefix: str, findings: list[str]) -> None:
    if not isinstance(source, dict) or set(source) - ALLOWED_SOURCE:
        findings.append("manifest.source: must carry only path and section")
        return
    if {"path", "section"} - set(source):
        findings.append("manifest.source: path and section are required")
        return
    if not bounded_text(source["path"], 5, 512) or not safe_repository_path(source["path"]):
        findings.append("manifest.source.path: must be a safe repository path")
    elif not (ROOT / source["path"]).is_file():
        findings.append(f"manifest.source.path: does not resolve: {source['path']}")
    if not bounded_text(source["section"], 2, 256):
        findings.append("manifest.source.section: must be bounded text")


def validate_conditions(raw: object, prefix_letter: str, findings: list[str]) -> dict[str, int]:
    declared: dict[str, int] = {}
    if not isinstance(raw, list) or not 1 <= len(raw) <= 32:
        findings.append("manifest.conditions: must be a bounded non-empty array")
        return declared
    seen_ordinals: list[int] = []
    for number, condition in enumerate(raw):
        where = f"manifest.conditions[{number}]"
        if not isinstance(condition, dict) or set(condition) - ALLOWED_CONDITION:
            findings.append(f"{where}: must carry only id, ordinal, title")
            continue
        if ALLOWED_CONDITION - set(condition):
            findings.append(f"{where}: id, ordinal, and title are required")
            continue
        identifier = condition["id"]
        if not isinstance(identifier, str) or not CONDITION_ID.fullmatch(identifier):
            findings.append(f"{where}: invalid condition id")
            continue
        if identifier[0] != prefix_letter:
            findings.append(f"{where}: id {identifier} does not match rung prefix {prefix_letter!r}")
        if identifier in declared:
            findings.append(f"{where}: duplicate condition id {identifier}")
            continue
        ordinal = condition["ordinal"]
        if not isinstance(ordinal, int) or isinstance(ordinal, bool) or not 1 <= ordinal <= 32:
            findings.append(f"{where}: ordinal must be an integer in 1..32")
        else:
            seen_ordinals.append(ordinal)
        if not bounded_text(condition["title"], 2, 512):
            findings.append(f"{where}: title must be bounded text")
        declared[identifier] = ordinal if isinstance(ordinal, int) else 0
    if seen_ordinals and sorted(seen_ordinals) != list(range(1, len(seen_ordinals) + 1)):
        findings.append("manifest.conditions: ordinals must be contiguous from 1")
    return declared


def validate_items(raw: object, declared: dict[str, int], findings: list[str]) -> dict[str, int]:
    tally = {state: 0 for state in STATES}
    if not isinstance(raw, list) or not 1 <= len(raw) <= MAX_ITEMS:
        findings.append("manifest.items: must be a bounded non-empty array")
        return tally
    identifiers: set[str] = set()
    covered: set[str] = set()
    for number, item in enumerate(raw):
        where = f"manifest.items[{number}]"
        if not isinstance(item, dict):
            findings.append(f"{where}: must be an object")
            continue
        unknown = set(item) - ALLOWED_ITEM
        if unknown:
            findings.append(f"{where}: unknown fields: {sorted(unknown)}")
        if REQUIRED_ITEM - set(item):
            findings.append(f"{where}: missing fields: {sorted(REQUIRED_ITEM - set(item))}")
            continue
        identifier = item["id"]
        matched = ITEM_ID.fullmatch(identifier) if isinstance(identifier, str) else None
        if not matched or len(identifier) > 128:
            findings.append(f"{where}: invalid item id")
            continue
        if identifier in identifiers:
            findings.append(f"{where}: duplicate item id {identifier}")
            continue
        identifiers.add(identifier)
        condition = item["condition"]
        if not isinstance(condition, str) or condition not in declared:
            findings.append(f"{where}: condition {condition!r} is not declared")
            continue
        if matched.group(1) != condition:
            findings.append(f"{where}: id prefix disagrees with condition {condition}")
        covered.add(condition)
        if not bounded_text(item["title"], 2, 256):
            findings.append(f"{where}: title is not bounded text")
        if not bounded_text(item["requirement"], 16, 2048):
            findings.append(f"{where}: requirement is not bounded text")
        state = item["state"]
        if not isinstance(state, str) or state not in STATES:
            findings.append(f"{where}: state must be one of {list(STATES)}")
            continue
        tally[state] += 1
        evidence = item.get("evidence")
        issue = item.get("issue")
        reason = item.get("reason")
        if state == "satisfied":
            if issue is not None or reason is not None:
                findings.append(f"{where}: satisfied item must not carry issue or reason")
            if not isinstance(evidence, list) or not 1 <= len(evidence) <= MAX_EVIDENCE:
                findings.append(f"{where}: satisfied item requires bounded non-empty evidence")
            else:
                for position, anchor in enumerate(evidence):
                    if isinstance(anchor, str):
                        findings.append(f"{where}: evidence must be typed anchors, not paths")
                        continue
                    check_anchor(anchor, f"{where} anchor {position}", findings)
            continue
        if evidence is not None:
            findings.append(f"{where}: evidence is only valid for a satisfied item")
        if state == "outstanding":
            if reason is not None:
                findings.append(f"{where}: outstanding item must not carry reason")
            if not isinstance(issue, int) or isinstance(issue, bool) or not 1 <= issue <= 1000000:
                findings.append(f"{where}: outstanding item requires a tracking issue number")
            continue
        if issue is not None:
            findings.append(f"{where}: issue is only valid for an outstanding item")
        if not bounded_text(reason, 16, 2048):
            findings.append(f"{where}: {state} item requires a bounded reason")
    uncovered = sorted(set(declared) - covered, key=lambda value: declared[value])
    if uncovered:
        findings.append(f"manifest.items: rung conditions carry no item: {uncovered}")
    return tally


def validate(data: object) -> tuple[list[str], dict[str, int]]:
    findings: list[str] = []
    tally = {state: 0 for state in STATES}
    if not isinstance(data, dict):
        return ["manifest: top-level value must be an object"], tally
    if set(data) - ALLOWED_ROOT:
        findings.append(f"manifest: unknown fields: {sorted(set(data) - ALLOWED_ROOT)}")
    if ALLOWED_ROOT - set(data):
        findings.append(f"manifest: missing fields: {sorted(ALLOWED_ROOT - set(data))}")
        return findings, tally
    if data["schema_version"] != SCHEMA_VERSION:
        findings.append(f"manifest: schema_version must be {SCHEMA_VERSION}")
    rung = data["rung"]
    if rung not in RUNGS:
        findings.append(f"manifest: rung must be one of {sorted(RUNGS)}")
        prefix_letter = ""
    else:
        prefix_letter = RUNGS[rung]
    validate_source(data["source"], prefix_letter, findings)
    declared = validate_conditions(data["conditions"], prefix_letter, findings)
    tally = validate_items(data["items"], declared, findings)
    return findings, tally


def main() -> int:
    # One file measures one rung (the schema says so); every file under the
    # glob is validated, and a rung may appear at most once across them.
    paths = sorted((ROOT / "docs").glob(MANIFEST_GLOB))
    if not paths:
        print("maturity readiness manifest is missing", file=sys.stderr)
        return 1
    seen_rungs: set[str] = set()
    failed = False
    for path in paths:
        try:
            data = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            print(f"{path.name}: unreadable: {exc}", file=sys.stderr)
            failed = True
            continue
        rung = data.get("rung", "unknown") if isinstance(data, dict) else "unknown"
        if rung in seen_rungs:
            print(f"{path.name}: rung {rung} is already measured by another manifest", file=sys.stderr)
            failed = True
            continue
        seen_rungs.add(rung)
        findings, tally = validate(data)
        if findings:
            failed = True
            for finding in findings:
                print(f"{path.name}: {finding}", file=sys.stderr)
            continue
        total = sum(tally.values())
        print(
            f"maturity readiness ({rung}, {path.name}) check passed: {total} item(s) — "
            + ", ".join(f"{count} {state}" for state, count in tally.items())
        )
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
