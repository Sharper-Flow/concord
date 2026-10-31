#!/usr/bin/env python3
"""Tests for the release admission mechanism (scripts/release-evidence.py).

Three batteries:

1. Workflow drift: the CI workflow keeps every verification owner, gains
   `workflow_call`, and loses its unconditional main-push trigger; the Release
   workflow starts on a main push, never on `workflow_run`, calls the same CI
   workflow file as its full fallback, and gates publication on an explicit
   admission job. No other workflow may trigger on a push to main, or a merged
   commit would run the full suite twice again.
2. Gate algebra: the real `release-evidence.py gate` command, executed as a
   subprocess, admits exactly (reuse, lookup success, fallback skipped) and
   (full, lookup success, fallback success) and refuses every other
   dependency state.
3. Evidence selection: the real `select` command, executed as a subprocess
   against temporary Git repositories and a fake `gh` on PATH, reuses only
   exact, authenticated, unambiguous merge-group evidence for the exact push
   SHA and selects the full fallback for every miss. Target identity failures
   refuse instead of selecting.

The GitHub API fixtures use synthetic repository, workflow, run, and job IDs.
The `gh` boundary is faked at the subprocess level so a source failure (wrong
endpoint, missing re-read, unpersisted failure) cannot hide behind a stubbed
function.
"""

from __future__ import annotations

import json
import os
import re
import select
import shlex
import subprocess
import sys
import tempfile
import time
import unittest
import xml.etree.ElementTree as ET
from unittest.mock import patch
from pathlib import Path

import git_environment

git_environment.scrub_inherited()

import yaml

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "release-evidence.py"
CI_WORKFLOW = ROOT / ".github" / "workflows" / "ci.yml"
RELEASE_WORKFLOW = ROOT / ".github" / "workflows" / "release.yml"
WORKFLOW_DIR = ROOT / ".github" / "workflows"

# The internal/store shard count that the shard margin rule in ci.yml sets.
STORE_SHARDS = 7

CI_JOB_IDS = (
    "verify-upstream-compatibility",
    "verify-history",
    "verify",
    "verify-adapter",
    "verify-adapter-rest",
    "verify-contracts",
    "verify-tooling",
    "verify-go",
    "verify-tests",
    "verify-tests-rest",
    "verify-tests-store-1",
    "verify-tests-store-2",
    "verify-tests-store-3",
    "verify-tests-store-4",
    "verify-tests-store-5",
    "verify-tests-store-6",
    "verify-tests-store-7",
    "verify-tests-cli-1",
    "verify-tests-cli-2",
    "verify-tests-agent-1",
    "verify-tests-agent-2",
    "verify-tests-agent-3",
    "verify-race",
    "verify-acceptance",
)

# Synthetic fixtures; never the runtime IDs of the real repository.
REPOSITORY = "example-owner/example-repo"
REPOSITORY_ID = 4242
WORKFLOW_ID = 909
OTHER_SHA = "2a1c2c7e8d4c0b3a9f5e7d1c3e5a7c9e1d3f5b7d"
RUN_ID = 777001
RUN_ATTEMPT = 1
CREATED_AT = "2026-10-09T08:00:00Z"
RUN_URL = f"https://example.invalid/example-owner/example-repo/actions/runs/{RUN_ID}"
CI_PATH = ".github/workflows/ci.yml"


def load_workflow(path: Path) -> dict:
    document = yaml.safe_load(path.read_text(encoding="utf-8"))
    if not isinstance(document, dict):
        raise AssertionError(f"{path} must parse to a YAML mapping")
    return document


def triggers(document: dict) -> dict:
    """The workflow `on:` block. PyYAML resolves the bare key `on` to True."""
    block = document.get("on", document.get(True))
    if block is None:
        raise AssertionError("workflow declares no triggers")
    if not isinstance(block, dict):
        raise AssertionError("workflow triggers must parse to a mapping")
    return block


def jobs(document: dict) -> dict:
    block = document.get("jobs")
    if not isinstance(block, dict):
        raise AssertionError("workflow jobs must parse to a mapping")
    return block


def needs_list(job: dict) -> list:
    """A job's needs as a list; YAML allows a bare scalar for one entry."""
    value = job.get("needs", [])
    if isinstance(value, str):
        return [value]
    if isinstance(value, list):
        return value
    raise AssertionError(f"job needs must be a string or a list, got {value!r}")


