#!/usr/bin/env python3
"""Resolve a repository path to its navigation Domain, or a Domain to its card.

Examples: python3 scripts/domain-navigation.py --path internal/store/events.go
          python3 scripts/domain-navigation.py --domain durable-authority
Outputs JSON. Unresolved is explicit; invalid or unknown input exits 1.
"""

import argparse
import json
from pathlib import Path
import sys

from domain_navigation import REGISTRY, NavigationError, card_path, partition, read_json, selector


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("--base-ref", default="HEAD", help="Git revision for the unresolved-path ratchet")
    choice = parser.add_mutually_exclusive_group(required=True)
    choice.add_argument("--path", help="normalized repository-relative file path")
    choice.add_argument("--domain", help="current Domain ID, including the root")
    args = parser.parse_args()
    try:
        root = args.root.resolve()
        registry = read_json(root, REGISTRY)
        state = partition(root, registry, base_ref=args.base_ref)
        if args.path is not None:
            path = selector(args.path)
            if path in state["owners"]:
                domain = state["owners"][path]
                result = {"status": "mapped", "path": path, "domain_id": domain, "card_path": card_path(domain)}
            elif path in state["unresolved"]:
                result = {"status": "unresolved", **state["unresolved"][path]}
            else:
                raise NavigationError(f"unknown repository path: {path}")
        else:
            if args.domain not in {d["domain_id"] for d in registry["domains"] if d["status"] == "current"}:
                raise NavigationError(f"unknown current Domain: {args.domain}")
            result = {"status": "mapped", "domain_id": args.domain, "card_path": card_path(args.domain)}
        print(json.dumps(result, sort_keys=True))
        return 0
    except (NavigationError, OSError, ValueError, KeyError, TypeError) as exc:
        print(f"domain navigation: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
