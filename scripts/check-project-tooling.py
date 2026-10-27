#!/usr/bin/env python3
"""Validate a repository's declared tooling manifest.

The manifest (.concord/tooling.v1.json) is the per-project record of which
quality tools, scanners, and check commands are already set up. Intent fields
are hand-authored because they have no upstream source to drift from. The
checker proves that referenced files resolve to regular files inside the
repository. A missing manifest is a finding, not a vacuous pass. The checker
performs file reads only and never executes a declared tool
(CD-0074).
"""

from __future__ import annotations

import json
import re
import sys
from contextlib import contextmanager
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import evidence_anchors as anchors
MANIFEST = Path(".concord/tooling.v1.json")
MAX_FINDINGS = 100
SCHEMA_VERSION = "1.0"
MAX_TOOLS = 64
MAX_COVERAGE = 64
ANCHOR_KINDS = {"adapter_test", "generated", "go_test", "scenario", "validator", "go_package"}
CASE_PATTERN = r"^case:[A-Za-z0-9][A-Za-z0-9._:-]*$"
ANCHOR_VALUE_PATTERN = r"^\S(?:[^\u0000-\u001F\u007F]*\S)?$"
PATH_MIN_LENGTH = 3
PATH_MAX_LENGTH = 512

ROOT_FIELDS = {"schema_version", "project", "tools"}
OPTIONAL_ROOT_FIELDS = {"domain_shape"}
DOMAIN_SHAPE_COMMANDS = {"boundary_check", "data_object_extractor", "config_extractor", "reference_check"}
DOMAIN_SHAPE_FIELDS = DOMAIN_SHAPE_COMMANDS | {"required_checks"}
TOOL_FIELDS = {
    "id",
    "purpose",
    "invocation",
    "tier",
    "cost_hint",
    "config_path",
    "automation_path",
    "notes",
    "coverage",
}
REQUIRED_TOOL_FIELDS = {"id", "purpose", "invocation", "tier"}
TIERS = {"fast", "standard", "slow"}
IDENTIFIER_PATTERN = r"^(?!.*[\r\n])[a-z][a-z0-9-]{1,63}$"
SAFE_PATH_PATTERN = r"^(?!/)(?!.*//)(?!\.{1,2}(?:/|$))(?!.*\/\.{1,2}(?:/|$))[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+)*$"
RECIPE_PATH_PATTERN = r"^(?!tmp(?:/|$))" + SAFE_PATH_PATTERN[1:]
NON_WHITESPACE_PATTERN = r"[^ \t\r\n]"
SINGLE_LINE_PATTERN = r"^[^\u0000-\u001F\u007F]+$"
# CD-0192: the refine-exit proof compares a worktree_verify run's argv against
# the whitespace-split invocation, so the invocation must be plain argv.
# Shell quoting and shell operators would make a shell re-tokenize the
# command and break the exact equality. The pattern is byte-identical to the
# one contracts/project-tooling.v1.schema.json declares for invocation.
PLAIN_ARGV_PATTERN = r'''^[^'"`\\$;|&<>()]+$'''
TEXT_CONSTRAINTS = {
    "purpose": (4, 256, False),
    "invocation": (1, 512, True),
    "cost_hint": (4, 128, True),
    "notes": (4, 512, False),
}
JSON_WHITESPACE = {" ", "\t", "\r", "\n"}
IDENTIFIER = re.compile(IDENTIFIER_PATTERN)
SAFE_PATH = re.compile(SAFE_PATH_PATTERN)
PLAIN_ARGV = re.compile(PLAIN_ARGV_PATTERN)