class CIWorkflowDriftTests(unittest.TestCase):
    def test_required_go_job_runs_pinned_vulnerability_scan_before_other_checks(self) -> None:
        job = jobs(load_workflow(CI_WORKFLOW))["verify-go"]
        self.assertNotIn("if", job)
        self.assertNotIn("continue-on-error", job)
        steps = job["steps"]
        scan = next(step for step in steps if step.get("name") == "Run govulncheck")
        self.assertEqual(scan["run"], "go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...")
        self.assertNotIn("if", scan)
        self.assertNotIn("continue-on-error", scan)
        setup = next(i for i, step in enumerate(steps) if "actions/setup-go@" in step.get("uses", ""))
        self.assertEqual(steps[setup + 1], scan)
        tools = json.loads((ROOT / ".concord/tooling.v1.json").read_text())["tools"]
        declared = next(tool for tool in tools if tool["id"] == "govulncheck")
        self.assertEqual(declared["invocation"], scan["run"])
        self.assertEqual(declared["automation_path"], CI_PATH)

    def test_ci_consumes_all_eight_continuity_witnesses_from_retained_results(self) -> None:
        block = jobs(load_workflow(CI_WORKFLOW))
        scenarios = json.loads((ROOT / ".concord/scenarios/adapter-continuity.v1.json").read_text())["scenarios"]
        tools = json.loads((ROOT / ".concord/tooling.v1.json").read_text())["tools"]
        bindings = {entry["anchor"]["value"]: entry["owner"] for tool in tools
                    for entry in tool.get("coverage", []) if entry["anchor"]["kind"] == "scenario"}
        adapter = next(step for step in block["verify-adapter-rest"]["steps"]
                       if step["name"] == "Require the seven adapter continuity witnesses in native results")
        reclaim = next(step for step in block["verify-go"]["steps"]
                       if step["name"] == "Require the continuity reclaim witness in native results")
        self.assertNotIn("if", adapter)
        self.assertNotIn("if", reclaim)
        argv = shlex.split(adapter["run"].replace("\\\n", " "))
        self.assertEqual(argv[:3], ["python3", "adapter/opencode/acceptance.py", "continuity_hook.test.ts"])
        self.assertEqual(argv[3:7], ["--read-report", "$RUNNER_TEMP/adapter-test-results.xml", "--process-exit-code", "0"])
        expected = argv[7:]
        self.assertEqual(expected[::2], ["--expect"] * 7)
        self.assertEqual(len(set(expected[1::2])), 7)
        go = shlex.split(reclaim["run"])
        self.assertEqual(go[:3], ["bin/oc-test", "read-go", "$RUNNER_TEMP/go-test-results/event-log"])
        self.assertEqual(len(go), 4)
        go_owner, go_id = go[3].split("=", 1)[1].split("/", 1)
        self.assertEqual(go[3].split("=", 1)[0], "github.com/sharper-flow/concord/internal/store")
        self.assertEqual(set(expected[1::2]) | {go_id}, {scenario["id"] for scenario in scenarios})
        for identity in expected[1::2]:
            self.assertEqual(bindings[identity], {"kind": "adapter_test", "value": f"adapter/opencode/continuity_hook.test.ts#{identity}"})
        self.assertEqual(bindings[go_id], {"kind": "go_test", "value": f"internal/store.{go_owner}"})
        producer = next(step for step in block["verify-go"]["steps"]
                        if step["name"] == "Check event log references and continuity reclaim")
        producer_argv = shlex.split(producer["run"])
        self.assertEqual(producer_argv[:4], ["python3", "scripts/go-test-results.py", "--output-dir", go[2]])
        self.assertIn(go_owner, producer_argv[producer_argv.index("-run") + 1])
        suite = "python3 scripts/test-adapter-acceptance.py"
        self.assertTrue(any(step.get("run") == suite for step in block["verify-tooling"]["steps"]))
        self.assertFalse(any(step.get("run") == suite for step in block["verify-contracts"]["steps"]))

    def test_ci_keeps_pull_request_and_merge_group_triggers(self) -> None:
        block = triggers(load_workflow(CI_WORKFLOW))
        self.assertIn("pull_request", block)
        self.assertIn("merge_group", block)

    def test_ci_gains_the_reusable_workflow_call_trigger(self) -> None:
        block = triggers(load_workflow(CI_WORKFLOW))
        self.assertIn("workflow_call", block)

    def test_ci_loses_the_unconditional_main_push_trigger(self) -> None:
        block = triggers(load_workflow(CI_WORKFLOW))
        self.assertNotIn("push", block, "a main-push CI trigger duplicates the queue run that release.yml admits")

    def test_ci_keeps_every_verification_owner_and_required_name(self) -> None:
        block = jobs(load_workflow(CI_WORKFLOW))
        self.assertEqual(sorted(block), sorted(CI_JOB_IDS))
        for job_id in CI_JOB_IDS:
            job = block[job_id]
            self.assertIsInstance(job, dict, f"{job_id} must be a mapping")
            name = job.get("name", job_id)
            self.assertEqual(name, job_id, f"{job_id} must keep its required check name")

    def test_ci_aggregate_gate_keeps_its_dependency_and_always_condition(self) -> None:
        block = jobs(load_workflow(CI_WORKFLOW))
        verify = block["verify"]
        self.assertEqual(verify.get("needs"), ["verify-history", "verify-contracts", "verify-adapter", "verify-tooling", "verify-upstream-compatibility"])
        self.assertEqual(str(verify.get("if")).strip(), "always()")

    def test_ci_test_shards_cover_each_package_and_gate_verify_tests(self) -> None:
        # Each shard job of a package deals its own index of one shared count
        # through go-test-shard.py, so together the jobs run every test once.
        # verify-tests is the required check, so it must need every shard job
        # and fail unless each one succeeded.
        block = jobs(load_workflow(CI_WORKFLOW))
        shard_run = re.compile(r'go-test-shard\.py (\S+) (\d+) (\d+)\)" (\S+)$')
        shards: dict[str, list[tuple[str, int, int]]] = {}
        for job_id, job in block.items():
            for step in job.get("steps", []):
                match = shard_run.search(str(step.get("run", "")))
                if match:
                    package, index, count, tested = match.groups()
                    self.assertEqual(package, tested, f"{job_id} must test the package it shards")
                    shards.setdefault(package, []).append((job_id, int(index), int(count)))
        self.assertEqual(sorted(shards), ["./cmd/concord", "./internal/agent", "./internal/store"])
        self.assertEqual({count for _, _, count in shards["./internal/store"]}, {STORE_SHARDS})
        for package, entries in shards.items():
            counts = {count for _, _, count in entries}
            self.assertEqual(len(counts), 1, f"{package} shards disagree on the count: {sorted(counts)}")
            self.assertEqual(sorted(index for _, index, _ in entries), list(range(1, counts.pop() + 1)), package)
        gate = block["verify-tests"]
        shard_jobs = sorted(job_id for entries in shards.values() for job_id, _, _ in entries)
        self.assertEqual(sorted(needs_list(gate)), sorted(["verify-tests-rest", *shard_jobs, "verify-upstream-compatibility"]))
        gate_run = "\n".join(str(step.get("run", "")) for step in gate.get("steps", []))
        gate_env = {key: value for step in gate.get("steps", []) for key, value in (step.get("env") or {}).items()}
        for job_id in needs_list(gate):
            variables = [key for key, value in gate_env.items() if value == f"${{{{ needs.{job_id}.result }}}}"]
            self.assertEqual(len(variables), 1, f"verify-tests must read {job_id}'s result once")
            self.assertIn(f'test "${variables[0]}" = success', gate_run)

    def test_store_shards_preserve_preparation_poison_and_result_retention(self) -> None:
        block = jobs(load_workflow(CI_WORKFLOW))
        reference = None
        for index in range(1, STORE_SHARDS + 1):
            steps = [dict(step) for step in block[f"verify-tests-store-{index}"]["steps"]]
            for step in steps:
                if step.get("name", "").startswith("Test shard"):
                    step["name"] = "Test store shard"
                    step["run"] = re.sub(r"(go-test-shard\.py ./internal/store) \d+ \d+", r"\1 INDEX COUNT", step["run"])
            if reference is None:
                reference = steps
            self.assertEqual(steps, reference, f"store shard {index} changed more than its selection")

    def test_ci_keeps_a_per_run_concurrency_group(self) -> None:
        document = load_workflow(CI_WORKFLOW)
        concurrency = document.get("concurrency")
        self.assertIsInstance(concurrency, dict)
        self.assertIn("group", concurrency)
        self.assertIn("cancel-in-progress", concurrency)

    def test_tooling_checkout_clones_full_history_for_the_lane_generator(self) -> None:
        # scripts/generate-agent-lanes.py walks committed manifest history and
        # refuses a shallow clone, so verify-tooling must check out with
        # fetch-depth: 0 like its verify-contracts sibling.
        steps = jobs(load_workflow(CI_WORKFLOW))["verify-tooling"].get("steps")
        self.assertIsInstance(steps, list)
        checkouts = [
            step for step in steps if isinstance(step, dict) and "actions/checkout" in str(step.get("uses", ""))
        ]
        self.assertEqual(len(checkouts), 1, "verify-tooling must keep exactly one actions/checkout step")
        with_block = checkouts[0].get("with")
        self.assertIsInstance(with_block, dict)
        fetch_depth = with_block.get("fetch-depth")
        self.assertEqual(
            fetch_depth,
            0,
            "verify-tooling's checkout must set fetch-depth: 0; the lane generator requires a full Git checkout",
        )
        self.assertIs(type(fetch_depth), int, "fetch-depth must parse as a YAML integer")
        self.assertIs(with_block.get("persist-credentials"), False)


