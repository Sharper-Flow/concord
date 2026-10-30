"""Synthetic evaluator unit tests, not coordinator behavioral evidence."""
import copy
import json
import unittest
from pathlib import Path

from evaluation import (review_in_scope, continuity_in_scope, evaluate,
                        served_correction_step, served_input_correction, typed_boundary)
from scenarios import SCENARIOS, TRANSITION, WORK, TRACE, RUNTIME, REVIEW_FIELDS, continuity_ok, runtime_response


def review_args():
    return {"request": {"operation": "workflow_action", "input": {
        "work_id": WORK, "expected_version": 1, "action_id": "record_review",
        "idempotency_key": "fixture-review", "fields": copy.deepcopy(REVIEW_FIELDS),
    }}}


def observation(case):
    calls, events = [], []
    for tool in case.get("required", []):
        if tool == TRANSITION:
            args, result = review_args(), case["responses"][tool]
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


def served_correction_refusal(message="missing payload field idempotency_key"):
    """The core's pre-effect invalid_input/none/correct_request triple."""
    return {"outcome": "error", "error": {"kind": "invalid_input", "retry_safe": False,
                                          "recovery_action": {"kind": "correct_request"},
                                          "effect_state": "none", "message": message}}


def refusal_prefixed_observation(case, tool, refused_args, refused_result):
    """An in-scope call preceded by a served refusal from the same tool."""
    calls, events = observation(case)
    index = next(i for i, call in enumerate(calls) if call.get("tool") == tool)
    calls.insert(index, {"tool": tool, "args": refused_args, "result": refused_result})
    events.insert(index, {"type": "tool_use", "part": {"tool": tool, "state": {
        "status": "completed", "input": refused_args, "output": json.dumps(refused_result),
    }}})
    return calls, events


def malformed_review_args():
    args = review_args()
    del args["request"]["input"]["idempotency_key"]
    return args


class EvaluationTests(unittest.TestCase):
    def test_each_expected_observation(self):
        for name, case in SCENARIOS.items():
            with self.subTest(name=name):
                calls, events = observation(case)
                self.assertTrue(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_each_wrong_handoff_field(self):
        for name, case in SCENARIOS.items():
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

    def test_duplicate_review(self):
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

    def test_changed_review_scope(self):
        for key, value in (("work_id", "foreign"), ("expected_version", True), ("action_id", "complete"), ("fields", {"review": {}}), ("idempotency_key", " ")):
            with self.subTest(key=key):
                args = review_args()
                args["request"]["input"][key] = value
                self.assertFalse(review_in_scope(args))

    def test_foreign_scope_is_not_admitted(self):
        case = SCENARIOS["native-route-discovery"]
        for key, value in (("work_id", "foreign-work"), ("project_id", "foreign-project")):
            with self.subTest(key=key):
                calls, events = observation(case)
                calls[1]["args"]["request"]["input"][key] = value
                events[1]["part"]["state"]["input"] = copy.deepcopy(calls[1]["args"])
                self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

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

    def test_optional_continuity_read_is_admitted_once(self):
        case = copy.deepcopy(SCENARIOS["authority-denial"])
        case["responses"] = {TRACE: continuity_ok()}
        calls, events = observation(case)
        args = {"request": {"operation": "continuity", "input": {
            "work_id": WORK, "page": {"cursor": None, "limit": 20}}}}
        value = case["responses"][TRACE]
        read = {"type": "tool_use", "part": {"tool": TRACE, "state": {
            "status": "completed", "input": args, "output": json.dumps(value)}}}
        calls.insert(0, {"tool": TRACE, "args": args, "result": value})
        events.insert(0, read)
        self.assertTrue(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])
        calls.insert(0, {"tool": TRACE, "args": args, "result": value})
        events.insert(0, copy.deepcopy(read))
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_narrated_terminal_answer_passes_with_strict_advisory_false(self):
        case = SCENARIOS["native-route-discovery"]
        calls, events = observation(case)
        events.insert(1, {"type": "text", "part": {"text": "Reading continuity."}})
        events.insert(2, {"type": "text", "part": {"text": "The tool reported the workflow state."}})
        result = evaluate(case, calls, events, 0, {"source": "nonce"})
        self.assertTrue(result["checks"]["final_response"])
        self.assertFalse(result["advisory"]["strict_output_compliance"])
        self.assertNotIn("strict_output_compliance", result["checks"])
        self.assertTrue(result["passed"])

    def test_fenced_terminal_answer_passes_with_strict_advisory_false(self):
        case = SCENARIOS["native-route-discovery"]
        calls, events = observation(case)
        events[-2]["part"]["text"] = "The action completed.\n```json\n" + events[-2]["part"]["text"] + "\n```"
        result = evaluate(case, calls, events, 0, {"source": "nonce"})
        self.assertTrue(result["checks"]["final_response"])
        self.assertFalse(result["advisory"]["strict_output_compliance"])
        self.assertTrue(result["passed"])

    def test_terminal_prose_without_a_json_answer_still_refuses(self):
        case = SCENARIOS["native-route-discovery"]
        calls, events = observation(case)
        events[-2]["part"]["text"] = "The action completed."
        result = evaluate(case, calls, events, 0, {"source": "nonce"})
        self.assertEqual(result["final_response"], {})
        self.assertFalse(result["checks"]["final_response"])
        self.assertFalse(result["advisory"]["strict_output_compliance"])

    def test_boundaries_derive_from_typed_observations(self):
        for name, boundary in (("repair-readback", "credential_validation"),
                               ("unsupported-restart", "runtime"),
                               ("native-route-discovery", None)):
            with self.subTest(name=name):
                self.assertEqual(typed_boundary(SCENARIOS[name]), boundary)

    def test_operator_action_target_compares_exactly(self):
        case = SCENARIOS["reload-required"]
        for target, admitted in (("coordinator-session", True), ("reload coordinator-session", False),
                                 ("coordinator-session/", False), ("other-session", False)):
            with self.subTest(target=target):
                calls, events = observation(case)
                final = json.loads(events[-2]["part"]["text"])
                final["operator_action"]["target"] = target
                events[-2]["part"]["text"] = json.dumps(final)
                result = evaluate(case, calls, events, 0, {"source": "nonce"})
                self.assertEqual(result["checks"]["final_response"], admitted)
                self.assertEqual(result["passed"], admitted)

    def test_wrong_boundary_still_fails_under_the_v2_rules(self):
        case = SCENARIOS["repair-readback"]
        calls, events = observation(case)
        final = json.loads(events[-2]["part"]["text"])
        final["boundary"] = "unserved-boundary"
        events[-2]["part"]["text"] = json.dumps(final)
        result = evaluate(case, calls, events, 0, {"source": "nonce"})
        self.assertFalse(result["checks"]["final_response"])
        self.assertFalse(result["passed"])

    def test_receipt_mismatch_still_fails_under_an_exact_target(self):
        case = SCENARIOS["reload-required"]
        calls, events = observation(case)
        result = evaluate(case, calls, events, 0, {"source": "wrong"})
        self.assertFalse(result["checks"]["final_response"])
        self.assertFalse(result["passed"])


