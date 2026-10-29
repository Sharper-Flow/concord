#!/usr/bin/env python3
"""Validate the named primary profiles and the inert intake example.

The generated named profiles `Concord-RTE`, `Concord`, and `Concord-Planner` ship
as managed agents. scripts/generate-agent-lanes.py --check owns their byte
equality with the profile contract; this check proves their portable role
boundaries:

  RTE                keeps Build-level permissions, item decisions with the
                     owning session, and human-only approval challenges, and
                     holds coordination in a Linear project, never in an issue
  Concord            needs no RTE, keeps the delegation anchor and cross-repository
                     boundary (CD-0178 D2)
  Planner            edits only under .planning/, asks before unnamed shell
                     commands, cannot commit, push, open or merge pull requests,
                     or release, delegates only to the read-only utilities, and
                     is denied the Concord tools that write work state; the
                     probes are evaluated with OpenCode's last-match wildcard
                     rules, not read as text
  all                no host-specific research tool names in frontmatter, no
                     duplicated conduct obligation, and no routing by
                     generated agent id in the body

The intake definition is a portable example. Concord does not package or
install it. This check proves the example contract in CD-0154:

  portable surface   no host-specific research tool names appear in any
                     frontmatter; host tool configuration stays outside the
                     example
  conduct ownership  technical-unknown evidence stays in the installed
                     conduct corpus, not in the portable example
  intake restriction the intake definition keeps its write and workflow
                     denials
"""
from __future__ import annotations

