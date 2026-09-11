import { test, expect, mock } from "bun:test"
import { manifestDigest } from "./generated-contracts"
import { validateGeneratedEnvelope, validateGeneratedPayload } from "./generated-contract-tests"
import { configureCoreBinary, validateAgentLanePacket } from "./dispatch"
import { agentLaneReportSchema, agentLaneReportConstraints, agentLanes } from "./generated-agent-lanes"

// The builder reaches core through the adapter transport in concord.ts, which
// imports the host plugin surface. The stub mirrors concord.test.ts so the
// suite never loads the real host module.
function schemaBuilder() {
  return {
    optional() { return this }, strict() { return this }, min() { return this }, max() { return this }, int() { return this }, regex() { return this },
    meta() { return this },
  }
}
const fakeTool = Object.assign((config: any) => config, {
  schema: {
    object: schemaBuilder, array: schemaBuilder, record: schemaBuilder, union: schemaBuilder, literal: schemaBuilder,
    string: schemaBuilder, number: schemaBuilder, unknown: schemaBuilder, null: schemaBuilder,
  },
})
mock.module("@opencode-ai/plugin", () => ({ tool: fakeTool }))

// Fake-runner suite: bind the transport to a nominal core path instead of
// the unstamped repository placeholder (CD-0111 D1).
configureCoreBinary("concord")

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
const DESIGN_RECORD = {
  work_version: 2,
  approach: "Use the recorded design as the implementation boundary.",
  decisions: [{ id: "decision:boundary", question: "What crosses into execution?", choice: "The typed design record.", rationale: "The implement lane must not select architecture.", rejected: ["Lane-authored methodology"] }],
  touched_refs: ["path:adapter/opencode/packet.ts"],
  recorded_at: "2026-09-09T00:00:00Z",
}

const contextResponse = () => ({ project_id: "project-1", product_ids: ["product-1"], scope_version: "1" })

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
function pinnedContract(outcomePayload: string = OUTCOME_PAYLOAD, premise: string = "Dispatch inputs are retyped rather than projected.") {
  return {
    version: 1,
    premise,
    outcome_predicates: [{ predicate_id: "predicate:primary", ordinal: 0, outcome_kind: OUTCOME_KIND, outcome_payload: outcomePayload }],
    required_evidence: [],
    route_conventions: [],
    spec_mandate: [],
    changes_product_truth: false,
  }
}

