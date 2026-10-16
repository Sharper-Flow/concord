#!/usr/bin/env python3
"""Unit tests for helpers of test-blackbox-acceptance.py, including the
opt-in neighbor mode.

Narrow and cheap: pure classification, aggregation, reconciliation, and
argument parsing, one small round trip that runs the real worker child
process against a stub binary emitting canned CLI JSON, and mocked
step_h_backup_restore report-consumer checks. No Go build, no concord
binary, no network, and no SQLite file.
"""

from __future__ import annotations

import importlib.util
import sys
import tempfile
import time
import unittest
from unittest.mock import Mock, patch
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


class WorkerCleanupTests(unittest.TestCase):
    def test_partial_start_is_cleanup_eligible(self):
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            mode = _bb.NeighborMode(root / "concord", {}, root, "digest", root, 2, 1)
            child = Mock(returncode=0)
            with patch.object(_bb.subprocess, "Popen", side_effect=[child, OSError("spawn failed")]):
                with self.assertRaises(OSError):
                    mode.start({"project_id": "project", "scope_version": 1})
            self.assertTrue(mode.started, "partial startup must reach the parent's cleanup")
            mode.stop_and_collect()
            child.wait.assert_called_once()
            self.assertTrue(mode.stop_path.exists())

    def test_cleanup_failures_fail_the_verdict(self):
        aggregate = {
            "busy": 0, "errors": 0,
            "reader_ok": {"reader-00": 1}, "writer_ok": {"writer-00": 1},
            "problems": ["child exited 1"],
        }
        self.assertIsNotNone(_bb._neighbor_mode_verdict(aggregate, 1, 1))


class RestoreReportTests(unittest.TestCase):
    """step_h_backup_restore consumes the current nested RestoreReport.

    Every CLI call is answered by a mocked _bb._run, so the checks stay pure
    report-consumer behavior: no concord binary, no SQLite file, no network.
    The packaged harness keeps its real backup/restore coverage in hosted CI.
    """

    WORK_ID = "work-restore-1"

    @staticmethod
    def _backup_manifest(snapshot_id: str = "snap-harness-1") -> dict:
        return {"schema_version": 1, "integrity_check": "ok", "snapshot_id": snapshot_id}

    @classmethod
    def _restore_report(cls, source_snapshot: dict) -> dict:
        return {
            "source_snapshot": source_snapshot,
            "restored_database": {
                "schema_version": 1,
                "db_checksum": "stub-checksum",
                "event_watermark": 7,
            },
        }

    def _mocked_run(self, restore_response: dict):
        calls: list[tuple[list[str], dict, dict]] = []

        def fake_run(binary, args, stdin_payload, env, timeout=60):
            calls.append((list(args), dict(stdin_payload or {}), dict(env)))
            command = args[0]
            if command == "backup":
                return self._backup_manifest()
            if command == "restore":
                destination = Path(stdin_payload["destination"])
                destination.parent.mkdir(parents=True, exist_ok=True)
                destination.write_bytes(b"restored")
                return restore_response
            if command == "invoke":
                return {
                    "outcome": "ok",
                    "result": {"items": [{"id": self.WORK_ID}], "next_cursor": None},
                }
            raise AssertionError(f"unexpected CLI call: {args}")

        return patch.object(_bb, "_run", fake_run), calls

    def _run_step(self, tmp: Path) -> None:
        _bb.step_h_backup_restore(
            binary=tmp / "concord",
            env={"PATH": "/usr/bin:/bin"},
            work=tmp / "work",
            repo=tmp / "repo",
            context={"project_id": "stub-project", "scope_version": 1},
            manifest_digest="stub-digest",
            work_id=self.WORK_ID,
        )

    def test_current_nested_report_succeeds_through_fresh_process_read(self):
        with tempfile.TemporaryDirectory() as raw:
            tmp = Path(raw)
            patcher, calls = self._mocked_run(self._restore_report(self._backup_manifest()))
            with patcher:
                self._run_step(tmp)
            self.assertEqual([args[0] for args, _, _ in calls], ["backup", "restore", "invoke"])
            restored_destination = Path(calls[1][1]["destination"])
            invoke_env = calls[2][2]
            self.assertEqual(
                restored_destination,
                Path(invoke_env["XDG_DATA_HOME"]) / "concord" / "concord.db",
            )
            self.assertTrue(restored_destination.is_file())

    def test_bad_or_missing_source_integrity_is_refused(self):
        for source_snapshot in (
            dict(self._backup_manifest(), integrity_check="failed"),
            {k: v for k, v in self._backup_manifest().items() if k != "integrity_check"},
        ):
            with self.subTest(source_integrity=source_snapshot.get("integrity_check")):
                with tempfile.TemporaryDirectory() as raw:
                    patcher, calls = self._mocked_run(self._restore_report(source_snapshot))
                    with patcher, self.assertRaises(_bb.HarnessError) as refused:
                        self._run_step(Path(raw))
                self.assertIn("integrity", str(refused.exception))
                self.assertEqual([args[0] for args, _, _ in calls], ["backup", "restore"])

    def test_mismatched_source_snapshot_id_is_refused(self):
        other_snapshot = dict(self._backup_manifest(), snapshot_id="snap-other")
        with tempfile.TemporaryDirectory() as raw:
            patcher, calls = self._mocked_run(self._restore_report(other_snapshot))
            with patcher, self.assertRaises(_bb.HarnessError) as refused:
                self._run_step(Path(raw))
        self.assertIn("snapshot", str(refused.exception))
        self.assertEqual([args[0] for args, _, _ in calls], ["backup", "restore"])

    def test_obsolete_flat_restore_response_is_refused(self):
        with tempfile.TemporaryDirectory() as raw:
            patcher, calls = self._mocked_run(self._backup_manifest())
            with patcher, self.assertRaises(_bb.HarnessError):
                self._run_step(Path(raw))
        self.assertEqual([args[0] for args, _, _ in calls], ["backup", "restore"])


if __name__ == "__main__":
    unittest.main()
