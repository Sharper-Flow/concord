#!/usr/bin/env python3
"""Tests for the CI umbrella behavior of scripts/check-json.py.

check-json.py is the one complete contract entrypoint in the verify-contracts
job: it nests scripts/check-agent-contracts.py, the nested subprocess inherits
the umbrella step's environment (where CONCORD_REQUIRE_BUN=1 now lives), and a
nonzero nested exit must fail the umbrella. External-suite options forward
ownership to separately required CI jobs. The subprocess layer is faked so
these tests do not execute the nested checkers.
"""
from __future__ import annotations

import ast
import importlib.util
import io
import sys
import unittest
import unittest.mock
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("check_json_umbrella", ROOT / "scripts/check-json.py")
assert SPEC and SPEC.loader
umbrella = importlib.util.module_from_spec(SPEC)
sys.modules["check_json_umbrella"] = umbrella
SPEC.loader.exec_module(umbrella)


def nested_contract_check_call() -> ast.Call:
    """The subprocess.run call in check-json.py that runs the contract check."""
    tree = ast.parse((ROOT / "scripts/check-json.py").read_text(encoding="utf-8"))
    calls = [
        node
        for node in ast.walk(tree)
        if isinstance(node, ast.Call)
        and isinstance(node.func, ast.Attribute)
        and node.func.attr == "run"
        and isinstance(node.func.value, ast.Name)
        and node.func.value.id == "subprocess"
        and any(
            isinstance(child, ast.Constant) and isinstance(child.value, str) and "check-agent-contracts.py" in child.value
            for argument in node.args
            for child in ast.walk(argument)
        )
    ]
    assert len(calls) == 1, f"expected one nested contract-check call, found {len(calls)}"
    return calls[0]


class NestedEnvironmentInheritanceTests(unittest.TestCase):
    def test_the_umbrella_invokes_the_complete_contract_check_exactly_once(self):
        # One complete entrypoint means the nesting itself is load-bearing:
        # deleting the nested call would leave CI with no complete contract
        # validation at all once the direct step is gone.
        nested_contract_check_call()

    def test_the_nested_contract_check_inherits_the_umbrella_environment(self):
        # subprocess.run without env= inherits the calling process's
        # environment, which is how CONCORD_REQUIRE_BUN=1 on the Validate
        # JSON step reaches the nested checker. An explicit env= here would
        # silently drop that inheritance, so passing one is a failure.
        call = nested_contract_check_call()
        self.assertEqual(
            [keyword.arg for keyword in call.keywords if keyword.arg == "env"],
            [],
            "the nested call must inherit the umbrella step's environment, not replace it",
        )


class _Completed:
    def __init__(self, returncode=0, stdout="", stderr=""):
        self.returncode = returncode
        self.stdout = stdout
        self.stderr = stderr


class _FakeSubprocess:
    """Answers every nested checker with success except the named script."""

    def __init__(self, failing: str | None = None):
        self.failing = failing
        self.calls: list[list[str]] = []

    def run(self, args, **kwargs):
        words = [str(arg) for arg in args]
        self.calls.append(words)
        if self.failing and any(self.failing in word for word in words):
            return _Completed(1, "", "simulated nested failure")
        return _Completed()


class UmbrellaPropagationTests(unittest.TestCase):
    def _main(self, fake: _FakeSubprocess, argv: list[str] | None = None) -> tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with (
            unittest.mock.patch.object(umbrella, "repository_files", return_value=[]),
            unittest.mock.patch.object(umbrella, "subprocess", fake),
            redirect_stdout(out),
            redirect_stderr(err),
        ):
            code = umbrella.main(list(argv or []))
        return code, out.getvalue(), err.getvalue()

    def test_a_nonzero_nested_contract_check_fails_the_umbrella(self):
        # The retained wiring is only an optimization if a nested failure
        # still fails the required check; a swallowed returncode would make
        # the umbrella report success over a broken contract check.
        code, out, err = self._main(_FakeSubprocess(failing="check-agent-contracts.py"))
        self.assertEqual(code, 1)
        self.assertIn("agent contract drift", out)
        self.assertIn("JSON validation failed", err)

    def test_a_clean_nested_run_passes_the_umbrella(self):
        # The failure above must come from the returncode mapping, not from
        # the shape of the fake: the same fake with no failing script passes.
        code, out, err = self._main(_FakeSubprocess())
        self.assertEqual(code, 0)
        self.assertIn("JSON validation passed", out)
        self.assertEqual(err, "")


class ExternalSuiteOptionForwardingTests(unittest.TestCase):
    """CON-893: the umbrella forwards CI's external-suite routing verbatim.

    verify-adapter and verify-tooling own the suites. verify-contracts passes
    both external-suite options. A default invocation forwards neither, and
    a nested failure still fails the umbrella.
    """

    def _main(self, argv: list[str], fake: _FakeSubprocess) -> tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with (
            unittest.mock.patch.object(umbrella, "repository_files", return_value=[]),
            unittest.mock.patch.object(umbrella, "subprocess", fake),
            redirect_stdout(out),
            redirect_stderr(err),
        ):
            code = umbrella.main(list(argv))
        return code, out.getvalue(), err.getvalue()

    def nested_arguments(self, fake: _FakeSubprocess) -> list[str]:
        calls = [call for call in fake.calls if any("check-agent-contracts.py" in word for word in call)]
        self.assertEqual(len(calls), 1, "expected exactly one nested contract-check invocation")
        return calls[0]

    def test_a_default_invocation_forwards_no_external_suite_option(self):
        fake = _FakeSubprocess()
        code, out, err = self._main([], fake)
        self.assertEqual(code, 0)
        arguments = self.nested_arguments(fake)
        for flag in ("--adapter-tests-external", "--contract-tests-external"):
            self.assertNotIn(flag, arguments)

    def test_each_external_suite_option_is_forwarded_individually(self):
        for flag in ("--adapter-tests-external", "--contract-tests-external"):
            with self.subTest(flag=flag):
                fake = _FakeSubprocess()
                code, _, _ = self._main([flag], fake)
                self.assertEqual(code, 0)
                self.assertIn(flag, self.nested_arguments(fake))

    def test_both_external_suite_options_are_forwarded_together(self):
        fake = _FakeSubprocess()
        code, _, _ = self._main(["--adapter-tests-external", "--contract-tests-external"], fake)
        self.assertEqual(code, 0)
        arguments = self.nested_arguments(fake)
        self.assertIn("--adapter-tests-external", arguments)
        self.assertIn("--contract-tests-external", arguments)

    def test_a_nested_failure_still_fails_the_umbrella_with_both_options(self):
        code, out, err = self._main(
            ["--adapter-tests-external", "--contract-tests-external"],
            _FakeSubprocess(failing="check-agent-contracts.py"),
        )
        self.assertEqual(code, 1)
        self.assertIn("agent contract drift", out)
        self.assertIn("JSON validation failed", err)


if __name__ == "__main__":
    unittest.main()