const continuityEnvelope = (contract: unknown = pinnedContract(), designRecord: unknown = null) => coreEnvelope("concord_work_trace", "continuity", "C19.Continuity", "ok", {
  result: {
    work_id: WORK_ID,
    pinned: {
      product_identity: [PRODUCT_ID],
      workflow_step: WORKFLOW_STEP,
      step_actions: [],
      contract,
      spec_mandate: [],
      pending_operator_decision: null,
      latest_checkpoint: null,
      design_record: designRecord,
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

function mandateParts(packet: { inputs: { constraints?: string[] } }): string[] {
  const entries = packet.inputs.constraints!.filter((entry) => entry.startsWith("Approved end-state mandate (join parts in order) "))
  for (const [index, entry] of entries.entries()) {
    expect(entry).toStartWith(`Approved end-state mandate (join parts in order) ${index + 1}/${entries.length}: `)
    expect(entry.length).toBeLessThanOrEqual(512)
  }
  return entries.map((entry) => entry.slice(entry.indexOf(": ") + 2))
}

test("packet and installed agent advertise only their lane's evidence vocabulary", async () => {
  for (const lane of agentLanes) {
    const built = await build(defaultScript(), { laneId: lane.id })
    expect(built.failure).toBeUndefined()
    const constraints = built.packet!.inputs.constraints!
    const enums = constraints.filter((entry) => entry.startsWith("evidence_entry.obligation: enum="))
    expect(enums).toHaveLength(1)
    const declared = JSON.parse(enums[0]!.slice("evidence_entry.obligation: enum=".length, -1))
    expect(declared).toEqual([...lane.evidence_obligations])
    const agent = await Bun.file(new URL(`../../.opencode/agents/concord-${lane.id}.md`, import.meta.url)).text()
    expect(agent).toContain(enums[0]!)
  }
})

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
  expect(packet.inputs.task).toContain(WORKFLOW_STEP)
  expect(packet.inputs.task).not.toContain(OUTCOME_KIND)
  expect(packet.inputs.task).not.toContain(OUTCOME_PAYLOAD)
  expect(JSON.parse(mandateParts(packet).join(""))).toEqual(pinnedContract().outcome_predicates)
  expect(packet.inputs.context).toBe(NARRATIVE)
  for (const obligation of agentLanes[1].evidence_obligations) {
    expect(packet.inputs.constraints!.some((entry) => entry.includes(`"${obligation}"`))).toBe(true)
  }
})

test("a packet projects the report schema bounds into worker constraints", async () => {
  const built = await build(defaultScript())
  expect(built.failure).toBeUndefined()
  const constraints = built.packet!.inputs.constraints!
  const reportProperties = agentLaneReportSchema.properties
  const reportEntry = agentLaneReportSchema.$defs.evidence_entry
  expect(constraints.some((entry) => entry.includes(`additionalProperties=${agentLaneReportSchema.additionalProperties}`))).toBe(true)
  expect(constraints.some((entry) => entry.includes(`maxItems=${reportProperties.evidence.maxItems}`))).toBe(true)
  expect(constraints.some((entry) => entry.includes(`maxLength=${reportEntry.properties.detail.maxLength}`))).toBe(true)
  expect(constraints.some((entry) => entry.includes(`maxLength=${reportProperties.readback_model.maxLength}`))).toBe(true)
  expect(constraints.some((entry) => entry.includes(reportProperties.readback_model.pattern))).toBe(true)
  const statusConstraint = constraints.find((entry) => entry.startsWith("status: enum="))
  expect(statusConstraint).toBeDefined()
  for (const status of reportProperties.status.enum) expect(statusConstraint).toContain(JSON.stringify(status))
})

// #903: the approved premise is the objective a dispatched worker must
// deliver, and the packet names the exact recorded state it projected. A
// worker that only satisfies the predicates without delivering the premise
// has not delivered the requested change.
test("the task carries the approved objective and binds to the work and contract versions", async () => {
  const built = await build(defaultScript())
  expect(built.failure).toBeUndefined()
  const task = built.packet!.inputs.task
  expect(task).toContain("Approved objective:")
  expect(task).toContain("Dispatch inputs are retyped rather than projected.")
  expect(task).toContain(`(work v1, contract v1)`)
})

test("the context carries the pinned design before the work narrative", async () => {
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), DESIGN_RECORD) })
  expect(built.failure).toBeUndefined()
  const context = built.packet!.inputs.context!
  expect(context.indexOf("Approved design record:")).toBe(0)
  expect(context.indexOf("The dispatched worker goal")).toBeGreaterThan(context.indexOf("Touched refs:"))
  expect(context).toContain("The typed design record.")
})

// #903: non-Initiative work items carry no narrative, and a missing
// narrative must not erase the objective from the packet.
test("a non-Initiative work item with no narrative still carries the approved objective", async () => {
  const built = await build({ ...defaultScript(), "concord_work_browse.scope": scopeEnvelope("") })
  expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
  const packet = built.packet!
  expect(validateAgentLanePacket(packet)).toBe(true)
  expect(packet.inputs.context).toBeUndefined()
  expect(packet.inputs.task).toContain("Approved objective:")
  expect(packet.inputs.task).toContain("Dispatch inputs are retyped rather than projected.")
  expect(packet.inputs.task).not.toContain(OUTCOME_PAYLOAD)
  expect(JSON.parse(mandateParts(packet).join(""))).toEqual(pinnedContract().outcome_predicates)
})

// #903/#904 boundary: a pinned contract whose premise carries no objective
// has nothing to deliver, so the builder refuses rather than projecting a
// predicate-only mandate that baseline checks could satisfy.
test("a pinned contract with a contentless premise is a typed unapproved-mandate failure", async () => {
  const contentless = pinnedContract()
  contentless.premise = "   "
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(contentless) })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("mandate_unapproved")
  expect(built.failure!.message).toContain("no approved objective")
})

