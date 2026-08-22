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
