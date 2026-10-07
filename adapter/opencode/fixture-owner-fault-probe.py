#!/usr/bin/env python3
"""Deterministic fault-injection probes for the owner lifecycle.

Permanent machinery for fixture_owner.test.ts, beside the real-run matrix.
The faults probed here — marker persistence failure, the post-spawn journal
failure, and a SIGINT/SIGTERM captured while executable resolution, drainage,
or removal is failing — cannot be timed against a real child run, so each
probe drives the owner's own run_owned() in this process with the kernel
boundary stubbed: no real child is spawned, no real signal is sent, and the
captured signal handler is invoked directly instead. Each probe allocates
one real directory as the exact owned root inside a confined temporary
parent that this probe removes itself, and nothing else is touched.

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
              drain_fails: bool = False, signal_name: str | None = None) -> dict:
    """Run one fault probe. The fault arguments:

    marker          fault the marker write (ENOSPC)
    journal         fault the inner_started journal emit (ENOSPC) after spawn
    which_missing   resolve no bun executable (startup failure)
    rmtree_fails    fault the root removal (EACCES)
    drain_fails     fault the kernel child list (EACCES) during drainage
    signal_name     when set, the captured SIGINT/SIGTERM handler fires from
                    inside the faulting operation, as a signal that raced it
    """
    signum = {"SIGINT": signal.SIGINT, "SIGTERM": signal.SIGTERM}[signal_name] if signal_name else None
    with tempfile.TemporaryDirectory(prefix="concord-owner-fault-") as parent:
        allocated = Path(parent, "exact-owned-root")
        handlers = {}
        events: list[dict] = []
        drains: list[str] = []

        def allocate(**_kwargs):
            allocated.mkdir()
            return str(allocated)

        def install(signum_installed, handler):
            handlers[signum_installed] = handler

        def capture_signal():
            handlers[signum](signum, None)

        fake_inner = SimpleNamespace(returncode=0, pid=123456,
                                     poll=lambda: 0, wait=lambda: 0)

        def marker_write(*_args, **_kwargs):
            if signum is not None:
                capture_signal()
            raise OSError(errno.ENOSPC, "synthetic marker disk full")

        def resolve_bun(_name):
            if which_missing:
                if signum is not None:
                    capture_signal()
                return None
            return "/synthetic/bun"

        def emit(event):
            events.append(event)
            if journal and event.get("kind") == "inner_started":
                raise OSError(errno.ENOSPC, "synthetic journal disk full")

        def drain_owned(reason):
            drains.append(reason)
            if drain_fails:
                if signum is not None:
                    capture_signal()
                raise OSError(errno.EACCES, "synthetic inaccessible kernel child list")
            return {"reason": reason, "signalled": [], "killed": [], "reaped": []}

        def remove_tree(_root):
            if signum is not None:
                capture_signal()
            raise PermissionError(errno.EACCES, "synthetic removal denied")

        result: dict = {"probe": label}
        with ExitStack() as stack:
            stack.enter_context(redirect_stderr(io.StringIO()))
            if marker:
                stack.enter_context(mock.patch.object(owner.Path, "write_text", side_effect=marker_write))
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
]


def main() -> int:
    for probe in PROBES:
        run_probe(**probe)
    print(json.dumps({"probes": len(PROBES), "real_processes_spawned": 0}), flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
