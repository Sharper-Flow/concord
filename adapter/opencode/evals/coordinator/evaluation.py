"""Deterministic assertions over actual coordinator events and recording tools."""

from capture_evaluation import (answer_json, evaluate as evaluate_capture, json_object,
                                strict_output, terminal_answer)
from scenarios import START, TRANSITION, WORK, TRACE, RUNTIME, runtime_response

# work_start resume selects a member Project in another repository only
# (cmd/concord/work_resume.go: the invocation must resolve to the requested
# Project). A member Project in the same repository is selected through the
# worktree_claim route instead, so its project id is never admitted here.
ADMITTED_RESUME_PROJECT_IDS = ("synthetic-cross-repo-project",)


def start_fixture(case):
    start = case.get("start")
    return start if isinstance(start, dict) and isinstance(start.get("admit"), dict) else None


def start_in_scope(args, case):
    start = start_fixture(case)
    if start is None or not isinstance(args, dict):
        return False
    admit = start["admit"]
    if set(args) != set(admit) or args.get("work_id") != WORK:
        return False
    project_id = args.get("project_id")
    return project_id is None or project_id in ADMITTED_RESUME_PROJECT_IDS


def transition_fixture(case):
    transition = case.get("transition")
    if not isinstance(transition, dict) or not isinstance(transition.get("admit"), dict):
        return None
    admit = transition["admit"]
    request = admit.get("request")
    if not isinstance(request, dict) or request.get("operation") != "worktree_claim":
        return None
    return transition


def claim_in_scope(args, case):
    transition = transition_fixture(case)
    if transition is None or not isinstance(args, dict) or set(args) != {"request"}:
        return False
    request = args["request"]
    if not isinstance(request, dict) or set(request) != {"operation", "input"}:
        return False
    data, want = request.get("input"), transition["admit"]["request"].get("input")
    if not isinstance(data, dict) or not isinstance(want, dict) or set(data) != set(want) | {"idempotency_key"}:
        return False
    return (
        request["operation"] == "worktree_claim"
        and data.get("work_id") == want.get("work_id")
        and data.get("project_id") == want.get("project_id")
        and data.get("base_sha") == want.get("base_sha")
        and data.get("expected_version") == want.get("expected_version")
        and isinstance(data.get("idempotency_key"), str) and bool(data["idempotency_key"].strip())
    )


def continuity_in_scope(args):
    if not isinstance(args, dict) or set(args) != {"request"}:
        return False
    request = args["request"]
    if not isinstance(request, dict) or set(request) != {"operation", "input"}:
        return False
    data = request["input"]
    if not isinstance(data, dict) or set(data) != {"work_id", "page"}:
        return False
    page = data["page"]
    return (
        request["operation"] == "continuity" and data["work_id"] == WORK
        and isinstance(page, dict) and set(page) == {"cursor", "limit"}
        and page["cursor"] is None and type(page["limit"]) is int
        and 1 <= page["limit"] <= 20
    )


def dispatch_in_scope(args):
    if not isinstance(args, dict) or set(args) != {"request"}:
        return False
    request = args["request"]
    if not isinstance(request, dict) or set(request) != {"operation", "input"}:
        return False
    data = request["input"]
    return (
        request["operation"] == "workflow_action"
        and isinstance(data, dict)
        and set(data) == {"work_id", "expected_version", "action_id", "idempotency_key", "fields"}
        and data["work_id"] == WORK and type(data["expected_version"]) is int
        and data["expected_version"] == 1 and data["action_id"] == "dispatch_worker"
        and data["fields"] == {"lane_id": "implement"}
        and isinstance(data["idempotency_key"], str) and bool(data["idempotency_key"].strip())
    )


def tool_output(output):
    """Split a tool output into its envelope line and the move-notice line after it."""
    if not isinstance(output, str):
        return ({}, None)
    envelope, _, notice = output.partition("\n")
    return (json_object(envelope), notice or None)


