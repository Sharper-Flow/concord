#!/usr/bin/env python3
"""Focused tests for the per-record doc-contract checker.

The validator reads the live manifest under ROOT and walks the spec records
to apply the contract. Each test uses a sandbox copy of the manifest, plus
synthetic .md files for the records, to exercise one rule at a time without
disturbing the live corpus.
"""
from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
from io import StringIO
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).with_name("check-doc-contract.py")
SPEC = importlib.util.spec_from_file_location("doc_contract", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


def sandbox() -> Path:
    root = Path(tempfile.mkdtemp(prefix="dc-"))
    docs = root / "docs"
    docs.mkdir()
    (root / "manifest.json").write_text("{}", encoding="utf-8")
    return root


def write_spec(
    root: Path, path: str, body: str, record_id: str = "spec-1"
) -> Path:
    absolute = root / path
    absolute.parent.mkdir(parents=True, exist_ok=True)
    absolute.write_text(body, encoding="utf-8")
    return absolute


def manifest_with(root: Path, records: list[dict], contract: dict | None = None) -> dict:
    if contract is None:
        contract = {
            "enforced": True,
            "spec": {
                "required_sections": ["Context", "Contract", "Acceptance criteria", "Verification"],
                "ac_required": True,
            },
            "banned_phrases": [
                "in order to", "utilize", "leverage",
                "it is important to note", "needless to say", "at the end of the day",
            ],
        }
    return {
        "schema_version": "1.2",
        "supported_kinds": ["spec"],
        "indexed_kinds": ["spec"],
        "domain_registry": {
            "schema_version": "1.0",
            "product_key": "concord",
            "root_domain_id": "product-root:concord",
            "domains": [{
                "domain_id": "product-root:concord",
                "name": "Concord",
                "purpose": "Concord law and architecture",
                "status": "current",
                "architecture_relations": [],
            }],
        },
        "doc_contract": contract,
        "records": records,
    }


def record(path: str, sha_digest: str = "a") -> dict:
    return {
        "id": f"spec-{path}",
        "kind": "spec",
        "path": path,
        "status": "accepted",
        "date": "2026-08-21T00:00:00Z",
        "title": "Spec",
        "summary": "Spec",
        "tags": [],
        "scopes": {
            "mode": "home", "product_ids": [], "project_ids": [],
            "domain_ids": [], "tag_ids": [],
        },
        "home_domain_id": "product-root:concord",
        "criterion_bindings": [{"criterion": 1, "scenario": "WF01-capture-late-outcome"}],
        "sha256": "sha256:" + sha_digest * 64,
    }


def run_checker(root: Path, manifest: dict, argv: list[str] | None = None) -> tuple[int, str, str]:
    manifest_path = root / "manifest.json"
    manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
    captured_out = StringIO()
    captured_err = StringIO()
    with mock.patch.object(checker, "ROOT", root), mock.patch.object(checker, "MANIFEST", manifest_path):
        old_out, old_err = sys.stdout, sys.stderr
        sys.stdout, sys.stderr = captured_out, captured_err
        try:
            exit_code = checker.main(list(argv or []))
        finally:
            sys.stdout, sys.stderr = old_out, old_err
    return exit_code, captured_out.getvalue(), captured_err.getvalue()


# ---------------------------------------------------------------------------
# Happy path: every section present, every AC Gherkin, no STE violations.
# ---------------------------------------------------------------------------


VALID_BODY = """# Valid spec

## Context

This is the context paragraph.

## Contract

This is the contract paragraph.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

This is the verification paragraph.
"""


def test_valid_spec_passes_when_enforced() -> None:
    root = sandbox()
    path = "docs/valid.md"
    write_spec(root, path, VALID_BODY)
    manifest = manifest_with(root, [record(path)])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)
    assert "doc contract check passed" in stdout, stdout


def test_valid_spec_passes_when_report_only() -> None:
    root = sandbox()
    path = "docs/valid.md"
    write_spec(root, path, VALID_BODY)
    manifest = manifest_with(root, [record(path)])
    manifest["doc_contract"]["enforced"] = False
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)
    assert "report-only" in stderr, stderr


# ---------------------------------------------------------------------------
# AC grammar: missing keywords, missing section, indentation.
# ---------------------------------------------------------------------------


def test_ac_missing_when_required_section_absent() -> None:
    root = sandbox()
    body = """# Spec

## Context

Body.

## Contract

Body.

## Verification

Body.
"""
    path = "docs/no-ac.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path)])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any("ac-missing: docs/no-ac.md" in line for line in stdout.splitlines()), stdout


