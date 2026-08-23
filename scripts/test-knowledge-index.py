#!/usr/bin/env python3
"""Focused tests for the durable-knowledge checker and atomic updater."""

from __future__ import annotations

import copy
import importlib.util
import json
import tempfile
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).with_name("check-knowledge-index.py")
SPEC = importlib.util.spec_from_file_location("knowledge_checker", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


def fixture(path: str = "docs/lesson.md", digest: str = "a" * 64) -> dict:
    return {
        "schema_version": "1.0",
        "supported_kinds": ["lesson", "research"],
        "indexed_kinds": ["lesson"],
        "records": [{
            "id": "lesson-1", "kind": "lesson", "path": path, "status": "published",
            "date": "2026-08-10T00:00:00Z", "title": "Lesson", "summary": "Summary", "tags": [],
            "scopes": {"mode": "home", "product_ids": [], "project_ids": [], "component_ids": [], "tag_ids": []},
            "sha256": "sha256:" + digest,
        }],
    }


def v12_fixture() -> dict:
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
                "purpose": "Product-wide Concord law and architecture",
                "status": "current",
                "architecture_relations": [],
            }],
        },
        "records": [{
            "id": "spec-1", "kind": "spec", "path": "docs/spec.md", "status": "accepted",
            "date": "2026-08-10T00:00:00Z", "title": "Decision", "summary": "Summary", "tags": [],
            "scopes": {"mode": "home", "product_ids": [], "project_ids": [], "domain_ids": [], "tag_ids": []},
            "home_domain_id": "product-root:concord", "sha256": "sha256:" + "a" * 64,
        }],
    }


def test_duplicate_keys_at_every_level() -> None:
    cases = [
        '{"schema_version":"1.0","schema_version":"1.0"}',
        '{"records":[{"id":"a","id":"b"}]}',
        '{"records":[{"scopes":{"mode":"home","mode":"explicit"}}]}',
    ]
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        path = Path(directory) / "duplicate.json"
        for raw in cases:
            path.write_text(raw, encoding="utf-8")
            findings: list[str] = []
            assert checker.load(path, findings) is None
            assert findings and "invalid JSON" in findings[0]


def test_invalid_update_is_byte_identical() -> None:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        (root / "docs").mkdir()
        document = root / "docs/lesson.md"
        document.write_text("lesson\n", encoding="utf-8")
        target = root / "manifest.json"
        value = fixture(digest="b" * 64)
        value["unknown"] = True
        original = json.dumps(value, indent=2) + "\n"
        target.write_text(original, encoding="utf-8")
        with mock.patch.object(checker, "ROOT", root), mock.patch.object(checker, "MANIFEST", target):
            findings = checker.update_manifest(value)
        assert findings
        assert target.read_text(encoding="utf-8") == original


def test_atomic_replacement_failure_preserves_original_and_cleans_temp() -> None:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        target = Path(directory) / "manifest.json"
        target.write_text("original\n", encoding="utf-8")
        with mock.patch.object(checker.os, "replace", side_effect=OSError("simulated replacement failure")):
            try:
                checker.atomic_write(target, "replacement\n")
            except OSError:
                pass
            else:
                raise AssertionError("atomic replacement unexpectedly succeeded")
        assert target.read_text(encoding="utf-8") == "original\n"
        assert not list(target.parent.glob(f".{target.name}.*.tmp"))
        with mock.patch.object(checker.tempfile, "NamedTemporaryFile", side_effect=OSError("simulated write failure")):
            try:
                checker.atomic_write(target, "replacement\n")
            except OSError:
                pass
            else:
                raise AssertionError("atomic write unexpectedly succeeded")
        assert target.read_text(encoding="utf-8") == "original\n"


