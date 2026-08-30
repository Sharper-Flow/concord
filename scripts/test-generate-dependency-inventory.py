#!/usr/bin/env python3
"""Tests for the CD-0014 dependency inventory generator."""
from __future__ import annotations

import json
import hashlib
import os
import subprocess
import sys
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts/generate-dependency-inventory.py"


def build_fixture() -> tuple[Path, dict[str, object], str, tempfile.TemporaryDirectory[str]]:
    directory = tempfile.TemporaryDirectory()
    root = Path(directory.name)
    (root / "docs/decisions").mkdir(parents=True)
    (root / "docs/knowledge/records").mkdir(parents=True)
    (root / "scripts").mkdir()
    (root / "bin").mkdir()
    module_dir = root / "module-cache"
    module_dir.mkdir()
    license_bytes = b"MIT License\nPermission is hereby granted, free of charge, to any person obtaining a copy.\n"
    (module_dir / "LICENSE").write_bytes(license_bytes)
    module = "example.com/widget"
    inventory = {
        "schema": "concord.bubbletea-dependency-inventory.v1",
        "package": "./internal/launcher/render/bubbletea",
        "runtime": [{
            "module": module,
            "version": "v1.0.0",
            "role": "runtime direct",
            "license": [{
                "file": "LICENSE",
                "family": "MIT",
                "sha256": hashlib.sha256(license_bytes).hexdigest(),
            }],
        }],
        "test_only": [],
        "module_graph_only": [],
    }
    (root / "docs/decisions/CD-0014-terminal-launcher-dependencies.v1.json").write_bytes(
        (json.dumps(inventory, indent=2) + "\n").encode()
    )
    (root / "docs/decisions/CD-0014-terminal-launcher-rendering.md").write_text(
        f"- `{module} v1.0.0`\nIts artifact SHA-256 is `{'0' * 64}`.\n", encoding="utf-8"
    )
    shard = {"id": "CD-0014", "sha256": "sha256:" + "0" * 64}
    (root / "docs/knowledge/records/CD-0014.json").write_text(json.dumps(shard, indent=2) + "\n", encoding="utf-8")
    metadata = {"Path": module, "Version": "v2.0.0", "Dir": str(module_dir)}
    (root / "bin/go").write_text(f"#!/usr/bin/env python3\nimport json\nprint(json.dumps({metadata!r}))\n", encoding="utf-8")
    (root / "bin/go").chmod(0o755)
    for name in ("generate-knowledge-index.py", "generate-law-coverage.py"):
        (root / "scripts" / name).write_text("raise SystemExit(0)\n", encoding="utf-8")
    return root, inventory, module, directory


def run_generator(root: Path, *args: str) -> subprocess.CompletedProcess[str]:
    environment = os.environ.copy()
    environment["PATH"] = str(root / "bin") + os.pathsep + environment["PATH"]
    return subprocess.run(
        [sys.executable, str(SCRIPT), *args, "--root", str(root)],
        cwd=ROOT,
        env=environment,
        capture_output=True,
        text=True,
    )


def test_update_applies_version_bump() -> None:
    root, _, module, directory = build_fixture()
    try:
        result = run_generator(root, "--update")
        assert result.returncode == 0, result.stderr
        inventory = json.loads((root / "docs/decisions/CD-0014-terminal-launcher-dependencies.v1.json").read_text())
        assert inventory["runtime"][0]["version"] == "v2.0.0"
        decision = (root / "docs/decisions/CD-0014-terminal-launcher-rendering.md").read_text()
        assert f"`{module} v2.0.0`" in decision
    finally:
        directory.cleanup()


def test_check_names_version_drift() -> None:
    root, _, module, directory = build_fixture()
    try:
        assert run_generator(root, "--update").returncode == 0
        path = root / "docs/decisions/CD-0014-terminal-launcher-dependencies.v1.json"
        value = json.loads(path.read_text())
        value["runtime"][0]["version"] = "v1.0.0"
        path.write_text(json.dumps(value, indent=2) + "\n")
        result = run_generator(root, "--check")
        assert result.returncode == 1
        assert module in result.stderr
    finally:
        directory.cleanup()


def test_update_refuses_license_hash_drift_without_writing() -> None:
    root, _, module, directory = build_fixture()
    try:
        assert run_generator(root, "--update").returncode == 0
        inventory_path = root / "docs/decisions/CD-0014-terminal-launcher-dependencies.v1.json"
        value = json.loads(inventory_path.read_text())
        value["runtime"][0]["license"][0]["sha256"] = "f" * 64
        inventory_path.write_text(json.dumps(value, indent=2) + "\n")
        before = {path: path.read_bytes() for path in (
            inventory_path,
            root / "docs/decisions/CD-0014-terminal-launcher-rendering.md",
            root / "docs/knowledge/records/CD-0014.json",
        )}
        result = run_generator(root, "--update")
        assert result.returncode == 1
        assert module in result.stderr and "sha256" in result.stderr
        assert all(path.read_bytes() == content for path, content in before.items())
    finally:
        directory.cleanup()


def main() -> int:
    tests = [value for name, value in sorted(globals().items()) if name.startswith("test_") and callable(value)]
    failures = 0
    for test in tests:
        try:
            test()
            print(f"ok  {test.__name__}")
        except AssertionError as error:
            failures += 1
            print(f"FAIL {test.__name__}: {error}")
    print(f"dependency inventory generator tests passed: {len(tests) - failures}/{len(tests)}")
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
