import { test, expect } from "bun:test"
import { readFileSync } from "node:fs"
import { contractOperations, manifestDigest, payloadSchemas, workflowActionPublicVariants } from "./generated-contracts"
import { expandedPublishedRequestSchema, validateGeneratedPayload } from "./generated-contract-tests"
import { validateAgainstSchema } from "./dispatch"

const adapter = await import("./concord")

// The published schema is the only contract a calling agent reads before the
// first call. This conformance suite proves, per operation, that the schema
// the host publishes admits the same minimal calls the generated fixture
// corpus proves the core admits, and refuses the fields the core refuses.
// Publication is compact (CON-812): local references and factored unions,
// so branch selection runs on the legacy-effective view the expansion
// helper resolves from the actual published document, and every verdict
// below is taken on the raw compact document and on the final hooked
// argument root, never on the expanded inspection view.
const fixtures = JSON.parse(readFileSync(new URL("../../contracts/agent-tool-surface.fixtures.json", import.meta.url), "utf8")) as {
  manifest_digest: string
  fixtures: { input_schema: string; input_valid: Record<string, unknown> }[]
}

const publishedSchemas = new Map<string, unknown>()
function publishedSchema(toolName: string): any {
  if (!publishedSchemas.has(toolName)) publishedSchemas.set(toolName, adapter.publishedRequestSchema(toolName))
  return publishedSchemas.get(toolName)
}

const finalRoots = new Map<string, unknown>()
async function finalRoot(toolName: string): Promise<any> {
  if (!finalRoots.has(toolName)) {
    const output = { description: "", parameters: {}, jsonSchema: undefined as unknown }
    await adapter.publishRequestDefinition({ toolID: toolName }, output)
    finalRoots.set(toolName, output.jsonSchema)
  }
  return finalRoots.get(toolName)
}

// workflow_action publishes its action variants grouped under one operation
// branch; the expanded view restores one closed branch per action. Select
// the branch by its action_id const, never by list position.
function actionVariantInput(schema: any, actionId: string): any {
  const expanded: any = expandedPublishedRequestSchema(schema)
  const branch = expanded.oneOf.find(
    (candidate: any) => candidate.properties.operation.const === "workflow_action"
      && candidate.properties.input.properties.action_id.const === actionId,
  )
  if (branch === undefined) throw new Error(`published schema carries no closed ${actionId} variant branch`)
  return branch.properties.input
}

function operationInput(schema: any, operation: string): any {
  const expanded: any = expandedPublishedRequestSchema(schema)
  const branch = expanded.oneOf.find(
    (candidate: any) => candidate.properties.operation.const === operation && candidate.properties.input.properties.action_id === undefined,
  )
  if (branch === undefined) throw new Error(`published schema carries no closed ${operation} branch`)
  return branch.properties.input
}

test("the fixture corpus is the generated manifest corpus", () => {
  expect(fixtures.manifest_digest).toBe(manifestDigest)
  expect(fixtures.fixtures).toHaveLength(contractOperations.length)
})

test("the published schema admits the core-admitted minimal call of every operation", () => {
  const bySchema = new Map(contractOperations.map((operation: any) => [operation.input_schema.replace("#/schemas/", ""), operation]))
  for (const fixture of fixtures.fixtures) {
    const operation = bySchema.get(fixture.input_schema)
    if (operation === undefined) throw new Error(`fixture schema ${fixture.input_schema} names no contract operation`)
    const schema = publishedSchema(operation.tool)
    const call = { operation: operation.id.slice(operation.id.indexOf(".") + 1), input: fixture.input_valid }
    const failures: string[] = []
    const admitted = validateAgainstSchema(schema, call, failures)
    expect(admitted, `${operation.id} published refusal: ${failures.join("; ")}`).toBe(true)
  }
})

