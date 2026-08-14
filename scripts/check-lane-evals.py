#!/usr/bin/env python3
"""Validate the CD-0017 D7 lane behavioural eval harness.

The harness itself is advisory: running it needs a model, and its verdicts never
complete a gate. What is checkable offline, and therefore enforced here, is that
the harness still describes the lanes the registry actually declares. A lane
added, retired, re-digested, or re-pinned without a matching eval packet is
drift, and this validator fails on it.

Deliberately not checked: prompt wording, rubric text, or anything requiring
promptfoo to execute. Use `npx promptfoo validate config` for schema validation
of the config document itself.
"""
import json
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
EVALS = ROOT / "adapter/opencode/evals"
CONFIG = EVALS / "promptfooconfig.yaml"
PACKETS = EVALS / "packets"
GENERATED = ROOT / "adapter/opencode/generated-agent-lanes.ts"
AGENTS = ROOT / "adapter/opencode/agents"

PACKET_REQUIRED = (
    "schema_version",
    "attempt_id",
    "lane_id",
    "lane_version",
    "lane_digest",
    "work_id",
    "step_id",
    "inputs",
)
DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")


def load_lanes(findings):
    """Read lanes from the generated projection, which carries derived digests."""
    if not GENERATED.is_file():
        findings.append(f"{GENERATED.relative_to(ROOT)}: missing generated lane projection")
        return []
    text = GENERATED.read_text()
    marker = "export const agentLanes = "
    if marker not in text:
        findings.append(f"{GENERATED.relative_to(ROOT)}: no agentLanes export")
        return []
    body = text.split(marker, 1)[1].split("] as const;", 1)[0] + "]"
    try:
        return json.loads(body)
    except json.JSONDecodeError as error:
        findings.append(f"{GENERATED.relative_to(ROOT)}: agentLanes is not valid JSON ({error})")
        return []


def main():
    findings = []

    if not CONFIG.is_file():
        print(f"adapter/opencode/evals/promptfooconfig.yaml: missing eval configuration", file=sys.stderr)
        return 1
    config = CONFIG.read_text()
    config_lines = {line.strip() for line in config.splitlines()}

    lanes = load_lanes(findings)
    if not lanes:
        for finding in findings:
            print(finding)
        print(f"lane eval check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1

    packet_files = sorted(PACKETS.glob("*.json")) if PACKETS.is_dir() else []
    packets_by_lane = {}

    for path in packet_files:
        rel = path.relative_to(ROOT)
        try:
            packet = json.loads(path.read_text())
        except json.JSONDecodeError as error:
            findings.append(f"{rel}: invalid JSON ({error})")
            continue

        missing = [field for field in PACKET_REQUIRED if field not in packet]
        if missing:
            findings.append(f"{rel}: packet is missing required field(s): {', '.join(missing)}")
            continue
        if packet["schema_version"] != "1.0":
            findings.append(f"{rel}: packet schema_version must be \"1.0\"")
        if not DIGEST.match(str(packet["lane_digest"])):
            findings.append(f"{rel}: lane_digest is not a sha256 digest")
        inputs = packet.get("inputs")
        if not isinstance(inputs, dict) or not isinstance(inputs.get("task"), str) or not inputs["task"]:
            findings.append(f"{rel}: packet inputs.task must be a non-empty string")

        packets_by_lane.setdefault(packet["lane_id"], []).append((rel, packet))

        # The packet must be reachable from the config, or the harness would
        # silently skip it.
        if f"- id: file://packets/{path.name}" not in config_lines:
            findings.append(f"{rel}: packet is not referenced by promptfooconfig.yaml")

    for lane in lanes:
        lane_id = lane["id"]
        found = packets_by_lane.get(lane_id, [])
        if not found:
            findings.append(f"contracts/agent-lanes.v1.json: lane {lane_id} has no eval packet")
            continue

        for rel, packet in found:
            if packet["lane_version"] != lane["version"]:
                findings.append(
                    f"{rel}: lane_version {packet['lane_version']} does not match registry version {lane['version']}"
                )
            if packet["lane_digest"] != lane["digest"]:
                findings.append(f"{rel}: lane_digest does not match the registered digest for lane {lane_id}")

        # Each lane is evaluated through its own pinned model, matching dispatch.
        provider = (
            f"- id: 'exec: opencode run --agent concord-{lane_id}"
            f" --model {lane['pinned_model']} --format json'"
        )
        if provider not in config_lines:
            findings.append(
                f"adapter/opencode/evals/promptfooconfig.yaml: lane {lane_id} has no provider pinned to {lane['pinned_model']}"
            )

        prose = AGENTS / f"concord-{lane_id}.md"
        if not prose.is_file():
            findings.append(f"adapter/opencode/agents/concord-{lane_id}.md: missing lane prose")

    known = {lane["id"] for lane in lanes}
    for lane_id in sorted(set(packets_by_lane) - known):
        findings.append(f"adapter/opencode/evals/packets: packets exist for unregistered lane {lane_id}")

    for finding in findings:
        print(finding)
    if findings:
        print(f"lane eval check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print(f"lane eval check passed ({len(lanes)} lanes, {len(packet_files)} packets)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
