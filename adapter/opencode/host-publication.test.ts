import { test, expect, mock } from "bun:test"
import { contractOperations, workflowActionPublicVariants } from "./generated-contracts"
import { expandedPublishedRequestSchema, actionFieldsBranches } from "./generated-contract-tests"
import { validateAgainstSchema } from "./core-runtime"

const fakeTool = (config: unknown) => config
mock.module("@opencode-ai/plugin", () => ({ tool: fakeTool }))

const adapter = await import("./concord")

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
    const schema = adapter.publishedRequestSchema(toolName) as any
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
  const schema = adapter.publishedRequestSchema("concord_work_transition") as any
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
  const schema = adapter.publishedRequestSchema("concord_work_transition") as any
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

test("the request definition hook publishes an isolated root per tool and hoists no definitions anywhere else", async () => {
  const outputFor = async (toolID: string) => {
    const output = { description: "kept", parameters: { kept: true } as Record<string, unknown>, jsonSchema: undefined as unknown }
    await adapter.publishRequestDefinition({ toolID }, output)
    return { ...output, jsonSchema: output.jsonSchema as { $defs: Record<string, unknown> } }
  }
  const untouched = { description: "d", parameters: {}, jsonSchema: { type: "object" } as unknown }
  await adapter.publishRequestDefinition({ toolID: "bash" }, untouched)
  expect(untouched.jsonSchema).toEqual({ type: "object" })
  expect(untouched.parameters).toEqual({})

  const first = await outputFor("concord_work_define")
  const transition = await outputFor("concord_work_transition")
  // Publication replaces the definition schema and nothing else: the host's
  // parameters and description pass through untouched.
  expect(first.parameters).toEqual({ kept: true })
  expect(first.description).toBe("kept")
  expect(transition.parameters).toEqual({ kept: true })

  // Each call publishes a fresh root: mutating one publication cannot mutate
  // the registration arguments, a later publication, or another tool's root.
  const registration = (adapter.work_transition as any).args.request
  const registrationSnapshot = JSON.stringify(registration)
  const firstSnapshot = JSON.stringify(first.jsonSchema)
  const transitionDefs = Object.keys(transition.jsonSchema.$defs).sort()
  delete first.jsonSchema.$defs[Object.keys(first.jsonSchema.$defs)[0]]
  expect(JSON.stringify((adapter.work_transition as any).args.request)).toBe(registrationSnapshot)
  expect(Object.keys(transition.jsonSchema.$defs).sort()).toEqual(transitionDefs)
  const firstAgain = await outputFor("concord_work_define")
  expect(firstAgain.jsonSchema).not.toBe(first.jsonSchema)
  expect(JSON.stringify(firstAgain.jsonSchema)).toBe(firstSnapshot)
  // Each root resolves only against its own table.
  expect(() => expandedPublishedRequestSchema(firstAgain.jsonSchema)).not.toThrow()
  expect(() => expandedPublishedRequestSchema(transition.jsonSchema)).not.toThrow()
})
