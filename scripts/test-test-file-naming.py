#!/usr/bin/env python3
"""Tests for scripts/check-test-file-naming.py."""

from __future__ import annotations

import importlib.util
import tempfile
from pathlib import Path


SPEC = importlib.util.spec_from_file_location(
    "check_test_file_naming", Path(__file__).with_name("check-test-file-naming.py")
)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


def test_decision_record_name_is_rejected() -> None:
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)
        path = root / "internal" / "store"
        path.mkdir(parents=True)
        (path / "cd0018_urgency_test.go").write_text("package store\n", encoding="utf-8")
        findings = checker.check(root=root)
        assert len(findings) == 1
        assert "decision record number" in findings[0]


def test_issue_name_is_rejected() -> None:
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)
        (root / "workflow_issue31_test.go").write_text("package main\n", encoding="utf-8")
        findings = checker.check(root=root)
        assert len(findings) == 1
        assert "issue number" in findings[0]


def test_behavior_name_is_accepted() -> None:
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)
        (root / "workflow_action_events_test.go").write_text("package main\n", encoding="utf-8")
        assert checker.check(root=root) == []


def main() -> int:
    failures = 0
    for name, function in sorted(globals().items()):
        if not name.startswith("test_") or not callable(function):
            continue
        try:
            function()
            print(f"ok  {name}")
        except AssertionError as error:
            failures += 1
            print(f"FAIL {name}: {error}")
    if failures:
        return 1
    print("test file naming tests passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