import json
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
PROFILE_DIR = Path(".opencode/agents")
NAMED_PROFILE_FILES = (
    "Concord-RTE.md",
    "Concord.md",
    "Concord-Planner.md",
)
# The profiles that leave every unnamed shell command to the host (CD-0154 D2).
FULL_PROFILE_FILES = ("Concord-RTE.md", "Concord.md")
PLANNER_PROFILE_FILE = "Concord-Planner.md"
PRIMARY_BUILD_TOOLS = ("edit", "write", "patch")
RTE_REQUIRED_PHRASES = (
    "Before allocation, load `concord-work-shaping`",
    "assign one independently deliverable outcome with its done condition to one primary, even when it spans related issues",
    "Load `concord-instruction-writing` before writing or editing agent instructions",
    "The RTE coordinates dependencies, not another session's technical choices or operator approvals",
    "They contact you for coordination, not permission",
    "Never answer a Concord approval challenge",
    "goes to the operator unchanged",
    "Each session owns its design, code, tests, review, law text, publication",
    "Keep Linear current",
    "Report ship status to the operator",
    "The user may choose to start this optional session",
    "On every activation, run a coordination pass across the assigned streams",
    "Inventory every agent session and account for every tab in the caller's workspace, not only assigned streams",
    "Include your own tab and non-agent tabs explicitly so the tab inventory reconciles",
    "Do not inventory other workspaces",
    "Inventory does not expand assignment scope or grant lifecycle rights over unassigned sessions",
    "A host session-state notice is an activation, not a timer or self-wakeup loop",
    "Preserve blocked dialogs, permission requests, and non-consent gates",
    "Do not answer another session's permission request or operator-reserved question",
    "Add no timer or self-wakeup loop",
    "name the missing evidence, who will recover it, and the concrete recovery action",
    "Every workspace tab has a reconciled row; no non-working row is blank or says only \"awaiting guidance\"",
    "On every pass, report a table with one row per workspace tab, keyed by observed tab title",
    "For every non-working row, include its state, the event and actor it waits for, and the RTE action to unblock or supply that wait",
    "Alternatively, mark it complete/retiring or outside scope and name its owner",
    "Handling a notice does not finish the pass",
    "Respect operator pauses",
    "Before going idle, confirm all of the following",
    "No incoming notice or permitted RTE-owned action remains",
    "No eligible assignment for available capacity remains without an owner or a recorded reason it cannot proceed",
    "Planning reflects the observed state",
    "Every unfinished stream has an owner and next action",
    "After acting, recheck only the affected streams",
    "Do not repeatedly scan unchanged state or prompt workers already doing permitted work",
    "Wait for worker notices, CI results, or operator input",
    "Follow host instructions for session launch, reuse, and lifecycle",
    "Reconcile assigned Linear issues during each pass",
    "Keep status, execution owner, blockers, next actions, and evidence links current",
    "An idle session, finished turn, or merged PR alone does not prove the item is Done",
    "Use the host's Linear MCP skill for tool procedures",
    "shared coordination instructions own worker and issue-state rules",
    "Keep unmerged, merged, released, and installed states distinct",
    "Do not keep a second planning backlog in a repository or host journal",
    "An implementation issue is not the RTE's session journal",
    "Routine same-session reloads use host continuity, not new planning comments",
    "A shared package or possible overlap does not establish merge precedence",
    "Continue the assigned outcome across partial deliveries",
    "Name the operation, target, observed refusal, effect state, and admitted next action",
    "Update a record only when its planning fact, decision, risk, or evidence changes",
    "Never approve, accept, overrule, or publish for another session's item",
    "Tell both owners when their streams change the same source owner",
    "Set merge order only for an actual conflict or dependency",
    "Compare each owner's retained posture, hold, or sequencing rule with your current directives",
    "Apply current accepted CD-0243 D2-D3 before asking for consent",
    "Assigning a worker to an in-scope dependency repair, and that worker publishing or merging its PR, do not independently require operator approval",
    "A new ticket or implementation step is not itself new Product direction",
    "Stated operator preferences remain binding",
    "Apply the same rule when directing an owner to seek approval: name the class or reservation and consequence",
    "Never make exact-wording approval an assignment condition outside that boundary",
    "Hold this assignment in a Linear project",
    "Add each assigned delivery issue that has no project",
    "an issue in another project stays there, and the coordination project's description links it",
    "mark it completed when the last issue it holds closes; linked issues finish in their own projects",
    "Never create or reuse an issue as a coordination record",
    "keep the assigned scope in the project description",
    "Post material cross-stream dependencies, sequencing decisions, and changes to RTE directives as project status updates",
)
# The RTE holds coordination in a Linear project. An issue used as the
# coordination record stays open with no deliverable, so any mention of one
# in the RTE body is a regression, whatever the surrounding sentence says.
RTE_FORBIDDEN_PATTERN = re.compile(r"coordination\s+issue", re.IGNORECASE)
CONCORD_REQUIRED_PHRASES = (
    "Before execution, load `concord-work-shaping`",
    "Load `concord-law-writing` when drafting, amending or pruning Product law",
    "Load `concord-instruction-writing` before writing or editing agent instructions",
    "You are Concord, the primary agent",
    "You do not need an RTE session",
    "The user may choose to start an RTE session",
    "Notices report actual overlap, not the chance of it",
    "You own design, code, tests, review, law text, publication, CI follow-through, deployment, and planning for your work item",
    "Managed participation is explicit (CD-0122 D1)",
    "when the operator makes it to you or an RTE assignment places the work in Concord workflow, unless the request places it outside the workflow",
    "Before capturing a production configuration-only operation, establish an admitted execution and acceptance route for its actual target",
    "If no route exists, report the limitation before capture",
    "Host-owned work has no Concord workflow authority, evidence, verdict, or completion claim",
    "An existing managed item stays held until its supported handoff succeeds",
    "a refusal is not permission to execute directly",
    "Obtain operator consent only under the shared asking instructions; otherwise decide within scope and record why",
    "When the core requests consent outside that boundary, report the implementation mismatch and follow its admitted recovery",
    "A law revision you enact without operator approval is `derived` under CD-0230 D4, even when the prior revision was operator-approved",
)
PLANNER_REQUIRED_PHRASES = (
    "Before triaging a request or refining a ticket, load `concord-work-shaping`",
    "The user may choose to start this optional session",
    "It is never a gate on Concord's work",
    "Surface where an executing agent would have to guess",
    "Separate what you observed from what you recommend",
    "Write only approved decisions into a ticket's description",
    "Keep each unapproved assumption visibly marked as unapproved",
    "Propose a consequential batch of record changes before you apply it",
    "Shell rules match command text, not what a command writes",
)
PLANNER_WRITERS = (
    "concord_work_compact",
    "concord_work_define",
    "concord_work_relate",
    "concord_work_start",
    "concord_work_transition",
)
# Probes evaluated against Planner's generated rules. Each names an input and
# the action OpenCode must resolve for it.
PLANNER_EDIT_PROBES = (
    (".planning/notes.md", "allow"),
    (".planning/prototype/main.go", "allow"),
    ("internal/store/store.go", "deny"),
    ("README.md", "deny"),
    (".planning", "deny"),
    ("../other-repo/file.go", "deny"),
    ("docs/.planning/notes.md", "deny"),
)
PLANNER_BASH_PROBES = (
    ("git status", "allow"),
    ("git log --oneline -5", "allow"),
    ("gh pr view 12", "allow"),
    ("gh pr checks 12", "allow"),
    ("git commit -m change", "deny"),
    ("git push origin main", "deny"),
    ("git merge feature", "deny"),
    ("git rebase main", "deny"),
    ("git tag v1.0.0", "deny"),
    ("git stash", "deny"),
    ("gh pr create --fill", "deny"),
    ("gh pr merge 12 --auto --squash", "deny"),
    ("gh release create v1.0.0", "deny"),
    ("gh workflow run release.yml", "deny"),
    ("sudo true", "deny"),
    ("make deploy", "ask"),
    ("python3 scripts/install.py", "ask"),
    ("go test ./...", "ask"),
    ("git grep -O'git push origin HEAD #' -e x", "ask"),
    ("go list -export -toolexec=sh ./...", "ask"),
)
PLANNER_TASK_PROBES = (
    ("concord-explore", "allow"),
    ("concord-lookup", "allow"),
    ("concord-advisor", "allow"),
    ("concord-implement", "deny"),
    ("concord-review", "deny"),
    ("general", "deny"),
    ("build", "deny"),
)
PROMPT_DIR = Path("examples/opencode/agents")
PRIMARY_PROMPT_FILES = (
    "concord-0.md",
)
RETIRED_PRIMARY_EXAMPLES = ("concord-1.md", "concord-2.md")
# Host-provided research providers. These arrive from the host configuration;
# a shipped prompt that names one has stopped being portable.
HOST_TOOL_FRAGMENTS = ("lgrep_", "context7_", "exa_", "github_")
LOOKUP_OBLIGATION_MARKERS = (
    "Recall is not evidence",
    "source to fit the surface",
    "blocked, unavailable, or inconclusive",
)
DELEGATION_ANCHOR_MARKERS = (
    "`dispatch_worker` among the next valid intents",
    "Size is not an exemption",
    "steps the core does not admit for dispatch",
)
# The generated agent ids under contracts/agent-lanes.v1.json. A portable
# example that names one routes per agent in prose; the registry descriptions
# own that routing.
GENERATED_AGENT_ID = re.compile(r"concord-(?:explore|lookup|advisor|research|implement|design|review|verify)\b")


