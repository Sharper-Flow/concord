"""Lock blind reviews and summarize a private pilot without making causal claims.

Run ``python -m adapter.opencode.evals.instructions.analysis lock PILOT REVIEWS LOCK``
before opening the arm key, then run ``... analysis report PILOT LOCK``. All
paths must be outside the repository. The reviewer must independently attest
that the arm key was not seen; a local file cannot prove that attestation.
"""

import argparse
import hashlib
import json
import math
import os
from collections import Counter, defaultdict
from decimal import Decimal, InvalidOperation
from pathlib import Path

from . import cases, evaluation, plan, run, snapshots

# Harness files that determine grades, families and the schedule. They must
# match the hashes the pilot froze, so analysis grades as the pilot did.
_GRADING_HASHES = ("evaluator", "cases", "plan")


def _private(path, repository):
    return snapshots.outside_repository(path, repository)


def _digest(data):
    return hashlib.sha256(data).hexdigest()


def _canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), allow_nan=False).encode()


def _pilot_files(root):
    """Read the schedule, journal and metadata once; return bytes and parsed values."""
    raw = {name: (root / f"pilot-{name}.json").read_bytes() for name in ("schedule", "journal", "metadata")}
    parsed = {name: json.loads(data) for name, data in raw.items()}
    current = run._hashes()
    frozen = parsed["metadata"].get("hashes")
    if not isinstance(frozen, dict) or any(frozen.get(name) != current[name] for name in _GRADING_HASHES):
        raise ValueError("grading harness differs from the pilot")
    return raw, parsed


def _valid_checks(checks, names):
    return (isinstance(checks, dict) and set(checks) == set(names)
            and all(isinstance(v, dict) and v.get("verified") is True and type(v.get("passed")) is bool
                    for v in checks.values()))


def lock(pilot_dir, reviews_path, lock_path, *, repository=run.REPOSITORY):
    """Validate and exclusively write the complete blind review lock.

    Reviews: {reviewer, independent_blind: true, reviews: {blind_id:
    {packet_sha256, semantic_checks: {name: {verified, passed}}, severity_note}}}.
    Hashes refer to the exact bytes of each pilot ``-blind.json`` packet.
    """
    root = _private(pilot_dir, repository)
    review_path = _private(reviews_path, repository)
    dest = _private(lock_path, repository)
    raw, parsed = _pilot_files(root)
    schedule, journal = parsed["schedule"], parsed["journal"]
    review_bytes = review_path.read_bytes()
    reviews = json.loads(review_bytes)
    if (not isinstance(reviews.get("reviewer"), str) or not reviews["reviewer"].strip()
            or reviews.get("independent_blind") is not True or not isinstance(reviews.get("reviews"), dict)):
        raise ValueError("independent blind reviewer attestation required")
    rows = {row["blind_id"]: row for row in schedule["rows"]}
    if len(rows) != len(schedule["rows"]):
        raise ValueError("duplicate schedule blind ID")
    attempts = journal["attempts"]
    if (len({a["id"] for a in attempts}) != len(attempts)
            or len({a["blind_id"] for a in attempts}) != len(attempts)
            or any(a["blind_id"] not in rows for a in attempts)):
        raise ValueError("journal attempts do not match schedule")
    completed = [a for a in attempts if a["status"] == "completed"]
    if set(reviews["reviews"]) != {a["blind_id"] for a in completed}:
        raise ValueError("reviews must cover exactly the completed attempts")
    grades = {}
    for attempt in completed:
        blind_id, attempt_id = attempt["blind_id"], attempt["id"]
        # Attempt IDs originate in the journal, not from a trusted path.
        if not isinstance(attempt_id, str) or not attempt_id.isalnum():
            raise ValueError("invalid attempt ID")
        packet_bytes = (root / (attempt_id + "-blind.json")).read_bytes()
        packet = json.loads(packet_bytes)
        result = json.loads((root / (attempt_id + "-result.json")).read_bytes())
        row, review = rows[blind_id], reviews["reviews"][blind_id]
        if (result.get("attempt_id") != attempt_id or result.get("blind_id") != blind_id
                or result.get("case_id") != row["case_id"] or result.get("status") != "completed"
                or packet.get("case") != row["case_id"] or not isinstance(review, dict)
                or review.get("packet_sha256") != _digest(packet_bytes)
                or not isinstance(review.get("severity_note"), str) or not review["severity_note"].strip()):
            raise ValueError("review, result or packet identity mismatch")
        names = cases.CASES[row["case_id"]]["semantic_checks"]
        checks = review.get("semantic_checks")
        if packet.get("semantic_checks") != names or not _valid_checks(checks, names):
            raise ValueError("semantic review is incomplete or mismatched")
        grade = evaluation.evaluate(row["case_id"], packet["events"], packet["artifacts"],
                                    packet["final_text"], semantic_review=checks)
        if grade["semantic_pending"]:
            raise ValueError("semantic review is pending")
        grades[blind_id] = {"packet_sha256": _digest(packet_bytes), "grade": grade,
                            "semantic_checks": checks, "severity_note": review["severity_note"]}
    locked = {"version": 1, "schedule_sha256": _digest(raw["schedule"]),
              "journal_sha256": _digest(raw["journal"]), "metadata_sha256": _digest(raw["metadata"]),
              "review_sha256": _digest(review_bytes),
              "reviewer": reviews["reviewer"], "independent_blind": True, "grades": grades}
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
    with os.fdopen(os.open(dest, flags, 0o400), "wb") as stream:
        stream.write(_canonical(locked) + b"\n")
        stream.flush()
        os.fsync(stream.fileno())
    return locked


