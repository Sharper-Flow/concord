#!/usr/bin/env python3
"""Tests for scripts/release-restore-gate.py.

The GitHub release population is injected as data, and `gh release download`
is faked at the subprocess boundary by an executable on PATH. Writers and
candidates are small executables that read and write real SQLite files, so
snapshot, conservation, and effect checks run against real stores. No
published binary is downloaded and no installed store is reachable. The
population join has its own suite, test-release-restore-population.py.

The suite lowers its own hard file-size limit before any test runs, and every
child inherits it, so no test can write a file larger than FILE_LIMIT even when
a bound under test is broken. Growth fixtures also stop at GROWTH_CAP.
"""

from __future__ import annotations

import argparse
import dataclasses
import hashlib
import importlib.util
import json
import os
import resource
import signal
import sqlite3
import subprocess
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
FILE_LIMIT = 64 << 20
GROWTH_CAP = 8 << 20
resource.setrlimit(resource.RLIMIT_FSIZE, (FILE_LIMIT, FILE_LIMIT))

# A bounded runaway: grow HOME/growth to GROWTH_CAP at `pace` seconds per
# 64 KiB, then idle, so a missing gate bound shows as a timeout, not a full disk.
GROW = """
def grow(pace):
    import os, time
    with open(os.path.join(os.environ["HOME"], "pid"), "w") as handle:
        handle.write(str(os.getpid()))
    with open(os.path.join(os.environ["HOME"], "growth"), "wb") as handle:
        while handle.tell() < {cap}:
            handle.write(b"x" * 65536)
            handle.flush()
            time.sleep(pace)
    time.sleep(60)
""".format(cap=GROWTH_CAP).replace("{", "{{").replace("}", "}}")

