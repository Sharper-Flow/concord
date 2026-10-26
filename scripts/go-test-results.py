#!/usr/bin/env python3
"""Retain one existing Go test invocation and read its native JSON results.

read_go_results(path, exit_code, selection) is the shared consumer boundary.
selection accepts expected_tests ({package: [exact names]}) and compile_only.
An absent expected_tests makes no named-selection claim. Test elapsed values
include nested/parallel work: they must never be summed into wall time.
"""

from __future__ import annotations

import argparse
from collections import Counter
from datetime import datetime, timezone
import hashlib
import json
import math
import os
from pathlib import Path
import re
import selectors
import signal
import subprocess
import sys
import time


TERMINALS = {"pass", "fail", "skip"}
ACTIONS = TERMINALS | {"start", "run", "pause", "cont", "output", "bench", "attr", "artifacts", "build-output", "build-fail"}
LIMIT = 50


def read_go_results(raw_path: Path | str, process_exit_code: int | None,
                    selection: dict | None = None) -> dict:
    """Read retained JSON, not a command or a workflow verdict; no execution."""
    selection = selection or {}
    packages: dict[str, dict] = {}
    errors = []
    build_failures = set()
    event_count = 0
    with Path(raw_path).open(encoding="utf-8", errors="replace") as stream:
        for number, line in enumerate(stream, 1):
            try:
                event = json.loads(line)
                if not isinstance(event, dict) or event.get("Action") not in ACTIONS:
                    raise ValueError("unknown or missing event action")
                action = event["Action"]
                name = event.get("Test")
                package = event.get("Package")
                elapsed = event.get("Elapsed")
                if elapsed is not None and (type(elapsed) not in (int, float) or not math.isfinite(elapsed) or elapsed < 0):
                    raise ValueError("invalid elapsed seconds")
                if action.startswith("build-"):
                    if not isinstance(event.get("ImportPath"), str):
                        raise ValueError("missing build import path")
                    if action == "build-fail":
                        build_failures.add(event["ImportPath"])
                    event_count += 1
                    continue
                if not isinstance(package, str) or not package or (name is not None and not isinstance(name, str)):
                    raise ValueError("missing package or invalid test name")
                if "Output" in event and not isinstance(event["Output"], str):
                    raise ValueError("invalid output")
            except (ValueError, TypeError) as error:
                errors.append({"line": number, "error": str(error)})
                continue
            event_count += 1
            pkg = packages.setdefault(package, {"package": package, "terminal": None,
                "cached": None, "elapsed_seconds": None, "tests": {}, "no_tests": False})
            output = event.get("Output", "")
            if not name and action == "output":
                if re.search(r"^ok\s+" + re.escape(package) + r"\s+\(cached\)", output, re.MULTILINE):
                    pkg["cached"] = True
                elif re.search(r"^ok\s+" + re.escape(package) + r"\s+[0-9.]+s", output, re.MULTILINE):
                    pkg["cached"] = False
                if "[no tests to run]" in output or "[no test files]" in output:
                    pkg["no_tests"] = True
            if name:
                test = pkg["tests"].setdefault(name, {"name": name, "terminal": None,
                    "elapsed_seconds": None, "level": name.count("/"), "observed_run": False})
                if action == "run":
                    test["observed_run"] = True
                if action in TERMINALS:
                    test["terminal"] = action
                    test["elapsed_seconds"] = elapsed
            elif action in TERMINALS:
                pkg["terminal"] = action
                pkg["elapsed_seconds"] = elapsed

    expected = selection.get("expected_tests", {})
    if not isinstance(expected, dict) or any(not isinstance(p, str) or not isinstance(names, list)
            or any(not isinstance(name, str) for name in names) for p, names in expected.items()):
        raise ValueError("expected_tests must map package names to lists of exact test names")
    missing, skipped = [], []
    for package, names in expected.items():
        tests = packages.get(package, {}).get("tests", {})
        for name in names:
            test = tests.get(name)
            if test is None or test["terminal"] is None or not test["observed_run"]:
                missing.append({"package": package, "name": name})
            elif test["terminal"] == "skip":
                skipped.append({"package": package, "name": name})
    complete = bool(packages) and not errors and process_exit_code is not None
    for pkg in packages.values():
        pkg["complete"] = pkg["terminal"] is not None and all(t["terminal"] is not None and t["observed_run"] for t in pkg["tests"].values())
        complete = complete and pkg["complete"]
        for test in pkg["tests"].values():
            test["measurement"] = "reused" if pkg["cached"] is True else "current" if pkg["cached"] is False else "unknown"
        pkg["tests"] = list(pkg["tests"].values())
    failed = process_exit_code not in (None, 0) or bool(build_failures) or any(
        p["terminal"] == "fail" or any(t["terminal"] == "fail" for t in p["tests"]) for p in packages.values())
    compile_only = bool(selection.get("compile_only", False))
    observed = sum(len(p["tests"]) for p in packages.values())
    outcome = "fail" if failed else "incomplete" if not complete else "compile_only" if compile_only else "selection_unproved" if missing or skipped or not observed else "pass"
    return {"schema_version": 1, "process_exit_code": process_exit_code,
        "complete": complete, "parser_errors": errors, "event_count": event_count,
        "packages": list(packages.values()), "build_failures": sorted(build_failures),
        "selection": {"expected_tests": expected, "named_claim": "expected_tests" in selection,
            "missing": missing, "skipped": skipped}, "compile_only": compile_only, "outcome": outcome}


