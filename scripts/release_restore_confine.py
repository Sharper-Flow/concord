"""Confinement for every child process the release restore gate starts.

Each child runs under a supervisor that leads its own process group and runs the
command as the leader of a second group. The supervisor dies with the gate's
spawning thread and refuses to start once the gate is gone. It kills the
command's whole group when the command exits, when the gate signals it, and when
the gate dies. The kernel caps every file the child and its descendants write.
While the child runs, its deadline, its stream budget, the caller's guard, and a
terminating signal to the gate are all checked, so a runaway child is stopped
rather than measured afterward. A terminating signal stops every running child.
"""

from __future__ import annotations

import os
import resource
import signal
import subprocess
import sys
import threading
import time
from pathlib import Path

STREAM_LIMIT = 64 * 1024 * 1024
POLL_SECONDS = 0.05


class CaseFailure(Exception):
    """A named non-pass outcome of one bounded step: its status, the stage that stopped, and why."""

    def __init__(self, status: str, stage: str, detail: str) -> None:
        super().__init__(detail)
        self.status, self.stage, self.detail = status, stage, detail


class Guard:
    """A child's running scratch check and the hard per-file size the kernel enforces on it."""

    def __init__(self, check, file_bytes) -> None:
        self.check, self.file_bytes = check, file_bytes

    def __call__(self) -> None:
        self.check()


# Process groups of running children. A terminating signal kills each of them,
# so stopping the gate cannot leave a writer or candidate running unwatched.
STOP = threading.Event()
ACTIVE: set[int] = set()
ACTIVE_LOCK = threading.RLock()


def interrupt(signum, frame) -> None:
    STOP.set()
    with ACTIVE_LOCK:
        for group in list(ACTIVE):
            try:
                os.killpg(group, signal.SIGTERM)
            except ProcessLookupError:
                pass


def install_interrupt_handlers() -> None:
    for signum in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
        signal.signal(signum, interrupt)


# Supervisor for every child. The gate starts it as the leader of its own
# process group; it runs the command as the leader of a second group and owns
# that group's whole lifetime. It arms PR_SET_PDEATHSIG with SIGTERM and refuses
# to run when its parent is no longer the gate, closing the window between fork
# and arming. On SIGTERM, from the gate or from the gate's death, and again when
# the command exits, it kills the command's group, so no descendant outlives
# either the command or the gate. Its file-size limit is inherited by every
# descendant, and the command gets back the signal dispositions Python ignores.
# The supervisor ends the way its command ended.
LAUNCHER = """
import ctypes, os, resource, signal, sys
command = 0
def stop(signum, frame):
    if command:
        try:
            os.killpg(command, signal.SIGKILL)
        except ProcessLookupError:
            pass
    os._exit(128 + signum)
signal.signal(signal.SIGTERM, stop)
if ctypes.CDLL(None, use_errno=True).prctl(1, signal.SIGTERM, 0, 0, 0) != 0:
    sys.exit(126)
if os.getppid() != int(sys.argv[1]):
    sys.exit(125)
resource.setrlimit(resource.RLIMIT_FSIZE, (int(sys.argv[2]), int(sys.argv[2])))
signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGTERM})
child = os.fork()
if child == 0:
    os.setpgid(0, 0)
    for signum in (signal.SIGPIPE, signal.SIGXFSZ, signal.SIGTERM):
        signal.signal(signum, signal.SIG_DFL)
    signal.pthread_sigmask(signal.SIG_UNBLOCK, {signal.SIGTERM})
    try:
        os.execvp(sys.argv[3], sys.argv[3:])
    except OSError:
        os._exit(127)
try:
    os.setpgid(child, child)
except OSError:
    pass
command = child
signal.pthread_sigmask(signal.SIG_UNBLOCK, {signal.SIGTERM})
os.waitid(os.P_PID, command, os.WEXITED | os.WNOWAIT)
signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGTERM})
try:
    os.killpg(command, signal.SIGKILL)
except ProcessLookupError:
    pass
code = os.waitstatus_to_exitcode(os.waitpid(command, 0)[1])
if code < 0:
    signal.signal(-code, signal.SIG_DFL)
    signal.pthread_sigmask(signal.SIG_UNBLOCK, {-code})
    os.kill(os.getpid(), -code)
sys.exit(code)
"""
SUPERVISOR_SECONDS = 10


