#!/usr/bin/env python3
"""Validate bounded relative Markdown paths and heading anchors."""

from __future__ import annotations

import re
import subprocess
import sys
import unicodedata
from collections import defaultdict
from pathlib import Path
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[1]
MAX_FINDINGS = 200
LINK_RE = re.compile(r"(?<!!)\[[^\]]+\]\(([^)\s]+)(?:\s+[\"'][^)]*[\"'])?\)")
HREF_RE = re.compile(r"\bhref\s*=\s*[\"']([^\"']+)[\"']", re.IGNORECASE)
HEADING_RE = re.compile(r"^\s{0,3}(#{1,6})\s+(.+?)\s*#*\s*$")


def repository_files(pattern: str) -> list[Path]:
    result = subprocess.run(
        ["git", "ls-files", "-co", "--exclude-standard", "--", pattern],
        cwd=ROOT,
        check=True,
        capture_output=True,
        text=True,
    )
    return sorted({ROOT / line for line in result.stdout.splitlines() if line})


def slugify(heading: str) -> str:
    heading = re.sub(r"<[^>]+>", "", heading)
    heading = re.sub(r"!?(?:\[([^\]]+)\]\([^)]*\))", r"\1", heading)
    heading = heading.replace("`", "").lower()
    heading = "".join(
        char for char in unicodedata.normalize("NFKD", heading)
        if not unicodedata.combining(char)
    )
    heading = re.sub(r"[^\w\- ]", "", heading, flags=re.UNICODE)
    return re.sub(r"\s+", "-", heading).strip("-")


def anchors(path: Path) -> set[str]:
    counts: defaultdict[str, int] = defaultdict(int)
    result: set[str] = set()
    for line in path.read_text(encoding="utf-8").splitlines():
        match = HEADING_RE.match(line)
        if not match:
            continue
        base = slugify(match.group(2))
        index = counts[base]
        counts[base] += 1
        result.add(base if index == 0 else f"{base}-{index}")
    return result


def check_link(source: Path, line_number: int, destination: str, findings: list[str]) -> None:
    destination = destination.strip().strip("<>")
    if not destination or destination.startswith(("#", "//")):
        target_path = source
        fragment = destination[1:] if destination.startswith("#") else ""
    else:
        parsed = urlsplit(destination)
        if parsed.scheme or destination.startswith("//"):
            return
        target_path = (source.parent / unquote(parsed.path)).resolve()
        fragment = unquote(parsed.fragment)
    try:
        target_path.relative_to(ROOT)
    except ValueError:
        findings.append(f"{source.relative_to(ROOT)}:{line_number}: link escapes repository: {destination}")
        return
    if not target_path.exists():
        findings.append(f"{source.relative_to(ROOT)}:{line_number}: missing link target: {destination}")
        return
    if fragment and target_path.is_file() and target_path.suffix.lower() == ".md":
        if fragment.lower() not in {item.lower() for item in anchors(target_path)}:
            findings.append(
                f"{source.relative_to(ROOT)}:{line_number}: missing heading anchor: {destination}"
            )


def main() -> int:
    findings: list[str] = []
    for source in repository_files("*.md"):
        if not source.is_file():
            continue
        for line_number, line in enumerate(source.read_text(encoding="utf-8").splitlines(), 1):
            destinations = LINK_RE.findall(line) + HREF_RE.findall(line)
            for destination in destinations:
                if destination.startswith(("http:", "https:", "mailto:")):
                    continue
                check_link(source, line_number, destination, findings)

    for finding in findings[:MAX_FINDINGS]:
        print(finding)
    if len(findings) > MAX_FINDINGS:
        print(f"... {len(findings) - MAX_FINDINGS} additional finding(s) omitted")
    if findings:
        print(f"Markdown link validation failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("Markdown link validation passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
