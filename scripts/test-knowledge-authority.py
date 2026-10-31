#!/usr/bin/env python3
"""Focused tests for the legislated-by-default authority grammar.

Covers the shared cohort loader, the checker's authority validation, the
generator's shard-level authority validation, and the schema's legislated_by
work- id parity. Every fixture is synthetic and lives in a temporary
directory; the only committed file these tests read is the cohort asset
itself, through the loader's own repository root.
"""

from __future__ import annotations

import copy
import importlib.util
import json
import tempfile
from pathlib import Path
from unittest import mock

SCRIPTS = Path(__file__).resolve().parent
REPO_ROOT = SCRIPTS.parent


def load_module(name: str, path: Path):
    spec = importlib.util.spec_from_file_location(name, path)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


checker = load_module("knowledge_checker_under_test", SCRIPTS / "check-knowledge-index.py")
generator = load_module("knowledge_generator_under_test", SCRIPTS / "generate-knowledge-index.py")
authority_defaults = load_module("knowledge_authority_defaults_under_test", SCRIPTS / "knowledge_authority_defaults.py")

VEHICLE = authority_defaults.load().vehicle_work_id
VEHICLE_VERSION = authority_defaults.load().vehicle_contract_version
MEMBER_ID, MEMBER_KIND = "CD-0162", "decision"  # a frozen cohort member
NONMEMBER_ID, NONMEMBER_KIND = "lesson-1", "lesson"
ORDINARY_WORK = "work-abcdef1234567890"  # well-shaped, not the vehicle


def manifest_record(identifier: str, kind: str, authority: dict) -> dict:
    return {
        "id": identifier, "kind": kind,
        "path": f".concord/docs/{identifier}.md",
        "status": "published" if kind not in {"constitution", "decision", "spec"} else "accepted",
        "date": "2026-08-10T00:00:00Z", "title": f"Title {identifier}", "summary": f"Summary {identifier}",
        "tags": [], "authority": authority,
        "scopes": {"mode": "home", "product_ids": [], "project_ids": [], "domain_ids": [], "tag_ids": []},
        "sha256": "sha256:" + "a" * 64,
    }


def manifest(identifier: str, kind: str, authority: dict, schema_version: str = "1.3") -> dict:
    record = manifest_record(identifier, kind, authority)
    if kind in {"constitution", "decision", "spec"}:
        record["path"] = f".concord/docs/decisions/{identifier}-example.md" if kind == "decision" else f".concord/docs/{identifier}.md"
        record["home_domain_id"] = "product-root:concord"
        record["product_wide_rationale"] = "Frozen cohort law binds every child Domain."
    if schema_version == "1.2":
        record.pop("authority", None)
    return {
        "schema_version": schema_version,
        "supported_kinds": ["lesson", "decision"],
        "indexed_kinds": ["lesson", "decision"],
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
        "records": [record],
    }


def checker_findings(data: dict) -> list[str]:
    with tempfile.TemporaryDirectory(dir=checker.ROOT) as directory:
        root = Path(directory)
        (root / ".concord/docs").mkdir(parents=True)
        for record in data["records"]:
            target = root / record["path"]
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text("document\n", encoding="utf-8")
        with mock.patch.object(checker, "ROOT", root):
            return checker.validate(data, check_hashes=False)


def authority_findings(findings: list[str]) -> list[str]:
    return [item for item in findings if "legislated" in item or "authority" in item or "cohort" in item or "vehicle" in item]


def generator_findings(record: dict) -> list[str]:
    findings: list[str] = []
    generator.validate_authority(record, "shard", findings)
    return findings


# --- checker integration -------------------------------------------------


def test_checker_refuses_non_work_legislated_by() -> None:
    findings = checker_findings(manifest("lesson-1", "lesson", {"tier": "legislated", "legislated_by": "operator", "contract_version": 1}))
    assert authority_findings(findings), findings


def test_checker_refuses_malformed_work_suffix() -> None:
    for bad in ("work-ABCDEFGH", "work-12345", "work-", "operator", "work-abcg1234"):
        findings = checker_findings(manifest("lesson-1", "lesson", {"tier": "legislated", "legislated_by": bad, "contract_version": 1}))
        assert authority_findings(findings), (bad, findings)


