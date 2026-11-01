#!/usr/bin/env python3
"""Describe local command selection without discovery or test execution.

This is an accident guard, not an impact analysis or a resource-cost proof.
Unknown command grammars require explicit broad admission. Native tools retain
execution and caching; a process exit is not proof that named tests ran.
"""

from __future__ import annotations

import json
import re
import sys


class PlanError(ValueError):
    pass


def native_command(command: list[str]) -> list[str]:
    """Unwrap only documented env assignments/unsets; never parse shell text."""
    if command[:1] != ["env"]:
        return command
    index = 1
    while index < len(command):
        value = command[index]
        if value == "-u" and index + 1 < len(command):
            if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", command[index + 1]):
                raise PlanError("invalid env unset")
            index += 2
        elif re.match(r"^[A-Za-z_][A-Za-z0-9_]*=", value):
            index += 1
        else:
            break
    return command[index:]


def go_selection(args: list[str]) -> list[str]:
    packages = []
    pattern = None
    index = 0
    # Unknown flags can change execution (benchmarks, fuzzing, custom TestMain
    # flags, discovery). They belong on the explicit broad route.
    values = {"-run", "-timeout", "-count", "-parallel", "-cpu", "-p"}
    switches = {"-v", "-race", "-short", "-json", "-failfast"}
    while index < len(args):
        token = args[index]
        flag, separator, value = token.partition("=")
        if flag in values:
            if not separator:
                index += 1
                if index >= len(args):
                    raise PlanError(f"missing value for {flag}")
                value = args[index]
            if flag == "-run":
                if pattern is not None:
                    raise PlanError("repeated -run")
                pattern = value
        elif flag in switches and (not separator or value in {"true", "false"}):
            pass
        elif token.startswith("-"):
            raise PlanError(f"unsupported targeted Go flag {flag}")
        elif "..." in token or token.endswith(".go") or token in {"all", "std", "cmd"}:
            raise PlanError("targeted Go checks need explicit packages, not wildcards or file lists")
        else:
            packages.append(token)
        index += 1
    if not packages or not pattern:
        raise PlanError("targeted Go checks need explicit packages and nonempty -run")
    # Only literal fragments or anchored top-level names are admitted here.
    # Subtest and arbitrary regexes need an explicitly admitted broad run.
    # Alternation binds looser than anchors, so more than one name needs the
    # group: '^TestA|TestB$' matches any name starting TestA or ending TestB.
    name = r"(?:Test|Example|Fuzz)[A-Za-z0-9_]*"
    match = re.fullmatch(rf"\^(?:({name})|\(({name}(?:\|{name})*)\))\$", pattern)
    if match:
        return (match.group(1) or match.group(2)).split("|")
    if re.fullmatch(r"[A-Za-z0-9_]+(?:\|[A-Za-z0-9_]+)*", pattern):
        return []  # Fragments do not identify expected full test names.
    raise PlanError("targeted -run must be nonempty literal fragments (e.g. 'Overlap|Sequenc') or anchored literal test names (e.g. '^TestName$' or '^(TestA|TestB)$')")


def plan(tier: str, command: list[str]) -> dict:
    if not command or tier not in {"targeted", "broad"}:
        raise PlanError("expected targeted or broad and a nonempty command")
    result = {"tier": tier, "admission": "full" if tier == "broad" else "targeted",
              "command": command, "evidence": "process-only; named execution is not verified",
              "expected_tests": [], "exclusions": "No automatic changed-file coverage claim"}
    if tier == "broad":
        result["reason"] = "Caller explicitly requested broad/unknown workload admission"
        return result
    native = native_command(command)
    if native[:2] == ["go", "test"]:
        result["expected_tests"] = go_selection(native[2:])
        result["reason"] = ("Explicit packages and finite anchored top-level test selection"
                            if result["expected_tests"] else
                            "Explicit packages and finite literal fragment selection; full test names unknown")
    elif native[:2] == ["bun", "test"] and len(native) > 2 and all(
        not token.startswith("-") and re.fullmatch(r"(?:\./)?adapter/[^\s]*\.(?:test|spec)\.[cm]?[jt]sx?", token)
        for token in native[2:]
    ):
        result["reason"] = "Explicit adapter test files (file existence/results remain native-tool checks)"
    elif len(native) >= 2 and native[0] == "python3" and re.fullmatch(
        r"scripts/(?:test|check)-[a-z0-9-]+\.py", native[1]
    ) and native[1] != "scripts/check-json.py":
        result["reason"] = "One repository validator or script test suite with native arguments"
    else:
        raise PlanError("command is not a supported bounded targeted shape")
    return result


def main(argv: list[str]) -> int:
    emit = argv[:1] == ["--json"]
    if emit:
        argv = argv[1:]
    try:
        tier, *command = argv
        if command[:1] == ["--"]:
            command = command[1:]
        result = plan(tier, command)
    except (PlanError, ValueError) as error:
        print(f"local-test-plan: {error}; use bin/oc-test broad -- <command> for broad or unknown workloads", file=sys.stderr)
        return 64
    if emit:
        print(json.dumps(result, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
