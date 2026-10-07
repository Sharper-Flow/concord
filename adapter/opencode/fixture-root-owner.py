#!/usr/bin/env python3
"""External fixture-run owner for adapter bun:test suites.

A bun:test file hook is not process shutdown, a grace timer is not drainage
proof, and the Bun runtime reaps children independently of any in-process JS
wait, so one plain Python process (standard library only) owns each fixture
suite run instead. This owner runs OUTSIDE bun:test, installs
PR_SET_CHILD_SUBREAPER before starting any descendant, allocates exactly one
short-lived private run root recorded with an ownership marker and nonce, and
launches the assertion-bearing suite as a child `bun test` process.

Removal happens only after the inner Bun process can no longer write AND the
owner's entire owned descendant tree is dead and reaped. The completion
boundary is the kernel's own: living children come from
/proc/self/task/<tid>/children (the kernel child list, which as subreaper
includes adopted orphans), reaping uses os.waitpid, and os.waitpid raising
ChildProcessError (ECHILD) — the kernel stating no children remain — is the
only fact that authorizes removal. A bounded round count or a deadline never
does. Syscall or child-list errors fail closed: the root is kept and a
removal_error is printed. No PID is ever signalled except one the kernel
listed as this owner's own child, so a reused PID or an unrelated process is
never signalled, and no process group is ever signalled: a child's PID says
nothing about who else joined its group.

One lifecycle owns the whole run: signal handling is installed before the
first allocation or spawn, the exact root is registered the moment it exists,
a startup failure (no executable, failed spawn) drains any started child and
removes that exact root, and a SIGINT/SIGTERM arriving during drain or
removal still decides this owner's exit status. A closed stdout/stderr pipe
(the launcher side dying) never aborts cleanup: pipe-write failures are
dropped, while every removal or drain failure stays visible and fails the
run.

Cancellation: the launcher keeps this owner's stdin open; EOF on it (the
launcher finishing abnormally or dying, including SIGKILL) requests
cancellation. SIGINT and SIGTERM do the same. On cancellation the owner stops
the inner Bun process, drains and reaps every owned descendant without
killing itself prematurely, then removes the exact root.

Exit status: the inner run's status is preserved — its exit code, or the
conventional 130/143 for the SIGINT/SIGTERM paths. A removal failure after an
otherwise passing inner run exits nonzero (91) and prints removal_error; a
prior inner failure or signal keeps its status with the removal error still
visible. A startup failure exits nonzero (92) after cleaning the exact root.
The inner run's assertion output passes straight through this owner's
inherited stdout/stderr, and every cleanup step is journalled as an
`@@concord-owner {json}` line on stderr, outside the disposable root.

Owner death by SIGKILL or OOM can run no cleanup at all: the leftover run
root stays, identifiable by its ownership marker. This program reclaims
nothing: it scans no process table, signals no process it did not spawn or
adopt, and removes no root from another run. Containment of a SIGKILL
leftover is test machinery only (fixture-owner-adopt-probe.py), never a
shipped recovery mode.
"""
from __future__ import annotations

import ctypes
import errno
import json
import os
import secrets
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from pathlib import Path

PR_SET_CHILD_SUBREAPER = 36
OWNED_ROOT_MARKER = ".concord-owned-fixture-root.json"
ROOT_PREFIX = "cfx-"  # short: fixture Unix socket paths must stay under 108 chars
TERM_GRACE_S = 0.8
STOP_GRACE_S = 2.0
POLL_S = 0.02
CANCEL_POLL_S = 0.05
REMOVAL_ERROR_EXIT = 91
STARTUP_ERROR_EXIT = 92
CANCEL_EOF_EXIT = 125

_libc = ctypes.CDLL(None, use_errno=True)


def _silence_broken_pipe() -> None:
    # The read end of an output pipe is gone (the launcher side died). Point
    # both standard streams at devnull so later journalling and interpreter
    # shutdown stay quiet instead of failing every write; the cleanup path
    # itself keeps running and its failures still decide the exit status.
    try:
        devnull = os.open(os.devnull, os.O_WRONLY)
        os.dup2(devnull, 1)
        os.dup2(devnull, 2)
        os.close(devnull)
    except OSError:
        pass


def write_stderr(text: str) -> None:
    # Best-effort stderr, immune only to the demonstrated pipe failure: a
    # closed pipe must not abort cleanup. Any other I/O error propagates.
    try:
        sys.stderr.write(text)
        sys.stderr.flush()
    except BrokenPipeError:
        _silence_broken_pipe()
    except OSError as error:
        if error.errno in (errno.EPIPE, errno.EBADF):
            _silence_broken_pipe()
        else:
            raise


def emit(event: dict) -> None:
    # Owner events ride stderr beside the forwarded inner output, never into
    # the disposable root.
    write_stderr(f"@@concord-owner {json.dumps(event, sort_keys=True)}\n")


