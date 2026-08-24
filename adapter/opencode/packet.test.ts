import { test, expect, mock } from "bun:test"
import { manifestDigest } from "./generated-contracts"
import { validateGeneratedEnvelope, validateGeneratedPayload } from "./generated-contract-tests"
import { validateAgentLanePacket } from "./dispatch"
import { agentLanes } from "./generated-agent-lanes"

// The builder reaches core through the adapter transport in concord.ts, which
// imports the host plugin surface. The stub mirrors concord.test.ts so the
// suite never loads the real host module.
function schemaBuilder() {
  return {
    optional() { return this }, strict() { return this }, min() { return this }, max() { return this }, int() { return this }, regex() { return this },
  }
}
const fakeTool = Object.assign((config: any) => config, {
  schema: {
    object: schemaBuilder, array: schemaBuilder, union: schemaBuilder, literal: schemaBuilder,
    string: schemaBuilder, number: schemaBuilder, unknown: schemaBuilder, null: schemaBuilder,
  },
})
mock.module("@opencode-ai/plugin", () => ({ tool: fakeTool }))

const adapter = await import("./concord")
const { buildAgentLanePacket } = await import("./packet")

const WORK_ID = "work-335"
const PRODUCT_ID = "product-1"
const NARRATIVE = "The dispatched worker goal is retyped prose today; project it from durable state instead."
const OUTCOME_KIND = "capability_available"
const OUTCOME_PAYLOAD = "A host-side builder projects work narrative, pinned contract, and lane obligations into agent-lane-packet.v1."
const WORKFLOW_STEP = "implement"
// The single string internal/store/workflow_continuity.go assigns to
// RestartUnavailableReason. A fixture that invents its own value would let the
// builder be proved against a response the core never emits.
const RESTART_UNAVAILABLE_REASON = "typed restart is deliberately excluded (CD-0027); pinned continuity is re-derived per call"

const grantResponse = () => ({ manifest_digest: manifestDigest, grant_token: "secret", grant_ref: "grant-1", client_ref: "opencode", principal_ref: "principal-1", session_ref: "session-1", agent_ref: "agent-1", scope_version: "1" })

const contextFor = (): any => ({ sessionID: "session-1", messageID: "message-1", agent: "agent-1", worktree: "/worktree", directory: "/worktree", abort: new AbortController().signal, ask: async () => {} })

const coreEnvelope = (tool: string, operation: string, queryID: string, outcome: string, fields: Record<string, unknown> = {}) => ({
  schema_version: "1.0", manifest_digest: manifestDigest, request_id: "session-1-message-1", origin: "core", tool, operation, query_id: queryID, outcome, resolved_scope: null, authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false, ...fields,
})

const scopeEnvelope = (narrative: string = NARRATIVE, result: Record<string, unknown> | null = null) => coreEnvelope("concord_work_browse", "scope", "PM1.Q6", "ok", {
  result: result ?? {
    work: { id: WORK_ID, kind: "task", title: "Project dispatch inputs from durable state", lifecycle: "in_progress", version: 1, priority: 0, project_ids: [PRODUCT_ID], ready: true, narrative, terminal_at: null },
    memberships: [{ project_id: PRODUCT_ID, role: "primary" }],
    items: [],
  },
})

// The two Product-truth flags are marshalled unconditionally by
// store.WorkflowReadContract and declared required since #363, so a real
// pinned contract always carries them. Omitting them here would prove the
// builder against a shape the core cannot emit.
function pinnedContract(outcomePayload: string = OUTCOME_PAYLOAD) {
  return {
    version: 1,
    premise: "Dispatch inputs are retyped rather than projected.",
    outcome_kind: OUTCOME_KIND,
    outcome_payload: outcomePayload,
    required_evidence: [],
    route_conventions: [],
    spec_mandate: [],
    changes_product_truth: false,
  }
}

const continuityEnvelope = (contract: unknown = pinnedContract()) => coreEnvelope("concord_work_trace", "continuity", "C19.Continuity", "ok", {
  result: {
    work_id: WORK_ID,
    pinned: {
      product_identity: [PRODUCT_ID],
      workflow_step: WORKFLOW_STEP,
      contract,
      spec_mandate: [],
      pending_operator_decision: null,
      latest_checkpoint: null,
      unresolved_failure: null,
    },
    latest_checkpoint: null,
    boundaries: { count: 0, items: [], next_cursor: null, watermark: "seq:1" },
    typed_availability: { restart: "unavailable", reason: RESTART_UNAVAILABLE_REASON },
    pending_messages: 0,
    observations: [],
  },
})

