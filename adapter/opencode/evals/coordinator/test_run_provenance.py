"""Run artifact provenance tests with the model process stubbed, not behavioral evidence."""
import argparse
import hashlib
import json
import shutil
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import run

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


if __name__ == "__main__":
    unittest.main()