def test_successful_update_changes_hashes_only() -> None:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        (root / "docs").mkdir()
        (root / "docs/knowledge/records").mkdir(parents=True)
        document = root / "docs/lesson.md"
        document.write_text("lesson\n", encoding="utf-8")
        target = root / "manifest.json"
        value = fixture(digest="b" * 64)
        (root / "docs/knowledge/records/lesson-1.json").write_text(
            json.dumps(value["records"][0], indent=2, sort_keys=True) + "\n", encoding="utf-8"
        )
        before = copy.deepcopy(value)
        with mock.patch.object(checker, "ROOT", root), mock.patch.object(checker, "MANIFEST", target):
            findings = checker.update_manifest(value)
        assert not findings
        after = json.loads(target.read_text(encoding="utf-8"))
        before_hash = before["records"][0].pop("sha256")
        after_hash = after["records"][0].pop("sha256")
        assert before["records"] == after["records"]
        assert before_hash != after_hash
        assert after_hash == "sha256:" + checker.hashlib.sha256(b"lesson\n").hexdigest()


def test_path_bound_uses_unicode_scalars() -> None:
    assert len("docs/" + "é" * 504 + ".md") == checker.MAX_MANIFEST_PATH
    assert len("docs/" + "é" * 505 + ".md") == checker.MAX_MANIFEST_PATH + 1


def test_uppercase_hash_is_rejected() -> None:
    findings = checker.validate(fixture(digest="A" * 64), check_hashes=False)
    assert any("invalid sha256 proof" in finding for finding in findings)


def test_records_may_not_share_a_title_or_summary() -> None:
    # A record copied from a sibling keeps its sha256 honest - the hash still
    # binds the bytes of its own target document - while the unhashed prose
    # describes a different law. Distinctness is what catches that.
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        (root / "docs").mkdir()
        (root / "docs/spec.md").write_text("spec\n", encoding="utf-8")
        (root / "docs/other.md").write_text("other\n", encoding="utf-8")
        with mock.patch.object(checker, "ROOT", root):
            for field, other in (("title", "summary"), ("summary", "title")):
                value = v12_fixture()
                second = copy.deepcopy(value["records"][0])
                second.update(id="spec-2", path="docs/other.md")
                second[other] = "Distinct " + str(second[other])
                value["records"].append(second)
                findings = checker.validate(value, check_hashes=False)
                assert any(f"spec-2]: {field} duplicates spec-1" in finding for finding in findings), findings

            distinct = v12_fixture()
            second = copy.deepcopy(distinct["records"][0])
            second.update(id="spec-2", path="docs/other.md", title="Distinct title", summary="Distinct summary")
            distinct["records"].append(second)
            assert checker.validate(distinct, check_hashes=False) == []


def test_v12_requires_domain_registry_domain_scopes_and_law_home() -> None:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        (root / "docs").mkdir()
        (root / "docs/spec.md").write_text("spec\n", encoding="utf-8")
        value = v12_fixture()
        with mock.patch.object(checker, "ROOT", root):
            assert checker.validate(value, check_hashes=False) == []
            missing_registry = copy.deepcopy(value)
            del missing_registry["domain_registry"]
            assert checker.validate(missing_registry, check_hashes=False)
            missing_scope = copy.deepcopy(value)
            del missing_scope["records"][0]["scopes"]["domain_ids"]
            assert checker.validate(missing_scope, check_hashes=False)
            missing_home = copy.deepcopy(value)
            del missing_home["records"][0]["home_domain_id"]
            assert checker.validate(missing_home, check_hashes=False)


def test_v12_rejects_historical_law_applicability_without_home() -> None:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        (root / "docs").mkdir()
        (root / "docs/spec.md").write_text("spec\n", encoding="utf-8")
        value = v12_fixture()
        value["records"][0]["status"] = "superseded"
        value["records"][0]["successor"] = "spec-2"
        value["records"][0]["applies_to_domain_ids"] = ["product-root:concord"]
        value["records"].append({
            "id": "spec-2", "kind": "spec", "path": "docs/spec-2.md", "status": "accepted",
            "date": "2026-08-10T00:00:00Z", "title": "Successor", "summary": "Summary", "tags": [],
            "scopes": {"mode": "home", "product_ids": [], "project_ids": [], "domain_ids": [], "tag_ids": []},
            "home_domain_id": "product-root:concord", "law_relations": [{"kind": "supersedes", "target_id": "spec-1"}],
            "sha256": "sha256:" + "b" * 64,
        })
        (root / "docs/spec-2.md").write_text("successor\n", encoding="utf-8")
        del value["records"][0]["home_domain_id"]
        with mock.patch.object(checker, "ROOT", root):
            assert checker.validate(value, check_hashes=False)


