"""Run artifact provenance tests with the model process stubbed, not behavioral evidence."""
import argparse
import copy
import hashlib
import json
import shutil
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import run
from evaluation import typed_boundary
from scenarios import SCENARIOS, START

REPO = Path(__file__).resolve().parents[4]


def sha(data):
    return hashlib.sha256(data).hexdigest()


class RunProvenanceTests(unittest.TestCase):
    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="coordinator-repo-"))
        self.artifacts = Path(tempfile.mkdtemp(prefix="coordinator-artifacts-"))
        for relative in run.PRODUCTION_SOURCES:
            target = self.repo / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(REPO / relative, target)

    def tearDown(self):
        shutil.rmtree(self.repo, ignore_errors=True)
        shutil.rmtree(self.artifacts, ignore_errors=True)

    def args(self):
        return argparse.Namespace(repo=self.repo, sdk_tool=Path("/synthetic/sdk/tool.js"),
                                  model="synthetic/model", artifacts_dir=self.artifacts)

    def run_case(self, during=None):
        def stub(command, root, stem, timeout):
            (root / f"{stem}.jsonl").write_text("")
            if during is not None:
                during()
            return 0
        with mock.patch.object(run, "command_to_files", side_effect=stub):
            return run.run_case(self.args(), "default-checkout-resume", "coordinator body", {"rules.md": b"rule"})

    def test_result_records_production_and_tool_source_hashes(self):
        result = self.run_case()
        root = Path(result["artifact_dir"])
        self.assertTrue(result["snapshots_unchanged"])
        self.assertEqual(set(result["production_source_sha256"]), set(run.PRODUCTION_SOURCES))
        for relative, value in result["production_source_sha256"].items():
            self.assertEqual(value, sha((self.repo / relative).read_bytes()))
            snapshot = root / "production-sources" / relative.replace("/", "__")
            self.assertEqual(sha(snapshot.read_bytes()), value)
        self.assertEqual(set(result["tool_source_sha256"]), {
            ".opencode/tools/concord.ts", ".opencode/recording-tool.ts", ".opencode/tools/runtime_status.ts"})
        for relative, value in result["tool_source_sha256"].items():
            self.assertEqual(sha((root / relative).read_bytes()), value)
        self.assertEqual(json.loads((root / "result.json").read_text())["production_source_sha256"],
                         result["production_source_sha256"])

    def test_conduct_corpus_resolves_in_the_repository(self):
        corpus = [path.name for path in (REPO / run.CONDUCT_CORPUS).glob("*.md") if path.name != "README.md"]
        self.assertIn("evidence.md", corpus)

    def test_production_source_change_during_a_run_fails_the_snapshot_check(self):
        target = self.repo / "adapter/opencode/move-notice.ts"
        result = self.run_case(during=lambda: target.write_text(target.read_text() + "\n// changed\n"))
        self.assertFalse(result["snapshots_unchanged"])
        self.assertFalse(result["passed"])

    def test_tool_source_change_during_a_run_fails_the_snapshot_check(self):
        def change():
            [root] = list(self.artifacts.iterdir())
            tool = root / ".opencode/tools/concord.ts"
            tool.write_text(tool.read_text() + "\n// changed\n")
        result = self.run_case(during=change)
        self.assertFalse(result["snapshots_unchanged"])


# The boundary sources in the precedence evaluation.typed_start_boundary
# applies: the typed boundary field on the error, the core diagnostic
# operation carried in the refusal message, the error kind, then the
# stopping tool name.
BOUNDARY_SOURCE_MARKERS = (
    "boundary field",
    "core diagnostic operation",
    "error's kind",
    "the name of the tool",
)


def format_boundary_clause():
    """The boundary clause of run.FORMAT, whitespace-normalized."""
    return " ".join(run.FORMAT.split("boundary:", 1)[1].split("cause:", 1)[0].split())


