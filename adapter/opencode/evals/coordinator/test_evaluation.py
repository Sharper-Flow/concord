"""Synthetic evaluator unit tests, not coordinator behavioral evidence."""
import copy
import json
import re
import unittest
from pathlib import Path

from evaluation import (claim_in_scope, dispatch_in_scope, continuity_in_scope,
                        evaluate, start_in_scope)
from scenarios import (BOUNDARY_NOTICE, SCENARIOS, START, TRANSITION, WORK, TRACE, RUNTIME,
                       WORKTREE, move_notice, runtime_response)

MOVE_NOTICE_SOURCE = Path(__file__).resolve().parents[2] / "move-notice.ts"
CONCORD_SOURCE = Path(__file__).resolve().parents[2] / "concord.ts"


def dispatch_args():
    return {"request": {"operation": "workflow_action", "input": {
        "work_id": WORK, "expected_version": 1, "action_id": "dispatch_worker",
        "idempotency_key": "fixture-dispatch", "fields": {"lane_id": "implement"},
    }}}


def start_args(case):
    return copy.deepcopy(case["start"]["admit"]), copy.deepcopy(case["start"]["result"])


def claim_args(case):
    args = copy.deepcopy(case["transition"]["admit"])
    args["request"]["input"]["idempotency_key"] = "fixture-claim"
    return args, copy.deepcopy(case["transition"]["result"])


