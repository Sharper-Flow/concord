"""Durable nominal reservations for a single private pilot journal."""

import fcntl
import json
import os
import uuid
from decimal import Decimal, InvalidOperation
from pathlib import Path


CAP = Decimal("10")


def _decimal(value):
    try:
        result = Decimal(str(value))
    except (InvalidOperation, ValueError) as exc:
        raise ValueError("invalid rate") from exc
    if not result.is_finite() or result <= 0:
        raise ValueError("rates must be positive")
    return result


def reservation(limits, rates):
    if not isinstance(limits, dict) or any(type(limits.get(k)) is not int or limits[k] <= 0 for k in ("context", "input", "output")):
        raise ValueError("positive integer model limits required")
    if limits["input"] > limits["context"]:
        raise ValueError("input limit exceeds context")
    if not isinstance(rates, dict) or set(rates) != {"input", "output", "cache_read", "cache_write"}:
        raise ValueError("four nominal rates required")
    prices = {key: _decimal(value) for key, value in rates.items()}
    return (limits["context"] * max(prices["input"], prices["cache_read"], prices["cache_write"])
            + limits["output"] * prices["output"]) / 1_000_000


def normalized_charge(info, provider, model, limits, rates):
    if isinstance(info, dict) and info.get("type") in ("assistant", "compaction"):
        model_ref = info.get("model") or {}
        if info["type"] == "compaction":
            if info.get("status") != "completed" or not info.get("summary"):
                raise ValueError("compaction completion is not verified")
        elif not info.get("time", {}).get("completed") or not info.get("finish") or info.get("error"):
            raise ValueError("assistant completion is not verified")
        info = {**info, "role": "assistant", "providerID": model_ref.get("providerID"),
                "modelID": model_ref.get("id"), "finish": info.get("finish", "stop"),
                "time": {"completed": info.get("time", {}).get("completed", 1)}}
    if (not isinstance(info, dict) or info.get("role") != "assistant" or info.get("error") is not None
            or info.get("providerID") != provider or info.get("modelID") != model
            or not isinstance(info.get("finish"), str) or not info["finish"] or info["finish"] == "error"
            or not isinstance(info.get("time"), dict) or type(info["time"].get("completed")) is not int
            or info["time"]["completed"] <= 0):
        raise ValueError("assistant completion is not verified")
    try:
        tokens = info["tokens"]
        counts = {"input": tokens["input"], "output": tokens["output"], "reasoning": tokens["reasoning"],
                  "cache_read": tokens["cache"]["read"], "cache_write": tokens["cache"]["write"]}
    except (KeyError, TypeError) as exc:
        raise ValueError("missing normalized usage") from exc
    if any(type(n) is not int or n < 0 for n in counts.values()):
        raise ValueError("invalid normalized usage")
    if (counts["input"] <= 0
            or counts["output"] + counts["reasoning"] <= 0
            or sum(counts[k] for k in ("input", "cache_read", "cache_write")) > limits["context"]
            or counts["output"] + counts["reasoning"] > limits["output"]):
        raise ValueError("anomalous normalized usage")
    prices = {key: _decimal(value) for key, value in rates.items()}
    return (sum(Decimal(counts[k]) * prices[k] for k in ("input", "cache_read", "cache_write"))
            + Decimal(counts["output"] + counts["reasoning"]) * prices["output"]) / 1_000_000


