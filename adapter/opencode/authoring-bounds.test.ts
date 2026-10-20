import { expect, test } from "bun:test"
import { contractOperations, hostToolSchemas, payloadSchemas } from "./generated-contracts"
import { expandedPublishedRequestSchema, validateGeneratedPayload } from "./generated-contract-tests"
import { publishCiWatchDefinition } from "./ci-watch"
import { publishWorkerReadDefinition } from "./worker_source"
import { publishWorkerCheckDefinition } from "./worker_control"
import { publishWorkerDiffDefinition } from "./worker_diff"
import { publishRequestDefinition, publishWorkStartDefinition } from "./concord"
import { validateAgainstSchema } from "./dispatch"

// Walk schema positions, not arbitrary objects in annotations or examples.
function visitSchemas(node: any, path: string, visit: (node: any, path: string) => void) {
  if (node === null || typeof node !== "object" || Array.isArray(node)) return
  visit(node, path)
  for (const key of ["$defs", "properties", "patternProperties"]) {
    for (const [name, child] of Object.entries(node[key] ?? {})) visitSchemas(child, `${path}.${key}.${name}`, visit)
  }
  for (const key of ["items", "additionalProperties", "propertyNames", "contains", "if", "then", "else", "not"]) {
    const child = node[key]
    if (Array.isArray(child)) child.forEach((item, i) => visitSchemas(item, `${path}.${key}[${i}]`, visit))
    else visitSchemas(child, `${path}.${key}`, visit)
  }
  for (const key of ["allOf", "oneOf", "anyOf", "prefixItems"]) {
    node[key]?.forEach((child: any, i: number) => visitSchemas(child, `${path}.${key}[${i}]`, visit))
  }
}

const boundUnits: Record<string, string> = {
  minimum: "", maximum: "",
  minLength: "code points", maxLength: "code points",
  "x-minBytes": "UTF-8 bytes", "x-maxBytes": "UTF-8 bytes",
  minItems: "items", maxItems: "items",
  minProperties: "properties", maxProperties: "properties",
}

const pairedBounds = [
  ["minimum", "maximum"], ["minLength", "maxLength"], ["x-minBytes", "x-maxBytes"],
  ["minItems", "maxItems"], ["minProperties", "maxProperties"],
] as const

function numberTokens(description: string): number[] {
  return (description.match(/-?\d+(?:\.\d+)?/g) ?? []).map((token: string) => Number(token))
}

// A bound description names the enforced value, its count unit, and inclusive
// endpoints; a paired bound states the lower endpoint before the upper one.
function boundFindings(schema: unknown, tool: string) {
  const missing: string[] = []
  let checked = 0
  visitSchemas(schema, tool, (node, path) => {
    for (const [keyword, unit] of Object.entries(boundUnits)) {
      if (typeof node[keyword] !== "number") continue
      checked++
      const description = node.description ?? ""
      const numbers = numberTokens(description)
      if (!numbers.some((number: number) => number === node[keyword]) || !description.includes(unit) || !description.includes("inclusive")) {
        missing.push(`${path}: ${keyword}=${node[keyword]} ${unit}`)
      }
    }
    for (const [low, high] of pairedBounds) {
      if (typeof node[low] !== "number" || typeof node[high] !== "number" || node[low] >= node[high]) continue
      const tokens = numberTokens(node.description ?? "")
      const lowIndex = tokens.findIndex((token: number) => token === node[low])
      const highIndex = tokens.findIndex((token: number) => token === node[high])
      if (lowIndex === -1 || highIndex === -1 || lowIndex > highIndex) {
        missing.push(`${path}: ${low}/${high} must state ${node[low]} before ${node[high]}`)
      }
    }
  })
  return { missing, checked }
}

test("every enforced input bound reaches production tool descriptions with its unit and inclusive endpoint", async () => {
  const published: Record<string, unknown> = {}
  for (const toolID of new Set(contractOperations.map(operation => operation.tool))) {
    const output = { description: "", parameters: {}, jsonSchema: undefined as unknown }
    await publishRequestDefinition({ toolID }, output)
    published[toolID] = output.jsonSchema
  }
  for (const [toolID, hook] of [
    ["concord_work_start", publishWorkStartDefinition],
    ["concord_ci_watch", publishCiWatchDefinition],
    ["concord_worker_read", publishWorkerReadDefinition],
    ["concord_worker_check", publishWorkerCheckDefinition],
    ["concord_worker_diff", publishWorkerDiffDefinition],
  ] as const) {
    const output = { description: "", parameters: {}, jsonSchema: undefined as unknown }
    await hook({ toolID }, output)
    published[toolID] = output.jsonSchema
  }
  const missing: string[] = []
  let checked = 0
  for (const [tool, schema] of Object.entries(published)) {
    expect(schema, tool).toBeObject()
    const findings = boundFindings(schema, tool)
    missing.push(...findings.missing)
    checked += findings.checked
  }
  expect(checked).toBeGreaterThan(100)
  expect(missing).toEqual([])
})

