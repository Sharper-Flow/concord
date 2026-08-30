#!/usr/bin/env python3
"""Validate the recorded lane eval baseline (issue #212).

The baseline is the only record that the lane prompts were measured, not
merely that a harness exists. A run proves nothing about a later run whose
provenance differs, so the record must name the lane contracts it measured
and the executing model each attempt read back (CD-0058 D2).

This check binds the baseline to the present:

- every registered lane's id, version, and digest in
  contracts/agent-lanes.v1.json appears with the same identity in the
  baseline, so a registry change orphans the baseline loudly;
- every packet under adapter/opencode/evals/packets/ has exactly one
  recorded run carrying a pass or fail outcome and a readback model that
  satisfies the agent-lane-report.v1 shape;
- the seeded-defect packets R6 §5 calls for are present, so the review lane
  is measured against defects it should catch, not only refusal boundaries.

Evals stay advisory (CD-0017 D7): this check asserts the record's structure
and binding, never an outcome. A recorded failure is a measurement, not a
build break.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path

DEFAULT_ROOT = Path(__file__).resolve().parents[1]
MIN_SEEDED = 3
READBACK_MODEL = re.compile(r"^[a-z][a-z0-9_.-]*/[^/ ]+$")


def canonical_digest(lane: dict) -> str:
    body = {key: value for key, value in lane.items() if key != "digest"}
    encoded = json.dumps(body, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()
    return "sha256:" + hashlib.sha256(encoded).hexdigest()


def lane_identity(lane: object) -> tuple[str, int, str]:
    body = dict(lane)
    digest = body.get("digest")
    if not isinstance(digest, str) or not digest.startswith("sha256:"):
        digest = canonical_digest(body)
    return str(body["id"]), int(body["version"]), digest


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=DEFAULT_ROOT, help="repository root to validate")
    args = parser.parse_args()
    root = args.root
    baseline_path = root / "adapter/opencode/evals/baselines/lane-baseline.v1.json"
    manifest_path = root / "contracts/agent-lanes.v1.json"
    packets_dir = root / "adapter/opencode/evals/packets"

    findings: list[str] = []
    try:
        baseline = json.loads(baseline_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        print(f"lane eval baseline is missing or unreadable: {exc}", file=sys.stderr)
        return 1
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        print(f"lane registry manifest is unreadable: {exc}", file=sys.stderr)
        return 1

    if baseline.get("schema_version") != "1.0":
        findings.append("baseline schema_version must be \"1.0\"")
    if not isinstance(baseline.get("generated_at"), str) or not baseline["generated_at"]:
        findings.append("baseline generated_at must be a non-empty string")

    registered = {lane_identity(lane) for lane in manifest["lanes"]}
    recorded = baseline.get("lane_registry")
    if not isinstance(recorded, list):
        findings.append("baseline lane_registry must be an array")
        recorded = []
    seen: set[tuple[str, int, str]] = set()
    for entry in recorded:
        try:
            identity = lane_identity(entry)
        except (KeyError, TypeError, ValueError):
            findings.append("baseline lane_registry entry is malformed")
            continue
        seen.add(identity)
    for identity in sorted(registered):
        if identity not in seen:
            findings.append(f"baseline does not bind registered lane {identity[0]} v{identity[1]} digest {identity[2]}")

    packet_files = sorted(path.name for path in packets_dir.glob("*.json")) if packets_dir.is_dir() else []
    runs = baseline.get("runs")
    if not isinstance(runs, list):
        findings.append("baseline runs must be an array")
        runs = []
    by_packet: dict[str, object] = {}
    for run in runs:
        name = run.get("packet") if isinstance(run, dict) else None
        if not isinstance(name, str) or not name:
            findings.append("baseline run is missing its packet name")
            continue
        if name in by_packet:
            findings.append(f"baseline records packet {name} more than once")
            continue
        by_packet[name] = run
        if run.get("outcome") not in ("pass", "fail"):
            findings.append(f"baseline run for {name} must record outcome pass or fail")
        model = run.get("readback_model")
        if not isinstance(model, str) or not READBACK_MODEL.match(model):
            findings.append(f"baseline run for {name} must record a readback_model matching agent-lane-report.v1")
    for name in packet_files:
        if name not in by_packet:
            findings.append(f"baseline does not record a run for packet {name}")
    for name in by_packet:
        if name not in packet_files:
            findings.append(f"baseline records packet {name}, which no longer exists")

    seeded = [name for name in packet_files if name.startswith("review-seeded-")]
    if len(seeded) < MIN_SEEDED:
        findings.append(f"packet corpus carries {len(seeded)} seeded-defect packets, want at least {MIN_SEEDED} (R6 §5)")

    for finding in findings:
        print(finding)
    if findings:
        print(f"lane eval baseline check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print(
        f"lane eval baseline check passed: {len(by_packet)} run(s) across "
        f"{len(registered)} lane(s), {len(seeded)} seeded-defect packet(s)"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
