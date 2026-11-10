"""Offline paired pilot allocation, without an execution path."""

import hashlib
import random
import re

from .cases import CASES


REPETITIONS = 2
_SHA256 = re.compile(r"[0-9a-f]{64}")


def _arm(snapshot, label):
    try:
        identity = snapshot["snapshot_id"]
        refs = {key: snapshot["provenance"][key] for key in ("concord_ref", "host_ref")}
    except (KeyError, TypeError) as exc:
        raise ValueError(f"{label} must be a loaded snapshot") from exc
    if not isinstance(identity, str) or not _SHA256.fullmatch(identity):
        raise ValueError(f"{label} snapshot identity must be a SHA-256 digest")
    if not all(isinstance(value, str) and value.strip() for value in refs.values()):
        raise ValueError(f"{label} snapshot references are required")
    return {"snapshot_id": identity, **refs}


def pilot_plan(seed, baseline, candidate):
    """Return the blinded schedule and a separate secret arm key.

    ``baseline`` and ``candidate`` are snapshots verified by ``snapshots.load``.
    The arms must differ in snapshot identity, Concord reference and host
    reference. ``seed``
    only orders the schedule and assigns arm positions; it is not a model
    sampling seed. The caller must store both outside the repository and keep
    the key from graders until grades are locked. This function starts no host
    sessions.
    """
    if not isinstance(seed, int) or isinstance(seed, bool) or seed.bit_length() < 128 or seed < 0:
        raise ValueError("seed must be a nonnegative integer with at least 128 bits")
    arms_by_name = {"baseline": _arm(baseline, "baseline"), "candidate": _arm(candidate, "candidate")}
    old, new = arms_by_name["baseline"], arms_by_name["candidate"]
    if any(old[key] == new[key] for key in ("snapshot_id", "concord_ref", "host_ref")):
        raise ValueError("baseline and candidate must differ in snapshot, Concord and host references")
    rng = random.Random(seed)
    rows = []
    key = {}
    for case_id in sorted(CASES):
        for repetition in range(1, REPETITIONS + 1):
            pair = f"{case_id}-{repetition}"
            arms = ["baseline", "candidate"]
            rng.shuffle(arms)
            for position, arm in enumerate(arms, start=1):
                blind_id = hashlib.sha256(f"{seed}:{pair}:{position}".encode()).hexdigest()[:24]
                rows.append({"case_id": case_id, "repetition": repetition,
                             "pair": pair, "position": position, "blind_id": blind_id})
                key[blind_id] = arm
    rng.shuffle(rows)
    # Preserve the randomized order within each pair after randomizing pair order.
    order = list(dict.fromkeys(row["pair"] for row in rows))
    positions = {pair: index for index, pair in enumerate(order)}
    rows.sort(key=lambda row: (positions[row["pair"]], row["position"]))
    return ({"sessions_per_model": len(rows), "rows": rows},
            {"seed": seed, "snapshots": arms_by_name, "arms": key})