def test_v12_rejects_self_referential_domain_dependency() -> None:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        (root / "docs").mkdir()
        (root / "docs/spec.md").write_text("spec\n", encoding="utf-8")
        value = v12_fixture()
        value["domain_registry"]["domains"][0]["architecture_relations"] = [{
            "kind": "depends_on",
            "target_domain_id": "product-root:concord",
            "governing_law_ids": ["spec-1"],
        }]
        with mock.patch.object(checker, "ROOT", root):
            assert checker.validate(value, check_hashes=False)


def test_v12_rejects_domain_graph_dangling_duplicate_bound_and_law_errors() -> None:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        (root / "docs").mkdir()
        (root / "docs/spec.md").write_text("spec\n", encoding="utf-8")
        base = v12_fixture()
        root_domain = base["domain_registry"]["domains"][0]
        alpha = {"domain_id": "alpha", "name": "Alpha", "purpose": "Alpha", "status": "current", "architecture_relations": []}
        beta = {"domain_id": "beta", "name": "Beta", "purpose": "Beta", "status": "current", "architecture_relations": []}

        def invalid(value: dict) -> None:
            with mock.patch.object(checker, "ROOT", root):
                assert checker.validate(value, check_hashes=False)

        unknown = copy.deepcopy(base)
        unknown["domain_registry"]["domains"][0]["extra"] = True
        invalid(unknown)
        duplicate = copy.deepcopy(base)
        duplicate["domain_registry"]["domains"].append(copy.deepcopy(root_domain))
        invalid(duplicate)
        oversized = copy.deepcopy(base)
        oversized["domain_registry"]["domains"] = [copy.deepcopy(root_domain) for _ in range(65)]
        invalid(oversized)
        dangling = copy.deepcopy(base)
        dangling["domain_registry"]["domains"].append({**alpha, "architecture_relations": [{"kind": "depends_on", "target_domain_id": "missing", "governing_law_ids": ["spec-1"]}]})
        invalid(dangling)
        bad_law = copy.deepcopy(base)
        bad_law["domain_registry"]["domains"].append({**alpha, "architecture_relations": [{"kind": "depends_on", "target_domain_id": "product-root:concord", "governing_law_ids": ["missing"]}]})
        invalid(bad_law)
        cycle = copy.deepcopy(base)
        cycle["domain_registry"]["domains"].extend([
            {**alpha, "architecture_relations": [{"kind": "depends_on", "target_domain_id": "beta", "governing_law_ids": ["spec-1"]}]},
            {**beta, "architecture_relations": [{"kind": "depends_on", "target_domain_id": "alpha", "governing_law_ids": ["spec-1"]}]},
        ])
        invalid(cycle)
        replacement_cycle = copy.deepcopy(base)
        replacement_cycle["domain_registry"]["domains"].extend([
            {**alpha, "architecture_relations": [{"kind": "replaces", "target_domain_id": "beta", "state": "building"}]},
            {**beta, "architecture_relations": [{"kind": "replaces", "target_domain_id": "alpha", "state": "building"}]},
        ])
        invalid(replacement_cycle)


def test_nul_path_is_rejected_without_traceback() -> None:
    findings = checker.validate(fixture(path="docs/lesson\x00.md"), check_hashes=False)
    assert any("forbidden or unsafe path" in finding for finding in findings)


