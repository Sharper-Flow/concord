#!/usr/bin/env python3
"""Native Bun JUnit acceptance controls; process success alone proves nothing."""
import importlib.util
import json
import os
import shutil
import subprocess
import tempfile
import unittest
import xml.etree.ElementTree as ET
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "adapter/opencode/acceptance.py"
SPEC = importlib.util.spec_from_file_location("adapter_acceptance", SOURCE)
acceptance = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(acceptance)


class JUnitGradingTests(unittest.TestCase):
    def grade(self, cases, expected=("suite.test.ts#alpha",)):
        with tempfile.TemporaryDirectory(dir=ROOT) as temporary:
            report = Path(temporary) / "native.xml"
            report.write_text('<testsuites><testsuite>' + cases + '</testsuite></testsuites>')
            return acceptance.grade_report(report, ["suite.test.ts"], list(expected))

    def test_exact_native_pass(self):
        self.assertEqual(self.grade('<testcase file="suite.test.ts" name="alpha"/>'),
                         [{"file": "suite.test.ts", "name": "alpha", "outcome": "pass"}])

    def test_flat_suite_metadata_cannot_supply_or_hide_outcomes(self):
        metadata = '<properties><property name="git.commit" value="synthetic"/></properties>'
        passed = '<testcase file="suite.test.ts" name="alpha"/>'
        self.assertEqual(self.grade(metadata + passed), self.grade(passed))
        for cases in (metadata, metadata + '<testcase file="suite.test.ts" name="alpha"><failure/></testcase>',
                      '<properties>' + passed + '</properties>' + passed,
                      '<properties><property name="x" value="y"><failure/></property></properties>' + passed,
                      '<properties><property name="x"/></properties>' + passed,
                      '<properties><property name="x" value="y" status="pass"/></properties>' + passed,
                      '<properties><property name="" value="y"/></properties>' + passed):
            with self.subTest(cases=cases), self.assertRaises(ValueError):
                self.grade(cases)

    def test_missing_empty_foreign_ambiguous_and_nonpass_refuse(self):
        cases = ["", '<testcase file="suite.test.ts" name="other"/>',
                 '<testcase file="elsewhere/suite.test.ts" name="alpha"/>',
                 '<testcase name="alpha"/>', '<testcase file="suite.test.ts" name=""/>',
                 '<testcase file="suite.test.ts" name="alpha"/>' * 2]
        cases += [f'<testcase file="suite.test.ts" name="alpha"><{tag}/></testcase>'
                  for tag in ("skipped", "failure", "error", "todo", "unknown")]
        cases += ['<testcase file="suite.test.ts" name="alpha" status="skipped"/>']
        for case in cases:
            with self.subTest(case=case), self.assertRaises(ValueError):
                self.grade(case)

    def test_legacy_file_selection_needs_a_pass_in_every_file(self):
        self.assertEqual(len(self.grade('<testcase file="suite.test.ts" name="alpha"/>', ())), 1)
        for case in ("", '<testcase file="suite.test.ts" name="alpha"><skipped/></testcase>'):
            with self.subTest(case=case), self.assertRaises(ValueError):
                self.grade(case, ())

    def test_legacy_mixed_skip_and_pass_is_compatible(self):
        observed = self.grade('<testcase file="suite.test.ts" name="alpha"/>'
                              '<testcase file="suite.test.ts" name="optional"><skipped/></testcase>', ())
        self.assertEqual([case["outcome"] for case in observed], ["pass", "skipped"])

    def test_native_nested_suites_and_parameterized_names_keep_file_only_semantics(self):
        case = '<testcase file="suite.test.ts" name="alpha"/>'
        observed = self.grade('<testsuite name="describe">' + case * 2 + '</testsuite>', ())
        self.assertEqual(len(observed), 2)
        with self.assertRaises(ValueError):
            self.grade(case + '<testcase file="suite.test.ts" name="alpha"><skipped/></testcase>', ())

    def test_missing_malformed_and_oversized_reports_refuse(self):
        with tempfile.TemporaryDirectory(dir=ROOT) as temporary:
            report = Path(temporary) / "native.xml"
            for raw in (None, b"<broken", b"<!DOCTYPE invalid><testsuites/>", b"x" * (2 * 1024 * 1024 + 1)):
                if raw is not None:
                    report.write_bytes(raw)
                with self.subTest(raw=None if raw is None else len(raw)), self.assertRaises(ValueError):
                    acceptance.grade_report(report, ["suite.test.ts"], [])

    def test_wrong_file_cannot_satisfy_an_expected_name(self):
        with tempfile.TemporaryDirectory(dir=ROOT) as temporary:
            report = Path(temporary) / "native.xml"
            report.write_text('<testsuites><testsuite><testcase file="other.test.ts" name="alpha"/>'
                              '<testcase file="suite.test.ts" name="beta"/></testsuite></testsuites>')
            with self.assertRaises(ValueError):
                acceptance.grade_report(report, ["suite.test.ts", "other.test.ts"], ["suite.test.ts#alpha"])


