#!/usr/bin/env python3
"""Validate Concord's authorizing first-usable-floor readiness manifest."""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / "docs/floor-readiness.v1.json"
SCHEMA = ROOT / "contracts/floor-readiness.schema.json"

ALLOWED_ROOT = {"schema_version", "source", "conditions", "items"}
ALLOWED_SOURCE = {"path", "section"}
ALLOWED_CONDITION = {"id", "ordinal", "title"}
ALLOWED_ITEM = {"id", "condition", "title", "requirement", "state", "evidence", "issue", "reason"}
REQUIRED_ITEM = {"id", "condition", "title", "requirement", "state"}
STATES = ("satisfied", "outstanding", "unmeasured", "out_of_scope")

CONDITION_ID = re.compile(r"^fc[0-9]{1,2}$")
ITEM_ID = re.compile(r"^(fc[0-9]{1,2})-[a-z0-9]+(?:-[a-z0-9]+)*$")

MAX_CONDITIONS = 32
MAX_ITEMS = 500
MAX_EVIDENCE = 32
MAX_FINDINGS = 200


class DuplicateKeyError(ValueError):
    pass


def reject_duplicate_pairs(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise DuplicateKeyError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def load(path: Path, findings: list[str]) -> object:
    try:
        return json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=reject_duplicate_pairs)
    except (OSError, UnicodeDecodeError, json.JSONDecodeError, DuplicateKeyError) as exc:
        findings.append(f"{path.name}: invalid JSON: {exc}")
        return None


def bounded_text(value: object, minimum: int, maximum: int) -> bool:
    return isinstance(value, str) and minimum <= len(value) <= maximum and value == value.strip()


def safe_repository_path(value: object) -> bool:
    if not isinstance(value, str) or not 3 <= len(value) <= 512 or "\x00" in value:
        return False
    if value != value.strip() or value.startswith("/"):
        return False
    candidate = Path(value)
    return candidate.as_posix() == value and ".." not in candidate.parts


def validate_source(source: object, findings: list[str], *, root: Path) -> None:
    if not isinstance(source, dict):
        findings.append("manifest.source: must be an object")
        return
    unknown = set(source) - ALLOWED_SOURCE
    if unknown:
        findings.append(f"manifest.source: unknown fields: {sorted(unknown)}")
    missing = ALLOWED_SOURCE - set(source)
    if missing:
        findings.append(f"manifest.source: missing fields: {sorted(missing)}")
        return
    if not bounded_text(source["section"], 2, 256):
        findings.append("manifest.source: section is not bounded text")
    path = source["path"]
    if not safe_repository_path(path):
        findings.append(f"manifest.source: unsafe path: {path!r}")
    elif not (root / path).is_file():
        findings.append(f"manifest.source: path does not exist: {path}")


def validate_conditions(raw: object, findings: list[str]) -> dict[str, int]:
    if not isinstance(raw, list) or not 1 <= len(raw) <= MAX_CONDITIONS:
        findings.append("manifest.conditions: must be a bounded non-empty array")
        return {}
    declared: dict[str, int] = {}
    ordinals: list[int] = []
    for number, condition in enumerate(raw):
        prefix = f"manifest.conditions[{number}]"
        if not isinstance(condition, dict):
            findings.append(f"{prefix}: must be an object")
            continue
        unknown = set(condition) - ALLOWED_CONDITION
        if unknown:
            findings.append(f"{prefix}: unknown fields: {sorted(unknown)}")
        missing = ALLOWED_CONDITION - set(condition)
        if missing:
            findings.append(f"{prefix}: missing fields: {sorted(missing)}")
            continue
        identifier = condition["id"]
        ordinal = condition["ordinal"]
        if not isinstance(identifier, str) or not CONDITION_ID.fullmatch(identifier):
            findings.append(f"{prefix}: invalid condition id")
            continue
        if identifier in declared:
            findings.append(f"{prefix}: duplicate condition id {identifier}")
            continue
        if not isinstance(ordinal, int) or isinstance(ordinal, bool) or not 1 <= ordinal <= MAX_CONDITIONS:
            findings.append(f"{prefix}: ordinal is out of range")
            continue
        if identifier != f"fc{ordinal}":
            findings.append(f"{prefix}: id {identifier} disagrees with ordinal {ordinal}")
        if not bounded_text(condition["title"], 2, 512):
            findings.append(f"{prefix}: title is not bounded text")
        declared[identifier] = ordinal
        ordinals.append(ordinal)
    if ordinals and sorted(ordinals) != list(range(1, len(ordinals) + 1)):
        findings.append("manifest.conditions: ordinals must be contiguous from 1")
    return declared


