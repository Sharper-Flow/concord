#!/usr/bin/env python3
"""Synthetic native Go results and CI artifact contract; no Go tests execute."""

import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts/go-test-results.py"
spec = importlib.util.spec_from_file_location("go_test_results", SCRIPT)
reader = importlib.util.module_from_spec(spec)
spec.loader.exec_module(reader)


def event(action, test=None, package="example/p", **fields):
    return {"Action": action, "Package": package, **({"Test": test} if test else {}), **fields}


def passing(names=("TestParent",)):
    return [event("start")] + [e for name in names for e in (
        event("run", name), event("pass", name, Elapsed=2.0))] + [
        event("output", Output="ok\texample/p\t2.5s\n"), event("pass", Elapsed=2.5)]


class ReaderTests(unittest.TestCase):
    def read(self, events, rc=0, selection=None, suffix=""):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "events.jsonl"
            path.write_text("".join(json.dumps(e) + "\n" for e in events) + suffix)
            return reader.read_go_results(path, rc, selection)

    def test_parallel_nested_levels_and_distinct_elapsed(self):
        events = [event("start"), event("run", "TestParent"),
            event("run", "TestParent/child"), event("pause", "TestParent/child"),
            event("run", "TestOther"), event("attr", "TestOther", Key="k", Value="v"),
            event("artifacts", "TestOther", Path="/tmp/a"), event("cont", "TestParent/child"),
            event("pass", "TestOther", Elapsed=8), event("pass", "TestParent/child", Elapsed=7),
            event("pass", "TestParent", Elapsed=9),
            event("output", Output="ok\texample/p\t10s\n"), event("pass", Elapsed=10)]
        result = self.read(events)
        self.assertEqual(result["parser_errors"], [])
        self.assertTrue(result["complete"])
        pkg = result["packages"][0]
        self.assertEqual(pkg["elapsed_seconds"], 10)
        tests = {t["name"]: t for t in pkg["tests"]}
        self.assertEqual(tests["TestParent/child"]["level"], 1)
        self.assertEqual(tests["TestParent/child"]["elapsed_seconds"], 7)
        self.assertEqual(reader.bounded_summary(result)["slow_tests"][0]["elapsed_seconds"], 9)
        self.assertNotIn("wall_seconds", result)

    def test_cached_replay_timestamps_do_not_mean_fresh(self):
        events = passing()
        events[-2]["Output"] = "ok\texample/p\t(cached)\n"
        for e in events:
            e["Time"] = "2026-01-01T00:00:00Z"
        result = self.read(events)
        self.assertTrue(result["packages"][0]["cached"])
        self.assertEqual(result["packages"][0]["tests"][0]["measurement"], "reused")
        self.assertEqual(result["packages"][0]["tests"][0]["elapsed_seconds"], 2)
        unknown = self.read([e for e in events if e["Action"] != "output"])
        self.assertIsNone(unknown["packages"][0]["cached"])
        self.assertEqual(unknown["packages"][0]["tests"][0]["measurement"], "unknown")

    def test_failure_and_build_failure_are_not_parser_success(self):
        for events in ([event("start"), event("run", "TestBad"), event("fail", "TestBad"), event("fail")],
                       [{"Action": "build-output", "ImportPath": "example/p", "Output": "undefined: x"},
                        {"Action": "build-fail", "ImportPath": "example/p"}, event("start"), event("fail", FailedBuild="example/p")]):
            with self.subTest(events=events):
                result = self.read(events, 7)
                self.assertEqual(result["outcome"], "fail")
                self.assertEqual(result["process_exit_code"], 7)
                self.assertEqual(result["parser_errors"], [])
        summary = reader.bounded_summary(self.read([event("start"), event("run", "TestBad"),
            event("fail", "TestBad"), event("run", "TestHung"), event("fail")], 1))
        self.assertEqual(summary["failed_tests"], [{"package": "example/p", "name": "TestBad"}])
        self.assertEqual(summary["unfinished_tests"], [{"package": "example/p", "name": "TestHung"}])

    def test_early_error_empty_interruption_and_unfinished_tests(self):
        for events, rc, suffix in (([], 1, ""), ([], 0, ""), (passing(), None, ""),
                                   ([event("start"), event("run", "TestHung")], 143, ""),
                                   (passing(), 0, '{"Action":'),
                                   ([], 1, "go: module missing\n")):
            with self.subTest(events=events, rc=rc):
                result = self.read(events, rc, suffix=suffix)
                self.assertFalse(result["complete"])
                self.assertNotEqual(result["outcome"], "pass")

    def test_bad_event_shapes_are_visible(self):
        for bad in ([], None, {}, event("alien"), event("pass", Elapsed=-1),
                    event("pass", Elapsed="2"), event("pass", Elapsed=float("nan")),
                    event("output", Output=7), {"Action": "build-fail"}):
            with self.subTest(bad=bad):
                result = self.read([bad])
                self.assertEqual(len(result["parser_errors"]), 1)
                self.assertFalse(result["complete"])

    def test_expected_missing_skip_compile_only_and_absent_claim(self):
        result = self.read(passing(), selection={"expected_tests": {"example/p": ["TestParent", "TestMissing"]}})
        self.assertEqual(result["selection"]["missing"], [{"package": "example/p", "name": "TestMissing"}])
        self.assertEqual(result["outcome"], "selection_unproved")
        events = [event("start"), event("run", "TestSkipped"), event("skip", "TestSkipped"), event("pass")]
        result = self.read(events, selection={"expected_tests": {"example/p": ["TestSkipped"]}})
        self.assertEqual(result["selection"]["skipped"], [{"package": "example/p", "name": "TestSkipped"}])
        self.assertEqual(result["outcome"], "selection_unproved")
        no_match = [event("start"), event("output", Output="ok\texample/p\t0.1s [no tests to run]\n"), event("pass")]
        self.assertEqual(self.read(no_match)["outcome"], "selection_unproved")
        self.assertEqual(self.read(no_match, selection={"compile_only": True})["outcome"], "compile_only")
        self.assertFalse(self.read(passing())["selection"]["named_claim"])

    def test_summaries_bound_lists_and_point_to_full_evidence(self):
        results = self.read(passing(tuple(f"Test{i}" for i in range(80))))
        summary = reader.bounded_summary(results)
        self.assertEqual(len(summary["slow_tests"]), 50)
        self.assertEqual(summary["total_counts"]["tests"], 80)
        self.assertEqual(summary["full_results"], "results.json")
        self.assertEqual(summary["raw_events"], "events.jsonl")

    def test_invalid_expected_inventory_is_not_split_into_characters(self):
        with self.assertRaisesRegex(ValueError, "lists of exact test names"):
            self.read(passing(), selection={"expected_tests": {"example/p": "TestParent"}})

    def test_terminal_without_run_never_proves_named_execution(self):
        result = self.read([event("start"), event("pass", "TestAbsentRun", Elapsed=1), event("pass")],
            selection={"expected_tests": {"example/p": ["TestAbsentRun"]}})
        self.assertFalse(result["complete"])
        self.assertEqual(result["selection"]["missing"], [{"package": "example/p", "name": "TestAbsentRun"}])
        self.assertNotEqual(result["outcome"], "pass")


