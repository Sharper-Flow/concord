#!/usr/bin/env python3
"""Fail on mechanism the product cannot reach unless the repository declares it.

CD-0047 D4. Law binding is a human claim and must be declared. Reachability is
not: deadcode derives it from a real callgraph, which is why this plane
declares only its exceptions and computes everything else. A per-symbol
manifest would restate a fact the toolchain already owns, and would drift from
the code on every refactor.

cmd/concord is the complete entry set. The OpenCode adapter reaches Go only by
executing the binary, so a function unreachable from these mains cannot be
invoked by any operator or agent action.

Textual analysis cannot answer this question here. internal/store is built on
method plus tx-scoped-core pairs — an exported `func (s *Store) X` delegating
to `func X(ctx, s, req)` — so grepping for `.X(` misses every call through the
core and reports live code as dead. deadcode resolves the callgraph properly.

Not every unreachable function is a defect. CD-0013 D11 keeps the workflow
action surface reserved until the engine ships, so its unreachability is
accepted law working as intended. The manifest is how the two categories are
told apart, and declaring an exception is a deliberate act with a reason
attached rather than an absence nobody noticed.
"""
from __future__ import annotations

import os
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))

from coverage_state import (  # noqa: E402
    bounded_text,
    check_state_obligations,
    check_subject_set,
    load_json,
    report,
)

MANIFEST = ROOT / "docs/reachability-exceptions.v1.json"
SCHEMA = ROOT / "contracts/reachability-exceptions.schema.json"

ALLOWED_ROOT = {"schema_version", "analysis", "exceptions"}
ALLOWED_ANALYSIS = {"tool", "version", "args", "entrypoints"}
ALLOWED_EXCEPTION = {"id", "state", "functions", "issue", "reason"}

# deadcode emits: path/to/file.go:LINE:COL: unreachable func: Name
# Line and column are deliberately discarded. Pinning them would make every
# unrelated edit above a declared function a manifest diff, and the identity
# being declared is the function, not its position in the file.
FINDING = re.compile(r"^(?P<file>[^:]+\.go):\d+:\d+: unreachable func: (?P<func>.+)$")

EXCEPTION_ID = re.compile(r"^[a-z0-9]+(?:-[a-z0-9]+)*$")
SYMBOL = re.compile(r"^[a-z0-9/_]+\.[A-Za-z_][A-Za-z0-9_.]*$")

# Only these states describe an intentionally unreachable function. A satisfied
# subject is reachable, and therefore is not an exception at all.
EXCEPTION_STATES = ("outstanding", "unmeasured", "out_of_scope")


def symbol_for(file: str, function: str) -> str:
    """Identify a finding as <package>.<func>, independent of line and file."""
    return f"{Path(file).parent.as_posix()}.{function}"


