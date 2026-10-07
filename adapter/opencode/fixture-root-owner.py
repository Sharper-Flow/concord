#!/usr/bin/env python3
"""External fixture-run owner for adapter bun:test suites.

The bound no_ship review rejected in-process root ownership: a bun:test file
hook is not process shutdown, a grace timer is not drainage proof, and the Bun
runtime reaps children independently of any in-process JS wait. This owner is
the single owner instead. It runs OUTSIDE bun:test as a plain Python process
(standard library only), installs PR_SET_CHILD_SUBREAPER before starting any
descendant, allocates exactly one short-lived private run root recorded with
an ownership marker and nonce, and launches the assertion-bearing suite as a
child `bun test` process.

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
never signalled; a process group is signalled only through the pgid a listed
child itself leads (ESRCH otherwise), never by guessing.

Cancellation: the launcher keeps this owner's stdin open; EOF on it (the
launcher finishing abnormally or dying, including SIGKILL) requests
cancellation. SIGINT and SIGTERM do the same. On cancellation the owner stops
the inner Bun process, drains and reaps every owned descendant without
killing itself prematurely, then removes the exact root.

Exit status: the inner run's status is preserved — its exit code, or the
conventional 130/143 for the SIGINT/SIGTERM paths. A removal failure after an
otherwise passing inner run exits nonzero (91) and prints removal_error; a
prior inner failure or signal keeps its status with the removal error still
visible. The inner run's assertion output passes straight through this
owner's inherited stdout/stderr, and every cleanup step is journalled as an
`@@concord-owner {json}` line on stderr, outside the disposable root.

Owner death by SIGKILL or OOM can run no cleanup at all: the leftover run
root stays, identifiable by its ownership marker, and is confined only by the
explicit recovery mode (--recover ROOT NONCE), which signals only processes
whose /proc/<pid>/environ carries this run's nonce and then removes exactly
that root.
"""
from __future__ import annotations

import ctypes
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
CANCEL_EOF_EXIT = 125

_libc = ctypes.CDLL(None, use_errno=True)


def emit(event: dict) -> None:
    # Owner events ride stderr beside the forwarded inner output, never into
    # the disposable root.
    sys.stderr.write(f"@@concord-owner {json.dumps(event, sort_keys=True)}\n")
    sys.stderr.flush()


def become_subreaper() -> None:
    if _libc.prctl(PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) != 0:
        err = ctypes.get_errno()
        raise OSError(err, os.strerror(err))


def living_children() -> list[int]:
    # The kernel's own child list for every thread of this owner. As a
    # subreaper this includes orphaned descendants the kernel adopted. A task
    # entry that vanishes mid-read is transient; a missing /proc/self/task is
    # a hard, fail-closed error.
    tasks = os.listdir("/proc/self/task")
    pids: set[int] = set()
    for tid in tasks:
        try:
            text = Path(f"/proc/self/task/{tid}/children").read_text()
        except OSError:
            continue
        pids.update(int(token) for token in text.split())
    return sorted(pids)


def kill_owned(pid: int, sig: int) -> None:
    # Only PIDs the kernel listed as this owner's own child. The group form
    # names at most the group this child itself leads; ESRCH answers any
    # guess. Never a wildcard, never an unrelated process.
    try:
        os.kill(pid, sig)
    except (ProcessLookupError, PermissionError):
        pass
    try:
        os.killpg(pid, sig)
    except (ProcessLookupError, PermissionError, OSError):
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
        sys.stderr.write(f"removal_error {root}: {message}\n")
        sys.stderr.flush()
        return message
    emit({"kind": "completion", "root": root})
    return None