FAKE_GO = '''#!{python}
import json, os, signal, sys
from pathlib import Path
if sys.argv[1] == "version":
    print("go version go1.26.7 linux/amd64")
elif sys.argv[1] == "env":
    print(json.dumps({{"GOOS":"linux", "GOARCH":"amd64", "CGO_ENABLED":"1", "GOVERSION":"go1.26.7", "GOCACHE":"/synthetic/go-cache", "GOFLAGS":"", "GODEBUG":""}}))
else:
    with open(os.environ["CALLS"], "a") as calls:
        calls.write(json.dumps(sys.argv[1:]) + "\\n")
    sys.stdout.write(Path(os.environ["FIXTURE"]).read_text())
    sys.stdout.flush()
    sys.stderr.write("full early diagnostic\\n")
    if os.environ.get("SIGNAL_HOLD"):
        signal.pause()
    if os.environ.get("SIGNAL"):
        os.kill(os.getpid(), int(os.environ["SIGNAL"]))
    sys.exit(int(os.environ.get("CHILD_EXIT", "0")))
'''


class ProducerTests(unittest.TestCase):
    def run_producer(self, events, rc=0, flags=(), signum=None, forward_signal=False):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            go = root / "go"
            go.write_text(FAKE_GO.format(python=sys.executable))
            go.chmod(0o700)
            fixture = root / "fixture"
            fixture.write_text("".join(json.dumps(e) + "\n" for e in events))
            output = root / "artifacts"
            env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"],
                CALLS=str(root / "calls"), FIXTURE=str(fixture), CHILD_EXIT=str(rc),
                SECRET_TEST_SENTINEL="do-not-record", GITHUB_RUN_ID="123", GITHUB_RUN_ATTEMPT="2")
            if signum:
                env["SIGNAL"] = str(signum)
            argv = [sys.executable, str(SCRIPT), "--output-dir", str(output), "--", "go", "test", *flags, "./example"]
            if forward_signal:
                env["SIGNAL_HOLD"] = "1"
                with subprocess.Popen(argv, cwd=ROOT, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True) as child:
                    first = child.stdout.readline()
                    child.send_signal(signal.SIGTERM)
                    stdout, stderr = child.communicate(timeout=15)
                    result = subprocess.CompletedProcess(argv, child.returncode, first + stdout, stderr)
            else:
                result = subprocess.run(argv, cwd=ROOT, env=env, capture_output=True, text=True, timeout=15)
            files = {p.name: p.read_text() for p in output.iterdir()}
            calls = (root / "calls").read_text().splitlines()
            return result, files, calls

    def test_one_execution_exact_flags_full_diagnostics_and_provenance(self):
        result, files, calls = self.run_producer(passing(), flags=("-race", "-count=1", "-run", "^TestParent$"))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(calls), 1)
        self.assertEqual(json.loads(calls[0]), ["test", "-json", "-race", "-count=1", "-run", "^TestParent$", "./example"])
        self.assertEqual(set(files), {"events.jsonl", "stderr.log", "metadata.json", "results.json", "summary.json", "summary.md"})
        self.assertIn("full early diagnostic", files["stderr.log"])
        self.assertIn("full early diagnostic", result.stderr)
        meta = json.loads(files["metadata.json"])
        self.assertEqual(meta["ci"]["GITHUB_RUN_ATTEMPT"], "2")
        self.assertEqual(meta["toolchain"], "go version go1.26.7 linux/amd64")
        self.assertIn("subject_after", meta)
        self.assertIsNone(meta["queue_seconds"])
        self.assertTrue(meta["cache"]["build_cache_enabled"])
        self.assertNotIn("/synthetic/go-cache", files["metadata.json"])
        self.assertNotIn("do-not-record", files["metadata.json"])

    def test_failure_and_signal_preserve_exit_and_artifacts(self):
        for rc, signum, want in ((7, None, 7), (0, signal.SIGTERM, 143)):
            with self.subTest(rc=rc, signum=signum):
                result, files, _ = self.run_producer([event("start"), event("run", "TestBad"), event("fail", "TestBad")], rc, signum=signum)
                self.assertEqual(result.returncode, want)
                self.assertEqual(json.loads(files["results.json"])["process_exit_code"], want)
                self.assertEqual(json.loads(files["results.json"])["outcome"], "fail")
                self.assertIn("full early diagnostic", files["stderr.log"])
                self.assertIn("example/p/TestBad", files["summary.md"].split("Failed or unfinished")[1])

    def test_empty_or_invalid_stream_cannot_be_passing_collection(self):
        for events in ([], [None]):
            result, files, _ = self.run_producer(events)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(json.loads(files["results.json"])["complete"])

    def test_wrapper_forwards_signal_and_invalidates_complete_stream(self):
        result, files, _ = self.run_producer(passing(), forward_signal=True)
        self.assertEqual(result.returncode, 143)
        self.assertEqual(json.loads(files["metadata.json"])["interrupted_signal"], signal.SIGTERM)
        self.assertFalse(json.loads(files["results.json"])["complete"])

    def test_compile_only_is_explicit_and_race_does_not_disable_cache(self):
        result, files, _ = self.run_producer([event("start"), event("pass")], flags=("-run", "^$"))
        self.assertEqual(result.returncode, 0)
        self.assertEqual(json.loads(files["results.json"])["outcome"], "compile_only")
        cached = passing()
        cached[-2]["Output"] = "ok\texample/p\t(cached)\n"
        _, files, _ = self.run_producer(cached, flags=("-race",))
        self.assertTrue(json.loads(files["results.json"])["packages"][0]["cached"])

    def test_artifact_failure_never_becomes_pass(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(sys, "argv", [str(SCRIPT), "--output-dir", str(Path(tmp) / "new"), "--", "go", "test", "./example"]), patch.object(reader, "probe", return_value=None), patch.object(reader, "capture", return_value=0), patch.object(reader, "read_go_results", side_effect=ValueError("broken parser")):
            self.assertEqual(reader.main(), 1)
            meta = json.loads((Path(tmp) / "new/metadata.json").read_text())
            self.assertEqual(meta["artifact_error"], "broken parser")

    def test_unobserved_child_status_stays_unknown(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(sys, "argv", [str(SCRIPT), "--output-dir", str(Path(tmp) / "new"), "--", "go", "test", "./example"]), patch.object(reader, "probe", return_value=None), patch.object(reader, "capture", side_effect=OSError("stream write failed")):
            self.assertEqual(reader.main(), 125)
            meta = json.loads((Path(tmp) / "new/metadata.json").read_text())
            self.assertIsNone(meta["process_exit_code"])
            self.assertFalse(json.loads((Path(tmp) / "new/summary.json").read_text())["complete"])


class CIContractTests(unittest.TestCase):
    def test_all_execution_jobs_retain_even_failed_results_and_gates_stay_fail_closed(self):
        import yaml
        jobs = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())["jobs"]
        owners = {"verify-go", "verify-tests-rest", "verify-race", "verify-acceptance"} | {
            f"verify-tests-store-{i}" for i in range(1, 6)} | {f"verify-tests-cli-{i}" for i in (1, 2)}
        for name in owners:
            steps = jobs[name]["steps"]
            producers = [s for s in steps if "scripts/go-test-results.py --output-dir" in s.get("run", "")]
            uploads = [s for s in steps if s.get("name") == "Retain native Go results"]
            self.assertEqual(len(producers), 1, name)
            self.assertEqual(len(uploads), 1, name)
            self.assertNotIn("continue-on-error", producers[0])
            self.assertEqual(uploads[0]["if"], "always()")
            self.assertEqual(uploads[0]["with"]["if-no-files-found"], "error")
        gate = jobs["verify-tests"]
        self.assertEqual(gate["if"], "always()")
        for result in ("REST", "STORE_1", "STORE_2", "STORE_3", "STORE_4", "STORE_5", "CLI_1", "CLI_2"):
            self.assertIn(f'test "${result}_RESULT" = success', gate["steps"][0]["run"])
        compile_step = next(s for s in jobs["verify-go"]["steps"] if s.get("name") == "Compile all tests")
        self.assertEqual(compile_step["run"], "go test -run '^$' -timeout=15m ./...")


if __name__ == "__main__":
    unittest.main()