def become_subreaper() -> None:
    if _libc.prctl(PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) != 0:
        err = ctypes.get_errno()
        raise OSError(err, os.strerror(err))


def living_children() -> list[int]:
    # The kernel's own child list for every thread of this owner. As a
    # subreaper this includes orphaned descendants the kernel adopted. A task
    # entry that vanishes mid-read is transient; any other unreadable child
    # list is a hard, fail-closed error — an inaccessible list must never
    # read as "no children".
    pids: set[int] = set()
    for tid in os.listdir("/proc/self/task"):
        try:
            text = Path(f"/proc/self/task/{tid}/children").read_text()
        except FileNotFoundError:
            continue
        except OSError as error:
            raise OSError(error.errno, f"unreadable kernel child list for task {tid}: {error}")
        pids.update(int(token) for token in text.split())
    return sorted(pids)


def kill_owned(pid: int, sig: int) -> None:
    # Only PIDs the kernel listed as this owner's own child. Group membership
    # is not ownership: the child's PID proves nothing about the other
    # processes in its group, so no group signal is ever sent, and no PID is
    # guessed. ESRCH answers a child that exited between listing and signal.
    try:
        os.kill(pid, sig)
    except (ProcessLookupError, PermissionError):
        pass


def reap(pid: int, block: bool) -> bool:
    """True when pid was reaped or is already gone; False while it lives."""
    try:
        got, _ = os.waitpid(pid, 0 if block else os.WNOHANG)
    except ChildProcessError:
        return True
    return got == pid


def drain_owned(reason: str) -> dict:
    """Kill and reap the entire owned tree. Returns only at the kernel's
    ECHILD boundary; a round count or deadline never authorizes return."""
    report = {"reason": reason, "signalled": [], "killed": [], "reaped": []}
    while True:
        kids = living_children()
        if not kids:
            try:
                got, _ = os.waitpid(-1, os.WNOHANG)
            except ChildProcessError:
                return report  # ECHILD: the kernel says no children remain.
            if got != 0:
                continue  # A zombie was reaped; recheck the living list.
            time.sleep(POLL_S)
            continue
        report["signalled"].extend(kids)
        for pid in kids:
            kill_owned(pid, signal.SIGTERM)
        deadline = time.monotonic() + TERM_GRACE_S
        pending = set(kids)
        while pending and time.monotonic() < deadline:
            time.sleep(POLL_S)
            for pid in sorted(pending):
                if reap(pid, block=False):
                    pending.discard(pid)
                    report["reaped"].append(pid)
        report["killed"].extend(sorted(pending))
        for pid in sorted(pending):
            kill_owned(pid, signal.SIGKILL)
        for pid in sorted(pending):
            if reap(pid, block=True):
                report["reaped"].append(pid)


def stop_inner(inner: subprocess.Popen, sig: int) -> int:
    """Stop the inner bun process; return its conventional status."""
    if inner.poll() is None:
        try:
            inner.send_signal(sig)
        except ProcessLookupError:
            pass
        deadline = time.monotonic() + STOP_GRACE_S
        while inner.poll() is None and time.monotonic() < deadline:
            time.sleep(POLL_S)
        if inner.poll() is None:
            try:
                inner.kill()
            except ProcessLookupError:
                pass
    code = inner.wait()
    if code is not None and code >= 0:
        return code
    return 128 + (-code if code is not None else 0)


def conventional_status(inner: subprocess.Popen) -> int:
    code = inner.returncode if inner.returncode is not None else inner.wait()
    if code >= 0:
        return code
    return 128 + (-code)


def remove_root(root: str) -> str | None:
    """Remove the exact run root; None on success, an error message else."""
    emit({"kind": "cleanup_entry", "root": root})
    emit({"kind": "removal_entry", "root": root})
    try:
        shutil.rmtree(root)
        if os.path.exists(root):
            raise OSError(f"root still present after removal: {root}")
    except OSError as error:
        message = f"{type(error).__name__}: {error}"
        emit({"kind": "removal_error", "root": root, "detail": message})
        write_stderr(f"removal_error {root}: {message}\n")
        return message
    emit({"kind": "completion", "root": root})
    return None


def fail_startup(root: str, detail: str) -> int:
    # The run never started. The exact root still exists and is owned, so it
    # is drained (nothing may have started) and removed here; the failure is
    # journalled and the exit is nonzero whatever cleanup does.
    emit({"kind": "startup_error", "root": root, "detail": detail})
    try:
        report = drain_owned("startup_failure")
        emit({"kind": "drain_complete", "root": root, **report})
    except OSError as error:
        message = f"{type(error).__name__}: {error}"
        emit({"kind": "removal_error", "root": root, "detail": f"drain failed closed: {message}"})
        write_stderr(f"removal_error {root}: drain failed closed: {message}\n")
        return STARTUP_ERROR_EXIT
    remove_root(root)
    return STARTUP_ERROR_EXIT


