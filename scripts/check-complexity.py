#!/usr/bin/env python3
"""Fail on production functions the complexity budget does not allow.

Go defines no normative complexity limit. gofmt and go vet do not measure it,
and neither the Go blog nor the Google or Uber style guides set a number, so
any threshold here is repository policy rather than Go law. golangci-lint's own
reference configuration recommends 10-20 with a default of 30; this repository
starts far above that because the budget ratchets over existing code instead of
declaring a target it does not meet.

Complexity is computed, not declared. The pinned analysis produces the finding
set, the manifest subtracts its allowances, and anything left is a finding. An
allowance carries the score measured when it was granted and must keep matching
exactly, which is what makes it a budget rather than an exemption:

  grew          the function regressed
  shrank        the allowance overstates the debt and should be revised down
  fell below    the allowance is spent and should be deleted
  disappeared   the allowance names code that no longer exists

Cognitive complexity is used rather than cyclomatic because the two disagree in
a way that matters here. A flat dispatch switch scores high on gocyclo while
reading cheaply; gocognit rewards that shape and penalises nesting, so the ratio
between them separates wide-but-flat functions from genuinely nested ones.
"""

from __future__ import annotations

import json
import os
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
MANIFEST = ROOT / "docs/complexity-budget.v1.json"
SCHEMA = ROOT / "contracts/complexity-budget.schema.json"
TIMEOUT_SECONDS = 900


def reject_duplicate_pairs(pairs: list[tuple[str, object]]) -> dict[str, object]:
    seen: dict[str, object] = {}
    for key, value in pairs:
        if key in seen:
            raise ValueError(f"duplicate key: {key}")
        seen[key] = value
    return seen


def load(path: pathlib.Path, findings: list[str]) -> object:
    try:
        return json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=reject_duplicate_pairs)
    except (OSError, ValueError) as exc:
        findings.append(f"{path.name}: {exc}")
        return None


def identity(record: dict) -> str:
    """Identity as <directory>.<FuncName>, matching the manifest."""
    return f"{os.path.dirname(record['Pos']['Filename'])}.{record['FuncName']}"


def run_analysis(manifest: dict, findings: list[str]) -> list[dict] | None:
    analysis = manifest["analysis"]
    command = [
        "go",
        "run",
        f"{analysis['tool']}@{analysis['version']}",
        *analysis["args"],
        *analysis["roots"],
    ]
    try:
        completed = subprocess.run(
            command, cwd=ROOT, capture_output=True, text=True, timeout=TIMEOUT_SECONDS
        )
    except (OSError, subprocess.SubprocessError) as exc:
        findings.append(f"complexity analysis did not run: {exc}")
        return None

    # gocognit exits 1 whenever -over produced output, which is its normal
    # reporting path and not a failure. An empty stdout with a nonzero exit is.
    if not completed.stdout.strip():
        detail = completed.stderr.strip().splitlines()
        findings.append(f"complexity analysis produced no output: {detail[-1] if detail else 'no stderr'}")
        return None

    try:
        records = json.loads(completed.stdout)
    except ValueError as exc:
        findings.append(f"complexity analysis output is not JSON: {exc}")
        return None
    if not isinstance(records, list):
        findings.append("complexity analysis output is not a list")
        return None
    return records


def validate_manifest(manifest: object, findings: list[str]) -> bool:
    if not isinstance(manifest, dict):
        findings.append("manifest: not an object")
        return False
    if manifest.get("schema_version") != "1.0":
        findings.append("manifest: schema_version must be 1.0")
        return False

    threshold = manifest.get("threshold")
    if not isinstance(threshold, int) or isinstance(threshold, bool) or threshold < 1:
        findings.append("manifest: threshold must be a positive integer")
        return False

    allowances = manifest.get("allowances")
    if not isinstance(allowances, list):
        findings.append("manifest: allowances must be an array")
        return False

    seen: set[str] = set()
    for index, allowance in enumerate(allowances):
        prefix = f"manifest.allowances[{index}]"
        if not isinstance(allowance, dict):
            findings.append(f"{prefix}: not an object")
            continue
        name = allowance.get("function")
        if not isinstance(name, str) or not name:
            findings.append(f"{prefix}: function must be a non-empty string")
            continue
        if name in seen:
            findings.append(f"{prefix}: duplicate allowance for {name}")
        seen.add(name)

        measured = allowance.get("measured")
        if not isinstance(measured, int) or isinstance(measured, bool) or measured < 1:
            findings.append(f"{prefix}: measured must be a positive integer")
        elif measured <= threshold:
            findings.append(
                f"{prefix}: {name} is allowed at {measured}, at or below the threshold of "
                f"{threshold}; delete the allowance rather than record a permitted score"
            )

        state = allowance.get("state")
        if state not in {"outstanding", "unmeasured", "out_of_scope"}:
            findings.append(f"{prefix}: state must be outstanding, unmeasured, or out_of_scope")
        elif state == "outstanding" and not isinstance(allowance.get("issue"), int):
            findings.append(f"{prefix}: an outstanding allowance requires an issue")
        elif state != "outstanding" and "issue" in allowance:
            findings.append(f"{prefix}: issue is only valid for an outstanding allowance")

        reason = allowance.get("reason")
        if not isinstance(reason, str) or len(reason) < 12:
            findings.append(f"{prefix}: reason must explain what makes the function complex")

    return not findings


def compare(manifest: dict, records: list[dict], findings: list[str]) -> None:
    threshold = manifest["threshold"]
    allowed = {a["function"]: a["measured"] for a in manifest["allowances"] if isinstance(a, dict)}
    actual = {identity(record): record["Complexity"] for record in records}

    for name, score in sorted(actual.items(), key=lambda item: -item[1]):
        if name not in allowed:
            if score > threshold:
                findings.append(
                    f"{name} scores {score}, above the threshold of {threshold}, and is not "
                    f"allowed; simplify it or add an allowance with a reason"
                )
            continue
        if score != allowed[name]:
            direction = "grew from" if score > allowed[name] else "fell from"
            findings.append(
                f"{name} {direction} {allowed[name]} to {score}; update its measured score in "
                f"{MANIFEST.name} so the change is reviewed"
            )

    for name in sorted(set(allowed) - set(actual)):
        findings.append(f"{name} is allowed but the analysis no longer reports it; delete the allowance")


def report(findings: list[str]) -> int:
    if not findings:
        print("complexity budget check passed")
        return 0
    print("complexity budget check failed:", file=sys.stderr)
    for finding in findings:
        print(f"  {finding}", file=sys.stderr)
    return 1


def main() -> int:
    findings: list[str] = []
    if not SCHEMA.exists():
        findings.append(f"{SCHEMA.name}: missing contract")
    manifest = load(MANIFEST, findings)
    if findings or manifest is None:
        return report(findings)
    if not validate_manifest(manifest, findings):
        return report(findings)

    records = run_analysis(manifest, findings)
    if records is None:
        return report(findings)

    compare(manifest, records, findings)
    return report(findings)


if __name__ == "__main__":
    raise SystemExit(main())
