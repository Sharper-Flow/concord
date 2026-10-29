#!/usr/bin/env python3
"""Focused tests for the durable-knowledge checker and atomic updater."""

from __future__ import annotations

import ast
import copy
import importlib.util
import json
import tempfile
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).with_name("check-knowledge-index.py")
SPEC = importlib.util.spec_from_file_location("knowledge_checker", SCRIPT)

checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)

SHARD_FORMAT_SPEC = importlib.util.spec_from_file_location(
    "shard_format", Path(__file__).with_name("shard_format.py")
)
shard_format = importlib.util.module_from_spec(SHARD_FORMAT_SPEC)
SHARD_FORMAT_SPEC.loader.exec_module(shard_format)


def fixture(path: str = ".concord/docs/lesson.md", digest: str = "a" * 64) -> dict:
    return {
        "schema_version": "1.2",
        "supported_kinds": ["lesson", "research"],
        "indexed_kinds": ["lesson"],
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
            "id": "lesson-1", "kind": "lesson", "path": path, "status": "published",
            "date": "2026-08-10T00:00:00Z", "title": "Lesson", "summary": "Summary", "tags": [],
            "authority": {"tier": "derived"},
            "scopes": {"mode": "home", "product_ids": [], "project_ids": [], "domain_ids": [], "tag_ids": []},
            "sha256": "sha256:" + digest,
        }],
    }


