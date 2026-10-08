"""Dependency-copy cleanup tests with the model process stubbed, not behavioral evidence.

These cover the retention mechanism run.py owns: each evaluation's disposable
`.opencode/node_modules` copy is removed after evidence collection and result
persistence — including failed and timed-out evaluations — while the results,
transcripts, snapshots, lockfiles, and provenance stay, another active run's
artifacts stay untouched, and a removal failure becomes visible beside the
retained evidence instead of being swallowed. No model call happens anywhere
in this file.
"""
import argparse
import hashlib
import json
import os
import shutil
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import run

REPO = Path(__file__).resolve().parents[4]


def sha(data):
    return hashlib.sha256(data).hexdigest()


class RunCleanupTests(unittest.TestCase):
    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="coordinator-repo-"))
        self.artifacts = Path(tempfile.mkdtemp(prefix="coordinator-artifacts-"))
        for relative in run.PRODUCTION_SOURCES:
            target = self.repo / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(REPO / relative, target)
        self.readonly = []

    def tearDown(self):
        # Restore access before the teardown removal: a read-only leaf would
        # otherwise leak the whole artifacts tree past ignore_errors.
        for path in self.readonly:
            try:
                os.chmod(path, 0o755)
            except OSError:
                pass
        shutil.rmtree(self.repo, ignore_errors=True)
        shutil.rmtree(self.artifacts, ignore_errors=True)

    def args(self):
        return argparse.Namespace(repo=self.repo, sdk_tool=Path("/synthetic/sdk/tool.js"),
                                  model="synthetic/model", artifacts_dir=self.artifacts)

    def install_dependency_copy(self, root):
        """The disposable copy the runtime installs under the run's .opencode."""
        package = root / ".opencode" / "node_modules" / "@opencode-ai" / "plugin"
        package.mkdir(parents=True, exist_ok=True)
        (package / "index.js").write_text("// synthetic dependency copy\n")
        (root / ".opencode" / "package.json").write_text('{"dependencies": {"@opencode-ai/plugin": "1.18.35"}}\n')
        (root / ".opencode" / "bun.lock").write_text("synthetic lockfile bytes\n")

    def run_case(self, during=None, exit_code=0):
        def stub(command, root, stem, timeout):
            (root / f"{stem}.jsonl").write_text("")
            self.install_dependency_copy(root)
            if during is not None:
                during(root)
            return exit_code
        with mock.patch.object(run, "command_to_files", side_effect=stub):
            return run.run_case(self.args(), "default-checkout-resume", "coordinator body", {"rules.md": b"rule"})

    def assert_evidence_retained(self, result):
        root = Path(result["artifact_dir"])
        persisted = json.loads((root / "result.json").read_text())
        self.assertEqual(persisted["scenario"], result["scenario"])
        # Transcripts and the case record stay.
        self.assertTrue((root / "events.jsonl").exists())
        self.assertTrue((root / "case.json").exists())
        self.assertTrue((root / "scenario.txt").exists())
        self.assertTrue((root / "opencode.json").exists())
        # Instruction and source snapshots stay, and the recorded evidence
        # hashes still match the retained bytes.
        self.assertTrue((root / "instructions" / "rules.md").exists())
        self.assertEqual(sha((root / "instructions" / "rules.md").read_bytes()),
                         result["snapshot_sha256"]["rules.md"])
        for relative, value in result["tool_source_sha256"].items():
            self.assertEqual(sha((root / relative).read_bytes()), value)
        for filename, value in result["evaluator_sha256"].items():
            self.assertEqual(sha((root / "evaluation-sources" / filename).read_bytes()), value)
        for relative, value in result["production_source_sha256"].items():
            snapshot = root / "production-sources" / relative.replace("/", "__")
            self.assertEqual(sha(snapshot.read_bytes()), value)
        # Lockfiles beside the removed copy stay.
        self.assertTrue((root / ".opencode" / "package.json").exists())
        self.assertTrue((root / ".opencode" / "bun.lock").exists())

    def test_three_consecutive_runs_retain_no_dependency_copy(self):
        for index in range(3):
            with self.subTest(run=index + 1):
                result = self.run_case()
                root = Path(result["artifact_dir"])
                self.assertFalse((root / ".opencode" / "node_modules").exists())
                self.assertNotIn("dependency_cleanup_error", result)
                self.assert_evidence_retained(result)
        retained = list(self.artifacts.glob("coordinator-*/.opencode/node_modules"))
        self.assertEqual(retained, [])

    def test_another_active_run_dependency_copy_stays_unchanged(self):
        foreign = self.artifacts / "coordinator-active-run"
        foreign_module = foreign / ".opencode" / "node_modules" / "@opencode-ai" / "plugin"
        foreign_module.mkdir(parents=True)
        (foreign_module / "index.js").write_text("// another active run's copy\n")
        (foreign / "run.json").write_text('{"active": true}\n')
        before = {path.relative_to(foreign).as_posix(): sha(path.read_bytes())
                  for path in sorted(foreign.rglob("*")) if path.is_file()}
        result = self.run_case()
        self.assertFalse((Path(result["artifact_dir"]) / ".opencode" / "node_modules").exists())
        after = {path.relative_to(foreign).as_posix(): sha(path.read_bytes())
                 for path in sorted(foreign.rglob("*")) if path.is_file()}
        self.assertEqual(after, before)

    def test_timed_out_evaluation_still_removes_its_dependency_copy(self):
        result = self.run_case(exit_code=None)
        root = Path(result["artifact_dir"])
        self.assertTrue(result["timed_out"])
        self.assertFalse(result["passed"])
        self.assertFalse((root / ".opencode" / "node_modules").exists())
        self.assertNotIn("dependency_cleanup_error", result)
        self.assert_evidence_retained(result)

    def test_cleanup_failure_is_visible_and_evidence_is_retained(self):
        def block_removal(root):
            # A read-only leaf package dir fails the removal at its first
            # unlink, before any of the copy's bytes are deleted.
            target = root / ".opencode" / "node_modules" / "@opencode-ai" / "plugin"
            os.chmod(target, 0o500)
            self.readonly.append(target)
        result = self.run_case(during=block_removal)
        root = Path(result["artifact_dir"])
        self.assertIn("dependency_cleanup_error", result)
        self.assertIn("PermissionError", result["dependency_cleanup_error"])
        # The persisted result carries the visible error beside the evidence.
        persisted = json.loads((root / "result.json").read_text())
        self.assertEqual(persisted["dependency_cleanup_error"], result["dependency_cleanup_error"])
        # The unremovable copy and every retained artifact stay in place.
        self.assertTrue((root / ".opencode" / "node_modules" / "@opencode-ai" / "plugin" / "index.js").exists())
        self.assert_evidence_retained(result)


if __name__ == "__main__":
    unittest.main()
