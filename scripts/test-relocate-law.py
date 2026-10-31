#!/usr/bin/env python3
"""Synthetic regression tests for the accepted-law relocation mover."""

from __future__ import annotations

import hashlib
import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import git_environment

# This suite drives Git directly, so a hook that launched it must
# not keep a redirecting Git namespace in place.
git_environment.scrub_inherited()

SCRIPT = Path(__file__).with_name("relocate-law.py")
SPEC = importlib.util.spec_from_file_location("relocate_law", SCRIPT)
assert SPEC and SPEC.loader
relocate = importlib.util.module_from_spec(SPEC)
# Module-level decorators resolve annotations through sys.modules, so the
# module must be registered before it executes.
sys.modules[SPEC.name] = relocate
SPEC.loader.exec_module(relocate)


def git(root: Path, *arguments: str) -> None:
    subprocess.run(["git", *arguments], cwd=root, check=True, capture_output=True)


DECISION_A = ".concord/docs/decisions/CD-0001-first-decision.md"
DECISION_B = ".concord/docs/decisions/CD-0002-second-decision.md"
CONSTITUTION = ".concord/docs/priorities.md"
SPEC_DOC = ".concord/docs/product-query-contract.md"
LESSON = ".concord/docs/lessons/2026-01-01-one-lesson.md"
REFERENCE_DOC = ".concord/docs/capability-placement.md"

NEW_DECISION_A = ".concord/law/decisions/CD-0001-first-decision.md"
NEW_DECISION_B = ".concord/law/decisions/CD-0002-second-decision.md"
NEW_CONSTITUTION = ".concord/law/constitution/priorities.md"
NEW_SPEC = ".concord/law/specs/product-query-contract.md"


class RelocationTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)
        git(self.root, "init", "--quiet", "--initial-branch=main")
        git(self.root, "config", "user.name", "relocate tests")
        git(self.root, "config", "user.email", "relocate-tests@example.invalid")

        self.seed_document(
            DECISION_A,
            "# CD-0001: first decision\n\n"
            f"Refines [{DECISION_B.rsplit('/', 1)[1]}](CD-0002-second-decision.md#d2) "
            "and [the constitution](../priorities.md#constraints) "
            f"beside [placement](../{REFERENCE_DOC.rsplit('/', 1)[1]}).\n",
        )
        self.seed_record("CD-0001", "decision", DECISION_A)
        self.seed_document(
            DECISION_B,
            "# CD-0002: second decision\n\n## D2\n\nBody.\n",
        )
        self.seed_record("CD-0002", "decision", DECISION_B)
        self.seed_document(
            CONSTITUTION,
            "# Priorities\n\n## Constraints\n\nThe envelope.\n",
        )
        self.seed_record("priorities", "constitution", CONSTITUTION)
        self.seed_document(
            SPEC_DOC,
            "# Product query contract\n\nSee [the constitution](priorities.md).\n",
        )
        self.seed_record("Q1", "spec", SPEC_DOC)
        self.seed_document(
            LESSON,
            "# A lesson\n\nLearned from [CD-0001](../decisions/CD-0001-first-decision.md).\n",
        )
        self.seed_record("lesson-one", "lesson", LESSON, status="published")
        self.seed_document(REFERENCE_DOC, "# Capability placement\n")
        self.seed_record("placement", "reference", REFERENCE_DOC, status="published")
        self.write("AGENTS.md", f"Law: [`CD-0001`]({DECISION_A}) and [priorities]({CONSTITUTION}#constraints).\n")
        self.commit()

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    # -- fixture helpers ---------------------------------------------------

    def digest(self, relative: str) -> str:
        return "sha256:" + hashlib.sha256((self.root / relative).read_bytes()).hexdigest()

    def seed_document(self, relative: str, text: str) -> None:
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")

    def seed_record(
        self,
        identifier: str,
        kind: str,
        path: str,
        *,
        status: str = "accepted",
        evidence: list[str] | None = None,
    ) -> None:
        record: dict = {
            "id": identifier,
            "kind": kind,
            "path": path,
            "sha256": self.digest(path),
            "status": status,
        }
        if evidence is not None:
            record["evidence"] = evidence
        self.write_shard(identifier, record)

    def write_shard(self, identifier: str, record: dict) -> None:
        path = self.root / f".concord/docs/knowledge/records/{identifier}.json"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(relocate.shard_format.canonical_bytes(record))

    def write(self, relative: str, text: str) -> None:
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")

    def commit(self) -> None:
        git(self.root, "add", "-A")
        git(self.root, "commit", "--quiet", "-m", "seed")

    def shard(self, identifier: str) -> dict:
        return json.loads(
            (self.root / f".concord/docs/knowledge/records/{identifier}.json").read_text(encoding="utf-8")
        )

    def plan(self) -> relocate.RelocationPlan:
        findings, prepared = relocate.plan(self.root)
        self.assertEqual(findings, [])
        assert prepared is not None
        return prepared

    def move(self) -> list[str]:
        findings, prepared = relocate.plan(self.root)
        if findings:
            return findings
        assert prepared is not None
        return relocate.apply(self.root, prepared)

    def tree_bytes(self) -> dict[str, bytes]:
        return {
            path.relative_to(self.root).as_posix(): path.read_bytes()
            for path in sorted(self.root.rglob("*"))
            if path.is_file() and ".git" not in path.parts
        }

    # -- membership and moves ---------------------------------------------

    def test_moves_accepted_law_and_preserves_basenames(self) -> None:
        self.assertEqual(self.move(), [])

        self.assertTrue((self.root / NEW_DECISION_A).is_file())
        self.assertTrue((self.root / NEW_DECISION_B).is_file())
        self.assertTrue((self.root / NEW_CONSTITUTION).is_file())
        self.assertTrue((self.root / NEW_SPEC).is_file())
        self.assertFalse((self.root / DECISION_A).exists())
        self.assertFalse((self.root / CONSTITUTION).exists())
        self.assertFalse((self.root / SPEC_DOC).exists())
        # Nonlaw stays in place.
        self.assertTrue((self.root / LESSON).is_file())
        self.assertTrue((self.root / REFERENCE_DOC).is_file())
        # Record shards are retained.
        self.assertTrue((self.root / ".concord/docs/knowledge/records/CD-0001.json").is_file())
        self.assertTrue((self.root / ".concord/docs/knowledge/records/priorities.json").is_file())

    def test_wholesale_moved_directory_is_removed_and_mapped(self) -> None:
        self.assertEqual(self.move(), [])

        self.assertFalse((self.root / ".concord/docs/decisions").exists())
        self.assertTrue((self.root / ".concord/docs/lessons").is_dir())

    def test_record_path_evidence_and_hash_follow_the_move(self) -> None:
        # Evidence naming a moved constitution document must move with it.
        record = self.shard("CD-0001")
        record["evidence"] = [CONSTITUTION, "scripts/check-doc-links.py"]
        self.write_shard("CD-0001", record)
        self.commit()

        self.assertEqual(self.move(), [])

        moved = self.shard("CD-0001")
        self.assertEqual(moved["path"], NEW_DECISION_A)
        self.assertEqual(moved["evidence"], [NEW_CONSTITUTION, "scripts/check-doc-links.py"])
        self.assertEqual(moved["kind"], "decision")
        self.assertEqual(moved["status"], "accepted")
        self.assertEqual(moved["sha256"], self.digest(NEW_DECISION_A))

    def test_nonlaw_record_hash_follows_repaired_links(self) -> None:
        self.assertEqual(self.move(), [])

        lesson = self.shard("lesson-one")
        self.assertEqual(lesson["path"], LESSON)
        self.assertEqual(lesson["status"], "published")
        self.assertEqual(lesson["sha256"], self.digest(LESSON))

    # -- link rewriting ----------------------------------------------------

    def test_path_labels_follow_the_rewritten_destination(self) -> None:
        self.write(
            ".concord/docs/catalog.md",
            "[`decisions/CD-0001-first-decision.md`](./decisions/CD-0001-first-decision.md#d1)\n"
            "[decisions/CD-0001-first-decision.md#d1](decisions/CD-0001-first-decision.md#d1)\n"
            "[Read the decision](decisions/CD-0001-first-decision.md#d1)\n",
        )
        self.commit()
        self.assertEqual(self.move(), [])
        body = (self.root / ".concord/docs/catalog.md").read_text()
        self.assertIn("[`../law/decisions/CD-0001-first-decision.md`]", body)
        self.assertIn("[../law/decisions/CD-0001-first-decision.md#d1]", body)
        self.assertIn("[Read the decision]", body)

    def test_links_between_moved_files_relativize_from_the_new_source(self) -> None:
        self.assertEqual(self.move(), [])

        body = (self.root / NEW_DECISION_A).read_text(encoding="utf-8")
        self.assertIn("[CD-0002-second-decision.md](CD-0002-second-decision.md#d2)", body)
        self.assertIn("[the constitution](../constitution/priorities.md#constraints)", body)
        self.assertIn(f"[placement](../../docs/{REFERENCE_DOC.rsplit('/', 1)[1]})", body)
        # Resolution handles the ../ prefix that check-doc-links resolves.

    def test_links_from_moved_files_to_nonlaw_targets_relativize(self) -> None:
        self.assertEqual(self.move(), [])

        body = (self.root / NEW_SPEC).read_text(encoding="utf-8")
        self.assertIn("[the constitution](../constitution/priorities.md)", body)

    def test_links_from_nonlaw_files_to_moved_law_are_repaired(self) -> None:
        self.assertEqual(self.move(), [])

        body = (self.root / LESSON).read_text(encoding="utf-8")
        self.assertIn("[CD-0001](../../law/decisions/CD-0001-first-decision.md)", body)

    def test_root_relative_links_are_mapped(self) -> None:
        self.assertEqual(self.move(), [])

        body = (self.root / "AGENTS.md").read_text(encoding="utf-8")
        self.assertIn(f"[`CD-0001`]({NEW_DECISION_A})", body)
        self.assertIn(f"[priorities]({NEW_CONSTITUTION}#constraints)", body)

    def test_html_href_and_src_are_mapped(self) -> None:
        self.write(
            "README.md",
            f'<a href="{DECISION_A}">law</a> <img src="{CONSTITUTION}" alt="c">\n',
        )
        self.write(".concord/docs/page.html", f'<a href="{DECISION_B}">b</a>\n')
        self.commit()

        self.assertEqual(self.move(), [])

        self.assertIn(f'<a href="{NEW_DECISION_A}">law</a>', (self.root / "README.md").read_text())
        self.assertIn(f'<img src="{NEW_CONSTITUTION}"', (self.root / "README.md").read_text())
        self.assertIn(f'<a href="{NEW_DECISION_B}">b</a>', (self.root / ".concord/docs/page.html").read_text())

    def test_reference_definitions_are_mapped(self) -> None:
        self.write(
            ".concord/docs/notes.md",
            "See [a][d1] and [b][d2].\n\n"
            f"[d1]: {DECISION_A}\n"
            f"[d2]: {CONSTITUTION}#constraints\n",
        )
        self.commit()

        self.assertEqual(self.move(), [])

        body = (self.root / ".concord/docs/notes.md").read_text(encoding="utf-8")
        self.assertIn(f"[d1]: {NEW_DECISION_A}\n", body)
        self.assertIn(f"[d2]: {NEW_CONSTITUTION}#constraints\n", body)

    def test_exact_path_literals_are_mapped_with_fragments(self) -> None:
        self.write(
            ".concord/docs/overview.md",
            f"Read {CONSTITUTION}#constraints first, then {DECISION_A}.\n",
        )
        self.commit()

        self.assertEqual(self.move(), [])

        body = (self.root / ".concord/docs/overview.md").read_text(encoding="utf-8")
        self.assertIn(f"Read {NEW_CONSTITUTION}#constraints first, then {NEW_DECISION_A}.", body)
        self.assertNotIn(".concord/docs/decisions/", body)

    def test_directory_literals_map_to_the_new_law_directory(self) -> None:
        self.write(
            ".concord/docs/overview.md",
            "Decisions live in .concord/docs/decisions/ under their basenames.\n",
        )
        self.commit()

        self.assertEqual(self.move(), [])

        body = (self.root / ".concord/docs/overview.md").read_text(encoding="utf-8")
        self.assertIn(".concord/law/decisions/", body)
        self.assertNotIn(".concord/docs/decisions/", body)

    def test_guards_never_rewrite_lookalike_paths(self) -> None:
        self.write(
            ".concord/docs/overview.md",
            "Not law: ~/.concord/docs/priorities.md, other.concord/docs/priorities.md, "
            f"{CONSTITUTION}.bak, {CONSTITUTION}-old, prefix{CONSTITUTION}, and never "
            ".concord/docs/decisions2/CD-0001-first-decision.md.\n"
            f"A backup under the moved directory follows it: {DECISION_A}.bak.\n",
        )
        self.commit()

        self.assertEqual(self.move(), [])

        body = (self.root / ".concord/docs/overview.md").read_text(encoding="utf-8")
        # Home-anchored, extended, and sibling-directory occurrences refuse.
        self.assertIn("~/.concord/docs/priorities.md", body)
        self.assertIn("other.concord/docs/priorities.md", body)
        self.assertIn(f"{CONSTITUTION}.bak", body)
        self.assertIn(f"{CONSTITUTION}-old", body)
        self.assertIn(f"prefix{CONSTITUTION}", body)
        self.assertIn(".concord/docs/decisions2/CD-0001-first-decision.md", body)
        # A path beneath the wholesale-moved directory maps with the directory.
        self.assertIn(f"{NEW_DECISION_A}.bak", body)
        self.assertNotIn(".concord/docs/decisions/", body)

    def test_external_historical_urls_are_never_rewritten(self) -> None:
        pinned = f"https://github.com/example/concord/tree/02c0bae9c28e/{DECISION_A}"
        self.write(
            "skills/demo/SKILL.md",
            f"Historical: [pinned]({pinned}) and bare {pinned} stays.\n",
        )
        self.commit()

        self.assertEqual(self.move(), [])

        body = (self.root / "skills/demo/SKILL.md").read_text(encoding="utf-8")
        self.assertEqual(body.count(DECISION_A), 2)

    def test_links_that_resolve_outside_the_repository_are_left_alone(self) -> None:
        self.write("AGENTS.md", "[escape](../../../../outside/priorities.md)\n")
        self.commit()

        self.assertEqual(self.move(), [])

        self.assertIn("[escape](../../../../outside/priorities.md)", (self.root / "AGENTS.md").read_text())

    def test_unrelated_links_are_untouched(self) -> None:
        self.write(
            "AGENTS.md",
            "[keep](scripts/check-doc-links.py) [self](#section) [authored](./README.md)\n",
        )
        self.commit()

        self.assertEqual(self.move(), [])

        body = (self.root / "AGENTS.md").read_text(encoding="utf-8")
        self.assertIn("[keep](scripts/check-doc-links.py)", body)
        self.assertIn("[self](#section)", body)
        # A destination nothing moved keeps its authored spelling.
        self.assertIn("[authored](./README.md)", body)

    # -- generated files ---------------------------------------------------

    def test_generated_files_are_skipped(self) -> None:
        self.write(
            "internal/store/generated_probe.go",
            f"// Code generated by scripts/generate-probe.py; DO NOT EDIT.\n// See {DECISION_A}\n",
        )
        self.write(
            ".opencode/agents/concord-demo.md",
            f"---\ndescription: lane\n---\nSee {DECISION_A}\n",
        )
        self.commit()
        before = self.tree_bytes()

        self.assertEqual(self.move(), [])

        self.assertEqual(
            (self.root / "internal/store/generated_probe.go").read_text(),
            before["internal/store/generated_probe.go"].decode(),
        )
        self.assertEqual(
            (self.root / ".opencode/agents/concord-demo.md").read_text(),
            before[".opencode/agents/concord-demo.md"].decode(),
        )

    # -- exact literals in authored source files ---------------------------

    def test_source_exact_literals_follow_the_moves(self) -> None:
        self.write(
            "scripts/skill-map.py",
            "SKILL_REFERENCES = {\n"
            f"    'intake': '{DECISION_A}',\n"
            f"    'law': '{CONSTITUTION}',\n"
            "}\n",
        )
        self.write(
            "internal/store/recipe_test.go",
            f"const documentPath = \"{DECISION_B}\"\n",
        )
        self.write(
            "contracts/engine.fixtures.json",
            json.dumps({"inputs": [DECISION_A]}),
        )
        self.commit()

        self.assertEqual(self.move(), [])

        py_body = (self.root / "scripts/skill-map.py").read_text(encoding="utf-8")
        self.assertIn(f"'intake': '{NEW_DECISION_A}'", py_body)
        self.assertIn(f"'law': '{NEW_CONSTITUTION}'", py_body)
        self.assertIn(
            f"const documentPath = \"{NEW_DECISION_B}\"",
            (self.root / "internal/store/recipe_test.go").read_text(encoding="utf-8"),
        )
        self.assertEqual(
            json.loads((self.root / "contracts/engine.fixtures.json").read_text(encoding="utf-8")),
            {"inputs": [NEW_DECISION_A]},
        )

    def test_source_admission_rules_and_legacy_fixtures_stay(self) -> None:
        admission = (
            "RECORD_PATH = r\"^.concord/docs/(?!work/).*\\.md$\"\n"
            "DECISIONS_DIR = \".concord/docs/decisions\"\n"
        )
        self.write("scripts/check-admission.py", admission)
        # A frozen-skip member whose synthetic fixture reuses a real law path.
        legacy = f"seed(\"{DECISION_A}\")\n"
        self.write("scripts/test-renumber-cd.py", legacy)
        self.write("scripts/check-agent-contracts.py", legacy)
        self.commit()

        self.assertEqual(self.move(), [])

        self.assertEqual(
            (self.root / "scripts/check-admission.py").read_text(encoding="utf-8"), admission
        )
        self.assertEqual(
            (self.root / "scripts/test-renumber-cd.py").read_text(encoding="utf-8"), legacy
        )
        self.assertEqual(
            (self.root / "scripts/check-agent-contracts.py").read_text(encoding="utf-8"), legacy
        )
        findings, prepared = relocate.plan(self.root)
        self.assertEqual(findings, [])
        assert prepared is not None
        self.assertEqual(relocate.survivors(self.root, prepared), [])

    def test_source_relative_prefix_literal_rewrites_when_it_resolves(self) -> None:
        self.write(
            "tools/gen/emit.ts",
            f"const lawLink = \"../../{CONSTITUTION}\";\n"
            f"const ambiguous = \"../{CONSTITUTION}\";\n",
        )
        self.commit()

        self.assertEqual(self.move(), [])

        body = (self.root / "tools/gen/emit.ts").read_text(encoding="utf-8")
        # The prefix climbs to the repository root, so the target is exact.
        self.assertIn(f"\"../../{NEW_CONSTITUTION}\"", body)
        # One climb short of the root resolves to a non-repository path; an
        # ambiguous link stays.
        self.assertIn(f"\"../{CONSTITUTION}\"", body)

    def test_source_urls_and_lookalikes_stay(self) -> None:
        pinned = f"https://example.invalid/tree/abc/{DECISION_A}"
        self.write(
            "scripts/notes.py",
            f"URL = \"{pinned}\"\n"
            f"HOME = \"~/{CONSTITUTION}\"\n"
            f"SIB = \"other{CONSTITUTION}\"\n"
            f"BAK = \"{DECISION_B}.bak\"\n",
        )
        self.commit()

        self.assertEqual(self.move(), [])

        body = (self.root / "scripts/notes.py").read_text(encoding="utf-8")
        for kept in (pinned, f"~/{CONSTITUTION}", f"other{CONSTITUTION}", f"{DECISION_B}.bak"):
            self.assertIn(kept, body)
        self.assertNotIn(NEW_DECISION_A, body)

    def test_scenario_directory_literal_stays(self) -> None:
        self.write(
            ".concord/scenarios/scenario.v1.json",
            json.dumps({"evidence": [".concord/docs/decisions/"]}) + "\n",
        )
        self.commit()

        self.assertEqual(self.move(), [])

        self.assertIn(
            ".concord/docs/decisions/",
            (self.root / ".concord/scenarios/scenario.v1.json").read_text(encoding="utf-8"),
        )

    def test_shard_prose_is_not_literal_rewritten(self) -> None:
        record = self.shard("lesson-one")
        record["summary"] = f"Learned via {DECISION_A} before the move."
        self.write_shard("lesson-one", record)
        self.commit()

        self.assertEqual(self.move(), [])

        self.assertEqual(
            self.shard("lesson-one")["summary"], f"Learned via {DECISION_A} before the move."
        )

    def test_unrewritten_source_literal_is_reported_as_a_survivor(self) -> None:
        self.write("scripts/mapped.py", f"LAW = \"{DECISION_A}\"\n")
        findings, prepared = relocate.plan(self.root)
        assert prepared is not None

        # Simulate any rewrite escape: the plan's write for one source file
        # never lands. The survivor proof must report the file, not stay
        # silent.
        del prepared.revisions[Path("scripts/mapped.py")]
        self.assertEqual(relocate.apply(self.root, prepared), ["scripts/mapped.py"])

    def test_refuses_outside_a_git_work_tree(self) -> None:
        with tempfile.TemporaryDirectory() as plain_name:
            plain = Path(plain_name)
            shard_dir = plain / ".concord/docs/knowledge/records"
            shard_dir.mkdir(parents=True)
            (plain / ".concord/docs/decisions").mkdir(parents=True)
            (plain / ".concord/docs/decisions/CD-9001-x.md").write_text("# x\n", encoding="utf-8")
            shard_dir.joinpath("CD-9001.json").write_bytes(
                relocate.shard_format.canonical_bytes({
                    "id": "CD-9001",
                    "kind": "decision",
                    "path": ".concord/docs/decisions/CD-9001-x.md",
                    "sha256": "sha256:" + hashlib.sha256(b"# x\n").hexdigest(),
                    "status": "accepted",
                })
            )

            findings, prepared = relocate.plan(plain)

            self.assertIsNone(prepared)
            self.assertTrue(any("git work tree" in finding for finding in findings))

    # -- idempotence -------------------------------------------------------

    def test_second_run_is_a_byte_for_byte_no_op(self) -> None:
        self.write(
            "scripts/skill-map.py",
            f"SKILL_REFERENCES = {{'law': '{CONSTITUTION}'}}\n",
        )
        self.commit()
        self.assertEqual(self.move(), [])
        after_first = self.tree_bytes()

        findings, prepared = relocate.plan(self.root)

        self.assertEqual(findings, [])
        assert prepared is not None
        self.assertEqual(prepared.moves, [])
        self.assertEqual(prepared.revisions, {})
        self.assertEqual(relocate.apply(self.root, prepared), [])
        self.assertEqual(self.tree_bytes(), after_first)

    # -- refusals ----------------------------------------------------------

    def test_refuses_a_dangling_law_record_path(self) -> None:
        record = self.shard("CD-0001")
        record["path"] = ".concord/docs/decisions/CD-0001-gone.md"
        self.write_shard("CD-0001", record)
        self.commit()

        findings, prepared = relocate.plan(self.root)

        self.assertIsNone(prepared)
        self.assertTrue(any("dangling" in finding for finding in findings))

    def test_refuses_a_target_basename_collision(self) -> None:
        # Two accepted decisions in different source directories whose basenames
        # agree would both claim .concord/law/decisions/priorities.md.
        self.seed_document(".concord/docs/decisions/priorities.md", "# other\n")
        self.seed_record("CD-0003", "decision", ".concord/docs/decisions/priorities.md")
        self.seed_document(".concord/docs/priorities.md", "# loose\n")
        self.seed_record("CD-0004", "decision", ".concord/docs/priorities.md")
        self.commit()

        findings, prepared = relocate.plan(self.root)

        self.assertIsNone(prepared)
        self.assertTrue(any("basename collision" in finding for finding in findings))

    def test_refuses_a_law_record_outside_concord_docs(self) -> None:
        # A law record outside .concord/docs/ is operator-override territory;
        # relocating it would silently override the recorded placement decision.
        self.seed_document("external/notes.md", "# external\n")
        self.seed_record("EXT-1", "constitution", "external/notes.md")
        self.commit()

        findings, prepared = relocate.plan(self.root)

        self.assertIsNone(prepared)
        self.assertTrue(any("outside .concord/docs/" in finding for finding in findings))

    def test_refuses_when_a_target_file_already_exists(self) -> None:
        self.seed_document(NEW_DECISION_A, "# occupied\n")
        self.commit()

        findings, prepared = relocate.plan(self.root)

        self.assertIsNone(prepared)
        self.assertTrue(any("already exists" in finding for finding in findings))

    def test_refuses_an_unreadable_shard(self) -> None:
        self.write(".concord/docs/knowledge/records/CD-0001.json", "{not json\n")
        self.commit()

        findings, prepared = relocate.plan(self.root)

        self.assertIsNone(prepared)
        self.assertTrue(any("invalid JSON" in finding for finding in findings))

    def test_refuses_when_the_records_directory_is_missing(self) -> None:
        for shard in (self.root / ".concord/docs/knowledge/records").glob("*.json"):
            shard.unlink()
        self.commit()

        findings, prepared = relocate.plan(self.root)

        self.assertIsNone(prepared)
        self.assertTrue(any("records" in finding for finding in findings))

    # -- interface ---------------------------------------------------------

    def test_dry_run_writes_nothing(self) -> None:
        before = self.tree_bytes()

        result = subprocess.run(
            [sys.executable, str(SCRIPT), "--root", str(self.root), "--dry-run"],
            cwd=self.root,
            capture_output=True,
            text=True,
        )

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(NEW_DECISION_A, result.stdout)
        self.assertEqual(self.tree_bytes(), before)

    def test_generated_markdown_is_skipped_and_not_reported(self) -> None:
        # The lane-contract document is generator-owned: the mover must skip it
        # for both rewriting and survivor reporting, because its generator
        # carries the rename forward.
        self.write(
            ".concord/docs/generated-contract.md",
            f"<!-- Code generated by scripts/generate-demo.py; DO NOT EDIT. -->\n\nSee {DECISION_A}\n",
        )
        self.commit()
        before = (self.root / ".concord/docs/generated-contract.md").read_text()

        self.assertEqual(self.move(), [])

        self.assertEqual(
            (self.root / ".concord/docs/generated-contract.md").read_text(), before
        )
        findings, prepared = relocate.plan(self.root)
        self.assertEqual(findings, [])
        assert prepared is not None
        self.assertEqual(relocate.survivors(self.root, prepared), [])


if __name__ == "__main__":
    unittest.main()
