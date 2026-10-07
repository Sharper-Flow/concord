#!/usr/bin/env python3
"""Deterministic fault-injection probes for the owner lifecycle.

Permanent machinery for fixture_owner.test.ts, beside the real-run matrix.
The faults probed here — marker persistence failure, the post-spawn journal
failure, persistent journal ENOSPC, journal failures inside cleanup itself
(cleanup_entry, removal_entry, completion), nonce generation failing after
the root exists, poll/stop/wait failures with and without a captured SIGINT or
SIGTERM, and a SIGINT/SIGTERM captured while executable resolution,
drainage, or removal is failing — cannot be timed against a real child run,
so each probe drives the owner's own run_owned() in this process with the
kernel boundary stubbed: no real child is spawned, no real signal is sent,
and the captured signal handler is invoked directly instead. Each probe
allocates one real directory as the exact owned root inside a confined
temporary parent that this probe removes itself, and nothing else is
touched. Every probe must terminate on its own: finiteness here is the
assertion that the lifecycle's reporting can never grow what it iterates.

Every probe must observe the same owner contract the real runs do: the exact
root is registered before the marker, every post-allocation failure drains
any started child and removes the exact root (a failed drainage proof or a
failed removal keeps it), a captured signal keeps its conventional 130/143
over startup, drain, and removal failures, and each failure stays journalled
and visible. One JSON object per probe is printed on stdout for the driving
test to assert on.
"""
from __future__ import annotations

import errno
import importlib.util
import io
import json
import signal
import tempfile
from contextlib import ExitStack, redirect_stderr
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

OWNER_SOURCE = Path(__file__).with_name("fixture-root-owner.py")
_spec = importlib.util.spec_from_file_location("fixture_root_owner", OWNER_SOURCE)
owner = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(owner)


