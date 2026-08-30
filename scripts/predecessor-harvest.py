#!/usr/bin/env python3
"""Harvest a predecessor snapshot without an interactive agent session.

`docs/predecessor-migration-runbook.md` harvested the predecessor by asking an
agent to read its sanctioned tool surface and transcribe the result. Host tool
output budgets truncate large listings, so the v1 capture lost wisdom for three
Products and one Project's totals. A Product whose listings exceed that budget
cannot migrate through the agent path at all.

This reader replaces the transcription with two non-interactive sources, both
sanctioned reads of the predecessor's own surface. Nothing here parses
predecessor state files.

  changes, terminal counts
      `adv status --json`, run with the Project directory as the working
      directory. The predecessor's status CLI resolves the Project from git and
      emits every in-flight change with its gate progress and task totals.
  wisdom
      the predecessor's read-only MCP server, spawned over stdio with the
      Project directory as the working directory. That working directory pins
      Project identity, which is why the surface needs no path argument -- and
      it rejects one, so no other route reaches a second Project.

Reflections are deliberately not captured. The stdio surface reports zero
reflections for a Project whose host tool reports 83, so the two sanctioned
reads disagree and neither can be trusted as the count. The gap is recorded in
`producer` and the harvest refuses unless `--accept-gaps` is given.

The output validates against `contracts/predecessor-snapshot.schema.json` before
it is written. An invalid or partial snapshot is never left on disk.
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCHEMA = ROOT / "contracts/predecessor-snapshot.schema.json"

GATES = ("proposal", "discovery", "design", "planning", "execution", "acceptance", "release")

MCP_COMMAND = (
    "exec fnm exec --using=24.15.0 -- "
    "node ~/.local/share/Advance/plugin/dist/mcp-server.js"
)

SCHEMA_VERSION = 1


class HarvestError(RuntimeError):
    """A sanctioned read failed, or returned a shape the contract cannot use."""


def run_status(project_dir: Path) -> dict:
    """Read in-flight changes and terminal counts from the predecessor status CLI."""
    try:
        completed = subprocess.run(
            ["adv", "status", "--json"],
            cwd=project_dir,
            capture_output=True,
            text=True,
            timeout=120,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        raise HarvestError(f"adv status failed for {project_dir}: {error}") from error
    if completed.returncode != 0:
        raise HarvestError(f"adv status exited {completed.returncode} for {project_dir}")
    try:
        payload = json.loads(completed.stdout)
    except json.JSONDecodeError as error:
        raise HarvestError(f"adv status emitted invalid JSON for {project_dir}") from error
    if not payload.get("live", False):
        raise HarvestError(f"adv status reports a non-live read for {project_dir}")
    return payload


def completed_gates(change: dict, change_id: str) -> list[str]:
    """Derive completed gates, and refuse when the two reported forms disagree."""
    first_incomplete = change.get("firstIncompleteGate")
    if first_incomplete is None:
        return list(GATES)
    if first_incomplete not in GATES:
        raise HarvestError(f"{change_id}: unknown gate {first_incomplete!r}")
    derived = list(GATES[: GATES.index(first_incomplete)])
    progress = change.get("gateProgressStr", "")
    if progress:
        marked = progress.count("\u2713")
        if marked != len(derived):
            raise HarvestError(
                f"{change_id}: gate progress reports {marked} complete, "
                f"first incomplete gate implies {len(derived)}"
            )
    return derived


def active_changes(payload: dict) -> list[dict]:
    """Select in-flight changes that have not reached a terminal phase."""
    selected: list[dict] = []
    for change in payload.get("changes", []):
        change_id = change.get("id")
        if not change_id:
            raise HarvestError("adv status returned a change without an id")
        if change.get("lifecycleState") != "open":
            continue
        gates = completed_gates(change, change_id)
        if len(gates) == len(GATES):
            continue
        selected.append(
            {
                "change_id": change_id,
                "summary": change.get("title") or change_id,
                "status": change.get("status") or "unknown",
                "completed_gates": gates,
                "tasks_total": int(change.get("tasksTotal", 0)),
                "tasks_done": int(change.get("tasksDone", 0)),
                "updated_at": change["lastActivityAt"],
            }
        )
    return selected


class McpReader:
    """Speak JSON-RPC over stdio to the predecessor's read-only MCP server."""

    def __init__(self, command: str, project_dir: Path) -> None:
        self.process = subprocess.Popen(  # noqa: S603 - fixed command, no shell input
            ["/usr/bin/bash", "-lc", command],
            cwd=project_dir,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
            bufsize=1,
        )
        self.next_id = 0
        self._initialize()

    def _send(self, message: dict) -> None:
        assert self.process.stdin is not None
        self.process.stdin.write(json.dumps(message) + "\n")
        self.process.stdin.flush()

    def _receive(self) -> dict:
        assert self.process.stdout is not None
        while True:
            line = self.process.stdout.readline()
            if not line:
                raise HarvestError("predecessor read surface closed the connection")
            line = line.strip()
            if line.startswith("{"):
                return json.loads(line)

    def _initialize(self) -> None:
        self.next_id += 1
        self._send(
            {
                "jsonrpc": "2.0",
                "id": self.next_id,
                "method": "initialize",
                "params": {
                    "protocolVersion": "2024-11-05",
                    "capabilities": {},
                    "clientInfo": {"name": "concord-predecessor-harvest", "version": "1"},
                },
            }
        )
        self._receive()
        self._send({"jsonrpc": "2.0", "method": "notifications/initialized"})

    def call(self, tool: str, arguments: dict) -> dict:
        self.next_id += 1
        self._send(
            {
                "jsonrpc": "2.0",
                "id": self.next_id,
                "method": "tools/call",
                "params": {"name": tool, "arguments": arguments},
            }
        )
        response = self._receive()
        try:
            text = response["result"]["content"][0]["text"]
        except (KeyError, IndexError) as error:
            raise HarvestError(f"{tool}: unusable response shape") from error
        payload = json.loads(text)
        if "error" in payload:
            raise HarvestError(f"{tool}: {payload['error']}")
        return payload

    def close(self) -> None:
        self.process.terminate()
        try:
            self.process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.process.kill()


