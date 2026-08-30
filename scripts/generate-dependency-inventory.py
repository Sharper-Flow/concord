#!/usr/bin/env python3
"""Generate the reviewed CD-0014 dependency inventory projections.

The inventory records a version and license evidence per module, and
`internal/launcher/dependency_inventory_test.go` checks it against the derived
module closure. Dependabot moves `go.mod` but cannot move reviewed evidence, so
a hand-maintained inventory makes every Charm bump red on arrival.

This generator owns the derived facts only: the `version` field, and the
projections that repeat it — the artifact hash and the direct-dependency lines
in the decision, and the knowledge shard digest over that decision. Which
modules are permitted remains a reviewed decision.

It refuses rather than rewrites whenever a module leaves the build list or a
license file changes, because those are review decisions and must fail loudly.
License family is not recomputed here: family is a pure function of the license
text, so an unchanged `sha256` already proves an unchanged family, and
`licenseFamily` in the launcher test owns that classification.
"""
from __future__ import annotations

import argparse
import copy
import hashlib
import json
import os
import re
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
INVENTORY = Path("docs/decisions/CD-0014-terminal-launcher-dependencies.v1.json")
DECISION = Path("docs/decisions/CD-0014-terminal-launcher-rendering.md")
KNOWLEDGE_SHARD = Path("docs/knowledge/records/CD-0014.json")
GROUPS = ("runtime", "test_only", "module_graph_only")


def load_json(path: Path) -> object:
    return json.loads(path.read_text(encoding="utf-8"))


def format_json(value: object) -> bytes:
    return (json.dumps(value, ensure_ascii=False, indent=2) + "\n").encode("utf-8")


def parse_go_list(output: str) -> dict[str, dict[str, str]]:
    decoder = json.JSONDecoder()
    modules: dict[str, dict[str, str]] = {}
    position = 0
    while position < len(output):
        while position < len(output) and output[position].isspace():
            position += 1
        if position == len(output):
            break
        value, position = decoder.raw_decode(output, position)
        if not isinstance(value, dict):
            raise ValueError("go list returned a non-object module record")
        path = value.get("Path")
        if isinstance(path, str) and path:
            modules[path] = {
                key: value[key]
                for key in ("Path", "Version", "Dir")
                if isinstance(value.get(key), str)
            }
    return modules


def module_build_list(root: Path) -> dict[str, dict[str, str]]:
    environment = os.environ.copy()
    environment.update({"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off"})
    result = subprocess.run(
        ["go", "list", "-m", "-json", "all"],
        cwd=root,
        env=environment,
        capture_output=True,
        text=True,
    )
    if result.returncode:
        detail = result.stderr.strip() or result.stdout.strip() or "unknown go list failure"
        raise ValueError(f"offline go list failed: {detail}")
    return parse_go_list(result.stdout)


def evidence_path(root: Path, metadata: dict[str, str], evidence: dict[str, object]) -> Path | None:
    file_name = evidence.get("file")
    if not isinstance(file_name, str) or not file_name:
        return None
    relative = Path(file_name)
    if relative.is_absolute() or ".." in relative.parts:
        return None
    source = evidence.get("source") or "module-cache"
    if source == "module-cache":
        module_dir = metadata.get("Dir")
        if not module_dir:
            return None
        base = Path(module_dir)
    elif source == "repository":
        base = root
    else:
        return None
    candidate = (base / relative).resolve()
    resolved_base = base.resolve()
    if candidate != resolved_base and resolved_base not in candidate.parents:
        return None
    return candidate


def derive_inventory(
    root: Path,
    inventory: dict[str, object],
    modules: dict[str, dict[str, str]],
    report_version_drift: bool = False,
) -> tuple[bytes | None, list[str]]:
    derived = copy.deepcopy(inventory)
    findings: list[str] = []
    for group in GROUPS:
        entries = inventory.get(group)
        if not isinstance(entries, list):
            findings.append(f"inventory group {group} is not an array")
            continue
        derived_entries = derived[group]
        if not isinstance(derived_entries, list):
            continue
        for index, entry in enumerate(entries):
            if not isinstance(entry, dict):
                findings.append(f"inventory {group}[{index}] is not an object")
                continue
            module = entry.get("module")
            if not isinstance(module, str) or not module:
                findings.append(f"inventory {group}[{index}] has no module")
                continue
            metadata = modules.get(module)
            if metadata is None:
                findings.append(f"module {module}: absent from the build list")
                continue
            if not metadata.get("Dir"):
                findings.append(f"module {module}: build list has no Dir")
                continue
            version = metadata.get("Version")
            if not version:
                findings.append(f"module {module}: build list has no Version")
                continue
            if report_version_drift and entry.get("version") != version:
                findings.append(
                    f"module {module}: version differs: recorded={entry.get('version')!r} derived={version}"
                )
            derived_entries[index]["version"] = version
            license_entries = entry.get("license")
            if not isinstance(license_entries, list) or not license_entries:
                findings.append(f"module {module}: license evidence is empty")
                continue
            for license_index, evidence in enumerate(license_entries):
                if not isinstance(evidence, dict):
                    findings.append(f"module {module}: license evidence {license_index} is not an object")
                    continue
                path = evidence_path(root, metadata, evidence)
                if path is None:
                    findings.append(f"module {module}: license file path is invalid")
                    continue
                try:
                    content = path.read_bytes()
                except OSError:
                    findings.append(f"module {module}: license file is missing: {evidence.get('file')}")
                    continue
                actual_hash = hashlib.sha256(content).hexdigest()
                if evidence.get("sha256") != actual_hash:
                    findings.append(
                        f"module {module}: license sha256 differs: recorded={evidence.get('sha256')!r} derived={actual_hash}"
                    )
    if findings:
        return None, findings
    return format_json(derived), findings