def test_accepted_contracts_are_eligible_record_paths() -> None:
    """CD-0014 accepted C18 and ea68397 accepted C17 within days of the
    exclusion that named them. Both now carry authority, so both may carry a
    record. This proves the repeal at the checker, not only at the schema."""
    for path in ("docs/product-coordination-view.md", "docs/terminal-launcher-contract.md"):
        findings = checker.validate(fixture(path=path), check_hashes=False)
        assert not any("forbidden or unsafe path" in finding for finding in findings), (path, findings)


def test_class_exclusions_survive_the_repeal() -> None:
    """The repeal removed two named files, not the live class exclusions."""
    for path in (
        "docs/work/scratch.md",
        "docs/research/R7-expedited-parallel-work.md",
        "docs/generated-agent-contracts.md",
        "docs/api/generated.md",
        "docs/Generated-Contracts.md",
    ):
        findings = checker.validate(fixture(path=path), check_hashes=False)
        assert any("forbidden or unsafe path" in finding for finding in findings), (path, findings)


def test_eligible_paths_are_not_swept_up_by_the_substring_rule() -> None:
    """`generated` is a substring rule; near misses must stay eligible."""
    for path in ("docs/workflows.md", "docs/generation-policy.md", "docs/researcher-guide.md"):
        findings = checker.validate(fixture(path=path), check_hashes=False)
        assert not any("forbidden or unsafe path" in finding for finding in findings), (path, findings)


def test_duplicate_decision_id_files_are_rejected_deterministically() -> None:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        decisions = root / "docs/decisions"
        decisions.mkdir(parents=True)
        later = decisions / "CD-0014-z-note.md"
        earlier = decisions / "CD-0014-a-decision.md"
        later.write_text("note\n", encoding="utf-8")
        earlier.write_text("decision\n", encoding="utf-8")
        value = {
            "schema_version": "1.0",
            "supported_kinds": ["decision"],
            "indexed_kinds": ["decision"],
            "records": [{
                "id": "CD-0014",
                "kind": "decision",
                "path": "docs/decisions/CD-0014-a-decision.md",
                "status": "accepted",
                "date": "2026-08-10T00:00:00Z",
                "title": "Decision",
                "summary": "Summary",
                "tags": [],
                "scopes": {"mode": "home", "product_ids": [], "project_ids": [], "component_ids": [], "tag_ids": []},
                "sha256": "sha256:" + "a" * 64,
            }],
        }
        with mock.patch.object(checker, "ROOT", root):
            findings = checker.validate(value, check_hashes=False)
        assert findings == [
            "manifest: decision CD-0014 has multiple canonical files: "
            "docs/decisions/CD-0014-a-decision.md, docs/decisions/CD-0014-z-note.md"
        ]


def taxonomy_fixture(root: Path, kind: str, path: str, status: str) -> dict:
    """A one-record v1.2 manifest whose kind, path, and status are the subject."""
    (root / path).parent.mkdir(parents=True, exist_ok=True)
    (root / path).write_text("body\n", encoding="utf-8")
    value = v12_fixture()
    value["supported_kinds"] = ["work_note", "constitution", "decision", "spec", "lesson", "reference", "research"]
    value["indexed_kinds"] = list(value["supported_kinds"])
    record = value["records"][0]
    record["kind"], record["path"], record["status"] = kind, path, status
    if kind == "decision":
        record["id"] = Path(path).stem
    if kind not in checker.LAW_BEARING_KINDS:
        record.pop("home_domain_id", None)
    return value


def test_record_status_follows_the_kind_tier() -> None:
    """Both directions of the tier rule, for every kind the taxonomy declares."""
    cases = [
        ("constitution", "docs/constitution.md", "accepted", "published"),
        ("decision", "docs/decisions/CD-0099.md", "accepted", "published"),
        ("spec", "docs/spec.md", "accepted", "published"),
        ("lesson", "docs/lessons/one.md", "published", "accepted"),
        ("reference", "docs/installation.md", "published", "accepted"),
        ("research", "docs/market-landscape.md", "published", "accepted"),
    ]
    for kind, path, valid_status, forbidden_status in cases:
        with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
            root = Path(directory)
            valid = taxonomy_fixture(root, kind, path, valid_status)
            with mock.patch.object(checker, "ROOT", root):
                assert checker.validate(valid, check_hashes=False) == [], (kind, valid_status)
                forbidden = copy.deepcopy(valid)
                forbidden["records"][0]["status"] = forbidden_status
                findings = checker.validate(forbidden, check_hashes=False)
            assert any("invalid status/kind combination" in finding for finding in findings), (kind, findings)


