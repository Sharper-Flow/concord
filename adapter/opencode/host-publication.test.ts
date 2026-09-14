import { test, expect, mock } from "bun:test"
import { contractOperations } from "./generated-contracts"

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
    expect(key === "oneOf", `${path} contains a union`).toBe(false)
    expect(key === "anyOf", `${path} contains a union`).toBe(false)
    expect(key === "allOf", `${path} contains a union`).toBe(false)
    expect(key === "definitions", `${path} contains definitions`).toBe(false)
    inspectHostSchema(child, `${path}.${key}`, seen)
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

test("flattened workflow action fields retain every outcome payload member", () => {
  const schema = adapter.publishedRequestSchema("concord_work_transition") as any
  const payload = schema.properties.input.properties.fields.properties.outcome_predicates.items.properties.outcome_payload
  expect(payload).toMatchObject({ type: "object", additionalProperties: true, required: [] })
  expect(payload.properties.kind).toEqual({ type: "string" })
  expect(payload.properties.check_ref.type).toBe("string")
  expect(payload.properties.expected_result.enum).toContain("pass")
})
