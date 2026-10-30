"""Negative tests for the observation evaluator; these are not agent evidence."""
import copy
import json
import unittest

from evaluation import evaluate
from scenarios import SCENARIOS
from test_evaluation import observation


class EvaluationTests(unittest.TestCase):
    def setUp(self):
        self.case = SCENARIOS["native-route-discovery"]
        self.calls, self.events = observation(self.case)
        self.args = self.calls[1]["args"]["request"]["input"]
        self.result = self.calls[0]["result"]

    def evaluate(self, exit_code=0, receipts=None):
        return evaluate(self.case, self.calls, self.events, exit_code,
                        {"source": "nonce"} if receipts is None else receipts)

    def test_valid_observation(self):
        self.assertTrue(self.evaluate()["passed"])

    def test_changed_scope(self):
        for key in ("work_id", "expected_version", "action_id", "fields"):
            with self.subTest(key=key):
                old = self.args[key]
                self.args[key] = "different scope"
                self.assertFalse(self.evaluate()["passed"])
                self.args[key] = old

    def test_failed_dispatch_before_success(self):
        failed = copy.deepcopy(self.calls[1])
        failed["result"] = {"outcome": "error"}
        self.calls.insert(0, failed)
        self.events.insert(0, copy.deepcopy(self.events[1]))
        self.assertFalse(self.evaluate()["passed"])

    def test_error_event(self):
        self.events[0]["part"]["state"]["status"] = "error"
        self.assertFalse(self.evaluate()["passed"])

    def test_changed_final_response(self):
        self.events[-2]["part"]["text"] = json.dumps({"status": "needs_operator", "work_id": "synthetic-work"})
        self.assertFalse(self.evaluate()["passed"])

    def test_missing_final_response(self):
        del self.events[-2]
        self.assertFalse(self.evaluate()["passed"])

    def test_event_log_mismatch(self):
        self.events[0]["part"]["state"]["input"] = {}
        self.assertFalse(self.evaluate()["passed"])

    def test_no_terminal_stop(self):
        self.events.pop()
        self.assertFalse(self.evaluate()["passed"])

    def test_nonzero_exit(self):
        self.assertFalse(self.evaluate(exit_code=1)["passed"])

    def test_unexpected_tool(self):
        self.events.insert(0, {"type": "tool_use", "part": {"tool": "bash"}})
        self.assertFalse(self.evaluate()["passed"])

    def test_missing_value(self):
        self.args["idempotency_key"] = " "
        self.assertFalse(self.evaluate()["passed"])

    def test_extra_scope_field(self):
        self.args["project_id"] = "another-project"
        self.assertFalse(self.evaluate()["passed"])

    def test_malformed_final(self):
        self.events[-2]["part"]["text"] = "Dispatch succeeded"
        self.assertFalse(self.evaluate()["passed"])

    def test_narrated_terminal_answer_passes_with_strict_advisory_false(self):
        # Contract v2: the terminal answer is the last text event, so
        # narration before it never gates the measured workflow behavior; the
        # strict output rule reports advisory only.
        self.events.insert(1, {"type": "text", "part": {"text": "Dispatching the task."}})
        result = self.evaluate()
        self.assertTrue(result["checks"]["final_response"])
        self.assertFalse(result["advisory"]["strict_output_compliance"])
        self.assertNotIn("strict_output_compliance", result["checks"])
        self.assertTrue(result["passed"])

    def test_fenced_terminal_answer_passes_with_strict_advisory_false(self):
        # Contract v2: a fenced final answer still measures the action, and
        # the harness FORMAT violation reports advisory only.
        self.events[-2]["part"]["text"] = "```json\n" + self.events[-2]["part"]["text"] + "\n```"
        result = self.evaluate()
        self.assertTrue(result["checks"]["final_response"])
        self.assertFalse(result["advisory"]["strict_output_compliance"])
        self.assertTrue(result["passed"])

    def test_missing_context_receipt(self):
        self.assertFalse(self.evaluate(receipts={"continuation.md": "nonce"})["passed"])

    def test_matching_context_receipt(self):
        receipts = {"continuation.md": "nonce"}
        self.events[-2]["part"]["text"] = json.dumps({
            **self.case["expected"], "context_receipts": receipts,
        })
        self.assertTrue(self.evaluate(receipts=receipts)["passed"])

    def test_runtime_error(self):
        self.events.append({"type": "error"})
        self.assertFalse(self.evaluate()["passed"])

    def test_duplicate_json_key(self):
        self.events[-2]["part"]["text"] = '{"status":"needs_operator","status":"completed","work_id":"synthetic-work"}'
        self.assertFalse(self.evaluate()["passed"])

    def test_wrong_work_identity(self):
        self.result = copy.deepcopy(self.result)
        self.result["result"]["work_id"] = "foreign-work"
        self.calls[0]["result"] = self.result
        self.events[0]["part"]["state"]["output"] = json.dumps(self.result)
        self.assertFalse(self.evaluate()["passed"])

    def test_unauthorized_attempt_count(self):
        self.args["action_id"] = "unapproved-action"
        self.assertEqual(self.evaluate()["unauthorized_mutation_calls"], 1)


if __name__ == "__main__":
    unittest.main()