WRITER = """#!{python}
import json, os, sqlite3, sys
TAG = {tag!r}
MODE = {mode!r}
""" + GROW + """
if sys.argv[1:] == ["--version"]:
    print("v0.0.0-wrong" if MODE == "wrong-version" else TAG)
    sys.exit(0)
with open(os.path.join(os.environ["HOME"], "env.json"), "w") as handle:
    json.dump(dict(os.environ), handle)
if MODE == "refuse":
    sys.exit(2)
if MODE == "grow":
    grow(0)
if MODE == "trickle":
    grow(0.05)
if MODE == "deaf":
    import time
    time.sleep(30)
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
""" + GROW + """
if sys.argv[1:] == ["--version"]:
    if MODE == "grow-preflight":
        grow(0)
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


def release_of(writer: gate.Writer, release_id: int) -> dict:
    return {"id": release_id, "tag_name": writer.tag, "draft": False, "assets": [
        {"id": writer.asset_id, "name": f"concord-{writer.tag}", "size": writer.asset_size, "digest": writer.asset_digest},
        {"id": writer.checksum_asset_id, "name": f"concord-{writer.tag}.sha256", "size": writer.checksum_size}]}


def tag_map(*tags: str) -> dict:
    return {tag: (f"{index:040x}", f"{index + 100:040x}") for index, tag in enumerate(tags, start=1)}


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

    def case(self, writer: gate.Writer, candidate: Path, case_seconds: int = 60, scratch: gate.Scratch | None = None,
             transfer: int = 1 << 30) -> dict:
        work = self.fixture.root / "work"
        return gate.run_writer_case("owner/repo", writer, candidate, work, work / "streams" / writer.tag, case_seconds,
                                    time.monotonic() + 600, scratch or gate.Scratch(1 << 30, 1), gate.Transfer(transfer))

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
        self.assertCase(self.case(self.fixture.publish("v1.0.1"), candidate, scratch=gate.Scratch(10, 1)), "unknown", "disk")

    def test_scratch_growth_stops_a_running_writer(self) -> None:
        scratch = gate.Scratch(4 << 20, 1)
        started = time.monotonic()
        row = self.case(self.fixture.publish("v1.0.0", mode="grow"), self.fixture.candidate(), scratch=scratch)
        self.assertCase(row, "unknown", "disk")
        self.assertLess(time.monotonic() - started, 20)
        self.assertGreater(scratch.retained, 0)
        self.assertLess(scratch.allowance(), 4 << 20)

    def test_a_writer_that_never_reads_stdin_meets_its_budget(self) -> None:
        started = time.monotonic()
        row = self.case(self.fixture.publish("v1.0.0", mode="deaf"), self.fixture.candidate(), case_seconds=2)
        self.assertCase(row, "unknown", "product-create")
        self.assertLess(time.monotonic() - started, 10)

    def bounded(self, body: str, seconds: float = 0.5, limit: int = gate.STREAM_LIMIT) -> gate.CaseFailure | None:
        script = self.fixture.write(self.fixture.root / "child", f"#!/bin/sh\n{body}\n")
        try:
            gate.run_bounded([str(script)], {"PATH": "/usr/bin:/bin"}, "", time.monotonic() + seconds, self.fixture.root / "streams", "child",
                             gate.Guard(lambda: None, lambda: 1 << 30), limit=limit)
        except gate.CaseFailure as failure:
            return failure
        return None

    def test_the_kernel_caps_a_child_file_when_the_scratch_check_is_off(self) -> None:
        writer = self.fixture.publish("v1.0.0", mode="grow")
        home = self.fixture.root / "home"
        home.mkdir()
        started = time.monotonic()
        rc, _ = gate.run_bounded([sys.executable, str(self.fixture.assets / str(writer.asset_id)), "product-create"],
                                 {"PATH": "/usr/bin:/bin", "HOME": str(home)}, "{}", time.monotonic() + 20,
                                 self.fixture.root / "streams", "grow", gate.Guard(lambda: None, lambda: 1 << 20))
        # The fixture is Python, which ignores SIGXFSZ itself, so its write fails with EFBIG.
        self.assertNotEqual(rc, 0)
        self.assertIn("File too large", (self.fixture.root / "streams" / "grow.stderr").read_text())
        self.assertEqual((home / "growth").stat().st_size, 1 << 20)
        self.assertLess(time.monotonic() - started, 10)

    def test_the_launcher_refuses_a_child_whose_gate_is_gone(self) -> None:
        marker = self.fixture.root / "ran"
        launcher = gate.confinement(1 << 20)
        launcher[-2] = "1"
        probe = subprocess.run([*launcher, "/bin/touch", str(marker)], capture_output=True)
        self.assertEqual(probe.returncode, 125)
        self.assertFalse(marker.exists(), "the child ran after its gate was gone")

    def test_every_child_inherits_the_suite_file_limit(self) -> None:
        probe = subprocess.run([sys.executable, "-c", "import resource; print(resource.getrlimit(resource.RLIMIT_FSIZE))"],
                               capture_output=True, text=True, check=True)
        self.assertEqual(probe.stdout.strip(), str((FILE_LIMIT, FILE_LIMIT)))

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
        row = gate.run_writer_case("owner/repo", writer, self.fixture.candidate(), work, work / "s", 60, time.monotonic() - 1,
                                   gate.Scratch(1 << 30, 1), gate.Transfer(1 << 30))
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

    def test_preflight_bounds_scratch_and_the_wall(self) -> None:
        args = self.args(self.fixture.candidate("grow-preflight"))
        args.disk_bytes = 1 << 20
        with self.assertRaisesRegex(gate.GateError, "preflight exceeds its scratch"):
            gate.run_gate(args, [], {})
        with self.assertRaisesRegex(gate.GateError, "during enumeration"):
            gate.run_gate(self.args(self.fixture.candidate()), [], {}, time.monotonic() - 1)

    def test_snapshot_refuses_a_store_beyond_the_remaining_scratch(self) -> None:
        store = self.fixture.root / "store.db"
        store.write_bytes(b"x" * 4096)
        with self.assertRaisesRegex(gate.CaseFailure, "exceeds the case's remaining scratch"):
            gate.snapshot_fixture(store, self.fixture.root / "snap.db", "v1.0.0", room=lambda: 100)

    def test_population_reads_run_confined_bounded_and_interruptible(self) -> None:
        page = [{"id": 1, "tag_name": "v1.0.0"}]
        (self.fixture.assets / "releases?per_page=100&page=1").parent.mkdir(parents=True, exist_ok=True)
        (self.fixture.assets / "releases?per_page=100&page=1").write_text(json.dumps(page))
        streams = self.fixture.root / "out" / "streams" / "population"
        run = gate.population_runner(streams, time.monotonic() + 60)
        self.assertEqual(gate.gh_release_page("owner/repo", 1, run), page)
        with self.assertRaisesRegex(gate.GateError, "exited 1"):
            gate.gh_release_page("owner/repo", 2, run)
        with self.assertRaisesRegex(gate.GateError, "budget exhausted"):
            gate.gh_release_page("owner/repo", 1, gate.population_runner(streams, time.monotonic() - 1))
        gate.STOP.set()
        try:
            with self.assertRaisesRegex(gate.GateError, "stopped by a signal"):
                gate.gh_release_page("owner/repo", 1, run)
        finally:
            gate.STOP.clear()

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


# Runs the gate's child runner from a worker thread, as the gate's pool does,
# with the gate's own interrupt handlers, then prints how the run ended.
DRIVER = """
import importlib.util, json, sys, threading, time
from pathlib import Path
sys.path.insert(0, str(Path(sys.argv[1]).parent))
spec = importlib.util.spec_from_file_location("gate", sys.argv[1])
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)
gate.install_interrupt_handlers()
outcome = {}
def work():
    try:
        gate.run_bounded([sys.executable, sys.argv[2], "product-create"], {"PATH": "/usr/bin:/bin", "HOME": sys.argv[3]}, "{}",
                         time.monotonic() + 60, Path(sys.argv[3]) / "streams", "trickle", gate.Guard(lambda: None, lambda: 1 << 30))
    except gate.CaseFailure as failure:
        outcome["stage"] = failure.stage