def validate_items(raw: object, declared: dict[str, int], findings: list[str], *, root: Path) -> dict[str, int]:
    tally = {state: 0 for state in STATES}
    if not isinstance(raw, list) or not 1 <= len(raw) <= MAX_ITEMS:
        findings.append("manifest.items: must be a bounded non-empty array")
        return tally
    identifiers: set[str] = set()
    covered: set[str] = set()
    for number, item in enumerate(raw):
        prefix = f"manifest.items[{number}]"
        if not isinstance(item, dict):
            findings.append(f"{prefix}: must be an object")
            continue
        unknown = set(item) - ALLOWED_ITEM
        if unknown:
            findings.append(f"{prefix}: unknown fields: {sorted(unknown)}")
        missing = REQUIRED_ITEM - set(item)
        if missing:
            findings.append(f"{prefix}: missing fields: {sorted(missing)}")
            continue

        identifier = item["id"]
        matched = ITEM_ID.fullmatch(identifier) if isinstance(identifier, str) else None
        if not matched or len(identifier) > 128:
            findings.append(f"{prefix}: invalid item id")
            continue
        if identifier in identifiers:
            findings.append(f"{prefix}: duplicate item id {identifier}")
            continue
        identifiers.add(identifier)

        condition = item["condition"]
        if not isinstance(condition, str) or condition not in declared:
            findings.append(f"{prefix}: condition {condition!r} is not declared")
            continue
        if matched.group(1) != condition:
            findings.append(f"{prefix}: id prefix disagrees with condition {condition}")
        covered.add(condition)

        if not bounded_text(item["title"], 2, 256):
            findings.append(f"{prefix}: title is not bounded text")
        if not bounded_text(item["requirement"], 16, 2048):
            findings.append(f"{prefix}: requirement is not bounded text")

        state = item["state"]
        if not isinstance(state, str) or state not in STATES:
            findings.append(f"{prefix}: state must be one of {list(STATES)}")
            continue
        tally[state] += 1

        evidence = item.get("evidence")
        issue = item.get("issue")
        reason = item.get("reason")

        if state == "satisfied":
            if issue is not None or reason is not None:
                findings.append(f"{prefix}: satisfied item must not carry issue or reason")
            if not isinstance(evidence, list) or not 1 <= len(evidence) <= MAX_EVIDENCE:
                findings.append(f"{prefix}: satisfied item requires bounded non-empty evidence")
                continue
            if len(evidence) != len(set(evidence)):
                findings.append(f"{prefix}: evidence contains duplicates")
            for reference in evidence:
                if not safe_repository_path(reference):
                    findings.append(f"{prefix}: unsafe evidence path: {reference!r}")
                elif not (root / reference).exists():
                    findings.append(f"{prefix}: evidence path does not exist: {reference}")
            continue

        if evidence is not None:
            findings.append(f"{prefix}: evidence is only valid for a satisfied item")

        if state == "outstanding":
            if reason is not None:
                findings.append(f"{prefix}: outstanding item must not carry reason")
            if not isinstance(issue, int) or isinstance(issue, bool) or not 1 <= issue <= 1000000:
                findings.append(f"{prefix}: outstanding item requires a tracking issue number")
            continue

        if issue is not None:
            findings.append(f"{prefix}: issue is only valid for an outstanding item")
        if not bounded_text(reason, 16, 2048):
            findings.append(f"{prefix}: {state} item requires a bounded reason")

    uncovered = sorted(set(declared) - covered, key=lambda value: declared[value])
    if uncovered:
        findings.append(f"manifest.items: floor conditions carry no item: {uncovered}")
    return tally


def validate(data: object, *, root: Path = ROOT) -> tuple[list[str], dict[str, int]]:
    findings: list[str] = []
    tally = {state: 0 for state in STATES}
    if not isinstance(data, dict):
        return ["manifest: top-level value must be an object"], tally
    unknown = set(data) - ALLOWED_ROOT
    if unknown:
        findings.append(f"manifest: unknown fields: {sorted(unknown)}")
    missing = ALLOWED_ROOT - set(data)
    if missing:
        findings.append(f"manifest: missing fields: {sorted(missing)}")
        return findings, tally
    if data["schema_version"] != "1.0":
        findings.append("manifest: schema_version must be 1.0")
    validate_source(data["source"], findings, root=root)
    declared = validate_conditions(data["conditions"], findings)
    tally = validate_items(data["items"], declared, findings, root=root)
    return findings, tally


def main() -> int:
    findings: list[str] = []
    if not SCHEMA.is_file():
        findings.append("contracts/floor-readiness.schema.json is missing")
    data = load(MANIFEST, findings)
    tally = {state: 0 for state in STATES}
    if data is not None:
        more, tally = validate(data)
        findings.extend(more)

    for finding in findings[:MAX_FINDINGS]:
        print(finding)
    if len(findings) > MAX_FINDINGS:
        print(f"... {len(findings) - MAX_FINDINGS} additional finding(s) omitted")
    if findings:
        print(f"floor readiness validation failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    total = sum(tally.values())
    summary = ", ".join(f"{tally[state]} {state}" for state in STATES)
    print(f"floor readiness validation passed: {total} item(s) — {summary}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
