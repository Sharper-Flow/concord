#!/usr/bin/env python3
"""Tests for the CD renumber command."""

from __future__ import annotations

import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("renumber-cd.py")
SPEC = importlib.util.spec_from_file_location("renumber_cd", SCRIPT)
assert SPEC and SPEC.loader
renumber = importlib.util.module_from_spec(SPEC)
# @dataclass resolves annotations through sys.modules, so the module must be
# registered before it executes.
sys.modules[SPEC.name] = renumber
SPEC.loader.exec_module(renumber)


def git(root: Path, *arguments: str) -> None:
    subprocess.run(["git", *arguments], cwd=root, check=True, capture_output=True)


class RenumberTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)
        for directory in ("docs/decisions", "docs/knowledge/records", "docs/knowledge/coverage"):
            (self.root / directory).mkdir(parents=True)
        git(self.root, "init", "--quiet", "--initial-branch=main")
        git(self.root, "config", "user.name", "renumber tests")
        git(self.root, "config", "user.email", "renumber-tests@example.invalid")
        self.seed("CD-0061", "root-home-states-its-claim")
        self.commit()

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def seed(self, identifier: str, slug: str) -> None:
        (self.root / f"docs/decisions/{identifier}-{slug}.md").write_text(
            f"# {identifier}: {slug}\n\nThis decision is {identifier}.\n", encoding="utf-8"
        )
        (self.root / f"docs/knowledge/records/{identifier}.json").write_text(
            json.dumps(
                {
                    "id": identifier,
                    "kind": "decision",
                    "path": f"docs/decisions/{identifier}-{slug}.md",
                    "sha256": "sha256:" + "0" * 64,
                },
                indent=2,
                sort_keys=True,
            )
            + "\n",
            encoding="utf-8",
        )
        (self.root / f"docs/knowledge/coverage/{identifier}.json").write_text(
            json.dumps({"id": identifier, "state": "satisfied"}, indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
        )

    def write(self, relative: str, text: str) -> None:
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")

    def write_manifest(self, identifiers: list[str]) -> None:
        self.write(
            "docs/concord-knowledge-index.v1.json",
            json.dumps({"records": [{"id": identifier} for identifier in identifiers]}) + "\n",
        )

    def commit(self) -> None:
        git(self.root, "add", "-A")
        git(self.root, "commit", "--quiet", "-m", "seed")

    def move(
        self, old: str = "CD-0061", new: str = "CD-0062", against: str = "missing-ref"
    ) -> list[str]:
        findings, prepared = renumber.plan(self.root, old, new, against=against)
        if findings:
            return findings
        assert prepared is not None
        return renumber.apply(self.root, prepared, generate=False)

    def test_moves_document_and_both_shards(self) -> None:
        self.assertEqual(self.move(), [])

        self.assertFalse(
            (self.root / "docs/decisions/CD-0061-root-home-states-its-claim.md").exists()
        )
        self.assertTrue(
            (self.root / "docs/decisions/CD-0062-root-home-states-its-claim.md").exists()
        )
        self.assertFalse((self.root / "docs/knowledge/records/CD-0061.json").exists())
        self.assertTrue((self.root / "docs/knowledge/records/CD-0062.json").exists())
        self.assertFalse((self.root / "docs/knowledge/coverage/CD-0061.json").exists())
        self.assertTrue((self.root / "docs/knowledge/coverage/CD-0062.json").exists())

    def test_record_id_and_path_follow_the_move(self) -> None:
        self.assertEqual(self.move(), [])

        record = json.loads((self.root / "docs/knowledge/records/CD-0062.json").read_text())
        self.assertEqual(record["id"], "CD-0062")
        self.assertEqual(record["path"], "docs/decisions/CD-0062-root-home-states-its-claim.md")
        coverage = json.loads((self.root / "docs/knowledge/coverage/CD-0062.json").read_text())
        self.assertEqual(coverage["id"], "CD-0062")

    def test_body_and_scattered_references_move(self) -> None:
        self.write("docs/priorities.md", "See CD-0061 for the root home rule.\n")
        self.write("internal/store/law.go", "// CD-0061 governs this guard.\n")
        self.commit()

        self.assertEqual(self.move(), [])

        self.assertIn(
            "This decision is CD-0062.",
            (self.root / "docs/decisions/CD-0062-root-home-states-its-claim.md").read_text(),
        )
        self.assertIn("CD-0062", (self.root / "docs/priorities.md").read_text())
        self.assertIn("CD-0062", (self.root / "internal/store/law.go").read_text())

    def test_no_reference_to_the_old_identifier_survives(self) -> None:
        self.write("docs/priorities.md", "CD-0061 and CD-0061 again.\n")
        self.commit()

        self.assertEqual(self.move(), [])
        self.assertEqual(renumber.survivors(self.root, "CD-0061"), [])

    def test_untracked_file_is_not_edited(self) -> None:
        self.write("docs/scratch.md", "CD-0061 lives in an untracked note.\n")

        self.assertEqual(self.move(), [])
        self.assertIn("CD-0061", (self.root / "docs/scratch.md").read_text())

    def test_refuses_when_the_target_number_is_taken(self) -> None:
        self.seed("CD-0062", "another-decision")
        self.commit()

        findings = self.move()

        self.assertTrue(findings)
        self.assertTrue(any("already has a shard" in finding for finding in findings))

    def test_refuses_when_the_target_is_referenced_elsewhere(self) -> None:
        self.write("docs/priorities.md", "CD-0062 is mentioned already.\n")
        self.commit()

        findings = self.move()

        self.assertTrue(any("already references CD-0062" in finding for finding in findings))

    def test_refuses_an_unknown_identifier(self) -> None:
        findings = self.move(old="CD-0999")

        self.assertTrue(any("no decision document matches" in finding for finding in findings))

    def test_refuses_a_malformed_identifier(self) -> None:
        findings = self.move(new="CD-62")

        self.assertEqual(len(findings), 1)
        self.assertIn("not of the form CD-NNNN", findings[0])

    def test_refuses_a_no_op_move(self) -> None:
        findings = self.move(new="CD-0061")

        self.assertEqual(len(findings), 1)
        self.assertIn("same identifier", findings[0])

    def test_refuses_when_a_shard_is_missing(self) -> None:
        (self.root / "docs/knowledge/coverage/CD-0061.json").unlink()
        self.commit()

        findings = self.move()

        self.assertTrue(any("is not a complete record" in finding for finding in findings))

    def test_refuses_an_unowned_generated_file(self) -> None:
        self.write(
            "internal/agent/generated_mystery.go", "// Code generated. DO NOT EDIT.\n// CD-0061\n"
        )
        self.commit()

        findings = self.move()

        self.assertTrue(any("DO NOT EDIT header" in finding for finding in findings))
        self.assertTrue(any("generated_mystery.go" in finding for finding in findings))

    def test_known_generated_output_is_left_to_its_generator(self) -> None:
        self.write("docs/law-coverage.v1.json", '{"note": "CD-0061"}\n')
        self.commit()

        findings, prepared = renumber.plan(
            self.root, "CD-0061", "CD-0062", against="missing-ref"
        )

        self.assertEqual(findings, [])
        assert prepared is not None
        self.assertNotIn(Path("docs/law-coverage.v1.json"), prepared.edits)

    def test_longer_number_is_not_matched(self) -> None:
        self.write("docs/priorities.md", "CD-0061 differs from CD-00610.\n")
        self.commit()

        self.assertEqual(self.move(), [])

        self.assertIn("CD-00610", (self.root / "docs/priorities.md").read_text())
        self.assertIn("CD-0062 differs", (self.root / "docs/priorities.md").read_text())

    def test_refuses_to_move_a_landed_cd(self) -> None:
        self.write_manifest(["CD-0061"])
        self.commit()
        git(self.root, "branch", "landed")

        findings = self.move(against="landed")

        self.assertTrue(any("durable law" in finding for finding in findings))
        self.assertTrue(any("has not landed" in finding for finding in findings))

    def test_refuses_a_target_that_landed(self) -> None:
        self.write_manifest(["CD-0062"])
        self.commit()
        git(self.root, "branch", "landed")

        findings = self.move(against="landed")

        self.assertTrue(any("already on landed" in finding for finding in findings))

    def test_branch_local_cd_moves_against_a_landed_ref(self) -> None:
        self.write_manifest(["CD-0060"])
        self.commit()
        git(self.root, "branch", "landed")

        self.assertEqual(self.move(against="landed"), [])
        self.assertTrue((self.root / "docs/knowledge/records/CD-0062.json").exists())

    def test_unreachable_ref_does_not_block(self) -> None:
        self.assertEqual(self.move(against="no-such-ref"), [])

    def test_dry_run_plan_writes_nothing(self) -> None:
        findings, prepared = renumber.plan(
            self.root, "CD-0061", "CD-0062", against="missing-ref"
        )

        self.assertEqual(findings, [])
        assert prepared is not None
        self.assertIn("CD-0061 -> CD-0062", renumber.describe(prepared))
        self.assertTrue((self.root / "docs/knowledge/records/CD-0061.json").exists())
        self.assertFalse((self.root / "docs/knowledge/records/CD-0062.json").exists())


if __name__ == "__main__":
    unittest.main()
