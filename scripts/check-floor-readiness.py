#!/usr/bin/env python3
"""Validate Concord's authorizing first-usable-floor readiness manifest.

A satisfied item must cite at least one anchor from the shared `evidence_anchors`
machinery — `go_test`, `scenario`, `validator`, or `generated`. A repository
path is not an anchor: paths are exactly what the old validator accepted, and
accepting them here would restate the defect in new syntax (issue #187). A
satisfied claim is only as load-bearing as the executable check that backs
it, so the validator proves the anchor resolves and, for the executable kinds,
that a required workflow invokes it.

Everything else — condition correspondence, source copying, state exclusivity,
ordinals, and path safety for `issue`/`reason`-free items — is unchanged from
the 1.0 form; the schema bump to 2.0 only narrows what an evidence reference
may be.
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))

from evidence_anchors import check_anchor  # noqa: E402

MANIFEST = ROOT / "docs/floor-readiness.v1.json"
SCHEMA = ROOT / "contracts/floor-readiness.schema.json"
SCHEMA_VERSION = "2.0"

ALLOWED_ROOT = {"schema_version", "source", "conditions", "items"}
ALLOWED_SOURCE = {"path", "section"}
ALLOWED_CONDITION = {"id", "ordinal", "title", "source"}
ALLOWED_ITEM = {"id", "condition", "title", "requirement", "state", "evidence", "issue", "reason"}
REQUIRED_ITEM = {"id", "condition", "title", "requirement", "state"}
STATES = ("satisfied", "outstanding", "unmeasured", "out_of_scope")

CONDITION_ID = re.compile(r"^fc[0-9]{1,2}$")
ITEM_ID = re.compile(r"^(fc[0-9]{1,2})-[a-z0-9]+(?:-[a-z0-9]+)*$")

HEADING_RE = re.compile(r"^\s{0,3}(#{1,6})\s+(.+?)\s*#*\s*$")
NUMBERED_ITEM_RE = re.compile(r"^\s{0,6}(\d+)\.\s+(.*\S)")
BULLET_ITEM_RE = re.compile(r"^\s{0,6}[-*+]\s+(.*\S)")

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


def anchor_dedup_key(anchor: object) -> str | None:
    """Stable string key for dedup of anchor objects; None when the entry
    is not a comparable anchor (a string here is rejected downstream, so it
    is its own kind and dedup is moot)."""
    if isinstance(anchor, dict):
        return json.dumps(anchor, sort_keys=True, separators=(",", ":"))
    return None


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


def find_markdown_section(text: str, section: str) -> str | None:
    """Return the body of the markdown `## Section` heading matching `section`,
    or None if no such heading exists. The body is taken up to (but not
    including) the next heading of the same or higher level. Matching is by
    trailing heading text after the prefix, so "3. Replacement-ready floor"
    matches a request for "Replacement-ready floor"."""
    lines = text.splitlines()
    target: tuple[int, str] | None = None
    for line in lines:
        match = HEADING_RE.match(line)
        if not match:
            continue
        level = len(match.group(1))
        heading = match.group(2).strip()
        # Strip an optional leading "<number>. " prefix used by numbered
        # sections like "## 3. Replacement-ready floor".
        heading_body = re.sub(r"^[\d.]+\s+", "", heading)
        if heading_body == section.strip():
            target = (level, line)
            break
    if target is None:
        return None
    level, header_line = target
    body_lines: list[str] = []
    seen_header = False
    for line in lines:
        if line == header_line:
            seen_header = True
            continue
        if not seen_header:
            continue
        match = HEADING_RE.match(line)
        if match and len(match.group(1)) <= level:
            break
        body_lines.append(line)
    return "\n".join(body_lines)


def extract_section_items(section_body: str) -> list[str]:
    """Extract the ordered list of items from a markdown section body.

    Uses a numbered list (`1. ...`) when present and any subsequent numbered
    lines are still in the same contiguous run; otherwise falls back to a
    bulleted list (`- ...`). Each item's text is the continuation lines
    following the marker, joined with spaces and leading whitespace stripped."""
    lines = section_body.splitlines()
    items: list[str] = []
    current: list[str] | None = None
    marker_re: re.Pattern[str] | None = None
    for line in lines:
        numbered = NUMBERED_ITEM_RE.match(line)
        bulleted = BULLET_ITEM_RE.match(line)
        if numbered:
            if current is not None and marker_re is NUMBERED_ITEM_RE:
                items.append(" ".join(current).strip())
            elif current is not None:
                items.append(" ".join(current).strip())
            current = [numbered.group(2).strip()]
            marker_re = NUMBERED_ITEM_RE
            continue
        if bulleted and marker_re is BULLET_ITEM_RE:
            items.append(" ".join(current).strip())
            current = [bulleted.group(1).strip()]
            marker_re = BULLET_ITEM_RE
            continue
        if bulleted and marker_re is None:
            current = [bulleted.group(1).strip()]
            marker_re = BULLET_ITEM_RE
            continue
        if current is not None:
            stripped = line.strip()
            if stripped:
                current.append(stripped)
    if current is not None:
        items.append(" ".join(current).strip())
    return items


def collapse_whitespace(text: str) -> str:
    return re.sub(r"\s+", " ", text).strip()


def first_sentence(text: str) -> str:
    """Return the first sentence of `text`. A sentence ends at the first
    period followed by whitespace or end-of-string, or at the first question
    or exclamation mark in the same rule. The marker is preserved when it
    is followed by end-of-string so wrapped items without subsequent prose
    keep their terminator."""
    match = re.search(r"([.!?])(?=\s|$)", text)
    if not match:
        return text.strip()
    return text[: match.end()].strip()


def validate_condition_correspondence(
    conditions: list[dict[str, object]],
    effective_sources: list[tuple[str, dict[str, str]]],
    root: Path,
    findings: list[str],
) -> None:
    """Group conditions by their effective source (path, section) and verify
    that each group's titles correspond one-to-one and in order with the
    numbered/bulleted items in that source. A non-resolvable section or a
    zero-item section is a failure, not a vacuous pass."""
    text_cache: dict[str, str] = {}
    by_source: dict[tuple[str, str], list[dict[str, object]]] = {}
    for condition, (identifier, source) in zip(conditions, effective_sources):
        key = (source["path"], source["section"])
        by_source.setdefault(key, []).append(condition)
    for source_key, group in by_source.items():
        path, section = source_key
        if path not in text_cache:
            target = root / path
            try:
                text_cache[path] = target.read_text(encoding="utf-8")
            except OSError as exc:
                findings.append(
                    f"manifest.conditions: could not read source {path}: {exc}"
                )
                continue
        text = text_cache[path]
        body = find_markdown_section(text, section)
        if body is None:
            findings.append(
                f"manifest.conditions: source {path} section {section!r} not found"
            )
            continue
        items = extract_section_items(body)
        if not items:
            findings.append(
                f"manifest.conditions: source {path} section {section!r} has no items"
            )
            continue
        if len(group) != len(items):
            findings.append(
                f"manifest.conditions: source {path} section {section!r} has {len(items)} item(s), "
                f"manifest declares {len(group)} condition(s) from it"
            )
            continue
        for number, (condition, item_text) in enumerate(zip(group, items), start=1):
            collapsed = collapse_whitespace(item_text)
            first = first_sentence(collapsed)
            if condition["title"] != first:
                identifier = condition["id"]
                findings.append(
                    f"manifest.conditions[{identifier}]: title does not equal first sentence of "
                    f"source item {number} from {path} section {section!r}: "
                    f"got {condition['title']!r}, want {first!r}"
                )


def validate_conditions(
    raw: object,
    findings: list[str],
    *,
    default_source: dict[str, str] | None = None,
) -> tuple[dict[str, int], list[tuple[str, dict[str, str]]]]:
    """Validate the conditions array. Returns the declared id→ordinal map and
    a list of (id, effective_source) pairs suitable for the correspondence
    check. The effective source is the condition's own `source` if present
    and well-formed, otherwise the manifest-level `source`."""
    if not isinstance(raw, list) or not 1 <= len(raw) <= MAX_CONDITIONS:
        findings.append("manifest.conditions: must be a bounded non-empty array")
        return {}, []
    declared: dict[str, int] = {}
    ordinals: list[int] = []
    effective_sources: list[tuple[str, dict[str, str]]] = []
    for number, condition in enumerate(raw):
        prefix = f"manifest.conditions[{number}]"
        if not isinstance(condition, dict):
            findings.append(f"{prefix}: must be an object")
            continue
        unknown = set(condition) - ALLOWED_CONDITION
        if unknown:
            findings.append(f"{prefix}: unknown fields: {sorted(unknown)}")
        missing = {"id", "ordinal", "title"} - set(condition)
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
        override: dict[str, str] | None = None
        if "source" in condition:
            source = condition["source"]
            if not isinstance(source, dict):
                findings.append(f"{prefix}: source must be an object")
            else:
                src_unknown = set(source) - ALLOWED_SOURCE
                if src_unknown:
                    findings.append(f"{prefix}.source: unknown fields: {sorted(src_unknown)}")
                src_missing = ALLOWED_SOURCE - set(source)
                if src_missing:
                    findings.append(f"{prefix}.source: missing fields: {sorted(src_missing)}")
                elif isinstance(source.get("path"), str) and isinstance(source.get("section"), str):
                    if not bounded_text(source["section"], 2, 256):
                        findings.append(f"{prefix}.source: section is not bounded text")
                    elif not safe_repository_path(source["path"]):
                        findings.append(f"{prefix}.source: unsafe path: {source['path']!r}")
                    else:
                        override = {"path": source["path"], "section": source["section"]}
        declared[identifier] = ordinal
        ordinals.append(ordinal)
        if override is not None:
            effective_sources.append((identifier, override))
        elif default_source is not None:
            effective_sources.append((identifier, dict(default_source)))
    if ordinals and sorted(ordinals) != list(range(1, len(ordinals) + 1)):
        findings.append("manifest.conditions: ordinals must be contiguous from 1")
    return declared, effective_sources


def validate_evidence(evidence: object, prefix: str, findings: list[str]) -> bool:
    """Validate the evidence array for a satisfied item.

    Returns False if the array itself is malformed (length, type) so the
    caller can skip per-entry checks. Each entry must be an anchor object —
    a string is a finding per issue #187, "it cannot become satisfied from a
    cited path alone". Anchors are resolved by the shared `check_anchor`
    so the proof machinery is the same as the law-coverage plane.
    """
    if not isinstance(evidence, list) or not 1 <= len(evidence) <= MAX_EVIDENCE:
        findings.append(f"{prefix}: satisfied item requires bounded non-empty evidence")
        return False
    keys = [anchor_dedup_key(item) for item in evidence]
    if any(key is None for key in keys):
        # A non-anchor (string) entry is rejected per element below; dedup is
        # not meaningful across a mixed array and would mask the rejection.
        pass
    elif len(set(keys)) != len(keys):
        findings.append(f"{prefix}: evidence contains duplicates")
    for position, anchor in enumerate(evidence):
        if isinstance(anchor, str):
            findings.append(f"{prefix}: evidence must be typed anchors, not paths")
            continue
        check_anchor(anchor, f"{prefix} anchor {position}", findings)
    return True


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
            validate_evidence(evidence, prefix, findings)
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
    if data["schema_version"] != SCHEMA_VERSION:
        findings.append(f"manifest: schema_version must be {SCHEMA_VERSION}")
    validate_source(data["source"], findings, root=root)
    default_source: dict[str, str] | None = None
    if isinstance(data["source"], dict):
        source = data["source"]
        if (
            isinstance(source.get("path"), str)
            and isinstance(source.get("section"), str)
            and safe_repository_path(source["path"])
            and (root / source["path"]).is_file()
        ):
            default_source = {"path": source["path"], "section": source["section"]}
    declared, effective_sources = validate_conditions(
        data["conditions"], findings, default_source=default_source
    )
    if effective_sources and not findings_in_findings(findings, "manifest.conditions"):
        validate_condition_correspondence(
            data["conditions"], effective_sources, root, findings
        )
    tally = validate_items(data["items"], declared, findings, root=root)
    return findings, tally


def findings_in_findings(findings: list[str], fragment: str) -> bool:
    return any(fragment in finding for finding in findings)


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