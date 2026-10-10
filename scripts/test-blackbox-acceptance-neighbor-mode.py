#!/usr/bin/env python3
"""Unit tests for the opt-in neighbor mode in test-blackbox-acceptance.py.

Narrow and cheap: pure classification, aggregation, reconciliation, and
argument parsing, plus one small round trip that runs the real worker child
process against a stub binary emitting canned CLI JSON. No Go build, no
concord binary, no network, and no SQLite file.
"""

from __future__ import annotations

import importlib.util
import sys
import tempfile
import time
import unittest
from pathlib import Path

_HARNESS_PATH = Path(__file__).resolve().parent / "test-blackbox-acceptance.py"


def _load_harness():
    spec = importlib.util.spec_from_file_location("test_blackbox_acceptance_under_test", _HARNESS_PATH)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


_bb = _load_harness()


class ClassificationTests(unittest.TestCase):
    def test_lock_contention_classifies_as_busy(self):
        for detail in (
            "HarnessError: exit=1; stderr=database is locked (5) (SQLITE_BUSY)",
            'HarnessError: outcome=error; error={"detail":"database table is locked"}',
            "HarnessError: exit=1; stderr=sqlite_busy on read snapshot",
            "HarnessError: exit=1; stderr=SQLITE_LOCKED on worktree claim",
        ):
            with self.subTest(detail=detail):
                self.assertEqual(_bb._classify_cli_failure(detail), "busy")

    def test_other_failures_classify_as_error(self):
        for detail in (
            "HarnessError: exit=1; stderr=unknown command",
            'HarnessError: outcome=error; error={"code":"version_conflict"}',
            "HarnessError: authorization denied for work_define",
            "",
        ):
            with self.subTest(detail=detail):
                self.assertEqual(_bb._classify_cli_failure(detail), "error")


class IdentityTests(unittest.TestCase):
    def test_idempotency_keys_and_titles_are_unique_across_writers(self):
        keys: set[str] = set()
        titles: set[str] = set()
        for writer in range(_bb.NEIGHBOR_MAX_NEIGHBORS):
            for number in range(1, _bb.NEIGHBOR_MAX_WRITES + 1):
                key = _bb._neighbor_key(writer, number)
                title = _bb._neighbor_title(writer, number)
                self.assertNotIn(key, keys, key)
                self.assertNotIn(title, titles, title)
                keys.add(key)
                titles.add(title)


class AggregationTests(unittest.TestCase):
    def test_counts_busy_errors_and_progress(self):
        entries = [
            ("reader-00", [{"kind": "read", "ok": True}, {"kind": "read", "ok": True}]),
            ("reader-01", [{"kind": "read", "ok": False, "busy": True, "detail": "db locked"}]),
            (
                "writer-00",
                [
                    {"kind": "write", "ok": True, "work_id": "work-a"},
                    {"kind": "write", "ok": False, "busy": False, "detail": "boom"},
                ],
            ),
        ]
        aggregate = _bb._aggregate_neighbor_entries(entries)
        self.assertEqual(aggregate["busy"], 1)
        self.assertEqual(aggregate["errors"], 1)
        self.assertEqual(aggregate["reader_ok"], {"reader-00": 2})
        self.assertEqual(aggregate["writer_ok"], {"writer-00": 1})
        self.assertEqual(aggregate["expected_ids"], ["work-a"])

    def test_verdict_reports_each_failure_shape(self):
        base = {
            "busy": 0,
            "errors": 0,
            "reader_ok": {"reader-00": 3},
            "writer_ok": {"writer-00": 2},
            "expected_ids": ["work-a"],
        }
        self.assertIsNone(_bb._neighbor_mode_verdict(base, 1, 2))
        self.assertIn("busy", _bb._neighbor_mode_verdict(dict(base, busy=2), 1, 2))
        self.assertIn("error", _bb._neighbor_mode_verdict(dict(base, errors=1), 1, 2))
        self.assertIn(
            "no successful read",
            _bb._neighbor_mode_verdict(dict(base, reader_ok={}), 1, 2),
        )
        self.assertIn(
            "1 of 2 writes",
            _bb._neighbor_mode_verdict(dict(base, writer_ok={"writer-00": 1}), 1, 2),
        )


