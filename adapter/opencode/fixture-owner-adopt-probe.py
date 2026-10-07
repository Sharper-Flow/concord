#!/usr/bin/env python3
"""Test-only adoption probe for the owner-SIGKILL regression.

This probe is machinery for fixture_owner.test.ts, not shipped recovery:
fixture-root-owner.py itself reclaims nothing after SIGKILL. The probe is the
owner's parent and a subreaper BEFORE the owner launches, so when the test
kills the owner by SIGKILL — which runs no cleanup at all — the kernel moves
every surviving descendant of that run onto this probe. The probe records the
exact allocated root from the owner's own journal first, observes that the
leftover is still present with its ownership marker intact, then kills and
reaps only the descendants the kernel actually adopted (the same
kernel-child/ECHILD boundary the owner uses) and removes only that recorded
root. It signals no process it did not adopt, scans no process table, and
touches no other path.
"""
from __future__ import annotations

import importlib.util
import json
import os
import subprocess
import sys
import threading
import time
from pathlib import Path

OWNER_SOURCE = Path(__file__).with_name("fixture-root-owner.py")
_spec = importlib.util.spec_from_file_location("fixture_root_owner", OWNER_SOURCE)
owner = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(owner)

POLL_S = 0.05


def journal_lines(stream, sink: list[str]) -> None:
    for line in stream:
        sink.append(line.rstrip("\n"))


def discard(stream) -> None:
    for _ in stream:
        pass


def owner_event(line: str) -> dict | None:
    prefix = "@@concord-owner "
    if not line.startswith(prefix):
        return None
    try:
        return json.loads(line[len(prefix):])
    except ValueError:
        return None


def pulse_pids(path: str) -> list[int]:
    try:
        text = Path(path).read_text()
    except OSError:
        return []
    return sorted({int(part.split(" ")[0]) for part in text.split("\n") if part})


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        owner.write_stderr("usage: fixture-owner-adopt-probe.py CASE_FILE PULSE_FILE\n")
        return 2
    case, pulse = argv

    # The subreaper role is established BEFORE the owner launches: adoption of
    # the killed owner's descendants is the kernel's doing, not a scan.
    owner.become_subreaper()
    proc = subprocess.Popen(
        [sys.executable, str(OWNER_SOURCE), case],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    lines: list[str] = []
    threading.Thread(target=journal_lines, args=(proc.stderr, lines), daemon=True).start()
    # The inner run's output rides the owner's stdout; keep draining it so a
    # full pipe can never stall the inner run.
    threading.Thread(target=discard, args=(proc.stdout,), daemon=True).start()

    root = None
    nonce = None
    inner_pid = None
    deadline = time.monotonic() + 30.0
    while time.monotonic() < deadline and proc.poll() is None:
        for line in list(lines):
            event = owner_event(line)
            if not event:
                continue
            if event.get("kind") == "allocated_root" and root is None:
                root = event.get("root")
                nonce = event.get("nonce")
                owner.emit({"kind": "probe_registered_root", "root": root, "nonce": nonce})
            elif event.get("kind") == "inner_started":
                inner_pid = event.get("inner_pid")
        if root is not None and len(pulse_pids(pulse)) >= 6:
            break
        time.sleep(POLL_S)
    if root is None:
        owner.emit({"kind": "probe_error", "detail": "owner never journalled its allocated root"})
        return 3

    # SIGKILL runs no cleanup at all: the owner dies, everything it owned
    # survives, and the kernel adopts the survivors onto this probe.
    proc.kill()
    _, status = os.waitpid(proc.pid, 0)
    owner_status = 128 + os.WTERMSIG(status) if os.WIFSIGNALED(status) else os.WEXITSTATUS(status)
    time.sleep(0.2)  # Let the kernel finish reparenting before observing.
    owner.emit({"kind": "probe_kill", "signal": "SIGKILL", "owner_status": owner_status, "inner_pid": inner_pid})

    # The leftover fact this regression exists for: the root is still there,
    # marker intact, and the killed owner journalled no removal.
    marker_path = Path(root, owner.OWNED_ROOT_MARKER)
    marker_ok = False
    try:
        marker_ok = json.loads(marker_path.read_text()).get("nonce") == nonce
    except (OSError, ValueError):
        marker_ok = False
    saw_removal_error = any('removal_error' in line for line in lines)
    owner.emit({"kind": "probe_leftover", "root": root, "marker_present": marker_ok,
                "owner_journalled_removal_error": saw_removal_error})

    # Drain only what the kernel adopted, to the kernel's own ECHILD
    # boundary, then remove exactly the registered root through the owner's
    # own removal path, so the containment journals and deletes exactly as a
    # live owner run would.
    report = owner.drain_owned("probe_sigkill_adoption")
    owner.emit({"kind": "probe_drain_complete", "root": root, **report})
    confined = owner.OwnedRun("probe_sigkill_adoption")
    confined.root = root
    if owner.remove_root(confined) is not None:
        return 1
    if os.path.exists(root):
        owner.emit({"kind": "probe_error", "root": root, "detail": "root survived removal"})
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
