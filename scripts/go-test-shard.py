#!/usr/bin/env python3
"""Print the `go test -run` pattern for one shard of a package's tests (CON-906).

`internal/store` alone took 798 s of the 14 min `verify-tests` job, so CI runs
that package, and `cmd/concord`, as several jobs. Each job asks this script
for its share:

    go test -run "$(python3 scripts/go-test-shard.py ./internal/store 1 3)" ./internal/store

The package's top-level tests, examples, and fuzz targets come from
`go test -list .`, sorted by name, and are dealt round-robin: shard i of n
takes every name whose sorted position p satisfies p % n == i - 1. The shards
are disjoint and together cover every listed name, so no test is lost when a
file is added or renamed. Subtests run under their parent.
"""

from __future__ import annotations

import re
import subprocess
import sys

LISTED_NAME = re.compile(r"^(Test|Example|Fuzz)[A-Za-z0-9_]*$")


def listed_names(go_list_output: str) -> list[str]:
    """The runnable names in `go test -list` output, without its trailing `ok` line."""
    return sorted({line.strip() for line in go_list_output.splitlines() if LISTED_NAME.match(line.strip())})


def shard(names: list[str], index: int, count: int) -> list[str]:
    """Shard `index` (1-based) of `count`, dealt round-robin over sorted names."""
    if count < 1 or not 1 <= index <= count:
        raise ValueError(f"shard {index} of {count} does not exist")
    return [name for position, name in enumerate(sorted(names)) if position % count == index - 1]


def run_pattern(names: list[str]) -> str:
    """An anchored `-run` pattern that selects exactly these top-level names."""
    if not names:
        raise ValueError("an empty shard would run no tests; lower the shard count")
    return "^(" + "|".join(names) + ")$"


def main(argv: list[str]) -> int:
    if len(argv) != 3:
        print("usage: go-test-shard.py <package> <index> <count>", file=sys.stderr)
        return 64
    package, index, count = argv[0], int(argv[1]), int(argv[2])
    listed = subprocess.run(["go", "test", "-list", ".", package], check=True, capture_output=True, text=True)
    print(run_pattern(shard(listed_names(listed.stdout), index, count)))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
