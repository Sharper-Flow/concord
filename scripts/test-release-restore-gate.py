#!/usr/bin/env python3
"""Tests for scripts/release-restore-gate.py.

The GitHub release population is injected as data, and `gh release download`
is faked at the subprocess boundary by an executable on PATH. Writers and
candidates are small executables that read and write real SQLite files, so
snapshot, conservation, and effect checks run against real stores. No
published binary is downloaded and no installed store is reachable.
"""

from __future__ import annotations

import argparse
import dataclasses
import hashlib
import importlib.util
import json
import os
import sqlite3
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("release_restore_gate", Path(__file__).with_name("release-restore-gate.py"))
assert SPEC and SPEC.loader
gate = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = gate
SPEC.loader.exec_module(gate)

TARGET = "a" * 40
SETTLED = gate.SETTLED_UNPUBLISHED["v8.28.5"]

WRITER = """#!{python}
import json, os, sqlite3, sys
TAG = {tag!r}
MODE = {mode!r}
if sys.argv[1:] == ["--version"]:
    print("v0.0.0-wrong" if MODE == "wrong-version" else TAG)
    sys.exit(0)
with open(os.path.join(os.environ["HOME"], "env.json"), "w") as handle:
    json.dump(dict(os.environ), handle)
if MODE == "refuse":
    sys.exit(2)
request = json.load(sys.stdin)
db = sqlite3.connect(os.environ["CONCORD_DB_PATH"])
db.execute("CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)")
db.execute("INSERT OR IGNORE INTO schema_migrations VALUES (7)")
db.execute("CREATE TABLE IF NOT EXISTS domain_events (seq INTEGER PRIMARY KEY, kind TEXT NOT NULL, payload TEXT NOT NULL)")
db.execute("INSERT INTO domain_events (kind, payload) VALUES (?, ?)", (sys.argv[1], json.dumps(request, sort_keys=True)))
db.commit()
print(json.dumps({{"ok": True}}))
"""

CANDIDATE = """#!{python}
import json, sqlite3, sys
VERSION = {version!r}
MODE = {mode!r}
if sys.argv[1:] == ["--version"]:
    print(VERSION)
    sys.exit(0)
if MODE == "refuse":
    sys.exit(1)
if MODE == "self-modify":
    open(__file__, "a").write("\\n")
request = json.load(sys.stdin)
manifest = json.load(open(request["source"] + ".manifest.json"))
second = manifest["binary_version"].endswith("-restored")
if MODE == "mutate-source":
    open(request["source"], "ab").write(b"x")
source = sqlite3.connect("file:" + request["source"] + "?mode=ro", uri=True)
target = sqlite3.connect(request["destination"])
source.backup(target)
source.close()
if MODE == "rewrite-event":
    target.execute("UPDATE domain_events SET payload = '{{}}' WHERE seq = 1")
if MODE == "drop-event":
    target.execute("DELETE FROM domain_events WHERE seq = 2")
elif MODE == "rebuild-drift" or not second:
    target.execute("INSERT INTO domain_events (kind, payload) VALUES ('migration.appended', '{{}}')")
for table, columns in (("products", "id, display_name"), ("projects", "id, display_name"), ("product_projects", "product_id, project_id, role"),
                       ("project_locators", "project_id, locator_id, kind, locator_value")):
    target.execute(f"CREATE TABLE IF NOT EXISTS {{table}} ({{columns}})")
if not second:
    value = "https://example.invalid/other.git" if MODE == "lose-effect" else "https://example.invalid/gate/project.git"
    target.execute("INSERT INTO products VALUES ('gate-product', 'Gate Product')")
    target.execute("INSERT INTO projects VALUES ('gate-project', 'Gate Project')")
    target.execute("INSERT INTO product_projects VALUES ('gate-product', 'gate-project', 'primary')")
    target.execute("INSERT INTO project_locators VALUES ('gate-project', 'gate-remote', 'git_remote', ?)", (value,))
target.commit()
watermark = target.execute("SELECT MAX(seq) FROM domain_events").fetchone()[0]
target.close()
print(json.dumps({{"source_snapshot": manifest, "restored_database": {{"schema_version": 9, "event_watermark": watermark}}}}))
"""

