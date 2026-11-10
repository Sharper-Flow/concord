"""Run at most six private calibration attempts per artifact directory; this is not a global limit; never run a pilot."""

import argparse
import fcntl
import hashlib
import json
import os
import time
import traceback
import uuid
from pathlib import Path

from . import accounting, cases, estimate, evaluation, host, plan, snapshots


REPOSITORY = Path(__file__).resolve().parents[4]
LIMIT = 6


def _hashes():
    paths = {name: Path(module.__file__) for name, module in
             (("evaluator", evaluation), ("cases", cases), ("host", host),
              ("accounting", accounting), ("snapshots", snapshots), ("plan", plan))}
    paths["run"] = Path(__file__)
    return {name: hashlib.sha256(path.read_bytes()).hexdigest() for name, path in paths.items()}


def _write_new(path, value):
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
    fd = os.open(path, flags, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as stream:
        json.dump(value, stream, ensure_ascii=False, sort_keys=True, allow_nan=False)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())


def _ledger(root, update):
    path = root / "calibration-ledger.json"
    fd = os.open(root / "calibration-ledger.lock", os.O_RDWR | os.O_CREAT | getattr(os, "O_NOFOLLOW", 0), 0o600)
    with os.fdopen(fd, "r+", encoding="utf-8") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if path.is_symlink():
            raise ValueError("invalid calibration ledger")
        ledger = json.loads(path.read_text(encoding="utf-8")) if path.exists() else {"version": 1, "attempts": []}
        if (not isinstance(ledger, dict) or ledger.get("version") != 1
                or not isinstance(ledger.get("attempts"), list)
                or len(ledger["attempts"]) > LIMIT):
            raise ValueError("invalid calibration ledger")
        result = update(ledger["attempts"])
        temporary = root / (".ledger-" + uuid.uuid4().hex)
        try:
            _write_new(temporary, ledger)
            os.replace(temporary, path)
            directory = os.open(root, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
        finally:
            temporary.unlink(missing_ok=True)
        return result


def _claim(root, case_id):
    def claim(attempts):
        if len(attempts) >= LIMIT:
            raise ValueError("calibration limit of six attempts reached")
        attempt = {"id": uuid.uuid4().hex, "case_id": case_id, "status": "pending"}
        attempts.append(attempt)
        return attempt["id"]
    return _ledger(root, claim)


def _finish(root, attempt_id, status):
    def finish(attempts):
        matches = [entry for entry in attempts if entry.get("id") == attempt_id]
        if len(matches) != 1 or matches[0].get("status") != "pending":
            raise ValueError("invalid pending claim")
        matches[0]["status"] = status
    _ledger(root, finish)


def _validate_rates(value, provider, model):
    if not isinstance(value, dict):
        raise ValueError("rates JSON must contain rates and source")
    rates, source = value.get("rates"), value.get("source")
    sample = [{"info": {"role": "assistant", "providerID": provider, "modelID": model,
                        "tokens": {"input": 0, "output": 0, "reasoning": 0,
                                   "cache": {"read": 0, "write": 0}}, "cost": 0}}]
    accounting.aggregate(sample, provider, model, rates, source)
    return rates, source


def calibrate(snapshot_path, artifacts, model, case_ids, rates_path, timeout_seconds=180, *, repository=REPOSITORY):
    """Run requested calibration cases once each, charging every claimed attempt.

    Host capture limits do not account for all provider retries or calls.
    The artifact directory is private and must not be shared with blind reviewers.
    """
    if not isinstance(model, str) or model.count("/") != 1 or any(not p or p.strip() != p for p in model.split("/")):
        raise ValueError("model must be providerID/modelID")
    provider, model_id = model.split("/")
    if (not isinstance(case_ids, (list, tuple)) or not 1 <= len(case_ids) <= LIMIT
            or any(not isinstance(case, str) or case not in cases.CASES for case in case_ids)):
        raise ValueError("request must contain one to six known cases")
    if type(timeout_seconds) is not int or timeout_seconds < 1:
        raise ValueError("timeout must be a positive integer")
    snapshot_path = snapshots.outside_repository(snapshot_path, repository)
    rates_path = snapshots.outside_repository(rates_path, repository)
    root = snapshots.outside_repository(artifacts, repository)
    snapshot = snapshots.load(snapshot_path, repository)
    rates, source = _validate_rates(json.loads(rates_path.read_text(encoding="utf-8")), provider, model_id)
    if root.exists() and not root.is_dir():
        raise ValueError("artifacts must be a directory")
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    root.chmod(0o700)
    # Reject an over-budget request before any of its host sessions start.
    def available(attempts):
        if len(attempts) + len(case_ids) > LIMIT:
            raise ValueError("request exceeds remaining calibration attempts")
    _ledger(root, available)
    results = []
    for case_id in case_ids:
        # Re-verify immediately before each claim, not just once per request.
        before_snapshot = snapshots.load(snapshot_path, repository)
        if before_snapshot["snapshot_id"] != snapshot["snapshot_id"]:
            raise ValueError("snapshot changed before attempt")
        before_hashes = _hashes()
        attempt_id = _claim(root, case_id)
        started = time.monotonic()
        result = {"attempt_id": attempt_id, "case_id": case_id, "snapshot_id_before": snapshot["snapshot_id"],
                  "hashes_before": before_hashes,
                  "accounting_limit": "Host capture limits do not account for all provider retries or calls."}
        try:
            output = host.run_session(root / (attempt_id + "-host"), before_snapshot, model, case_id,
                                      timeout_seconds=timeout_seconds)
            for key in ("host_version", "identity", "capture_paths", "capture_stage", "compacted", "compaction_index"):
                result[key] = output[key]
            result["usage"] = accounting.aggregate(output["messages"], provider, model_id, rates, source)
            result["grade"] = evaluation.evaluate(case_id, output["events"], output["artifacts"], output["final_text"])
            result["snapshot_id_after"] = snapshots.load(snapshot_path, repository)["snapshot_id"]
            result["hashes_after"] = _hashes()
            if result["snapshot_id_after"] != result["snapshot_id_before"] or result["hashes_after"] != before_hashes:
                raise ValueError("snapshot or harness changed during attempt")
            packet = {"case": case_id, "events": output["events"], "artifacts": output["artifacts"],
                      "final_text": output["final_text"], "semantic_checks": cases.CASES[case_id]["semantic_checks"]}
            _write_new(root / (attempt_id + "-blind.json"), packet)
            result["status"] = "completed"
        except Exception as exc:
            result["status"] = "failed"
            # Frame names and line numbers aid diagnosis without paths, source or exception text.
            result["failure"] = {"type": type(exc).__name__, "message": "attempt failed; private details withheld",
                                 "traceback": [{"function": frame.name, "line": frame.lineno}
                                               for frame in traceback.extract_tb(exc.__traceback__)]}
            try:
                result["snapshot_id_after"] = snapshots.load(snapshot_path, repository)["snapshot_id"]
            except Exception:
                result["snapshot_id_after"] = None
            try:
                result["hashes_after"] = _hashes()
            except Exception:
                result["hashes_after"] = None
        result["elapsed_seconds"] = time.monotonic() - started
        _write_new(root / (attempt_id + "-result.json"), result)
        _finish(root, attempt_id, result["status"])
        results.append(result)
    return results


def estimate_artifacts(artifacts, proposed_cap, *, repository=REPOSITORY):
    """Reconcile ledger claims and available results before estimating."""
    try:
        root = snapshots.outside_repository(artifacts, repository)
    except ValueError as exc:
        raise ValueError("invalid artifact directory") from exc
    ledger = json.loads((root / "calibration-ledger.json").read_text(encoding="utf-8"))
    if not isinstance(ledger, dict) or ledger.get("version") != 1 or not isinstance(ledger.get("attempts"), list):
        raise ValueError("invalid calibration ledger")
    records = []
    ids = set()
    identities = set()
    pricing_sources = set()
    for entry in ledger["attempts"]:
        if not isinstance(entry, dict):
            raise ValueError("invalid ledger attempt")
        attempt_id = entry.get("id")
        if (not isinstance(attempt_id, str) or len(attempt_id) != 32
                or any(c not in "0123456789abcdef" for c in attempt_id) or attempt_id in ids
                or entry.get("status") not in ("pending", "failed", "completed")):
            raise ValueError("invalid ledger attempt")
        ids.add(attempt_id)
        path = root / (attempt_id + "-result.json")
        if path.exists():
            result = json.loads(path.read_text(encoding="utf-8"))
            if (not isinstance(result, dict) or result.get("attempt_id") != attempt_id
                    or result.get("case_id") != entry.get("case_id")
                    or result.get("status") not in ("completed", "failed")
                    or (entry["status"] != "pending" and result["status"] != entry["status"])):
                raise ValueError("result does not match ledger")
            if entry["status"] == "completed":
                identity, usage = result.get("identity"), result.get("usage")
                if (not isinstance(identity, dict) or
                        any(not isinstance(identity.get(key), str) or not identity[key].strip()
                            for key in ("providerID", "modelID"))):
                    raise ValueError("completed result must include provider and model identity")
                if not isinstance(usage, dict) or not isinstance(usage.get("pricing_source"), str) or not usage["pricing_source"].strip():
                    raise ValueError("completed result must include pricing_source")
                identities.add((identity["providerID"], identity["modelID"]))
                pricing_sources.add(usage["pricing_source"])
            records.append({**result, "status": entry["status"]})
        else:
            records.append(entry)
    if any(path.name != path.name[:32] + "-result.json" or path.name[:32] not in ids
           for path in root.glob("*-result.json")):
        raise ValueError("unclaimed result")
    if len(identities) > 1 or len(pricing_sources) > 1:
        raise ValueError("cannot combine different model identities or pricing sources")
    return estimate.estimate(records, proposed_cap=proposed_cap)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    run = sub.add_parser("calibrate")
    run.add_argument("--snapshot", required=True)
    run.add_argument("--artifacts", required=True)
    run.add_argument("--model", required=True)
    run.add_argument("--cases", nargs="+", required=True)
    run.add_argument("--rates-json", required=True)
    run.add_argument("--timeout-seconds", type=int, default=180)
    projection = sub.add_parser("estimate")
    projection.add_argument("--artifacts", required=True)
    projection.add_argument("--proposed-cap", type=float, required=True)
    args = parser.parse_args(argv)
    try:
        if args.command == "estimate":
            print(json.dumps(estimate_artifacts(args.artifacts, args.proposed_cap), allow_nan=False))
            return 0
        results = calibrate(args.snapshot, args.artifacts, args.model, args.cases, args.rates_json, args.timeout_seconds)
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        parser.exit(2, f"{args.command} refused: {type(exc).__name__}\n")
    except ValueError as exc:
        detail = str(exc) if args.command == "estimate" else type(exc).__name__
        parser.exit(2, f"{args.command} refused: {detail}\n")
    print(json.dumps({"attempts": [{"id": r["attempt_id"], "status": r["status"]} for r in results],
                      "warning": "Provider retries and calls outside host capture are not fully accounted."}))
    return 1 if any(r["status"] != "completed" for r in results) else 0


if __name__ == "__main__":
    raise SystemExit(main())
