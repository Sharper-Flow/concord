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
    visitSchemas(schema, tool, (node, path) => {
      for (const [keyword, unit] of Object.entries(boundUnits)) {
        if (typeof node[keyword] !== "number") continue
        checked++
        const description = node.description ?? ""
        const numbers = description.match(/-?\d+(?:\.\d+)?/g) ?? []
        if (!numbers.some((number: string) => Number(number) === node[keyword]) || !description.includes(unit) || !description.includes("inclusive")) {
          missing.push(`${path}: ${keyword}=${node[keyword]} ${unit}`)
        }
      }
    })
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
})