def observation(case):
    calls, events = [], []
    for tool in case.get("required", []):
        notice = None
        if tool == START:
            args, result = start_args(case)
        elif tool == TRANSITION and "transition" in case:
            args, result = claim_args(case)
            notice = case["transition"].get("notice")
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
            "status": "completed", "input": args,
            "output": json.dumps(result) + (f"\n{notice}" if notice else ""),
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
        case = SCENARIOS["cross-repository-second-session"]
        args, _ = start_args(case)
        self.assertTrue(start_in_scope(args, case))
        for change, valid in (
            ({"project_id": "synthetic-foreign-project"}, False),
            ({"project_id": "synthetic-same-repo-project"}, False),
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

    def test_claim_scope_gates(self):
        case = SCENARIOS["same-repository-second-project"]
        args, _ = claim_args(case)
        self.assertTrue(claim_in_scope(args, case))
        data = args["request"]["input"]
        for key, value, valid in (
            ("project_id", "synthetic-foreign-project", False),
            ("project_id", "synthetic-cross-repo-project", False),
            ("work_id", "foreign-work", False),
            ("base_sha", "f" * 40, False),
            ("expected_version", 2, False),
            ("idempotency_key", " ", False),
            ("host_pid", 1234, False),
        ):
            with self.subTest(key=key, value=value):
                varied = copy.deepcopy(args)
                if value is None:
                    varied["request"]["input"].pop(key, None)
                else:
                    varied["request"]["input"][key] = value
                self.assertEqual(claim_in_scope(varied, case), valid)

    def test_foreign_project_resume_is_not_admitted(self):
        case = SCENARIOS["cross-repository-second-session"]
        calls, events = observation(case)
        calls[0]["args"]["project_id"] = "synthetic-foreign-project"
        calls[0]["result"] = {"outcome": "error", "error": {"reason_code": "authorization_denied"}}
        events[0]["part"]["state"]["input"] = calls[0]["args"]
        events[0]["part"]["state"]["output"] = json.dumps(calls[0]["result"])
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_same_repository_start_selection_is_not_admitted(self):
        case = SCENARIOS["same-repository-second-project"]
        calls, events = observation(case)
        calls[0] = {"tool": START, "args": {"work_id": WORK, "project_id": "synthetic-same-repo-project"},
                    "result": {"outcome": "error", "error": {"reason_code": "authorization_denied"}}}
        events[0] = {"type": "tool_use", "part": {"tool": START, "state": {
            "status": "completed", "input": calls[0]["args"], "output": json.dumps(calls[0]["result"]),
        }}}
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_move_result_mismatch_fails(self):
        case = SCENARIOS["default-checkout-resume"]
        calls, events = observation(case)
        calls[0]["result"]["worktree_path"] = "/synthetic/elsewhere"
        events[0]["part"]["state"]["output"] = json.dumps(calls[0]["result"])
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_claim_output_must_carry_the_move_notice_line(self):
        case = SCENARIOS["same-repository-second-project"]
        for output in (json.dumps(case["transition"]["result"]),
                       json.dumps(case["transition"]["result"]) + "\n" + move_notice("/synthetic/elsewhere", True)):
            with self.subTest(output=output[-40:]):
                calls, events = observation(case)
                events[0]["part"]["state"]["output"] = output
                self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_dispatch_after_an_armed_boundary_fails(self):
        # The claim notice names an active turn-move boundary, so a dispatch
        # in the same turn is outside the admitted sequence.
        case = SCENARIOS["same-repository-second-project"]
        calls, events = observation(case)
        refused = {"outcome": "error", "error": {"reason_code": "authorization_denied"}}
        calls.append({"tool": TRANSITION, "args": dispatch_args(), "result": refused})
        events.insert(1, {"type": "tool_use", "part": {"tool": TRANSITION, "state": {
            "status": "completed", "input": dispatch_args(), "output": json.dumps(refused),
        }}})
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_same_turn_replay_of_an_unlanded_resume_fails(self):
        case = SCENARIOS["stale-context-turn-boundary"]
        calls, events = observation(case)
        calls.append(copy.deepcopy(calls[0]))
        events.insert(1, copy.deepcopy(events[0]))
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_optional_continuity_read_is_admitted_once(self):
        case = SCENARIOS["default-checkout-resume"]
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

    def test_declared_replay_is_admitted_once_on_the_dirty_origin_refusal_only(self):
        case = SCENARIOS["genuine-refusal-no-fallback"]
        calls, events = observation(case)
        calls.insert(0, copy.deepcopy(calls[0]))
        events.insert(0, copy.deepcopy(events[0]))
        self.assertTrue(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])
        calls.insert(0, copy.deepcopy(calls[0]))
        events.insert(0, copy.deepcopy(events[0]))
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_notice_doubles_match_the_adapter_source(self):
        # The doubles must stay production-shaped: the adapter's notice text
        # in move-notice.ts is the source the fixture copies.
        source = MOVE_NOTICE_SOURCE.read_text()
        constant = re.search(r'TURN_MOVE_BOUNDARY_NOTICE =\s*"([^"]+)"', source)
        template = re.search(r'const notice = `([^`]+)`', source)
        self.assertIsNotNone(constant)
        self.assertIsNotNone(template)
        self.assertEqual(BOUNDARY_NOTICE, constant.group(1))
        self.assertEqual(move_notice("/p", False), template.group(1).replace("${newPath}", "/p"))
        self.assertEqual(move_notice("/p", True), f"{move_notice('/p', False)} {BOUNDARY_NOTICE}")

    def test_resume_without_fixture_stays_unauthorized(self):
        case = SCENARIOS["authority-denial"]
        self.assertFalse(start_in_scope({"work_id": WORK}, case))

    def test_resume_admission_excludes_same_repository_project(self):
        # work_start resume selects a member Project in another repository
        # only; the same-repository selection route is worktree_claim, so a
        # fixture declaring the wrong route must not admit it either.
        case = {"start": {"admit": {"work_id": WORK, "project_id": "synthetic-same-repo-project"}}}
        self.assertFalse(start_in_scope({"work_id": WORK, "project_id": "synthetic-same-repo-project"}, case))

    def test_intermediate_narration_does_not_erase_the_terminal_answer(self):
        # The terminal assistant answer is identified structurally as the
        # last text event, so narration before it cannot discard the measured
        # relocation behavior; it fails only the strict output check.
        case = SCENARIOS["dirty-same-target-reuse"]
        calls, events = observation(case)
        events.insert(1, {"type": "text", "part": {"text": "Resuming the item now."}})
        events.insert(2, {"type": "text", "part": {"text": "The tool reported the landing."}})
        result = evaluate(case, calls, events, 0, {"source": "nonce"})
        self.assertTrue(result["checks"]["final_response"])
        self.assertFalse(result["checks"]["strict_output_compliance"])
        self.assertFalse(result["passed"])

    def test_fenced_terminal_answer_keeps_the_formatter_failure_visible(self):
        # A fenced final answer still measures the behavior it carries, while
        # the harness FORMAT violation stays its own failed check.
        case = SCENARIOS["default-checkout-resume"]
        calls, events = observation(case)
        events[-2]["part"]["text"] = (
            "The resume completed.\n```json\n" + events[-2]["part"]["text"] + "\n```"
        )
        result = evaluate(case, calls, events, 0, {"source": "nonce"})
        self.assertTrue(result["checks"]["final_response"])
        self.assertFalse(result["checks"]["strict_output_compliance"])
        self.assertFalse(result["passed"])

    def test_terminal_prose_without_a_json_answer_still_refuses(self):
        case = SCENARIOS["default-checkout-resume"]
        calls, events = observation(case)
        events[-2]["part"]["text"] = "The resume completed."
        result = evaluate(case, calls, events, 0, {"source": "nonce"})
        self.assertEqual(result["final_response"], {})
        self.assertFalse(result["checks"]["final_response"])
        self.assertFalse(result["checks"]["strict_output_compliance"])

    def test_relocation_boundaries_derive_from_typed_observations(self):
        # The measured boundary is the identifier the typed stopping
        # observation names: the refusal's boundary, a core boundary carried
        # in its message, or its error kind; the turn-move boundary only
        # while the served move notice announces it as active.
        from evaluation import typed_boundary
        for name, boundary in (
            ("default-checkout-resume", None),
            ("dirty-same-target-reuse", None),
            ("same-repository-second-project", "turn_move_boundary"),
            ("cross-repository-second-session", "session_opener_unregistered"),
            ("stale-context-turn-boundary", "session_directory_mismatch"),
            ("genuine-refusal-no-fallback", "work_bootstrap"),
            ("authority-denial", None),
            ("missing-credential", None),
        ):
            with self.subTest(name=name):
                self.assertEqual(typed_boundary(SCENARIOS[name]), boundary)
                if boundary is not None:
                    self.assertEqual(SCENARIOS[name]["expected"]["boundary"], boundary)

    def test_unlanded_refusal_boundary_accepts_only_the_typed_name(self):
        # Naming the refusal's own typed identifier passes; asserting an
        # armed turn-move boundary or the bare tool name does not.
        case = SCENARIOS["stale-context-turn-boundary"]
        for boundary, admitted in (
            ("session_directory_mismatch", True),
            ("turn_move_boundary", False),
            (START, False),
        ):
            with self.subTest(boundary=boundary):
                calls, events = observation(case)
                final = json.loads(events[-2]["part"]["text"])
                final["boundary"] = boundary
                events[-2]["part"]["text"] = json.dumps(final)
                result = evaluate(case, calls, events, 0, {"source": "nonce"})
                self.assertEqual(result["checks"]["final_response"], admitted)

    def test_core_boundary_does_not_depend_on_a_fixture_label(self):
        from evaluation import typed_boundary
        case = copy.deepcopy(SCENARIOS["genuine-refusal-no-fallback"])
        case["start"].pop("boundary", None)
        self.assertEqual(typed_boundary(case), "work_bootstrap")
        case["start"]["boundary"] = "invalid_operation"
        self.assertEqual(typed_boundary(case), "work_bootstrap")

    def test_boundary_words_in_unstructured_details_are_not_core_identifiers(self):
        from evaluation import typed_boundary
        for message in (
            "Operator must resolve work_bootstrap state before resume.",
            "noise concord work-resume: store: work_bootstrap: invalid_operation: detail",
            "concord work-resume: store: work_bootstrap: detail",
        ):
            with self.subTest(message=message):
                case = copy.deepcopy(SCENARIOS["genuine-refusal-no-fallback"])
                case["start"]["result"]["error"]["message"] = message
                self.assertEqual(typed_boundary(case), "resume_failure")

    def test_refusal_boundary_names_accept_only_the_typed_identifier(self):
        for name, typed in (
            ("genuine-refusal-no-fallback", "work_bootstrap"),
            ("cross-repository-second-session", "session_opener_unregistered"),
        ):
            for boundary, admitted in ((typed, True), (START, False)):
                with self.subTest(name=name, boundary=boundary):
                    case = SCENARIOS[name]
                    calls, events = observation(case)
                    final = json.loads(events[-2]["part"]["text"])
                    final["boundary"] = boundary
                    events[-2]["part"]["text"] = json.dumps(final)
                    result = evaluate(case, calls, events, 0, {"source": "nonce"})
                    self.assertEqual(result["checks"]["final_response"], admitted)

    def test_unlanded_refusal_double_mirrors_the_adapter_source(self):
        # The stale-context fixture message is the production
        # move_context_not_landed refusal verbatim, with the synthetic paths
        # substituted, so the evaluated guidance is the shipped guidance.
        source = CONCORD_SOURCE.read_text()
        template = re.search(r'move_context_not_landed", `([^`]+)`', source)
        self.assertIsNotNone(template)
        rendered = (
            template.group(1)
            .replace("${JSON.stringify(target.worktree.path)}", json.dumps(WORKTREE))
            .replace("${JSON.stringify(context.directory)}", json.dumps("/synthetic/repo"))
        )
        served = SCENARIOS["stale-context-turn-boundary"]["start"]["result"]["error"]["message"]
        self.assertEqual(served, rendered)


if __name__ == "__main__":
    unittest.main()
