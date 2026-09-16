#!/usr/bin/env python3
"""Validate the inert numbered primary-agent examples.

The three primary coordinator definitions are portable examples. Concord does
not package or install them. This check proves the example contract in CD-0154:

  portable surface   no host-specific research tool names appear in any
                     frontmatter; host tool configuration stays outside the
                     examples
  shared authority   every section outside the posture is byte-identical
                     between the shaping and driving definitions
  advisory handoffs  each coordinator carries its advisory switch rule, with
                     no mandatory switch and no repeat-after-decline
  conduct ownership  technical-unknown evidence stays in the installed
                     conduct corpus, not in the portable examples
  intake restriction the intake definition keeps its write and workflow
                     denials
"""
from __future__ import annotations

import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
PROMPT_DIR = Path("examples/opencode/agents")
PRIMARY_PROMPT_FILES = (
    "concord-0.md",
    "concord-1.md",
    "concord-2.md",
)
# Host-provided research providers. These arrive from the host configuration;
# a shipped prompt that names one has stopped being portable.
HOST_TOOL_FRAGMENTS = ("lgrep_", "context7_", "exa_", "github_")
POSTURE_HEADING = "## Posture"
SECTION_HEADING = "## "
ADVISORY_HANDOFF_MARKERS = {
    # Each coordinator carries the advisory rule for the switch it may
    # propose. Both directions forbid a mandatory switch, a repeated
    # unchanged recommendation after decline, and a pause of permitted work.
    "concord-1.md": (
        "Advisory handoff",
        "advisory",
        "No phase requires it",
        "do not repeat the unchanged offer",
        "do not pause work the contract still permits",
    ),
    "concord-2.md": (
        "Advisory reconsideration",
        "advisory",
        "do not repeat the unchanged recommendation",
        "do not pause work the contract still permits",
    ),
}
LOOKUP_OBLIGATION_MARKERS = (
    "Recall is not evidence",
    "source to fit the surface",
    "blocked, unavailable, or inconclusive",
)


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


def frontmatter_field(frontmatter: str, name: str) -> str:
    """Return one top-level YAML field without parsing host-specific YAML."""
    lines = frontmatter.splitlines()
    start = next((index for index, line in enumerate(lines) if line.startswith(f"{name}:")), None)
    if start is None:
        return ""
    end = len(lines)
    for index in range(start + 1, len(lines)):
        if lines[index] and not lines[index][0].isspace() and ":" in lines[index]:
            end = index
            break
    return "\n".join(lines[start:end])


def body(text: str) -> str:
    parts = text.split("\n---\n", 1)
    if len(parts) != 2:
        raise ValueError("primary prompt has no body after frontmatter")
    return parts[1]


def sections(text: str) -> dict[str, str]:
    """Map each level-2 heading to its text, preserving order."""
    result: dict[str, str] = {}
    current: str | None = None
    for line in body(text).splitlines():
        if line.startswith(SECTION_HEADING) and not line.startswith("### "):
            current = line[len(SECTION_HEADING):].strip()
            result[current] = ""
        elif current is not None:
            result[current] += line + "\n"
    return result


def shared_sections(text: str) -> str:
    """Everything outside the posture, with the header line normalized.

    The shaping and driving definitions differ only in name, description,
    and posture, so this text must stay byte-identical between them.
    """
    content = body(text)
    shared = content[:content.index(POSTURE_HEADING)] + content[content.index("## Scope"):]
    lines = ["# Concord — N" if line.startswith("# Concord — ") else line for line in shared.splitlines()]
    return "\n".join(lines)


def flatten(text: str) -> str:
    """Collapse whitespace so wrapped lines cannot hide a marker."""
    return " ".join(text.split())


def check_primary_prompts(root: Path) -> list[str]:
    findings: list[str] = []
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
            for fragment in HOST_TOOL_FRAGMENTS:
                if fragment in frontmatter:
                    findings.append(f"{name} frontmatter names host tool fragment {fragment!r}")
            if name in {"concord-1.md", "concord-2.md"} and "morph_edit" in frontmatter:
                findings.append(f"{name} frontmatter names host tool fragment 'morph_edit'")
            if "mode: primary" not in frontmatter:
                findings.append(f"{name} frontmatter does not declare mode: primary")
            if any(marker in flat for marker in LOOKUP_OBLIGATION_MARKERS):
                findings.append(f"{name} duplicates the technical-unknown conduct obligation")
            if name in ADVISORY_HANDOFF_MARKERS:
                for marker in ADVISORY_HANDOFF_MARKERS[name]:
                    if marker not in flat:
                        findings.append(f"{name} is missing its advisory handoff marker {marker!r}")
        except ValueError as error:
            findings.append(f"{name} is malformed: {error}")

    if any(" is malformed: " in finding for finding in findings):
        return findings

    shaping = texts["concord-1.md"]
    driving = texts["concord-2.md"]
    if shared_sections(shaping) != shared_sections(driving):
        findings.append("concord-1.md and concord-2.md differ outside the posture section")
    if frontmatter_field(split_frontmatter(shaping), "permission") != frontmatter_field(split_frontmatter(driving), "permission"):
        findings.append("concord-1.md and concord-2.md have different permission frontmatter")

    evidence = (root / "instructions" / "evidence.md").read_text(encoding="utf-8")
    for marker in LOOKUP_OBLIGATION_MARKERS:
        if marker not in evidence:
            findings.append(f"instructions/evidence.md is missing the lookup obligation marker {marker!r}")

    intake_frontmatter = split_frontmatter(texts["concord-0.md"])
    for boundary in (
        "edit: deny",
        "write: deny",
        "patch: deny",
        "morph_edit: deny",
        "concord_work_transition: deny",
        "concord_work_compact: deny",
    ):
        if boundary not in intake_frontmatter:
            findings.append(f"concord-0.md frontmatter lost its intake boundary {boundary!r}")
    intake_permission = intake_frontmatter.split("task:", 1)[0]
    if '"*": deny' not in intake_permission:
        findings.append("concord-0.md frontmatter does not deny bash by default")
    coordinator_frontmatter = split_frontmatter(shaping)
    if 'task:\n    "*": deny\n    "concord-*": allow' not in coordinator_frontmatter:
        findings.append("concord-1.md frontmatter lost the Concord-lane-only task boundary")
    return findings


def main() -> int:
    findings = check_primary_prompts(ROOT)
    for finding in findings:
        print(f"primary example finding: {finding}")
    if findings:
        return 1
    print(f"primary example check passed: {', '.join(PRIMARY_PROMPT_FILES)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