def update_decision(text: str, inventory: dict[str, object], artifact_hash: str) -> tuple[bytes | None, list[str]]:
    updated = text
    findings: list[str] = []
    runtime = inventory.get("runtime")
    if not isinstance(runtime, list):
        findings.append("inventory runtime group is not an array")
    else:
        for entry in runtime:
            if not isinstance(entry, dict) or entry.get("role") != "runtime direct":
                continue
            module = entry.get("module")
            version = entry.get("version")
            if not isinstance(module, str) or not isinstance(version, str):
                findings.append("runtime direct inventory entry has no module or version")
                continue
            pattern = re.compile(r"`" + re.escape(module) + r" [^`]+`")
            updated, replacements = pattern.subn(f"`{module} {version}`", updated)
            if replacements == 0:
                findings.append(f"decision omits runtime direct module {module}")
    artifact_pattern = re.compile(r"(Its artifact SHA-256 is `)[0-9a-f]{64}(`\.)")
    updated, replacements = artifact_pattern.subn(rf"\g<1>{artifact_hash}\g<2>", updated)
    if replacements == 0:
        findings.append("decision has no artifact SHA-256 field")
    if findings:
        return None, findings
    return updated.encode("utf-8"), findings


def update_knowledge_shard(root: Path, decision: bytes) -> tuple[bytes | None, list[str]]:
    findings: list[str] = []
    try:
        shard = load_json(root / KNOWLEDGE_SHARD)
    except (OSError, json.JSONDecodeError) as exc:
        return None, [f"knowledge shard cannot be read: {exc}"]
    if not isinstance(shard, dict):
        return None, ["knowledge shard is not an object"]
    shard["sha256"] = "sha256:" + hashlib.sha256(decision).hexdigest()
    return format_json(shard), findings


def cascade(root: Path, flag: str) -> list[str]:
    findings: list[str] = []
    for script in ("generate-knowledge-index.py", "generate-law-coverage.py"):
        path = root / "scripts" / script
        result = subprocess.run(
            [sys.executable, str(path), flag],
            cwd=root,
            capture_output=True,
            text=True,
        )
        if result.returncode:
            detail = result.stderr.strip() or result.stdout.strip() or "unknown generator failure"
            findings.append(f"{script} {flag} failed: {detail}")
    return findings


def derive_outputs(root: Path, report_version_drift: bool = False) -> tuple[dict[Path, bytes] | None, list[str]]:
    findings: list[str] = []
    try:
        inventory = load_json(root / INVENTORY)
    except (OSError, json.JSONDecodeError) as exc:
        return None, [f"dependency inventory cannot be read: {exc}"]
    if not isinstance(inventory, dict):
        return None, ["dependency inventory is not an object"]
    try:
        modules = module_build_list(root)
    except (OSError, ValueError) as exc:
        return None, [str(exc)]
    inventory_bytes, inventory_findings = derive_inventory(root, inventory, modules, report_version_drift)
    findings.extend(inventory_findings)
    if inventory_bytes is None:
        return None, findings
    try:
        decision_text = (root / DECISION).read_text(encoding="utf-8")
    except OSError as exc:
        return None, [f"decision cannot be read: {exc}"]
    artifact_hash = hashlib.sha256(inventory_bytes).hexdigest()
    decision_bytes, decision_findings = update_decision(decision_text, json.loads(inventory_bytes), artifact_hash)
    findings.extend(decision_findings)
    if decision_bytes is None:
        return None, findings
    shard_bytes, shard_findings = update_knowledge_shard(root, decision_bytes)
    findings.extend(shard_findings)
    if shard_bytes is None:
        return None, findings
    return {
        root / INVENTORY: inventory_bytes,
        root / DECISION: decision_bytes,
        root / KNOWLEDGE_SHARD: shard_bytes,
    }, findings


def compare_outputs(expected: dict[Path, bytes], root: Path) -> list[str]:
    findings: list[str] = []
    for path, content in expected.items():
        try:
            actual = path.read_bytes()
        except OSError:
            findings.append(f"generated artifact missing: {path.relative_to(root)}")
            continue
        if actual != content:
            findings.append(f"generated artifact drift: {path.relative_to(root)}")
    return findings


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true", help="check derived dependency evidence without writing")
    parser.add_argument("--update", action="store_true", help="write derived dependency evidence")
    parser.add_argument("--root", type=Path, default=ROOT, help="repository root")
    args = parser.parse_args()
    if args.check == args.update:
        parser.error("pass exactly one of --check or --update")
    root = args.root.resolve()
    try:
        expected, findings = derive_outputs(root, args.check)
        if expected is None:
            for finding in findings:
                print(finding, file=sys.stderr)
            print(f"dependency inventory generation failed: {len(findings)} finding(s)", file=sys.stderr)
            return 1
        if args.check:
            findings.extend(compare_outputs(expected, root))
            findings.extend(cascade(root, "--check"))
            if findings:
                for finding in findings:
                    print(finding, file=sys.stderr)
                print(f"dependency inventory check failed: {len(findings)} finding(s)", file=sys.stderr)
                return 1
            print("dependency inventory is up to date")
            return 0
        for path, content in expected.items():
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(content)
        findings.extend(cascade(root, "--update"))
        if findings:
            for finding in findings:
                print(finding, file=sys.stderr)
            print(f"dependency inventory generation failed: {len(findings)} finding(s)", file=sys.stderr)
            return 1
        print("dependency inventory updated")
        return 0
    except (OSError, json.JSONDecodeError, ValueError) as exc:
        print(f"dependency inventory generation failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
