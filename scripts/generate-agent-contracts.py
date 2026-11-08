#!/usr/bin/env python3
"""Validate and deterministically generate the TS8 agent contract projections."""
from __future__ import annotations
import argparse, copy, hashlib, json, os, re, shutil, subprocess, sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / "contracts/agent-tool-surface.v1.json"
IR = ROOT / "contracts/agent-tool-surface.schema.json"
PAYLOAD = ROOT / "contracts/agent-tool-surface-payloads.schema.json"
ENVELOPE = ROOT / "contracts/agent-tool-envelope.schema.json"
HOST_MANIFEST = ROOT / "contracts/host-tool-surface.v1.json"
HOST_SCHEMA = ROOT / "contracts/host-tool-surface.schema.json"

def canonical(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()

def fail(message: str) -> None:
    raise ValueError(message)

# A failed Go producer can bury its one-line cause (for example
# `undefined: containsString`) under every compiler error a broken merge
# produced. Failure messages keep a bounded head of the producer's stderr —
# the first error is the root cause — and say when they truncated.
PRODUCER_STDERR_LIMIT = 4096


def bounded_producer_stderr(detail: str) -> str:
    text = detail.strip()
    if len(text) <= PRODUCER_STDERR_LIMIT:
        return text
    return (
        f"{text[:PRODUCER_STDERR_LIMIT].rstrip()}\n"
        f"[producer stderr truncated: first {PRODUCER_STDERR_LIMIT} of {len(text)} characters]"
    )

# The published request document is compact: local #/$defs references and
# factored object unions (shared property constraints and required fields at
# the union parent, empty declarations in each closed child, the workflow
# actions grouped under their operation). Structural inspection needs the
# legacy-effective view, so the emitted test helper resolves every reference
# against the passed document and merges each union parent's type, required
# set, property constraints, and closure back into its branches. This is a
# plain string so the TypeScript braces never meet the f-string escaping.
PUBLICATION_EXPANSION_HELPER = '''
// expandedPublishedRequestSchema resolves the passed document's local
// references and returns the legacy-effective branch view for structural
// inspection only: one fully resolved closed branch per operation, and for
// workflow_action one branch per action variant, with each factored union
// parent's type, required set, property constraints, and closure merged back
// into its branches. The argument is the compact published request document,
// or the final hooked argument root whose root carries the request
// definitions beside properties.request. Validation never runs on this view:
// the published compact document and the final hooked root stay the admitted
// inputs.
export function expandedPublishedRequestSchema(published: unknown): unknown {
  const doc: Record<string, any> = published !== null && typeof published === "object" && !Array.isArray(published) ? published : {};
  const candidate: unknown = (doc.properties ?? {}).request;
  const request: Record<string, any> = candidate !== null && typeof candidate === "object" && !Array.isArray(candidate) ? candidate : doc;
  const defs: Record<string, any> = { ...(doc.$defs ?? {}), ...(request.$defs ?? {}) };
  const resolving = new Set<string>();
  const resolve = (node: any): any => {
    if (node === null || typeof node !== "object" || Array.isArray(node)) return node;
    if (typeof node.$ref !== "string") return node;
    if (!node.$ref.startsWith("#/$defs/")) throw new Error(`published request carries a non-local reference ${node.$ref}`);
    const name = node.$ref.slice("#/$defs/".length);
    if (!Object.hasOwn(defs, name)) throw new Error(`published request reference ${node.$ref} resolves no local definition`);
    if (resolving.has(name)) throw new Error(`published request references are cyclic at ${node.$ref}`);
    resolving.add(name);
    // Keep the name active through its full subtree, not only alias refs.
    try { return expandValue(resolve(defs[name])); } finally { resolving.delete(name); }
  };
  // conjunction merges one factored union parent's object constraints into
  // one branch: required sets union, a parent property constraint fills the
  // child's empty declaration, and the child's own keyword wins where both
  // state one. Only the object keywords publication factors travel here, so
  // an authored scalar alternation passes through unchanged.
  const conjunction = (parent: any, child: any): any => {
    const merged: Record<string, any> = { ...child };
    if (parent.type !== undefined && merged.type === undefined) merged.type = parent.type;
    if (parent.additionalProperties !== undefined && merged.additionalProperties === undefined) merged.additionalProperties = parent.additionalProperties;
    const required = [...new Set([...(Array.isArray(parent.required) ? parent.required : []), ...(Array.isArray(child.required) ? child.required : [])])];
    if (required.length > 0) merged.required = required;
    const parentProperties = parent.properties !== null && typeof parent.properties === "object" && !Array.isArray(parent.properties) ? parent.properties : {};
    const childProperties = child.properties !== null && typeof child.properties === "object" && !Array.isArray(child.properties) ? child.properties : {};
    const properties: Record<string, any> = { ...parentProperties, ...childProperties };
    for (const [name, childProperty] of Object.entries(childProperties)) {
      const parentProperty = parentProperties[name];
      if (parentProperty !== null && typeof parentProperty === "object" && !Array.isArray(parentProperty) && childProperty !== null && typeof childProperty === "object" && !Array.isArray(childProperty)) {
        properties[name] = { ...parentProperty, ...childProperty };
      }
    }
    if (Object.keys(properties).length > 0) merged.properties = properties;
    return merged;
  };
  const expandValue = (value: any): any => {
    if (Array.isArray(value)) return value.map(expandValue);
    if (value === null || typeof value !== "object") return value;
    const node = resolve(value);
    const union = node !== null && typeof node === "object" && !Array.isArray(node) ? ["oneOf", "anyOf"].find((keyword) => Array.isArray(node[keyword])) : undefined;
    if (union !== undefined) {
      const expanded: Record<string, any> = {};
      for (const [key, entry] of Object.entries(node)) {
        if (key === union || key === "$defs") continue;
        expanded[key] = expandValue(entry);
      }
      expanded[union] = node[union].map((branch: any) => expandValue(conjunction(node, resolve(branch))));
      return expanded;
    }
    const result: Record<string, any> = {};
    for (const [key, entry] of Object.entries(node)) {
      if (key === "$defs") continue;
      result[key] = expandValue(entry);
    }
    return result;
  };
  const root: any = resolve(request);
  if (!Array.isArray(root.oneOf)) return expandValue(root);
  const branches: any[] = [];
  for (const member of root.oneOf) {
    const branch = conjunction(root, resolve(member));
    const input = resolve(branch.properties?.input);
    if (branch.properties?.operation?.const === "workflow_action" && input !== null && typeof input === "object" && Array.isArray(input.oneOf)) {
      // The workflow_action union groups the action variants under one
      // operation branch; the legacy view flattens it back to one request
      // branch per action, restoring the branch description the factored
      // form carries on each grouped variant.
      for (const variant of input.oneOf) {
        const mergedInput = expandValue(conjunction(input, resolve(variant)));
        const flattened: Record<string, any> = { ...branch, properties: { ...branch.properties, input: mergedInput } };
        if (flattened.description === undefined && typeof mergedInput.description === "string") {
          flattened.description = mergedInput.description;
          delete flattened.properties.input.description;
        }
        branches.push(flattened);
      }
      continue;
    }
    branches.push(expandValue(branch));
  }
  const body: Record<string, any> = {};
  for (const [key, entry] of Object.entries(root)) {
    if (key === "oneOf" || key === "$defs") continue;
    body[key] = expandValue(entry);
  }
  body.oneOf = branches;
  return body;
}
// actionFieldsBranches returns the expanded branch list of one action
// variant's fields surface: every oneOf/anyOf branch when publication
// factored the fields union, or the single flat object when the action
// declares one closed shape. Structural inspection reads fields through this
// helper so a branch-only field (a cross-field admission's variant field)
// is checked where it lives, never through the factored parent
// view that carries only the shared constraints. Inspection only: the
// published compact document stays the admitted input.
export function actionFieldsBranches(actionInput: unknown, fieldName: string = "fields"): any[] {
  const fields = (actionInput as any)?.properties?.[fieldName];
  if (fields === null || typeof fields !== "object" || Array.isArray(fields)) return [];
  if (Array.isArray((fields as any).oneOf)) return (fields as any).oneOf;
  if (Array.isArray((fields as any).anyOf)) return (fields as any).anyOf;
  return [fields];
}
'''


SCHEMA_KEYWORDS = {"$schema", "$id", "$defs", "$ref", "title", "description", "examples", "type", "properties", "patternProperties", "propertyNames", "required", "additionalProperties", "unevaluatedProperties", "items", "contains", "minItems", "maxItems", "uniqueItems", "minLength", "maxLength", "pattern", "format", "minimum", "maximum", "enum", "const", "oneOf", "anyOf", "allOf", "not", "if", "then", "else", "default", "minProperties", "maxProperties", "x-maxBytes", "x-minBytes", "x-maxArrayBytes"}

# The store counts workflow string bounds in UTF-8 bytes
# (validateWorkflowPayloadValue); JSON Schema minLength/maxLength count
# Unicode code points. The published standard bounds are therefore DERIVED
# from the enforcing byte bounds — maxLength equals the byte maximum and
# minLength is ceil(byte minimum / 4), the smallest code-point count whose
# byte length can still reach the floor — so a core-admitted string can never
# be refused by the code-point bounds. The byte bounds travel as x-maxBytes
# and x-minBytes from the same registry metadata and the byte-aware
# validators (adapter core-runtime.ts, Go payloadschema) enforce them.
BYTE_LIMIT_GUIDANCE = (
    "The store counts this field's length in UTF-8 bytes: x-maxBytes and "
    "x-minBytes carry the enforcing byte bounds, and the code-point "
    "minLength/maxLength are derived from them so they never refuse a "
    "core-admitted string."
)

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
    if "enum" in schema and value not in schema["enum"]: fail(f"enum mismatch at {path}: accepted values are {json.dumps(schema['enum'], ensure_ascii=False, separators=(',', ':'))}")
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
            # allOf requires every branch, so the first branch failure is the
            # whole reason. Reporting it unwrapped keeps the offending field at
            # the front of the message.
            if keyword=="allOf" and count != len(results): fail(errors[0] if errors else f"allOf mismatch at {path}")
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


def load_workflow_action_contracts() -> tuple[list[dict], dict, dict, list[dict]]:
    result = subprocess.run(
        ["go", "run", "./scripts/workflow-action-contracts"],
        cwd=ROOT,
        text=True,
        capture_output=True,
        timeout=300,
        check=True,
    )
    projection = json.loads(result.stdout)
    if set(projection) != {"schema_version", "actions", "teaching", "schema_refs", "contract_updates"} or projection["schema_version"] != "2.0":
        fail("workflow action contract projection is invalid")
    schema_refs = projection["schema_refs"]
    if set(schema_refs) != {"current"}:
        fail("workflow action contract projection carries no schema-ref classes")
    for side in ("current",):
        refs = schema_refs[side]
        if not isinstance(refs, list) or any(not isinstance(ref, str) or not ref for ref in refs) or len(set(refs)) != len(refs) or refs != sorted(refs):
            fail(f"workflow action contract projection {side} schema refs are not a sorted unique name list")
    teaching = projection["teaching"]
    if set(teaching) != {"spec_mandate_rule", "law_addition_mandate_rule", "obligation_membership_rule", "architecture_domain_rule", "alignment_combination_rule", "action_rules"}:
        fail("workflow action contract projection carries no guard-owned teaching rules")
    for rule in (value for key, value in teaching.items() if key != "action_rules"):
        if not isinstance(rule, str) or not rule.strip():
            fail("a guard-owned teaching rule is empty")
    if not isinstance(teaching["action_rules"], dict) or any(not isinstance(key, str) or not key or not isinstance(value, str) or not value.strip() for key, value in teaching["action_rules"].items()):
        fail("action teaching rules are invalid")
    actions = projection["actions"]
    if not actions or len({action.get("id") for action in actions}) != len(actions):
        fail("workflow action contract projection has no unique actions")
    for action in actions:
        if set(action) != {"id", "variants", "cross_field"}:
            fail("workflow action contract projection contains an open record")
        if not action["variants"]:
            fail(f"workflow action contract projection names no variant: {action.get('id')}")
        for variant in action["variants"]:
            if set(variant) != {"payload", "public_payload"}:
                fail(f"workflow action variant is an open record: {action.get('id')}")
            for payload_key in ("payload", "public_payload"):
                if set(variant[payload_key]) - {"closed", "fields"}:
                    fail("workflow action contract projection contains an open payload")
                if variant[payload_key].get("closed") is not True:
                    fail(f"current workflow action {payload_key} is not closed: {action.get('id')}")
                validate_workflow_cross_field(action, variant[payload_key])
    return actions, teaching, schema_refs, projection["contract_updates"]


def validate_workflow_cross_field(action: dict, payload: dict) -> None:
    declared = {field["name"] for field in payload["fields"]}
    cross = action["cross_field"]
    if cross is None or set(cross) - {"combinations", "kind_matches"}:
        fail("workflow action contract projection contains an open cross-field declaration")
    combinations = cross.get("combinations", [])
    kind_matches = cross.get("kind_matches", [])
    for match in kind_matches:
        if set(match) - {"field", "object", "discriminator", "remedy"}:
            fail(f"workflow action kind match is an open record: {action.get('id')}")
        if match["field"] not in declared or match["object"] not in declared or not match.get("discriminator"):
            fail(f"workflow action {action.get('id')} kind match names undeclared fields or no discriminator")
    for combination in combinations:
        if set(combination) - {"field", "value", "requires", "forbids", "remedy"}:
            fail(f"workflow action combination is an open record: {action.get('id')}")
        if combination["field"] not in declared:
            fail(f"workflow action {action.get('id')} combination names undeclared discriminator {combination['field']}")
        for side in ("requires", "forbids"):
            for name in combination.get(side, []):
                if name not in declared:
                    fail(f"workflow action {action.get('id')} combination names undeclared field {name}")


def schema_ref_closure(defs: dict, roots: list[str]) -> list[str]:
    """The transitive $defs closure the named schema refs reach."""
    seen: set[str] = set()

    def walk(node: object) -> None:
        if isinstance(node, dict):
            ref = node.get("$ref")
            if isinstance(ref, str) and ref.startswith("#/$defs/"):
                name = ref.removeprefix("#/$defs/")
                if name not in seen:
                    seen.add(name)
                    if name in defs:
                        walk(defs[name])
            for key, value in node.items():
                if key != "$ref":
                    walk(value)
        elif isinstance(node, list):
            for entry in node:
                walk(entry)

    for root in roots:
        if root not in defs:
            fail(f"declared payload schema ref {root} resolves in no generated definition")
        seen.add(root)
        walk(defs[root])
    return sorted(seen)


def validate_current_schema_refs(schema_refs: dict, payload_defs: dict) -> None:
    for ref in schema_ref_closure(payload_defs, schema_refs["current"]):
        if ref not in payload_defs:
            fail(f"current payload schema ref {ref} does not resolve in the public authoring document")


def single_variant(action: dict) -> dict:
    """The one payload half of an action that must stay single-shaped.

    Some projections name one action's payload directly (the design-content
    def, the confirm_premise envelope fields, the supersede fieldset). An
    action with several current variants cannot feed them without silently
    choosing a branch, so the demand itself must fail loudly.
    """
    if len(action["variants"]) != 1:
        fail(f"action {action['id']} carries {len(action['variants'])} current variants; this projection requires exactly one")
    return action["variants"][0]


def workflow_payload_field_schema(field: dict, defs: dict, field_teaching: dict | None = None) -> dict:
    value_type = field["value_type"]
    schema_ref = field.get("schema_ref")
    item_ref = field.get("item_ref")
    if value_type == "array":
        # item_ref names the element contract. schema_ref names the whole value,
        # and frozen definitions that point an array field at a non-array schema
        # keep their original per-element reading.
        if item_ref:
            element = item_ref
        elif schema_ref and defs.get(schema_ref, {}).get("type") != "array":
            element = schema_ref
        elif schema_ref:
            schema = {"$ref": f"#/$defs/{schema_ref}"}
            element = None
        else:
            fail(f"workflow action array field {field.get('name')} names no schema")
        if element is not None:
            if element not in defs:
                fail(f"workflow action field {field.get('name')} references unknown schema {element}")
            schema = {"type": "array", "items": {"$ref": f"#/$defs/{element}"}}
    elif schema_ref:
        if schema_ref not in defs:
            fail(f"workflow action field {field.get('name')} references unknown schema {schema_ref}")
        schema = {"$ref": f"#/$defs/{schema_ref}"}
    elif value_type == "string":
        schema = {"type": "string"}
    elif value_type == "integer":
        schema = {"type": "integer"}
    elif value_type == "boolean":
        schema = {"type": "boolean"}
    elif value_type == "ref":
        schema = {"$ref": "#/$defs/reference"}
    elif value_type == "digest":
        schema = {"$ref": "#/$defs/digest"}
    elif value_type == "string_list":
        if field.get("enum"):
            items = {"type": "string", "enum": field["enum"]}
        else:
            items = {"$ref": f"#/$defs/{field.get('item_ref', 'reference')}"}
        schema = {"type": "array", "uniqueItems": True, "items": items}
    else:
        fail(f"workflow action field {field.get('name')} has unsupported type {value_type}")
    byte_bounded = False
    if "min_length" in field or "max_length" in field:
        # The enforcing unit is the UTF-8 byte; the standard code-point
        # bounds are derived so publication never refuses a core-admitted
        # string, and the byte bounds ride as x-* keywords the byte-aware
        # validators enforce (see BYTE_LIMIT_GUIDANCE).
        if "max_length" in field:
            schema["maxLength"] = field["max_length"]
            schema["x-maxBytes"] = field["max_length"]
            byte_bounded = True
        if "min_length" in field:
            schema["minLength"] = -(-field["min_length"] // 4)
            if field["min_length"] > 1:
                schema["x-minBytes"] = field["min_length"]
                byte_bounded = True
    for source, target in (("min_items", "minItems"), ("max_items", "maxItems"), ("minimum", "minimum"), ("maximum", "maximum"), ("enum", "enum")):
        if source == "enum" and value_type == "string_list":
            continue
        if source in field:
            schema[target] = field[source]
    # Bounds beside a $ref are siblings, and sibling keywords of a $ref are
    # ignored under the draft this surface publishes: the authored bound
    # would declare an enforcement no validator applies. Inline the target
    # and attach the siblings to the copy, so one schema carries both the
    # referenced shape and the declared bounds.
    if "$ref" in schema and any(key != "$ref" and key != "description" for key in schema):
        target_name = schema["$ref"].split("/")[-1]
        target = defs.get(target_name)
        if not isinstance(target, dict):
            fail(f"workflow action field {field.get('name')} references unknown schema {target_name}")
        inlined = copy.deepcopy(target)
        for key, value in schema.items():
            if key == "$ref":
                continue
            inlined[key] = value
        schema = inlined
    if field.get("non_blank"):
        schema["pattern"] = r"\S"
    if field.get("forbidden_values"):
        schema["not"] = {"type": "string", "enum": field["forbidden_values"]}
    # Guard-owned relational teaching: the description comes from the store
    # projection (WorkflowContractTeaching), authored beside the enforcing
    # guard, so the published field cannot teach a rule the store dropped.
    rule = (field_teaching or {}).get(field["name"])
    if rule is not None:
        schema["description"] = rule
    elif byte_bounded and "description" not in schema:
        # One concise byte-limit guidance line for every byte-bounded field
        # no guard-owned rule already teaches.
        schema["description"] = BYTE_LIMIT_GUIDANCE
    return schema


def workflow_payload_object_schema(payload: dict, defs: dict, field_teaching: dict | None = None, cross_field: dict | None = None) -> dict:
    result = workflow_payload_flat_object_schema(payload, defs, field_teaching)
    combinations = (cross_field or {}).get("combinations") or []
    if combinations:
        # Registry-declared legal input combinations become closed per-value
        # branches: each branch keeps the discriminator const, requires what
        # the combination requires, and omits the forbidden properties
        # entirely so closed-object validation refuses them. The branches are
        # total over the discriminator's enum and mutually exclusive through
        # the consts, so the published shape and core admission accept exactly
        # the same payloads — no if/then the host can drop.
        return {"oneOf": [
            workflow_payload_combination_branch(payload, defs, field_teaching, combination)
            for combination in combinations
        ]}
    return result


def workflow_payload_flat_object_schema(payload: dict, defs: dict, field_teaching: dict | None = None) -> dict:
    properties = {field["name"]: workflow_payload_field_schema(field, defs, field_teaching) for field in payload["fields"]}
    required = [field["name"] for field in payload["fields"] if field.get("required")]
    result = {"type": "object", "additionalProperties": False, "maxProperties": 32, "properties": properties}
    if required:
        result["required"] = required
    return result


def workflow_payload_combination_branch(payload: dict, defs: dict, field_teaching: dict | None, combination: dict) -> dict:
    forbidden = set(combination.get("forbids", []))
    properties = {}
    required = []
    for field in payload["fields"]:
        name = field["name"]
        if name in forbidden and name != combination["field"]:
            continue
        schema = workflow_payload_field_schema(field, defs, field_teaching)
        if name == combination["field"]:
            schema = {key: value for key, value in schema.items() if key != "enum"}
            schema["const"] = combination["value"]
        properties[name] = schema
        if field.get("required") or name in combination.get("requires", []):
            required.append(name)
    result = {"type": "object", "additionalProperties": False, "maxProperties": 32, "properties": properties}
    if required:
        result["required"] = required
    return result


def field_variants_disjoint(left: dict, right: dict) -> bool:
    """Prove object alternatives incompatible by required values or closed keys."""
    if "$ref" in left or "$ref" in right or left.get("type") != "object" or right.get("type") != "object":
        return False
    left_required = set(left.get("required", []))
    right_required = set(right.get("required", []))
    left_properties = left.get("properties", {})
    right_properties = right.get("properties", {})
    for key in left_required & right_required:
        left_field = left_properties.get(key)
        right_field = right_properties.get(key)
        if (isinstance(left_field, dict) and isinstance(right_field, dict)
                and "$ref" not in left_field and "$ref" not in right_field
                and "const" in left_field and "const" in right_field
                and left_field["const"] != right_field["const"]):
            return True
    for required, other in ((left_required, right), (right_required, left)):
        if other.get("additionalProperties") is False and not other.get("patternProperties"):
            if required - set(other.get("properties", {})):
                return True
    return False


def closed_field_union(branches: list[dict], defs: dict, name: str) -> dict:
    """Publish the exact union without overlapping oneOf members.

    Contained fieldsets add no accepted inputs. Proven-disjoint alternatives
    need no exclusion; other alternatives exclude shared definitions of earlier
    members so an input valid under several original contracts still passes.
    """
    prefix = name + "_fields_member_"
    for key in tuple(defs):
        if key.startswith(prefix):
            del defs[key]
    flattened = []
    for branch in branches:
        for member in branch.get("oneOf", [branch]):
            if member not in flattened:
                flattened.append(member)

    def contains(broad: dict, narrow: dict) -> bool:
        allowed = {"type", "additionalProperties", "maxProperties", "properties", "required"}
        if set(broad) - allowed or set(narrow) - allowed:
            return False
        if broad.get("type") != "object" or broad.get("additionalProperties") is not False:
            return False
        if any(broad.get(key) != narrow.get(key) for key in ("type", "additionalProperties", "maxProperties")):
            return False
        if not set(broad.get("required", [])) <= set(narrow.get("required", [])):
            return False
        return all(broad.get("properties", {}).get(name) == field for name, field in narrow.get("properties", {}).items())

    members = [member for index, member in enumerate(flattened) if not any(
        other_index != index and contains(other, member)
        and (not contains(member, other) or other_index < index)
        for other_index, other in enumerate(flattened)
    )]

    if len(members) == 1:
        return copy.deepcopy(members[0])
    disjoint = []
    for index, member in enumerate(members):
        branch = copy.deepcopy(member)
        exclusions = []
        for earlier_index, earlier in enumerate(members[:index]):
            if field_variants_disjoint(member, earlier):
                continue
            key = prefix + str(earlier_index)
            if key not in defs:
                defs[key] = copy.deepcopy(earlier)
            exclusions.append({"$ref": "#/$defs/" + key})
        if exclusions:
            if "not" in branch:
                exclusions.insert(0, branch.pop("not"))
            branch["not"] = exclusions[0] if len(exclusions) == 1 else {"anyOf": exclusions}
        disjoint.append(branch)
    return {"oneOf": disjoint}


def remove_generated_guidance(node: object) -> None:
    """Rebuild teaching after constraints change, rather than retaining stale examples."""
    if isinstance(node, dict):
        description = node.get("description")
        markers = [description.index(marker) for marker in ("Accepted shape:", "Bounds:")
                   if isinstance(description, str) and marker in description]
        if markers:
            original = description[:min(markers)].rstrip()
            if original:
                node["description"] = original
            else:
                node.pop("description")
            node.pop("examples", None)
        for key, child in node.items():
            if key != "examples":
                remove_generated_guidance(child)
    elif isinstance(node, list):
        for child in node:
            remove_generated_guidance(child)


def project_schema_guidance(document: dict, names: list[str], *, bounds_only: bool = False) -> dict:
    """One Go owner generates and validates the teaching consumed by both clients."""
    result = subprocess.run(
        ["go", "run", "./scripts/schema-guidance", *(["--bounds-only"] if bounds_only else []), *sorted(set(names))],
        input=json.dumps(document), cwd=ROOT, text=True, capture_output=True, timeout=120,
    )
    if result.returncode:
        fail(f"schema guidance generation failed: {bounded_producer_stderr(result.stderr)}")
    projected = json.loads(result.stdout)
    def preserve_order(original: object, annotated: object) -> object:
        if isinstance(original, dict) and isinstance(annotated, dict):
            ordered = {key: preserve_order(value, annotated[key]) for key, value in original.items()}
            ordered.update((key, value) for key, value in annotated.items() if key not in ordered)
            return ordered
        if isinstance(original, list) and isinstance(annotated, list):
            return [preserve_order(left, right) for left, right in zip(original, annotated, strict=True)]
        return annotated
    projected = preserve_order(document, projected)
    def prune_replaced_count_text(node: object) -> None:
        if isinstance(node, dict):
            description = node.get("description")
            if isinstance(description, str) and description.startswith(BYTE_LIMIT_GUIDANCE + " ") and ("Accepted shape:" in description or "Bounds:" in description):
                node["description"] = description[len(BYTE_LIMIT_GUIDANCE) + 1:]
            for key, child in node.items():
                if key != "examples":
                    prune_replaced_count_text(child)
        elif isinstance(node, list):
            for child in node:
                prune_replaced_count_text(child)
    prune_replaced_count_text(projected)
    return projected


def project_workflow_plan_template_defs(defs: dict) -> None:
    template = json.loads((ROOT / "contracts/workflow-step-templates.v1.json").read_text())
    nested = copy.deepcopy(template["plan_schema"]["$defs"])
    names = {name: "workflow_plan_" + name for name in nested}

    def rewrite(node):
        if isinstance(node, list):
            return [rewrite(value) for value in node]
        if isinstance(node, dict):
            result = {key: rewrite(value) for key, value in node.items()}
            if "enum" in result and "type" not in result and all(isinstance(value, str) for value in result["enum"]):
                result["type"] = "string"
            ref = result.get("$ref")
            if isinstance(ref, str) and ref.startswith("#/$defs/"):
                name = ref.removeprefix("#/$defs/")
                if name not in names:
                    fail(f"workflow plan schema references unknown definition {name}")
                result["$ref"] = "#/$defs/" + names[name]
            return result
        return node

    for name, schema in nested.items():
        defs[names[name]] = rewrite(schema)


def project_workflow_action_schema(document: dict, actions: list[dict], teaching: dict, contract_updates: list[dict]) -> tuple[dict, list[dict]]:
    projected = copy.deepcopy(document)
    remove_generated_guidance(projected)
    defs = projected["$defs"]
    # The action-discriminated variants replaced the shared conditional input.
    # Projection starts from the previous document, so drop the retired
    # definition explicitly; a stale copy would keep satisfying checks that
    # read it while no validation path consumes it.
    defs.pop("work_transition_action_shared_input", None)
    common = {
        "work_id": {"$ref": "#/$defs/id"},
        "expected_version": {"$ref": "#/$defs/version"},
        "action_id": {"$ref": "#/$defs/id"},
        "idempotency_key": {"$ref": "#/$defs/id"},
        "evidence": {"type": "array", "maxItems": 32, "items": {"$ref": "#/$defs/evidence"}},
        "requested_budget_seconds": {"$ref": "#/$defs/requested_budget_seconds"},
    }
    common_required = ["work_id", "expected_version", "action_id", "idempotency_key"]

    defs["workflow_contract_version"] = {"$ref": "#/$defs/version"}
    # Guard-owned teaching for relational action fields. The keys name fields
    # whose rules a schema range cannot encode; the values come from the store
    # projection beside the enforcing guards.
    field_teaching = {
        "spec_mandate": teaching["spec_mandate_rule"],
        "law_additions": teaching["law_addition_mandate_rule"],
        "related_ids": teaching["alignment_combination_rule"],
        "architecture_binding": teaching["architecture_domain_rule"],
    }
    defs["workflow_forward_relation"] = {"type": "object", "additionalProperties": False, "required": ["kind"], "properties": {"kind": {"const": "forward_link"}, "class": {"type": "string", "enum": ["hard", "soft", "none"]}, "severity": {"type": "string", "enum": ["breaking", "non-breaking", "informational"]}}}
    defs["workflow_completion_payload"] = {"type": "object", "additionalProperties": False, "properties": {"evidence_commit": {"type": "string", "minLength": 1, "maxLength": 128}, "current_commit": {"type": "string", "minLength": 1, "maxLength": 128}, "staleness": {"type": "object", "additionalProperties": False, "required": ["drifted"], "properties": {"drifted": {"type": "boolean"}, "severity": {"type": "string", "enum": ["block", "warning"]}}}}}
    defs["proposal_affected_text"] = {"type": "string", "minLength": 1, "maxLength": 256}
    defs["proposal_text"] = {"type": "string", "minLength": 1, "maxLength": 512}
    defs["clarification_answer_text"] = {"type": "string", "minLength": 1, "maxLength": 2048, "pattern": r"\S"}

    outer_properties = copy.deepcopy(common)
    outer_properties["fields"] = {}

    # Remove variants with no compiled owner (CD-0218 D1/D2, CD-0242 D14).
    # Preserve existing live slots so generation does not reorder the source.
    live_prefixes = tuple(prefix + action["id"] for action in actions for prefix in
                          ("work_transition_action_variant_", "work_transition_action_public_variant_"))
    for name in list(defs):
        if name.startswith(("work_transition_action_variant_", "work_transition_action_public_variant_")) and not any(
                name == prefix or name.startswith(prefix + "_fields_member_") for prefix in live_prefixes):
            del defs[name]
    for stale in ("work_transition_action_legacy_input", "decision_record_text", "native_report_timestamp"):
        defs.pop(stale, None)

    # Each action publishes its current closed fieldsets from the registry.
    # Cross-field rules apply inside each alternative; no field union invents
    # a contract.
    def action_variant(action: dict, payload_key: str, name: str) -> None:
        action_id = action["id"]
        properties = copy.deepcopy(outer_properties)
        properties["action_id"] = {"type": "string", "const": action_id}
        required = list(common_required)
        if action_id == "confirm_premise":
            # The envelope alternative is closed: fields is not a property of
            # this variant at all, so supplying it is a structural refusal.
            properties.pop("fields", None)
        else:
            # One path for every action: the
            # engine registry's declarations (fields plus the action's
            # cross-field rules) produce the closed shape, so no action's
            # branch is hand-authored.
            payloads = [variant[payload_key] for variant in action["variants"]]
            branches = []
            for payload in payloads:
                validate_workflow_cross_field(action, payload)
                branch = workflow_payload_object_schema(payload, defs, field_teaching, action["cross_field"])
                if branch not in branches:
                    branches.append(branch)
            field_object = closed_field_union(branches, defs, name)
            properties["fields"] = field_object
            def requires_fields(branch: dict) -> bool:
                return bool(branch.get("required")) or any(all(requires_fields(child) for child in branch[keyword]) for keyword in ("oneOf", "anyOf") if keyword in branch)
            if all(requires_fields(branch) for branch in branches):
                required.append("fields")
        defs[name] = {"type": "object", "additionalProperties": False, "required": required, "properties": properties}
        if action_id in teaching.get("action_rules", {}):
            defs[name]["description"] = teaching["action_rules"][action_id]
    public_variants: list[dict] = []
    current_variant_refs: list[dict] = []
    for action in actions:
        action_id = action["id"]
        action_variant(action, "payload", f"work_transition_action_variant_{action_id}")
        current_variant_refs.append({"$ref": f"#/$defs/work_transition_action_variant_{action_id}"})
        public_name = f"work_transition_action_variant_{action_id}"
        if any(variant["payload"] != variant["public_payload"] for variant in action["variants"]):
            public_name = f"work_transition_action_public_variant_{action_id}"
            action_variant(action, "public_payload", public_name)
        public_variants.append({"action_id": action_id, "schema": public_name})

    # Core validation and host publication consume the same authored
    # variants from the live heads and the recovery list. No legacy input
    # union exists: a historical payload layout replays through the frozen
    # historical decoder and is authorable nowhere (CD-0218 D4).
    variant_wrapper = {"type": "object", "additionalProperties": False, "required": common_required, "properties": copy.deepcopy(outer_properties)}
    defs["work_transition_action_input"] = copy.deepcopy(variant_wrapper) | {"anyOf": current_variant_refs}
    defs["work_transition_action_public_input"] = copy.deepcopy(variant_wrapper) | {"anyOf": [{"$ref": f"#/$defs/{variant['schema']}"} for variant in public_variants]}
    update_branches = [workflow_payload_object_schema(payload, defs, field_teaching)
                       for payload in contract_updates]
    defs["workflow_contract_update"] = closed_field_union(update_branches, defs, "workflow_contract_update")
    defs["work_define_revise_input"]["properties"]["contract"] = {"$ref": "#/$defs/workflow_contract_update"}
    project_workflow_plan_template_defs(defs)
    contracts = {"approve_contract"}
    guidance_names = [f"work_transition_action_variant_{action}" for action in sorted(contracts)]
    guidance_names.extend(variant["schema"] for variant in public_variants if variant["action_id"] in contracts)
    guidance_names.append("workflow_contract_update")
    projected = project_schema_guidance(projected, guidance_names)
    return projected, public_variants


def prune_payload_definitions(payload: dict, manifest: dict, public_variants: list[dict], schema_roots: list[str] | None = None) -> None:
    """Keep current public schemas and the registry's authoring seeds."""
    defs = payload["$defs"]
    roots = {schema["ref"].rsplit("/", 1)[-1] for schema in manifest["schemas"].values()
             if schema["ref"].partition("#")[0] in {"", "contracts/agent-tool-surface-payloads.schema.json"}}
    roots.add("work_transition_action_public_input")
    roots.update(variant["schema"] for variant in public_variants)
    roots.update(schema_roots or [])
    reached: set[str] = set()

    def visit(node: object) -> None:
        if isinstance(node, dict):
            ref = node.get("$ref")
            if isinstance(ref, str):
                if not ref.startswith("#/$defs/"):
                    fail(f"non-local payload reference {ref}")
                include(ref.removeprefix("#/$defs/"))
            for key, value in node.items():
                if key not in {"examples", "const", "enum", "default"}:
                    visit(value)
        elif isinstance(node, list):
            for value in node:
                visit(value)

    def include(name: str) -> None:
        if name in reached:
            return
        if name not in defs:
            fail(f"public contract names missing payload schema {name}")
        reached.add(name)
        visit(defs[name])

    for name in sorted(roots):
        include(name)
    payload["$defs"] = {name: node for name, node in defs.items() if name in reached}


def check_schema_keywords(node, path="schema"):
    if not isinstance(node, dict): return
    unsupported=set(node)-SCHEMA_KEYWORDS
    if unsupported: fail(f"unsupported JSON Schema keywords at {path}: {sorted(unsupported)}")
    # Moonshot's flavored tool-schema validator rejects enum nodes without an
    # explicit type ("type is not defined"), so an enum must always name one.
    if "enum" in node and "type" not in node: fail(f"enum without type at {path}")
    for key,value in node.items():
        if key in {"examples", "const", "enum", "default"}:
            continue
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

# The request schema (contracts/agent-tool-surface-payloads.schema.json $defs/evidence) owns every
# evidence field bound. Result evidence_refs echo request evidence, so the result schema
# (contracts/agent-tool-envelope.schema.json $defs/evidenceRef) must declare the same bounds: a
# narrower result bound turns an admitted request into a malformed_response, and a wider one
# advertises values no request can supply. Generation refuses any disagreement.
def resolve_schema_ref(node: object, root: dict) -> object:
    if not isinstance(node, dict): return node
    ref = node.get("$ref")
    if not isinstance(ref, str) or not ref.startswith("#/$defs/"): return node
    target = root.get("$defs", {}).get(ref.removeprefix("#/$defs/"))
    if not isinstance(target, dict): return node
    return resolve_schema_ref(target, root)

# evidence_field_bound returns the bound-relevant keywords (type, minLength,
# maxLength, pattern, format, enum) on a property node, after any $ref has been
# resolved. Two evidence declarations agree when these sets compare equal.
def evidence_field_bound(schema: object, root: dict) -> dict:
    resolved = resolve_schema_ref(schema, root)
    if not isinstance(resolved, dict): return {}
    return {key: value for key, value in resolved.items() if key in {"type", "minLength", "maxLength", "pattern", "format", "enum"}}

def check_evidence_bound_parity(payload: dict, envelope: dict) -> None:
    payload_evidence = payload.get("$defs", {}).get("evidence")
    envelope_evidence_ref = envelope.get("$defs", {}).get("evidenceRef")
    if not isinstance(payload_evidence, dict): fail("contracts/agent-tool-surface-payloads.schema.json: $defs/evidence is not an object")
    if not isinstance(envelope_evidence_ref, dict): fail("contracts/agent-tool-envelope.schema.json: $defs/evidenceRef is not an object")
    fields = ("kind", "authority", "locator_kind", "locator", "version", "digest")
    for field in fields:
        payload_props = payload_evidence.get("properties", {}).get(field)
        envelope_props = envelope_evidence_ref.get("properties", {}).get(field)
        if not isinstance(payload_props, dict): fail(f"contracts/agent-tool-surface-payloads.schema.json: $defs/evidence/properties/{field} is not an object")
        if not isinstance(envelope_props, dict): fail(f"contracts/agent-tool-envelope.schema.json: $defs/evidenceRef/properties/{field} is not an object")
        payload_bound = evidence_field_bound(payload_props, payload)
        envelope_bound = evidence_field_bound(envelope_props, envelope)
        if payload_bound != envelope_bound:
            fail(
                f"evidence bound mismatch on {field}: "
                f"$defs/evidence in contracts/agent-tool-surface-payloads.schema.json declares "
                f"{json.dumps(payload_bound, ensure_ascii=False, sort_keys=True)}; "
                f"$defs/evidenceRef in contracts/agent-tool-envelope.schema.json declares "
                f"{json.dumps(envelope_bound, ensure_ascii=False, sort_keys=True)}"
            )

def union_result_branch_requires(branch: object, defs: dict) -> bool:
    """Check requiredness on a closed object branch, resolving one local ref."""
    target = branch
    if isinstance(branch, dict):
        ref = branch.get("$ref")
        if isinstance(ref, str) and ref.startswith("#/$defs/"):
            target = defs.get(ref.split("/")[-1], {})
    return (isinstance(target, dict) and target.get("type") == "object"
            and target.get("additionalProperties") is False
            and isinstance(target.get("required"), list) and bool(target["required"]))


def validate(manifest: dict) -> str:
    expected_top = {"$schema", "schema_version", "surface", "envelope", "tools", "operations", "schemas", "capabilities", "consequences", "bounds", "generation", "payload_digest", "digest"}
    if set(manifest) != expected_top:
        fail("manifest has unknown or missing top-level sections")
    expected_tools = 7
    if manifest.get("schema_version") != "1.0" or manifest.get("surface", {}).get("tool_count") != expected_tools:
        fail("manifest schema or tool count is invalid")
    capabilities = set(manifest.get("capabilities", []))
    consequences = set(manifest.get("consequences", []))
    tools = manifest.get("tools", [])
    if len(tools) != expected_tools or len({t.get("id") for t in tools}) != expected_tools:
        fail(f"manifest must contain exactly {expected_tools} unique tools")
    for tool in tools:
        if set(tool) != {"id", "description", "operations"} or not tool["operations"]:
            fail(f"tool section is not closed: {tool.get('id')}")
    operations = manifest.get("operations", [])
    # The operation surface is closed: the manifest must enumerate the full
    # published set exactly, and the count changes only with an accepted
    # surface change.
    expected_operations = 20
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
        if set(op) - {"id", "tool", "kind", "availability", "query_id", "input_schema", "result_schema", "capability", "consequence", "metadata", "supported_budget_seconds"}:
            fail(f"operation section is not closed: {op.get('id')}")
        if set(op["metadata"]) != {"context", "idempotency", "pagination", "output_bytes", "versions"}:
            fail(f"operation metadata is not closed: {op.get('id')}")
        if not isinstance(op.get("supported_budget_seconds"), int) or not 1 <= op["supported_budget_seconds"] <= 300:
            fail(f"operation budget ceiling is not an integer in [1, 300]: {op.get('id')}")
        if not re.fullmatch(r"concord_[a-z0-9_]+\.[a-z0-9_]+", op["id"]): fail(f"invalid operation id {op.get('id')}")
        if op["id"].split(".", 1)[0] != op["tool"]: fail(f"operation/tool pairing mismatch: {op['id']}")
        if op["capability"] not in capabilities or op["consequence"] not in consequences: fail(f"operation classification is not declared: {op['id']}")
        if op["kind"] == "read" and not op.get("query_id"): fail(f"read operation lacks query id: {op['id']}")
        if op["kind"] == "mutation" and op.get("query_id") is not None: fail(f"mutation has query id: {op['id']}")
        if op["id"] == "concord_work_transition.workflow_action" and op.get("availability", "always") != "always": fail("workflow_action must be available at intake")
        if op["input_schema"] not in {f"#/schemas/{key}" for key in manifest.get("schemas", {})} or op["result_schema"] not in {f"#/schemas/{key}" for key in manifest.get("schemas", {})}:
            fail(f"operation schema reference missing: {op['id']}")
    # CD-0038 D2: workflow_action has the evidence-fixed TS1 ceiling.
    evidence_fixed = {
        "concord_work_transition.workflow_action": 30,
    }
    uniform = {o["supported_budget_seconds"] for o in operations if o["id"] not in evidence_fixed}
    if (len(uniform) != 1
            or uniform & set(evidence_fixed.values())
            or any(o["supported_budget_seconds"] != evidence_fixed[o["id"]] for o in operations if o["id"] in evidence_fixed)):
        fail("supported_budget_seconds must be one uniform value across all operations except workflow_action TS1 30")
    if any(set(value) != {"ref", "closed"} or value["closed"] is not True for value in manifest.get("schemas", {}).values()):
        fail("schema references must be closed")
    if manifest.get("envelope", {}).get("max_bytes") != 51200 or manifest.get("bounds", {}).get("max_output_bytes") != 51200:
        fail("envelope bounds are not canonical")
    payload = json.loads(PAYLOAD.read_text())
    envelope = json.loads(ENVELOPE.read_text())
    check_evidence_bound_parity(payload, envelope)
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
        if not result_schema.get("required"):
            # Disjoint result shapes can have no common required root field.
            # Each closed union branch must still require its own identity.
            branches = result_schema.get("oneOf", [])
            if not isinstance(branches, list) or not branches or any(not union_result_branch_requires(branch, defs) for branch in branches):
                fail(f"result schema lacks required fields: {op['id']}")
    unsigned = dict(manifest); unsigned.pop("digest", None)
    return "sha256:" + hashlib.sha256(canonical(unsigned)).hexdigest()

def go_projection(manifest: dict, digest: str) -> str:
    ops = ",\n".join(
        f'\t{{ID: "{o["id"]}", Tool: "{o["tool"]}", Operation: "{o["id"].split(".", 1)[1]}", '
        f'Kind: OperationKind("{o["kind"]}"), QueryID: "{o.get("query_id") or ""}", '
        f'Capability: Capability("{o["capability"]}"), Consequence: OperationConsequence("{o["consequence"]}"), '
        f'Availability: Availability("{o.get("availability", "always")}"), '
        f'SupportedBudgetSeconds: {o["supported_budget_seconds"]}, '
        f'InputSchema: "{o["input_schema"].split("/")[-1]}", ResultSchema: "{o["result_schema"].split("/")[-1]}"}}'
        for o in manifest["operations"]
    )
    payload = json.loads(PAYLOAD.read_text())
    rules = []
    for name, schema in sorted(payload["$defs"].items()):
        if schema.get("type") == "object":
            required = ", ".join(json.dumps(v) for v in schema.get("required", []))
            properties = ", ".join(json.dumps(v) for v in schema.get("properties", {}))
            rules.append(f'\t"{name}": {{Required: []string{{{required}}}, Properties: []string{{{properties}}}}},')
    field_owners = {}
    for operation in manifest["operations"]:
        for direction, schema_ref in (("input", operation["input_schema"]), ("result", operation["result_schema"])):
            schema = payload["$defs"][schema_ref.split("/")[-1]]
            owner_key = f'{operation["tool"]}/{direction}'
            for field in schema.get("properties", {}):
                field_owners.setdefault(owner_key, {}).setdefault(field, []).append(operation["id"])
    owner_rules = []
    for owner_key in sorted(field_owners):
        fields = ", ".join(
            json.dumps(field) + ": []string{" + ", ".join(json.dumps(owner) for owner in sorted(set(owners))) + "}"
            for field, owners in sorted(field_owners[owner_key].items())
        )
        owner_rules.append("\t" + json.dumps(owner_key) + ": map[string][]string{" + fields + "},")
    return f'''// Code generated by scripts/generate-agent-contracts.py; DO NOT EDIT.\npackage agentcontract\n\nimport ("encoding/json"; "fmt"; "sort"; "strings")\nconst ManifestDigest = "{digest}"\n\ntype OperationKind string\nconst ( OperationRead OperationKind = "read"; OperationMutation OperationKind = "mutation" )\ntype Capability string\ntype OperationConsequence string\ntype Availability string\nconst AvailabilityAlways Availability = "always"\ntype ContractOperation struct {{ ID, Tool, Operation string; Kind OperationKind; QueryID string; Capability Capability; Consequence OperationConsequence; Availability Availability; SupportedBudgetSeconds int; InputSchema, ResultSchema string }}\nvar ContractOperations = []ContractOperation{{\n{ops},\n}}\nfunc ValidateContractOperation(tool, operation string) (ContractOperation, bool) {{\n    for _, candidate := range ContractOperations {{ if candidate.Tool == tool && candidate.Operation == operation {{ return candidate, true }} }}\n    return ContractOperation{{}}, false\n}}\ntype GeneratedPayloadRule struct {{ Required []string; Properties []string }}\nvar GeneratedPayloadRules = map[string]GeneratedPayloadRule{{\n{chr(10).join(rules)}\n}}\nvar GeneratedPayloadFieldOwners = map[string]map[string][]string{{\n{chr(10).join(owner_rules)}\n}}\n// ValidateGeneratedPayload admits an operation's payload against the closed\n// per-operation rule. The published host tool schema is one permissive shape\n// merged across a tool's operations, so a caller can send a field a sibling\n// operation owns; naming that owner here is the only place the caller can\n// learn it, because the published schema cannot discriminate and no validator\n// runs in front of the core.\n// One refusal names every missing required field in declared order, then\n// every undeclared field sorted by name, so a caller repairs the payload in\n// one retry; each segment keeps its per-field message.\nfunc ValidateGeneratedPayload(tool, operation, schemaName string, data []byte, result bool) error {{\n    rule, ok := GeneratedPayloadRules[schemaName]; if !ok {{ return fmt.Errorf("unknown payload schema %s", schemaName) }}\n    var object map[string]json.RawMessage; if err := json.Unmarshal(data, &object); err != nil {{ return fmt.Errorf("payload is not an object: %w", err) }}\n    allowed := map[string]bool{{}}; for _, name := range rule.Properties {{ allowed[name] = true }}\n    segments := []string{{}}\n    for _, name := range rule.Required {{ if _, ok := object[name]; !ok {{ segments = append(segments, fmt.Sprintf("missing payload field %s", name)) }} }}\n    unknown := []string{{}}\n    for name := range object {{ if !allowed[name] {{ unknown = append(unknown, name) }} }}\n    sort.Strings(unknown)\n    direction := "input"; if result {{ direction = "result" }}\n    for _, name := range unknown {{\n        owners := GeneratedPayloadFieldOwners[tool+"/"+direction][name]\n        if len(owners) != 0 {{ segments = append(segments, fmt.Sprintf("unknown payload field %s for operation %s; field is declared by operation(s) %s", name, operation, strings.Join(owners, ", "))) }} else {{ segments = append(segments, fmt.Sprintf("unknown payload field %s", name)) }}\n    }}\n    if len(segments) != 0 {{ return fmt.Errorf("%s", strings.Join(segments, "; ")) }}\n    return nil\n}}\n'''


# The named host-tool validators: one named check per host-published tool.
# A new host tool joins by adding its contract entry and its validator here;
# the manifest must declare exactly this set once each, so an unknown,
# missing, or duplicated tool fails generation instead of widening silently.
HOST_TOOL_VALIDATORS = {
}


def validate_host_manifest(manifest: dict, schema: dict) -> str:
    schema_validate(manifest, schema, schema)
    tools = manifest.get("tools", [])
    names = [tool.get("name") for tool in tools]
    unknown = [name for name in names if name not in HOST_TOOL_VALIDATORS]
    if unknown:
        fail(f"host tool manifest declares tool(s) with no named validator: {unknown}")
    if len(names) != len(set(names)):
        fail("host tool manifest must declare each host tool once")
    missing = sorted(set(HOST_TOOL_VALIDATORS) - set(names))
    if missing:
        fail(f"host tool manifest must declare every named host tool; missing: {missing}")
    by_name = {tool["name"]: tool for tool in tools}
    for name, validate_tool in HOST_TOOL_VALIDATORS.items():
        validate_tool(by_name[name])
    return "sha256:" + hashlib.sha256(canonical(manifest)).hexdigest()


def ts_projection(manifest: dict, digest: str, host_manifest: dict, host_digest: str, public_variants: list[dict]) -> str:
    data = json.dumps(manifest["operations"], ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    payload_defs = json.dumps(json.loads(PAYLOAD.read_text())["$defs"], ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    variants = json.dumps(public_variants, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    host_schemas = {tool["name"]: tool["args"] for tool in host_manifest["tools"]}
    host_descriptions = {tool["name"]: tool["description"] for tool in host_manifest["tools"]}
    # Host projection preserves the authored declaration order: the published
    # host document must stay byte-identical to the authored contract's own
    # args serialization, so the projection never reorders keys. The
    # authored contract pins each tool's key order; array element order is
    # untouched either way.
    host_data = json.dumps(host_schemas, ensure_ascii=False, sort_keys=False, separators=(",", ":"))
    description_data = json.dumps(host_descriptions, ensure_ascii=False, sort_keys=False, separators=(",", ":"))
    return f'''// Code generated by scripts/generate-agent-contracts.py; DO NOT EDIT.\nexport const manifestDigest = "{digest}" as const;\nexport const maxEnvelopeBytes = 51200 as const;\nexport const contractOperations = {data} as const;\nexport const payloadSchemas = {payload_defs} as const;\n// One closed action-discriminated variant per workflow action, in registry\n// order. Host publication emits one closed branch per entry instead of a\n// merged union, so the published workflow_action input keeps each action's\n// exact requireds and fields from the same authored structure the core reads.\nexport const workflowActionPublicVariants = {variants} as const;\nexport const hostToolManifestDigest = "{host_digest}" as const;\nexport const hostToolDescriptions = {description_data} as const;\nexport const hostToolSchemas = {host_data} as const;\n'''

def fixtures_projection(manifest: dict, public_variants: list[dict]) -> str:
    payload = json.loads(PAYLOAD.read_text())["$defs"]
    # The workflow_action fixture is assembled from the first registry
    # action's closed variant — the same variant host publication emits —
    # because the merged union form's minimal call (a bare action_id string)
    # is not a call any closed variant admits. The fixture proves
    # both directions only when the first action's core and public payloads
    # are one declaration; a divergent first action must fail generation
    # rather than emit a fixture the published surface refuses.
    first_action = public_variants[0]
    first_variant = payload.get(f"work_transition_action_variant_{first_action['action_id']}")
    if first_action["schema"] != f"work_transition_action_variant_{first_action['action_id']}" or not isinstance(first_variant, dict):
        fail("the first registry workflow action diverges between core and public payloads; the workflow_action fixture cannot serve both surfaces")
    def resolve(schema):
        if isinstance(schema, dict) and "$ref" in schema: return payload[schema["$ref"].removeprefix("#/$defs/")]
        return schema
    def sample(schema):
        schema=resolve(schema)
        if "const" in schema: return schema["const"]
        if "oneOf" in schema or "anyOf" in schema:
            branch=(schema.get("oneOf") or schema["anyOf"])[0]; result=sample(branch); base={key:sample(schema.get("properties",{}).get(key,{})) for key in schema.get("required",[])}
            if isinstance(result,dict): base.update(result); return base
            if isinstance(branch,dict): base.update({key:sample(schema.get("properties",{}).get(key,{})) for key in branch.get("required",[])}); return base
            return result
        if "allOf" in schema:
            result=sample({key: value for key, value in schema.items() if key != "allOf"}) or {}
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
            if schema.get("format") == "date-time": return "2026-08-08T00:00:00Z"
            pattern=schema.get("pattern","")
            if "sha256:" in pattern: return "sha256:"+"0"*64
            if "[0-9a-f]{40}" in pattern or "[0-9a-f]{40,64}" in pattern: return "0"*40
            if pattern.startswith("^[a-z][a-z0-9_-]"): return "fence:prod-pause"
            # Uppercase issue-key patterns (CD-0213 D3 human_key, e.g. "EX-1"):
            # the generic "id-1" exemplar fails ^[A-Z][A-Z0-9]*-[1-9][0-9]*$.
            if pattern.startswith("^[A-Z]"): return "EX-1"
            if pattern.startswith("^msg:"): return "msg:" + "0"*32
            if pattern.startswith("^https://"): return "https://example.test/pull/1"
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
    def oversized_case(value, schema):
        """The oversized case only where its string member carries a cap: an
        uncapped member admits any length, so its oversized value is valid."""
        candidate = oversized(value)
        try:
            schema_validate(candidate, schema, {"$defs": payload})
        except ValueError:
            return candidate
        return value
    fixtures = []
    for operation in manifest["operations"]:
        input_name=operation["input_schema"].split("/")[-1]
        result_name = operation["result_schema"].split("/")[-1]
        input_sample_schema = payload[input_name]
        if input_name == "work_transition_action_input":
            input_sample_schema = first_variant
        valid_input=sample(input_sample_schema);valid_result=sample(payload[result_name])
        invalid_input=dict(valid_input) if isinstance(valid_input,dict) else {"value":valid_input};invalid_input["unknown"]=True
        invalid_result=dict(valid_result) if isinstance(valid_result,dict) else {"value":valid_result};invalid_result["unknown"]=True
        input_invalid=[case for case in [invalid_input,nested_unknown(valid_input),oversized_case(valid_input,input_sample_schema)] if case != valid_input]
        result_invalid=[case for case in [invalid_result,nested_unknown(valid_result),oversized_case(valid_result,payload[result_name])] if case != valid_result]
        fixtures.append({"operation": operation["id"], "input_schema": input_name, "input_valid": valid_input, "input_invalid_cases":input_invalid, "result_schema": result_name, "result_valid": valid_result, "result_invalid_cases":result_invalid})
    return json.dumps({"manifest_digest": manifest["digest"], "fixtures": fixtures}, ensure_ascii=False, sort_keys=True, indent=2) + "\n"

def docs_projection(manifest: dict) -> str:
    rows = ["# Generated Concord agent tool surface", "", f"Manifest digest: `{manifest['digest']}`", "Payload schema digest: `" + manifest["payload_digest"] + "`", "Envelope schema: `1.0`", "", "| Operation | Kind | Query | Capability | Consequence | Availability |", "|---|---|---|---|---|---|"]
    rows.extend(f"| `{o['id']}` | `{o['kind']}` | `{o.get('query_id') or '—'}` | `{o['capability']}` | `{o['consequence']}` | `{o.get('availability', 'always')}` |" for o in manifest["operations"])
    return "\n".join(rows) + "\n"

def go_workflow_templates_projection() -> str:
    document = (ROOT / "contracts/workflow-step-templates.v1.json").read_text()
    if "`" in document:
        fail("workflow step templates contract contains a Go raw-string delimiter")
    body = subprocess.run(["gofmt"], input=(
        "// Code generated by scripts/generate-agent-contracts.py; DO NOT EDIT.\n"
        "package store\n\n"
        "// GeneratedWorkflowStepTemplatesDocument is contracts/workflow-step-templates.v1.json,\n"
        "// the single authority for each workflow type's ordered required steps and the\n"
        "// stored step plan's schema. The store parses it at initialization; regenerate\n"
        "// this file after changing the contract.\n"
        "const GeneratedWorkflowStepTemplatesDocument = `" + document + "`\n"
    ), text=True, capture_output=True, check=True).stdout
    return body

def ts_validator_projection(manifest: dict) -> str:
    envelope_schema = json.dumps(json.loads((ROOT / "contracts/agent-tool-envelope.schema.json").read_text()), ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    return f'''// Code generated by scripts/generate-agent-contracts.py; DO NOT EDIT.
import {{ payloadSchemas }} from "./generated-contracts";
export const envelopeSchema = {envelope_schema};
type Validation = {{ valid: boolean; evaluated: Set<string>; path: string | null; reason?: string }};
export function validateGeneratedPayload(name: string, value: unknown): boolean {{
  return payloadFailurePath(name, value) === null;
}}
export function validateGeneratedEnvelope(value: unknown): boolean {{
  return envelopeFailurePath(value) === null;
}}
export function payloadFailurePath(name: string, value: unknown): string | null {{
  return validateSchema((payloadSchemas as Record<string, unknown>)[name], value, payloadSchemas as Record<string, unknown>).path;
}}
export function payloadFailureMessage(name: string, value: unknown): string | null {{
  const result = validateSchema((payloadSchemas as Record<string, unknown>)[name], value, payloadSchemas as Record<string, unknown>);
  return result.valid ? null : `${{result.path}}: ${{result.reason ?? "does not match the declared schema"}}`;
}}
export function envelopeFailurePath(value: unknown): string | null {{
  return validateSchema(envelopeSchema, value, envelopeSchema as Record<string, unknown>).path;
}}
{PUBLICATION_EXPANSION_HELPER}
function pass(evaluated: Iterable<string> = []): Validation {{ return {{ valid: true, evaluated: new Set(evaluated), path: null }}; }}
function fail(at: string, reason?: string): Validation {{ return {{ valid: false, evaluated: new Set(), path: at, reason }}; }}
function joinPath(path: string, key: string): string {{ return path ? path + "." + key : key; }}
function merge(target: Set<string>, source: Set<string>): void {{ for (const key of source) target.add(key); }}
function dateTime(value: string): boolean {{
  const parts = /^([0-9]{{4}})-([0-9]{{2}})-([0-9]{{2}})T([0-9]{{2}}):([0-9]{{2}}):([0-9]{{2}})(?:[.][0-9]+)?(Z|[+-]([0-9]{{2}}):([0-9]{{2}}))$/.exec(value);
  if (!parts) return false;
  const [, year, month, day, hour, minute, second, zone, offsetHour, offsetMinute] = parts;
  const y = Number(year), m = Number(month), d = Number(day);
  const days = [31, y % 4 === 0 && (y % 100 !== 0 || y % 400 === 0) ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
  return m >= 1 && m <= 12 && d >= 1 && d <= days[m - 1] && Number(hour) < 24 && Number(minute) < 60 && Number(second) < 60 && (zone === "Z" || Number(offsetHour) < 24 && Number(offsetMinute) < 60);
}}
function validateSchema(schema: any, value: unknown, root: Record<string, unknown>, path: string = ""): Validation {{
  if (!schema || typeof schema !== "object") return fail(path || "<root>");
  const evaluated = new Set<string>();
  if (schema.$ref) {{ const definitions: Record<string, unknown> = (root.$defs as Record<string, unknown> | undefined) ?? root; const target = definitions[schema.$ref.replace("#/$defs/", "")]; const result = validateSchema(target, value, root, path); if (!result.valid) return result; merge(evaluated, result.evaluated); }}
  if ("const" in schema && JSON.stringify(schema.const) !== JSON.stringify(value)) return fail(path || "<root>");
  if (schema.enum && !schema.enum.some((candidate: unknown) => JSON.stringify(candidate) === JSON.stringify(value))) return fail(path || "<root>");
  if (schema.type) {{ const types = Array.isArray(schema.type) ? schema.type : [schema.type]; if (!types.some((kind: string) => kind === "null" ? value === null : kind === "array" ? Array.isArray(value) : kind === "object" ? value !== null && typeof value === "object" && !Array.isArray(value) : kind === "integer" ? typeof value === "number" && Number.isInteger(value) : typeof value === kind || kind === "number" && typeof value === "number")) return fail(path || "<root>", `type must be ${{types.join(" or ")}}`); }}
  if (typeof value === "string") {{
    const length = Array.from(value).length;
    for (const [keyword, exceeds] of [["minLength", length < schema.minLength], ["maxLength", length > schema.maxLength]] as const) if (exceeds) return fail(path || "<root>", `${{keyword}} bound ${{schema[keyword]}} Unicode code points; received ${{length}}`);
    if (schema.pattern && !(new RegExp(schema.pattern).test(value))) return fail(path || "<root>", `pattern must match ${{schema.pattern}}`);
    if (schema.format === "date-time" && !dateTime(value)) return fail(path || "<root>", "format must be date-time");
    const bytes = Buffer.byteLength(value);
    for (const [keyword, exceeds] of [["x-minBytes", bytes < schema["x-minBytes"]], ["x-maxBytes", bytes > schema["x-maxBytes"]]] as const) if (exceeds) return fail(path || "<root>", `${{keyword}} bound ${{schema[keyword]}} UTF-8 bytes; received ${{bytes}}`);
  }}
  if (typeof value === "number") for (const [keyword, exceeds] of [["minimum", value < schema.minimum], ["maximum", value > schema.maximum]] as const) if (exceeds) return fail(path || "<root>", `${{keyword}} bound ${{schema[keyword]}}`);
  if (value !== null && typeof value === "object" && !Array.isArray(value)) {{
    const object = value as Record<string, unknown>; const properties = schema.properties ?? {{}}; const patterns = schema.patternProperties ?? {{}}; const known = new Set<string>();
    if (schema.required) for (const key of schema.required) if (!(key in object)) return fail(joinPath(path, key), "required field is missing");
    for (const [key, child] of Object.entries(properties)) if (key in object) {{ const result = validateSchema(child, object[key], root, joinPath(path, key)); if (!result.valid) return result; evaluated.add(key); }}
    for (const [pattern, child] of Object.entries(patterns)) for (const key of Object.keys(object)) if (new RegExp(pattern).test(key)) {{ const result = validateSchema(child, object[key], root, joinPath(path, key)); if (!result.valid) return result; evaluated.add(key); }}
    for (const key of Object.keys(properties)) known.add(key);
    for (const pattern of Object.keys(patterns)) for (const key of Object.keys(object)) if (new RegExp(pattern).test(key)) known.add(key);
    const additional = Object.keys(object).filter((key) => !known.has(key));
    if (schema.additionalProperties === false && additional.length > 0) return fail(joinPath(path, additional[0]), "additional property is not allowed");
    if (schema.additionalProperties && typeof schema.additionalProperties === "object") for (const key of additional) {{ const result = validateSchema(schema.additionalProperties, object[key], root, joinPath(path, key)); if (!result.valid) return result; evaluated.add(key); }}
    for (const [keyword, exceeds] of [["minProperties", Object.keys(object).length < schema.minProperties], ["maxProperties", Object.keys(object).length > schema.maxProperties]] as const) if (exceeds) return fail(path || "<root>", `${{keyword}} bound ${{schema[keyword]}}`);
  }}
  if (Array.isArray(value)) {{
    if (schema["x-maxArrayBytes"] !== undefined && Buffer.byteLength(JSON.stringify(value), "utf8") > schema["x-maxArrayBytes"]) return fail(path || "<root>", `x-maxArrayBytes bound ${{schema["x-maxArrayBytes"]}} UTF-8 bytes`);
    for (const [keyword, exceeds] of [["minItems", value.length < schema.minItems], ["maxItems", value.length > schema.maxItems]] as const) if (exceeds) return fail(path || "<root>", `${{keyword}} bound ${{schema[keyword]}}`);
    if (schema.uniqueItems && new Set(value.map((item) => JSON.stringify(item))).size !== value.length) return fail(path || "<root>");
    if (schema.items) for (let index = 0; index < value.length; index++) {{ const result = validateSchema(schema.items, value[index], root, path + "[" + index + "]"); if (!result.valid) return result; }}
  }}
  for (const keyword of ["allOf", "anyOf", "oneOf"] as const) if (schema[keyword]) {{
    if (keyword === "allOf") {{ for (const branch of schema[keyword]) {{ const result = validateSchema(branch, value, root, path); if (!result.valid) return result; merge(evaluated, result.evaluated); }} continue; }}
    const results = schema[keyword].map((branch: unknown) => validateSchema(branch, value, root, path)); const valid = results.filter((result: Validation) => result.valid);
    if (keyword === "anyOf" && valid.length < 1 || keyword === "oneOf" && valid.length !== 1) {{
      // A combinator failure is a property of the whole value, but the
      // operator needs the member. Among the failed branches, the one whose
      // path reaches deepest names the most specific mismatch.
      const failed = results.filter((result: Validation) => !result.valid);
      const specific = failed.reduce((best: Validation | null, current: Validation) => best === null || (current.path ?? "").length > (best.path ?? "").length ? current : best, null);
      return fail(specific?.path ?? path ?? "<root>", specific?.reason);
    }}
    for (const result of valid) merge(evaluated, result.evaluated);
  }}
  if (schema.if) {{ const condition = validateSchema(schema.if, value, root, path); const branch = condition.valid ? schema.then : schema.else; if (branch) {{ const result = validateSchema(branch, value, root, path); if (!result.valid) return result; merge(evaluated, result.evaluated); }} }}
  if (schema.not && validateSchema(schema.not, value, root, path).valid) return fail(path || "<root>");
  if (value !== null && typeof value === "object" && !Array.isArray(value) && schema.unevaluatedProperties !== undefined) {{
    const object = value as Record<string, unknown>; const remaining = Object.keys(object).filter((key) => !evaluated.has(key));
    if (schema.unevaluatedProperties === false && remaining.length > 0) return fail(joinPath(path, remaining[0]));
    if (schema.unevaluatedProperties && typeof schema.unevaluatedProperties === "object") for (const key of remaining) {{ const result = validateSchema(schema.unevaluatedProperties, object[key], root, joinPath(path, key)); if (!result.valid) return result; evaluated.add(key); }}
  }}
  return pass(evaluated);
}}
'''

def go_typed_error_kind_projection(envelope: dict) -> str:
    """Project the closed TS7 error vocabulary into internal/payloadschema.

    The enum is declared once in the envelope schema. Its schema owner supplies
    the same membership predicate to the store fold and agent envelope validator
    without depending on either consumer.
    """
    try:
        kinds = envelope["$defs"]["typedError"]["properties"]["kind"]["enum"]
    except (KeyError, TypeError):
        fail("envelope schema does not declare $defs/typedError/properties/kind/enum")
    if not kinds or sorted(set(kinds)) != sorted(kinds):
        fail("typed error kind enum must be unique and non-empty")
    entries = "\n".join(f'\t{json.dumps(kind)}: true,' for kind in kinds)
    return (
        "// Code generated by scripts/generate-agent-contracts.py; DO NOT EDIT.\n"
        "package payloadschema\n\n"
        "// typedErrorKinds is the closed TS7 error vocabulary, projected from\n"
        "// contracts/agent-tool-envelope.schema.json so the store fold and the\n"
        "// agent envelope validator cannot drift from the declared enum.\n"
        "var typedErrorKinds = map[string]bool{\n"
        f"{entries}\n"
        "}\n\n"
        "// TypedErrorKindAllowed reports whether kind is in the closed TS7 error\n"
        "// vocabulary.\n"
        "func TypedErrorKindAllowed(kind string) bool { return typedErrorKinds[kind] }\n\n"
        "// TypedErrorKinds returns the closed TS7 error vocabulary in schema order.\n"
        "func TypedErrorKinds() []string {\n"
        f"\treturn []string{{{', '.join(json.dumps(kind) for kind in kinds)}}}\n"
        "}\n"
    )


def ts_publication_pin_projection() -> str:
    script = '''import { contractOperations } from "./adapter/opencode/generated-contracts.ts";
import { publishedRequestSchema } from "./adapter/opencode/concord.ts";
const tools = [...new Set(contractOperations.map(operation => operation.tool))];
const total = tools.reduce((sum, tool) => sum + Buffer.byteLength(JSON.stringify(publishedRequestSchema(tool)), "utf8"), 0);
console.log(JSON.stringify(total));'''
    measured = subprocess.run(
        ["bun", "-e", script], cwd=ROOT, text=True,
        capture_output=True, check=True, timeout=30,
    )
    total = json.loads(measured.stdout)
    if type(total) is not int or total <= 0:
        fail("published request measurement must be a positive UTF-8 byte count")
    return f"\nexport const PINNED_PUBLISHED_TOTAL_BYTES = {total};\n"


def prepare_workflow_templates(check: bool) -> None:
    path = ROOT / "internal/store/generated_workflow_templates.go"
    projected = go_workflow_templates_projection()
    if path.is_file() and path.read_text() == projected:
        return
    if check:
        fail(f"generated contract drift: {path.relative_to(ROOT)}")
    path.write_text(projected)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument("--check", action="store_true",
                        help="fail on generated contract drift instead of writing projections")
    args = parser.parse_args()
    try:
        check = args.check
        manifest = json.loads(MANIFEST.read_text())
        ir = json.loads(IR.read_text())
        payload = json.loads(PAYLOAD.read_text())
        bun = shutil.which("bun")
        if bun is None and (not check or os.environ.get("CONCORD_REQUIRE_BUN") == "1"):
            fail("Bun is not installed: the publication measurement requires Bun, so write mode and strict check refuse rather than skip it")
        prepare_workflow_templates(check)
        actions, teaching, schema_refs, contract_updates = load_workflow_action_contracts()
        projected_payload, public_variants = project_workflow_action_schema(payload, actions, teaching, contract_updates)
        validate_current_schema_refs(schema_refs, projected_payload.get("$defs", {}))
        prune_payload_definitions(projected_payload, manifest, public_variants, schema_refs["current"])
        input_names = [operation["input_schema"].rsplit("/", 1)[-1] for operation in manifest["operations"]
                       if operation["id"] != "concord_work_transition.workflow_action"]
        input_names.extend(variant["schema"] for variant in public_variants)
        projected_payload = project_schema_guidance(projected_payload, input_names, bounds_only=True)
        if payload != projected_payload:
            if check:
                fail("generated payload contract drift (workflow action projection): contracts/agent-tool-surface-payloads.schema.json")
            PAYLOAD.write_text(json.dumps(projected_payload, ensure_ascii=False, indent=2) + "\n")
            payload = projected_payload
        envelope = json.loads(ENVELOPE.read_text())
        host_manifest = json.loads(HOST_MANIFEST.read_text())
        host_schema = json.loads(HOST_SCHEMA.read_text())
        host_projected = copy.deepcopy(host_manifest)
        host_roots = {tool["name"]: tool["args"] for tool in host_projected["tools"]}
        for schema in host_roots.values():
            remove_generated_guidance(schema)
        host_document = {"$defs": {**copy.deepcopy(payload["$defs"]), **host_roots}}
        annotated_hosts = project_schema_guidance(host_document, list(host_roots), bounds_only=True)["$defs"]
        for tool in host_projected["tools"]:
            tool["args"] = annotated_hosts[tool["name"]]
        if host_projected != host_manifest:
            if check:
                fail("generated host bound guidance drift: contracts/host-tool-surface.v1.json")
            HOST_MANIFEST.write_text(json.dumps(host_projected, ensure_ascii=False, indent=2) + "\n")
            host_manifest = host_projected
        check_schema_keywords(ir, "ir")
        check_schema_keywords(payload, "payload")
        check_schema_keywords(envelope, "envelope")
        check_schema_keywords(host_schema, "host")
        host_digest = validate_host_manifest(host_manifest, host_schema)
        validate_persisted_manifest(manifest, ir)
        payload_digest = "sha256:" + hashlib.sha256(canonical(payload)).hexdigest()
        manifest["payload_digest"] = payload_digest
        digest = validate(manifest)
        if manifest.get("digest") != digest or manifest.get("payload_digest") != payload_digest:
            if check:
                fail(f"manifest digest is {manifest.get('digest')}, expected {digest}")
            manifest["digest"] = digest
            MANIFEST.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")
        formatted_go = subprocess.run(["gofmt"], input=go_projection(manifest, digest), text=True, capture_output=True, check=True).stdout
        expected = {
            ROOT / "internal/agentcontract/generated_contracts.go": formatted_go,
            ROOT / "adapter/opencode/generated-contracts.ts": ts_projection(manifest, digest, host_manifest, host_digest, public_variants),
            ROOT / "contracts/agent-tool-surface.digest": digest + "\n",
            ROOT / "contracts/agent-tool-surface.fixtures.json": fixtures_projection(manifest, public_variants),
            ROOT / ".concord/docs/generated-agent-tool-surface.md": docs_projection(manifest),
            ROOT / "adapter/opencode/generated-contract-tests.ts": ts_validator_projection(manifest),
            ROOT / "internal/payloadschema/generated_typed_error_kinds.go": subprocess.run(["gofmt"], input=go_typed_error_kind_projection(envelope), text=True, capture_output=True, check=True).stdout,
            ROOT / "internal/store/generated_workflow_templates.go": go_workflow_templates_projection(),
        }
        publication_tests = ROOT / "adapter/opencode/generated-contract-tests.ts"
        if check:
            for path, content in expected.items():
                if path == publication_tests:
                    continue
                if not path.is_file() or path.read_text() != content:
                    fail(f"generated contract drift: {path.relative_to(ROOT)}")
            if bun is None:
                # Without Bun, validate the deterministic body and pin syntax,
                # not the publication byte count.
                if not re.fullmatch(
                    re.escape(expected[publication_tests]) + r"\nexport const PINNED_PUBLISHED_TOTAL_BYTES = [1-9][0-9]*;\n",
                    publication_tests.read_text(),
                ):
                    fail("generated contract drift: adapter/opencode/generated-contract-tests.ts")
                print("publication byte measurement NOT run (Bun unavailable); the validator body and pin format were checked")
                print(digest)
                return 0
        else:
            for path, content in expected.items():
                path.parent.mkdir(exist_ok=True)
                path.write_text(content)
        publication_content = expected[publication_tests] + ts_publication_pin_projection()
        if check:
            if publication_tests.read_text() != publication_content:
                fail("generated contract drift: adapter/opencode/generated-contract-tests.ts")
        else:
            publication_tests.write_text(publication_content)
        print(digest)
        return 0
    except (OSError, json.JSONDecodeError, ValueError, subprocess.SubprocessError) as exc:
        print(f"agent contract generation failed: {exc}", file=sys.stderr)
        if isinstance(exc, subprocess.CalledProcessError) and exc.stderr:
            print(bounded_producer_stderr(exc.stderr), file=sys.stderr)
        return 1
if __name__ == "__main__": raise SystemExit(main())
