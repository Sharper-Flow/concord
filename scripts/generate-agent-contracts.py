#!/usr/bin/env python3
"""Validate and deterministically generate the TS8 agent contract projections."""
from __future__ import annotations
import copy, hashlib, json, re, subprocess, sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / "contracts/agent-tool-surface.v1.json"
IR = ROOT / "contracts/agent-tool-surface.schema.json"
PAYLOAD = ROOT / "contracts/agent-tool-surface-payloads.schema.json"
LANE_PACKET = ROOT / "contracts/agent-lane-packet.schema.json"
WORKFLOW_OUTCOME = ROOT / "contracts/workflow-outcome.schema.json"
ENVELOPE = ROOT / "contracts/agent-tool-envelope.schema.json"
HOST_MANIFEST = ROOT / "contracts/host-tool-surface.v1.json"
HOST_SCHEMA = ROOT / "contracts/host-tool-surface.schema.json"

def canonical(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()

def fail(message: str) -> None:
    raise ValueError(message)

# CD-0184: agents read contracts through the projected tool surface, so the
# delivery-decidable rule travels in the descriptions the published schema
# carries. The emitted admission-teaching gap check asserts the same markers.
DELIVERY_RULE_PREDICATE_DESCRIPTION = (
    "CD-0184: acceptance is decidable at delivery. Each predicate names an end "
    "state verification can decide when the change is delivered. Post-delivery "
    "observation over a time window (traffic, an error rate, a metric over "
    "hours or days) is not acceptance: capture a follow-up work item and link "
    "it raised_from the delivering item before that item completes. A one-shot "
    "live check that verification can decide at delivery stays allowed."
)
DELIVERY_RULE_WAIT_DESCRIPTION = (
    "CD-0184: this wait bounds an event a declared authority can resolve while "
    "the item is open. Do not hold the item open to observe production over a "
    "time window. Capture that observation as a follow-up work item and link "
    "it raised_from the delivering item."
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
    try { return resolve(defs[name]); } finally { resolving.delete(name); }
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
    if (node !== null && typeof node === "object" && !Array.isArray(node) && Array.isArray(node.oneOf)) {
      const expanded: Record<string, any> = {};
      for (const [key, entry] of Object.entries(node)) {
        if (key === "oneOf" || key === "$defs") continue;
        expanded[key] = expandValue(entry);
      }
      expanded.oneOf = node.oneOf.map((branch: any) => expandValue(conjunction(node, resolve(branch))));
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
'''


ADD_CONDITION_VARIANTS = ("work_transition_action_variant_add_condition", "work_transition_action_public_variant_add_condition")


def require_delivery_rule_teaching(defs: dict) -> None:
    """CD-0184: the projected authoring schema must teach that acceptance is
    decidable at delivery, so generation fails before any artifact lands when
    a projection drops the rule."""
    items = defs.get("workflow_action_outcome_predicates")
    text = items.get("description") if isinstance(items, dict) else None
    if text != DELIVERY_RULE_PREDICATE_DESCRIPTION:
        fail("workflow_action_outcome_predicates does not teach the CD-0184 delivery-decidable rule")
    variants = [definition for name, definition in defs.items() if name in ADD_CONDITION_VARIANTS and isinstance(definition, dict)]
    if not variants:
        fail("the projected workflow action input names no add_condition variant for the delivery-rule teaching")
    for variant in variants:
        wait = variant.get("properties", {}).get("fields", {}).get("properties", {}).get("expected_within_seconds")
        wait_text = wait.get("description") if isinstance(wait, dict) else None
        if wait_text != DELIVERY_RULE_WAIT_DESCRIPTION:
            fail("add_condition expected_within_seconds does not teach the CD-0184 delivery-decidable rule")

SCHEMA_KEYWORDS = {"$schema", "$id", "$defs", "$ref", "title", "description", "type", "properties", "patternProperties", "propertyNames", "required", "additionalProperties", "unevaluatedProperties", "items", "contains", "minItems", "maxItems", "uniqueItems", "minLength", "maxLength", "pattern", "format", "minimum", "maximum", "enum", "const", "oneOf", "anyOf", "allOf", "not", "if", "then", "else", "default", "minProperties", "maxProperties", "x-maxBytes", "x-minBytes"}

# The store counts workflow string bounds in UTF-8 bytes
# (validateWorkflowPayloadValue); JSON Schema minLength/maxLength count
# Unicode code points. The published standard bounds are therefore DERIVED
# from the enforcing byte bounds — maxLength equals the byte maximum and
# minLength is ceil(byte minimum / 4), the smallest code-point count whose
# byte length can still reach the floor — so a core-admitted string can never
# be refused by the code-point bounds. The byte bounds travel as x-maxBytes
# and x-minBytes from the same registry metadata and the byte-aware
# validators (adapter dispatch.ts, Go payloadschema) enforce them (CON-412).
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


def load_workflow_action_contracts() -> tuple[list[dict], list[dict]]:
    result = subprocess.run(
        ["go", "run", "./scripts/workflow-action-contracts"],
        cwd=ROOT,
        text=True,
        capture_output=True,
        timeout=300,
        check=True,
    )
    projection = json.loads(result.stdout)
    if set(projection) != {"schema_version", "actions", "workflows", "teaching"} or projection["schema_version"] != "1.0":
        fail("workflow action contract projection is invalid")
    teaching = projection["teaching"]
    if set(teaching) != {"predicate_ordinal_rule", "law_addition_mandate_rule", "obligation_membership_rule", "architecture_domain_rule", "alignment_combination_rule", "outcome_kind_equality_rule"}:
        fail("workflow action contract projection carries no guard-owned teaching rules")
    for rule in teaching.values():
        if not isinstance(rule, str) or not rule.strip():
            fail("a guard-owned teaching rule is empty")
    actions = projection["actions"]
    if not actions or len({action.get("id") for action in actions}) != len(actions):
        fail("workflow action contract projection has no unique actions")
    for action in actions:
        if set(action) != {"id", "payload", "public_payload", "legacy_payloads", "cross_field"}:
            fail("workflow action contract projection contains an open record")
        for payload_key in ("payload", "public_payload"):
            if set(action[payload_key]) - {"closed", "fields"}:
                fail("workflow action contract projection contains an open payload")
            if action[payload_key].get("closed") is not True:
                fail(f"current workflow action {payload_key} is not closed: {action.get('id')}")
            declared = {field["name"] for field in action[payload_key]["fields"]}
            cross = action.get("cross_field")
            if cross is None or set(cross) - {"combinations", "alternatives", "kind_matches"}:
                fail("workflow action contract projection contains an open cross-field declaration")
            combinations = (cross or {}).get("combinations", [])
            alternatives = (cross or {}).get("alternatives", [])
            kind_matches = (cross or {}).get("kind_matches", [])
            if combinations and alternatives:
                fail(f"workflow action {action.get('id')} declares both cross-field rule families")
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
            for group in alternatives:
                if set(group) - {"fields", "forbids", "remedy"} or not group.get("fields"):
                    fail(f"workflow action {action.get('id')} declares an open or empty alternative group")
                for name in group["fields"]:
                    if name not in declared:
                        fail(f"workflow action {action.get('id')} alternative names undeclared field {name}")
                for name in group.get("forbids", []):
                    if name not in declared:
                        fail(f"workflow action {action.get('id')} alternative forbids undeclared field {name}")
        if not isinstance(action["legacy_payloads"], list):
            fail(f"legacy workflow action payloads are not a list: {action.get('id')}")
        for legacy in action["legacy_payloads"]:
            if set(legacy) - {"closed", "fields"}:
                fail(f"legacy workflow action payload is an open record: {action.get('id')}")
            if legacy.get("closed") is not True or legacy.get("fields"):
                fail(f"legacy workflow action payload is not an empty closed payload: {action.get('id')}")
    workflows = projection["workflows"]
    if not workflows or len({workflow.get("ref") for workflow in workflows}) != len(workflows):
        fail("workflow outcome contract projection has no unique workflows")
    for workflow in workflows:
        if set(workflow) != {"ref", "allowed_kinds", "allowed_outcome_tokens", "decision_record_required"}:
            fail(f"workflow outcome contract projection contains an open record: {workflow.get('ref')}")
        if not isinstance(workflow["allowed_kinds"], list) or not workflow["allowed_kinds"]:
            fail(f"workflow outcome contract projection names no predicate kinds: {workflow.get('ref')}")
        if not isinstance(workflow["allowed_outcome_tokens"], list):
            fail(f"workflow outcome contract projection has no token list: {workflow.get('ref')}")
    return actions, workflows, teaching


def workflow_allowed_tokens_description(workflows: list[dict]) -> str:
    """Compose the allowed-token annotation from the store's pinned outcome schemas.

    The store owns the map (internal/store/workflow_registry.go); this renders
    the projection into a description so the annotated schema cannot drift
    from what the store enforces at admission.
    """
    with_tokens = sorted((workflow for workflow in workflows if workflow["allowed_outcome_tokens"]), key=lambda workflow: workflow["ref"])
    for workflow in with_tokens:
        if "outcome" not in workflow["allowed_kinds"]:
            fail(f"workflow {workflow['ref']} pins outcome tokens without the outcome predicate kind")
    without = sorted(workflow["ref"] for workflow in workflows if not workflow["allowed_outcome_tokens"])
    for ref in without:
        kinds = next(workflow["allowed_kinds"] for workflow in workflows if workflow["ref"] == ref)
        if "outcome" in kinds:
            fail(f"workflow {ref} allows the outcome predicate kind without pinned outcome tokens")
    clauses = []
    for workflow in with_tokens:
        clause = f"{workflow['ref']} admits " + ", ".join(workflow["allowed_outcome_tokens"])
        if workflow["decision_record_required"]:
            clause += " (decision_record required)"
        clauses.append(clause)
    if without:
        clauses.append(", ".join(without) + " admit no outcome tokens (only exists, absent and check predicate kinds)")
    return (
        "The store pins which outcome tokens the contract of the work item being "
        "approved may carry, per its workflow type: " + "; ".join(clauses) + "."
    )


# A workflow action field whose value an author writes against a shared
# contract takes that contract's guidance from the owning $def, so the
# published field and the def cannot drift apart. The approval premise is the
# objective every worker packet carries, and its def states the count units.
FIELD_GUIDANCE_DEFS = {"premise": "workflow_premise"}


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
        # validators enforce (see BYTE_LIMIT_GUIDANCE, CON-412).
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
    # referenced shape and the declared bounds (CON-412).
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
    guidance = FIELD_GUIDANCE_DEFS.get(field["name"])
    if guidance is not None and "$ref" not in schema:
        description = defs.get(guidance, {}).get("description")
        if not isinstance(description, str) or not description:
            fail(f"workflow action field {field['name']} names guidance def {guidance}, which carries no description")
        schema["description"] = description
    # Guard-owned relational teaching: the description comes from the store
    # projection (WorkflowContractTeaching), authored beside the enforcing
    # guard, so the published field cannot teach a rule the store dropped.
    rule = (field_teaching or {}).get(field["name"])
    if rule is not None:
        schema["description"] = rule
    elif byte_bounded and "description" not in schema:
        # One concise byte-limit guidance line for every byte-bounded field
        # no guard-owned rule already teaches (CON-412).
        schema["description"] = BYTE_LIMIT_GUIDANCE
    return schema


def install_workflow_outcome_schema(defs: dict) -> dict:
    document = json.loads(WORKFLOW_OUTCOME.read_text())
    names = {name: "workflow_outcome_" + re.sub(r"(?<!^)(?=[A-Z])", "_", name).lower() for name in document["$defs"]}

    def rewrite(node):
        if isinstance(node, list):
            return [rewrite(value) for value in node]
        if not isinstance(node, dict):
            return node
        result = {key: rewrite(value) for key, value in node.items()}
        if isinstance(result.get("$ref"), str) and result["$ref"].startswith("#/$defs/"):
            result["$ref"] = "#/$defs/" + names[result["$ref"].removeprefix("#/$defs/")]
        return result

    for name, schema in document["$defs"].items():
        defs[names[name]] = rewrite(schema)
    # The payload oneOf stays inline at each use site. Moonshot's flavored
    # tool-schema validator rejects a $ref whose target is rooted at a bare
    # combinator, reporting "detected infinite recursion without termination
    # condition"; this def would be exactly that shape.
    return {"oneOf": [rewrite(branch) for branch in document["oneOf"]]}


def install_workflow_self_repair_schema(defs: dict) -> None:
    envelope = json.loads(ENVELOPE.read_text())
    refusal_kinds = envelope["$defs"]["typedError"]["properties"]["kind"]["enum"]
    defs["workflow_self_repair"] = {
        "type": "object", "additionalProperties": False,
        "required": ["refusal_kind", "blocked_operation", "evidence_refs"],
        "properties": {
            "refusal_kind": {"type": "string", "enum": refusal_kinds},
            "blocked_operation": {"$ref": "#/$defs/reference"},
            "evidence_refs": {
                "type": "array", "minItems": 1, "maxItems": 32, "uniqueItems": True,
                "items": {"$ref": "#/$defs/reference"},
            },
        },
    }
    contract = defs.get("workflow_contract")
    if not isinstance(contract, dict) or not isinstance(contract.get("properties"), dict):
        fail("workflow_contract schema is unavailable for self-repair projection")
    contract["properties"]["self_repair"] = {"$ref": "#/$defs/workflow_self_repair"}


def workflow_payload_object_schema(payload: dict, defs: dict, field_teaching: dict | None = None, cross_field: dict | None = None) -> dict:
    result = workflow_payload_flat_object_schema(payload, defs, field_teaching)
    combinations = (cross_field or {}).get("combinations") or []
    alternatives = (cross_field or {}).get("alternatives") or []
    if combinations and alternatives:
        fail("a workflow action declares both cross-field rule families")
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
    if alternatives:
        # Registry-declared alternative groups become closed branches: each
        # branch requires its group's fields, omits the other groups'
        # fields, and omits this group's forbidden fields entirely, so a
        # payload supplying none or several of the groups, or carrying a
        # field this group forbids beside it, validates no branch — the
        # exactly-one and beside-batch rules core admission enforces from
        # the same declaration. A group whose declared kind match pairs a
        # scalar field with an object field's discriminator expands into one
        # closed branch per kind, so outcome_kind and outcome_payload.kind
        # cannot disagree in any published branch (CON-412).
        branches: list[dict] = []
        for group in alternatives:
            branches.extend(workflow_payload_alternative_branches(payload, defs, field_teaching, alternatives, group, (cross_field or {}).get("kind_matches", [])))
        return {"oneOf": branches}
    return result


# outcome_payload_kind_branches resolves the object side of a declared kind
# match to its per-kind closed branches: the discriminator consts of the
# referenced oneOf. The kinds are derived from the same outcome schema the
# store's DecodeWorkflowPredicate enforces, never hand-listed, and a branch
# without a discriminator const fails generation (CON-412).
def outcome_payload_kind_branches(field_name: str, object_field: dict, defs: dict, discriminator: str) -> list[tuple[str, dict]]:
    target_name = object_field.get("schema_ref", "")
    if not target_name:
        fail(f"workflow action kind-match object {field_name} names no schema")
    target = defs.get(target_name)
    one_of = target.get("oneOf") if isinstance(target, dict) else None
    if not isinstance(one_of, list) or not one_of:
        fail(f"workflow action kind-match object schema {target_name} is not a discriminated union")
    pairs = []
    for branch in one_of:
        resolved = branch
        if isinstance(branch, dict) and "$ref" in branch:
            resolved = defs.get(branch["$ref"].split("/")[-1])
        kind = None
        if isinstance(resolved, dict):
            const = resolved.get("properties", {}).get(discriminator, {}).get("const")
            if isinstance(const, str):
                kind = const
        if kind is None:
            fail(f"workflow action kind-match object schema {target_name} carries a branch without a {discriminator} const")
        pairs.append((kind, copy.deepcopy(resolved)))
    kinds = [kind for kind, _ in pairs]
    if len(set(kinds)) != len(kinds):
        fail(f"workflow action kind-match object schema {target_name} carries duplicate {discriminator} consts")
    return pairs


def workflow_payload_alternative_branches(payload: dict, defs: dict, field_teaching: dict | None, alternatives: list[dict], group: dict, kind_matches: list[dict]) -> list[dict]:
    for match in kind_matches:
        if match["field"] not in group["fields"] or match["object"] not in group["fields"]:
            continue
        scalar_field = next(field for field in payload["fields"] if field["name"] == match["field"])
        object_field = next(field for field in payload["fields"] if field["name"] == match["object"])
        if not scalar_field.get("enum"):
            fail(f"workflow action kind-match field {match['field']} carries no closed enum")
        pairs = outcome_payload_kind_branches(match["object"], object_field, defs, match["discriminator"])
        if sorted(scalar_field["enum"]) != sorted(kind for kind, _ in pairs):
            fail(f"workflow action kind-match field {match['field']} enum does not cover the {match['object']} kinds")
        branches = []
        for kind, payload_branch in pairs:
            base = workflow_payload_alternative_branch(payload, defs, field_teaching, alternatives, group)
            base["properties"][match["field"]] = {"type": "string", "const": kind}
            base["properties"][match["object"]] = payload_branch
            branches.append(base)
        return branches
    return [workflow_payload_alternative_branch(payload, defs, field_teaching, alternatives, group)]


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


def workflow_payload_alternative_branch(payload: dict, defs: dict, field_teaching: dict | None, alternatives: list[dict], group: dict) -> dict:
    exclusive = {name for other in alternatives if other is not group for name in other["fields"]}
    forbidden = set(group.get("forbids", []))
    properties = {}
    required = []
    for field in payload["fields"]:
        name = field["name"]
        if name in exclusive or name in forbidden:
            continue
        properties[name] = workflow_payload_field_schema(field, defs, field_teaching)
        if field.get("required") or name in group["fields"]:
            required.append(name)
    result = {"type": "object", "additionalProperties": False, "maxProperties": 32, "properties": properties}
    if required:
        result["required"] = required
    return result


def project_workflow_action_schema(document: dict, actions: list[dict], workflows: list[dict], teaching: dict) -> tuple[dict, list[dict]]:
    projected = copy.deepcopy(document)
    defs = projected["$defs"]
    # Continuity and work pins expose the same card references as dispatch.
    # The authored packet schema owns the source shape, not a second union.
    packet_defs = json.loads((ROOT / "contracts/agent-lane-packet.schema.json").read_text())["$defs"]
    card_refs = copy.deepcopy(packet_defs["lane_work_context_domain_group"]["properties"]["domain_cards"])
    card_refs["items"]["$ref"] = "#/$defs/work_context_reading_source_repository_file"
    defs["work_context_domain_group"]["properties"]["domain_cards"] = card_refs
    # The action-discriminated variants replaced the shared conditional input.
    # Projection starts from the previous document, so drop the retired
    # definition explicitly; a stale copy would keep satisfying checks that
    # read it while no validation path consumes it.
    defs.pop("work_transition_action_shared_input", None)
    install_workflow_self_repair_schema(defs)
    common = {
        "work_id": {"$ref": "#/$defs/id"},
        "expected_version": {"$ref": "#/$defs/version"},
        "action_id": {"$ref": "#/$defs/id"},
        "idempotency_key": {"$ref": "#/$defs/id"},
        "evidence": {"type": "array", "maxItems": 32, "items": {"$ref": "#/$defs/evidence"}},
        "approval": {"$ref": "#/$defs/approval"},
        "research_bindings": {
            "type": "array", "minItems": 1, "maxItems": 16,
            "items": {"type": "object", "additionalProperties": False, "required": ["pack_id", "revision", "use_role", "required"], "properties": {
                "pack_id": {"$ref": "#/$defs/id"}, "revision": {"type": "integer", "minimum": 1},
                "use_role": {"type": "string", "enum": ["context", "design_input", "verification_basis", "decision_basis"]}, "required": {"type": "boolean"},
            }},
        },
        "requested_budget_seconds": {"$ref": "#/$defs/requested_budget_seconds"},
    }
    common_required = ["work_id", "expected_version", "action_id", "idempotency_key"]

    outcome_payload = install_workflow_outcome_schema(defs)
    defs["workflow_action_outcome"] = copy.deepcopy(outcome_payload)
    defs["workflow_contract_version"] = {"$ref": "#/$defs/version"}
    # The pinned-token annotation lands on the outcome def BEFORE any branch
    # copies are taken: the per-kind item branches and the supersede pair
    # branches deepcopy the outcome branches, and each copy must teach the
    # per-workflow allowed tokens (CON-412).
    allowed_property = defs.get("workflow_outcome_outcome", {}).get("properties", {}).get("allowed")
    if not isinstance(allowed_property, dict):
        fail("projected workflow_outcome_outcome schema names no allowed property for the token annotation")
    allowed_property["description"] = workflow_allowed_tokens_description(workflows)
    # Each published predicate item is closed per kind: outcome_kind and
    # outcome_payload.kind are one value, the equality the store's
    # contract_approved fold enforces through DecodeWorkflowPredicate and
    # the engine's kind-match declaration. The item branches derive from the
    # same outcome oneOf the fold decodes — never a hand-listed kind set —
    # so publication cannot admit an outcome_kind/outcome_payload pair the
    # core refuses (CON-412).
    outcome_kinds = outcome_payload_kind_branches("outcome_payload", {"schema_ref": "workflow_action_outcome"}, defs, "kind")
    defs["workflow_action_outcome_predicates"] = {
        "type": "array", "minItems": 1, "maxItems": 8,
        "description": DELIVERY_RULE_PREDICATE_DESCRIPTION,
        "items": {"oneOf": [
            {
                "type": "object", "additionalProperties": False,
                "required": ["predicate_id", "ordinal", "outcome_kind", "outcome_payload"],
                "properties": {
                    "predicate_id": {"$ref": "#/$defs/id", "description": "The store refuses a predicate_id without the \"predicate:\" prefix. Write ids in the form \"predicate:<name>\"."},
                    "ordinal": {"type": "integer", "minimum": 0, "maximum": 7, "description": teaching["predicate_ordinal_rule"]},
                    "outcome_kind": {"type": "string", "const": kind, "description": teaching["outcome_kind_equality_rule"]},
                    "outcome_payload": payload_branch,
                },
            }
            for kind, payload_branch in outcome_kinds
        ]},
    }
    # Guard-owned teaching for relational action fields. The keys name fields
    # whose rules a schema range cannot encode; the values come from the store
    # projection beside the enforcing guards.
    field_teaching = {
        "law_additions": teaching["law_addition_mandate_rule"],
        "required_evidence": teaching["obligation_membership_rule"],
        "related_ids": teaching["alignment_combination_rule"],
        "architecture_binding": teaching["architecture_domain_rule"],
    }
    # CD-0198 D1: one record_verdict call may carry a verdict per judged
    # predicate through the fields.verdicts array. Each item names one
    # approved predicate of the call's contract version and carries its own
    # verdict kind, evaluation evidence, and incomparable flag. The store
    # refuses a call that carries fields.predicate_id beside the array, or
    # neither form, and refuses an entry-level field beside the batch.
    defs["workflow_verdict_batch_entry"] = {
        "type": "object", "additionalProperties": False,
        "required": ["predicate_id"],
        "properties": {
            "predicate_id": {"$ref": "#/$defs/id", "description": "The store refuses a predicate_id without the \"predicate:\" prefix. Write ids in the form \"predicate:<name>\"."},
            "verdict_kind": {"type": "string", "enum": ["ok", "outcome_mismatch", "insufficient_evidence"]},
            "evaluation_evidence": {"type": "array", "minItems": 1, "maxItems": 32, "uniqueItems": True, "items": {"$ref": "#/$defs/reference"}},
            "incomparable_with_approved": {"type": "boolean"},
        },
    }
    defs["workflow_forward_relation"] = {"type": "object", "additionalProperties": False, "required": ["kind"], "properties": {"kind": {"const": "forward_link"}, "class": {"type": "string", "enum": ["hard", "soft", "none"]}, "severity": {"type": "string", "enum": ["breaking", "non-breaking", "informational"]}}}
    defs["workflow_completion_payload"] = {"type": "object", "additionalProperties": False, "properties": {"evidence_commit": {"type": "string", "minLength": 1, "maxLength": 128}, "current_commit": {"type": "string", "minLength": 1, "maxLength": 128}, "staleness": {"type": "object", "additionalProperties": False, "required": ["drifted"], "properties": {"drifted": {"type": "boolean"}, "severity": {"type": "string", "enum": ["block", "warning"]}}}}}
    defs["proposal_affected_text"] = {"type": "string", "minLength": 1, "maxLength": 256}
    defs["proposal_text"] = {"type": "string", "minLength": 1, "maxLength": 512}
    # CD-0205: record_worker_job carries bounded check and unresolved-reference
    # text, and exact prerequisite revision references. The store re-checks
    # each prerequisite against the recorded revisions at dispatch time.
    defs["worker_job_text"] = {"type": "string", "minLength": 1, "maxLength": 256}
    defs["worker_job_prerequisite"] = {
        "type": "object", "additionalProperties": False, "required": ["job_id", "revision"],
        "properties": {
            "job_id": {"$ref": "#/$defs/id"},
            "revision": {"type": "integer", "minimum": 1, "maximum": 2147483647},
            "result_ref": {"type": "string", "minLength": 1, "maxLength": 512},
        },
    }
    defs["decision_record_text"] = {"type": "string", "minLength": 2, "maxLength": 128}
    defs["native_report_timestamp"] = {"type": "string", "minLength": 20, "maxLength": 64, "format": "date-time"}
    design_action = next(action for action in actions if action["id"] == "record_design")
    defs["workflow_design_content"] = workflow_payload_object_schema(design_action["payload"], defs)

    outer_properties = copy.deepcopy(common)
    # The registry declares the envelope-carried fields once: the generator
    # projects the action's declared envelope fields to the envelope outer
    # level, so the work pin (required_fields), this envelope schema, and the
    # store validator read one declaration and cannot drift.
    confirm_action = next(action for action in actions if action["id"] == "confirm_premise")
    for field in confirm_action["payload"]["fields"]:
        if not field.get("envelope"):
            continue
        outer_properties[field["name"]] = workflow_payload_field_schema(field, defs)
    outer_properties["fields"] = {}

    def legacy_condition(action: dict) -> dict | None:
        # Historical execution compatibility: the core input still admits each
        # action's legacy payload layouts through the same registry
        # declaration, but the legacy branch is a separate definition that no
        # published variant references — the surface never advertises legacy
        # forms (CON-412).
        if not action["legacy_payloads"]:
            return None
        def payload_branch(payload: dict) -> dict:
            field_object = workflow_payload_object_schema(payload, defs, field_teaching)
            branch = {"properties": {"fields": field_object}}
            if "required" in field_object:
                branch["required"] = ["fields"]
            return branch
        branches = [payload_branch(legacy) for legacy in action["legacy_payloads"]]
        then = branches[0] if len(branches) == 1 else {"anyOf": branches}
        return {"if": {"properties": {"action_id": {"const": action["id"]}}, "required": ["action_id"]}, "then": then}

    # One authored closed action-discriminated variant per action, generated
    # from the same registry projection for core validation and host
    # publication — no union merging, no handwritten field list, no separate
    # conditional core input (CON-412). Each variant is self-contained and
    # flat: the action_id const discriminates, the required set is exact, and
    # forbidden envelope fields are absent from properties so
    # additionalProperties:false refuses them structurally — no provider-side
    # if/then/not has to survive publication.
    def action_variant(action: dict, payload_key: str, name: str) -> None:
        action_id = action["id"]
        properties = copy.deepcopy(outer_properties)
        properties["action_id"] = {"type": "string", "const": action_id}
        required = list(common_required)
        if action_id == "confirm_premise":
            required.extend(field["name"] for field in action[payload_key]["fields"] if field.get("envelope") and field.get("required"))
            # The envelope alternative is closed: fields is not a property of
            # this variant at all, so supplying it is a structural refusal.
            properties.pop("fields", None)
        else:
            # One path for every action, supersede_contract included: the
            # engine registry's declarations (fields plus the action's
            # cross-field rules) produce the closed shape, so no action's
            # branch is hand-authored.
            field_object = workflow_payload_object_schema(action[payload_key], defs, field_teaching, action.get("cross_field"))
            if action_id == "add_condition":
                wait = field_object.get("properties", {}).get("expected_within_seconds")
                if isinstance(wait, dict):
                    wait["description"] = DELIVERY_RULE_WAIT_DESCRIPTION
            properties["fields"] = field_object
            if "required" in field_object or ("oneOf" in field_object and any("required" in branch for branch in field_object["oneOf"])):
                required.append("fields")
        defs[name] = {"type": "object", "additionalProperties": False, "required": required, "properties": properties}

    public_variants: list[dict] = []
    current_variant_refs: list[dict] = []
    for action in actions:
        action_id = action["id"]
        action_variant(action, "payload", f"work_transition_action_variant_{action_id}")
        current_variant_refs.append({"$ref": f"#/$defs/work_transition_action_variant_{action_id}"})
        public_name = f"work_transition_action_variant_{action_id}"
        if action["payload"] != action["public_payload"]:
            public_name = f"work_transition_action_public_variant_{action_id}"
            action_variant(action, "public_payload", public_name)
        public_variants.append({"action_id": action_id, "schema": public_name})

    # Core validation and host publication consume the same authored variants.
    # The core input additionally admits historical payload layouts through a
    # separate never-published legacy definition; the public input is exactly
    # the published variants.
    variant_wrapper = {"type": "object", "additionalProperties": False, "required": common_required, "properties": copy.deepcopy(outer_properties)}
    legacy_conditions = [condition for condition in (legacy_condition(action) for action in actions) if condition is not None]
    # The legacy definition must never admit an action the registry closes
    # with its own variant: without this gate the legacy envelope — whose
    # fields property is unconstrained — accepts any fields object for any
    # current action, and the core union admits payloads every published
    # variant refuses. The gate names exactly the actions that carry recorded
    # legacy payload layouts, so historic execution stays admitted and current
    # shapes answer only to their closed variants.
    legacy_action_ids = sorted(action["id"] for action in actions if action["legacy_payloads"])
    legacy_gate = {"anyOf": [{"properties": {"action_id": {"type": "string", "enum": legacy_action_ids}}, "required": ["action_id"]}]} if legacy_action_ids else None
    defs["work_transition_action_legacy_input"] = copy.deepcopy(variant_wrapper) | {"allOf": ([legacy_gate] if legacy_gate else []) + legacy_conditions}
    defs["work_transition_action_input"] = copy.deepcopy(variant_wrapper) | {"anyOf": current_variant_refs + [{"$ref": "#/$defs/work_transition_action_legacy_input"}]}
    defs["work_transition_action_public_input"] = copy.deepcopy(variant_wrapper) | {"anyOf": [{"$ref": f"#/$defs/{variant['schema']}"} for variant in public_variants]}
    require_delivery_rule_teaching(defs)
    return projected, public_variants


# CD-0067 D1: the payloads schema's worker_packet def mirrors the canonical
# lane packet contract, and the mirror's inputs object is derived from
# contracts/agent-lane-packet.schema.json, never handwritten. Fields the
# canonical schema binds inline land verbatim; fields it binds through
# lane-local $defs stay anchored to the payloads schema's shared defs, which
# own the published bounds. work_context and checkpoint keep their published
# teaching text, which deliberately trims the canonical wire text.
WORKER_PACKET_INPUT_ALIASES = {
    "binding": "worker_packet_binding",
    "correction": "workflow_correction_context",
    "worker_job": "worker_packet_worker_job",
    "law_context": "workflow_law_context",
    "design_record": "workflow_design_record",
    "proposal_record": "workflow_proposal_record",
    "work_record": "worker_packet_work_record",
    "work_context": "work_context_view",
    "checkpoint": "continuity_checkpoint",
    "outcome_predicates": "worker_packet_outcome_predicates",
}
WORKER_PACKET_INPUT_PUBLISHED_TEXT = {
    "work_context": (
        "CON-887: the current work-context view the work pin carried when the "
        "packet was built. The core refuses a dispatch whose packet does not "
        "consume the current view byte-for-byte."
    ),
    "checkpoint": (
        "CON-883: the latest context checkpoint the pinned continuity "
        "projection carried when the packet was built. The core refuses a "
        "dispatch whose packet does not carry the latest checkpoint "
        "byte-for-byte."
    ),
}


def contains_schema_ref(node: object) -> bool:
    if isinstance(node, dict):
        if isinstance(node.get("$ref"), str):
            return True
        return any(contains_schema_ref(value) for value in node.values())
    if isinstance(node, list):
        return any(contains_schema_ref(value) for value in node)
    return False


def project_worker_packet_inputs(payload: dict, lane_packet: dict) -> None:
    """Derive $defs/worker_packet inputs from the canonical lane packet.

    The canonical contract owns the field set and the required set; the
    payloads schema owns the shared def aliases the aliased fields anchor
    to. A canonical inputs change either lands here on regeneration or
    fails --check, so the published surface cannot again omit a wire field
    the dispatcher sends.
    """
    inputs = lane_packet.get("properties", {}).get("inputs")
    if not isinstance(inputs, dict) or inputs.get("type") != "object" or inputs.get("additionalProperties") is not False:
        fail("contracts/agent-lane-packet.schema.json: inputs is not a closed object")
    packet = payload.get("$defs", {}).get("worker_packet")
    if not isinstance(packet, dict) or not isinstance(packet.get("properties"), dict):
        fail("contracts/agent-tool-surface-payloads.schema.json: $defs/worker_packet is not an object with properties")
    properties = {}
    for name, schema in inputs.get("properties", {}).items():
        alias = WORKER_PACKET_INPUT_ALIASES.get(name)
        if alias is not None:
            if alias not in payload.get("$defs", {}):
                fail(f"worker_packet inputs alias for {name} names unknown def {alias}")
            node = {"$ref": f"#/$defs/{alias}"}
            published = WORKER_PACKET_INPUT_PUBLISHED_TEXT.get(name, schema.get("description"))
            if published is not None:
                node["description"] = published
            properties[name] = node
            continue
        if contains_schema_ref(schema):
            fail(f"canonical lane packet inputs field {name} carries a $ref; anchor it through WORKER_PACKET_INPUT_ALIASES")
        properties[name] = copy.deepcopy(schema)
    projected = {"type": "object", "additionalProperties": False, "properties": properties}
    required = list(inputs.get("required", []))
    if required:
        projected["required"] = required
    packet["properties"]["inputs"] = projected


def check_schema_keywords(node, path="schema"):
    if not isinstance(node, dict): return
    unsupported=set(node)-SCHEMA_KEYWORDS
    if unsupported: fail(f"unsupported JSON Schema keywords at {path}: {sorted(unsupported)}")
    # Moonshot's flavored tool-schema validator rejects enum nodes without an
    # explicit type ("type is not defined"), so an enum must always name one.
    if "enum" in node and "type" not in node: fail(f"enum without type at {path}")
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

def require_mutation_approval_property(operation: dict, input_schema: dict) -> None:
    """Every mutation can reach the cross-Product approval escalation: the
    runtime forces requiresApproval whenever the derived Product scope crosses
    the selected Product, and the approved resubmission carries the core-issued
    reference in input.approval. So every mutation input must declare the
    optional typed approval property — a $defs/approval ref, never required —
    or generation fails before the declared surface and the runtime rule can
    disagree."""
    approval_schema = input_schema.get("properties", {}).get("approval")
    if approval_schema != {"$ref": "#/$defs/approval"} or "approval" in input_schema.get("required", []):
        fail(f"mutation must expose an optional typed approval property: {operation['id']}")

def validate(manifest: dict) -> str:
    expected_top = {"$schema", "schema_version", "surface", "envelope", "tools", "operations", "schemas", "capabilities", "consequences", "bounds", "generation", "payload_digest", "digest"}
    if set(manifest) != expected_top:
        fail("manifest has unknown or missing top-level sections")
    if manifest.get("schema_version") != "1.0" or manifest.get("surface", {}).get("tool_count") != 10:
        fail("manifest schema or tool count is invalid")
    capabilities = set(manifest.get("capabilities", []))
    consequences = set(manifest.get("consequences", []))
    tools = manifest.get("tools", [])
    if len(tools) != 10 or len({t.get("id") for t in tools}) != 10:
        fail("manifest must contain exactly ten unique tools")
    for tool in tools:
        if set(tool) != {"id", "description", "operations"} or not tool["operations"]:
            fail(f"tool section is not closed: {tool.get('id')}")
    operations = manifest.get("operations", [])
    # 75 base operations (74 plus concord_work_transition.worker_reconcile)
    # plus the two operator-approved outside-repair transition actions
    # (CD-0210): concord_work_transition.outside_repair and
    # concord_work_transition.outside_repair_reconcile.
    expected_operations = 77
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
        if set(op) - {"id", "tool", "kind", "availability", "query_id", "input_schema", "result_schema", "capability", "consequence", "approval", "metadata", "supported_budget_seconds"}:
            fail(f"operation section is not closed: {op.get('id')}")
        if set(op["metadata"]) != {"context", "idempotency", "pagination", "output_bytes", "versions"}:
            fail(f"operation metadata is not closed: {op.get('id')}")
        if not isinstance(op.get("supported_budget_seconds"), int) or not 1 <= op["supported_budget_seconds"] <= 1800:
            fail(f"operation budget ceiling is not an integer in [1, 1800]: {op.get('id')}")
        if not re.fullmatch(r"concord_[a-z0-9_]+\.[a-z0-9_]+", op["id"]): fail(f"invalid operation id {op.get('id')}")
        if op["id"].split(".", 1)[0] != op["tool"]: fail(f"operation/tool pairing mismatch: {op['id']}")
        if op["capability"] not in capabilities or op["consequence"] not in consequences: fail(f"operation classification is not declared: {op['id']}")
        if op["kind"] == "read" and not op.get("query_id"): fail(f"read operation lacks query id: {op['id']}")
        if op["kind"] == "mutation" and op.get("query_id") is not None: fail(f"mutation has query id: {op['id']}")
        if op["id"] == "concord_work_transition.workflow_action" and op.get("availability") != "workflow_definition": fail("workflow_action must be accepted but unavailable")
        if op["input_schema"] not in {f"#/schemas/{key}" for key in manifest.get("schemas", {})} or op["result_schema"] not in {f"#/schemas/{key}" for key in manifest.get("schemas", {})}:
            fail(f"operation schema reference missing: {op['id']}")
    # CD-0038 D2: one ceiling value serves the whole surface. Per-operation
    # proliferation is what this rule exists to prevent: a ceiling may only
    # differ where accepted scenario evidence fixes it. Two operations are
    # evidence-fixed today: workflow_action at 30 (TS1) and worktree_verify
    # at 1800 (the 367.08s verification lane measured in CON-317). This rule
    # pins both operations to their evidence-fixed values, so a new distinct
    # value arrives only with accepted evidence and this rule failing loudly,
    # never by quiet accretion.
    evidence_fixed = {
        "concord_work_transition.workflow_action": 30,
        "concord_work_transition.worktree_verify": 1800,
    }
    uniform = {o["supported_budget_seconds"] for o in operations if o["id"] not in evidence_fixed}
    if (len(uniform) != 1
            or uniform & set(evidence_fixed.values())
            or any(o["supported_budget_seconds"] != evidence_fixed[o["id"]] for o in operations if o["id"] in evidence_fixed)):
        fail("supported_budget_seconds must be one uniform value across all operations except the evidence-fixed ceilings (workflow_action TS1 30, worktree_verify CON-317 1800)")
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
        if op["kind"] == "mutation":
            require_mutation_approval_property(op, input_schema)
        if not result_schema.get("required"): fail(f"result schema lacks required fields: {op['id']}")
    unsigned = dict(manifest); unsigned.pop("digest", None)
    return "sha256:" + hashlib.sha256(canonical(unsigned)).hexdigest()

def go_projection(manifest: dict, digest: str) -> str:
    ops = ",\n".join(
        f'\t{{ID: "{o["id"]}", Tool: "{o["tool"]}", Operation: "{o["id"].split(".", 1)[1]}", '
        f'Kind: OperationKind("{o["kind"]}"), QueryID: "{o.get("query_id") or ""}", '
        f'Capability: Capability("{o["capability"]}"), Consequence: OperationConsequence("{o["consequence"]}"), '
        f'Approval: ApprovalClass("{o["approval"]}"), Availability: Availability("{o.get("availability", "always")}"), '
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
    return f'''// Code generated by scripts/generate-agent-contracts.py; DO NOT EDIT.\npackage agent\n\nimport ("encoding/json"; "fmt"; "sort"; "strings")\nconst ManifestDigest = "{digest}"\n\ntype OperationKind string\nconst ( OperationRead OperationKind = "read"; OperationMutation OperationKind = "mutation" )\ntype Capability string\ntype OperationConsequence string\ntype ApprovalClass string\ntype Availability string\nconst ( AvailabilityAlways Availability = "always"; AvailabilityWorkflowDefinition Availability = "workflow_definition" )\ntype ContractOperation struct {{ ID, Tool, Operation string; Kind OperationKind; QueryID string; Capability Capability; Consequence OperationConsequence; Approval ApprovalClass; Availability Availability; SupportedBudgetSeconds int; InputSchema, ResultSchema string }}\nvar ContractOperations = []ContractOperation{{\n{ops},\n}}\nfunc ValidateContractOperation(tool, operation string) (ContractOperation, bool) {{\n    for _, candidate := range ContractOperations {{ if candidate.Tool == tool && candidate.Operation == operation {{ return candidate, true }} }}\n    return ContractOperation{{}}, false\n}}\nfunc WorkflowActionAvailable() bool {{ return false }}\ntype GeneratedPayloadRule struct {{ Required []string; Properties []string }}\nvar GeneratedPayloadRules = map[string]GeneratedPayloadRule{{\n{chr(10).join(rules)}\n}}\nvar GeneratedPayloadFieldOwners = map[string]map[string][]string{{\n{chr(10).join(owner_rules)}\n}}\n// ValidateGeneratedPayload admits an operation's payload against the closed\n// per-operation rule. The published host tool schema is one permissive shape\n// merged across a tool's operations, so a caller can send a field a sibling\n// operation owns; naming that owner here is the only place the caller can\n// learn it, because the published schema cannot discriminate and no validator\n// runs in front of the core.\n// One refusal names every missing required field in declared order, then\n// every undeclared field sorted by name, so a caller repairs the payload in\n// one retry; each segment keeps its per-field message.\nfunc ValidateGeneratedPayload(tool, operation, schemaName string, data []byte, result bool) error {{\n    rule, ok := GeneratedPayloadRules[schemaName]; if !ok {{ return fmt.Errorf("unknown payload schema %s", schemaName) }}\n    var object map[string]json.RawMessage; if err := json.Unmarshal(data, &object); err != nil {{ return fmt.Errorf("payload is not an object: %w", err) }}\n    allowed := map[string]bool{{}}; for _, name := range rule.Properties {{ allowed[name] = true }}\n    segments := []string{{}}\n    for _, name := range rule.Required {{ if _, ok := object[name]; !ok {{ segments = append(segments, fmt.Sprintf("missing payload field %s", name)) }} }}\n    unknown := []string{{}}\n    for name := range object {{ if !allowed[name] {{ unknown = append(unknown, name) }} }}\n    sort.Strings(unknown)\n    direction := "input"; if result {{ direction = "result" }}\n    for _, name := range unknown {{\n        owners := GeneratedPayloadFieldOwners[tool+"/"+direction][name]\n        if len(owners) != 0 {{ segments = append(segments, fmt.Sprintf("unknown payload field %s for operation %s; field is declared by operation(s) %s", name, operation, strings.Join(owners, ", "))) }} else {{ segments = append(segments, fmt.Sprintf("unknown payload field %s", name)) }}\n    }}\n    if len(segments) != 0 {{ return fmt.Errorf("%s", strings.Join(segments, "; ")) }}\n    return nil\n}}\n'''


def validate_host_manifest(manifest: dict, schema: dict) -> str:
    schema_validate(manifest, schema, schema)
    tools = manifest.get("tools", [])
    if [tool.get("name") for tool in tools] != ["concord_work_start"]:
        fail("host tool manifest must declare concord_work_start once")
    args = tools[0].get("args", {})
    capture = ["title", "value_statement", "kind", "task", "idempotency_key", "priority", "urgency", "tags", "workflow_type_ref", "external_ref", "raised_from_work_id", "governing_requirements", "ref", "defect_intake"]
    resume_required = ["work_id"]
    resume_properties = ["work_id", "project_id"]
    branches = args.get("oneOf")
    if args.get("type") != "object" or not isinstance(branches, list) or len(branches) != 2:
        fail("concord_work_start host schema must be a oneOf of the capture and resume shapes")
    if branches[0].get("required") != ["title", "value_statement", "kind", "task", "idempotency_key"] or list(branches[0].get("properties", {})) != capture:
        fail("concord_work_start capture branch has an unexpected argument surface")
    if branches[0].get("additionalProperties") is not False or branches[1].get("additionalProperties") is not False:
        fail("concord_work_start branches must close their argument surface")
    if branches[1].get("required") != resume_required or list(branches[1].get("properties", {})) != resume_properties:
        fail("concord_work_start resume branch must carry work_id and the optional project_id selector")
    if branches[1].get("properties", {}).get("project_id") != branches[1].get("properties", {}).get("work_id"):
        fail("concord_work_start project_id must share the work_id bounds")
    for name, maximum in (("title", 256), ("value_statement", 256), ("external_ref", 256), ("task", 8192)):
        if branches[0]["properties"].get(name, {}).get("x-maxBytes") != maximum:
            fail(f"concord_work_start {name} must pin x-maxBytes={maximum}")
    for branch in branches:
        if "product_id" in branch.get("properties", {}):
            fail("concord_work_start must derive Product identity")
    if "project_id" in branches[0].get("properties", {}):
        fail("concord_work_start capture must derive Project identity")
    return "sha256:" + hashlib.sha256(canonical(manifest)).hexdigest()


def ts_projection(manifest: dict, digest: str, host_manifest: dict, host_digest: str, public_variants: list[dict]) -> str:
    data = json.dumps(manifest["operations"], ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    payload_defs = json.dumps(json.loads(PAYLOAD.read_text())["$defs"], ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    variants = json.dumps(public_variants, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    host_schemas = {tool["name"]: tool["args"] for tool in host_manifest["tools"]}
    host_descriptions = {tool["name"]: tool["description"] for tool in host_manifest["tools"]}
    host_data = json.dumps(host_schemas, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    description_data = json.dumps(host_descriptions, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    return f'''// Code generated by scripts/generate-agent-contracts.py; DO NOT EDIT.\nexport const manifestDigest = "{digest}" as const;\nexport const maxEnvelopeBytes = 51200 as const;\nexport const contractOperations = {data} as const;\nexport const payloadSchemas = {payload_defs} as const;\n// One closed action-discriminated variant per workflow action, in registry\n// order. Host publication emits one closed branch per entry instead of a\n// merged union, so the published workflow_action input keeps each action's\n// exact requireds and fields from the same authored structure the core reads.\nexport const workflowActionPublicVariants = {variants} as const;\nexport const hostToolManifestDigest = "{host_digest}" as const;\nexport const hostToolDescriptions = {description_data} as const;\nexport const hostToolSchemas = {host_data} as const;\n'''

def fixtures_projection(manifest: dict, public_variants: list[dict]) -> str:
    payload = json.loads(PAYLOAD.read_text())["$defs"]
    # The workflow_action fixture is assembled from the first registry
    # action's closed variant — the same variant host publication emits —
    # because the merged union form's minimal call (a bare action_id string)
    # is not a call any closed variant admits (CON-412). The fixture proves
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
        if "oneOf" in schema:
            branch=schema["oneOf"][0]; result=sample(branch); base={key:sample(schema.get("properties",{}).get(key,{})) for key in schema.get("required",[])}
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
            if "[0-9a-f]{40}" in pattern: return "0"*40
            if pattern.startswith("^[a-z][a-z0-9_-]"): return "fence:prod-pause"
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
        input_invalid=[case for case in [invalid_input,nested_unknown(valid_input),oversized(valid_input)] if case != valid_input]
        result_invalid=[case for case in [invalid_result,nested_unknown(valid_result),oversized(valid_result)] if case != valid_result]
        fixtures.append({"operation": operation["id"], "input_schema": input_name, "input_valid": valid_input, "input_invalid_cases":input_invalid, "result_schema": result_name, "result_valid": valid_result, "result_invalid_cases":result_invalid})
    return json.dumps({"manifest_digest": manifest["digest"], "fixtures": fixtures}, ensure_ascii=False, sort_keys=True, indent=2) + "\n"

def docs_projection(manifest: dict) -> str:
    rows = ["# Generated Concord agent tool surface", "", f"Manifest digest: `{manifest['digest']}`", "Payload schema digest: `" + manifest["payload_digest"] + "`", "Envelope schema: `1.0`", "", "| Operation | Kind | Query | Capability | Consequence | Availability |", "|---|---|---|---|---|---|"]
    rows.extend(f"| `{o['id']}` | `{o['kind']}` | `{o.get('query_id') or '—'}` | `{o['capability']}` | `{o['consequence']}` | `{o.get('availability', 'always')}` |" for o in manifest["operations"])
    return "\n".join(rows) + "\n"

def go_payload_schema_projection() -> str:
    document = json.dumps(json.loads(PAYLOAD.read_text()), ensure_ascii=False, sort_keys=True, indent=2)
    return "// Code generated by scripts/generate-agent-contracts.py; DO NOT EDIT.\npackage payloadschema\n\nconst GeneratedPayloadSchemaDocument = `" + document + "`\n"

def go_envelope_schema_projection() -> str:
    document = json.dumps(json.loads((ROOT / "contracts/agent-tool-envelope.schema.json").read_text()), ensure_ascii=False, sort_keys=True, indent=2)
    if "`" in document:
        fail("envelope schema document contains a Go raw-string delimiter")
    return (
        "// Code generated by scripts/generate-agent-contracts.py; DO NOT EDIT.\n"
        "package agent\n\n"
        "// GeneratedEnvelopeSchemaDocument is the canonical TS7 envelope schema,\n"
        "// contracts/agent-tool-envelope.schema.json, projected into the agent\n"
        "// package so Envelope.Validate checks the same contract the generated\n"
        "// adapter validator enforces. The core cannot emit a wire shape the\n"
        "// declared envelope law does not name.\n"
        "const GeneratedEnvelopeSchemaDocument = `" + document + "`\n"
    )

def ts_validator_projection(manifest: dict) -> str:
    envelope_schema = json.dumps(json.loads((ROOT / "contracts/agent-tool-envelope.schema.json").read_text()), ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    return f'''// Code generated by scripts/generate-agent-contracts.py; DO NOT EDIT.
import {{ payloadSchemas }} from "./generated-contracts";
export const envelopeSchema = {envelope_schema};
type Validation = {{ valid: boolean; evaluated: Set<string>; path: string | null }};
export function validateGeneratedPayload(name: string, value: unknown): boolean {{
  return payloadFailurePath(name, value) === null;
}}
export function validateGeneratedEnvelope(value: unknown): boolean {{
  return envelopeFailurePath(value) === null;
}}
export function payloadFailurePath(name: string, value: unknown): string | null {{
  return validateSchema((payloadSchemas as Record<string, unknown>)[name], value, payloadSchemas as Record<string, unknown>).path;
}}
export function envelopeFailurePath(value: unknown): string | null {{
  return validateSchema(envelopeSchema, value, envelopeSchema as Record<string, unknown>).path;
}}
{PUBLICATION_EXPANSION_HELPER}
// advertisedAdmissionTeachingGaps reports every approve_contract rule the
// published concord_work_transition schema fails to teach a calling agent. An
// empty list means the advertised schema carries four store admission rules
// (the item-level required set with ordinal, the strict four-variant
// outcome_payload oneOf, the predicate_id prefix, the per-workflow pinned
// outcome tokens) and the CD-0184 delivery-decidable rule. CD-0184 is
// authoring guidance only: the store does not refuse a predicate for it. The
// store's ValidateOperationPayload stays the closed admission boundary; this
// checks only what the advertised surface teaches. The published request is
// one compact branch per operation, with the workflow actions grouped under
// their operation; the traversal expands the passed document's own local
// references and factored unions, so every check below reads the
// legacy-effective branch view of the actual published document.
export function advertisedAdmissionTeachingGaps(published: unknown): string[] {{
  const gaps: string[] = [];
  const expanded: any = expandedPublishedRequestSchema(published);
  const requestBranches: any[] = Array.isArray(expanded?.oneOf) ? expanded.oneOf : [];
  // workflow_action publishes one closed branch per action variant; the
  // teaching checks read whichever variant carries the taught field.
  const actionInputs: any[] = requestBranches
    .filter((branch) => branch?.properties?.operation?.const === "workflow_action")
    .map((branch) => branch?.properties?.input)
    .filter((input) => input?.properties?.fields?.properties);
  const items = actionInputs.map((input) => input.properties.fields.properties.outcome_predicates?.items).find((candidate) => candidate !== undefined);
  // The published item is closed per kind: outcome_kind consts to the branch
  // and outcome_payload carries that branch's payload, so a mismatched
  // outcome_kind/outcome_payload pair validates no branch (CON-412).
  const kindBranches: any[] = Array.isArray(items?.oneOf) ? items.oneOf : [];
  const firstBranch = kindBranches[0] ?? {{}};
  const required: string[] = Array.isArray(firstBranch.required) ? firstBranch.required : [];
  for (const field of ["predicate_id", "ordinal", "outcome_kind", "outcome_payload"]) {{
    if (!required.includes(field)) gaps.push(`outcome_predicates items do not require ${{field}}`);
  }}
  if (kindBranches.length !== 4) {{
    gaps.push(`outcome_predicates items carry ${{kindBranches.length}} per-kind branches, expected the 4 strict variants`);
  }} else {{
    const kinds = new Set(kindBranches.map((branch) => branch?.properties?.outcome_kind?.const));
    for (const kind of ["exists", "absent", "outcome", "check"]) {{
      if (!kinds.has(kind)) gaps.push(`outcome_predicates items lack the ${{kind}} variant`);
    }}
    for (const branch of kindBranches) {{
      if (branch?.additionalProperties !== false) gaps.push("outcome_predicates item branch is not closed");
      const payload = branch?.properties?.outcome_payload;
      if (payload?.additionalProperties !== false) gaps.push("outcome_payload branch is not closed");
      if (branch?.properties?.outcome_kind?.const !== payload?.properties?.kind?.const) {{
        gaps.push(`outcome_predicates item does not bind outcome_kind to outcome_payload.kind on ${{branch?.properties?.outcome_kind?.const}}`);
      }}
      if (!Array.isArray(payload?.required) || !payload.required.includes("kind")) gaps.push("outcome_payload branch does not require kind");
    }}
  }}
  const prefix: unknown = firstBranch?.properties?.predicate_id?.description;
  if (typeof prefix !== "string" || !prefix.includes("predicate:")) gaps.push("predicate_id description does not name the predicate: prefix");
  const ordinalRule: unknown = firstBranch?.properties?.ordinal?.description;
  if (typeof ordinalRule !== "string" || !ordinalRule.includes("zero-based position")) gaps.push("ordinal description does not teach the zero-based position rule");
  const allowedBranch = kindBranches.find((branch) => branch?.properties?.outcome_kind?.const === "outcome");
  const tokens: unknown = allowedBranch?.properties?.outcome_payload?.properties?.allowed?.description;
  if (typeof tokens !== "string" || !tokens.includes("workflow.research") || !tokens.includes("report_recorded") || !tokens.includes("no outcome tokens")) {{
    gaps.push("allowed description does not name the per-workflow pinned outcome tokens");
  }}
  // More than one action variant can carry the taught field (approve_contract
  // and supersede_contract share outcome_predicates): every carrier must
  // teach the rule, or a drop from one variant hides behind its sibling.
  const predicateCarriers = actionInputs.filter((input) => input.properties.fields.properties.outcome_predicates !== undefined);
  for (const input of predicateCarriers) {{
    const delivery: unknown = input.properties.fields.properties.outcome_predicates?.description;
    if (delivery !== {json.dumps(DELIVERY_RULE_PREDICATE_DESCRIPTION)}) {{
      gaps.push(`outcome_predicates description does not teach the delivery-decidable rule (CD-0184) on action ${{input?.properties?.action_id?.const}}`);
    }}
  }}
  const waitCarriers = actionInputs.filter((input) => input.properties.fields.properties.expected_within_seconds !== undefined);
  for (const input of waitCarriers) {{
    const wait: unknown = input.properties.fields.properties.expected_within_seconds?.description;
    if (wait !== {json.dumps(DELIVERY_RULE_WAIT_DESCRIPTION)}) {{
      gaps.push(`expected_within_seconds description does not teach the delivery-decidable rule (CD-0184) on action ${{input?.properties?.action_id?.const}}`);
    }}
  }}
  return gaps;
}}
function pass(evaluated: Iterable<string> = []): Validation {{ return {{ valid: true, evaluated: new Set(evaluated), path: null }}; }}
function fail(at: string): Validation {{ return {{ valid: false, evaluated: new Set(), path: at }}; }}
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
  if (schema.type) {{ const types = Array.isArray(schema.type) ? schema.type : [schema.type]; if (!types.some((kind: string) => kind === "null" ? value === null : kind === "array" ? Array.isArray(value) : kind === "object" ? value !== null && typeof value === "object" && !Array.isArray(value) : kind === "integer" ? typeof value === "number" && Number.isInteger(value) : typeof value === kind || kind === "number" && typeof value === "number")) return fail(path || "<root>"); }}
  if (typeof value === "string") {{ const length = Array.from(value).length; if (schema.minLength !== undefined && length < schema.minLength || schema.maxLength !== undefined && length > schema.maxLength || schema.pattern && !(new RegExp(schema.pattern).test(value))) return fail(path || "<root>"); if (schema.format === "date-time" && !dateTime(value)) return fail(path || "<root>"); const bytes = Buffer.byteLength(value); if (schema["x-maxBytes"] !== undefined && bytes > schema["x-maxBytes"]) return fail(path || "<root>"); if (schema["x-minBytes"] !== undefined && bytes < schema["x-minBytes"]) return fail(path || "<root>"); }}
  if (typeof value === "number" && (schema.minimum !== undefined && value < schema.minimum || schema.maximum !== undefined && value > schema.maximum)) return fail(path || "<root>");
  if (value !== null && typeof value === "object" && !Array.isArray(value)) {{
    const object = value as Record<string, unknown>; const properties = schema.properties ?? {{}}; const patterns = schema.patternProperties ?? {{}}; const known = new Set<string>();
    if (schema.required) for (const key of schema.required) if (!(key in object)) return fail(joinPath(path, key));
    for (const [key, child] of Object.entries(properties)) if (key in object) {{ const result = validateSchema(child, object[key], root, joinPath(path, key)); if (!result.valid) return result; evaluated.add(key); }}
    for (const [pattern, child] of Object.entries(patterns)) for (const key of Object.keys(object)) if (new RegExp(pattern).test(key)) {{ const result = validateSchema(child, object[key], root, joinPath(path, key)); if (!result.valid) return result; evaluated.add(key); }}
    for (const key of Object.keys(properties)) known.add(key);
    for (const pattern of Object.keys(patterns)) for (const key of Object.keys(object)) if (new RegExp(pattern).test(key)) known.add(key);
    const additional = Object.keys(object).filter((key) => !known.has(key));
    if (schema.additionalProperties === false && additional.length > 0) return fail(joinPath(path, additional[0]));
    if (schema.additionalProperties && typeof schema.additionalProperties === "object") for (const key of additional) {{ const result = validateSchema(schema.additionalProperties, object[key], root, joinPath(path, key)); if (!result.valid) return result; evaluated.add(key); }}
    if (schema.minProperties !== undefined && Object.keys(object).length < schema.minProperties || schema.maxProperties !== undefined && Object.keys(object).length > schema.maxProperties) return fail(path || "<root>");
  }}
  if (Array.isArray(value)) {{
    if (schema.minItems !== undefined && value.length < schema.minItems || schema.maxItems !== undefined && value.length > schema.maxItems) return fail(path || "<root>");
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
      return fail(specific?.path ?? path ?? "<root>");
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
    """Project the closed TS7 error vocabulary into internal/store.

    The enum is declared once in the envelope schema. Both the store fold and
    the agent envelope validator need it, and store is the lower layer, so the
    generated definition lands there and the agent layer refers to it.
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
        "package store\n\n"
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


def main() -> int:
    try:
        check = "--check" in sys.argv[1:]
        manifest = json.loads(MANIFEST.read_text())
        ir = json.loads(IR.read_text())
        payload = json.loads(PAYLOAD.read_text())
        actions, workflows, teaching = load_workflow_action_contracts()
        projected_payload, public_variants = project_workflow_action_schema(payload, actions, workflows, teaching)
        project_worker_packet_inputs(projected_payload, json.loads(LANE_PACKET.read_text()))
        if payload != projected_payload:
            if check:
                fail("generated payload contract drift (workflow action or worker packet projection): contracts/agent-tool-surface-payloads.schema.json")
            PAYLOAD.write_text(json.dumps(projected_payload, ensure_ascii=False, indent=2) + "\n")
            payload = projected_payload
        envelope = json.loads(ENVELOPE.read_text())
        host_manifest = json.loads(HOST_MANIFEST.read_text())
        host_schema = json.loads(HOST_SCHEMA.read_text())
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
        formatted_go = formatted_go.replace("func WorkflowActionAvailable() bool { return false }\n", "")
        expected = {
            ROOT / "internal/agent/generated_contracts.go": formatted_go,
            ROOT / "adapter/opencode/generated-contracts.ts": ts_projection(manifest, digest, host_manifest, host_digest, public_variants),
            ROOT / "contracts/agent-tool-surface.digest": digest + "\n",
            ROOT / "contracts/agent-tool-surface.fixtures.json": fixtures_projection(manifest, public_variants),
            ROOT / ".concord/docs/generated-agent-tool-surface.md": docs_projection(manifest),
            ROOT / "adapter/opencode/generated-contract-tests.ts": ts_validator_projection(manifest),
            ROOT / "internal/payloadschema/generated_schemas.go": go_payload_schema_projection(),
            ROOT / "internal/agent/generated_envelope_schema.go": subprocess.run(["gofmt"], input=go_envelope_schema_projection(), text=True, capture_output=True, check=True).stdout,
            ROOT / "internal/store/generated_typed_error_kinds.go": subprocess.run(["gofmt"], input=go_typed_error_kind_projection(envelope), text=True, capture_output=True, check=True).stdout,
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
