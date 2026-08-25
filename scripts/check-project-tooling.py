#!/usr/bin/env python3
"""Validate a repository's declared tooling manifest.

The manifest (.concord/tooling.v1.json) is the per-project record of which
quality tools, scanners, and check commands are already set up. Intent fields
are hand-authored because they have no upstream source to drift from. The
checker proves that referenced files resolve to regular files inside the
repository. A missing manifest is a finding, not a vacuous pass. The checker
performs file reads only and never executes a declared tool (CD-0074,
issue #510).
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
MANIFEST = Path(".concord/tooling.v1.json")
MAX_FINDINGS = 100
SCHEMA_VERSION = "1.0"
MAX_TOOLS = 64
PATH_MIN_LENGTH = 3
PATH_MAX_LENGTH = 512

ROOT_FIELDS = {"schema_version", "project", "tools"}
TOOL_FIELDS = {
    "id",
    "purpose",
    "invocation",
    "tier",
    "cost_hint",
    "config_path",
    "automation_path",
    "notes",
}
REQUIRED_TOOL_FIELDS = {"id", "purpose", "invocation", "tier"}
TIERS = {"fast", "standard", "slow"}
IDENTIFIER_PATTERN = r"^(?!.*[\r\n])[a-z][a-z0-9-]{1,63}$"
SAFE_PATH_PATTERN = r"^(?!/)(?!.*//)(?!\.{1,2}(?:/|$))(?!.*\/\.{1,2}(?:/|$))[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+)*$"
NON_WHITESPACE_PATTERN = r"[^ \t\r\n]"
SINGLE_LINE_PATTERN = r"^[^\u0000-\u001F\u007F]+$"
TEXT_CONSTRAINTS = {
    "purpose": (4, 256, False),
    "invocation": (1, 512, True),
    "cost_hint": (4, 128, True),
    "notes": (4, 512, False),
}
JSON_WHITESPACE = {" ", "\t", "\r", "\n"}
IDENTIFIER = re.compile(IDENTIFIER_PATTERN)
SAFE_PATH = re.compile(SAFE_PATH_PATTERN)


def reject_duplicate_keys(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def bounded_text(
    path: str,
    key: str,
    value: object,
    minimum: int,
    maximum: int,
    findings: list[str],
    *,
    single_line: bool = False,
) -> None:
    if not isinstance(value, str):
        findings.append(f"{path}: {key} must be a string")
        return
    if not minimum <= len(value) <= maximum:
        findings.append(f"{path}: {key} length must be {minimum}..{maximum} characters")
    if all(character in JSON_WHITESPACE for character in value):
        findings.append(f"{path}: {key} must contain a character outside JSON whitespace")
    if single_line and any(ord(character) < 32 or ord(character) == 127 for character in value):
        findings.append(f"{path}: {key} must be a single line without control characters")


def inside_repository(root: Path, target: Path) -> bool:
    try:
        target.relative_to(root)
    except ValueError:
        return False
    return True


def validate_file_path(root: Path, label: str, key: str, value: object, findings: list[str]) -> None:
    if not isinstance(value, str) or not PATH_MIN_LENGTH <= len(value) <= PATH_MAX_LENGTH or not SAFE_PATH.fullmatch(value):
        findings.append(f"{label}: {key} must be a safe repository-relative path of {PATH_MIN_LENGTH}..{PATH_MAX_LENGTH} characters")
        return
    try:
        resolved = (root / value).resolve(strict=True)
    except OSError:
        findings.append(f"{label}: {key} does not resolve: {value}")
        return
    if not inside_repository(root.resolve(), resolved):
        findings.append(f"{label}: {key} resolves outside the repository: {value}")
    elif not resolved.is_file():
        findings.append(f"{label}: {key} must resolve to a regular file: {value}")


def check(*, root: Path = ROOT) -> list[str]:
    findings: list[str] = []
    repository_root = root.resolve()
    manifest = root / MANIFEST
    try:
        resolved_manifest = manifest.resolve(strict=True)
    except OSError:
        findings.append(f"missing manifest: {MANIFEST} (a project declares its ready tooling there; deletion must be a visible decision, not a silent pass)")
        return findings
    if not inside_repository(repository_root, resolved_manifest):
        return [f"{MANIFEST}: manifest resolves outside the repository"]
    if not resolved_manifest.is_file():
        return [f"{MANIFEST}: manifest must resolve to a regular file"]

    try:
        raw = resolved_manifest.read_text(encoding="utf-8")
        document = json.loads(raw, object_pairs_hook=reject_duplicate_keys)
    except (OSError, json.JSONDecodeError, ValueError) as error:
        return [f"{MANIFEST}: {error}"]

    if not isinstance(document, dict):
        return [f"{MANIFEST}: top-level JSON value must be an object"]
    unknown_root = set(document) - ROOT_FIELDS
    if unknown_root:
        findings.append(f"{MANIFEST}: unknown root field(s): {', '.join(sorted(unknown_root))}")
    missing_root = ROOT_FIELDS - set(document)
    if missing_root:
        findings.append(f"{MANIFEST}: missing root field(s): {', '.join(sorted(missing_root))}")
        return findings

    if document["schema_version"] != SCHEMA_VERSION:
        findings.append(f"{MANIFEST}: schema_version must be \"{SCHEMA_VERSION}\"")
    project = document["project"]
    if not isinstance(project, str) or not IDENTIFIER.fullmatch(project):
        findings.append(f"{MANIFEST}: project must match {IDENTIFIER.pattern}")

    tools = document["tools"]
    if not isinstance(tools, list) or not tools:
        findings.append(f"{MANIFEST}: tools must be a non-empty array")
        return findings
    if len(tools) > MAX_TOOLS:
        findings.append(f"{MANIFEST}: tools must hold at most {MAX_TOOLS} entries")

    seen_ids: set[str] = set()
    for index, tool in enumerate(tools):
        path = f"{MANIFEST}: tools[{index}]"
        if not isinstance(tool, dict):
            findings.append(f"{path}: must be an object")
            continue
        identifier = tool.get("id", f"tools[{index}]")
        label = f"{MANIFEST}: {identifier}" if isinstance(tool.get("id"), str) else path
        unknown = set(tool) - TOOL_FIELDS
        if unknown:
            findings.append(f"{label}: unknown field(s): {', '.join(sorted(unknown))}")
        missing = REQUIRED_TOOL_FIELDS - set(tool)
        if missing:
            findings.append(f"{label}: missing field(s): {', '.join(sorted(missing))}")
            continue
        if not isinstance(tool["id"], str) or not IDENTIFIER.fullmatch(tool["id"]):
            findings.append(f"{label}: id must match {IDENTIFIER.pattern}")
        elif tool["id"] in seen_ids:
            findings.append(f"{label}: duplicate tool id")
        else:
            seen_ids.add(tool["id"])
        for key, (minimum, maximum, single_line) in TEXT_CONSTRAINTS.items():
            if key in tool:
                bounded_text(label, key, tool[key], minimum, maximum, findings, single_line=single_line)
        tier = tool["tier"]
        if not isinstance(tier, str) or tier not in TIERS:
            findings.append(f"{label}: tier must be one of: {', '.join(sorted(TIERS))}")
        for key in ("config_path", "automation_path"):
            if key in tool:
                validate_file_path(root, label, key, tool[key], findings)

    return findings


def main() -> int:
    findings = check()
    for finding in findings[:MAX_FINDINGS]:
        print(finding)
    if len(findings) > MAX_FINDINGS:
        print(f"... {len(findings) - MAX_FINDINGS} additional finding(s) omitted")
    if findings:
        print(f"project tooling validation failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("project tooling validation passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
