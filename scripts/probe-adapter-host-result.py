#!/usr/bin/env python3
"""Probe the pinned OpenCode ToolResult boundary without using host config."""
from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
PIN_PATH = ROOT / "docs/adapter-host-pin.v1.json"
EXPECTED_ERROR = "undefined is not an object (evaluating 'c.split')"


def run_probe(bunx: str, version: str, tool_name: str, home: Path) -> subprocess.CompletedProcess[str]:
    environment = {
        "PATH": os.environ.get("PATH", ""),
        "HOME": str(home),
        "XDG_CONFIG_HOME": str(home / "config"),
        "XDG_DATA_HOME": str(home / "data"),
        "XDG_CACHE_HOME": str(home / "cache"),
    }
    return subprocess.run(
        [bunx, f"opencode-ai@{version}", "--pure", "debug", "agent", "build", "--tool", tool_name, "--params", "{}"],
        cwd=home,
        env=environment,
        capture_output=True,
        text=True,
        timeout=300,
        check=False,
    )


def main() -> int:
    try:
        pin = json.loads(PIN_PATH.read_text(encoding="utf-8"))
        runtime = pin["runtime_probe"]
        version = runtime["host_version"]
    except (OSError, json.JSONDecodeError, KeyError, TypeError) as exc:
        print(json.dumps({"status": "error", "error": f"host pin is unreadable: {exc}"}, sort_keys=True))
        return 1

    with tempfile.TemporaryDirectory(prefix="concord-host-result-probe-") as directory:
        home = Path(directory)
        tools = home / ".opencode" / "tools"
        tools.mkdir(parents=True)
        (tools / "result-probe.ts").write_text(
            '''export const bare = {
    description: "Return a bare object for the host probe",
    args: {},
    async execute() {
      return {}
    },
  }
export const conforming = {
    description: "Return a conforming result for the host probe",
    args: {},
    async execute() {
      return { title: "probe", output: "{}", metadata: {} }
    },
}
''',
            encoding="utf-8",
        )
        bunx = os.environ.get("BUNX", "bunx")
        try:
            bare = run_probe(bunx, version, "result-probe_bare", home)
            conforming = run_probe(bunx, version, "result-probe_conforming", home)
        except (OSError, subprocess.SubprocessError) as exc:
            print(json.dumps({"status": "error", "error": str(exc)}, sort_keys=True))
            return 1

    conforming_result: object = {}
    try:
        conforming_result = json.loads(conforming.stdout)["result"]
    except (json.JSONDecodeError, KeyError, TypeError):
        pass
    conforming_passed = (
        conforming.returncode == 0
        and isinstance(conforming_result, dict)
        and conforming_result.get("output") == "{}"
        and isinstance(conforming_result.get("metadata"), dict)
        and conforming_result["metadata"].get("truncated") is False
    )
    observed = {
        "probe_identity": runtime["probe_identity"],
        "runner": runtime["runner"],
        "observed_on": runtime["observed_on"],
        "host_package": runtime["host_package"],
        "host_version": version,
        "source_urls": runtime["source_urls"],
        "commands": runtime["commands"],
        "bare_object_failure": {
            "status": "failed" if bare.returncode != 0 and EXPECTED_ERROR in (bare.stdout + bare.stderr) else "unexpected",
            "exit_code": bare.returncode,
            "error_contains": EXPECTED_ERROR,
        },
        "conforming_result_success": {
            "status": "passed" if conforming_passed else "failed",
            "exit_code": conforming.returncode,
            "output": conforming_result.get("output")
            if isinstance(conforming_result, dict)
            else None,
            "metadata_truncated": conforming_result.get("metadata", {}).get("truncated")
            if isinstance(conforming_result, dict)
            and isinstance(conforming_result.get("metadata"), dict)
            else None,
        },
        "limits": {"max_bytes": 51200, "max_lines": 2000},
    }
    matches_record = observed == runtime
    evidence = {"schema_version": "1.0", **observed, "matches_recorded_evidence": matches_record}
    print(json.dumps(evidence, sort_keys=True, separators=(",", ":")))
    return 0 if matches_record else 1


if __name__ == "__main__":
    sys.exit(main())
