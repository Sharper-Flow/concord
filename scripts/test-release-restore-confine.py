#!/usr/bin/env python3
"""Tests for scripts/release_restore_confine.py.

Children are small scripts. The suite lowers its own hard file-size limit
before any test runs, and every child inherits it, so no test can write a file
larger than FILE_LIMIT even when the bound under test is broken. The grower
also stops at GROWTH_CAP, so a missing bound shows as a timeout.
"""

from __future__ import annotations

import json
import resource
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path

import release_restore_confine as confine

FILE_LIMIT = 64 << 20
GROWTH_CAP = 8 << 20
resource.setrlimit(resource.RLIMIT_FSIZE, (FILE_LIMIT, FILE_LIMIT))

# Grows HOME/growth to GROWTH_CAP and records its pid. "grow" writes at full
# speed; "trickle" writes from a forked grandchild while the command idles;
# "orphan" does the same, but the command exits once growth has begun.
GROWER = """import os, sys, time
mode = sys.argv[1]
home = os.environ["HOME"]
growth = os.path.join(home, "growth")
if mode != "grow" and os.fork():
    while mode == "orphan" and not (os.path.exists(growth) and os.path.getsize(growth)):
        time.sleep(0.02)
    if mode == "orphan":
        open(os.path.join(home, "exited"), "w").close()
        sys.exit(0)
    time.sleep(60)
    sys.exit(0)
with open(os.path.join(home, "pid"), "w") as handle:
    handle.write(str(os.getpid()))
with open(growth, "wb") as handle:
    while handle.tell() < %d:
        handle.write(b"x" * 65536)
        handle.flush()
        time.sleep(0 if mode == "grow" else 0.05)
time.sleep(60)
""" % GROWTH_CAP


def write(path: Path, text: str) -> Path:
    path.write_text(text, encoding="utf-8")
    path.chmod(0o755)
    return path


def write_grower(root: Path) -> Path:
    return write(root / "grower.py", GROWER)


class RunBoundedTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def bounded(self, body: str, seconds: float = 0.5, limit: int = confine.STREAM_LIMIT) -> confine.CaseFailure | None:
        script = write(self.root / "child", f"#!/bin/sh\n{body}\n")
        try:
            confine.run_bounded([str(script)], {"PATH": "/usr/bin:/bin"}, "", time.monotonic() + seconds, self.root / "streams", "child",
                             confine.Guard(lambda: None, lambda: 1 << 30), limit=limit)
        except confine.CaseFailure as failure:
            return failure
        return None

    def test_the_kernel_caps_a_child_file_when_the_scratch_check_is_off(self) -> None:
        home = self.root / "home"
        home.mkdir()
        started = time.monotonic()
        rc, _ = confine.run_bounded([sys.executable, str(write_grower(self.root)), "grow"],
                                 {"PATH": "/usr/bin:/bin", "HOME": str(home)}, "{}", time.monotonic() + 20,
                                 self.root / "streams", "grow", confine.Guard(lambda: None, lambda: 1 << 20))
        # The fixture is Python, which ignores SIGXFSZ itself, so its write fails with EFBIG.
        self.assertNotEqual(rc, 0)
        self.assertIn("File too large", (self.root / "streams" / "grow.stderr").read_text())
        self.assertEqual((home / "growth").stat().st_size, 1 << 20)
        self.assertLess(time.monotonic() - started, 10)

    def test_the_launcher_refuses_a_child_whose_gate_is_gone(self) -> None:
        marker = self.root / "ran"
        launcher = confine.confinement(1 << 20)
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
                marker = self.root / f"survivor-{name}"
                self.bounded(f"(sleep 2; touch {marker}) &\n{body}")
                time.sleep(2.5)
                self.assertFalse(marker.exists(), "a background child survived its parent")

    def test_streams_are_bounded_while_the_child_runs(self) -> None:
        failure = self.bounded("yes", seconds=20, limit=1 << 20)
        self.assertIsNotNone(failure)
        self.assertIn("streams exceeded", failure.detail)

    def test_checkpoint_names_a_signal_and_an_exhausted_deadline(self) -> None:
        with self.assertRaisesRegex(confine.CaseFailure, "budget exhausted"):
            confine.checkpoint(time.monotonic() - 1)
        confine.STOP.set()
        try:
            with self.assertRaisesRegex(confine.CaseFailure, "stopped by a signal"):
                confine.checkpoint(time.monotonic() + 60)
            self.assertEqual(self.bounded("exit 0").stage, "interrupted")
        finally:
            confine.STOP.clear()


# Runs the child runner from a worker thread, as the gate's pool does, with the
# gate's interrupt handlers, then prints how the run ended. With "linger", the
# runner is held after the command exits, before its own cleanup runs.
DRIVER = """
import json, sys, threading, time
from pathlib import Path
sys.path.insert(0, sys.argv[1])
import release_restore_confine as confine
confine.install_interrupt_handlers()
outcome = {}
def linger():
    # Holds the gate after the command exited, before its own cleanup runs.
    if (Path(sys.argv[3]) / "exited").exists():
        time.sleep(30)
def work():
    try:
        confine.run_bounded([sys.executable, sys.argv[2], sys.argv[4]], {"PATH": "/usr/bin:/bin", "HOME": sys.argv[3]}, "{}",
                         time.monotonic() + 60, Path(sys.argv[3]) / "streams", "trickle", confine.Guard(linger, lambda: 1 << 30))
    except confine.CaseFailure as failure:
        outcome["stage"] = failure.stage
worker = threading.Thread(target=work)
worker.start()
while worker.is_alive():
    worker.join(0.05)
print(json.dumps(outcome))
"""


class InterruptTests(unittest.TestCase):
    """Stopping the gate stops its children and their descendants: a handled signal kills the group, and a
    SIGKILL of the gate reaches the group through its supervisor's death signal. The writer is a forked grandchild."""

    def stop_gate(self, signum: int, mode: str = "trickle") -> tuple[subprocess.CompletedProcess, Path]:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            grower = write_grower(root)
            home = root / "home"
            home.mkdir()
            driver = subprocess.Popen([sys.executable, "-c", DRIVER, str(Path(__file__).parent), str(grower), str(home), mode],
                                      stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            deadline = time.monotonic() + 15
            while not ((home / "growth").exists() and (home / "growth").stat().st_size > 0):
                self.assertLess(time.monotonic(), deadline, "the trickle child never started")
                time.sleep(0.05)
            if mode == "orphan":
                while not (home / "exited").exists():
                    self.assertLess(time.monotonic(), deadline, "the command never exited")
                    time.sleep(0.05)
                time.sleep(1)
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

    def test_a_background_writer_dies_with_its_command_before_the_gate_cleans_up(self) -> None:
        result, _ = self.stop_gate(signal.SIGKILL, mode="orphan")
        self.assertEqual(result.returncode, -signal.SIGKILL)


def alive(pid: int) -> bool:
    try:
        state = Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()[0]
    except FileNotFoundError:
        return False
    return state not in ("Z", "X")


if __name__ == "__main__":
    unittest.main()
