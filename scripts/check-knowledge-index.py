#!/usr/bin/env python3
"""Validate and mechanically hash Concord's authored durable-knowledge registry."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sys
import tempfile
from datetime import datetime
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / "docs/concord-knowledge-index.v1.json"
MAX_MANIFEST_PATH = 512  # JSON Schema maxLength and Python Unicode scalar count.
ALLOWED_ROOT = {"schema_version", "supported_kinds", "indexed_kinds", "records"}
ALLOWED_RECORD = {"id", "kind", "path", "status", "date", "title", "summary", "tags", "scopes", "successor", "sha256"}
ALLOWED_SCOPES = {"mode", "product_ids", "project_ids", "component_ids", "tag_ids"}
KINDS = {"work_note", "lesson", "decision", "spec", "research"}
RECORD_KINDS = {"lesson", "decision", "spec"}


class DuplicateKeyError(ValueError):
    pass


def reject_duplicate_pairs(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise DuplicateKeyError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def fail(findings: list[str], message: str) -> None:
    findings.append(message)


def load(path: Path, findings: list[str]) -> object:
    try:
        return json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=reject_duplicate_pairs)
    except (OSError, UnicodeDecodeError, json.JSONDecodeError, DuplicateKeyError) as exc:
        fail(findings, f"{path.relative_to(ROOT)}: invalid JSON: {exc}")
        return None


def unique_string_list(value: object, maximum: int) -> bool:
    if not isinstance(value, list) or len(value) > maximum or not all(isinstance(item, str) for item in value):
        return False
    return len(value) == len(set(value))


def valid_id(value: object) -> bool:
    return isinstance(value, str) and 0 < len(value) <= 256 and value == value.strip()


def validate(data: object, *, check_hashes: bool = True) -> list[str]:
    findings: list[str] = []
    if not isinstance(data, dict):
        return ["manifest: top-level value must be an object"]
    unknown = set(data) - ALLOWED_ROOT
    if unknown:
        fail(findings, f"manifest: unknown fields: {sorted(unknown)}")
    if data.get("schema_version") != "1.0":
        fail(findings, "manifest: schema_version must be 1.0")
    supported = data.get("supported_kinds")
    indexed = data.get("indexed_kinds")
    records = data.get("records")
    if not unique_string_list(supported, 5) or not all(kind in KINDS for kind in supported):
        fail(findings, "manifest: supported_kinds is not a unique closed bounded array")
        supported = []
    if not unique_string_list(indexed, 4) or not all(kind in {"work_note", "lesson", "decision", "spec"} for kind in indexed):
        fail(findings, "manifest: indexed_kinds is not a unique closed bounded array")
        indexed = []
    if not set(indexed).issubset(supported):
        fail(findings, "manifest: indexed_kinds contains an unsupported kind")
    if not isinstance(records, list) or len(records) > 1000:
        fail(findings, "manifest: records must be a bounded array")
        records = []

    ids: set[str] = set()
    paths: set[str] = set()
    for number, record in enumerate(records):
        prefix = f"manifest.records[{number}]"
        if not isinstance(record, dict):
            fail(findings, f"{prefix}: record must be an object")
            continue
        unknown = set(record) - ALLOWED_RECORD
        if unknown:
            fail(findings, f"{prefix}: unknown fields: {sorted(unknown)}")
        required = ALLOWED_RECORD - {"successor"}
        missing = required - set(record)
        if missing:
            fail(findings, f"{prefix}: missing fields: {sorted(missing)}")
            continue

        identifier = record["id"]
        if not valid_id(identifier):
            fail(findings, f"{prefix}: invalid ID")
        elif identifier in ids:
            fail(findings, f"{prefix}: duplicate ID {identifier}")
        else:
            ids.add(identifier)

        kind = record["kind"]
        if not isinstance(kind, str) or kind not in RECORD_KINDS or kind not in indexed:
            fail(findings, f"{prefix}: record kind is not indexed: {kind}")
        path = record["path"]
        if (
            not isinstance(path, str)
            or len(path) > MAX_MANIFEST_PATH
            or (isinstance(path, str) and "\x00" in path)
            or path in {"docs/concord-knowledge-index.v1.json"}
            or not path.startswith("docs/")
            or not path.endswith(".md")
            or path.startswith("docs/work/")
            or path.startswith("docs/research/")
            or "generated" in path.lower()
            or path in {"docs/product-coordination-view.md", "docs/terminal-launcher-contract.md"}
        ):
            fail(findings, f"{prefix}: forbidden or unsafe path: {path}")
            continue
        if Path(path).as_posix() != path or ".." in Path(path).parts:
            fail(findings, f"{prefix}: forbidden or unsafe path: {path}")
            continue
        if path in paths:
            fail(findings, f"{prefix}: duplicate path {path}")
        else:
            paths.add(path)
        if kind == "decision" and (not isinstance(path, str) or not re.fullmatch(r"docs/decisions/CD-[0-9]{4}(?:-.*)?\.md", path)):
            fail(findings, f"{prefix}: decision is outside the canonical CD decision path")

        status = record["status"]
        if not isinstance(status, str) or status not in {"accepted", "published", "superseded"} or (status == "published" and kind != "lesson") or (status == "accepted" and kind == "lesson"):
            fail(findings, f"{prefix}: invalid status/kind combination")
        successor = record.get("successor")
        if status == "superseded" and not valid_id(successor):
            fail(findings, f"{prefix}: superseded record requires a clean successor")
        if status != "superseded" and "successor" in record:
            fail(findings, f"{prefix}: successor is only valid for superseded records")

        try:
            datetime.fromisoformat(record["date"].replace("Z", "+00:00"))
        except (AttributeError, ValueError):
            fail(findings, f"{prefix}: date is not RFC3339")
        for field, maximum in (("title", 256), ("summary", 4096)):
            if not isinstance(record[field], str) or not 0 < len(record[field]) <= maximum or record[field] != record[field].strip():
                fail(findings, f"{prefix}: invalid bounded {field}")
        if not unique_string_list(record["tags"], 64) or not all(valid_id(item) for item in record["tags"]):
            fail(findings, f"{prefix}: invalid tags")

        scopes = record["scopes"]
        if not isinstance(scopes, dict) or set(scopes) != ALLOWED_SCOPES or scopes.get("mode") not in {"home", "explicit"}:
            fail(findings, f"{prefix}: invalid closed scopes")
        else:
            for field in sorted(ALLOWED_SCOPES - {"mode"}):
                values = scopes[field]
                if not unique_string_list(values, 64) or not all(valid_id(item) for item in values):
                    fail(findings, f"{prefix}: invalid {field}")
            if scopes["mode"] == "home" and any(scopes[field] for field in ALLOWED_SCOPES - {"mode"}):
                fail(findings, f"{prefix}: home scopes cannot contain explicit IDs")

        if not isinstance(record["sha256"], str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", record["sha256"]):
            fail(findings, f"{prefix}: invalid sha256 proof")
        target = ROOT / path if isinstance(path, str) else ROOT / "missing"
        if isinstance(path, str) and (target.is_symlink() or not target.is_file()):
            fail(findings, f"{prefix}: dangling path: {path}")
        elif check_hashes and target.is_file() and isinstance(record.get("sha256"), str) and re.fullmatch(r"sha256:[0-9a-f]{64}", record["sha256"]):
            actual = "sha256:" + hashlib.sha256(target.read_bytes()).hexdigest()
            if actual != record["sha256"]:
                fail(findings, f"{prefix}: hash drift for {path}")

    by_id = {record.get("id"): record for record in records if isinstance(record, dict) and isinstance(record.get("id"), str)}
    for number, record in enumerate(records):
        if not isinstance(record, dict) or record.get("status") != "superseded" or not isinstance(record.get("successor"), str):
            continue
        prefix = f"manifest.records[{number}]"
        successor = record["successor"]
        if successor == record.get("id"):
            fail(findings, f"{prefix}: successor cannot be self")
            continue
        target = by_id.get(successor)
        if target is None:
            fail(findings, f"{prefix}: successor is not declared in this manifest: {successor}")
            continue
        if target.get("kind") != record.get("kind"):
            fail(findings, f"{prefix}: successor kind does not match")
        expected_status = "published" if record.get("kind") == "lesson" else "accepted"
        if target.get("status") != expected_status:
            fail(findings, f"{prefix}: successor status is incompatible")

    expected_decisions = {
        path.stem.split("-", 2)[0] + ("-" + path.stem.split("-", 2)[1] if len(path.stem.split("-", 2)) > 1 else ""): path.relative_to(ROOT).as_posix()
        for path in (ROOT / "docs/decisions").glob("CD-*.md")
    }
    for identifier, expected_path in expected_decisions.items():
        matches = [record for record in records if isinstance(record, dict) and record.get("id") == identifier]
        if len(matches) != 1:
            fail(findings, f"manifest: decision {identifier} must map exactly once")
            continue
        record = matches[0]
        if record.get("path") != expected_path or record.get("kind") != "decision":
            fail(findings, f"manifest: decision {identifier} does not map to its exact decision path/kind")
        if record.get("status") not in {"accepted", "superseded"}:
            fail(findings, f"manifest: decision {identifier} has invalid status")
    for record in records:
        if not isinstance(record, dict):
            continue
        path = record.get("path")
        identifier = record.get("id")
        if isinstance(path, str) and path.startswith("docs/decisions/") and path not in expected_decisions.values():
            fail(findings, f"manifest: extra decision path is forbidden: {path}")
        if isinstance(identifier, str) and identifier.startswith("CD-") and identifier not in expected_decisions:
            fail(findings, f"manifest: extra decision ID is forbidden: {identifier}")
    return findings


def atomic_write(path: Path, content: str) -> None:
    temporary: str | None = None
    try:
        with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=path.parent, prefix=f".{path.name}.", suffix=".tmp", delete=False) as handle:
            temporary = handle.name
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
        temporary = None
    finally:
        if temporary is not None:
            try:
                os.unlink(temporary)
            except FileNotFoundError:
                pass


def update_manifest(data: object) -> list[str]:
    findings = validate(data, check_hashes=False)
    if findings:
        return findings
    assert isinstance(data, dict)
    records = data["records"]
    for record in records:
        assert isinstance(record, dict)
        target = ROOT / record["path"]
        record["sha256"] = "sha256:" + hashlib.sha256(target.read_bytes()).hexdigest()
    findings = validate(data, check_hashes=True)
    if findings:
        return findings
    try:
        content = json.dumps(data, indent=2, ensure_ascii=False) + "\n"
        atomic_write(MANIFEST, content)
    except OSError as exc:
        return [f"manifest: atomic update failed: {exc}"]
    return []


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--update", action="store_true", help="recompute hashes for an already-valid authored manifest")
    args = parser.parse_args()
    findings: list[str] = []
    data = load(MANIFEST, findings)
    if data is not None and args.update and not findings:
        findings = update_manifest(data)
    elif data is not None:
        findings = validate(data)
    for finding in findings:
        print(finding)
    if findings:
        print(f"Knowledge index validation failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("Knowledge index validation passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
