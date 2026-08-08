#!/usr/bin/env python3
"""Unit tests for scripts/release.py using temporary Git repositories."""
from __future__ import annotations

import subprocess
import tempfile
import unittest
from pathlib import Path

import release


class ReleaseTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.repo = Path(self.tempdir.name)
        self.git("init", "--quiet")
        self.git("config", "user.email", "test@example.com")
        self.git("config", "user.name", "Release Test")
        self.commit("chore: constitutional snapshot")
        self.git("tag", "constitutional-bootstrap")

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def git(self, *args: str) -> str:
        return subprocess.check_output(["git", "-C", str(self.repo), *args], text=True)

    def commit(self, subject: str, body: str = "") -> None:
        path = self.repo / "history.txt"
        path.write_text(path.read_text() + subject + "\n" if path.exists() else subject + "\n")
        self.git("add", "history.txt")
        command = ["git", "-C", str(self.repo), "commit", "--quiet", "-m", subject]
        if body:
            command.extend(["-m", body])
        subprocess.check_call(command)

    def test_breaking_change_wins_with_major_bump(self) -> None:
        self.commit("fix: patch first")
        self.commit("feat!: incompatible API")
        result = release.compute(self.repo)
        self.assertEqual(result["bump"], "major")
        self.assertEqual(result["version"], "v1.0.0")
        self.assertIn("## Breaking Changes", result["changelog"])

    def test_breaking_footer_is_major(self) -> None:
        self.commit("feat: new API", "BREAKING CHANGE: old API removed")
        self.assertEqual(release.compute(self.repo)["bump"], "major")

    def test_malformed_header_with_breaking_footer_is_not_major(self) -> None:
        self.commit("not conventional", "BREAKING CHANGE: this must be ignored")
        result = release.compute(self.repo)
        self.assertFalse(result["release"])
        self.assertIsNone(result["bump"])

    def test_feat_is_minor(self) -> None:
        self.commit("feat(cli): add status")
        result = release.compute(self.repo)
        self.assertEqual(result["version"], "v0.1.0")
        self.assertIn("**cli:** feat(cli): add status", result["changelog"])

    def test_fix_is_patch(self) -> None:
        self.commit("fix: repair output")
        result = release.compute(self.repo)
        self.assertEqual(result["bump"], "patch")
        self.assertEqual(result["version"], "v0.0.1")

    def test_other_and_nonconventional_commits_are_noop(self) -> None:
        self.commit("docs: explain release process")
        self.commit("update spelling")
        result = release.compute(self.repo)
        self.assertFalse(result["release"])
        self.assertIsNone(result["version"])

    def test_first_release_ignores_constitutional_bootstrap_tag(self) -> None:
        self.commit("fix: first released fix")
        result = release.compute(self.repo)
        self.assertIsNone(result["base_tag"])
        self.assertEqual(result["base_version"], "v0.0.0")
        self.assertEqual(result["version"], "v0.0.1")

    def test_bootstrap_only_history_has_no_release(self) -> None:
        result = release.compute(self.repo)
        self.assertEqual(result["commit_boundary"], "constitutional-bootstrap")
        self.assertFalse(result["release"])
        self.assertEqual(result["commits"], [])

    def test_uses_latest_semver_tag_as_base(self) -> None:
        self.commit("fix: initial release")
        self.git("tag", "v1.2.3")
        self.commit("feat: follow-up feature")
        result = release.compute(self.repo)
        self.assertEqual(result["base_tag"], "v1.2.3")
        self.assertEqual(result["version"], "v1.3.0")


if __name__ == "__main__":
    unittest.main()
