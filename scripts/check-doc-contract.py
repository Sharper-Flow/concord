#!/usr/bin/env python3
"""Per-record doc contract: outline, Gherkin ACs, STE subset.

scripts/check-knowledge-index.py proves that every manifest record points at a
real, unmodified file; it says nothing about whether that file conforms to
the kind's required outline. A record with a body that violates the kind's
contract is out of spec by construction (issue #295 prose-contract extension):
the record and the artifact agree, but they agree on something the contract
does not allow.

This validator enforces three machine-checkable layers on records whose kind
is in scope, gating hard-fail mode behind a manifest-level flag so the
existing corpus can dogfood the rule before it blocks CI.

  outline       exact-case headings under the kind's required_sections list
  ac grammar    ac_required true requires an "Acceptance criteria" section
                whose every criterion parses as Given/When/Then (Given
                optional; Then required; exactly one When, because a second
                trigger is a second criterion). ac_required false forbids the
                section outright: only a spec carries acceptance criteria, so
                any other kind that grows one is claiming a testable contract
                its kind cannot hold.
  ac coverage   the "Verification" section states at least as many entries as
                there are criteria, so no criterion is left unproven
  ste subset    sentence length ≤ 40 words, banned phrases absent,
                abbreviation discipline on first use

A doc's failure to satisfy these layers is a *finding*; the policy decision
of whether a finding is a hard fail is the `doc_contract.enforced` manifest
flag. When false, the script runs in report-only mode in CI (prints findings,
exits 0) so the curation pass can complete before enforcement. When true,
the same findings exit 1.
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / "docs/concord-knowledge-index.v1.json"
MAX_FINDINGS = 1000
MAX_SENTENCE_WORDS = 40
# The record kinds a doc contract may address, in taxonomy order. A kind absent
# from the manifest's doc_contract is not checked at all; a kind present is
# checked against its outline, its acceptance-criteria rule, and the STE subset.
DOC_CONTRACT_KINDS = ("constitution", "decision", "spec", "lesson", "reference", "research")
DOC_CONTRACT_FIELDS = {"enforced", *DOC_CONTRACT_KINDS, "banned_phrases"}
DEFAULT_ABBREVIATION_ALLOWLIST = frozenset(
    {
        "JSON", "API", "CLI", "TUI", "SQL", "WAL", "CI", "PR", "ADV", "TS",
        "PM", "CD", "MCP", "AST", "RFC", "LLM", "ID", "UUID", "HTTP",
        # Software technical nouns and units (ASD-STE100 §1 permits domain
        # terms; these are names, not expandable phrase abbreviations):
        "ADR", "AI", "ANSI", "ASCII", "CAS", "CDP", "CGO", "CLA", "CPU",
        "CRDT", "CRUD", "CQRS", "DB", "DDL", "DSL", "EAV", "EARS", "ESC",
        "FFI", "FTS", "GUI", "IDE", "IO", "IPC", "KB", "KV", "LMDB", "MB",
        "MVCC", "NUL", "OID", "OS", "OSS", "POSIX", "RCA", "RDF", "README",
        "REST", "SDK", "SDLC", "SGR", "SHA", "SLSA", "SLO", "SSRF", "UTF",
        "UID", "URL", "UTC", "UI", "WASM", "WIP",
    }
)
# Uppercase words that are tokens of the languages being documented, not
# abbreviations of an author's prose. SQL keywords and SQLite pragma values
# appear verbatim in documents about queries, schemas, and durability
# settings; expanding them on first use would put fiction into technical
# writing ("Structured Query Language (SELECT)" is wrong — SELECT is the
# token, not the abbreviation).
SQL_KEYWORD_TOKENS = frozenset(
    {
        "AND", "CHECK", "DELETE", "EXPLAIN", "FULL", "IS", "NOT", "PLAN",
        "PRAGMA", "QUERY", "STRICT", "TEXT", "TRUNCATE", "UPDATE",
        "INSERT", "SELECT", "WHERE", "FROM", "NORMAL",
    }
)
# RFC 2119 requirement keywords quoted uppercase in requirement statements.
# Their meaning is defined by the cited RFC, not by the document at hand.
RFC2119_KEYWORDS = frozenset({"MUST", "SHALL", "SHOULD", "MAY", "REQUIRED", "OPTIONAL"})
# Version-bump classes named by the semver policy (Conventional Commit
# titles map type/breaking to MAJOR and MINOR), the MIT license name, the
# CD-NNNN record-number placeholder, and the Language Server Protocol —
# each a defined name, not an expandable phrase abbreviation.
DEFINED_NAME_TOKENS = frozenset({"MAJOR", "MINOR", "MIT", "NNNN", "LSP", "SCIP"})
STRUCTURAL_TOKEN_EXCLUSIONS = SQL_KEYWORD_TOKENS | RFC2119_KEYWORDS | DEFINED_NAME_TOKENS
DEFAULT_BANNED_PHRASES = (
    "in order to",
    "utilize",
    "leverage",
    "it is important to note",
    "needless to say",
    "at the end of the day",
)
HEADING_RE = re.compile(r"^\s{0,3}(#{1,6})\s+(.+?)\s*#*\s*$")
LIST_ITEM_RE = re.compile(r"^\s*(?:[-*]|\d+\.)\s+(.*)")
TABLE_ROW_RE = re.compile(r"^\s*\|")
CODE_FENCE_RE = re.compile(r"^\s{0,3}```")
INLINE_CODE_RE = re.compile(r"``[^`]+``|`[^`]+`")
HTML_COMMENT_RE = re.compile(r"<!--.*?-->", re.DOTALL)
ABBR_RE = re.compile(r"\b([A-Z]{2,})\b")
# Keyword counting is word-bounded so "Whenever" and "Thenceforth" do not
# register as clauses. A raw substring count makes the clause tally unusable
# as a granularity signal, because prose that merely starts with the keyword
# letters inflates it.
GHERKIN_KEYWORD_RE = {
    keyword: re.compile(rf"\b{keyword}\b") for keyword in ("Given", "When", "Then")
}
SENTENCE_SPLIT_RE = re.compile(r"[.!?]+\s+")
WORD_RE = re.compile(r"\b\w+\b", re.UNICODE)


class DuplicateKeyError(ValueError):
    pass


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
            MANIFEST.read_text(encoding="utf-8"),
            object_pairs_hook=reject_duplicate_pairs,
        )
    except (OSError, UnicodeDecodeError, json.JSONDecodeError, DuplicateKeyError) as exc:
        findings.append(f"{MANIFEST.name}: invalid JSON: {exc}")
        return None


def bounded_text_list(value: object, maximum: int, minimum: int = 0) -> bool:
    """A bounded array of trimmed, non-empty strings.

    `minimum` bounds the array, not its members: an empty section name is
    never a section name, whatever the array's own lower bound is.
    """
    if not isinstance(value, list):
        return False
    if not (minimum <= len(value) <= maximum):
        return False
    return all(isinstance(item, str) and 0 < len(item.strip()) <= 256 for item in value)


def unique_string_list(value: object, maximum: int, minimum: int = 0) -> bool:
    if not bounded_text_list(value, maximum, minimum):
        return False
    seen: set[str] = set()
    for item in value:
        stripped = item.strip()
        if stripped in seen:
            return False
        seen.add(stripped)
    return True


def validate_doc_contract(manifest: dict, findings: list[str]) -> dict | None:
    """Validate and return the doc_contract block, or None on failure."""
    contract = manifest.get("doc_contract")
    if not isinstance(contract, dict):
        findings.append("manifest: doc_contract must be an object")
        return None
    unknown = set(contract) - DOC_CONTRACT_FIELDS
    if unknown:
        findings.append(f"manifest.doc_contract: unknown fields: {sorted(unknown)}")

    if "enforced" in contract and not isinstance(contract["enforced"], bool):
        findings.append("manifest.doc_contract: enforced must be a boolean")
        contract["enforced"] = False

    for kind in DOC_CONTRACT_KINDS:
        body = contract.get(kind)
        if body is None:
            continue
        if not isinstance(body, dict):
            findings.append(f"manifest.doc_contract.{kind}: must be an object")
            continue
        body_unknown = set(body) - {"required_sections", "ac_required"}
        if body_unknown:
            findings.append(
                f"manifest.doc_contract.{kind}: unknown fields: {sorted(body_unknown)}"
            )
        if not unique_string_list(body.get("required_sections"), 32, minimum=1 if kind == "spec" else 0):
            findings.append(
                f"manifest.doc_contract.{kind}: required_sections must be a unique array of 1-32 trimmed strings (only spec must be non-empty)"
            )
        if "ac_required" in body and not isinstance(body["ac_required"], bool):
            findings.append(f"manifest.doc_contract.{kind}: ac_required must be a boolean")

    if "banned_phrases" in contract and not unique_string_list(
        contract["banned_phrases"], 64
    ):
        findings.append(
            "manifest.doc_contract: banned_phrases must be a unique array of 1-64 trimmed strings"
        )

    return contract


def read_file(path: Path, findings: list[str]) -> list[str] | None:
    try:
        return path.read_text(encoding="utf-8").splitlines()
    except (OSError, UnicodeDecodeError) as exc:
        findings.append(f"{path.relative_to(ROOT)}: cannot read: {exc}")
        return None


def collect_headings(lines: list[str]) -> dict[str, int]:
    """Map exact heading text to its first 1-based line number."""
    headings: dict[str, int] = {}
    for index, line in enumerate(lines, start=1):
        match = HEADING_RE.match(line)
        if not match:
            continue
        text = match.group(2).strip()
        if text not in headings:
            headings[text] = index
    return headings


def check_required_sections(
    headings: dict[str, int], required: list[str], path: Path, findings: list[str]
) -> None:
    for section in required:
        if section not in headings:
            findings.append(f"missing-section: {path.relative_to(ROOT)} ({section})")


def find_section(
    lines: list[str], title: str, start_after: int = 0
) -> tuple[int, int] | None:
    """Return (start, end) 1-based line range of the named section.

    `start` is the line of the heading itself; `end` is the line *after* the
    next heading at the same or higher level, or len(lines) + 1 when the
    section runs to EOF.
    """
    section_level: int | None = None
    start_line = 0
    for index, line in enumerate(lines, start=1):
        if index <= start_after:
            continue
        match = HEADING_RE.match(line)
        if not match:
            continue
        text = match.group(2).strip()
        level = len(match.group(1))
        if text == title:
            section_level = level
            start_line = index
            break
    if section_level is None:
        return None
    for index, line in enumerate(lines[start_line:], start=start_line + 1):
        match = HEADING_RE.match(line)
        if match and len(match.group(1)) <= section_level:
            return start_line, index - 1
    return start_line, len(lines)


def iter_section_blocks(
    lines: list[str], section_start: int, section_end: int
) -> list[tuple[int, list[str]]]:
    """Split a section into blocks, returning (start_line, text_lines) pairs.

    A block is a list item, numbered item, or paragraph (a run of consecutive
    non-blank, non-heading, non-code lines). Indented continuation lines join
    the parent block. Both the acceptance-criteria grammar and the
    verification join count the same block shape, so the split lives here
    rather than in either caller.
    """
    blocks: list[tuple[int, list[str]]] = []
    in_code = False
    current: list[str] = []
    block_start = 0

    def flush() -> None:
        nonlocal current
        if current:
            blocks.append((block_start, current))
            current = []

    for index in range(section_start + 1, section_end + 1):
        line = lines[index - 1]
        if CODE_FENCE_RE.match(line):
            in_code = not in_code
            continue
        if in_code:
            continue
        if HEADING_RE.match(line):
            flush()
            continue
        list_match = LIST_ITEM_RE.match(line)
        if list_match:
            flush()
            block_start = index
            current = [list_match.group(1).rstrip()]
            continue
        if not line.strip():
            flush()
            continue
        if current:
            parent = lines[block_start - 1]
            parent_indent = len(parent) - len(parent.lstrip())
            if len(line) - len(line.lstrip()) > parent_indent:
                current.append(line.strip())
                continue
        # Paragraph-level block: a non-blank, non-list, non-heading line.
        if not current:
            block_start = index
        current.append(line.strip())

    flush()
    return blocks


def parse_gherkin_criteria(
    lines: list[str],
    section_start: int,
    section_end: int,
    path: Path,
    findings: list[str],
) -> int:
    """Validate the acceptance-criteria section, return the number of criteria.

    A criterion's first keyword must be Given/When/Then. It is well-formed iff
    it contains at least one When and one Then (Given is optional per the
    recorded amendment) and states exactly one When. A criterion with two
    triggers is two criteria: the single-When rule is the granularity test.
    """
    keywords = ("Given", "When", "Then")
    blocks = iter_section_blocks(lines, section_start, section_end)

    for start_line, text_lines in blocks:
        first = text_lines[0].lstrip()
        first_word = first.split(None, 1)[0] if first else ""
        if first_word not in keywords:
            findings.append(
                f"ac-not-gherkin: {path.relative_to(ROOT)}#{start_line} "
                f"(first token must be one of {list(keywords)}, got {first_word!r})"
            )
            continue
        joined = " ".join(text_lines)
        seen = {kw: len(GHERKIN_KEYWORD_RE[kw].findall(joined)) for kw in keywords}
        if seen["When"] < 1 or seen["Then"] < 1:
            findings.append(
                f"ac-not-gherkin: {path.relative_to(ROOT)}#{start_line} "
                f"(must contain at least one When and one Then, got {seen})"
            )
            continue
        if seen["When"] > 1:
            findings.append(
                f"ac-multiple-when: {path.relative_to(ROOT)}#{start_line} "
                f"(a criterion states one trigger; got {seen['When']} When clauses, "
                f"split it into one criterion per trigger)"
            )

    return len(blocks)


def check_verification_coverage(
    lines: list[str], criteria_count: int, path: Path, findings: list[str]
) -> None:
    """Join the acceptance criteria to the Verification section.

    The contract already requires both sections, so the join needs no
    identifier syntax inside the Gherkin sentence: a record that states more
    criteria than it verifies has left criteria unproven. This is a necessary
    condition only. Proving that a named artifact actually exercises a given
    criterion is the typed scenario-resolution work on issue #319.
    """
    if criteria_count == 0:
        return
    section = find_section(lines, "Verification")
    if section is None:
        # check_required_sections already reports the absent section.
        return
    entries = iter_section_blocks(lines, section[0], section[1])
    if not entries:
        findings.append(
            f"verification-empty: {path.relative_to(ROOT)} "
            f"({criteria_count} criteria, no verification entries)"
        )
        return
    if len(entries) < criteria_count:
        findings.append(
            f"verification-underspecified: {path.relative_to(ROOT)} "
            f"({criteria_count} criteria, {len(entries)} verification entries)"
        )


def check_gherkin(
    lines: list[str], path: Path, findings: list[str]
) -> int:
    section = find_section(lines, "Acceptance criteria")
    if section is None:
        findings.append(f"ac-missing: {path.relative_to(ROOT)}")
        return 0
    return parse_gherkin_criteria(lines, section[0], section[1], path, findings)


def check_no_gherkin(lines: list[str], path: Path, findings: list[str]) -> None:
    """ac_required false means an acceptance-criteria section is forbidden.

    Only spec records carry acceptance criteria. A decision, a constitution, a
    lesson, a reference, or a research document that grows an Acceptance
    criteria section is claiming a testable contract its kind cannot hold, so
    the section is a finding rather than an unchecked extra.

    The boundary is find_section, the same heading scan the required half uses.
    Prose that happens to say "when X, then Y" is untouched; only a real section
    counts.
    """
    section = find_section(lines, "Acceptance criteria")
    if section is None:
        return
    findings.append(f"ac-forbidden: {path.relative_to(ROOT)}#{section[0]}")


def strip_html_comments(lines: list[str]) -> list[str]:
    """Replace HTML comments with blank lines to preserve line numbering.

    A `<!-- ... -->` block, even one that spans multiple lines, is a
    machine-authored artifact (typically a generator marker like
    "DO NOT EDIT"). Treating it as prose catches every uppercase word in
    the comment as a candidate abbreviation, which is the failure mode the
    abbrev check is meant to detect in real prose, not in comments. Keeping
    the lines present (as blanks) means a line that previously carried
    content still occupies its number, so existing findings do not silently
    shift.
    """
    text = "\n".join(lines)
    return HTML_COMMENT_RE.sub(lambda m: "\n" * m.group(0).count("\n"), text).splitlines()


def strip_inline_code(lines: list[str]) -> list[str]:
    """Blank inline code spans, preserving line length and numbering.

    A backtick span is a code quotation, not prose. Scanned as prose it
    reports SQL keywords and command names as unexpanded abbreviations and
    counts identifiers as sentence words, which is noise the checks are not
    meant to catch. Fenced blocks are already excluded as segments; this
    closes the same hole for inline spans.

    Spans become spaces rather than disappearing, so line length and
    indentation stay put and reported line numbers keep pointing at the
    original line. Fence lines are left alone: they open and close a block
    that is excluded wholesale, and rewriting them would break detection.
    """
    scrubbed: list[str] = []
    for line in lines:
        if CODE_FENCE_RE.match(line):
            scrubbed.append(line)
            continue
        scrubbed.append(INLINE_CODE_RE.sub(lambda m: " " * len(m.group(0)), line))
    return scrubbed


def split_into_segments(lines: list[str]) -> list[tuple[str, list[int]]]:
    """Group lines into STE-scannable segments: prose, code, table, heading.

    Returns a list of (segment_kind, line_numbers) tuples. Prose segments are
    contiguous non-heading, non-table, non-fence lines outside code blocks.
    Code segments include the fence lines themselves; sentence-length
    checks exclude them. Headings and table rows are excluded individually.
    HTML comments are stripped to blanks so their uppercase tokens do not
    seed false-positive abbreviation findings.
    """
    segments: list[tuple[str, list[int]]] = []
    in_code = False
    current_kind = "prose"
    current_lines: list[int] = []

    def flush(kind: str, line_numbers: list[int]) -> None:
        if line_numbers:
            segments.append((kind, line_numbers))

    scrubbed = strip_inline_code(strip_html_comments(lines))
    line_text: dict[int, str] = {}
    for index, line in enumerate(scrubbed, start=1):
        line_text[index] = line
        if CODE_FENCE_RE.match(line):
            if in_code:
                flush("code", current_lines + [index])
                current_lines = []
                in_code = False
            else:
                flush(current_kind, current_lines)
                current_lines = [index]
                current_kind = "code"
                in_code = True
            continue
        if in_code:
            current_lines.append(index)
            continue
        if HEADING_RE.match(line):
            flush(current_kind, current_lines)
            current_lines = []
            current_kind = "prose"
            continue
        if TABLE_ROW_RE.match(line):
            if current_kind != "table":
                flush(current_kind, current_lines)
                current_kind = "table"
                current_lines = []
            current_lines.append(index)
            continue
        # Prose line
        if current_kind != "prose":
            flush(current_kind, current_lines)
            current_kind = "prose"
            current_lines = []
        current_lines.append(index)

    flush(current_kind, current_lines)
    return segments, line_text


def check_sentence_length(
    segments: list[tuple[str, list[int]]],
    lines: list[str],
    path: Path,
    findings: list[str],
) -> None:
    for kind, line_numbers in segments:
        if kind != "prose":
            continue
        for line_no in line_numbers:
            text = lines[line_no - 1].strip()
            if not text:
                continue
            words = WORD_RE.findall(text)
            if len(words) > MAX_SENTENCE_WORDS:
                findings.append(
                    f"ste-sentence-length: {path.relative_to(ROOT)}:{line_no} "
                    f"({len(words)} words)"
                )


def check_banned_phrases(
    segments: list[tuple[str, list[int]]],
    lines: list[str],
    path: Path,
    findings: list[str],
    banned: tuple[str, ...],
) -> None:
    if not banned:
        return
    lowered = tuple(phrase.lower() for phrase in banned)
    for kind, line_numbers in segments:
        if kind != "prose":
            continue
        for line_no in line_numbers:
            haystack = lines[line_no - 1].lower()
            for phrase in lowered:
                if phrase in haystack:
                    findings.append(
                        f"ste-banned-phrase: {path.relative_to(ROOT)}:{line_no} "
                        f"({phrase!r})"
                    )


def check_abbreviations(
    segments: list[tuple[str, list[int]]],
    lines: list[str],
    path: Path,
    findings: list[str],
    allowlist: frozenset[str],
) -> None:
    """Expand-on-first-use abbreviation discipline.

    For each abbreviation (uppercase token, ≥2 chars, not in allowlist), the
    first occurrence in the document must appear as `Expansion (ABBR)` —
    either inline at the same line or as the immediately preceding line.
    The same abbreviation repeated unexpanded is fine after first use; only
    the first occurrence is checked.
    """
    seen_abbreviations: set[str] = set()
    for kind, line_numbers in segments:
        if kind != "prose":
            continue
        for line_no in line_numbers:
            line = lines[line_no - 1]
            for abbr in ABBR_RE.findall(line):
                if abbr in allowlist or abbr in STRUCTURAL_TOKEN_EXCLUSIONS or abbr in seen_abbreviations:
                    continue
                seen_abbreviations.add(abbr)
                if not has_expansion(line, abbr):
                    findings.append(
                        f"ste-abbreviation: {path.relative_to(ROOT)} (ABBR={abbr})"
                    )


def has_expansion(line: str, abbr: str) -> bool:
    """Return True when `Expansion (ABBR)` appears on the same line."""
    pattern = re.compile(rf"\b[A-Za-z][A-Za-z0-9 ./\-]*\({re.escape(abbr)}\)")
    return pattern.search(line) is not None


def check_record(
    record: dict,
    contract: dict,
    abbreviations: frozenset[str],
    banned: tuple[str, ...],
    findings: list[str],
) -> None:
    kind = record.get("kind")
    if kind not in DOC_CONTRACT_KINDS or kind not in contract:
        return
    path_value = record.get("path")
    if not isinstance(path_value, str) or not path_value.endswith(".md"):
        findings.append(f"doc-contract: {record.get('id')!r}: path must be a .md string")
        return
    absolute = ROOT / path_value
    if not absolute.is_file():
        # Missing files are check-knowledge-index's responsibility; skip.
        return
    lines = read_file(absolute, findings)
    if lines is None:
        return

    spec = contract[kind]
    headings = collect_headings(lines)
    required = spec.get("required_sections", [])
    check_required_sections(headings, required, absolute, findings)

    if spec.get("ac_required", False):
        criteria_count = check_gherkin(lines, absolute, findings)
        check_verification_coverage(lines, criteria_count, absolute, findings)
    else:
        check_no_gherkin(lines, absolute, findings)

    segments, line_text = split_into_segments(lines)
    # Replace lines with the scrubbed content for content checks so HTML
    # comments cannot seed false-positive abbreviation findings. The segment
    # line numbers still point at the original line, so reporting stays put.
    scrubbed_lines = [line_text.get(i, lines[i - 1] if i - 1 < len(lines) else "") for i in range(1, len(lines) + 1)]
    check_sentence_length(segments, scrubbed_lines, absolute, findings)
    check_banned_phrases(segments, scrubbed_lines, absolute, findings, banned)
    check_abbreviations(segments, scrubbed_lines, absolute, findings, abbreviations)


def report(findings: list[str], noun: str) -> int:
    for finding in findings[:MAX_FINDINGS]:
        print(finding)
    if len(findings) > MAX_FINDINGS:
        print(f"... {len(findings) - MAX_FINDINGS} additional finding(s) omitted")
    if findings:
        print(f"{noun} check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print(f"{noun} check passed")
    return 0


def report_only(findings: list[str], noun: str) -> int:
    for finding in findings[:MAX_FINDINGS]:
        print(finding)
    if len(findings) > MAX_FINDINGS:
        print(f"... {len(findings) - MAX_FINDINGS} additional finding(s) omitted")
    print(f"{noun} check: {len(findings)} finding(s) (report-only mode)", file=sys.stderr)
    return 0


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument(
        "--report-only",
        action="store_true",
        help="exit 0 and print findings; the default when doc_contract.enforced is false.",
    )
    args = parser.parse_args(argv)

    findings: list[str] = []
    manifest = load_manifest(findings)
    if not isinstance(manifest, dict):
        return report(findings, "doc contract")

    contract = validate_doc_contract(manifest, findings)
    if contract is None:
        return report(findings, "doc contract")

    enforced = bool(contract.get("enforced", False))
    report_only_mode = args.report_only or not enforced
    banned: tuple[str, ...] = tuple(contract.get("banned_phrases", DEFAULT_BANNED_PHRASES))
    abbreviations = DEFAULT_ABBREVIATION_ALLOWLIST

    records = manifest.get("records") or []
    for record in records:
        if not isinstance(record, dict):
            continue
        check_record(record, contract, abbreviations, banned, findings)

    if report_only_mode:
        return report_only(findings, "doc contract")
    return report(findings, "doc contract")


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