FAKE_GH = """#!{python}
import sys
from pathlib import Path
asset = Path({assets!r}) / sys.argv[-1].rsplit("/", 1)[-1]
if not asset.exists():
    sys.exit(1)
sys.stdout.buffer.write(asset.read_bytes())
"""


def release(tag: str, release_id: int, assets: tuple[str, ...] | None = None, draft: bool = False) -> dict:
    names = assets if assets is not None else (f"concord-{tag}", f"concord-{tag}.sha256")
    return {"id": release_id, "tag_name": tag, "draft": draft,
            "assets": [{"id": release_id * 10 + index, "name": name, "size": 1} for index, name in enumerate(names)]}


def release_of(writer: gate.Writer, release_id: int) -> dict:
    return {"id": release_id, "tag_name": writer.tag, "draft": False, "assets": [
        {"id": writer.asset_id, "name": f"concord-{writer.tag}", "size": writer.asset_size, "digest": writer.asset_digest},
        {"id": writer.checksum_asset_id, "name": f"concord-{writer.tag}.sha256", "size": writer.checksum_size}]}


def tag_map(*tags: str) -> dict:
    return {tag: (f"{index:040x}", f"{index + 100:040x}") for index, tag in enumerate(tags, start=1)}


class PopulationTests(unittest.TestCase):
    def test_every_release_page_is_read(self) -> None:
        pages = {1: [release(f"v1.0.{n}", n) for n in range(gate.PER_PAGE)], 2: [release("v2.0.0", 999)]}
        releases = gate.list_releases(gate.PER_PAGE + 1, lambda page: pages.get(page, []))
        self.assertEqual(releases[-1]["tag_name"], "v2.0.0")
        self.assertEqual(len(releases), gate.PER_PAGE + 1)

    def test_short_or_long_pagination_refuses(self) -> None:
        pages = {1: [release("v1.0.0", 1)]}
        with self.assertRaisesRegex(gate.GateError, "1 of 2"):
            gate.list_releases(2, lambda page: pages.get(page, []))
        with self.assertRaisesRegex(gate.GateError, "2 of 1"):
            gate.list_releases(1, lambda page: [release("v1.0.0", 1), release("v1.0.1", 2)])

    def test_join_classifies_every_tag(self) -> None:
        releases = [release("v1.0.0", 1), release("v1.1.0", 2, assets=("concord-v1.1.0",)),
                    release("v1.2.0", 3), release("v1.3.0", 4, draft=True)]
        tags = tag_map("v1.0.0", "v1.1.0", "v1.4.0", "v9.1.0")
        tags["v8.28.5"] = SETTLED
        writers, unknown, unavailable, excluded = gate.enumerate_writers(releases, tags, "v9.0.0", TARGET)
        self.assertEqual([writer.tag for writer in writers], ["v1.0.0"])
        self.assertEqual({(row["tag"], row["stage"]) for row in unknown},
                         {("v1.1.0", "artifact"), ("v1.2.0", "population"), ("v1.4.0", "population"), ("v9.1.0", "population")})
        self.assertEqual([row["tag"] for row in unavailable], ["v8.28.5"])
        self.assertEqual(excluded, [])

    def test_retargeted_settled_tag_is_unknown(self) -> None:
        tags = {"v8.28.5": (SETTLED[0], "f" * 40)}
        _, unknown, unavailable, _ = gate.enumerate_writers([], tags, "v9.0.0", TARGET)
        self.assertEqual([row["tag"] for row in unknown], ["v8.28.5"])
        self.assertEqual(unavailable, [])

    def test_candidate_identity_rules(self) -> None:
        tags = tag_map("v1.0.0", "v2.0.0")
        tags["v2.0.0"] = ("b" * 40, TARGET)
        releases = [release("v1.0.0", 1), release("v2.0.0", 2)]
        writers, _, _, excluded = gate.enumerate_writers(releases, tags, "v2.0.0", TARGET)
        self.assertEqual(([w.tag for w in writers], excluded), (["v1.0.0"], ["v2.0.0"]))
        writers, unknown, _, excluded = gate.enumerate_writers(releases[:1], tags, "v2.0.0", TARGET)
        self.assertEqual((excluded, unknown), (["v2.0.0"], []))
        _, unknown, _, excluded = gate.enumerate_writers(releases[:1], tags, "v2.0.0", "c" * 40)
        self.assertEqual((excluded, [row["tag"] for row in unknown]), ([], ["v2.0.0"]))
        with self.assertRaisesRegex(gate.GateError, "not earlier"):
            gate.enumerate_writers(releases, tags, "v2.0.0", "c" * 40)
        with self.assertRaisesRegex(gate.GateError, "not earlier"):
            gate.enumerate_writers(releases, tags, "v1.5.0", TARGET)

    def test_duplicate_or_unknown_grammar_refuses(self) -> None:
        tags = tag_map("v1.0.0")
        with self.assertRaisesRegex(gate.GateError, "duplicate"):
            gate.enumerate_writers([release("v1.0.0", 1), release("v1.0.0", 2)], tags, "v2.0.0", TARGET)
        with self.assertRaisesRegex(gate.GateError, "strict release version"):
            gate.enumerate_writers([release("v1.0", 1)], tags, "v2.0.0", TARGET)


