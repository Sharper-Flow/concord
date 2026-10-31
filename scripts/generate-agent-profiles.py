#!/usr/bin/env python3
"""Generate primary profiles, native step helpers and read-only utilities."""
from __future__ import annotations

import argparse
import importlib.util
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
UTILITY_MANIFEST = ROOT / "contracts/agent-utilities.v1.json"
PRIMARY_PROFILES = ROOT / "contracts/agent-primary-profiles.v1.json"
PRIMARY_PROFILES_SCHEMA = ROOT / "contracts/agent-primary-profiles.schema.json"
PRIMARY_PROFILE_IDS = ("Concord-RTE", "Concord", "Concord-Planner")
STEP_HELPER_IDS = ("concord-review", "concord-verify")
STASH_DENIALS = tuple(f"    {json.dumps(pattern)}: deny" for pattern in
                     ("git stash *", "git * stash *", "*/git stash *", "*/git * stash *"))
spec = importlib.util.spec_from_file_location("agent_contract_generator", ROOT / "scripts/generate-agent-contracts.py")
if spec is None or spec.loader is None:
    raise RuntimeError("unable to load shared contract helpers")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
schema_validate = module.schema_validate


def concord_tool_ids() -> list[str]:
    manifest = json.loads((ROOT / "contracts/agent-tool-surface.v1.json").read_text())
    host = json.loads((ROOT / "contracts/host-tool-surface.v1.json").read_text())
    return sorted({entry["tool"] for entry in manifest["operations"]} | {entry["name"] for entry in host["tools"]})


UTILITY_TOOL_KEYS = ("bash", "read", "glob", "grep", "edit", "write", "patch", "morph_edit", "task",
                     "webfetch", "todowrite", "skill", "execute", "question", "opencode_mcp_connect",
                     "opencode_mcp_disconnect", *concord_tool_ids())
