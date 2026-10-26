#!/usr/bin/env python3
"""Validate package-grain roots against the code tree and current Domains.

Schema 1.1 declares domain, mixed, composition and test_support roots under
CD-0244 D2. Schema 1.0 retains historical evidence-to-Domain validation.
Both versions require canonical JSON within 16 KiB, current Domain references,
real contained directories, exact Go package entries and tracked code coverage.
A path resolves through the longest containing directory prefix. Schema 1.1
permits an empty map only when there is no declared code to cover.
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path, PurePosixPath

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import knowledge_index  # noqa: E402
import shard_format  # noqa: E402

MAP_PATH = ".concord/docs/knowledge/package-domains.json"
SCHEMA_VERSION = "1.0"
SHAPE_VERSION = "1.1"
MAX_DOMAINS = 16
MAX_PACKAGES = 512
# The store reads every file under the knowledge root through one bounded
# archive and refuses a member above this size (maxKnowledgeRecord).
MAX_MAP_BYTES = 16 * 1024
CODE_SUFFIXES = (".go", ".ts", ".js", ".py", ".sh")
CODE_ROOTS = ("bin/",)
GLOB = re.compile(r"[*?\[\]{}]")


def is_code_file(path: str) -> bool:
    return path.endswith(CODE_SUFFIXES) or path.startswith(CODE_ROOTS)


def tracked_files(root: Path) -> list[str]:
    out = subprocess.run(["git", "-C", str(root), "ls-files", "-z"], check=True, capture_output=True).stdout
    return [name for name in out.decode("utf-8").split("\0") if name]


def go_packages(root: Path) -> list[str]:
    out = subprocess.run(["go", "list", "-f", "{{.Dir}}", "./..."], cwd=root, check=True, capture_output=True, text=True).stdout
    packages = []
    for line in out.splitlines():
        if line.strip():
            packages.append(Path(line.strip()).resolve().relative_to(root.resolve()).as_posix())
    return packages


def resolve(path: str, entries: list[str]) -> str | None:
    best = None
    for entry in entries:
        if path.startswith(entry + "/") and (best is None or len(entry) > len(best)):
            best = entry
    return best


def normalized_path(value: object) -> bool:
    if not isinstance(value, str) or not value:
        return False
    try:
        size = len(value.encode("utf-8"))
    except UnicodeEncodeError:
        return False
    if size > 256 or GLOB.search(value) or any(char == "\\" or ord(char) < 32 for char in value):
        return False
    pure = PurePosixPath(value)
    return not pure.is_absolute() and ".." not in pure.parts and pure.as_posix() == value and value != "."


def unique_object(pairs: list[tuple]) -> dict:
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate key: {key}")
        result[key] = value
    return result


def entry_domains(entry: dict) -> list[str]:
    if entry.get("role") == "domain":
        return [entry["owner_domain_id"]]
    return entry.get("domain_ids", [])


def validate_shape(raw: bytes) -> tuple[list[dict], list[str]]:
    findings: list[str] = []
    if len(raw) > MAX_MAP_BYTES:
        return [], [f"{MAP_PATH}: {len(raw)} bytes exceeds the {MAX_MAP_BYTES}-byte knowledge shard bound"]
    try:
        document = json.loads(raw, object_pairs_hook=unique_object)
    except (ValueError, UnicodeDecodeError) as exc:
        return [], [f"{MAP_PATH}: not valid JSON: {exc}"]
    if not isinstance(document, dict) or set(document) != {"schema_version", "packages"}:
        return [], [f"{MAP_PATH}: the document must carry exactly schema_version and packages"]
    version = document["schema_version"]
    if version not in (SCHEMA_VERSION, SHAPE_VERSION):
        return [], [f"{MAP_PATH}: schema_version must be '1.0' or '1.1'"]
    packages = document["packages"]
    minimum = 0 if version == SHAPE_VERSION else 1
    if not isinstance(packages, list) or not minimum <= len(packages) <= MAX_PACKAGES:
        return [], findings + [f"{MAP_PATH}: packages must list {minimum}-{MAX_PACKAGES} entries"]
    entries: list[dict] = []
    for position, entry in enumerate(packages):
        anchor = f"{MAP_PATH} packages[{position}]"
        if not isinstance(entry, dict):
            findings.append(f"{anchor}: an entry must be an object")
            continue
        expected = {"path", "domain_ids"}
        minimum_domains = 1
        if version == SHAPE_VERSION:
            role = entry.get("role")
            if role == "domain":
                expected = {"path", "role", "owner_domain_id"}
            elif role == "mixed":
                expected = {"path", "role", "domain_ids"}
                minimum_domains = 2
            elif role in ("composition", "test_support"):
                expected = {"path", "role"}
                minimum_domains = 0
            else:
                findings.append(f"{anchor}: requires an explicit domain, mixed, composition or test_support role")
                continue
        if set(entry) != expected:
            findings.append(f"{anchor}: entry fields must be exactly {', '.join(sorted(expected))}")
            continue
        if not normalized_path(entry["path"]):
            findings.append(f"{anchor}: path {entry['path']!r} is not a normalized relative directory without globs")
            continue
        domain_ids = entry_domains(entry)
        if (
            not isinstance(domain_ids, list)
            or not minimum_domains <= len(domain_ids) <= MAX_DOMAINS
            or any(not isinstance(domain_id, str) or not domain_id for domain_id in domain_ids)
        ):
            findings.append(f"{anchor}: domain_ids must list {minimum_domains}-{MAX_DOMAINS} Domain IDs (domain role requires one owner_domain_id)")
            continue
        if domain_ids != sorted(set(domain_ids)):
            findings.append(f"{anchor}: domain_ids must be sorted and unique")
        entries.append(entry)
    paths = [entry["path"] for entry in entries]
    if len(set(paths)) != len(paths):
        findings.append(f"{MAP_PATH}: package paths must be unique")
    if paths != sorted(paths):
        findings.append(f"{MAP_PATH}: packages must be sorted by path")
    if not findings and shard_format.canonical_bytes(document) != raw:
        findings.append(f"{MAP_PATH}: bytes are not canonical; rewrite with sorted keys and two-space indent")
    return entries, findings


def validate(root: Path, files: list[str], packages: list[str]) -> list[str]:
    map_file = root / MAP_PATH
    if not map_file.is_file():
        return [f"{MAP_PATH}: missing"]
    raw = map_file.read_bytes()
    entries, findings = validate_shape(raw)
    if findings:
        return findings
    version = json.loads(raw)["schema_version"]
    try:
        manifest = knowledge_index.compose_manifest(root)
    except knowledge_index.ComposeError as exc:
        return findings + list(exc.findings)
    registry = manifest.get("domain_registry") or {}
    root_id = registry.get("root_domain_id")
    current = {domain.get("domain_id") for domain in registry.get("domains", []) if domain.get("status") == "current"}
    served = {entry["path"]: set(entry_domains(entry)) for entry in entries}
    paths = list(served)

    for entry in entries:
        directory = root / entry["path"]
        try:
            contained = directory.resolve().is_relative_to(root.resolve())
            exists = directory.is_dir()
        except (OSError, RuntimeError):
            contained, exists = False, False
        if not contained or not exists:
            findings.append(f"package {entry['path']}: must resolve to an existing repository-contained directory/module root")
        for domain_id in entry_domains(entry):
            if domain_id not in current:
                findings.append(f"package {entry['path']}: {domain_id!r} is not a current registry Domain")

    for package in sorted(set(packages) - set(paths)):
        findings.append(f"Go package {package}: has no package-map entry")

    resolved: set[str] = set()
    for name in sorted(files):
        if not is_code_file(name):
            continue
        package = resolve(name, paths)
        if package is None:
            findings.append(f"code file {name}: resolves to no package-map entry")
        else:
            resolved.add(package)
    for package in sorted(set(paths) - resolved):
        findings.append(f"package {package}: resolves no tracked code file")

    # CD-0244 D2 replaces evidence-based multi-Domain ownership. Historical
    # maps retain their validation until explicitly moved to schema 1.1.
    for record in manifest.get("records", []) if version == SCHEMA_VERSION else []:
        if not isinstance(record, dict) or record.get("status") != "accepted":
            continue
        home = record.get("home_domain_id")
        if not home or home == root_id:
            continue
        for evidence in record.get("evidence", []):
            if not isinstance(evidence, str) or not is_code_file(evidence):
                continue
            package = resolve(evidence, paths)
            if package is not None and home not in served[package]:
                findings.append(
                    f"package {package}: law {record.get('id')} homed in {home!r} names {evidence}, "
                    f"so the entry must list {home!r}"
                )
    return findings


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", type=Path, default=ROOT, help="repository root")
    args = parser.parse_args()
    root = args.root.resolve()
    findings = validate(root, tracked_files(root), go_packages(root))
    if findings:
        print(f"package-domain map check failed: {len(findings)} finding(s)")
        for finding in findings:
            print(f"  {finding}")
        return 1
    print("package-domain map check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
