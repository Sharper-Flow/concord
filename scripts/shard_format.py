#!/usr/bin/env python3
"""The one canonical byte form for an authored knowledge shard.

Knowledge records and coverage records are hand-authored JSON objects, one file
per record. Nothing owned their byte form, so it drifted: indentation and
trailing newlines diverged across the corpus until a reformatting diff and a
content diff were indistinguishable in review, and an editor writing a shard had
no way to know which form was correct.

Both shard generators normalise through this module. Regenerating repairs a
drifted shard in place; --check refuses one. The canonical form is the one 141
of the 144 existing shards already used, so adopting it rewrites only the
outliers.

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
