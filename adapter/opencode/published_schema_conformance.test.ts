import { test, expect } from "bun:test"
import { readFileSync } from "node:fs"
import { contractOperations, manifestDigest } from "./generated-contracts"
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

test("the fixture corpus is the generated manifest corpus", () => {
  expect(fixtures.manifest_digest).toBe(manifestDigest)
  expect(fixtures.fixtures).toHaveLength(contractOperations.length)
})

test("the published schema admits the core-admitted minimal call of every operation", () => {
  const bySchema = new Map(contractOperations.map((operation: any) => [operation.input_schema.replace("#/schemas/", ""), operation]))
  for (const fixture of fixtures.fixtures) {
    const operation = bySchema.get(fixture.input_schema === "work_transition_action_public_input" ? "work_transition_action_input" : fixture.input_schema)
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
    const operation = bySchema.get(fixture.input_schema === "work_transition_action_public_input" ? "work_transition_action_input" : fixture.input_schema)
    if (operation === undefined) throw new Error(`fixture schema ${fixture.input_schema} names no contract operation`)
    const schema = publishedSchema(operation.tool)
    const branch = branchFor(schema, operation.id.slice(operation.id.indexOf(".") + 1))
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

test("the published workflow_action branch names the fields the merged view dropped", () => {
  const schema = publishedSchema("concord_work_transition") as any
  const action = branchFor(schema, "workflow_action").properties.input
  for (const field of ["action_id", "selected_choice", "decision_context_digest", "requested_budget_seconds"]) {
    expect(action.properties[field], field).toBeObject()
  }
  expect(action.required).toEqual(["work_id", "expected_version", "action_id", "idempotency_key"])
  // The retired result-size budget object is named nowhere on the published
  // surface; time is bounded only by requested_budget_seconds.
  expect(JSON.stringify(schema)).not.toContain('"budget"')
  expect(JSON.stringify(schema)).not.toContain("max_bytes")
  expect(JSON.stringify(schema)).not.toContain("max_items")
  expect(JSON.stringify(schema)).not.toContain("max_millis")
})
