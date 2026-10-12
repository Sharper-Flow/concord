#!/usr/bin/env python3
"""Check the native-run status closure, literal migration coverage, and runtime guards."""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from collections.abc import Callable
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
VOCABULARY = Path("contracts/native-run-statuses.v1.json")
SCHEMA_SOURCE = Path("internal/store/schema.go")

NativeStatusLoader = Callable[[Path, list[str]], list[str] | None]


def load_json(root: Path, relative: Path, findings: list[str]):
    try:
        return json.loads((root / relative).read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        findings.append(f"json-load: {relative}: invalid JSON: {exc}")
        return None


def migration_block(source: str) -> str | None:
    match = re.search(r'Version:\s+49,\s+Name:\s+"work_kind_and_native_run_vocabularies",\s+(?:Breaking:\s+true,\s+)?SQL:\s+`(?P<sql>.*?)`,\s+\},', source, re.S)
    return match.group("sql") if match else None


def run_generator(root: Path, findings: list[str]) -> None:
    result = subprocess.run(
        [sys.executable, str(root / "scripts/generate-native-run-statuses.py"), "--check"],
        cwd=root, capture_output=True, text=True, check=False,
    )
    if result.returncode:
        detail = result.stderr.strip() or result.stdout.strip() or f"exit status {result.returncode}"
        findings.append(f"generator-freshness: {detail.replace(chr(10), ' ')}")


def trigger_block(sql: str, name: str) -> str | None:
    match = re.search(rf"CREATE TRIGGER {re.escape(name)}\b(?P<body>.*?)\bEND;", sql, re.S)
    return match.group("body") if match else None


def load_native_statuses(root: Path, findings: list[str]) -> list[str] | None:
    """Derive the native status union from the compiled historical registry.

    The registry's declaration metadata is the authority for which native
    statuses still exist on the replay side, so the union comes from the
    workflow-action-contracts projection's read-only metadata mode, never
    from the public authoring schema or the frozen decoder.
    """
    try:
        result = subprocess.run(
            ["go", "run", "./scripts/workflow-action-contracts", "-native-statuses"],
            cwd=root, capture_output=True, text=True, check=False, timeout=300,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        findings.append(f"native-status-projection: unable to run the projection command: {exc}")
        return None
    if result.returncode:
        detail = result.stderr.strip() or result.stdout.strip() or f"exit status {result.returncode}"
        findings.append(f"native-status-projection: {detail.replace(chr(10), ' ')}")
        return None
    try:
        projection = json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        findings.append(f"native-status-projection: invalid JSON: {exc}")
        return None
    statuses = projection.get("statuses") if isinstance(projection, dict) else None
    if not isinstance(statuses, list) or any(not isinstance(status, str) or not status for status in statuses):
        findings.append("native-status-projection: projection carries no native status union")
        return None
    return statuses


def check(root: Path, load_statuses: NativeStatusLoader | None = None) -> list[str]:
    findings: list[str] = []
    run_generator(root, findings)
    loader = load_statuses or load_native_statuses
    vocabulary = load_json(root, VOCABULARY, findings)
    if not isinstance(vocabulary, dict):
        return findings
    phases = vocabulary.get("phases")
    if not isinstance(phases, list):
        findings.append("vocabulary-shape: phases must be an array")
        return findings
    expected_pairs = {(phase["phase"], status["status"]) for phase in phases for status in phase["statuses"]}
    expected_statuses = sorted({status for _, status in expected_pairs})
    declared_statuses = loader(root, findings)
    if declared_statuses is not None:
        actual_statuses = sorted(set(declared_statuses))
        if actual_statuses != expected_statuses:
            findings.append(f"status-union-closure: expected {expected_statuses}, got {actual_statuses}")
    try:
        source = (root / SCHEMA_SOURCE).read_text(encoding="utf-8")
    except OSError as exc:
        findings.append(f"migration-source: unable to read {SCHEMA_SOURCE}: {exc}")
        return findings
    sql = migration_block(source)
    if sql is None:
        findings.append("migration-coverage: missing migration 49 work_kind_and_native_run_vocabularies")
        return findings
    rows: set[tuple[str, str, int]] = set()
    for match in re.finditer(r"INSERT INTO workflow_native_run_statuses\s*\([^)]*\)\s*VALUES\s*\('([^']+)',\s*'([^']+)',\s*(0|1)\);", sql):
        rows.add((match.group(1), match.group(2), int(match.group(3))))
    expected_rows = {(phase["phase"], status["status"], int(status["failure"])) for phase in phases for status in phase["statuses"]}
    if rows != expected_rows:
        findings.append(f"migration-native-run-rows: expected {sorted(expected_rows)}, got {sorted(rows)}")
    trigger_predicate = re.compile(
        r"WHEN\s+NOT\s+EXISTS\s*\(\s*SELECT\s+1\s+FROM\s+workflow_native_run_statuses\s+"
        r"WHERE\s+phase\s*=\s*NEW\.phase\s+AND\s+status\s*=\s*NEW\.status\s*\)",
        re.I | re.S,
    )
    for trigger in ("workflow_native_runs_status_registry_insert", "workflow_native_runs_status_registry_update"):
        block = trigger_block(sql, trigger)
        if block is None or not trigger_predicate.search(block):
            findings.append(f"migration-trigger-coverage: missing native registry guard {trigger}")
    for trigger in (
        "workflow_native_run_statuses_registry_no_insert",
        "workflow_native_run_statuses_registry_no_update",
        "workflow_native_run_statuses_registry_no_delete",
    ):
        block = trigger_block(sql, trigger)
        if block is None or "workflow_native_run_statuses registry is immutable" not in block:
            findings.append(f"migration-registry-immutability: missing native-run registry guard {trigger}")
    runtime_markers = {
        Path("internal/store/native_runs.go"): "NativeRunStatusAllowed(payload.Phase, payload.Status)",
        Path("internal/store/workflow_dispatch.go"): "NativeRunStatusAllowed(phase, status)",
        Path("internal/agent/mutations.go"): "NativeRunStatusIsFailure(execution.NativeRun.Phase, execution.NativeRun.Status)",
    }
    for relative, marker in runtime_markers.items():
        try:
            source = (root / relative).read_text(encoding="utf-8")
        except OSError as exc:
            findings.append(f"runtime-guard: unable to read {relative}: {exc}")
            continue
        if marker not in source:
            findings.append(f"runtime-guard: {relative} does not use generated vocabulary marker {marker}")
    return findings


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", type=Path, default=ROOT)
    args = parser.parse_args(argv)
    findings = check(args.root.resolve())
    for finding in findings:
        print(finding)
    if findings:
        print(f"native-run status vocabulary check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("native-run status vocabulary check passed", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