def test_ac_not_gherkin_when_first_token_invalid() -> None:
    root = sandbox()
    body = """# Spec

## Context

Body.

## Contract

Body.

## Acceptance criteria

- A non-Gherkin criterion with no keywords.

## Verification

Body.
"""
    path = "docs/bad-ac.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path)])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any("ac-not-gherkin: docs/bad-ac.md" in line for line in stdout.splitlines()), stdout


def test_ac_not_gherkin_when_then_missing() -> None:
    root = sandbox()
    body = """# Spec

## Context

Body.

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens.

## Verification

Body.
"""
    path = "docs/then-missing.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path)])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any("ac-not-gherkin" in line for line in stdout.splitlines()), stdout


def test_ac_gherkin_accepts_when_precondition_absent() -> None:
    root = sandbox()
    body = """# Spec

## Context

Body.

## Contract

Body.

## Acceptance criteria

- When an action happens
  Then an outcome follows.

## Verification

Body.
"""
    path = "docs/no-given.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path)])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)


def test_ac_gherkin_with_multiline_continuation() -> None:
    root = sandbox()
    body = """# Spec

## Context

Body.

## Contract

Body.

## Acceptance criteria

- Given a precondition
  with a multi-line
  description
  When an action happens
  Then an outcome follows.

## Verification

Body.
"""
    path = "docs/multiline.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path)])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)


# ---------------------------------------------------------------------------
# Sentence length
# ---------------------------------------------------------------------------


def test_sentence_length_over_limit_fails() -> None:
    root = sandbox()
    long_sentence = " ".join(["word"] * 45) + "."
    body = f"""# Spec

## Context

{long_sentence}

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

Body.
"""
    path = "docs/long.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path)])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any("ste-sentence-length" in line and "(45 words)" in line for line in stdout.splitlines()), stdout


def test_sentence_length_at_limit_passes() -> None:
    root = sandbox()
    at_limit = " ".join(["word"] * 40) + "."
    body = f"""# Spec

## Context

{at_limit}

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

Body.
"""
    path = "docs/at-limit.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path)])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)


# ---------------------------------------------------------------------------
# Banned phrases — one assertion per seed phrase.
# ---------------------------------------------------------------------------


def _banned_body(phrase: str) -> str:
    return f"""# Spec

## Context

We {phrase} say something.

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

Body.
"""


def test_banned_phrases_each_fail() -> None:
    for phrase in checker.DEFAULT_BANNED_PHRASES:
        root = sandbox()
        path = "docs/banned.md"
        write_spec(root, path, _banned_body(phrase))
        manifest = manifest_with(root, [record(path, sha_digest="b")])
        exit_code, stdout, stderr = run_checker(root, manifest)
        assert exit_code == 1, (phrase, exit_code, stdout, stderr)
        assert any("ste-banned-phrase" in line for line in stdout.splitlines()), (phrase, stdout)


# ---------------------------------------------------------------------------
# Abbreviation discipline
# ---------------------------------------------------------------------------


def test_unexpanded_abbreviation_fails() -> None:
    root = sandbox()
    body = """# Spec

## Context

Use this RPC for calls.

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

Body.
"""
    path = "docs/unexp.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="c")])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any("ste-abbreviation" in line and "ABBR=RPC" in line for line in stdout.splitlines()), stdout


def test_expanded_abbreviation_passes() -> None:
    root = sandbox()
    body = """# Spec

## Context

Use this Remote Procedure Call (RPC) for calls.

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

Body.
"""
    path = "docs/exp.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="d")])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)


def test_allowlisted_abbreviation_passes() -> None:
    root = sandbox()
    body = """# Spec

## Context

Use this JSON for storage.

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

Body.
"""
    path = "docs/json.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="e")])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)


# ---------------------------------------------------------------------------
# Section / code / table exclusions
# ---------------------------------------------------------------------------


def test_heading_text_is_excluded_from_sentence_length() -> None:
    root = sandbox()
    long_phrase = " ".join(["h"] * 50)
    body = f"""# Spec

## Context

Short.

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

Body.

# {long_phrase}
"""
    path = "docs/heading.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="f")])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)


def test_code_block_is_excluded_from_sentence_length() -> None:
    root = sandbox()
    long_line = " ".join(["code"] * 60)
    body = f"""# Spec

## Context

Short.

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

```
{long_line}
```

Body.
"""
    path = "docs/code.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="1")])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)


