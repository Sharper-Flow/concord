import { test, expect } from "bun:test"
import { readFileSync } from "node:fs"
import { contractOperations, manifestDigest, workflowActionPublicVariants } from "./generated-contracts"
import { validateAgainstSchema } from "./dispatch"

const adapter = await import("./concord")

// The published schema is the only contract a calling agent reads before the
// first call. This conformance suite proves, per operation, that the schema
// the host publishes admits the same minimal calls the generated fixture
// corpus proves the core admits, and refuses the fields the core refuses.
const fixtures = JSON.parse(readFileSync(new URL("../../contracts/agent-tool-surface.fixtures.json", import.meta.url), "utf8")) as {
  manifest_digest: string
  fixtures: { input_schema: string; input_valid: Record<string, unknown> }[]
}

const publishedSchemas = new Map<string, unknown>()
function publishedSchema(toolName: string): any {
  if (!publishedSchemas.has(toolName)) publishedSchemas.set(toolName, adapter.publishedRequestSchema(toolName))
  return publishedSchemas.get(toolName)
}

function branchFor(schema: any, operation: string): any {
  const branch = schema.oneOf.find((candidate: any) => candidate.properties.operation.const === operation)
  if (branch === undefined) throw new Error(`published schema carries no ${operation} branch`)
  return branch
}

// workflow_action publishes one closed branch per registry action variant;
// select the branch by its action_id const, never by list position.
function actionVariantBranch(schema: any, actionId: string): any {
  const branch = schema.oneOf.find(
    (candidate: any) => candidate.properties.operation.const === "workflow_action"
      && candidate.properties.input.properties.action_id.const === actionId,
  )
  if (branch === undefined) throw new Error(`published schema carries no closed ${actionId} variant branch`)
  return branch
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
      ? actionVariantBranch(schema, actionId)
      : branchFor(schema, operation.id.slice(operation.id.indexOf(".") + 1))
    const properties = Object.keys(branch.properties.input.properties)
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
  const variants = schema.oneOf.filter((branch: any) => branch.properties.operation.const === "workflow_action")
  // One closed variant per registry action, in registry order: the published
  // branch list is the registry projection, not a merged union.
  expect(variants.map((branch: any) => branch.properties.input.properties.action_id.const)).toEqual(
    workflowActionPublicVariants.map((variant: any) => variant.action_id),
  )
  for (const branch of variants) {
    const input = branch.properties.input
    for (const field of ["work_id", "expected_version", "action_id", "idempotency_key"]) {
      expect(input.required, `${input.properties.action_id.const} must require ${field}`).toContain(field)
    }
    for (const field of ["selected_choice", "decision_context_digest", "requested_budget_seconds"]) {
      expect(input.properties[field], `${input.properties.action_id.const} names ${field}`).toBeObject()
    }
  }
  // The registry declares approve_contract's fields required: the variant
  // states it, where the merged union left fields optional.
  const approve = actionVariantBranch(schema, "approve_contract").properties.input
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
  const approveBranch = actionVariantBranch(schema, "approve_contract")
  const premise = approveBranch.properties.input.properties.fields.properties.premise
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
  const supersedeBranch = actionVariantBranch(schema, "supersede_contract")
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
  const approveBranch = actionVariantBranch(schema, "approve_contract")
  const approveFields = approveBranch.properties.input.properties.fields.properties
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
