#!/usr/bin/env python3
"""Focused tests for the knowledge-closure checker.

The validator runs against a real manifest under ROOT. To exercise its
behavior without perturbing the live manifest, each test invokes main() with
synthetic knowledge roots and a sandbox copy of the manifest under
tempfile.TemporaryDirectory(). check-knowledge-closure.py reads
`docs/concord-knowledge-index.v1.json` relative to its own ROOT; the tests
patch both `ROOT` and `MANIFEST` onto the sandbox.
"""
from __future__ import annotations

import importlib.util
import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).with_name("check-knowledge-closure.py")
REPO_ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("knowledge_closure", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


def build_sandbox() -> Path:
    """Build a sandbox with a docs/ tree and a manifest, return its root."""
    root = Path(tempfile.mkdtemp(prefix="kc-"))
    docs = root / "docs"
    docs.mkdir()
    (docs / "recorded.md").write_text("recorded\n", encoding="utf-8")
    (docs / "orphan.md").write_text("orphan\n", encoding="utf-8")
    (docs / "sub").mkdir()
    (docs / "sub" / "nested.md").write_text("nested\n", encoding="utf-8")
    (docs / "research").mkdir()
    (docs / "research" / "R1-finding.md").write_text("finding\n", encoding="utf-8")
    (root / "manifest.json").write_text(
        json.dumps(
            {
                "schema_version": "1.2",
                "supported_kinds": ["spec"],
                "indexed_kinds": ["spec"],
                "knowledge_roots": ["docs/"],
                "exclusions": [],
                "records": [
                    {
                        "id": "recorded-1",
                        "kind": "spec",
                        "path": "docs/recorded.md",
                        "status": "accepted",
                        "date": "2026-08-21T00:00:00Z",
                        "title": "Recorded",
                        "summary": "recorded",
                        "tags": [],
                        "scopes": {
                            "mode": "home",
                            "product_ids": [],
                            "project_ids": [],
                            "domain_ids": [],
                            "tag_ids": [],
                        },
                        "sha256": "sha256:" + "a" * 64,
                    }
                ],
            }
        ),
        encoding="utf-8",
    )
    return root


def run_with_sandbox(root: Path, argv: list[str] | None = None) -> tuple[int, str, str]:
    """Run checker.main with sandbox as ROOT; return (exit, stdout, stderr)."""
    from io import StringIO

    argv = list(argv or [])
    captured_out = StringIO()
    captured_err = StringIO()
    manifest = root / "manifest.json"
    with mock.patch.object(checker, "ROOT", root), mock.patch.object(checker, "MANIFEST", manifest):
        old_out, old_err = sys.stdout, sys.stderr
        sys.stdout, sys.stderr = captured_out, captured_err
        try:
            exit_code = checker.main(argv)
        finally:
            sys.stdout, sys.stderr = old_out, old_err
    return exit_code, captured_out.getvalue(), captured_err.getvalue()


def test_unprocessed_files_are_listed_and_exit_zero() -> None:
    root = build_sandbox()
    exit_code, stdout, stderr = run_with_sandbox(root)
    assert exit_code == 0, stderr
    lines = stdout.splitlines()
    assert any(line == "unprocessed: docs/orphan.md" for line in lines), lines
    assert any(line == "unprocessed: docs/sub/nested.md" for line in lines), lines
    assert "knowledge closure check passed" in stdout, stdout


def test_strict_mode_exits_one_when_unprocessed_non_empty() -> None:
    root = build_sandbox()
    exit_code, stdout, stderr = run_with_sandbox(root, ["--strict"])
    assert exit_code == 1, (exit_code, stderr)
    assert "strict mode refused" in stderr, stderr


def test_strict_mode_passes_when_zero_unprocessed() -> None:
    root = build_sandbox()
    manifest_path = root / "manifest.json"
    data = json.loads(manifest_path.read_text(encoding="utf-8"))
    data["records"].append(
        {
            "id": "recorded-orphan",
            "kind": "spec",
            "path": "docs/orphan.md",
            "status": "accepted",
            "date": "2026-08-21T00:00:00Z",
            "title": "Orphan",
            "summary": "orphan",
            "tags": [],
            "scopes": {
                "mode": "home",
                "product_ids": [],
                "project_ids": [],
                "domain_ids": [],
                "tag_ids": [],
            },
            "sha256": "sha256:" + "b" * 64,
        }
    )
    data["records"].append(
        {
            "id": "recorded-nested",
            "kind": "spec",
            "path": "docs/sub/nested.md",
            "status": "accepted",
            "date": "2026-08-21T00:00:00Z",
            "title": "Nested",
            "summary": "nested",
            "tags": [],
            "scopes": {
                "mode": "home",
                "product_ids": [],
                "project_ids": [],
                "domain_ids": [],
                "tag_ids": [],
            },
            "sha256": "sha256:" + "c" * 64,
        }
    )
    data["exclusions"] = ["docs/research/"]
    manifest_path.write_text(json.dumps(data), encoding="utf-8")
    exit_code, stdout, stderr = run_with_sandbox(root, ["--strict"])
    assert exit_code == 0, (exit_code, stderr)


def test_exclusions_suppress_listed_paths() -> None:
    root = build_sandbox()
    manifest_path = root / "manifest.json"
    data = json.loads(manifest_path.read_text(encoding="utf-8"))
    data["exclusions"] = ["docs/research/", "docs/sub/"]
    manifest_path.write_text(json.dumps(data), encoding="utf-8")
    exit_code, stdout, stderr = run_with_sandbox(root)
    assert exit_code == 0, stderr
    lines = stdout.splitlines()
    assert any(line == "unprocessed: docs/orphan.md" for line in lines), lines
    assert not any("docs/research/" in line for line in lines), lines
    assert not any("docs/sub/" in line for line in lines), lines


def test_file_path_exclusion_suppresses_exactly_one_file() -> None:
    """Generated build output is excluded by path, not by hiding its directory."""
    root = build_sandbox()
    (root / "docs" / "generated-surface.md").write_text("generated\n", encoding="utf-8")
    manifest_path = root / "manifest.json"
    data = json.loads(manifest_path.read_text(encoding="utf-8"))
    data["exclusions"] = ["docs/generated-surface.md"]
    manifest_path.write_text(json.dumps(data), encoding="utf-8")
    exit_code, stdout, stderr = run_with_sandbox(root)
    assert exit_code == 0, stderr
    lines = stdout.splitlines()
    assert "unprocessed: docs/generated-surface.md" not in lines, lines
    # The sibling documents in the same directory stay visible.
    assert "unprocessed: docs/orphan.md" in lines, lines
    assert "unprocessed: docs/sub/nested.md" in lines, lines


def test_go_read_matches_validator_population() -> None:
    """The Go read and Python validator must return the same fixture paths."""
    root = build_sandbox()
    (root / "docs" / "excluded-file.md").write_text("excluded\n", encoding="utf-8")
    (root / "docs" / "not-markdown.txt").write_text("not knowledge\n", encoding="utf-8")
    manifest_path = root / "manifest.json"
    data = json.loads(manifest_path.read_text(encoding="utf-8"))
    data["exclusions"] = ["docs/research/", "docs/excluded-file.md"]
    manifest_path.write_text(json.dumps(data), encoding="utf-8")

    exit_code, stdout, stderr = run_with_sandbox(root)
    assert exit_code == 0, stderr
    python_paths = sorted(
        line.removeprefix("unprocessed: ")
        for line in stdout.splitlines()
        if line.startswith("unprocessed: ")
    )

    env = os.environ.copy()
    env["CONCORD_KNOWLEDGE_FIXTURE_ROOT"] = str(root)
    completed = subprocess.run(
        ["go", "test", "./internal/store", "-run", "^TestUnprocessedKnowledgeDocsFixture$", "-count=1", "-v"],
        cwd=REPO_ROOT,
        env=env,
        capture_output=True,
        text=True,
        check=False,
        timeout=120,
    )
    assert completed.returncode == 0, completed.stdout + completed.stderr
    match = re.search(r"UNPROCESSED_JSON=(\[.*\])", completed.stdout)
    assert match, completed.stdout + completed.stderr
    go_paths = json.loads(match.group(1))
    assert go_paths == python_paths, (go_paths, python_paths, completed.stdout, completed.stderr)
    assert go_paths == ["docs/orphan.md", "docs/sub/nested.md"]


def test_directory_exclusion_still_suppresses_a_whole_subtree() -> None:
    root = build_sandbox()
    manifest_path = root / "manifest.json"
    data = json.loads(manifest_path.read_text(encoding="utf-8"))
    data["exclusions"] = ["docs/sub/"]
    manifest_path.write_text(json.dumps(data), encoding="utf-8")
    exit_code, stdout, stderr = run_with_sandbox(root)
    assert exit_code == 0, stderr
    assert "unprocessed: docs/sub/nested.md" not in stdout.splitlines(), stdout


def test_exclusion_without_slash_or_markdown_suffix_is_rejected() -> None:
    root = build_sandbox()
    manifest_path = root / "manifest.json"
    data = json.loads(manifest_path.read_text(encoding="utf-8"))
    data["exclusions"] = ["docs/orphan"]
    manifest_path.write_text(json.dumps(data), encoding="utf-8")
    exit_code, stdout, stderr = run_with_sandbox(root)
    assert exit_code == 1, (exit_code, stderr)
    assert any(
        "a relative directory prefix with trailing slash, or a relative markdown file path" in line
        for line in stdout.splitlines()
    ), stdout


def test_disposition_subtracts_a_file_and_is_counted_separately() -> None:
    """A disposed document leaves the unprocessed set and enters its own count."""
    root = build_sandbox()
    manifest_path = root / "manifest.json"
    data = json.loads(manifest_path.read_text(encoding="utf-8"))
    data["dispositions"] = [{
        "path": "docs/orphan.md",
        "disposition": "archived",
        "reason": "Superseded working note kept for provenance only.",
    }]
    manifest_path.write_text(json.dumps(data), encoding="utf-8")
    exit_code, stdout, stderr = run_with_sandbox(root)
    assert exit_code == 0, stderr
    lines = stdout.splitlines()
    assert "unprocessed: docs/orphan.md" not in lines, lines
    assert "disposition: docs/orphan.md" in lines, lines
    assert "disposition summary: 1 file(s) recorded as deliberately not formalized" in stderr, stderr
    # The subtraction must not be folded into the unprocessed count.
    assert "unprocessed summary: 2 file(s)" in stderr, stderr


def test_disposition_count_is_reported_even_when_zero() -> None:
    root = build_sandbox()
    exit_code, stdout, stderr = run_with_sandbox(root)
    assert exit_code == 0, stderr
    assert "disposition summary: 0 file(s) recorded as deliberately not formalized" in stderr, stderr


def test_strict_mode_passes_when_the_remainder_is_disposed() -> None:
    """Strict cutover accepts a corpus whose remainder is explicitly disposed."""
    root = build_sandbox()
    manifest_path = root / "manifest.json"
    data = json.loads(manifest_path.read_text(encoding="utf-8"))
    data["exclusions"] = ["docs/research/"]
    data["dispositions"] = [
        {"path": "docs/orphan.md", "disposition": "archived", "reason": "Not formalized."},
        {"path": "docs/sub/nested.md", "disposition": "archived", "reason": "Not formalized."},
    ]
    manifest_path.write_text(json.dumps(data), encoding="utf-8")
    exit_code, stdout, stderr = run_with_sandbox(root, ["--strict"])
    assert exit_code == 0, (exit_code, stdout, stderr)
    assert "disposition summary: 2 file(s)" in stderr, stderr


def test_malformed_disposition_is_reported_not_silently_dropped() -> None:
    root = build_sandbox()
    manifest_path = root / "manifest.json"
    data = json.loads(manifest_path.read_text(encoding="utf-8"))
    data["dispositions"] = [{"disposition": "archived", "reason": "No path at all."}]
    manifest_path.write_text(json.dumps(data), encoding="utf-8")
    exit_code, stdout, stderr = run_with_sandbox(root)
    assert exit_code == 1, (exit_code, stderr)
    assert any("malformed disposition skipped" in line for line in stdout.splitlines()), stdout


def test_missing_record_file_is_warned() -> None:
    root = build_sandbox()
    manifest_path = root / "manifest.json"
    data = json.loads(manifest_path.read_text(encoding="utf-8"))
    data["records"].append(
        {
            "id": "ghost",
            "kind": "spec",
            "path": "docs/does-not-exist.md",
            "status": "accepted",
            "date": "2026-08-21T00:00:00Z",
            "title": "Ghost",
            "summary": "ghost",
            "tags": [],
            "scopes": {
                "mode": "home",
                "product_ids": [],
                "project_ids": [],
                "domain_ids": [],
                "tag_ids": [],
            },
            "sha256": "sha256:" + "d" * 64,
        }
    )
    manifest_path.write_text(json.dumps(data), encoding="utf-8")
    exit_code, stdout, stderr = run_with_sandbox(root)
    # Missing-record-file findings still exit 1 because they are findings.
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any("missing-record-file: docs/does-not-exist.md" in line for line in stdout.splitlines()), stdout
    # Unprocessed listing is still emitted.
    assert any("unprocessed: docs/orphan.md" in line for line in stdout.splitlines()), stdout


def test_knowledge_roots_traversal_is_rejected() -> None:
    root = build_sandbox()
    manifest_path = root / "manifest.json"
    data = json.loads(manifest_path.read_text(encoding="utf-8"))
    data["knowledge_roots"] = ["../escape/"]
    manifest_path.write_text(json.dumps(data), encoding="utf-8")
    exit_code, stdout, stderr = run_with_sandbox(root)
    assert exit_code == 1, (exit_code, stderr)
    assert any("traversal segments are forbidden" in line for line in stdout.splitlines()), stdout


def test_knowledge_roots_absolute_path_is_rejected() -> None:
    root = build_sandbox()
    manifest_path = root / "manifest.json"
    data = json.loads(manifest_path.read_text(encoding="utf-8"))
    data["knowledge_roots"] = ["/etc/passwd/"]
    manifest_path.write_text(json.dumps(data), encoding="utf-8")
    exit_code, stdout, stderr = run_with_sandbox(root)
    assert exit_code == 1, (exit_code, stderr)
    assert any("traversal" in line or "absolute" in line or "must be" in line for line in stdout.splitlines()), stdout


def test_knowledge_roots_missing_trailingslash_is_rejected() -> None:
    root = build_sandbox()
    manifest_path = root / "manifest.json"
    data = json.loads(manifest_path.read_text(encoding="utf-8"))
    data["knowledge_roots"] = ["docs"]
    manifest_path.write_text(json.dumps(data), encoding="utf-8")
    exit_code, stdout, stderr = run_with_sandbox(root)
    assert exit_code == 1, (exit_code, stderr)
    assert any("must be a relative directory path with trailing slash" in line for line in stdout.splitlines()), stdout


def test_exclusions_traversal_is_rejected() -> None:
    root = build_sandbox()
    manifest_path = root / "manifest.json"
    data = json.loads(manifest_path.read_text(encoding="utf-8"))
    data["exclusions"] = ["../outside/"]
    manifest_path.write_text(json.dumps(data), encoding="utf-8")
    exit_code, stdout, stderr = run_with_sandbox(root)
    assert exit_code == 1, (exit_code, stderr)
    assert any("traversal" in line for line in stdout.splitlines()), stdout


def test_exclusions_default_when_absent() -> None:
    root = build_sandbox()
    manifest_path = root / "manifest.json"
    data = json.loads(manifest_path.read_text(encoding="utf-8"))
    del data["exclusions"]
    manifest_path.write_text(json.dumps(data), encoding="utf-8")
    exit_code, stdout, stderr = run_with_sandbox(root)
    assert exit_code == 0, stderr
    lines = stdout.splitlines()
    assert any("docs/sub/nested.md" in line for line in lines), lines
    assert any("docs/research/R1-finding.md" in line for line in lines), lines


def test_default_knowledge_root_is_docs_when_absent() -> None:
    root = build_sandbox()
    manifest_path = root / "manifest.json"
    data = json.loads(manifest_path.read_text(encoding="utf-8"))
    del data["knowledge_roots"]
    manifest_path.write_text(json.dumps(data), encoding="utf-8")
    exit_code, stdout, stderr = run_with_sandbox(root)
    assert exit_code == 0, stderr
    lines = stdout.splitlines()
    assert any("docs/orphan.md" in line for line in lines), lines


def test_root_that_does_not_exist_produces_no_listing() -> None:
    root = build_sandbox()
    manifest_path = root / "manifest.json"
    data = json.loads(manifest_path.read_text(encoding="utf-8"))
    data["knowledge_roots"] = ["notes/"]
    manifest_path.write_text(json.dumps(data), encoding="utf-8")
    exit_code, stdout, stderr = run_with_sandbox(root)
    assert exit_code == 0, stderr
    assert "unprocessed: docs/orphan.md" not in stdout, stdout


def main() -> int:
    tests = [
        value
        for name, value in sorted(globals().items())
        if name.startswith("test_") and callable(value)
    ]
    for test in tests:
        test()
    print(f"knowledge closure checker tests passed: {len(tests)} test(s)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