def write_shard_tree(root: Path, value: dict) -> None:
    """Lay the fixture out as the shard tree the composer reads."""
    (root / ".concord/docs/knowledge/records").mkdir(parents=True, exist_ok=True)
    head = {key: value[key] for key in ("schema_version", "supported_kinds", "indexed_kinds") if key in value}
    (root / ".concord/docs/knowledge/manifest.json").write_text(json.dumps(head, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    (root / ".concord/docs/knowledge/domain-registry.json").write_text(
        json.dumps(value["domain_registry"], indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    for record in value["records"]:
        (root / ".concord/docs/knowledge/records" / f"{record['id']}.json").write_text(
            json.dumps(record, indent=2, sort_keys=True) + "\n", encoding="utf-8"
        )


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
            "id": "spec-1", "kind": "spec", "path": ".concord/docs/spec.md", "status": "accepted",
            "date": "2026-08-10T00:00:00Z", "title": "Decision", "summary": "Summary", "tags": [],
            "authority": {"tier": "legislated", "legislated_by": "work-0f1e2d3c4b5a6978", "contract_version": 1},
            "scopes": {"mode": "home", "product_ids": [], "project_ids": [], "domain_ids": [], "tag_ids": []},
            "home_domain_id": "product-root:concord", "product_wide_rationale": "Product-wide law binding every child Domain.",
            "sha256": "sha256:" + "a" * 64,
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
        (root / ".concord/docs").mkdir(parents=True)
        document = root / ".concord/docs/lesson.md"
        document.write_text("lesson\n", encoding="utf-8")
        value = fixture(digest="b" * 64)
        write_shard_tree(root, value)
        shard = root / ".concord/docs/knowledge/records/lesson-1.json"
        original = shard.read_text(encoding="utf-8")
        value["unknown"] = True
        with mock.patch.object(checker, "ROOT", root):
            findings = checker.update_manifest(value)
        assert findings
        assert shard.read_text(encoding="utf-8") == original


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
        (root / ".concord/docs").mkdir(parents=True)
        document = root / ".concord/docs/lesson.md"
        document.write_text("lesson\n", encoding="utf-8")
        value = fixture(digest="b" * 64)
        write_shard_tree(root, value)
        before = copy.deepcopy(value)
        with mock.patch.object(checker, "ROOT", root):
            findings = checker.update_manifest(value)
        assert not findings
        after = json.loads((root / ".concord/docs/knowledge/records/lesson-1.json").read_text(encoding="utf-8"))
        before_hash = before["records"][0].pop("sha256")
        after_hash = after.pop("sha256")
        assert before["records"] == [after]
        assert before_hash != after_hash
        assert after_hash == "sha256:" + checker.hashlib.sha256(b"lesson\n").hexdigest()


def test_path_bound_uses_unicode_scalars() -> None:
    assert len(".concord/docs/" + "é" * 495 + ".md") == checker.MAX_MANIFEST_PATH
    assert len(".concord/docs/" + "é" * 496 + ".md") == checker.MAX_MANIFEST_PATH + 1


def test_uppercase_hash_is_rejected() -> None:
    findings = checker.validate(fixture(digest="A" * 64), check_hashes=False)
    assert any("invalid sha256 proof" in finding for finding in findings)


def test_non_spec_record_cannot_carry_criterion_bindings() -> None:
    value = fixture()
    value["records"][0]["criterion_bindings"] = [{"criterion": 1, "scenario": "WF01-capture-late-outcome"}]
    findings = checker.validate(value, check_hashes=False)
    assert any("criterion_bindings are only allowed on spec records" in finding for finding in findings), findings


def spec_fixture_with_bindings(bindings: list[dict]) -> dict:
    value = v12_fixture()
    value["records"][0]["criterion_bindings"] = bindings
    return value


def test_spec_criterion_predicate_binding_is_valid() -> None:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        (root / ".concord/docs").mkdir(parents=True)
        (root / ".concord/docs/spec.md").write_text("spec\n", encoding="utf-8")
        value = spec_fixture_with_bindings([{"criterion": 1, "work_id": "work-" + "a" * 24, "predicate_id": "predicate:criterion-bindings-predicate-form"}])
        with mock.patch.object(checker, "ROOT", root):
            assert checker.validate(value, check_hashes=False) == []


def test_spec_criterion_predicate_binding_rejects_bad_shapes() -> None:
    work_id = "work-" + "a" * 24
    predicate_id = "predicate:criterion-bindings-predicate-form"
    for name, binding in {
        "bad predicate prefix": {"criterion": 1, "work_id": work_id, "predicate_id": "pred:criterion-bindings-predicate-form"},
        "bad work id": {"criterion": 1, "work_id": "job-1234", "predicate_id": predicate_id},
        "half predicate reference": {"criterion": 1, "work_id": work_id},
        "mixed with scenario": {"criterion": 1, "scenario": "WF01-capture-late-outcome", "work_id": work_id, "predicate_id": predicate_id},
    }.items():
        with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
            root = Path(directory)
            (root / ".concord/docs").mkdir(parents=True)
            (root / ".concord/docs/spec.md").write_text("spec\n", encoding="utf-8")
            value = spec_fixture_with_bindings([binding])
            with mock.patch.object(checker, "ROOT", root):
                findings = checker.validate(value, check_hashes=False)
        assert findings, name
        assert any("binding" in finding for finding in findings), (name, findings)


def test_records_may_not_share_a_title_or_summary() -> None:
    # A record copied from a sibling keeps its sha256 honest - the hash still
    # binds the bytes of its own target document - while the unhashed prose
    # describes a different law. Distinctness is what catches that.
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        (root / ".concord/docs").mkdir(parents=True)
        (root / ".concord/docs/spec.md").write_text("spec\n", encoding="utf-8")
        (root / ".concord/docs/other.md").write_text("other\n", encoding="utf-8")
        with mock.patch.object(checker, "ROOT", root):
            for field, other in (("title", "summary"), ("summary", "title")):
                value = v12_fixture()
                second = copy.deepcopy(value["records"][0])
                second.update(id="spec-2", path=".concord/docs/other.md")
                second[other] = "Distinct " + str(second[other])
                value["records"].append(second)
                findings = checker.validate(value, check_hashes=False)
                assert any(f"spec-2]: {field} duplicates spec-1" in finding for finding in findings), findings

            distinct = v12_fixture()
            second = copy.deepcopy(distinct["records"][0])
            second.update(id="spec-2", path=".concord/docs/other.md", title="Distinct title", summary="Distinct summary")
            distinct["records"].append(second)
            assert checker.validate(distinct, check_hashes=False) == []


def test_v12_requires_domain_registry_domain_scopes_and_law_home() -> None:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        (root / ".concord/docs").mkdir(parents=True)
        (root / ".concord/docs/spec.md").write_text("spec\n", encoding="utf-8")
        value = v12_fixture()
        with mock.patch.object(checker, "ROOT", root):
            assert checker.validate(value, check_hashes=False) == []
            missing_registry = copy.deepcopy(value)
            del missing_registry["domain_registry"]
            assert checker.validate(missing_registry, check_hashes=False)
            bad_relation = copy.deepcopy(value)
            bad_relation["records"][0]["law_relations"] = [{"kind": "refines", "target_id": "SRC/LAW"}]
            assert any("invalid law relation" in finding for finding in checker.validate(bad_relation, check_hashes=False))
            missing_scope = copy.deepcopy(value)
            del missing_scope["records"][0]["scopes"]["domain_ids"]
            assert checker.validate(missing_scope, check_hashes=False)
            missing_home = copy.deepcopy(value)
            del missing_home["records"][0]["home_domain_id"]
            assert checker.validate(missing_home, check_hashes=False)


def test_legacy_manifest_versions_are_rejected() -> None:
    for version in ("1.0", "1.1", "1.4"):
        value = fixture()
        value["schema_version"] = version
        findings = checker.validate(value, check_hashes=False)
        assert any("schema_version must be one of" in finding for finding in findings)


def test_schema_1_2_record_needs_no_authority_object() -> None:
    # CD-0159 arrived after 1.2 was published. A 1.2 corpus declares no
    # authority object and the parser supplies the derived tier, so the
    # validator must not demand a field that version never carried.
    value = fixture()
    value["schema_version"] = "1.2"
    for record in value["records"]:
        record.pop("authority", None)
    findings = checker.validate(value, check_hashes=False)
    assert not any("authority" in finding for finding in findings), findings


def test_schema_1_3_record_requires_an_authority_object() -> None:
    value = fixture()
    value["schema_version"] = "1.3"
    for record in value["records"]:
        record.pop("authority", None)
    findings = checker.validate(value, check_hashes=False)
    assert any("authority" in finding for finding in findings), findings


def test_v12_rejects_law_applicability_without_home() -> None:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        (root / ".concord/docs").mkdir(parents=True)
        (root / ".concord/docs/spec.md").write_text("spec\n", encoding="utf-8")
        value = v12_fixture()
        value["records"][0]["applies_to_domain_ids"] = ["product-root:concord"]
        del value["records"][0]["home_domain_id"]
        del value["records"][0]["product_wide_rationale"]
        with mock.patch.object(checker, "ROOT", root):
            assert checker.validate(value, check_hashes=False)


def test_v12_rejects_self_referential_domain_dependency() -> None:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        (root / ".concord/docs").mkdir(parents=True)
        (root / ".concord/docs/spec.md").write_text("spec\n", encoding="utf-8")
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
        (root / ".concord/docs").mkdir(parents=True)
        (root / ".concord/docs/spec.md").write_text("spec\n", encoding="utf-8")
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
    findings = checker.validate(fixture(path=".concord/docs/lesson\x00.md"), check_hashes=False)
    assert any("forbidden or unsafe path" in finding for finding in findings)


def test_accepted_contracts_are_eligible_record_paths() -> None:
    """CD-0014 accepted C18 and ea68397 accepted C17 within days of the
    exclusion that named them. Both now carry authority, so both may carry a
    record. This proves the repeal at the checker, not only at the schema."""
    for path in (".concord/docs/product-coordination-view.md", ".concord/docs/terminal-launcher-contract.md"):
        findings = checker.validate(fixture(path=path), check_hashes=False)
        assert not any("forbidden or unsafe path" in finding for finding in findings), (path, findings)


def test_class_exclusions_survive_the_repeal() -> None:
    """The repeal removed two named files, not the live class exclusions."""
    for path in (
        ".concord/docs/work/scratch.md",
        ".concord/docs/research/R7-expedited-parallel-work.md",
        ".concord/docs/generated-agent-contracts.md",
        ".concord/docs/api/generated.md",
        ".concord/docs/Generated-Contracts.md",
    ):
        findings = checker.validate(fixture(path=path), check_hashes=False)
        assert any("forbidden or unsafe path" in finding for finding in findings), (path, findings)


def test_eligible_paths_are_not_swept_up_by_the_substring_rule() -> None:
    """`generated` is a substring rule; near misses must stay eligible."""
    for path in (".concord/docs/workflows.md", ".concord/docs/generation-policy.md", ".concord/docs/researcher-guide.md"):
        findings = checker.validate(fixture(path=path), check_hashes=False)
        assert not any("forbidden or unsafe path" in finding for finding in findings), (path, findings)


def test_duplicate_decision_id_files_are_rejected_deterministically() -> None:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        decisions = root / ".concord/docs/decisions"
        decisions.mkdir(parents=True)
        later = decisions / "CD-0014-z-note.md"
        earlier = decisions / "CD-0014-a-decision.md"
        later.write_text("note\n", encoding="utf-8")
        earlier.write_text("decision\n", encoding="utf-8")
        value = v12_fixture()
        value["supported_kinds"] = ["decision"]
        value["indexed_kinds"] = ["decision"]
        value["records"] = [{
                "id": "CD-0014",
                "kind": "decision",
                "path": ".concord/docs/decisions/CD-0014-a-decision.md",
                "status": "accepted",
                "date": "2026-08-10T00:00:00Z",
                "title": "Decision",
                 "summary": "Summary",
                 "tags": [],
				"authority": {"tier": "legislated", "legislated_by": "work-0f1e2d3c4b5a6978", "contract_version": 1},
				"scopes": {"mode": "home", "product_ids": [], "project_ids": [], "domain_ids": [], "tag_ids": []},
				"home_domain_id": "product-root:concord",
				"product_wide_rationale": "Fixture law binds every child Domain.",
                "sha256": "sha256:" + "a" * 64,
            }]
        with mock.patch.object(checker, "ROOT", root):
            findings = checker.validate(value, check_hashes=False)
        assert findings == [
            "manifest: decision CD-0014 has multiple canonical files: "
            ".concord/docs/decisions/CD-0014-a-decision.md, .concord/docs/decisions/CD-0014-z-note.md"
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
        record.pop("product_wide_rationale", None)
    return value


def test_record_status_follows_the_kind_tier() -> None:
    """Both directions of the tier rule, for every kind the taxonomy declares."""
    cases = [
        ("constitution", ".concord/docs/constitution.md", "accepted", "published"),
        ("decision", ".concord/docs/decisions/CD-0099.md", "accepted", "published"),
        ("spec", ".concord/docs/spec.md", "accepted", "published"),
        ("lesson", ".concord/docs/lessons/one.md", "published", "accepted"),
        ("reference", ".concord/docs/installation.md", "published", "accepted"),
        ("research", ".concord/docs/market-landscape.md", "published", "accepted"),
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
    for kind, path in (("lesson", ".concord/docs/lessons/one.md"), ("reference", ".concord/docs/installation.md"), ("research", ".concord/docs/market-landscape.md")):
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
        value = taxonomy_fixture(root, "constitution", ".concord/docs/constitution.md", "accepted")
        value["records"][0]["law_relations"] = [{"kind": "refines", "target_id": "spec-2"}]
        with mock.patch.object(checker, "ROOT", root):
            findings = checker.validate(value, check_hashes=False)
        assert any("law_relations are only allowed on decision/spec records" in finding for finding in findings), findings


def test_valid_disposition_is_accepted_and_a_record_path_is_not() -> None:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        value = taxonomy_fixture(root, "spec", ".concord/docs/spec.md", "accepted")
        value["dispositions"] = [{
            "path": ".concord/docs/scratch.md",
            "disposition": "archived",
            "reason": "Superseded working note kept for provenance only.",
        }]
        with mock.patch.object(checker, "ROOT", root):
            assert checker.validate(value, check_hashes=False) == []
            collision = copy.deepcopy(value)
            collision["dispositions"][0]["path"] = ".concord/docs/spec.md"
            findings = checker.validate(collision, check_hashes=False)
        assert findings == [
            "manifest.dispositions[0]: path is both a record and a disposition: .concord/docs/spec.md"
        ]


def test_malformed_dispositions_are_rejected() -> None:
    cases = {
        "unknown field": {"path": ".concord/docs/scratch.md", "disposition": "archived", "reason": "Reason.", "owner": "operator"},
        "missing reason": {"path": ".concord/docs/scratch.md", "disposition": "archived"},
        "empty reason": {"path": ".concord/docs/scratch.md", "disposition": "archived", "reason": ""},
        "unclosed disposition": {"path": ".concord/docs/scratch.md", "disposition": "deferred", "reason": "Reason."},
        "non markdown path": {"path": ".concord/docs/scratch.txt", "disposition": "archived", "reason": "Reason."},
        "absolute path": {"path": "/docs/scratch.md", "disposition": "archived", "reason": "Reason."},
        "traversal path": {"path": ".concord/docs/../scratch.md", "disposition": "archived", "reason": "Reason."},
    }
    for name, entry in cases.items():
        with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
            root = Path(directory)
            value = taxonomy_fixture(root, "spec", ".concord/docs/spec.md", "accepted")
            value["dispositions"] = [entry]
            with mock.patch.object(checker, "ROOT", root):
                findings = checker.validate(value, check_hashes=False)
            assert findings, name


def test_duplicate_disposition_path_is_rejected() -> None:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        value = taxonomy_fixture(root, "spec", ".concord/docs/spec.md", "accepted")
        entry = {"path": ".concord/docs/scratch.md", "disposition": "archived", "reason": "Reason."}
        value["dispositions"] = [entry, dict(entry)]
        with mock.patch.object(checker, "ROOT", root):
            findings = checker.validate(value, check_hashes=False)
        assert any("duplicate disposition path" in finding for finding in findings), findings


def test_root_home_requires_a_stated_product_wide_rationale() -> None:
    """The root is reachable by deciding nothing, so it carries a stated claim."""
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        (root / ".concord/docs").mkdir(parents=True)
        (root / ".concord/docs/spec.md").write_text("spec\n", encoding="utf-8")
        value = v12_fixture()
        with mock.patch.object(checker, "ROOT", root):
            assert checker.validate(value, check_hashes=False) == []
            for blank in (None, "", "   "):
                missing = copy.deepcopy(value)
                if blank is None:
                    del missing["records"][0]["product_wide_rationale"]
                else:
                    missing["records"][0]["product_wide_rationale"] = blank
                findings = checker.validate(missing, check_hashes=False)
                assert any("must state product_wide_rationale" in finding for finding in findings), (blank, findings)


def test_only_root_homed_law_states_a_product_wide_rationale() -> None:
    """A child home has decided already; the claim would contradict it."""
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        (root / ".concord/docs").mkdir(parents=True)
        (root / ".concord/docs/spec.md").write_text("spec\n", encoding="utf-8")
        value = v12_fixture()
        value["domain_registry"]["domains"].append({
            "domain_id": "child", "name": "Child", "purpose": "A child Domain",
            "parent_domain_id": "product-root:concord", "status": "current",
            "architecture_relations": [],
        })
        value["records"][0]["home_domain_id"] = "child"
        with mock.patch.object(checker, "ROOT", root):
            findings = checker.validate(value, check_hashes=False)
        assert any("only root-homed law states product_wide_rationale" in finding for finding in findings), findings

def test_record_shards_carry_the_encoding_update_writes() -> None:
    """A shard differing only in whitespace parses, so nothing else catches it."""
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        records_dir = root / ".concord/docs/knowledge/records"
        coverage_dir = root / ".concord/docs/knowledge/coverage"
        records_dir.mkdir(parents=True)
        coverage_dir.mkdir(parents=True)
        canonical = shard_format.canonical_bytes
        # Insertion order is deliberately unsorted so the sort_keys drift differs.
        record = {"title": "Decision", "id": "CD-0099", "kind": "decision"}
        coverage = {"state": "satisfied", "id": "CD-0099"}
        for label, shard_dir, shard in (
            ("record", records_dir, record),
            ("coverage", coverage_dir, coverage),
        ):
            shard_path = shard_dir / "CD-0099.json"
            shard_path.write_bytes(canonical(shard))
            with mock.patch.object(checker, "ROOT", root):
                findings: list[str] = []
                checker.check_shard_encoding(findings)
                assert findings == [], (label, findings)
                drifts = (
                    json.dumps(shard, indent=1, ensure_ascii=False, sort_keys=True) + "\n",
                    json.dumps(shard, indent=2, ensure_ascii=False, sort_keys=True),
                    json.dumps(shard, indent=2, ensure_ascii=False, sort_keys=False) + "\n",
                )
                for drift in drifts:
                    assert drift.encode("utf-8") != canonical(shard), (label, drift)
                    shard_path.write_text(drift, encoding="utf-8")
                    findings = []
                    checker.check_shard_encoding(findings)
                    assert any("not canonically encoded" in f for f in findings), (label, drift, findings)
                    shard_path.write_bytes(canonical(shard))


SCHEMA_PATH = Path(__file__).resolve().parents[1] / "contracts" / "concord-knowledge-index.v1.schema.json"


def schema_validator():
    """Load the canonical knowledge-index schema for document-level checks."""
    try:
        import jsonschema
    except ImportError as error:  # pragma: no cover - CI images carry jsonschema
        raise AssertionError("jsonschema is required to validate the canonical schema") from error
    schema = json.loads(SCHEMA_PATH.read_text(encoding="utf-8"))
    jsonschema.Draft202012Validator.check_schema(schema)
    return jsonschema.Draft202012Validator(schema)


def schema_errors(validator, document: dict) -> list[str]:
    return sorted(error.message for error in validator.iter_errors(document))


def test_composed_repository_manifest_passes_the_canonical_schema() -> None:
    """The shipped corpus validates against the shipped schema, end to end."""
    validator = schema_validator()
    compose_spec = importlib.util.spec_from_file_location(
        "knowledge_index", Path(__file__).with_name("knowledge_index.py")
    )
    knowledge_index = importlib.util.module_from_spec(compose_spec)
    compose_spec.loader.exec_module(knowledge_index)
    errors = schema_errors(validator, knowledge_index.compose_manifest())
    assert errors == [], errors[:5]


CORRECTED_DERIVED_RECORD_IDS = frozenset({
    "CD-0165", "CD-0166", "CD-0168", "CD-0169", "CD-0170", "CD-0176",
    "CD-0177", "CD-0178", "CD-0180", "CD-0189", "CD-0192", "CD-0195", "CD-0197", "CD-0198",
    "CD-0199", "CD-0203", "CD-0204", "CD-0207", "CD-0209",
})


def test_composed_manifest_carries_the_corrected_derived_authorities() -> None:
    """CD-0159 D7: these records' legislative declarations failed D2 and are derived."""
    compose_spec = importlib.util.spec_from_file_location(
        "knowledge_index", Path(__file__).with_name("knowledge_index.py")
    )
    knowledge_index = importlib.util.module_from_spec(compose_spec)
    compose_spec.loader.exec_module(knowledge_index)
    manifest = knowledge_index.compose_manifest()
    record_ids = [record["id"] for record in manifest["records"]]
    by_id = {record["id"]: record for record in manifest["records"]}
    for record_id in sorted(CORRECTED_DERIVED_RECORD_IDS):
        assert record_ids.count(record_id) == 1, (record_id, record_ids.count(record_id))
        assert by_id[record_id]["authority"] == {"tier": "derived"}, (record_id, by_id[record_id]["authority"])


def external_record_document(path: str, with_override: bool) -> dict:
    """A v1.3 manifest whose one record lives outside the default tree, with
    or without the operator override head that admits it."""
    value = v12_fixture()
    value["schema_version"] = "1.3"
    value["supported_kinds"] = ["reference"]
    value["indexed_kinds"] = ["reference"]
    value["records"] = [{
        "id": "external-1", "kind": "reference", "path": path, "status": "published",
        "date": "2026-08-10T00:00:00Z", "title": "External", "summary": "External placement", "tags": [],
        "authority": {"tier": "derived"},
        "scopes": {"mode": "home", "product_ids": [], "project_ids": [], "domain_ids": [], "tag_ids": []},
        "sha256": "sha256:" + "a" * 64,
    }]
    if with_override:
        value["operator_overrides"] = [{
            "path": "external/knowledge/", "product_id": "concord",
            "recorded_in": "external-1", "reason": "the operator recorded this placement for the external tree",
        }]
    return value


def test_schema_admits_only_the_closed_external_record_shape() -> None:
    """CD-0194 D2 schema/reader lockstep, at the schema itself.

    The default-tree rule moved into $defs.record.allOf so an
    override-admitted external record can validate beside it. A clean
    external markdown path under an override head validates; a generated
    path, a traversal path, and a non-markdown path refuse, and the override
    object bound is the closed four-field shape.
    """
    validator = schema_validator()
    assert schema_errors(validator, external_record_document("external/knowledge/reference.md", True)) == []

    generated = schema_errors(validator, external_record_document("external/knowledge/generated-reference.md", True))
    assert any("does not match" in message for message in generated), generated

    traversal = schema_errors(validator, external_record_document("external/../secrets.md", True))
    assert traversal != [], traversal

    dot_segment = schema_errors(validator, external_record_document("external/./reference.md", True))
    assert dot_segment != [], dot_segment

    not_markdown = schema_errors(validator, external_record_document("external/knowledge/reference.txt", True))
    assert not_markdown != [], not_markdown

    malformed = external_record_document("external/knowledge/reference.md", True)
    malformed["operator_overrides"][0]["extra"] = "field"
    errors = schema_errors(validator, malformed)
    assert any("Additional properties are not allowed" in message for message in errors), errors


def test_checker_admits_only_an_override_of_the_owning_product() -> None:
    """CD-0194 D2 reader lockstep, at the checker: the schema declares the
    closed external shape, and record_path_admitted gates it on an override
    that names the manifest's owning Product."""
    admitted = checker.record_path_admitted(
        "external/knowledge/reference.md",
        [{"path": "external/knowledge/", "product_id": "concord", "recorded_in": "r", "reason": "recorded placement"}],
        "concord",
    )
    assert admitted is True
    foreign = checker.record_path_admitted(
        "external/knowledge/reference.md",
        [{"path": "external/knowledge/", "product_id": "other-product", "recorded_in": "r", "reason": "recorded placement"}],
        "concord",
    )
    assert foreign is False
    ownerless = checker.record_path_admitted(
        "external/knowledge/reference.md",
        [{"path": "external/knowledge/", "product_id": "concord", "recorded_in": "r", "reason": "recorded placement"}],
        None,
    )
    assert ownerless is False


def run_tests() -> None:
    """Run every module-level test, and prove none was missed.

    globals() is read at the moment this executes, so a test appended below the
    call is not yet defined and silently never runs. Parsing the source for the
    authoritative set turns that omission into a failure instead.
    """
    ran = set()
    for name, function in sorted(globals().items()):
        if name.startswith("test_"):
            function()
            ran.add(name)
    declared = {
        node.name
        for node in ast.parse(Path(__file__).read_text(encoding="utf-8")).body
        if isinstance(node, ast.FunctionDef) and node.name.startswith("test_")
    }
    missed = sorted(declared - ran)
    if missed:
        raise AssertionError(f"tests defined but never run: {', '.join(missed)}")
    print("knowledge checker tests passed")


if __name__ == "__main__":
    run_tests()
