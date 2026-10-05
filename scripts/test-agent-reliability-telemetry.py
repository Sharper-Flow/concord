#!/usr/bin/env python3
"""Tests for scripts/agent-reliability-telemetry.py."""

from __future__ import annotations

import importlib.util
import json
import sqlite3
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("agent-reliability-telemetry.py")
SPEC = importlib.util.spec_from_file_location("agent_reliability_telemetry", SCRIPT)
assert SPEC and SPEC.loader
telemetry = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(telemetry)

NOW_MS = 1_800_000_000_000  # far above the seconds threshold the script detects


def tool_part(tool: str, status: str, output: str = "", subagent_type: str = "", model: str = "glm-5.3-flash") -> tuple[str, str]:
    # The shapes OpenCode stores: a tool part's state carries its status and
    # output, and the assistant message that owns the part names its serving
    # model in top-level providerID and modelID fields.
    state: dict = {"status": status, "output": output}
    if subagent_type:
        state["input"] = {"subagent_type": subagent_type}
    part = {"type": "tool", "tool": tool, "state": state, "time": {"start": NOW_MS}}
    message = {"role": "assistant", "providerID": "zai-coding-plan", "modelID": model}
    return json.dumps(part), json.dumps(message)


def envelope(outcome: str, kind: str = "", message: str = "") -> str:
    # A Concord tool answers a refusal as a completed call whose output is the
    # result envelope; the refusal kind is error.kind.
    document: dict = {"schema_version": "1.0", "outcome": outcome}
    if kind:
        document["error"] = {"kind": kind, "retry_safe": False, "message": message}
    return json.dumps(document)


def refusal(tool: str, kind: str, message: str = "", model: str = "glm-5.3-flash") -> tuple[str, str]:
    return tool_part(tool, "completed", envelope("error", kind, message), model=model)


class AgentReliabilityTelemetryTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.shard = Path(self.tempdir.name)
        self.connection = sqlite3.connect(self.shard / "opencode.db")
        self.connection.executescript(
            "CREATE TABLE part (id text PRIMARY KEY, message_id text NOT NULL, time_created integer NOT NULL, data text NOT NULL);"
            "CREATE TABLE message (id text PRIMARY KEY, time_created integer NOT NULL, data text NOT NULL);"
        )
        self.counter = 0

    def tearDown(self) -> None:
        self.connection.close()
        self.tempdir.cleanup()

    def add(self, part_data: str, message_data: str) -> None:
        self.counter += 1
        self.connection.execute(
            "INSERT INTO message (id, time_created, data) VALUES (?, ?, ?)",
            (f"msg-{self.counter}", NOW_MS, message_data),
        )
        self.connection.execute(
            "INSERT INTO part (id, message_id, time_created, data) VALUES (?, ?, ?, ?)",
            (f"part-{self.counter}", f"msg-{self.counter}", NOW_MS, part_data),
        )

    def document(self) -> dict:
        self.connection.commit()
        return telemetry.measure(self.shard, 21)

    def test_invalid_input_counts_per_tool_and_model(self) -> None:
        for _ in range(3):
            self.add(*refusal("concord_work_transition", "invalid_input", "missing payload field reason"))
        self.add(*tool_part("concord_work_transition", "completed", envelope("ok")))
        self.add(*refusal("concord_work_transition", "version_conflict", model="gpt-6.1-sol"))
        document = self.document()
        bucket = document["measures"]["invalid_input"]["concord_work_transition"]
        self.assertEqual(bucket["calls"], 5)
        self.assertEqual(bucket["invalid_input"], 3)
        self.assertEqual(bucket["rate"], 0.6)
        self.assertEqual(bucket["by_model"]["glm-5.3-flash"]["calls"], 4)
        self.assertEqual(bucket["by_model"]["glm-5.3-flash"]["invalid_input"], 3)
        self.assertEqual(bucket["by_model"]["gpt-6.1-sol"]["invalid_input"], 0)

    def test_non_invalid_refusals_do_not_count(self) -> None:
        self.add(*refusal("concord_work_browse", "unknown_scope", "work reference is not in Product scope"))
        document = self.document()
        bucket = document["measures"]["invalid_input"]["concord_work_browse"]
        self.assertEqual(bucket["calls"], 1)
        self.assertEqual(bucket["invalid_input"], 0)

    def test_refusal_kind_comes_from_the_envelope_not_its_text(self) -> None:
        # A successful result may quote refusal vocabulary in its payload, and
        # a host failure has no envelope; neither is an invalid_input refusal.
        self.add(*tool_part("concord_work_browse", "completed", json.dumps({"outcome": "ok", "result": {"title": "missing payload field invalid_input"}})))
        self.add(*tool_part("concord_work_define", "error", "undefined is not an object (evaluating 'args.operation')"))
        self.add(*tool_part("concord_ci_watch", "completed", json.dumps({"status": "started"})))
        self.add(*tool_part("concord_work_define", "completed", "not an envelope: invalid_input"))
        document = self.document()
        for tool in ("concord_work_browse", "concord_work_define", "concord_ci_watch"):
            self.assertEqual(document["measures"]["invalid_input"][tool]["invalid_input"], 0, tool)

    def test_serving_model_is_read_from_the_assistant_message(self) -> None:
        self.add(*refusal("concord_work_define", "invalid_input", model="gpt-6.1-sol"))
        bucket = self.document()["measures"]["invalid_input"]["concord_work_define"]
        self.assertEqual(list(bucket["by_model"]), ["gpt-6.1-sol"])

    def test_execute_guess_classification(self) -> None:
        self.add(*tool_part("execute", "error", 'tools.codemode: "unknown tool concord_work_trace.research"'))
        self.add(*tool_part("execute", "error", 'json: unknown field "budget"'))
        self.add(*tool_part("execute", "error", "transport_failure: the binary is missing"))
        self.add(*tool_part("execute", "error", "[codemode-signature-guard] execute script refused: it calls tool path(s) this session has not seen a signature for: linear.get_issue."))
        self.add(*tool_part("execute", "completed", ""))
        document = self.document()
        execute = document["measures"]["execute"]
        self.assertEqual(execute["calls"], 5)
        self.assertEqual(execute["errors"], 4)
        self.assertEqual(execute["guessed"], 3)

    def test_subagent_use_counts_task_calls_per_type(self) -> None:
        self.add(*tool_part("task", "completed", subagent_type="concord-explore"))
        self.add(*tool_part("task", "completed", subagent_type="concord-explore"))
        self.add(*tool_part("task", "completed", subagent_type="concord-lookup"))
        document = self.document()
        self.assertEqual(document["measures"]["subagent_use"], {"concord-explore": 2, "concord-lookup": 1})

    def test_window_excludes_older_parts(self) -> None:
        self.add(*refusal("concord_work_define", "invalid_input", "inside the window"))
        self.connection.execute(
            "INSERT INTO message (id, time_created, data) VALUES ('msg-old', ?, ?)",
            (NOW_MS - 40 * 86400 * 1000, json.dumps({"role": "assistant", "providerID": "z", "modelID": "glm-5.3-flash"})),
        )
        old_part = json.dumps({"type": "tool", "tool": "concord_work_define", "state": {"status": "completed", "output": envelope("error", "invalid_input", "outside the window")}, "time": {"start": NOW_MS - 40 * 86400 * 1000}})
        self.connection.execute(
            "INSERT INTO part (id, message_id, time_created, data) VALUES ('part-old', 'msg-old', ?, ?)",
            (NOW_MS - 40 * 86400 * 1000, old_part),
        )
        document = self.document()
        bucket = document["measures"]["invalid_input"]["concord_work_define"]
        self.assertEqual(bucket["calls"], 1)
        self.assertEqual(bucket["invalid_input"], 1)

    def test_baseline_round_trip_and_regression(self) -> None:
        for _ in range(2):
            self.add(*refusal("concord_work_define", "invalid_input", "missing payload field reason"))
        self.add(*tool_part("concord_work_define", "completed", envelope("ok")))
        baseline_path = self.shard / "baseline.json"
        document = self.document()
        baseline_path.write_text(json.dumps(document), encoding="utf-8")
        self.assertEqual(telemetry.compare(document, baseline_path, 0.0), [])
        # One more refusal on the same population raises the rate above baseline.
        self.add(*refusal("concord_work_define", "invalid_input", "missing payload field reason"))
        regressed = self.document()
        findings = telemetry.compare(regressed, baseline_path, 0.0)
        self.assertTrue(findings and "concord_work_define" in findings[0])


if __name__ == "__main__":
    unittest.main()
