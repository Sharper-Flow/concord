"""Negative tests for the observation evaluator; these are not agent evidence."""
import copy
import json
import unittest

from capture_evaluation import evaluate


class EvaluationTests(unittest.TestCase):
    def setUp(self):
        self.args = {
            "title": "Synthetic parser repair", "kind": "bug",
            "task": "Fix the synthetic parser defect with regression coverage",
            "value_statement": "Repair the parser", "idempotency_key": "capture-parser",
        }
        self.result = {"outcome": "ok", "work_id": "synthetic-work"}
        self.calls = [{"tool": "concord_work_start", "args": self.args, "result": self.result}]
        self.events = [
            {"type": "tool_use", "part": {"tool": "concord_work_start", "state": {
                "status": "completed", "input": self.args, "output": json.dumps(self.result),
            }}},
            {"type": "text", "part": {"text": json.dumps({"status": "completed", "work_id": "synthetic-work"})}},
            {"type": "step_finish", "part": {"reason": "stop"}},
        ]

    def test_valid_observation(self):
        self.assertTrue(evaluate(self.calls, self.events, 0)["passed"])

    def test_changed_scope(self):
        for key in ("title", "kind", "task"):
            with self.subTest(key=key):
                old = self.args[key]
                self.args[key] = "different scope"
                self.assertFalse(evaluate(self.calls, self.events, 0)["passed"])
                self.args[key] = old

    def test_failed_capture_before_success(self):
        failed = copy.deepcopy(self.calls[0])
        failed["result"] = {"outcome": "error"}
        self.calls.insert(0, failed)
        self.events.insert(0, copy.deepcopy(self.events[0]))
        self.assertFalse(evaluate(self.calls, self.events, 0)["passed"])

    def test_error_event(self):
        self.events[0]["part"]["state"]["status"] = "error"
        self.assertFalse(evaluate(self.calls, self.events, 0)["passed"])

    def test_changed_final_response(self):
        self.events[1]["part"]["text"] = json.dumps({"status": "needs_operator", "work_id": "synthetic-work"})
        self.assertFalse(evaluate(self.calls, self.events, 0)["passed"])

    def test_missing_final_response(self):
        del self.events[1]
        self.assertFalse(evaluate(self.calls, self.events, 0)["passed"])

    def test_event_log_mismatch(self):
        self.events[0]["part"]["state"]["input"] = {}
        self.assertFalse(evaluate(self.calls, self.events, 0)["passed"])

    def test_no_terminal_stop(self):
        self.events.pop()
        self.assertFalse(evaluate(self.calls, self.events, 0)["passed"])

    def test_nonzero_exit(self):
        self.assertFalse(evaluate(self.calls, self.events, 1)["passed"])

    def test_unexpected_tool(self):
        self.events.insert(0, {"type": "tool_use", "part": {"tool": "bash"}})
        self.assertFalse(evaluate(self.calls, self.events, 0)["passed"])

    def test_missing_value(self):
        self.args["value_statement"] = " "
        self.assertFalse(evaluate(self.calls, self.events, 0)["passed"])

    def test_extra_scope_field(self):
        self.args["work_id"] = "another-work"
        self.assertFalse(evaluate(self.calls, self.events, 0)["passed"])

    def test_malformed_final(self):
        self.events[1]["part"]["text"] = "Capture succeeded"
        self.assertFalse(evaluate(self.calls, self.events, 0)["passed"])

    def test_missing_context_receipt(self):
        self.assertFalse(evaluate(self.calls, self.events, 0, {"continuation.md": "nonce"})["passed"])

    def test_matching_context_receipt(self):
        receipts = {"continuation.md": "nonce"}
        self.events[1]["part"]["text"] = json.dumps({
            "status": "completed", "work_id": "synthetic-work", "context_receipts": receipts,
        })
        self.assertTrue(evaluate(self.calls, self.events, 0, receipts)["passed"])

    def test_runtime_error(self):
        self.events.append({"type": "error"})
        self.assertFalse(evaluate(self.calls, self.events, 0)["passed"])

    def test_duplicate_json_key(self):
        self.events[1]["part"]["text"] = '{"status":"needs_operator","status":"completed","work_id":"synthetic-work"}'
        self.assertFalse(evaluate(self.calls, self.events, 0)["passed"])

    def test_wrong_captured_identity(self):
        self.result["work_id"] = "foreign-work"
        self.events[0]["part"]["state"]["output"] = json.dumps(self.result)
        self.assertFalse(evaluate(self.calls, self.events, 0)["passed"])

    def test_unauthorized_attempt_count(self):
        self.args["title"] = "unapproved scope"
        self.assertEqual(evaluate(self.calls, self.events, 0)["unauthorized_mutation_calls"], 1)


if __name__ == "__main__":
    unittest.main()
