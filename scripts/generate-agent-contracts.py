#!/usr/bin/env python3
"""Validate and deterministically generate the TS8 agent contract projections."""
from __future__ import annotations
import copy, hashlib, json, re, subprocess, sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / "contracts/agent-tool-surface.v1.json"
IR = ROOT / "contracts/agent-tool-surface.schema.json"
PAYLOAD = ROOT / "contracts/agent-tool-surface-payloads.schema.json"

def canonical(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()

def fail(message: str) -> None:
    raise ValueError(message)

SCHEMA_KEYWORDS = {"$schema", "$id", "$defs", "$ref", "title", "description", "type", "properties", "patternProperties", "propertyNames", "required", "additionalProperties", "unevaluatedProperties", "items", "contains", "minItems", "maxItems", "uniqueItems", "minLength", "maxLength", "pattern", "format", "minimum", "maximum", "enum", "const", "oneOf", "anyOf", "allOf", "not", "if", "then", "else", "default", "minProperties", "maxProperties"}

def schema_validate(value, schema, root, path="$"):
    """Validate an instance using every JSON-Schema keyword used in-repo."""
    if not isinstance(schema, dict): fail(f"schema node is not an object at {path}")
    unsupported = set(schema) - SCHEMA_KEYWORDS
    if unsupported: fail(f"unsupported JSON Schema keywords at {path}: {sorted(unsupported)}")
    if "$ref" in schema:
        ref = schema["$ref"]
        if not ref.startswith("#/$defs/"): fail(f"non-local schema ref {ref}")
        target = root.get("$defs", {}).get(ref.removeprefix("#/$defs/"))
        if target is None: fail(f"missing schema ref {ref}")
        return schema_validate(value, target, root, path)
    if "const" in schema and value != schema["const"]: fail(f"const mismatch at {path}")
    if "enum" in schema and value not in schema["enum"]: fail(f"enum mismatch at {path}")
    types = schema.get("type")
    if types is not None:
        types = types if isinstance(types, list) else [types]
        def is_type(kind):
            return {"object": isinstance(value, dict), "array": isinstance(value, list), "string": isinstance(value, str), "integer": isinstance(value, int) and not isinstance(value, bool), "number": isinstance(value, (int,float)) and not isinstance(value,bool), "boolean": isinstance(value,bool), "null": value is None}.get(kind, False)
        if not any(is_type(kind) for kind in types): fail(f"type mismatch at {path}")
    evaluated = set()
    if isinstance(value, dict):
        if "minProperties" in schema and len(value) < schema["minProperties"]: fail(f"minProperties at {path}")
        if "maxProperties" in schema and len(value) > schema["maxProperties"]: fail(f"maxProperties at {path}")
        if "propertyNames" in schema:
            for key in value:
                try: schema_validate(key, schema["propertyNames"], root, f"{path}.{key}")
                except ValueError as exc: fail(f"propertyNames at {path}.{key}: {exc}")
        required = schema.get("required", [])
        for key in required:
            if key not in value: fail(f"missing required {path}.{key}")
        properties = schema.get("properties", {})
        patterns = schema.get("patternProperties", {})
        for key, child in properties.items():
            if key in value:
                schema_validate(value[key], child, root, f"{path}.{key}")
                evaluated.add(key)
        for pattern, child in patterns.items():
            for key in value:
                if re.search(pattern, key):
                    schema_validate(value[key], child, root, f"{path}.{key}")
                    evaluated.add(key)
        additional = set(value) - evaluated
        if schema.get("additionalProperties") is False and additional:
            fail(f"unknown properties at {path}: {sorted(additional)}")
        if isinstance(schema.get("additionalProperties"), dict):
            for key in additional:
                schema_validate(value[key], schema["additionalProperties"], root, f"{path}.{key}")
                evaluated.add(key)
    if isinstance(value, list):
        if "minItems" in schema and len(value) < schema["minItems"]: fail(f"minItems at {path}")
        if "maxItems" in schema and len(value) > schema["maxItems"]: fail(f"maxItems at {path}")
        if schema.get("uniqueItems") and len({json.dumps(item, sort_keys=True) for item in value}) != len(value): fail(f"uniqueItems at {path}")
        if "items" in schema:
            for index, item in enumerate(value): schema_validate(item, schema["items"], root, f"{path}[{index}]")
        if "contains" in schema and not any(_valid(item, schema["contains"], root, f"{path}[{index}]") for index, item in enumerate(value)):
            fail(f"contains at {path}")
    if isinstance(value, str):
        if "minLength" in schema and len(value) < schema["minLength"]: fail(f"minLength at {path}")
        if "maxLength" in schema and len(value) > schema["maxLength"]: fail(f"maxLength at {path}")
        if "pattern" in schema and not re.search(schema["pattern"], value): fail(f"pattern at {path}")
        if schema.get("format") == "date-time":
            from datetime import datetime
            try: datetime.fromisoformat(value.replace("Z", "+00:00"))
            except ValueError: fail(f"format at {path}")
    if isinstance(value, (int,float)) and not isinstance(value,bool):
        if "minimum" in schema and value < schema["minimum"]: fail(f"minimum at {path}")
        if "maximum" in schema and value > schema["maximum"]: fail(f"maximum at {path}")
    for keyword in ("allOf", "anyOf", "oneOf"):
        if keyword in schema:
            results=[]
            errors=[]
            branch_evaluated=[]
            for branch in schema[keyword]:
                try: branch_evaluated.append(schema_validate(value, branch, root, path)); results.append(True)
                except ValueError as exc: results.append(False); errors.append(str(exc))
            count=sum(results)
            detail = f": {'; '.join(errors)}" if errors else ""
            if keyword=="allOf" and count != len(results): fail(f"allOf mismatch at {path}{detail}")
            if keyword=="anyOf" and count < 1: fail(f"anyOf mismatch at {path}{detail}")
            if keyword=="oneOf" and count != 1: fail(f"oneOf mismatch at {path}{detail}")
            for branch_result in branch_evaluated: evaluated.update(branch_result)
    if "if" in schema:
        condition = _valid(value, schema["if"], root, path)
        branch = schema.get("then") if condition else schema.get("else")
        if branch is not None: evaluated.update(schema_validate(value, branch, root, path))
    if "not" in schema:
        try: schema_validate(value, schema["not"], root, path)
        except ValueError: pass
        else: fail(f"not mismatch at {path}")
    if isinstance(value, dict) and "unevaluatedProperties" in schema:
        remaining = set(value) - evaluated
        if schema["unevaluatedProperties"] is False and remaining:
            fail(f"unevaluated properties at {path}: {sorted(remaining)}")
        if isinstance(schema["unevaluatedProperties"], dict):
            for key in remaining:
                schema_validate(value[key], schema["unevaluatedProperties"], root, f"{path}.{key}")
                evaluated.add(key)
    return evaluated

def _valid(value, schema, root, path):
    try:
        schema_validate(value, schema, root, path)
        return True
    except ValueError:
        return False

def validate_persisted_manifest(manifest, ir):
    schema_validate(manifest, ir, ir, "manifest")

def check_schema_keywords(node, path="schema"):
    if not isinstance(node, dict): return
    unsupported=set(node)-SCHEMA_KEYWORDS
    if unsupported: fail(f"unsupported JSON Schema keywords at {path}: {sorted(unsupported)}")
    for key,value in node.items():
        if key in {"properties", "patternProperties", "$defs"} and isinstance(value, dict):
            for name, child in value.items(): check_schema_keywords(child, f"{path}.{key}.{name}")
        elif isinstance(value, dict): check_schema_keywords(value, f"{path}.{key}")
        elif isinstance(value, list):
            for index, child in enumerate(value): check_schema_keywords(child, f"{path}.{key}[{index}]")

def check_payload_closed(node, path="payload"):
    if not isinstance(node, dict): return
    if node.get("type") == "object" and node.get("additionalProperties") is not False: fail(f"payload object is not closed: {path}")
    for key,value in node.items():
        if key in {"properties", "$defs"} and isinstance(value, dict):
            for name, child in value.items(): check_payload_closed(child, f"{path}.{name}")
        elif key in {"items", "not", "additionalProperties"} and isinstance(value, dict): check_payload_closed(value, f"{path}.{key}")
        elif key in {"allOf", "anyOf", "oneOf"} and isinstance(value, list):
            for index, child in enumerate(value): check_payload_closed(child, f"{path}.{key}[{index}]")

def validate(manifest: dict) -> str:
    expected_top = {"$schema", "schema_version", "surface", "envelope", "tools", "operations", "schemas", "capabilities", "consequences", "bounds", "deprecations", "generation", "payload_digest", "digest"}
    if set(manifest) != expected_top:
        fail("manifest has unknown or missing top-level sections")
    if manifest.get("schema_version") != "1.0" or manifest.get("surface", {}).get("tool_count") != 9:
        fail("manifest schema or tool count is invalid")
    capabilities = set(manifest.get("capabilities", []))
    consequences = set(manifest.get("consequences", []))
    tools = manifest.get("tools", [])
    if len(tools) != 9 or len({t.get("id") for t in tools}) != 9:
        fail("manifest must contain exactly nine unique tools")
    for tool in tools:
        if set(tool) != {"id", "description", "operations"} or not tool["operations"]:
            fail(f"tool section is not closed: {tool.get('id')}")
    operations = manifest.get("operations", [])
    expected_operations = {"3.8.0": 47, "3.7.0": 47, "3.6.0": 47, "3.5.0": 46, "3.4.0": 43, "3.3.0": 40, "3.2.0": 39, "3.1.0": 38, "3.0.0": 32, "2.4.0": 25, "2.3.0": 23}.get(manifest.get("surface", {}).get("version"))
    if expected_operations is None:
        expected_operations = 22 if manifest.get("surface", {}).get("version") in {"2.1.0", "2.2.0"} else 21
    if len(operations) != expected_operations or len({o.get("id") for o in operations}) != expected_operations:
        fail(f"manifest must contain exactly {expected_operations} unique operations")
    tool_ids = {t["id"] for t in tools}
    declared = {op for tool in tools for op in tool["operations"]}
    actual = {o["id"] for o in operations}
    if declared != actual or any(o["tool"] not in tool_ids for o in operations):
        fail("tool operation coverage is incomplete")
    if any("alias" in o or "alias" in t for o in operations for t in tools):
        fail("aliases are not permitted")
    for op in operations:
        if set(op) - {"id", "tool", "kind", "availability", "query_id", "input_schema", "result_schema", "capability", "consequence", "approval", "metadata"}:
            fail(f"operation section is not closed: {op.get('id')}")
        if set(op["metadata"]) != {"context", "idempotency", "pagination", "output_bytes", "versions"}:
            fail(f"operation metadata is not closed: {op.get('id')}")
        if not re.fullmatch(r"concord_[a-z0-9_]+\.[a-z0-9_]+", op["id"]): fail(f"invalid operation id {op.get('id')}")
        if op["id"].split(".", 1)[0] != op["tool"]: fail(f"operation/tool pairing mismatch: {op['id']}")
        if op["capability"] not in capabilities or op["consequence"] not in consequences: fail(f"operation classification is not declared: {op['id']}")
        if op["kind"] == "read" and not op.get("query_id"): fail(f"read operation lacks query id: {op['id']}")
        if op["kind"] == "mutation" and op.get("query_id") is not None: fail(f"mutation has query id: {op['id']}")
        if op["id"] == "concord_work_transition.workflow_action" and op.get("availability") != "workflow_definition": fail("workflow_action must be accepted but unavailable")
        if op["input_schema"] not in {f"#/schemas/{key}" for key in manifest.get("schemas", {})} or op["result_schema"] not in {f"#/schemas/{key}" for key in manifest.get("schemas", {})}:
            fail(f"operation schema reference missing: {op['id']}")
    if any(set(value) != {"ref", "closed"} or value["closed"] is not True for value in manifest.get("schemas", {}).values()):
        fail("schema references must be closed")
    if manifest.get("envelope", {}).get("max_bytes") != 65536 or manifest.get("bounds", {}).get("max_output_bytes") != 65536:
        fail("envelope bounds are not canonical")
    payload = json.loads(PAYLOAD.read_text())
    check_schema_keywords(payload, "payload")
    check_payload_closed(payload)
    defs = payload.get("$defs", {})
    if not defs or any(not isinstance(schema, dict) for schema in defs.values()): fail("payload definitions are not objects")
    refs = {f"contracts/agent-tool-surface-payloads.schema.json#/$defs/{name}" for name in defs}
    for name, schema in defs.items():
        if schema.get("type") == "object" and schema.get("additionalProperties") is not False: fail(f"payload object is not closed: {name}")
        if "additionalProperties" in schema and schema["additionalProperties"] is not False: fail(f"payload contains arbitrary map: {name}")
    for key, value in manifest.get("schemas", {}).items():
        if value["ref"] not in refs and value["ref"] != "contracts/agent-tool-envelope.schema.json#/$defs/base": fail(f"schema ref missing or outside canonical schemas: {key}")
    for op in operations:
        for schema_ref in (op["input_schema"], op["result_schema"]):
            schema_name = schema_ref.split("/")[-1]
            schema = defs.get(schema_name)
            if not schema or schema.get("type") != "object" or not isinstance(schema.get("properties"), dict): fail(f"operation payload is not a closed object: {op['id']} / {schema_name}")
        input_schema = defs[op["input_schema"].split("/")[-1]]
        result_schema = defs[op["result_schema"].split("/")[-1]]
        branch_required=[set(branch.get("required",[])) for branch in input_schema.get("oneOf",[])]
        has_idempotency="idempotency_key" in input_schema.get("required",[]) or bool(branch_required and all("idempotency_key" in branch for branch in branch_required))
        if op["kind"] == "mutation" and not has_idempotency: fail(f"mutation lacks idempotency identity: {op['id']}")
        if not result_schema.get("required"): fail(f"result schema lacks required fields: {op['id']}")
    unsigned = dict(manifest); unsigned.pop("digest", None)
    return "sha256:" + hashlib.sha256(canonical(unsigned)).hexdigest()

def go_projection(manifest: dict, digest: str) -> str:
    ops = ",\n".join(f'\t{{ID: "{o["id"]}", Tool: "{o["tool"]}", Operation: "{o["id"].split(".",1)[1]}", Kind: OperationKind("{o["kind"]}"), QueryID: "{o.get("query_id") or ""}", Capability: Capability("{o["capability"]}"), Approval: ApprovalClass("{o["approval"]}"), Availability: Availability("{o.get("availability", "always")}")}}' for o in manifest["operations"])
    payload = json.loads(PAYLOAD.read_text())
    rules = []
    for name, schema in sorted(payload["$defs"].items()):
        if schema.get("type") == "object":
            required = ", ".join(json.dumps(v) for v in schema.get("required", []))
            properties = ", ".join(json.dumps(v) for v in schema.get("properties", {}))
            rules.append(f'\t"{name}": {{Required: []string{{{required}}}, Properties: []string{{{properties}}}}},')
    return f'''// Code generated by scripts/generate-agent-contracts.py; DO NOT EDIT.\npackage agent\n\nimport ("encoding/json"; "fmt")\nconst ManifestVersion = "{manifest["surface"]["version"]}"\nconst ManifestDigest = "{digest}"\n\ntype OperationKind string\nconst ( OperationRead OperationKind = "read"; OperationMutation OperationKind = "mutation" )\ntype Capability string\ntype ApprovalClass string\ntype Availability string\nconst ( AvailabilityAlways Availability = "always"; AvailabilityWorkflowDefinition Availability = "workflow_definition" )\ntype ContractOperation struct {{ ID, Tool, Operation string; Kind OperationKind; QueryID string; Capability Capability; Approval ApprovalClass; Availability Availability }}\nvar ContractOperations = []ContractOperation{{\n{ops},\n}}\nfunc ValidateContractOperation(tool, operation string) (ContractOperation, bool) {{\n    for _, candidate := range ContractOperations {{ if candidate.Tool == tool && candidate.Operation == operation {{ return candidate, true }} }}\n    return ContractOperation{{}}, false\n}}\nfunc WorkflowActionAvailable() bool {{ return false }}\ntype GeneratedPayloadRule struct {{ Required []string; Properties []string }}\nvar GeneratedPayloadRules = map[string]GeneratedPayloadRule{{\n{chr(10).join(rules)}\n}}\nfunc ValidateGeneratedPayload(schemaName string, data []byte) error {{\n    rule, ok := GeneratedPayloadRules[schemaName]; if !ok {{ return fmt.Errorf("unknown payload schema %s", schemaName) }}\n    var object map[string]json.RawMessage; if err := json.Unmarshal(data, &object); err != nil {{ return fmt.Errorf("payload is not an object: %w", err) }}\n    allowed := map[string]bool{{}}; for _, name := range rule.Properties {{ allowed[name] = true }}\n    for name := range object {{ if !allowed[name] {{ return fmt.Errorf("unknown payload field %s", name) }} }}\n    for _, name := range rule.Required {{ if _, ok := object[name]; !ok {{ return fmt.Errorf("missing payload field %s", name) }} }}\n    return nil\n}}\n'''

def ts_projection(manifest: dict, digest: str) -> str:
    data = json.dumps(manifest["operations"], ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    schema_refs = json.dumps(manifest["schemas"], ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    payload_defs = json.dumps(json.loads(PAYLOAD.read_text())["$defs"], ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    return f'''// Code generated by scripts/generate-agent-contracts.py; DO NOT EDIT.\nexport const manifestVersion = "{manifest["surface"]["version"]}" as const;\nexport const manifestDigest = "{digest}" as const;\nexport const maxEnvelopeBytes = 65536 as const;\nexport const contractOperations = {data} as const;\nexport const schemaRefs = {schema_refs} as const;\nexport const payloadSchemas = {payload_defs} as const;\n\n/** Canonical assertion encoding: UTF-8 `v1\\0`, then fixed fields in this order; each is `name=<decimal byte length>:<UTF-8 value>|`. Lists are sorted and joined with `,`; no whitespace or JSON maps are accepted. */\nexport function canonicalAssertion(fields: Record<string, string | string[]>): Uint8Array {{\n  const names = ["client_ref", "client_version", "session_ref", "agent_ref", "directory", "worktree", "requested_product_id", "requested_project_ids", "requested_capabilities", "issued_at", "nonce", "surface_range", "envelope_versions", "manifest_digest"];\n  const body = names.map((name) => {{ const raw = fields[name] ?? ""; const value = Array.isArray(raw) ? [...raw].sort().join(",") : raw; const bytes = new TextEncoder().encode(value); return `${{name}}=${{bytes.length}}:${{value}}|`; }}).join("");\n  return new TextEncoder().encode(`v1\\0${{body}}`);\n}}\n'''

def fixtures_projection(manifest: dict) -> str:
    payload = json.loads(PAYLOAD.read_text())["$defs"]
    def resolve(schema):
        if isinstance(schema, dict) and "$ref" in schema: return payload[schema["$ref"].removeprefix("#/$defs/")]
        return schema
    def sample(schema):
        schema=resolve(schema)
        if "const" in schema: return schema["const"]
        if "oneOf" in schema:
            branch=schema["oneOf"][0]; result=sample(branch); base={key:sample(schema.get("properties",{}).get(key,{})) for key in schema.get("required",[])}
            if isinstance(result,dict): base.update(result); return base
            if isinstance(branch,dict): base.update({key:sample(schema.get("properties",{}).get(key,{})) for key in branch.get("required",[])}); return base
            return result
        if "allOf" in schema:
            result={}
            for branch in schema["allOf"]: result.update(sample(branch) or {})
            return result
        if "enum" in schema: return schema["enum"][0]
        types=schema.get("type"); types=types if isinstance(types,list) else [types] if types else []
        if "null" in types and len(types)>1: types=[kind for kind in types if kind!="null"]
        kind=types[0] if types else None
        if kind=="object":
            result={}
            for key in schema.get("required",[]): result[key]=sample(schema.get("properties",{}).get(key,{}))
            return result
        if kind=="array": return [sample(schema.get("items",{}))] if schema.get("minItems",0)>0 else []
        if kind=="string":
            pattern=schema.get("pattern","")
            if "sha256:" in pattern: return "sha256:"+"0"*64
            if "[0-9a-f]{40}" in pattern: return "0"*40
            if pattern.startswith("^[a-z][a-z0-9_-]"): return "fence:prod-pause"
            if pattern.startswith("^msg:"): return "msg:" + "0"*32
            if "date" in pattern: return "2026-08-08T00:00:00Z"
            return "id-1"
        if kind=="integer" or kind=="number": return schema.get("minimum",1)
        if kind=="boolean": return False
        return None
    def nested_unknown(value):
        candidate=copy.deepcopy(value)
        if isinstance(candidate,dict):
            for key,child in candidate.items():
                if isinstance(child,dict): child["nested_unknown"]=True; return candidate
                if isinstance(child,list) and child and isinstance(child[0],dict): child[0]["nested_unknown"]=True; return candidate
        return candidate
    def oversized(value):
        candidate=copy.deepcopy(value)
        if isinstance(candidate,dict):
            for key,child in candidate.items():
                if isinstance(child,str): candidate[key]="x"*100000; return candidate
                changed=oversized(child)
                if changed != child: candidate[key]=changed; return candidate
        if isinstance(candidate,list) and candidate:
            changed=oversized(candidate[0]);candidate[0]=changed;return candidate
        return candidate
    fixtures = []
    for operation in manifest["operations"]:
        input_name=operation["input_schema"].split("/")[-1]
        result_name = operation["result_schema"].split("/")[-1]
        valid_input=sample(payload[input_name]);valid_result=sample(payload[result_name])
        invalid_input=dict(valid_input) if isinstance(valid_input,dict) else {"value":valid_input};invalid_input["unknown"]=True
        invalid_result=dict(valid_result) if isinstance(valid_result,dict) else {"value":valid_result};invalid_result["unknown"]=True
        input_invalid=[case for case in [invalid_input,nested_unknown(valid_input),oversized(valid_input)] if case != valid_input]
        result_invalid=[case for case in [invalid_result,nested_unknown(valid_result),oversized(valid_result)] if case != valid_result]
        fixtures.append({"operation": operation["id"], "input_schema": input_name, "input_valid": valid_input, "input_invalid_cases":input_invalid, "result_schema": result_name, "result_valid": valid_result, "result_invalid_cases":result_invalid})
    return json.dumps({"manifest_digest": manifest["digest"], "fixtures": fixtures}, ensure_ascii=False, sort_keys=True, indent=2) + "\n"

def docs_projection(manifest: dict) -> str:
    rows = ["# Generated Concord agent tool surface", "", f"Source manifest: `{manifest['surface']['version']}` / `{manifest['digest']}`", "Payload schema digest: `" + manifest["payload_digest"] + "`", f"Supported surface versions: `{manifest['surface']['version']}`; envelope schema: `1.0`", "", "| Operation | Kind | Query | Capability | Consequence | Availability |", "|---|---|---|---|---|---|"]
    rows.extend(f"| `{o['id']}` | `{o['kind']}` | `{o.get('query_id') or '—'}` | `{o['capability']}` | `{o['consequence']}` | `{o.get('availability', 'always')}` |" for o in manifest["operations"])
    return "\n".join(rows) + "\n"

def go_payload_schema_projection() -> str:
    document = json.dumps(json.loads(PAYLOAD.read_text()), ensure_ascii=False, sort_keys=True, indent=2)
    return "// Code generated by scripts/generate-agent-contracts.py; DO NOT EDIT.\npackage agent\n\nconst GeneratedPayloadSchemaDocument = `" + document + "`\n"

def ts_validator_projection(manifest: dict) -> str:
    envelope_schema = json.dumps(json.loads((ROOT / "contracts/agent-tool-envelope.schema.json").read_text()), ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    return f'''// Code generated by scripts/generate-agent-contracts.py; DO NOT EDIT.
import {{ payloadSchemas }} from "./generated-contracts";
export const envelopeSchema = {envelope_schema};
type Validation = {{ valid: boolean; evaluated: Set<string> }};
export function validateGeneratedPayload(name: string, value: unknown): boolean {{
  return validateSchema((payloadSchemas as Record<string, unknown>)[name], value, payloadSchemas as Record<string, unknown>).valid;
}}
export function validateGeneratedEnvelope(value: unknown): boolean {{
  return validateSchema(envelopeSchema, value, envelopeSchema as Record<string, unknown>).valid;
}}
function pass(evaluated: Iterable<string> = []): Validation {{ return {{ valid: true, evaluated: new Set(evaluated) }}; }}
function fail(): Validation {{ return {{ valid: false, evaluated: new Set() }}; }}
function merge(target: Set<string>, source: Set<string>): void {{ for (const key of source) target.add(key); }}
function validateSchema(schema: any, value: unknown, root: Record<string, unknown>): Validation {{
  if (!schema || typeof schema !== "object") return fail();
  if (schema.$ref) {{ const definitions = root.$defs ?? root; const target = definitions[schema.$ref.replace("#/$defs/", "")]; return validateSchema(target, value, root); }}
  if ("const" in schema && JSON.stringify(schema.const) !== JSON.stringify(value)) return fail();
  if (schema.enum && !schema.enum.some((candidate: unknown) => JSON.stringify(candidate) === JSON.stringify(value))) return fail();
  if (schema.type) {{ const types = Array.isArray(schema.type) ? schema.type : [schema.type]; if (!types.some((kind: string) => kind === "null" ? value === null : kind === "array" ? Array.isArray(value) : kind === "object" ? value !== null && typeof value === "object" && !Array.isArray(value) : kind === "integer" ? typeof value === "number" && Number.isInteger(value) : typeof value === kind || kind === "number" && typeof value === "number")) return fail(); }}
  if (typeof value === "string") {{ if (schema.minLength !== undefined && value.length < schema.minLength || schema.maxLength !== undefined && value.length > schema.maxLength || schema.pattern && !(new RegExp(schema.pattern).test(value))) return fail(); if (schema.format === "date-time" && Number.isNaN(Date.parse(value))) return fail(); }}
  if (typeof value === "number" && (schema.minimum !== undefined && value < schema.minimum || schema.maximum !== undefined && value > schema.maximum)) return fail();
  const evaluated = new Set<string>();
  if (value !== null && typeof value === "object" && !Array.isArray(value)) {{
    const object = value as Record<string, unknown>; const properties = schema.properties ?? {{}}; const patterns = schema.patternProperties ?? {{}}; const known = new Set<string>();
    if (schema.required && !schema.required.every((key: string) => key in object)) return fail();
    for (const [key, child] of Object.entries(properties)) if (key in object) {{ const result = validateSchema(child, object[key], root); if (!result.valid) return fail(); evaluated.add(key); }}
    for (const [pattern, child] of Object.entries(patterns)) for (const key of Object.keys(object)) if (new RegExp(pattern).test(key)) {{ const result = validateSchema(child, object[key], root); if (!result.valid) return fail(); evaluated.add(key); }}
    for (const key of Object.keys(properties)) known.add(key);
    for (const pattern of Object.keys(patterns)) for (const key of Object.keys(object)) if (new RegExp(pattern).test(key)) known.add(key);
    const additional = Object.keys(object).filter((key) => !known.has(key));
    if (schema.additionalProperties === false && additional.length > 0) return fail();
    if (schema.additionalProperties && typeof schema.additionalProperties === "object") for (const key of additional) {{ const result = validateSchema(schema.additionalProperties, object[key], root); if (!result.valid) return fail(); evaluated.add(key); }}
    if (schema.minProperties !== undefined && Object.keys(object).length < schema.minProperties || schema.maxProperties !== undefined && Object.keys(object).length > schema.maxProperties) return fail();
  }}
  if (Array.isArray(value)) {{
    if (schema.minItems !== undefined && value.length < schema.minItems || schema.maxItems !== undefined && value.length > schema.maxItems) return fail();
    if (schema.uniqueItems && new Set(value.map((item) => JSON.stringify(item))).size !== value.length) return fail();
    if (schema.items) for (const item of value) if (!validateSchema(schema.items, item, root).valid) return fail();
  }}
  for (const keyword of ["allOf", "anyOf", "oneOf"] as const) if (schema[keyword]) {{
    const results = schema[keyword].map((branch: unknown) => validateSchema(branch, value, root)); const valid = results.filter((result: Validation) => result.valid);
    if (keyword === "allOf" && valid.length !== results.length || keyword === "anyOf" && valid.length < 1 || keyword === "oneOf" && valid.length !== 1) return fail();
    for (const result of valid) merge(evaluated, result.evaluated);
  }}
  if (schema.if) {{ const condition = validateSchema(schema.if, value, root); const branch = condition.valid ? schema.then : schema.else; if (branch) {{ const result = validateSchema(branch, value, root); if (!result.valid) return fail(); merge(evaluated, result.evaluated); }} }}
  if (schema.not && validateSchema(schema.not, value, root).valid) return fail();
  if (value !== null && typeof value === "object" && !Array.isArray(value) && schema.unevaluatedProperties !== undefined) {{
    const object = value as Record<string, unknown>; const remaining = Object.keys(object).filter((key) => !evaluated.has(key));
    if (schema.unevaluatedProperties === false && remaining.length > 0) return fail();
    if (schema.unevaluatedProperties && typeof schema.unevaluatedProperties === "object") for (const key of remaining) {{ const result = validateSchema(schema.unevaluatedProperties, object[key], root); if (!result.valid) return fail(); evaluated.add(key); }}
  }}
  return pass(evaluated);
}}
'''

def main() -> int:
    try:
        manifest = json.loads(MANIFEST.read_text())
        ir = json.loads(IR.read_text())
        payload = json.loads(PAYLOAD.read_text())
        envelope = json.loads((ROOT / "contracts/agent-tool-envelope.schema.json").read_text())
        check_schema_keywords(ir, "ir")
        check_schema_keywords(payload, "payload")
        check_schema_keywords(envelope, "envelope")
        validate_persisted_manifest(manifest, ir)
        payload_digest = "sha256:" + hashlib.sha256(canonical(payload)).hexdigest()
        manifest["payload_digest"] = payload_digest
        digest = validate(manifest)
        check = "--check" in sys.argv[1:]
        if manifest.get("digest") != digest or manifest.get("payload_digest") != payload_digest:
            if check:
                fail(f"manifest digest is {manifest.get('digest')}, expected {digest}")
            manifest["digest"] = digest
            MANIFEST.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")
        formatted_go = subprocess.run(["gofmt"], input=go_projection(manifest, digest), text=True, capture_output=True, check=True).stdout
        formatted_go = formatted_go.replace("func WorkflowActionAvailable() bool { return false }\n", "")
        expected = {
            ROOT / "internal/agent/generated_contracts.go": formatted_go,
            ROOT / "adapter/opencode/generated-contracts.ts": ts_projection(manifest, digest).replace('["client_ref", "client_version", "session_ref", "agent_ref", "directory", "worktree", "issued_at", "nonce", "surface_range", "envelope_versions", "manifest_digest"]', '["client_ref", "client_version", "session_ref", "agent_ref", "directory", "worktree", "requested_product_id", "requested_project_ids", "requested_capabilities", "issued_at", "nonce", "surface_range", "envelope_versions", "manifest_digest"]'),
            ROOT / "contracts/agent-tool-surface.digest": digest + "\n",
            ROOT / "contracts/agent-tool-surface.fixtures.json": fixtures_projection(manifest),
            ROOT / "docs/generated-agent-tool-surface.md": docs_projection(manifest),
            ROOT / "adapter/opencode/generated-contract-tests.ts": ts_validator_projection(manifest),
            ROOT / "internal/agent/generated_payload_schemas.go": go_payload_schema_projection(),
        }
        if check:
            for path, content in expected.items():
                if not path.is_file() or path.read_text() != content:
                    fail(f"generated contract drift: {path.relative_to(ROOT)}")
        else:
            (ROOT / "internal/agent").mkdir(exist_ok=True)
            for path, content in expected.items():
                path.parent.mkdir(exist_ok=True)
                path.write_text(content)
        print(digest)
        return 0
    except (OSError, json.JSONDecodeError, ValueError) as exc:
        print(f"agent contract generation failed: {exc}", file=sys.stderr)
        return 1
if __name__ == "__main__": raise SystemExit(main())
