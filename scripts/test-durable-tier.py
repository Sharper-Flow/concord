#!/usr/bin/env python3
"""Tests for scripts/check-durable-tier.py.

Each test builds a minimal repository shape in a temp directory (budget file,
tier roots, artifacts), runs the checker as a subprocess against it, and
asserts the exit code and finding text. The checker's ROOT is derived from
its own file location, so the temp tree carries a copy of the script.
"""

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SOURCE = Path(__file__).resolve().parent / "check-durable-tier.py"
PAGE = "# What shipped\n\nDistilled prose for a future reader.\n"


def run_checker(root: Path) -> tuple[int, str]:
    result = subprocess.run(
        [sys.executable, str(root / "scripts" / "check-durable-tier.py")],
        cwd=root,
        capture_output=True,
        text=True,
        timeout=60,
    )
    return result.returncode, result.stdout + result.stderr


def build_repo(budget: dict) -> tuple[Path, object]:
    tmp = tempfile.TemporaryDirectory()
    root = Path(tmp.name)
    (root / "scripts").mkdir()
    (root / "scripts" / "check-durable-tier.py").write_bytes(SOURCE.read_bytes())
    (root / "docs").mkdir()
    (root / "docs" / "durable-tier-budget.v1.json").write_text(json.dumps(budget), encoding="utf-8")
    return root, tmp


def base_budget() -> dict:
    return {
        "schema_version": "1.0",
        "note_roots": ["docs/work", "docs/lessons"],
        "max_note_bytes": 200,
        "max_fenced_json_bytes": 80,
        "note_allowances": [],
        "non_markdown_inventory": [],
    }


def write_note(root: Path, rel: str, text: str) -> None:
    path = root / rel
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