class Fixture:
    """Fake published assets, a fake gh, writers, and candidates under one root."""

    def __init__(self, root: Path) -> None:
        self.root = root
        self.assets = root / "assets"
        self.next_id = 0
        (root / "bin").mkdir(parents=True)
        self.write(root / "bin" / "gh", FAKE_GH.format(python=sys.executable, assets=str(self.assets)))

    @staticmethod
    def write(path: Path, text: str) -> Path:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")
        path.chmod(0o755)
        return path

    def publish(self, tag: str, mode: str = "ok", corrupt: bool = False, size_delta: int = 0) -> gate.Writer:
        self.next_id += 2
        binary = self.write(self.assets / str(self.next_id), WRITER.format(python=sys.executable, tag=tag, mode=mode))
        digest = hashlib.sha256(binary.read_bytes()).hexdigest()
        checksum = self.assets / str(self.next_id + 1)
        checksum.write_text(f"{'0' * 64 if corrupt else digest}  concord-{tag}\n", encoding="utf-8")
        return gate.Writer(tag, 1, "1" * 40, "2" * 40, self.next_id, binary.stat().st_size + size_delta, f"sha256:{digest}",
                           self.next_id + 1, checksum.stat().st_size)

    def candidate(self, mode: str = "ok", version: str = "v9.0.0") -> Path:
        return self.write(self.root / f"candidate-{mode}", CANDIDATE.format(python=sys.executable, version=version, mode=mode))


class CaseTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.fixture = Fixture(Path(self.tempdir.name))
        path = f"{self.fixture.root / 'bin'}{os.pathsep}{os.environ['PATH']}"
        env = {"PATH": path, "CONCORD_DB_PATH": "/live/concord.db", "GH_TOKEN": "synthetic-token"}
        self.env = patch.dict(os.environ, env)
        self.env.start()

    def tearDown(self) -> None:
        self.env.stop()
        self.tempdir.cleanup()

    def case(self, writer: gate.Writer, candidate: Path, case_seconds: int = 60, disk: int = 1 << 30, transfer: int = 1 << 30) -> dict:
        work = self.fixture.root / "work"
        return gate.run_writer_case("owner/repo", writer, candidate, work, work / "streams" / writer.tag,
                                    case_seconds, time.monotonic() + 600, disk, gate.Transfer(transfer))

    def assertCase(self, row: dict, status: str, stage: str) -> None:
        self.assertEqual((row["status"], row["stage"]), (status, stage), row)

    def test_a_writer_restores_twice_and_its_case_is_removed(self) -> None:
        row = self.case(self.fixture.publish("v1.0.0"), self.fixture.candidate())
        self.assertCase(row, "pass", "complete")
        self.assertEqual((row["source"]["event_watermark"], row["restored"]["appended_events"], row["rebuilt"]["appended_events"]), (2, 1, 0))
        self.assertFalse((self.fixture.root / "work" / "v1.0.0").exists())

    def test_writer_environment_is_constructed(self) -> None:
        self.case(self.fixture.publish("v1.0.0", mode="refuse"), self.fixture.candidate())
        case = self.fixture.root / "work" / "v1.0.0"
        seen = json.loads((case / "home" / "env.json").read_text())
        self.assertEqual(seen["CONCORD_DB_PATH"], str(case / "store" / "concord.db"))
        self.assertNotIn("GH_TOKEN", seen)
        self.assertEqual(seen["PATH"], "/usr/bin:/bin")

    def test_artifact_and_writer_failures_are_named(self) -> None:
        candidate = self.fixture.candidate()
        self.assertCase(self.case(self.fixture.publish("v1.0.0", corrupt=True), candidate), "fail", "checksum")
        self.assertCase(self.case(self.fixture.publish("v1.0.1", mode="wrong-version"), candidate), "fail", "version")
        self.assertCase(self.case(self.fixture.publish("v1.0.2", mode="refuse"), candidate), "unknown", "product-create")
        self.assertCase(self.case(self.fixture.publish("v1.0.3", size_delta=1), candidate), "fail", "download")
        replaced = dataclasses.replace(self.fixture.publish("v1.0.4"), asset_id=999)
        self.assertCase(self.case(replaced, candidate), "unknown", "download")

    def test_candidate_failures_are_named(self) -> None:
        for number, (mode, stage) in enumerate((("refuse", "restore"), ("mutate-source", "conservation"),
                                                ("rewrite-event", "conservation"), ("drop-event", "restore"),
                                                ("lose-effect", "effects"), ("rebuild-drift", "rebuild"))):
            with self.subTest(mode=mode):
                row = self.case(self.fixture.publish(f"v1.1.{number}"), self.fixture.candidate(mode))
                self.assertCase(row, "fail", stage)

    def test_resource_budgets_stop_the_case(self) -> None:
        candidate = self.fixture.candidate()
        self.assertCase(self.case(self.fixture.publish("v1.0.0"), candidate, transfer=10), "unknown", "transfer")
        self.assertCase(self.case(self.fixture.publish("v1.0.1"), candidate, disk=1), "unknown", "disk")

    def bounded(self, body: str, seconds: float = 0.5, limit: int = gate.STREAM_LIMIT) -> gate.CaseFailure | None:
        script = self.fixture.write(self.fixture.root / "child", f"#!/bin/sh\n{body}\n")
        try:
            gate.run_bounded([str(script)], {"PATH": "/usr/bin:/bin"}, "", time.monotonic() + seconds, self.fixture.root / "streams", "child", limit=limit)
        except gate.CaseFailure as failure:
            return failure
        return None

    def test_every_child_ending_kills_its_process_group(self) -> None:
        for name, body in (("timeout", "sleep 30"), ("exit", "exit 0")):
            with self.subTest(ending=name):
                marker = self.fixture.root / f"survivor-{name}"
                self.bounded(f"(sleep 2; touch {marker}) &\n{body}")
                time.sleep(2.5)
                self.assertFalse(marker.exists(), "a background child survived its parent")

    def test_streams_are_bounded_while_the_child_runs(self) -> None:
        failure = self.bounded("yes", seconds=20, limit=1 << 20)
        self.assertIsNotNone(failure)
        self.assertIn("streams exceeded", failure.detail)

    def test_exhausted_wall_budget_is_unknown(self) -> None:
        writer = self.fixture.publish("v1.0.0")
        work = self.fixture.root / "work"
        row = gate.run_writer_case("owner/repo", writer, self.fixture.candidate(), work, work / "s", 60, time.monotonic() - 1, 1 << 30, gate.Transfer(1 << 30))
        self.assertCase(row, "unknown", "wall")

    def args(self, candidate: Path, version: str = "v9.0.0") -> argparse.Namespace:
        return argparse.Namespace(repository="owner/repo", target_sha=TARGET, candidate=str(candidate), candidate_version=version,
                                  output_dir=str(self.fixture.root / "out"), workers=2, wall_seconds=600, case_seconds=60,
                                  disk_bytes=1 << 30, transfer_bytes=1 << 30)

    def gate_run(self, candidate: Path, *writers: gate.Writer) -> tuple[dict, int]:
        releases = [release_of(writer, index) for index, writer in enumerate(writers, start=1)]
        return gate.run_gate(self.args(candidate), releases, tag_map(*(writer.tag for writer in writers)))

    def test_gate_holds_coverage_when_every_writer_passes(self) -> None:
        report, status = self.gate_run(self.fixture.candidate(), self.fixture.publish("v1.0.0"), self.fixture.publish("v1.2.0"))
        self.assertEqual((status, report["result"], report["coverage"]["status"]), (3, "pass", "held"))
        self.assertEqual([row["tag"] for row in report["rows"]], ["v1.0.0", "v1.2.0"])
        self.assertGreater(report["budgets"]["transferred_bytes"], 0)

    def test_gate_fails_on_any_writer_or_empty_population(self) -> None:
        report, status = self.gate_run(self.fixture.candidate(), self.fixture.publish("v1.0.0"), self.fixture.publish("v1.2.0", corrupt=True))
        self.assertEqual((status, report["result"], report["counts"]), (1, "fail", {"pass": 1, "fail": 1, "unknown": 0}))
        report, status = gate.run_gate(self.args(self.fixture.candidate()), [], {})
        self.assertEqual((status, report["result"]), (1, "unknown"))

    def test_candidate_identity_is_bound(self) -> None:
        with self.assertRaisesRegex(gate.GateError, "candidate reports"):
            gate.run_gate(self.args(self.fixture.candidate(version="v8.0.0")), [], {})
        with self.assertRaisesRegex(gate.GateError, "changed during the run"):
            self.gate_run(self.fixture.candidate("self-modify"), self.fixture.publish("v1.0.0"))

    def test_main_refuses_bad_identity_and_reused_output(self) -> None:
        output = self.fixture.root / "used"
        (output / "old").mkdir(parents=True)
        common = ["run", "--repository", "owner/repo", "--candidate", "x", "--candidate-version", "v9.0.0"]
        self.assertEqual(gate.main([*common, "--target-sha", TARGET, "--output-dir", str(output)]), 1)
        self.assertIn("not empty", json.loads((output / "report.json").read_text())["detail"])
        fresh = self.fixture.root / "fresh"
        self.assertEqual(gate.main([*common, "--target-sha", "abc", "--output-dir", str(fresh)]), 1)
        self.assertEqual(json.loads((fresh / "report.json").read_text())["result"], "unknown")


class SnapshotTests(unittest.TestCase):
    def test_empty_journal_is_refused(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            store = Path(directory) / "store.db"
            db = sqlite3.connect(store)
            db.execute("CREATE TABLE schema_migrations (version INTEGER)")
            db.execute("CREATE TABLE domain_events (seq INTEGER PRIMARY KEY)")
            db.commit()
            db.close()
            with self.assertRaisesRegex(gate.CaseFailure, "empty"):
                gate.snapshot_fixture(store, Path(directory) / "snap.db", "v1.0.0")


if __name__ == "__main__":
    unittest.main()
