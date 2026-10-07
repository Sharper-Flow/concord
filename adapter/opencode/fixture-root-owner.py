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

One lifecycle owns the whole run. Signal handling is installed before the
first allocation or spawn, the root becomes owned state the moment mkdtemp
returns — before nonce generation, registration, marker persistence, or any
other fallible operation — and every post-allocation failure (nonce
generation, registration, marker persistence, executable resolution, the
spawn, a journal write, a poll, a wait, a termination) records itself on the
same lifecycle state and reaches the same termination/drain/removal path:
there is no separate startup cleanup branch, and no failure path skips the
drain. A started child is stopped and its entire kernel-owned tree
adopted/reaped before removal. Journal failures are captured separately and
never interrupt stop, drain, or removal: reporting is an observer of cleanup,
never control flow for it, and no reporter extends or re-reports itself. If
kernel drainage proof itself fails, the root is kept, the error is reported,
and the run fails; removal never happens on a timeout or round count. A
closed stdout/stderr pipe (the launcher side dying) never aborts cleanup:
pipe-write failures are dropped, while every removal or drain failure stays
visible and fails the run.

Cancellation: the launcher keeps this owner's stdin open; EOF on it (the
launcher finishing abnormally or dying, including SIGKILL) requests
cancellation. SIGINT and SIGTERM do the same. On cancellation the owner stops
the inner Bun process, drains and reaps every owned descendant without
killing itself prematurely, then removes the exact root.

Exit status is decided once, after lifecycle cleanup, from the recorded
state alone: a captured SIGINT/SIGTERM — whenever it was captured, including
during failed executable resolution, drain, or removal — keeps its
conventional 130/143, with the failure diagnostics journalled beside it; an
EOF cancellation keeps 125. Below cancellation, a recorded nonzero inner
status is preserved. Otherwise a startup failure (no executable, failed
spawn, failed marker persistence) exits nonzero (92), and an owner, journal,
or cleanup failure exits nonzero (91). Faults are reported after cleanup,
including journal failures during removal and completion.
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
SIGNAL_EXIT = {"SIGINT": 130, "SIGTERM": 143}

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


def remove_root(run: OwnedRun) -> str | None:
    """Remove the exact run root; None on success, an error message else.

    The cleanup and removal entries are journalled as observers, each
    independently of the deletion itself: a journal failure is retained and
    fails the run, but it can never skip the removal the kernel's drainage
    proof already authorized."""
    root = run.root
    journal(run, {"kind": "cleanup_entry", "root": root})
    journal(run, {"kind": "removal_entry", "root": root})
    try:
        shutil.rmtree(root)
        if os.path.exists(root):
            raise OSError(f"root still present after removal: {root}")
    except OSError as error:
        message = f"{type(error).__name__}: {error}"
        run.cleanup_failed = True
        journal(run, {"kind": "removal_error", "root": root, "detail": message})
        report_line(run, f"removal_error {root}: {message}\n")
        return message
    journal(run, {"kind": "completion", "root": root})
    return None


class OwnedRun:
    """One owned run's state, from allocation to removal.

    Holding the exact root, the started inner child, and every failure fact
    in one object is what makes the lifecycle cohesive: each failure records
    itself here, and one finish() path plus one decide() call serve every
    case — success, inner failure, cancellation, startup fault, journal
    fault, drain failure, removal failure — with no parallel cleanup branch.
    """

    def __init__(self, case: str) -> None:
        self.case = case
        self.root: str | None = None
        self.nonce: str | None = None
        self.inner: subprocess.Popen | None = None
        self.inner_status: int | None = None
        self.captured_signal: str | None = None  # SIGINT/SIGTERM, whenever captured
        self.cancelled_by: str | None = None  # what ended the inner wait
        self.startup_detail: str | None = None  # the run never started
        self.owner_faults: list[str] = []  # visible, run-failing owner faults
        # Journal-channel failures, kept OUT of owner_faults: the reporter
        # must never be able to extend the fault list finish() iterates, and a
        # reporting failure is not the same fact as the fault it was reporting.
        self.journal_faults: list[str] = []
        self.cleanup_failed = False

    def request_cancel(self, name: str) -> None:
        self.captured_signal = name


def journal(run: OwnedRun, event: dict) -> None:
    """Journal one lifecycle event. Reporting is an observer of cleanup,
    never control flow for it: a journal failure — a closed pipe is silenced
    inside write_stderr; anything else is captured here — is retained
    separately on the lifecycle and fails the run, but it never interrupts
    stop, drain, or removal, and it is never appended to owner_faults, whose
    iteration must stay finite."""
    try:
        emit(event)
    except OSError as error:
        run.journal_faults.append(f"journal {event.get('kind')} failed: {type(error).__name__}: {error}")