def test_table_is_excluded_from_sentence_length() -> None:
    root = sandbox()
    cells = " | ".join(["word"] * 50)
    body = f"""# Spec

## Context

Short.

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

| {cells} |
| --- |

Body.
"""
    path = "docs/table.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="2")])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)


# ---------------------------------------------------------------------------
# Enforced flag and --report-only override
# ---------------------------------------------------------------------------


def test_enforced_false_emits_findings_and_exits_zero() -> None:
    root = sandbox()
    body = """# Spec

## Context

Short.

## Contract

Body.
"""
    path = "docs/incomplete.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="3")])
    manifest["doc_contract"]["enforced"] = False
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)
    assert any("missing-section" in line for line in stdout.splitlines()), stdout
    assert "report-only mode" in stderr, stderr


def test_report_only_flag_overrides_enforced_true() -> None:
    root = sandbox()
    body = """# Spec

## Context

Short.

## Contract

Body.
"""
    path = "docs/incomplete.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="4")])
    assert manifest["doc_contract"]["enforced"] is True
    exit_code, stdout, stderr = run_checker(root, manifest, ["--report-only"])
    assert exit_code == 0, (exit_code, stdout, stderr)
    assert "report-only" in stderr, stderr


# ---------------------------------------------------------------------------
# Manifest contract validation
# ---------------------------------------------------------------------------


def test_manifest_unknown_doc_contract_field_is_rejected() -> None:
    root = sandbox()
    body = VALID_BODY
    path = "docs/valid.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="5")])
    manifest["doc_contract"]["unknown"] = True
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any("unknown fields" in line for line in stdout.splitlines()), stdout


# ---------------------------------------------------------------------------
# ac_required false forbids an acceptance-criteria section.
# ---------------------------------------------------------------------------


def non_spec_record(kind: str, path: str, sha_digest: str = "a") -> dict:
    entry = record(path, sha_digest)
    entry["id"], entry["kind"], entry["status"] = f"{kind}-1", kind, "published"
    entry.pop("home_domain_id", None)
    entry.pop("criterion_bindings", None)
    return entry


def kind_contract(kind: str, required_sections: list[str]) -> dict:
    return {
        "enforced": True,
        kind: {"required_sections": required_sections, "ac_required": False},
        "banned_phrases": list(checker.DEFAULT_BANNED_PHRASES),
    }


def test_non_spec_kind_without_acceptance_criteria_passes() -> None:
    root = sandbox()
    path = "docs/reference.md"
    write_spec(root, path, "# Reference\n\nBody.\n")
    manifest = manifest_with(root, [non_spec_record("reference", path)], kind_contract("reference", []))
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)


def test_non_spec_kind_with_acceptance_criteria_is_a_finding() -> None:
    root = sandbox()
    body = """# Reference

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.
"""
    path = "docs/reference.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [non_spec_record("reference", path, "b")], kind_contract("reference", []))
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any("ac-forbidden: docs/reference.md#5" in line for line in stdout.splitlines()), stdout


def test_prose_that_says_when_and_then_is_not_an_acceptance_section() -> None:
    """The boundary is the heading scan, not the presence of Gherkin words."""
    root = sandbox()
    body = """# Research

## Findings

When the cache is cold, then the first read pays the index build.
"""
    path = "docs/research-note.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [non_spec_record("research", path, "c")], kind_contract("research", ["Findings"]))
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)


def test_new_kind_required_sections_are_enforced() -> None:
    root = sandbox()
    path = "docs/constitution.md"
    write_spec(root, path, "# Constitution\n\nBody.\n")
    contract = kind_contract("constitution", ["Purpose"])
    manifest = manifest_with(root, [non_spec_record("constitution", path, "d")], contract)
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any("missing-section: docs/constitution.md (Purpose)" in line for line in stdout.splitlines()), stdout

    root = sandbox()
    write_spec(root, path, "# Constitution\n\n## Purpose\n\nBody.\n")
    manifest = manifest_with(root, [non_spec_record("constitution", path, "e")], contract)
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)


def test_empty_section_name_is_rejected_for_a_kind_whose_list_may_be_empty() -> None:
    """An empty array of sections is allowed; an empty section name is not."""
    root = sandbox()
    path = "docs/reference.md"
    write_spec(root, path, "# Reference\n\nBody.\n")
    manifest = manifest_with(root, [non_spec_record("reference", path, "f")], kind_contract("reference", [""]))
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any("required_sections must be a unique array" in line for line in stdout.splitlines()), stdout