class Journal:
    """All mutations, including admission, share one lock and durable journal."""

    def __init__(self, root):
        self.root = Path(root)

    def create(self):
        path = self.root / "pilot-journal.json"
        flags = os.O_CREAT | os.O_EXCL | os.O_WRONLY | getattr(os, "O_NOFOLLOW", 0)
        with os.fdopen(os.open(path, flags, 0o600), "w") as stream:
            json.dump({"version": 2, "attempts": [], "requests": []}, stream)
            stream.flush()
            os.fsync(stream.fileno())

    def update(self, operation):
        fd = os.open(self.root / "pilot-journal.lock", os.O_CREAT | os.O_RDWR | getattr(os, "O_NOFOLLOW", 0), 0o600)
        with os.fdopen(fd, "r+") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            path = self.root / "pilot-journal.json"
            if path.is_symlink():
                raise ValueError("invalid journal")
            data = json.loads(path.read_text())
            if (not isinstance(data, dict) or data.get("version") != 2
                    or not isinstance(data.get("attempts"), list) or not isinstance(data.get("requests"), list)):
                raise ValueError("invalid journal")
            attempts, requests = data["attempts"], data["requests"]
            if (len(attempts) > 96 or any(not isinstance(a, dict) or not isinstance(a.get("id"), str)
                    or a.get("status") not in ("pending", "completed", "failed") for a in attempts)
                    or len({a["id"] for a in attempts}) != len(attempts)):
                raise ValueError("invalid journal attempts")
            attempt_ids = {a["id"] for a in attempts}
            charges = []
            groups = {}
            for ordinal, entry in enumerate(requests, 1):
                if (not isinstance(entry, dict) or entry.get("ordinal") != ordinal
                        or entry.get("attempt_id") not in attempt_ids
                        or entry.get("status") not in ("reserved", "settled")
                        or entry.get("kind") not in ("context", "compaction")
                        or any(not isinstance(entry.get(key), str) or not entry[key] for key in ("group_id", "session_id"))
                        or (entry["status"] == "settled" and (not isinstance(entry.get("subject_id"), str) or not entry["subject_id"]))
                        or (entry["status"] == "reserved" and entry.get("subject_id") is not None)):
                    raise ValueError("invalid journal request")
                try:
                    charge = Decimal(entry.get("charge"))
                except (InvalidOperation, TypeError, ValueError) as exc:
                    raise ValueError("invalid journal charge") from exc
                if not charge.is_finite() or charge < 0 or (entry["status"] == "reserved" and charge == 0):
                    raise ValueError("invalid journal charge")
                group_key = (entry["session_id"], entry["group_id"])
                identity = (entry["attempt_id"], entry["kind"], entry["status"], entry["subject_id"])
                if group_key in groups and groups[group_key] != identity:
                    raise ValueError("inconsistent journal group")
                groups[group_key] = identity
                charges.append(charge)
            open_sessions = [session for (session, _), identity in groups.items() if identity[2] == "reserved"]
            if len(open_sessions) != len(set(open_sessions)):
                raise ValueError("multiple open groups in one session")
            if sum(charges) > CAP:
                raise ValueError("invalid journal total")
            result = operation(data)
            temp = self.root / (".journal-" + uuid.uuid4().hex)
            flags = os.O_CREAT | os.O_EXCL | os.O_WRONLY | getattr(os, "O_NOFOLLOW", 0)
            try:
                with os.fdopen(os.open(temp, flags, 0o600), "w") as stream:
                    json.dump(data, stream, sort_keys=True, allow_nan=False)
                    stream.flush()
                    os.fsync(stream.fileno())
                os.replace(temp, path)
                directory = os.open(self.root, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
                try:
                    os.fsync(directory)
                finally:
                    os.close(directory)
            finally:
                temp.unlink(missing_ok=True)
            return result

    def reserve(self, attempt_id, session_id, kind, group_id, amount):
        def apply(data):
            if not isinstance(amount, Decimal) or not amount.is_finite() or amount <= 0:
                raise ValueError("invalid reservation")
            if not any(a.get("id") == attempt_id and a.get("status") == "pending" for a in data["attempts"]):
                raise ValueError("attempt is not pending")
            if (kind not in ("context", "compaction") or not isinstance(session_id, str) or not session_id
                    or not isinstance(group_id, str) or not group_id):
                raise ValueError("invalid reservation identity")
            matches = [r for r in data["requests"] if r["session_id"] == session_id]
            if any(r["group_id"] == group_id and r["status"] == "settled" for r in matches):
                raise ValueError("group already settled")
            if any(r["status"] == "reserved" and (r["group_id"] != group_id or r["kind"] != kind
                   or r["attempt_id"] != attempt_id) for r in matches):
                raise ValueError("previous group not settled")
            used = sum(Decimal(r["charge"]) for r in data["requests"])
            if used + amount > CAP:
                raise ValueError("nominal budget exhausted")
            ordinal = len(data["requests"]) + 1
            data["requests"].append({"ordinal": ordinal, "attempt_id": attempt_id, "session_id": session_id,
                                     "kind": kind, "group_id": group_id, "subject_id": None,
                                     "status": "reserved", "charge": str(amount)})
            return ordinal
        return self.update(apply)

    def settle(self, attempt_id, session_id, kind, group_id, info, provider, model, limits, rates):
        def apply(data):
            if not any(a.get("id") == attempt_id and a.get("status") == "pending" for a in data["attempts"]):
                raise ValueError("attempt is not pending")
            matches = [r for r in data["requests"] if r["attempt_id"] == attempt_id and r["session_id"] == session_id
                       and r["kind"] == kind and r["group_id"] == group_id and r["status"] == "reserved"]
            if (not matches or not isinstance(info, dict) or not isinstance(info.get("id"), str) or not info["id"]
                    or info.get("type") != ("assistant" if kind == "context" else "compaction")):
                raise ValueError("usage does not match request reservation")
            aggregate_limits = {key: limits[key] * len(matches) for key in ("context", "input", "output")}
            charge = normalized_charge(info, provider, model, aggregate_limits, rates)
            if charge > sum(Decimal(r["charge"]) for r in matches):
                raise ValueError("usage does not match request reservation")
            for entry in matches:
                entry.update(status="settled", subject_id=info["id"], charge="0")
            matches[-1]["charge"] = str(charge)
            return matches[-1]["ordinal"]
        return self.update(apply)
