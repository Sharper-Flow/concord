#!/usr/bin/env python3
"""Move a CD record to a new number in one operation.

A CD number lives in several places that must move together: the decision
document filename under `.concord/law/decisions/`, the document body, and the
document's authored frontmatter ``id``. Folder placement supplies the path,
the kind, and the content digest, so no second record file follows the move.
Moving them by hand can leave a law file whose frontmatter names an identifier
its filename no longer carries.

This tool moves all of them, then proves the move by refusing to finish while
the old identifier survives anywhere in the tree.

It renumbers a CD its branch allocated and has not landed. Whether a record
is landed is a question of lineage, not of any mutable record field: the
document at the comparison ref, compared with the document at the merge base
of this branch and that ref, proves what this branch carried before it could
change anything. A tree document equal to the landed one, or descended from
it, is durable law, and moving it is refused here: a landed number is
referenced by records this repository cannot see. A document the branch
allocated after the merge base is branch-local even when the comparison ref
later claimed the identifier for another record; renumbering this branch's
document is the remedy for that collision. A changed title on an
otherwise-landed document is drift, not a new allocation, and refuses like
the record it edits. This holds when both sides changed the document after
the merge base: an amendment on the comparison ref and a local edit are two
changes to one durable record, and the merge base carrying the identifier is
what proves the lineage.
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
# The law decisions tree is the one canonical home; folder membership supplies
# law status, so a document outside it is not a record this tool moves.
DECISION_TREE = Path(".concord/law/decisions")

FRONT_MATTER_RE = re.compile(r"\A---\r?\n(.*?)\r?\n---\r?\n", re.DOTALL)

# Regenerated rather than edited, in dependency order. Each entry is the argv
# that rewrites the file from its authored source. A CD identifier reaches
# these outputs only through a source this tool edits directly, so
# regeneration carries the rename forward without a hand edit.
GENERATORS: tuple[tuple[str, ...], ...] = (
    ("scripts/generate-agent-contracts.py",),
    ("scripts/generate-agent-profiles.py",),
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
        ".concord/docs/agent-lanes-contract.md",
        "internal/agentcontract/generated_contracts.go",
        "internal/payloadschema/generated_schemas.go",
        "internal/payloadschema/generated_envelope_schema.go",
        "internal/store/generated_agent_lanes.go",
        "internal/payloadschema/generated_typed_error_kinds.go",
    }
)


@dataclass
class Plan:
    old: str
    new: str
    document: tuple[Path, Path]
    edits: list[Path] = field(default_factory=list)


def occurrence_re(identifier: str) -> re.Pattern[str]:
    """Match the identifier but not a longer number that starts with it."""
    return re.compile(re.escape(identifier) + r"(?![0-9])")


def git(root: Path, *arguments: str) -> subprocess.CompletedProcess[bytes]:
    return subprocess.run(
        ["git", *arguments], cwd=root, capture_output=True, check=False
    )


def repository_files(root: Path) -> list[Path]:
    """Every file the repository carries, whether or not it is staged yet.

    A CD is authored before it is committed, so an untracked document is the
    normal state at renumber time. This population feeds the rewrite set, the
    collision precondition, and the survivor proof alike; restricting it to the
    index makes all three blind to the document being renamed.
    """
    result = git(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
    if result.returncode != 0:
        return []
    names = result.stdout.decode("utf-8").split("\0")
    return [Path(name) for name in names if name]


def read_text(path: Path) -> str | None:
    try:
        return path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError):
        return None


def front_matter(text: str | None) -> dict | None:
    """A law document's authored frontmatter object, or None when absent."""
    if text is None:
        return None
    match = FRONT_MATTER_RE.match(text)
    if not match:
        return None
    try:
        record = json.loads(match.group(1))
    except json.JSONDecodeError:
        return None
    return record if isinstance(record, dict) else None


def tree_stem_paths(root: Path, ref: str, identifier: str) -> list[Path] | None:
    """Law decision paths at ``ref`` whose basename starts ``identifier-``.

    None means the ref yielded no tree this tool can read, which proves
    nothing either way and never blocks an offline renumber.
    """
    result = git(root, "ls-tree", "-r", "--name-only", "-z", ref, "--", str(DECISION_TREE))
    if result.returncode != 0:
        return None
    names = result.stdout.decode("utf-8", "replace").split("\0")
    return [
        Path(name)
        for name in names
        if name and Path(name).name.startswith(f"{identifier}-")
    ]