def core_diagnostic_operation(message):
    """The operation the complete refusal diagnostic names: the adapter's
    `concord work-resume: store: <operation>: <kind>: <detail>` prefix, not a
    boundary-shaped word anywhere in the detail."""
    if not isinstance(message, str):
        return None
    parts = message.split(": ", 4)
    if (len(parts) == 5 and parts[:2] == ["concord work-resume", "store"]
            and parts[2].isidentifier() and parts[3].isidentifier() and parts[4]):
        return parts[2]
    return None


def served_error(result):
    error = result.get("error") if isinstance(result, dict) else None
    return error if isinstance(error, dict) else None


def named_string(error, key):
    value = error.get(key)
    return value if isinstance(value, str) and value else None


def instruction_boundary(case):
    """The boundary the FORMAT boundary instruction tells the coordinator to
    name for a served stopping observation, applying the sources in the
    order the clause states them."""
    clause = format_boundary_clause()
    transition = case.get("transition")
    if isinstance(transition, dict):
        notice = transition.get("notice")
        if (isinstance(notice, str) and "A turn-move boundary is active" in notice
                and "turn_move_boundary when an active turn-move boundary" in clause):
            return "turn_move_boundary"
        error = served_error(transition.get("result"))
    else:
        start = case.get("start")
        error = served_error(start.get("result")) if isinstance(start, dict) else None
    if error is None:
        return None  # a successful task names no boundary
    extractors = {
        "boundary field": lambda error: named_string(error, "boundary"),
        "core diagnostic operation": lambda error: core_diagnostic_operation(error.get("message")),
        "error's kind": lambda error: named_string(error, "kind"),
        "the name of the tool": lambda error: START,
    }
    stated = sorted((marker for marker in BOUNDARY_SOURCE_MARKERS if marker in clause), key=clause.find)
    for marker in stated:
        value = extractors[marker](error)
        if value is not None:
            return value
    return None


class FormatBoundaryInstructionTests(unittest.TestCase):
    """The served FORMAT boundary instruction must state the evaluator's
    precedence. The evaluator measures the boundary the served typed
    observation names, so an instruction that sends the model elsewhere is a
    defect in the instruction, not in the model."""

    def test_boundary_clause_states_the_evaluator_precedence(self):
        clause = format_boundary_clause()
        positions = {marker: clause.find(marker) for marker in BOUNDARY_SOURCE_MARKERS}
        missing = [marker for marker, position in positions.items() if position < 0]
        self.assertEqual(missing, [])
        ordered = [positions[marker] for marker in BOUNDARY_SOURCE_MARKERS]
        self.assertEqual(ordered, sorted(ordered),
                         f"boundary clause states sources out of precedence order: {positions}")
        self.assertIn("turn_move_boundary when an active turn-move boundary", clause)
        self.assertIn("null when the task succeeded", clause)

    def test_boundary_clause_agrees_with_the_evaluator_on_served_observations(self):
        # Derived from the typed observations each scenario serves, never
        # from a model output: both sides read the same served error or
        # move notice and must name the same boundary.
        for name, case in SCENARIOS.items():
            if case.get("capture") or ("start" not in case and "transition" not in case):
                continue
            with self.subTest(name=name):
                self.assertEqual(instruction_boundary(case), typed_boundary(case))

    def test_served_refusal_without_typed_names_agrees_on_the_tool_name(self):
        # The recording double's own refusal constant carries no boundary
        # field, no complete core diagnostic, and no kind; instruction and
        # evaluator then both fall back to the stopping tool name.
        case = copy.deepcopy(SCENARIOS["genuine-refusal-no-fallback"])
        case["start"]["result"] = {"outcome": "error", "error": {
            "reason_code": "authorization_denied", "effect_state": "none",
            "message": "Outside the fixture grant."}}
        self.assertEqual(instruction_boundary(case), START)
        self.assertEqual(typed_boundary(case), START)


if __name__ == "__main__":
    unittest.main()