def test_checker_refuses_vehicle_claim_outside_cohort() -> None:
    findings = checker_findings(manifest(NONMEMBER_ID, NONMEMBER_KIND, {"tier": "legislated", "legislated_by": VEHICLE, "contract_version": VEHICLE_VERSION}))
    assert any("cohort" in item for item in findings), findings


def test_checker_refuses_vehicle_claim_at_wrong_version() -> None:
    findings = checker_findings(manifest(MEMBER_ID, MEMBER_KIND, {"tier": "legislated", "legislated_by": VEHICLE, "contract_version": VEHICLE_VERSION + 1}))
    assert any("contract_version" in item for item in findings), findings


def test_checker_accepts_frozen_member_at_vehicle_version() -> None:
    findings = checker_findings(manifest(MEMBER_ID, MEMBER_KIND, {"tier": "legislated", "legislated_by": VEHICLE, "contract_version": VEHICLE_VERSION}))
    assert not authority_findings(findings), findings


def test_checker_accepts_ordinary_work_reference() -> None:
    findings = checker_findings(manifest("lesson-1", "lesson", {"tier": "legislated", "legislated_by": ORDINARY_WORK, "contract_version": 7}))
    assert not authority_findings(findings), findings


def test_checker_keeps_derived_and_legacy_rules() -> None:
    derived = checker_findings(manifest("lesson-1", "lesson", {"tier": "derived", "legislated_by": ORDINARY_WORK, "contract_version": 1}))
    assert any("derived record carries no legislative fields" in item for item in derived), derived
    legacy = manifest("lesson-1", "lesson", {}, schema_version="1.2")
    findings = checker_findings(legacy)
    assert not authority_findings(findings), findings


def test_checker_keeps_positive_nonboolean_version_rule() -> None:
    for bad_version in (0, -1, True):
        findings = checker_findings(manifest("lesson-1", "lesson", {"tier": "legislated", "legislated_by": ORDINARY_WORK, "contract_version": bad_version}))
        assert any("positive contract_version" in item for item in findings), (bad_version, findings)


# --- generator shard-level parity ---------------------------------------


def test_generator_refuses_non_work_legislated_by() -> None:
    findings = generator_findings(manifest_record("lesson-1", "lesson", {"tier": "legislated", "legislated_by": "operator", "contract_version": 1}))
    assert any("work-" in item for item in findings), findings


def test_generator_refuses_vehicle_claim_outside_cohort() -> None:
    findings = generator_findings(manifest_record(NONMEMBER_ID, NONMEMBER_KIND, {"tier": "legislated", "legislated_by": VEHICLE, "contract_version": VEHICLE_VERSION}))
    assert any("cohort" in item for item in findings), findings


def test_generator_refuses_vehicle_claim_at_wrong_version() -> None:
    findings = generator_findings(manifest_record(MEMBER_ID, MEMBER_KIND, {"tier": "legislated", "legislated_by": VEHICLE, "contract_version": VEHICLE_VERSION + 1}))
    assert any("contract_version" in item for item in findings), findings


def test_generator_accepts_frozen_member_and_ordinary_reference() -> None:
    member = manifest_record(MEMBER_ID, MEMBER_KIND, {"tier": "legislated", "legislated_by": VEHICLE, "contract_version": VEHICLE_VERSION})
    assert not generator_findings(member), generator_findings(member)
    ordinary = manifest_record("lesson-1", "lesson", {"tier": "legislated", "legislated_by": ORDINARY_WORK, "contract_version": 7})
    assert not generator_findings(ordinary), generator_findings(ordinary)


# --- shared loader -------------------------------------------------------


def test_loader_parses_the_committed_asset() -> None:
    defaults = authority_defaults.load()
    assert defaults.vehicle_work_id == VEHICLE
    assert isinstance(VEHICLE_VERSION, int) and VEHICLE_VERSION >= 1
    assert len(defaults.members) == 109
    assert (MEMBER_ID, MEMBER_KIND) in defaults.members
    assert (NONMEMBER_ID, NONMEMBER_KIND) not in defaults.members


def test_loader_membership_verdicts() -> None:
    defaults = authority_defaults.load()
    assert defaults.vehicle_membership_error(MEMBER_ID, MEMBER_KIND, VEHICLE_VERSION) is None
    assert defaults.vehicle_membership_error(NONMEMBER_ID, NONMEMBER_KIND, VEHICLE_VERSION) is not None
    assert defaults.vehicle_membership_error(MEMBER_ID, MEMBER_KIND, VEHICLE_VERSION + 1) is not None


