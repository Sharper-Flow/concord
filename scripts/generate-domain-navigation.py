#!/usr/bin/env python3
"""Generate bounded Domain cards and their complete navigation inventory."""

import argparse
from pathlib import Path
import sys

from domain_navigation import COMPANION, OUTPUT, NavigationError, artifacts


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("--check", action="store_true", help="refuse stale, missing, or extra generated artifacts")
    parser.add_argument("--base-ref", default="HEAD", help="Git revision for the unresolved-path ratchet")
    args = parser.parse_args()
    root = args.root.resolve()
    if not (root / COMPANION).exists():
        print("domain navigation: no companion; not adopted")
        return 0
    try:
        expected = artifacts(root, base_ref=args.base_ref)
        actual = {str(p.relative_to(root)) for p in (root / OUTPUT).rglob("*") if p.is_file()}
        extras = actual - expected.keys()
        if extras:
            raise NavigationError(f"unexpected generated artifacts: {', '.join(sorted(extras))}")
        for path, content in expected.items():
            target = root / path
            if args.check:
                if not target.is_file() or target.read_bytes() != content:
                    raise NavigationError(f"stale or missing generated artifact: {path}")
            else:
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(content)
        print(f"domain navigation {'check passed' if args.check else 'generated'}: {len(expected) - 1} cards")
        return 0
    except (NavigationError, OSError, ValueError, KeyError, TypeError) as exc:
        print(f"domain navigation: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
