#!/usr/bin/env python3
"""Replay identical regression assertions against a pinned baseline and HEAD.

The baseline contains known defects: only named assertion failures count as
reproductions. Compilation, setup, timeout, or transport failures do not count.
JSON evidence is retained outside the checkout and uploaded by CI.
"""

import argparse
import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile
import unittest
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[1]
BASELINE_REF = "v8.13.3"
BASELINE_COMMIT = "d6a97fcb7f7b8ae0095abdf51431cbb1147b4cdf"


class HistoricalReplayTest(unittest.TestCase):
    output = None

    @classmethod
    def setUpClass(cls):
        cls.output.mkdir(parents=True, exist_ok=True)
        actual = subprocess.check_output(
            ["git", "rev-parse", BASELINE_REF + "^{commit}"], cwd=ROOT, text=True
        ).strip()
        if actual != BASELINE_COMMIT:
            raise RuntimeError("historical baseline tag does not match its pinned commit")
        cls.head = subprocess.check_output(
            ["git", "rev-parse", "HEAD"], cwd=ROOT, text=True
        ).strip()
        cls.checkout_clean = not subprocess.check_output(
            ["git", "status", "--porcelain"], cwd=ROOT
        ).strip()
        cls.temporary = tempfile.TemporaryDirectory(prefix="concord-history-")
        cls.addClassCleanup(cls.temporary.cleanup)
        cls.baseline = Path(cls.temporary.name)
        archive = subprocess.check_output(["git", "archive", BASELINE_COMMIT], cwd=ROOT)
        with tarfile.open(fileobj=io.BytesIO(archive)) as source:
            source.extractall(cls.baseline, filter="data")

    def execute(self, label, root, command, source, owner, failing):
        result = subprocess.run(command, cwd=root, capture_output=True, text=True, check=False)
        record = {
            "baseline_ref": BASELINE_REF,
            "source_commit": BASELINE_COMMIT if root == self.baseline else self.head,
            "checkout_clean": root == self.baseline or self.checkout_clean,
            "fixture_sha256": hashlib.sha256(source).hexdigest(),
            "fixture_owner": owner,
            "expected_result": "named-assertion-failures" if failing else "pass",
            "command": command,
            "returncode": result.returncode,
            "stdout": result.stdout,
            "stderr": result.stderr,
        }
        (self.output / (label + ".json")).write_text(json.dumps(record, indent=2) + "\n")
        return result

    def go_pair(self, relative, baseline_owner, baseline_fails):
        source = (ROOT / relative).read_bytes()
        names = set(re.findall(r"func (TestHistoricalRepro\w+)\(", source.decode()))
        self.assertTrue(names, "fixture contains no named historical regressions")
        current_owner = Path(relative).parent.name
        selected = "^(" + "|".join(sorted(names)) + ")$"
        for label, root, owner, failing in (
            ("before", self.baseline, baseline_owner, baseline_fails),
            ("after", ROOT, current_owner, False),
        ):
            with self.subTest(fixture=relative, revision=label):
                if label == "before":
                    adapted = re.sub(rb"\Apackage [a-z]+", b"package " + owner.encode(), source, count=1)
                    (root / "internal" / owner / "zz_workflow_history_test.go").write_bytes(adapted)
                else:
                    adapted = source
                command = ["go", "test", "./internal/" + owner, "-run", selected,
                           "-count=1", "-json", "-timeout=3m"]
                result = self.execute(Path(relative).stem + "-" + label, root, command, adapted, owner, failing)
                passed, failed, assertions = set(), set(), set()
                for line in result.stdout.splitlines():
                    try:
                        event = json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    name = event.get("Test", "").split("/")[0]
                    if event.get("Action") == "pass":
                        passed.add(name)
                    if event.get("Action") == "fail":
                        failed.add(name)
                    if "REPRO:" in event.get("Output", ""):
                        assertions.add(name)
                if failing:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertTrue(names <= failed, result.stdout + result.stderr)
                    self.assertTrue(names <= assertions, "baseline failed before the regression assertion")
                else:
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertTrue(names <= passed, "current regression tests did not execute")

    def test_store_contracts(self):
        self.go_pair("internal/store/workflow_historical_regression_test.go", "store", True)

    def test_shared_validator(self):
        self.go_pair("internal/payloadschema/historical_validation_test.go", "agent", True)

    def test_array_compatibility(self):
        # Valid arrays are a compatibility control on the released baseline.
        self.go_pair("internal/store/workflow_array_regression_test.go", "store", False)

    def test_adapter_contracts(self):
        relative = "adapter/opencode/historical_validation.test.ts"
        source = (ROOT / relative).read_bytes()
        names = set(re.findall(r'test\("([^"]+)"', source.decode()))
        self.assertTrue(names)
        for label, root, failing in (("before", self.baseline, True), ("after", ROOT, False)):
            with self.subTest(revision=label):
                target = relative
                if failing:
                    target = "adapter/opencode/zz-historical_validation.test.ts"
                    (root / target).write_bytes(source)
                report = self.output / ("adapter-" + label + ".xml")
                report.unlink(missing_ok=True)
                command = ["bun", "test", target, "--reporter=junit", "--reporter-outfile=" + str(report)]
                result = self.execute("adapter-" + label, root, command, source, "adapter/opencode", failing)
                cases = {case.attrib["name"]: case for case in ET.parse(report).iter("testcase")}
                self.assertTrue(names <= cases.keys())
                if failing:
                    self.assertNotEqual(result.returncode, 0)
                    assertions = set()
                    for line in result.stderr.splitlines():
                        try:
                            event = json.loads(line)
                        except json.JSONDecodeError:
                            continue
                        if isinstance(event, dict) and event.get("historical_repro") in names:
                            assertions.add(event["historical_repro"])
                    self.assertTrue(names <= assertions, "baseline failed before the regression assertion")
                    for name in names:
                        self.assertIsNotNone(cases[name].find("failure"))
                else:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    for name in names:
                        self.assertIsNone(cases[name].find("failure"))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    HistoricalReplayTest.output = args.output_dir.resolve()
    if HistoricalReplayTest.output.is_relative_to(ROOT):
        parser.error("execution evidence must be written outside the checkout")
    unittest.main(argv=[__file__], verbosity=2)
