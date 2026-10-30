#!/usr/bin/env python3
"""The one canonical byte form for an authored knowledge shard.

Knowledge records are hand-authored JSON objects, one file per record. This
module owns their byte form. The shard generator normalises through it:
regenerating repairs a drifted shard in place, and --check refuses one.

Key order is alphabetical rather than authored, because a stable order is what
makes a content change legible in a diff.
"""

from __future__ import annotations

import json
from pathlib import Path


def canonical_bytes(record: object) -> bytes:
    """The canonical serialisation of one shard."""
    return (json.dumps(record, indent=2, ensure_ascii=False, sort_keys=True) + "\n").encode("utf-8")


def drifted(paths: list[Path]) -> list[Path]:
    """Shards whose bytes differ from their canonical form.

    A shard that is not valid JSON is not reported here. The caller's own
    validation owns that failure and reports it with better context.
    """
    result: list[Path] = []
    for path in paths:
        try:
            raw = path.read_bytes()
            if canonical_bytes(json.loads(raw)) != raw:
                result.append(path)
        except (OSError, json.JSONDecodeError):
            continue
    return result


def normalise(paths: list[Path]) -> list[Path]:
    """Rewrite drifted shards in place. Returns the paths rewritten."""
    rewritten = drifted(paths)
    for path in rewritten:
        path.write_bytes(canonical_bytes(json.loads(path.read_bytes())))
    return rewritten