def _wilson(successes, n, z=1.959963984540054):
    if not n:
        return None
    p = successes / n
    d = 1 + z * z / n
    center = (p + z * z / (2 * n)) / d
    half = z * math.sqrt(p * (1 - p) / n + z * z / (4 * n * n)) / d
    return [max(0.0, center - half), min(1.0, center + half)]


def _paired(better, worse, n):
    """Bonferroni Wilson intervals and exact two-sided discordance test."""
    if not n:
        return {"difference": None, "ci_95": None, "exact_p": None}
    # Bonferroni combines two approximate 97.5% Wilson marginal intervals.
    z = 2.241402727604947
    a, b = _wilson(better, n, z), _wilson(worse, n, z)
    discordant = better + worse
    p = min(1.0, 2 * sum(math.comb(discordant, k) for k in range(min(better, worse) + 1)) / 2 ** discordant) if discordant else 1.0
    return {"difference": (better - worse) / n, "ci_95": [max(-1, a[0] - b[1]), min(1, a[1] - b[0])], "exact_p": p}


def _verified_key(key, schedule, metadata):
    """Accept the key only if it regenerates the locked schedule and names the pilot's snapshots."""
    try:
        arms = [{"snapshot_id": key["snapshots"][arm]["snapshot_id"],
                 "provenance": {ref: key["snapshots"][arm][ref] for ref in ("concord_ref", "host_ref")}}
                for arm in ("baseline", "candidate")]
        expected = plan.pilot_plan(key["seed"], *arms)
    except (KeyError, TypeError) as exc:
        raise ValueError("key does not match schedule") from exc
    if (expected != (schedule, key)
            or metadata.get("snapshots") != {arm: key["snapshots"][arm]["snapshot_id"] for arm in ("baseline", "candidate")}):
        raise ValueError("key does not match schedule")
    return key["arms"]


LIMITATIONS = (
    "Two repetitions are low power; zero events do not prove safety. "
    "Wilson intervals and their Bonferroni difference are approximate, not exact guarantees. "
    "Pairs in a family share cases, so the paired test treats correlated repetitions as independent, "
    "and family p-values are not corrected for testing several families. "
    "Combined arms do not identify a causal instruction change or which merge caused a difference. "
    "safety_blocked counts S3 events in either arm; regression and S3 flags require manual attribution, "
    "and this report reverts nothing and declares no success. "
    "Costs are nominal amounts from normalized token counts and supplied rates, not provider or subscription billing; "
    "unresolved reservations remain charged.")


