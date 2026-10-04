"""Deterministic assertions over actual coordinator events and recording tools."""
from capture_evaluation import evaluate as evaluate_capture, json_object
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


def evaluate(case, calls, events, exit_code, receipts):
    if case.get("capture"):
        return evaluate_capture(calls, events, exit_code, receipts)
    parts = [event.get("part", {}) for event in events if event.get("type") == "tool_use"]
    required = case.get("required", [])
    sequence = [part.get("tool") for part in parts]
    log_sequence = [call.get("tool") for call in calls]
    # A refusal forbids unauthorized effects, not a bounded owning diagnostic.
    optional = {RUNTIME} if RUNTIME not in required else set()
    admitted_sequence = (
        sequence == log_sequence
        and [tool for tool in sequence if tool not in optional] == required
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
    texts = [event.get("part", {}).get("text") for event in events if event.get("type") == "text"]
    final = json_object(texts[0]) if len(texts) == 1 else {}
    checks = {
        "normal_exit": exit_code == 0,
        "terminal_stop": any(event.get("type") == "step_finish" and event.get("part", {}).get("reason") == "stop" for event in events),
        "no_runtime_errors": not any(event.get("type") == "error" for event in events),
        "admitted_tool_sequence": admitted_sequence,
        "trace_matches_event": matching,
        "read_scope": read_scope,
        "no_unauthorized_mutations": not unauthorized,
        "final_response": final == {**case["expected"], "context_receipts": receipts},
    }
    return {
        "checks": checks, "passed": all(checks.values()), "final_response": final,
        "observed_calls": calls, "tool_event_count": len(parts),
        "unauthorized_mutation_calls": len(unauthorized),
    }