def run_probe(label: str, *, marker: bool = False, journal: bool = False,
              which_missing: bool = False, rmtree_fails: bool = False,
              drain_fails: bool = False, signal_name: str | None = None,
              nonce_fails: bool = False, stop_fails: bool = False,
              poll_fails: bool = False, fail_kinds: tuple[str, ...] = (),
              journal_fails_from: str | None = None,
              fail_signal: str | None = None, stop_signal: str | None = None,
              poll_signal: str | None = None, wait_fails: bool = False,
              wait_signal: str | None = None, inner_status: int = 0,
              finish_poll_fails: bool = False) -> dict:
    """Run one fault probe. The fault arguments:

    marker              fault the marker write (ENOSPC)
    journal             fault the inner_started journal emit (ENOSPC) after spawn
    which_missing       resolve no bun executable (startup failure)
    rmtree_fails        fault the root removal (EACCES)
    drain_fails         fault the kernel child list (EACCES) during drainage
    signal_name         when set, the captured SIGINT/SIGTERM handler fires from
                        inside the faulting operation, as a signal that raced it
    nonce_fails         fault nonce generation (ENOSPC) after the root exists
    stop_fails          fault stop_inner (EPERM); stop_signal, when set, is
                        captured from inside the failing stop
    poll_fails          fault the inner poll (ESRCH); poll_signal, when set, is
                        captured from inside the failing poll
    fail_kinds          journal kinds that fail persistently (ENOSPC): a
                        cleanup_entry, removal_entry, or completion failure
                        inside cleanup itself
    journal_fails_from  the first journal kind after which every emit fails
                        (persistent journal ENOSPC)
    fail_signal         when set, the signal handler fires from inside the
                        first failing journal emit
    wait_fails          fault the inner wait (EIO); wait_signal, when set, is
                        captured from inside the failing wait
    inner_status        the inner process's returncode, before normalization
    finish_poll_fails   fault only the poll after the inner status was captured
    """
    signum = {"SIGINT": signal.SIGINT, "SIGTERM": signal.SIGTERM}[signal_name] if signal_name else None
    fail_signum = {"SIGINT": signal.SIGINT, "SIGTERM": signal.SIGTERM}[fail_signal] if fail_signal else None
    stop_signum = {"SIGINT": signal.SIGINT, "SIGTERM": signal.SIGTERM}[stop_signal] if stop_signal else None
    poll_signum = {"SIGINT": signal.SIGINT, "SIGTERM": signal.SIGTERM}[poll_signal] if poll_signal else None
    wait_signum = {"SIGINT": signal.SIGINT, "SIGTERM": signal.SIGTERM}[wait_signal] if wait_signal else None
    if journal:
        fail_kinds = (*fail_kinds, "inner_started")
    with tempfile.TemporaryDirectory(prefix="concord-owner-fault-") as parent:
        allocated = Path(parent, "exact-owned-root")
        handlers = {}
        events: list[dict] = []
        failed_events: list[dict] = []
        drains: list[str] = []
        journal_failing = [False]
        signalled = [False]
        stopped = [False]
        polls = [0]

        def allocate(**_kwargs):
            allocated.mkdir()
            return str(allocated)

        def install(signum_installed, handler):
            handlers[signum_installed] = handler

        def capture_signal(num):
            handlers[num](num, None)

        def token_hex(_length):
            raise OSError(errno.ENOSPC, "synthetic entropy exhaustion")

        def inner_poll():
            if poll_signum is not None:
                capture_signal(poll_signum)
            raise OSError(errno.ESRCH, "synthetic poll failure")

        def poll():
            polls[0] += 1
            if poll_fails or (finish_poll_fails and polls[0] > 1):
                return inner_poll()
            return None if stop_fails or (wait_fails and not stopped[0]) else inner_status

        def send_signal(_sig):
            stopped[0] = True

        def wait():
            if wait_fails:
                if wait_signum is not None:
                    capture_signal(wait_signum)
                raise OSError(errno.EIO, "synthetic wait failure")
            return inner_status

        def stop_inner(_inner, _sig):
            if stop_signum is not None:
                capture_signal(stop_signum)
            raise OSError(errno.EPERM, "synthetic termination failure")

        fake_inner = SimpleNamespace(returncode=inner_status, pid=123456,
                                     poll=poll, wait=wait, send_signal=send_signal,
                                     kill=lambda: send_signal(signal.SIGKILL))

        def marker_write(*_args, **_kwargs):
            if signum is not None:
                capture_signal(signum)
            raise OSError(errno.ENOSPC, "synthetic marker disk full")

        def resolve_bun(_name):
            if which_missing:
                if signum is not None:
                    capture_signal(signum)
                return None
            return "/synthetic/bun"

        def emit(event):
            kind = event.get("kind")
            if journal_fails_from is not None and kind == journal_fails_from:
                journal_failing[0] = True
            if kind in fail_kinds or journal_failing[0]:
                failed_events.append(dict(event))
                if fail_signum is not None and not signalled[0]:
                    signalled[0] = True
                    capture_signal(fail_signum)
                raise OSError(errno.ENOSPC, "synthetic journal disk full")
            events.append(event)

        def drain_owned(reason):
            drains.append(reason)
            if drain_fails:
                if signum is not None:
                    capture_signal(signum)
                raise OSError(errno.EACCES, "synthetic inaccessible kernel child list")
            return {"reason": reason, "signalled": [], "killed": [], "reaped": []}

        def remove_tree(_root):
            if signum is not None:
                capture_signal(signum)
            raise PermissionError(errno.EACCES, "synthetic removal denied")

        result: dict = {"probe": label}
        with ExitStack() as stack:
            diagnostics = stack.enter_context(redirect_stderr(io.StringIO()))
            if marker:
                stack.enter_context(mock.patch.object(owner.Path, "write_text", side_effect=marker_write))
            if nonce_fails:
                stack.enter_context(mock.patch.object(owner.secrets, "token_hex", side_effect=token_hex))
            if stop_fails:
                stack.enter_context(mock.patch.object(owner, "stop_inner", side_effect=stop_inner))
            if rmtree_fails:
                stack.enter_context(mock.patch.object(owner.shutil, "rmtree", side_effect=remove_tree))
            stack.enter_context(mock.patch.object(owner, "become_subreaper"))
            stack.enter_context(mock.patch.object(owner.signal, "signal", side_effect=install))
            stack.enter_context(mock.patch.object(owner.os, "set_blocking"))
            stack.enter_context(mock.patch.object(owner.tempfile, "mkdtemp", side_effect=allocate))
            stack.enter_context(mock.patch.object(owner, "emit", side_effect=emit))
            stack.enter_context(mock.patch.object(owner.shutil, "which", side_effect=resolve_bun))
            stack.enter_context(mock.patch.dict(owner.os.environ, {"CONCORD_OWNER_BUN": ""}))
            stack.enter_context(mock.patch.object(owner.subprocess, "Popen", return_value=fake_inner))
            stack.enter_context(mock.patch.object(owner, "drain_owned", side_effect=drain_owned))
            try:
                result["exit"] = owner.run_owned("synthetic.case.ts")
            except OSError as error:
                # The failure escaped the owner lifecycle entirely.
                result["escaped"] = f"{type(error).__name__}: {error}"
        result["root_removed"] = not allocated.exists()
        result["events"] = [event.get("kind") for event in events]
        result["details"] = {event.get("kind"): event.get("detail") or event.get("via")
                             for event in events if event.get("detail") or event.get("via")}
        result["drain_reasons"] = drains
        result["stderr"] = diagnostics.getvalue()
        result["fault_details"] = [event["detail"] for event in events
                                   if event.get("kind") == "owner_fault"]
        result["journal_failures"] = [event["kind"] for event in failed_events]
        result["failed_fault_details"] = [event["detail"] for event in failed_events
                                          if event.get("kind") == "owner_fault"]
        print(json.dumps(result, sort_keys=True), flush=True)
        return result