test("the published schema refuses the forbidden field of every operation", () => {
  const bySchema = new Map(contractOperations.map((operation: any) => [operation.input_schema.replace("#/schemas/", ""), operation]))
  for (const fixture of fixtures.fixtures) {
    const operation = bySchema.get(fixture.input_schema)
    if (operation === undefined) throw new Error(`fixture schema ${fixture.input_schema} names no contract operation`)
    const schema = publishedSchema(operation.tool)
    // Select the exact closed branch the fixture's call targets: the
    // operation branch, or for workflow_action the variant branch whose
    // action_id const names the fixture's action.
    const actionId = fixture.input_valid.action_id
    if (fixture.input_schema === "work_transition_action_input" && typeof actionId !== "string") {
      throw new Error("workflow_action fixture must name a string action_id")
    }
    const branch = fixture.input_schema === "work_transition_action_input" && typeof actionId === "string"
      ? actionVariantInput(schema, actionId)
      : operationInput(schema, operation.id.slice(operation.id.indexOf(".") + 1))
    const properties = Object.keys(branch.properties)
    const forbidden = properties.includes("forbidden_probe") ? "forbidden_probe" : "zz_forbidden_probe"
    // The core-admitted minimal call plus one field the branch does not name:
    // the closed per-operation branch refuses it, the way the core's closed
    // admission refuses an undeclared field.
    const input: Record<string, unknown> = structuredClone(fixture.input_valid)
    input[forbidden] = true
    const failures: string[] = []
    const admitted = validateAgainstSchema(schema, { operation: operation.id.slice(operation.id.indexOf(".") + 1), input }, failures)
    expect(admitted, `${operation.id} admitted the forbidden field ${forbidden}`).toBe(false)
    const refusal = failures.join("; ")
    expect(refusal.includes(forbidden) || refusal.includes("exactly one is required"), `${operation.id}: ${refusal}`).toBe(true)
  }
})

test("the published workflow_action variants state each action's exact required set", () => {
  const schema = publishedSchema("concord_work_transition") as any
  const variants = workflowActionPublicVariants.map((variant: any) => actionVariantInput(schema, variant.action_id))
  // One closed variant per registry action, in registry order: the expanded
  // branch list is the registry projection, not a merged union.
  expect(variants.map((branch: any) => branch.properties.action_id.const)).toEqual(
    workflowActionPublicVariants.map((variant: any) => variant.action_id),
  )
  for (const input of variants) {
    for (const field of ["work_id", "expected_version", "action_id", "idempotency_key"]) {
      expect(input.required, `${input.properties.action_id.const} must require ${field}`).toContain(field)
    }
    for (const field of ["selected_choice", "decision_context_digest", "requested_budget_seconds"]) {
      expect(input.properties[field], `${input.properties.action_id.const} names ${field}`).toBeObject()
    }
  }
  // The registry declares approve_contract's fields required: the variant
  // states it, where the merged union left fields optional.
  const approve = actionVariantInput(schema, "approve_contract")
  expect(approve.required).toEqual(["work_id", "expected_version", "action_id", "idempotency_key", "fields"])
  // The retired result-size budget object is named nowhere on the published
  // surface; time is bounded only by requested_budget_seconds.
  expect(JSON.stringify(schema)).not.toContain('"budget"')
  expect(JSON.stringify(schema)).not.toContain("max_bytes")
  expect(JSON.stringify(schema)).not.toContain("max_items")
  expect(JSON.stringify(schema)).not.toContain("max_millis")
})

// CON-412 multibyte parity, both directions, at the published boundary. The
// store counts string bounds in UTF-8 bytes; the published code-point
// minLength/maxLength are derived from the byte bounds (maxLength = byte
// maximum, minLength = ceil(byte minimum / 4)) so they never refuse a
// core-admitted string, and x-maxBytes/x-minBytes carry the enforcing byte
// bounds this adapter's schema validator applies. These cases run through
// the same validateAgainstSchema the request conformance suite uses.
test("published string bounds admit and refuse with the core in both directions on multibyte boundaries", () => {
  const schema = publishedSchema("concord_work_transition")
  const premise = actionVariantInput(schema, "approve_contract").properties.fields.properties.premise
  expect(premise["x-maxBytes"]).toBe(4096)
  expect(premise["x-minBytes"]).toBe(2)
  expect(premise.minLength).toBe(1)
  expect(premise.maxLength).toBe(4096)
  const approveCall = (value: string) => ({
    operation: "workflow_action",
    input: {
      work_id: "work-multibyte-parity",
      expected_version: 4,
      action_id: "approve_contract",
      idempotency_key: "idem-multibyte-parity",
      fields: {
        premise: value,
        outcome_predicates: [{
          predicate_id: "predicate:multibyte-parity",
          ordinal: 0,
          outcome_kind: "check",
          outcome_payload: { kind: "check", check_ref: "check:multibyte-parity", immutable_subject_ref: "commit:multibyte-parity", expected_result: "pass" },
        }],
        required_evidence: ["verification"],
        route_conventions: [],
        spec_mandate: [],
        law_modifies: [],
        rigor_class: "prototype_internal",
      },
    },
  })
  // One code point of two bytes: the core byte floor admits it, so the
  // published bounds must admit it too (the old byte-copied minLength 2
  // refused it).
  const failures: string[] = []
  expect(validateAgainstSchema(schema, approveCall("é"), failures), failures.join("; ")).toBe(true)
  // One byte under the two-byte floor: the core refuses it and
  // x-minBytes refuses it, though the derived code-point minLength is 1.
  expect(validateAgainstSchema(schema, approveCall("v"), failures)).toBe(false)
  expect(failures.join("; ")).toContain("carries 1 UTF-8 bytes against a minimum of 2")
  // The byte maximum in ASCII: admitted on both counts.
  expect(validateAgainstSchema(schema, approveCall("v".repeat(4096)), failures)).toBe(true)
  // 4096 code points of two bytes each: 8192 bytes, over the byte maximum
  // the core enforces, under the code-point maxLength — only x-maxBytes
  // refuses it.
  expect(validateAgainstSchema(schema, approveCall("é".repeat(4096)), failures)).toBe(false)
  expect(failures.join("; ")).toContain("UTF-8 bytes against a limit of 4096")
})

