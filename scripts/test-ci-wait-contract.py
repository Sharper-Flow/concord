#!/usr/bin/env python3
"""Validate the deterministic ci-wait wait contract (CD-0160)."""
from __future__ import annotations

import json
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / "contracts/agent-lanes.v1.json"


def go_build(t: unittest.TestCase, binary: Path) -> None:
    result = subprocess.run(
        ["go", "build", "-o", binary, "./cmd/concord"],
        cwd=ROOT, capture_output=True, text=True, timeout=300,
    )
    if result.returncode != 0:
        t.fail(f"go build failed: {result.stderr}")


def run_cli(binary: Path, payload: dict) -> tuple[int, dict | str]:
    result = subprocess.run(
        [str(binary), "ci-wait"], input=json.dumps(payload).encode(),
        capture_output=True, timeout=300,
    )
    try:
        parsed = json.loads(result.stdout)
    except json.JSONDecodeError:
        parsed = result.stdout.decode(errors="replace")
    return result.returncode, parsed


def run_cli_text(binary: Path, payload: dict) -> tuple[int, str, str]:
    result = subprocess.run(
        [str(binary), "ci-wait"], input=json.dumps(payload).encode(),
        capture_output=True, timeout=300,
    )
    return result.returncode, result.stdout.decode(errors="replace"), result.stderr.decode(errors="replace")


def run_generator_check() -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, str(ROOT / "scripts/generate-agent-lanes.py"), "--check"],
        cwd=ROOT, capture_output=True, text=True, timeout=120,
    )


class CiWaitDeterministicWaitContract(unittest.TestCase):
    """The wait must be enforced by the CLI, not by the model."""

    @classmethod
    def setUpClass(cls):
        cls._tmp = tempfile.TemporaryDirectory(prefix="ciwait-regression-")
        cls.binary = Path(cls._tmp.name) / "concord"
        go_build(cls, cls.binary)

    @classmethod
    def tearDownClass(cls):
        cls._tmp.cleanup()

    def test_verb_exists_in_usage(self):
        """The wait verb is reachable through the operator CLI surface."""
        result = subprocess.run([str(self.binary), "--help"], capture_output=True, text=True, timeout=30)
        self.assertIn("ci-wait", result.stdout)

    def test_missing_selector_refuses(self):
        """No selector: refused before any effect."""
        code, out, err = run_cli_text(self.binary, {})
        self.assertEqual(code, 1)
        self.assertIn("selector", err)
        self.assertEqual(out.strip(), "")

    def test_unknown_selector_kind_refuses(self):
        """An unknown selector kind is a JSON refusal from the handler."""
        code, out = run_cli(self.binary, {"selector": {"kind": "blob", "value": "x"}, "repo": "o/r"})
        self.assertEqual(code, 1)
        self.assertIsInstance(out, dict)
        self.assertEqual(out.get("status"), "refused")

    def test_slice_zero_terminated_timeout(self):
        """A zero-bounded slice must terminate with timeout, never hang or succeed."""
        code, out = run_cli(self.binary, {
            "selector": {"kind": "pr", "value": "1701"},
            "repo": "Sharper-Flow/PokeEdge",
            "time_seconds_max": 0,
        })
        self.assertEqual(code, 1)
        self.assertIsInstance(out, dict)
        self.assertEqual(out.get("status"), "timeout")
        self.assertFalse(out.get("success", False))

    def test_impossible_slice_times_out_offline(self):
        """A 1-second budget cannot fit a gh round trip: deterministic
        timeout at the bound, never a fabricated success. No network: the
        deadline pre-check fires before any gh invocation."""
        code, out = run_cli(self.binary, {
            "selector": {"kind": "run", "value": "35474173917"},
            "repo": "Sharper-Flow/PokeEdge",
            "time_seconds_max": 1,
        })
        self.assertEqual(code, 1)
        self.assertIsInstance(out, dict)
        self.assertEqual(out.get("status"), "timeout")
        self.assertLessEqual(out.get("elapsed_seconds", 9999), 1800)

    def test_generated_prompt_names_the_cli_verb(self):
        """The generated utility body must delegate waiting to `concord ci-wait`."""
        installed = ROOT / ".opencode/agents/concord-ci-wait.md"
        text = installed.read_text(encoding="utf-8")
        self.assertIn("concord ci-wait", text)
        self.assertNotIn("Count your iterations", text)

    def test_generated_prompt_has_no_model_maintained_counter(self):
        """The loop must not ask the model to count iterations or sleep."""
        text = (ROOT / ".opencode/agents/concord-ci-wait.md").read_text(encoding="utf-8")
        self.assertNotIn("sleep 15", text)

    def test_generator_check_clean(self):
        """Registry, generator, and generated projections agree."""
        result = run_generator_check()
        self.assertEqual(result.returncode, 0, f"generator drift: {result.stderr}")

    def test_registry_declares_bounded_wait(self):
        manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
        utility = next(u for u in manifest["utilities"] if u["id"] == "ci-wait")
        self.assertEqual(utility["time_seconds_max"], 1800)


if __name__ == "__main__":
    unittest.main(verbosity=2)
