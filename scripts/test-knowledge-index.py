#!/usr/bin/env python3
"""Focused tests for the durable-knowledge checker and atomic updater."""

from __future__ import annotations

import copy
import importlib.util
import json
import tempfile
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).with_name("check-knowledge-index.py")
SPEC = importlib.util.spec_from_file_location("knowledge_checker", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


def fixture(path: str = "docs/lesson.md", digest: str = "a" * 64) -> dict:
    return {
        "schema_version": "1.0",
        "supported_kinds": ["lesson", "research"],
        "indexed_kinds": ["lesson"],
        "records": [{
            "id": "lesson-1", "kind": "lesson", "path": path, "status": "published",
            "date": "2026-08-10T00:00:00Z", "title": "Lesson", "summary": "Summary", "tags": [],
            "scopes": {"mode": "home", "product_ids": [], "project_ids": [], "component_ids": [], "tag_ids": []},
            "sha256": "sha256:" + digest,
        }],
    }


def test_duplicate_keys_at_every_level() -> None:
    cases = [
        '{"schema_version":"1.0","schema_version":"1.0"}',
        '{"records":[{"id":"a","id":"b"}]}',
        '{"records":[{"scopes":{"mode":"home","mode":"explicit"}}]}',
    ]
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        path = Path(directory) / "duplicate.json"
        for raw in cases:
            path.write_text(raw, encoding="utf-8")
            findings: list[str] = []
            assert checker.load(path, findings) is None
            assert findings and "invalid JSON" in findings[0]


def test_invalid_update_is_byte_identical() -> None:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        (root / "docs").mkdir()
        document = root / "docs/lesson.md"
        document.write_text("lesson\n", encoding="utf-8")
        target = root / "manifest.json"
        value = fixture(digest="b" * 64)
        value["unknown"] = True
        original = json.dumps(value, indent=2) + "\n"
        target.write_text(original, encoding="utf-8")
        with mock.patch.object(checker, "ROOT", root), mock.patch.object(checker, "MANIFEST", target):
            findings = checker.update_manifest(value)
        assert findings
        assert target.read_text(encoding="utf-8") == original


def test_atomic_replacement_failure_preserves_original_and_cleans_temp() -> None:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        target = Path(directory) / "manifest.json"
        target.write_text("original\n", encoding="utf-8")
        with mock.patch.object(checker.os, "replace", side_effect=OSError("simulated replacement failure")):
            try:
                checker.atomic_write(target, "replacement\n")
            except OSError:
                pass
            else:
                raise AssertionError("atomic replacement unexpectedly succeeded")
        assert target.read_text(encoding="utf-8") == "original\n"
        assert not list(target.parent.glob(f".{target.name}.*.tmp"))
        with mock.patch.object(checker.tempfile, "NamedTemporaryFile", side_effect=OSError("simulated write failure")):
            try:
                checker.atomic_write(target, "replacement\n")
            except OSError:
                pass
            else:
                raise AssertionError("atomic write unexpectedly succeeded")
        assert target.read_text(encoding="utf-8") == "original\n"


def test_successful_update_changes_hashes_only() -> None:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        (root / "docs").mkdir()
        document = root / "docs/lesson.md"
        document.write_text("lesson\n", encoding="utf-8")
        target = root / "manifest.json"
        value = fixture(digest="b" * 64)
        before = copy.deepcopy(value)
        with mock.patch.object(checker, "ROOT", root), mock.patch.object(checker, "MANIFEST", target):
            findings = checker.update_manifest(value)
        assert not findings
        after = json.loads(target.read_text(encoding="utf-8"))
        before_hash = before["records"][0].pop("sha256")
        after_hash = after["records"][0].pop("sha256")
        assert before["records"] == after["records"]
        assert before_hash != after_hash
        assert after_hash == "sha256:" + checker.hashlib.sha256(b"lesson\n").hexdigest()


def test_path_bound_uses_unicode_scalars() -> None:
    assert len("docs/" + "é" * 504 + ".md") == checker.MAX_MANIFEST_PATH
    assert len("docs/" + "é" * 505 + ".md") == checker.MAX_MANIFEST_PATH + 1


def test_uppercase_hash_is_rejected() -> None:
    findings = checker.validate(fixture(digest="A" * 64), check_hashes=False)
    assert any("invalid sha256 proof" in finding for finding in findings)


def test_nul_path_is_rejected_without_traceback() -> None:
    findings = checker.validate(fixture(path="docs/lesson\x00.md"), check_hashes=False)
    assert any("forbidden or unsafe path" in finding for finding in findings)


if __name__ == "__main__":
    for name, function in sorted(globals().items()):
        if name.startswith("test_"):
            function()
    print("knowledge checker tests passed")