// CON-412 nested kind equality: outcome_kind and outcome_payload.kind hold
// one value. The published items close per kind and the supersede pair
// branches close per kind, so a mismatched pair validates no published
// branch while the matching pair admits.
test("published outcome kinds refuse outcome_kind and outcome_payload.kind disagreements", () => {
  const schema = publishedSchema("concord_work_transition")
  const supersedeFields = (fields: Record<string, unknown>) => ({
    operation: "workflow_action",
    input: {
      work_id: "work-kind-parity",
      expected_version: 9,
      action_id: "supersede_contract",
      idempotency_key: "idem-kind-parity",
      fields,
    },
  })
  const commonFields = {
    contract_version: 2,
    premise: "the successor premise",
    required_evidence: ["verification"],
    route_conventions: [],
    spec_mandate: [],
    law_modifies: [],
    rigor_class: "prototype_internal",
    supersede_reason: "the operator corrected the requirement",
    audit_evidence: ["evidence:kind-parity"],
  }
  const failures: string[] = []
  // Matching check pair admits.
  expect(validateAgainstSchema(schema, supersedeFields({
    ...commonFields,
    outcome_kind: "check",
    outcome_payload: { kind: "check", check_ref: "check:kind-parity", immutable_subject_ref: "commit:kind-parity", expected_result: "pass" },
  } as any), failures), failures.join("; ")).toBe(true)
  // Mismatched pair refuses: outcome_kind exists with a check payload.
  expect(validateAgainstSchema(schema, supersedeFields({
    ...commonFields,
    outcome_kind: "exists",
    outcome_payload: { kind: "check", check_ref: "check:kind-parity", immutable_subject_ref: "commit:kind-parity", expected_result: "pass" },
  } as any), failures)).toBe(false)
  // Mismatched approve item refuses: outcome_kind exists with a check payload.
  const approveFields = actionVariantInput(schema, "approve_contract").properties.fields.properties
  const itemMismatch = {
    work_id: "work-kind-parity",
    expected_version: 4,
    action_id: "approve_contract",
    idempotency_key: "idem-kind-parity-approve",
    fields: {
      premise: "the approved premise",
      outcome_predicates: [{
        predicate_id: "predicate:kind-parity",
        ordinal: 0,
        outcome_kind: "exists",
        outcome_payload: { kind: "check", check_ref: "check:kind-parity", immutable_subject_ref: "commit:kind-parity", expected_result: "pass" },
      }],
      required_evidence: ["verification"],
      route_conventions: [],
      spec_mandate: [],
      law_modifies: [],
      rigor_class: "prototype_internal",
    },
  }
  expect(approveFields.outcome_predicates.items.oneOf.length).toBe(4)
  expect(validateAgainstSchema(schema, { operation: "workflow_action", input: itemMismatch }, failures)).toBe(false)
  expect(validateAgainstSchema(schema, {
    operation: "workflow_action",
    input: { ...itemMismatch, fields: { ...itemMismatch.fields, outcome_predicates: [{ ...itemMismatch.fields.outcome_predicates[0], outcome_kind: "check" }] } },
  }, failures), failures.join("; ")).toBe(true)
})

