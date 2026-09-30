#!/usr/bin/env python3
"""Focused tests for the knowledge-placement checker.

The validator runs against the real repository under ROOT. To exercise its
behavior without perturbing the live tree, each test builds a sandbox with
`.concord/` homes and a manifest document under tempfile.TemporaryDirectory(),
patches `ROOT` onto the sandbox, and replaces `load_manifest` so each test
shapes the head fields the placement rules read.
"""
from __future__ import annotations

import importlib.util
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).with_name("check-knowledge-placement.py")
REPO_ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("knowledge_placement", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


def build_sandbox() -> Path:
    """Build a sandbox with the three .concord homes and no retired trees."""
    root = Path(tempfile.mkdtemp(prefix="kp-"))
    for home in checker.CONCORD_HOMES:
        (root / home).mkdir(parents=True)
    return root


def manifest_document(**overrides: object) -> dict:
    document: dict = {
        "schema_version": "1.3",
        "supported_kinds": ["decision"],
        "indexed_kinds": ["decision"],
        "knowledge_roots": [".concord/docs/"],
        "exclusions": [],
        "records": [
            {
                "id": "CD-0001",
                "kind": "decision",
                "path": ".concord/docs/decisions/CD-0001-example.md",
                "status": "accepted",
                "date": "2026-09-29T00:00:00Z",
                "title": "Example",
                "summary": "example record for the placement sandbox",
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
    document.update(overrides)
    return document


def run_with_sandbox(root: Path, document: dict) -> tuple[int, str, str]:
    import contextlib
    import io

    stdout, stderr = io.StringIO(), io.StringIO()
    with mock.patch.object(checker, "ROOT", root), mock.patch.object(
        checker, "load_manifest", return_value=document
    ), contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
        code = checker.main([])
    return code, stdout.getvalue(), stderr.getvalue()


class DefaultPlacementTest(unittest.TestCase):
    def test_a_compliant_repository_passes(self) -> None:
        root = build_sandbox()
        (root / ".concord/docs/decisions").mkdir(parents=True)
        (root / ".concord/docs/decisions/CD-0001-example.md").write_text("law\n", encoding="utf-8")
        code, out, err = run_with_sandbox(root, manifest_document())
        self.assertEqual(code, 0, out + err)
        self.assertIn("knowledge placement check passed", out)

    def test_a_knowledge_root_outside_the_default_tree_refuses(self) -> None:
        root = build_sandbox()
        code, out, _ = run_with_sandbox(root, manifest_document(knowledge_roots=["external/knowledge/"]))
        self.assertEqual(code, 1)
        self.assertIn("knowledge root outside the default tree without an operator override", out)

    def test_an_unapproved_external_record_refuses(self) -> None:
        root = build_sandbox()
        document = manifest_document()
        document["records"][0]["path"] = "docs/decisions/CD-0001-example.md"
        code, out, _ = run_with_sandbox(root, document)
        self.assertEqual(code, 1)
        self.assertIn("unapproved external knowledge placement", out)


class RetiredRootTreeTest(unittest.TestCase):
    def test_a_retired_root_directory_refuses(self) -> None:
        root = build_sandbox()
        (root / "docs").mkdir()
        code, out, _ = run_with_sandbox(root, manifest_document())
        self.assertEqual(code, 1)
        self.assertIn("retired root tree is present: docs", out)

    def test_a_compatibility_symlink_refuses(self) -> None:
        root = build_sandbox()
        (root / "instructions").symlink_to(".concord/instructions", target_is_directory=True)
        code, out, _ = run_with_sandbox(root, manifest_document())
        self.assertEqual(code, 1)
        self.assertIn("retired root tree is present: instructions", out)

    def test_a_symlinked_concord_home_refuses(self) -> None:
        root = Path(tempfile.mkdtemp(prefix="kp-"))
        real = root / "elsewhere"
        real.mkdir()
        (real / "docs").mkdir(parents=True)
        (root / ".concord").mkdir()
        (root / ".concord/docs").symlink_to("../elsewhere/docs", target_is_directory=True)
        (root / ".concord/instructions").mkdir()
        (root / ".concord/scenarios").mkdir()
        code, out, _ = run_with_sandbox(root, manifest_document())
        self.assertEqual(code, 1)
        self.assertIn("default home is not a real directory: .concord/docs", out)


class OperatorOverrideTest(unittest.TestCase):
    def override(self, **fields: object) -> dict:
        entry: dict = {
            "path": "external/knowledge/",
            "product_id": "example-product",
            "recorded_in": "CD-0001",
            "reason": "the operator recorded this placement for the example Product",
        }
        entry.update(fields)
        return entry

    def instruction_block(
        self,
        path: str = "external/knowledge/",
        product_id: str = "example-product",
        decision: str = "approve",
        extra_lines: tuple[str, ...] = (),
        drop_fields: tuple[str, ...] = (),
    ) -> str:
        lines = ["<!-- concord-operator-override"]
        if "product" not in drop_fields:
            lines.append(f"product: {product_id}")
        if "path" not in drop_fields:
            lines.append(f"path: {path}")
        if "decision" not in drop_fields:
            lines.append(f"decision: {decision}")
        lines.extend(extra_lines)
        lines.append("-->")
        return "\n".join(lines) + "\n"

    def write_anchor(self, root: Path, body: str) -> None:
        target = root / ".concord/docs/decisions/CD-0001-example.md"
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(body, encoding="utf-8")

    def run_override(self, root: Path, *overrides: dict) -> tuple[int, str]:
        root_set = sorted({entry["path"] for entry in overrides})
        code, out, _ = run_with_sandbox(
            root,
            manifest_document(
                knowledge_roots=root_set,
                operator_overrides=list(overrides),
            ),
        )
        return code, out

    def test_a_valid_recorded_override_admits_an_external_root(self) -> None:
        root = build_sandbox()
        (root / "external/knowledge").mkdir(parents=True)
        self.write_anchor(root, self.instruction_block())
        code, out = self.run_override(root, self.override())
        self.assertEqual(code, 0, out)

    def test_an_override_for_a_missing_path_refuses(self) -> None:
        root = build_sandbox()
        code, out = self.run_override(root, self.override())
        self.assertEqual(code, 1)
        self.assertIn("override directory does not exist on disk", out)

    def test_an_override_anchor_that_is_not_a_manifest_record_refuses(self) -> None:
        root = build_sandbox()
        (root / "external/knowledge").mkdir(parents=True)
        code, out = self.run_override(root, self.override(recorded_in="convention"))
        self.assertEqual(code, 1)
        self.assertIn("is not a manifest record", out)

    def test_an_override_inside_the_default_tree_is_not_an_exception(self) -> None:
        root = build_sandbox()
        (root / "external").mkdir()
        code, out, _ = run_with_sandbox(
            root,
            manifest_document(
                knowledge_roots=["external/"],
                operator_overrides=[self.override(path=".concord/docs/")],
            ),
        )
        self.assertEqual(code, 1)
        self.assertIn("knowledge root outside the default tree without an operator override: external/", out)

    def test_a_work_item_identifier_is_never_an_override_anchor(self) -> None:
        # A work item lives in the external store, so a work-shaped anchor is
        # unverifiable from the repository. A well-formed but fabricated
        # identifier must refuse, not pass on its syntax.
        root = build_sandbox()
        (root / "external/knowledge").mkdir(parents=True)
        code, out = self.run_override(
            root, self.override(recorded_in="work-" + "a" * 16)
        )
        self.assertEqual(code, 1)
        self.assertIn("is not a manifest record", out)

    def test_a_manifest_record_anchor_admits_an_external_root(self) -> None:
        root = build_sandbox()
        (root / "external/knowledge").mkdir(parents=True)
        self.write_anchor(root, self.instruction_block())
        code, out = self.run_override(root, self.override(recorded_in="CD-0001"))
        self.assertEqual(code, 0, out)

    def test_an_instruction_for_another_path_refuses(self) -> None:
        # The block must pair the exact Product with the exact path. An
        # instruction that names the Product for a different location never
        # admits this one.
        root = build_sandbox()
        (root / "external/knowledge").mkdir(parents=True)
        self.write_anchor(root, self.instruction_block(path="elsewhere/"))
        code, out = self.run_override(root, self.override())
        self.assertEqual(code, 1)
        self.assertIn("carries no operator instruction for Product", out)
        self.assertIn("names the Product or the path but never both", out)

    def test_an_instruction_for_another_product_refuses(self) -> None:
        root = build_sandbox()
        (root / "external/knowledge").mkdir(parents=True)
        self.write_anchor(root, self.instruction_block(product_id="other-product"))
        code, out = self.run_override(root, self.override())
        self.assertEqual(code, 1)
        self.assertIn("carries no operator instruction for Product", out)
        self.assertIn("names the Product or the path but never both", out)

    def test_prose_denial_is_never_an_instruction(self) -> None:
        # A sentence that names the path, the Product, and the words of an
        # override, and then denies it, refuses. Prose is not an instruction;
        # only the closed block admits a placement.
        root = build_sandbox()
        (root / "external/knowledge").mkdir(parents=True)
        self.write_anchor(
            root,
            "The operator override for example-product does NOT admit "
            "external/knowledge/. The operator denied this placement.\n",
        )
        code, out = self.run_override(root, self.override())
        self.assertEqual(code, 1)
        self.assertIn("carries no operator instruction", out)

    def test_a_deny_instruction_refuses(self) -> None:
        root = build_sandbox()
        (root / "external/knowledge").mkdir(parents=True)
        self.write_anchor(root, self.instruction_block(decision="deny"))
        code, out = self.run_override(root, self.override())
        self.assertEqual(code, 1)
        self.assertIn("records a denial, not an approval", out)

    def test_a_deny_for_one_pair_does_not_admit_another_pair(self) -> None:
        root = build_sandbox()
        (root / "external/knowledge").mkdir(parents=True)
        (root / "external/other").mkdir(parents=True)
        self.write_anchor(
            root,
            self.instruction_block(decision="deny")
            + "\n"
            + self.instruction_block(path="external/other/"),
        )
        code, out = self.run_override(
            root,
            self.override(),
            self.override(
                path="external/other/",
                reason="the operator recorded this placement for the second tree",
            ),
        )
        self.assertEqual(code, 1)
        self.assertIn(
            "records a denial, not an approval, for Product 'example-product' at 'external/knowledge/'",
            out,
        )

    def test_a_superseded_anchor_refuses(self) -> None:
        # A superseded decision carries no authority, so its instruction
        # block never admits a placement.
        root = build_sandbox()
        (root / "external/knowledge").mkdir(parents=True)
        self.write_anchor(root, self.instruction_block())
        document = manifest_document(knowledge_roots=["external/knowledge/"])
        document["records"][0]["status"] = "superseded"
        document["operator_overrides"] = [self.override()]
        code, out, _ = run_with_sandbox(root, document)
        self.assertEqual(code, 1)
        self.assertIn("has status 'superseded'", out)
        self.assertIn("only an accepted decision carries operator override authority", out)

    def test_a_non_decision_anchor_refuses(self) -> None:
        root = build_sandbox()
        (root / "external/knowledge").mkdir(parents=True)
        self.write_anchor(root, self.instruction_block())
        document = manifest_document(knowledge_roots=["external/knowledge/"])
        document["records"][0]["kind"] = "spec"
        document["operator_overrides"] = [self.override()]
        code, out, _ = run_with_sandbox(root, document)
        self.assertEqual(code, 1)
        self.assertIn("is a 'spec' record", out)
        self.assertIn("only a decision carries operator override authority", out)

    def test_an_unknown_instruction_field_refuses(self) -> None:
        root = build_sandbox()
        (root / "external/knowledge").mkdir(parents=True)
        self.write_anchor(
            root,
            self.instruction_block(extra_lines=("note: see the meeting minutes",)),
        )
        code, out = self.run_override(root, self.override())
        self.assertEqual(code, 1)
        self.assertIn("instruction is malformed", out)
        self.assertIn("instruction field must be one of", out)

    def test_a_missing_instruction_field_refuses(self) -> None:
        root = build_sandbox()
        (root / "external/knowledge").mkdir(parents=True)
        self.write_anchor(root, self.instruction_block(drop_fields=("path",)))
        code, out = self.run_override(root, self.override())
        self.assertEqual(code, 1)
        self.assertIn("missing the path field", out)

    def test_a_duplicate_instruction_field_refuses(self) -> None:
        root = build_sandbox()
        (root / "external/knowledge").mkdir(parents=True)
        self.write_anchor(
            root,
            self.instruction_block(extra_lines=("product: example-product",)),
        )
        code, out = self.run_override(root, self.override())
        self.assertEqual(code, 1)
        self.assertIn("duplicate instruction field product", out)

    def test_an_unclosed_instruction_block_refuses(self) -> None:
        root = build_sandbox()
        (root / "external/knowledge").mkdir(parents=True)
        self.write_anchor(root, self.instruction_block().replace("-->", "").rstrip() + "\n")
        code, out = self.run_override(root, self.override())
        self.assertEqual(code, 1)
        self.assertIn("never closed", out)

    def test_an_unknown_decision_value_refuses(self) -> None:
        root = build_sandbox()
        (root / "external/knowledge").mkdir(parents=True)
        self.write_anchor(root, self.instruction_block(decision="approved"))
        code, out = self.run_override(root, self.override())
        self.assertEqual(code, 1)
        self.assertIn("decision must be 'approve' or 'deny'", out)

    def test_duplicate_instruction_blocks_for_one_override_refuse(self) -> None:
        root = build_sandbox()
        (root / "external/knowledge").mkdir(parents=True)
        self.write_anchor(root, self.instruction_block() + "\n" + self.instruction_block())
        code, out = self.run_override(root, self.override())
        self.assertEqual(code, 1)
        self.assertIn("carries 2 operator instruction blocks", out)

    def test_an_unreadable_anchor_document_refuses(self) -> None:
        root = build_sandbox()
        (root / "external/knowledge").mkdir(parents=True)
        code, out = self.run_override(root, self.override())
        self.assertEqual(code, 1)
        self.assertIn("cannot be read", out)


class ExclusionTest(unittest.TestCase):
    def test_an_exclusion_outside_every_root_refuses(self) -> None:
        root = build_sandbox()
        code, out, _ = run_with_sandbox(root, manifest_document(exclusions=["elsewhere/stray.md"]))
        self.assertEqual(code, 1)
        self.assertIn("exclusion outside every knowledge root subtracts nothing", out)

    def test_an_exclusion_under_a_declared_root_passes(self) -> None:
        root = build_sandbox()
        code, out, _ = run_with_sandbox(
            root, manifest_document(exclusions=[".concord/docs/generated-agent-tool-surface.md"])
        )
        self.assertEqual(code, 0, out)


class LiveRepositoryTest(unittest.TestCase):
    def test_this_repository_passes_with_an_empty_override_list(self) -> None:
        import contextlib
        import io

        stdout, stderr = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            code = checker.main([])
        self.assertEqual(code, 0, stdout.getvalue() + stderr.getvalue())
        self.assertIn("knowledge placement check passed", stdout.getvalue())


if __name__ == "__main__":
    unittest.main()