def typed_start_boundary(case):
    """The boundary a served start refusal names, in the FORMAT's order: a
    typed boundary field on the error, a core boundary carried in the refusal
    message, then the refusal's error kind. None when no refusal is served."""
    start = start_fixture(case)
    if start is None:
        return None
    result = start.get("result")
    error = result.get("error") if isinstance(result, dict) else None
    if not isinstance(error, dict):
        return None
    named = error.get("boundary")
    if isinstance(named, str) and named:
        return named
    message = error.get("message")
    if isinstance(message, str):
        # cmd/concord/main.go writeOperatorDiagnostic wraps Failure.Error
        # from internal/store/errors.go. Recognize the complete diagnostic
        # prefix, not a fixture-supplied word anywhere in the detail.
        parts = message.split(": ", 4)
        if (len(parts) == 5 and parts[:2] == ["concord work-resume", "store"]
                and parts[2].isidentifier() and parts[3].isidentifier() and parts[4]):
            return parts[2]
    kind = error.get("kind")
    if isinstance(kind, str) and kind:
        return kind
    return START


def typed_transition_boundary(case):
    """The boundary a served claim result names: turn_move_boundary only
    while the appended move notice announces that boundary as active."""
    transition = transition_fixture(case)
    if transition is None:
        return None
    notice = transition.get("notice")
    if isinstance(notice, str) and "A turn-move boundary is active" in notice:
        return "turn_move_boundary"
    return None


def typed_boundary(case):
    """The boundary the typed stopping observation names, or None when the
    served observations name none and the FORMAT falls back to the tool."""
    if transition_fixture(case) is not None:
        return typed_transition_boundary(case)
    return typed_start_boundary(case)


# The characters the production adapter's shellQuote
# (adapter/opencode/concord.ts) renders plainly; it shell-quotes every
# other value. A launch value made only of these characters is the only
# kind a plain, unquoted command spelling exists for.
PLAIN_LAUNCH_CHARACTERS = frozenset(
    "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
    "abcdefghijklmnopqrstuvwxyz"
    "0123456789"
    "_@%+=:,./-"
)


def plain_launch_value(value):
    """A launch value that needs no quoting or expansion: a non-empty
    string whose every character the adapter renders plainly."""
    return isinstance(value, str) and value != "" and set(value) <= PLAIN_LAUNCH_CHARACTERS


def launch_targets(case):
    """The closed set of plain command strings the served typed launch
    grant admits: the argv joined by single spaces, and a plain cd into
    the exact served directory, one literal " && ", then that same join.
    The grant derives from the served fixture, never from model output,
    and admission is fail-closed. The grant must be complete — a
    non-empty argv of non-empty strings, with string directory and
    runnable — internally consistent (runnable equals the plain argv
    join), and wholly safe (every argv word and the directory are plain
    values the adapter renders unquoted). Any other grant, and any value
    the adapter would shell-quote, admits nothing: no plain spelling of
    it exists, and this comparator does not interpret quoting, escaping,
    or any other shell syntax to reconstruct one."""
    start = start_fixture(case)
    if start is None:
        return ()
    result = start.get("result")
    launch = result.get("launch") if isinstance(result, dict) else None
    if not isinstance(launch, dict):
        return ()
    argv, directory = launch.get("argv"), launch.get("directory")
    runnable = launch.get("runnable")
    if not (isinstance(argv, list) and argv
            and all(isinstance(token, str) and token for token in argv)
            and isinstance(directory, str) and bool(directory)
            and isinstance(runnable, str) and bool(runnable)):
        return ()
    if not all(plain_launch_value(value) for value in (*argv, directory)):
        return ()
    direct = " ".join(argv)
    if runnable != direct:
        return ()
    return (direct, f"cd {directory} && {direct}")


def target_matches(actual, expected, case, action):
    """operator_action.target compares exactly for every action except
    open_session, whose meaning the served typed launch grant defines:
    the grant's closed plain forms and nothing else. No exact-equality
    fast path bypasses that boundary — an open_session target that is
    not one of the grant's plain forms is a non-match even when it
    equals the expected string, and an absent, malformed, unsafe, or
    inconsistent grant admits no target at all. Unsupported spellings of
    an otherwise shell-equivalent command — quoting or escaping around
    words or operators, line continuations and newlines, comments,
    globs, redirections, extra commands or operators, expansions — are
    non-matches by design. No parsing, interpretation, or pre-scan of
    the target exists here; the comparison is one closed string
    equality against the served grant's plain forms."""
    if action == "open_session":
        return isinstance(actual, str) and actual in launch_targets(case)
    return actual == expected


