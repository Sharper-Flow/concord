#!/usr/bin/env python3
"""Tests for scripts/check-committed-content.py.

Each case builds a scratch repository, commits a base, adds lines on a
branch, and asserts which added lines the check reports.
"""

from __future__ import annotations

import contextlib
import importlib.util
import io
import subprocess
import tempfile
import unittest
from pathlib import Path

import git_environment

# A hook that launched this suite must not redirect the scratch repositories'
# Git operations into the outer repository.
git_environment.scrub_inherited()

SCRIPT = Path(__file__).with_name("check-committed-content.py")
SPEC = importlib.util.spec_from_file_location("committed_content_checker", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)

# Fixture identifiers are assembled so this file carries none literally.
KEY = "CON" + "-4242"
WORK = "work-" + "0123456789abcdef01234567"


def git(root: Path, *arguments: str) -> str:
    return subprocess.run(["git", *arguments], cwd=root, check=True, capture_output=True, text=True).stdout


class CommittedContentTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)
        git(self.root, "init", "--quiet", "--initial-branch=main")
        git(self.root, "config", "user.name", "committed content tests")
        git(self.root, "config", "user.email", "committed-content-tests@example.invalid")
        self.write("README.md", "# Fixture\n")
        self.commit("base")
        git(self.root, "switch", "--quiet", "-c", "feature")

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def write(self, path: str, text: str) -> None:
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text, encoding="utf-8")

    def commit(self, message: str) -> None:
        git(self.root, "add", "-A")
        git(self.root, "commit", "--quiet", "-m", message)

    def findings(self, base_ref: str = "main") -> list[str]:
        return checker.check(self.root, base_ref)

    def assertFlags(self, path: str, line: int, rule: str, findings: list[str]) -> None:
        prefix = f"{path}:{line}: {rule}:"
        self.assertTrue(any(f.startswith(prefix) for f in findings), f"expected {prefix} in {findings}")

    def test_go_comment_identifiers_are_flagged_and_string_literals_are_not(self):
        self.write(
            "internal/x/x.go",
            "package x\n"
            f"// Load reads the file ({KEY}).\n"
            f'var name = "{KEY} // {WORK}"\n'
            f"var raw = `\n// {KEY}\n`\n"
            f"/* tracked by {WORK} */\n",
        )
        findings = self.findings()
        self.assertFlags("internal/x/x.go", 2, "planning-identifier", findings)
        self.assertFlags("internal/x/x.go", 7, "planning-identifier", findings)
        self.assertEqual(len(findings), 2, findings)

    def test_only_added_lines_are_inspected(self):
        git(self.root, "switch", "--quiet", "main")
        self.write("internal/x/x.go", f"package x\n\n// Old note ({KEY}).\nvar a = 1\n")
        self.commit("existing content")
        git(self.root, "switch", "--quiet", "feature")
        git(self.root, "merge", "--quiet", "main")
        self.write("internal/x/x.go", f"package x\n\n// Old note ({KEY}).\nvar a = 2\n// The value is fixed.\n")
        self.assertEqual(self.findings(), [])

    def test_lines_the_base_removes_after_the_branch_point_are_not_inspected(self):
        # Against the base tip, a line the base later deletes reads as added
        # on the branch; the merge base keeps it out of the change.
        git(self.root, "switch", "--quiet", "main")
        self.write("notes.md", f"Tracked by {KEY}.\nA durable sentence.\n")
        self.commit("existing notes")
        git(self.root, "switch", "--quiet", "feature")
        git(self.root, "merge", "--quiet", "main")
        git(self.root, "switch", "--quiet", "main")
        self.write("notes.md", "A durable sentence.\n")
        self.commit("base cleans the notes")
        git(self.root, "switch", "--quiet", "feature")
        self.write("guide.md", "A durable sentence.\n")
        self.assertEqual(self.findings(), [])

    def test_markdown_issue_references_are_flagged_and_heading_anchors_are_not(self):
        self.write(
            "docs/guide.md",
            "See [scope](#1-scope).\n"
            "Fixed in #412.\n"
            "See https://github.com/example/repo/pull/77 for detail.\n"
            'A suffix such as "#01" names no issue.\n',
        )
        findings = self.findings()
        self.assertFlags("docs/guide.md", 2, "planning-identifier", findings)
        self.assertFlags("docs/guide.md", 3, "planning-identifier", findings)
        self.assertEqual(len(findings), 2, findings)

    def test_python_comments_and_docstrings_are_flagged_and_other_strings_are_not(self):
        self.write(
            "scripts/tool.py",
            f'"""Tool for the thing ({KEY})."""\n'
            "\n"
            f"VALUE = '{KEY}'  # added by {WORK}\n"
            "\n"
            "def run():\n"
            f'    """Run it; this used to be a shell script."""\n'
            f"    return '{KEY}'\n",
        )
        findings = self.findings()
        self.assertFlags("scripts/tool.py", 1, "planning-identifier", findings)
        self.assertFlags("scripts/tool.py", 3, "planning-identifier", findings)
        self.assertFlags("scripts/tool.py", 6, "history-narration", findings)
        self.assertEqual(len(findings), 3, findings)

    def test_history_narration_phrases_are_flagged_and_ordinary_use_is_not(self):
        self.write(
            "adapter/a.ts",
            "// This module was renamed from the old adapter.\n"
            "// The key used to sign each request stays private.\n"
            "const url = `https://${host}//path`; // Formerly a constant.\n"
            "const t = `outer ${`inner // not a comment`} tail`;\n",
        )
        findings = self.findings()
        self.assertFlags("adapter/a.ts", 1, "history-narration", findings)
        self.assertFlags("adapter/a.ts", 3, "history-narration", findings)
        self.assertEqual(len(findings), 2, findings)

    def test_hash_comment_files_inspect_full_line_comments(self):
        self.write("ci.yml", f"# Gate added for {KEY}.\nrun: echo '#9'\n")
        self.write("bin/tool", f"#!/usr/bin/env bash\n# In this change the tool moved ({WORK}).\necho hi\n")
        findings = self.findings()
        self.assertFlags("ci.yml", 1, "planning-identifier", findings)
        self.assertFlags("bin/tool", 2, "history-narration", findings)
        self.assertFlags("bin/tool", 2, "planning-identifier", findings)
        self.assertEqual(len(findings), 3, findings)

    def test_exempt_paths_and_uninspected_kinds_are_never_flagged(self):
        line = f"Tracked by {KEY} and {WORK}; this used to be #12.\n"
        for path in (
            ".concord/docs/decisions/CD-0001-example.md",
            ".concord/docs/knowledge/records/example.md",
            "CHANGELOG.md",
            ".opencode/agents/concord-implement.md",
            "adapter/opencode/generated-contracts.ts",
            "internal/store/generated_work_kinds.go",
            ".concord/navigation/domains/example.md",
            "cmd/concord/testdata/case.md",
            ".concord/scenarios/example.md",
            "contracts/example.json",
        ):
            self.write(path, line)
        self.assertEqual(self.findings(), [])

    def test_a_moved_file_reports_no_added_lines(self):
        git(self.root, "switch", "--quiet", "main")
        self.write("docs/old.md", f"Tracked by {KEY}.\n" + "".join(f"Line {n}.\n" for n in range(20)))
        self.commit("existing doc")
        git(self.root, "switch", "--quiet", "feature")
        git(self.root, "merge", "--quiet", "main")
        git(self.root, "mv", "docs/old.md", "docs/new.md")
        self.assertEqual(self.findings(), [])

    def test_diff_text_inside_a_document_is_not_read_as_a_diff_header(self):
        # The base holds the file, so its added lines arrive as tracked diff
        # content that begins with "+++ " and "@@".
        git(self.root, "switch", "--quiet", "main")
        self.write("docs/patch.md", "Example:\n")
        self.commit("existing example")
        git(self.root, "switch", "--quiet", "feature")
        git(self.root, "merge", "--quiet", "main")
        self.write("docs/patch.md", "Example:\n++ b/ghost.md\n@@ -1 +1 @@\n+fixed in #7\n")
        self.write("docs/zeta.md", "A durable sentence.\n")
        findings = self.findings()
        self.assertFlags("docs/patch.md", 4, "planning-identifier", findings)
        self.assertEqual(len(findings), 1, findings)

    def test_crlf_missing_final_newline_quoted_paths_and_odd_bytes_keep_line_numbers(self):
        git(self.root, "switch", "--quiet", "main")
        (self.root / "docs").mkdir()
        (self.root / "docs/old notes.md").write_bytes(b"Intro.\r\n")
        (self.root / "docs/blob.bin").write_bytes(b"\x00\x01#12")
        self.write('docs/say "hi".md', "Intro.\n")
        self.commit("existing files")
        git(self.root, "switch", "--quiet", "feature")
        git(self.root, "merge", "--quiet", "main")
        (self.root / "docs/old notes.md").write_bytes(b"Intro.\r\nPage\x0cbreak \xff.\r\nTracked by " + KEY.encode() + b".")
        (self.root / "docs/blob.bin").write_bytes(b"\x00\x02#13")
        self.write('docs/say "hi".md', f"Intro.\nTracked by {WORK}.\n")
        findings = self.findings()
        self.assertFlags("docs/old notes.md", 3, "planning-identifier", findings)
        self.assertFlags('docs/say "hi".md', 2, "planning-identifier", findings)
        self.assertEqual(len(findings), 2, findings)

    def test_a_no_prefix_git_config_does_not_hide_added_lines(self):
        git(self.root, "config", "diff.noprefix", "true")
        git(self.root, "switch", "--quiet", "main")
        self.write("docs/guide.md", "Intro.\n")
        self.commit("existing guide")
        git(self.root, "switch", "--quiet", "feature")
        git(self.root, "merge", "--quiet", "main")
        self.write("docs/guide.md", f"Intro.\nTracked by {KEY}.\n")
        self.assertFlags("docs/guide.md", 2, "planning-identifier", self.findings())

    def test_main_exits_nonzero_with_findings_and_zero_when_clean(self):
        self.write("docs/clean.md", "A durable sentence.\n")
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(checker.main(["--root", str(self.root), "--base-ref", "main"]), 0)
        self.write("docs/dirty.md", f"Tracked by {KEY}.\n")
        output = io.StringIO()
        with contextlib.redirect_stdout(output), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(checker.main(["--root", str(self.root), "--base-ref", "main"]), 1)
        self.assertIn("docs/dirty.md:1: planning-identifier:", output.getvalue())

    def test_an_unresolvable_base_ref_fails_closed(self):
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(checker.main(["--root", str(self.root), "--base-ref", "no-such-ref"]), 2)


if __name__ == "__main__":
    unittest.main()