def test_non_law_records_cannot_author_law_home_fields() -> None:
    for kind, path in (("lesson", "docs/lessons/one.md"), ("reference", "docs/installation.md"), ("research", "docs/market-landscape.md")):
        with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
            root = Path(directory)
            value = taxonomy_fixture(root, kind, path, "published")
            value["records"][0]["home_domain_id"] = "product-root:concord"
            with mock.patch.object(checker, "ROOT", root):
                findings = checker.validate(value, check_hashes=False)
            assert any("non-law records cannot author law-home fields" in finding for finding in findings), (kind, findings)


def test_law_relations_remain_decision_and_spec_only() -> None:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        value = taxonomy_fixture(root, "constitution", "docs/constitution.md", "accepted")
        value["records"][0]["law_relations"] = [{"kind": "refines", "target_id": "spec-2"}]
        with mock.patch.object(checker, "ROOT", root):
            findings = checker.validate(value, check_hashes=False)
        assert any("law_relations are only allowed on decision/spec records" in finding for finding in findings), findings


def test_valid_disposition_is_accepted_and_a_record_path_is_not() -> None:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        value = taxonomy_fixture(root, "spec", "docs/spec.md", "accepted")
        value["dispositions"] = [{
            "path": "docs/scratch.md",
            "disposition": "archived",
            "reason": "Superseded working note kept for provenance only.",
        }]
        with mock.patch.object(checker, "ROOT", root):
            assert checker.validate(value, check_hashes=False) == []
            collision = copy.deepcopy(value)
            collision["dispositions"][0]["path"] = "docs/spec.md"
            findings = checker.validate(collision, check_hashes=False)
        assert findings == [
            "manifest.dispositions[0]: path is both a record and a disposition: docs/spec.md"
        ]


def test_malformed_dispositions_are_rejected() -> None:
    cases = {
        "unknown field": {"path": "docs/scratch.md", "disposition": "archived", "reason": "Reason.", "owner": "operator"},
        "missing reason": {"path": "docs/scratch.md", "disposition": "archived"},
        "empty reason": {"path": "docs/scratch.md", "disposition": "archived", "reason": ""},
        "unclosed disposition": {"path": "docs/scratch.md", "disposition": "deferred", "reason": "Reason."},
        "non markdown path": {"path": "docs/scratch.txt", "disposition": "archived", "reason": "Reason."},
        "absolute path": {"path": "/docs/scratch.md", "disposition": "archived", "reason": "Reason."},
        "traversal path": {"path": "docs/../scratch.md", "disposition": "archived", "reason": "Reason."},
    }
    for name, entry in cases.items():
        with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
            root = Path(directory)
            value = taxonomy_fixture(root, "spec", "docs/spec.md", "accepted")
            value["dispositions"] = [entry]
            with mock.patch.object(checker, "ROOT", root):
                findings = checker.validate(value, check_hashes=False)
            assert findings, name


def test_duplicate_disposition_path_is_rejected() -> None:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        value = taxonomy_fixture(root, "spec", "docs/spec.md", "accepted")
        entry = {"path": "docs/scratch.md", "disposition": "archived", "reason": "Reason."}
        value["dispositions"] = [entry, dict(entry)]
        with mock.patch.object(checker, "ROOT", root):
            findings = checker.validate(value, check_hashes=False)
        assert any("duplicate disposition path" in finding for finding in findings), findings


if __name__ == "__main__":
    for name, function in sorted(globals().items()):
        if name.startswith("test_"):
            function()
    print("knowledge checker tests passed")