class DurableTierCheckerTests(unittest.TestCase):
    def test_missing_roots_pass(self) -> None:
        root, tmp = build_repo(base_budget())
        with tmp:
            code, output = run_checker(root)
        self.assertEqual(code, 0, output)
        self.assertIn("satisfies", output)

    def test_distilled_note_passes(self) -> None:
        root, tmp = build_repo(base_budget())
        with tmp:
            write_note(root, "docs/work/2026-08-24-slug.md", PAGE)
            code, output = run_checker(root)
        self.assertEqual(code, 0, output)

    def test_non_markdown_in_note_root_fails(self) -> None:
        root, tmp = build_repo(base_budget())
        with tmp:
            write_note(root, "docs/work/state.json", "{}")
            code, output = run_checker(root)
        self.assertEqual(code, 1)
        self.assertIn("markdown only", output)

    def test_oversized_note_fails_and_boundary_is_inclusive(self) -> None:
        budget = base_budget()
        budget["max_note_bytes"] = len(PAGE.encode())
        root, tmp = build_repo(budget)
        with tmp:
            write_note(root, "docs/work/at-bound.md", PAGE)
            write_note(root, "docs/work/over-bound.md", PAGE + "x")
            code, output = run_checker(root)
        self.assertEqual(code, 1)
        self.assertIn("over-bound.md", output)
        self.assertNotIn("at-bound.md", output)

    def test_state_dump_fence_fails_but_small_example_passes(self) -> None:
        root, tmp = build_repo(base_budget())
        small = "```json\n{\"check_ref\": \"check:x\"}\n```\n"
        dump = "```json\n" + json.dumps({"state": ["x" * 40] * 4}) + "\n```\n"
        with tmp:
            write_note(root, "docs/lessons/small-ok.md", PAGE + small)
            write_note(root, "docs/lessons/dump.md", PAGE + dump)
            code, output = run_checker(root)
        self.assertEqual(code, 1)
        self.assertIn("dump.md", output)
        self.assertIn("distill", output)
        self.assertNotIn("small-ok.md", output)

    def test_unterminated_json_fence_is_measured(self) -> None:
        # Everything after an unterminated fence is inside it. Measuring only
        # closed blocks would let a note carry an unbounded dump by omitting
        # one line, and would put this layer out of step with the
        # producer-side parse in internal/store/durable_tier.go.
        root, tmp = build_repo(base_budget())
        dump = "```json\n" + json.dumps({"state": ["x" * 40] * 4}) + "\n"
        with tmp:
            write_note(root, "docs/lessons/unterminated.md", PAGE + dump)
            code, output = run_checker(root)
        self.assertEqual(code, 1)
        self.assertIn("unterminated.md", output)
        self.assertIn("distill", output)

    def test_plain_fence_is_not_a_json_finding(self) -> None:
        root, tmp = build_repo(base_budget())
        with tmp:
            write_note(root, "docs/lessons/prose.md", PAGE + "```\nnot json at all, just a long quoted shell transcript that is clearly over the json threshold\n```\n")
            code, output = run_checker(root)
        self.assertEqual(code, 0, output)

    def test_byte_allowance_exempts_only_the_named_note(self) -> None:
        budget = base_budget()
        budget["note_allowances"] = [
            {
                "path": "docs/work/allowed.md",
                "state": "outstanding",
                "issue": 463,
                "reason": "transitional note awaiting distillation",
            }
        ]
        root, tmp = build_repo(budget)
        with tmp:
            write_note(root, "docs/work/allowed.md", PAGE + "x" * 300)
            write_note(root, "docs/work/plain.md", PAGE + "x" * 300)
            code, output = run_checker(root)
        self.assertEqual(code, 1)
        self.assertIn("plain.md", output)
        self.assertNotIn("allowed.md", output)

    def test_allowance_does_not_permit_non_markdown_or_dumps(self) -> None:
        budget = base_budget()
        budget["note_allowances"] = [
            {
                "path": "docs/work/allowed.md",
                "state": "outstanding",
                "issue": 463,
                "reason": "transitional note awaiting distillation",
            }
        ]
        root, tmp = build_repo(budget)
        dump = "```json\n" + json.dumps({"state": ["x" * 40] * 4}) + "\n```\n"
        with tmp:
            write_note(root, "docs/work/allowed.md", PAGE + dump)
            write_note(root, "docs/work/blob.json", "{}")
            code, output = run_checker(root)
        self.assertEqual(code, 1)
        self.assertIn("allowed.md", output)
        self.assertIn("blob.json", output)

    def test_uninventoried_decision_artifact_fails(self) -> None:
        root, tmp = build_repo(base_budget())
        with tmp:
            write_note(root, "docs/decisions/CD-9999-state.v1.json", "{}")
            code, output = run_checker(root)
        self.assertEqual(code, 1)
        self.assertIn("not in the budget inventory", output)

    def test_dangling_inventory_entry_fails(self) -> None:
        budget = base_budget()
        budget["non_markdown_inventory"] = [
            {"path": "docs/decisions/gone.json", "reason": "was here when the budget was written"}
        ]
        root, tmp = build_repo(budget)
        with tmp:
            code, output = run_checker(root)
        self.assertEqual(code, 1)
        self.assertIn("inventory entry", output)

    def test_inventoried_decision_artifact_passes(self) -> None:
        budget = base_budget()
        budget["non_markdown_inventory"] = [
            {"path": "docs/decisions/license-evidence/x/LICENSE", "reason": "license text is legal material"}
        ]
        root, tmp = build_repo(budget)
        with tmp:
            write_note(root, "docs/decisions/license-evidence/x/LICENSE", "MIT")
            code, output = run_checker(root)
        self.assertEqual(code, 0, output)

    def test_budget_bound_is_single_sourced(self) -> None:
        loose = base_budget()
        loose["max_note_bytes"] = 10_000
        root, tmp = build_repo(loose)
        with tmp:
            write_note(root, "docs/work/big.md", PAGE + "x" * 500)
            code, output = run_checker(root)
        self.assertEqual(code, 0, output)

    def test_malformed_budget_fails(self) -> None:
        for bad in (
            {**base_budget(), "schema_version": "2.0"},
            {**base_budget(), "max_note_bytes": 0},
            {**base_budget(), "note_roots": []},
            {**base_budget(), "note_allowances": [{"path": "docs/work/x.txt", "state": "outstanding", "issue": 1, "reason": "not a markdown note"}]},
        ):
            root, tmp = build_repo(bad)
            with tmp:
                code, output = run_checker(root)
            self.assertEqual(code, 1, output)
            self.assertIn("budget", output)


if __name__ == "__main__":
    unittest.main()