def test_live_manifest_declares_a_contract_for_every_new_kind() -> None:
    """The taxonomy's non-law kinds must forbid acceptance criteria, not ignore them."""
    live = json.loads(
        (Path(checker.ROOT) / "docs/concord-knowledge-index.v1.json").read_text(encoding="utf-8")
    )
    contract = live["doc_contract"]
    for kind in ("constitution", "reference", "research"):
        assert kind in contract, kind
        assert contract[kind]["ac_required"] is False, kind
    assert contract["spec"]["ac_required"] is True


def test_decision_records_are_skipped_when_not_in_contract() -> None:
    root = sandbox()
    path = "docs/decision.md"
    write_spec(root, path, "# Decision\n\nBody without required sections.\n")
    manifest = manifest_with(root, [{
        "id": "decision-1",
        "kind": "decision",
        "path": path,
        "status": "accepted",
        "date": "2026-08-21T00:00:00Z",
        "title": "Decision",
        "summary": "Decision",
        "tags": [],
        "scopes": {
            "mode": "home", "product_ids": [], "project_ids": [],
            "domain_ids": [], "tag_ids": [],
        },
        "home_domain_id": "product-root:concord",
        "sha256": "sha256:" + "6" * 64,
    }])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)


# ---------------------------------------------------------------------------
# Repeat abbreviations: only the first occurrence is checked.
# ---------------------------------------------------------------------------


def test_repeated_unexpanded_abbreviation_after_first_expansion_passes() -> None:
    root = sandbox()
    body = """# Spec

## Context

Use this Remote Procedure Call (RPC) for the first call.
Subsequent references may use RPC again.

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

Body.
"""
    path = "docs/repeat.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="7")])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)


def test_html_comments_do_not_seed_abbreviation_findings() -> None:
    root = sandbox()
    body = """# Spec

<!-- Generated by x; DO NOT EDIT. -->

## Context

Body.

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

Body.
"""
    path = "docs/comment.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="c1")])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)


# ---------------------------------------------------------------------------
# Inline code span exclusion
#
# Every exclusion is tested on both sides: the token is ignored inside a
# backtick span, and the same token is still reported when it appears as
# prose on the same document.
# ---------------------------------------------------------------------------


def _inline_code_body(context: str) -> str:
    return f"""# Spec

## Context

{context}

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

Body.
"""


def test_abbreviation_inside_inline_code_is_not_flagged() -> None:
    root = sandbox()
    body = _inline_code_body("Run `EXPLAIN QUERY PLAN` before merging.")
    path = "docs/inline-abbr.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="i1")])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)


def test_abbreviation_outside_inline_code_is_still_flagged() -> None:
    """The true-positive side of the exclusion.

    Without this, blanking spans could silence the abbreviation check
    entirely and the test above would still pass.
    """
    root = sandbox()
    body = _inline_code_body("Run `EXPLAIN QUERY PLAN` before merging the RPC.")
    path = "docs/inline-abbr-mixed.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="i2")])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any("ABBR=RPC" in line for line in stdout.splitlines()), stdout
    assert not any("ABBR=QUERY" in line for line in stdout.splitlines()), stdout


def test_double_backtick_span_is_excluded() -> None:
    root = sandbox()
    body = _inline_code_body("Write ``SELECT COUNT(*)`` in the query.")
    path = "docs/double-backtick.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="i3")])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)


def test_banned_phrase_inside_inline_code_is_not_flagged() -> None:
    root = sandbox()
    body = _inline_code_body("Call `utilize_backend()` to start.")
    path = "docs/inline-banned.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="i4")])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)


def test_banned_phrase_outside_inline_code_is_still_flagged() -> None:
    root = sandbox()
    body = _inline_code_body("Call `utilize_backend()` and utilize the result.")
    path = "docs/inline-banned-mixed.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="i5")])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any("ste-banned-phrase" in line for line in stdout.splitlines()), stdout


def test_inline_code_words_do_not_count_toward_sentence_length() -> None:
    root = sandbox()
    span = "`" + " ".join(f"token{i}" for i in range(30)) + "`"
    sentence = "This sentence has " + span + " and stays short."
    body = _inline_code_body(sentence)
    path = "docs/inline-sentence.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="i6")])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)


# ---------------------------------------------------------------------------
# Criterion granularity and verification coverage
# ---------------------------------------------------------------------------