def confinement(file_bytes: int) -> list[str]:
    """The supervisor prefix: the group dies with the spawning thread, and no file grows above file_bytes.

    PR_SET_PDEATHSIG fires when the forking thread ends, so a caller spawns and
    waits for its child on one thread. RLIMIT_FSIZE cannot be raised past the
    inherited hard limit, so the cap is clamped to it.
    """
    hard = resource.getrlimit(resource.RLIMIT_FSIZE)[1]
    cap = file_bytes if hard == resource.RLIM_INFINITY else min(file_bytes, hard)
    if cap < 1:
        raise CaseFailure("unknown", "disk", "no scratch remains for the child's files")
    return [sys.executable, "-I", "-c", LAUNCHER, str(os.getpid()), str(cap)]


def run_bounded(argv: list[str], env: dict[str, str] | None, stdin: str, deadline: float, streams: Path, label: str,
                guard: Guard, stdout_path: Path | None = None, limit: int = STREAM_LIMIT) -> tuple[int, str]:
    """Run one confined child in its own process group and kill the whole group when it ends.

    The deadline, the stream limit, the caller's guard, and an interrupt are
    checked while the child runs and once it exits, so a runaway child is stopped
    rather than measured afterward. The kernel caps every file the child writes.
    """
    if STOP.is_set():
        raise CaseFailure("unknown", "interrupted", "the gate was stopped by a signal")
    prefix = confinement(guard.file_bytes())
    streams.mkdir(parents=True, exist_ok=True)
    in_path, out_path, err_path = streams / f"{label}.stdin", stdout_path or streams / f"{label}.stdout", streams / f"{label}.stderr"
    in_path.write_text(stdin, encoding="utf-8")
    with in_path.open("rb") as feed, out_path.open("wb") as out, err_path.open("wb") as err:
        child = subprocess.Popen([*prefix, *argv], env=env, stdin=feed, stdout=out, stderr=err, start_new_session=True)
        with ACTIVE_LOCK:
            ACTIVE.add(child.pid)
        try:
            # WNOWAIT leaves the exited supervisor unreaped, so its group ID cannot
            # be reused before the group is signalled below.
            while os.waitid(os.P_PID, child.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT) is None:
                if STOP.is_set():
                    raise CaseFailure("unknown", "interrupted", "the gate was stopped by a signal")
                if time.monotonic() >= deadline:
                    raise CaseFailure("unknown", label, "case budget exhausted")
                if out_path.stat().st_size + err_path.stat().st_size > limit:
                    raise CaseFailure("unknown", label, f"streams exceeded their {limit} byte budget")
                guard()
                time.sleep(POLL_SECONDS)
            if STOP.is_set():
                raise CaseFailure("unknown", "interrupted", "the gate was stopped by a signal")
            guard()
        finally:
            # SIGTERM lets the supervisor kill the command's group before it exits.
            try:
                os.killpg(child.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            # The supervisor stays unreaped until it leaves the registry, so an
            # interrupt never signals a reused process-group ID.
            settle = time.monotonic() + SUPERVISOR_SECONDS
            while os.waitid(os.P_PID, child.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT) is None and time.monotonic() < settle:
                time.sleep(POLL_SECONDS)
            try:
                os.killpg(child.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            with ACTIVE_LOCK:
                ACTIVE.discard(child.pid)
            child.wait()
    if out_path.stat().st_size + err_path.stat().st_size > limit:
        raise CaseFailure("unknown", label, f"streams exceeded their {limit} byte budget")
    return child.returncode, "" if stdout_path else out_path.read_text(errors="replace")


def checkpoint(deadline: float) -> None:
    if STOP.is_set():
        raise CaseFailure("unknown", "interrupted", "the gate was stopped by a signal")
    if time.monotonic() >= deadline:
        raise CaseFailure("unknown", "budget", "case budget exhausted")