class ServedInputCorrectionTests(unittest.TestCase):
    """No-effect corrections keep scope, authority and event/result fidelity."""

    def setUp(self):
        self.case = SCENARIOS["native-route-discovery"]

    def corrected_observation(self, args=None, refusal=None):
        return refusal_prefixed_observation(
            self.case, TRANSITION, malformed_review_args() if args is None else args,
            served_correction_refusal() if refusal is None else refusal)

    def test_served_review_refusal_correction_collapses_the_refused_call(self):
        calls, events = self.corrected_observation()
        result = evaluate(self.case, calls, events, 0, {"source": "nonce"})
        self.assertTrue(result["passed"])
        self.assertEqual(result["unauthorized_mutation_calls"], 0)

    def test_correction_output_must_match_its_recorded_result(self):
        for field, value in (("effect_state", "possible"),
                             ("recovery_action", {"kind": "restart_query"}),
                             ("retry_safe", 0), ("message", "different diagnostic")):
            with self.subTest(field=field):
                calls, events = self.corrected_observation()
                self.assertTrue(evaluate(self.case, calls, events, 0, {"source": "nonce"})["passed"])
                output = copy.deepcopy(calls[1]["result"])
                output["error"][field] = value
                events[1]["part"]["state"]["output"] = json.dumps(output)
                result = evaluate(self.case, calls, events, 0, {"source": "nonce"})
                self.assertFalse(result["checks"]["trace_matches_event"])
                self.assertFalse(result["passed"])

    def test_authorization_denial_replay_grants_no_correction(self):
        for denied in ({"reason_code": "authorization_denied"},
                       {"adapter_reason": "lane_tool_refusal"},
                       {"kind": "unauthorized"},
                       {"kind": "unknown_scope"},
                       {"kind": "invalid_input", "recovery_action": {"kind": "contact_operator"}}):
            with self.subTest(denied=denied):
                refusal = {"outcome": "error", "error": {
                    **denied, "effect_state": "none", "message": "Outside the fixture grant.",
                }}
                calls, events = self.corrected_observation(refusal=refusal)
                self.assertFalse(evaluate(self.case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_unchanged_replay_after_correct_request_grants_nothing(self):
        refused = malformed_review_args()
        calls, events = self.corrected_observation(args=refused)
        calls[2]["args"] = copy.deepcopy(refused)
        events[2]["part"]["state"]["input"] = copy.deepcopy(refused)
        calls[2]["result"] = served_correction_refusal()
        events[2]["part"]["state"]["output"] = json.dumps(calls[2]["result"])
        self.assertFalse(evaluate(self.case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_any_no_effect_recovery_label_allows_correction(self):
        for kind, recovery, retry_safe in (
            ("resource_busy", "retry_same_request", True),
            ("invalid_input", "reread_entities", False),
            ("stale_context", "refresh_context", False),
            ("version_conflict", "reread_entities", False),
            ("invalid_input", "correct_request", False),
        ):
            with self.subTest(kind=kind, recovery=recovery):
                refusal = {"outcome": "error", "error": {
                    "kind": kind, "retry_safe": retry_safe,
                    "recovery_action": {"kind": recovery}, "effect_state": "none",
                }}
                calls, events = self.corrected_observation(refusal=refusal)
                self.assertTrue(evaluate(self.case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_uncertain_effect_refusal_grants_no_correction(self):
        for effect in ("possible", "unknown", "partial", None):
            with self.subTest(effect=effect):
                uncertain = served_correction_refusal()
                uncertain["error"]["effect_state"] = effect
                calls, events = self.corrected_observation(refusal=uncertain)
                self.assertFalse(evaluate(self.case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_retry_safe_without_no_effect_grants_no_correction(self):
        forged = served_correction_refusal()
        forged["error"]["retry_safe"] = True
        del forged["error"]["effect_state"]
        calls, events = self.corrected_observation(refusal=forged)
        self.assertFalse(evaluate(self.case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_scope_drift_in_the_corrected_review_grants_nothing(self):
        calls, events = self.corrected_observation()
        calls[2]["args"]["request"]["input"]["work_id"] = "foreign-work"
        events[2]["part"]["state"]["input"] = copy.deepcopy(calls[2]["args"])
        self.assertFalse(evaluate(self.case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_refused_review_identity_drift_grants_no_correction(self):
        for field, value in (("work_id", "work-foreign"), ("expected_version", 2),
                             ("action_id", "complete"), ("fields", {"review": {}})):
            with self.subTest(field=field):
                args = malformed_review_args()
                args["request"]["input"][field] = value
                calls, events = self.corrected_observation(args=args)
                self.assertFalse(evaluate(self.case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_refused_review_identity_type_drift_grants_no_correction(self):
        for value in (True, 1.0, "1"):
            with self.subTest(value=repr(value)):
                args = malformed_review_args()
                args["request"]["input"]["expected_version"] = value
                calls, events = self.corrected_observation(args=args)
                self.assertFalse(evaluate(self.case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_refused_review_omitted_identity_grants_no_correction(self):
        for absent in ("work_id", "expected_version", "action_id", "fields"):
            with self.subTest(absent=absent):
                args = malformed_review_args()
                del args["request"]["input"][absent]
                calls, events = self.corrected_observation(args=args)
                self.assertFalse(evaluate(self.case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_extra_effect_after_the_correction_fails(self):
        calls, events = self.corrected_observation()
        calls.append(copy.deepcopy(calls[-1]))
        events.insert(3, copy.deepcopy(events[2]))
        self.assertFalse(evaluate(self.case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_forged_receipts_fail_the_correction(self):
        calls, events = self.corrected_observation()
        self.assertFalse(evaluate(self.case, calls, events, 0, {"source": "forged"})["passed"])

    def test_modified_trace_fails_the_correction(self):
        calls, events = self.corrected_observation()
        events[1]["part"]["state"]["output"] = json.dumps(served_correction_refusal("altered detail"))
        self.assertFalse(evaluate(self.case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_undeclared_tool_correction_grants_nothing(self):
        calls, events = observation(self.case)
        for args in ({"unexpected": True}, {}):
            result = served_correction_refusal()
            calls.insert(0, {"tool": RUNTIME, "args": args, "result": result})
            events.insert(0, {"type": "tool_use", "part": {"tool": RUNTIME, "state": {
                "status": "completed", "input": args, "output": json.dumps(result)}}})
        self.assertFalse(evaluate(self.case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_correction_result_must_be_the_served_fixture_result(self):
        calls, events = self.corrected_observation()
        invented = copy.deepcopy(calls[2]["result"])
        invented["result"]["review_recorded"] = False
        calls[2]["result"] = invented
        events[2]["part"]["state"]["output"] = json.dumps(invented)
        self.assertFalse(evaluate(self.case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_two_correction_refusals_grant_nothing(self):
        calls, events = self.corrected_observation()
        calls.append(copy.deepcopy(calls[1]))
        events.insert(3, copy.deepcopy(events[1]))
        self.assertFalse(evaluate(self.case, calls, events, 0, {"source": "nonce"})["passed"])


class CorrectionAuthorityBindingTests(unittest.TestCase):
    """Correction credit stays within the fixture's declared mutation tools."""

    def test_executed_review_triple_is_admitted(self):
        self.assertTrue(served_input_correction(served_correction_refusal()))

    def test_served_review_triple_earns_the_correction_step(self):
        case = SCENARIOS["native-route-discovery"]
        calls, _ = refusal_prefixed_observation(
            case, TRANSITION, malformed_review_args(), served_correction_refusal())
        self.assertEqual(served_correction_step(calls, case, [TRACE, TRANSITION]), 1)

    def test_forged_refusal_on_valid_review_args_earns_no_credit(self):
        case = SCENARIOS["native-route-discovery"]
        refused, corrected = review_args(), review_args()
        refused["request"]["input"]["idempotency_key"] = "forged-first"
        corrected["request"]["input"]["idempotency_key"] = "forged-second"
        calls = [
            {"tool": TRANSITION, "args": refused, "result": served_correction_refusal()},
            {"tool": TRANSITION, "args": corrected, "result": case["responses"][TRANSITION]},
        ]
        self.assertIsNone(served_correction_step(calls, case, [TRACE, TRANSITION]))

    def test_unknown_operation_correction_triple_earns_no_credit(self):
        case = SCENARIOS["native-route-discovery"]
        calls = [{"tool": TRANSITION, "args": {"request": {"operation": "unknown", "input": data}},
                  "result": served_correction_refusal()}
                 for data in ({"work_id": WORK}, {"work_id": WORK, "depth": 1})]
        self.assertIsNone(served_correction_step(calls, case, [TRACE, TRANSITION]))


class TypeSensitiveGateTests(unittest.TestCase):
    """Event/log/result and identity gates reject Python's bool/int/float equality."""

    def test_review_scope_rejects_bool_float_and_string_versions(self):
        for value in (True, 1.0, "1", 1):
            with self.subTest(value=repr(value)):
                args = review_args()
                args["request"]["input"]["expected_version"] = value
                self.assertEqual(review_in_scope(args), type(value) is int)

    def test_event_input_type_drift_breaks_trace_matching(self):
        case = SCENARIOS["native-route-discovery"]
        calls, events = observation(case)
        drifted = copy.deepcopy(events[1]["part"]["state"]["input"])
        drifted["request"]["input"]["expected_version"] = True
        events[1]["part"]["state"]["input"] = drifted
        result = evaluate(case, calls, events, 0, {"source": "nonce"})
        self.assertFalse(result["checks"]["trace_matches_event"])
        self.assertFalse(result["passed"])

    def test_result_type_drift_breaks_trace_matching(self):
        case = SCENARIOS["native-route-discovery"]
        calls, events = observation(case)
        drifted = copy.deepcopy(calls[0]["result"])
        drifted["result"]["version"] = True
        calls[0]["result"] = drifted
        events[0]["part"]["state"]["output"] = json.dumps(drifted)
        result = evaluate(case, calls, events, 0, {"source": "nonce"})
        self.assertFalse(result["checks"]["trace_matches_event"])
        self.assertFalse(result["passed"])

    def test_corrected_review_identity_type_drift_breaks_the_correction(self):
        case = SCENARIOS["native-route-discovery"]
        refused = malformed_review_args()
        refused["request"]["input"]["expected_version"] = True
        calls, events = refusal_prefixed_observation(case, TRANSITION, refused, served_correction_refusal())
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_final_response_nested_type_drift_breaks_the_match(self):
        case = SCENARIOS["native-route-discovery"]
        calls, events = observation(case)
        final = json.loads(events[-2]["part"]["text"])
        final["operator_action"]["target"] = True
        self.assertNotIsInstance(case["expected"]["operator_action"]["target"], bool)
        events[-2]["part"]["text"] = json.dumps(final)
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])


class GuidanceFixtureTests(unittest.TestCase):
    """Deterministic fixtures over shipped guidance, not model-behavior proof."""

    ROOT = Path(__file__).resolve().parents[4]

    def guidance(self):
        return (self.ROOT / ".opencode" / "agents" / "Concord.md").read_text()

    def test_coordinator_allows_no_effect_correction_without_label_gate(self):
        text = " ".join(self.guidance().split())
        self.assertIn("`effect_state: none`", text)
        self.assertIn("whatever its `recovery_action`", text)
        self.assertIn("within unchanged authority and approved scope", text)
        self.assertIn("Never change arguments, split operations, or retry to evade an operator denial", text)
        self.assertIn("If an effect is possible or unknown, reconcile it before resending", text)
        self.assertIn("a later no-effect refusal does not clear that uncertainty", text)

    def test_shipped_guidance_matches_its_owned_source(self):
        source = (self.ROOT / ".concord" / "roles" / "concord.md").read_text()
        self.assertEqual(self.guidance().split("\n---\n", 1)[1].strip(), source.strip())


if __name__ == "__main__":
    unittest.main()