// --- The CON-812 parity oracle ---------------------------------------------
//
// The compact publication must stay semantically lossless against the
// authored payload contract. Two independent authorities decide every
// verdict: the generated payload validator runs the authored schema
// document, and the host validator runs the raw compact published document
// and the final hooked argument root. The expanded view never decides
// admission; it only selects branches for structural comparison.

// authoredInput is the authored schema document for one operation input,
// used as the independent oracle surface.
function authoredInput(name: string): any {
  return (payloadSchemas as Record<string, any>)[name]
}

// inlinedProperty resolves one authored property one level, merging sibling
// keywords the way publication does.
function inlinedProperty(schema: any): any {
  if (schema === null || typeof schema !== "object" || typeof schema.$ref !== "string") return schema
  return mergeRefSiblings((payloadSchemas as Record<string, any>)[schema.$ref.replace("#/$defs/", "")], schema)
}

function mergeRefSiblings(target: any, node: any): any {
  if (target === undefined) throw new Error(`authored property names the unknown reference ${node.$ref}`)
  const merged: Record<string, unknown> = { ...target }
  for (const [key, value] of Object.entries(node)) {
    if (key === "$ref") continue
    if (key === "required" && Array.isArray(value) && Array.isArray(merged.required)) {
      merged.required = [...new Set([...(merged.required as unknown[]), ...value])]
    } else if (key === "properties" && typeof value === "object" && value !== null && !Array.isArray(value) && typeof merged.properties === "object" && merged.properties !== null) {
      merged.properties = { ...(merged.properties as Record<string, unknown>), ...(value as Record<string, unknown>) }
    } else {
      merged[key] = value
    }
  }
  return merged
}

// deepInline resolves every authored reference, so the structural comparison
// below compares like with like: the published form is fully inlined.
function deepInline(value: any, resolving = new Set<string>()): any {
  if (Array.isArray(value)) return value.map((item) => deepInline(item, resolving))
  if (value === null || typeof value !== "object") return value
  if (typeof value.$ref === "string") {
    const name = value.$ref.replace("#/$defs/", "")
    if (resolving.has(name)) return {}
    resolving.add(name)
    try {
      return deepInline(mergeRefSiblings((payloadSchemas as Record<string, any>)[name], value), resolving)
    } finally {
      resolving.delete(name)
    }
  }
  const out: Record<string, unknown> = {}
  for (const [key, child] of Object.entries(value)) out[key] = deepInline(child, resolving)
  return out
}

// expectStructureLossless compares the authored schema subtree with the
// effective published one. Union keywords are compared branch by branch, so
// the constraint keywords each authored branch carries must survive
// factoring's hoist-and-inherit round trip; scalar keywords, descriptions,
// and bounds must be equal everywhere.
function expectStructureLossless(authored: any, actual: any, label: string): void {
  const inlined = deepInline(authored)
  for (const [keyword, value] of Object.entries(inlined)) {
    if (keyword === "properties") {
      for (const [name, property] of Object.entries(value as Record<string, unknown>)) {
        expectStructureLossless(property, (actual?.properties as Record<string, unknown> | undefined)?.[name], `${label}.${name}`)
      }
      continue
    }
    if (keyword === "items") {
      expectStructureLossless(value, actual?.items, `${label}[]`)
      continue
    }
    if (keyword === "oneOf" || keyword === "anyOf") {
      const branches = value as unknown[]
      const actualBranches = actual?.[keyword]
      expect(Array.isArray(actualBranches), `${label} carries ${keyword}`).toBe(true)
      expect(actualBranches ?? [], `${label} ${keyword} branch count`).toHaveLength(branches.length)
      for (const [index, branch] of branches.entries()) {
        expectStructureLossless(branch, (actualBranches as unknown[])[index], `${label} ${keyword}[${index}]`)
      }
      continue
    }
    if (keyword === "not" || keyword === "if" || keyword === "then" || keyword === "else" || keyword === "allOf") {
      expect(actual?.[keyword] !== undefined, `${label} keeps ${keyword}`).toBe(true)
      continue
    }
    if (keyword === "required") {
      // JSON Schema required is an unordered set; factoring may move the
      // fields every branch shares to the union parent, and the expansion
      // unions them back, so the sets must match, not their orders.
      const actualRequired = Array.isArray(actual?.[keyword]) ? actual?.[keyword] : []
      expect([...(actualRequired as string[])].sort(), `${label} keeps required`).toEqual([...(value as string[])].sort())
      continue
    }
    expect(actual?.[keyword], `${label} keeps ${keyword}`).toEqual(value)
  }
}

