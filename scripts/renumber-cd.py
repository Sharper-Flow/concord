#!/usr/bin/env python3
"""Move a CD record to a new number in one operation.

A CD number lives in several places that must move together: the decision
document filename, the document body, the record shard's ``id`` and ``path``,
the coverage shard's ``id``, the record's ``sha256``, the generated aggregates,
and every in-repository reference. Moving them by hand produced #392, a manifest
that validated while pointing at the wrong document.

This tool moves all of them, then proves the move by refusing to finish while
the old identifier survives anywhere in the tree.

It renumbers a CD its branch allocated and has not landed. A CD that already
exists on the comparison ref is durable law, and moving it is refused here for
the same reason ``check-cd-allocation.py`` reports it as a removal: a landed
number is referenced by records this repository cannot see.
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
CD_ID_RE = re.compile(r"^CD-[0-9]{4}$")
MANIFEST = "docs/concord-knowledge-index.v1.json"
DECISIONS = Path("docs/decisions")
RECORDS = Path("docs/knowledge/records")
COVERAGE = Path("docs/knowledge/coverage")

# Regenerated rather than edited, in dependency order. Each entry is the argv
# that rewrites the file from its authored source. A CD identifier reaches these
# outputs only through a source this tool edits directly, so regeneration
# carries the rename forward without a hand edit.
#
# Order is load-bearing, and the knowledge index appears twice on purpose.
# Renaming the shards makes the aggregate stale, and `check-knowledge-index.py
# --update` refuses to run against a stale aggregate, so the aggregate is built
# first. Recomputing the sha256 then changes the shards again, so the aggregate
# is rebuilt after it. The lane generator cascades into knowledge-index
# validation, so it runs last, once the hashes are settled.
GENERATORS: tuple[tuple[str, ...], ...] = (
    ("scripts/generate-knowledge-index.py", "--update"),
    ("scripts/check-knowledge-index.py", "--update"),
    ("scripts/generate-knowledge-index.py", "--update"),
    ("scripts/generate-law-coverage.py", "--update"),
    ("scripts/generate-agent-contracts.py",),
    ("scripts/generate-agent-lanes.py",),
)

# Outputs the generators own. This tool never edits them, and it fails when a
# generated file it does not know about carries the identifier being moved,
# rather than silently hand-editing a file with a DO NOT EDIT header.
GENERATED = frozenset(
    {
        "adapter/opencode/generated-agent-lanes.ts",
        "adapter/opencode/generated-contract-tests.ts",
        "adapter/opencode/generated-contracts.ts",
        "contracts/agent-lanes.digest",
        "docs/agent-lanes-contract.md",
        "docs/concord-knowledge-index.v1.json",
        "docs/law-coverage.v1.json",
        "internal/agent/generated_contracts.go",
        "internal/agent/generated_payload_schemas.go",
        "internal/store/generated_agent_lanes.go",
        "internal/store/generated_typed_error_kinds.go",
    }
)


@dataclass
class Plan:
    old: str
    new: str
    document: tuple[Path, Path]
    record: tuple[Path, Path]
    coverage: tuple[Path, Path]
    edits: list[Path] = field(default_factory=list)


def occurrence_re(identifier: str) -> re.Pattern[str]:
    """Match the identifier but not a longer number that starts with it."""
    return re.compile(re.escape(identifier) + r"(?![0-9])")


def git(root: Path, *arguments: str) -> subprocess.CompletedProcess[bytes]:
    return subprocess.run(
        ["git", *arguments], cwd=root, capture_output=True, check=False
    )


def tracked_files(root: Path) -> list[Path]:
    result = git(root, "ls-files", "-z")
    if result.returncode != 0:
        return []
    names = result.stdout.decode("utf-8").split("\0")
    return [Path(name) for name in names if name]


def read_text(path: Path) -> str | None:
    try:
        return path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError):
        return None


def landed_ids(root: Path, ref: str) -> set[str] | None:
    """CD ids present in the manifest at ``ref``, or None when it is unreachable."""
    result = git(root, "show", f"{ref}:{MANIFEST}")
    if result.returncode != 0:
        return None
    try:
        data = json.loads(result.stdout.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        return None
    records = data.get("records")
    if not isinstance(records, list):
        return None
    return {
        record["id"]
        for record in records
        if isinstance(record, dict)
        and isinstance(record.get("id"), str)
        and CD_ID_RE.fullmatch(record["id"])
    }


def find_document(root: Path, identifier: str, findings: list[str]) -> Path | None:
    matches = sorted((root / DECISIONS).glob(f"{identifier}-*.md"))
    if not matches:
        findings.append(f"no decision document matches {DECISIONS}/{identifier}-*.md")
        return None
    if len(matches) > 1:
        names = ", ".join(match.name for match in matches)
        findings.append(f"{identifier} matches more than one decision document: {names}")
        return None
    return matches[0].relative_to(root)


def plan(
    root: Path, old: str, new: str, *, against: str = "origin/main"
) -> tuple[list[str], Plan | None]:
    """Describe the move without performing it."""
    findings: list[str] = []
    for label, identifier in (("old", old), ("new", new)):
        if not CD_ID_RE.fullmatch(identifier):
            findings.append(f"{label} identifier {identifier!r} is not of the form CD-NNNN")
    if findings:
        return findings, None
    if old == new:
        return [f"{old} and {new} are the same identifier"], None

    # An unreachable ref proves nothing either way, so it does not block an
    # offline renumber; check-cd-allocation.py remains the enforcing check.
    landed = landed_ids(root, against)
    if landed is not None:
        if old in landed:
            findings.append(
                f"{old} is already on {against} and is durable law; renumber only a CD this "
                "branch allocated and has not landed"
            )
        if new in landed:
            findings.append(f"{new} is already on {against}; choose a free number")

    document = find_document(root, old, findings)
    record = RECORDS / f"{old}.json"
    coverage = COVERAGE / f"{old}.json"
    for path in (record, coverage):
        if not (root / path).is_file():
            findings.append(f"{path}: shard is missing; {old} is not a complete record")

    # The target must be entirely free. A partially occupied number produces a
    # manifest that still validates, which is the failure mode in #392.
    if (root / RECORDS / f"{new}.json").exists() or (root / COVERAGE / f"{new}.json").exists():
        findings.append(f"{new} already has a shard; choose a free number")
    if sorted((root / DECISIONS).glob(f"{new}-*.md")):
        findings.append(f"{new} already has a decision document; choose a free number")

    if findings:
        return findings, None
    assert document is not None

    old_re = occurrence_re(old)
    new_re = occurrence_re(new)
    edits: list[Path] = []
    for path in tracked_files(root):
        text = read_text(root / path)
        if text is None:
            continue
        posix = path.as_posix()
        if new_re.search(text) and posix not in GENERATED:
            findings.append(f"{posix}: already references {new}; choose a free number")
        if not old_re.search(text):
            continue
        if posix in GENERATED:
            continue
        if "DO NOT EDIT" in text[:2000]:
            findings.append(
                f"{posix}: carries a DO NOT EDIT header, references {old}, and no generator "
                "in this tool owns it; add its generator to GENERATORS and its output to GENERATED"
            )
            continue
        edits.append(path)

    if findings:
        return findings, None

    return [], Plan(
        old=old,
        new=new,
        document=(document, DECISIONS / document.name.replace(old, new, 1)),
        record=(record, RECORDS / f"{new}.json"),
        coverage=(coverage, COVERAGE / f"{new}.json"),
        edits=edits,
    )


def rename(root: Path, source: Path, target: Path, findings: list[str]) -> None:
    result = git(root, "mv", source.as_posix(), target.as_posix())
    if result.returncode != 0:
        # Not every caller runs inside a git work tree; fall back to a plain move
        # so the tool stays usable, and let the postcondition catch a bad move.
        try:
            (root / source).rename(root / target)
        except OSError as exc:
            findings.append(f"{source}: could not move to {target}: {exc}")


def rewrite_json(path: Path, changes: dict[str, str], findings: list[str]) -> None:
    text = read_text(path)
    if text is None:
        findings.append(f"{path}: could not read shard")
        return
    try:
        data = json.loads(text)
    except json.JSONDecodeError as exc:
        findings.append(f"{path}: invalid JSON: {exc}")
        return
    data.update(changes)
    path.write_text(json.dumps(data, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def run_generators(root: Path, findings: list[str]) -> None:
    for argv in GENERATORS:
        result = subprocess.run(
            [sys.executable, *argv], cwd=root, capture_output=True, check=False
        )
        if result.returncode != 0:
            detail = result.stderr.decode("utf-8", "replace").strip().splitlines()
            tail = detail[-1] if detail else "no stderr"
            findings.append(f"{argv[0]}: generator failed: {tail}")
            return


def survivors(root: Path, identifier: str) -> list[str]:
    pattern = occurrence_re(identifier)
    found: list[str] = []
    for path in tracked_files(root):
        text = read_text(root / path)
        if text is not None and pattern.search(text):
            found.append(path.as_posix())
    return sorted(found)


def apply(root: Path, prepared: Plan, *, generate: bool = True) -> list[str]:
    """Perform the move described by ``prepared`` and prove it landed."""
    findings: list[str] = []
    old_re = occurrence_re(prepared.old)

    for path in prepared.edits:
        text = read_text(root / path)
        if text is None:
            findings.append(f"{path}: could not read file")
            continue
        (root / path).write_text(old_re.sub(prepared.new, text), encoding="utf-8")

    rename(root, *prepared.document, findings)
    rename(root, *prepared.record, findings)
    rename(root, *prepared.coverage, findings)
    if findings:
        return findings

    rewrite_json(
        root / prepared.record[1],
        {"id": prepared.new, "path": prepared.document[1].as_posix()},
        findings,
    )
    rewrite_json(root / prepared.coverage[1], {"id": prepared.new}, findings)
    if findings:
        return findings

    if generate:
        run_generators(root, findings)
        if findings:
            return findings

    remaining = survivors(root, prepared.old)
    if remaining:
        findings.append(
            f"{prepared.old} survives in {len(remaining)} file(s) after the move: "
            + ", ".join(remaining[:5])
        )
    return findings


def describe(prepared: Plan) -> str:
    lines = [
        f"{prepared.old} -> {prepared.new}",
        f"  document {prepared.document[0]} -> {prepared.document[1]}",
        f"  record   {prepared.record[0]} -> {prepared.record[1]}",
        f"  coverage {prepared.coverage[0]} -> {prepared.coverage[1]}",
        f"  edits    {len(prepared.edits)} file(s)",
    ]
    lines.extend(f"    {path}" for path in prepared.edits)
    lines.append(f"  regenerate {len(GENERATORS)} generator run(s)")
    return "\n".join(lines)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("old", help="identifier to move, of the form CD-NNNN")
    parser.add_argument("new", help="free identifier to move it to")
    parser.add_argument(
        "--dry-run", action="store_true", help="describe the move without performing it"
    )
    parser.add_argument(
        "--against",
        default="origin/main",
        help="ref whose manifest holds the landed CDs (default: origin/main)",
    )
    parser.add_argument("--root", type=Path, default=ROOT, help="repository root")
    args = parser.parse_args()

    root = args.root.resolve()
    findings, prepared = plan(root, args.old, args.new, against=args.against)
    if findings:
        for finding in findings:
            print(finding)
        print(f"renumber refused: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    assert prepared is not None

    print(describe(prepared))
    if args.dry_run:
        print("dry run: nothing was written")
        return 0

    findings = apply(root, prepared)
    if findings:
        for finding in findings:
            print(finding)
        print(f"renumber failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print(f"renumbered {args.old} to {args.new}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