def read_wisdom(reader: McpReader, project_id: str) -> list[dict]:
    """Read durable project-level wisdom, and refuse a truncated listing."""
    # maxEntries caps at 200 and defaults to the whole listing, so it is never sent.
    payload = reader.call("wisdom_list", {"project_only": True})
    entries = payload.get("wisdom", [])
    reported = payload.get("count")
    if reported is not None and reported != len(entries):
        raise HarvestError(
            f"{project_id}: wisdom listing truncated, {len(entries)} of {reported} entries"
        )
    harvested = []
    for entry in entries:
        harvested.append(
            {
                "id": entry["id"],
                "type": entry.get("type") or "unknown",
                "content": entry["content"],
                "change_id": entry.get("source_change") or "",
                "promoted": entry.get("scope") == "project",
                "recorded_at": entry["promoted_at"],
            }
        )
    return harvested


def harvest_project(project_id: str, project_dir: Path, mcp_command: str) -> tuple[dict, list[str]]:
    status = run_status(project_dir)
    counts = status.get("counts", {})
    reader = McpReader(mcp_command, project_dir)
    try:
        wisdom = read_wisdom(reader, project_id)
    finally:
        reader.close()
    project = {
        "project_id": project_id,
        "locator": str(project_dir),
        "archived_changes": int(counts.get("archived", 0)),
        "closed_changes": int(counts.get("closed", 0)),
        "active_changes": active_changes(status),
        "wisdom_entries": wisdom,
        "reflections": [],
    }
    gaps = [f"{project_id}: reflections not captured; sanctioned reads disagree on the count"]
    return project, gaps


def validate(snapshot: dict) -> None:
    try:
        import jsonschema
    except ImportError as error:  # pragma: no cover - CI images carry jsonschema
        raise HarvestError("jsonschema is required to validate the snapshot") from error
    schema = json.loads(SCHEMA.read_text(encoding="utf-8"))
    jsonschema.validate(snapshot, schema)


def parse_project(value: str) -> tuple[str, Path]:
    project_id, separator, path = value.partition("=")
    if not separator or not project_id or not path:
        raise argparse.ArgumentTypeError("expected <project_id>=<absolute path>")
    resolved = Path(path)
    if not resolved.is_absolute() or not resolved.is_dir():
        raise argparse.ArgumentTypeError(f"{path} is not an existing absolute directory")
    return project_id, resolved


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument(
        "--project",
        action="append",
        required=True,
        type=parse_project,
        metavar="ID=PATH",
        help="a Project of the Product being migrated; repeat for each",
    )
    parser.add_argument("--out", required=True, type=Path, help="snapshot file to write")
    parser.add_argument("--producer", default="scripts/predecessor-harvest.py")
    parser.add_argument(
        "--accept-gaps",
        action="store_true",
        help="write the snapshot with recorded capture gaps instead of refusing",
    )
    parser.add_argument("--mcp-command", default=MCP_COMMAND)
    args = parser.parse_args(argv)

    projects = []
    gaps: list[str] = []
    try:
        for project_id, project_dir in args.project:
            project, project_gaps = harvest_project(project_id, project_dir, args.mcp_command)
            projects.append(project)
            gaps.extend(project_gaps)
    except HarvestError as error:
        print(f"predecessor-harvest: {error}", file=sys.stderr)
        return 2

    producer = args.producer
    if gaps:
        producer = f"{producer}; capture gaps: {'; '.join(gaps)}"
    snapshot = {
        "schema_version": SCHEMA_VERSION,
        "captured_at": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
        "producer": producer[:256],
        "source_system": "advance",
        "projects": projects,
    }

    try:
        validate(snapshot)
    except HarvestError as error:
        print(f"predecessor-harvest: {error}", file=sys.stderr)
        return 2
    except Exception as error:  # jsonschema.ValidationError and friends
        print(f"predecessor-harvest: snapshot failed contract validation: {error}", file=sys.stderr)
        return 2

    if gaps and not args.accept_gaps:
        for gap in gaps:
            print(f"predecessor-harvest: capture gap: {gap}", file=sys.stderr)
        print("predecessor-harvest: refusing to write a partial snapshot", file=sys.stderr)
        return 3

    args.out.write_text(json.dumps(snapshot, indent=2) + "\n", encoding="utf-8")
    print(f"predecessor-harvest: wrote {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