class BunNativeTests(unittest.TestCase):
    def test_native_ci_properties_are_metadata_not_test_outcomes(self):
        with tempfile.TemporaryDirectory(dir=ROOT) as temporary:
            root = Path(temporary)
            (root / "suite.test.ts").write_text('import {test,expect} from "bun:test"; test("alpha",()=>expect(true).toBe(true));')
            report = root / "native.xml"
            environment = {**os.environ, "CI": "true", "GITHUB_ACTIONS": "true",
                           "GITHUB_REPOSITORY": "example/project", "GITHUB_SHA": "a" * 40,
                           "GITHUB_RUN_ID": "1", "GITHUB_RUN_NUMBER": "1", "GITHUB_JOB": "test",
                           "GITHUB_WORKFLOW": "test", "GITHUB_REF": "refs/heads/test"}
            run = subprocess.run(["bun", "test", "--reporter=junit", f"--reporter-outfile={report}", "./suite.test.ts"],
                                 cwd=root, env=environment, capture_output=True, text=True, timeout=20)
            self.assertEqual(run.returncode, 0, run.stdout + run.stderr)
            properties = ET.parse(report).find("./testsuite/properties")
            self.assertIsNotNone(properties)
            self.assertGreater(len(properties), 0)
            self.assertTrue(all(item.tag == "property" and set(item.attrib) == {"name", "value"}
                                and len(item) == 0 for item in properties), ET.tostring(properties, encoding="unicode"))
            self.assertEqual(acceptance.grade_report(report, ["suite.test.ts"], ["alpha"]),
                             [{"file": "suite.test.ts", "name": "alpha", "outcome": "pass"}])

    def test_generator_keeps_the_corpus_pinned_and_only_emits_checked_owners(self):
        spec = importlib.util.spec_from_file_location("adapter_generator", ROOT / "scripts/generate-adapter-oracle.py")
        generator = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(generator)
        manifest = json.loads(generator.render()[generator.OUTPUT])
        self.assertEqual(manifest["support_files"], [".concord/scenarios/adapter-continuity.v1.json"])
        witnesses = manifest["cases"]["case:adapter-continuity"]
        self.assertEqual(len(witnesses), 7)
        for witness in witnesses:
            self.assertIn({"witness": witness, "argv": ["python3", "acceptance.py", "continuity_hook.test.ts", "--expect", witness]}, manifest["checks"])

    def test_current_continuity_exact_names_actually_pass(self):
        document = json.loads((ROOT / ".concord/scenarios/adapter-continuity.v1.json").read_text())
        expected = [scenario["id"] for scenario in document["scenarios"]
                    if scenario["id"] != "adapter-continuity-session-vacate-then-reclaim-passes-occupancy-gate"]
        arguments = [argument for identity in expected for argument in ("--expect", identity)]
        run = subprocess.run(["python3", str(SOURCE), "continuity_hook.test.ts", *arguments],
                             cwd=ROOT, capture_output=True, text=True, timeout=60)
        self.assertEqual(run.returncode, 0, run.stdout + run.stderr)
        observed = json.loads(run.stdout.splitlines()[-1])["observed"]
        for identity in expected:
            self.assertIn({"file": "continuity_hook.test.ts", "name": identity, "outcome": "pass"}, observed)

    def test_existing_dispatch_checks_keep_their_file_selection(self):
        spec = importlib.util.spec_from_file_location("adapter_generator", ROOT / "scripts/generate-adapter-oracle.py")
        generator = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(generator)
        for files in generator.CHECKS.values():
            with self.subTest(files=files):
                run = subprocess.run(["python3", str(SOURCE), *files], cwd=ROOT,
                                     capture_output=True, text=True, timeout=60)
                self.assertEqual(run.returncode, 0, run.stdout + run.stderr)
                observed = json.loads(run.stdout.splitlines()[-1])["observed"]
                self.assertEqual({case["file"] for case in observed}, set(files))
                for file in files:
                    self.assertTrue(any(case["file"] == file and case["outcome"] == "pass" for case in observed))

    def test_pinned_bun_real_outcomes_and_legacy_invocation(self):
        pin = json.loads((ROOT / ".concord/oracles/adapter/dispatch-acceptance.v1.json").read_text())["pin"]["bun"]
        self.assertEqual(subprocess.run(["bun", "--version"], capture_output=True, text=True,
                                        timeout=10).stdout.strip(), pin)
        with tempfile.TemporaryDirectory(dir=ROOT) as temporary:
            root = Path(temporary)
            shutil.copyfile(SOURCE, root / "acceptance.py")
            (root / "acceptance-preload.ts").write_text("")
            sources = {
                "pass.test.ts": 'import {test,expect} from "bun:test"; test("alpha",()=>expect(2+2).toBe(4));',
                "skip.test.ts": 'import {test} from "bun:test"; test.skip("alpha",()=>{});',
                "todo.test.ts": 'import {test} from "bun:test"; test.todo("alpha",()=>{});',
                "empty.test.ts": 'export const empty = true;',
                "fail.test.ts": 'import {test,expect} from "bun:test"; test("alpha",()=>expect(true).toBe(false));',
                "duplicate.test.ts": 'import {test} from "bun:test"; test("alpha",()=>{}); test("alpha",()=>{});',
                "mixed.test.ts": 'import {test} from "bun:test"; test("alpha",()=>{}); test.skip("optional",()=>{});',
                "syntax.test.ts": 'import { missing syntax !!!',
            }
            for name, source in sources.items():
                (root / name).write_text(source)
            for file, expected, success in [
                ("pass.test.ts", [], True), ("pass.test.ts", ["--expect", "alpha"], True),
                ("pass.test.ts", ["--expect", "pass.test.ts#alpha"], True),
                ("pass.test.ts", ["--expect", "absent"], False),
                ("skip.test.ts", [], False), ("todo.test.ts", [], False),
                ("empty.test.ts", [], False), ("fail.test.ts", [], False),
                ("duplicate.test.ts", [], True), ("duplicate.test.ts", ["--expect", "alpha"], False),
                ("mixed.test.ts", [], True),
                ("mixed.test.ts", ["--expect", "optional"], False), ("syntax.test.ts", [], False),
            ]:
                with self.subTest(file=file, expected=expected):
                    run = subprocess.run(["python3", str(root / "acceptance.py"), file, *expected],
                                         cwd=root, capture_output=True, text=True, timeout=20)
                    self.assertEqual(run.returncode == 0, success, run.stdout + run.stderr)
                    if success:
                        summary = json.loads(run.stdout.splitlines()[-1])
                        self.assertIn({"file": file, "name": "alpha", "outcome": "pass"}, summary["observed"])


if __name__ == "__main__":
    unittest.main()
