#!/usr/bin/env python3
"""Compute the agent-reliability measures from one OpenCode shard.

The baseline measures this script computes are the ones the 2026-10-02
agent-reliability program was diagnosed from:

  invalid_input   concord tool calls refused with kind invalid_input, per tool
                  and per serving model
  execute_guess   execute (Code Mode) calls that failed on a guessed tool name
                  or argument
  subagent_use    Task calls per subagent type

The shard is the OpenCode data directory that holds `opencode.db`. The script
opens that database read-only, so a live shard is safe to measure. Output is
aggregate counts and rates only: no prompt text, no paths, no session
identifiers.

Usage:
  agent-reliability-telemetry.py --shard <dir> [--window-days 21]
  agent-reliability-telemetry.py --shard <dir> --baseline <file> --write-baseline
  agent-reliability-telemetry.py --shard <dir> --baseline <file> [--tolerance 0.0]

Compare mode exits 1 when any rate measure is worse than the recorded
baseline by more than the tolerance (absolute rate points).
"""
from __future__ import annotations

import argparse
import json
import re
import sqlite3
import sys
from pathlib import Path

WINDOW_DAYS_DEFAULT = 21
# Signatures an execute failure carries when the script guessed a tool name or
# argument shape instead of copying a seen signature.
GUESS_PATTERNS = (
    re.compile(r"unknown tool", re.IGNORECASE),
    # The Code Mode signature guard refuses a script that calls a tool path the
    # session never discovered, which is a guessed tool name refused early.
    re.compile(r"has not seen a signature for", re.IGNORECASE),
    re.compile(r"unknown field", re.IGNORECASE),
    re.compile(r"not a function", re.IGNORECASE),
    re.compile(r"is not defined", re.IGNORECASE),
    re.compile(r"does not exist", re.IGNORECASE),
    re.compile(r"invalid input", re.IGNORECASE),
    re.compile(r"unexpected", re.IGNORECASE),
)


def open_shard(shard: Path) -> sqlite3.Connection:
    database = shard / "opencode.db"
    if not database.is_file():
        raise SystemExit(f"agent-reliability-telemetry: no shard database at {database}")
    connection = sqlite3.connect(f"file:{database}?mode=ro", uri=True)
    connection.row_factory = sqlite3.Row
    return connection


def epoch_seconds(value: int) -> int:
    # OpenCode stores epoch milliseconds; a seconds-precision store still
    # rounds into the same window arithmetic.
    return value // 1000 if value > 10**12 else value


def tool_parts(connection: sqlite3.Connection, window_start: int):
    rows = connection.execute(
        "SELECT p.data AS data, m.data AS message_data FROM part p "
        "JOIN message m ON m.id = p.message_id "
        "WHERE json_extract(p.data, '$.type') = 'tool'"
    )
    for row in rows:
        part = json.loads(row["data"])
        message = json.loads(row["message_data"]) if row["message_data"] else {}
        created = part.get("time", {}).get("start")
        if created is None:
            state_time = (part.get("state") or {}).get("time") or {}
            created = state_time.get("start")
        if isinstance(created, (int, float)) and epoch_seconds(int(created)) < window_start:
            continue
        yield part, message


def refusal_kind(state: dict) -> str | None:
    # A Concord tool answers a refusal as a completed call whose output is the
    # result envelope, so the refusal kind is the envelope's error.kind. A host
    # failure carries no envelope and therefore no refusal kind.
    output = state.get("output")
    if state.get("status") != "completed" or not isinstance(output, str):
        return None
    try:
        document, _ = json.JSONDecoder().raw_decode(output.lstrip())
    except json.JSONDecodeError:
        return None
    if not isinstance(document, dict) or document.get("outcome") != "error":
        return None
    error = document.get("error")
    return error.get("kind") if isinstance(error, dict) else None


def classify(state_output: object) -> str:
    text = state_output if isinstance(state_output, str) else json.dumps(state_output)
    for pattern in GUESS_PATTERNS:
        if pattern.search(text):
            return "guessed"
    return "other_error"