def base_asset_document() -> dict:
    return {
        "schema": "knowledge-authority-defaults.v1",
        "application_sha": "a" * 40,
        "vehicle": {"work_id": ORDINARY_WORK, "contract_version": 1},
        "members": [{"record_id": "CD-0002", "kind": "decision"}],
    }


def test_loader_refuses_missing_or_corrupt_asset() -> None:
    with tempfile.TemporaryDirectory() as directory:
        try:
            authority_defaults.load(Path(directory))
        except authority_defaults.AuthorityDefaultsError:
            pass
        else:
            raise AssertionError("missing asset must raise")
        for mutate in (lambda d: d.update(schema="other"), lambda d: d.update(application_sha="nothex"), lambda d: d["vehicle"].update(contract_version=True), lambda d: d.update(members=[]), lambda d: d.update(members=[{"record_id": "CD-0002", "kind": "decision", "extra": 1}]), lambda d: d.update(members=[{"record_id": "CD-0002", "kind": "decision"}, {"record_id": "CD-0002", "kind": "decision"}])):
            document = copy.deepcopy(base_asset_document())
            mutate(document)
            try:
                authority_defaults.AuthorityDefaults(document)
            except authority_defaults.AuthorityDefaultsError:
                continue
            raise AssertionError(f"corrupt asset must raise: {document}")


def test_loader_refuses_unsorted_members() -> None:
    document = base_asset_document()
    document["members"] = [{"record_id": "CD-0002", "kind": "spec"}, {"record_id": "CD-0002", "kind": "decision"}]
    try:
        authority_defaults.AuthorityDefaults(document)
    except authority_defaults.AuthorityDefaultsError:
        return
    raise AssertionError("unsorted members must raise")


# --- schema parity -------------------------------------------------------


def test_history_census_retains_only_surviving_members() -> None:
    history = load_module("knowledge_authority_history_under_test", SCRIPTS / "check-knowledge-authority-history.py")
    document = base_asset_document()
    defaults = authority_defaults.AuthorityDefaults(document)

    def git_result(root: Path, *args: str) -> str:
        if args[0] == "cat-file":
            return "commit"
        if args[0] == "ls-tree":
            return "a blob CD-0002.json\nb blob CD-0009.json\n"
        identifier = "CD-0009" if args[1].endswith("CD-0009.json") else "CD-0002"
        return json.dumps({"id": identifier, "kind": "decision", "authority": {"tier": "legislated", "legislated_by": ORDINARY_WORK, "contract_version": 1}})

    current = {"records": [{"id": "CD-0002", "kind": "decision"}]}
    with mock.patch.object(history, "git", side_effect=git_result), mock.patch.object(history.knowledge_index, "raw_manifest", return_value=current):
        assert history.census(REPO_ROOT, defaults) == []
        current["records"].append({"id": "CD-0009", "kind": "decision"})
        assert any("missing cohort member" in finding for finding in history.census(REPO_ROOT, defaults))
        current["records"].clear()
        assert any("expanded cohort member" in finding for finding in history.census(REPO_ROOT, defaults))


def test_schema_legislated_by_matches_the_work_grammar() -> None:
    schema = json.loads((REPO_ROOT / "contracts/concord-knowledge-index.v1.schema.json").read_text(encoding="utf-8"))
    legislated_by = schema["$defs"]["authority"]["properties"]["legislated_by"]
    assert legislated_by.get("pattern") == authority_defaults.WORK_ID_PATTERN.pattern
    assert legislated_by.get("maxLength") == 128
    assert legislated_by.get("type") == "string"
    version = schema["$defs"]["authority"]["properties"]["contract_version"]
    assert version == {"type": "integer", "minimum": 1, "maximum": 2147483647}


if __name__ == "__main__":
    failures = 0
    for name, test in sorted(globals().items()):
        if name.startswith("test_") and callable(test):
            try:
                test()
            except AssertionError as exc:
                failures += 1
                print(f"FAIL {name}: {exc}")
            except Exception as exc:  # noqa: BLE001 - report and continue
                failures += 1
                print(f"ERROR {name}: {type(exc).__name__}: {exc}")
    if failures:
        raise SystemExit(f"{failures} failing test(s)")
    print("knowledge authority grammar tests passed")
