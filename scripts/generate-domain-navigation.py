#!/usr/bin/env python3
"""Generate bounded Domain cards and their complete navigation inventory."""

import argparse
from pathlib import Path
import sys

from domain_navigation import COMPANION, GENERATED, OUTPUT, REGISTRY, NavigationError, artifacts, expected_paths, read_json


def owned_card(root: Path, path: str) -> bool:
    target = root / path
    return (target.parent == root / OUTPUT / "domains" and target.suffix == ".md"
            and not target.is_symlink() and target.parent.resolve() == target.parent
            and target.read_bytes().startswith(f"<!-- {GENERATED} -->\n".encode()))


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
        desired = expected_paths(read_json(root, REGISTRY))
        actual = {str(p.relative_to(root)) for p in (root / OUTPUT).rglob("*") if p.is_file()}
        extras = actual - desired
        if extras and (args.check or not all(owned_card(root, path) for path in extras)):
            raise NavigationError(f"unexpected generated artifacts: {', '.join(sorted(extras))}")
        # Only obsolete cards in this generator's namespace can be removed.
        # Remove them before deriving the inventory's exact file universe.
        for path in sorted(extras):
            (root / path).unlink()
        expected = artifacts(root, base_ref=args.base_ref)
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