// scriptedInvoke answers each read by tool and operation, so a test states the
// core envelopes it is projecting from rather than the order they are fetched.
const scriptedInvoke = (responses: Record<string, unknown>) => {
  const seen: string[] = []
  const invoke = async (toolName: string, args: { operation: string }) => {
    const key = `${toolName}.${args.operation}`
    seen.push(key)
    if (!(key in responses)) throw new Error(`unscripted read ${key}`)
    return responses[key]
  }
  return Object.assign(invoke, { seen: () => seen })
}

const defaultScript = () => ({
  "concord_work_browse.scope": scopeEnvelope(),
  "concord_work_trace.continuity": continuityEnvelope(),
})

const build = (script: Record<string, unknown>, overrides: Record<string, unknown> = {}) =>
  buildAgentLanePacket(
    { workId: WORK_ID, productId: PRODUCT_ID, laneId: "implement", attemptId: "attempt-1", stepId: "step-1", ...overrides } as any,
    { context: contextFor(), invoke: scriptedInvoke(script) as any },
  )

test("a well-formed build projects mandate, narrative, and obligations into a valid packet", async () => {
  const built = await build(defaultScript())
  expect(built.failure).toBeUndefined()
  const packet = built.packet!
  expect(validateAgentLanePacket(packet)).toBe(true)
  expect(packet.schema_version).toBe("1.0")
  expect(packet.attempt_id).toBe("attempt-1")
  expect(packet.work_id).toBe(WORK_ID)
  expect(packet.step_id).toBe("step-1")
  expect(packet.lane_id).toBe("implement")
  expect(packet.lane_version).toBe(1)
  expect(packet.lane_digest).toBe("sha256:ec541caf3d4df2d5fe70602cf65e747f19e5ac525b001fdd86ea7cf921b737fc")
  expect(packet.inputs.task).toContain(OUTCOME_KIND)
  expect(packet.inputs.task).toContain(OUTCOME_PAYLOAD)
  expect(packet.inputs.task).toContain(WORKFLOW_STEP)
  expect(packet.inputs.context).toBe(NARRATIVE)
  for (const obligation of agentLanes[1].evidence_obligations) {
    expect(packet.inputs.constraints!.some((entry) => entry.includes(`"${obligation}"`))).toBe(true)
  }
})

// Every fixture below is a hand-written response shape. contractProofs binds
// each one to the generated contract by tool.operation: the envelope schema for
// the whole response and the operation's declared result schema for its payload.
// A fixture that drifts from what internal/agent emits would otherwise let the
// whole builder suite pass against a shape the core never produces.
const contractProofs: Record<string, { envelope: Record<string, unknown>; resultSchema: string }> = {
  "concord_work_browse.scope": { envelope: scopeEnvelope(), resultSchema: "work_scope" },
  "concord_work_trace.continuity": { envelope: continuityEnvelope(), resultSchema: "continuity_snapshot" },
}

test("every core read this builder performs satisfies the generated envelope and result contract", async () => {
  // The read list comes from the builder itself rather than from a literal, so
  // a third read is covered by construction: it appears in seen() and fails
  // here until its own contract proof is declared.
  const invoke = scriptedInvoke(defaultScript())
  const built = await buildAgentLanePacket(
    { workId: WORK_ID, productId: PRODUCT_ID, laneId: "implement", attemptId: "attempt-1", stepId: "step-1" },
    { context: contextFor(), invoke: invoke as any },
  )
  expect(built.failure).toBeUndefined()
  const performed = invoke.seen()
  expect(performed.length).toBeGreaterThan(0)
  for (const read of performed) {
    const proof = contractProofs[read]
    expect(proof, `${read} is projected from but has no contract proof`).toBeDefined()
    expect(validateGeneratedEnvelope(proof.envelope), `${read} envelope`).toBe(true)
    expect(validateGeneratedPayload(proof.resultSchema, proof.envelope.result), `${read} result payload`).toBe(true)
  }
})

