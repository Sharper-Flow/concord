"""Evaluate retained coordinator observations, not predicted policy decisions."""
import json
import re


def structural_equal(actual, expected):
    """Type-sensitive structural equality. Python's == admits True == 1 and
    1 == 1.0, so an event log that records a bool where the call log records
    an int, a float where an int was served, or nested type drift inside a
    result envelope compares equal under == and hides the drift. Every owning
    event/log/result and in-scope identity comparison goes through this
    walker: two values are structurally equal only when their types, their
    shapes, and their values match exactly, recursively."""
    if type(actual) is not type(expected):
        return False
    if isinstance(actual, dict):
        return (set(actual) == set(expected)
                and all(structural_equal(actual[key], expected[key]) for key in actual))
    if isinstance(actual, (list, tuple)):
        return (len(actual) == len(expected)
                and all(structural_equal(item, other) for item, other in zip(actual, expected)))
    return actual == expected

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


FENCED_BLOCK = re.compile(r"```[ \t]*(?:json[ \t]*)?\r?\n(.*?)\r?\n?[ \t]*```", re.DOTALL)


def text_events(events):
    return [event.get("part", {}).get("text") for event in events if event.get("type") == "text"]


def terminal_answer(events):
    """The terminal assistant answer is the last text event in the stream.

    `opencode run --format json` emits one text event per finished text part,
    so intermediate narration and the terminal answer differ by position,
    never by count.
    """
    texts = text_events(events)
    return texts[-1] if texts else None


def answer_json(text):
    """The JSON object the terminal answer carries: the whole text, else its
    last fenced JSON block. Anything else carries no measured answer."""
    if not isinstance(text, str):
        return {}
    whole = json_object(text)
    if whole != {}:
        return whole
    for block in reversed(FENCED_BLOCK.findall(text)):
        value = json_object(block.strip())
        if value != {}:
            return value
    return {}


def strict_output(events):
    """The harness FORMAT contract: exactly one text event, itself one bare
    JSON object with no Markdown and no other text."""
    texts = text_events(events)
    return len(texts) == 1 and json_object(texts[0]) != {}
