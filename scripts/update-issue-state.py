#!/usr/bin/env python3
"""Refresh the offline pointer snapshot from Linear MCP observations and GitHub.

Agents capture raw Linear status types through MCP into an observation file.
This script validates that fixed-shape input and normalizes states; it never
contacts Linear. CI validates the resulting snapshot without network lookups.

It covers every plane in coverage_state.ISSUE_STATE_MANIFESTS at once. A
per-plane refresh would let a new plane's pointers go uncovered until someone
noticed, which is the failure the shared registry exists to prevent.
"""
from __future__ import annotations

import argparse
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
    LINEAR_ISSUE,
    collect_outstanding_issues,
    load_json,
    parse_utc_time,
    write_issue_state,
)


def resolve_github(number: int) -> str | None:
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


def read_linear_observations(path: Path, required: list[str]) -> tuple[dict[str, str], dict[str, str]]:
    findings: list[str] = []
    document = load_json(path, findings)
    if findings:
        raise ValueError("; ".join(findings))
    if (not isinstance(document, dict) or set(document) != {"schema_version", "observations"}
            or document["schema_version"] != "1.0" or not isinstance(document["observations"], list)):
        raise ValueError("expected schema_version 1.0 and an observations array")
    states: dict[str, str] = {}
    read_times: dict[str, str] = {}
    now = datetime.now(timezone.utc)
    for row in document["observations"]:
        if not isinstance(row, dict) or set(row) != {"identifier", "status_type", "read_at"}:
            raise ValueError("each observation requires only identifier, status_type, read_at")
        identifier, status = row["identifier"], row["status_type"]
        if not isinstance(identifier, str) or not LINEAR_ISSUE.fullmatch(identifier):
            raise ValueError("observation identifier must be a Linear issue identifier")
        if identifier in states:
            raise ValueError(f"duplicate observation: {identifier}")
        if status in ("completed", "canceled", "duplicate"):
            states[identifier] = "closed"
        elif status in ("triage", "backlog", "unstarted", "started"):
            states[identifier] = "open"
        else:
            raise ValueError(f"unknown status_type for {identifier}: {status!r}")
        if parse_utc_time(row["read_at"]) > now:
            raise ValueError(f"future read_at for {identifier}")
        read_times[identifier] = row["read_at"]
    if set(states) != set(required):
        raise ValueError(f"observation identifiers differ; missing: {sorted(set(required) - set(states))}; extra: {sorted(set(states) - set(required))}")
    return states, read_times


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--linear-observations", type=Path, help="JSON observations captured by an agent from Linear MCP get_issue reads")
    arguments = parser.parse_args()
    try:
        numbers = collect_outstanding_issues()
    except (OSError, json.JSONDecodeError) as exc:
        print(f"cannot read a coverage manifest: {exc}", file=sys.stderr)
        return 1

    states: dict[str, str] = {}
    linear = [pointer for pointer in numbers if isinstance(pointer, str)]
    read_times: dict[str, str] = {}
    try:
        if linear and arguments.linear_observations is None:
            raise ValueError("--linear-observations is required")
        if arguments.linear_observations is not None:
            resolved, read_times = read_linear_observations(arguments.linear_observations, linear)
            states.update(resolved)
    except ValueError as exc:
        print(f"Linear observations invalid: {exc}; required identifiers: {', '.join(linear) or '(none)'}", file=sys.stderr)
        return 1
    for number in (pointer for pointer in numbers if isinstance(pointer, int)):
        state = resolve_github(number)
        if state is None:
            return 1
        states[str(number)] = state

    generated_at = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
    write_issue_state(states, generated_at, read_times)
    print(
        f"wrote {ISSUE_STATE.relative_to(ROOT)} covering "
        f"{len(states)} outstanding issue(s)"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
