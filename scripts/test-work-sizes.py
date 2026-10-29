#!/usr/bin/env python3
"""Negative tests for the work-size closure checker."""
from __future__ import annotations

import importlib.util
import json
import shutil
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT = Path(__file__).with_name("check-work-sizes.py")
SPEC = importlib.util.spec_from_file_location("work_sizes_checker", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)

STORED_CHECK = "CHECK(size_class IN ('small','larger','crucial'))"


class WorkSizesFixture(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory(prefix="work-sizes-")
        self.root = Path(self.tempdir.name)
        for relative in (
            "contracts/work-sizes.v1.json",
            "contracts/work-sizes.schema.json",
            "contracts/work-sizes.digest",
            "internal/store/generated_work_sizes.go",
            "internal/store/work_size_model.go",
            "internal/store/schema.go",
            "scripts/generate-work-sizes.py",
            "scripts/vocabulary_utils.py",
            "contracts/agent-tool-surface-payloads.schema.json",
            "contracts/host-tool-surface.v1.json",
        ):
            destination = self.root / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(REPO_ROOT / relative, destination)

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def findings(self) -> list[str]:
        return checker.check(self.root)

    def edit(self, relative: str, old: str, new: str) -> None:
        path = self.root / relative
        text = path.read_text(encoding="utf-8")
        self.assertIn(old, text)
        path.write_text(text.replace(old, new), encoding="utf-8")

    def test_clean_fixture_passes(self) -> None:
        self.assertEqual(self.findings(), [])

    def test_check_order_is_not_closure(self) -> None:
        self.edit("internal/store/schema.go", STORED_CHECK, "CHECK(size_class IN ('crucial', 'small', 'larger'))")
        self.assertEqual(self.findings(), [])

    def test_stored_check_missing_size_is_reported(self) -> None:
        self.edit("internal/store/schema.go", STORED_CHECK, "CHECK(size_class IN ('small','larger'))")
        self.assertTrue(any(item.startswith("migration-check-closure:") for item in self.findings()))

    def test_stored_check_extra_size_is_reported(self) -> None:
        self.edit("internal/store/schema.go", STORED_CHECK, "CHECK(size_class IN ('small','larger','crucial','huge'))")
        self.assertTrue(any(item.startswith("migration-check-closure:") for item in self.findings()))

    def test_open_column_is_reported(self) -> None:
        self.edit("internal/store/schema.go", "size_class TEXT NOT NULL " + STORED_CHECK, "size_class TEXT NOT NULL")
        self.assertTrue(any(item.startswith("migration-check-coverage:") for item in self.findings()))

    def test_contract_size_without_stored_value_is_reported(self) -> None:
        path = self.root / "contracts/work-sizes.v1.json"
        vocabulary = json.loads(path.read_text(encoding="utf-8"))
        vocabulary["sizes"].append({"size": "huge", "summary": "A size the store cannot hold."})
        path.write_text(json.dumps(vocabulary, indent=2) + "\n", encoding="utf-8")
        findings = self.findings()
        self.assertTrue(any(item.startswith("migration-check-closure:") for item in findings))
        self.assertTrue(any(item.startswith("generator-freshness:") for item in findings))

    def test_generator_drift_is_reported(self) -> None:
        self.edit("internal/store/generated_work_sizes.go", 'WorkSizeCrucial WorkSize = "crucial"', 'WorkSizeCrucial WorkSize = "critical"')
        self.assertTrue(any(item.startswith("generator-freshness:") for item in self.findings()))

    def test_hand_written_constant_is_reported(self) -> None:
        path = self.root / "internal/store/work_size_model.go"
        path.write_text(path.read_text(encoding="utf-8") + '\nconst workSizeHuge WorkSize = "huge"\n', encoding="utf-8")
        self.assertTrue(any(item.startswith("go-closure:") for item in self.findings()))

    def edit_json(self, relative: str, change) -> None:
        path = self.root / relative
        document = json.loads(path.read_text(encoding="utf-8"))
        change(document)
        path.write_text(json.dumps(document, indent=2) + "\n", encoding="utf-8")

    def test_schema_size_enum_drift_is_reported(self) -> None:
        def drop(document) -> None:
            document["$defs"]["work_size_summary"]["properties"]["size"]["enum"].remove("crucial")
        self.edit_json("contracts/agent-tool-surface-payloads.schema.json", drop)
        self.assertTrue(any(item.startswith("schema-closure: work_size_summary.size") for item in self.findings()))

    def test_payload_size_inputs_proposed_size_is_reported(self) -> None:
        def propose(document) -> None:
            document["$defs"]["work_size_inputs"]["properties"]["size"] = {"type": "string"}
        self.edit_json("contracts/agent-tool-surface-payloads.schema.json", propose)
        self.assertTrue(any("declares a proposed size field" in item for item in self.findings()))

    def test_host_size_inputs_proposed_size_is_reported(self) -> None:
        def propose(document) -> None:
            tool = next(tool for tool in document["tools"] if tool["name"] == "concord_work_start")
            tool["args"]["oneOf"][0]["properties"]["size_inputs"]["properties"]["model_size"] = {"type": "string"}
        self.edit_json("contracts/host-tool-surface.v1.json", propose)
        self.assertTrue(any("host-tool-surface" in item and "proposed size field" in item for item in self.findings()))

    def test_open_size_inputs_is_reported(self) -> None:
        def open_object(document) -> None:
            document["$defs"]["work_size_inputs"]["additionalProperties"] = True
        self.edit_json("contracts/agent-tool-surface-payloads.schema.json", open_object)
        self.assertTrue(any("is not closed" in item for item in self.findings()))

    def test_size_class_enum_drift_is_reported(self) -> None:
        def drop(document) -> None:
            document["$defs"]["work_size_class"]["enum"].remove("crucial")
        self.edit_json("contracts/agent-tool-surface-payloads.schema.json", drop)
        self.assertTrue(any(item.startswith("schema-closure: work_size_class") for item in self.findings()))

    def test_work_summary_size_must_reference_the_summary(self) -> None:
        def drift(document) -> None:
            document["$defs"]["work_summary"]["properties"]["size"] = {"type": "string"}
        self.edit_json("contracts/agent-tool-surface-payloads.schema.json", drift)
        self.assertTrue(any("work_summary.size" in item for item in self.findings()))

    def test_model_size_must_reference_the_size_class(self) -> None:
        def drift(document) -> None:
            document["$defs"]["work_size_summary"]["properties"]["model_size"] = {"type": "string"}
        self.edit_json("contracts/agent-tool-surface-payloads.schema.json", drift)
        self.assertTrue(any("work_size_summary.model_size" in item for item in self.findings()))

    def test_capture_size_inputs_off_the_closed_object_is_reported(self) -> None:
        def inline(document) -> None:
            document["$defs"]["work_define_capture_input"]["properties"]["size_inputs"] = {"type": "object"}
        self.edit_json("contracts/agent-tool-surface-payloads.schema.json", inline)
        self.assertTrue(any("work_define_capture_input.size_inputs" in item for item in self.findings()))

    def test_revise_size_inputs_off_the_closed_object_is_reported(self) -> None:
        def inline(document) -> None:
            document["$defs"]["work_define_revise_input"]["properties"]["size_inputs"] = {"type": "object"}
        self.edit_json("contracts/agent-tool-surface-payloads.schema.json", inline)
        self.assertTrue(any("work_define_revise_input.size_inputs" in item for item in self.findings()))


if __name__ == "__main__":
    unittest.main()