def read_prompt(root: Path, name: str) -> str:
    path = root / PROMPT_DIR / name
    if not path.is_file():
        raise ValueError(f"missing primary agent example {path.relative_to(root)}")
    return path.read_text(encoding="utf-8")


def split_frontmatter(text: str) -> str:
    if not text.startswith("---\n"):
        raise ValueError("primary prompt must start with a frontmatter block")
    try:
        end = text.index("\n---\n", 4)
    except ValueError as error:
        raise ValueError("primary prompt frontmatter is not closed") from error
    return text[4:end]


def body(text: str) -> str:
    parts = text.split("\n---\n", 1)
    if len(parts) != 2:
        raise ValueError("primary prompt has no body after frontmatter")
    return parts[1]


def flatten(text: str) -> str:
    """Collapse whitespace so wrapped lines cannot hide a marker."""
    return " ".join(text.split())


def wildcard_match(value: str, pattern: str) -> bool:
    """OpenCode's permission wildcard: `*` spans any text, `?` one character,
    the whole value must match, and a trailing ` *` makes its arguments
    optional."""
    escaped = re.sub(r"[.+^${}()|\[\]\\]", lambda m: "\\" + m.group(0), pattern.replace("\\", "/"))
    escaped = escaped.replace("*", ".*").replace("?", ".")
    if escaped.endswith(" .*"):
        escaped = escaped[:-3] + "( .*)?"
    return re.fullmatch(escaped, value.replace("\\", "/"), re.DOTALL) is not None


def permission_rules(frontmatter: str, key: str) -> list[tuple[str, str]]:
    """The ordered pattern rules of one `permission.<key>` block."""
    match = re.search(rf"\n  {re.escape(key)}:\n((?:    .*\n?)*)", "\n" + frontmatter + "\n")
    if not match:
        return []
    rules = []
    for line in match[1].splitlines():
        rule = re.fullmatch(r'    ("(?:[^"\\]|\\.)*"): (allow|ask|deny)', line)
        if rule is None:
            raise ValueError(f"permission.{key} rule {line.strip()!r} is not a quoted pattern and action")
        rules.append((json.loads(rule[1]), rule[2]))
    return rules


