"""Private, sequential, nominally budgeted instruction pilot."""

import fcntl
import hashlib
import json
import os
import secrets
import time
import uuid
from pathlib import Path

from . import budget, evaluation, host, plan, run, snapshots


def _hashes():
    result = run._hashes()
    result.update({"pilot": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
                   "budget": hashlib.sha256(Path(budget.__file__).read_bytes()).hexdigest()})
    return result


class Guard:
    def __init__(self, journal, attempt, model, limits, rates):
        self.journal, self.attempt = journal, attempt
        self.provider, self.model = model.split("/")
        self.limits, self.rates = limits, rates
        self.failed = False

    def reserve(self, value):
        try:
            if self.failed:
                raise ValueError("pilot guard stopped")
            message, model, assistant = value["message"], value["model"], value["assistant"]
            session = value["sessionID"]
            if (not isinstance(session, str) or not session or not isinstance(message.get("id"), str)
                    or assistant.get("sessionID") != session or assistant.get("parentID") != message["id"]
                    or assistant.get("providerID") != self.provider or assistant.get("modelID") != self.model
                    or assistant.get("time", {}).get("completed") or not isinstance(assistant.get("id"), str) or not assistant["id"]
                    or model.get("providerID") != self.provider or model.get("id") != self.model
                    or any(type(model.get("limit", {}).get(k)) is not int or model["limit"][k] != self.limits[k]
                           for k in ("context", "input", "output"))):
                raise ValueError("request identity or declared limits mismatch")
            return self.journal.reserve(self.attempt, assistant["id"], message["id"], session,
                                        budget.reservation(self.limits, self.rates))
        except Exception:
            self.failed = True
            raise

    def settle(self, info):
        try:
            if self.failed:
                raise ValueError("pilot guard stopped")
            return self.journal.settle(self.attempt, info, self.provider, self.model, self.limits, self.rates)
        except Exception:
            self.failed = True
            raise

    def verify(self):
        requests = self.journal.update(lambda data: [r for r in data["requests"] if r["attempt_id"] == self.attempt])
        latest = {r["assistant_id"]: r for r in requests}
        if self.failed or not latest or any(r["status"] != "settled" for r in latest.values()):
            raise ValueError("attempt has unresolved current usage")


