import { test, expect } from "bun:test"
import { contractOperations, workflowActionPublicVariants } from "./generated-contracts"

const adapter = await import("./concord")

// The published surface is deterministic: `publishedRequestSchema` is the
// same production function the
// plugin tool hook registers through `argsSchema`, two publications of one
// tool are byte-identical, branch order follows the contract operations and
// the registry action order, and the per-tool published size is pinned in
// UTF-8 bytes so a size regression is a visible test change, not silent
// drift.
const tools = [...new Set(contractOperations.map((operation: any) => operation.tool))]

const publishedBytes = (tool: string): number => Buffer.byteLength(JSON.stringify(adapter.publishedRequestSchema(tool)), "utf8")

// The pinned total is the sum of every tool's published UTF-8 byte size on
// reviewed surface, including the typed research retirement operation. Update it
// only through a reviewed size change. The figure counts schema bytes, not
// tokens.
const PINNED_PUBLISHED_TOTAL_BYTES = 380403

test("publication is repeatable: two publications are byte-identical", () => {
  for (const tool of tools) {
    const first = JSON.stringify(adapter.publishedRequestSchema(tool))
    const second = JSON.stringify(adapter.publishedRequestSchema(tool))
    expect(first, tool).toBe(second)
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
    const published: string[] = schema.oneOf.map((branch: any) =>
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
  expect(total).toBeGreaterThan(0)
  // Deterministic across publications: re-publishing must not move any byte
  // size.
  for (const tool of tools) {
    expect(publishedBytes(tool), tool).toBe(sizes[tool])
  }
})
