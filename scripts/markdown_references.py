"""Bounded single-line Markdown references shared by citation and link checks."""

import re


DEFINITION_RE = re.compile(
    r"^\s{0,3}\[([^\]]+)\]:\s*(?:<([^>\n]+)>|(\S+))"
    r"(?:[ \t]+(?:\".*\"|'.*'|\(.*\)))?[ \t]*$"
)
LABEL_RE = re.compile(r"(?<!!)\[([^\]\n]+)\]")
SUFFIX_RE = re.compile(r"\[([^\]\n]*)\]")
FENCE_RE = re.compile(r"^\s{0,3}(`{3,}|~{3,})(.*)$")


def label_key(label: str) -> str:
    return " ".join(label.split()).casefold()


def reference_definitions(lines: list[str]) -> dict[str, str]:
    definitions: dict[str, str] = {}
    fence: tuple[str, int] | None = None
    for line in lines:
        match = FENCE_RE.match(line)
        if match:
            delimiter, rest = match.groups()
            if fence is None:
                fence = (delimiter[0], len(delimiter))
            elif delimiter[0] == fence[0] and len(delimiter) >= fence[1] and not rest.strip():
                fence = None
            continue
        if fence is None:
            match = DEFINITION_RE.match(line)
            if match:
                label, angled, bare = match.groups()
                definitions.setdefault(label_key(label), angled or bare)
    return definitions


def reference_links(line: str, definitions: dict[str, str]):
    if DEFINITION_RE.match(line):
        return
    position = 0
    while match := LABEL_RE.search(line, position):
        label = match.group(1)
        position = match.end()
        suffix = SUFFIX_RE.match(line, position)
        if suffix:
            destination = definitions.get(label_key(suffix.group(1) or label))
            if destination is not None:
                position = suffix.end()
                yield (match.start(), position), destination
                continue
        if line[position:].startswith("("):
            continue
        destination = definitions.get(label_key(label))
        if destination is not None:
            yield (match.start(), position), destination