def _granularity_body(criteria: str, verification: str) -> str:
    return f"""# Spec

## Context

Body.

## Contract

Body.

## Acceptance criteria

{criteria}

## Verification

{verification}
"""


def test_ac_multiple_when_fails() -> None:
    root = sandbox()
    body = _granularity_body(
        "- Given a precondition\n"
        "  When an action happens\n"
        "  When a second action happens\n"
        "  Then an outcome follows.",
        "Body.",
    )
    path = "docs/two-triggers.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="g1")])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any("ac-multiple-when" in line for line in stdout.splitlines()), stdout


def test_ac_split_triggers_pass() -> None:
    root = sandbox()
    body = _granularity_body(
        "- When an action happens\n"
        "  Then an outcome follows.\n"
        "- When a second action happens\n"
        "  Then a second outcome follows.",
        "- One check.\n- A second check.",
    )
    path = "docs/split-triggers.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="g2")])
    manifest["records"][0]["criterion_bindings"].append(
        {"criterion": 2, "exemption": "A recorded reason for this exemption."}
    )
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)


def test_when_prefixed_word_is_not_a_second_trigger() -> None:
    """A raw substring count reads "Whenever" as a When clause.

    Without word-bounded matching this criterion tallies two triggers and is
    wrongly reported as too coarse, so this is the non-vacuity check for the
    keyword regex rather than a restatement of the single-When rule.
    """
    root = sandbox()
    body = _granularity_body(
        "- When a request arrives\n"
        "  Then the system responds. Whenever load is high it queues first.",
        "Body.",
    )
    path = "docs/whenever.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="g3")])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)


def test_verification_underspecified_when_entries_fewer_than_criteria() -> None:
    root = sandbox()
    body = _granularity_body(
        "- When an action happens\n"
        "  Then an outcome follows.\n"
        "- When a second action happens\n"
        "  Then a second outcome follows.",
        "One check only.",
    )
    path = "docs/underspecified.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="g4")])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any(
        "verification-underspecified" in line for line in stdout.splitlines()
    ), stdout


def test_verification_empty_section_fails() -> None:
    root = sandbox()
    body = _granularity_body(
        "- When an action happens\n  Then an outcome follows.",
        "",
    )
    path = "docs/empty-verification.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="g5")])
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any("verification-empty" in line for line in stdout.splitlines()), stdout


def test_structural_sql_keyword_tokens_are_not_flagged() -> None:
    """SQL keywords and SQLite pragma values are language tokens, not prose
    abbreviations (audit F4): expanding them on first use would put fiction
    into technical writing. Both sides: every excluded class is silent, and
    an ordinary unexpanded abbreviation on the same page still fires."""
    root = sandbox()
    body = """# Spec

## Context

Run EXPLAIN QUERY PLAN before PRAGMA synchronous=NORMAL, with journal_mode=TRUNCATE and a CHECK constraint on TEXT columns.

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

Body.
"""
    path = "docs/sql-tokens.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="e")])
    exit_code, stdout, _ = run_checker(root, manifest)
    assert exit_code == 0, stdout

    noisy = body.replace("Body.\n\n## Acceptance", "The FK constraint applies.\n\n## Acceptance")
    write_spec(root, path, noisy)
    exit_code, stdout, _ = run_checker(root, manifest)
    assert exit_code == 1, stdout
    assert "ABBR=FK" in stdout


def test_structural_rfc2119_keywords_are_not_flagged() -> None:
    root = sandbox()
    body = """# Spec

## Context

The implementation MUST refresh the cursor and MAY batch reads. Agents SHOULD retry.

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

Body.
"""
    path = "docs/rfc2119.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="e")])
    exit_code, stdout, _ = run_checker(root, manifest)
    assert exit_code == 0, stdout

    noisy = body.replace("Agents SHOULD retry.", "Agents SHOULD retry after SPOF recovery.")
    write_spec(root, path, noisy)
    exit_code, stdout, _ = run_checker(root, manifest)
    assert exit_code == 1, stdout
    assert "ABBR=SPOF" in stdout


def test_defined_name_tokens_are_not_flagged() -> None:
    """Semver bump classes, the MIT license name, the CD-NNNN placeholder,
    and protocol acronyms that are names rather than expandable phrases."""
    root = sandbox()
    body = """# Spec

## Context

A breaking title lands a MAJOR bump; the MIT license covers CD-NNNN records; SCIP/LSP own navigation.

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

Body.
"""
    path = "docs/names.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="e")])
    exit_code, stdout, _ = run_checker(root, manifest)
    assert exit_code == 0, stdout

    noisy = body.replace("SCIP/LSP own navigation.", "SCIP/LSP own navigation for POC work.")
    write_spec(root, path, noisy)
    exit_code, stdout, _ = run_checker(root, manifest)
    assert exit_code == 1, stdout
    assert "ABBR=POC" in stdout


