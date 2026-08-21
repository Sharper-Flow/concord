#!/usr/bin/env python3
"""Tests for scripts/check-agents-md.py."""

from __future__ import annotations

import importlib.util
import sys
import tempfile
from pathlib import Path

spec = importlib.util.spec_from_file_location(
    "check_agents_md", Path(__file__).with_name("check-agents-md.py")
)
guard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guard)


def test_repository_agents_md_passes() -> None:
    assert guard.main() == 0, "the committed AGENTS.md must satisfy its own check"


def test_budget_accepts_a_file_at_the_limit() -> None:
    findings: list[str] = []
    guard.check_budget(["line"] * guard.MAX_LINES, findings)
    assert findings == [], f"a file at the limit is not over it: {findings}"


def test_budget_rejects_a_file_over_the_limit() -> None:
    findings: list[str] = []
    guard.check_budget(["line"] * (guard.MAX_LINES + 1), findings)
    assert len(findings) == 1, f"expected one budget finding, got {findings}"
    assert "exceeds the context budget" in findings[0]
    assert findings[0].startswith(f"AGENTS.md:{guard.MAX_LINES + 1}:")


def test_paths_reject_a_named_path_that_does_not_exist() -> None:
    findings: list[str] = []
    guard.check_paths(
        ["run `scripts/check-nothing.py` first"], {"scripts"}, findings
    )
    assert len(findings) == 1, f"expected one path finding, got {findings}"
    assert "named path does not exist: scripts/check-nothing.py" in findings[0]
    assert findings[0].startswith("AGENTS.md:1:")


def test_paths_accept_a_path_that_resolves() -> None:
    findings: list[str] = []
    guard.check_paths(["see `scripts/check-agents-md.py`"], {"scripts"}, findings)
    assert findings == [], f"an existing path is not a finding: {findings}"


def test_paths_accept_a_directory_reference_with_a_trailing_slash() -> None:
    findings: list[str] = []
    guard.check_paths(["lanes live in `.opencode/agents/`"], {".opencode"}, findings)
    assert findings == [], f"a directory that exists is not a finding: {findings}"


def test_paths_ignore_prose_that_is_not_a_repository_path() -> None:
    findings: list[str] = []
    guard.check_paths(
        ["never call `s.db` inside a transaction, and avoid `busy_timeout`"],
        {"scripts", "internal"},
        findings,
    )
    assert findings == [], f"non-repository tokens are not paths: {findings}"


def test_paths_ignore_placeholders_and_globs() -> None:
    findings: list[str] = []
    guard.check_paths(
        ["records live at `docs/decisions/CD-NNNN-*.md`"], {"docs"}, findings
    )
    assert findings == [], f"a placeholder cannot be resolved: {findings}"


def test_paths_ignore_a_multi_word_code_span() -> None:
    findings: list[str] = []
    guard.check_paths(["run `go test ./...`"], {"go.mod"}, findings)
    assert findings == [], f"a command is not a path token: {findings}"


def _workflow(body: str) -> Path:
    directory = Path(tempfile.mkdtemp())
    workflow = directory / "ci.yml"
    workflow.write_text(body, encoding="utf-8")
    return workflow


def test_ci_commands_collect_a_scalar_run_step() -> None:
    workflow = _workflow(
        "jobs:\n"
        "  verify:\n"
        "    steps:\n"
        "      - name: Vet\n"
        "        run: go vet ./...\n"
    )
    assert "go vet ./..." in guard.ci_command_lines(workflow)


def test_ci_commands_collect_each_chained_segment() -> None:
    workflow = _workflow(
        "jobs:\n"
        "  verify:\n"
        "    steps:\n"
        "      - name: Test tooling\n"
        "        run: python3 scripts/test-release.py && python3 scripts/test-installer.py\n"
    )
    banned = guard.ci_command_lines(workflow)
    assert "python3 scripts/test-release.py" in banned
    assert "python3 scripts/test-installer.py" in banned


def test_ci_commands_collect_a_block_scalar_run_step() -> None:
    workflow = _workflow(
        "jobs:\n"
        "  verify:\n"
        "    steps:\n"
        "      - name: Multi\n"
        "        run: |\n"
        "          go build ./...\n"
        "          go vet ./...\n"
        "      - name: Next\n"
        "        run: go mod tidy\n"
    )
    banned = guard.ci_command_lines(workflow)
    assert "go build ./..." in banned
    assert "go vet ./..." in banned
    assert "go mod tidy" in banned


def test_ci_commands_ignore_a_bare_program_name() -> None:
    workflow = _workflow(
        "jobs:\n"
        "  verify:\n"
        "    steps:\n"
        "      - name: Bare\n"
        "        run: bun\n"
    )
    assert guard.ci_command_lines(workflow) == set()


def test_ci_commands_reject_a_reproduced_command_line() -> None:
    findings: list[str] = []
    guard.check_ci_commands(
        ["Before pushing, run `go test -race -timeout=20m ./...`"],
        {"go test -race -timeout=20m ./..."},
        findings,
    )
    assert len(findings) == 1, f"expected one duplication finding, got {findings}"
    assert "reproduces a CI command line" in findings[0]
    assert findings[0].startswith("AGENTS.md:1:")


def test_ci_commands_accept_a_pointer_to_the_workflow() -> None:
    findings: list[str] = []
    guard.check_ci_commands(
        ["The verification contract lives in `.github/workflows/ci.yml`."],
        {"go test -race -timeout=20m ./...", "go mod tidy"},
        findings,
    )
    assert findings == [], f"a pointer is the intended form: {findings}"


def test_real_ci_workflow_yields_commands() -> None:
    banned = guard.ci_command_lines(guard.CI_WORKFLOW)
    assert banned, "the repository workflow must yield at least one command"
    assert all(" " in command for command in banned)


if __name__ == "__main__":
    failures = 0
    for name, value in sorted(globals().items()):
        if name.startswith("test_") and callable(value):
            try:
                value()
                print(f"ok  {name}")
            except AssertionError as error:
                failures += 1
                print(f"FAIL {name}: {error}")
    sys.exit(1 if failures else 0)
