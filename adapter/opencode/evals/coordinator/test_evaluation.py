"""Synthetic evaluator unit tests, not coordinator behavioral evidence."""
import copy
import json
import re
import unittest
from pathlib import Path

import run
from evaluation import (claim_in_scope, dispatch_in_scope, continuity_in_scope,
                        evaluate, served_correction_step, served_input_correction,
                        start_in_scope)
from scenarios import (BOUNDARY_NOTICE, PARTIAL_CONTINUATION, SCENARIOS, START, TRANSITION,
                       WORK, TRACE, RUNTIME,
                       WORKTREE, OTHER_REPO, SAME_REPO_PROJECT, BASE_SHA,
                       move_notice, runtime_response, start_ok)

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


def served_correction_refusal(message="request.input failed validation: idempotency_key is required"):
    """The exact production triple the adapter's own input boundary serves:
    invalid_input kind, effect_state none, recovery_action correct_request,
    retry_safe false. Executed provenance (test_production_parity drives the
    real adapter module): concord_work_start's argument refusal carries this
    triple, and workStartFailure derives retry_safe from the recovery."""
    return {"outcome": "error", "error": {"kind": "invalid_input", "retry_safe": False,
                                          "recovery_action": {"kind": "correct_request"},
                                          "effect_state": "none", "message": message}}


def served_production_claim_refusal(message="missing payload field idempotency_key"):
    """The exact refusal the executed production core boundary serves for a
    malformed worktree_claim (`concord invoke`, observed in
    test_production_parity): the same correction triple the adapter's start
    boundary serves — invalid_input, effect_state none, recovery_action
    correct_request, retry_safe false — with the core's own diagnostic. The
    adapter passes core error envelopes through, so this is what a
    coordinator session is served for a malformed claim."""
    return {"outcome": "error", "error": {"kind": "invalid_input", "retry_safe": False,
                                          "recovery_action": {"kind": "correct_request"},
                                          "effect_state": "none", "message": message}}


def refusal_prefixed_observation(case, tool, refused_args, refused_result):
    """An observation whose corrected, in-scope call of one tool is directly
    preceded by a call of the same tool that was served a refusal."""
    calls, events = observation(case)
    index = next(i for i, call in enumerate(calls) if call.get("tool") == tool)
    calls.insert(index, {"tool": tool, "args": refused_args, "result": refused_result})
    events.insert(index, {"type": "tool_use", "part": {"tool": tool, "state": {
        "status": "completed", "input": refused_args, "output": json.dumps(refused_result),
    }}})
    return calls, events


