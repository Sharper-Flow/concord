#!/usr/bin/env python3
"""Validate the named primary profiles and the inert intake example.

The generated named profiles `Concord-RTE`, `Concord`, and `Concord-Planner` ship
as managed agents. scripts/generate-agent-profiles.py --check owns their byte
equality with the profile contract; this check proves their portable role
boundaries:

  RTE                keeps Build-level permissions and rejects coordination-issue
                     wording
  Concord            keeps Build-level permissions and the delegation anchor
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
# The RTE holds coordination in a Linear project. An issue used as the
# coordination record stays open with no deliverable, so any mention of one
# in the RTE body is a regression, whatever the surrounding sentence says.
RTE_FORBIDDEN_PATTERN = re.compile(r"coordination\s+issue", re.IGNORECASE)
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
    "Use native Task to delegate implementation and design under host permissions",
    "Obtain independent review through native Task against the exact current subject",
    "Bind an attributed local or required CI verification report for the recorded commit",
    "A Task answer alone is not a bound report",
    "compare the recorded commits for Review, evidence and verdicts",
)
# Portable profiles route by need; agent descriptions own routing by id.
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
    if name == "Concord-RTE.md" and (match := RTE_FORBIDDEN_PATTERN.search(flat_body)):
        findings.append(f"{name} names {match.group(0)!r}; the RTE holds coordination in a Linear project, never an issue")
    if name == "Concord.md":
        findings.extend(
            f"{name} lost the delegation anchor marker {marker!r}"
            for marker in DELEGATION_ANCHOR_MARKERS
            if marker not in flat_body
        )
    return findings


def check_planner_profile(name: str, frontmatter: str) -> list[str]:
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
            findings.extend(check_planner_profile(name, frontmatter))
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
