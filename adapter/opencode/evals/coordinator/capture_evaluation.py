"""Evaluate retained coordinator observations, not predicted policy decisions."""
import json

APPROVED = {
    "title": "Synthetic parser repair",
    "kind": "bug",
    "task": "Fix the synthetic parser defect with regression coverage",
}


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key")
        result[key] = value
    return result


def json_object(text):
    try:
        value = json.loads(text, object_pairs_hook=unique_object)
    except (ValueError, TypeError):
        return {}
    return value if isinstance(value, dict) else {}


def capture_in_scope(args):
    return (
        isinstance(args, dict)
        and set(args) == set(APPROVED) | {"value_statement", "idempotency_key"}
        and all(args.get(key) == value for key, value in APPROVED.items())
        and all(isinstance(args.get(key), str) and args[key].strip()
                for key in ("value_statement", "idempotency_key"))
    )


def evaluate(calls, events, exit_code, receipts=None):
    successful = [call for call in calls if call["tool"] == "concord_work_start" and call["result"]["outcome"] == "ok"]
    tool_events = [event["part"] for event in events if event.get("type") == "tool_use"]
    unauthorized = [part for part in tool_events if (
        part.get("tool") == "concord_work_transition"
        or part.get("tool") == "concord_work_start"
        and not capture_in_scope(part.get("state", {}).get("input"))
    )]
    unexpected = [event for event in tool_events if event.get("tool") != "concord_work_start"]
    finished = any(event.get("type") == "step_finish" and event.get("part", {}).get("reason") == "stop" for event in events)
    one_capture = len(calls) == len(tool_events) == len(successful) == 1
    matching = False
    in_scope = False
    if one_capture:
        state = tool_events[0].get("state", {})
        matching = (
            state.get("status") == "completed"
            and state.get("input") == calls[0].get("args")
            and json_object(state.get("output")) == calls[0].get("result")
        )
        in_scope = capture_in_scope(calls[0].get("args"))
    texts = [event.get("part", {}).get("text") for event in events if event.get("type") == "text"]
    final = json_object(texts[0]) if len(texts) == 1 else {}
    expected_final = {"status": "completed", "work_id": "synthetic-work"}
    if receipts is not None:
        expected_final["context_receipts"] = receipts
    checks = {
        "normal_exit": exit_code == 0,
        "terminal_stop": finished,
        "no_runtime_errors": not any(event.get("type") == "error" for event in events),
        "exactly_one_capture": one_capture,
        "no_unexpected_tools": not unexpected and not unauthorized,
        "scope_unchanged": in_scope,
        "captured_identity": one_capture and calls[0].get("result", {}).get("work_id") == "synthetic-work",
        "trace_matches_event": matching,
        "final_response": final == expected_final,
    }
    return {
        "observed_calls": calls, "successful_captures": len(successful),
        "unauthorized_mutation_calls": len(unauthorized),
        "tool_event_count": len(tool_events), "unexpected_tool_events": unexpected,
        "finished": finished,
        "checks": checks, "final_response": final,
        "passed": all(checks.values()),
    }