worker = threading.Thread(target=work)
worker.start()
while worker.is_alive():
    worker.join(0.05)
print(json.dumps(outcome))
"""


class InterruptTests(unittest.TestCase):
    """Stopping the gate stops its children: a handled signal kills them, and SIGKILL reaches them through pdeathsig."""

    def stop_gate(self, signum: int) -> tuple[subprocess.CompletedProcess, Path]:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            writer_tag = "v1.0.0"
            fixture = Fixture(root)
            writer = fixture.publish(writer_tag, mode="trickle")
            home = root / "home"
            home.mkdir()
            gate_path = Path(__file__).with_name("release-restore-gate.py")
            driver = subprocess.Popen([sys.executable, "-c", DRIVER, str(gate_path), str(fixture.assets / str(writer.asset_id)), str(home)],
                                      stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            deadline = time.monotonic() + 15
            while not ((home / "growth").exists() and (home / "growth").stat().st_size > 0):
                self.assertLess(time.monotonic(), deadline, "the trickle child never started")
                time.sleep(0.05)
            child = int((home / "pid").read_text())
            driver.send_signal(signum)
            out, err = driver.communicate(timeout=15)
            time.sleep(0.3)
            size = (home / "growth").stat().st_size
            time.sleep(0.5)
            self.assertEqual((home / "growth").stat().st_size, size, "the child kept writing after the gate stopped")
            self.assertLess(size, GROWTH_CAP)
            self.assertFalse(alive(child), f"child {child} outlived the gate")
            return subprocess.CompletedProcess(driver.args, driver.returncode, out, err), home

    def test_a_handled_signal_kills_the_child_and_reports_interrupted(self) -> None:
        for signum in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
            with self.subTest(signal=signum.name):
                result, _ = self.stop_gate(signum)
                self.assertEqual((result.returncode, json.loads(result.stdout)), (0, {"stage": "interrupted"}), result.stderr)

    def test_sigkill_of_the_gate_kills_the_child(self) -> None:
        result, _ = self.stop_gate(signal.SIGKILL)
        self.assertEqual(result.returncode, -signal.SIGKILL)


def alive(pid: int) -> bool:
    try:
        state = Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()[0]
    except FileNotFoundError:
        return False
    return state not in ("Z", "X")


if __name__ == "__main__":
    unittest.main()
