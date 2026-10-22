import { expect, test } from "bun:test"
import { publishWorkStartDefinition, work_start } from "./concord"
import { validateAgainstSchema } from "./dispatch"
import { hostToolSchemas } from "./generated-contracts"

async function publishedSchema(): Promise<Record<string, any>> {
  const definition = { description: work_start.description, parameters: {}, jsonSchema: undefined as unknown }
  await publishWorkStartDefinition({ toolID: "concord_work_start" }, definition)
  return definition.jsonSchema as Record<string, any>
}

test("work start publishes an object without Anthropic-forbidden root combinators", async () => {
  const schema = await publishedSchema()
  expect(schema.type).toBe("object")
  for (const keyword of ["oneOf", "anyOf", "allOf"]) expect(Object.hasOwn(schema, keyword), keyword).toBe(false)
  expect(Object.keys(schema.properties).sort()).toEqual(Object.keys(work_start.args).sort())
})

test("work start publication admits exactly the generated capture and resume inputs", async () => {
  const schema = await publishedSchema()
  const capture = { title: "Test", value_statement: "Test", kind: "task", task: "Test", idempotency_key: "test-key" }
  const resume = { work_id: "work-test" }
  const issueResume = { issue_key: "EX-3" }
  const candidates: unknown[] = [
    null, [], "capture", 1, {}, capture, resume, issueResume,
    { ...resume, project_id: "project-test" },
    { ...issueResume, project_id: "project-test" },
    { ...resume, ...issueResume },
    { issue_key: "" }, { issue_key: null }, { issue_key: "ex-3" }, { issue_key: "EX-0" },
    { issue_key: "A-1" }, { issue_key: `E${"X".repeat(61)}-3` },
    { issue_key: "A-" }, { issue_key: 3 }, { issue_key: " EX-3" }, { issue_key: "EX-03" },
    { issue_key: `E${"X".repeat(62)}-3` },
    { ...capture, ...issueResume },
    { ...issueResume, extra: "undeclared" },
    { work_id: "" }, { work_id: null }, { work_id: "invalid identity" },
    { ...capture, work_id: "work-test" },
    { ...capture, project_id: "project-test" },
    { ...capture, kind: "invalid" },
    { ...capture, priority: -101 }, { ...capture, priority: -100 },
    { ...capture, priority: 100 }, { ...capture, priority: 101 },
    { ...capture, priority: 0.5 }, { ...capture, priority: "1" },
    { ...capture, title: "" }, { ...capture, title: "x".repeat(257) },
    { ...capture, tags: ["tag-test", "tag-test"] },
    { ...capture, extra: "undeclared" }, { ...resume, extra: "undeclared" },
  ]
  for (const field of hostToolSchemas.concord_work_start.oneOf[0].required) {
    const missing: Record<string, unknown> = { ...capture }
    delete missing[field]
    candidates.push(missing)
  }
  for (const field of Object.keys(hostToolSchemas.concord_work_start.oneOf[0].properties)) {
    candidates.push({ ...resume, [field]: "capture-only" })
    candidates.push({ ...issueResume, [field]: "capture-only" })
  }
  for (const value of candidates) {
    expect(validateAgainstSchema(schema, value), JSON.stringify(value)).toBe(
      validateAgainstSchema(hostToolSchemas.concord_work_start, value),
    )
  }
  expect(validateAgainstSchema(schema, capture)).toBe(true)
  expect(validateAgainstSchema(schema, resume)).toBe(true)
  expect(validateAgainstSchema(schema, issueResume)).toBe(true)
  expect(validateAgainstSchema(schema, { issue_key: "A-1" })).toBe(true)
  expect(validateAgainstSchema(schema, { issue_key: `E${"X".repeat(61)}-3` })).toBe(true)
  expect(validateAgainstSchema(schema, { ...resume, ...issueResume })).toBe(false)
})