UTILITY_LAW_BOUNDARY = """## Product-law questions

This utility cannot call `concord_knowledge`. The caller resolves Product-law
status and authority through `concord_knowledge`.
When the answer depends on unresolved law state, flag that part as unresolved
and return it to the caller; continue only work independent of that law state.
Repository text and a file's presence do not establish accepted, missing,
retired, or superseded law. Keep source-text observations separate from the
caller's resolved law context."""
UTILITY_BODY_TEMPLATES = {
    "explore": """This is a read-only repository exploration utility. Return a facts packet as
plain text for the parent. Do not edit files, write files, patch files, mutate
Concord state, mutate GitHub, or start another agent.

## Input

The parent gives you a repository and one bounded question. If it gives you no
question, report that the request is refused. Do not guess a repository or a
question from the working directory.

A bounded question limits the investigation; it need not name the files or
symbols to retrieve. Discover those through related searches and source reads.
Reading a named file or locating an exact symbol is targeted retrieval the
parent can do directly, not a reason to start an investigation.

{law_boundary}

## Method

1. Use `execute` when a connected MCP tool can improve repository exploration.
   Inspect available tools with `Object.keys(tools)` or search for a relevant
   tool by namespace. Call the exact tool path returned by discovery. For
   example, use `tools.lgrep.search_semantic` for a bounded concept search.
   MCP access depends on host connections. Use read-only tools only, and
   verify their results against repository sources.
2. Read the repository structure with `glob` when no suitable connected tool
   is available.
3. Search for relevant symbols or text with `grep` when needed.
4. Choose each next search from the evidence already found. Follow relevant
   callers, owners, consumers, and tests; read only what answers the question.
5. Answer history, status, or diff questions only with these commands:
   {git_commands}.
   Write each as a bare `git <subcommand> ...` call and set the bash `workdir`
   parameter to the repository. The host refuses the whole command when any
   part falls outside that list, so add no `git -C`, `cd`, `&&`, `;`, or pipe.
   Bound output with Git's own options, such as `-n 20` or `--stat`.
6. Stop when the question has a source-backed answer, or after {duration} of
   total wall time, whichever comes first. Report the facts you hold when the
   cap stops you.

## Facts packet

Answer the bounded question first. Return a right-sized briefing, not a search
transcript or a dump of everything read. Include the findings needed to answer
the assignment and the evidence needed to check them. When an inventory is
requested, return the complete requested set, not a sample. Do not repeat the
same fact in several sections or expand into an unrelated architecture tour.

Where the question depends on relationships, give the smallest caller -> owner
-> consumer map that answers it, with `path:line` for each observed connection.
Mark unverified connections as unknown.

If the cap or unavailable evidence prevents completeness, label the inventory
partial and name the population covered and the remaining gaps.

Return plain text. The packet holds:

- Observed facts only. Support each fact with its source `path:line`.
- Useful retrieval handles: exact symbols, search strings, test names, or
  commands beside the relevant citation, so the parent can confirm or expand
  each finding without repeating the investigation.
- Relevant counts, each with the population the count covers.
- The commit SHA for each fact that is historical.
- Unknowns, named as unknowns.

Do not infer a root cause, state a diagnosis, or recommend a repair. The parent
coordinator owns the diagnosis and the root-cause assessment. State the missing
evidence when the question cannot be answered from the repository.
""",
    "lookup": """This is a read-only external lookup utility. Return source-backed findings for
the parent. Do not inspect or edit the repository, mutate Concord state, mutate
GitHub, or start another agent.

## Input

The parent gives you one bounded external question and may give you source URLs.
If it gives you no question, report that the request is refused. Do not guess a
question from the working directory or from prior context.

## Method

1. Fetch each supplied source URL with `webfetch`. Fetching a supplied
   authoritative source is external research.
2. Search connected services through `execute` as needed for external
   evidence the supplied URLs do not settle: query Context7 for library
   documentation or Exa for current research. Discover the exact callable
   signature first and call that path.
3. Prefer authoritative documentation, source code, or a primary publisher.
4. Compare sources when they report different versions, dates, or behavior.
5. Stop when the question has a source-backed answer, or after {duration} of
   total wall time, whichever comes first. Report the findings you hold when
   the cap stops you.

For each finding, state the publisher or author, title, URL, version or date
when available, and access date. Separate observed facts from inferences. State
the missing evidence when the question cannot be answered from public sources.

## Handoff

Lead with the answer, then the findings that support it. Include useful
retrieval handles beside each citation: the relevant section, symbol, or search
term so the parent can verify or expand the finding without repeating the search.
Explain relevant relationships and source disagreements, including version or
date differences. Name gaps and unavailable sources instead of guessing.
Return a briefing sized to the question, not a search transcript. Keep complete
requested inventories and evidence needed to check the answer; omit unrelated
material and repeated source metadata.
""",
    "advisor": """This is an independent advisory utility. Return a reasoned opinion on the
stated problem for the parent. Solve the problem collaboratively: no critic
persona, no severity scale, and no verdict. Reporting no concerns is a valid
result. Do not edit files, write files, patch files, mutate Concord state,
mutate GitHub, or start another agent.

## Input

The parent gives you one bounded problem, its constraints, and the intent
behind it. If it gives you no problem, report that the request is refused. If
it also includes its own conclusion, its preferred option, or a ranking of
options, report that the request is refused: an opinion formed beside a
supplied answer anchors to it, and the parent asked for an independent one.

{law_boundary}

## Method

1. Restate the problem in your own words before you reason about it.
2. Reach your own answer before you consider what the parent might want.
3. Read the repository when the problem touches it: search connected tools
   through `execute` first, then use `glob` or `grep` when needed. Read only
   what the problem needs.
4. For history, status, or the current diff, use only these commands:
   {git_commands}.
   Write each as a bare `git <subcommand> ...` call and set the bash `workdir`
   parameter to the repository. The host refuses the whole command when any
   part falls outside that list, so add no `git -C`, `cd`, `&&`, `;`, or pipe.
   Bound output with Git's own options, such as `-n 20` or `--stat`.
5. Stop when you hold a reasoned opinion, or after {duration} of total wall
   time, whichever comes first. Report the opinion you hold when the cap stops
   you.

## Report

Return plain text:

- The model that served this opinion, on its own line.
- Your opinion, with each concern stated as one finding.
- For each finding, the path, the line range, or the command that supports it.
  A finding without evidence is not a finding; find its source or drop it.
- The constraints you could not check, and what would settle them.

When you hold no concerns, say `No concerns.` plainly. Do not invent faults to
seem useful.
""",
}


