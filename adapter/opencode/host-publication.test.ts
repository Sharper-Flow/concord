import { test, expect, mock } from "bun:test"
import { contractOperations, workflowActionPublicVariants } from "./generated-contracts"
import { advertisedAdmissionTeachingGaps } from "./generated-contract-tests"
import { validateAgainstSchema } from "./dispatch"

const fakeTool = (config: unknown) => config
mock.module("@opencode-ai/plugin", () => ({ tool: fakeTool }))

const adapter = await import("./concord")

// workflow_action publishes one closed branch per registry action variant.
// Select by the action_id const, never by list position.
function actionInputBranch(schema: any, actionId: string): any {
  const branch = schema.oneOf.find(
    (candidate: any) => candidate.properties.operation.const === "workflow_action"
      && candidate.properties.input.properties.action_id.const === actionId,
  )
  if (branch === undefined) throw new Error(`published schema carries no closed ${actionId} variant branch`)
  return branch.properties.input
}

function inspectHostSchema(value: unknown, path = "$", seen = new Set<unknown>()): void {
  if (typeof value !== "object" || value === null || seen.has(value)) return
  seen.add(value)
  if (Array.isArray(value)) {
    value.forEach((item, index) => inspectHostSchema(item, `${path}[${index}]`, seen))
    return
  }
  for (const [key, child] of Object.entries(value)) {
    expect(key === "$ref", `${path} contains a reference`).toBe(false)
    expect(key === "anyOf", `${path} contains a union`).toBe(false)
    expect(key === "allOf", `${path} contains a union`).toBe(false)
    expect(key === "definitions", `${path} contains definitions`).toBe(false)
    // oneOf survives only as a bounded variant node: every branch is a
    // self-contained closed object the host renders directly, as the
    // outcome_payload variants do. Open or nested unions stay merged.
    if (key === "oneOf") {
      expect(Array.isArray(child), `${path} oneOf is a list`).toBe(true)
      for (const [index, branch] of (child as unknown[]).entries()) {
        expect(branch, `${path} oneOf branch ${index} is an object`).toBeObject()
        expect((branch as Record<string, unknown>).additionalProperties, `${path} oneOf branch ${index} is closed`).toBe(false)
      }
    } else {
      inspectHostSchema(child, `${path}.${key}`, seen)
    }
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
        input: { type: "object" },
      },
    })
    const branches = schema.oneOf
    // One closed branch per operation — and for workflow_action, one closed
    // branch per registry action variant instead of a merged union — so the
    // branch count is registry-derived, not a handwritten parity list.
    const toolOperations = contractOperations.filter((operation: any) => operation.tool === toolName)
    const actionBranchExtra = toolOperations.some((operation: any) => operation.id === "concord_work_transition.workflow_action")
      ? workflowActionPublicVariants.length - 1
      : 0
    expect(branches).toHaveLength(toolOperations.length + actionBranchExtra)
    for (const branch of branches) {
      expect(branch.type).toBe("object")
      expect(branch.additionalProperties).toBe(false)
      expect(Array.isArray(branch.required)).toBe(true)
      expect(branch.properties.input.type).toBe("object")
      expect(branch.properties.input.additionalProperties).toBe(false)
    }
    expect(schema.properties.operation.enum).toEqual(
      contractOperations.filter((operation: any) => operation.tool === toolName).map((operation: any) => operation.id.split(".")[1]),
    )
  }
})

test("published workflow transition teaches every approve_contract admission rule", () => {
  const schema = adapter.publishedRequestSchema("concord_work_transition") as any
  // The advertised schema carries all four store admission rules; the store's
  // ValidateOperationPayload stays the closed boundary.
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
  // zero unexplained gaps.
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

  // Dropping either teaching from the published schema is a reported gap, so
  // generation and its tests fail on drift.
  const droppedPredicateRule = structuredClone(schema)
  delete actionInputBranch(droppedPredicateRule, "approve_contract").properties.fields.properties.outcome_predicates.description
  expect(advertisedAdmissionTeachingGaps(droppedPredicateRule)).toContain(
    "outcome_predicates description does not teach the delivery-decidable rule (CD-0184) on action approve_contract",
  )

  const droppedWaitRule = structuredClone(schema)
  delete actionInputBranch(droppedWaitRule, "add_condition").properties.fields.properties.expected_within_seconds.description
  expect(advertisedAdmissionTeachingGaps(droppedWaitRule)).toContain(
    "expected_within_seconds description does not teach the delivery-decidable rule (CD-0184) on action add_condition",
  )

  // A description cut down to its marker phrases no longer carries the rule,
  // so it is a gap too.
  const reducedRules = structuredClone(schema)
  actionInputBranch(reducedRules, "approve_contract").properties.fields.properties.outcome_predicates.description = "decidable at delivery; raised_from"
  actionInputBranch(reducedRules, "add_condition").properties.fields.properties.expected_within_seconds.description = "raised_from; time window"
  expect(advertisedAdmissionTeachingGaps(reducedRules)).toEqual(expect.arrayContaining([
    "outcome_predicates description does not teach the delivery-decidable rule (CD-0184) on action approve_contract",
    "expected_within_seconds description does not teach the delivery-decidable rule (CD-0184) on action add_condition",
  ]))
})
