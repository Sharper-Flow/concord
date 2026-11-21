import { test, expect, mock } from "bun:test"
import { contractOperations, workflowActionPublicVariants } from "./generated-contracts"
import { expandedPublishedRequestSchema, actionFieldsBranches } from "./generated-contract-tests"
import { validateAgainstSchema } from "./core-runtime"

const fakeTool = (config: unknown) => config
mock.module("@opencode-ai/plugin", () => ({ tool: fakeTool }))

const adapter = await import("./concord")

const tools: Record<string, any> = {
  concord_product_view: adapter.product_view, concord_work_browse: adapter.work_browse,
  concord_work_trace: adapter.work_trace, concord_work_define: adapter.work_define,
  concord_domain: adapter.domain, concord_work_transition: adapter.work_transition,
  concord_work_relate: adapter.work_relate,
}
const requestSchema = (toolName: string) => {
  const input = tools[toolName].input
  return { ...input.properties.request, $defs: input.$defs }
}

test("registered input publishes compact request constraints", () => {
  const schema: any = adapter.work_browse.input
  expect(schema.required).toEqual(["request"])
  expect(schema.properties.request.properties.operation.enum).toContain("list")
  expect(Object.keys(schema.$defs).every(name => /^d\d+$/.test(name))).toBe(true)
  expect(validateAgainstSchema(schema, { request: { operation: "list", input: { product_id: "concord" } } }, [])).toBe(true)
  expect(validateAgainstSchema(schema, { operation: "list", input: { product_id: "concord" } }, [])).toBe(false)
})

// workflow_action publishes its action variants grouped under one operation
// branch; the expanded view restores one closed branch per action. Select by
// the action_id const, never by list position.
function actionInputBranch(published: any, actionId: string): any {
  const direct = published.oneOf.find((candidate: any) => candidate.properties.operation.const === "workflow_action" && candidate.properties.input.properties?.action_id?.const === actionId)
  if (direct !== undefined) return direct.properties.input
  const expanded: any = expandedPublishedRequestSchema(published)
  const branch = expanded.oneOf.find(
    (candidate: any) => candidate.properties.operation.const === "workflow_action"
      && candidate.properties.input.properties.action_id.const === actionId,
  )
  if (branch === undefined) throw new Error(`published schema carries no closed ${actionId} variant branch`)
  return branch.properties.input
}

test("published request schemas are compact and safe for host publication", () => {
  const tools = [...new Set(contractOperations.map((operation: any) => operation.tool))]
  for (const toolName of tools) {
    const schema = requestSchema(toolName) as any
    expect(schema).toMatchObject({
      type: "object",
      required: ["operation", "input"],
      properties: {
        operation: { type: "string" },
        input: { type: "object" },
      },
    })
    // The compact document is self-contained: every reference is local, and
    // the expansion resolves the whole document or throws.
    expect(schema.$defs, toolName).toBeObject()
    expect(() => expandedPublishedRequestSchema(schema), toolName).not.toThrow()
    expect(JSON.stringify(schema), toolName).not.toContain('"allOf"')
    const branches = schema.oneOf
    // One compact branch per operation; the action variants share the
    // workflow_action branch's grouped input union.
    const toolOperations = contractOperations.filter((operation: any) => operation.tool === toolName)
    expect(branches, toolName).toHaveLength(toolOperations.length)
    expect(schema.properties.operation.enum).toEqual(
      contractOperations.filter((operation: any) => operation.tool === toolName).map((operation: any) => operation.id.split(".")[1]),
    )
    // The legacy-effective view carries one closed branch per operation and
    // per action variant: factoring inherits constraints, it never opens a
    // branch.
    const expanded: any = expandedPublishedRequestSchema(schema)
    const actionBranchExtra = toolOperations.some((operation: any) => operation.id === "concord_work_transition.workflow_action")
      ? workflowActionPublicVariants.length - 1
      : 0
    expect(expanded.oneOf, toolName).toHaveLength(toolOperations.length + actionBranchExtra)
    for (const branch of expanded.oneOf) {
      expect(branch, toolName).toMatchObject({
        type: "object",
        additionalProperties: false,
        properties: { input: { type: "object", additionalProperties: false } },
      })
      expect(Array.isArray(branch.required), toolName).toBe(true)
    }
  }
})

test("published approval validates contract fields", () => {
  const schema = requestSchema("concord_work_transition") as any
  const fields = actionFieldsBranches(actionInputBranch(schema, "approve_contract"))
  expect(fields).toHaveLength(1)
  const request = { operation: "workflow_action", input: {
    work_id: "work-conformance", expected_version: 2, action_id: "approve_contract",
    idempotency_key: "idem-conformance", fields: {
      legislative_subjects: ["spec:one"],
    },
  } }
  expect(validateAgainstSchema(schema, request, [])).toBe(true)
  const duplicate = structuredClone(request)
  duplicate.input.fields.legislative_subjects.push("spec:one")
  expect(validateAgainstSchema(schema, duplicate, [])).toBe(false)
})

test("published workflow action variants state their guard-owned requirements", () => {
  const schema = requestSchema("concord_work_transition") as any
  // The provider reads each requirement on the compact document's action
  // variant, so inspect that node rather than the expanded view.
  const operation = schema.oneOf.find((candidate: any) => candidate.properties.operation.const === "workflow_action")
  const variants = operation.properties.input.oneOf.map((variant: any) => typeof variant.$ref === "string" ? schema.$defs[variant.$ref.slice("#/$defs/".length)] : variant)
  for (const [actionId, term] of [
    ["complete_step", "step_reviewed"],
    ["bind_evidence", "one immutable subject"],
    ["record_report", "one immutable subject"],
    ["complete", "confirm_premise"],
  ]) {
    const variant = variants.find((candidate: any) => candidate.properties?.action_id?.const === actionId)
    expect(variant?.description, actionId).toContain(term)
  }
  const fields = actionFieldsBranches(actionInputBranch(schema, "approve_contract"))[0]
  expect(fields.properties.architecture_binding.description).toContain("required for change, fix, and removal")
  expect(fields.properties.spec_mandate.description).toContain("2..128 Unicode code points")
})

test("registered inputs have isolated local definition tables", () => {
  const first = structuredClone(adapter.work_define.input) as any
  const transition = adapter.work_transition.input as any
  const snapshot = JSON.stringify(transition)
  delete first.$defs[Object.keys(first.$defs)[0]]
  expect(JSON.stringify(transition)).toBe(snapshot)
  expect(() => expandedPublishedRequestSchema(adapter.work_define.input)).not.toThrow()
  expect(() => expandedPublishedRequestSchema(transition)).not.toThrow()
})