def execute(baseline_path, candidate_path, artifacts, model, limits_path, rates_path,
            timeout_seconds=180, *, max_new_attempts=96, repository=run.REPOSITORY):
    if not isinstance(model, str) or model.count("/") != 1 or any(not p or p.strip() != p for p in model.split("/")):
        raise ValueError("model must be providerID/modelID")
    if type(timeout_seconds) is not int or timeout_seconds <= 0:
        raise ValueError("timeout must be a positive integer")
    if type(max_new_attempts) is not int or max_new_attempts <= 0:
        raise ValueError("max_new_attempts must be a positive integer")
    baseline_path, candidate_path, artifacts, limits_path, rates_path = [
        snapshots.outside_repository(p, repository) for p in (baseline_path, candidate_path, artifacts, limits_path, rates_path)]
    baseline, candidate = snapshots.load(baseline_path, repository), snapshots.load(candidate_path, repository)
    limits_data = json.loads(limits_path.read_text())
    if (not isinstance(limits_data, dict) or limits_data.get("model") != model
            or not isinstance(limits_data.get("source"), str) or not limits_data["source"].strip()):
        raise ValueError("limits require matching model and source")
    limits = limits_data.get("limits")
    rates, source = run._validate_rates(json.loads(rates_path.read_text()), *model.split("/"))
    reserve = budget.reservation(limits, rates)
    if reserve > budget.CAP:
        raise ValueError("one reservation exceeds the pilot ceiling")
    artifacts.mkdir(mode=0o700, parents=True, exist_ok=True)
    artifacts.chmod(0o700)
    lock_fd = os.open(artifacts / "pilot.lock", os.O_CREAT | os.O_RDWR | getattr(os, "O_NOFOLLOW", 0), 0o600)
    with os.fdopen(lock_fd, "r+") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        metadata = {"version": 1, "model": model, "limits": limits, "limits_source": limits_data["source"],
                    "rates": rates, "rate_source": source, "cap_usd": "10", "reservation_usd": str(reserve),
                    "snapshots": {"baseline": baseline["snapshot_id"], "candidate": candidate["snapshot_id"]},
                    "hashes": _hashes(), "timeout_seconds": timeout_seconds}
        meta_path, schedule_path, key_path = [artifacts / n for n in ("pilot-metadata.json", "pilot-schedule.json", "pilot-key.json")]
        journal = budget.Journal(artifacts)
        if not meta_path.exists():
            if any(p.name != "pilot.lock" for p in artifacts.iterdir()):
                raise ValueError("pilot initialization requires an empty directory")
            schedule, key = plan.pilot_plan(secrets.randbits(256) | (1 << 255), baseline, candidate)
            run._write_new(schedule_path, schedule)
            run._write_new(key_path, key)
            journal.create()
            run._write_new(meta_path, metadata)
            for path in (schedule_path, key_path, meta_path):
                path.chmod(0o400)
        else:
            if any(p.is_symlink() for p in (meta_path, schedule_path, key_path)):
                raise ValueError("invalid pilot files")
            if json.loads(meta_path.read_text()) != metadata:
                raise ValueError("pilot identity changed")
            schedule, key = json.loads(schedule_path.read_text()), json.loads(key_path.read_text())
            expected, expected_key = plan.pilot_plan(key["seed"], baseline, candidate)
            if schedule != expected or key != expected_key:
                raise ValueError("pilot schedule or key changed")
        results = []
        prior = journal.update(lambda data: list(data["attempts"]))
        row_ids = {row["blind_id"] for row in schedule["rows"]}
        claimed = [entry.get("blind_id") for entry in prior]
        if any(row_id not in row_ids for row_id in claimed) or len(set(claimed)) != len(claimed):
            raise ValueError("journal does not match the pilot schedule")
        if any(entry.get("status") != "completed" for entry in prior):
            return results
        for row in schedule["rows"]:
            if len(results) >= max_new_attempts:
                break
            if (any(p.is_symlink() for p in (meta_path, schedule_path, key_path))
                    or json.loads(meta_path.read_text()) != metadata
                    or json.loads(schedule_path.read_text()) != schedule
                    or json.loads(key_path.read_text()) != key):
                raise ValueError("pilot metadata or schedule changed")
            if _hashes() != metadata["hashes"] or snapshots.load(baseline_path, repository)["snapshot_id"] != baseline["snapshot_id"] or snapshots.load(candidate_path, repository)["snapshot_id"] != candidate["snapshot_id"]:
                raise ValueError("snapshot or harness changed")
            attempt_id = uuid.uuid4().hex
            def claim(data):
                if any(a.get("blind_id") == row["blind_id"] for a in data["attempts"]):
                    return False
                data["attempts"].append({"id": attempt_id, "blind_id": row["blind_id"], "status": "pending"})
                return True
            if not journal.update(claim):
                continue
            arm = key["arms"][row["blind_id"]]
            guard = Guard(journal, attempt_id, model, limits, rates)
            result = {"attempt_id": attempt_id, "blind_id": row["blind_id"], "case_id": row["case_id"], "status": "failed"}
            started = time.monotonic()
            try:
                output = host.run_session(artifacts / (attempt_id + "-host"), {"baseline": baseline, "candidate": candidate}[arm],
                                          model, row["case_id"], timeout_seconds=timeout_seconds, guard=guard)
                guard.verify()
                if _hashes() != metadata["hashes"] or snapshots.load(baseline_path, repository)["snapshot_id"] != baseline["snapshot_id"] or snapshots.load(candidate_path, repository)["snapshot_id"] != candidate["snapshot_id"]:
                    raise ValueError("snapshot or harness changed")
                result["grade"] = evaluation.evaluate(row["case_id"], output["events"], output["artifacts"], output["final_text"])
                result["identity"] = output["identity"]
                run._write_new(artifacts / (attempt_id + "-blind.json"),
                               run._blind_packet(row["case_id"], output, artifacts / (attempt_id + "-host")))
                result["status"] = "completed"
            except Exception as exc:
                guard.failed = True
                result["failure"] = type(exc).__name__
            result["elapsed_seconds"] = time.monotonic() - started
            run._write_new(artifacts / (attempt_id + "-result.json"), result)
            def finish(data):
                entry = next(a for a in data["attempts"] if a["id"] == attempt_id)
                if entry["status"] != "pending":
                    raise ValueError("invalid pending attempt")
                entry["status"] = result["status"]
            journal.update(finish)
            results.append(result)
            if result["status"] != "completed":
                break
        return results