def blob_bytes(root: Path, ref: str, path: Path) -> bytes | None:
    result = git(root, "show", f"{ref}:{path.as_posix()}")
    if result.returncode != 0:
        return None
    return result.stdout


def working_tree_record(root: Path, document: Path, identifier: str) -> dict | None:
    """The tree document's frontmatter, or None when the record is incomplete.

    None covers a document whose frontmatter is absent, malformed, or names
    another identifier: not a record this tool can move, and the caller
    refuses with a named finding.
    """
    record = front_matter(read_text(root / document))
    if record is None or record.get("id") != identifier:
        return None
    return record


def merge_base(root: Path, ref: str) -> str | None:
    """The best common ancestor of HEAD and ``ref``, or None when git cannot
    name one."""
    result = git(root, "merge-base", "HEAD", ref)
    if result.returncode != 0:
        return None
    base = result.stdout.decode("utf-8", "replace").strip()
    return base or None


def find_document(root: Path, identifier: str, findings: list[str]) -> Path | None:
    matches: list[Path] = sorted((root / DECISION_TREE).glob(f"{identifier}-*.md"))
    if not matches:
        findings.append(f"no decision document matches {DECISION_TREE}/{identifier}-*.md")
        return None
    if len(matches) > 1:
        names = ", ".join(match.relative_to(root).as_posix() for match in matches)
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
    # offline renumber.
    # Landed is a property of lineage, not of any mutable record field: the
    # merge base proves which record this branch carried before the branch
    # could have changed it. A tree document equal to the landed one, or
    # descended from it, is durable law — a changed title does not make it
    # branch-local. A document absent from the merge base is this branch's own
    # allocation even when the ref later claimed the identifier for another
    # record; that collision is the case whose prescribed remedy is this
    # renumber.
    landed_old_paths = tree_stem_paths(root, against, old)
    document = find_document(root, old, findings)
    record = working_tree_record(root, document, old) if document is not None else None
    if document is not None and record is None:
        findings.append(
            f"{DECISION_TREE / document.name}: frontmatter does not carry id {old}; "
            f"{old} is not a complete record"
        )
    if landed_old_paths is not None:
        if landed_old_paths and document is not None:
            landed_bytes = blob_bytes(root, against, landed_old_paths[0])
            working_bytes = (root / document).read_bytes()
            if landed_bytes is not None and working_bytes == landed_bytes:
                findings.append(
                    f"{old} is already on {against} and is durable law; renumber only a CD this "
                    "branch allocated and has not landed"
                )
            else:
                base = merge_base(root, against)
                base_old = tree_stem_paths(root, base, old) if base else []
                # The merge base carrying the id is what proves lineage, not
                # agreement between the two landed copies: an amendment on
                # the comparison ref after the merge base, and a local edit
                # on this branch, are two edits of one durable record, and
                # neither makes it branch-local.
                if base_old:
                    findings.append(
                        f"{old} descends from the landed record on {against} (the merge base "
                        f"{base[:12]} already carried it); local changes such as a new title do "
                        "not make it branch-local, and renumbering would move durable law"
                    )
        if tree_stem_paths(root, against, new):
            findings.append(f"{new} is already on {against}; choose a free number")

    # The target must be entirely free: no decision document with that number,
    # and no law file whose frontmatter already claims the identifier.
    if sorted((root / DECISION_TREE).glob(f"{new}-*.md")):
        findings.append(
            f"{new} already has a decision document in {DECISION_TREE}; choose a free number"
        )
    for path in repository_files(root):
        if path.parent != DECISION_TREE or path.suffix != ".md":
            continue
        claimed = front_matter(read_text(root / path))
        if claimed is not None and claimed.get("id") == new:
            findings.append(
                f"{path.as_posix()} already carries law id {new}; choose a free number"
            )
            break

    if findings:
        return findings, None
    assert document is not None

    old_re = occurrence_re(old)
    new_re = occurrence_re(new)
    edits: list[Path] = []
    for path in repository_files(root):
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

    # The renamed document stays in the law decisions tree: the folder is the
    # one canonical home, and the folder supplies kind, path, and digest.
    return [], Plan(
        old=old,
        new=new,
        document=(document, document.parent / document.name.replace(old, new, 1)),
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
    for path in repository_files(root):
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

    rename(root, prepared.document[0], prepared.document[1], findings)
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
        help="ref whose law folder holds the landed CDs (default: origin/main)",
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