test("startup, capture and revise sizing keep identical bounds and source-free research can omit sizing", () => {
  const [capture] = hostToolSchemas.concord_work_start.oneOf
  const definitions: any = payloadSchemas
  const schemas = [capture.properties.size_inputs, definitions.work_define_capture_input.properties.size_inputs, definitions.work_define_revise_input.properties.size_inputs]
  const resolve = (schema: any): any => schema.$ref ? resolve(definitions[schema.$ref.split("/").at(-1)]) : schema
  const estimates = schemas.map(schema => resolve(resolve(schema).properties.estimated_production_lines))
  for (const estimate of estimates) expect([estimate.minimum, estimate.maximum]).toEqual([1, 100000])
  const research = { title: "Research", value_statement: "Findings without source changes", kind: "research", task: "Investigate only", idempotency_key: "research" }
  expect(validateAgainstSchema(capture, research, [])).toBe(true)
  const input = { ...research, project_ids: ["project-1"] }
  expect(validateGeneratedPayload("work_define_capture_input", input)).toBe(true)
  for (const estimate of [undefined, -1, 0, 100001]) {
    const size_inputs = { behavior: "investigate", security: false, user_visible: false, blast_radius: "contained", ...(estimate === undefined ? {} : { estimated_production_lines: estimate }) }
    expect(validateAgainstSchema(capture, { ...research, size_inputs }, [])).toBe(false)
    expect(validateGeneratedPayload("work_define_capture_input", { ...input, size_inputs })).toBe(false)
  }
  // The supplied estimate range is inclusive at both documented endpoints.
  for (const estimate of [1, 100000]) {
    const size_inputs = { behavior: "investigate", security: false, user_visible: false, blast_radius: "contained", estimated_production_lines: estimate }
    expect(validateAgainstSchema(capture, { ...research, size_inputs }, [])).toBe(true)
    expect(validateGeneratedPayload("work_define_capture_input", { ...input, size_inputs })).toBe(true)
    const revise = { work_id: "work-1", expected_version: 1, title: "Research", value_statement: "No source changes", kind: "research", reason: "narrow the title only", idempotency_key: `revise-${estimate}`, size_inputs }
    expect(validateGeneratedPayload("work_define_revise_input", revise)).toBe(true)
  }
})

test("observation publication retains code-point bounds after reference expansion", async () => {
  const output = { description: "", parameters: {}, jsonSchema: undefined as unknown }
  await publishRequestDefinition({ toolID: "concord_work_define" }, output)
  const expanded: any = expandedPublishedRequestSchema(output.jsonSchema)
  const input = expanded.oneOf.find((branch: any) => branch.properties.operation.const === "observation_record").properties.input
  for (const [name, maximum] of [["statement", 512], ["refs", 256], ["tags", 32]] as const) {
    const field = name === "statement" ? input.properties[name] : input.properties[name].items
    expect([field.minLength, field.maxLength]).toEqual([1, maximum])
    expect(field.description).toContain("code points")
    expect(field.description).toContain("inclusive")
    expect(field["x-maxBytes"]).toBeUndefined()
  }
  for (const [name, maximum] of [["refs", 16], ["tags", 8]] as const) {
    expect(input.properties[name].maxItems).toBe(maximum)
  }
})

test("the bound checker itself catches wrong units, missing values, excluded endpoints, and reversed roles", async () => {
  const output = { description: "", parameters: {}, jsonSchema: undefined as unknown }
  await publishRequestDefinition({ toolID: "concord_work_define" }, output)
  const expanded: any = expandedPublishedRequestSchema(output.jsonSchema)
  const input = expanded.oneOf.find((branch: any) => branch.properties.operation.const === "observation_record").properties.input
  const statement = input.properties.statement
  expect(statement.minLength).toBe(1)
  expect(statement.maxLength).toBe(512)
  const check = (description: string) => boundFindings({ ...statement, description }, "witness").missing
  expect(check(`Bounded 1 to ${statement.maxLength} code points, inclusive.`)).toEqual([])
  // A byte unit on a code-point bound must fail.
  expect(check(`Up to ${statement.maxLength} UTF-8 bytes, inclusive.`).some((finding: string) => finding.includes("maxLength"))).toBe(true)
  // A description without the enforced value must fail.
  expect(check("Bounded non-empty code points, inclusive.").length).toBeGreaterThan(0)
  // An exclusive endpoint contradicts the inclusive keyword pair.
  expect(check(`1 to ${statement.maxLength} code points, exclusive.`).length).toBeGreaterThan(0)
  // A paired numeric bound stated high-before-low must fail.
  const reversed = boundFindings({ minimum: 1, maximum: 100000, description: "100000 down to 1, inclusive" }, "witness").missing
  expect(reversed.some((finding: string) => finding.includes("minimum/maximum"))).toBe(true)
})
