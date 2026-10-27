#!/usr/bin/env python3
"""Validate and deterministically generate the TS8 agent contract projections."""
from __future__ import annotations
import argparse, copy, hashlib, json, os, re, shutil, subprocess, sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / "contracts/agent-tool-surface.v1.json"
IR = ROOT / "contracts/agent-tool-surface.schema.json"
PAYLOAD = ROOT / "contracts/agent-tool-surface-payloads.schema.json"
LANE_PACKET = ROOT / "contracts/agent-lane-packet.schema.json"
LANE_REPORT = ROOT / "contracts/agent-lane-report.schema.json"
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
// actionFieldsBranches returns the expanded branch list of one action
// variant's fields surface: every oneOf/anyOf branch when publication
// factored the fields union, or the single flat object when the action
// declares one closed shape. Structural inspection reads fields through this
// helper so a branch-only field (the consequence_v1 decision_domains
// admission) is checked where it lives, never through the factored parent
// view that carries only the shared constraints. Inspection only: the
// published compact document stays the admitted input.
export function actionFieldsBranches(actionInput: unknown): any[] {
  const fields = (actionInput as any)?.properties?.fields;
  if (fields === null || typeof fields !== "object" || Array.isArray(fields)) return [];
  if (Array.isArray((fields as any).oneOf)) return (fields as any).oneOf;
  if (Array.isArray((fields as any).anyOf)) return (fields as any).anyOf;
  return [fields];
}
'''


ADD_CONDITION_VARIANTS = ("work_transition_action_variant_add_condition", "work_transition_action_public_variant_add_condition")

def require_delivery_rule_teaching(defs: dict) -> None:
    """CD-0184: the projected authoring schema must teach that acceptance is
    decidable at delivery, so generation fails before any artifact lands when
    a projection drops the rule.

    The rule's live owner is the outcome_predicates array shared by
    approve_contract and supersede_contract. The wait-rule teaching retired
    with add_condition (CD-0218 D1), so a projected def for its variants is
    a stale copy the projection must refuse, never a silently skipped check.
    """
    items = defs.get("workflow_action_outcome_predicates")
    text = items.get("description") if isinstance(items, dict) else None
    if text != DELIVERY_RULE_PREDICATE_DESCRIPTION:
        fail("workflow_action_outcome_predicates does not teach the CD-0184 delivery-decidable rule")
    stale = sorted(name for name in ADD_CONDITION_VARIANTS if name in defs)
    if stale:
        fail(f"retired published action add_condition remains projected: {', '.join(stale)} (CD-0218 D1)")

SCHEMA_KEYWORDS = {"$schema", "$id", "$defs", "$ref", "title", "description", "examples", "type", "properties", "patternProperties", "propertyNames", "required", "additionalProperties", "unevaluatedProperties", "items", "contains", "minItems", "maxItems", "uniqueItems", "minLength", "maxLength", "pattern", "format", "minimum", "maximum", "enum", "const", "oneOf", "anyOf", "allOf", "not", "if", "then", "else", "default", "minProperties", "maxProperties", "x-maxBytes", "x-minBytes", "x-maxArrayBytes"}

# The store counts workflow string bounds in UTF-8 bytes
# (validateWorkflowPayloadValue); JSON Schema minLength/maxLength count
# Unicode code points. The published standard bounds are therefore DERIVED
# from the enforcing byte bounds — maxLength equals the byte maximum and
# minLength is ceil(byte minimum / 4), the smallest code-point count whose
# byte length can still reach the floor — so a core-admitted string can never
# be refused by the code-point bounds. The byte bounds travel as x-maxBytes
# and x-minBytes from the same registry metadata and the byte-aware
# validators (adapter dispatch.ts, Go payloadschema) enforce them.
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


def load_workflow_action_contracts() -> tuple[list[dict], list[dict], dict, dict]:
    result = subprocess.run(
        ["go", "run", "./scripts/workflow-action-contracts"],
        cwd=ROOT,
        text=True,
        capture_output=True,
        timeout=300,
        check=True,
    )
    projection = json.loads(result.stdout)
    if set(projection) != {"schema_version", "actions", "workflows", "teaching", "schema_refs"} or projection["schema_version"] != "2.0":
        fail("workflow action contract projection is invalid")
    schema_refs = projection["schema_refs"]
    if set(schema_refs) != {"current"}:
        fail("workflow action contract projection carries no schema-ref classes")
    for side in ("current",):
        refs = schema_refs[side]
        if not isinstance(refs, list) or any(not isinstance(ref, str) or not ref for ref in refs) or len(set(refs)) != len(refs) or refs != sorted(refs):
            fail(f"workflow action contract projection {side} schema refs are not a sorted unique name list")
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
    return actions, workflows, teaching, schema_refs


def validate_workflow_cross_field(action: dict, payload: dict) -> None:
    declared = {field["name"] for field in payload["fields"]}
    cross = action["cross_field"]
    if cross is None or set(cross) - {"combinations", "alternatives", "kind_matches"}:
        fail("workflow action contract projection contains an open cross-field declaration")
    combinations = cross.get("combinations", [])
    alternatives = cross.get("alternatives", [])
    kind_matches = cross.get("kind_matches", [])
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
        clauses.append(f"{workflow['ref']} admits " + ", ".join(workflow["allowed_outcome_tokens"]))
    if without:
        clauses.append(", ".join(without) + " admit no outcome tokens (only exists, absent and check predicate kinds)")
    return (
        "The store pins which outcome tokens the contract of the work item being approved "
        "may carry, per its workflow type: " + "; ".join(clauses) + "."
    )


def narrow_live_outcome_union(defs: dict, workflows: list[dict]) -> None:
    """Narrow the projected outcome union to what the live heads pin (S4).

    The published authoring outcome carries only the outcome tokens some
    live workflow head pins — the research trio today — and never a
    decision_record: new contract authoring names explicit outcome
    predicates only (CD-0218 D1). The frozen historical decoder keeps the
    full retired vocabulary, so retained pins replay unchanged. A live head
    demanding a decision_record or a token outside its pins fails
    generation instead of publishing a shape admission would refuse.
    """
    outcome = defs.get("workflow_outcome_outcome")
    if not isinstance(outcome, dict) or not isinstance(outcome.get("properties"), dict):
        fail("the projected workflow_outcome_outcome schema is unavailable for live narrowing")
    live_tokens = sorted({token for workflow in workflows for token in workflow["allowed_outcome_tokens"]})
    if not live_tokens:
        fail("no live workflow pins outcome tokens; the projected outcome union would be empty")
    allowed = outcome["properties"].get("allowed")
    if not isinstance(allowed, dict) or not isinstance(allowed.get("items"), dict):
        fail("the projected workflow_outcome_outcome schema names no allowed token array")
    allowed["items"]["enum"] = live_tokens
    allowed["maxItems"] = len(live_tokens)
    if any(workflow.get("decision_record_required") for workflow in workflows):
        fail("a live workflow head requires a decision_record; new authoring projects no decision_record (CD-0218 D1)")
    outcome["properties"].pop("decision_record", None)
    # Projection is idempotent: the drop also clears a stale copy the
    # previous document generation may still carry.
    defs.pop("workflow_outcome_decision_record", None)


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
        # no guard-owned rule already teaches.
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


# The retained output def names the output surfaces anchor to after the
# separation: the narrowed live defs stay the authoring owner.
RETAINED_OUTCOME_DEF = "workflow_retained_outcome"
RETAINED_DECISION_RECORD_DEF = "workflow_retained_decision_record"
# DecodeWorkflowPredicate refuses a serialized predicate above
# maxWorkflowPredicateBytes (internal/store/workflow_outcome.go), and a
# serialized string of N bytes decodes to at most N code points, so this is
# the finite ceiling of every retained decision-record string no field guard
# bounds. The output bound ledger proves this value against that constant.
WORKFLOW_PREDICATE_MAX_BYTES = 64 * 1024


# Read outputs that replay stored values verbatim share some definitions with
# worker packets, and dispatch admission validates packets against those
# definitions (internal/store/workflow_preflight.go validates every payload
# field against its declared schema). The packet copies keep the admission
# caps; each read parent here points at a retained twin that drops only the
# caps no store writer enforces. A twin that drops nothing exists because a
# packet also embeds it (worker_packet embeds work_context_view), so the read
# parents need their own copy to reach the other twins. The output bound
# ledger proves every dropped cap unbounded and every kept cap finite.
RETAINED_OUTPUT_TWINS = {
    "workflow_correction_context": ("/properties/diagnosis/maxLength", "/properties/failure_detail/maxLength"),
    "workflow_law_context": ("/properties/registry_path/maxLength",),
    "workflow_law_context_law": ("/properties/path/maxLength",),
    "work_context_finding_view": ("/properties/source_event_id/maxLength",),
    "research_source": tuple(f"/properties/{name}/maxLength" for name in ("locator", "title", "publisher_or_author", "published_at", "accessed_at")),
    "native_oracle_preparation": tuple(f"{branch}/properties/{name}/maxLength" for branch in ("", "/oneOf/0", "/oneOf/1") for name in ("project_id", "toolchain_identity")),
    "work_context_research_finding": ("/properties/statement/maxLength", "/properties/source_ids/maxItems", "/properties/source_ids/items/maxLength"),
    "work_context_research_finding_view": ("/oneOf/0/properties/selection_event_id/maxLength", "/oneOf/0/properties/sources/maxItems", "/oneOf/1/properties/selection_event_id/maxLength"),
    "work_context_view": (),
    "research_scopes": (),
    "worker_oracle_finding": (),
}
# Caps a retained twin raises: the store's retained producer exceeds the cap
# dispatch admission enforces on the shared packet definition, so the read
# copy carries the producer's finite maximum, which the output bound ledger
# proves, while the packet keeps the admitted cap.
RETAINED_OUTPUT_RAISES = {
    "workflow_correction_context": {"/properties/evidence_refs/maxItems": 64},
    "workflow_law_context_law": {"/properties/criteria/maxItems": 1000},
    "research_scopes": {f"/properties/{name}/maxItems": 100 for name in ("product_ids", "project_ids", "domain_ids", "tag_ids")},
    "worker_oracle_finding": {"/properties/owner_id/maxLength": 133},
}
# References a retained twin points at a wider definition: the retained
# producers admit identities above the lane contract's bounded id
# (validateResearchScopes admits 256-rune scope IDs; raw retained research
# source IDs exceed the id bound), while packet dispatch keeps that id.
RETAINED_OUTPUT_RETYPES = {
    "research_scopes": {f"/properties/{name}/items/$ref": "#/$defs/knowledge_id" for name in ("product_ids", "project_ids", "domain_ids", "tag_ids")},
    "research_source": {"/properties/source_id/$ref": "#/$defs/research_record_id"},
}
# The read parents that replay stored values, each with the twinned
# definitions it references directly.
RETAINED_OUTPUT_PARENTS = {
    "continuity_snapshot": ("workflow_law_context", "work_context_view"),
    "research_revision": ("research_source",),
    "research_finding": ("research_scopes",),
    "work_pin": ("workflow_correction_context", "work_context_view"),
}


def retained_twin_name(name: str) -> str:
    return "retained_" + name


def retained_cap_parent(node: dict, name: str, pointer: str) -> tuple[dict, str]:
    tokens = pointer.split("/")[1:]
    parent = node
    for token in tokens[:-1]:
        parent = parent[int(token)] if isinstance(parent, list) else parent.get(token)
        if parent is None:
            fail(f"retained twin {name} names {pointer}, which does not resolve")
    if not isinstance(parent, dict) or tokens[-1] not in parent:
        fail(f"retained twin {name} names {pointer}, which carries no such cap")
    return parent, tokens[-1]


def drop_retained_caps(node: dict, name: str) -> None:
    """Remove from a copy of the named shared definition the caps its
    retained output twin drops."""
    for pointer in RETAINED_OUTPUT_TWINS[name]:
        parent, keyword = retained_cap_parent(node, name, pointer)
        del parent[keyword]


def raise_retained_caps(node: dict, name: str) -> None:
    """Raise in a twin the caps its retained producer exceeds."""
    for pointer, value in RETAINED_OUTPUT_RAISES.get(name, {}).items():
        parent, keyword = retained_cap_parent(node, name, pointer)
        if parent[keyword] >= value:
            fail(f"retained twin {name} raises {pointer} to {value}, which the shared cap {parent[keyword]} already admits")
        parent[keyword] = value


def retype_retained_items(node: dict, name: str) -> None:
    """Point a twin's items at the wider definition its retained producer needs."""
    for pointer, ref in RETAINED_OUTPUT_RETYPES.get(name, {}).items():
        parent, keyword = retained_cap_parent(node, name, pointer)
        if parent[keyword] == ref:
            fail(f"retained twin {name} retypes {pointer} to {ref}, which the shared definition already references")
        parent[keyword] = ref


def install_retained_output_twins(defs: dict) -> None:
    # The projection starts from the previous document, which already carries
    # the twins and the retargeted refs. Undo both first, so every run derives
    # the twins from the shared definitions and no ref to a twin survives
    # outside the declared parents.
    twin_names = {retained_twin_name(name): name for name in RETAINED_OUTPUT_TWINS}
    for twin_name in twin_names:
        defs.pop(twin_name, None)

    def restore(value: object) -> None:
        if isinstance(value, list):
            for child in value:
                restore(child)
        elif isinstance(value, dict):
            ref = value.get("$ref")
            if isinstance(ref, str) and ref.removeprefix("#/$defs/") in twin_names:
                value["$ref"] = "#/$defs/" + twin_names[ref.removeprefix("#/$defs/")]
            for child in value.values():
                restore(child)
    restore(defs)

    def retarget(node: object, where: str, allowed: set[str]) -> set[str]:
        """Rewrite every ref to a twinned definition and return the names rewritten."""
        seen: set[str] = set()
        def walk(value: object) -> None:
            if isinstance(value, list):
                for child in value:
                    walk(child)
            elif isinstance(value, dict):
                ref = value.get("$ref")
                if isinstance(ref, str) and ref.startswith("#/$defs/"):
                    target = ref.removeprefix("#/$defs/")
                    if target in RETAINED_OUTPUT_TWINS:
                        if target not in allowed:
                            fail(f"{where} references {target}, which is not a declared retained twin target there")
                        value["$ref"] = "#/$defs/" + retained_twin_name(target)
                        seen.add(target)
                for key, child in value.items():
                    if key != "$ref":
                        walk(child)
        walk(node)
        return seen

    twins = {}
    for name in RETAINED_OUTPUT_TWINS:
        if name not in defs:
            fail(f"retained twin source {name} is not defined")
        twin = copy.deepcopy(defs[name])
        drop_retained_caps(twin, name)
        raise_retained_caps(twin, name)
        retype_retained_items(twin, name)
        retarget(twin, retained_twin_name(name), set(RETAINED_OUTPUT_TWINS))
        twins[retained_twin_name(name)] = twin
    defs.update(twins)
    for parent, targets in RETAINED_OUTPUT_PARENTS.items():
        if parent not in defs:
            fail(f"retained twin parent {parent} is not defined")
        seen = retarget(defs[parent], parent, set(targets))
        if seen != set(targets):
            fail(f"retained twin parent {parent} references {sorted(seen)}, want {sorted(targets)}")


def preserve_retained_outcome_outputs(defs: dict) -> None:
    """Preserve the canonical outcome and decision-record definitions as the
    retained output shapes, and retarget the output uses of the outcome
    branch on the work pin to them.

    The store folds validate a retained predicate against the stored
    definition (foldWorkflowContractApproved through DecodeWorkflowPredicate)
    and the work pin copies OutcomePayload unchanged, so
    work_pin_verified_criterion.outcome_payload must keep admitting the full
    frozen vocabulary and the optional decision_record that live authoring
    narrows away. Worker packets keep the live shape: dispatch admission
    validates them against it, and a current head's contract carries only
    live predicates. The copies are taken before narrow_live_outcome_union
    mutates the authored vocabulary; the frozen historical decoder is never
    rewritten here.
    """
    live_outcome = defs.get("workflow_outcome_outcome")
    if not isinstance(live_outcome, dict) or not isinstance(live_outcome.get("properties"), dict):
        fail("the canonical workflow_outcome_outcome schema is unavailable for retained-output preservation")
    canonical_decision = defs.get("workflow_outcome_decision_record")
    if not isinstance(canonical_decision, dict) or not isinstance(canonical_decision.get("properties"), dict):
        fail("the canonical workflow_outcome_decision_record schema is unavailable for retained-output preservation")

    # The retained decision record's enforcing guards are the inline
    # validateDecisionRecord comparisons and the anchored actor/approval
    # patterns (internal/store/workflow_outcome.go). The canonical per-element
    # text maxima and the supersedes bounds are no store guard, so the output
    # shape replaces them with the whole-predicate decode ceiling instead of
    # pretending the canonical limits are enforced, and it admits exactly the
    # null/absence shapes the decoder admits.
    retained_decision = copy.deepcopy(canonical_decision)
    decision_properties = retained_decision["properties"]
    for name in ("question", "rationale", "poc_findings"):
        node = decision_properties.get(name)
        if not isinstance(node, dict) or node.get("type") != "string" or node.get("minLength") != 1 or node.get("maxLength") != 4096:
            fail(f"the canonical decision record {name} is not the guarded 1..4096 string; the retained output projection is stale")
    for name, cap in (("options_considered", 16), ("consequences", 16), ("inputs", 32), ("unknowns", 16), ("required_to_decide", 16)):
        node = decision_properties.get(name)
        if not isinstance(node, dict) or node.get("type") != "array" or node.get("maxItems") != cap or not isinstance(node.get("items"), dict):
            fail(f"the canonical decision record {name} is not the guarded ..{cap} array; the retained output projection is stale")
        node["items"].pop("minLength", None)
        node["items"]["maxLength"] = WORKFLOW_PREDICATE_MAX_BYTES
        if name != "options_considered":
            # validateDecisionRecord enforces uniqueStrings on
            # options_considered only.
            node.pop("uniqueItems", None)
    for name in ("unknowns", "required_to_decide"):
        # A null array decodes to nil, and an accepted-decision record may
        # leave both absent.
        decision_properties[name]["type"] = ["array", "null"]
    for name in ("supersedes", "superseded_by"):
        node = decision_properties.get(name)
        if not isinstance(node, dict) or node.get("type") != ["string", "null"]:
            fail(f"the canonical decision record {name} is not the optional nullable string; the retained output projection is stale")
        # Optional and guard-free: absent, null, and empty all decode, and
        # validateDecisionRecord checks no supersedes field.
        node.pop("minLength", None)
        node["maxLength"] = WORKFLOW_PREDICATE_MAX_BYTES
    actor_ref = decision_properties.get("reviewer_actor_ref")
    if not isinstance(actor_ref, dict) or actor_ref.get("pattern") != "^actor:[0-9a-f]{64}$" or actor_ref.get("maxLength") != 70:
        fail("the canonical decision record reviewer_actor_ref is not the exact actor-pattern shape; the retained output projection is stale")
    approval_ref = decision_properties.get("operator_approval_ref")
    if not isinstance(approval_ref, dict) or approval_ref.get("pattern") != "^approval:[a-z0-9][a-z0-9_.:/-]{1,127}$":
        fail("the canonical decision record operator_approval_ref does not carry the enforcing approval pattern; the retained output projection is stale")
    # The enforcing pattern admits 137 characters ("approval:" plus 1 plus
    # 127); the canonical maxLength 136 would refuse a guard-admitted record,
    # so the output shape keeps the pattern alone.
    approval_ref.pop("maxLength", None)
    retained_decision["required"] = [
        "question", "options_considered", "decision", "rationale", "consequences",
        "inputs", "poc_findings", "reviewer_actor_ref", "operator_approval_ref",
    ]

    retained_outcome = copy.deepcopy(live_outcome)
    decision_property = retained_outcome["properties"].get("decision_record")
    if not isinstance(decision_property, dict) or decision_property.get("$ref") != "#/$defs/workflow_outcome_decision_record":
        fail("the canonical workflow_outcome_outcome carries no decision_record ref; the retained output projection is stale")
    # A decision_record:null decodes to a nil record, so the retained output
    # shape admits it beside the retained record object.
    retained_outcome["properties"]["decision_record"] = {
        "description": "The retained decision record the stored predicate carries; a null decodes to nil.",
        "oneOf": [{"$ref": "#/$defs/" + RETAINED_DECISION_RECORD_DEF}, {"type": "null"}],
    }
    defs[RETAINED_OUTCOME_DEF] = retained_outcome
    defs[RETAINED_DECISION_RECORD_DEF] = retained_decision

    def retarget_predicate_union(node: object, where: str) -> None:
        branches = node.get("oneOf") if isinstance(node, dict) else None
        if not isinstance(branches, list) or len(branches) != 4:
            fail(f"{where}.outcome_payload is not the four-branch predicate union; the retained output retarget is stale")
        outcome_branches = 0
        for branch in branches:
            if not isinstance(branch, dict) or not isinstance(branch.get("$ref"), str):
                fail(f"{where}.outcome_payload carries a non-reference predicate branch")
            if branch["$ref"] == "#/$defs/workflow_outcome_outcome":
                branch["$ref"] = "#/$defs/" + RETAINED_OUTCOME_DEF
                outcome_branches += 1
            elif branch["$ref"] == "#/$defs/" + RETAINED_OUTCOME_DEF:
                # Already retargeted by a previous projection run.
                outcome_branches += 1
        if outcome_branches != 1:
            fail(f"{where}.outcome_payload names {outcome_branches} outcome unions; expected exactly one live or retained branch")

    criterion = defs.get("work_pin_verified_criterion")
    if not isinstance(criterion, dict) or not isinstance(criterion.get("properties"), dict):
        fail("the projected work_pin_verified_criterion schema is missing; the retained output retarget has no owner")
    retarget_predicate_union(criterion["properties"].get("outcome_payload"), "work_pin_verified_criterion")


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
        # cannot disagree in any published branch.
        branches: list[dict] = []
        for group in alternatives:
            branches.extend(workflow_payload_alternative_branches(payload, defs, field_teaching, alternatives, group, (cross_field or {}).get("kind_matches", [])))
        return {"oneOf": branches}
    return result


# outcome_payload_kind_branches resolves the object side of a declared kind
# match to its per-kind closed branches: the discriminator consts of the
# referenced oneOf. The kinds are derived from the same outcome schema the
# store's DecodeWorkflowPredicate enforces, never hand-listed, and a branch
# without a discriminator const fails generation.
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


def closed_operation_variants(branches: list[dict], required: list[str]) -> dict:
    """Keep the operation's object root while closed branches own exclusions."""
    properties = {}
    for branch in branches:
        properties.update(copy.deepcopy(branch["properties"]))
    return {"type": "object", "additionalProperties": False, "required": required,
            "properties": properties, "oneOf": branches}


def project_native_oracle_requests(defs: dict) -> None:
    """CD-0096: extend the existing verify mutation and inspect read only."""
    def obj(properties: dict, required: list[str] | None = None) -> dict:
        return {"type": "object", "additionalProperties": False,
                "required": list(properties) if required is None else required, "properties": properties}

    defs["native_oracle_digest"] = {"type": "string", "minLength": 71, "maxLength": 71, "pattern": "^sha256:[0-9a-f]{64}$"}

    control = copy.deepcopy(defs["worker_oracle_control"])
    control["required"].remove("readiness_evidence_refs")
    control["properties"].pop("readiness_evidence_refs")
    control["description"] = "Native prepare input only: readiness references are omitted, never invented. Recorded ready jobs still require them."
    defs["native_oracle_prepare_control"] = control
    defs["native_oracle_control_bundle"] = obj({
        "owner": {"$ref": "#/$defs/worker_oracle_owner"},
        "control": {"$ref": "#/$defs/native_oracle_prepare_control"},
        "cases": {"type": "array", "minItems": 1, "maxItems": 64, "items": {"$ref": "#/$defs/worker_oracle_case"}},
    })
    defs["native_oracle_job_binding"] = obj({
        "job_id": {"$ref": "#/$defs/id"}, "revision": {"type": "integer", "minimum": 1, "maximum": 2147483647},
        "digest": {"$ref": "#/$defs/native_oracle_digest"},
    })
    defs["native_oracle_prepare_request"] = obj({
        "phase": {"type": "string", "const": "prepare"},
        "expected_contract_version": {"type": "integer", "minimum": 1, "maximum": 9223372036854775807},
        "control_bundle": {"$ref": "#/$defs/native_oracle_control_bundle"},
    })
    defs["native_oracle_execute_request"] = obj({
        "phase": {"type": "string", "const": "execute"},
        "attempt_id": {"$ref": "#/$defs/id"},
        "attempt_epoch": {"type": "integer", "minimum": 1, "maximum": 2147483647},
        "worker_packet_digest": {"$ref": "#/$defs/native_oracle_digest"},
        "worker_job_binding": {"$ref": "#/$defs/native_oracle_job_binding"},
        "control_id": copy.deepcopy(defs["worker_oracle_control"]["properties"]["control_id"]),
        "preparation_run_ref": {"type": "string", "minLength": 1, "maxLength": 512},
    })
    prepare = defs["native_oracle_prepare_request"]
    execute = defs["native_oracle_execute_request"]
    defs["native_oracle_request"] = closed_operation_variants([prepare, execute], ["phase"])
    defs["native_oracle_request"]["properties"]["phase"] = {"type": "string", "enum": ["prepare", "execute"]}
    defs["native_oracle_request"]["description"] = (
        "native_oracle_v2: prepare qualifies one closed recipe family without running its workload and grants readiness only. "
        "The pinned pure-Go harness compiles without test execution; a target_runner_v1 recipe materializes its pinned "
        "dependencies offline and runs only its collection or check-input stage. Execute requires the exact persisted dispatch, "
        "job, epoch, packet digest, and clean subject. The producer owns recipe bytes, cwd, environment, candidate, stages, and "
        "streams. Unsupported recipes, families, and materializer/runner pairs are unavailable. Both phases are explicit "
        "preparation and readiness only: no acceptance body, no install, no automatic preparation, no new worker authority."
    )
    verify = defs["work_transition_worktree_verify_input"]
    verify = verify.get("oneOf", [verify])[0]
    # Projection is idempotent: the legacy branch retains the exact authored
    # command fields and bounds, not oracle properties from a prior run.
    legacy = obj({name: copy.deepcopy(verify["properties"][name]) for name in
                  ("work_id", "command", "idempotency_key", "approval", "requested_budget_seconds")},
                 ["work_id", "command", "idempotency_key"])
    native_properties = {name: copy.deepcopy(value) for name, value in legacy["properties"].items() if name != "command"}
    native_properties["oracle"] = {"$ref": "#/$defs/native_oracle_request"}
    native = obj(native_properties, ["work_id", "oracle", "idempotency_key"])
    defs["work_transition_worktree_verify_input"] = closed_operation_variants([legacy, native], ["work_id", "idempotency_key"])
    inspect = defs["work_browse_worktree_inspect_input"]
    inspect = inspect.get("oneOf", [inspect])[0]
    legacy = obj({name: copy.deepcopy(inspect["properties"][name]) for name in
                  ("work_id", "mode", "path", "requested_budget_seconds")}, ["work_id", "mode"])
    legacy["properties"]["mode"]["enum"] = ["status", "diff", "file"]
    output = obj({
        "work_id": copy.deepcopy(legacy["properties"]["work_id"]),
        "mode": {"type": "string", "const": "oracle_output"},
        "run_ref": {"type": "string", "minLength": 1, "maxLength": 512},
        "stream": {"type": "string", "enum": ["stdout", "stderr"]},
        "offset": {"type": "integer", "minimum": 0, "maximum": 9223372036854775807},
        "length": {"type": "integer", "minimum": 1, "maximum": 16384},
        "requested_budget_seconds": {"$ref": "#/$defs/requested_budget_seconds"},
    }, ["work_id", "mode", "run_ref", "stream", "offset", "length"])
    defs["work_browse_worktree_inspect_input"] = closed_operation_variants([legacy, output], ["work_id", "mode"])
    defs["work_browse_worktree_inspect_input"]["properties"]["mode"] = {
        "type": "string", "enum": ["status", "diff", "file", "oracle_output"],
        "description": "oracle_output reads retained same-work/ambient-Project native stream bytes, even after worktree reclaim. No caller path, process, lease, or idempotency write. At most 16 KiB raw bytes per page within the unchanged envelope.",
    }


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


def project_native_oracle_metadata(defs: dict, packet_defs: dict) -> None:
    """The packet owns the shared native preparation projection; results add
    only the fields the store producer publishes, never caller observations.
    The canonical packet also owns the selected-research projection:
    the shared view's research member and its closed defs project from the
    lane contract, so the public read view and the frozen view3 anchor the
    same schema content instead of independently copied runtime policy."""
    aliases = {
        "lane_native_oracle_stream": "native_oracle_stream",
        "lane_native_oracle_file": "native_oracle_file",
        "lane_native_oracle_test_name": "native_oracle_test_name",
        "lane_native_oracle_preparation": "native_oracle_preparation",
        "lane_work_context_reading_source_repository_file": "work_context_reading_source_repository_file",
        "lane_work_context_research_selection": "work_context_research_selection",
        "lane_work_context_research_finding": "work_context_research_finding",
        "lane_work_context_research_finding_view": "work_context_research_finding_view",
        "lane_research_scopes": "research_scopes",
        "lane_research_source": "research_source",
    }
    # The lane-local research defs the payloads document does not author: the
    # canonical lane contract owns their bytes and the projection copies them
    # under the shared payload names. lane_research_scopes and
    # lane_research_source only remap references; the payloads document's own
    # research defs stay the published tool-surface owner.
    projected_research_defs = (
        "lane_work_context_research_selection",
        "lane_work_context_research_finding",
        "lane_work_context_research_finding_view",
    )

    def remap(node):
        if isinstance(node, list):
            return [remap(value) for value in node]
        if not isinstance(node, dict):
            return node
        result = {key: remap(value) for key, value in node.items()}
        if "$ref" in result:
            name = result["$ref"].removeprefix("#/$defs/")
            if name not in aliases:
                fail(f"native oracle metadata names an unprojected schema: {name}")
            result["$ref"] = "#/$defs/" + aliases[name]
        return result

    for source, target in aliases.items():
        if source.startswith("lane_native_") or source in projected_research_defs:
            defs[target] = remap(packet_defs[source])
    for name in ("subject_commit", "oracle_preparations"):
        defs["work_context_view"]["properties"][name] = remap(packet_defs["lane_work_context_view"]["properties"][name])
    research = packet_defs["lane_work_context_view"]["properties"].get("research_findings")
    if not isinstance(research, dict):
        fail("the canonical lane work-context view carries no research_findings member")
    defs["work_context_view"]["properties"]["research_findings"] = remap(research)
    defs["work_context_view"]["properties"].pop("candidate_subject", None)
    receipt = defs["worker_oracle_receipt"]
    receipt["properties"].pop("candidate_subject", None)
    receipt["properties"]["subject_commit"] = copy.deepcopy(packet_defs["lane_worker_oracle_receipt"]["properties"]["subject_commit"])
    receipt["description"] = packet_defs["lane_worker_oracle_receipt"]["description"]
    stage = {
        "name": {"type": "string", "minLength": 1, "maxLength": 128},
        "argv": copy.deepcopy(defs["work_transition_worktree_verify_input"]["properties"]["command"]),
        "exit_code": {"type": "integer", "minimum": -1, "maximum": 255},
    }
    stage.update({field: {"type": "integer", "minimum": 0, "maximum": 2097152} for field in
                  ("stdout_offset", "stdout_length", "stderr_offset", "stderr_length")})
    defs["native_oracle_stage"] = {"type": "object", "additionalProperties": False, "required": list(stage), "properties": stage}

    def project_phase_into_branches(node: dict, properties: dict, required: list[str]) -> None:
        """Each closed family branch carries the phase's own fields: a
        result fixture synthesizes from one branch alone, so a branch that
        omits a phase-required field would build an invalid result."""
        for branch in node.get("oneOf", []):
            branch["properties"] = {**branch.get("properties", {}), **copy.deepcopy(properties)}
            known = list(branch.get("required", []))
            branch["required"] = known + [name for name in required if name not in known]

    # The worktree_verify result replays the lease's stored preparation, so
    # it starts from the retained shape, not the dispatch-admitted one.
    prepare = copy.deepcopy(defs["native_oracle_preparation"])
    drop_retained_caps(prepare, "native_oracle_preparation")
    prepare_fields = {
        "control_id": copy.deepcopy(defs["worker_oracle_control"]["properties"]["control_id"]),
        "input_manifest_digest": {"$ref": "#/$defs/native_oracle_digest"},
        "binary_digest": {"$ref": "#/$defs/native_oracle_digest"},
        "stages": {"type": "array", "minItems": 0, "maxItems": 16, "items": {"$ref": "#/$defs/native_oracle_stage"}},
        "detail": {"type": "string"},
    }
    prepare["properties"].update(copy.deepcopy(prepare_fields))
    prepare["required"].extend(["control_id", "stages"])
    project_phase_into_branches(prepare, prepare_fields, ["control_id", "stages"])
    execute = copy.deepcopy(prepare)
    execute["properties"]["phase"] = {"type": "string", "const": "execute"}
    execute["properties"]["qualification"] = {"type": "string", "enum": ["pass", "fail", "unavailable"]}
    request = defs["native_oracle_execute_request"]
    for name in ("attempt_id", "attempt_epoch", "worker_packet_digest", "worker_job_binding", "preparation_run_ref"):
        execute["properties"][name] = copy.deepcopy(request["properties"][name])
        execute["required"].append(name)
    execute["properties"].update({
        "authorization_event_id": {"$ref": "#/$defs/id"},
        "authorization_seq": {"type": "integer", "minimum": 1, "maximum": 9223372036854775807},
        "start_seq": {"type": "integer", "minimum": 1, "maximum": 9223372036854775807},
        "observed_test_names": {"type": "array", "minItems": 0, "maxItems": 40, "uniqueItems": True,
                                "items": {"$ref": "#/$defs/native_oracle_test_name"}},
        "observed_witness_ids": {"type": "array", "minItems": 0, "maxItems": 40, "uniqueItems": True,
                                 "items": {"type": "string", "minLength": 1, "maxLength": 256,
                                           "pattern": "^[^\\x00-\\x1f\\x7f]*$"}},
    })
    execute["required"].extend(["authorization_event_id", "authorization_seq", "start_seq"])
    project_phase_into_branches(execute, {name: copy.deepcopy(execute["properties"][name]) for name in
                                          ("attempt_id", "attempt_epoch", "worker_packet_digest", "worker_job_binding",
                                           "preparation_run_ref", "authorization_event_id", "authorization_seq", "start_seq",
                                           "input_manifest_digest", "binary_digest", "observed_test_names", "observed_witness_ids")},
                                ["attempt_id", "attempt_epoch", "worker_packet_digest", "worker_job_binding",
                                 "preparation_run_ref", "authorization_event_id", "authorization_seq", "start_seq"])
    # The family branches restate the phase fields they inherited from the
    # preparation union; an execute branch speaks the execute phase grammar.
    for branch in execute.get("oneOf", []):
        branch["properties"]["phase"] = {"type": "string", "const": "execute"}
        branch["properties"]["qualification"] = {"type": "string", "enum": ["pass", "fail", "unavailable"]}
    execute["if"] = {"properties": {"qualification": {"const": "pass"}}, "required": ["qualification"]}
    # A pass names the witnesses the run actually observed, in the record's
    # own family grammar: observed_test_names for a Go preparation and
    # observed_witness_ids for a target_runner_v1 preparation, never both.
    # The ready-then streams block lives in each family branch of the
    # authored preparation union.
    streams_then = copy.deepcopy(defs["native_oracle_preparation"].get("oneOf", [{}])[0].get("then", {}))
    def pass_branch(observed: str) -> dict:
        branch = copy.deepcopy(streams_then)
        branch["required"] = ["input_manifest_digest", "binary_digest", observed]
        branch["not"] = {"required": ["observed_witness_ids" if observed == "observed_test_names" else "observed_test_names"]}
        branch["properties"] = {**branch.get("properties", {}), observed: {"minItems": 1}}
        return branch
    execute["then"] = {"oneOf": [pass_branch("observed_test_names"), pass_branch("observed_witness_ids")]}
    defs["native_oracle_result"] = {"oneOf": [prepare, execute], "description": (
        "Native producer observations only. The canonical plan/digest are immutable in the existing lease; command remains bare argv. "
        "Prepare grants readiness only. Execute pass requires actual run/pass witnesses in the record's own family grammar "
        "(observed test names for Go, observed witness IDs for target_runner_v1), complete streams, pinned inputs/cwd/environment, "
        "and the persisted live dispatch's three-way clean subject equality. "
        "Qualified green result metadata is byte-identical to lease result_json. "
        "Raw BLOBs never enter result metadata or idempotency records. Replay starts no process. No semantic acceptance or independent evidence role is implied."
    )}
    defs["worktree_verify_result"]["properties"]["oracle"] = {"$ref": "#/$defs/native_oracle_result"}
    defs["worktree_verify_result"]["properties"]["branch"]["maxLength"] = 256
    page_properties = {
        "run_ref": {"type": "string", "minLength": 1, "maxLength": 512},
        "stream": {"type": "string", "enum": ["stdout", "stderr"]},
        "offset": {"type": "integer", "minimum": 0, "maximum": 2097152},
        "data_base64": {"type": "string", "maxLength": 21848, "pattern": "^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$"},
        "length": {"type": "integer", "minimum": 0, "maximum": 16384},
        "total_length": {"type": "integer", "minimum": 0, "maximum": 2097152},
        "sha256": {"$ref": "#/$defs/native_oracle_digest"}, "complete": {"type": "boolean"},
        "next_offset": {"type": "integer", "minimum": 0, "maximum": 2097152}, "eof": {"type": "boolean"},
    }
    defs["native_oracle_output_page"] = {"type": "object", "additionalProperties": False, "required": list(page_properties), "properties": page_properties}
    result = defs["worktree_inspect_result"]
    result = result.get("oneOf", [result])[0]
    legacy = {"type": "object", "additionalProperties": False, "required": list(result["required"]),
              "properties": {name: copy.deepcopy(result["properties"][name]) for name in
                             ("work_id", "project_id", "branch", "path", "mode", "content", "truncated")}}
    legacy["required"] = list(legacy["properties"])
    legacy["properties"]["branch"]["maxLength"] = 256
    legacy["properties"]["mode"]["enum"] = ["status", "diff", "file"]
    output_properties = {name: copy.deepcopy(value) for name, value in legacy["properties"].items()}
    output_properties["mode"] = {"type": "string", "const": "oracle_output"}
    for name in ("branch", "path"):
        output_properties[name].pop("minLength", None)
    output_properties["oracle_output"] = {"$ref": "#/$defs/native_oracle_output_page"}
    output = {"type": "object", "additionalProperties": False, "required": list(output_properties), "properties": output_properties}
    defs["worktree_inspect_result"] = closed_operation_variants([legacy, output], ["work_id", "project_id", "mode"])
    defs["worktree_inspect_result"]["properties"]["mode"] = {"type": "string", "enum": ["status", "diff", "file", "oracle_output"]}
    for name in ("branch", "path"):
        defs["worktree_inspect_result"]["properties"][name].pop("minLength", None)


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
        fail(f"schema guidance generation failed: {result.stderr.strip()}")
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


def project_workflow_action_schema(document: dict, actions: list[dict], workflows: list[dict], teaching: dict) -> tuple[dict, list[dict]]:
    projected = copy.deepcopy(document)
    remove_generated_guidance(projected)
    defs = projected["$defs"]
    # Continuity and work pins expose the same card references as dispatch.
    # The authored packet schema owns the source shape, not a second union.
    packet_defs = json.loads((ROOT / "contracts/agent-lane-packet.schema.json").read_text())["$defs"]
    for name in ("recipe_source", "readiness_evidence_refs"):
        defs["worker_oracle_control"]["properties"][name]["description"] = packet_defs["lane_worker_oracle_control"]["properties"][name]["description"]
    project_native_oracle_requests(defs)
    project_native_oracle_metadata(defs, packet_defs)
    # The card-free work-context view carries domain groups without
    # the retired domain_cards member. The live view keeps its own closed
    # shape; released card-bearing 1.0/1.1 packets replay through the
    # versioned lane packet contract's branches, never through this view.
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
    # The retained output shapes are preserved and their uses retargeted
    # before the live outcome union narrows: stored predicates replay the
    # full frozen vocabulary, and the narrowing must never leak
    # into the copies.
    preserve_retained_outcome_outputs(defs)
    # The live outcome union narrows before any branch copy is taken: the
    # published tokens are exactly what the live heads pin, and the
    # decision_record authoring surface retires (CD-0218 D1). The frozen
    # historical decoder keeps the full retired vocabulary for replay.
    narrow_live_outcome_union(defs, workflows)
    defs["workflow_action_outcome"] = copy.deepcopy(outcome_payload)
    defs["workflow_contract_version"] = {"$ref": "#/$defs/version"}
    # The pinned-token annotation lands on the outcome def BEFORE any branch
    # copies are taken: the per-kind item branches and the supersede pair
    # branches deepcopy the outcome branches, and each copy must teach the
    # per-workflow allowed tokens.
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
    # core refuses.
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
    # Main associates accepted criteria with captured producer receipts. A
    # caller verdict, missing receipt, or operation-minted ref is not proof.
    defs["workflow_verdict_batch_entry"] = {
        "type": "object", "additionalProperties": False,
        "required": ["predicate_id", "receipt_refs"],
        "properties": {
            "predicate_id": {"$ref": "#/$defs/id", "description": "The store refuses a predicate_id without the \"predicate:\" prefix. Write ids in the form \"predicate:<name>\"."},
            "receipt_refs": {"type": "array", "minItems": 1, "maxItems": 32, "uniqueItems": True, "items": {"$ref": "#/$defs/reference"}, "description": "Captured passing producer receipts covering this criterion on the current candidate. Main associates proof; it supplies no verdict or replacement subject."},
        },
    }
    # CD-0230 D4: a serious law change pins the exact text the operator
    # approves. Each pin names one changed law and the sha256 content hash of
    # its drafted after-state document; completion refuses other bytes.
    defs["workflow_law_text_pin"] = {
        "type": "object", "additionalProperties": False,
        "required": ["law_id", "content_hash"],
        "properties": {
            "law_id": {"$ref": "#/$defs/law_id"},
            "content_hash": {"type": "string", "minLength": 71, "maxLength": 71, "pattern": "^sha256:[0-9a-f]{64}$"},
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
    design_action = next((action for action in actions if action["id"] == "record_design"), None)
    if design_action is None:
        fail("the live projection names no record_design action; the design-content def has no live owner")
    defs["workflow_design_content"] = workflow_payload_object_schema(single_variant(design_action)["payload"], defs)

    outer_properties = copy.deepcopy(common)
    # The registry declares the envelope-carried fields once: the generator
    # projects the action's declared envelope fields to the envelope outer
    # level, so the work pin (required_fields), this envelope schema, and the
    # store validator read one declaration and cannot drift.
    confirm_action = next((action for action in actions if action["id"] == "confirm_premise"), None)
    if confirm_action is None:
        fail("the live projection names no confirm_premise action; the envelope fields have no live owner")
    for field in single_variant(confirm_action)["payload"]["fields"]:
        if not field.get("envelope"):
            continue
        outer_properties[field["name"]] = workflow_payload_field_schema(field, defs)
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
            required.extend(field["name"] for field in single_variant(action)[payload_key]["fields"] if field.get("envelope") and field.get("required"))
            # The envelope alternative is closed: fields is not a property of
            # this variant at all, so supplying it is a structural refusal.
            properties.pop("fields", None)
        else:
            # One path for every action, supersede_contract included: the
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
        if action_id != "confirm_premise":
            # Publish the common envelope declarations without admitting
            # decision-only values on other actions. The host preserves this
            # exclusion and the core retains the same closed input set.
            defs[name]["not"] = {"anyOf": [{"required": [field]} for field in
                                           ("selected_choice", "decision_context_digest")]}

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
    require_delivery_rule_teaching(defs)
    install_retained_output_twins(defs)
    contracts = {"approve_contract", "supersede_contract"}
    guidance_names = [f"work_transition_action_variant_{action}" for action in sorted(contracts)]
    guidance_names.extend(variant["schema"] for variant in public_variants if variant["action_id"] in contracts)
    projected = project_schema_guidance(projected, guidance_names)
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
    "review_input": "worker_packet_review_input",
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
        "The current work-context view the work pin carried when the "
        "packet was built. The core refuses a dispatch whose packet does not "
        "consume the current view byte-for-byte."
    ),
    "checkpoint": (
        "The latest context checkpoint the pinned continuity "
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


def project_domain_fact_schema(payload: dict) -> None:
    """Reuse the fact contract with namespaced local refs in the payload document.

    Runtime/publication validators resolve local $defs, not external documents.
    The fact's variant and standing constraints remain owned by its schema.
    """
    document = json.loads((ROOT / "contracts/domain-fact.v1.schema.json").read_text())
    def project(node):
        if isinstance(node, list):
            return [project(child) for child in node]
        if not isinstance(node, dict):
            return node
        result = {key: project(value) for key, value in node.items()
                  if key not in {"$schema", "$id", "$defs"}}
        if "$ref" in result:
            ref = result["$ref"]
            if not ref.startswith("#/$defs/"):
                fail(f"non-local Domain fact reference {ref}")
            result["$ref"] = "#/$defs/domain_fact_" + ref.removeprefix("#/$defs/")
        if "enum" in result and "type" not in result:
            result["type"] = "string"
        # The fact ID's anchored pattern already bounds it to 128 characters.
        if result.get("pattern") == "^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$":
            result.update(minLength=1, maxLength=128)
        return result
    defs = payload["$defs"]
    defs["domain_fact"] = project(document)
    for name, schema in document["$defs"].items():
        defs["domain_fact_" + name] = project(schema)
    # Go's RE2 has no lookahead. Express the source path's two negative
    # lookaheads as JSON Schema `not` constraints, preserving their meaning.
    path = defs["domain_fact_path"]
    prefix = r"^(?!/)(?!.*(?:^|/)\.{1,2}(?:/|$))"
    if not path["pattern"].startswith(prefix):
        fail("Domain fact path pattern changed; review its RE2 projection")
    path["pattern"] = "^" + path["pattern"].removeprefix(prefix)
    path["not"] = {"anyOf": [path["not"], {"pattern": r"^/|(?:^|/)\.{1,2}(?:/|$)"}]}


def project_worker_packet_inputs(payload: dict, lane_packet: dict) -> None:
    """Project the current packet and its job from the canonical lane input.

    Shared record aliases keep their own published bounds. Packet identity,
    Review provenance and the worker-job field set have one owner.
    """
    inputs = lane_packet.get("properties", {}).get("inputs")
    if not isinstance(inputs, dict) or inputs.get("type") != "object" or inputs.get("additionalProperties") is not False:
        fail("contracts/agent-lane-packet.schema.json: inputs is not a closed object")
    doc_defs = payload.get("$defs", {})
    packet_aliases = {
        "lane_review_input": "worker_packet_review_input",
        "review_candidate": "worker_packet_review_candidate",
        "review_dependencies": "worker_packet_review_dependencies",
        "lane_packet_worker_job": "worker_packet_worker_job",
    }
    imported = set(packet_aliases)
    refs = {f"#/$defs/{source}": f"#/$defs/{alias}" for source, alias in packet_aliases.items()}
    for source in lane_packet["$defs"]:
        shared = source.removeprefix("lane_")
        if shared in doc_defs:
            refs.setdefault("#/$defs/" + source, "#/$defs/" + shared)

    def review_alias(node):
        if isinstance(node, dict):
            result = {key: review_alias(value) for key, value in node.items()}
            if "$ref" in result:
                ref = result["$ref"]
                if ref not in refs:
                    if not ref.startswith("#/$defs/"):
                        fail(f"lane packet carries a non-local reference {ref}")
                    source = ref.removeprefix("#/$defs/")
                    if source not in lane_packet["$defs"]:
                        fail(f"lane packet references unknown definition {source}")
                    alias = "worker_packet_" + source.removeprefix("lane_")
                    refs[ref] = "#/$defs/" + alias
                    if source not in imported:
                        imported.add(source)
                        doc_defs[alias] = review_alias(lane_packet["$defs"][source])
                result["$ref"] = refs[ref]
            if "enum" in result and "type" not in result:
                result["type"] = "string"
            return result
        if isinstance(node, list):
            return [review_alias(value) for value in node]
        return node

    for source, alias in packet_aliases.items():
        doc_defs[alias] = review_alias(lane_packet["$defs"][source])
    packet = {key: copy.deepcopy(lane_packet[key]) for key in ("type", "additionalProperties", "required")}
    packet["description"] = "CD-0067 D1: current closed worker packet, projected from contracts/agent-lane-packet.schema.json."
    packet["properties"] = {name: review_alias(schema) for name, schema in lane_packet["properties"].items() if name != "inputs"}
    # Live dispatch admits only the canonical format's current identity.
    packet["properties"]["schema_version"]["enum"] = [lane_packet["properties"]["schema_version"]["enum"][-1]]
    doc_defs["worker_packet"] = packet
    properties = {}
    for name, schema in inputs.get("properties", {}).items():
        alias = WORKER_PACKET_INPUT_ALIASES.get(name)
        if alias is not None:
            if alias not in doc_defs:
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


def project_review_report_types(payload: dict, lane_report: dict) -> None:
    """Expose the canonical report shape in current-state reads, not a copy."""
    source = lane_report["$defs"]
    target = payload["$defs"]
    imported: set[str] = set()

    def import_definition(name: str) -> str:
        alias = "lane_report_" + name
        if name not in imported:
            imported.add(name)
            if name not in source:
                fail(f"lane report references unknown definition {name}")
            target[alias] = rewrite(copy.deepcopy(source[name]))
        return alias

    def rewrite(node: object) -> object:
        if isinstance(node, list):
            return [rewrite(value) for value in node]
        if not isinstance(node, dict):
            return node
        result = {key: rewrite(value) for key, value in node.items()}
        if "$ref" in result:
            ref = result["$ref"]
            if not isinstance(ref, str) or not ref.startswith("#/$defs/"):
                fail(f"lane report carries a non-local reference {ref}")
            result["$ref"] = "#/$defs/" + import_definition(ref.removeprefix("#/$defs/"))
        if "enum" in result and "type" not in result:
            if not all(isinstance(value, str) for value in result["enum"]):
                fail("lane report enum needs an explicit type")
            result["type"] = "string"
        return result

    for name in ("review_candidate", "review_block", "review_finding"):
        import_definition(name)

    # A retained delivery may predate Project or tree observation. Readback
    # exposes what was recorded; a new worker report still requires all three.
    candidate_view = copy.deepcopy(target["lane_report_review_candidate"])
    candidate_view["required"] = ["commit"]
    candidate_view["description"] = "The retained candidate commit, with Project and tree only when recorded. Missing identity fields grant no worker-report or acceptance authority."
    target["workflow_review_candidate_view"] = candidate_view
    target["workflow_review_state"]["properties"]["candidate"]["anyOf"][1] = {
        "$ref": "#/$defs/workflow_review_candidate_view"
    }

    unidentified = copy.deepcopy(target["lane_report_review_finding"])
    observed = {"severity", "confidence", "detail", "oracle"}
    unidentified["properties"] = {
        name: schema for name, schema in unidentified["properties"].items() if name in observed
    }
    unidentified["required"] = [name for name in unidentified["required"] if name in observed]
    unidentified["description"] = "A retained current finding without recorded identity, status, scope, blocking or reference fields. Only its observed content is exposed; it grants no worker-report, closure or acceptance authority."
    target["workflow_review_unidentified_finding"] = unidentified
    target["workflow_review_state"]["properties"]["findings"]["items"] = {"anyOf": [
        {"$ref": "#/$defs/lane_report_review_finding"},
        {"$ref": "#/$defs/workflow_review_unidentified_finding"},
    ]}


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
    # The operation surface is closed: the manifest must enumerate the full
    # published set exactly, and the count changes only with an accepted
    # surface change.
    expected_operations = 64
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
    # at 1800 (the verification lane measured 367.08s). This rule
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


def validate_work_start_host_tool(tool: dict) -> None:
    args = tool.get("args", {})
    # The projection preserves the authored declaration order, so the
    # published capture surface is pinned in exactly the contract's own
    # (lexicographic) property order.
    capture = ["defect_intake", "external_ref", "governing_requirements", "idempotency_key", "kind", "priority", "raised_from_work_id", "ref", "size_inputs", "tags", "task", "title", "urgency", "value_statement", "workflow_type_ref"]
    # Resume names exactly one selector; the core alone resolves an issue key.
    resume_properties = ["issue_key", "work_id"]
    resume_selectors = [{"required": ["work_id"]}, {"required": ["issue_key"]}]
    branches = args.get("oneOf")
    if args.get("type") != "object" or not isinstance(branches, list) or len(branches) != 2:
        fail("concord_work_start host schema must be a oneOf of the capture and resume shapes")
    if branches[0].get("required") != ["title", "value_statement", "kind", "task", "idempotency_key"] or list(branches[0].get("properties", {})) != capture:
        fail("concord_work_start capture branch has an unexpected argument surface")
    if branches[0].get("additionalProperties") is not False or branches[1].get("additionalProperties") is not False:
        fail("concord_work_start branches must close their argument surface")
    resume = branches[1]
    if resume.get("oneOf") != resume_selectors or list(resume.get("properties", {})) != resume_properties or set(resume) != {"additionalProperties", "properties", "oneOf"}:
        fail("concord_work_start resume branch must carry exactly one of work_id or issue_key")
    issue_key = resume["properties"]["issue_key"]
    if issue_key.get("type") != "string" or issue_key.get("minLength") != 3 or issue_key.get("maxLength") != 64 or issue_key.get("pattern") != "^[A-Z][A-Z0-9]*-[1-9][0-9]*$":
        fail("concord_work_start issue_key must pin the recorded Linear issue key bounds")
    for name, maximum in (("title", 256), ("value_statement", 256), ("external_ref", 256), ("task", 8192)):
        if branches[0]["properties"].get(name, {}).get("x-maxBytes") != maximum:
            fail(f"concord_work_start {name} must pin x-maxBytes={maximum}")
    for branch in branches:
        if "product_id" in branch.get("properties", {}):
            fail("concord_work_start must derive Product identity")
    if "project_id" in branches[0].get("properties", {}):
        fail("concord_work_start capture must derive Project identity")


def validate_ci_watch_host_tool(tool: dict) -> None:
    args = tool.get("args", {})
    properties = args.get("properties", {})
    if args.get("type") != "object" or args.get("additionalProperties") is not False:
        fail("concord_ci_watch host schema must be a closed object")
    if list(properties) != ["repo", "selector", "head_sha", "mode", "time_seconds_max"]:
        fail("concord_ci_watch must publish exactly repo, selector, head_sha, mode, and time_seconds_max")
    if args.get("required") != ["repo", "selector"]:
        fail("concord_ci_watch must require only repo and selector")
    repo = properties.get("repo", {})
    if (repo.get("type") != "string"
            or repo.get("pattern") != "^[A-Za-z0-9][A-Za-z0-9.-]*/[A-Za-z0-9][A-Za-z0-9.-]*$"
            or repo.get("minLength") != 3 or repo.get("maxLength") != 256):
        fail("concord_ci_watch repo must carry the owner/name pattern bounded 3..256")
    selector = properties.get("selector", {})
    selector_properties = selector.get("properties", {})
    if selector.get("type") != "object" or selector.get("additionalProperties") is not False:
        fail("concord_ci_watch selector must be a closed object")
    if selector.get("required") != ["kind", "value"] or list(selector_properties) != ["kind", "value"]:
        fail("concord_ci_watch selector must carry only the required kind and value")
    if selector_properties.get("kind", {}).get("enum") != ["pr", "sha", "run"]:
        fail("concord_ci_watch selector.kind must admit pr, sha, and run only")
    value = selector_properties.get("value", {})
    if value.get("type") != "string" or value.get("minLength") != 1 or value.get("maxLength") != 64:
        fail("concord_ci_watch selector.value must be a string bounded 1..64")
    head = properties.get("head_sha", {})
    if head.get("type") != "string" or head.get("pattern") != "^[0-9a-f]{40}$":
        fail("concord_ci_watch head_sha must be a lowercase 40-character commit SHA")
    if properties.get("mode", {}).get("enum") != ["checks", "merge"]:
        fail("concord_ci_watch mode must admit checks and merge only")
    budget = properties.get("time_seconds_max", {})
    if budget.get("type") != "integer" or budget.get("minimum") != 0 or budget.get("maximum") != 1800:
        fail("concord_ci_watch time_seconds_max must be an integer bounded 0..1800")


def validate_worker_read_host_tool(tool: dict) -> None:
    args = tool.get("args", {})
    properties = args.get("properties", {})
    fields = ["ordinal", "offset", "length"]
    if args.get("type") != "object" or args.get("additionalProperties") is not False:
        fail("concord_worker_read host schema must be a closed object")
    if list(properties) != fields or args.get("required") != fields:
        fail("concord_worker_read must require only ordinal, offset, and length")
    for name, minimum, maximum in (("ordinal", 0, 9007199254740991), ("offset", 0, 9007199254740991), ("length", 1, 4096)):
        field = properties[name]
        if field.get("type") != "integer" or field.get("minimum") != minimum or field.get("maximum") != maximum:
            fail(f"concord_worker_read {name} must be an integer bounded {minimum}..{maximum}")


def validate_worker_diff_host_tool(tool: dict) -> None:
    args = tool.get("args", {})
    properties = args.get("properties", {})
    if args.get("type") != "object" or args.get("additionalProperties") is not False:
        fail("concord_worker_diff host schema must be a closed object")
    if list(properties) != ["offset", "length"] or args.get("required") != ["offset", "length"]:
        fail("concord_worker_diff must require only offset and length")
    for name, minimum, maximum in (("offset", 0, 9007199254740991), ("length", 1, 4096)):
        field = properties[name]
        if field.get("type") != "integer" or field.get("minimum") != minimum or field.get("maximum") != maximum:
            fail(f"concord_worker_diff {name} must be an integer bounded {minimum}..{maximum}")


def validate_worker_check_host_tool(tool: dict) -> None:
    args = tool.get("args", {})
    properties = args.get("properties", {})
    if args.get("type") != "object" or args.get("additionalProperties") is not False:
        fail("concord_worker_check host schema must be a closed object")
    if list(properties) != ["control_id"] or args.get("required") != ["control_id"]:
        fail("concord_worker_check must require only control_id")
    reference = json.loads(PAYLOAD.read_text(encoding="utf-8"))["$defs"]["reference"]
    actual = {key: value for key, value in properties["control_id"].items() if key not in {"description", "examples"}}
    expected = {key: value for key, value in reference.items() if key not in {"description", "examples"}}
    if actual != expected:
        fail("concord_worker_check control_id must use the workflow reference bounds")


# The named host-tool validators: one named check per host-published tool.
# A new host tool joins by adding its contract entry and its validator here;
# the manifest must declare exactly this set once each, so an unknown,
# missing, or duplicated tool fails generation instead of widening silently.
HOST_TOOL_VALIDATORS = {
    "concord_work_start": validate_work_start_host_tool,
    "concord_ci_watch": validate_ci_watch_host_tool,
    "concord_worker_read": validate_worker_read_host_tool,
    "concord_worker_check": validate_worker_check_host_tool,
    "concord_worker_diff": validate_worker_diff_host_tool,
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
        if result_name == "research_retire_result" and isinstance(valid_result, dict):
            # A real retirement run reclassifies every eligible candidate to
            # retired before the result returns, so eligible only appears in a
            # dry run. Keep the sampled eligible candidate and flip the run to
            # dry; dry_run=false with eligible is a result the core can never
            # produce.
            valid_result["dry_run"]=True
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

def go_payload_schema_projection() -> str:
    document = json.dumps(json.loads(PAYLOAD.read_text()), ensure_ascii=False, sort_keys=True, indent=2)
    return "// Code generated by scripts/generate-agent-contracts.py; DO NOT EDIT.\npackage payloadschema\n\nconst GeneratedPayloadSchemaDocument = `" + document + "`\n"

def go_envelope_schema_projection() -> str:
    document = json.dumps(json.loads((ROOT / "contracts/agent-tool-envelope.schema.json").read_text()), ensure_ascii=False, sort_keys=True, indent=2)
    if "`" in document:
        fail("envelope schema document contains a Go raw-string delimiter")
    return (
        "// Code generated by scripts/generate-agent-contracts.py; DO NOT EDIT.\n"
        "package payloadschema\n\n"
        "// GeneratedEnvelopeSchemaDocument is the TS7 envelope schema from\n"
        "// contracts/agent-tool-envelope.schema.json, shared by the core validator\n"
        "// and the generated adapter validator.\n"
        "const GeneratedEnvelopeSchemaDocument = `" + document + "`\n"
    )

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
// their operation, and approve_contract's and supersede_contract's fields is
// one closed union: the legacy shape and the consequence_v1 extension. The
// traversal expands the passed document's own local references and factored
// unions, so every check below reads the legacy-effective view of the actual
// published document, and every fields check runs on each branch of the
// fields union, so a branch-only drop cannot hide behind the factored parent.
export function advertisedAdmissionTeachingGaps(published: unknown): string[] {{
  const gaps: string[] = [];
  const expanded: any = expandedPublishedRequestSchema(published);
  const requestBranches: any[] = Array.isArray(expanded?.oneOf) ? expanded.oneOf : [];
  // workflow_action publishes one closed branch per action variant; the
  // teaching checks read each fields branch that carries the taught field.
  const actionInputs: any[] = requestBranches
    .filter((branch) => branch?.properties?.operation?.const === "workflow_action")
    .map((branch) => branch?.properties?.input);
  const carriers: any[] = actionInputs.flatMap((input) =>
    actionFieldsBranches(input)
      .filter((fields) => fields?.properties?.outcome_predicates !== undefined)
      .map((fields) => ({{ actionId: input?.properties?.action_id?.const, fields }})),
  );
  if (carriers.length === 0) {{
    gaps.push("outcome_predicates items carry 0 per-kind branches, expected the 4 strict variants");
  }}
  for (const carrier of carriers) {{
  // The published item is closed per kind: outcome_kind consts to the branch
  // and outcome_payload carries that branch's payload, so a mismatched
  // outcome_kind/outcome_payload pair validates no branch.
  const items: any = carrier.fields.properties.outcome_predicates?.items;
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
  // Every fields branch that carries outcome_predicates on every carrier
  // action (approve_contract and supersede_contract) must teach the rule,
  // or a drop from one branch hides behind its siblings.
  const delivery: unknown = carrier.fields.properties.outcome_predicates?.description;
  if (typeof delivery !== "string" || delivery.split(" Accepted shape:", 1)[0] !== {json.dumps(DELIVERY_RULE_PREDICATE_DESCRIPTION)}) {{
    gaps.push(`outcome_predicates description does not teach the delivery-decidable rule (CD-0184) on action ${{carrier.actionId}}`);
  }}
  }}
  // The CD-0184 wait-rule teaching retired with add_condition (CD-0218 D1):
  // no live action declares expected_within_seconds, so no wait carrier
  // check exists. require_delivery_rule_teaching refuses a stale projection
  // that revives the retired variant.
  return gaps;
}}
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


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument("--check", action="store_true",
                        help="fail on generated contract drift instead of writing projections")
    args = parser.parse_args()
    try:
        check = args.check
        manifest = json.loads(MANIFEST.read_text())
        fixture_path = ROOT / "contracts/workflow-engine.fixtures.json"
        fixture_text = fixture_path.read_text()
        first_case = json.loads(fixture_text)["cases"][0]
        if first_case["schema"] != "workflow-definition.schema.json" or first_case["valid"] is not True:
            fail("the first workflow engine fixture must be a valid workflow definition")
        instance_start = fixture_text.index("{", fixture_text.index('"instance":'))
        instance, instance_length = json.JSONDecoder().raw_decode(fixture_text[instance_start:])
        canonical_fixture = subprocess.run(
            ["go", "run", "./scripts/workflow-action-contracts", "--canonical-definition"],
            cwd=ROOT, input=json.dumps(instance), text=True, capture_output=True,
            timeout=300, check=True,
        )
        instance_text = json.dumps(json.loads(canonical_fixture.stdout), ensure_ascii=False, indent=2).replace("\n", "\n      ")
        expected_fixture = fixture_text[:instance_start] + instance_text + fixture_text[instance_start + instance_length:]
        if expected_fixture != fixture_text:
            if check:
                fail("generated definition fixture drift: contracts/workflow-engine.fixtures.json")
            fixture_path.write_text(expected_fixture)
        ir = json.loads(IR.read_text())
        payload = json.loads(PAYLOAD.read_text())
        actions, workflows, teaching, schema_refs = load_workflow_action_contracts()
        bun = shutil.which("bun")
        if bun is None and (not check or os.environ.get("CONCORD_REQUIRE_BUN") == "1"):
            fail("Bun is not installed: the publication measurement requires Bun, so write mode and strict check refuse rather than skip it")
        validate_current_schema_refs(schema_refs, payload.get("$defs", {}))
        projected_payload, public_variants = project_workflow_action_schema(payload, actions, workflows, teaching)
        project_domain_fact_schema(projected_payload)
        project_worker_packet_inputs(projected_payload, json.loads(LANE_PACKET.read_text()))
        project_review_report_types(projected_payload, json.loads(LANE_REPORT.read_text()))
        # The adapter publishes public action variants, never the core-owned
        # dispatch packet in the internal workflow_action input union.
        input_names = [operation["input_schema"].rsplit("/", 1)[-1] for operation in manifest["operations"]
                       if operation["id"] != "concord_work_transition.workflow_action"]
        input_names.extend(variant["schema"] for variant in public_variants)
        projected_payload = project_schema_guidance(projected_payload, input_names, bounds_only=True)
        if payload != projected_payload:
            if check:
                fail("generated payload contract drift (workflow action or worker packet projection): contracts/agent-tool-surface-payloads.schema.json")
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
        formatted_go = formatted_go.replace("func WorkflowActionAvailable() bool { return false }\n", "")
        expected = {
            ROOT / "internal/agent/generated_contracts.go": formatted_go,
            ROOT / "adapter/opencode/generated-contracts.ts": ts_projection(manifest, digest, host_manifest, host_digest, public_variants),
            ROOT / "contracts/agent-tool-surface.digest": digest + "\n",
            ROOT / "contracts/agent-tool-surface.fixtures.json": fixtures_projection(manifest, public_variants),
            ROOT / ".concord/docs/generated-agent-tool-surface.md": docs_projection(manifest),
            ROOT / "adapter/opencode/generated-contract-tests.ts": ts_validator_projection(manifest),
            ROOT / "internal/payloadschema/generated_schemas.go": go_payload_schema_projection(),
            ROOT / "internal/payloadschema/generated_envelope_schema.go": subprocess.run(["gofmt"], input=go_envelope_schema_projection(), text=True, capture_output=True, check=True).stdout,
            ROOT / "internal/store/generated_typed_error_kinds.go": subprocess.run(["gofmt"], input=go_typed_error_kind_projection(envelope), text=True, capture_output=True, check=True).stdout,
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
            (ROOT / "internal/agent").mkdir(exist_ok=True)
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
        return 1
if __name__ == "__main__": raise SystemExit(main())