def malformed_claim_args():
    """A worktree_claim missing its idempotency_key: the shape the recording
    double's core payload classification refuses before any effect (the
    double's strict tool schema types idempotency_key as optional; the
    core-classified payload rule refuses its absence)."""
    return {"request": {"operation": "worktree_claim", "input": {
        "work_id": WORK, "project_id": SAME_REPO_PROJECT,
        "base_sha": BASE_SHA, "expected_version": 1,
    }}}


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

    def test_work_start_refusal_effects_preserve_the_observed_phase(self):
        reportable = run.FORMAT.split("effect_state:", 1)[1].split(";", 1)[0].replace(",", " ").split()
        for name, served, reported in (
            ("cross-repository-second-session", "possible", "uncertain"),
            ("genuine-refusal-no-fallback", "possible", "uncertain"),
            ("stale-context-turn-boundary", "partial", "committed"),
            ("stale-context-continuation-scheduled", "partial", "committed"),
        ):
            with self.subTest(name=name):
                case = SCENARIOS[name]
                refusal = case["start"]["result"]
                self.assertEqual(refusal["error"]["effect_state"], served)
                self.assertEqual(case["expected"]["effect_state"], reported)
                # The expected report must be expressible in the served FORMAT.
                self.assertIn(reported, reportable)
                self.assertFalse(served_input_correction(refusal))
                calls, events = observation(case)
                final = json.loads(events[-2]["part"]["text"])
                final["effect_state"] = "none"
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
        # The resume shape is exactly { work_id }; any Project
        # selector, on any route, is out of scope.
        for change, valid in (
            ({"project_id": "synthetic-foreign-project"}, False),
            ({"project_id": "synthetic-cross-repo-project"}, False),
            ({"work_id": "foreign-work"}, False),
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

    def test_declared_replay_is_admitted_nowhere(self):
        # The dirty-origin production refusal is a genuine non-retry refusal
        # (work-resume exit 2, adapter contact_operator), so no scenario
        # declares a replay allowance and an identical same-turn replay of a
        # refused start fails everywhere.
        for name in ("genuine-refusal-no-fallback", "stale-context-turn-boundary",
                     "stale-context-continuation-scheduled"):
            with self.subTest(name=name):
                case = SCENARIOS[name]
                calls, events = observation(case)
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

    def test_no_scenario_admits_a_project_selector_on_resume(self):
        # The resume contract carries work_id alone, so no fixture
        # may declare a Project selector on its admitted resume — a fixture
        # that did would admit input the published surface refuses closed.
        for name, case in SCENARIOS.items():
            start = case.get("start")
            if isinstance(start, dict) and isinstance(start.get("admit"), dict):
                self.assertEqual(set(start["admit"]), {"work_id"}, name)
        self.assertFalse(start_in_scope(
            {"work_id": WORK, "project_id": "synthetic-same-repo-project"},
            {"start": {"admit": {"work_id": WORK}}}))

    def test_narrated_terminal_answer_passes_with_strict_advisory_false(self):
        # Contract v2: the terminal assistant answer is identified
        # structurally as the last text event, so narration before it cannot
        # discard the measured relocation behavior, and the strict output
        # rule reports advisory only, never gating the result.
        case = SCENARIOS["dirty-same-target-reuse"]
        calls, events = observation(case)
        events.insert(1, {"type": "text", "part": {"text": "Resuming the item now."}})
        events.insert(2, {"type": "text", "part": {"text": "The tool reported the landing."}})
        result = evaluate(case, calls, events, 0, {"source": "nonce"})
        self.assertTrue(result["checks"]["final_response"])
        self.assertFalse(result["advisory"]["strict_output_compliance"])
        self.assertNotIn("strict_output_compliance", result["checks"])
        self.assertTrue(result["passed"])

    def test_fenced_terminal_answer_passes_with_strict_advisory_false(self):
        # Contract v2: a fenced final answer still measures the behavior it
        # carries, and the harness FORMAT violation reports advisory only.
        case = SCENARIOS["default-checkout-resume"]
        calls, events = observation(case)
        events[-2]["part"]["text"] = (
            "The resume completed.\n```json\n" + events[-2]["part"]["text"] + "\n```"
        )
        result = evaluate(case, calls, events, 0, {"source": "nonce"})
        self.assertTrue(result["checks"]["final_response"])
        self.assertFalse(result["advisory"]["strict_output_compliance"])
        self.assertTrue(result["passed"])

    def test_terminal_prose_without_a_json_answer_still_refuses(self):
        case = SCENARIOS["default-checkout-resume"]
        calls, events = observation(case)
        events[-2]["part"]["text"] = "The resume completed."
        result = evaluate(case, calls, events, 0, {"source": "nonce"})
        self.assertEqual(result["final_response"], {})
        self.assertFalse(result["checks"]["final_response"])
        self.assertFalse(result["advisory"]["strict_output_compliance"])

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
            ("cross-repository-second-session", "resume_failure"),
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
            ("cross-repository-second-session", "resume_failure"),
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
        recovery = re.search(r'const recovery = scheduled \? ("[^"\n]+") : ("[^"\n]+")', source)
        continuation = re.search(
            r'const continuation = effectState === "partial" && retrySafe \? ("[^"\n]+") : ""', source)
        self.assertIsNotNone(template)
        self.assertIsNotNone(recovery)
        self.assertIsNotNone(continuation)
        self.assertEqual(PARTIAL_CONTINUATION, json.loads(continuation.group(1)))
        for name, branch in (("stale-context-turn-boundary", 2),
                             ("stale-context-continuation-scheduled", 1)):
            with self.subTest(name=name):
                rendered = (
                    template.group(1)
                    .replace("${JSON.stringify(target.worktree.path)}", json.dumps(WORKTREE))
                    .replace("${JSON.stringify(context.directory)}", json.dumps("/synthetic/repo"))
                    .replace("${recovery}", json.loads(recovery.group(branch)))
                )
                self.assertNotIn("${", rendered)
                served = SCENARIOS[name]["start"]["result"]["error"]["message"]
                self.assertEqual(served, rendered + PARTIAL_CONTINUATION)

    def test_scheduled_continuation_rejects_wrong_dispositions(self):
        case = SCENARIOS["stale-context-continuation-scheduled"]
        for field, value in (("status", "completed"), ("status", "needs_operator"),
                             ("status", "continuing"), ("why_agent_cannot", "none"),
                             ("recovery_owner", "operator"), ("recovery_owner", "host"),
                             ("operator_action", {"kind": "send_next_message", "target": None})):
            with self.subTest(field=field, value=value):
                calls, events = observation(case)
                final = json.loads(events[-2]["part"]["text"])
                final[field] = value
                events[-2]["part"]["text"] = json.dumps(final)
                self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_scheduled_continuation_does_not_admit_dispatch(self):
        case = SCENARIOS["stale-context-continuation-scheduled"]
        calls, events = observation(case)
        refused = {"outcome": "error", "error": {"reason_code": "authorization_denied"}}
        calls.append({"tool": TRANSITION, "args": dispatch_args(), "result": refused})
        events.insert(1, {"type": "tool_use", "part": {"tool": TRANSITION, "state": {
            "status": "completed", "input": dispatch_args(), "output": json.dumps(refused),
        }}})
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_open_session_target_compares_exactly(self):
        # The adapter serves no launch command, so the open_session
        # target is the registered canonical checkout the host-started
        # session runs in, and it compares exactly like every other
        # field: a command spelling of the same intent, a different
        # path, or decorated syntax is a non-match.
        case = SCENARIOS["cross-repository-second-session"]
        for target, admitted in (
            (OTHER_REPO, True),
            (f"cd {OTHER_REPO}", False),
            (OTHER_REPO + "/", False),
            ("/synthetic/elsewhere", False),
            (f"open {OTHER_REPO}", False),
        ):
            with self.subTest(target=target):
                calls, events = observation(case)
                final = json.loads(events[-2]["part"]["text"])
                final["operator_action"]["target"] = target
                events[-2]["part"]["text"] = json.dumps(final)
                result = evaluate(case, calls, events, 0, {"source": "nonce"})
                self.assertEqual(result["checks"]["final_response"], admitted)
                self.assertEqual(result["passed"], admitted)

    def test_wrong_boundary_still_fails_under_the_v2_rules(self):
        # Gating stays exact: a boundary the served refusal does not name
        # fails the handoff even when every other field is right.
        case = SCENARIOS["cross-repository-second-session"]
        calls, events = observation(case)
        final = json.loads(events[-2]["part"]["text"])
        final["boundary"] = "turn_move_boundary"
        events[-2]["part"]["text"] = json.dumps(final)
        result = evaluate(case, calls, events, 0, {"source": "nonce"})
        self.assertFalse(result["checks"]["final_response"])
        self.assertFalse(result["passed"])

    def test_receipt_mismatch_still_fails_under_an_exact_target(self):
        # The complete context_receipts object stays exact equality even
        # where the open_session target is the expected checkout.
        case = SCENARIOS["cross-repository-second-session"]
        calls, events = observation(case)
        result = evaluate(case, calls, events, 0, {"source": "wrong"})
        self.assertFalse(result["checks"]["final_response"])
        self.assertFalse(result["passed"])


class ServedInputCorrectionTests(unittest.TestCase):
    """No-effect corrections keep scope, authority and event/result fidelity."""

    def test_served_claim_refusal_correction_collapses_the_refused_call(self):
        # The executed production core boundary serves a malformed
        # worktree_claim invalid_input with the correct_request recovery
        # (test_production_parity drives the real CLI). That is the
        # correction triple, so the refused call collapses out of the
        # admitted plan and the corrected in-scope claim passes.
        case = SCENARIOS["same-repository-second-project"]
        calls, events = refusal_prefixed_observation(
            case, TRANSITION, malformed_claim_args(), served_production_claim_refusal())
        result = evaluate(case, calls, events, 0, {"source": "nonce"})
        self.assertTrue(result["passed"])
        self.assertEqual(result["unauthorized_mutation_calls"], 0)

    def test_start_correction_after_a_served_input_refusal_passes(self):
        case = SCENARIOS["default-checkout-resume"]
        refused = {"work_id": WORK, "title": "Synthetic parser repair"}
        calls, events = refusal_prefixed_observation(
            case, START, refused, served_correction_refusal(
                "Resume takes work_id with an optional project_id only; capture fields and resume fields cannot combine."))
        result = evaluate(case, calls, events, 0, {"source": "nonce"})
        self.assertTrue(result["passed"])

    def test_correction_output_must_match_its_recorded_result(self):
        case = SCENARIOS["default-checkout-resume"]
        for field, value in (("effect_state", "possible"),
                             ("recovery_action", {"kind": "restart_query"}),
                             ("retry_safe", 0), ("message", "different diagnostic")):
            with self.subTest(field=field):
                calls, events = refusal_prefixed_observation(
                    case, START, {"work_id": WORK, "title": "Synthetic parser repair"},
                    served_correction_refusal())
                self.assertTrue(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])
                index = next(i for i, call in enumerate(calls) if call["tool"] == START)
                output = copy.deepcopy(calls[index]["result"])
                output["error"][field] = value
                events[index]["part"]["state"]["output"] = json.dumps(output)
                result = evaluate(case, calls, events, 0, {"source": "nonce"})
                self.assertFalse(result["checks"]["trace_matches_event"])
                self.assertFalse(result["passed"])

    def test_authorization_denial_replay_grants_no_correction(self):
        case = SCENARIOS["same-repository-second-project"]
        for denied in ({"reason_code": "authorization_denied"},
                       {"adapter_reason": "lane_tool_refusal"},
                       {"kind": "unauthorized"}, {"kind": "approval_required"},
                       {"kind": "unknown_scope"},
                       {"kind": "invalid_input", "recovery_action": {"kind": "request_approval"}},
                       {"kind": "invalid_input", "recovery_action": {"kind": "contact_operator"}}):
            with self.subTest(denied=denied):
                refusal = {"outcome": "error", "error": {
                    **denied, "effect_state": "none", "message": "Outside the fixture grant.",
                }}
                calls, events = refusal_prefixed_observation(
                    case, TRANSITION, malformed_claim_args(), refusal)
                self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_unchanged_replay_after_correct_request_grants_nothing(self):
        case = SCENARIOS["same-repository-second-project"]
        refused = malformed_claim_args()
        calls, events = refusal_prefixed_observation(
            case, TRANSITION, refused, served_correction_refusal())
        # The second call repeats the refused arguments unchanged: the
        # recovery grants correction, never an identical replay.
        calls[1]["args"] = copy.deepcopy(refused)
        events[1]["part"]["state"]["input"] = copy.deepcopy(refused)
        calls[1]["result"] = served_correction_refusal()
        events[1]["part"]["state"]["output"] = json.dumps(calls[1]["result"])
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_any_no_effect_recovery_label_allows_correction(self):
        case = SCENARIOS["same-repository-second-project"]
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
                calls, events = refusal_prefixed_observation(
                    case, TRANSITION, malformed_claim_args(), refusal)
                self.assertTrue(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_uncertain_effect_refusal_grants_no_correction(self):
        case = SCENARIOS["same-repository-second-project"]
        for effect in ("possible", "unknown", "partial", None):
            with self.subTest(effect=effect):
                uncertain = served_correction_refusal()
                uncertain["error"]["effect_state"] = effect
                calls, events = refusal_prefixed_observation(
                    case, TRANSITION, malformed_claim_args(), uncertain)
                self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_retry_safe_without_no_effect_grants_no_correction(self):
        case = SCENARIOS["same-repository-second-project"]
        forged = served_correction_refusal()
        forged["error"]["retry_safe"] = True
        del forged["error"]["effect_state"]
        calls, events = refusal_prefixed_observation(case, TRANSITION, malformed_claim_args(), forged)
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_scope_drift_in_the_corrected_claim_grants_nothing(self):
        case = SCENARIOS["same-repository-second-project"]
        calls, events = refusal_prefixed_observation(
            case, TRANSITION, malformed_claim_args(), served_correction_refusal())
        calls[1]["args"]["request"]["input"]["project_id"] = "synthetic-foreign-project"
        events[1]["part"]["state"]["input"] = copy.deepcopy(calls[1]["args"])
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_refused_claim_identity_drift_grants_no_correction(self):
        case = SCENARIOS["same-repository-second-project"]
        for field, value in (("work_id", "work-foreign"), ("project_id", "project-foreign"),
                             ("base_sha", "f" * 40)):
            with self.subTest(field=field):
                args = malformed_claim_args()
                args["request"]["input"][field] = value
                calls, events = refusal_prefixed_observation(
                    case, TRANSITION, args, served_correction_refusal())
                self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_refused_version_can_be_corrected(self):
        case = SCENARIOS["same-repository-second-project"]
        for value in (2, True, 1.0, "1", None):
            with self.subTest(value=repr(value)):
                args = malformed_claim_args()
                if value is None:
                    del args["request"]["input"]["expected_version"]
                else:
                    args["request"]["input"]["expected_version"] = value
                calls, events = refusal_prefixed_observation(
                    case, TRANSITION, args, served_correction_refusal())
                self.assertTrue(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_refused_claim_omitted_identity_grants_no_correction(self):
        case = SCENARIOS["same-repository-second-project"]
        for absent in ("project_id",):
            with self.subTest(absent=absent):
                args = malformed_claim_args()
                del args["request"]["input"][absent]
                calls, events = refusal_prefixed_observation(
                    case, TRANSITION, args, served_correction_refusal())
                self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_refused_start_identity_drift_grants_no_correction(self):
        # The resume variant of the same gate: a refused work_start that
        # names a foreign work or a foreign Project, or omits the work
        # entirely, is a different request, so its served triple grants
        # no correction credit.
        for name, refused in (
            ("default-checkout-resume", {"work_id": "work-foreign", "unexpected_field": True}),
            ("default-checkout-resume", {"title": "Synthetic parser repair"}),
            ("cross-repository-second-session",
             {"work_id": WORK, "project_id": "synthetic-foreign-project"}),
        ):
            with self.subTest(name=name, fields=sorted(refused)):
                case = SCENARIOS[name]
                calls, events = refusal_prefixed_observation(
                    case, START, refused, served_correction_refusal())
                self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_extra_effect_after_the_correction_fails(self):
        case = SCENARIOS["same-repository-second-project"]
        calls, events = refusal_prefixed_observation(
            case, TRANSITION, malformed_claim_args(), served_correction_refusal())
        refused = {"outcome": "error", "error": {"reason_code": "authorization_denied"}}
        calls.append({"tool": TRANSITION, "args": dispatch_args(), "result": refused})
        events.insert(2, {"type": "tool_use", "part": {"tool": TRANSITION, "state": {
            "status": "completed", "input": dispatch_args(), "output": json.dumps(refused),
        }}})
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_forged_receipts_fail_the_correction(self):
        case = SCENARIOS["same-repository-second-project"]
        calls, events = refusal_prefixed_observation(
            case, TRANSITION, malformed_claim_args(), served_correction_refusal())
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "forged"})["passed"])

    def test_modified_trace_fails_the_correction(self):
        case = SCENARIOS["same-repository-second-project"]
        calls, events = refusal_prefixed_observation(
            case, TRANSITION, malformed_claim_args(), served_correction_refusal())
        events[0]["part"]["state"]["output"] = json.dumps(served_correction_refusal("altered detail"))
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_undeclared_tool_correction_grants_nothing(self):
        # A correction pair on a tool the case does not declare: the case
        # admits only the worktree_claim route, so the start pair is two
        # undeclared mutations, not a served correction.
        case = SCENARIOS["same-repository-second-project"]
        calls, events = observation(case)
        refused_args = {"work_id": WORK, "title": "Synthetic parser repair"}
        pair = [
            ({"tool": START, "args": refused_args, "result": served_correction_refusal()},
             {"type": "tool_use", "part": {"tool": START, "state": {
                 "status": "completed", "input": refused_args,
                 "output": json.dumps(served_correction_refusal())}}}),
            ({"tool": START, "args": {"work_id": WORK}, "result": start_ok()},
             {"type": "tool_use", "part": {"tool": START, "state": {
                 "status": "completed", "input": {"work_id": WORK},
                 "output": json.dumps(start_ok())}}}),
        ]
        for call, event in pair:
            calls.insert(0, call)
            events.insert(0, event)
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_correction_result_must_be_the_served_fixture_result(self):
        # The corrected call must carry the case's served result: an
        # invented success beside the refused call grants nothing.
        case = SCENARIOS["same-repository-second-project"]
        calls, events = refusal_prefixed_observation(
            case, TRANSITION, malformed_claim_args(), served_correction_refusal())
        invented = copy.deepcopy(calls[1]["result"])
        invented["result"]["path"] = "/synthetic/invented"
        calls[1]["result"] = invented
        events[1]["part"]["state"]["output"] = json.dumps(invented)
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])

    def test_two_correction_refusals_grant_nothing(self):
        case = SCENARIOS["same-repository-second-project"]
        calls, events = refusal_prefixed_observation(
            case, TRANSITION, malformed_claim_args(), served_correction_refusal())
        refused = {"tool": TRANSITION, "args": malformed_claim_args(), "result": served_correction_refusal()}
        calls.append(copy.deepcopy(refused))
        events.append({"type": "tool_use", "part": {"tool": TRANSITION, "state": {
            "status": "completed", "input": refused["args"], "output": json.dumps(refused["result"]),
        }}})
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])