// sample mirrors the fixture generator's own sampler
// (scripts/generate-agent-contracts.py sample()), so the minimal calls the
// oracle builds come from the same source the fixtures come from, not from a
// handwritten payload list.
function sample(schemaNode: any): unknown {
  const schema = inlinedProperty(schemaNode)
  if ("const" in schema) return schema.const
  if (schema.oneOf) {
    const branch = schema.oneOf[0]
    const result = sample(branch)
    const base: Record<string, unknown> = {}
    for (const key of schema.required ?? []) base[key] = sample(schema.properties?.[key] ?? {})
    if (result !== null && typeof result === "object" && !Array.isArray(result)) return { ...base, ...result }
    if (branch !== null && typeof branch === "object" && !Array.isArray(branch)) {
      const inlined = inlinedProperty(branch)
      for (const key of inlined.required ?? []) base[key] = sample(inlined.properties?.[key] ?? {})
    }
    return base
  }
  if (schema.anyOf) return sample(schema.anyOf[0])
  if (schema.allOf) {
    const { allOf, ...rest } = schema
    let result = sample(rest) ?? {}
    for (const branch of allOf) result = { ...(result as Record<string, unknown>), ...(sample(branch) as Record<string, unknown> ?? {}) }
    return result
  }
  if (schema.enum) return schema.enum[0]
  const types = Array.isArray(schema.type) ? schema.type : schema.type ? [schema.type] : []
  const kind = types.filter((candidate: string) => candidate !== "null")[0]
  if (kind === "object") {
    const result: Record<string, unknown> = {}
    for (const key of schema.required ?? []) result[key] = sample(schema.properties?.[key] ?? {})
    return result
  }
  if (kind === "array") return (schema.minItems ?? 0) > 0 ? [sample(schema.items ?? {})] : []
  if (kind === "string") {
    if (schema.format === "date-time") return "2026-08-08T00:00:00Z"
    const pattern: string = schema.pattern ?? ""
    if (pattern.includes("sha256:")) return "sha256:" + "0".repeat(64)
    if (pattern.includes("[0-9a-f]{40}")) return "0".repeat(40)
    if (pattern.startsWith("^[a-z][a-z0-9_-]")) return "fence:prod-pause"
    if (pattern.startsWith("^msg:")) return "msg:" + "0".repeat(32)
    if (pattern.startsWith("^https://")) return "https://example.test/pull/1"
    if (pattern.includes("date")) return "2026-08-08T00:00:00Z"
    return "id-1"
  }
  if (kind === "integer" || kind === "number") return schema.minimum ?? 1
  if (kind === "boolean") return false
  return null
}

// wrongValues names values of a genuinely different type than the declared
// one, so a nullable or multi-typed field never reports a false wrong-type
// refusal.
function wrongValues(property: any): unknown[] {
  const schema = inlinedProperty(property)
  const types = new Set(Array.isArray(schema.type) ? schema.type : schema.type ? [schema.type] : [])
  const matches = (value: unknown): boolean => {
    if (value === null) return types.has("null")
    if (typeof value === "boolean") return types.has("boolean")
    if (typeof value === "number") return types.has("number") || types.has("integer")
    if (typeof value === "string") return types.has("string")
    if (Array.isArray(value)) return types.has("array")
    return types.has("object")
  }
  return [null, false, 0, "", [], {}].filter((value) => !matches(value))
}

type Verdict = { ok: boolean; failures: string[] }

function compactVerdict(toolName: string, call: unknown): Verdict {
  const failures: string[] = []
  return { ok: validateAgainstSchema(publishedSchema(toolName), call, failures), failures }
}

async function rootVerdict(toolName: string, call: unknown): Promise<Verdict> {
  const failures: string[] = []
  return { ok: validateAgainstSchema(await finalRoot(toolName), { request: call }, failures), failures }
}

