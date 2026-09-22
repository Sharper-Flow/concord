#!/usr/bin/env python3
"""Focused tests for the worker-scope contract validation."""

from __future__ import annotations

import contextlib
import importlib.util
import io
import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = Path(__file__).with_name("check-worker-scope-contract.py")
SPEC = importlib.util.spec_from_file_location("check_worker_scope_contract", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


def lane(lane_id: str, obligations: list[str]) -> dict:
    return {"id": lane_id, "evidence_obligations": obligations}


def lanes_registry() -> dict:
    return {
        "lanes": [
            lane("research", ["bounded_findings", "source_citations", "uncertainties"]),
            lane("implement", ["files_touched", "verification_commands", "unresolved_issues"]),
            lane("design", ["files_touched", "verification_commands", "visual_artifacts", "unresolved_issues"]),
            lane("review", ["contract_findings", "severity", "verification_commands"]),
            lane("verify", ["commands", "exit_codes", "failure_classification"]),
        ]
    }


def contract_schema() -> dict:
    return json.loads((ROOT / "contracts/worker-scope.schema.json").read_text(encoding="utf-8"))


def lane_schema() -> dict:
    return json.loads((ROOT / "contracts/agent-lanes.schema.json").read_text(encoding="utf-8"))


def assignments() -> list[dict]:
    return [
        {"lane_id": "research", "result": "bounded_findings"},
        {"lane_id": "implement", "result": "files_touched"},
        {"lane_id": "design", "result": "visual_artifacts"},
        {"lane_id": "review", "result": "contract_findings"},
        {"lane_id": "verify", "result": "exit_codes"},
    ]


def contract(**overrides) -> dict:
    value: dict = {
        "schema_version": "1.0",
        "registry": "worker_scope",
        "version": 1,
        "summary": "Every lane carries one assigned result.",
        "premise": {
            "single_assigned_result": "Each worker attempt completes only its assigned result.",
            "parent_explicit_sequential": "The parent workflow keeps other required results explicit and can dispatch sequential bounded attempts.",
        },
        "assignments": assignments(),
    }
    value.update(overrides)
    return value


def kinds(findings: list[str]) -> list[str]:
    return [finding.split(":", 1)[0] for finding in findings]


class WorkerScopeContractValidationTests(unittest.TestCase):
    def test_valid_contract_yields_no_findings(self) -> None:
        self.assertEqual(checker.validate(contract(), contract_schema(), lanes_registry(), lane_schema()), [])

    def test_design_lane_scoped_to_visual_artifacts(self) -> None:
        value = contract()
        value["assignments"] = [entry for entry in assignments() if entry["lane_id"] != "design"]
        value["assignments"].append({"lane_id": "design", "result": "files_touched"})

        findings = checker.validate(value, contract_schema(), lanes_registry(), lane_schema())

        self.assertEqual(kinds(findings), ["design-scope"])

    def test_missing_lane_assignment_is_a_finding(self) -> None:
        value = contract()
        value["assignments"] = [entry for entry in assignments() if entry["lane_id"] != "review"]

        findings = checker.validate(value, contract_schema(), lanes_registry(), lane_schema())

        self.assertEqual(kinds(findings), ["assignment-missing-lane"])

    def test_unknown_lane_is_a_finding(self) -> None:
        value = contract()
        value["assignments"].append({"lane_id": "ghost", "result": "exit_codes"})

        findings = checker.validate(value, contract_schema(), lanes_registry(), lane_schema())

        self.assertEqual(kinds(findings), ["assignment-unknown-lane"])

    def test_result_outside_lane_obligations_is_a_finding(self) -> None:
        value = contract()
        value["assignments"] = [entry for entry in assignments() if entry["lane_id"] != "verify"]
        value["assignments"].append({"lane_id": "verify", "result": "visual_artifacts"})

        findings = checker.validate(value, contract_schema(), lanes_registry(), lane_schema())

        self.assertEqual(kinds(findings), ["assignment-undeclared-obligation"])

    def test_duplicate_lane_is_a_finding(self) -> None:
        value = contract()
        value["assignments"].append({"lane_id": "design", "result": "visual_artifacts"})

        findings = checker.validate(value, contract_schema(), lanes_registry(), lane_schema())

        self.assertEqual(kinds(findings), ["assignment-duplicate-lane"])

    def test_premise_drift_is_a_finding(self) -> None:
        value = contract()
        value["premise"]["single_assigned_result"] = "Each attempt does everything."

        findings = checker.validate(value, contract_schema(), lanes_registry(), lane_schema())

        self.assertEqual(kinds(findings), ["premise-clause"])

    def test_result_enum_drift_from_lane_vocabulary_is_a_finding(self) -> None:
        schema = contract_schema()
        schema["$defs"]["result_obligation"]["enum"] = ["visual_artifacts"]

        findings = checker.validate(contract(), schema, lanes_registry(), lane_schema())

        self.assertEqual(kinds(findings)[0], "vocabulary-drift")
        self.assertEqual(set(kinds(findings)[1:]), {"assignment-unknown-result"})

    def test_main_reports_and_exits_against_a_temp_root(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "contracts").mkdir()
            sources = {
                "contracts/worker-scope.v1.json": contract(),
                "contracts/worker-scope.schema.json": contract_schema(),
                "contracts/agent-lanes.v1.json": lanes_registry(),
                "contracts/agent-lanes.schema.json": lane_schema(),
            }
            for name, value in sources.items():
                (root / name).write_text(json.dumps(value), encoding="utf-8")
            self.assertEqual(checker.run(root), [])

            broken = contract()
            broken["assignments"] = [entry for entry in assignments() if entry["lane_id"] != "design"]
            (root / "contracts/worker-scope.v1.json").write_text(json.dumps(broken), encoding="utf-8")
            findings = checker.run(root)
            self.assertEqual(kinds(findings), ["assignment-missing-lane"])

            with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()), patch.object(
                sys, "argv", [str(SCRIPT), "--root", str(root)]
            ):
                self.assertEqual(checker.main(), 1)


if __name__ == "__main__":
    unittest.main()