def bounded_summary(results: dict) -> dict:
    """Keep full results in results.json and diagnostics in the original streams."""
    tests = [{"package": p["package"], **t} for p in results["packages"] for t in p["tests"]]
    slow = sorted((t for t in tests if t["elapsed_seconds"] is not None),
                  key=lambda t: (-t["elapsed_seconds"], t["package"], t["name"]))
    summary = {key: value for key, value in results.items() if key not in {"packages", "parser_errors", "selection", "build_failures"}}
    summary.update({"packages": [{k: v for k, v in p.items() if k != "tests"} for p in results["packages"][:LIMIT]],
        "test_counts": dict(Counter(t["terminal"] or "incomplete" for t in tests)),
        "slow_tests": slow[:LIMIT], "parser_errors": results["parser_errors"][:LIMIT],
        "failed_tests": [{"package": t["package"], "name": t["name"]} for t in tests if t["terminal"] == "fail"][:LIMIT],
        "unfinished_tests": [{"package": t["package"], "name": t["name"]} for t in tests if t["terminal"] is None][:LIMIT],
        "build_failures": results["build_failures"][:LIMIT],
        "selection": {"named_claim": results["selection"]["named_claim"],
            "missing": results["selection"]["missing"][:LIMIT], "skipped": results["selection"]["skipped"][:LIMIT]},
        "total_counts": {"packages": len(results["packages"]), "tests": len(tests),
            "parser_errors": len(results["parser_errors"]), "build_failures": len(results["build_failures"]),
            "missing": len(results["selection"]["missing"]), "skipped": len(results["selection"]["skipped"])},
        "limit_per_list": LIMIT, "full_results": "results.json", "raw_events": "events.jsonl", "stderr": "stderr.log"})
    return summary


def probe(argv: list[str]) -> str | None:
    result = subprocess.run(argv, capture_output=True, text=True, check=False)
    return result.stdout.strip() if result.returncode == 0 else None


def subject() -> dict:
    sha = probe(["git", "rev-parse", "HEAD"])
    status = probe(["git", "status", "--porcelain", "--untracked-files=normal"])
    diff = probe(["git", "diff", "HEAD", "--binary"])
    return {"commit": sha, "dirty": status != "" if status is not None else None,
        "tracked_diff_sha256": hashlib.sha256(diff.encode()).hexdigest() if diff is not None else None}