function authoredVerdict(inputSchema: string, input: unknown): Verdict {
  return { ok: validateGeneratedPayload(inputSchema, input), failures: [] }
}

// expectParity asserts the authored oracle and both published surfaces give
// the same verdict, naming the losing surface when they diverge.
async function expectParity(toolName: string, inputSchema: string, call: { operation: string; input: unknown }, label: string): Promise<void> {
  const authored = authoredVerdict(inputSchema, call.input)
  const compact = compactVerdict(toolName, call)
  const root = await rootVerdict(toolName, call)
  expect(compact.ok, `${label}: compact published request ${compact.ok ? "admitted" : `refused: ${compact.failures.join("; ")}`} where the authored contract ${authored.ok ? "admits" : "refuses"}`)
    .toBe(authored.ok)
  expect(root.ok, `${label}: final hooked root ${root.ok ? "admitted" : `refused: ${root.failures.join("; ")}`} where the authored contract ${authored.ok ? "admits" : "refuses"}`)
    .toBe(authored.ok)
}

// refuseEverywhere asserts the closed published surfaces refuse a payload
// outright: missing requireds and forbidden fields have no admitting branch.
async function refuseEverywhere(toolName: string, call: { operation: string; input: unknown }, label: string): Promise<void> {
  const compact = compactVerdict(toolName, call)
  const root = await rootVerdict(toolName, call)
  expect(compact.ok, `${label}: compact published request admitted`).toBe(false)
  expect(root.ok, `${label}: final hooked root admitted`).toBe(false)
}

// boundedStringPath finds the first declared string ceiling in a schema
// tree and names its payload path, so each action's battery proves one bound
// is enforced after publication. Steps are property names; `items` becomes a
// first-element step.
function boundedStringPath(node: unknown, path: string[] = [], seen = new Set<unknown>()): { path: string[]; maxLength: number } | null {
  if (node === null || typeof node !== "object" || seen.has(node)) return null
  seen.add(node)
  if (Array.isArray(node)) {
    for (const item of node) {
      const found = boundedStringPath(item, path, seen)
      if (found) return found
    }
    return null
  }
  const schema = node as Record<string, unknown>
  if (typeof schema.maxLength === "number" && (schema.type === "string" || (Array.isArray(schema.type) && schema.type.includes("string")))) {
    return { path, maxLength: schema.maxLength }
  }
  for (const [key, value] of Object.entries(schema)) {
    if (key === "properties" && value !== null && typeof value === "object") {
      for (const [name, property] of Object.entries(value as Record<string, unknown>)) {
        const found = boundedStringPath(property, [...path, name], seen)
        if (found) return found
      }
      continue
    }
    if (key === "items") {
      const found = boundedStringPath(value, [...path, "[0]"], seen)
      if (found) return found
      continue
    }
    if (["oneOf", "anyOf", "allOf"].includes(key) && Array.isArray(value)) {
      for (const branch of value) {
        const found = boundedStringPath(branch, path, seen)
        if (found) return found
      }
      continue
    }
    if (key === "description" || key === "enum" || key === "const") continue
    const found = boundedStringPath(value, path, seen)
    if (found) return found
  }
  return null
}

function stepValue(holder: unknown, step: string): unknown {
  const index = /^\[(\d+)\]$/.exec(step)
  if (index) return (holder as unknown[])[Number(index[1])]
  return (holder as Record<string, unknown>)[step]
}

function payloadAt(payload: unknown, path: string[]): Record<string, unknown> | null {
  let holder: unknown = payload
  for (const step of path.slice(0, -1)) {
    if (holder === null || typeof holder !== "object") return null
    holder = stepValue(holder, step)
  }
  if (holder === null || typeof holder !== "object" || Array.isArray(holder)) return null
  return holder as Record<string, unknown>
}

