#!/usr/bin/env python3
"""Tests for the CD allocation preflight checker."""

from __future__ import annotations

import importlib.util
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("check-cd-allocation.py")
SPEC = importlib.util.spec_from_file_location("cd_allocation_checker", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


def git(root: Path, *arguments: str, when: str | None = None) -> None:
    environment = None
    if when is not None:
        environment = {**os.environ, "GIT_AUTHOR_DATE": when, "GIT_COMMITTER_DATE": when}
    subprocess.run(
        ["git", *arguments], cwd=root, check=True, capture_output=True, env=environment
    )


class RepoFixture(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)
        (self.root / "docs").mkdir()
        git(self.root, "init", "--quiet", "--initial-branch=main")
        git(self.root, "config", "user.name", "CD allocation tests")
        git(self.root, "config", "user.email", "cd-allocation-tests@example.invalid")
        self.write_manifest(["CD-0001"])
        self.commit("baseline")

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def write_manifest(
        self, identifiers: list[str], paths: dict[str, str] | None = None
    ) -> None:
        records: list[dict[str, str]] = []
        for identifier in identifiers:
            record = {"id": identifier}
            if paths is not None and identifier in paths:
                record["path"] = paths[identifier]
            records.append(record)
        (self.root / "docs/concord-knowledge-index.v1.json").write_text(
            json.dumps({"records": records}) + "\n",
            encoding="utf-8",
        )

    def commit(self, message: str, when: str | None = None) -> None:
        git(self.root, "add", "docs/concord-knowledge-index.v1.json")
        git(self.root, "commit", "--quiet", "-m", message, when=when)

    def seed_peer(
        self,
        identifiers: list[str],
        when: str,
        name: str = "peer",
        paths: dict[str, str] | None = None,
    ) -> None:
        """Create a branch claiming identifiers, then return to a fresh branch off main."""
        git(self.root, "checkout", "--quiet", "-b", name, "main")
        self.write_manifest(identifiers, paths)
        self.commit(f"{name} claim", when=when)
        git(self.root, "checkout", "--quiet", "main")

    def start_branch(self, name: str = "mine") -> None:
        git(self.root, "checkout", "--quiet", "-b", name, "main")

    def peer_check(self, **overrides: object) -> list[str]:
        arguments: dict[str, object] = {
            "root": self.root,
            "against": "main",
            "no_fetch": True,
            "peer_namespace": "refs/heads",
        }
        arguments.update(overrides)
        return checker.check(**arguments)  # type: ignore[arg-type]


class CDAllocationTests(RepoFixture):
    def test_clean_new_record_passes(self) -> None:
        self.write_manifest(["CD-0001", "CD-0002"])
        self.assertEqual(
            checker.check(root=self.root, against="main", no_fetch=True), []
        )

    def test_removed_record_is_reported(self) -> None:
        self.write_manifest(["CD-0001", "CD-0002"])
        self.commit("add second baseline record")
        self.write_manifest(["CD-0001"])

        findings = checker.check(root=self.root, against="main", no_fetch=True)

        self.assertEqual(len(findings), 1)
        self.assertIn("removed-records", findings[0])
        self.assertIn("CD-0002", findings[0])
        self.assertIn("explicit superseding record", findings[0])

    def test_missing_ref_in_offline_mode_fails(self) -> None:
        findings = checker.check(root=self.root, against="missing", no_fetch=True)

        self.assertEqual(len(findings), 1)
        self.assertIn("comparison ref 'missing' is unavailable", findings[0])
        self.assertIn("rerun without --no-fetch", findings[0])

    def test_against_synthetic_ref(self) -> None:
        git(self.root, "branch", "synthetic")
        self.write_manifest(["CD-0001", "CD-0002"])

        self.assertEqual(
            checker.check(root=self.root, against="synthetic", no_fetch=True), []
        )

    def test_duplicate_new_id_is_reported(self) -> None:
        self.write_manifest(["CD-0001", "CD-0002", "CD-0002"])

        findings = checker.check(root=self.root, against="main", no_fetch=True)

        self.assertEqual(len(findings), 1)
        self.assertIn("duplicate-new", findings[0])
        self.assertIn("new CD id CD-0002 appears 2 times", findings[0])

    def test_non_cd_records_are_ignored(self) -> None:
        self.write_manifest(["CD-0001", "lesson-1", "lesson-1"])

        self.assertEqual(
            checker.check(root=self.root, against="main", no_fetch=True), []
        )

    def test_duplicate_json_keys_are_rejected(self) -> None:
        self.write_manifest(["CD-0001"])
        (self.root / "docs/concord-knowledge-index.v1.json").write_text(
            '{"records": [], "records": []}\n', encoding="utf-8"
        )

        findings = checker.check(root=self.root, against="main", no_fetch=True)

        self.assertEqual(len(findings), 1)
        self.assertIn("duplicate JSON key: records", findings[0])


class ConcurrentClaimTests(RepoFixture):
    def test_peer_claiming_a_different_id_passes(self) -> None:
        self.seed_peer(["CD-0001", "CD-0002"], when="2026-01-01T00:00:00Z")
        self.start_branch()
        self.write_manifest(["CD-0001", "CD-0003"])
        self.commit("mine claim", when="2026-01-02T00:00:00Z")

        self.assertEqual(self.peer_check(), [])

    def test_earlier_peer_claim_makes_this_branch_renumber(self) -> None:
        self.seed_peer(["CD-0001", "CD-0002"], when="2026-01-01T00:00:00Z")
        self.start_branch()
        self.write_manifest(["CD-0001", "CD-0002"])
        self.commit("mine claim", when="2026-01-02T00:00:00Z")

        findings = self.peer_check()

        self.assertEqual(len(findings), 1)
        self.assertIn("concurrent-claim", findings[0])
        self.assertIn("CD-0002", findings[0])
        self.assertIn("refs/heads/peer", findings[0])
        self.assertIn("later claimant", findings[0])
        self.assertIn("renumber-cd.py", findings[0])

    def test_later_peer_claim_leaves_this_branch_alone(self) -> None:
        self.seed_peer(["CD-0001", "CD-0002"], when="2026-01-03T00:00:00Z")
        self.start_branch()
        self.write_manifest(["CD-0001", "CD-0002"])
        self.commit("mine claim", when="2026-01-02T00:00:00Z")

        self.assertEqual(self.peer_check(), [])

    def test_own_branch_is_not_a_peer(self) -> None:
        self.start_branch()
        self.write_manifest(["CD-0001", "CD-0002"])
        self.commit("mine claim", when="2026-01-02T00:00:00Z")

        self.assertEqual(self.peer_check(), [])

    def test_uncommitted_claim_yields_to_any_peer(self) -> None:
        self.seed_peer(["CD-0001", "CD-0002"], when="2030-01-01T00:00:00Z")
        self.start_branch()
        self.write_manifest(["CD-0001", "CD-0002"])

        findings = self.peer_check()

        self.assertEqual(len(findings), 1)
        self.assertIn("concurrent-claim", findings[0])

    def test_one_record_reached_through_two_refs_is_not_a_collision(self) -> None:
        # A merge queue builds a temporary ref per attempt, so the branch
        # already on the remote and the queue ref both carry the same claim.
        # Ref identity reports that branch as colliding with itself, and no
        # renumber escapes it: the next push reproduces the pair at the new id.
        shared = {"CD-0002": "docs/decisions/CD-0002-shared.md"}
        self.seed_peer(
            ["CD-0001", "CD-0002"],
            when="2026-01-01T00:00:00Z",
            name="origin-mine",
            paths=shared,
        )
        self.start_branch(name="gh-readonly-queue-main-pr-1")
        self.write_manifest(["CD-0001", "CD-0002"], shared)
        self.commit("queue attempt", when="2026-01-02T00:00:00Z")

        self.assertEqual(self.peer_check(), [])

    def test_two_records_claiming_one_id_still_collide(self) -> None:
        self.seed_peer(
            ["CD-0001", "CD-0002"],
            when="2026-01-01T00:00:00Z",
            paths={"CD-0002": "docs/decisions/CD-0002-theirs.md"},
        )
        self.start_branch()
        self.write_manifest(
            ["CD-0001", "CD-0002"], {"CD-0002": "docs/decisions/CD-0002-mine.md"}
        )
        self.commit("mine claim", when="2026-01-02T00:00:00Z")

        findings = self.peer_check()

        self.assertEqual(len(findings), 1)
        self.assertIn("concurrent-claim", findings[0])
        self.assertIn("CD-0002", findings[0])

    def test_next_free_accounts_for_peer_claims(self) -> None:
        self.seed_peer(["CD-0001", "CD-0007"], when="2026-01-01T00:00:00Z")
        self.start_branch()
        self.write_manifest(["CD-0001"])

        nxt = checker.next_free(
            root=self.root, against="main", peer_namespace="refs/heads"
        )

        self.assertEqual(nxt, "CD-0008")


if __name__ == "__main__":
    unittest.main()
