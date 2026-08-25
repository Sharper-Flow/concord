#!/usr/bin/env python3
"""Tests for scripts/check-predecessor-independence.py.

The acceptance bar for issue #318: the check is proven to FAIL on each
prohibited shape, not assumed to. Every rejected class is planted in a
sandbox repository copy and must produce a finding; the clean baseline and
the permitted-citation boundary are asserted on the real repository.
"""

from __future__ import annotations

import importlib.util
import shutil
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location(
    "check_predecessor_independence", ROOT / "scripts" / "check-predecessor-independence.py"
)
checker = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(checker)


def run_check(root: Path) -> tuple[int, str]:
    import subprocess

    result = subprocess.run(
        [sys.executable, str(ROOT / "scripts" / "check-predecessor-independence.py")],
        capture_output=True,
        text=True,
        cwd=root,
    )
    return result.returncode, result.stdout + result.stderr


def sandbox() -> Path:
    """Copy the real repository surfaces into a temp root the checker can scan."""
    root = Path(tempfile.mkdtemp(prefix="pred-indep-"))
    (root / ".opencode" / "agents").mkdir(parents=True)
    (root / "contracts").mkdir()
    (root / "scripts").mkdir()
    (root / "adapter" / "opencode").mkdir(parents=True)
    surfaces = list((ROOT / ".opencode" / "agents").glob("*.md"))
    surfaces += (ROOT / "contracts").glob("agent-lanes*.json")
    surfaces += (ROOT / "contracts").glob("agent-lane-*.json")
    generator = ROOT / "scripts" / "generate-agent-lanes.py"
    if generator.is_file():
        surfaces.append(generator)
    surfaces += (ROOT / "adapter" / "opencode").glob("*.ts")
    for source in surfaces:
        target = root / source.relative_to(ROOT)
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy(source, target)
    return root


def test_clean_repository_passes() -> None:
    code, out = run_check(ROOT)
    assert code == 0, out


def test_lane_definition_with_predecessor_tool_fails() -> None:
    root = sandbox()
    lane = root / ".opencode" / "agents" / "concord-research.md"
    lane.write_text(lane.read_text() + "\nUse adv_change_show to read state.\n")
    code, out = run_check_with_root(root)
    assert code == 1, out
    assert "concord-research.md" in out and "predecessor tool identifier" in out


def test_lane_manifest_with_state_path_fails() -> None:
    root = sandbox()
    manifest = root / "contracts" / "agent-lanes.v1.json"
    manifest.write_text(manifest.read_text().replace('"capabilities"', '"note": "reads ~/.local/share/Advance/change.json", "capabilities"', 1))
    code, out = run_check_with_root(root)
    assert code == 1, out
    assert "predecessor state path" in out


def test_adapter_spawning_predecessor_fails() -> None:
    root = sandbox()
    adapter = root / "adapter" / "opencode" / "dispatch.ts"
    adapter.write_text(adapter.read_text() + "\n// fallback: run --agent adv concord-research\n")
    code, out = run_check_with_root(root)
    assert code == 1, out
    assert "predecessor spawn" in out


def test_generator_input_with_tool_prefix_fails() -> None:
    root = sandbox()
    generator = root / "scripts" / "generate-agent-lanes.py"
    generator.write_text(generator.read_text().replace("mode: all", "mode: all\n  adv_task_update: true", 1))
    code, out = run_check_with_root(root)
    assert code == 1, out
    assert "generate-agent-lanes.py" in out


def test_docs_citation_is_not_a_dependency() -> None:
    """The prohibition is tool grants and calls, not mentions: docs/ is out of
    scope by construction (never scanned), and the real repository's docs
    freely cite predecessor evidence while the check passes."""
    code, out = run_check(ROOT)
    assert code == 0, out
    docs = ROOT / "docs" / "predecessor-operational-coverage.md"
    assert "Advance" in docs.read_text(encoding="utf-8")


def run_check_with_root(root: Path) -> tuple[int, str]:
    """Run the checker against a sandbox root through its --root flag."""
    import subprocess

    result = subprocess.run(
        [
            sys.executable,
            str(ROOT / "scripts" / "check-predecessor-independence.py"),
            "--root",
            str(root),
        ],
        capture_output=True,
        text=True,
    )
    return result.returncode, result.stdout + result.stderr


def main() -> int:
    tests = [
        value
        for name, value in sorted(globals().items())
        if name.startswith("test_") and callable(value)
    ]
    for test in tests:
        test()
    print(f"predecessor independence tests passed: {len(tests)} test(s)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
