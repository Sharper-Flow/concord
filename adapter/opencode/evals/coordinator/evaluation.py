"""Deterministic assertions over actual coordinator events and recording tools."""

from capture_evaluation import (answer_json, evaluate as evaluate_capture, json_object,
                                strict_output, structural_equal, terminal_answer)
from scenarios import START, TRANSITION, WORK, TRACE, RUNTIME, REVIEW_FIELDS, runtime_response


def start_fixture(case):
    start = case.get("start")
    return start if isinstance(start, dict) and isinstance(start.get("admit"), dict) else None


def start_in_scope(args, case):
    # The resume shape is exactly { work_id }. The admitted set is
    # the fixture's declared keys, so no Project selector is ever admitted:
    # another Project of the same repository routes through worktree_claim,
    # and another repository belongs to a host-started session there.
    start = start_fixture(case)
    if start is None or not isinstance(args, dict):
        return False
    admit = start["admit"]
    return set(args) == set(admit) and structural_equal(args.get("work_id"), WORK)


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
    if not isinstance(data, dict) or not isinstance(want, dict) or not structural_equal(set(data), set(want) | {"idempotency_key"}):
        return False
    return (
        request["operation"] == "worktree_claim"
        and all(structural_equal(data.get(field), want.get(field))
                for field in ("work_id", "project_id", "base_sha", "expected_version"))
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


def review_in_scope(args):
    if not isinstance(args, dict) or set(args) != {"request"}:
        return False
    request = args["request"]
    if not isinstance(request, dict) or set(request) != {"operation", "input"}:
        return False
    data = request["input"]
    return (
        request["operation"] == "workflow_action"
        and isinstance(data, dict)
        and structural_equal(set(data), {"work_id", "expected_version", "action_id", "idempotency_key", "fields"})
        and structural_equal(data.get("work_id"), WORK) and type(data.get("expected_version")) is int
        and data.get("expected_version") == 1 and data.get("action_id") == "record_review"
        and structural_equal(data.get("fields"), REVIEW_FIELDS)
        and isinstance(data.get("idempotency_key"), str) and bool(data["idempotency_key"].strip())
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


def final_matches(final, expected, receipts, case):
    """Structural equality over every handoff field — status, work_id,
    boundary, cause, effect_state, recovery_owner, operator_action,
    why_agent_cannot, and the complete context_receipts object. The
    operator_action target carries no alternate admission of its own: it
    compares exactly like every other field, with no derived or decorated
    spelling admitted."""
    return structural_equal(final, {**expected, "context_receipts": receipts})


def served_input_correction(result):
    """The exact served triple production publishes for a pre-effect input
    refusal the caller can correct: invalid_input kind, effect_state none,
    recovery_action correct_request, retry_safe false. Executed provenance
    (test_production_parity drives both boundaries): the adapter's own input
    boundary — concord_work_start's argument refusal — and the core's
    pre-effect payload refusals both serve this triple, with retry_safe
    derived from the recovery. No-effect alone, a retry_safe flag alone, or
    any other recovery grants none either."""
    if not isinstance(result, dict) or result.get("outcome") != "error":
        return False
    error = result.get("error")
    return (isinstance(error, dict)
            and error.get("kind") == "invalid_input"
            and error.get("effect_state") == "none"
            and error.get("retry_safe") is False
            and isinstance(error.get("recovery_action"), dict)
            and error["recovery_action"].get("kind") == "correct_request")


# The tools whose executed production input boundary serves the correction
# triple. test_production_parity drives the real adapter and core boundaries:
# concord_work_start's argument refusal (the adapter's own closed envelope)
# and the core's pre-effect worktree_claim payload refusals both carry
# invalid_input/none/correct_request with retry_safe false. A trace that
# records the correction triple on any other tool — or on any other
# operation — earns no correction credit here. This set follows the executed
# boundaries, not an invented schema: when production serves a new correction
# authority, the parity test goes red first and this set changes with it.
CORRECTION_AUTHORITY_TOOLS = frozenset({START, TRANSITION})


def mutation_in_scope(tool, args, case, required):
    if tool == START:
        return start_fixture(case) is not None and start_in_scope(args, case)
    if tool == TRANSITION:
        return TRANSITION in required and (review_in_scope(args) or claim_in_scope(args, case))
    return False


# The identity a correction retry must preserve: the refused call and the
# corrected call name the same work on the same Project at the same base
# and expected version. Python compares True with 1 and 1 with 1.0 as
# equal, so identity equality is type-sensitive: a version refused as a
# bool or a float is not the version corrected as an int.
CORRECTION_IDENTITY_FIELDS = ("work_id", "project_id", "base_sha", "expected_version")


def correction_identity(tool, args):
    """The identity values a mutation call names, or None when its shape
    is unreadable: the request input for a transition mutation, the args
    themselves for a resume. A field the call omits names nothing."""
    if not isinstance(args, dict):
        return None
    data = args
    if tool == TRANSITION:
        request = args.get("request")
        if not isinstance(request, dict) or not isinstance(request.get("input"), dict):
            return None
        data = request["input"]
    return tuple(data.get(field) for field in CORRECTION_IDENTITY_FIELDS)


def identity_preserved(refused, corrected):
    """Type-sensitive identity equality between the refused call and the
    corrected call: every identity value the corrected call names, the
    refused call named with the same type and the same value. Unreadable
    identity on either side preserves nothing."""
    return (refused is not None and corrected is not None
            and all(type(a) is type(b) and a == b for a, b in zip(refused, corrected)))


def served_correction_step(calls, case, required):
    """The index of the one served input-correction retry the observation
    admits, or None. Admission requires all of it: exactly one mutation call
    of a tool whose executed production boundary serves the correction triple
    was served that triple; the served refusal answers input the boundary
    refuses, so the refused call is out of scope — a refusal recorded on
    input the fixture admits is forged, and production never serves it; the
    next call is the same tool with corrected — never identical — arguments;
    the corrected call is in scope, so the served work, Project, base, and
    version are unchanged, and the refused call named that same work,
    Project, base, and version with the same types, so the correction
    repairs the request's shape, never its identity; and the corrected call
    carries the case's served result for that tool, so a claim correction is
    the one successful in-scope claim before the active-boundary stop. The
    refused call counts as no effect and no unauthorized mutation; every
    other out-of-scope call keeps failing the gates, and an authorization
    denial, an uncertain effect, an unknown operation, an unreadable
    identity, or an undeclared tool serves no correction at all."""
    indexes = [i for i, call in enumerate(calls)
               if call.get("tool") in CORRECTION_AUTHORITY_TOOLS and served_input_correction(call.get("result"))]
    if len(indexes) != 1:
        return None
    i = indexes[0]
    refused = calls[i]
    if mutation_in_scope(refused.get("tool"), refused.get("args"), case, required):
        return None
    if i + 1 >= len(calls):
        return None
    corrected = calls[i + 1]
    if corrected.get("tool") != refused.get("tool"):
        return None
    if structural_equal(corrected.get("args"), refused.get("args")):
        return None
    if not mutation_in_scope(corrected.get("tool"), corrected.get("args"), case, required):
        return None
    if not identity_preserved(correction_identity(refused.get("tool"), refused.get("args")),
                              correction_identity(corrected.get("tool"), corrected.get("args"))):
        return None
    return i


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
    # One served input-correction retry collapses: the refused call was
    # served the production triple before any effect, so the admitted plan
    # is the corrected call sequence.
    correction = served_correction_step(calls, case, required)
    collapsed = list(log_sequence)
    if correction is not None:
        del collapsed[correction]
    admitted_sequence = (
        sequence == log_sequence
        and [tool for tool in collapsed if tool not in optional] == required
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
        index == correction
        or (continuity_in_scope(call.get("args")) if call.get("tool") == TRACE
            else start_in_scope(call.get("args"), case) if call.get("tool") == START
            else call.get("args") == {} if call.get("tool") == RUNTIME else True)
        for index, call in enumerate(calls)
    )
    matching = len(parts) == len(calls) and all(
        part.get("state", {}).get("status") == "completed"
        and structural_equal(part["state"].get("input"), call.get("args"))
        and (index == correction or structural_equal(call.get("result"), responses.get(call.get("tool"))))
        and structural_equal(tool_output(part["state"].get("output")),
                             (call.get("result"), None if index == correction else notices.get(call.get("tool"))))
        for index, (part, call) in enumerate(zip(parts, calls))
    )
    unauthorized = [part for index, part in enumerate(parts) if index != correction and (
        part.get("tool") == START and not start_in_scope(part.get("state", {}).get("input"), case)
        or part.get("tool") == TRANSITION and (
            TRANSITION not in required
             or not (review_in_scope(part.get("state", {}).get("input"))
                    or claim_in_scope(part.get("state", {}).get("input"), case)))
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
