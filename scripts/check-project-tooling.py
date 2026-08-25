#!/usr/bin/env python3
"""Validate a repository's declared tooling manifest.

The manifest (.concord/tooling.v1.json) is the per-project record of which
quality tools, scanners, and check commands are already set up. Intent fields
(purpose, invocation, tier, notes) are hand-authored because they have no
upstream source to drift from. Resolution claims are proved here so a stale
entry fails a check instead of rotting in prose: config_path must resolve on
disk, ci_reference must appear in a workflow file, and in_ci false forbids a
ci_reference. A missing manifest is a finding, not a vacuous pass. The checker
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
SCHEMA = Path("contracts/project-tooling.v1.schema.json")
WORKFLOWS = Path(".github/workflows")
MAX_FINDINGS = 100

ROOT_FIELDS = {"schema_version", "project", "tools"}
TOOL_FIELDS = {"id", "purpose", "invocation", "tier", "config_path", "in_ci", "ci_reference", "notes"}
REQUIRED_TOOL_FIELDS = {"id", "purpose", "invocation", "tier", "in_ci"}
TIERS = {"fast", "standard", "slow"}
IDENTIFIER = re.compile(r"^[a-z][a-z0-9-]{1,63}$")
SAFE_PATH = re.compile(r"^[A-Za-z0-9.][A-Za-z0-9._/-]{2,511}$")


def reject_duplicate_keys(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def bounded_text(path: str, key: str, value: object, minimum: int, maximum: int, findings: list[str]) -> None:
    if not isinstance(value, str):
        findings.append(f"{path}: {key} must be a string")
        return
    if not minimum <= len(value.strip()) <= maximum:
        findings.append(f"{path}: {key} length must be {minimum}..{maximum} characters")
    if "\x00" in value or ("\n" in value and key == "invocation"):
        findings.append(f"{path}: {key} must be a single line without control characters")


def workflow_text(root: Path) -> str:
    parts: list[str] = []
    directory = root / WORKFLOWS
    if not directory.is_dir():
        return ""
    for workflow in sorted(directory.glob("*.yml")):
        try:
            parts.append(workflow.read_text(encoding="utf-8"))
        except (OSError, UnicodeDecodeError):
            continue
    return "\n".join(parts)


def check(*, root: Path = ROOT) -> list[str]:
    findings: list[str] = []
    manifest = root / MANIFEST
    if not manifest.is_file():
        findings.append(f"missing manifest: {MANIFEST} (a project declares its ready tooling there; deletion must be a visible decision, not a silent pass)")
        return findings
    if not (root / SCHEMA).is_file():
        findings.append(f"missing schema: {SCHEMA}")

    try:
        raw = manifest.read_text(encoding="utf-8")
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

    if document["schema_version"] != "1.0":
        findings.append(f"{MANIFEST}: schema_version must be \"1.0\"")
    project = document["project"]
    if not isinstance(project, str) or not IDENTIFIER.match(project):
        findings.append(f"{MANIFEST}: project must match {IDENTIFIER.pattern}")

    tools = document["tools"]
    if not isinstance(tools, list) or not tools:
        findings.append(f"{MANIFEST}: tools must be a non-empty array")
        return findings
    if len(tools) > 64:
        findings.append(f"{MANIFEST}: tools must hold at most 64 entries")

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
        if not isinstance(tool["id"], str) or not IDENTIFIER.match(tool["id"]):
            findings.append(f"{label}: id must match {IDENTIFIER.pattern}")
        elif tool["id"] in seen_ids:
            findings.append(f"{label}: duplicate tool id")
        else:
            seen_ids.add(tool["id"])
        bounded_text(label, "purpose", tool["purpose"], 4, 256, findings)
        bounded_text(label, "invocation", tool["invocation"], 1, 512, findings)
        if "notes" in tool:
            bounded_text(label, "notes", tool["notes"], 4, 512, findings)
        if tool["tier"] not in TIERS:
            findings.append(f"{label}: tier must be one of: {', '.join(sorted(TIERS))}")
        if not isinstance(tool["in_ci"], bool):
            findings.append(f"{label}: in_ci must be a boolean")
            continue

        config_path = tool.get("config_path")
        if config_path is not None:
            if not isinstance(config_path, str) or not SAFE_PATH.match(config_path) or config_path.startswith("/") or ".." in config_path.split("/"):
                findings.append(f"{label}: config_path must be a repository-relative path without traversal")
            elif not (root / config_path).is_file():
                findings.append(f"{label}: config_path does not resolve: {config_path}")

        if tool["in_ci"]:
            reference = tool.get("ci_reference")
            if reference is None:
                findings.append(f"{label}: ci_reference is required when in_ci is true")
            elif not isinstance(reference, str) or not (2 <= len(reference.strip()) <= 256):
                findings.append(f"{label}: ci_reference length must be 2..256 characters")
            elif reference not in workflow_text(root):
                findings.append(f"{label}: ci_reference not found in any workflow under {WORKFLOWS}: {reference}")
        elif "ci_reference" in tool:
            findings.append(f"{label}: ci_reference is forbidden when in_ci is false")

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