class NativeRecipePrerequisiteTests(unittest.TestCase):
    STEP = "Provision the committed adapter recipe Python"

    def recipe_steps(self):
        block = jobs(load_workflow(CI_WORKFLOW))
        consumers = [block[f"verify-tests-store-{index}"] for index in range(1, STORE_SHARDS + 1)]
        consumers += list(jobs(load_workflow(WORKFLOW_DIR / "nightly.yml")).values())
        for job in consumers:
            steps = job["steps"]
            matches = [(i, step) for i, step in enumerate(steps) if step.get("name") == self.STEP]
            self.assertEqual(len(matches), 1, "Every store shard and the full-tree nightly need the recipe runtime")
            index, step = matches[0]
            warm = next(i for i, s in enumerate(steps) if s.get("name") == "Warm the uv fixture cache from the committed lock")
            self.assertLess(index, warm)
            self.assertTrue(any("astral-sh/setup-uv@" in s.get("uses", "") for s in steps[:index]))
            yield step["run"]

    def test_every_store_consumer_provisions_the_recipe_before_offline_tests(self):
        commands = list(self.recipe_steps())
        self.assertEqual(len(commands), STORE_SHARDS + 1)
        self.assertEqual(len(set(commands)), 1)

    def test_provisioning_reads_pin_publishes_python3_and_refuses_unavailable_runtime(self):
        command = next(self.recipe_steps())
        version = json.loads((ROOT / ".concord/oracles/adapter/native-acceptance.v1.json").read_text())["pin"]["python"]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "bin"
            binary.mkdir()
            managed = root / "managed"
            managed.mkdir()
            uv = binary / "uv"
            uv.write_text(f"#!{sys.executable}\n" + """import json, os, sys
with open(os.environ['PROBE_LOG'], 'a') as log:
    log.write(json.dumps(sys.argv[1:]) + '\\n')
if sys.argv[1:3] == ['python', 'install']:
    sys.exit(int(os.environ.get('INSTALL_EXIT', '0')))
if os.environ.get('FIND_EXIT'):
    sys.exit(int(os.environ['FIND_EXIT']))
print(os.environ['MANAGED_PYTHON'])
""")
            uv.chmod(0o700)
            python = managed / "python3"
            python.write_text('#!/bin/sh\nprintf "Python %s\\n" "$RECIPE_VERSION"\n')
            python.chmod(0o700)
            env = dict(os.environ, PATH=f"{binary}:{os.environ['PATH']}", GITHUB_PATH=str(root / "path"),
                       MANAGED_PYTHON=str(python), PROBE_LOG=str(root / "calls"), RECIPE_VERSION=version)
            result = subprocess.run(["bash", "-euo", "pipefail", "-c", command], cwd=ROOT, env=env,
                                    capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((root / "path").read_text().strip(), str(managed))
            self.assertEqual([json.loads(line) for line in (root / "calls").read_text().splitlines()],
                             [["python", "install", "--no-bin", version], ["python", "find", "--managed-python", version]])
            (root / "path").unlink()
            for override in ({"INSTALL_EXIT": "7"}, {"FIND_EXIT": "8"}, {"RECIPE_VERSION": "0.0.0"}):
                result = subprocess.run(["bash", "-euo", "pipefail", "-c", command], cwd=ROOT,
                                        env=dict(env, **override), capture_output=True, text=True, timeout=10)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((root / "path").exists(), "A failed runtime must never enter PATH")


class AdapterCISuiteTests(unittest.TestCase):
    def test_aggregate_adapter_gate_refuses_every_non_success_result(self) -> None:
        gate = jobs(load_workflow(CI_WORKFLOW))["verify-adapter"]
        self.assertEqual(needs_list(gate), ["verify-adapter-rest"])
        self.assertEqual(str(gate.get("if")).strip(), "always()")
        self.assertNotIn("continue-on-error", gate)
        steps = gate["steps"]
        self.assertEqual(len(steps), 1)
        step = steps[0]
        self.assertEqual(step["env"], {
            "REST_RESULT": "${{ needs.verify-adapter-rest.result }}",
        })
        for rest in ("success", "failure", "cancelled", "skipped"):
            with self.subTest(rest=rest):
                run = subprocess.run(
                    ["bash", "-euo", "pipefail", "-c", step["run"]],
                    env={**os.environ, "REST_RESULT": rest},
                    capture_output=True, text=True, timeout=10,
                )
                self.assertEqual(run.returncode == 0, rest == "success", run.stdout + run.stderr)

    def test_real_suite_command_executes_each_discovered_test_once(self) -> None:
        workflow = jobs(load_workflow(CI_WORKFLOW))
        shard_ids = needs_list(workflow["verify-adapter"])
        environments = []
        reported = []
        files = (
            "adapter/opencode/session-scope.test.ts",
            "adapter/opencode/ordinary.test.ts",
            "adapter/opencode/nested/session-scope.test.ts",
            "adapter/opencode/nested/new.spec.ts",
        )
        with tempfile.TemporaryDirectory(prefix="adapter-ci-partition-") as temporary:
            root = Path(temporary)
            observed = root / "observed.jsonl"
            # Every discovered test, including nested namesakes, joins without
            # a manually maintained file allowlist.
            for name in (*files, "outside/unrelated.test.ts"):
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(
                    'import { test } from "bun:test";\n'
                    'import { appendFileSync } from "node:fs";\n'
                    'test("records its execution", () => appendFileSync('
                    'process.env.CI_ADAPTER_OBSERVATIONS!, JSON.stringify(import.meta.path) + "\\n"));\n',
                    encoding="utf-8",
                )
            for selection in shard_ids:
                shard = workflow[selection]
                self.assertNotIn("strategy", shard)
                self.assertNotIn("continue-on-error", shard)
                test_steps = [step for step in shard["steps"] if "bun test" in str(step.get("run", ""))]
                self.assertEqual(len(test_steps), 1)
                step = test_steps[0]
                environments.append(step["env"])
                output = root / selection
                output.mkdir()
                run = subprocess.run(
                    ["bash", "-euo", "pipefail", "-c", step["run"]],
                    cwd=root,
                    env={**os.environ, "RUNNER_TEMP": str(output),
                         "CI_ADAPTER_OBSERVATIONS": str(observed)},
                    capture_output=True, text=True, timeout=30,
                )
                self.assertEqual(run.returncode, 0, run.stdout + run.stderr)
                self.assertTrue((output / "adapter-test-timings.json").is_file(), run.stdout + run.stderr)
                report = ET.parse(output / "adapter-test-results.xml").getroot()
                cases = list(report.iter("testcase"))
                self.assertTrue(cases, "Process success cannot replace an empty result report")
                for case in cases:
                    self.assertIsNone(case.find("skipped"))
                    self.assertIsNone(case.find("failure"))
                    self.assertIsNone(case.find("error"))
                    reported.append(str((root / case.attrib["file"]).resolve()))
                uploads = [step for step in shard["steps"] if "actions/upload-artifact@" in step.get("uses", "")]
                self.assertEqual(len(uploads), 2)
                self.assertCountEqual([s["with"]["path"] for s in uploads], [
                    "${{ runner.temp }}/adapter-test-timings.json", "${{ runner.temp }}/adapter-test-results.xml"])
                suffix = selection.removeprefix("verify-adapter-")
                self.assertCountEqual([s["with"]["name"] for s in uploads], [
                    f"adapter-test-timings-{suffix}", f"adapter-test-results-{suffix}"])
                for upload in uploads:
                    self.assertEqual(str(upload.get("if")).strip(), "always()")
                    self.assertEqual(upload["with"]["if-no-files-found"], "error")
                    self.assertEqual(upload["with"]["retention-days"], 90)
            self.assertCountEqual(
                [json.loads(line) for line in observed.read_text(encoding="utf-8").splitlines()],
                [str(root / name) for name in files],
            )
            self.assertCountEqual(reported, [str(root / name) for name in files])
        self.assertEqual(len(environments), 1)
        self.assertEqual(environments[0]["CONCORD_SELECTED_PRODUCT_ID"], "poison-product")
        self.assertEqual(environments[0]["CONCORD_DB_PATH"], "/nonexistent/poison.db")


class BunNativeResultTests(unittest.TestCase):
    def test_pinned_native_results_keep_process_and_case_outcomes_distinct(self) -> None:
        workflow = jobs(load_workflow(CI_WORKFLOW))
        pins = [step["with"]["bun-version"]
                for name in ("verify-adapter-rest",)
                for step in workflow[name]["steps"] if step.get("uses", "").startswith("oven-sh/setup-bun@")]
        version = subprocess.run(["bun", "--version"], capture_output=True, text=True, timeout=10)
        self.assertEqual(version.returncode, 0, version.stderr)
        self.assertEqual(pins, [version.stdout.strip()], "Use CI's exact Bun version for result fixtures")
        sources = {
            "suite.test.ts": 'import {test, expect} from "bun:test";\n'
                             'test("alpha",()=>expect(2+2).toBe(4));\n'
                             'test.skip("skip-me",()=>{});\n',
            "failed.test.ts": 'import {test, expect} from "bun:test";\n'
                              'test("failure",()=>expect(true).toBe(false));\n',
            "broken.test.ts": 'import { missing syntax !!!',
            "empty.test.ts": 'export const value = 1;\n',
            "hanging.test.ts": 'import {test} from "bun:test";\n'
                               'test("hanging",()=>{process.stdout.write("started\\n");'
                               'return new Promise(()=>{});},30000);\n',
        }
        skipped = {"alpha": "skipped", "skip-me": "skipped"}
        scenarios = (
            ("named", ["suite.test.ts", "-t", "^alpha$"], 0, {"alpha": "pass", "skip-me": "skipped"}),
            ("unmatched", ["suite.test.ts", "-t", "^absent$"], 1, skipped),
            ("skipped", ["suite.test.ts", "-t", "^skip-me$"], 0, skipped),
            ("assertion", ["failed.test.ts"], 1, {"failure": "failure"}),
            ("syntax", ["broken.test.ts"], 1, None),
            ("empty-file", ["empty.test.ts"], 0, None),
            ("empty-shard", ["suite.test.ts", "--shard=2/2"], 0, None),
            ("empty-directory", ["empty-directory/"], 1, None),
            ("allowed-empty", ["empty-directory/", "--pass-with-no-tests"], 0, None),
        )
        with tempfile.TemporaryDirectory(prefix="bun-native-results-") as temporary:
            root = Path(temporary)
            for name, source in sources.items():
                (root / name).write_text(source, encoding="utf-8")
            (root / "empty-directory").mkdir()
            for name, flags, status, expected in scenarios:
                with self.subTest(scenario=name):
                    report = root / f"{name}.xml"
                    run = subprocess.run(
                        ["bun", "test", *flags, "--reporter=junit", f"--reporter-outfile={report}"],
                        cwd=root, capture_output=True, text=True, timeout=10,
                    )
                    self.assertEqual(run.returncode, status, run.stdout + run.stderr)
                    if expected is None:
                        self.assertFalse(report.exists(), run.stdout + run.stderr)
                        continue
                    cases = list(ET.parse(report).getroot().iter("testcase"))
                    self.assertEqual(len(cases), len(expected))
                    observed = {}
                    for case in cases:
                        outcomes = [tag for tag in ("failure", "error", "skipped") if case.find(tag) is not None]
                        self.assertLessEqual(len(outcomes), 1)
                        observed[case.attrib["name"]] = outcomes[0] if outcomes else "pass"
                    self.assertEqual(observed, expected)
            report = root / "interrupted.xml"
            process = subprocess.Popen(
                ["bun", "test", "hanging.test.ts", "--reporter=junit", f"--reporter-outfile={report}"],
                cwd=root, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
            )
            try:
                observed = b""
                deadline = time.monotonic() + 10
                while b"started\n" not in observed:
                    remaining = deadline - time.monotonic()
                    self.assertGreater(remaining, 0, observed)
                    self.assertTrue(select.select([process.stdout], [], [], remaining)[0], observed)
                    chunk = os.read(process.stdout.fileno(), 4096)
                    self.assertTrue(chunk, "Bun exited before starting the fixture")
                    observed += chunk
                    self.assertLessEqual(len(observed), 8192)
            finally:
                process.kill()
                stdout, stderr = process.communicate(timeout=5)
            self.assertNotEqual(process.returncode, 0, stdout + stderr)
            self.assertFalse(report.exists(), "An interrupted process has no completed native report")


class ReleaseWorkflowDriftTests(unittest.TestCase):
    def test_release_starts_only_on_a_push_to_main(self) -> None:
        block = triggers(load_workflow(RELEASE_WORKFLOW))
        self.assertEqual(sorted(block), ["push"])
        self.assertEqual(block["push"], {"branches": ["main"]})

    def test_release_never_triggers_on_workflow_run_again(self) -> None:
        text = RELEASE_WORKFLOW.read_text(encoding="utf-8")
        self.assertNotIn("workflow_run", text, "the workflow_run route would re-create duplicate releases")

    def test_release_binds_every_former_head_sha_reference_to_the_push_sha(self) -> None:
        text = RELEASE_WORKFLOW.read_text(encoding="utf-8")
        self.assertNotIn("github.event.workflow_run.head_sha", text)
        for needle in ("${{ github.sha }}",):
            self.assertIn(needle, text)

    def test_release_owns_the_admission_job_graph(self) -> None:
        block = jobs(load_workflow(RELEASE_WORKFLOW))
        for job_id in (
            "verify-lookup",
            "ci-fallback",
            "admit-verification",
            "prepare",
            "build-and-publish",
        ):
            self.assertIn(job_id, block, f"release.yml must keep the {job_id} job")

    def test_lookup_job_reads_the_api_with_scoped_read_only_permissions(self) -> None:
        job = jobs(load_workflow(RELEASE_WORKFLOW))["verify-lookup"]
        self.assertEqual(job.get("permissions"), {"actions": "read", "contents": "read"})
        self.assertNotIn("needs", job, "the lookup owns the first admission step")
        steps = job.get("steps")
        self.assertIsInstance(steps, list)
        select_steps = [
            step for step in steps if isinstance(step, dict) and "release-evidence.py select" in str(step.get("run", ""))
        ]
        self.assertEqual(len(select_steps), 1, "exactly one select step must run the evidence script")
        env = select_steps[0].get("env")
        self.assertIsInstance(env, dict)
        self.assertEqual(env.get("GH_TOKEN"), "${{ github.token }}")
        self.assertEqual(env.get("TARGET_SHA"), "${{ github.sha }}")
        self.assertEqual(env.get("REPOSITORY"), "${{ github.repository }}")

    def test_fallback_calls_the_same_ci_workflow_file_at_job_level(self) -> None:
        job = jobs(load_workflow(RELEASE_WORKFLOW))["ci-fallback"]
        self.assertEqual(job.get("uses"), "./.github/workflows/ci.yml")
        self.assertNotIn("steps", job)
        self.assertNotIn("secrets", job, "the fallback must not inherit caller secrets")
        self.assertEqual(job.get("permissions"), {"contents": "read"})
        self.assertEqual(needs_list(job), ["verify-lookup"])
        self.assertEqual(
            job.get("if"),
            "needs.verify-lookup.outputs.mode == 'full'",
            "the fallback must run exactly on a selected full route",
        )

    def test_admission_gate_runs_always_and_binds_every_dependency_result(self) -> None:
        job = jobs(load_workflow(RELEASE_WORKFLOW))["admit-verification"]
        self.assertEqual(sorted(needs_list(job)), ["ci-fallback", "verify-lookup"])
        self.assertEqual(str(job.get("if")).strip(), "always()")
        steps = job.get("steps")
        self.assertIsInstance(steps, list)
        gate_steps = [
            step for step in steps if isinstance(step, dict) and "release-evidence.py gate" in str(step.get("run", ""))
        ]
        self.assertEqual(len(gate_steps), 1, "exactly one gate step must evaluate admission")
        env = gate_steps[0].get("env")
        self.assertIsInstance(env, dict)
        self.assertEqual(
            env,
            {
                "LOOKUP_RESULT": "${{ needs.verify-lookup.result }}",
                "MODE": "${{ needs.verify-lookup.outputs.mode }}",
                "FALLBACK_RESULT": "${{ needs.ci-fallback.result }}",
            },
        )

    def test_publication_jobs_depend_on_the_admission_gate(self) -> None:
        block = jobs(load_workflow(RELEASE_WORKFLOW))
        prepare = block["prepare"]
        self.assertEqual(needs_list(prepare), ["admit-verification"])
        self.assertEqual(
            prepare.get("if"),
            "${{ !cancelled() && needs.admit-verification.result == 'success' }}",
            "a refused admission must prevent version computation and publication",
        )
        publish = block["build-and-publish"]
        self.assertEqual(needs_list(publish), ["prepare"])
        self.assertEqual(
            publish.get("if"),
            "${{ !cancelled() && needs.prepare.result == 'success' && needs.prepare.outputs.should_release == 'true' }}",
        )
        self.assertEqual(
            publish.get("permissions"),
            {"contents": "write", "id-token": "write", "attestations": "write"},
        )

    def test_no_other_workflow_triggers_a_duplicate_main_push(self) -> None:
        for workflow in sorted(WORKFLOW_DIR.glob("*.yml")):
            if workflow == RELEASE_WORKFLOW:
                continue
            document = load_workflow(workflow)
            push = triggers(document).get("push")
            if isinstance(push, dict) and "main" in (push.get("branches") or []):
                self.fail(f"{workflow.name} still triggers a push to main; only release.yml may")


def load_release_evidence():
    """Import scripts/release-evidence.py, whose dashed name is not a module."""
    import importlib.util

    spec = importlib.util.spec_from_file_location("release_evidence_for_tests", SCRIPT)
    if spec is None or spec.loader is None:
        raise AssertionError("scripts/release-evidence.py cannot be loaded")
    module = importlib.util.module_from_spec(spec)
    sys.modules["release_evidence_for_tests"] = module
    spec.loader.exec_module(module)
    return module


class RealContractEligibilityTests(unittest.TestCase):
    def test_the_real_ci_contract_stays_statically_reusable(self) -> None:
        """If ci.yml grows a shape select cannot interpret, every release pays the full fallback."""
        release_evidence = load_release_evidence()
        document = release_evidence.strict_yaml_load(CI_WORKFLOW.read_text(encoding="utf-8"))
        names = release_evidence.expected_ci_jobs(document)
        self.assertEqual(sorted(names), sorted(CI_JOB_IDS))


class GateAlgebraTests(unittest.TestCase):
    """Execute the real gate command for every dependency state."""

    ADMIT = {
        ("reuse", "success", "skipped"),
        ("full", "success", "success"),
    }

    REFUSE = {
        # lookup not successful (mode output unset when the job failed)
        ("", "failure", "skipped"),
        ("", "cancelled", "skipped"),
        # reuse route violations
        ("reuse", "success", "success"),
        ("reuse", "success", "failure"),
        ("reuse", "success", "cancelled"),
        # full route violations
        ("full", "success", "skipped"),
        ("full", "success", "failure"),
        ("full", "success", "cancelled"),
        # unknown mode or fabricated results
        ("auto", "success", "skipped"),
        ("reuse", "bogus", "skipped"),
        ("reuse", "success", "bogus"),
    }

    def run_gate(self, mode: str, lookup: str, fallback: str) -> subprocess.CompletedProcess:
        return subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "gate",
                "--mode",
                mode,
                "--lookup-result",
                lookup,
                "--fallback-result",
                fallback,
            ],
            capture_output=True,
            text=True,
        )

    def test_every_admissible_state_admits(self) -> None:
        for mode, lookup, fallback in sorted(self.ADMIT):
            with self.subTest(mode=mode, lookup=lookup, fallback=fallback):
                completed = self.run_gate(mode, lookup, fallback)
                self.assertEqual(completed.returncode, 0, completed.stderr)
                self.assertIn("release admission: admitted", completed.stdout)

    def test_every_other_state_refuses_publication(self) -> None:
        for mode, lookup, fallback in sorted(self.REFUSE):
            with self.subTest(mode=mode, lookup=lookup, fallback=fallback):
                completed = self.run_gate(mode, lookup, fallback)
                self.assertEqual(completed.returncode, 1, completed.stdout)
                self.assertNotIn("admitted", completed.stdout)
                self.assertIn("release admission:", completed.stdout)

    def test_gate_requires_every_dependency_result(self) -> None:
        completed = subprocess.run(
            [sys.executable, str(SCRIPT), "gate", "--mode", "reuse"],
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(completed.returncode, 0)


FIXTURE_CI = """\
name: CI

on:
  pull_request:
  merge_group:
  workflow_call:

jobs:
  verify-history:
    name: verify-history
    runs-on: ubuntu-latest
    steps:
      - run: echo history
  verify:
    name: verify
    needs: [verify-history]
    if: always()
    runs-on: ubuntu-latest
    steps:
      - run: echo aggregate
  verify-tooling:
    name: verify-tooling
    runs-on: ubuntu-latest
    steps:
      - run: echo tooling
"""

FIXTURE_JOB_NAMES = ["verify-history", "verify", "verify-tooling"]

FAKE_GH = """\
#!/usr/bin/env python3
import json
import os
import sys

fixture = os.environ["FAKE_GH_FIXTURE"]
log_path = os.environ["FAKE_GH_LOG"]
arguments = sys.argv[1:]
if not arguments or arguments[0] != "api":
    print(f"fake gh: unexpected argv {arguments!r}", file=sys.stderr)
    raise SystemExit(9)
endpoint = " ".join(arguments[1:])
with open(log_path, "a+", encoding="utf-8") as log:
    log.seek(0)
    already = len(log.read().splitlines())
    log.write(endpoint + "\\n")
fixture_data = json.loads(open(fixture, encoding="utf-8").read())
steps = fixture_data["steps"]
index = already
if index >= len(steps):
    fallback = fixture_data.get("always")
    if fallback is None:
        print(f"fake gh: no fixture step remains for {endpoint!r}", file=sys.stderr)
        raise SystemExit(9)
    step = fallback
else:
    step = steps[index]
if step.get("match") not in endpoint:
    print(
        f"fake gh: step {index} expected {step.get('match')!r} but served {endpoint!r}",
        file=sys.stderr,
    )
    raise SystemExit(9)
if step.get("exit"):
    print(step.get("stderr", "fake gh failure"), file=sys.stderr)
    raise SystemExit(step["exit"])
print(json.dumps(step["body"]))
"""


class SelectEvidenceTests(unittest.TestCase):
    """Execute the real select command against temporary repos and a fake gh."""

    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.addCleanup(self.tempdir.cleanup)
        self.base = Path(self.tempdir.name)
        self.bin = self.base / "bin"
        self.bin.mkdir()
        (self.bin / "gh").write_text(FAKE_GH, encoding="utf-8")
        (self.bin / "gh").chmod(0o755)
        self.fixture = self.base / "gh-fixture.json"
        self.log = self.base / "gh.log"

    # -- fixture repositories -------------------------------------------------

    def git(self, repo: Path, *args: str) -> str:
        return subprocess.check_output(["git", "-C", str(repo), *args], text=True).strip()

    def commit(self, repo: Path, message: str, files: dict[str, str] | None = None) -> str:
        for relpath, content in (files or {}).items():
            path = repo / relpath
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content, encoding="utf-8")
        self.git(repo, "add", "-A")
        subprocess.check_call(
            ["git", "-C", str(repo), "commit", "--quiet", "--allow-empty", "-m", message],
        )
        return self.git(repo, "rev-parse", "HEAD")

    def make_pair(self, ci_yaml: str = FIXTURE_CI, *, side_branch: bool = False) -> tuple[Path, Path, str]:
        origin = self.base / "origin"
        origin.mkdir()
        self.git(origin, "init", "--initial-branch", "main")
        self.git(origin, "config", "user.email", "test@example.invalid")
        self.git(origin, "config", "user.name", "Select Test")
        self.commit(origin, "chore: base")
        clone = self.base / "clone"
        subprocess.check_call(
            ["git", "clone", "--quiet", f"file://{origin}", str(clone)],
        )
        for repo in (origin, clone):
            self.git(repo, "config", "user.email", "test@example.invalid")
            self.git(repo, "config", "user.name", "Select Test")
        if side_branch:
            self.git(origin, "checkout", "--quiet", "-b", "feature")
            target = self.commit(origin, "feat: side branch commit", {".github/workflows/ci.yml": ci_yaml})
            subprocess.check_call(
                ["git", "-C", str(clone), "fetch", "--quiet", "origin", "feature:refs/heads/feature"],
            )
            self.git(clone, "checkout", "--quiet", "feature")
        else:
            target = self.commit(origin, "feat: merged target", {".github/workflows/ci.yml": ci_yaml})
            subprocess.check_call(["git", "-C", str(clone), "pull", "--quiet"])
        self.target = target
        return origin, clone, target

    # -- API fixtures ---------------------------------------------------------

    def run_body(self) -> dict:
        return {
            "id": RUN_ID,
            "run_id": RUN_ID,
            "run_attempt": RUN_ATTEMPT,
            "event": "merge_group",
            "status": "completed",
            "conclusion": "success",
            "head_sha": self.target,
            "workflow_id": WORKFLOW_ID,
            "path": CI_PATH,
            "created_at": CREATED_AT,
            "html_url": RUN_URL,
            "repository": {"id": REPOSITORY_ID},
            "head_repository": {"id": REPOSITORY_ID},
        }

    def job_body(self, index: int, name: str) -> dict:
        return {
            "id": 5000 + index,
            "name": name,
            "run_id": RUN_ID,
            "run_attempt": RUN_ATTEMPT,
            "head_sha": self.target,
            "status": "completed",
            "conclusion": "success",
            "html_url": f"https://example.invalid/jobs/{5000 + index}",
        }

    def jobs_page(self, names: list[str], **override) -> dict:
        page = {
            "total_count": len(names),
            "jobs": [self.job_body(index, name) for index, name in enumerate(names)],
        }
        page.update(override)
        return page

    def api_steps(
        self,
        *,
        runs_body: dict | None = None,
        runs_bodies: list[dict] | None = None,
        jobs_bodies: list[dict] | None = None,
        reread_body: dict | None = None,
        repo_body: dict | None = None,
        workflow_body: dict | None = None,
        jobs_match: str | None = None,
        failures: dict[str, dict] | None = None,
    ) -> dict:
        failures = failures or {}
        steps = [
            failures.get(
                "repo",
                {"match": f"repos/{REPOSITORY}", "body": repo_body or {"id": REPOSITORY_ID, "full_name": REPOSITORY}},
            ),
            failures.get(
                "workflow",
                failures.get(
                    "workflow",
                    {"match": "actions/workflows/ci.yml", "body": workflow_body or {"id": WORKFLOW_ID, "path": CI_PATH}},
                ),
            ),
        ]
        runs_match = f"actions/workflows/ci.yml/runs?event=merge_group&head_sha={self.target}"
        if runs_bodies is not None:
            steps.extend({"match": runs_match, "body": body} for body in runs_bodies)
        else:
            steps.append(
                failures.get(
                    "runs",
                    {"match": runs_match, "body": runs_body or {"total_count": 1, "workflow_runs": [self.run_body()]}},
                ),
            )
        for body in jobs_bodies or [self.jobs_page(FIXTURE_JOB_NAMES)]:
            steps.append(
                failures.get(
                    "jobs",
                    {"match": jobs_match or f"actions/runs/{RUN_ID}/attempts/{RUN_ATTEMPT}/jobs", "body": body},
                ),
            )
        steps.append(
            failures.get(
                "reread",
                {
                    "match": f"actions/runs/{RUN_ID}",
                    "body": reread_body if reread_body is not None else self.run_body(),
                },
            ),
        )
        return {"steps": steps}

    # -- execution ------------------------------------------------------------

    def run_select(
        self,
        clone: Path,
        *,
        repository: str = REPOSITORY,
        target_sha: str | None = None,
        steps: dict | None = None,
        always: dict | None = None,
    ) -> tuple[subprocess.CompletedProcess, dict | None, list[str]]:
        if steps is None and always is None:
            steps = self.api_steps()
        fixture: dict = steps or {"steps": []}
        if always is not None:
            fixture["always"] = always
        self.fixture.write_text(json.dumps(fixture), encoding="utf-8")
        self.log.write_text("", encoding="utf-8")
        output_file = self.base / "receipt.json"
        output_file.unlink(missing_ok=True)
        github_output = self.base / "github-output"
        environment = dict(os.environ)
        environment["PATH"] = f"{self.bin}:{environment['PATH']}"
        environment["FAKE_GH_FIXTURE"] = str(self.fixture)
        environment["FAKE_GH_LOG"] = str(self.log)
        target = target_sha if target_sha is not None else self.target
        completed = subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "select",
                "--repository",
                repository,
                "--target-sha",
                target,
                "--root",
                str(clone),
                "--output-file",
                str(output_file),
                "--github-output",
                str(github_output),
            ],
            capture_output=True,
            text=True,
            env=environment,
        )
        receipt = None
        if output_file.exists():
            receipt = json.loads(output_file.read_text(encoding="utf-8"))
        calls = self.log.read_text(encoding="utf-8").splitlines()
        return completed, receipt, calls

    # -- the reuse route ------------------------------------------------------

    def test_exact_evidence_selects_reuse_and_records_a_receipt(self) -> None:
        origin, clone, target = self.make_pair()
        completed, receipt, calls = self.run_select(clone)
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertIsNotNone(receipt)
        assert receipt is not None
        self.assertEqual(receipt["mode"], "reuse")
        self.assertEqual(receipt["route"], "merge_group_reuse")
        self.assertEqual(receipt["repository_id"], REPOSITORY_ID)
        self.assertEqual(receipt["target_sha"], target)
        self.assertEqual(receipt["source_workflow_id"], WORKFLOW_ID)
        self.assertEqual(receipt["source_run_id"], RUN_ID)
        self.assertEqual(receipt["source_run_attempt"], RUN_ATTEMPT)
        self.assertEqual(receipt["source_run_url"], RUN_URL)
        self.assertEqual(receipt["expected_jobs"], sorted(FIXTURE_JOB_NAMES))
        self.assertEqual(receipt["observed_job_ids"], sorted(5000 + index for index in range(len(FIXTURE_JOB_NAMES))))
        self.assertEqual(receipt["ci_workflow_blob_oid"], self.git(clone, "rev-parse", "HEAD:.github/workflows/ci.yml"))
        self.assertIn("observed_at", receipt)
        github_output = (self.base / "github-output").read_text(encoding="utf-8")
        self.assertEqual(github_output, "mode=reuse\n")
        # The attempt-specific jobs endpoint and the post-jobs re-read must
        # both have been used; a latest-jobs shortcut hides attempt mixing.
        self.assertTrue(any(f"actions/runs/{RUN_ID}/attempts/{RUN_ATTEMPT}/jobs" in call for call in calls), calls)
        self.assertTrue(any(call.endswith(f"actions/runs/{RUN_ID}") for call in calls), calls)

    def test_jobs_split_across_pages_are_fully_accounted(self) -> None:
        origin, clone, target = self.make_pair()
        steps = self.api_steps(
            jobs_bodies=[
                {"total_count": 3, "jobs": [self.job_body(0, "verify-history")]},
                {"total_count": 3, "jobs": [self.job_body(1, "verify"), self.job_body(2, "verify-tooling")]},
            ],
        )
        completed, receipt, _ = self.run_select(clone, steps=steps)
        self.assertEqual(completed.returncode, 0, completed.stderr)
        assert receipt is not None
        self.assertEqual(receipt["mode"], "reuse")

    def test_explicit_root_owns_git_identity_despite_inherited_git_dir(self) -> None:
        origin, clone, target = self.make_pair()
        before = {path.relative_to(origin): path.read_bytes() for path in origin.rglob("*") if path.is_file()}
        with patch.dict(os.environ, {"GIT_DIR": str(origin / ".git")}):
            completed, receipt, _ = self.run_select(clone)
        after = {path.relative_to(origin): path.read_bytes() for path in origin.rglob("*") if path.is_file()}
        self.assertEqual(before, after, "the explicit root must not redirect Git writes into the outer repository")
        self.assertEqual(completed.returncode, 0, completed.stderr)
        assert receipt is not None
        self.assertEqual(receipt["mode"], "reuse")
        self.assertEqual(receipt["target_sha"], target)

    def test_diagnostic_metadata_cannot_override_the_selected_route(self) -> None:
        origin, clone, target = self.make_pair()
        run = self.run_body() | {"status": "queued\nmode=reuse\nignored=", "conclusion": None}
        completed, receipt, _ = self.run_select(
            clone, steps=self.api_steps(runs_body={"total_count": 1, "workflow_runs": [run]})
        )
        self.assertEqual(completed.returncode, 0, completed.stderr)
        assert receipt is not None
        self.assertEqual(receipt["mode"], "full")
        self.assertIn(run["status"], receipt["reason"])
        self.assertEqual((self.base / "github-output").read_text(encoding="utf-8"), "mode=full\n")

    # -- eligibility misses select the full fallback --------------------------

    def assert_full(self, clone: Path, reason_part: str | None = None, **kwargs) -> None:
        completed, receipt, _ = self.run_select(clone, **kwargs)
        self.assertEqual(completed.returncode, 0, completed.stderr)
        assert receipt is not None
        self.assertEqual(receipt["mode"], "full")
        self.assertEqual(receipt["route"], "full_ci")
        if reason_part is not None:
            self.assertIn(reason_part, receipt["reason"])
        return None

    def test_no_run_for_the_target_selects_full(self) -> None:
        origin, clone, target = self.make_pair()
        self.assert_full(
            clone,
            f"no merge_group run of the CI workflow tested {target}",
            steps=self.api_steps(runs_body={"total_count": 0, "workflow_runs": []}),
        )

    def test_pull_request_event_is_not_queue_evidence(self) -> None:
        origin, clone, target = self.make_pair()
        run = self.run_body() | {"event": "pull_request"}
        self.assert_full(
            clone,
            "no merge_group run",
            steps=self.api_steps(runs_body={"total_count": 1, "workflow_runs": [run]}),
        )

    def test_same_tree_different_sha_run_is_not_evidence(self) -> None:
        origin, clone, target = self.make_pair()
        run = self.run_body() | {"head_sha": OTHER_SHA}
        self.assert_full(
            clone,
            "no merge_group run",
            steps=self.api_steps(runs_body={"total_count": 1, "workflow_runs": [run]}),
        )

    def test_job_testing_a_different_sha_is_not_evidence(self) -> None:
        origin, clone, target = self.make_pair()
        job = self.job_body(0, "verify-history") | {"head_sha": OTHER_SHA}
        self.assert_full(
            clone,
            "not the target",
            steps=self.api_steps(jobs_bodies=[{"total_count": 3, "jobs": [job] + [self.job_body(i, n) for i, n in enumerate(FIXTURE_JOB_NAMES[1:], 1)]}]),
        )

    def test_foreign_repository_or_workflow_identity_is_not_evidence(self) -> None:
        origin, clone, target = self.make_pair()
        foreign_repo = self.run_body() | {"repository": {"id": 640640}}
        self.assert_full(
            clone,
            "no merge_group run",
            steps=self.api_steps(runs_body={"total_count": 1, "workflow_runs": [foreign_repo]}),
        )
        foreign_head = self.run_body() | {"head_repository": {"id": 640640}}
        self.assert_full(
            clone,
            "no merge_group run",
            steps=self.api_steps(runs_body={"total_count": 1, "workflow_runs": [foreign_head]}),
        )
        other_workflow = self.run_body() | {"workflow_id": WORKFLOW_ID + 7}
        self.assert_full(
            clone,
            "no merge_group run",
            steps=self.api_steps(runs_body={"total_count": 1, "workflow_runs": [other_workflow]}),
        )
        wrong_path = self.api_steps(workflow_body={"id": WORKFLOW_ID, "path": ".github/workflows/other.yml"})
        self.assert_full(clone, "api-error", steps=wrong_path)

    def test_newer_failed_run_conceals_no_older_success(self) -> None:
        origin, clone, target = self.make_pair()
        older = self.run_body() | {"id": RUN_ID - 1, "run_id": RUN_ID - 1, "created_at": "2026-10-09T07:00:00Z"}
        newer = self.run_body() | {"status": "completed", "conclusion": "failure"}
        self.assert_full(
            clone,
            "completed/failure",
            steps=self.api_steps(runs_body={"total_count": 2, "workflow_runs": [newer, older]}),
        )

    def test_pending_latest_run_selects_full(self) -> None:
        origin, clone, target = self.make_pair()
        run = self.run_body() | {"status": "in_progress", "conclusion": None}
        self.assert_full(
            clone,
            "in_progress",
            steps=self.api_steps(runs_body={"total_count": 1, "workflow_runs": [run]}),
        )

    def test_missing_skipped_neutral_failed_and_duplicate_owners_select_full(self) -> None:
        origin, clone, target = self.make_pair()
        cases: list[tuple[str, list[dict], str]] = [
            ("missing-owner", [self.job_body(i, n) for i, n in enumerate(FIXTURE_JOB_NAMES[:-1])], "omits expected jobs"),
            ("skipped-owner", [self.job_body(0, "verify-history") | {"conclusion": "skipped"}] + [self.job_body(i, n) for i, n in enumerate(FIXTURE_JOB_NAMES[1:], 1)], "completed/skipped"),
            ("neutral-owner", [self.job_body(0, "verify-history") | {"conclusion": "neutral"}] + [self.job_body(i, n) for i, n in enumerate(FIXTURE_JOB_NAMES[1:], 1)], "completed/neutral"),
            ("failed-owner", [self.job_body(1, "verify") | {"conclusion": "failure"}] + [self.job_body(i, n) for i, n in [(0, "verify-history")] + list(enumerate(FIXTURE_JOB_NAMES[2:], 2))], "completed/failure"),
            ("unknown-owner", [self.job_body(3, "verify-mystery")] + [self.job_body(i, n) for i, n in enumerate(FIXTURE_JOB_NAMES)], "unexpected job"),
            (
                "duplicate-owner",
                [self.job_body(0, "verify-history"), self.job_body(3, "verify-history")]
                + [self.job_body(i, n) for i, n in enumerate(FIXTURE_JOB_NAMES[1:], 1)],
                "duplicate owners",
            ),
            ("duplicate-job-id", [self.job_body(0, "verify-history"), self.job_body(0, "verify")] + [self.job_body(i, n) for i, n in enumerate(FIXTURE_JOB_NAMES[2:], 2)], "appears twice"),
        ]
        for label, jobs, reason in cases:
            with self.subTest(case=label):
                self.assert_full(
                    clone,
                    reason,
                    steps=self.api_steps(jobs_bodies=[{"total_count": len(jobs), "jobs": jobs}]),
                )

    def test_mixed_attempt_observations_select_full(self) -> None:
        origin, clone, target = self.make_pair()
        stale_attempt_job = self.job_body(0, "verify-history") | {"run_attempt": RUN_ATTEMPT + 1}
        jobs = [stale_attempt_job] + [self.job_body(i, n) for i, n in enumerate(FIXTURE_JOB_NAMES[1:], 1)]
        self.assert_full(
            clone,
            "does not belong to run",
            steps=self.api_steps(jobs_bodies=[{"total_count": 3, "jobs": jobs}]),
        )
        foreign_run_job = self.job_body(0, "verify-history") | {"run_id": RUN_ID + 5}
        jobs = [foreign_run_job] + [self.job_body(i, n) for i, n in enumerate(FIXTURE_JOB_NAMES[1:], 1)]
        self.assert_full(
            clone,
            "does not belong to run",
            steps=self.api_steps(jobs_bodies=[{"total_count": 3, "jobs": jobs}]),
        )

    def test_superseding_rerun_after_reading_jobs_selects_full(self) -> None:
        origin, clone, target = self.make_pair()
        rerun = self.run_body() | {"run_attempt": RUN_ATTEMPT + 1, "status": "queued", "conclusion": None}
        self.assert_full(
            clone,
            "re-read run no longer matches",
            steps=self.api_steps(reread_body=rerun),
        )
        failed_now = self.run_body() | {"conclusion": "failure"}
        self.assert_full(
            clone,
            "completed/failure",
            steps=self.api_steps(reread_body=failed_now),
        )

    def test_truncated_pagination_selects_full(self) -> None:
        origin, clone, target = self.make_pair()
        truncated = self.api_steps(
            jobs_bodies=[
                {"total_count": 5, "jobs": [self.job_body(i, n) for i, n in enumerate(FIXTURE_JOB_NAMES)]},
                {"total_count": 5, "jobs": []},
            ],
        )
        self.assert_full(clone, "pagination is incomplete", steps=truncated)

    def test_total_beyond_the_bounded_budget_selects_full(self) -> None:
        origin, clone, target = self.make_pair()
        filler = [{"id": 1000 + i} for i in range(100)]
        steps = self.api_steps()
        steps["steps"] = steps["steps"][:2]  # repo + workflow only
        always = {
            "match": "actions/workflows/ci.yml/runs",
            "body": {"total_count": 1200, "workflow_runs": filler},
        }
        self.assert_full(clone, "beyond the bounded page budget", steps=steps, always=always)

    def test_api_failure_selects_full_and_names_the_failure(self) -> None:
        origin, clone, target = self.make_pair()
        for stage, match in (
            ("repo", f"repos/{REPOSITORY}"),
            ("workflow", "actions/workflows/ci.yml"),
            ("runs", "runs?event=merge_group"),
            ("jobs", "attempts/1/jobs"),
            ("reread", "actions/runs/" + str(RUN_ID)),
        ):
            with self.subTest(stage=stage):
                steps = self.api_steps(
                    failures={stage: {"match": match, "exit": 1, "stderr": "fake gh: HTTP 403 rate limit"}},
                )
                self.assert_full(clone, "api-error", steps=steps)

    def test_matrix_contract_shape_selects_full(self) -> None:
        matrix = FIXTURE_CI.replace(
            "  verify-tooling:\n    name: verify-tooling\n    runs-on: ubuntu-latest",
            "  verify-tooling:\n    name: verify-tooling\n    runs-on: ubuntu-latest\n    strategy:\n      matrix:\n        os: [ubuntu-latest]",
        )
        origin, clone, target = self.make_pair(ci_yaml=matrix)
        self.assert_full(clone, "matrix")

    def test_conditional_contract_shape_selects_full(self) -> None:
        conditional = FIXTURE_CI.replace(
            "  verify-tooling:\n    name: verify-tooling\n    runs-on: ubuntu-latest",
            "  verify-tooling:\n    name: verify-tooling\n    runs-on: ubuntu-latest\n    if: github.event_name == 'push'",
        )
        origin, clone, target = self.make_pair(ci_yaml=conditional)
        self.assert_full(clone, "condition")

    def test_duplicate_contract_names_select_full(self) -> None:
        duplicate = FIXTURE_CI.replace("    name: verify-tooling", "    name: verify")
        origin, clone, target = self.make_pair(ci_yaml=duplicate)
        self.assert_full(clone, "share one required name")

    # -- target identity refusals ---------------------------------------------

    def assert_refused(self, completed: subprocess.CompletedProcess, reason_part: str) -> None:
        self.assertEqual(completed.returncode, 2, completed.stdout)
        self.assertIn("refusing publication", completed.stderr)
        self.assertIn(reason_part, completed.stderr)
        self.assertFalse((self.base / "receipt.json").exists(), "a refusal must not emit a mode receipt")

    def test_non_sha_target_argument_is_refused(self) -> None:
        origin, clone, target = self.make_pair()
        completed, _, calls = self.run_select(clone, target_sha="main")
        self.assert_refused(completed, "not a full lowercase commit SHA")
        self.assertEqual(calls, [])

    def test_checkout_that_is_not_the_target_is_refused(self) -> None:
        origin, clone, target = self.make_pair()
        base = self.git(clone, "rev-parse", "HEAD~1")
        completed, _, calls = self.run_select(clone, target_sha=base)
        self.assert_refused(completed, "is not the release target")
        self.assertEqual(calls, [])

    def test_target_off_main_is_refused(self) -> None:
        origin, clone, target = self.make_pair(side_branch=True)
        completed, _, calls = self.run_select(clone)
        self.assert_refused(completed, "not reachable from origin/main")
        self.assertEqual(calls, [])

    def test_missing_contract_at_target_is_refused(self) -> None:
        origin, clone, target = self.make_pair()
        (clone / ".github" / "workflows" / "ci.yml").unlink()
        completed, receipt, _ = self.run_select(clone)
        self.assertEqual(completed.returncode, 0, completed.stderr)
        assert receipt is not None
        self.assertEqual(receipt["mode"], "reuse")
        self.assertEqual(receipt["expected_jobs"], sorted(FIXTURE_JOB_NAMES))

    def test_unreadable_contract_at_target_is_refused(self) -> None:
        origin, clone, target = self.make_pair(ci_yaml="on: [push\njobs: {{{{")
        completed, _, calls = self.run_select(clone)
        self.assert_refused(completed, "cannot be read")
        self.assertEqual(calls, [])

    # -- bounded-safety regressions: parent-reviewed gap obligations ----------
    # These obligations exist in the accepted design (exact target identity,
    # unambiguous attempt, linked canonical proof, fully accounted
    # populations). They are regressed here against malformed or lying API
    # input: every case must select the complete fallback, never raise, and
    # never emit a reuse receipt.

    def test_expected_population_derives_from_the_committed_target_blob(self) -> None:
        """The target's Git blob owns the expected population, not the working tree.

        select reads the CI contract from the checkout's working tree while
        recording the HEAD blob OID in its receipt. A working tree that drops
        a committed expected job must not shrink the contract: evidence that
        matches only the reduced working tree is a miss against the target's
        committed population, never a reuse.
        """
        origin, clone, target = self.make_pair()
        committed = (clone / ".github" / "workflows" / "ci.yml").read_text(encoding="utf-8")
        tooling_block = (
            "  verify-tooling:\n"
            "    name: verify-tooling\n"
            "    runs-on: ubuntu-latest\n"
            "    steps:\n"
            "      - run: echo tooling\n"
        )
        self.assertIn(tooling_block, committed)
        (clone / ".github" / "workflows" / "ci.yml").write_text(
            committed.replace(tooling_block, ""), encoding="utf-8"
        )
        reduced_names = ["verify-history", "verify"]
        steps = self.api_steps(
            jobs_bodies=[
                {
                    "total_count": len(reduced_names),
                    "jobs": [self.job_body(i, n) for i, n in enumerate(reduced_names)],
                }
            ]
        )
        self.assert_full(clone, "verify-tooling", steps=steps)

    def test_reread_run_with_a_conflicting_identity_selects_full(self) -> None:
        """A re-read that keeps the attempt but lies about identity voids reuse.

        The re-read is the last observation before admission. Matching only
        its attempt, status, and conclusion would admit a run that no longer
        names this target, event, workflow, path, repository, or id.
        """
        origin, clone, target = self.make_pair()
        cases = {
            "head-sha": {"head_sha": OTHER_SHA},
            "event": {"event": "pull_request"},
            "workflow-id": {"workflow_id": WORKFLOW_ID + 7},
            "workflow-path": {"path": ".github/workflows/other.yml"},
            "repository-id": {"repository": {"id": 640640}},
            "head-repository-id": {"head_repository": {"id": 640640}},
            "run-id": {"id": RUN_ID + 99, "run_id": RUN_ID + 99},
        }
        for label, override in sorted(cases.items()):
            with self.subTest(case=label):
                self.assert_full(clone, steps=self.api_steps(reread_body=self.run_body() | override))

    def test_malformed_page_totals_select_full_never_reuse(self) -> None:
        origin, clone, target = self.make_pair()
        with self.subTest(case="runs-total-bool"):
            # True satisfies isinstance(int) and equals 1, so it slips through
            # numeric accounting while being no population count at all.
            self.assert_full(
                clone,
                steps=self.api_steps(runs_body={"total_count": True, "workflow_runs": [self.run_body()]}),
            )
        with self.subTest(case="jobs-total-bool"):
            page = self.jobs_page(FIXTURE_JOB_NAMES) | {"total_count": True}
            self.assert_full(clone, steps=self.api_steps(jobs_bodies=[page]))
        with self.subTest(case="runs-total-negative"):
            self.assert_full(
                clone,
                steps=self.api_steps(runs_body={"total_count": -5, "workflow_runs": [self.run_body()]}),
            )
        with self.subTest(case="jobs-total-negative"):
            page = self.jobs_page(FIXTURE_JOB_NAMES) | {"total_count": -5}
            self.assert_full(clone, steps=self.api_steps(jobs_bodies=[page]))
        with self.subTest(case="totals-change-across-pages"):
            older = self.run_body() | {"id": RUN_ID - 1, "run_id": RUN_ID - 1, "created_at": "2026-10-09T07:00:00Z"}
            newer = self.run_body()
            self.assert_full(
                clone,
                steps=self.api_steps(
                    runs_bodies=[
                        {"total_count": 2, "workflow_runs": [older]},
                        {"total_count": 1, "workflow_runs": [newer]},
                    ]
                ),
            )

    def test_nonobject_collection_entries_select_full_never_reuse(self) -> None:
        origin, clone, target = self.make_pair()
        with self.subTest(case="runs-entry"):
            self.assert_full(
                clone,
                steps=self.api_steps(
                    runs_body={"total_count": 1, "workflow_runs": [self.run_body(), "not-a-run"]}
                ),
            )
        with self.subTest(case="jobs-entry"):
            page = self.jobs_page(FIXTURE_JOB_NAMES) | {
                "jobs": [self.job_body(i, n) for i, n in enumerate(FIXTURE_JOB_NAMES)] + [42]
            }
            self.assert_full(clone, steps=self.api_steps(jobs_bodies=[page]))

    def test_malformed_native_identifiers_select_full_never_reuse(self) -> None:
        origin, clone, target = self.make_pair()

        def jobs_with(index: int, **override) -> dict:
            jobs = [self.job_body(i, n) for i, n in enumerate(FIXTURE_JOB_NAMES)]
            jobs[index] = jobs[index] | override
            return {"total_count": len(jobs), "jobs": jobs}

        for label, override in (
            ("job-id-none", {"id": None}),
            ("job-id-zero", {"id": 0}),
            ("job-id-negative", {"id": -3}),
            ("job-id-string", {"id": "113677363241"}),
            ("job-id-bool", {"id": True}),
            ("job-id-unhashable", {"id": ["not-a-job-id"]}),
        ):
            with self.subTest(case=label):
                self.assert_full(clone, steps=self.api_steps(jobs_bodies=[jobs_with(0, **override)]))

        for label, attempt in (
            ("attempt-zero", 0),
            ("attempt-bool", True),
            ("attempt-negative", -1),
        ):
            with self.subTest(case=label):
                run = self.run_body() | {"run_attempt": attempt}
                jobs = [self.job_body(i, n) | {"run_attempt": attempt} for i, n in enumerate(FIXTURE_JOB_NAMES)]
                self.assert_full(
                    clone,
                    steps=self.api_steps(
                        runs_body={"total_count": 1, "workflow_runs": [run]},
                        jobs_bodies=[{"total_count": len(jobs), "jobs": jobs}],
                        jobs_match=f"actions/runs/{RUN_ID}/attempts/{attempt}/jobs",
                        reread_body=run,
                    ),
                )

        with self.subTest(case="run-id-bool"):
            run = self.run_body() | {"id": True}
            self.assert_full(
                clone,
                steps=self.api_steps(
                    runs_body={"total_count": 1, "workflow_runs": [run]},
                    jobs_match=f"actions/runs/True/attempts/{RUN_ATTEMPT}/jobs",
                ),
            )

    def test_missing_native_proof_urls_select_full_never_reuse(self) -> None:
        """A reuse receipt claims linked canonical proof; a missing URL is no proof."""
        origin, clone, target = self.make_pair()
        run = self.run_body()
        del run["html_url"]
        with self.subTest(case="run-without-url"):
            self.assert_full(
                clone,
                steps=self.api_steps(runs_body={"total_count": 1, "workflow_runs": [run]}, reread_body=run),
            )
        with self.subTest(case="run-null-url"):
            self.assert_full(
                clone,
                steps=self.api_steps(reread_body=self.run_body() | {"html_url": None}),
            )
        with self.subTest(case="job-null-url"):
            jobs = [self.job_body(i, n) for i, n in enumerate(FIXTURE_JOB_NAMES)]
            jobs[1] = jobs[1] | {"html_url": None}
            self.assert_full(clone, steps=self.api_steps(jobs_bodies=[{"total_count": 3, "jobs": jobs}]))
        with self.subTest(case="job-nonstring-url"):
            jobs = [self.job_body(i, n) for i, n in enumerate(FIXTURE_JOB_NAMES)]
            jobs[2] = jobs[2] | {"html_url": 42}
            self.assert_full(clone, steps=self.api_steps(jobs_bodies=[{"total_count": 3, "jobs": jobs}]))


if __name__ == "__main__":
    unittest.main()