def test_technical_noun_allowlist_extension_is_not_flagged() -> None:
    root = sandbox()
    body = """# Spec

## Context

The SHA pin, the NUL byte separator, WIP logs, the UI surface, IPC scope, SDK clients, and KB bounds all stay silent.

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

Body.
"""
    path = "docs/nouns.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="e")])
    exit_code, stdout, _ = run_checker(root, manifest)
    assert exit_code == 0, stdout


# ---------------------------------------------------------------------------
# Typed criterion resolution
# ---------------------------------------------------------------------------


SCENARIO_ID = "WF01-capture-late-outcome"


def criterion_manifest(root: Path, path: str, binding: dict | None) -> dict:
    write_spec(root, path, VALID_BODY)
    manifest = manifest_with(root, [record(path, sha_digest="criterion")])
    manifest["records"][0]["criterion_bindings"] = [] if binding is None else [binding]
    return manifest


def test_criterion_binding_to_existing_scenario_passes() -> None:
    root = sandbox()
    path = "docs/bound.md"
    manifest = criterion_manifest(root, path, {"criterion": 1, "scenario": SCENARIO_ID})
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 0, (exit_code, stdout, stderr)


def test_criterion_binding_to_unknown_scenario_fails() -> None:
    root = sandbox()
    path = "docs/unknown-scenario.md"
    manifest = criterion_manifest(root, path, {"criterion": 1, "scenario": "SCENARIO-does-not-exist"})
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any("criterion binding names no scenario" in line for line in stdout.splitlines()), stdout


def test_unbound_criterion_fails() -> None:
    root = sandbox()
    path = "docs/unbound.md"
    manifest = criterion_manifest(root, path, None)
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any("criterion is unresolved" in line for line in stdout.splitlines()), stdout


def test_criterion_binding_without_resolution_kind_fails() -> None:
    root = sandbox()
    path = "docs/missing-resolution.md"
    manifest = criterion_manifest(root, path, {"criterion": 1})
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any("criterion binding invalid" in line for line in stdout.splitlines()), stdout


def test_criterion_exemption_without_reason_fails() -> None:
    root = sandbox()
    path = "docs/empty-exemption.md"
    manifest = criterion_manifest(root, path, {"criterion": 1, "exemption": ""})
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any("criterion exemption reason invalid" in line for line in stdout.splitlines()), stdout


def test_criterion_exemption_with_too_short_reason_fails() -> None:
    root = sandbox()
    path = "docs/short-exemption.md"
    manifest = criterion_manifest(root, path, {"criterion": 1, "exemption": "too short"})
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any("criterion exemption reason invalid" in line for line in stdout.splitlines()), stdout


def test_duplicate_criterion_binding_fails() -> None:
    root = sandbox()
    path = "docs/duplicate-binding.md"
    manifest = criterion_manifest(root, path, {"criterion": 1, "scenario": SCENARIO_ID})
    manifest["records"][0]["criterion_bindings"].append(
        {"criterion": 1, "exemption": "A recorded reason for this exemption."}
    )
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any("criterion binding duplicate index" in line for line in stdout.splitlines()), stdout


def test_out_of_range_criterion_binding_fails() -> None:
    root = sandbox()
    path = "docs/out-of-range.md"
    manifest = criterion_manifest(root, path, {"criterion": 2, "scenario": SCENARIO_ID})
    exit_code, stdout, stderr = run_checker(root, manifest)
    assert exit_code == 1, (exit_code, stdout, stderr)
    assert any("criterion binding index is out of range" in line for line in stdout.splitlines()), stdout