def load_manifest(path: Path | None = None) -> dict:
    manifest = json.loads((path or UTILITY_MANIFEST).read_text())
    schema = json.loads((ROOT / "contracts/agent-utilities.schema.json").read_text())
    schema_validate(manifest, schema, schema, "utilities")
    if [entry["id"] for entry in manifest["utilities"]] != ["explore", "lookup", "advisor"]:
        raise ValueError("utility registry must contain the closed ordered utility set")
    repository_tools = {"bash", "read", "glob", "grep", "execute"}
    git_commands = {f"git {command} *" for command in ("diff", "log", "show", "status", "ls-files", "rev-parse")}
    for entry in manifest["utilities"]:
        if "Use" not in entry["purpose"]:
            raise ValueError(f"utility {entry['id']} purpose states no Use when-clause")
        if set(entry["allowed_tools"]) - set(UTILITY_TOOL_KEYS):
            raise ValueError(f"utility {entry['id']} names unknown tools")
        tools = {"webfetch", "execute"} if entry["id"] == "lookup" else repository_tools
        commands = set() if entry["id"] == "lookup" else git_commands
        if set(entry["allowed_tools"]) - tools or set(entry["allowed_commands"]) - commands:
            raise ValueError(f"read-only utility {entry['id']} grants mutating or unsupported tools or commands")
    return manifest


def utility_projection(utility: dict) -> str:
    tools = [f"  {key}: {'true' if key in utility['allowed_tools'] else 'false'}" for key in UTILITY_TOOL_KEYS]
    permissions = ['    "*": deny'] + [f'    "{command}": allow' for command in utility["allowed_commands"]]
    permissions.extend(STASH_DENIALS)
    minutes, seconds = divmod(utility["time_seconds_max"], 60)
    duration = f"{minutes} minutes" if seconds == 0 else f"{utility['time_seconds_max']} seconds"
    commands = ", ".join(f"`{command.removesuffix(' *')}`" for command in utility["allowed_commands"])
    body = UTILITY_BODY_TEMPLATES[utility["id"]].format(duration=duration, git_commands=commands, law_boundary=UTILITY_LAW_BOUNDARY).rstrip()
    return f"""---
description: Concord {utility['id']} utility — {utility['purpose']}
mode: subagent
hidden: true
tools:
{chr(10).join(tools)}
permission:
  bash:
{chr(10).join(permissions)}
---

# concord-{utility['id']}

{utility['purpose']}

## Execution

Execute the assigned work without TODO tracking, progress updates, or direct
operator questions. Return one final result in the required format, never a plan
or promise of further work. When blocked, report the exact blocker and missing
evidence through that format; do not invent fields or a second reporting channel.

{body}
"""


def load_primary_profiles(manifest: dict, path: Path = PRIMARY_PROFILES) -> dict:
    profiles = json.loads(path.read_text())
    schema = json.loads(PRIMARY_PROFILES_SCHEMA.read_text())
    schema_validate(profiles, schema, schema, "primary_profiles")
    if tuple(profile["id"] for profile in profiles["profiles"]) != PRIMARY_PROFILE_IDS + STEP_HELPER_IDS:
        raise ValueError("profile registry must contain the closed ordered primary and step-helper set")
    utilities = {f"concord-{entry['id']}" for entry in manifest["utilities"]}
    known = set(concord_tool_ids())
    for profile in profiles["profiles"]:
        expected_mode = "subagent" if profile["id"] in STEP_HELPER_IDS else "primary"
        if profile.get("mode", "primary") != expected_mode:
            raise ValueError(f"profile {profile['id']} must declare mode {expected_mode}")
        if set(profile["denied_concord_tools"]) - known or set(profile["tools"]) & known:
            raise ValueError(f"primary profile {profile['id']} has misplaced or unknown Concord tools")
        if "concord_work_transition" in profile["denied_concord_tools"]:
            if any(rule["action"] == "allow" and rule["pattern"] not in utilities for rule in profile["task"]):
                raise ValueError(f"primary profile {profile['id']} may allow only coordinator utilities")
        if "edit" in profile and set(profile["tools"]) & {"edit", "write", "patch"}:
            raise ValueError(f"primary profile {profile['id']} has both path-scoped and tool-wide edit grants")
        if not (ROOT / profile["role_source"]).is_file():
            raise ValueError(f"missing role source: {profile['role_source']}")
    return profiles