def run_owned(case: str) -> int:
    become_subreaper()

    cancel: dict = {}

    def request(name: str) -> None:
        cancel["signal"] = name

    # Signal handling is installed BEFORE the first allocation or spawn, and
    # it stays installed through drain and removal: a SIGINT/SIGTERM arriving
    # at any point — startup included — still decides this owner's exit
    # status.
    signal.signal(signal.SIGINT, lambda *_: request("SIGINT"))
    signal.signal(signal.SIGTERM, lambda *_: request("SIGTERM"))
    # EOF watch without a reader thread: a daemon thread blocked in read()
    # aborts interpreter finalization once this owner exits normally, so the
    # stdin pipe is polled non-blockingly from the wait loop instead.
    stdin_fd = sys.stdin.fileno()
    try:
        os.set_blocking(stdin_fd, False)
    except OSError:
        pass

    root = tempfile.mkdtemp(prefix=ROOT_PREFIX)
    nonce = secrets.token_hex(8)
    marker = {
        "owner": "concord-adapter-fixture-run-owner",
        "nonce": nonce,
        "owner_pid": os.getpid(),
        "case": case,
        "allocated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }
    Path(root, OWNED_ROOT_MARKER).write_text(json.dumps(marker, indent=2) + "\n")
    # The exact root is registered the moment it exists, before anything is
    # resolved or spawned: from here on, cleanup owns one exact path.
    emit({"kind": "allocated_root", "root": root, "nonce": nonce})

    bun = os.environ.get("CONCORD_OWNER_BUN") or shutil.which("bun")
    if not bun:
        return fail_startup(root, "no bun executable for the inner run")
    try:
        inner = subprocess.Popen(
            [bun, "test", os.path.abspath(case)],
            env={**os.environ, "CONCORD_FIXTURE_RUN_ROOT": root, "CONCORD_FIXTURE_RUN_NONCE": nonce},
            stdin=subprocess.DEVNULL,
        )
    except OSError as error:
        return fail_startup(root, f"inner spawn failed: {type(error).__name__}: {error}")
    emit({"kind": "inner_started", "root": root, "inner_pid": inner.pid})

    eof = False
    while inner.poll() is None and "signal" not in cancel and not eof:
        try:
            if os.read(stdin_fd, 4096) == b"":
                eof = True  # The launcher finished or died: cancellation.
        except BlockingIOError:
            pass  # No data and no EOF yet: the launcher still holds the pipe.
        except OSError:
            eof = True
        time.sleep(CANCEL_POLL_S)

    cancelled = cancel.get("signal") or ("eof" if eof else None)
    if cancelled:
        stop_sig = signal.SIGINT if cancelled == "SIGINT" else signal.SIGTERM
        inner_status = stop_inner(inner, stop_sig)
        emit({"kind": "cancelled", "via": cancelled, "inner_status": inner_status})
    else:
        inner_status = conventional_status(inner)
    emit({"kind": "child_exit", "root": root, "status": inner_status})

    try:
        report = drain_owned(f"cancel_{cancelled}" if cancelled else "run_end")
        emit({"kind": "drain_complete", "root": root, **report})
    except OSError as error:
        message = f"{type(error).__name__}: {error}"
        emit({"kind": "removal_error", "root": root, "detail": f"drain failed closed: {message}"})
        write_stderr(f"removal_error {root}: drain failed closed: {message}\n")
        return inner_status if inner_status != 0 else REMOVAL_ERROR_EXIT

    if remove_root(root) is not None:
        return inner_status if inner_status != 0 else REMOVAL_ERROR_EXIT
    if cancelled:
        return {"SIGINT": 130, "SIGTERM": 143, "eof": CANCEL_EOF_EXIT}[cancelled]
    late = cancel.get("signal")
    if late:
        # A SIGINT/SIGTERM that arrived during drain or removal keeps its
        # conventional status; the cleanup itself already completed.
        return {"SIGINT": 130, "SIGTERM": 143}[late]
    return inner_status


def main(argv: list[str]) -> int:
    # Exactly one form is accepted: owning one case file. There is no
    # recovery route, no scanner mode, and no flag at all — anything that
    # starts with "--" is a usage error that touches no root and no process.
    if len(argv) != 1 or argv[0].startswith("--"):
        write_stderr("usage: fixture-root-owner.py CASE_FILE\n")
        return 2
    return run_owned(argv[0])


if __name__ == "__main__":
    try:
        code = main(sys.argv[1:])
    except Exception as error:  # Fail closed on any unexpected fault.
        try:
            write_stderr(f"owner_error {type(error).__name__}: {error}\n")
        except OSError:
            pass
        code = REMOVAL_ERROR_EXIT
    try:
        sys.stdout.flush()
        sys.stderr.flush()
    except OSError:
        _silence_broken_pipe()
    sys.exit(code)