def report_line(run: OwnedRun, text: str) -> None:
    """Write one visible stderr line. Reporting is an observer here too: the
    line's failure is retained beside the journal faults and never aborts the
    cleanup step that is reporting itself."""
    try:
        write_stderr(text)
    except OSError as error:
        run.journal_faults.append(f"report line failed: {type(error).__name__}: {error}")


def start(run: OwnedRun, stdin_fd: int) -> None:
    """Take the run from its registered root to a finished inner run.

    Every failure here — marker persistence, executable resolution, the
    spawn, or the journal — is recorded on the lifecycle and returns, so
    finish() owns every path; nothing escapes to a parallel cleanup.
    """
    marker = {
        "owner": "concord-adapter-fixture-run-owner",
        "nonce": run.nonce,
        "owner_pid": os.getpid(),
        "case": run.case,
        "allocated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }
    try:
        Path(run.root, OWNED_ROOT_MARKER).write_text(json.dumps(marker, indent=2) + "\n")
    except OSError as error:
        run.startup_detail = f"marker persistence failed: {type(error).__name__}: {error}"
        return
    if run.journal_faults:
        # A journal already failed (the allocated_root emit itself): fail the
        # run here rather than start an unobservable inner run.
        return
    bun = os.environ.get("CONCORD_OWNER_BUN") or shutil.which("bun")
    if not bun:
        run.startup_detail = "no bun executable for the inner run"
        return
    try:
        run.inner = subprocess.Popen(
            [bun, "test", os.path.abspath(run.case)],
            env={**os.environ, "CONCORD_FIXTURE_RUN_ROOT": run.root, "CONCORD_FIXTURE_RUN_NONCE": run.nonce},
            stdin=subprocess.DEVNULL,
        )
    except OSError as error:
        run.startup_detail = f"inner spawn failed: {type(error).__name__}: {error}"
        return
    journal(run, {"kind": "inner_started", "root": run.root, "inner_pid": run.inner.pid})
    if run.journal_faults:
        # The journal broke after the spawn: stop and clean now, visibly,
        # instead of running an unobservable inner run.
        return
    await_inner(run, stdin_fd)


def await_inner(run: OwnedRun, stdin_fd: int) -> None:
    """Wait for the inner run to end on its own, by captured cancellation
    signal, or by launcher EOF (the stdin pipe closing)."""
    eof = False
    inner = run.inner
    while inner.poll() is None and run.captured_signal is None and not eof:
        try:
            if os.read(stdin_fd, 4096) == b"":
                eof = True  # The launcher finished or died: cancellation.
        except BlockingIOError:
            pass  # No data and no EOF yet: the launcher still holds the pipe.
        except OSError:
            eof = True
        time.sleep(CANCEL_POLL_S)
    if run.captured_signal:
        run.cancelled_by = run.captured_signal
        stop = signal.SIGINT if run.captured_signal == "SIGINT" else signal.SIGTERM
        run.inner_status = stop_inner(inner, stop)
    elif eof:
        run.cancelled_by = "eof"
        run.inner_status = stop_inner(inner, signal.SIGTERM)
    else:
        run.inner_status = conventional_status(inner)


def finish(run: OwnedRun) -> None:
    """The one termination/drain/removal path every run ends in.

    No step in here may raise past the lifecycle: an inner termination
    failure, a poll or wait failure, a journal failure, a failed drainage
    proof, and a failed removal each record themselves on the lifecycle and
    let the remaining steps still run — a termination error never assumes the
    child dead, so the kernel-only drainage still establishes ECHILD before
    any removal. Reporting and the final decision follow this lifecycle."""
    # Stop a still-live inner child first — a fault path may have left it
    # running — so nothing owned outlives the drain boundary.
    if run.inner is not None:
        try:
            if run.inner.poll() is None:
                stop = signal.SIGINT if run.captured_signal == "SIGINT" else signal.SIGTERM
                run.inner_status = stop_inner(run.inner, stop)
        except Exception as error:
            run.owner_faults.append(f"inner termination failed: {type(error).__name__}: {error}")
    if run.captured_signal or run.cancelled_by == "eof":
        journal(run, {"kind": "cancelled", "via": run.captured_signal or run.cancelled_by,
                      "inner_status": run.inner_status})
    if run.inner is not None:
        journal(run, {"kind": "child_exit", "root": run.root, "status": run.inner_status})
    if run.startup_detail is not None:
        journal(run, {"kind": "startup_error", "root": run.root, "detail": run.startup_detail})
    if run.cancelled_by:
        reason = f"cancel_{run.cancelled_by}"
    elif run.startup_detail is not None:
        reason = "startup_failure"
    elif run.owner_faults or run.journal_faults:
        reason = "owner_fault"
    else:
        reason = "run_end"
    try:
        report = drain_owned(reason)
    except OSError as error:
        message = f"{type(error).__name__}: {error}"
        run.cleanup_failed = True
        journal(run, {"kind": "removal_error", "root": run.root,
                      "detail": f"drain failed closed: {message}"})
        report_line(run, f"removal_error {run.root}: drain failed closed: {message}\n")
        # The root is kept: no removal without the kernel's drainage proof.
        return
    journal(run, {"kind": "drain_complete", "root": run.root, **report})
    remove_root(run)


def report_faults(run: OwnedRun) -> None:
    """Report faults once after cleanup, including its last journal write.

    Snapshots keep reporting finite. If the journal remains inaccessible,
    reporting its own failure cannot restore it; retain the failure in state
    for the exit decision without recursively reporting it.
    """
    for fault in list(run.owner_faults):
        journal(run, {"kind": "owner_fault", "root": run.root, "detail": fault})
    for fault in list(run.journal_faults):
        try:
            emit({"kind": "owner_fault", "root": run.root, "detail": fault})
        except OSError:
            pass


def decide(run: OwnedRun) -> int:
    """The one exit-status decision, made once, after lifecycle cleanup.

    A captured SIGINT/SIGTERM — whenever it was captured: startup,
    resolution, the run, drain, or removal — keeps its conventional status;
    neither a startup fault nor a drain or removal failure overrides it, and
    the diagnostics stay journalled beside it. An EOF cancellation keeps its
    own cancellation status. Below cancellation, a recorded nonzero inner
    status wins over every later fault. Without an inner failure, a startup
    fault exits 92; an owner, journal, or cleanup fault exits 91.
    """
    if run.captured_signal in SIGNAL_EXIT:
        return SIGNAL_EXIT[run.captured_signal]
    if run.cancelled_by == "eof":
        return CANCEL_EOF_EXIT
    if run.inner_status not in (None, 0):
        return run.inner_status
    if run.startup_detail is not None:
        return STARTUP_ERROR_EXIT
    if run.owner_faults or run.journal_faults or run.cleanup_failed:
        return REMOVAL_ERROR_EXIT
    return 0


def run_owned(case: str) -> int:
    become_subreaper()

    run = OwnedRun(case)
    # Signal handling is installed BEFORE the first allocation or spawn, and
    # it stays installed through drain and removal: a SIGINT/SIGTERM arriving
    # at any point — startup included — still decides this owner's exit
    # status.
    signal.signal(signal.SIGINT, lambda *_: run.request_cancel("SIGINT"))
    signal.signal(signal.SIGTERM, lambda *_: run.request_cancel("SIGTERM"))
    # EOF watch without a reader thread: a daemon thread blocked in read()
    # aborts interpreter finalization once this owner exits normally, so the
    # stdin pipe is polled non-blockingly from the wait loop instead.
    stdin_fd = sys.stdin.fileno()
    try:
        os.set_blocking(stdin_fd, False)
    except OSError:
        pass

    # The root is owned state from the moment mkdtemp returns. Everything
    # fallible after that — nonce generation, registration, marker
    # persistence, the spawn, the wait — happens inside this guarded region,
    # records itself on the lifecycle, and still reaches the one finish()
    # path; nothing between allocation and removal may escape it.
    run.root = tempfile.mkdtemp(prefix=ROOT_PREFIX)
    try:
        run.nonce = secrets.token_hex(8)
        # The exact root is registered before the marker or any other
        # fallible I/O, so cleanup owns one exact path from here on.
        journal(run, {"kind": "allocated_root", "root": run.root, "nonce": run.nonce})
        start(run, stdin_fd)
    except Exception as error:
        # No post-allocation failure bypasses the lifecycle: an unexpected
        # fault is recorded — visibly, failing the run — and finish() still
        # drains and removes.
        run.owner_faults.append(f"owner fault: {type(error).__name__}: {error}")
    try:
        finish(run)
    except Exception as error:
        # Unreachable by design — every finish step records instead of
        # raising — but a lifecycle bug still fails closed through the same
        # one decision instead of a script-level fallback status.
        run.owner_faults.append(f"finish fault: {type(error).__name__}: {error}")
    report_faults(run)
    return decide(run)


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