def write_json(path: Path, value: dict) -> None:
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def capture(argv: list[str], directory: Path, metadata: dict) -> int:
    """Stream both pipes without a shell or a buffering tee hiding child status."""
    started = time.monotonic()
    child = subprocess.Popen(argv, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    previous = {}
    def forward(signum, _frame):
        metadata["interrupted_signal"] = signum
        child.send_signal(signum)
    for signum in (signal.SIGINT, signal.SIGTERM):
        previous[signum] = signal.signal(signum, forward)
    try:
        with selectors.DefaultSelector() as selector, (directory / "events.jsonl").open("wb") as stdout, (directory / "stderr.log").open("wb") as stderr:
            selector.register(child.stdout, selectors.EVENT_READ, (stdout, sys.stdout.buffer))
            selector.register(child.stderr, selectors.EVENT_READ, (stderr, sys.stderr.buffer))
            while selector.get_map():
                for key, _ in selector.select():
                    data = os.read(key.fileobj.fileno(), 65536)
                    if not data:
                        selector.unregister(key.fileobj)
                        continue
                    retained, console = key.data
                    retained.write(data)
                    retained.flush()
                    console.write(data)
                    console.flush()
        rc = child.wait()
    finally:
        for signum, handler in previous.items():
            signal.signal(signum, handler)
        if child.poll() is None:
            child.terminate()
            child.wait()
        child.stdout.close()
        child.stderr.close()
        metadata["command_wall_seconds"] = time.monotonic() - started
    return 128 - rc if rc < 0 else rc


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    command = args.command[1:] if args.command[:1] == ["--"] else args.command
    if command[:2] != ["go", "test"]:
        parser.error("expected -- go test [arguments]")
    argv = command[:2] + ["-json"] + command[2:]
    directory = args.output_dir
    directory.mkdir(parents=True, exist_ok=False)
    go_environment = json.loads(probe(["go", "env", "-json", "GOOS", "GOARCH", "CGO_ENABLED", "GOVERSION", "GOCACHE", "GOFLAGS", "GODEBUG"]) or "null")
    cache_path = go_environment.pop("GOCACHE", None) if go_environment else None
    implicit_inputs = {key: hashlib.sha256(go_environment.pop(key).encode()).hexdigest()
        for key in ("GOFLAGS", "GODEBUG") if go_environment is not None and key in go_environment}
    metadata = {"schema_version": 1, "argv": argv, "requested_argv": command,
        "subject_before": subject(), "started_at": datetime.now(timezone.utc).isoformat(),
        "toolchain": probe(["go", "version"]),
        "go_environment": go_environment, "implicit_go_inputs_sha256": implicit_inputs,
        "ci": {key: os.environ.get(key) for key in ("GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT", "GITHUB_JOB", "GITHUB_SHA", "RUNNER_OS", "RUNNER_ARCH")},
        "cache": {"test_reuse": "native Go cache; see package cached flags", "build_cache_hit": None,
            "build_cache_enabled": cache_path != "off" if cache_path is not None else None},
        "environment": {key: os.environ.get(key) for key in ("UV_OFFLINE", "UV_PYTHON_DOWNLOADS", "CONCORD_CONFORMANCE_LONG", "CONCORD_ACCEPTANCE_RUNNER")},
        "queue_seconds": None, "build_setup_seconds": None, "job_elapsed_seconds": None,
        "diagnostics": {"stdout": "events.jsonl", "stderr": "stderr.log", "results": "results.json"}}
    write_json(directory / "metadata.json", metadata)
    # A hard cancellation may prevent finalization. Retained initial records
    # must still say incomplete, never inherit an earlier invocation's pass.
    initial = {"schema_version": 1, "outcome": "incomplete", "complete": False,
        "process_exit_code": None, "provenance": metadata,
        "raw_events": "events.jsonl", "stderr": "stderr.log"}
    write_json(directory / "summary.json", initial)
    rc = None
    try:
        rc = capture(argv, directory, metadata)
        selection = {"compile_only": "-run=^$" in command or any(a == "-run" and b == "^$" for a, b in zip(command, command[1:]))}
        results = read_go_results(directory / "events.jsonl", rc, selection)
        if metadata.get("interrupted_signal"):
            results["complete"] = False
            results["outcome"] = "fail"
        metadata["subject_after"] = subject()
        before, after = metadata["subject_before"], metadata["subject_after"]
        metadata["source_applicability"] = "unchanged_clean" if before == after and before["dirty"] is False and before["commit"] else "unproved"
        metadata["process_exit_code"] = rc
        metadata["finished_at"] = datetime.now(timezone.utc).isoformat()
        write_json(directory / "results.json", results)
        summary = bounded_summary(results)
        summary["provenance"] = metadata
        write_json(directory / "summary.json", summary)
        text = [f"Go results: {results['outcome']} (process exit {rc})",
            "Durations are per test/subtest, never additive wall time. Cached durations are reused measurements.",
            "Full evidence: events.jsonl, stderr.log, results.json, metadata.json", "", "Slow tests (seconds, measurement, package/test):"]
        text.extend(f"{t['elapsed_seconds']:.3f} {t['measurement']} {t['package']}/{t['name']}" for t in summary["slow_tests"])
        text.extend(["", "Failed or unfinished tests (package/test; counts in summary.json test_counts):"])
        text.extend(f"{t['package']}/{t['name']}" for t in summary["failed_tests"] + summary["unfinished_tests"])
        (directory / "summary.md").write_text("\n".join(text) + "\n", encoding="utf-8")
        print("\n".join(text))
        return rc or (1 if results["parser_errors"] or not results["complete"] else 0)
    except (OSError, ValueError) as error:
        metadata["artifact_error"] = str(error)
        print(f"Go result artifact failure: {error}", file=sys.stderr)
        return rc or (125 if rc is None else 1)
    finally:
        metadata["process_exit_code"] = rc
        metadata.setdefault("finished_at", datetime.now(timezone.utc).isoformat())
        write_json(directory / "metadata.json", metadata)


if __name__ == "__main__":
    raise SystemExit(main())
