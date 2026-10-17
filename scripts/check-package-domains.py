#!/usr/bin/env python3
"""Validate the package-to-Domain map against the code tree and current law.

CD-0222 D2 declares which Domains each code package serves in a Git-authored
map, .concord/docs/knowledge/package-domains.json. The map is architecture
data, not law, and the footprint read joins it with the law index. This
validator owns the propositions that keep it true:

  shape     schema_version "1.0"; packages sorted by path with unique,
            normalized relative directory paths and no glob characters; each
            entry names 1-16 sorted, unique Domain IDs; canonical shard bytes
  go        every Go package that `go list ./...` reports has an exact entry
  coverage  every tracked code file resolves to an entry, and every entry
            resolves at least one code file
  domains   every named Domain is a current Domain in the registry
  law       every child Domain that homes a current law whose evidence names a
            code file is listed on that file's entry

A path resolves to the entry with the longest directory prefix that contains
it. No Domain is required to appear in the map.
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
MAX_DOMAINS = 16
MAX_PACKAGES = 512
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
    if not isinstance(value, str) or not value or len(value) > 256 or GLOB.search(value):
        return False
    pure = PurePosixPath(value)
    return not pure.is_absolute() and ".." not in pure.parts and pure.as_posix() == value and value != "."


def validate_shape(raw: bytes) -> tuple[list[dict], list[str]]:
    findings: list[str] = []
    try:
        document = json.loads(raw)
    except json.JSONDecodeError as exc:
        return [], [f"{MAP_PATH}: not valid JSON: {exc}"]
    if not isinstance(document, dict) or set(document) != {"schema_version", "packages"}:
        return [], [f"{MAP_PATH}: the document must carry exactly schema_version and packages"]
    if document["schema_version"] != SCHEMA_VERSION:
        findings.append(f"{MAP_PATH}: schema_version must be {SCHEMA_VERSION!r}")
    packages = document["packages"]
    if not isinstance(packages, list) or not 1 <= len(packages) <= MAX_PACKAGES:
        return [], findings + [f"{MAP_PATH}: packages must list 1-{MAX_PACKAGES} entries"]
    entries: list[dict] = []
    for position, entry in enumerate(packages):
        anchor = f"{MAP_PATH} packages[{position}]"
        if not isinstance(entry, dict) or set(entry) != {"path", "domain_ids"}:
            findings.append(f"{anchor}: an entry carries exactly path and domain_ids")
            continue
        if not normalized_path(entry["path"]):
            findings.append(f"{anchor}: path {entry['path']!r} is not a normalized relative directory without globs")
            continue
        domain_ids = entry["domain_ids"]
        if (
            not isinstance(domain_ids, list)
            or not 1 <= len(domain_ids) <= MAX_DOMAINS
            or any(not isinstance(domain_id, str) or not domain_id for domain_id in domain_ids)
        ):
            findings.append(f"{anchor}: domain_ids must list 1-{MAX_DOMAINS} Domain IDs")
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
    entries, findings = validate_shape(map_file.read_bytes())
    if not entries:
        return findings
    try:
        manifest = knowledge_index.compose_manifest(root)
    except knowledge_index.ComposeError as exc:
        return findings + list(exc.findings)
    registry = manifest.get("domain_registry") or {}
    root_id = registry.get("root_domain_id")
    current = {domain.get("domain_id") for domain in registry.get("domains", []) if domain.get("status") == "current"}
    served = {entry["path"]: set(entry["domain_ids"]) for entry in entries}
    paths = list(served)

    for entry in entries:
        for domain_id in entry["domain_ids"]:
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

    for record in manifest.get("records", []):
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
