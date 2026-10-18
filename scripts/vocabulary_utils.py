"""Shared JSON Schema, canonical JSON, generator, and checker helpers for vocabularies."""
from __future__ import annotations

import argparse
import hashlib
import json
import re
import subprocess
import sys
from collections.abc import Callable
from pathlib import Path


SCHEMA_KEYWORDS = {
    "$schema",
    "$id",
    "$defs",
    "$ref",
    "title",
    "description",
    "type",
    "properties",
    "required",
    "additionalProperties",
    "items",
    "minItems",
    "maxItems",
    "uniqueItems",
    "minLength",
    "maxLength",
    "pattern",
    "enum",
    "const",
    "oneOf",
    "allOf",
    "not",
    "if",
    "then",
    "else",
}


def canonical(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()


def digest(value: object) -> str:
    return "sha256:" + hashlib.sha256(canonical(value)).hexdigest()


def fail(message: str) -> None:
    raise ValueError(message)


def schema_validate(value, schema, root, path="$"):
    if not isinstance(schema, dict):
        fail(f"schema node is not an object at {path}")
    unsupported = set(schema) - SCHEMA_KEYWORDS
    if unsupported:
        fail(f"unsupported JSON Schema keywords at {path}: {sorted(unsupported)}")
    if "$ref" in schema:
        ref = schema["$ref"]
        if not ref.startswith("#/$defs/"):
            fail(f"non-local schema ref {ref}")
        target = root.get("$defs", {}).get(ref.removeprefix("#/$defs/"))
        if target is None:
            fail(f"missing schema ref {ref}")
        return schema_validate(value, target, root, path)
    if "const" in schema and value != schema["const"]:
        fail(f"const mismatch at {path}")
    if "enum" in schema and value not in schema["enum"]:
        accepted = json.dumps(schema["enum"], ensure_ascii=False, separators=(",", ":"))
        fail(f"enum mismatch at {path}: accepted values are {accepted}")
    types = schema.get("type")
    if types is not None:
        types = types if isinstance(types, list) else [types]

        def is_type(kind):
            return {
                "object": isinstance(value, dict),
                "array": isinstance(value, list),
                "string": isinstance(value, str),
                "integer": isinstance(value, int) and not isinstance(value, bool),
                "number": isinstance(value, (int, float)) and not isinstance(value, bool),
                "boolean": isinstance(value, bool),
                "null": value is None,
            }.get(kind, False)

        if not any(is_type(kind) for kind in types):
            fail(f"type mismatch at {path}")
    evaluated = set()
    if isinstance(value, dict):
        required = schema.get("required", [])
        for key in required:
            if key not in value:
                fail(f"missing required {path}.{key}")
        properties = schema.get("properties", {})
        for key, child in properties.items():
            if key in value:
                schema_validate(value[key], child, root, f"{path}.{key}")
                evaluated.add(key)
        additional = set(value) - evaluated
        if schema.get("additionalProperties") is False and additional:
            fail(f"unknown properties at {path}: {sorted(additional)}")
    if isinstance(value, list):
        if "minItems" in schema and len(value) < schema["minItems"]:
            fail(f"minItems at {path}")
        if "maxItems" in schema and len(value) > schema["maxItems"]:
            fail(f"maxItems at {path}")
        if schema.get("uniqueItems") and len({json.dumps(item, sort_keys=True) for item in value}) != len(value):
            fail(f"uniqueItems at {path}")
        if "items" in schema:
            for index, item in enumerate(value):
                schema_validate(item, schema["items"], root, f"{path}[{index}]")
    if isinstance(value, str):
        if "minLength" in schema and len(value) < schema["minLength"]:
            fail(f"minLength at {path}")
        if "maxLength" in schema and len(value) > schema["maxLength"]:
            fail(f"maxLength at {path}")
        if "pattern" in schema and not re.search(schema["pattern"], value):
            fail(f"pattern at {path}")
    for keyword in ("allOf", "oneOf"):
        if keyword in schema:
            results = []
            errors = []
            for branch in schema[keyword]:
                try:
                    schema_validate(value, branch, root, path)
                    results.append(True)
                except ValueError as exc:
                    results.append(False)
                    errors.append(str(exc))
            count = sum(results)
            detail = f": {'; '.join(errors)}" if errors else ""
            if keyword == "allOf" and count != len(results):
                # allOf requires every branch, so the first branch failure is
                # the whole reason. Reporting it unwrapped keeps the offending
                # field at the front, where a schema factored through $defs
                # would otherwise stack one frame per composition level.
                fail(errors[0] if errors else f"allOf mismatch at {path}")
            if keyword == "oneOf" and count != 1:
                fail(f"oneOf mismatch at {path}{detail}")
    if "if" in schema:
        try:
            schema_validate(value, schema["if"], root, path)
        except ValueError:
            branch = schema.get("else")
        else:
            branch = schema.get("then")
        if branch is not None:
            schema_validate(value, branch, root, path)
    if "not" in schema:
        try:
            schema_validate(value, schema["not"], root, path)
        except ValueError:
            pass
        else:
            fail(f"not mismatch at {path}")
    return evaluated


def check_schema_keywords(node, path="schema"):
    if not isinstance(node, dict):
        return
    unsupported = set(node) - SCHEMA_KEYWORDS
    if unsupported:
        fail(f"unsupported JSON Schema keywords at {path}: {sorted(unsupported)}")
    for key, value in node.items():
        if key in {"properties", "$defs"} and isinstance(value, dict):
            for name, child in value.items():
                check_schema_keywords(child, f"{path}.{key}.{name}")
        elif isinstance(value, dict):
            check_schema_keywords(value, f"{path}.{key}")
        elif isinstance(value, list):
            for index, child in enumerate(value):
                check_schema_keywords(child, f"{path}.{key}[{index}]")


def gofmt(source: str) -> str:
    return subprocess.run(["gofmt"], input=source, text=True, capture_output=True, check=True).stdout


def generate_projections(
    label: str,
    vocabulary_path: Path,
    schema_path: Path,
    root: Path,
    project: Callable[[dict, str], dict[Path, str]],
    semantic: Callable[[dict], None] | None = None,
) -> int:
    """Run one vocabulary generator: validate the declaration against its
    schema and semantic rules, then write its projections, or with --check
    fail when a written projection drifts from the declaration."""
    parser = argparse.ArgumentParser(description=f"Validate and deterministically generate {label} projections.", allow_abbrev=False)
    parser.add_argument("--check", action="store_true",
                        help=f"fail on {label} projection drift instead of writing generated files")
    args = parser.parse_args()
    try:
        vocabulary = json.loads(vocabulary_path.read_text(encoding="utf-8"))
        schema = json.loads(schema_path.read_text(encoding="utf-8"))
        check_schema_keywords(schema)
        schema_validate(vocabulary, schema, schema, "vocabulary")
        if semantic is not None:
            semantic(vocabulary)
        vocabulary_digest = digest(vocabulary)
        expected = project(vocabulary, vocabulary_digest)
        if args.check:
            for path, content in expected.items():
                if not path.is_file() or path.read_text(encoding="utf-8") != content:
                    fail(f"generated {label} drift: {path.relative_to(root)}")
        else:
            for path, content in expected.items():
                path.parent.mkdir(exist_ok=True)
                path.write_text(content, encoding="utf-8")
        print(vocabulary_digest)
        return 0
    except (OSError, json.JSONDecodeError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"{label} generation failed: {exc}", file=sys.stderr)
        return 1


def load_json_finding(root: Path, relative: Path, findings: list[str]):
    try:
        return json.loads((root / relative).read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        findings.append(f"json-load: {relative}: invalid JSON: {exc}")
        return None


def generator_freshness(root: Path, script: str, findings: list[str]) -> None:
    result = subprocess.run(
        [sys.executable, str(root / "scripts" / script), "--check"],
        cwd=root, capture_output=True, text=True, check=False,
    )
    if result.returncode:
        detail = result.stderr.strip() or result.stdout.strip() or f"exit status {result.returncode}"
        findings.append(f"generator-freshness: {detail.replace(chr(10), ' ')}")
