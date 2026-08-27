#!/usr/bin/env python3
"""Test archived-work vocabulary closure mutations."""
from __future__ import annotations

import importlib.util
import json
import re
import tempfile
from pathlib import Path


SCRIPT = Path(__file__).with_name("check-archived-work-kind.py")
SPEC = importlib.util.spec_from_file_location("archived_work_kind", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)

ROOT = checker.ROOT


def repository_copy() -> tuple[tempfile.TemporaryDirectory[str], Path]:
    temporary = tempfile.TemporaryDirectory()
    root = Path(temporary.name)
    (root / "contracts").mkdir()
    (root / "internal/store").mkdir(parents=True)
    for relative in (checker.SCHEMA, checker.SCHEMA_SOURCE):
        destination = root / relative
        destination.write_text((ROOT / relative).read_text(encoding="utf-8"), encoding="utf-8")
    return temporary, root


def migration_text(root: Path) -> str:
    return (root / checker.SCHEMA_SOURCE).read_text(encoding="utf-8")


def replace_trigger_list(root: Path, replacement: str, trigger: str = "archived_work_kind_insert") -> None:
    source = migration_text(root)
    pattern = re.compile(
        rf"(CREATE TRIGGER {re.escape(trigger)}\b.*?WHEN\s+NEW\.type\s+NOT\s+IN\s*\()(?P<values>.*?)(\))",
        re.S,
    )
    updated, count = pattern.subn(rf"\g<1>{replacement}\g<3>", source, count=1)
    assert count == 1
    (root / checker.SCHEMA_SOURCE).write_text(updated, encoding="utf-8")


def test_repository_passes() -> None:
    assert checker.check(ROOT) == []


def test_dropping_a_trigger_kind_fails() -> None:
    temporary, root = repository_copy()
    try:
        replace_trigger_list(root, "'work_note', 'constitution', 'decision', 'spec', 'lesson', 'reference'")
        findings = checker.check(root)
        assert any("archived_work_kind_insert" in finding for finding in findings), findings
    finally:
        temporary.cleanup()


def test_adding_a_trigger_kind_fails() -> None:
    temporary, root = repository_copy()
    try:
        replace_trigger_list(root, "'work_note', 'constitution', 'decision', 'spec', 'lesson', 'reference', 'research', 'charter'")
        findings = checker.check(root)
        assert any("archived_work_kind_insert" in finding for finding in findings), findings
    finally:
        temporary.cleanup()


def test_changing_the_manifest_enum_fails() -> None:
    temporary, root = repository_copy()
    try:
        manifest_path = root / checker.SCHEMA
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        manifest["properties"]["supported_kinds"]["items"]["enum"].remove("research")
        manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
        findings = checker.check(root)
        assert any("archived_work_kind_insert" in finding for finding in findings), findings
    finally:
        temporary.cleanup()


if __name__ == "__main__":
    for name, function in sorted(globals().items()):
        if name.startswith("test_"):
            function()
    print("archived-work kind vocabulary tests passed")
