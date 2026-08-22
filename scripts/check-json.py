#!/usr/bin/env python3
"""Parse every repository JSON file without third-party dependencies."""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MAX_FINDINGS = 200
FIXTURE_SOURCE_PREFIX = "https://raw.githubusercontent.com/Sharper-Flow/concord/main/"


def repository_files(pattern: str) -> list[Path]:
    result = subprocess.run(
        ["git", "ls-files", "-co", "--exclude-standard", "--", pattern],
        cwd=ROOT,
        check=True,
        capture_output=True,
        text=True,
    )
    return sorted({ROOT / line for line in result.stdout.splitlines() if line})


def validate_fixture_sources(path: Path, value: object, findings: list[str]) -> None:
    if not isinstance(value, dict) or "fixture_sources" not in value:
        return
    sources = value["fixture_sources"]
    if not isinstance(sources, list) or not all(isinstance(source, str) for source in sources):
        findings.append(f"{path.relative_to(ROOT)}: fixture_sources must be an array of strings")
        return
    for source in sources:
        if not source.startswith(FIXTURE_SOURCE_PREFIX):
            findings.append(f"{path.relative_to(ROOT)}: fixture source is not a canonical public asset: {source}")
            continue
        relative = source.removeprefix(FIXTURE_SOURCE_PREFIX)
        target = ROOT / relative
        if not relative or target.resolve().parent != ROOT / "scenarios" or not target.is_file():
            findings.append(f"{path.relative_to(ROOT)}: missing fixture source asset: {source}")


def main() -> int:
    findings: list[str] = []
    for path in repository_files("*.json"):
        if not path.is_file():
            continue
        try:
            value = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            findings.append(f"{path.relative_to(ROOT)}: {exc}")
            continue
        if not isinstance(value, (dict, list)):
            findings.append(
                f"{path.relative_to(ROOT)}: top-level JSON value must be an object or array"
            )
        if isinstance(value, dict):
            for key in ("schema_version", "$schema", "$id"):
                if key in value and not isinstance(value[key], str):
                    findings.append(
                        f"{path.relative_to(ROOT)}: top-level {key} must be a string"
                    )
        validate_fixture_sources(path, value, findings)

    generator = ROOT / "scripts/generate-agent-contracts.py"
    if generator.is_file():
        checked = subprocess.run([sys.executable, str(ROOT / "scripts/check-agent-contracts.py")], cwd=ROOT, capture_output=True, text=True)
        if checked.returncode:
            findings.append(f"agent contract drift: {checked.stderr.strip() or checked.stdout.strip()}")

    knowledge_checker = ROOT / "scripts/check-knowledge-index.py"
    if knowledge_checker.is_file():
        checked = subprocess.run([sys.executable, str(knowledge_checker)], cwd=ROOT, capture_output=True, text=True)
        if checked.returncode:
            findings.append(f"knowledge index drift: {checked.stdout.strip() or checked.stderr.strip()}")

    floor_checker = ROOT / "scripts/check-floor-readiness.py"
    if floor_checker.is_file():
        checked = subprocess.run([sys.executable, str(floor_checker)], cwd=ROOT, capture_output=True, text=True)
        if checked.returncode:
            findings.append(f"floor readiness drift: {checked.stdout.strip() or checked.stderr.strip()}")

    tx_scope_checker = ROOT / "scripts/check-tx-scope.py"
    if tx_scope_checker.is_file():
        checked = subprocess.run([sys.executable, str(tx_scope_checker)], cwd=ROOT, capture_output=True, text=True)
        if checked.returncode:
            findings.append(f"tx scope drift: {checked.stdout.strip() or checked.stderr.strip()}")

    lane_eval_checker = ROOT / "scripts/check-lane-evals.py"
    if lane_eval_checker.is_file():
        checked = subprocess.run([sys.executable, str(lane_eval_checker)], cwd=ROOT, capture_output=True, text=True)
        if checked.returncode:
            findings.append(f"lane eval drift: {checked.stdout.strip() or checked.stderr.strip()}")

    agent_jobs_checker = ROOT / "scripts/check-agent-jobs.py"
    if agent_jobs_checker.is_file():
        checked = subprocess.run([sys.executable, str(agent_jobs_checker)], cwd=ROOT, capture_output=True, text=True)
        if checked.returncode:
            findings.append(f"agent jobs drift: {checked.stdout.strip() or checked.stderr.strip()}")

    # Deliberately not guarded by is_file(). CD-0047 records which law is proved
    # by which check; a coverage validator that skips itself when absent would
    # let the whole record lapse silently, which is the failure it exists to
    # surface. Its absence is a finding, not a reason to continue.
    law_coverage_checker = ROOT / "scripts/check-law-coverage.py"
    if not law_coverage_checker.is_file():
        findings.append("law coverage checker is missing: scripts/check-law-coverage.py")
    else:
        checked = subprocess.run([sys.executable, str(law_coverage_checker)], cwd=ROOT, capture_output=True, text=True)
        if checked.returncode:
            findings.append(f"law coverage drift: {checked.stdout.strip() or checked.stderr.strip()}")

    # Knowledge closure (issue #295): files-to-records inverse coverage. Runs
    # in warn mode by design — unprocessed documents are not yet a hard fail;
    # the cutover criterion is unprocessed count zero, gated by the operator
    # running the script in --strict mode. The current run is informational.
    knowledge_closure_checker = ROOT / "scripts/check-knowledge-closure.py"
    if knowledge_closure_checker.is_file():
        checked = subprocess.run([sys.executable, str(knowledge_closure_checker)], cwd=ROOT, capture_output=True, text=True)
        if checked.returncode:
            findings.append(f"knowledge closure drift: {checked.stdout.strip() or checked.stderr.strip()}")

    # Doc contract (issue #295 prose extension): outline + Gherkin ACs + STE
    # subset on records in scope. The script self-gates on doc_contract.enforced;
    # when false it runs in report-only mode and exits 0 even with findings.
    # Nesting it under check-json.py would only fire on exit 1, which is
    # exactly when enforcement is true and we want a fail signal.
    doc_contract_checker = ROOT / "scripts/check-doc-contract.py"
    if doc_contract_checker.is_file():
        checked = subprocess.run([sys.executable, str(doc_contract_checker)], cwd=ROOT, capture_output=True, text=True)
        if checked.returncode:
            findings.append(f"doc contract drift: {checked.stdout.strip() or checked.stderr.strip()}")

    for finding in findings[:MAX_FINDINGS]:
        print(finding)
    if len(findings) > MAX_FINDINGS:
        print(f"... {len(findings) - MAX_FINDINGS} additional finding(s) omitted")
    if findings:
        print(f"JSON validation failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("JSON validation passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
