#!/usr/bin/env python3
"""Focused tests for the knowledge-index shard generator."""

from __future__ import annotations

import importlib.util
import json
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "generate_knowledge_index", Path(__file__).with_name("generate-knowledge-index.py")
)
assert SPEC and SPEC.loader
generator = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(generator)


def build_root(directory: str) -> Path:
    root = Path(directory)
    (root / "docs/knowledge/records").mkdir(parents=True)
    (root / "docs/knowledge/domain-registry.json").write_text(
        json.dumps({
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
        }, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    return root


def record(identifier: str) -> dict:
    return {
        "id": identifier,
        "kind": "lesson",
        "path": f"docs/{identifier}.md",
        "status": "published",
        "date": "2026-08-20T00:00:00Z",
        "title": identifier,
        "summary": "A bounded lesson.",
        "tags": [],
        "scopes": {"mode": "home", "product_ids": [], "project_ids": [], "domain_ids": [], "tag_ids": []},
        "sha256": "sha256:" + "a" * 64,
    }


def write_shard(root: Path, value: dict) -> None:
    (root / "docs/knowledge/records" / f"{value['id']}.json").write_text(
        json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + "\n", encoding="utf-8"
    )


def run(*args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, str(ROOT / "scripts/generate-knowledge-index.py"), *args],
        cwd=ROOT,
        capture_output=True,
        text=True,
    )


def test_clean_generation_and_id_order() -> None:
    with tempfile.TemporaryDirectory() as directory:
        root = build_root(directory)
        write_shard(root, record("ZZ-0001"))
        write_shard(root, record("AA-0001"))
        result = run("--update", "--root", str(root))
        assert result.returncode == 0, result.stderr
        aggregate = json.loads((root / generator.AGGREGATE).read_text(encoding="utf-8"))
        assert [item["id"] for item in aggregate["records"]] == ["AA-0001", "ZZ-0001"]
        assert run("--check", "--root", str(root)).returncode == 0


def test_stale_aggregate_has_bounded_finding() -> None:
    with tempfile.TemporaryDirectory() as directory:
        root = build_root(directory)
        write_shard(root, record("AA-0001"))
        assert run("--update", "--root", str(root)).returncode == 0
        aggregate = root / generator.AGGREGATE
        aggregate.write_bytes(aggregate.read_bytes().replace(b"AA-0001", b"ZZ-0001", 1))
        result = run("--check", "--root", str(root))
        assert result.returncode == 1
        assert "aggregate drift" in result.stderr
        assert len(result.stderr.splitlines()) <= 85


def test_malformed_shard_is_rejected() -> None:
    with tempfile.TemporaryDirectory() as directory:
        root = build_root(directory)
        broken = record("AA-0001")
        broken["unknown"] = True
        write_shard(root, broken)
        result = run("--check", "--root", str(root))
        assert result.returncode == 1
        assert "unknown fields" in result.stdout


def test_legacy_aggregate_template_is_rejected() -> None:
    with tempfile.TemporaryDirectory() as directory:
        root = build_root(directory)
        write_shard(root, record("AA-0001"))
        template = {
            "schema_version": "1.1",
            "supported_kinds": ["lesson"],
            "indexed_kinds": ["lesson"],
        }
        findings: list[str] = []
        assert generator.derive_aggregate(root, findings, template) is None
        assert findings == ["aggregate template schema_version must be 1.2"]


def test_generation_is_deterministic() -> None:
    with tempfile.TemporaryDirectory() as directory:
        root = build_root(directory)
        write_shard(root, record("BB-0001"))
        write_shard(root, record("AA-0001"))
        assert run("--update", "--root", str(root)).returncode == 0
        first = (root / generator.AGGREGATE).read_bytes()
        assert run("--update", "--root", str(root)).returncode == 0
        assert (root / generator.AGGREGATE).read_bytes() == first


def test_repository_shards_match_aggregate() -> None:
    result = run("--check")
    assert result.returncode == 0, result.stderr


if __name__ == "__main__":
    failures = 0
    tests = [value for name, value in sorted(globals().items()) if name.startswith("test_") and callable(value)]
    for test in tests:
        try:
            test()
            print(f"ok  {test.__name__}")
        except AssertionError as err:
            failures += 1
            print(f"FAIL {test.__name__}: {err}")
    print(f"knowledge index generator tests passed: {len(tests) - failures}/{len(tests)}")
    raise SystemExit(1 if failures else 0)
