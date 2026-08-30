#!/usr/bin/env python3
"""Prove the predecessor harvest derives, refuses, and validates correctly.

The reader's failure mode is a snapshot that looks complete and is not, so every
assertion here is about a refusal or a derivation, never about formatting. The
sanctioned reads themselves are not exercised: they belong to another product,
and a test that needs a live predecessor store proves nothing on a clean runner.
"""

from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("harvest", ROOT / "scripts/predecessor-harvest.py")
harvest = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(harvest)


def change(**overrides) -> dict:
    base = {
        "id": "repairThing",
        "title": "Repair thing",
        "status": "draft",
        "lifecycleState": "open",
        "lastActivityAt": "2026-08-30T19:14:35.005Z",
        "tasksDone": 1,
        "tasksTotal": 12,
        "firstIncompleteGate": "execution",
        "gateProgressStr": "\u2713 \u2713 \u2713 \u2713 \u25cb \u25cb \u25cb",
    }
    base.update(overrides)
    return base


def test_completed_gates_derives_the_prefix() -> None:
    gates = harvest.completed_gates(change(), "repairThing")
    assert gates == ["proposal", "discovery", "design", "planning"], gates


def test_disagreeing_gate_forms_refuse() -> None:
    entry = change(gateProgressStr="\u2713 \u2713 \u25cb \u25cb \u25cb \u25cb \u25cb")
    try:
        harvest.completed_gates(entry, "repairThing")
    except harvest.HarvestError as error:
        assert "gate progress" in str(error), error
        return
    raise AssertionError("a gate-count disagreement must refuse")


def test_unknown_gate_refuses() -> None:
    try:
        harvest.completed_gates(change(firstIncompleteGate="triage"), "repairThing")
    except harvest.HarvestError:
        return
    raise AssertionError("an unknown gate must refuse")


def test_active_selection_keeps_execution_and_drops_terminal() -> None:
    payload = {
        "changes": [
            change(),
            change(id="doneThing", firstIncompleteGate=None, gateProgressStr=""),
            change(id="closedThing", lifecycleState="closed"),
        ]
    }
    selected = harvest.active_changes(payload)
    assert [item["change_id"] for item in selected] == ["repairThing"], selected
    assert selected[0]["tasks_total"] == 12
    assert selected[0]["updated_at"] == "2026-08-30T19:14:35.005Z"


def test_change_without_id_refuses() -> None:
    try:
        harvest.active_changes({"changes": [{"lifecycleState": "open"}]})
    except harvest.HarvestError:
        return
    raise AssertionError("a change without an id must refuse")


class FakeReader:
    def __init__(self, payload: dict) -> None:
        self.payload = payload

    def call(self, tool: str, arguments: dict) -> dict:
        assert tool == "wisdom_list", tool
        assert arguments == {"project_only": True}, arguments
        return self.payload


def test_truncated_wisdom_refuses() -> None:
    reader = FakeReader({"wisdom": [], "count": 4})
    try:
        harvest.read_wisdom(reader, "example-project")
    except harvest.HarvestError as error:
        assert "truncated" in str(error), error
        return
    raise AssertionError("a truncated wisdom listing must refuse")


def test_wisdom_maps_to_the_contract() -> None:
    reader = FakeReader(
        {
            "wisdom": [
                {
                    "id": "pw-1",
                    "type": "pattern",
                    "content": "use idempotent jobs",
                    "source_change": "resolveExampleBlockers",
                    "promoted_at": "2026-05-26T17:16:31.500Z",
                    "scope": "project",
                }
            ],
            "count": 1,
        }
    )
    entries = harvest.read_wisdom(reader, "example-project")
    assert entries == [
        {
            "id": "pw-1",
            "type": "pattern",
            "content": "use idempotent jobs",
            "change_id": "resolveExampleBlockers",
            "promoted": True,
            "recorded_at": "2026-05-26T17:16:31.500Z",
        }
    ], entries


def snapshot_with(project: dict) -> dict:
    return {
        "schema_version": 1,
        "captured_at": "2026-08-30T22:00:00Z",
        "producer": "scripts/predecessor-harvest.py",
        "source_system": "advance",
        "projects": [project],
    }


def test_snapshot_validates_against_the_contract() -> None:
    project = {
        "project_id": "example-project",
        "locator": "/srv/example-project",
        "archived_changes": 0,
        "closed_changes": 0,
        "active_changes": harvest.active_changes({"changes": [change()]}),
        "wisdom_entries": [],
        "reflections": [],
    }
    harvest.validate(snapshot_with(project))


def test_gaps_refuse_unless_accepted() -> None:
    original = harvest.harvest_project

    def fake(project_id: str, project_dir: Path, mcp_command: str):
        project = {
            "project_id": project_id,
            "locator": str(project_dir),
            "archived_changes": 2,
            "closed_changes": 0,
            "active_changes": [],
            "wisdom_entries": [],
            "reflections": [],
        }
        return project, [f"{project_id}: reflections not captured"]

    harvest.harvest_project = fake
    try:
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory) / "snapshot.json"
            argv = ["--project", f"example-project={directory}", "--out", str(out)]
            assert harvest.main(argv) == 3, "a recorded gap must refuse by default"
            assert not out.exists(), "a refused harvest must write nothing"
            assert harvest.main(argv + ["--accept-gaps"]) == 0
            written = json.loads(out.read_text(encoding="utf-8"))
            assert "capture gaps" in written["producer"], written["producer"]
            assert written["projects"][0]["reflections"] == []
    finally:
        harvest.harvest_project = original


def main() -> int:
    failures = 0
    tests = [value for name, value in sorted(globals().items()) if name.startswith("test_")]
    for test in tests:
        try:
            test()
        except AssertionError as error:
            failures += 1
            print(f"FAIL {test.__name__}: {error}", file=sys.stderr)
    if failures:
        print(f"predecessor-harvest tests: {failures} failed", file=sys.stderr)
        return 1
    print("predecessor-harvest tests: all passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