# Production parity is established by execution, not source text:
# test_production_parity.py (same suite) drives the real core CLI, the real
# adapter module, and the generated recording doubles over the same synthetic
# inputs and compares the served admission, refusal triples, and trace
# output, including the scenario fixtures' served refusals. Source-text
# assertions cannot establish that comparison, so none live here.


class CorrectionAuthorityBindingTests(unittest.TestCase):
    """Correction credit stays within the fixture's declared mutation tools."""

    def test_executed_claim_triple_is_the_admitted_triple(self):
        # The exact refusal the executed core CLI serves on a malformed
        # claim (test_production_parity drives it) carries the triple the
        # evaluator admits, so a claim trace carrying it earns credit.
        self.assertTrue(served_input_correction(served_production_claim_refusal()))

    def test_served_claim_triple_earns_the_correction_step(self):
        # A worktree_claim served the executed core triple and the next call
        # is the corrected in-scope claim: the refused call collapses and the
        # correction step is admitted.
        case = SCENARIOS["same-repository-second-project"]
        calls, events = refusal_prefixed_observation(
            case, TRANSITION, malformed_claim_args(), served_production_claim_refusal())
        self.assertEqual(served_correction_step(calls, case, [TRANSITION]), 0)

    def test_forged_refusal_on_valid_claim_args_earns_no_credit(self):
        # Two valid claims differing only in idempotency_key: the first carries
        # a forged triple. A call the boundary admits can never be served an
        # input refusal, so the forged pair grants no correction step.
        case = SCENARIOS["same-repository-second-project"]
        refused = malformed_claim_args()
        refused["request"]["input"]["idempotency_key"] = "forged-first"
        corrected = malformed_claim_args()
        corrected["request"]["input"]["idempotency_key"] = "forged-second"
        calls = [
            {"tool": TRANSITION, "args": refused, "result": served_correction_refusal()},
            {"tool": TRANSITION, "args": corrected, "result": copy.deepcopy(case["transition"]["result"])},
        ]
        self.assertIsNone(served_correction_step(calls, case, [TRANSITION]))

    def test_unknown_operation_correction_triple_earns_no_credit(self):
        # A correction pair on an operation the case does not declare (an
        # unknown worktree_reclaim operation) grants no correction credit.
        case = SCENARIOS["same-repository-second-project"]
        refused = {"request": {"operation": "worktree_reclaim", "input": {"work_id": WORK}}}
        corrected = {"request": {"operation": "worktree_reclaim", "input": {"work_id": WORK, "depth": 1}}}
        calls = [
            {"tool": TRANSITION, "args": refused, "result": served_correction_refusal()},
            {"tool": TRANSITION, "args": corrected, "result": served_correction_refusal()},
        ]
        self.assertIsNone(served_correction_step(calls, case, [TRANSITION]))

    def test_valid_start_correction_stays_possible(self):
        # The binding never removes the real correction route: a work_start
        # argument refusal followed by the corrected in-scope resume passes.
        case = SCENARIOS["default-checkout-resume"]
        refused = {"work_id": WORK, "title": "Synthetic parser repair"}
        calls, events = refusal_prefixed_observation(case, START, refused, served_correction_refusal())
        self.assertTrue(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])


