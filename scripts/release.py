#!/usr/bin/env python3
"""Compute a conventional-commit release and render its changelog.

The script is intentionally independent of the workflow runner.  It reads Git
history, emits stable JSON, and can write the exact changelog used by a release.
"""
from __future__ import annotations

import argparse
import dataclasses
import json
import re
import subprocess
from pathlib import Path


SEMVER_TAG = re.compile(r"^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$")
CONVENTIONAL_HEADER = re.compile(
    r"^(?P<type>[A-Za-z][A-Za-z0-9-]*)(?:\((?P<scope>[^()\r\n]+)\))?(?P<breaking>!)?: (?P<subject>.+)$"
)
BREAKING_FOOTER = re.compile(r"^BREAKING(?:-| )CHANGE\s*:", re.IGNORECASE)


@dataclasses.dataclass(frozen=True)
class Commit:
    sha: str
    subject: str
    body: str
    commit_type: str | None
    scope: str | None
    breaking: bool
    bump: str | None


def run_git(repo: Path, *args: str) -> str:
    return subprocess.check_output(
        ["git", "-C", str(repo), *args], text=True, encoding="utf-8"
    )


def version_tuple(tag: str) -> tuple[int, int, int]:
    match = SEMVER_TAG.fullmatch(tag)
    if not match:
        raise ValueError(f"not a semantic version tag: {tag}")
    return tuple(int(part) for part in match.groups())  # type: ignore[return-value]


def latest_semver_tag(repo: Path) -> str | None:
    tags = [
        tag
        for tag in run_git(repo, "tag", "--merged", "HEAD", "--list").splitlines()
        if SEMVER_TAG.fullmatch(tag)
    ]
    if not tags:
        return None
    return max(tags, key=lambda tag: (version_tuple(tag), tag))


def constitutional_tag(repo: Path) -> str | None:
    try:
        run_git(repo, "rev-parse", "--verify", "constitutional-bootstrap^{commit}")
        return "constitutional-bootstrap"
    except subprocess.CalledProcessError:
        return None


def parse_commit(sha: str, subject: str, body: str) -> Commit:
    match = CONVENTIONAL_HEADER.fullmatch(subject)
    commit_type = match.group("type").lower() if match else None
    scope = match.group("scope") if match else None
    breaking = bool(
        match
        and (
            match.group("breaking")
            or any(BREAKING_FOOTER.match(line.strip()) for line in body.splitlines())
        )
    )
    if breaking:
        bump = "major"
    elif commit_type == "feat":
        bump = "minor"
    elif commit_type == "fix":
        bump = "patch"
    else:
        bump = None
    return Commit(sha, subject, body, commit_type, scope, breaking, bump)


def commits_since(repo: Path, boundary: str | None) -> list[Commit]:
    if boundary is None:
        return []
    revision = f"{boundary}..HEAD"
    raw = run_git(
        repo,
        "log",
        "--no-merges",
        "--no-decorate",
        "--no-color",
        "--format=%H%x1f%s%x1f%b%x1e",
        revision,
    )
    commits: list[Commit] = []
    for record in raw.split("\x1e"):
        record = record.strip("\n")
        if not record:
            continue
        fields = record.split("\x1f", 2)
        if len(fields) != 3:
            raise ValueError("unexpected git log record")
        commits.append(parse_commit(*fields))
    return commits


def select_bump(commits: list[Commit]) -> str | None:
    for bump in ("major", "minor", "patch"):
        if any(commit.bump == bump for commit in commits):
            return bump
    return None


def next_version(base: tuple[int, int, int], bump: str) -> tuple[int, int, int]:
    major, minor, patch = base
    if bump == "major":
        return major + 1, 0, 0
    if bump == "minor":
        return major, minor + 1, 0
    if bump == "patch":
        return major, minor, patch + 1
    raise ValueError(f"unknown bump: {bump}")


def changelog(version: str, commits: list[Commit]) -> str:
    groups: dict[str, list[Commit]] = {
        "Breaking Changes": [],
        "Features": [],
        "Fixes": [],
        "Other Changes": [],
    }
    for commit in commits:
        if commit.breaking:
            group = "Breaking Changes"
        elif commit.commit_type == "feat":
            group = "Features"
        elif commit.commit_type == "fix":
            group = "Fixes"
        else:
            group = "Other Changes"
        groups[group].append(commit)

    lines = [f"# {version}", ""]
    for title, grouped in groups.items():
        if not grouped:
            continue
        lines.extend([f"## {title}", ""])
        for commit in grouped:
            label = f"**{commit.scope}:** " if commit.scope else ""
            lines.append(f"- {label}{commit.subject} (`{commit.sha[:7]}`)")
        lines.append("")
    return "\n".join(lines).rstrip() + "\n"


def compute(repo: Path) -> dict[str, object]:
    tag = latest_semver_tag(repo)
    boundary = tag or constitutional_tag(repo)
    commits = commits_since(repo, boundary)
    bump = select_bump(commits)
    base = version_tuple(tag) if tag else (0, 0, 0)
    released = next_version(base, bump) if bump else None
    version = f"v{released[0]}.{released[1]}.{released[2]}" if released else None
    return {
        "base_tag": tag,
        "commit_boundary": boundary,
        "base_version": f"v{base[0]}.{base[1]}.{base[2]}",
        "bump": bump,
        "release": bump is not None,
        "tag": version,
        "version": version,
        "commits": [dataclasses.asdict(commit) for commit in commits],
        "changelog": changelog(version, commits) if version else "",
    }


def write_outputs(result: dict[str, object], output_dir: Path, github_output: Path | None) -> None:
    output_dir.mkdir(parents=True, exist_ok=True)
    (output_dir / "release.json").write_text(
        json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    (output_dir / "CHANGELOG.md").write_text(str(result["changelog"]), encoding="utf-8")
    if github_output:
        release = "true" if result["release"] else "false"
        version = str(result["version"] or "")
        with github_output.open("a", encoding="utf-8") as stream:
            stream.write(f"should_release={release}\n")
            stream.write(f"version={version}\n")
            stream.write(f"tag={version}\n")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("--output-dir", type=Path)
    parser.add_argument("--github-output", type=Path)
    args = parser.parse_args()
    result = compute(args.repo.resolve())
    if args.output_dir:
        write_outputs(result, args.output_dir, args.github_output)
    else:
        print(json.dumps(result, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