def final_matches(final, expected, receipts, case):
    """Exact equality for every handoff field — status, work_id, boundary,
    cause, effect_state, recovery_owner, operator_action.kind,
    why_agent_cannot, and the complete context_receipts object — with only
    operator_action.target additionally admitted through the closed plain
    forms a served launch grant defines. No whole-response equality fast
    path exists: for open_session the grant's plain forms govern even
    when the served target equals the expected one."""
    want = {**expected, "context_receipts": receipts}
    action = expected.get("operator_action")
    if not isinstance(final, dict) or not isinstance(action, dict):
        return final == want
    got = final.get("operator_action")
    if not isinstance(got, dict) or set(got) != set(action):
        return False
    normalized = {**final, "operator_action": {**got, "target": action.get("target")}}
    if normalized != want:
        return False
    return target_matches(got.get("target"), action.get("target"), case, action.get("kind"))


def collapse_declared_replay(case, sequence, calls):
    """Admit one identical start replay only where the scenario declares the production result invites it."""
    starts = [call.get("args") for call in calls if call.get("tool") == START]
    if case.get("admits_one_replay") is True and sequence[:2] == [START, START] and len(starts) == 2 and starts[0] == starts[1]:
        return sequence[1:]
    return sequence


def evaluate(case, calls, events, exit_code, receipts):
    if case.get("capture"):
        return evaluate_capture(calls, events, exit_code, receipts)
    parts = [event.get("part", {}) for event in events if event.get("type") == "tool_use"]
    required = case.get("required", [])
    sequence = [part.get("tool") for part in parts]
    log_sequence = [call.get("tool") for call in calls]
    # A refusal forbids unauthorized effects, not a bounded owning diagnostic.
    # A case that serves continuity admits one continuity read too: the
    # coordinator definitions require it before a consequential action.
    optional = {RUNTIME} if RUNTIME not in required else set()
    if TRACE not in required and TRACE in case.get("responses", {}):
        optional.add(TRACE)
    admitted_sequence = (
        sequence == log_sequence
        and collapse_declared_replay(case, [tool for tool in sequence if tool not in optional], calls) == required
        and all(sequence.count(tool) <= 1 for tool in optional)
    )
    responses = {**case.get("responses", {}), RUNTIME: runtime_response(case)}
    notices = {}
    start = start_fixture(case)
    if start is not None:
        responses[START] = start.get("result")
    transition = transition_fixture(case)
    if transition is not None:
        responses[TRANSITION] = transition.get("result")
        notices[TRANSITION] = transition.get("notice")
    read_scope = all(
        continuity_in_scope(call.get("args")) if call.get("tool") == TRACE
        else start_in_scope(call.get("args"), case) if call.get("tool") == START
        else call.get("args") == {} if call.get("tool") == RUNTIME else True
        for call in calls
    )
    matching = len(parts) == len(calls) and all(
        part.get("state", {}).get("status") == "completed"
        and part["state"].get("input") == call.get("args")
        and tool_output(part["state"].get("output")) == (call.get("result"), notices.get(call.get("tool")))
        and call.get("result") == responses.get(call.get("tool"))
        for part, call in zip(parts, calls)
    )
    unauthorized = [part for part in parts if (
        part.get("tool") == START and not start_in_scope(part.get("state", {}).get("input"), case)
        or part.get("tool") == TRANSITION and (
            TRANSITION not in required
            or not (dispatch_in_scope(part.get("state", {}).get("input"))
                    or claim_in_scope(part.get("state", {}).get("input"), case))
        )
    )]
    final = answer_json(terminal_answer(events))
    # The measured boundary comes from the typed stopping observation, never
    # from a label the scenario hand-picks: a refusal names its boundary
    # through its typed fields, and a move notice names the turn-move
    # boundary only while it is active.
    expected = dict(case["expected"])
    boundary = typed_boundary(case)
    if boundary is not None:
        expected["boundary"] = boundary
    checks = {
        "normal_exit": exit_code == 0,
        "terminal_stop": any(event.get("type") == "step_finish" and event.get("part", {}).get("reason") == "stop" for event in events),
        "no_runtime_errors": not any(event.get("type") == "error" for event in events),
        "admitted_tool_sequence": admitted_sequence,
        "trace_matches_event": matching,
        "read_scope": read_scope,
        "no_unauthorized_mutations": not unauthorized,
        "final_response": final_matches(final, expected, receipts, case),
    }
    # Presentation diagnostics do not change behavioral acceptance.
    return {
        "checks": checks, "passed": all(checks.values()), "final_response": final,
        "observed_calls": calls, "tool_event_count": len(parts),
        "unauthorized_mutation_calls": len(unauthorized),
        "advisory": {"strict_output_compliance": strict_output(events)},
    }