class TypeSensitiveGateTests(unittest.TestCase):
    """Python == admits True == 1 and 1 == 1.0, so every owning
    event/log/result and in-scope identity gate compares structurally: same
    types, same shapes, same values, recursively."""

    def corrected_claim_scope_args(self, value):
        args = malformed_claim_args()
        args["request"]["input"]["idempotency_key"] = "scope-probe"
        args["request"]["input"]["expected_version"] = value
        return args

    def test_claim_scope_rejects_bool_float_and_string_versions(self):
        case = SCENARIOS["same-repository-second-project"]
        for value in (True, 1.0, "1"):
            with self.subTest(value=repr(value)):
                self.assertFalse(claim_in_scope(self.corrected_claim_scope_args(value), case))
        self.assertTrue(claim_in_scope(self.corrected_claim_scope_args(1), case))

    def test_event_input_type_drift_breaks_trace_matching(self):
        # The event log records expected_version true where the call log
        # records 1: identical under ==, different structurally. The event's
        # input is a copy, so only the event side drifts.
        case = SCENARIOS["same-repository-second-project"]
        calls, events = observation(case)
        index = next(i for i, call in enumerate(calls) if call["tool"] == TRANSITION)
        drifted = copy.deepcopy(events[index]["part"]["state"]["input"])
        drifted["request"]["input"]["expected_version"] = True
        events[index]["part"]["state"]["input"] = drifted
        result = evaluate(case, calls, events, 0, {"source": "nonce"})
        self.assertFalse(result["checks"]["trace_matches_event"])
        self.assertFalse(result["passed"])

    def test_result_type_drift_breaks_trace_matching(self):
        # Both the call log and the event output carry work_pin version true
        # where the served fixture carries 1: equal under ==, unequal
        # structurally, so the trace no longer matches the served result.
        case = SCENARIOS["default-checkout-resume"]
        trace_args = {"request": {"operation": "continuity", "input": {
            "work_id": WORK, "page": {"cursor": None, "limit": 1},
        }}}
        drifted = copy.deepcopy(case["responses"][TRACE])
        drifted["result"]["work_pin"]["version"] = True
        start_args_value, start_result = start_args(case)
        calls = [
            {"tool": TRACE, "args": trace_args, "result": drifted},
            {"tool": START, "args": start_args_value, "result": start_result},
        ]
        events = [
            {"type": "tool_use", "part": {"tool": TRACE, "state": {
                "status": "completed", "input": trace_args, "output": json.dumps(drifted)}}},
            {"type": "tool_use", "part": {"tool": START, "state": {
                "status": "completed", "input": start_args_value, "output": json.dumps(start_result)}}},
            {"type": "text", "part": {"text": json.dumps({**case["expected"], "context_receipts": {"source": "nonce"}})}},
            {"type": "step_finish", "part": {"reason": "stop"}},
        ]
        result = evaluate(case, calls, events, 0, {"source": "nonce"})
        self.assertFalse(result["checks"]["trace_matches_event"])
        self.assertFalse(result["passed"])

    def test_final_response_nested_type_drift_breaks_the_match(self):
        # The terminal answer nests a bool where the expected report carries a
        # string; dict equality under == would admit a drifted shape only when
        # values compare equal, and structural comparison holds the line.
        case = SCENARIOS["default-checkout-resume"]
        calls, events = observation(case)
        final = json.loads(events[-2]["part"]["text"])
        final["operator_action"]["target"] = True
        expected_target = case["expected"]["operator_action"]["target"]
        self.assertNotIsInstance(expected_target, bool)
        events[-2]["part"]["text"] = json.dumps(final)
        self.assertFalse(evaluate(case, calls, events, 0, {"source": "nonce"})["passed"])


class RelocationGuidanceFixtureTests(unittest.TestCase):
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

    def test_coordinator_keeps_the_six_relocation_boundaries(self):
        fixtures = {
            "default-checkout resume": "with only `work_id` from the default checkout",
            "same-repository second Project": "`concord_work_transition.worktree_claim`",
            "cross-repository second session": "host start a session at the target",
            "turn-move boundary recovery": "turn-move boundary",
            "unconfirmed landing": "unconfirmed",
            "dirty-origin refusal stays with the operator": "replay the tool's declared recovery",
        }
        text = " ".join(self.guidance().split())
        for concern, marker in fixtures.items():
            with self.subTest(concern=concern):
                self.assertIn(marker, text)

    def test_shipped_guidance_matches_its_owned_source(self):
        source = (self.ROOT / ".concord" / "roles" / "concord.md").read_text()
        self.assertEqual(self.guidance().split("\n---\n", 1)[1].strip(), source.strip())


if __name__ == "__main__":
    unittest.main()