class ReconcileTests(unittest.TestCase):
    def test_clean_population_has_no_findings(self):
        findings = _bb._reconcile_neighbor_items(
            ["work-a", "work-b"], ["suite-1", "work-a", "work-b"], 3)
        self.assertEqual(findings, [])

    def test_lost_item_is_reported(self):
        findings = _bb._reconcile_neighbor_items(["work-a", "work-b"], ["work-a"], 2)
        self.assertTrue(any(f.startswith("lost: work-b") for f in findings), findings)
        self.assertTrue(any("count 1 != expected 2" in f for f in findings), findings)

    def test_duplicate_item_is_reported(self):
        findings = _bb._reconcile_neighbor_items(["work-a"], ["work-a", "work-a"], 1)
        self.assertTrue(any("duplicate: work-a listed 2 times" in f for f in findings), findings)

    def test_duplicate_capture_identity_is_reported(self):
        findings = _bb._reconcile_neighbor_items(["work-a", "work-a"], ["work-a"], 1)
        self.assertTrue(any("duplicate capture identity work-a" in f for f in findings), findings)

    def test_unexpected_population_count_is_reported(self):
        findings = _bb._reconcile_neighbor_items(["work-a"], ["work-a", "x", "y"], 2)
        self.assertEqual(findings, ["work item count 3 != expected 2"])


class ArgumentTests(unittest.TestCase):
    def test_defaults_leave_the_mode_off(self):
        args = _bb._parse_args([])
        self.assertEqual(args.neighbors, 0)
        self.assertEqual(args.neighbor_writes, 4)
        self.assertIsNone(args.neighbor_worker)

    def test_neighbor_mode_parses(self):
        args = _bb._parse_args(["--neighbors", "3", "--neighbor-writes", "6"])
        self.assertEqual(args.neighbors, 3)
        self.assertEqual(args.neighbor_writes, 6)

    def test_out_of_range_neighbors_are_refused(self):
        with self.assertRaises(SystemExit):
            _bb._parse_args(["--neighbors", str(_bb.NEIGHBOR_MAX_NEIGHBORS + 1)])

    def test_out_of_range_writes_are_refused(self):
        with self.assertRaises(SystemExit):
            _bb._parse_args(["--neighbors", "1", "--neighbor-writes", "0"])


class WorkerRoundTripTests(unittest.TestCase):
    """The real worker child process, driven by a stub binary that prints one
    canned CLI JSON line acceptable to the writer capture, the product read,
    and the work list shapes alike."""

    STUB_RESPONSE = (
        '{"outcome":"ok",'
        '"changed_refs":[{"entity_kind":"work","id":"work-stub-1","version":4}],'
        '"result":{"items":[{"id":"work-stub-1"}],"next_cursor":null}}'
    )

    def _run_mode(self, tmp: Path, neighbors: int, writes: int, hold: float):
        stub_dir = tmp / "bin"
        stub_dir.mkdir(exist_ok=True)
        stub = stub_dir / "stub-concord"
        stub.write_text(f"#!/bin/sh\nprintf '%s\\n' '{self.STUB_RESPONSE}'\n", encoding="utf-8")
        stub.chmod(0o755)
        work = tmp / "mode"
        work.mkdir(exist_ok=True)
        mode = _bb.NeighborMode(
            binary=stub,
            env={"PATH": "/usr/bin:/bin"},
            repo=tmp,
            manifest_digest="stub-digest",
            work=work,
            neighbors=neighbors,
            writes=writes,
        )
        mode.start({"project_id": "stub-project", "scope_version": 1})
        self.assertFalse(mode.stop_path.exists())
        time.sleep(hold)
        aggregate = mode.stop_and_collect()
        self.assertTrue(mode.stop_path.exists())
        return aggregate


class SymmetricWorkerRoundTrip(WorkerRoundTripTests):
    def test_readers_and_writers_report_clean_progress(self):
        with tempfile.TemporaryDirectory() as raw:
            aggregate = self._run_mode(Path(raw), neighbors=2, writes=2, hold=2.0)
        self.assertEqual(aggregate.get("problems"), [])
        self.assertEqual(aggregate["busy"], 0)
        self.assertEqual(aggregate["errors"], 0)
        self.assertGreaterEqual(aggregate["reader_ok"].get("reader-00", 0), 1)
        self.assertGreaterEqual(aggregate["reader_ok"].get("reader-01", 0), 1)
        self.assertEqual(aggregate["writer_ok"].get("writer-00", 0), 2)
        self.assertEqual(aggregate["writer_ok"].get("writer-01", 0), 2)
        self.assertEqual(aggregate["expected_ids"], ["work-stub-1"] * 4)
        self.assertIsNone(_bb._neighbor_mode_verdict(aggregate, 2, 2))


if __name__ == "__main__":
    unittest.main()
