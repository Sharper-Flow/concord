import { test, expect } from "bun:test"
import { contractOperations } from "./generated-contracts"
import { publishedRequestSchema } from "./concord"

const toolNames = [...new Set(contractOperations.map((operation: any) => operation.tool))]

function forbiddenNodes(value: unknown, path = "$", found: string[] = []): string[] {
  if (typeof value !== "object" || value === null) return found
  if (Array.isArray(value)) {
    value.forEach((item, index) => forbiddenNodes(item, `${path}[${index}]`, found))
    return found
  }
  for (const [key, item] of Object.entries(value)) {
    if (key === "$ref" || key === "oneOf") found.push(`${path}.${key}`)
    forbiddenNodes(item, `${path}.${key}`, found)
  }
  return found
}

test("published request schemas are flattened and ref-free for the host", () => {
  for (const toolName of toolNames) {
    const schema = publishedRequestSchema(toolName) as any
    expect(forbiddenNodes(schema), toolName).toEqual([])
    expect(schema).toMatchObject({
      type: "object",
      required: ["operation", "input"],
      properties: {
        operation: { type: "string" },
        input: { type: "object", additionalProperties: true },
      },
    })
  }
})

test("the flattened workflow input carries outcome predicate kinds", () => {
  const schema = publishedRequestSchema("concord_work_transition") as any
  const predicate = schema.properties.input.properties.fields.properties.outcome_predicates.items
  expect(predicate.properties.outcome_kind.enum).toEqual(["exists", "absent", "outcome", "check"])
  expect(predicate.properties.outcome_payload.properties.kind.enum).toEqual(["exists", "absent", "outcome", "check"])
  expect(predicate.properties.outcome_payload.properties.kind.type).toBe("string")
})
