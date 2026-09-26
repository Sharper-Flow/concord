import { test, expect, mock } from "bun:test"
import { contractOperations } from "./generated-contracts"
import { advertisedAdmissionTeachingGaps } from "./generated-contract-tests"
import { validateAgainstSchema } from "./dispatch"

const fakeTool = (config: unknown) => config
mock.module("@opencode-ai/plugin", () => ({ tool: fakeTool }))

const adapter = await import("./concord")

function inspectHostSchema(value: unknown, path = "$", seen = new Set<unknown>()): void {
  if (typeof value !== "object" || value === null || seen.has(value)) return
  seen.add(value)
  if (Array.isArray(value)) {
    value.forEach((item, index) => inspectHostSchema(item, `${path}[${index}]`, seen))
    return
  }
  for (const [key, child] of Object.entries(value)) {
    expect(key === "$ref", `${path} contains a reference`).toBe(false)
    expect(key === "anyOf", `${path} contains a union`).toBe(false)
    expect(key === "allOf", `${path} contains a union`).toBe(false)
    expect(key === "definitions", `${path} contains definitions`).toBe(false)
    // oneOf survives only as a bounded variant node: every branch is a
    // self-contained closed object the host renders directly, as the
    // outcome_payload variants do. Open or nested unions stay merged.
    if (key === "oneOf") {
      expect(Array.isArray(child), `${path} oneOf is a list`).toBe(true)
      for (const [index, branch] of (child as unknown[]).entries()) {
        expect(branch, `${path} oneOf branch ${index} is an object`).toBeObject()
        expect((branch as Record<string, unknown>).additionalProperties, `${path} oneOf branch ${index} is closed`).toBe(false)
      }
    } else {
      inspectHostSchema(child, `${path}.${key}`, seen)
    }
  }
}

test("published request schemas are flattened and safe for host publication", () => {
  const tools = [...new Set(contractOperations.map((operation: any) => operation.tool))]
  for (const toolName of tools) {
    const schema = adapter.publishedRequestSchema(toolName) as any
    inspectHostSchema(schema, toolName)
    expect(schema).toMatchObject({
      type: "object",
      required: ["operation", "input"],
      properties: {
        operation: { type: "string" },
        input: { type: "object", required: [], additionalProperties: true },
      },
    })
    expect(schema.properties.operation.enum).toEqual(
      contractOperations.filter((operation: any) => operation.tool === toolName).map((operation: any) => operation.id.split(".")[1]),
    )
  }
})

test("published workflow transition teaches every approve_contract admission rule", () => {
  const schema = adapter.publishedRequestSchema("concord_work_transition") as any
  // The advertised schema carries all four store admission rules; the store's
  // ValidateOperationPayload stays the closed boundary.
  expect(advertisedAdmissionTeachingGaps(schema)).toEqual([])
  const items = schema.properties.input.properties.fields.properties.outcome_predicates.items
  expect(items.required).toEqual(["predicate_id", "ordinal", "outcome_kind", "outcome_payload"])
  expect(items.additionalProperties).toBe(false)
  expect(items.properties.outcome_payload.oneOf.map((branch: any) => branch.properties.kind.const)).toEqual(["exists", "absent", "outcome", "check"])

  // Schema-vs-payload conformance: a canonical workflow.research
  // approve_contract payload validates against the advertised schema with
  // zero unexplained gaps.
  const canonical = {
    operation: "workflow_action",
    input: {
      work_id: "work-conformance",
      expected_version: 2,
      action_id: "approve_contract",
      idempotency_key: "idem-admission-conformance",
      premise: "Advertise the admission rules the store enforces.",
      fields: {
        outcome_predicates: [{
          predicate_id: "predicate:admission-conformance",
          ordinal: 0,
          outcome_kind: "outcome",
          outcome_payload: { kind: "outcome", allowed: ["report_recorded"] },
        }],
      },
    },
  }
  const failures: string[] = []
  expect(validateAgainstSchema(schema, canonical, failures), failures.join("; ")).toBe(true)
  expect(failures).toEqual([])

  // The advertised required set is enforced, not merely rendered: a predicate
  // missing its ordinal fails against the advertised schema.
  const missingOrdinal = structuredClone(canonical)
  delete (missingOrdinal.input.fields.outcome_predicates[0] as Record<string, unknown>).ordinal
  const missingFailures: string[] = []
  expect(validateAgainstSchema(schema, missingOrdinal, missingFailures)).toBe(false)
  expect(missingFailures.join("; ")).toContain("ordinal")
})