def reject_duplicate_keys(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def bounded_text(
    path: str,
    key: str,
    value: object,
    minimum: int,
    maximum: int,
    findings: list[str],
    *,
    single_line: bool = False,
) -> None:
    if not isinstance(value, str):
        findings.append(f"{path}: {key} must be a string")
        return
    if not minimum <= len(value) <= maximum:
        findings.append(f"{path}: {key} length must be {minimum}..{maximum} characters")
    if all(character in JSON_WHITESPACE for character in value):
        findings.append(f"{path}: {key} must contain a character outside JSON whitespace")
    if single_line and any(ord(character) < 32 or ord(character) == 127 for character in value):
        findings.append(f"{path}: {key} must be a single line without control characters")


def inside_repository(root: Path, target: Path) -> bool:
    try:
        target.relative_to(root)
    except ValueError:
        return False
    return True


def validate_file_path(root: Path, label: str, key: str, value: object, findings: list[str]) -> None:
    if not isinstance(value, str) or not PATH_MIN_LENGTH <= len(value) <= PATH_MAX_LENGTH or not SAFE_PATH.fullmatch(value):
        findings.append(f"{label}: {key} must be a safe repository-relative path of {PATH_MIN_LENGTH}..{PATH_MAX_LENGTH} characters")
        return
    try:
        resolved = (root / value).resolve(strict=True)
    except OSError:
        findings.append(f"{label}: {key} does not resolve: {value}")
        return
    if not inside_repository(root.resolve(), resolved):
        findings.append(f"{label}: {key} resolves outside the repository: {value}")
    elif not resolved.is_file():
        findings.append(f"{label}: {key} must resolve to a regular file: {value}")


@contextmanager
def anchor_root(root: Path):
    previous = anchors.ROOT
    anchors.ROOT = root
    try:
        yield
    finally:
        anchors.ROOT = previous


def check(*, root: Path = ROOT) -> list[str]:
    with anchor_root(root):
        return check_document(root=root)


def check_document(*, root: Path) -> list[str]:
    findings: list[str] = []
    repository_root = root.resolve()
    manifest = root / MANIFEST
    try:
        resolved_manifest = manifest.resolve(strict=True)
    except OSError:
        findings.append(f"missing manifest: {MANIFEST} (a project declares its ready tooling there; deletion must be a visible decision, not a silent pass)")
        return findings
    if not inside_repository(repository_root, resolved_manifest):
        return [f"{MANIFEST}: manifest resolves outside the repository"]
    if not resolved_manifest.is_file():
        return [f"{MANIFEST}: manifest must resolve to a regular file"]

    try:
        raw_bytes = resolved_manifest.read_bytes()
        if len(raw_bytes) > 65536:
            return [f"{MANIFEST}: tooling manifest exceeds 65536 bytes"]
        raw = raw_bytes.decode("utf-8")
        document = json.loads(raw, object_pairs_hook=reject_duplicate_keys)
    except (OSError, json.JSONDecodeError, ValueError) as error:
        return [f"{MANIFEST}: {error}"]

    if not isinstance(document, dict):
        return [f"{MANIFEST}: top-level JSON value must be an object"]
    unknown_root = set(document) - ROOT_FIELDS - OPTIONAL_ROOT_FIELDS
    if unknown_root:
        findings.append(f"{MANIFEST}: unknown root field(s): {', '.join(sorted(unknown_root))}")
    missing_root = ROOT_FIELDS - set(document)
    if missing_root:
        findings.append(f"{MANIFEST}: missing root field(s): {', '.join(sorted(missing_root))}")
        return findings

    if document["schema_version"] != SCHEMA_VERSION:
        findings.append(f"{MANIFEST}: schema_version must be \"{SCHEMA_VERSION}\"")
    project = document["project"]
    if not isinstance(project, str) or not IDENTIFIER.fullmatch(project):
        findings.append(f"{MANIFEST}: project must match {IDENTIFIER.pattern}")

    tools = document["tools"]
    if not isinstance(tools, list):
        findings.append(f"{MANIFEST}: tools must be an array")
        return findings
    if not tools and "domain_shape" not in document:
        findings.append(f"{MANIFEST}: empty tools requires domain_shape declarations")
    if len(tools) > MAX_TOOLS:
        findings.append(f"{MANIFEST}: tools must hold at most {MAX_TOOLS} entries")

    seen_ids: set[str] = set()
    bindings: dict[tuple[str, str], tuple[str, str]] = {}
    for index, tool in enumerate(tools):
        path = f"{MANIFEST}: tools[{index}]"
        if not isinstance(tool, dict):
            findings.append(f"{path}: must be an object")
            continue
        identifier = tool.get("id", f"tools[{index}]")
        label = f"{MANIFEST}: {identifier}" if isinstance(tool.get("id"), str) else path
        unknown = set(tool) - TOOL_FIELDS
        if unknown:
            findings.append(f"{label}: unknown field(s): {', '.join(sorted(unknown))}")
        missing = REQUIRED_TOOL_FIELDS - set(tool)
        if missing:
            findings.append(f"{label}: missing field(s): {', '.join(sorted(missing))}")
            continue
        if not isinstance(tool["id"], str) or not IDENTIFIER.fullmatch(tool["id"]):
            findings.append(f"{label}: id must match {IDENTIFIER.pattern}")
        elif tool["id"] in seen_ids:
            findings.append(f"{label}: duplicate tool id")
        else:
            seen_ids.add(tool["id"])
        for key, (minimum, maximum, single_line) in TEXT_CONSTRAINTS.items():
            if key in tool:
                bounded_text(label, key, tool[key], minimum, maximum, findings, single_line=single_line)
        if "invocation" in tool and isinstance(tool["invocation"], str) and not PLAIN_ARGV.fullmatch(tool["invocation"]):
            findings.append(f"{label}: invocation must be plain argv without shell quoting or shell operators")
        tier = tool["tier"]
        if not isinstance(tier, str) or tier not in TIERS:
            findings.append(f"{label}: tier must be one of: {', '.join(sorted(TIERS))}")
        for key in ("config_path", "automation_path"):
            if key in tool:
                validate_file_path(root, label, key, tool[key], findings)
        if "coverage" in tool:
            validate_coverage(root, label, tool, bindings, findings)

    if "domain_shape" in document:
        shape = document["domain_shape"]
        label = f"{MANIFEST}: domain_shape"
        if not isinstance(shape, dict):
            findings.append(f"{label}: must be an object")
            return findings
        unknown = set(shape) - DOMAIN_SHAPE_FIELDS
        if unknown:
            findings.append(f"{label}: unknown field(s): {', '.join(sorted(unknown))}")
        for key in sorted(DOMAIN_SHAPE_COMMANDS):
            if key in shape:
                identifier = shape[key]
                if not isinstance(identifier, str) or not IDENTIFIER.fullmatch(identifier) or identifier not in seen_ids:
                    findings.append(f"{label}: {key} must reference a declared tool id")
        if "required_checks" in shape:
            checks = shape["required_checks"]
            if not isinstance(checks, list) or len(checks) > MAX_TOOLS:
                findings.append(f"{label}: required_checks must be an array of at most {MAX_TOOLS} declared tool ids")
            else:
                seen_checks: set[str] = set()
                for identifier in checks:
                    if not isinstance(identifier, str) or not IDENTIFIER.fullmatch(identifier) or identifier not in seen_ids or identifier in seen_checks:
                        findings.append(f"{label}: required_checks must reference distinct declared tool ids")
                    else:
                        seen_checks.add(identifier)

    return findings


def direct_command(invocation: object) -> bool:
    if (not isinstance(invocation, str) or not 1 <= len(invocation) <= 512
            or not re.fullmatch(SINGLE_LINE_PATTERN, invocation) or not PLAIN_ARGV.fullmatch(invocation)):
        return False
    args = invocation.split()
    if len(args) == 2 and args[0] == "python3":
        return args[1].startswith("scripts/") and args[1].endswith(".py") and bool(re.fullmatch(RECIPE_PATH_PATTERN, args[1]))
    if len(args) < 3 or args[0] not in {"go", "bun"} or args[1] != "test":
        return False
    inputs: set[str] = set()
    flags: set[str] = set()
    index = 2
    while index < len(args):
        arg = args[index]
        index += 1
        if arg.startswith("-"):
            flag, separator, value = arg.partition("=")
            if flag in flags:
                return False
            flags.add(flag)
            if args[0] == "go" and flag in {"-race", "-v", "-json"}:
                if separator:
                    return False
                continue
            if flag not in ({"-count", "-timeout", "-p", "-parallel"} if args[0] == "go" else {"--timeout"}):
                return False
            if not separator:
                if index >= len(args):
                    return False
                value = args[index]
                index += 1
            pattern = r"[1-9][0-9]{0,5}(ms|s|m|h)" if flag == "-timeout" else r"[1-9][0-9]{0,5}"
            if not re.fullmatch(pattern, value) or flag == "-count" and value != "1":
                return False
            continue
        if args[0] == "go":
            base = arg.removeprefix("./").removesuffix("/...")
            if arg not in {".", "./..."} and (not arg.startswith("./") or not re.fullmatch(RECIPE_PATH_PATTERN, base)):
                return False
        elif not arg.endswith(".test.ts") or not re.fullmatch(RECIPE_PATH_PATTERN, arg.removeprefix("./")):
            return False
        if arg in inputs:
            return False
        inputs.add(arg)
    return bool(inputs)


def validate_coverage(root: Path, label: str, tool: dict[str, object], bindings: dict[tuple[str, str], tuple[str, str]], findings: list[str]) -> None:
    coverage = tool["coverage"]
    if not isinstance(coverage, list) or len(coverage) > MAX_COVERAGE:
        findings.append(f"{label}: coverage must be an array of at most {MAX_COVERAGE} entries")
        return
    if coverage and not direct_command(tool.get("invocation")):
        findings.append(f"{label}: coverage requires a direct allowlisted command")
    seen: set[tuple[str, str]] = set()
    for entry in coverage:
        if not isinstance(entry, dict) or set(entry) != {"anchor", "recipe_path", "case_id"}:
            findings.append(f"{label}: coverage requires only anchor, recipe_path and case_id")
            continue
        anchor = entry["anchor"]
        if (not isinstance(anchor, dict) or set(anchor) != {"kind", "value"}
                or not isinstance(anchor["kind"], str) or anchor["kind"] not in ANCHOR_KINDS
                or not isinstance(anchor["value"], str) or not 1 <= len(anchor["value"]) <= 512
                or not re.fullmatch(ANCHOR_VALUE_PATTERN, anchor["value"])):
            findings.append(f"{label}: invalid coverage anchor")
            continue
        validate_file_path(root, label, "recipe_path", entry["recipe_path"], findings)
        if isinstance(entry["recipe_path"], str) and not re.fullmatch(RECIPE_PATH_PATTERN, entry["recipe_path"]):
            findings.append(f"{label}: recipe_path must name a normalized non-tmp repository source")
        case_id = entry["case_id"]
        if not isinstance(case_id, str) or len(case_id) > 128 or not re.fullmatch(CASE_PATTERN, case_id):
            findings.append(f"{label}: invalid coverage case_id")
            continue
        key = (anchor["kind"], anchor["value"])
        if key in seen:
            findings.append(f"{label}: duplicate coverage anchor")
        seen.add(key)
        relation = (entry["recipe_path"], case_id)
        if key in bindings and bindings[key] != relation:
            findings.append(f"{label}: conflicting coverage binding")
        bindings[key] = relation
        if (isinstance(entry["recipe_path"], str) and re.fullmatch(RECIPE_PATH_PATTERN, entry["recipe_path"])
                and direct_command(tool.get("invocation"))):
            try:
                recipe = selected_recipe(root, entry["recipe_path"], case_id)
                if not coverage_lineage(tool["invocation"], recipe) or not anchor_case(root, anchor, recipe, tool["invocation"]):
                    raise ValueError("typed anchor does not match the selected case/check owner")
            except (OSError, ValueError, KeyError, TypeError, anchors.GoTestProjectionError) as error:
                findings.append(f"{label}: coverage anchor {key!r} case {case_id}: {error}")


def source_file(root: Path, name: str) -> Path:
    if not re.fullmatch(RECIPE_PATH_PATTERN, name):
        raise ValueError("unsafe source path")
    target = root / name
    if target.is_symlink() or not target.is_file() or not inside_repository(root.resolve(), target.resolve()):
        raise ValueError(f"source is not a contained regular file: {name}")
    return target


def selected_recipe(root: Path, name: str, case_id: str) -> dict:
    raw = source_file(root, name).read_bytes()
    if len(raw) > 16384:
        raise ValueError("recipe exceeds 16384 bytes")
    recipe = json.loads(raw, object_pairs_hook=reject_duplicate_keys)
    if not isinstance(recipe, dict):
        raise ValueError("recipe must be an object")
    kind = recipe.get("kind")
    if kind in {"go_top_level_v1", "go_package_v1"}:
        allowed = {"kind", "package_cwd", "test_files", "fixture_files", "cases", "expected_skips"}
        cwd = recipe.get("package_cwd")
        files, fixtures = recipe.get("test_files", []), recipe.get("fixture_files", [])
    elif kind == "target_runner_v1":
        allowed = {"kind", "root", "materializer", "runner", "harness_files", "support_files", "cases", "checks", "pin"}
        cwd = recipe.get("root")
        files, fixtures = recipe.get("harness_files", []), recipe.get("support_files", [])
    else:
        raise ValueError("unsupported recipe kind")
    if set(recipe) - allowed or not isinstance(cwd, str) or cwd != "." and not re.fullmatch(RECIPE_PATH_PATTERN, cwd):
        raise ValueError("unsupported recipe fields or cwd")
    cases = recipe.get("cases")
    if not isinstance(cases, dict) or not 1 <= len(cases) <= 64 or case_id not in cases:
        raise ValueError("unknown recipe case")
    for identity, names in cases.items():
        if (not re.fullmatch(CASE_PATTERN, identity) or not isinstance(names, list) or not names
                or any(not isinstance(w, str) or not w or len(w.encode()) > 256 for w in names)
                or len(names) != len(set(names))):
            raise ValueError("invalid case witnesses")
    if (not isinstance(files, list) or not isinstance(fixtures, list) or len(files) + len(fixtures) > 4096
            or any(not isinstance(f, str) for f in files + fixtures)
            or len(set(files + fixtures)) != len(files + fixtures)):
        raise ValueError("invalid recipe input inventory")
    for file in files + fixtures:
        source_file(root, file)
    witnesses = cases[case_id]
    selected = []
    if kind == "go_top_level_v1":
        if not files or recipe.get("expected_skips") or len(witnesses) > 40 or any(not go_witness(w) for names in cases.values() for w in names):
            raise ValueError("invalid selected Go recipe")
    elif kind == "go_package_v1":
        package = "./" + cwd
        if files or fixtures or any(names != ["go_package:" + package] for names in cases.values()):
            raise ValueError("package recipe must cover its whole candidate inventory")
    else:
        checks = recipe.get("checks", [])
        if recipe.get("runner") != "check" or recipe.get("materializer") not in {"none", "uv", "bun"} or not isinstance(checks, list):
            raise ValueError("unsupported target check owner")
        seen = set()
        for check in checks:
            if (not isinstance(check, dict) or set(check) != {"witness", "argv"}
                    or not isinstance(check["witness"], str) or check["witness"] in seen
                    or not isinstance(check["argv"], list) or not 2 <= len(check["argv"]) <= 32
                    or any(not isinstance(arg, str) or not arg or len(arg.encode()) > 512 for arg in check["argv"])):
                raise ValueError("invalid target check")
            seen.add(check["witness"])
            if check["witness"] in witnesses:
                selected.append(check)
        if set(witnesses) != {check["witness"] for check in selected}:
            raise ValueError("case lacks selected check commands")
    return dict(recipe, cwd=cwd, files=files, fixtures=fixtures, witnesses=witnesses, selected=selected)


def go_witness(name: str) -> bool:
    parts = name.split("/")
    return (len(name.encode()) <= 256 and len(parts) <= 8 and len(parts[0]) <= 128
            and bool(re.fullmatch(r"Test[A-Za-z0-9_]+", parts[0]))
            and all(re.fullmatch(r"[A-Za-z0-9_.:-]+", p) for p in parts[1:]))


def adapter_command(recipe: dict, check: dict) -> tuple[list[str], list[str]] | None:
    args = check["argv"]
    if (recipe["kind"] != "target_runner_v1" or recipe.get("materializer") != "bun" or recipe.get("runner") != "check"
            or recipe["cwd"] != "adapter/opencode" or len(args) < 3 or args[:2] != ["python3", "acceptance.py"]
            or "adapter/opencode/acceptance.py" not in recipe["files"]):
        return None
    split = args.index("--expect") if "--expect" in args else len(args)
    files, expected = args[2:split], args[split + 1:]
    if (not files or len(set(files)) != len(files) or any(Path(f).name != f or not f.endswith(".test.ts") for f in files)
            or any("adapter/opencode/" + f not in recipe["files"] for f in files)
            or split < len(args) and not expected or len(set(expected)) != len(expected)
            or any(name.startswith("-") for name in expected)):
        return None
    return ["adapter/opencode/" + f for f in files], expected


def selected_python(recipe: dict, script: str, flags: list[str] | None = None) -> bool:
    if recipe["cwd"] != "." and not script.startswith(recipe["cwd"] + "/"):
        return False
    relative = script.removeprefix(recipe["cwd"] + "/") if recipe["cwd"] != "." else script
    return script in recipe["files"] and any(c["argv"] == ["python3", relative, *(flags or [])] for c in recipe["selected"])


def coverage_lineage(invocation: str, recipe: dict) -> bool:
    args = invocation.split()
    if args[0] == "go":
        if recipe["kind"] not in {"go_top_level_v1", "go_package_v1"}:
            return False
        cwd = recipe["cwd"]
        return any(arg == "./..." or arg == "./" + cwd or arg == "." and cwd == "."
                   or arg.endswith("/...") and (cwd == arg[2:-4] or cwd.startswith(arg[2:-4] + "/")) for arg in args[2:])
    if recipe["kind"] != "target_runner_v1":
        return False
    if args[0] == "python3":
        return selected_python(recipe, args[1])
    inputs = [arg.removeprefix("./") for arg in args[2:] if arg.endswith(".test.ts")]
    selected = [adapter_command(recipe, c) for c in recipe["selected"]]
    return bool(inputs) and all(any(c is not None and f in c[0] for c in selected) for f in inputs)


def anchor_case(root: Path, anchor: dict, recipe: dict, invocation: str) -> bool:
    kind, value = anchor["kind"], anchor["value"]
    args = invocation.split()
    adapter_inputs = [arg.removeprefix("./") for arg in args[2:] if arg.endswith(".test.ts")] if args[0] == "bun" else []
    if kind == "go_package":
        package = "./" + recipe["cwd"]
        return recipe["kind"] == "go_package_v1" and value == package and recipe["witnesses"] == ["go_package:" + package]
    if kind == "go_test":
        match = anchors.GO_TEST_ANCHOR.fullmatch(value)
        return bool(match and recipe["kind"] == "go_top_level_v1" and match["package"] == recipe["cwd"]
                    and match["test"] in recipe["witnesses"] and anchors.go_test_asserts(match["package"], match["test"]))
    if kind == "scenario":
        declaration = anchors.scenario_declarations().get(value)
        if declaration is None or value in anchors.deferred_scenarios() or declaration[0] not in recipe["fixtures"]:
            return False
        if recipe["kind"] == "go_top_level_v1":
            for witness in recipe["witnesses"]:
                if not go_witness(witness) or not witness.endswith("/" + value):
                    continue
                parent = witness.split("/", 1)[0]
                if not anchors.go_test_asserts(recipe["cwd"], parent):
                    continue
                for file in recipe["files"]:
                    tokens = anchors.source_tokens(source_file(root, file).read_text(encoding="utf-8"))
                    if any(tokens[i:i + 3] == ["func", parent, "("] for i in range(len(tokens) - 2)):
                        return True
            return False
        owner = anchors.scenario_adapter_test(value)
        return owner is not None and owner in adapter_inputs and any(c is not None and owner in c[0] and value in c[1]
                                         for c in (adapter_command(recipe, check) for check in recipe["selected"]))
    if kind == "adapter_test":
        match = anchors.ADAPTER_TEST_ANCHOR.fullmatch(value)
        if not match or "adapter/opencode/" + match["file"] not in adapter_inputs or not anchors.adapter_test_exists(match["file"], match["test"]):
            return False
        return any(c is not None and "adapter/opencode/" + match["file"] in c[0] and match["test"] in c[1]
                   for c in (adapter_command(recipe, check) for check in recipe["selected"]))
    if kind == "validator":
        return bool(args == ["python3", value] and anchors.VALIDATOR_ANCHOR.fullmatch(value) and selected_python(recipe, value))
    if kind == "generated":
        match = anchors.GENERATED_ANCHOR.fullmatch(value)
        if not match or not anchors.generated_symbol_exists(match["file"], match["symbol"]):
            return False
        text = source_file(root, match["file"]).read_text(encoding="utf-8")
        owner = re.search(r"(?m)^// Code generated by (scripts/generate-[a-z0-9-]+\.py); DO NOT EDIT\.", text)
        if not owner:
            return False
        generator = owner[1]
        parity = "scripts/test-" + generator.removeprefix("scripts/")
        if args != ["python3", parity] or not selected_python(recipe, generator, ["--check"]) or not selected_python(recipe, parity):
            return False
        generator_text = source_file(root, generator).read_text(encoding="utf-8")
        parity_text = source_file(root, parity).read_text(encoding="utf-8")
        return ('"' + match["file"] + '"' in generator_text and match["symbol"] in generator_text
                and '"' + generator + '"' in parity_text)
    return False


def main() -> int:
    findings = check()
    for finding in findings[:MAX_FINDINGS]:
        print(finding)
    if len(findings) > MAX_FINDINGS:
        print(f"... {len(findings) - MAX_FINDINGS} additional finding(s) omitted")
    if findings:
        print(f"project tooling validation failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("project tooling validation passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
