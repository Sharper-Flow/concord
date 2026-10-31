#!/usr/bin/env python3
"""Move accepted law documents into .concord/law/ and repair references.

The accepted constitution, specification and decision records under
.concord/docs/knowledge/records/ name current law. This mover relocates
exactly those documents to .concord/law/{constitution,specs,decisions}/ under
their basenames, keeps the record shards where they are, and repairs the
repository through one moves map:

* Markdown and HTML links resolve from the source's old directory, map source
  and target onto the new tree, and relativize from the source's new
  directory. Inline links, images, href/src, and reference definitions
  rewrite; fragments carry through; external URLs and repository escapes stay.
* Exact path literals rewrite through the moves map only. Authored source
  files (Python, JSON, Go, TS, ...) get file-path literals only: admission
  rules and synthetic legacy fixtures name the old tree on purpose, so the
  frozen skip list keeps them and directory literals never rewrite there.
  Guards refuse lookalike paths (``~/``, ``other.concord/``, ``x.md.bak``, a
  longer prefix); pinned historical URLs keep the paths their commit had.
* Shards update path and evidence through the same map and recompute sha256
  whenever the bound bytes moved or changed, so each hash still binds its
  record to its document. Shard prose and every other record field keep
  their semantics.

Generated files are left to their generators and excluded from the survivor
proof. The mover refuses before writing on a dangling law path, an unreadable
shard, a target collision, or a law record outside .concord/docs/ and
.concord/law/ (operator override territory), and runs only inside a git work
tree. A second run over a relocated tree is a byte-for-byte no-op.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(Path(__file__).resolve().parent))
import markdown_references  # noqa: E402
import shard_format  # noqa: E402

LAW_ROOT = ".concord/law"
RECORDS = Path(".concord/docs/knowledge/records")
RECORDS_POSIX = RECORDS.as_posix()
# Law is what the current accepted records say it is, never a folder scan.
LAW_KINDS = frozenset({"constitution", "decision", "spec"})
KIND_DIRS = {"constitution": "constitution", "spec": "specs", "decision": "decisions"}

# Document surfaces this mover rewrites and re-checks.
DOCUMENT_SUFFIXES = {".md", ".html", ".htm"}
# Authored source surfaces for exact-path literal repair (moves map only,
# file paths only). Directory literals never rewrite here: admission rules
# and synthetic legacy fixtures name the old tree on purpose.
SOURCE_SUFFIXES = frozenset({".py", ".json", ".go", ".ts", ".js", ".yaml", ".yml", ".toml", ".sh"})
# Source files whose real-path literals must keep the old tree: the renumber
# fixtures pair with renumber-cd's own decisions constant, the knowledge
# fixtures exercise transitional old-tree admission, and the mover's own test
# corpus is synthetic.
SOURCE_LITERAL_SKIPPED = frozenset(
    {
        "scripts/test-relocate-law.py",
        "scripts/test-renumber-cd.py",
        "scripts/test-knowledge-index.py",
        "internal/store/knowledge_vocabulary_test.go",
        "scripts/check-agent-contracts.py",  # Paths at the shipped skills' pinned revisions.
    }
)
# Generator-owned outputs, never edited: their generators carry the move
# forward. .opencode/agents/ lane definitions carry frontmatter instead of a
# generated header but are equally generated.
GENERATED_PATHS = frozenset(
    {
        "adapter/opencode/generated-contract-tests.ts",
        "adapter/opencode/generated-contracts.ts",
        "internal/agentcontract/generated_contracts.go",
        "internal/payloadschema/generated_schemas.go",
        "internal/payloadschema/generated_envelope_schema.go",
        "internal/payloadschema/generated_typed_error_kinds.go",
    }
)
GENERATED_PREFIXES = (".opencode/agents/",)
GENERATED_MARKER_RE = re.compile(r"Code generated\b")

INLINE_RE = re.compile(
    r"(?P<bang>!?)\[(?P<text>[^\]]*)\]\(\s*(?:<(?P<angled>[^>]*)>|(?P<bare>[^)\s]+))"
    r"(?P<title>\s+[\"'][^)]*[\"'])?\s*\)"
)
HTML_RE = re.compile(
    r"(?P<attr>\b(?:href|src))\s*=\s*(?P<quote>[\"'])(?P<dest>[^\"']+)(?P=quote)", re.IGNORECASE
)
URL_RE = re.compile(r"https?://[^\s)\]>'\"<>]+")
FRAGMENT_PATTERN = r"#[^\s)\]}'\"]*"
RELATIVE_PREFIX_PATTERN = r"(?:\.{1,2}/)+"
# Characters whose presence before a path literal makes that occurrence part
# of a longer path, a home-anchored ``~/`` path, or a URL rather than a
# standalone reference to a repository path.
PREFIX_BLOCKED = set("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ._/:~#?&-")
SUFFIX_BLOCKED = set("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ._-#?&")
# A directory literal keeps a deeper path, so a following '/' is expected
# there and refused on a file literal.
SUFFIX_BLOCKED_DIRECTORY = SUFFIX_BLOCKED - {"/"}


@dataclass
class RelocationPlan:
    """Everything apply() writes, computed before anything is touched."""

    moves: list[tuple[Path, Path]] = field(default_factory=list)
    dir_moves: dict[str, str] = field(default_factory=dict)
    revisions: dict[Path, bytes] = field(default_factory=dict)
    removals: list[Path] = field(default_factory=list)
    document_paths: frozenset[Path] = field(default_factory=frozenset)
    source_paths: frozenset[Path] = field(default_factory=frozenset)


def git(root: Path, *arguments: str) -> subprocess.CompletedProcess[bytes]:
    return subprocess.run(["git", "-C", str(root), *arguments], capture_output=True, check=False)


def repository_files(root: Path) -> list[Path]:
    """Every file the repository carries, from Git's own rooted listing.

    Staged and untracked files alike, so authored-but-uncommitted documents
    are normal at move time. An empty list means Git could not answer; the
    caller refuses rather than guessing at a tree.
    """
    result = git(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
    if result.returncode != 0:
        return []
    names = result.stdout.decode("utf-8").split("\0")
    return [Path(name) for name in names if name]


def is_generated(relative: Path) -> bool:
    posix = relative.as_posix()
    return posix in GENERATED_PATHS or posix.startswith(GENERATED_PREFIXES)


def is_generated_document(relative: Path, text: str | None) -> bool:
    """A generator-owned output, or a file a generator stamps as its own.

    The marker needs both phrases: prose may quote ``DO NOT EDIT`` without
    being generated, and a generated file always names itself as generated.
    """
    if is_generated(relative) or text is None:
        return is_generated(relative)
    head = text[:2000]
    return bool(GENERATED_MARKER_RE.search(head)) and "DO NOT EDIT" in head


def norm(relative: str) -> str:
    return os.path.normpath(relative).replace(os.sep, "/")


def read_text(path: Path) -> str | None:
    try:
        return path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError):
        return None


def document_files(files: list[Path]) -> list[Path]:
    return [
        relative
        for relative in files
        if relative.suffix.lower() in DOCUMENT_SUFFIXES and RECORDS not in relative.parents
    ]


def source_files(files: list[Path]) -> list[Path]:
    return [
        relative
        for relative in files
        if relative.suffix.lower() in SOURCE_SUFFIXES and RECORDS not in relative.parents
    ]


def load_shards(root: Path, findings: list[str]) -> list[tuple[Path, dict]]:
    """Every record shard as (relative shard path, record object)."""
    shard_dir = root / RECORDS
    if not shard_dir.is_dir():
        findings.append(f"records directory is missing: {RECORDS_POSIX}")
        return []
    shards = sorted(shard_dir.glob("*.json"))
    if not shards:
        findings.append(f"records directory holds no shards: {RECORDS_POSIX}")
        return []
    loaded: list[tuple[Path, dict]] = []
    for shard in shards:
        try:
            record = json.loads(shard.read_text(encoding="utf-8"))
        except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
            findings.append(f"{RECORDS_POSIX}/{shard.name}: invalid JSON: {exc}")
            continue
        if not isinstance(record, dict):
            findings.append(f"{RECORDS_POSIX}/{shard.name}: shard is not an object")
            continue
        loaded.append((RECORDS / shard.name, record))
    return loaded


def map_target(target: str, moves: dict[str, str], dir_moves: dict[str, str]) -> str:
    """One resolved old repository path on the new tree."""
    if target in moves:
        return moves[target]
    if target in dir_moves:
        return dir_moves[target]
    for old_dir in sorted(dir_moves, key=len, reverse=True):
        if target.startswith(old_dir + "/"):
            return dir_moves[old_dir] + target[len(old_dir):]
    return target


def relativize(source_parent: str, target: str) -> str:
    return os.path.relpath(target, source_parent).replace(os.sep, "/")


def rewrite_destination(
    source_parent: str,
    new_source_parent: str,
    destination: str,
    moves: dict[str, str],
    dir_moves: dict[str, str],
) -> str | None:
    """Resolve one destination from the old source directory, map both ends,
    and relativize from the source's new directory. External URLs, pure
    fragments, and repository escapes stay (None); fragments carry through
    verbatim.
    """
    destination = destination.strip().strip("<>")
    if not destination or destination.startswith(("#", "//")):
        return None
    if urlsplit(destination).scheme:
        return None
    path_part, separator, fragment = destination.partition("#")
    if not path_part:
        return None
    resolved = norm(os.path.join(source_parent, unquote(path_part)))
    if resolved.startswith(".."):
        return None
    mapped = map_target(resolved, moves, dir_moves)
    if mapped == resolved and new_source_parent == source_parent:
        return destination
    new_destination = relativize(new_source_parent, mapped)
    return new_destination + (separator + fragment if separator else "")


def literal_occurrences(text: str, old: str):
    """Standalone occurrences of ``old``, with an optional fragment."""
    return re.finditer(re.escape(old) + r"(?:" + FRAGMENT_PATTERN + r")?", text)


def guarded_literal_replacements(
    text: str, old: str, new: str, *, directory: bool
) -> tuple[str, int]:
    """Rewrite standalone literals of ``old``; a neighbour that extends the
    token (``~/``, ``other.concord/``, ``x.md.bak``, a longer prefix, a URL
    segment) refuses. A directory literal keeps deeper segments; a file
    literal keeps only its fragment.
    """
    blocked_suffix = SUFFIX_BLOCKED_DIRECTORY if directory else SUFFIX_BLOCKED
    pieces: list[str] = []
    position = 0
    count = 0
    for match in literal_occurrences(text, old):
        start, end = match.span()
        if start > 0 and text[start - 1] in PREFIX_BLOCKED:
            continue
        if end < len(text) and text[end] in blocked_suffix:
            continue
        remainder = text[start + len(old):end]
        pieces.append(text[position:start])
        pieces.append(new + remainder)
        position = end
        count += 1
    if not pieces:
        return text, 0
    pieces.append(text[position:])
    return "".join(pieces), count


def literal_survives(text: str, old: str, *, directory: bool) -> bool:
    blocked_suffix = SUFFIX_BLOCKED_DIRECTORY if directory else SUFFIX_BLOCKED
    for match in literal_occurrences(text, old):
        start, end = match.span()
        if start > 0 and text[start - 1] in PREFIX_BLOCKED:
            continue
        if end < len(text) and text[end] in blocked_suffix:
            continue
        return True
    return False


def rewrite_document(text: str, source: str, moves: dict[str, str], dir_moves: dict[str, str]) -> str:
    """Links first, then the exact literals links did not cover."""
    source_parent = os.path.dirname(source)
    new_source_parent = os.path.dirname(map_target(source, moves, dir_moves))

    def map_destination(destination: str) -> str:
        rewritten = rewrite_destination(
            source_parent, new_source_parent, destination, moves, dir_moves
        )
        return destination if rewritten is None else rewritten

    def inline(match: re.Match[str]) -> str:
        old_destination = match.group("angled") if match.group("angled") is not None else match.group("bare")
        new_destination = map_destination(old_destination)
        if new_destination == old_destination:
            return match.group(0)
        label = match.group("text")
        code_label = label.startswith("`") and label.endswith("`")
        path_label = label[1:-1] if code_label else label
        label_destination = old_destination.removeprefix("./")
        if path_label.removeprefix("./") in {label_destination, label_destination.partition("#")[0]}:
            label = new_destination if "#" in path_label else new_destination.partition("#")[0]
            if code_label:
                label = f"`{label}`"
        if match.group("angled") is not None:
            body = f"<{new_destination}>{match.group('title') or ''}"
        else:
            body = f"{new_destination}{match.group('title') or ''}"
        return f"{match.group('bang')}[{label}]({body})"

    rewritten = INLINE_RE.sub(inline, text)
    rewritten = HTML_RE.sub(
        lambda match: match.group(0).replace(match.group("dest"), map_destination(match.group("dest"))),
        rewritten,
    )

    # Reference definitions: rewrite the destination in place, fence-aware,
    # and every usage follows its definition.
    lines = rewritten.splitlines(keepends=True)
    fence: tuple[str, int] | None = None
    for number, line in enumerate(lines):
        fence_match = markdown_references.FENCE_RE.match(line)
        if fence_match:
            delimiter, rest = fence_match.groups()
            if fence is None:
                fence = (delimiter[0], len(delimiter))
            elif delimiter[0] == fence[0] and len(delimiter) >= fence[1] and not rest.strip():
                fence = None
            continue
        if fence is not None:
            continue
        definition = markdown_references.DEFINITION_RE.match(line)
        if not definition:
            continue
        _label, angled, bare = definition.groups()
        old_destination = angled if angled is not None else bare
        new_destination = map_destination(old_destination)
        if new_destination == old_destination:
            continue
        start, end = definition.span(2) if angled is not None else definition.span(3)
        lines[number] = line[:start] + new_destination + line[end:]
    rewritten = "".join(lines)

    # Exact literals: what links did not name, prose and data still can.
    for old in sorted(moves, key=len, reverse=True):
        rewritten, _ = guarded_literal_replacements(rewritten, old, moves[old], directory=False)
    for old_dir in sorted(dir_moves, key=len, reverse=True):
        rewritten, _ = guarded_literal_replacements(
            rewritten, old_dir, dir_moves[old_dir], directory=True
        )
    return rewritten


def rewrite_source_literals(text: str, source: str, moves: dict[str, str]) -> str:
    """Exact file-path literals from the moves map, never a directory prefix.

    A relative prefix rewrites only when prefix and old path resolve from the
    source file's directory to exactly that old path; any other relative
    shape is ambiguous and stays.
    """
    updated = text
    for old in sorted(moves, key=len, reverse=True):
        updated, _ = guarded_literal_replacements(updated, old, moves[old], directory=False)
    source_parent = os.path.dirname(source)
    for old in sorted(moves, key=len, reverse=True):
        pattern = re.compile(
            r"(" + RELATIVE_PREFIX_PATTERN + r")("
            + re.escape(old) + r")(" + FRAGMENT_PATTERN + r")?"
        )
        pieces: list[str] = []
        position = 0
        for match in pattern.finditer(updated):
            start, end = match.span()
            prefix, fragment = match.group(1), match.group(3)
            if start > 0 and updated[start - 1] in PREFIX_BLOCKED:
                continue
            if end < len(updated) and updated[end] in SUFFIX_BLOCKED:
                continue
            if norm(os.path.join(source_parent, prefix + old)) != old:
                continue
            pieces.append(updated[position:start])
            pieces.append(prefix + moves[old] + (fragment or ""))
            position = end
        if pieces:
            pieces.append(updated[position:])
            updated = "".join(pieces)
    return updated


def derive_dir_moves(files: list[Path], moves: dict[str, str]) -> dict[str, str]:
    """Source directories whose every file relocates wholesale, one-to-one."""
    dir_moves: dict[str, str] = {}
    moved = set(moves)
    source_dirs = {os.path.dirname(path) for path in moved if os.path.dirname(path)}
    all_files = {relative.as_posix() for relative in files}
    for source_dir in sorted(source_dirs):
        prefix = source_dir + "/"
        descendants = [name for name in all_files if name.startswith(prefix)]
        if not descendants or not set(descendants) <= moved:
            continue
        new_dirs = {os.path.dirname(moves[member]) for member in descendants}
        if len(new_dirs) != 1:
            continue
        dir_moves[source_dir] = new_dirs.pop()
    return dir_moves


def plan(root: Path) -> tuple[list[str], RelocationPlan | None]:
    """Describe the relocation without performing it."""
    findings: list[str] = []
    shards = load_shards(root, findings)

    claimed: dict[str, str] = {}
    by_target: dict[str, str] = {}
    for shard, record in shards:
        kind = record.get("kind")
        status = record.get("status")
        if kind not in LAW_KINDS or status != "accepted":
            continue
        path = record.get("path")
        if not isinstance(path, str) or not path:
            findings.append(f"{shard.as_posix()}: accepted law record carries no path")
            continue
        if not (root / path).is_file():
            findings.append(f"{record.get('id')}: dangling law path: {path}")
            continue
        # .concord/docs/ is the old home, .concord/law/ the new one; a record
        # under the law root is a prior run's settled output. Any other root
        # is operator-override territory and is not relocated.
        if not (path.startswith(".concord/docs/") or path.startswith(LAW_ROOT + "/")):
            findings.append(
                f"{record.get('id')}: law record outside .concord/docs/ is operator-override "
                f"territory and is not relocated: {path}"
            )
            continue
        target = f"{LAW_ROOT}/{KIND_DIRS[kind]}/{Path(path).name}"
        if path in claimed and claimed[path] != target:
            findings.append(
                f"{path}: two records claim one document with different kinds "
                f"({Path(claimed[path]).parent.name} and {KIND_DIRS[kind]})"
            )
            continue
        claimed[path] = target
        if target in by_target and by_target[target] != path:
            findings.append(
                f"basename collision: {by_target[target]} and {path} both relocate to {target}"
            )
            continue
        by_target[target] = path

    for path, target in sorted(claimed.items()):
        if path != target and (root / target).exists():
            findings.append(f"target already exists: {target}")
    if findings:
        return findings, None

    moves = {path: target for path, target in sorted(claimed.items()) if path != target}
    files = repository_files(root)
    if not files:
        findings.append("git listed no repository files; run the mover inside a git work tree")
        return findings, None
    dir_moves = derive_dir_moves(files, moves)

    # Documents: links resolve, map, and relativize; literals follow.
    revisions: dict[Path, bytes] = {}
    for relative in document_files(files):
        if not (root / relative).is_file():
            continue
        text = read_text(root / relative)
        if text is None or is_generated_document(relative, text):
            continue
        updated = rewrite_document(text, relative.as_posix(), moves, dir_moves)
        if updated != text:
            revisions[relative] = updated.encode("utf-8")

    # Authored source files: exact file-path literals only, never directory
    # prefixes, with the frozen skip list keeping old-tree fixtures.
    for relative in source_files(files):
        if relative.as_posix() in SOURCE_LITERAL_SKIPPED:
            continue
        text = read_text(root / relative)
        if text is None or is_generated_document(relative, text):
            continue
        if not any(old in text for old in moves):
            continue
        updated = rewrite_source_literals(text, relative.as_posix(), moves)
        if updated != text:
            revisions[relative] = updated.encode("utf-8")

    # The shards. Path and evidence follow the move; the hash is recomputed
    # whenever the bytes it binds moved or changed. Document bytes are still
    # at their pre-move locations, so the lookup keys on those.
    for shard, record in shards:
        changed = False
        path = record.get("path")
        if isinstance(path, str) and path in moves:
            record["path"] = moves[path]
            changed = True
        evidence = record.get("evidence")
        if isinstance(evidence, list):
            updated_evidence = [
                moves.get(item, item) if isinstance(item, str) else item for item in evidence
            ]
            if updated_evidence != evidence:
                record["evidence"] = updated_evidence
                changed = True
        final_path = record.get("path")
        binds_rewritten = isinstance(final_path, str) and (
            final_path in moves.values() or Path(final_path) in revisions
        )
        if changed or binds_rewritten:
            payload: bytes | None = None
            if isinstance(path, str) and Path(path) in revisions:
                payload = revisions[Path(path)]
            elif isinstance(final_path, str) and Path(final_path) in revisions:
                payload = revisions[Path(final_path)]
            if payload is None and isinstance(path, str) and path != final_path:
                payload = read_bytes(root / path)
            if payload is None and isinstance(final_path, str):
                payload = read_bytes(root / final_path)
            if payload is not None:
                record["sha256"] = "sha256:" + hashlib.sha256(payload).hexdigest()
                changed = True
        if not changed:
            continue
        canonical = shard_format.canonical_bytes(record)
        if canonical != (root / shard).read_bytes():
            revisions[shard] = canonical

    prepared = RelocationPlan(
        moves=[(Path(old), Path(new)) for old, new in moves.items()],
        dir_moves=dir_moves,
        revisions=revisions,
        removals=[Path(old_dir) for old_dir in sorted(dir_moves)],
        document_paths=frozenset(document_files(files)),
        source_paths=frozenset(source_files(files)),
    )
    return findings, prepared


def read_bytes(path: Path) -> bytes | None:
    try:
        return path.read_bytes()
    except OSError:
        return None


def move_file(root: Path, source: Path, target: Path, findings: list[str]) -> None:
    (root / target).parent.mkdir(parents=True, exist_ok=True)
    result = git(root, "mv", source.as_posix(), target.as_posix())
    if result.returncode != 0:
        # git mv refuses an untracked file; a plain move keeps the run going
        # and the survivor proof catches a bad move.
        try:
            (root / source).rename(root / target)
        except OSError as exc:
            findings.append(f"{source.as_posix()}: could not move to {target.as_posix()}: {exc}")


def apply(root: Path, prepared: RelocationPlan) -> list[str]:
    """Perform the relocation described by ``prepared`` and prove it landed."""
    findings: list[str] = []
    for path, payload in sorted(prepared.revisions.items()):
        destination = root / path
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes(payload)

    for source, target in prepared.moves:
        if (root / target).exists():
            findings.append(f"target already exists: {target.as_posix()}")
            continue
        move_file(root, source, target, findings)
    if findings:
        return findings

    for directory in prepared.removals:
        candidate = root / directory
        if candidate.is_dir() and not any(candidate.iterdir()):
            candidate.rmdir()

    return survivors(root, prepared)


def survivors(root: Path, prepared: RelocationPlan) -> list[str]:
    """Documents and source files still naming an old law path after apply.

    Generated surfaces and the frozen source skip list are excluded, source
    files scan on file-path literals only, and URLs are masked first. The
    matcher is the rewrite matcher, so a literal the guards refuse to touch
    is never reported.
    """
    plan_moves = {old.as_posix(): new.as_posix() for old, new in prepared.moves}
    found: list[str] = []

    def still_names_old(relative: Path, *, directories: bool) -> bool:
        text = read_text(root / relative)
        if text is None:
            return False
        masked = URL_RE.sub("", text)
        if any(literal_survives(masked, old, directory=False) for old in plan_moves):
            return True
        return directories and any(
            literal_survives(masked, old_dir, directory=True) for old_dir in prepared.dir_moves
        )

    for relative in sorted(prepared.document_paths):
        if is_generated_document(relative, read_text(root / relative)):
            continue
        if still_names_old(relative, directories=True):
            found.append(relative.as_posix())
    for relative in sorted(prepared.source_paths):
        if relative.as_posix() in SOURCE_LITERAL_SKIPPED:
            continue
        if is_generated_document(relative, read_text(root / relative)):
            continue
        if still_names_old(relative, directories=False):
            found.append(relative.as_posix())
    return found


def describe(prepared: RelocationPlan) -> str:
    lines = [f"moves     {len(prepared.moves)} document(s)"]
    lines.extend(f"  {old.as_posix()} -> {new.as_posix()}" for old, new in prepared.moves)
    if prepared.dir_moves:
        lines.append(f"directories {len(prepared.dir_moves)}")
        lines.extend(f"  {old}/ -> {new}/" for old, new in sorted(prepared.dir_moves.items()))
    shards = sum(1 for path in prepared.revisions if RECORDS in path.parents)
    documents = sum(1 for path in prepared.revisions if path.suffix.lower() in DOCUMENT_SUFFIXES)
    sources = len(prepared.revisions) - shards - documents
    lines.append(f"rewrites  {documents} document(s), {sources} source file(s), {shards} shard(s)")
    lines.append(f"removals  {len(prepared.removals)} emptied director(y/ies)")
    return "\n".join(lines)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", type=Path, default=ROOT, help="repository root")
    parser.add_argument("--dry-run", action="store_true", help="describe the move without writing")
    arguments = parser.parse_args()

    root = arguments.root.resolve()
    findings, prepared = plan(root)
    if findings:
        for finding in findings:
            print(finding)
        print(f"relocation refused: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    assert prepared is not None

    print(describe(prepared))
    if arguments.dry_run:
        print("dry run: nothing was written")
        return 0

    findings = apply(root, prepared)
    if findings:
        for finding in findings:
            print(finding)
        print(f"relocation failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("relocated accepted law to .concord/law/")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
