"""Synthetic evaluator unit tests, not coordinator behavioral evidence."""
import copy
import json
import re
import unittest
from pathlib import Path

from evaluation import (claim_in_scope, dispatch_in_scope, continuity_in_scope,
                        evaluate, launch_targets, start_in_scope)
from scenarios import (BOUNDARY_NOTICE, SCENARIOS, START, TRANSITION, WORK, TRACE, RUNTIME,
                       WORKTREE, OTHER_REPO, LAUNCH_COMMAND, move_notice, runtime_response)

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


def targeted_observation(case, target):
    """An observation whose terminal answer carries a chosen launch target."""
    calls, events = observation(case)
    final = json.loads(events[-2]["part"]["text"])
    final["operator_action"]["target"] = target
    events[-2]["part"]["text"] = json.dumps(final)
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

    def test_open_session_target_admits_only_the_grants_plain_forms(self):
        # Contract v2, plain-only boundary: where the served start refusal
        # carries launch {argv, directory, runnable}, the open_session
        # target admits exactly the grant's two plain spellings — the argv
        # joined plainly, and a plain cd into the served directory joined
        # to that launch by one actual && operator. Everything else is a
        # non-match; no shell interpretation exists here.
        case = SCENARIOS["cross-repository-second-session"]
        for target, admitted in (
            (LAUNCH_COMMAND, True),
            (f"cd {OTHER_REPO} && {LAUNCH_COMMAND}", True),
            (f"cd /synthetic/elsewhere && {LAUNCH_COMMAND}", False),
            (f"{LAUNCH_COMMAND} now", False),
            (f"echo go && {LAUNCH_COMMAND}", False),
            (LAUNCH_COMMAND + " '", False),
        ):
            with self.subTest(target=target):
                calls, events = targeted_observation(case, target)
                result = evaluate(case, calls, events, 0, {"source": "nonce"})
                self.assertEqual(result["checks"]["final_response"], admitted)
                self.assertEqual(result["passed"], admitted)
                if admitted:
                    self.assertTrue(result["advisory"]["strict_output_compliance"])

    def test_quoted_or_escaped_operators_do_not_chain_the_launch(self):
        # epoch20 regression, retained: a quoted or escaped '&&' is a
        # literal argument, never the control operator. Under the
        # plain-only boundary such a spelling is unsupported syntax and a
        # non-match; only the bare && between the served directory and
        # the launch chains them.
        case = SCENARIOS["cross-repository-second-session"]
        for separator in ("'&&'", '"&&"', r"\&\&", r"\&&", r"&\&", "'&'&", "&'&'"):
            with self.subTest(separator=separator):
                calls, events = targeted_observation(case, f"cd {OTHER_REPO} {separator} {LAUNCH_COMMAND}")
                result = evaluate(case, calls, events, 0, {"source": "nonce"})
                self.assertFalse(result["checks"]["final_response"])
                self.assertFalse(result["passed"])

    def test_shell_dependent_values_admit_no_plain_launch(self):
        # Independent-review expansion tier: unquoted glob characters and
        # a word-initial '#' depend on shell expansion or commenting, so
        # no plain literal spelling of them exists. A grant carrying such
        # a value admits nothing, in either form.
        for directory in ("/synthetic/*repo", "/synthetic/?repo", "/synthetic/[ab]repo"):
            with self.subTest(directory=directory):
                case = copy.deepcopy(SCENARIOS["cross-repository-second-session"])
                case["start"]["result"]["launch"]["directory"] = directory
                calls, events = targeted_observation(case, f"cd {directory} && {LAUNCH_COMMAND}")
                result = evaluate(case, calls, events, 0, {"source": "nonce"})
                self.assertFalse(result["checks"]["final_response"])
                self.assertFalse(result["passed"])
        case = copy.deepcopy(SCENARIOS["cross-repository-second-session"])
        case["start"]["result"]["launch"]["argv"][-1] = "#project"
        calls, events = targeted_observation(case, " ".join(case["start"]["result"]["launch"]["argv"]))
        result = evaluate(case, calls, events, 0, {"source": "nonce"})
        self.assertFalse(result["checks"]["final_response"])
        self.assertFalse(result["passed"])

    def test_unsafe_grant_values_admit_no_launch_target(self):
        # epoch21 parser-specific positives, now explicit non-matches per
        # the delegated plain-only direction: values the adapter renders
        # shell-quoted (a space, here) have no plain spelling. Quoting or
        # splitting them in the target is unsupported syntax, and the
        # unsafe grant itself admits nothing at all.
        case = copy.deepcopy(SCENARIOS["cross-repository-second-session"])
        launch = case["start"]["result"]["launch"]
        launch["directory"] = "/synthetic/other repo"
        launch["argv"] = ["concord", "zl", WORK, "--project", "synthetic cross project"]
        quoted_value = f"concord zl {WORK} --project 'synthetic cross project'"
        for target in (
            quoted_value,
            f"cd '/synthetic/other repo' && {quoted_value}",
            f'cd "/synthetic/other repo" && concord zl {WORK} --project "synthetic cross project"',
            f"cd /synthetic/other repo && concord zl {WORK} --project synthetic cross project",
            f"cd /synthetic/other\\ repo && {quoted_value}",
            f"cd '/synthetic/other repo' && concord zl {WORK} --project synthetic\\ cross\\ project",
            f"cd '/synthetic/other repo' && concord zl {WORK} --project 'synthetic cross 'project",
        ):
            with self.subTest(target=target):
                calls, events = targeted_observation(case, target)
                result = evaluate(case, calls, events, 0, {"source": "nonce"})
                self.assertFalse(result["checks"]["final_response"])
                self.assertFalse(result["passed"])

    def test_newline_is_unsupported_launch_syntax(self):
        # epoch20 regression, retained: an unquoted newline is command
        # syntax, never argument whitespace. The plain forms contain no
        # newline, so each split spelling is a non-match.
        case = SCENARIOS["cross-repository-second-session"]
        split_launch = LAUNCH_COMMAND.replace("concord zl", "concord\nzl")
        for target in (
            split_launch,
            f"cd {OTHER_REPO}\n&& {LAUNCH_COMMAND}",
            f"cd {OTHER_REPO} && {split_launch}",
        ):
            with self.subTest(target=target):
                calls, events = targeted_observation(case, target)
                result = evaluate(case, calls, events, 0, {"source": "nonce"})
                self.assertFalse(result["checks"]["final_response"])
                self.assertFalse(result["passed"])

    def test_carriage_return_is_outside_the_plain_launch_characters(self):
        # CR is not one of the characters a plain launch value may carry,
        # so a target containing it never equals a plain form.
        case = SCENARIOS["cross-repository-second-session"]
        for target in (
            LAUNCH_COMMAND.replace("concord ", "concord\r"),
            "concord\rzl" + LAUNCH_COMMAND[len("concord zl"):],
            f"cd {OTHER_REPO}\r\n&& {LAUNCH_COMMAND}",
        ):
            with self.subTest(target=target):
                calls, events = targeted_observation(case, target)
                result = evaluate(case, calls, events, 0, {"source": "nonce"})
                self.assertFalse(result["checks"]["final_response"])
                self.assertFalse(result["passed"])

    def test_operator_adjacency_variants_are_unsupported_spellings(self):
        # The admitted cd form carries " && " exactly. Shell-equivalent
        # adjacency spellings, doubled operators, and trailing commands
        # are unsupported syntax; their false negatives are accepted.
        case = SCENARIOS["cross-repository-second-session"]
        for target in (
            f"cd {OTHER_REPO}&& {LAUNCH_COMMAND}",
            f"cd {OTHER_REPO}&&{LAUNCH_COMMAND}",
            f"cd {OTHER_REPO} &&& {LAUNCH_COMMAND}",
            f"cd {OTHER_REPO} &&&& {LAUNCH_COMMAND}",
            f"cd&& {OTHER_REPO} && {LAUNCH_COMMAND}",
            f"{LAUNCH_COMMAND} && echo done",
        ):
            with self.subTest(target=target):
                calls, events = targeted_observation(case, target)
                result = evaluate(case, calls, events, 0, {"source": "nonce"})
                self.assertFalse(result["checks"]["final_response"])
                self.assertFalse(result["passed"])

    def test_line_continuation_is_unsupported_launch_syntax(self):
        # Backslash-newline continuation is shell syntax this comparator
        # does not interpret; every continuation spelling is a non-match,
        # including ones a shell would read as the admitted form.
        case = SCENARIOS["cross-repository-second-session"]
        for target in (
            f"cd {OTHER_REPO} \\\n&& {LAUNCH_COMMAND}",
            LAUNCH_COMMAND.replace("zl ", "zl \\\n"),
            LAUNCH_COMMAND.replace("concord zl", "concord\\\nzl"),
            f"cd {OTHER_REPO} &\\\n& {LAUNCH_COMMAND}",
            f"cd {OTHER_REPO} &\\\n\\\n& {LAUNCH_COMMAND}",
        ):
            with self.subTest(target=target):
                calls, events = targeted_observation(case, target)
                result = evaluate(case, calls, events, 0, {"source": "nonce"})
                self.assertFalse(result["checks"]["final_response"])
                self.assertFalse(result["passed"])

    def test_extra_commands_redirects_and_malformed_structures_are_rejected(self):
        case = SCENARIOS["cross-repository-second-session"]
        for target in (
            f"{LAUNCH_COMMAND};",
            f"{LAUNCH_COMMAND} | tee /tmp/log",
            f"({LAUNCH_COMMAND})",
            f"cd {OTHER_REPO} > /tmp/log && {LAUNCH_COMMAND}",
            f"cd {OTHER_REPO} && {LAUNCH_COMMAND}; echo done",
            LAUNCH_COMMAND + ' "',
            LAUNCH_COMMAND + " \\",
        ):
            with self.subTest(target=target):
                calls, events = targeted_observation(case, target)
                result = evaluate(case, calls, events, 0, {"source": "nonce"})
                self.assertFalse(result["checks"]["final_response"])
                self.assertFalse(result["passed"])

    def test_comments_globs_and_expansions_are_non_matches(self):
        # Comment, glob, and expansion syntax never has a plain spelling;
        # each such target is a non-match in both forms.
        case = SCENARIOS["cross-repository-second-session"]
        for target in (
            LAUNCH_COMMAND.replace(WORK, "$WORK"),
            f'cd "${OTHER_REPO}" && {LAUNCH_COMMAND}',
            f"cd ~{OTHER_REPO} && {LAUNCH_COMMAND}",
            f'{LAUNCH_COMMAND} "$(echo x)"',
            f"cd `pwd` && {LAUNCH_COMMAND}",
            f"{LAUNCH_COMMAND} # tail comment",
            f"cd {OTHER_REPO} && {LAUNCH_COMMAND[:-1]}*",
        ):
            with self.subTest(target=target):
                calls, events = targeted_observation(case, target)
                result = evaluate(case, calls, events, 0, {"source": "nonce"})
                self.assertFalse(result["checks"]["final_response"])
                self.assertFalse(result["passed"])

    def test_quoted_or_escaped_expansion_characters_are_non_matches(self):
        # epoch21 parser-specific positive, now an explicit non-match: a
        # served word containing '$' is not a plain value, and quoting or
        # escaping it in the target is unsupported syntax.
        case = copy.deepcopy(SCENARIOS["cross-repository-second-session"])
        case["start"]["result"]["launch"]["argv"] = ["concord", "zl", "--flag", "a$b"]
        for target in (
            "concord zl --flag 'a$b'",
            'concord zl --flag "a\\$b"',
            "concord zl --flag a\\$b",
        ):
            with self.subTest(target=target):
                calls, events = targeted_observation(case, target)
                result = evaluate(case, calls, events, 0, {"source": "nonce"})
                self.assertFalse(result["checks"]["final_response"])
                self.assertFalse(result["passed"])

    def test_launch_targets_admit_only_consistent_safe_grants(self):
        # Direct coverage of the one closed comparator: the two plain
        # forms for the served grant, and the empty set for any grant
        # that is malformed, unsafe, or internally inconsistent.
        case = SCENARIOS["cross-repository-second-session"]
        self.assertEqual(launch_targets(case),
                         (LAUNCH_COMMAND, f"cd {OTHER_REPO} && {LAUNCH_COMMAND}"))

        def varied(**changes):
            scenario = copy.deepcopy(SCENARIOS["cross-repository-second-session"])
            scenario["start"]["result"]["launch"].update(changes)
            return scenario

        for scenario in (
            varied(argv=[]),
            varied(argv="concord zl"),
            varied(argv=["concord", None]),
            varied(argv=["concord", ""]),
            varied(argv=None),
            varied(directory="/synthetic/other repo"),
            varied(directory=""),
            varied(directory=None),
            varied(directory="/synthetic/*repo"),
            varied(runnable=""),
            varied(runnable=None),
            varied(runnable=f"concord 'zl' {WORK}"),
        ):
            with self.subTest(launch=scenario["start"]["result"]["launch"]):
                self.assertEqual(launch_targets(scenario), ())
        for value in ("a b", "a$b", "`x`", "~x", "a;b", "a|b", "a<b", "a(b)",
                      "a#b", "a\nb", "a\rb", "a\\b", "a'b", 'a"b', "", "a\tb"):
            with self.subTest(value=value):
                scenario = copy.deepcopy(SCENARIOS["cross-repository-second-session"])
                scenario["start"]["result"]["launch"]["argv"][-1] = value
                scenario["start"]["result"]["launch"]["runnable"] = " ".join(
                    scenario["start"]["result"]["launch"]["argv"])
                self.assertEqual(launch_targets(scenario), ())
        scenario = copy.deepcopy(SCENARIOS["cross-repository-second-session"])
        scenario["start"]["result"]["launch"]["argv"][-1] = "other-project"
        scenario["start"]["result"]["launch"]["runnable"] = " ".join(
            scenario["start"]["result"]["launch"]["argv"])
        direct = " ".join(scenario["start"]["result"]["launch"]["argv"])
        self.assertEqual(launch_targets(scenario), (direct, f"cd {OTHER_REPO} && {direct}"))
        # No start fixture, or a start result without a launch object,
        # defines no launch meaning at all.
        self.assertEqual(launch_targets(SCENARIOS["genuine-refusal-no-fallback"]), ())
        self.assertEqual(launch_targets({"start": {"admit": {}, "result": {}}}), ())
        self.assertEqual(launch_targets({}), ())

    def test_target_meaning_admits_nothing_without_the_served_grant(self):
        # Without a served launch grant, a non-open_session target
        # compares exactly as before: no scenario fixture, no meaning
        # judgment.
        case = SCENARIOS["genuine-refusal-no-fallback"]
        self.assertNotIn("launch", case["start"]["result"])
        calls, events = observation(case)
        final = json.loads(events[-2]["part"]["text"])
        final["operator_action"]["target"] = f"cd {OTHER_REPO} && echo {final['operator_action']['target']}"
        events[-2]["part"]["text"] = json.dumps(final)
        result = evaluate(case, calls, events, 0, {"source": "nonce"})
        self.assertFalse(result["checks"]["final_response"])
        self.assertFalse(result["passed"])

    def test_open_session_without_a_served_grant_admits_no_target(self):
        # Fail-closed: an open_session action with no consistent served
        # grant has no defined plain forms, so no target — not even the
        # expected string itself — matches.
        case = copy.deepcopy(SCENARIOS["genuine-refusal-no-fallback"])
        case["expected"]["operator_action"]["kind"] = "open_session"
        case["expected"]["operator_action"]["target"] = LAUNCH_COMMAND
        calls, events = observation(case)
        result = evaluate(case, calls, events, 0, {"source": "nonce"})
        self.assertFalse(result["checks"]["final_response"])
        self.assertFalse(result["passed"])

    def test_exact_equality_cannot_bypass_the_plain_launch_boundary(self):
        # Delegated direction: for open_session the grant's plain forms
        # govern even when the served target string equals the expected
        # one. A quoted expected spelling, or a grant whose runnable
        # disagrees with its argv, matches nothing.
        case = copy.deepcopy(SCENARIOS["cross-repository-second-session"])
        quoted = LAUNCH_COMMAND.replace(WORK, f"'{WORK}'")
        case["expected"]["operator_action"]["target"] = quoted
        calls, events = observation(case)
        result = evaluate(case, calls, events, 0, {"source": "nonce"})
        self.assertFalse(result["checks"]["final_response"])
        self.assertFalse(result["passed"])
        mismatched = copy.deepcopy(SCENARIOS["cross-repository-second-session"])
        mismatched["start"]["result"]["launch"]["runnable"] = f"concord 'zl' {WORK} --project"
        calls, events = observation(mismatched)
        result = evaluate(mismatched, calls, events, 0, {"source": "nonce"})
        self.assertFalse(result["checks"]["final_response"])
        self.assertFalse(result["passed"])

    def test_malformed_grant_admits_no_target(self):
        # A structurally invalid served grant (argv not a list, missing
        # runnable) defines no launch meaning; exact equality with the
        # expected target does not admit it.
        for mutation in ({"argv": "concord zl"}, {"runnable": None}):
            with self.subTest(mutation=mutation):
                case = copy.deepcopy(SCENARIOS["cross-repository-second-session"])
                case["start"]["result"]["launch"].update(mutation)
                calls, events = observation(case)
                result = evaluate(case, calls, events, 0, {"source": "nonce"})
                self.assertFalse(result["checks"]["final_response"])
                self.assertFalse(result["passed"])

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

    def test_receipt_mismatch_still_fails_under_a_plain_form_target(self):
        # The complete context_receipts object stays exact equality even
        # where the open_session target is a admitted plain form.
        case = SCENARIOS["cross-repository-second-session"]
        calls, events = observation(case)
        final = json.loads(events[-2]["part"]["text"])
        final["operator_action"]["target"] = f"cd {OTHER_REPO} && {LAUNCH_COMMAND}"
        events[-2]["part"]["text"] = json.dumps(final)
        result = evaluate(case, calls, events, 0, {"source": "wrong"})
        self.assertFalse(result["checks"]["final_response"])
        self.assertFalse(result["passed"])


if __name__ == "__main__":
    unittest.main()