def run_owned(case: str) -> int:
    become_subreaper()
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

    bun = os.environ.get("CONCORD_OWNER_BUN") or shutil.which("bun")
    if not bun:
        emit({"kind": "removal_error", "root": root, "detail": "no bun executable for the inner run"})
        return REMOVAL_ERROR_EXIT
    inner = subprocess.Popen(
        [bun, "test", os.path.abspath(case)],
        env={**os.environ, "CONCORD_FIXTURE_RUN_ROOT": root, "CONCORD_FIXTURE_RUN_NONCE": nonce},
        stdin=subprocess.DEVNULL,
    )
    emit({"kind": "allocated_root", "root": root, "nonce": nonce, "inner_pid": inner.pid})

    cancel: dict = {}

    def request(name: str) -> None:
        cancel["signal"] = name

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
        sys.stderr.write(f"removal_error {root}: drain failed closed: {message}\n")
        sys.stderr.flush()
        return inner_status if inner_status != 0 else REMOVAL_ERROR_EXIT

    if remove_root(root) is None:
        if cancelled:
            return {"SIGINT": 130, "SIGTERM": 143, "eof": CANCEL_EOF_EXIT}[cancelled]
        return inner_status
    return inner_status if inner_status != 0 else REMOVAL_ERROR_EXIT


def nonce_processes(nonce: str) -> list[int]:
    # Exact ownership for recovery: a process belongs to this run only if its
    # own environment carries this run's nonce. A reused PID never does.
    owned = []
    for entry in os.listdir("/proc"):
        if not entry.isdigit():
            continue
        pid = int(entry)
        if pid == os.getpid():
            continue
        try:
            environ = Path(f"/proc/{pid}/environ").read_bytes()
        except OSError:
            continue
        if f"CONCORD_FIXTURE_RUN_NONCE={nonce}\0".encode() in environ:
            owned.append(pid)
    return sorted(owned)


def recover(root: str, nonce: str) -> int:
    marker_path = Path(root, OWNED_ROOT_MARKER)
    try:
        marker = json.loads(marker_path.read_text())
    except (OSError, ValueError) as error:
        sys.stderr.write(f"recovery_error {root}: unreadable marker: {error}\n")
        return 1
    if marker.get("nonce") != nonce:
        sys.stderr.write(f"recovery_error {root}: nonce mismatch; refusing to touch this root\n")
        return 1
    owned = nonce_processes(nonce)
    emit({"kind": "recovery_entry", "root": root, "processes": owned})
    for pid in owned:
        try:
            os.kill(pid, signal.SIGTERM)
        except (ProcessLookupError, PermissionError):
            pass
    deadline = time.monotonic() + STOP_GRACE_S
    alive = set(owned)
    while alive and time.monotonic() < deadline:
        time.sleep(POLL_S)
        for pid in sorted(alive):
            # The recovery owner is not these processes' parent, so it cannot
            # reap them; it can only observe death and let init reap.
            try:
                os.kill(pid, 0)
            except (ProcessLookupError, PermissionError):
                alive.discard(pid)
    for pid in sorted(alive):
        try:
            os.kill(pid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass
    deadline = time.monotonic() + STOP_GRACE_S
    while alive and time.monotonic() < deadline:
        time.sleep(POLL_S)
        for pid in sorted(alive):
            try:
                os.kill(pid, 0)
            except (ProcessLookupError, PermissionError):
                alive.discard(pid)
    return 0 if remove_root(root) is None else 1


def main(argv: list[str]) -> int:
    if argv[:1] == ["--recover"]:
        if len(argv) != 3:
            sys.stderr.write("usage: fixture-root-owner.py --recover ROOT NONCE\n")
            return 2
        return recover(argv[1], argv[2])
    if len(argv) != 1:
        sys.stderr.write("usage: fixture-root-owner.py CASE_FILE | --recover ROOT NONCE\n")
        return 2
    return run_owned(argv[0])


if __name__ == "__main__":
    try:
        code = main(sys.argv[1:])
    except Exception as error:  # Fail closed on any unexpected fault.
        sys.stderr.write(f"owner_error {type(error).__name__}: {error}\n")
        code = REMOVAL_ERROR_EXIT
    sys.stderr.flush()
    sys.exit(code)
