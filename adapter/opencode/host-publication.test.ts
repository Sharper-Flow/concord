import { test, expect, mock } from "bun:test"
import { contractOperations, workflowActionPublicVariants } from "./generated-contracts"
import { advertisedAdmissionTeachingGaps, expandedPublishedRequestSchema } from "./generated-contract-tests"
import { validateAgainstSchema } from "./dispatch"

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

// The inspection view is a deep copy of the actual publication. Mutants edit
// its selected branches, not a second copy returned by a subsequent traversal.
function inlineExpanded(published: unknown): any {
  return expandedPublishedRequestSchema(published)
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
    // One compact branch per operation; the 58 action variants share the
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
      expect(branch.type, toolName).toBe("object")
      expect(branch.additionalProperties, toolName).toBe(false)
      expect(Array.isArray(branch.required), toolName).toBe(true)
      expect(branch.properties.input.type, toolName).toBe("object")
      expect(branch.properties.input.additionalProperties, toolName).toBe(false)
    }
  }
})

test("published workflow transition teaches every approve_contract admission rule", () => {
  const schema = adapter.publishedRequestSchema("concord_work_transition") as any
  // The advertised schema carries all four store admission rules; the store's
  // ValidateOperationPayload stays the closed boundary. The traversal reads
  // the compact document through the expansion helper.
  expect(advertisedAdmissionTeachingGaps(schema)).toEqual([])
  const items = actionInputBranch(schema, "approve_contract").properties.fields.properties.outcome_predicates.items
  // CON-412: each published predicate item is closed per kind — outcome_kind
  // consts to the branch and outcome_payload carries that branch's payload,
  // so a mismatched outcome_kind/outcome_payload pair validates no branch.
  expect(items.oneOf.map((branch: any) => branch.properties.outcome_kind.const)).toEqual(["exists", "absent", "outcome", "check"])
  for (const branch of items.oneOf) {
    expect(branch.required).toEqual(["predicate_id", "ordinal", "outcome_kind", "outcome_payload"])
    expect(branch.additionalProperties).toBe(false)
    expect(branch.properties.outcome_kind.const).toBe(branch.properties.outcome_payload.properties.kind.const)
  }

  // Schema-vs-payload conformance: a canonical workflow.research
  // approve_contract payload validates against the advertised schema with
  // zero unexplained gaps. The verdict runs on the raw compact document.
  const canonical = {
    operation: "workflow_action",
    input: {
      work_id: "work-conformance",
      expected_version: 2,
      action_id: "approve_contract",
      idempotency_key: "idem-admission-conformance",
      fields: {
        premise: "Advertise the admission rules the store enforces.",
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

test("delivery-decidable-rule: the published schema teaches it and the gap check detects its removal", () => {
  const schema = adapter.publishedRequestSchema("concord_work_transition") as any
  // CD-0184 teaches the rule at both authoring points: the outcome_predicates
  // array on approve_contract (shared with supersede_contract), and the
  // add_condition hold bound.
  const predicateFields = actionInputBranch(schema, "approve_contract").properties.fields.properties
  const waitFields = actionInputBranch(schema, "add_condition").properties.fields.properties
  expect(predicateFields.outcome_predicates.description).toContain("decidable at delivery")
  expect(predicateFields.outcome_predicates.description).toContain("raised_from")
  expect(waitFields.expected_within_seconds.description).toContain("raised_from")
  expect(waitFields.expected_within_seconds.description).toContain("time window")

  // Dropping either teaching from the published document is a reported gap,
  // so generation and its tests fail on drift. The mutation lands on an
  // inlined clone of the actual compact document, and the gap check walks
  // that mutated document the same way it walks the real one.
  const droppedPredicateRule = inlineExpanded(schema)
  delete actionInputBranch(droppedPredicateRule, "approve_contract").properties.fields.properties.outcome_predicates.description
  expect(advertisedAdmissionTeachingGaps(droppedPredicateRule)).toContain(
    "outcome_predicates description does not teach the delivery-decidable rule (CD-0184) on action approve_contract",
  )

  const droppedWaitRule = inlineExpanded(schema)
  delete actionInputBranch(droppedWaitRule, "add_condition").properties.fields.properties.expected_within_seconds.description
  expect(advertisedAdmissionTeachingGaps(droppedWaitRule)).toContain(
    "expected_within_seconds description does not teach the delivery-decidable rule (CD-0184) on action add_condition",
  )

  // A description cut down to its marker phrases no longer carries the rule,
  // so it is a gap too.
  const reducedRules = inlineExpanded(schema)
  actionInputBranch(reducedRules, "approve_contract").properties.fields.properties.outcome_predicates.description = "decidable at delivery; raised_from"
  actionInputBranch(reducedRules, "add_condition").properties.fields.properties.expected_within_seconds.description = "raised_from; time window"
  expect(advertisedAdmissionTeachingGaps(reducedRules)).toEqual(expect.arrayContaining([
    "outcome_predicates description does not teach the delivery-decidable rule (CD-0184) on action approve_contract",
    "expected_within_seconds description does not teach the delivery-decidable rule (CD-0184) on action add_condition",
  ]))
})

test("the request definition hook publishes an isolated root per tool and hoists no definitions anywhere else", async () => {
  const outputFor = async (toolID: string) => {
    const output = { description: "kept", parameters: { kept: true } as Record<string, unknown>, jsonSchema: undefined as unknown }
    await adapter.publishRequestDefinition({ toolID }, output)
    return output
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