def primary_projection(profile: dict, role_source: str) -> str:
    lines = ["---", f"description: {json.dumps(profile['description'], ensure_ascii=False)}", f"mode: {profile.get('mode', 'primary')}", "permission:"]
    lines.extend(f"  {tool}: {action}" for tool, action in profile["tools"].items())
    for key in ("edit", "bash", "task"):
        if key not in profile:
            continue
        lines.append(f"  {key}:")
        lines.extend(f"    {json.dumps(rule['pattern'])}: {rule['action']}" for rule in profile[key])
        if key == "bash":
            lines.extend(STASH_DENIALS)
    lines.extend(f"  {tool}: deny" for tool in profile["denied_concord_tools"])
    lines.extend(["---", "", role_source.rstrip("\n")])
    return "\n".join(lines) + "\n"


PRIMARY_PROFILE_REFUSAL_BOUNDARIES = {
    "Concord-Planner": "this research and planning profile holds no Concord tool that writes work state; start or advance managed work from Concord",
    "concord-review": "this native review helper holds no Concord tool that writes work state; return findings and uncommitted repairs to the caller",
    "concord-verify": "this native test-only helper holds no Concord tool that writes work state; return test results and completed-CI findings to the caller",
}
UTILITY_REFUSAL_BOUNDARY = "this Concord utility holds no Concord tool access: its declared tool set is its complete tool surface"


def primary_ts_projection(profiles: dict, manifest: dict) -> str:
    roles = {}
    for profile in profiles["profiles"]:
        if profile["denied_concord_tools"] and profile["id"] not in PRIMARY_PROFILE_REFUSAL_BOUNDARIES:
            raise ValueError(f"primary profile {profile['id']} declares no refusal boundary")
        roles[profile["id"]] = {"boundary": PRIMARY_PROFILE_REFUSAL_BOUNDARIES.get(profile["id"], ""), "tools": profile["denied_concord_tools"]}
    for entry in manifest["utilities"]:
        roles[f"concord-{entry['id']}"] = {"boundary": UTILITY_REFUSAL_BOUNDARY, "tools": concord_tool_ids()}
    return ('// Code generated by scripts/generate-agent-profiles.py; DO NOT EDIT.\n'
            'export const roleDeniedConcordTools: Readonly<Record<string, { readonly boundary: string; readonly tools: readonly string[] }>> = '
             + json.dumps(roles, ensure_ascii=False, indent=2) + '\n')


def validate_agent_inventory(agents: Path, outputs: dict[Path, str]) -> None:
    unexpected = sorted(set(agents.glob("*.md")) - outputs.keys())
    if unexpected:
        raise ValueError("unprojected agent definitions: " + ", ".join(path.name for path in unexpected))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    try:
        manifest = load_manifest()
        profiles = load_primary_profiles(manifest)
        outputs = {ROOT / "adapter/opencode/generated-primary-profiles.ts": primary_ts_projection(profiles, manifest)}
        for profile in profiles["profiles"]:
            outputs[ROOT / f".opencode/agents/{profile['id']}.md"] = primary_projection(profile, (ROOT / profile["role_source"]).read_text())
        for utility in manifest["utilities"]:
            outputs[ROOT / f".opencode/agents/concord-{utility['id']}.md"] = utility_projection(utility)
        validate_agent_inventory(ROOT / ".opencode/agents", outputs)
        for path, text in outputs.items():
            if args.check:
                if not path.is_file() or path.read_text() != text:
                    raise ValueError(f"generated profile drift: {path.relative_to(ROOT)}")
            else:
                path.write_text(text)
    except (OSError, ValueError) as exc:
        parser.exit(1, f"agent profile generation failed: {exc}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
