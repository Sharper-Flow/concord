#!/usr/bin/env python3
"""Placement of Product knowledge under .concord/ (CD-0194).

check-knowledge-index.py proves that each record describes a real file and
check-knowledge-closure.py proves that each file under a knowledge root is
recorded. Neither proves where the knowledge lives. CD-0194 sets the default
Product law: all repository-authored Product knowledge lives under
`.concord/`, and a location outside it is valid only through an explicit
operator override recorded in the manifest head — never an inferred
exception.

This validator owns four propositions:

1. Default placement. Every declared knowledge root and every record path
   lives under `.concord/`, or is covered by an operator override.
2. Valid operator overrides. Each override names a path that exists on disk,
   a Product it belongs to, and a record that carries the operator's
   instruction. The anchor must be a manifest record, the only override
   anchor a repository check can verify, and it must be an accepted
   decision: the anchor's own document must carry the closed
   operator-instruction block that names the Product, names the overridden
   path, and records the approve decision. An anchor that merely exists —
   an unrelated record about something else — proves nothing and refuses.
   Prose never admits a placement: a document that discusses, denies, or
   negates an override in sentences carries no instruction block, so it
   refuses. A work item lives in the external store, so an identifier that
   merely looks like one refuses here rather than standing in for an
   instruction nobody can read from the repository.
3. Refusal of unapproved external placement. A record path outside
   `.concord/` with no override is a finding, and so is an exclusion that
   sits outside every knowledge root, because it silently subtracts nothing.
4. Absence of the retired root trees. The root `docs/`, `instructions/`, and
   `scenarios/` paths must not exist — as directories, files, or symlinks —
   and the three `.concord/` homes must be real directories, not symlinks.
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import knowledge_index  # noqa: E402

DEFAULT_TREE = ".concord/"
DEFAULT_ROOTS = (".concord/docs/",)
RETIRED_ROOT_TREES = ("docs", "instructions", "scenarios")
CONCORD_HOMES = (".concord/docs", ".concord/instructions", ".concord/scenarios")
MAX_FINDINGS = 1000

OVERRIDE_BLOCK_OPEN = "<!-- concord-operator-override"
OVERRIDE_BLOCK_CLOSE = "-->"
INSTRUCTION_FIELDS = ("decision", "path", "product")
APPROVE_DECISION = "approve"
DENY_DECISION = "deny"


def load_manifest(findings: list[str]) -> dict | None:
    """The composed manifest, or None with findings when it does not compose."""
    try:
        return json.loads(knowledge_index.compose_manifest_bytes(ROOT))
    except knowledge_index.ComposeError as exc:
        findings.extend(exc.findings)
        return None
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        findings.append(f"{knowledge_index.KNOWLEDGE_ROOT}: invalid JSON: {exc}")
        return None


def override_covers(override_path: str, candidate: str) -> bool:
    """True when one override path admits one candidate location.

    A directory override ends in a slash and covers every path beneath it,
    so `external/knowledge/` cannot be narrowed by a sibling such as
    `external/knowledge-notes/`. A file override ends in .md and covers
    exactly that path.
    """
    if override_path.endswith("/"):
        return candidate.startswith(override_path)
    return candidate == override_path


def covering_override(path: str, overrides: list[dict]) -> dict | None:
    for override in overrides:
        if not isinstance(override, dict):
            continue
        override_path = override.get("path")
        if isinstance(override_path, str) and override_covers(override_path, path):
            return override
    return None


def parse_override_instructions(text: str) -> tuple[list[dict], list[str]]:
    """Extract the closed operator-instruction blocks from a document.

    The instruction is an HTML comment, which CommonMark keeps out of the
    rendered document, so the grammar never competes with prose. A block
    opens with the opener line, closes with a lone `-->` line, and carries
    the fields `product`, `path`, and `decision` and no others; `decision`
    is `approve` or `deny`. Anything else in a block is a malformation, and
    prose outside blocks is never read for an instruction.
    """
    instructions: list[dict] = []
    malformations: list[str] = []
    inside = False
    start_line = 0
    fields: dict[str, str] = {}
    seen: set[str] = set()
    for number, line in enumerate(text.splitlines(), start=1):
        stripped = line.strip()
        if not inside:
            if stripped == OVERRIDE_BLOCK_OPEN:
                inside = True
                start_line = number
                fields = {}
                seen = set()
            continue
        if stripped == OVERRIDE_BLOCK_CLOSE:
            inside = False
            for required in INSTRUCTION_FIELDS:
                if required not in seen:
                    malformations.append(
                        f"line {start_line}: instruction block is missing the {required} field"
                    )
            if fields.get("decision") not in (None, APPROVE_DECISION, DENY_DECISION):
                malformations.append(
                    f"line {start_line}: decision must be '{APPROVE_DECISION}' or "
                    f"'{DENY_DECISION}', not {fields['decision']!r}"
                )
            instructions.append({"line": start_line, "fields": fields})
            continue
        if "-->" in stripped:
            malformations.append(
                f"line {number}: instruction block must close on its own line with '-->'"
            )
            continue
        name, separator, value = stripped.partition(":")
        if not separator or name not in INSTRUCTION_FIELDS:
            malformations.append(
                f"line {number}: instruction field must be one of "
                f"{', '.join(INSTRUCTION_FIELDS)}: {stripped!r}"
            )
            continue
        value = value.strip()
        if not value:
            malformations.append(f"line {number}: instruction field {name} carries no value")
            continue
        if name in seen:
            malformations.append(f"line {number}: duplicate instruction field {name}")
            continue
        seen.add(name)
        fields[name] = value
    if inside:
        malformations.append(f"line {start_line}: instruction block is never closed by '-->'")
    return instructions, malformations


def anchor_instruction_findings(
    prefix: str, override: dict, anchor: dict, findings: list[str]
) -> None:
    """Prove the anchor record is an accepted decision that carries the
    operator's approve instruction.

    The placement decision makes a manifest record the only override anchor
    a repository check can verify, because the anchor's own document is the
    instruction an operator can open and read. Existence alone proves
    nothing: the record must be an accepted decision, and the document must
    carry the closed instruction block whose product and path equal the
    override's exactly and whose decision approves. Prose that names the
    path and the Product, even with the words of an override, is not an
    instruction and never admits a placement, so an explicit denial in
    prose refuses exactly like silence.
    """
    recorded_in = anchor.get("id")
    if anchor.get("kind") != "decision":
        findings.append(
            f"{prefix}: recorded_in {recorded_in!r} is a {anchor.get('kind')!r} record; "
            "only a decision carries operator override authority"
        )
        return
    if anchor.get("status") != "accepted":
        findings.append(
            f"{prefix}: recorded_in {recorded_in!r} has status {anchor.get('status')!r}; "
            "only an accepted decision carries operator override authority"
        )
        return
    document = anchor.get("path")
    text: str | None = None
    if isinstance(document, str):
        try:
            text = (ROOT / document).read_text(encoding="utf-8")
        except (OSError, UnicodeDecodeError):
            text = None
    if text is None:
        findings.append(
            f"{prefix}: anchor document {document!r} cannot be read, so it carries no operator instruction"
        )
        return
    instructions, malformations = parse_override_instructions(text)
    for malformation in malformations:
        findings.append(f"{prefix}: anchor document instruction is malformed: {malformation}")
    path = override.get("path")
    product_id = override.get("product_id")
    if not isinstance(path, str) or not isinstance(product_id, str):
        return
    matching = [
        instruction
        for instruction in instructions
        if instruction["fields"].get("path") == path
        and instruction["fields"].get("product") == product_id
    ]
    if len(matching) > 1:
        findings.append(
            f"{prefix}: anchor document carries {len(matching)} operator instruction blocks "
            f"for Product {product_id!r} at {path!r}"
        )
        return
    if not matching:
        near_miss = any(
            instruction["fields"].get("path") == path
            or instruction["fields"].get("product") == product_id
            for instruction in instructions
        )
        detail = (
            "an instruction names the Product or the path but never both"
            if near_miss
            else "no operator instruction block is present"
        )
        findings.append(
            f"{prefix}: anchor document carries no operator instruction for "
            f"Product {product_id!r} at {path!r}: {detail}"
        )
        return
    if matching[0]["fields"].get("decision") == DENY_DECISION:
        findings.append(
            f"{prefix}: anchor document records a denial, not an approval, for "
            f"Product {product_id!r} at {path!r}"
        )


def validate_overrides(
    overrides: list[dict], records_by_id: dict[str, dict], findings: list[str]
) -> None:
    """An override admits a placement only when its anchor is real and
    carries the instruction.

    The path must exist: an override asserts an operator decision about a
    placement that is in use, not a reservation for one that is not. The
    recorded_in anchor must be a manifest record: an accepted decision whose
    document carries the closed operator-instruction block for the
    override's exact Product and path with the approve decision. Anything
    else refuses. A work-item identifier is never an anchor: the store is
    external live state a repository check cannot reach, so accepting its
    shape would admit an unverifiable instruction as law.
    """
    for index, override in enumerate(overrides):
        prefix = f"manifest.operator_overrides[{index}]"
        path = override.get("path")
        if not isinstance(path, str):
            findings.append(f"{prefix}: path must be a string")
            continue
        target = ROOT / path
        if path.endswith("/"):
            if target.is_symlink() or not target.is_dir():
                findings.append(f"{prefix}: override directory does not exist on disk: {path}")
        elif target.is_symlink() or not target.is_file():
            findings.append(f"{prefix}: override file does not exist on disk: {path}")
        recorded_in = override.get("recorded_in")
        anchor = records_by_id.get(recorded_in) if isinstance(recorded_in, str) else None
        if anchor is None:
            findings.append(
                f"{prefix}: recorded_in {recorded_in!r} is not a manifest record carrying the operator's instruction"
            )
            continue
        anchor_instruction_findings(prefix, override, anchor, findings)


def check_retired_root_trees(findings: list[str]) -> None:
    """The root trees are gone, and the .concord homes are real directories."""
    for name in RETIRED_ROOT_TREES:
        candidate = ROOT / name
        if candidate.is_symlink() or candidate.exists():
            findings.append(
                f"retired root tree is present: {name}; CD-0194 D3 removes it and forbids a compatibility symlink"
            )
    for home in CONCORD_HOMES:
        candidate = ROOT / home
        if candidate.is_symlink() or not candidate.is_dir():
            findings.append(f"default home is not a real directory: {home}")


def knowledge_roots_of(manifest: dict) -> list[str]:
    raw = manifest.get("knowledge_roots")
    if isinstance(raw, list):
        return [value for value in raw if isinstance(value, str)]
    return list(DEFAULT_ROOTS)


def check_placement(manifest: dict, findings: list[str]) -> None:
    overrides = manifest.get("operator_overrides")
    if not isinstance(overrides, list):
        overrides = []

    roots = knowledge_roots_of(manifest)
    for root in roots:
        if root.startswith(DEFAULT_TREE):
            continue
        if covering_override(root, overrides) is not None:
            continue
        findings.append(f"knowledge root outside the default tree without an operator override: {root}")

    records_by_id = {
        record.get("id"): record
        for record in manifest.get("records", [])
        if isinstance(record, dict) and isinstance(record.get("id"), str)
    }
    validate_overrides(overrides, records_by_id, findings)

    for number, record in enumerate(manifest.get("records", [])):
        if not isinstance(record, dict):
            continue
        path = record.get("path")
        if not isinstance(path, str) or path.startswith(DEFAULT_TREE):
            continue
        if covering_override(path, overrides) is not None:
            continue
        findings.append(
            f"manifest.records[{number}] ({record.get('id')}): unapproved external knowledge placement: {path}"
        )

    prefixes = tuple(value for value in roots if value.endswith("/"))
    for number, exclusion in enumerate(manifest.get("exclusions", []) if isinstance(manifest.get("exclusions"), list) else []):
        if not isinstance(exclusion, str):
            continue
        if exclusion.startswith(DEFAULT_TREE):
            continue
        if covering_override(exclusion, overrides) is not None:
            continue
        under_root = any(exclusion.startswith(prefix) for prefix in prefixes)
        if not under_root:
            findings.append(
                f"manifest.exclusions[{number}]: exclusion outside every knowledge root subtracts nothing: {exclusion}"
            )


def report(findings: list[str]) -> int:
    for finding in findings[:MAX_FINDINGS]:
        print(finding)
    if len(findings) > MAX_FINDINGS:
        print(f"... {len(findings) - MAX_FINDINGS} additional finding(s) omitted")
    if findings:
        print(f"knowledge placement check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("knowledge placement check passed")
    return 0


def main(argv: list[str] | None = None) -> int:
    findings: list[str] = []
    manifest = load_manifest(findings)
    check_retired_root_trees(findings)
    if isinstance(manifest, dict):
        check_placement(manifest, findings)
    return report(findings)


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
