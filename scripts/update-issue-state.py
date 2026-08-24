#!/usr/bin/env python3
"""Refresh the committed issue-state snapshot from the issue tracker.

This is the authoritative online half of the pointer-liveness rule in
scripts/coverage_state.py. CI reads the snapshot and performs no lookups, so
the snapshot is what makes the rule deterministic and offline; this script is
what makes it true.

It covers every plane in coverage_state.ISSUE_STATE_MANIFESTS at once. A
per-plane refresh would let a new plane's pointers go uncovered until someone
noticed, which is the failure the shared registry exists to prevent (#451).
"""
from __future__ import annotations

import json
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))

from coverage_state import (  # noqa: E402
    ISSUE_REPO,
    ISSUE_STATE,
    collect_outstanding_issues,
    write_issue_state,
)


def resolve(number: int) -> str | None:
    result = subprocess.run(
        ["gh", "issue", "view", str(number), "--repo", ISSUE_REPO, "--json", "state"],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        print(f"issue {number}: gh failed: {result.stderr.strip()}", file=sys.stderr)
        return None
    try:
        state = json.loads(result.stdout).get("state")
    except json.JSONDecodeError:
        print(f"issue {number}: gh returned unparseable output", file=sys.stderr)
        return None
    if state not in ("OPEN", "CLOSED"):
        print(f"issue {number}: unexpected state {state!r}", file=sys.stderr)
        return None
    return state.lower()


def main() -> int:
    try:
        numbers = collect_outstanding_issues()
    except (OSError, json.JSONDecodeError) as exc:
        print(f"cannot read a coverage manifest: {exc}", file=sys.stderr)
        return 1

    states: dict[str, str] = {}
    for number in numbers:
        state = resolve(number)
        if state is None:
            return 1
        states[str(number)] = state

    generated_at = (
        datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")
    )
    write_issue_state(states, generated_at)
    print(
        f"wrote {ISSUE_STATE.relative_to(ROOT)} covering "
        f"{len(states)} outstanding issue(s)"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
