"""Small JSON Schema and canonical JSON helpers for vocabulary generators."""
from __future__ import annotations

import hashlib
import json
import re


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
                fail(f"allOf mismatch at {path}{detail}")
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
