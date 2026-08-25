#!/usr/bin/env python3
"""Validate and deterministically generate relation vocabulary projections."""
from __future__ import annotations

import hashlib
import json
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
VOCABULARY = ROOT / "contracts/relation-vocabulary.v1.json"
SCHEMA = ROOT / "contracts/relation-vocabulary.schema.json"


def canonical(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()


def fail(message: str) -> None:
    raise ValueError(message)


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


def schema_validate(value, schema, root, path="$:"):
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
        fail(f"enum mismatch at {path}")
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


def go_projection(vocabulary: dict) -> str:
    kinds = vocabulary["kinds"]
    stored = sorted(kind["kind"] for kind in kinds)
    identity_events = sorted({event for kind in kinds for event in kind["written_by"]})
    addressable = sorted(kind["kind"] for kind in kinds if kind["link"] in {"allowed", "refused"})
    refusals = {
        kind["kind"]: kind["link_refusal"]
        for kind in kinds
        if kind["link"] == "refused"
    }
    transitive = sorted(kind["kind"] for kind in kinds if kind["transitive"])
    labels = {}
    defaults = sorted(kind["kind"] for kind in kinds)
    for kind in kinds:
        name = kind["kind"]
        labels[name] = (name, name, False)
        if kind["inverse_label"] is not None:
            labels[kind["inverse_label"]] = (kind["inverse_label"], name, True)

    def go_strings(values):
        return ", ".join(json.dumps(value) for value in values)

    refusal_entries = "\n".join(
        f'\t{json.dumps(name)}: {{Message: '
        + json.dumps(value["message"])
        + ", Recovery: "
        + json.dumps(value["recovery"])
        + "},"
        for name, value in sorted(refusals.items())
    )
    label_entries = "\n".join(
        f'\t{json.dumps(label)}: {{label: '
        + json.dumps(spec[0])
        + ", stored: "
        + json.dumps(spec[1])
        + ", invert: "
        + str(spec[2]).lower()
        + "},"
        for label, spec in sorted(labels.items())
    )
    addressable_entries = "\n".join(f'\t{json.dumps(kind)}: true,' for kind in addressable)
    transitive_entries = "\n".join(f'\t{json.dumps(kind)}: true,' for kind in transitive)
    return f'''// Code generated by scripts/generate-relation-vocabulary.py; DO NOT EDIT.
package store

var relationStoredKinds = []string{{{go_strings(stored)}}}

var relationIdentityEventKinds = []string{{{go_strings(identity_events)}}}

var relationKinds = map[string]bool{{
 {addressable_entries}
}}

type relationLinkRefusal struct {{
	Message  string
	Recovery string
}}

var relationLinkRefusals = map[string]relationLinkRefusal{{
{refusal_entries}
}}

var relationTransitiveKinds = map[string]bool{{
 {transitive_entries}
}}

var relationQueryLabels = map[string]relationSpec{{
{label_entries}
}}

var relationDefaultQueryLabels = []string{{{go_strings(defaults)}}}
'''


def main() -> int:
    try:
        vocabulary = json.loads(VOCABULARY.read_text())
        schema = json.loads(SCHEMA.read_text())
        check_schema_keywords(schema)
        schema_validate(vocabulary, schema, schema, "vocabulary")
        kinds = vocabulary["kinds"]
        names = [kind["kind"] for kind in kinds]
        labels = names + [kind["inverse_label"] for kind in kinds if kind["inverse_label"] is not None]
        if len(set(names)) != len(names):
            fail("relation kinds must be unique")
        if len(set(labels)) != len(labels):
            fail("relation query labels must be unique")
        digest = "sha256:" + hashlib.sha256(canonical(vocabulary)).hexdigest()
        expected = {
            ROOT / "internal/store/generated_relation_vocabulary.go": subprocess.run(
                ["gofmt"], input=go_projection(vocabulary), text=True, capture_output=True, check=True
            ).stdout,
            ROOT / "contracts/relation-vocabulary.digest": digest + "\n",
        }
        if "--check" in sys.argv[1:]:
            for path, content in expected.items():
                if not path.is_file() or path.read_text() != content:
                    fail(f"generated relation vocabulary drift: {path.relative_to(ROOT)}")
        else:
            for path, content in expected.items():
                path.parent.mkdir(exist_ok=True)
                path.write_text(content)
        print(digest)
        return 0
    except (OSError, json.JSONDecodeError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"relation vocabulary generation failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