test("every registered lane projects its own obligation set and nothing else", async () => {
  for (const lane of agentLanes) {
    const built = await build(defaultScript(), { laneId: lane.id })
    expect(built.failure, `${lane.id}: ${JSON.stringify(built.failure)}`).toBeUndefined()
    const packet = built.packet!
    expect(validateAgentLanePacket(packet)).toBe(true)
    expect(packet.lane_id).toBe(lane.id)
    expect(packet.lane_version).toBe(lane.version)
    expect(packet.lane_digest).toBe(lane.digest)
    expect(packet.inputs.constraints!.length).toBe(lane.evidence_obligations.length)
    for (const obligation of lane.evidence_obligations) {
      expect(packet.inputs.constraints!.some((entry) => entry.includes(`"${obligation}"`)), `${lane.id} omitted ${obligation}`).toBe(true)
    }
    const own = new Set<string>(lane.evidence_obligations)
    const foreign = [...new Set(agentLanes.flatMap((other) => other.evidence_obligations as readonly string[]))].filter((obligation) => !own.has(obligation))
    for (const obligation of foreign) {
      expect(packet.inputs.constraints!.some((entry) => entry.includes(`"${obligation}"`)), `${lane.id} leaked ${obligation}`).toBe(false)
    }
  }
})

test("an unregistered lane is a typed failure", async () => {
  const built = await build(defaultScript(), { laneId: "summarize" })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("unregistered_lane")
  expect(built.failure!.message).toContain("summarize")
})

test("an oversized narrative is a typed context overflow, not a truncated packet", async () => {
  const narrative = "n".repeat(16_385)
  const built = await build({ ...defaultScript(), "concord_work_browse.scope": scopeEnvelope(narrative) })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("projection_overflow")
  expect(built.failure!.field).toBe("context")
  expect(built.failure!.limit).toBe(16_384)
  expect(built.failure!.actual).toBe(16_385)
  expect(built.failure!.message).toContain("inputs.context")
})

test("an oversized mandate is a typed task overflow", async () => {
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(pinnedContract("m".repeat(4_096))) })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("projection_overflow")
  expect(built.failure!.field).toBe("task")
  expect(built.failure!.limit).toBe(4_096)
  expect(built.failure!.actual).toBeGreaterThan(4_096)
  expect(built.failure!.message).toContain("inputs.task")
})

test("a narrative at the context bound still fits", async () => {
  const narrative = "n".repeat(16_384)
  const built = await build({ ...defaultScript(), "concord_work_browse.scope": scopeEnvelope(narrative) })
  expect(built.failure).toBeUndefined()
  expect(validateAgentLanePacket(built.packet!)).toBe(true)
  expect(built.packet!.inputs.context!.length).toBe(16_384)
})

test("no pinned contract is a typed unapproved-mandate failure", async () => {
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(null) })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("mandate_unapproved")
  expect(built.failure!.message).toContain("no pinned workflow contract")
})

test("an error-enveloped core read is a typed transport failure", async () => {
  const refusal = coreEnvelope("concord_work_browse", "scope", "PM1.Q6", "error", {
    error: { kind: "unknown_scope", retry_safe: false, recovery_action: { kind: "reread_entities" }, effect_state: "none" },
  })
  const built = await build({ "concord_work_browse.scope": refusal })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("transport_failure")
  expect(built.failure!.message).toContain("unknown_scope")
})

test("a scope read that returns no work item is a typed missing-work failure", async () => {
  const built = await build({ "concord_work_browse.scope": scopeEnvelope(NARRATIVE, { items: [] }) })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("missing_work_item")
})

test("a pinned contract without typed outcome fields is a typed transport failure", async () => {
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope({ version: 1, premise: "p", outcome_kind: 7, outcome_payload: OUTCOME_PAYLOAD, required_evidence: [], route_conventions: [], spec_mandate: [] }) })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("transport_failure")
  expect(built.failure!.message).toContain("outcome_kind")
})

test("the default transport is the adapter transport, and its refusals stay typed", async () => {
  // The builder is wired to the shipped transport by accepting the adapter's
  // invokeConcordOperation through its injected seam. The grant runner returns
  // a typed envelope that subsequent invoke calls cannot match, so the second
  // call lands on the opencode stub and the response is malformed; the test
  // only proves the wired transport was used, not what it returned.
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32) } },
    runner: { async run() { return { exitCode: 0, stdout: JSON.stringify(grantResponse()), stderr: "" } } } as any,
  })
  const built = await buildAgentLanePacket({ workId: WORK_ID, productId: PRODUCT_ID, laneId: "implement", attemptId: "attempt-1", stepId: "step-1" }, { context: contextFor(), invoke: adapter.invokeConcordOperation as any })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("transport_failure")
  expect(built.failure!.message).toContain("concord_work_browse.scope")
})
