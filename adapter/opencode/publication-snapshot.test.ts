import { test, expect } from "bun:test"
import fs from "node:fs"
import path from "node:path"
import { contractOperations, hostToolSchemas, workflowActionPublicVariants } from "./generated-contracts"
import { expandedPublishedRequestSchema, PINNED_PUBLISHED_TOTAL_BYTES } from "./generated-contract-tests"

const adapter = await import("./concord")

// The published surface is deterministic: `publishedRequestSchema` is the
// same production function the plugin tool hook registers through `argsSchema`.
// Publication is compact: one branch per operation with the
// workflow actions grouped under their operation, local `#/$defs/`
// references, and factored unions; `expandedPublishedRequestSchema` resolves
// that document for structural inspection. `publishRequestDefinition` hoists
// the definitions onto the final registered argument root the host hands the
// provider. Two publications of one tool are byte-identical, branch order
// follows the contract operations and the registry action order, and the
// published size is pinned in UTF-8 bytes so a size regression is a visible
// test change, not silent drift.
const tools = [...new Set(contractOperations.map((operation: any) => operation.tool))]
// Repository regression budget for both measured populations, not a provider limit.
const SCHEMA_REGRESSION_BUDGET = 165_000

const publishedBytes = (tool: string): number => Buffer.byteLength(JSON.stringify(adapter.publishedRequestSchema(tool)), "utf8")

// scripts/generate-agent-contracts.py measures the production request
// documents and emits the reviewed byte pin with the contract test data.

test("publication is repeatable: two publications are byte-identical", () => {
  for (const tool of tools) {
    const first = JSON.stringify(adapter.publishedRequestSchema(tool))
    const second = JSON.stringify(adapter.publishedRequestSchema(tool))
    expect(first, tool).toBe(second)
  }
})

test("the compact publication stays a self-contained local-reference document", () => {
  for (const tool of tools) {
    const schema: any = adapter.publishedRequestSchema(tool)
    // Publication factors shared structure; it never introduces allOf.
    expect(JSON.stringify(schema), tool).not.toContain('"allOf"')
    const defs = schema.$defs
    expect(defs, tool).toBeObject()
    const names = new Set(Object.keys(defs))
    let refs = 0
    const walk = (value: unknown) => {
      if (Array.isArray(value)) {
        value.forEach(walk)
        return
      }
      if (value === null || typeof value !== "object") return
      for (const [key, child] of Object.entries(value)) {
        if (key === "$ref") {
          expect(typeof child, `${tool} carries a non-string reference`).toBe("string")
          expect((child as string).startsWith("#/$defs/"), `${tool} carries the non-local reference ${child}`).toBe(true)
          expect(names.has((child as string).slice("#/$defs/".length)), `${tool} carries the unresolvable reference ${child}`).toBe(true)
          refs++
          continue
        }
        walk(child)
      }
    }
    walk(schema)
    expect(refs, tool).toBeGreaterThan(0)
    // One compact branch per operation: action variants share the single
    // workflow_action branch.
    const toolOperations = contractOperations.filter((operation: any) => operation.tool === tool)
    expect(schema.oneOf, tool).toHaveLength(toolOperations.length)
  }
})

test("publication branch order follows contract order then registry action order", () => {
  for (const tool of tools) {
    const schema: any = adapter.publishedRequestSchema(tool)
    const expected: string[] = contractOperations
      .filter((operation: any) => operation.tool === tool)
      .flatMap((operation: any) => {
        const name = operation.id.slice(operation.id.indexOf(".") + 1)
        if (name !== "workflow_action") return [name]
        return workflowActionPublicVariants.map((variant) => `workflow_action:${variant.action_id}`)
      })
    // The grouped action union flattens back to one branch per action, in
    // the compact document's own order: factoring may regroup, never
    // reorder.
    const expanded: any = expandedPublishedRequestSchema(schema)
    const published: string[] = expanded.oneOf.map((branch: any) =>
      branch.properties.operation.const === "workflow_action" && branch.properties.input.properties.action_id?.const
        ? `workflow_action:${branch.properties.input.properties.action_id.const}`
        : branch.properties.operation.const,
    )
    expect(published, tool).toEqual(expected)
  }
})

test("published byte sizes are pinned in UTF-8 bytes", () => {
  const sizes: Record<string, number> = {}
  for (const tool of tools) sizes[tool] = publishedBytes(tool)
  const total = Object.values(sizes).reduce((sum, size) => sum + size, 0)
  expect(total).toBe(PINNED_PUBLISHED_TOTAL_BYTES)
  expect(total).toBeLessThan(SCHEMA_REGRESSION_BUDGET)
  expect(total).toBeGreaterThan(0)
  // Deterministic across publications: re-publishing must not move any byte
  // size.
  for (const tool of tools) {
    expect(publishedBytes(tool), tool).toBe(sizes[tool])
  }
})

// Registered request tools publish through the production definition hook,
// with definitions hoisted to the argument root. Host tools keep their own
// hooks. Both identified populations must stay below the
// serving budget.
test("all registered parameter schemas stay below the serving budget", async () => {
  const registered = new Map<string, number>()
  for (const tool of tools) {
    const output = { description: "", parameters: {}, jsonSchema: undefined as unknown }
    await adapter.publishRequestDefinition({ toolID: tool }, output)
    expect(output.jsonSchema, tool).toBeObject()
    registered.set(tool, Buffer.byteLength(JSON.stringify(output.jsonSchema), "utf8"))
  }
  const workStartOutput = { description: "", parameters: {}, jsonSchema: undefined as unknown }
  await adapter.publishWorkStartDefinition({ toolID: "concord_work_start" }, workStartOutput)
  expect(workStartOutput.jsonSchema).toBeObject()
  registered.set("concord_work_start", Buffer.byteLength(JSON.stringify(workStartOutput.jsonSchema), "utf8"))
  // The registered set is exactly the catalog the two tool-surface
  // contracts declare, so a new registration cannot escape the generated
  // denial derivation.
  expect([...registered.keys()].sort()).toEqual([...tools, ...Object.keys(hostToolSchemas)].sort())
  const total = [...registered.values()].reduce((sum, size) => sum + size, 0)
  expect(total).toBeGreaterThan(0)
  expect(total).toBeLessThan(SCHEMA_REGRESSION_BUDGET)
})

// The generated host schemas are the authored contract documents: the
// projection preserves the authored key order, so JSON stringification is
// byte-identical to the contract file's own args. Publication order drift
// is a test failure here, not a silent reordering.
test("generated host schemas serialize byte for byte as the authored contract", () => {
  const authored = JSON.parse(
    fs.readFileSync(path.resolve(import.meta.dir, "../../contracts/host-tool-surface.v1.json"), "utf8"),
  ) as { tools: Array<{ name: string; args: unknown }> }
  expect(authored.tools.length).toBe(Object.keys(hostToolSchemas).length)
  for (const tool of authored.tools) {
    expect(JSON.stringify((hostToolSchemas as Record<string, unknown>)[tool.name]), tool.name).toBe(JSON.stringify(tool.args))
  }
})