def run_analysis(analysis: dict, findings: list[str]) -> list[str] | None:
    tool = f"{analysis['tool']}@{analysis['version']}"
    command = ["go", "run", tool, *analysis["args"], *analysis["entrypoints"]]
    try:
        completed = subprocess.run(
            command, cwd=ROOT, capture_output=True, text=True, timeout=900
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        findings.append(f"reachability analysis could not run: {exc}")
        return None
    if completed.returncode:
        detail = (completed.stderr or completed.stdout).strip().splitlines()
        findings.append(
            "reachability analysis failed: " + (detail[-1] if detail else "no output")
        )
        return None

    symbols: list[str] = []
    for line in completed.stdout.splitlines():
        match = FINDING.fullmatch(line.strip())
        if match:
            symbols.append(symbol_for(match.group("file"), match.group("func")))
    return symbols


def check_analysis_block(manifest: dict, findings: list[str]) -> dict | None:
    """Validate the pinned analysis command, returning None if it is unusable."""
    analysis = manifest.get("analysis")
    if not isinstance(analysis, dict):
        findings.append("analysis must be an object")
        return None

    local: list[str] = []
    unknown = set(analysis) - ALLOWED_ANALYSIS
    if unknown:
        local.append(f"analysis has unknown keys: {sorted(unknown)}")
    missing = ALLOWED_ANALYSIS - set(analysis)
    if missing:
        local.append(f"analysis requires {sorted(missing)}")
        findings.extend(local)
        return None

    if not bounded_text(analysis["tool"], 8, 256):
        local.append("analysis.tool must be a trimmed module path")
    # Pinning the version is what makes the finding set reproducible. An
    # unpinned tool would turn a toolchain upgrade into an unexplained CI
    # failure instead of a reviewable manifest diff.
    if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", str(analysis["version"])):
        local.append("analysis.version must be an exact semver tag")
    for key in ("args", "entrypoints"):
        value = analysis[key]
        if not isinstance(value, list) or not value or not all(
            bounded_text(item, 1, 128) for item in value
        ):
            local.append(f"analysis.{key} must be a non-empty array of trimmed strings")

    findings.extend(local)
    return None if local else analysis


def check_schema_states() -> list[str]:
    findings: list[str] = []
    document = load_json(SCHEMA, findings)
    if not isinstance(document, dict):
        return findings
    enum = (
        document.get("properties", {})
        .get("exceptions", {})
        .get("items", {})
        .get("properties", {})
        .get("state", {})
        .get("enum")
    )
    if enum != list(EXCEPTION_STATES):
        findings.append(
            f"{SCHEMA.name}: state enum must equal {list(EXCEPTION_STATES)}, got {enum!r}"
        )
    return findings


def main() -> int:
    findings: list[str] = check_schema_states()
    manifest = load_json(MANIFEST, findings)
    if not isinstance(manifest, dict):
        return report(findings, "reachability")

    unknown = set(manifest) - ALLOWED_ROOT
    if unknown:
        findings.append(f"unknown manifest keys: {sorted(unknown)}")
    if manifest.get("schema_version") != "1.0":
        findings.append("schema_version must be \"1.0\"")

    analysis = check_analysis_block(manifest, findings)
    exceptions = manifest.get("exceptions")
    if not isinstance(exceptions, list):
        findings.append("exceptions must be an array")
        return report(findings, "reachability")

    declared: list[str] = []
    for exception in exceptions:
        if not isinstance(exception, dict):
            findings.append("exception must be an object")
            continue
        identifier = exception.get("id")
        if not isinstance(identifier, str) or not EXCEPTION_ID.fullmatch(identifier):
            findings.append(f"exception id must be kebab-case: {identifier!r}")
            continue
        prefix = f"exception {identifier}"

        unknown_fields = set(exception) - ALLOWED_EXCEPTION
        if unknown_fields:
            findings.append(f"{prefix}: unknown fields: {sorted(unknown_fields)}")

        if exception.get("state") == "satisfied":
            # A reachable function needs no exception, so this state is not
            # merely unused here — it is a category error worth naming.
            findings.append(
                f"{prefix}: state 'satisfied' is meaningless for an exception; "
                "a reachable function is not declared, it is simply reachable"
            )
            continue
        if not check_state_obligations(exception, prefix, findings):
            continue

        functions = exception.get("functions")
        if not isinstance(functions, list) or not functions:
            findings.append(f"{prefix}: functions must be a non-empty array")
            continue
        for symbol in functions:
            if not isinstance(symbol, str) or not SYMBOL.fullmatch(symbol):
                findings.append(f"{prefix}: function must read <package>.<Name>: {symbol!r}")
                continue
            declared.append(symbol)

    if analysis is None:
        return report(findings, "reachability")

    if os.environ.get("CONCORD_SKIP_REACHABILITY_ANALYSIS") == "1":
        # Deliberately narrow: lets the manifest's own shape be tested without
        # a Go toolchain. It never runs in CI, and it cannot hide a finding
        # because skipping produces no verdict at all.
        print("reachability analysis skipped by request; manifest shape checked only")
        return report(findings, "reachability")

    discovered = run_analysis(analysis, findings)
    if discovered is None:
        return report(findings, "reachability")

    check_subject_set(declared, discovered, "unreachable function", findings)
    return report(findings, "reachability")


if __name__ == "__main__":
    raise SystemExit(main())