def test_activation_criterion_must_be_all_zero_with_bounded_evidence() -> None:
    root = sandbox()
    path = "docs/spec-a.md"
    write_spec(root, path, "# Spec\n\nbody\n")
    spec_record = record(path, sha_digest="e")
    spec_record.pop("criterion_bindings", None)
    base = manifest_with(root, [spec_record])
    base["doc_contract"] = {
        "enforced": False,
        "spec": {"required_sections": ["Context"], "ac_required": False},
        "activation": {
            "max_registered_findings": 0,
            "max_unresolved_criteria": 0,
            "max_false_positives": 0,
            "evidence": "x" * 20,
        },
    }
    exit_code, stdout, _ = run_checker(root, base)
    assert "activation" not in stdout, stdout

    for field in ("max_registered_findings", "max_unresolved_criteria", "max_false_positives"):
        broken = json.loads(json.dumps(base))
        broken["doc_contract"]["activation"][field] = 1
        _, stdout, _ = run_checker(root, broken)
        assert f"{field} must be 0" in stdout, stdout

    for bad in ("", "short", "x" * 1025):
        broken = json.loads(json.dumps(base))
        broken["doc_contract"]["activation"]["evidence"] = bad
        _, stdout, _ = run_checker(root, broken)
        assert "activation" in stdout, stdout

    broken = json.loads(json.dumps(base))
    broken["doc_contract"]["activation"]["extra"] = 1
    _, stdout, _ = run_checker(root, broken)
    assert "four criterion fields" in stdout, stdout


def test_activation_sequence_requires_a_prior_committed_criterion() -> None:
    """The two-step rule: introducing enforced=true in a git repo whose parent
    manifest lacks the identical activation object is a finding (issue #319)."""
    import subprocess as sp

    root = sandbox()
    manifest = root / "docs" / "concord-knowledge-index.v1.json"
    manifest.parent.mkdir(parents=True, exist_ok=True)

    def git(*args):
        return sp.run(["git", *args], cwd=root, capture_output=True, text=True)

    git("init", "-q", "--initial-branch=main")
    git("config", "user.email", "t@t")
    git("config", "user.name", "t")
    (root / "seed.txt").write_text("seed")
    git("add", "-A")
    git("commit", "-qm", "seed")
    activation = {
        "max_registered_findings": 0,
        "max_unresolved_criteria": 0,
        "max_false_positives": 0,
        "evidence": "recorded before the flip" + "x" * 0,
    }
    base = {
        "schema_version": "1.2",
        "supported_kinds": ["spec"],
        "indexed_kinds": ["spec"],
        "knowledge_roots": ["docs"],
        "records": [],
        "doc_contract": {"enforced": False, "spec": {"required_sections": [], "ac_required": False}},
    }
    flipped = json.loads(json.dumps(base))
    flipped["doc_contract"]["enforced"] = True
    flipped["doc_contract"]["activation"] = json.loads(json.dumps(activation))

    # One-change flip: parent has no activation at all.
    manifest.write_text(json.dumps(flipped))
    git("add", "-A")
    git("commit", "-qm", "one-change flip")
    findings: list[str] = []
    with mock.patch.object(checker, "ROOT", root), mock.patch.object(checker, "MANIFEST", manifest):
        checker.check_activation_sequence(True, activation, findings)
    assert any("no parent manifest carrying the activation object" in f for f in findings), findings

    # Two-step: parent carries the identical object, flip is clean.
    manifest.write_text(json.dumps(base))
    manifest.write_text(json.dumps({**base, "doc_contract": {**base["doc_contract"], "activation": activation}}))
    git("add", "-A")
    git("commit", "-qm", "record activation")
    manifest.write_text(json.dumps(flipped))
    git("add", "-A")
    git("commit", "-qm", "flip")
    findings = []
    with mock.patch.object(checker, "ROOT", root), mock.patch.object(checker, "MANIFEST", manifest):
        checker.check_activation_sequence(True, activation, findings)
    assert not findings, findings

    # Criterion changed together with the flip.
    drifted = json.loads(json.dumps(activation))
    drifted["evidence"] = "changed late"
    manifest.write_text(json.dumps({**flipped, "doc_contract": {**flipped["doc_contract"], "activation": drifted}}))
    git("add", "-A")
    git("commit", "-qm", "drift with flip")
    findings = []
    with mock.patch.object(checker, "ROOT", root), mock.patch.object(checker, "MANIFEST", manifest):
        checker.check_activation_sequence(True, drifted, findings)
    assert any("changed together with" in f for f in findings), findings




def test_url_path_segments_are_not_scanned_for_abbreviations() -> None:
    """A URL is an address, not prose. Its path segments carry the remote
    site's capitalization, so scanning one reports the host's spelling as the
    author's defect. Both sides: the URL is silent, and the same token written
    as prose on the same page still fires."""
    root = sandbox()
    body = """# Spec

## Context

See <https://cwiki.apache.org/confluence/display/KAFKA/KIP-161+handlers> for prior art.

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

Body.
"""
    path = "docs/url-tokens.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="u")])
    exit_code, stdout, _ = run_checker(root, manifest)
    assert exit_code == 0, stdout

    noisy = body.replace("for prior art.", "for prior art. The KIP process applies.")
    write_spec(root, path, noisy)
    exit_code, stdout, _ = run_checker(root, manifest)
    assert exit_code == 1, stdout
    assert "ABBR=KIP" in stdout, stdout