def report(pilot_dir, lock_path, *, repository=run.REPOSITORY):
    """Unmask only after validating the locked grades against the pilot files."""
    root, locked_path = _private(pilot_dir, repository), _private(lock_path, repository)
    lock_bytes = locked_path.read_bytes()
    locked = json.loads(lock_bytes)
    raw, parsed = _pilot_files(root)
    schedule, journal, metadata = parsed["schedule"], parsed["journal"], parsed["metadata"]
    if (locked.get("version") != 1 or locked.get("independent_blind") is not True
            or any(locked.get(f"{name}_sha256") != _digest(raw[name]) for name in raw)):
        raise ValueError("lock or pilot changed")
    completed = [a for a in journal["attempts"] if a["status"] == "completed"]
    if set(locked["grades"]) != {a["blind_id"] for a in completed}:
        raise ValueError("lock does not cover completed attempts")
    for a in completed:
        grade = locked["grades"][a["blind_id"]]
        packet = (root / (a["id"] + "-blind.json")).read_bytes()
        if grade["packet_sha256"] != _digest(packet) or grade["grade"]["semantic_pending"]:
            raise ValueError("locked packet changed or semantic review pending")
        body = json.loads(packet)
        checks = grade["semantic_checks"]
        if (not _valid_checks(checks, cases.CASES[body["case"]]["semantic_checks"])
                or not isinstance(grade.get("severity_note"), str) or not grade["severity_note"].strip()
                or evaluation.evaluate(body["case"], body["events"], body["artifacts"], body["final_text"],
                                       semantic_review=checks) != grade["grade"]):
            raise ValueError("invalid locked semantic review or grade")
    # Do not read the key until all blind lock checks above have passed.
    arm_of = _verified_key(json.loads((root / "pilot-key.json").read_bytes()), schedule, metadata)
    rows = schedule["rows"]
    pairs = defaultdict(dict)
    for row in rows:
        pairs[row["pair"]][arm_of[row["blind_id"]]] = row
    grouped = defaultdict(list)
    for pair in pairs.values():
        grouped[cases.CASES[pair["baseline"]["case_id"]]["family"]].append(pair)
    statuses = Counter(a["status"] for a in journal["attempts"])
    attempts = {s: statuses[s] for s in ("completed", "failed", "pending")}
    attempts["unclaimed"] = len(rows) - len(journal["attempts"])
    output = {"pilot": {"complete": attempts["completed"] == len(rows), "attempts": attempts,
                        "model": metadata["model"], "snapshots": metadata["snapshots"],
                        "metadata_sha256": _digest(raw["metadata"]), "lock_sha256": _digest(lock_bytes),
                        "analysis_sha256": _digest(Path(__file__).read_bytes())},
              "families": {}, "safety_blocked": False, "limitations": LIMITATIONS}
    for family, family_pairs in sorted(grouped.items()):
        arms = {arm: {"n": 0, "passed": 0, "failed": 0, "ungraded": 0,
                      "severity": {s: 0 for s in ("S0", "S1", "S2", "S3")}}
                for arm in ("baseline", "candidate")}
        complete, better, worse, regression = 0, 0, 0, False
        for pair in family_pairs:
            gs = {}
            for arm, row in pair.items():
                entry = locked["grades"].get(row["blind_id"])
                if entry is None:
                    arms[arm]["ungraded"] += 1
                    continue
                grade = entry["grade"]
                gs[arm] = grade
                arms[arm]["n"] += 1
                arms[arm]["passed" if grade["passed"] else "failed"] += 1
                arms[arm]["severity"][grade["severity"]] += 1
                output["safety_blocked"] |= grade["severity"] == "S3"
            if len(gs) == 2:
                complete += 1
                better += gs["candidate"]["passed"] and not gs["baseline"]["passed"]
                worse += gs["baseline"]["passed"] and not gs["candidate"]["passed"]
                # Material regression: a candidate S2/S3 worse than its paired baseline.
                regression |= gs["candidate"]["severity"] in ("S2", "S3") and gs["candidate"]["severity"] > gs["baseline"]["severity"]
        for arm in arms.values():
            arm["pass_ci_95"] = _wilson(arm["passed"], arm["n"])
        paired = _paired(better, worse, complete)
        dropped = len(family_pairs) - complete
        output["families"][family] = {"arms": arms, "complete_pairs": complete,
                                      "dropped_incomplete_pairs": dropped,
                                      "candidate_pass_better": better, "candidate_pass_worse": worse,
                                      "paired_pass_difference": paired,
                                      # Missing pairs or an interval containing zero cannot show a difference.
                                      "inconclusive": dropped > 0 or paired["ci_95"] is None
                                      or paired["ci_95"][0] <= 0 <= paired["ci_95"][1],
                                      "material_regression_requires_attribution": regression}
    costs = {"cap_usd": Decimal(metadata["cap_usd"]), "settled_nominal_usd": Decimal(0),
             "unresolved_reservations_usd": Decimal(0), "unresolved_requests": 0}
    for request in journal["requests"]:
        try:
            amount = Decimal(request["charge"])
            if not amount.is_finite() or amount < 0:
                raise ValueError("invalid journal charge")
        except (InvalidOperation, TypeError, KeyError) as exc:
            raise ValueError("invalid journal charge") from exc
        if request["status"] == "settled":
            costs["settled_nominal_usd"] += amount
        elif request["status"] == "reserved":
            costs["unresolved_reservations_usd"] += amount
            costs["unresolved_requests"] += 1
        else:
            raise ValueError("invalid request status")
    costs["total_charged_usd"] = costs["settled_nominal_usd"] + costs["unresolved_reservations_usd"]
    if costs["total_charged_usd"] > costs["cap_usd"]:
        raise ValueError("journal charges exceed the pilot ceiling")
    output["costs"] = {k: str(v) if isinstance(v, Decimal) else v for k, v in costs.items()}
    return output


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    locking = sub.add_parser("lock")
    locking.add_argument("pilot")
    locking.add_argument("reviews")
    locking.add_argument("lock")
    reporting = sub.add_parser("report")
    reporting.add_argument("pilot")
    reporting.add_argument("lock")
    args = parser.parse_args()
    result = lock(args.pilot, args.reviews, args.lock) if args.command == "lock" else report(args.pilot, args.lock)
    print(json.dumps(result, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
