#!/usr/bin/env python3
"""Compose the knowledge manifest and the law-coverage record from their shards.

The shards under docs/knowledge/ are the committed authority (CD-0114). No
committed file lists every record, so two changes that each add a record never
touch the same line. Every reader composes the aggregate shape in memory
through this module: from the working tree, or from a git ref.

A ref that predates the shards carries the aggregate file instead. Reading it
is not a fallback around the shards; it is the shape that commit has.
"""

from __future__ import annotations

import importlib.util
import json
import subprocess
import sys
import tarfile
import tempfile
from io import BytesIO
from pathlib import Path
from types import ModuleType

SCRIPTS = Path(__file__).resolve().parent
ROOT = SCRIPTS.parent

KNOWLEDGE_ROOT = "docs/knowledge"
HEAD_PATH = "docs/knowledge/manifest.json"
DOMAIN_REGISTRY_PATH = "docs/knowledge/domain-registry.json"
RECORD_DIR = "docs/knowledge/records"
COVERAGE_DIR = "docs/knowledge/coverage"
LEGACY_MANIFEST_PATH = "docs/concord-knowledge-index.v1.json"
LEGACY_COVERAGE_PATH = "docs/law-coverage.v1.json"


class ComposeError(ValueError):
    """The shards do not compose: the findings name each defect."""

    def __init__(self, findings: list[str]) -> None:
        super().__init__("; ".join(findings[:8]))
        self.findings = findings


def _load_module(name: str, filename: str) -> ModuleType:
    spec = importlib.util.spec_from_file_location(name, SCRIPTS / filename)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"unable to load {filename}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


def _manifest_generator() -> ModuleType:
    return sys.modules.get("generate_knowledge_index") or _load_module("generate_knowledge_index", "generate-knowledge-index.py")


def _coverage_generator() -> ModuleType:
    return sys.modules.get("generate_law_coverage") or _load_module("generate_law_coverage", "generate-law-coverage.py")


def compose_manifest_bytes(root: Path = ROOT) -> bytes:
    """The manifest the shards under root compose, as canonical bytes."""
    findings: list[str] = []
    derived = _manifest_generator().derive_aggregate(root, findings)
    if derived is None:
        raise ComposeError(findings or ["knowledge shards did not compose"])
    return derived


def compose_manifest(root: Path = ROOT) -> dict:
    return json.loads(compose_manifest_bytes(root))


def compose_law_coverage_bytes(root: Path = ROOT) -> bytes:
    findings: list[str] = []
    derived = _coverage_generator().derive_aggregate(root, findings)
    if derived is None:
        raise ComposeError(findings or ["coverage shards did not compose"])
    return derived


def compose_law_coverage(root: Path = ROOT) -> dict:
    return json.loads(compose_law_coverage_bytes(root))


def _git(root: Path, *args: str) -> bytes:
    return subprocess.run(["git", "-C", str(root), *args], check=True, capture_output=True).stdout


def ref_has_path(root: Path, ref: str, path: str) -> bool:
    out = subprocess.run(["git", "-C", str(root), "cat-file", "-e", f"{ref}:{path}"], capture_output=True)
    return out.returncode == 0


def compose_manifest_at(root: Path, ref: str) -> dict:
    """The manifest at a git ref: composed from its shards, or read from the
    aggregate file when the ref predates the shards."""
    if not ref_has_path(root, ref, HEAD_PATH):
        return json.loads(_git(root, "show", f"{ref}:{LEGACY_MANIFEST_PATH}"))
    archive = _git(root, "archive", "--format=tar", ref, "--", KNOWLEDGE_ROOT)
    with tempfile.TemporaryDirectory(prefix="concord-knowledge-") as scratch:
        with tarfile.open(fileobj=BytesIO(archive), mode="r:") as tar:
            tar.extractall(scratch, filter="data")
        return compose_manifest(Path(scratch))


def manifest_paths_at(root: Path, ref: str) -> list[str]:
    """The committed paths that carry the manifest at a ref, for git queries
    that need a pathspec."""
    if ref_has_path(root, ref, HEAD_PATH):
        return [KNOWLEDGE_ROOT]
    return [LEGACY_MANIFEST_PATH]


class DuplicateKeyError(ValueError):
    pass


def _reject_duplicate_pairs(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise DuplicateKeyError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def _read_json(path: Path, findings: list[str]) -> object:
    try:
        return json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=_reject_duplicate_pairs)
    except (OSError, UnicodeDecodeError, json.JSONDecodeError, DuplicateKeyError) as exc:
        findings.append(f"{path}: invalid JSON: {exc}")
        return None


def raw_manifest(root: Path = ROOT) -> dict:
    """The manifest shape composed from the shards under root with no record
    validation: the head fields, the domain registry when present, and every
    record shard parsed as JSON, sorted by filename. Readers that need only
    identity fields use this; readers that need a valid manifest compose it."""
    findings: list[str] = []
    head = _read_json(root / HEAD_PATH, findings)
    if not isinstance(head, dict):
        if not findings:
            findings.append(f"{HEAD_PATH}: manifest head must be an object")
        raise ComposeError(findings)
    manifest = dict(head)
    registry_path = root / DOMAIN_REGISTRY_PATH
    if registry_path.is_file():
        manifest["domain_registry"] = _read_json(registry_path, findings)
    records: list[object] = []
    for shard in sorted((root / RECORD_DIR).glob("*.json")):
        records.append(_read_json(shard, findings))
    if findings:
        raise ComposeError(findings)
    manifest["records"] = records
    return manifest


def raw_manifest_at(root: Path, ref: str) -> dict:
    if not ref_has_path(root, ref, HEAD_PATH):
        return json.loads(_git(root, "show", f"{ref}:{LEGACY_MANIFEST_PATH}"), object_pairs_hook=_reject_duplicate_pairs)
    archive = _git(root, "archive", "--format=tar", ref, "--", KNOWLEDGE_ROOT)
    with tempfile.TemporaryDirectory(prefix="concord-knowledge-") as scratch:
        with tarfile.open(fileobj=BytesIO(archive), mode="r:") as tar:
            tar.extractall(scratch, filter="data")
        return raw_manifest(Path(scratch))
