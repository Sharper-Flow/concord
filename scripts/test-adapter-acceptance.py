#!/usr/bin/env python3
"""Native Bun JUnit acceptance controls; process success alone proves nothing."""
import importlib.util
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
import xml.etree.ElementTree as ET
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "adapter/opencode/acceptance.py"
SPEC = importlib.util.spec_from_file_location("adapter_acceptance", SOURCE)
acceptance = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(acceptance)


class JUnitGradingTests(unittest.TestCase):
    def grade(self, cases, expected=("suite.test.ts#alpha",)):
        with tempfile.TemporaryDirectory() as temporary:
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
                      '<properties status="pass"><property name="x" value="y"/></properties>' + passed,
                      '<properties>pass<property name="x" value="y"/></properties>' + passed,
                      '<properties><property name="x" value="y">pass</property></properties>' + passed,
                      '<properties><property name="x" value="y"/>pass</properties>' + passed,
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
        with tempfile.TemporaryDirectory() as temporary:
            report = Path(temporary) / "native.xml"
            for raw in (None, b"<broken", b"<!DOCTYPE invalid><testsuites/>", b"x" * (2 * 1024 * 1024 + 1)):
                if raw is not None:
                    report.write_bytes(raw)
                with self.subTest(raw=None if raw is None else len(raw)), self.assertRaises(ValueError):
                    acceptance.grade_report(report, ["suite.test.ts"], [])

    def test_wrong_file_cannot_satisfy_an_expected_name(self):
        with tempfile.TemporaryDirectory() as temporary:
            report = Path(temporary) / "native.xml"
            report.write_text('<testsuites><testsuite><testcase file="other.test.ts" name="alpha"/>'
                              '<testcase file="suite.test.ts" name="beta"/></testsuite></testsuites>')
            with self.assertRaises(ValueError):
                acceptance.grade_report(report, ["suite.test.ts", "other.test.ts"], ["suite.test.ts#alpha"])

    def test_reader_grades_retained_ci_file_selection_without_spawning(self):
        with tempfile.TemporaryDirectory() as temporary:
            report = Path(temporary) / "native.xml"
            passed = '<testcase file="adapter/opencode/suite.test.ts" name="alpha"/>'
            other = '<testcase file="adapter/opencode/other.test.ts" name="other"/>'
            argv = [str(SOURCE), "suite.test.ts", "--read-report", str(report),
                    "--process-exit-code", "0", "--expect", "suite.test.ts#alpha"]
            for extra in (other, other.replace('/>', '><failure/></testcase>')):
                report.write_text('<testsuites><testsuite>' + passed + extra + '</testsuite></testsuites>')
                with patch.object(sys, "argv", argv), patch.object(acceptance.subprocess, "run") as run:
                    self.assertEqual(acceptance.main(), 0 if extra == other else 1)
                    run.assert_not_called()
            report.write_text('<testsuites><testsuite>' + passed + '</testsuite></testsuites>')
            for status in ("1", "-9"):
                with patch.object(sys, "argv", [*argv[:4], "--process-exit-code", status]):
                    self.assertNotEqual(acceptance.main(), 0)
            for arguments in ([str(SOURCE), "suite.test.ts", "--read-report", str(report)],
                              [str(SOURCE), "suite.test.ts", "--process-exit-code", "0"]):
                with patch.object(sys, "argv", arguments), self.assertRaises(SystemExit):
                    acceptance.main()


class BunNativeTests(unittest.TestCase):
    def test_native_ci_properties_are_metadata_not_test_outcomes(self):
        with tempfile.TemporaryDirectory() as temporary:
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

    def test_reader_consumes_a_native_repository_root_ci_report(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            directory = root / "adapter/opencode"
            directory.mkdir(parents=True)
            for file, name in (("suite.test.ts", "alpha"), ("other.test.ts", "other")):
                (directory / file).write_text(f'import {{test}} from "bun:test"; test("{name}",()=>{{}});')
            report = root / "native.xml"
            run = subprocess.run(["bun", "test", "adapter/opencode/", "--reporter=junit", f"--reporter-outfile={report}"],
                                 cwd=root, env={**os.environ, "CI": "true"}, capture_output=True, text=True, timeout=20)
            self.assertEqual(run.returncode, 0, run.stdout + run.stderr)
            arguments = [str(SOURCE), "suite.test.ts", "--read-report", str(report),
                         "--process-exit-code", str(run.returncode), "--expect", "suite.test.ts#alpha"]
            with patch.object(sys, "argv", arguments), patch.object(acceptance.subprocess, "run") as spawn:
                self.assertEqual(acceptance.main(), 0)
                spawn.assert_not_called()

    def test_bun_real_outcomes_and_legacy_invocation(self):
        with tempfile.TemporaryDirectory() as temporary:
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