def measure(shard: Path, window_days: int) -> dict:
    connection = open_shard(shard)
    try:
        newest = connection.execute("SELECT max(time_created) FROM part").fetchone()[0] or 0
        window_start = epoch_seconds(int(newest)) - window_days * 86400
        invalid_input: dict[str, dict] = {}
        execute = {"calls": 0, "errors": 0, "guessed": 0}
        subagent_use: dict[str, int] = {}
        for part, message in tool_parts(connection, window_start):
            tool = part.get("tool") or ""
            state = part.get("state") or {}
            status = state.get("status")
            # The assistant message that owns a tool part names its serving
            # model in a top-level modelID field.
            model = message.get("modelID") or "unknown"
            if tool.startswith("concord_"):
                bucket = invalid_input.setdefault(tool, {"calls": 0, "invalid_input": 0, "calls_by_model": {}, "invalid_by_model": {}})
                bucket["calls"] += 1
                bucket["calls_by_model"][model] = bucket["calls_by_model"].get(model, 0) + 1
                if refusal_kind(state) == "invalid_input":
                    bucket["invalid_input"] += 1
                    bucket["invalid_by_model"][model] = bucket["invalid_by_model"].get(model, 0) + 1
            elif tool == "execute":
                execute["calls"] += 1
                if status == "error":
                    execute["errors"] += 1
                    if classify(state.get("output") or state.get("error")) == "guessed":
                        execute["guessed"] += 1
            elif tool == "task":
                subagent_type = (state.get("input") or {}).get("subagent_type") or "unknown"
                subagent_use[subagent_type] = subagent_use.get(subagent_type, 0) + 1
    finally:
        connection.close()
    for bucket in invalid_input.values():
        calls = bucket["calls"]
        bucket["rate"] = round(bucket["invalid_input"] / calls, 4) if calls else 0.0
        # Per-model rates draw both terms from the same population: the
        # refusals and the calls a model served are counted on the same parts.
        bucket["by_model"] = {
            model: {
                "calls": bucket["calls_by_model"].get(model, 0),
                "invalid_input": bucket["invalid_by_model"].get(model, 0),
                "rate": round(bucket["invalid_by_model"].get(model, 0) / bucket["calls_by_model"][model], 4) if bucket["calls_by_model"].get(model) else 0.0,
            }
            for model in sorted(set(bucket["calls_by_model"]) | set(bucket["invalid_by_model"]))
        }
        del bucket["calls_by_model"]
        del bucket["invalid_by_model"]
    total_execute = execute["calls"]
    execute["error_rate"] = round(execute["errors"] / total_execute, 4) if total_execute else 0.0
    execute["guess_rate"] = round(execute["guessed"] / total_execute, 4) if total_execute else 0.0
    return {
        "measures": {"invalid_input": dict(sorted(invalid_input.items())), "execute": execute, "subagent_use": dict(sorted(subagent_use.items()))},
        "window_days": window_days,
    }


def write_baseline(document: dict, baseline: Path) -> None:
    baseline.write_text(json.dumps(document, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def compare(document: dict, baseline: Path, tolerance: float) -> list[str]:
    recorded = json.loads(baseline.read_text(encoding="utf-8"))
    findings: list[str] = []
    for tool, bucket in document["measures"]["invalid_input"].items():
        recorded_bucket = recorded["measures"]["invalid_input"].get(tool)
        if recorded_bucket is None:
            continue
        if bucket["rate"] > recorded_bucket["rate"] + tolerance:
            findings.append(f"invalid_input {tool} rate {bucket['rate']} above baseline {recorded_bucket['rate']}")
    execute = document["measures"]["execute"]
    recorded_execute = recorded["measures"]["execute"]
    if execute["guess_rate"] > recorded_execute.get("guess_rate", 0.0) + tolerance:
        findings.append(f"execute guess rate {execute['guess_rate']} above baseline {recorded_execute.get('guess_rate')}")
    return findings


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--shard", type=Path, required=True, help="OpenCode data directory holding opencode.db")
    parser.add_argument("--window-days", type=int, default=WINDOW_DAYS_DEFAULT)
    parser.add_argument("--baseline", type=Path, help="baseline document to write or compare against")
    parser.add_argument("--write-baseline", action="store_true", help="record the measured document as the baseline")
    parser.add_argument("--tolerance", type=float, default=0.0, help="absolute rate points a measure may exceed the baseline by")
    arguments = parser.parse_args()
    document = measure(arguments.shard, arguments.window_days)
    if arguments.write_baseline:
        if arguments.baseline is None:
            parser.error("--write-baseline requires --baseline")
        write_baseline(document, arguments.baseline)
        print(f"baseline recorded: {arguments.baseline}")
        return 0
    if arguments.baseline is not None and arguments.baseline.is_file():
        findings = compare(document, arguments.baseline, arguments.tolerance)
        for finding in findings:
            print(f"agent-reliability regression: {finding}")
        if findings:
            return 1
    print(json.dumps(document, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
