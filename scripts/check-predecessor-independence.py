#!/usr/bin/env python3
"""Keep Concord's repository-owned agent surfaces predecessor-independent.

AGENTS.md states the rule: Advance is public predecessor evidence only — no
created or dual-written predecessor state, no routing Concord work through
the predecessor's tooling. Before this check that rule was prose with no
mechanism, while sibling rules (AGENTS.md budget, publication hygiene)
already carried validators.

Scope — repository-owned agent surfaces only:

  * the generated lane definitions under .opencode/agents/
  * the lane manifest and the generator (contracts/agent-lanes*.json,
    scripts/generate-agent-lanes.py, contracts/agent-lane-*.json)
  * the adapter (adapter/opencode/*.ts, excluding nothing)

Out of scope, deliberately: the operator's host configuration outside this
repository (the check cannot and must not own that surface) and every path
under docs/, where predecessor evidence is legitimately cited — the
operational-coverage table, the postmortem, and the predecessor-lessons
records depend on those names. A citation is not a dependency: what this
check rejects is a tool grant or a tool call shape, not a mention.

Rejected shapes, per surface kind:

  agent markdown / manifest / generator input
      a tool identifier from the predecessor's exported tool prefix
      (`adv_<name>`), or a predecessor state path under the plugin's
      change/artifact store (`~/.local/share/Advance/`, `advance-conformance`)
  adapter TypeScript
      an imported or invoked `adv_*` tool, a spawn of the predecessor
      plugin, or a read/write of the predecessor state directories

The check is textual by design and says so: a dependency that lands in a
generated lane definition persists through regeneration, and the class of
failure is invisible to tests (nothing fails at runtime — the agent simply
reaches for a tool Concord does not own). This is the same declared-textual-
guard class as the AGENTS.md command rule under CD-0055 D3.

A deliberately failing run is exercised by scripts/test-predecessor-
independence.py before any pass counts.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

AGENT_SURFACES = [
    ROOT / ".opencode" / "agents",
    ROOT / "contracts",
    ROOT / "scripts" / "generate-agent-lanes.py",
]
ADAPTER_DIR = ROOT / "adapter" / "opencode"

ADV_TOOL = re.compile(r"\badv_[a-z][a-z0-9_]*\b")
ADV_STATE_PATH = re.compile(
    r"\.local/share/Advance/|advance-conformance|/Advance/plugin"
)
ADV_SPAWN = re.compile(r"[\"']adv[-\s]|--agent adv\b|Advance/plugin")

MAX_FINDINGS = 200


def scan(path: Path, patterns: dict[str, re.Pattern[str]], findings: list[str], root: Path) -> None:
    try:
        text = path.read_text(encoding="utf-8", errors="replace")
    except OSError as exc:
        findings.append(f"{path.relative_to(root)}: unreadable: {exc}")
        return
    for label, pattern in patterns.items():
        match = pattern.search(text)
        if match:
            findings.append(
                f"predecessor dependence: {path.relative_to(root)}: "
                f"{label} matched {match.group(0)!r}"
            )


def main(argv: list[str] | None = None) -> int:
    import argparse

    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument(
        "--root",
        type=Path,
        default=None,
        help="scan this repository root instead of the one containing this script (test harness)",
    )
    args = parser.parse_args(argv)
    root = args.root.resolve() if args.root else ROOT

    def under(path: Path) -> Path:
        return path if path.is_absolute() else root / path

    agent_files: list[Path] = []
    agents_dir = under(root / ".opencode" / "agents")
    if agents_dir.is_dir():
        agent_files.extend(sorted(agents_dir.glob("*.md")))
    for pattern in ("agent-lanes*.json", "agent-lane-*.json"):
        agent_files.extend(sorted(under(root / "contracts").glob(pattern)))
    generator = under(root / "scripts" / "generate-agent-lanes.py")
    if generator.is_file():
        agent_files.append(generator)

    adapter_dir = under(root / "adapter" / "opencode")
    adapter_files = sorted(adapter_dir.glob("*.ts")) if adapter_dir.is_dir() else []

    findings: list[str] = []
    agent_patterns = {
        "predecessor tool identifier": ADV_TOOL,
        "predecessor state path": ADV_STATE_PATH,
    }
    adapter_patterns = {
        "predecessor tool identifier": ADV_TOOL,
        "predecessor state path": ADV_STATE_PATH,
        "predecessor spawn": ADV_SPAWN,
    }
    for path in agent_files:
        scan(path, agent_patterns, findings, root)
    for path in adapter_files:
        scan(path, adapter_patterns, findings, root)
    if len(findings) > MAX_FINDINGS:
        findings = findings[:MAX_FINDINGS]
    for finding in findings:
        print(finding)
    print(
        f"predecessor independence check "
        f"{'failed' if findings else 'passed'} "
        f"({len(agent_files)} agent surfaces, "
        f"{len(adapter_files)} adapter files scanned)"
    )
    return 1 if findings else 0


if __name__ == "__main__":
    raise SystemExit(main())