def evaluate(rules: list[tuple[str, str]], value: str) -> str:
    """The last matching rule wins; with no match OpenCode asks."""
    return next((action for pattern, action in reversed(rules) if wildcard_match(value, pattern)), "ask")


def check_full_profile(name: str, frontmatter: str, flat_body: str) -> list[str]:
    findings = [
        f"{name} frontmatter lost the Build-level grant '{tool}: allow'"
        for tool in PRIMARY_BUILD_TOOLS
        if f"\n  {tool}: allow" not in "\n" + frontmatter
    ]
    if '\n  task:\n    "*": allow' not in "\n" + frontmatter:
        findings.append(f"{name} frontmatter does not allow task by default")
    bash = re.search(r'\n  bash:\n((?:    .*\n?)*)', "\n" + frontmatter)
    if not bash or re.search(r'^    "\*":|: "?(?:allow|ask)"?\s*$', bash[1], re.MULTILINE):
        findings.append(f"{name} bash rules must only deny named commands; the host governs other commands")
    findings.extend(
        f"{name} frontmatter denies Concord tool {tool}"
        for tool in re.findall(r"^  (concord_\w+): deny$", frontmatter, re.MULTILINE)
    )
    required_phrases = RTE_REQUIRED_PHRASES if name == "Concord-RTE.md" else CONCORD_REQUIRED_PHRASES
    findings.extend(
        f"{name} lost the role rule {phrase!r}"
        for phrase in required_phrases
        if phrase not in flat_body
    )
    if name == "Concord-RTE.md" and (match := RTE_FORBIDDEN_PATTERN.search(flat_body)):
        findings.append(f"{name} names {match.group(0)!r}; the RTE holds coordination in a Linear project, never an issue")
    if name == "Concord.md":
        findings.extend(
            f"{name} lost the delegation anchor marker {marker!r}"
            for marker in DELEGATION_ANCHOR_MARKERS
            if marker not in flat_body
        )
        findings.extend(
            f"{name} lost the cross-repository boundary phrase {phrase!r}"
            for phrase in (
                "Keep a work-item session in each repository",
                "a second Project in another git repository",
                "never claim or move across the boundary",
                "this session stops driving the other repository",
                "`concord_work_start` with `work_id` alone",
                "`session_placement`",
            )
            if phrase not in flat_body
        )
    return findings


def check_planner_profile(name: str, frontmatter: str, flat_body: str) -> list[str]:
    findings = [
        f"{name} frontmatter grants unscoped '{tool}: allow'; edits must stay path-scoped"
        for tool in PRIMARY_BUILD_TOOLS
        if re.search(rf"^  {tool}: (?:allow|ask)$", frontmatter, re.MULTILINE)
    ]
    try:
        rules = {key: permission_rules(frontmatter, key) for key in ("edit", "bash", "task")}
    except ValueError as error:
        return [*findings, f"{name} {error}"]
    for key, default in (("edit", "deny"), ("bash", "ask"), ("task", "deny")):
        if not rules[key] or rules[key][0] != ("*", default):
            findings.append(f'{name} permission.{key} must open with "*": {default}')
    for key, probes in (("edit", PLANNER_EDIT_PROBES), ("bash", PLANNER_BASH_PROBES), ("task", PLANNER_TASK_PROBES)):
        findings.extend(
            f"{name} permission.{key} resolves {value!r} to {evaluate(rules[key], value)}, not {expected}"
            for value, expected in probes
            if evaluate(rules[key], value) != expected
        )
    findings.extend(
        f"{name} permission.edit allows {pattern!r} outside .planning/"
        for pattern, action in rules["edit"]
        if action != "deny" and not pattern.startswith(".planning/")
    )
    findings.extend(
        f"{name} frontmatter does not deny Concord tool {tool}"
        for tool in PLANNER_WRITERS
        if f"\n  {tool}: deny" not in "\n" + frontmatter
    )
    findings.extend(
        f"{name} lost the role rule {phrase!r}"
        for phrase in PLANNER_REQUIRED_PHRASES
        if phrase not in flat_body
    )
    return findings


