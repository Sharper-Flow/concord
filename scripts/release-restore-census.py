#!/usr/bin/env python3
"""Run inventory batches under heavy admission and the original job's RAM cap.

Invoke through oc-run: python3 scripts/release-restore-census.py INVENTORY ARGS...
The inventory's exit 3 means another batch remains. Every other status is final.
No new scope is created: oc-run retains its timeout and descendant ownership.
"""

import re
import subprocess
import sys
from pathlib import Path, PurePosixPath


def bound_job() -> None:
    membership = Path("/proc/self/cgroup").read_text(encoding="utf-8")
    group = next((line[3:] for line in membership.splitlines() if line.startswith("0::")), "")
    unit = PurePosixPath(group).name
    if not re.fullmatch(r"oc-run-[0-9a-f]{64}\.service", unit):
        raise ValueError("census must run in its original oc-run service")
    observed = subprocess.run(
        ["systemctl", "--user", "show", "--property=ControlGroup", "--value", unit],
        check=True, capture_output=True, text=True, timeout=10,
    ).stdout.strip()
    if observed != group:
        raise ValueError("census cgroup does not match the original job")
    subprocess.run(
        ["systemctl", "--user", "set-property", "--runtime", unit,
         "MemoryHigh=3G", "MemoryMax=4G", "MemorySwapMax=0"],
        check=True, timeout=10,
    )
    root = Path("/sys/fs/cgroup") / group.lstrip("/")
    for name, expected in (("memory.high", 3 << 30), ("memory.max", 4 << 30), ("memory.swap.max", 0)):
        if (root / name).read_text(encoding="ascii").strip() != str(expected):
            raise ValueError(f"census resource limit is not enforced: {name}")


def exit_status(status: int) -> int:
    return 128 - status if status < 0 else status


def run_batches(command: list[str]) -> int:
    while True:
        result = subprocess.run(command, check=False)
        if result.returncode != 3:
            return exit_status(result.returncode)


def run_census(command: list[str]) -> int:
    bound_job()
    result = subprocess.run(
        ["oc-test-gate", "full", "--timeout", "20m", "--", sys.executable,
         str(Path(__file__).resolve()), "--batch-loop", *command], check=False,
    )
    return exit_status(result.returncode)


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__, file=sys.stderr)
        return 64
    try:
        if sys.argv[1] == "--batch-loop":
            if len(sys.argv) < 3:
                return 64
            bound_job()
            return run_batches(sys.argv[2:])
        return run_census(sys.argv[1:])
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"release-restore-census: {error}", file=sys.stderr)
        return 78


if __name__ == "__main__":
    sys.exit(main())