test("every workflow action's effective branch is structurally equal to its authored variant", () => {
  const schema = publishedSchema("concord_work_transition")
  const expanded: any = expandedPublishedRequestSchema(schema)
  for (const variant of workflowActionPublicVariants) {
    const authored = authoredInput(variant.schema)
    const effective = actionVariantInput(schema, variant.action_id)
    // Factoring hoists shared constraints to the grouped input parent; the
    // expansion inherits them back. Nothing may be lost: the effective
    // branch carries the authored required set, the authored field set, and
    // every authored keyword value, descriptions included.
    expect(effective.required, `${variant.action_id} effective required set`).toEqual(authored.required)
    expect(Object.keys(effective.properties).sort(), `${variant.action_id} effective field set`).toEqual(Object.keys(authored.properties).sort())
    expect(effective.additionalProperties, `${variant.action_id} stays closed`).toBe(false)
    expect(effective.type, `${variant.action_id} stays an object`).toBe("object")
    for (const [field, authoredProperty] of Object.entries(authored.properties)) {
      expectStructureLossless(authoredProperty, effective.properties[field], `${variant.action_id}.${field}`)
    }
    // The action branch keeps its discriminated description: factoring
    // carries it on the grouped variant and the legacy view restores it.
    const actionBranch = expanded.oneOf.find((candidate: any) => candidate.properties?.input?.properties?.action_id?.const === variant.action_id)
    expect(
      actionBranch?.description ?? actionBranch?.properties?.input?.description,
      `${variant.action_id} branch description`,
    ).toContain(`workflow_action action ${variant.action_id}`)
  }
})

test("every workflow action admits its minimal call and refuses the broken ones on both published surfaces", async () => {
  const toolName = "concord_work_transition"
  const inputSchema = "work_transition_action_public_input"
  for (const variant of workflowActionPublicVariants) {
    const authored = authoredInput(variant.schema)
    const minimal: Record<string, unknown> = {}
    for (const key of authored.required ?? []) minimal[key] = sample(authored.properties[key])
    const call = { operation: "workflow_action", input: minimal }
    // The sampled minimal call must be a real core-admitted call; a sampling
    // gap is a failure of this test, not a silent pass.
    expect(authoredVerdict(inputSchema, minimal).ok, `${variant.action_id} sampled minimal call is core-admitted`).toBe(true)
    await expectParity(toolName, inputSchema, call, `${variant.action_id} minimal call`)
    // Missing requireds: each published surface refuses what the authored
    // closed variant refuses.
    for (const field of authored.required ?? []) {
      const broken: Record<string, unknown> = { ...minimal }
      delete broken[field]
      await refuseEverywhere(toolName, { operation: "workflow_action", input: broken }, `${variant.action_id} missing ${field}`)
    }
    // Wrong types: one wrong replacement per required field, and the
    // published verdict must equal the authored verdict on each.
    let index = 0
    for (const field of Object.keys(minimal)) {
      for (const wrong of wrongValues(authored.properties[field])) {
        const broken: Record<string, unknown> = { ...minimal, [field]: wrong }
        await expectParity(toolName, inputSchema, { operation: "workflow_action", input: broken }, `${variant.action_id} wrong-type ${field} ${JSON.stringify(wrong)}`)
        index++
        if (index > 24) break
      }
      if (index > 24) break
    }
    // Forbidden fields: the closed variant refuses an undeclared sibling.
    await refuseEverywhere(toolName, { operation: "workflow_action", input: { ...minimal, zz_forbidden_probe: true } }, `${variant.action_id} forbidden field`)
    // Nested variant: a declared outcome_predicates array must keep its
    // per-kind closure after factoring.
    const fields = minimal.fields as Record<string, unknown> | undefined
    if (fields && Array.isArray(fields.outcome_predicates) && fields.outcome_predicates.length > 0) {
      const nested = structuredClone(minimal)
      ;(nested.fields as Record<string, unknown>).outcome_predicates = [
        { ...(fields.outcome_predicates as Record<string, unknown>[])[0], outcome_kind: "check" },
      ]
      await expectParity(toolName, inputSchema, { operation: "workflow_action", input: nested }, `${variant.action_id} nested outcome-kind flip`)
    }
    // Bounds: one declared string ceiling must still refuse the over-long
    // value on both surfaces, at a payload position the minimal call
    // actually carries.
    const bound = boundedStringPath(authored.properties.fields)
    if (bound) {
      const oversized = structuredClone(minimal)
      const holder = payloadAt(oversized.fields, bound.path)
      const finalStep = bound.path[bound.path.length - 1]
      if (holder && finalStep !== undefined && typeof stepValue(holder, finalStep) === "string") {
        holder[finalStep] = "v".repeat(bound.maxLength + 1)
        await expectParity(toolName, inputSchema, { operation: "workflow_action", input: oversized }, `${variant.action_id} oversized bound probe`)
      }
    }
  }
})