def check_named_profiles(root: Path) -> list[str]:
    findings: list[str] = []
    for retired in ("concord-coordinator.md", "concord-operator.md"):
        if (root / PROFILE_DIR / retired).exists():
            findings.append(f"retired named primary profile {retired} is present")
    for name in NAMED_PROFILE_FILES:
        path = root / PROFILE_DIR / name
        if not path.is_file():
            findings.append(f"missing named primary profile {PROFILE_DIR / name}")
            continue
        text = path.read_text(encoding="utf-8")
        try:
            frontmatter = split_frontmatter(text)
            flat_body = flatten(body(text))
        except ValueError as error:
            findings.append(f"{name} is malformed: {error}")
            continue
        findings.extend(
            f"{name} frontmatter names host tool fragment {fragment!r}"
            for fragment in (*HOST_TOOL_FRAGMENTS, "morph_edit")
            if fragment in frontmatter
        )
        if "mode: primary" not in frontmatter:
            findings.append(f"{name} frontmatter does not declare mode: primary")
        if any(marker in flat_body for marker in LOOKUP_OBLIGATION_MARKERS):
            findings.append(f"{name} duplicates the technical-unknown conduct obligation")
        findings.extend(
            f"{name} routes by generated agent id {match.group(0)!r}; route by need and let the registry descriptions name the agent"
            for match in [GENERATED_AGENT_ID.search(flat_body)]
            if match is not None
        )
        if name == PLANNER_PROFILE_FILE:
            findings.extend(check_planner_profile(name, frontmatter, flat_body))
        else:
            findings.extend(check_full_profile(name, frontmatter, flat_body))
    return findings


def check_primary_prompts(root: Path) -> list[str]:
    findings: list[str] = check_named_profiles(root)
    for retired in RETIRED_PRIMARY_EXAMPLES:
        if (root / PROMPT_DIR / retired).exists():
            findings.append(f"retired primary agent example {retired} is present")
    texts: dict[str, str] = {}
    for name in PRIMARY_PROMPT_FILES:
        try:
            texts[name] = read_prompt(root, name)
        except ValueError as error:
            findings.append(str(error))
    if len(texts) != len(PRIMARY_PROMPT_FILES):
        return findings

    for name, text in texts.items():
        try:
            flat = flatten(text)
            frontmatter = split_frontmatter(text)
            findings.extend(
                f"{name} frontmatter names host tool fragment {fragment!r}"
                for fragment in HOST_TOOL_FRAGMENTS
                if fragment in frontmatter
            )
            if "mode: primary" not in frontmatter:
                findings.append(f"{name} frontmatter does not declare mode: primary")
            if any(marker in flat for marker in LOOKUP_OBLIGATION_MARKERS):
                findings.append(f"{name} duplicates the technical-unknown conduct obligation")
        except ValueError as error:
            findings.append(f"{name} is malformed: {error}")

    if any(" is malformed: " in finding for finding in findings):
        return findings

    evidence = (root / ".concord/instructions" / "evidence.md").read_text(encoding="utf-8")
    findings.extend(
        f".concord/instructions/evidence.md is missing the lookup obligation marker {marker!r}"
        for marker in LOOKUP_OBLIGATION_MARKERS
        if marker not in evidence
    )

    intake_frontmatter = split_frontmatter(texts["concord-0.md"])
    findings.extend(
        f"concord-0.md frontmatter lost its intake boundary {boundary!r}"
        for boundary in (
            "edit: deny",
            "write: deny",
            "patch: deny",
            "morph_edit: deny",
            "concord_work_transition: deny",
            "concord_work_compact: deny",
        )
        if boundary not in intake_frontmatter
    )
    try:
        intake_bash = permission_rules(intake_frontmatter, "bash")
        if not intake_bash or intake_bash[0] != ("*", "deny"):
            findings.append("concord-0.md frontmatter does not deny bash by default")
        findings.extend(
            f"concord-0.md has an unapproved bash grant {pattern!r}; Linear MCP owns planning"
            for pattern, action in intake_bash
            if action != "deny" and (pattern, action) not in (
                ("gh issue list *", "allow"), ("gh issue view *", "allow"),
            )
        )
    except ValueError as error:
        findings.append(f"concord-0.md {error}")
    # Registry routing: the registry descriptions state what and when, so the
    # portable examples route by need and never hard-code a generated agent id.
    # An id here would fork the registry's routing authority into prose.
    findings.extend(
        f"{name} routes by generated agent id {match.group(0)!r}; route by need and let the registry descriptions name the agent"
        for name, text in texts.items()
        for match in [GENERATED_AGENT_ID.search(text)]
        if match is not None
    )
    return findings


def main() -> int:
    findings = check_primary_prompts(ROOT)
    for finding in findings:
        print(f"primary agent finding: {finding}")
    if findings:
        return 1
    print(f"primary agent check passed: {', '.join(NAMED_PROFILE_FILES + PRIMARY_PROMPT_FILES)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