PROBES = [
    {"label": "marker-fault", "marker": True},
    {"label": "marker-fault-sigterm", "marker": True, "signal_name": "SIGTERM"},
    {"label": "postspawn-journal-fault", "journal": True},
    {"label": "startup-signal-sigterm", "which_missing": True, "signal_name": "SIGTERM"},
    {"label": "signal-failed-removal-sigterm", "rmtree_fails": True, "signal_name": "SIGTERM"},
    {"label": "signal-failed-removal-sigint", "rmtree_fails": True, "signal_name": "SIGINT"},
    {"label": "signal-failed-drain-sigterm", "drain_fails": True, "signal_name": "SIGTERM"},
    {"label": "signal-failed-drain-sigint", "drain_fails": True, "signal_name": "SIGINT"},
    {"label": "plain-failed-removal", "rmtree_fails": True},
    {"label": "plain-startup-failure", "which_missing": True},
    # Nonce generation after root acquisition: the root is owned state
    # before any fallible operation, so this fault must still drain and
    # remove it.
    {"label": "nonce-fault", "nonce_fails": True},
    # Persistent journal ENOSPC: reporting may never grow the fault list it
    # is reporting (the self-growing iteration) and may never skip the
    # rmtree that completed drainage already authorized.
    {"label": "persistent-journal-fault", "journal_fails_from": "inner_started"},
    {"label": "persistent-journal-fault-sigterm", "journal_fails_from": "inner_started",
     "fail_signal": "SIGTERM"},
    # Journal failures inside cleanup itself: each entry is attempted
    # independently of the deletion, and a failed completion report never
    # claims a successful journal.
    {"label": "cleanup-entry-journal-fault", "fail_kinds": ("cleanup_entry",)},
    {"label": "removal-entry-journal-fault", "fail_kinds": ("removal_entry",)},
    {"label": "completion-journal-fault", "fail_kinds": ("completion",)},
    # Termination failures: a stop or poll error never assumes the child
    # dead, never skips the kernel-only drain, and never overrides a
    # captured signal's conventional status.
    {"label": "stop-failure-eof", "stop_fails": True},
    {"label": "stop-failure-sigterm", "stop_fails": True, "stop_signal": "SIGTERM"},
    {"label": "poll-failure", "poll_fails": True},
    {"label": "poll-failure-sigint", "poll_fails": True, "poll_signal": "SIGINT"},
    # Combinations: a failing stop beside a failing removal under a captured
    # signal, and a persistent journal failure beside a failed drainage.
    {"label": "stop-removal-failure-sigterm", "stop_fails": True, "stop_signal": "SIGTERM",
     "rmtree_fails": True},
    {"label": "persistent-journal-and-drain-fault", "journal_fails_from": "removal_error",
     "drain_fails": True},
]

# Both signal races at every process-observation boundary, including wait in
# the real stop_inner() path, and beside journal and removal failures.
PROBES.extend([
    {"label": "stop-failure-sigint", "stop_fails": True, "stop_signal": "SIGINT"},
    {"label": "poll-failure-sigterm", "poll_fails": True, "poll_signal": "SIGTERM"},
    {"label": "wait-failure-eof", "wait_fails": True},
])
for boundary in ("poll", "stop", "wait"):
    for name in ("SIGINT", "SIGTERM"):
        if boundary == "wait":
            PROBES.append({"label": f"wait-failure-{name.lower()}",
                           "wait_fails": True, "wait_signal": name})
        PROBES.append({"label": f"{boundary}-journal-removal-failure-{name.lower()}",
                       f"{boundary}_fails": True, f"{boundary}_signal": name,
                       "fail_kinds": ("cleanup_entry",), "rmtree_fails": True})

# A recorded failing inner status wins over a later owner, journal, drain, or
# removal fault. Passing inner runs must still fail for each of those faults.
for status in (0, 1, 7, -9):
    for fault, fields in (("journal", {"fail_kinds": ("completion",)}),
                          ("owner", {"finish_poll_fails": True}),
                          ("drain", {"drain_fails": True}),
                          ("removal", {"rmtree_fails": True})):
        PROBES.append({"label": f"inner-{status}-{fault}-fault", "inner_status": status, **fields})


def main() -> int:
    for probe in PROBES:
        run_probe(**probe)
    print(json.dumps({"probes": len(PROBES), "real_processes_spawned": 0}), flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