test("every operation's minimal call and forbidden field agree across the authored contract, the compact request, and the final hooked root", async () => {
  const bySchema = new Map(contractOperations.map((operation: any) => [operation.input_schema.replace("#/schemas/", ""), operation]))
  for (const fixture of fixtures.fixtures) {
    const operation = bySchema.get(fixture.input_schema)
    if (operation === undefined) throw new Error(`fixture schema ${fixture.input_schema} names no contract operation`)
    const operationName = operation.id.slice(operation.id.indexOf(".") + 1)
    const call = { operation: operationName, input: fixture.input_valid }
    // The fixture is the generated core-admitted call; the authored oracle
    // must admit it before it can judge publication.
    expect(authoredVerdict(fixture.input_schema, fixture.input_valid).ok, `${operation.id} fixture is core-admitted`).toBe(true)
    await expectParity(operation.tool, fixture.input_schema, call, `${operation.id} fixture call`)
    // Missing requireds, per published surface, against the effective
    // branch's required set.
    const input = operationName === "workflow_action"
      ? actionVariantInput(publishedSchema(operation.tool), String((fixture.input_valid as Record<string, unknown>).action_id))
      : operationInput(publishedSchema(operation.tool), operationName)
    for (const field of input.required ?? []) {
      const broken: Record<string, unknown> = structuredClone(fixture.input_valid)
      delete broken[field]
      await refuseEverywhere(operation.tool, { operation: operationName, input: broken }, `${operation.id} missing ${field}`)
    }
    // The forbidden field refuses on the final hooked root too.
    const root = await rootVerdict(operation.tool, { operation: operationName, input: { ...fixture.input_valid, zz_forbidden_probe: true } })
    expect(root.ok, `${operation.id}: final hooked root admitted the forbidden field`).toBe(false)
  }
})

test("nested oneOf selectors keep their exactly-one exclusions on both published surfaces", async () => {
  // product_view resolve: product_id and project_id are exclusive.
  const resolveInput = { operation: "resolve", input: { product_id: "prod-1", project_id: "proj-1" } }
  await refuseEverywhere("concord_product_view", resolveInput, "resolve product/project exclusivity")
  // outside_repair_reconcile: release_tag and pull_requests cannot combine.
  const reconcileInput = {
    operation: "outside_repair_reconcile",
    input: { work_id: "work-selector", expected_version: 1, reason: "the operator repaired outside the workflow", mode: "completed", release_tag: "v1.2.3", pull_requests: [12] },
  }
  await refuseEverywhere("concord_work_transition", reconcileInput, "outside repair reconcile exclusivity")
})

test("published references refuse missing, external, and cyclic definitions", () => {
  const schema = structuredClone(publishedSchema("concord_work_transition"))
  // A reference naming a definition the document does not carry is a
  // publication fault: the traversal throws, and the host validator fails
  // closed for the same document.
  const usedRefs = new Set<string>()
  const collect = (value: unknown) => {
    if (Array.isArray(value)) return value.forEach(collect)
    if (value !== null && typeof value === "object") {
      for (const [key, child] of Object.entries(value)) {
        if (key === "$ref" && typeof child === "string" && child.startsWith("#/$defs/")) usedRefs.add(child.slice("#/$defs/".length))
        else collect(child)
      }
    }
  }
  collect(schema)
  expect(usedRefs.size).toBeGreaterThan(0)
  const missing = structuredClone(schema)
  delete missing.$defs[[...usedRefs][0]]
  expect(() => expandedPublishedRequestSchema(missing)).toThrow(/resolves no local definition/)
  // The host validator fails closed on an unresolvable reference: a document
  // whose reference names no definition refuses every value instead of
  // admitting one.
  const failures: string[] = []
  expect(validateAgainstSchema({ $defs: {}, $ref: "#/$defs/d0" }, { any: "value" }, failures)).toBe(false)
  // An external reference is not publication.
  expect(() => expandedPublishedRequestSchema({ type: "object", oneOf: [{ $ref: "https://external.example/schema" }] })).toThrow(/non-local reference/)
  // A cyclic reference chain never resolves.
  expect(() => expandedPublishedRequestSchema({
    $defs: { d0: { $ref: "#/$defs/d1" }, d1: { $ref: "#/$defs/d0" } },
    type: "object",
    oneOf: [{ $ref: "#/$defs/d0" }],
  })).toThrow(/cyclic/)
})