test("a pinned contract without a typed version is a typed transport failure", async () => {
  const unversioned = pinnedContract()
  delete (unversioned as Record<string, unknown>).version
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(unversioned) })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("transport_failure")
  expect(built.failure!.message).toContain("versions")
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
    expect(packet.inputs.constraints!.length).toBeGreaterThan(lane.evidence_obligations.length)
    const laneConstraints = packet.inputs.constraints!.filter((entry) => entry.startsWith("Evidence obligation "))
    for (const obligation of lane.evidence_obligations) {
      expect(laneConstraints.some((entry) => entry.includes(`"${obligation}"`)), `${lane.id} omitted ${obligation}`).toBe(true)
    }
    const own = new Set<string>(lane.evidence_obligations)
    const foreign = [...new Set(agentLanes.flatMap((other) => other.evidence_obligations as readonly string[]))].filter((obligation) => !own.has(obligation))
    for (const obligation of foreign) {
      expect(laneConstraints.some((entry) => entry.includes(`"${obligation}"`)), `${lane.id} leaked ${obligation}`).toBe(false)
    }
  }
})

test("an unregistered lane is a typed failure", async () => {
  const built = await build(defaultScript(), { laneId: "summarize" })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("unregistered_lane")
  expect(built.failure!.message).toContain("summarize")
})

test("a representable multi-subject predicate is not limited to one constraint entry", async () => {
  const contract = pinnedContract()
  contract.outcome_predicates = [{
    predicate_id: "predicate:files",
    ordinal: 0,
    outcome_kind: "exists",
    outcome_payload: JSON.stringify({ kind: "exists", surface: "repository", subjects: Array.from({ length: 8 }, (_, index) => `file/path-${index}-${"a".repeat(64)}`) }),
  }]
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(contract) })
  expect(built.failure).toBeUndefined()
  expect(validateAgentLanePacket(built.packet!)).toBe(true)
  expect(JSON.parse(mandateParts(built.packet!).join(""))).toEqual(contract.outcome_predicates)
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

test("an oversized pinned design and narrative are a typed context overflow", async () => {
  const design = { ...DESIGN_RECORD, approach: "d".repeat(4_096) }
  const built = await build({ ...defaultScript(), "concord_work_browse.scope": scopeEnvelope("n".repeat(12_289)), "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), design) })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("projection_overflow")
  expect(built.failure!.field).toBe("context")
  expect(built.failure!.limit).toBe(16_384)
})

test("a predicate larger than one constraint remains lossless across parts", async () => {
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(pinnedContract("m".repeat(4_096))) })
  expect(built.failure).toBeUndefined()
  expect(validateAgentLanePacket(built.packet!)).toBe(true)
  expect(JSON.parse(mandateParts(built.packet!).join(""))).toEqual(pinnedContract("m".repeat(4_096)).outcome_predicates)
})

test("a mandate that overflowed the task bound remains lossless in constraints", async () => {
  const predicates = Array.from({ length: 8 }, (_, ordinal) => ({
    predicate_id: `predicate:synthetic-${ordinal}`,
    ordinal,
    outcome_kind: "check",
    outcome_payload: JSON.stringify({ kind: "check", check_ref: `check:synthetic/${ordinal}/${"r".repeat(100)}`, immutable_subject_ref: "contract:synthetic/v1", expected_result: "pass" }),
  }))
  const legacyTaskFor = (premise: string) => [
    `Deliver the approved objective for work ${WORK_ID}, at workflow step "${WORKFLOW_STEP}" (work v1, contract v1).`,
    "",
    "Approved objective:",
    premise,
    "",
    "Approved end-state mandate:",
    JSON.stringify(predicates),
  ].join("\n")
  const premise = "o".repeat(4_584 - legacyTaskFor("").length)
  const legacyTask = legacyTaskFor(premise)
  expect(legacyTask.length).toBe(4_584)
  expect(legacyTask.length).toBeGreaterThan(4_096)

  const contract = pinnedContract(OUTCOME_PAYLOAD, premise)
  contract.outcome_predicates = predicates
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(contract) })
  expect(built.failure).toBeUndefined()
  const packet = built.packet!
  expect(packet.inputs.task).not.toContain("predicate:synthetic-0")
  expect(packet.inputs.task).toContain(premise)
  expect(mandateParts(packet).join("")).toBe(JSON.stringify(predicates))
  expect(validateAgentLanePacket(packet)).toBe(true)
})

