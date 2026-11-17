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
            json.dump({"version": 1, "attempts": [], "requests": []}, stream)
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
            if (not isinstance(data, dict) or data.get("version") != 1
                    or not isinstance(data.get("attempts"), list) or not isinstance(data.get("requests"), list)):
                raise ValueError("invalid journal")
            attempts, requests = data["attempts"], data["requests"]
            if (len(attempts) > 96 or any(not isinstance(a, dict) or not isinstance(a.get("id"), str)
                    or a.get("status") not in ("pending", "completed", "failed") for a in attempts)
                    or len({a["id"] for a in attempts}) != len(attempts)):
                raise ValueError("invalid journal attempts")
            attempt_ids = {a["id"] for a in attempts}
            charges = []
            for ordinal, entry in enumerate(requests, 1):
                if (not isinstance(entry, dict) or entry.get("ordinal") != ordinal
                        or entry.get("attempt_id") not in attempt_ids
                        or entry.get("status") not in ("reserved", "settled")
                        or any(not isinstance(entry.get(key), str) or not entry[key] for key in ("assistant_id", "user_id", "session_id"))):
                    raise ValueError("invalid journal request")
                charges.append(_decimal(entry.get("charge")))
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

    def reserve(self, attempt_id, assistant_id, user_id, session_id, amount):
        def apply(data):
            if not isinstance(amount, Decimal) or not amount.is_finite() or amount <= 0:
                raise ValueError("invalid reservation")
            if not any(a.get("id") == attempt_id and a.get("status") == "pending" for a in data["attempts"]):
                raise ValueError("attempt is not pending")
            if any(r.get("attempt_id") == attempt_id and r.get("assistant_id") == assistant_id and r.get("status") == "settled" for r in data["requests"]):
                raise ValueError("assistant has already completed")
            if any(Decimal(r["charge"]) != amount for r in data["requests"] if r["status"] == "reserved"):
                raise ValueError("reservation amount changed")
            used = sum(Decimal(r["charge"]) for r in data["requests"])
            if used + amount > CAP:
                raise ValueError("nominal budget exhausted")
            ordinal = len(data["requests"]) + 1
            data["requests"].append({"ordinal": ordinal, "attempt_id": attempt_id, "assistant_id": assistant_id,
                                     "user_id": user_id, "session_id": session_id, "status": "reserved", "charge": str(amount)})
            return ordinal
        return self.update(apply)

    def settle(self, attempt_id, info, provider, model, limits, rates):
        charge = normalized_charge(info, provider, model, limits, rates)
        def apply(data):
            if not any(a.get("id") == attempt_id and a.get("status") == "pending" for a in data["attempts"]):
                raise ValueError("attempt is not pending")
            matches = [r for r in data["requests"] if r["attempt_id"] == attempt_id and r["assistant_id"] == info.get("id")]
            if not matches or matches[-1]["status"] != "reserved":
                raise ValueError("no unique unsettled request")
            latest = matches[-1]
            if (latest["user_id"] != info.get("parentID") or latest["session_id"] != info.get("sessionID")
                    or charge > Decimal(latest["charge"])):
                raise ValueError("usage does not match request reservation")
            latest["status"] = "settled"
            latest["charge"] = str(charge)
            return latest["ordinal"]
        return self.update(apply)