def test_markdown_link_text_is_still_scanned_but_target_is_not() -> None:
    """Link text is prose the author wrote and stays in scope; the target is an
    address and does not."""
    root = sandbox()
    body = """# Spec

## Context

Read [the handler guide](https://example.test/display/KAFKA/index.html) first.

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

Body.
"""
    path = "docs/link-target.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="l")])
    exit_code, stdout, _ = run_checker(root, manifest)
    assert exit_code == 0, stdout

    noisy = body.replace("[the handler guide]", "[the KAFKA guide]")
    write_spec(root, path, noisy)
    exit_code, stdout, _ = run_checker(root, manifest)
    assert exit_code == 1, stdout
    assert "ABBR=KAFKA" in stdout, stdout


def test_sentence_length_measures_a_sentence_not_a_line() -> None:
    """The limit is per sentence. Two short sentences sharing one source line
    are two sentences, and a paragraph that happens to be unwrapped is not one
    long sentence. Both sides: the pair is silent, and a single sentence over
    the limit on the same page still fires."""
    root = sandbox()
    pair = " ".join(["word"] * 30) + ". " + " ".join(["word"] * 30) + "."
    body = f"""# Spec

## Context

{pair}

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

Body.
"""
    path = "docs/two-sentences.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="s")])
    exit_code, stdout, _ = run_checker(root, manifest)
    assert exit_code == 0, stdout

    single = body.replace(pair, " ".join(["word"] * 60) + ".")
    write_spec(root, path, single)
    exit_code, stdout, _ = run_checker(root, manifest)
    assert exit_code == 1, stdout
    assert "ste-sentence-length" in stdout and "(60 words)" in stdout, stdout


def test_identifiers_and_references_count_as_one_word_each() -> None:
    """ASD-STE100 rule 8.6 counts an identifier and a reference as one word.
    Counting their internal characters measures the length of a filename, not
    the length of the prose. Both sides: a sentence carrying many references is
    silent, and the same sentence padded with real words still fires."""
    root = sandbox()
    refs = " ".join(
        f"[`../product-memory-{n}-contract.md`](../product-memory-{n}-contract.md)"
        for n in range(12)
    )
    body = f"""# Spec

## Context

This record is superseded in part by {refs} and nothing else.

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

Body.
"""
    path = "docs/references.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="r")])
    exit_code, stdout, _ = run_checker(root, manifest)
    assert exit_code == 0, stdout

    padded = body.replace("and nothing else.", "and " + " ".join(["word"] * 45) + ".")
    write_spec(root, path, padded)
    exit_code, stdout, _ = run_checker(root, manifest)
    assert exit_code == 1, stdout
    assert "ste-sentence-length" in stdout, stdout


def test_emphasis_words_and_proper_names_are_not_flagged() -> None:
    """An English word capitalized as a step label has no expansion, and a name
    is not an expandable phrase. Both sides: every excluded token is silent, and
    an ordinary unexpanded abbreviation on the same page still fires."""
    root = sandbox()
    body = """# Spec

## Context

The flow is DETECT, then PAUSE, then PRESENT CHOICE, then RECORD. It cites NIST SHA-256 and OCI descriptors, and the RB #5 item.

## Contract

Body.

## Acceptance criteria

- Given a precondition
  When an action happens
  Then an outcome follows.

## Verification

Body.
"""
    path = "docs/emphasis-tokens.md"
    write_spec(root, path, body)
    manifest = manifest_with(root, [record(path, sha_digest="e2")])
    exit_code, stdout, _ = run_checker(root, manifest)
    assert exit_code == 0, stdout

    noisy = body.replace("and the RB #5 item.", "and the RB #5 item. A SPOF remains.")
    write_spec(root, path, noisy)
    exit_code, stdout, _ = run_checker(root, manifest)
    assert exit_code == 1, stdout
    assert "ABBR=SPOF" in stdout, stdout


def main() -> int:
    tests = [
        value
        for name, value in sorted(globals().items())
        if name.startswith("test_") and callable(value)
    ]
    for test in tests:
        test()
    print(f"doc contract checker tests passed: {len(tests)} test(s)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
