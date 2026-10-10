#!/usr/bin/env python3
"""Compose the knowledge manifest and the law-coverage record from their shards.

The shards under .concord/docs/knowledge/ are the committed authority (CD-0114). No
committed file lists every record, so two changes that each add a record never
touch the same line. Every reader composes the aggregate shape in memory
through this module: from the working tree, or from a git ref.

A ref that predates the shards carries the aggregate file instead. Reading it
is not a fallback around the shards; it is the shape that commit has.
"""

from __future__ import annotations

import importlib.util
import json
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
from io import BytesIO
from pathlib import Path
from types import ModuleType

SCRIPTS = Path(__file__).resolve().parent
ROOT = SCRIPTS.parent

KNOWLEDGE_ROOT = ".concord/docs/knowledge"
HEAD_PATH = ".concord/docs/knowledge/manifest.json"
DOMAIN_REGISTRY_PATH = ".concord/docs/knowledge/domain-registry.json"
RECORD_DIR = ".concord/docs/knowledge/records"
COVERAGE_DIR = ".concord/docs/knowledge/coverage"
# Layout tiers for reading history. A ref composes from the newest layout it
# carries; each tier is the shape that commit has, not a fallback around it.
# - current:  shards under .concord/docs/knowledge (CD-0194 placement law)
# - previous: shards under docs/knowledge (CD-0114 placement)
# - legacy:   one aggregate manifest file
PRE_MIGRATION_KNOWLEDGE_ROOT = "docs/knowledge"
PRE_MIGRATION_HEAD_PATH = "docs/knowledge/manifest.json"
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


def compose_manifest_bytes(root: Path = ROOT, record_path_re: re.Pattern[str] | None = None) -> bytes:
    """The manifest the shards under root compose, as canonical bytes.

    record_path_re selects the record path rule of a layout tier; None keeps
    the current authored rule.
    """
    findings: list[str] = []
    generator = _manifest_generator()
    if record_path_re is None:
        derived = generator.derive_aggregate(root, findings)
    else:
        derived = generator.derive_aggregate(root, findings, record_path_re=record_path_re)
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
    """The manifest at a git ref: composed from its shards in the newest
    layout the ref carries, or read from the aggregate file when the ref
    predates the shards. Each tier composes under the record path rule that
    tier's layout carries (CD-0194 D5)."""
    if ref_has_path(root, ref, HEAD_PATH):
        return _compose_manifest_from_ref_tree(root, ref, KNOWLEDGE_ROOT, None)
    if ref_has_path(root, ref, PRE_MIGRATION_HEAD_PATH):
        pre_migration = _manifest_generator().PRE_MIGRATION_RECORD_PATH_RE
        return _compose_manifest_from_ref_tree(root, ref, PRE_MIGRATION_KNOWLEDGE_ROOT, pre_migration)
    return json.loads(_git(root, "show", f"{ref}:{LEGACY_MANIFEST_PATH}"))


def _extract_ref_knowledge_tree(root: Path, ref: str, knowledge_root: str) -> tuple[Path, tempfile.TemporaryDirectory]:
    """Extract one ref's knowledge shard tree into a scratch directory laid
    out at the current shard paths, so the extractors read every layout tier
    with the current-layout code. Returns (tree, scratch); the caller holds
    the scratch context open while it reads the tree."""
    archive = _git(root, "archive", "--format=tar", ref, "--", knowledge_root)
    scratch = tempfile.TemporaryDirectory(prefix="concord-knowledge-")
    held = Path(scratch.name)
    with tarfile.open(fileobj=BytesIO(archive), mode="r:") as tar:
        tar.extractall(held, filter="data")
    if knowledge_root != KNOWLEDGE_ROOT:
        target = held / KNOWLEDGE_ROOT
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.move(str(held / knowledge_root), str(target))
    return held, scratch


def _compose_manifest_from_ref_tree(root: Path, ref: str, knowledge_root: str, record_path_re: re.Pattern[str] | None) -> dict:
    held, scratch = _extract_ref_knowledge_tree(root, ref, knowledge_root)
    with scratch:
        return json.loads(compose_manifest_bytes(held, record_path_re))


def _raw_manifest_from_ref_tree(root: Path, ref: str, knowledge_root: str) -> dict:
    held, scratch = _extract_ref_knowledge_tree(root, ref, knowledge_root)
    with scratch:
        return raw_manifest(held)


def manifest_paths_at(root: Path, ref: str) -> list[str]:
    """The committed paths that carry the manifest at a ref, for git queries
    that need a pathspec."""
    if ref_has_path(root, ref, HEAD_PATH):
        return [KNOWLEDGE_ROOT]
    if ref_has_path(root, ref, PRE_MIGRATION_HEAD_PATH):
        return [PRE_MIGRATION_KNOWLEDGE_ROOT]
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
    records = [_read_json(shard, findings) for shard in sorted((root / RECORD_DIR).glob("*.json"))]
    if findings:
        raise ComposeError(findings)
    manifest["records"] = records
    return manifest


def raw_manifest_at(root: Path, ref: str) -> dict:
    if ref_has_path(root, ref, HEAD_PATH):
        return _raw_manifest_from_ref_tree(root, ref, KNOWLEDGE_ROOT)
    if ref_has_path(root, ref, PRE_MIGRATION_HEAD_PATH):
        return _raw_manifest_from_ref_tree(root, ref, PRE_MIGRATION_KNOWLEDGE_ROOT)
    return json.loads(_git(root, "show", f"{ref}:{LEGACY_MANIFEST_PATH}"), object_pairs_hook=_reject_duplicate_pairs)


def _raw_manifest_from_ref_tree(root: Path, ref: str, knowledge_root: str) -> dict:
    held, scratch = _extract_ref_knowledge_tree(root, ref, knowledge_root)
    with scratch:
        return raw_manifest(held)
