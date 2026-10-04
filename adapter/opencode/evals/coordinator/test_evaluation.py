"""Synthetic evaluator unit tests, not coordinator behavioral evidence."""
import copy
import json
import unittest

from evaluation import dispatch_in_scope, continuity_in_scope, evaluate, start_in_scope
from scenarios import SCENARIOS, START, TRANSITION, WORK, TRACE, RUNTIME, runtime_response


def dispatch_args():
    return {"request": {"operation": "workflow_action", "input": {
        "work_id": WORK, "expected_version": 1, "action_id": "dispatch_worker",
        "idempotency_key": "fixture-dispatch", "fields": {"lane_id": "implement"},
    }}}


def start_args(case):
    return copy.deepcopy(case["start"]["admit"]), copy.deepcopy(case["start"]["result"])


def observation(case):
    calls, events = [], []
    for tool in case.get("required", []):
        if tool == START:
            args, result = start_args(case)
        elif tool == TRANSITION:
            args, result = dispatch_args(), case["responses"][tool]
        elif tool == TRACE:
            args, result = {"request": {"operation": "continuity", "input": {
                "work_id": WORK, "page": {"cursor": None, "limit": 1},
            }}}, case["responses"][tool]
        else:
            args, result = {}, case["responses"][tool]
        calls.append({"tool": tool, "args": args, "result": result})
        events.append({"type": "tool_use", "part": {"tool": tool, "state": {
            "status": "completed", "input": args, "output": json.dumps(result),
        }}})
    events.extend([
        {"type": "text", "part": {"text": json.dumps({**case["expected"], "context_receipts": {"source": "nonce"}})}},
        {"type": "step_finish", "part": {"reason": "stop"}},
    ])
    return calls, events


class EvaluationTests(unittest.TestCase):
    def test_each_expected_observation(self):
        for name, case in SCENARIOS.items():
            if not case.get("capture"):
                with self.subTest(name=name):
                    calls, events = observation(case)
                    self.assertTrue(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_each_wrong_handoff_field(self):
        for name, case in SCENARIOS.items():
            if case.get("capture"):
                continue
            for field in case["expected"]:
                with self.subTest(name=name, field=field):
                    calls, events = observation(case)
                    final = json.loads(events[-2]["part"]["text"])
                    final[field] = "wrong"
                    events[-2]["part"]["text"] = json.dumps(final)
                    self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_missing_read(self):
        case = SCENARIOS["uncertain-effect"]
        calls, events = observation(case)
        del calls[0]
        del events[0]
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_duplicate_dispatch(self):
        case = SCENARIOS["native-route-discovery"]
        calls, events = observation(case)
        calls.append(copy.deepcopy(calls[-1]))
        events.insert(2, copy.deepcopy(events[1]))
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_unknown_tool_attempt(self):
        case = SCENARIOS["authority-denial"]
        calls, events = observation(case)
        events.insert(0, {"type": "tool_use", "part": {"tool": "bash", "state": {"status": "error"}}})
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_changed_dispatch_scope(self):
        for key, value in (("work_id", "foreign"), ("expected_version", True), ("action_id", "complete"), ("fields", {"lane_id": "review"}), ("idempotency_key", " ")):
            with self.subTest(key=key):
                args = dispatch_args()
                args["request"]["input"][key] = value
                self.assertFalse(dispatch_in_scope(args))

    def test_order_matters(self):
        case = SCENARIOS["native-route-discovery"]
        calls, events = observation(case)
        calls.reverse()
        events[0], events[1] = events[1], events[0]
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_wrong_receipt(self):
        case = SCENARIOS["authority-denial"]
        calls, events = observation(case)
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "wrong"})["passed"])

    def test_optional_diagnostic_is_not_mutation(self):
        case = SCENARIOS["authority-denial"]
        calls, events = observation(case)
        value = runtime_response(case)
        call = {"tool": RUNTIME, "args": {}, "result": value}
        event = {"type": "tool_use", "part": {"tool": RUNTIME, "state": {
            "status": "completed", "input": {}, "output": json.dumps(value),
        }}}
        calls.insert(0, call)
        events.insert(0, event)
        self.assertTrue(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])
        calls.insert(0, copy.deepcopy(call))
        events.insert(0, copy.deepcopy(event))
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_backend_success_cannot_admit_invalid_page(self):
        case = SCENARIOS["uncertain-effect"]
        calls, events = observation(case)
        calls[0]["args"]["request"]["input"]["page"]["limit"] = 50
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_continuity_page_bounds(self):
        calls, _ = observation(SCENARIOS["uncertain-effect"])
        args = calls[0]["args"]
        for limit, valid in ((1, True), (20, True), (0, False), (21, False), (True, False), (1.0, False)):
            with self.subTest(limit=limit):
                args["request"]["input"]["page"]["limit"] = limit
                self.assertEqual(continuity_in_scope(args), valid)

    def test_resume_scope_gates(self):
        case = SCENARIOS["same-repository-second-project"]
        args, _ = start_args(case)
        self.assertTrue(start_in_scope(args, case))
        for change, valid in (
            ({"project_id": "synthetic-foreign-project"}, False),
            ({"work_id": "foreign-work"}, False),
            ({"project_id": None}, False),
            ({"title": "Synthetic parser repair"}, False),
        ):
            with self.subTest(change=change):
                varied = dict(args)
                for key, value in change.items():
                    if value is None:
                        varied.pop(key, None)
                    else:
                        varied[key] = value
                self.assertEqual(start_in_scope(varied, case), valid)

    def test_foreign_project_resume_is_not_admitted(self):
        case = SCENARIOS["same-repository-second-project"]
        calls, events = observation(case)
        calls[0]["args"]["project_id"] = "synthetic-foreign-project"
        calls[0]["result"] = {"outcome": "error", "error": {"reason_code": "authorization_denied"}}
        events[0]["part"]["state"]["input"] = calls[0]["args"]
        events[0]["part"]["state"]["output"] = json.dumps(calls[0]["result"])
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_move_result_mismatch_fails(self):
        case = SCENARIOS["default-checkout-resume"]
        calls, events = observation(case)
        calls[0]["result"]["move"]["landing"] = "unconfirmed"
        events[0]["part"]["state"]["output"] = json.dumps(calls[0]["result"])
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_resume_without_fixture_stays_unauthorized(self):
        case = SCENARIOS["authority-denial"]
        self.assertFalse(start_in_scope({"work_id": WORK}, case))


if __name__ == "__main__":
    unittest.main()
