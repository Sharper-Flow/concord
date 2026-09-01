#!/usr/bin/env python3
"""Tests for the invocation proof in scripts/evidence_anchors.py.

A `validator` anchor claims a checker is executed. The claim was resolved by
asking whether the script path appeared as text in a workflow or in
`check-json.py`, so a name in a comment, a step title, or an unused variable
counted as proof that a law record is enforced. These tests pin the two
structural resolutions that replaced it: a workflow proves invocation through
its `run:` commands, and nesting proves it through the `subprocess.run` call
graph.
"""
from __future__ import annotations

import importlib.util
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "evidence_anchors", Path(__file__).with_name("evidence_anchors.py")
)
assert SPEC and SPEC.loader
anchors = importlib.util.module_from_spec(SPEC)
sys.modules["evidence_anchors"] = anchors
SPEC.loader.exec_module(anchors)


def test_workflow_commands_keep_run_bodies() -> None:
    text = "\n".join(
        [
            "      - name: Validate",
            "        run: python3 scripts/check-one.py",
            "      - name: Bundle",
            "        run: |",
            "          python3 scripts/check-two.py",
            "          python3 scripts/check-three.py",
        ]
    )
    commands = anchors.workflow_commands(text)
    for script in ("scripts/check-one.py", "scripts/check-two.py", "scripts/check-three.py"):
        assert script in commands, f"{script} missing from extracted commands"


def test_workflow_commands_drop_prose() -> None:
    text = "\n".join(
        [
            "      # scripts/check-comment.py explains the rule",
            "      - name: Validate with scripts/check-title.py",
            "        run: python3 scripts/check-real.py",
        ]
    )
    commands = anchors.workflow_commands(text)
    assert "scripts/check-real.py" in commands
    assert "scripts/check-comment.py" not in commands, "a comment counted as a command"
    assert "scripts/check-title.py" not in commands, "a step name counted as a command"


def test_nested_invocations_find_subprocess_calls(tmp: Path) -> None:
    module = tmp / "nesting.py"
    module.write_text(
        "\n".join(
            [
                "import subprocess",
                "import sys",
                "ROOT = __file__",
                "checker = 'scripts/check-bound.py'",
                "subprocess.run([sys.executable, checker])",
                "subprocess.run([sys.executable, 'scripts/check-literal.py'])",
            ]
        ),
        encoding="utf-8",
    )
    found = anchors.nested_invocations(module)
    assert "scripts/check-bound.py" in found, "variable-bound invocation missed"
    assert "scripts/check-literal.py" in found, "literal invocation missed"


def test_nested_invocations_reject_mentions(tmp: Path) -> None:
    module = tmp / "mentions.py"
    module.write_text(
        "\n".join(
            [
                "import subprocess",
                "import sys",
                "# scripts/check-comment.py is related",
                "'''scripts/check-docstring.py is also related'''",
                "unused = 'scripts/check-unused.py'",
                "subprocess.run([sys.executable, 'scripts/check-real.py'])",
            ]
        ),
        encoding="utf-8",
    )
    found = anchors.nested_invocations(module)
    assert found == {"scripts/check-real.py"}, f"mentions counted as invocations: {sorted(found)}"


def test_unparseable_module_proves_nothing(tmp: Path) -> None:
    module = tmp / "broken.py"
    module.write_text("def (:\n", encoding="utf-8")
    assert anchors.nested_invocations(module) == set()



def test_adapter_test_anchor_resolves() -> None:
    findings: list[str] = []
    anchors.check_anchor(
        {"kind": "adapter_test", "value": "adapter/opencode/concord.test.ts#a read answered by a newer core contract is typed as version skew"},
        "prefix",
        findings,
    )
    assert findings == [], findings


def test_adapter_test_anchor_rejects_unknown_test() -> None:
    findings: list[str] = []
    anchors.check_anchor(
        {"kind": "adapter_test", "value": "adapter/opencode/concord.test.ts#no such test is registered"},
        "prefix",
        findings,
    )
    assert any("does not resolve" in f for f in findings), findings


def test_adapter_test_anchor_rejects_foreign_path() -> None:
    findings: list[str] = []
    anchors.check_anchor(
        {"kind": "adapter_test", "value": "adapter/elsewhere/concord.test.ts#a read answered by a newer core contract is typed as version skew"},
        "prefix",
        findings,
    )
    assert any("must read" in f for f in findings), findings


def main() -> int:
    import tempfile

    tests = [
        value
        for name, value in sorted(globals().items())
        if name.startswith("test_") and callable(value)
    ]
    failures = 0
    for test in tests:
        try:
            if test.__code__.co_argcount:
                with tempfile.TemporaryDirectory() as directory:
                    test(Path(directory))
            else:
                test()
            print(f"ok  {test.__name__}")
        except AssertionError as err:
            failures += 1
            print(f"FAIL {test.__name__}: {err}")
    print(f"evidence anchor tests passed: {len(tests) - failures}/{len(tests)}")
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