test("the task bound rejects only the next character", async () => {
  const taskPrefix = [
    `Deliver the approved objective for work ${WORK_ID}, at workflow step "${WORKFLOW_STEP}" (work v1, contract v1).`,
    "",
    "Approved objective:",
  ].join("\n") + "\n"
  const exactPremise = "o".repeat(4_096 - taskPrefix.length)
  const exactTask = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(pinnedContract(OUTCOME_PAYLOAD, exactPremise)) })
  expect(exactTask.failure).toBeUndefined()
  expect(exactTask.packet!.inputs.task.length).toBe(4_096)

  const oversizedTask = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(pinnedContract(OUTCOME_PAYLOAD, `${exactPremise}o`)) })
  expect(oversizedTask.failure!.field).toBe("task")
  expect(oversizedTask.failure!.actual).toBe(4_097)

})

test("the combined mandate and report guidance bound admits exactly 64 entries", async () => {
  const contract = pinnedContract("")
  const partLimit = 512 - "Approved end-state mandate (join parts in order) 64/64: ".length
  const reportCount = agentLanes.find((lane) => lane.id === "implement")!.evidence_obligations.length + agentLaneReportConstraints.length
  const payloadLength = (64 - reportCount) * partLimit - JSON.stringify(contract.outcome_predicates).length
  contract.outcome_predicates[0].outcome_payload = "p".repeat(payloadLength)
  const exact = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(contract) })
  expect(exact.failure).toBeUndefined()
  expect(exact.packet!.inputs.constraints).toHaveLength(64)
  expect(validateAgentLanePacket(exact.packet!)).toBe(true)
  expect(JSON.parse(mandateParts(exact.packet!).join(""))).toEqual(contract.outcome_predicates)

  contract.outcome_predicates[0].outcome_payload += "p"
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(contract) })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("projection_overflow")
  expect(built.failure!.field).toBe("constraints")
  expect(built.failure!.limit).toBe(64)
  expect(built.failure!.actual).toBe(65)
})

test("mandate chunking preserves Unicode and is deterministic across boundaries", async () => {
  for (const padding of [0, 1, 127, 255, 450, 451, 452, 511, 512, 513, 1_024]) {
    const payload = JSON.stringify({ text: `${"x".repeat(padding)}${"🚀e\u0301漢\n\"\\".repeat(80)}` })
    const contract = pinnedContract(payload)
    const script = { ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(contract, DESIGN_RECORD) }
    const first = await build(script)
    const second = await build(script)
    expect(first.failure).toBeUndefined()
    expect(second).toEqual(first)
    expect(validateAgentLanePacket(first.packet!)).toBe(true)
    const parts = mandateParts(first.packet!)
    expect(parts.length).toBeGreaterThan(1)
    for (const part of parts) {
      expect(/[\uD800-\uDBFF]$/.test(part)).toBe(false)
      expect(/^[\uDC00-\uDFFF]/.test(part)).toBe(false)
    }
    expect(parts.join("")).toBe(JSON.stringify(contract.outcome_predicates))
    expect(JSON.parse(parts.join(""))).toEqual(contract.outcome_predicates)
    expect(first.packet!.inputs.context).toContain("The typed design record.")
    expect(first.packet!.inputs.context).toEndWith(NARRATIVE)
  }
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

test("a pinned contract without typed outcome predicates is a typed transport failure", async () => {
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope({ version: 1, premise: "p", outcome_predicates: 7, required_evidence: [], route_conventions: [], spec_mandate: [] }) })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("transport_failure")
  expect(built.failure!.message).toContain("outcome_predicates")
})

test("the default transport is the adapter transport, and its refusals stay typed", async () => {
  // The builder is wired to the shipped transport by accepting the adapter's
  // invokeConcordOperation through its injected seam. The context runner returns
  // a typed context that the invoke call cannot match, so the second
  // call lands on the opencode stub and the response is malformed; the test
  // only proves the wired transport was used, not what it returned.
  adapter.configureConcordAdapter({
    runner: { async run() { return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" } } } as any,
  })
  const built = await buildAgentLanePacket({ workId: WORK_ID, productId: PRODUCT_ID, laneId: "implement", attemptId: "attempt-1", stepId: "step-1" }, { context: contextFor(), invoke: adapter.invokeConcordOperation as any })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("transport_failure")
  expect(built.failure!.message).toContain("concord_work_browse.scope")
})
