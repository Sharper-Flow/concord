#!/usr/bin/env python3
"""Run the exact Bun test files named by a pinned native check."""
import subprocess
import sys
from pathlib import Path


def main() -> int:
    files = sys.argv[1:]
    if not files or any(Path(name).name != name or not name.endswith(".test.ts") for name in files):
        raise SystemExit("acceptance requires literal adapter .test.ts file names")
    return subprocess.run(
        ["bun", "test", "--preload", "./acceptance-preload.ts", *[f"./{name}" for name in files]],
        cwd=Path(__file__).resolve().parent,
        check=False,
        timeout=120,
    ).returncode


if __name__ == "__main__":
    sys.exit(main())
