#!/usr/bin/env python3
"""Files-to-records inverse coverage: detect unprocessed knowledge documents.

scripts/check-knowledge-index.py proves that every record in the manifest points
at a real, unmodified file. That is records-to-files; it does not detect files
that have no record at all. A document the manifest does not acknowledge is
not law for this Product regardless of how much it reads like a spec (issue
#295 knowledge-closure contract): an agent working the repo must never
conflate an unprocessed document with Product law. The structural answer is
files-to-records coverage, surfaced both as a per-file listing and as a count,
so a migration can declare itself closed only when the empty-set difference is
reached.

Severity is split per the recorded amendment. Unprocessed documents are
warn-severity with explicit per-file listing — a migration in progress must
not block on them, but the listing must exist. A `--strict` flag exits 1 on
any unprocessed document so the operator can use the same script for cutover
checks without weakening the contract. Records whose file is missing are
already hard-failed by check-knowledge-index; this script duplicates that as
a warn so the inverse report is readable in one place, not because the
existing check is insufficient.
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / "docs/concord-knowledge-index.v1.json"
DEFAULT_KNOWLEDGE_ROOTS = ("docs/",)

ROOT_RE = re.compile(r"^[a-zA-Z0-9._-]+(?:/[a-zA-Z0-9._-]+)*/$")
# An exclusion is either a directory prefix or a single markdown file. Generated
# build output such as docs/generated-agent-tool-surface.md is not knowledge
# awaiting formalization, and excluding its whole directory would hide the
# authored documents beside it.
EXCLUSION_RE = re.compile(r"^[a-zA-Z0-9._-]+(?:/[a-zA-Z0-9._-]+)*(?:/|\.md)$")
MAX_ROOTS = 16
MAX_EXCLUSIONS = 32
MAX_DISPOSITIONS = 1000
MAX_FINDINGS = 1000


class DuplicateKeyError(ValueError):
    """A JSON object repeated a key, so one value silently won."""


def reject_duplicate_pairs(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise DuplicateKeyError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def load_manifest(findings: list[str]) -> object:
    try:
        return json.loads(
            MANIFEST.read_text(encoding="utf-8"), object_pairs_hook=reject_duplicate_pairs
        )
    except (OSError, UnicodeDecodeError, json.JSONDecodeError, DuplicateKeyError) as exc:
        findings.append(f"{MANIFEST.name}: invalid JSON: {exc}")
        return None


def validate_root_path(
    value: str, prefix: str, findings: list[str], pattern: re.Pattern[str] = ROOT_RE, shape: str = "a relative directory path with trailing slash"
) -> bool:
    if not isinstance(value, str) or not pattern.fullmatch(value):
        findings.append(f"{prefix}: must be {shape}: {value!r}")
        return False
    if ".." in value.split("/"):
        findings.append(f"{prefix}: traversal segments are forbidden: {value!r}")
        return False
    if value.startswith("/"):
        findings.append(f"{prefix}: absolute paths are forbidden: {value!r}")
        return False
    parts = value.rstrip("/").split("/")
    if any(part in {"", ".", ".."} for part in parts):
        findings.append(f"{prefix}: empty or traversal path segment: {value!r}")
        return False
    return True


def validate_knowledge_roots(
    manifest: dict, findings: list[str]
) -> tuple[tuple[str, ...], tuple[str, ...]]:
    """Validate knowledge_roots and exclusions. Returns (roots, exclusions).

    Both fields default if absent; malformed values short-circuit to empty
    tuples so the rest of the report still runs.
    """
    raw_roots = manifest.get("knowledge_roots")
    if raw_roots is None:
        roots: list[str] = list(DEFAULT_KNOWLEDGE_ROOTS)
    elif isinstance(raw_roots, list):
        roots = list(raw_roots)
    else:
        findings.append("manifest: knowledge_roots must be an array")
        roots = []
    if len(roots) > MAX_ROOTS:
        findings.append(f"manifest: knowledge_roots carries more than {MAX_ROOTS} entries")
        roots = roots[:MAX_ROOTS]
    valid_roots: list[str] = []
    seen_roots: set[str] = set()
    for index, value in enumerate(roots):
        prefix = f"manifest.knowledge_roots[{index}]"
        if value in seen_roots:
            findings.append(f"{prefix}: duplicate entry: {value!r}")
            continue
        seen_roots.add(value)
        if validate_root_path(value, prefix, findings):
            valid_roots.append(value)

    raw_exclusions = manifest.get("exclusions")
    if raw_exclusions is None:
        exclusions: list[str] = []
    elif isinstance(raw_exclusions, list):
        exclusions = list(raw_exclusions)
    else:
        findings.append("manifest: exclusions must be an array")
        exclusions = []
    if len(exclusions) > MAX_EXCLUSIONS:
        findings.append(f"manifest: exclusions carries more than {MAX_EXCLUSIONS} entries")
        exclusions = exclusions[:MAX_EXCLUSIONS]
    valid_exclusions: list[str] = []
    seen_exclusions: set[str] = set()
    for index, value in enumerate(exclusions):
        prefix = f"manifest.exclusions[{index}]"
        if value in seen_exclusions:
            findings.append(f"{prefix}: duplicate entry: {value!r}")
            continue
        seen_exclusions.add(value)
        if validate_root_path(
            value,
            prefix,
            findings,
            EXCLUSION_RE,
            "a relative directory prefix with trailing slash, or a relative markdown file path",
        ):
            valid_exclusions.append(value)

    return tuple(valid_roots), tuple(valid_exclusions)


def validate_dispositions(manifest: dict, findings: list[str]) -> tuple[str, ...]:
    """Return the paths the manifest records as deliberately not formalized.

    Shape validation of a disposition belongs to check-knowledge-index.py,
    which owns the manifest contract. This function reads the paths and
    reports an entry it cannot read, so a malformed disposition cannot silently
    subtract nothing while looking like it subtracted something.
    """
    raw = manifest.get("dispositions")
    if raw is None:
        return ()
    if not isinstance(raw, list):
        findings.append("manifest: dispositions must be an array")
        return ()
    if len(raw) > MAX_DISPOSITIONS:
        findings.append(f"manifest: dispositions carries more than {MAX_DISPOSITIONS} entries")
        raw = raw[:MAX_DISPOSITIONS]
    paths: list[str] = []
    for index, entry in enumerate(raw):
        if not isinstance(entry, dict) or not isinstance(entry.get("path"), str):
            findings.append(f"manifest.dispositions[{index}]: malformed disposition skipped")
            continue
        paths.append(normalize(entry["path"]))
    return tuple(paths)


def normalize(path: str) -> str:
    return path.replace("\\", "/").lstrip("/")


def walk_markdown(root: Path, prefix: str) -> list[str]:
    """Return repository-relative POSIX paths of every *.md under root.

    The root is given as a forward-slash prefix relative to ROOT; files not
    found on disk contribute nothing (the schema's MAX_MANIFEST_PATH bound
    covers length, not existence). Sorting at the call site keeps the report
    deterministic across runs and platforms.
    """
    absolute = ROOT / prefix
    if not absolute.exists():
        return []
    found: list[str] = []
    for candidate in sorted(absolute.rglob("*.md")):
        if not candidate.is_file():
            continue
        relative = candidate.relative_to(ROOT).as_posix()
        found.append(relative)
    return found


def manifest_record_paths(manifest: dict) -> tuple[set[str], list[str]]:
    """Return (referenced_paths, malformed_record_paths).

    Path normalization follows the existing check-knowledge-index convention
    of forward slashes and no leading slash, so the comparison against
    walk_markdown's output is structural rather than representation-sensitive.
    """
    referenced: set[str] = set()
    malformed: list[str] = []
    for record in manifest.get("records") or []:
        if not isinstance(record, dict):
            malformed.append("<non-object record>")
            continue
        path = record.get("path")
        if not isinstance(path, str):
            malformed.append("<record without string path>")
            continue
        referenced.add(normalize(path))
    return referenced, malformed


def compute_unprocessed(
    walked: list[str],
    referenced: set[str],
    exclusions: tuple[str, ...],
    dispositions: tuple[str, ...] = (),
) -> list[str]:
    """Files under knowledge_roots that have no record, exclusion, or disposition.

    A directory exclusion ends in a slash and drops every walked file beneath
    it; the prefix comparison keeps the trailing slash so a typo like
    `docs/research` does not match `docs/researcher-notes/`. A file exclusion
    ends in `.md` and drops exactly that path, which is how generated build
    output is removed without hiding the authored documents beside it.

    A disposition subtracts one path too, but for the opposite reason: the file
    is source material the operator decided not to formalize, so it is counted
    and reported separately rather than folded into a green result.
    """
    prefixes = tuple(value for value in exclusions if value.endswith("/"))
    files = frozenset(value for value in exclusions if not value.endswith("/"))
    disposed = frozenset(dispositions)
    kept: list[str] = []
    for path in walked:
        if path in files or path in disposed:
            continue
        if any(path.startswith(prefix) for prefix in prefixes):
            continue
        if path not in referenced:
            kept.append(path)
    return kept


def compute_missing(referenced: set[str]) -> list[str]:
    """Manifest paths whose on-disk file does not exist (warn-mode mirror)."""
    missing: list[str] = []
    for path in sorted(referenced):
        if not (ROOT / path).is_file():
            missing.append(path)
    return missing


def report_unprocessed(unprocessed: list[str]) -> None:
    for path in unprocessed:
        print(f"unprocessed: {path}")
    print(
        f"unprocessed summary: {len(unprocessed)} file(s) under declared knowledge_roots "
        f"have no manifest record",
        file=sys.stderr,
    )


def report_dispositions(dispositions: tuple[str, ...]) -> None:
    """Report the disposition count on its own line, always.

    A disposed document is not processed knowledge; it is a document the
    operator refused to formalize. Folding it into the unprocessed count would
    hide a growing pile of refusals behind a shrinking backlog, so the number
    is printed even when it is zero.
    """
    for path in dispositions:
        print(f"disposition: {path}")
    print(
        f"disposition summary: {len(dispositions)} file(s) recorded as deliberately not formalized",
        file=sys.stderr,
    )


def report_missing(missing: list[str]) -> None:
    for path in missing:
        print(f"missing-record-file: {path}")
    if missing:
        print(
            f"missing-record-file summary: {len(missing)} manifest record(s) point at "
            f"a file that is not on disk",
            file=sys.stderr,
        )


def report(findings: list[str]) -> int:
    for finding in findings[:MAX_FINDINGS]:
        print(finding)
    if len(findings) > MAX_FINDINGS:
        print(f"... {len(findings) - MAX_FINDINGS} additional finding(s) omitted")
    if findings:
        print(f"knowledge closure check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("knowledge closure check passed")
    return 0


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument(
        "--strict",
        action="store_true",
        help="exit 1 when unprocessed documents are non-empty (cutover mode).",
    )
    args = parser.parse_args(argv)

    findings: list[str] = []
    manifest = load_manifest(findings)
    if not isinstance(manifest, dict):
        return report(findings)

    roots, exclusions = validate_knowledge_roots(manifest, findings)
    if not roots:
        return report(findings)

    walked: list[str] = []
    for root in roots:
        walked.extend(walk_markdown(ROOT, root))
    walked = sorted(set(walked))

    referenced, malformed = manifest_record_paths(manifest)
    for path in malformed:
        findings.append(f"manifest: malformed record path skipped: {path}")

    dispositions = validate_dispositions(manifest, findings)
    unprocessed = compute_unprocessed(walked, referenced, exclusions, dispositions)
    missing = compute_missing(referenced)

    if missing:
        findings.extend(f"missing-record-file: {path}" for path in missing)

    report_unprocessed(unprocessed)
    report_dispositions(dispositions)
    report_missing(missing)

    if findings:
        return report(findings)
    if unprocessed and args.strict:
        print(
            f"knowledge closure: strict mode refused: {len(unprocessed)} unprocessed document(s)",
            file=sys.stderr,
        )
        return 1
    print("knowledge closure check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
