#!/usr/bin/env python3
"""Tests for scripts/generate-law-coverage.py.

The generator owns the shard-to-aggregate pipeline: it validates each shard,
sorts records by id deterministically, writes the aggregate, and can check
whether the aggregate is stale. These tests exercise the public functions so
the repository can catch generator regressions without relying on the on-disk
aggregate.
"""
from __future__ import annotations

import importlib.util
import json
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "generate_law_coverage", Path(__file__).with_name("generate-law-coverage.py")
)
assert SPEC and SPEC.loader
generator = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(generator)


def build_root(directory: str) -> Path:
    root = Path(directory)
    shard_dir = root / "docs/knowledge/coverage"
    shard_dir.mkdir(parents=True, exist_ok=True)
    return root


def write_shard(root: Path, record: dict) -> None:
    shard_path = root / "docs/knowledge/coverage" / f"{record['id']}.json"
    shard_path.write_text(
        json.dumps(record, ensure_ascii=False, sort_keys=True, indent=2) + "\n",
        encoding="utf-8",
    )


def test_empty_shard_directory_is_rejected() -> None:
    with tempfile.TemporaryDirectory() as directory:
        root = build_root(directory)
        findings: list[str] = []
        result = generator.derive_aggregate(root, findings)
        assert result is None
        assert any("no shards found" in finding for finding in findings)


def test_aggregate_sorted_by_id_and_rejects_stale() -> None:
    with tempfile.TemporaryDirectory() as directory:
        root = build_root(directory)
        write_shard(root, {"id": "ZZ-0001", "state": "outstanding", "issue": 1})
        write_shard(root, {"id": "AA-0001", "state": "outstanding", "issue": 2})
        write_shard(root, {"id": "CD-0001", "state": "outstanding", "issue": 3})

        findings: list[str] = []
        derived = generator.derive_aggregate(root, findings)
        assert derived is not None and not findings
        aggregate = json.loads(derived.decode("utf-8"))
        assert [record["id"] for record in aggregate["records"]] == [
            "AA-0001",
            "CD-0001",
            "ZZ-0001",
        ]

        aggregate_path = root / "docs/law-coverage.v1.json"
        aggregate_path.write_bytes(derived)

        # --check passes on the freshly written aggregate.
        result = subprocess.run(
            [sys.executable, str(ROOT / "scripts/generate-law-coverage.py"), "--check", "--root", str(root)],
            capture_output=True,
            text=True,
        )
        assert result.returncode == 0, result.stderr

        # Hand-edit the aggregate and --check fails.
        broken = json.loads(aggregate_path.read_text(encoding="utf-8"))
        broken["records"].pop()
        aggregate_path.write_text(
            json.dumps(broken, ensure_ascii=False, sort_keys=False, indent=2) + "\n",
            encoding="utf-8",
        )
        result = subprocess.run(
            [sys.executable, str(ROOT / "scripts/generate-law-coverage.py"), "--check", "--root", str(root)],
            capture_output=True,
            text=True,
        )
        assert result.returncode == 1
        assert "aggregate drift" in result.stderr.lower()


def test_malformed_shard_is_rejected() -> None:
    with tempfile.TemporaryDirectory() as directory:
        root = build_root(directory)
        write_shard(root, {"id": "CD-0001", "state": "satisfied"})
        result = subprocess.run(
            [sys.executable, str(ROOT / "scripts/generate-law-coverage.py"), "--check", "--root", str(root)],
            capture_output=True,
            text=True,
        )
        assert result.returncode == 1
        output = result.stdout + result.stderr
        assert "evidence must be a non-empty array" in output


def test_repository_shards_generate_up_to_date_aggregate() -> None:
    """The on-disk aggregate matches a fresh derivation from the shards."""
    result = subprocess.run(
        [sys.executable, str(ROOT / "scripts/generate-law-coverage.py"), "--check"],
        capture_output=True,
        text=True,
    )
    assert result.returncode == 0, result.stderr


def main() -> int:
    tests = [value for name, value in sorted(globals().items()) if name.startswith("test_") and callable(value)]
    failures = 0
    for test in tests:
        try:
            test()
            print(f"ok  {test.__name__}")
        except AssertionError as err:
            failures += 1
            print(f"FAIL {test.__name__}: {err}")
    print(f"law coverage generator tests passed: {len(tests) - failures}/{len(tests)}")
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
