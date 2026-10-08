import { test, expect, mock } from "bun:test"
import { manifestDigest } from "./generated-contracts"
import { validateGeneratedEnvelope, validateGeneratedPayload } from "./generated-contract-tests"
import { configureCoreBinary, validateAgentLanePacket, type AgentLanePacketBinding, type AgentLanePacketCorrection } from "./dispatch"
import { agentLaneReportSchema, agentLanes, workerScopeAssignedResult } from "./generated-agent-lanes"

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
const OUTCOME_KIND = "check"
// The continuity read carries each outcome_payload as a JSON-encoded string;
// the builder decodes it into the typed packet field.
const OUTCOME_PAYLOAD_OBJECT = { kind: "check", check_ref: "check:projected-dispatch-inputs", immutable_subject_ref: "contracts/agent-lane-packet.schema.json", expected_result: "pass" }
const OUTCOME_PAYLOAD = JSON.stringify(OUTCOME_PAYLOAD_OBJECT)
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

// The typed packet field the builder emits: the contract's predicates with
// each serialized outcome_payload decoded into its validated object.
const decodedContractPredicates = (contract = pinnedContract()) =>
  contract.outcome_predicates.map((predicate: any) => ({ ...predicate, outcome_payload: JSON.parse(predicate.outcome_payload) }))

function typedPredicates(packet: { inputs: { outcome_predicates?: unknown[] } }): unknown[] {
  expect(Array.isArray(packet.inputs.outcome_predicates)).toBe(true)
  return packet.inputs.outcome_predicates!
}

function assertNoMandateSplice(packet: { inputs: { constraints?: string[] } }): void {
  expect(packet.inputs.constraints).toBeUndefined()
}

const continuityEnvelope = (contract: unknown = pinnedContract(), designRecord: unknown = null, workPin: unknown = null, lawContext: unknown = null, proposalRecord: unknown = null) => coreEnvelope("concord_work_trace", "continuity", "C19.Continuity", "ok", {
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
       ...(workPin === null ? {} : { work_pin: workPin }),
      ...(lawContext === null ? {} : { law_context: lawContext }),
      ...(proposalRecord === null ? {} : { proposal_record: proposalRecord }),
      unresolved_failure: null,
    },
    boundaries: { count: 0, items: [], next_cursor: null, watermark: "seq:1" },
    typed_availability: { restart: "unavailable", reason: RESTART_UNAVAILABLE_REASON },
    pending_messages: 0,
    observations: [],
    observations_total: 0,
    observations_read: { tool: "concord_work_trace", operation: "observations", input: { work_id: WORK_ID } },
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

test("binding preserves recorded work and contract versions beyond signed int32", async () => {
  const version = 2_147_483_648
  const scope = scopeEnvelope() as any
  scope.result.work.version = version
  const built = await build({
    ...defaultScript(),
    "concord_work_browse.scope": scope,
    "concord_work_trace.continuity": continuityEnvelope({ ...pinnedContract(), version }),
  })
  expect(built.failure).toBeUndefined()
  expect(built.packet!.inputs.binding.work_version).toBe(version)
  expect(built.packet!.inputs.binding.contract_version).toBe(version)
})

test("the authoring contract distinguishes premise bytes from packet code points and model tokens", async () => {
  const payloadSchema = await Bun.file(new URL("../../contracts/agent-tool-surface-payloads.schema.json", import.meta.url)).json()
  const premise = payloadSchema.$defs.workflow_premise
  expect(premise.maxLength).toBe(4_096)
  expect(premise.description).toContain("Unicode code points")
  expect(premise.description).toContain("UTF-8 bytes")
  expect(premise.description).toContain("model-token limit")
  expect(premise.description).toContain("Do not truncate an approved objective")
})

test("installed agents advertise only their lane's evidence vocabulary", async () => {
  for (const lane of agentLanes) {
    const built = await build(defaultScript(), { laneId: lane.id })
    expect(built.failure).toBeUndefined()
    const agent = await Bun.file(new URL(`../../.opencode/agents/concord-${lane.id}.md`, import.meta.url)).text()
    for (const obligation of lane.evidence_obligations) expect(agent).toContain("`" + obligation + "`")
    assertNoMandateSplice(built.packet!)
  }
})

test("a well-formed build projects mandate, narrative, and obligations into a valid packet", async () => {
  const built = await build(defaultScript())
  expect(built.failure).toBeUndefined()
  const packet = built.packet!
  expect(validateAgentLanePacket(packet)).toBe(true)
  expect(packet.schema_version).toBe("1.1")
  expect(packet.attempt_id).toBe("attempt-1")
  expect(packet.work_id).toBe(WORK_ID)
  expect(packet.step_id).toBe("step-1")
  expect(packet.lane_id).toBe("implement")
  expect(packet.lane_version).toBe(1)
  expect(packet.lane_digest).toBe("sha256:f4dad03f0b94430af796eecc1c53740d6441286c78879b46bee17ec79ce604e5")
  expect(packet.inputs.task).toBe("Dispatch inputs are retyped rather than projected.")
  expect(packet.inputs.task).not.toContain(OUTCOME_KIND)
  expect(packet.inputs.task).not.toContain(OUTCOME_PAYLOAD)
  expect(packet.inputs.binding).toEqual({ objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "files_touched" })
  expect(typedPredicates(packet)).toEqual(decodedContractPredicates())
  expect(packet.inputs.context).toBe(NARRATIVE)
  const agent = await Bun.file(new URL("../../.opencode/agents/concord-implement.md", import.meta.url)).text()
  for (const obligation of agentLanes[1].evidence_obligations) {
    expect(agent).toContain(`"${obligation}"`)
  }
  assertNoMandateSplice(packet)
})

// The worker-scope contract bounds each attempt to its lane's one assigned
// result, so every projected packet binds that result as typed data in
// inputs.binding rather than as prose appended to the objective.
const bindingOf = (packet: { inputs: { binding: AgentLanePacketBinding } }): AgentLanePacketBinding =>
  packet.inputs.binding

test("each dispatch binds its lane's one assigned result in inputs.binding", async () => {
  for (const lane of agentLanes) {
    const built = await build(defaultScript(), { laneId: lane.id })
    expect(built.failure).toBeUndefined()
    const binding = bindingOf(built.packet!)
    expect(binding.assigned_result).toBe(workerScopeAssignedResult(lane.id)!)
    expect(binding.objective_source).toBe("contract_premise")
    expect(binding.work_version).toBe(1)
    expect(binding.contract_version).toBe(1)
  }
})

// The parent dispatches sequential bounded attempts: each successive packet
// for the same work binds its own attempt to its own lane's assigned result.
test("sequential dispatches bound each attempt to its own lane's assigned result", async () => {
  const first = await build(defaultScript(), { laneId: "research", attemptId: "attempt-1" })
  const second = await build(defaultScript(), { laneId: "implement", attemptId: "attempt-2" })
  expect(first.failure).toBeUndefined()
  expect(second.failure).toBeUndefined()
  expect(bindingOf(first.packet!).assigned_result).toBe("bounded_findings")
  expect(bindingOf(second.packet!).assigned_result).toBe("files_touched")
  expect(bindingOf(first.packet!).assigned_result).not.toBe("files_touched")
  expect(second.packet!.attempt_id).toBe("attempt-2")
})

test("a correction projects recorded failure fields into the packet", async () => {
  const correction: AgentLanePacketCorrection = {
    disposition: "failed",
    attempt_count: 1,
    attempt_limit: 3,
    escalated: false,
    diagnosis: "worker attempt failed",
    strategy: "retry with a fresh fenced attempt",
    failure_kind: "transport_failure",
    failure_detail: "the worker could not reach the service",
    predicate_ids: [],
    evidence_refs: [],
  }
  const built = await build({
    ...defaultScript(),
    "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), null, { correction }),
  })
  expect(built.failure).toBeUndefined()
  expect(built.packet!.inputs.correction).toEqual(correction)
  expect(built.packet!.inputs.context).toBe(NARRATIVE)
})

// The checkpoint failed-review return (amended CD-0143 D1) projects the same
// bounded context: the failure disposition stands beside the affected
// predicates and bound evidence the operator approved, and the narrative
// context stays separate from it.
test("a checkpoint failed-review correction projects its bounded verdict evidence into the packet", async () => {
  const correction: AgentLanePacketCorrection = {
    disposition: "failed",
    attempt_count: 1,
    attempt_limit: 3,
    escalated: false,
    diagnosis: "the checkpoint review attempt failed and the latest verification verdict is not healthy",
    strategy: "return to the repair step and dispatch a fresh attempt",
    predicate_ids: ["predicate:return-route"],
    evidence_refs: ["evidence:return-route-verification"],
  }
  const built = await build({
    ...defaultScript(),
    "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), null, { correction }),
  })
  expect(built.failure).toBeUndefined()
  expect(built.packet!.inputs.correction).toEqual(correction)
  expect(built.packet!.inputs.correction!.disposition).toBe("failed")
  expect(built.packet!.inputs.context).toBe(NARRATIVE)
})

// A correction counts every operator-authorized retry, so its count passes
// attempt_limit once the operator allows work past the limit. Dropping such a
// correction left the packet unable to consume it, and the core refuses a
// dispatch that carries no correction while one is durable. Because a
// correction clears only when a dispatch follows it, that stranded the work
// item: no lane could ever be dispatched again, review and verify included.
test("a correction past the attempt limit still projects into the packet", async () => {
  const correction: AgentLanePacketCorrection = {
    disposition: "rejected",
    attempt_count: 6,
    attempt_limit: 3,
    escalated: true,
    diagnosis: "the attempt did not repair the reported defects",
    strategy: "repair the reported defects with a failing regression before each fix",
    predicate_ids: [],
    evidence_refs: [],
  }
  const built = await build({
    ...defaultScript(),
    "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), null, { correction }),
  })
  expect(built.failure).toBeUndefined()
  expect(built.packet!.inputs.correction).toEqual(correction)
  expect(built.packet!.inputs.correction!.attempt_count).toBe(6)
  expect(built.packet!.inputs.correction!.escalated).toBe(true)
})

test("installed agents project the report schema bounds", async () => {
  const built = await build(defaultScript())
  expect(built.failure).toBeUndefined()
  const agent = await Bun.file(new URL("../../.opencode/agents/concord-implement.md", import.meta.url)).text()
  const reportProperties = agentLaneReportSchema.properties
  const reportEntry = agentLaneReportSchema.$defs.evidence_entry
  expect(agent).toContain(`additionalProperties=${agentLaneReportSchema.additionalProperties}`)
  expect(agent).toContain(`maxItems=${reportProperties.evidence.maxItems}`)
  expect(agent).toContain(`maxLength=${reportEntry.properties.detail.maxLength}`)
  expect(agent).toContain(`maxLength=${reportProperties.readback_model.maxLength}`)
  expect(agent).toContain(reportProperties.readback_model.pattern)
  expect(agent).toContain("evidence_entry.predicate_ids: optional array; type=array, minItems=0, maxItems=8")
  expect(agent).toContain("omit it or use an empty array on an entry that proves no declared predicate")
  expect(agent).toContain("Both forms mean no predicate tie")
  expect(agent).not.toContain("an empty array fails")
  const statusConstraint = `status: enum=[${reportProperties.status.enum.map((status) => JSON.stringify(status)).join(", ")}]`
  expect(agent).toContain(statusConstraint)
})

// The approved premise is the objective a dispatched worker must
// deliver, and the packet binds the exact recorded state it projected. A
// worker that only satisfies the predicates without delivering the premise
// has not delivered the requested change. The premise is inputs.task
// verbatim — no header, no trailer — and the versions ride inputs.binding.
test("the task is the approved objective verbatim and the binding carries the versions", async () => {
  const premise = "Dispatch inputs are retyped rather than projected."
  const built = await build(defaultScript())
  expect(built.failure).toBeUndefined()
  const packet = built.packet!
  expect(packet.inputs.task).toBe(premise)
  expect(packet.inputs.task).not.toContain("Approved objective:")
  expect(packet.inputs.task).not.toContain(WORK_ID)
  expect(packet.inputs.binding).toEqual({ objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "files_touched" })
})

test("a read-only lane carries the recorded question verbatim before contract approval", async () => {
  const built = await build(
    { ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(null) },
    { laneId: "research", stepId: "investigate" },
  )
  expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
  const packet = built.packet!
  expect(validateAgentLanePacket(packet)).toBe(true)
  expect(packet.inputs.task).toBe("Project dispatch inputs from durable state")
  expect(packet.inputs.task).not.toContain("Answer the recorded question")
  expect(packet.inputs.task).not.toContain("Step question:")
  expect(packet.inputs.task).not.toContain(NARRATIVE)
  expect(packet.inputs.binding).toEqual({ objective_source: "work_question", work_version: 1, contract_version: null, assigned_result: "bounded_findings" })
  expect(packet.inputs.context).toBe(NARRATIVE)
  expect(packet.inputs.outcome_predicates).toBeUndefined()
  assertNoMandateSplice(packet)
})

test("a read-only recorded question preserves surrounding whitespace and Unicode", async () => {
  const question = "  Compare 𝕏 and é.\n"
  const scope = scopeEnvelope() as any
  scope.result.work.task = question
  const built = await build({
    ...defaultScript(),
    "concord_work_browse.scope": scope,
    "concord_work_trace.continuity": continuityEnvelope(null),
  }, { laneId: "research" })
  expect(built.failure).toBeUndefined()
  expect(built.packet!.inputs.task).toBe(question)
  expect(Buffer.from(built.packet!.inputs.task)).toEqual(Buffer.from(question))
  expect(built.packet!.inputs.context).not.toContain(question)
})

test("a review lane keeps the pinned contract mandate after read-only classification", async () => {
  const contract = pinnedContract()
  const built = await build(
    { ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(contract) },
    { laneId: "review", stepId: "review" },
  )
  expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
  const packet = built.packet!
  expect(validateAgentLanePacket(packet)).toBe(true)
  expect(packet.inputs.task).toBe(contract.premise)
  expect(packet.inputs.binding).toEqual({ objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "contract_findings" })
  expect(typedPredicates(packet)).toEqual(decodedContractPredicates(contract))
})

test("the context carries the pinned design before the work narrative", async () => {
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), DESIGN_RECORD) })
  expect(built.failure).toBeUndefined()
  const context = built.packet!.inputs.context!
  expect(context.indexOf("Approved design record:")).toBe(0)
  expect(context.indexOf("The dispatched worker goal")).toBeGreaterThan(context.indexOf("Touched refs:"))
  expect(context).toContain("The typed design record.")
})

// The core resolves the approved contract's bound law and Domains at
// continuity read time; the builder renders that block and the recorded
// proposal after the design record, ahead of the work narrative.
const LAW_CONTEXT = {
  laws: [
    { roles: ["added"], law_id: "law:new" },
    { roles: ["mandated", "modified", "obligation"], law_id: "spec:one", kind: "spec", status: "accepted", title: "Synthetic test law", path: ".concord/docs/spec.md", obligation_ids: ["verification"] },
  ],
  domains: [
    { domain_id: "root", name: "Root", purpose: "Product law" },
    { domain_id: "child", name: "Child", purpose: "Child law" },
  ],
  registry_path: ".concord/docs/knowledge/domain-registry.json",
}
const PROPOSAL = { problem: "Workers receive bare law IDs", user_outcomes: ["Workers read the binding law"], constraints: ["Overflow stays fail-closed"] }

test("the context carries the resolved law block and proposal after the design record", async () => {
  const continuity = continuityEnvelope(pinnedContract(), DESIGN_RECORD, null, LAW_CONTEXT, PROPOSAL)
  expect(validateGeneratedEnvelope(continuity)).toBe(true)
  expect(validateGeneratedPayload("continuity_snapshot", (continuity as any).result)).toBe(true)
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuity })
  expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
  expect(validateAgentLanePacket(built.packet!)).toBe(true)
  const context = built.packet!.inputs.context!
  const designAt = context.indexOf("Approved design record:")
  const lawAt = context.indexOf("Approved law and Domains (binding Product law):")
  const proposalAt = context.indexOf("Recorded proposal:")
  expect(designAt).toBe(0)
  expect(lawAt).toBeGreaterThan(designAt)
  expect(proposalAt).toBeGreaterThan(lawAt)
  expect(context.indexOf(NARRATIVE)).toBeGreaterThan(proposalAt)
  expect(context).toContain("- mandated, modified, obligation law spec:one (obligation verification): Synthetic test law, spec, accepted — .concord/docs/spec.md")
  expect(context).toContain("- added law law:new")
  expect(context).toContain("- Domain root: Root — Product law")
  expect(context).toContain("- Domain child: Child — Child law")
  expect(context).toContain("Domain registry: .concord/docs/knowledge/domain-registry.json")
  expect(context).toContain("Problem: Workers receive bare law IDs")
  expect(context).toContain("- Workers read the binding law")
  expect(context).toContain("- Overflow stays fail-closed")
})

// The registry path rides the law context only when the core sets it, so a
// context without one renders no registry line for the lane to follow.
test("a law context without a registry path renders no registry line", async () => {
  const { registry_path: _omitted, ...withoutRegistry } = LAW_CONTEXT
  const continuity = continuityEnvelope(pinnedContract(), DESIGN_RECORD, null, withoutRegistry, PROPOSAL)
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuity })
  expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
  expect(built.packet!.inputs.context).toContain("- Domain root: Root — Product law")
  expect(built.packet!.inputs.context).not.toContain("Domain registry:")
})

test("a contract with no bound law dispatches without a law block", async () => {
  const built = await build(defaultScript())
  expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
  expect(built.packet!.inputs.context).not.toContain("Approved law and Domains")
  expect(built.packet!.inputs.context).not.toContain("Recorded proposal:")
})

// The core resolves the mandated spec's criteria bound to this work item's
// predicates (CD-0180) into the law entry's criteria field; the packet lists
// the chaining on the law line so the worker sees which criterion each of
// this item's predicates discharges.
test("the law block lists the mandated criteria bound to this work item's predicates", async () => {
  const lawContext = {
    laws: [
      { roles: ["mandated"], law_id: "spec:one", kind: "spec", status: "accepted", title: "Synthetic test law", path: ".concord/docs/spec.md", criteria: [{ criterion: 2, predicate_id: "predicate:criterion-bindings-predicate-form" }, { criterion: 1, predicate_id: "predicate:packet-mandated-criteria" }] },
      { roles: ["mandated"], law_id: "spec:plain", kind: "spec", status: "accepted", title: "Unbound spec", path: ".concord/docs/plain.md" },
    ],
    domains: [],
  }
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), null, null, lawContext) })
  expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
  expect(validateAgentLanePacket(built.packet!)).toBe(true)
  const context = built.packet!.inputs.context!
  expect(context).toContain("law spec:one")
  expect(context).toContain("(criteria bound to this work item: criterion 2 discharges predicate:criterion-bindings-predicate-form; criterion 1 discharges predicate:packet-mandated-criteria)")
  expect(context).toContain("law spec:plain")
  expect(context).not.toContain("criteria bound to this work item: criterion 1 discharges predicate:criterion-bindings-predicate-form")
})

test("an oversized law block is a typed context overflow, not a truncated packet", async () => {
  // Every entry stays inside the generated law-context bounds; only their
  // number pushes the combined context past the bound.
  const oversized = { laws: Array.from({ length: 64 }, (_, index) => ({ roles: ["mandated"], law_id: `spec:big-${index}`, title: "t".repeat(512) })), domains: [] }
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), null, null, oversized) })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("projection_overflow")
  expect(built.failure!.field).toBe("context")
  expect(built.failure!.limit).toBe(16_384)
})

test("the packet carries bounded correction data outside the narrative", async () => {
  const continuity = continuityEnvelope()
  const pinned = (continuity as any).result.pinned
  pinned.work_pin = {
    work_id: WORK_ID,
    title: "Correction",
    linear_issue_key: "",
    version: 4,
    lifecycle: "in_progress",
    workflow_type: "workflow.implementation",
    step: WORKFLOW_STEP,
    attempt: null,
    pending_operator_decision: null,
    watermark: "seq:4",
    next_valid_intents: [],
    correction: {
      disposition: "rejected",
      attempt_count: 1,
      attempt_limit: 3,
      escalated: false,
      predicate_ids: ["predicate:primary"],
      evidence_refs: ["evidence:review"],
      diagnosis: "the result misses the boundary case",
      strategy: "change the helper and add a test",
    },
  }
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuity })
  expect(built.failure).toBeUndefined()
  expect(built.packet!.inputs.correction).toEqual(pinned.work_pin.correction)
  expect(built.packet!.inputs.context).toBe(NARRATIVE)
  expect(built.packet!.inputs.context).not.toContain("change the helper and add a test")
})

// #903: non-Initiative work items carry no narrative, and a missing
// narrative must not erase the objective from the packet.
test("a non-Initiative work item with no narrative still carries the approved objective", async () => {
  const built = await build({ ...defaultScript(), "concord_work_browse.scope": scopeEnvelope("") })
  expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
  const packet = built.packet!
  expect(validateAgentLanePacket(packet)).toBe(true)
  expect(packet.inputs.context).toBeUndefined()
  expect(packet.inputs.task).toBe("Dispatch inputs are retyped rather than projected.")
  expect(packet.inputs.task).not.toContain(OUTCOME_PAYLOAD)
  expect(typedPredicates(packet)).toEqual(decodedContractPredicates())
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

test("every registered lane packet projects only its serialized mandate", async () => {
  for (const lane of agentLanes) {
    const built = await build(defaultScript(), { laneId: lane.id })
    expect(built.failure, `${lane.id}: ${JSON.stringify(built.failure)}`).toBeUndefined()
    const packet = built.packet!
    expect(validateAgentLanePacket(packet)).toBe(true)
    expect(packet.lane_id).toBe(lane.id)
    expect(packet.lane_version).toBe(lane.version)
    expect(packet.lane_digest).toBe(lane.digest)
    expect(typedPredicates(packet)).toEqual(decodedContractPredicates())
    assertNoMandateSplice(packet)
  }
})

test("every installed lane definition states the multi-entry remedy", async () => {
  const detailMax = agentLaneReportSchema.$defs.evidence_entry.properties.detail["x-maxBytes"]
  for (const lane of agentLanes) {
    const agent = await Bun.file(`${import.meta.dir}/../../.opencode/agents/concord-${lane.id}.md`).text()
    expect(agent, `${lane.id} omitted the multi-entry remedy`).toContain("One obligation may span several entries")
    expect(agent).toContain(`${detailMax}-byte (UTF-8)`)
  }
})

// The lane contract owns what the law block in inputs.context means and what
// the report must disclose, so every generated lane definition carries the
// conformance rule. Only a lane granted edit_scoped_files is told to change
// files or law documents.
test("every installed lane definition carries the law conformance rule", async () => {
  for (const lane of agentLanes) {
    const agent = (await Bun.file(`${import.meta.dir}/../../.opencode/agents/concord-${lane.id}.md`).text()).replace(/\s+/g, " ")
    expect(agent, `${lane.id} omitted the law conformance rule`).toContain("Approved law and architecture block")
    expect(agent).toContain("Conform to it.")
    expect(agent).toContain("Report any conflict between that law and the assigned result in your evidence")
    expect(agent).toContain("`status` `failed`")
    if ((lane.capabilities as readonly string[]).includes("edit_scoped_files")) {
      expect(agent).toContain("Read each named law document before you change files")
      expect(agent).toContain("`modified` or `added`")
    } else {
      expect(agent).toContain("Read each named law document before you assess the result")
      expect(agent).not.toContain("before you change files")
      expect(agent).not.toContain("`modified` or `added`")
    }
  }
})

test("an unregistered lane is a typed failure", async () => {
  const built = await build(defaultScript(), { laneId: "summarize" })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("unregistered_lane")
  expect(built.failure!.message).toContain("summarize")
})

test("a representable multi-subject predicate rides the typed field verbatim", async () => {
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
  expect(typedPredicates(built.packet!)).toEqual(decodedContractPredicates(contract))
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

test("an outcome_payload the core recorded but that cannot decode is a typed transport failure", async () => {
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(pinnedContract("not json {")) })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("transport_failure")
  expect(built.failure!.message).toContain("outcome_payload")
})

test("an outcome_kind outside the closed set is a typed transport failure", async () => {
  const contract = pinnedContract()
  contract.outcome_predicates = [{ predicate_id: "predicate:legacy", ordinal: 0, outcome_kind: "capability_available", outcome_payload: "{}" }]
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(contract) })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("transport_failure")
  expect(built.failure!.message).toContain("capability_available")
})

test("a premise that outgrows the task bound is a typed task overflow, not a constraint spill", async () => {
  const premise = "o".repeat(4_500)
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(pinnedContract(OUTCOME_PAYLOAD, premise)) })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("projection_overflow")
  expect(built.failure!.field).toBe("task")
  expect(built.failure!.limit).toBe(4_096)
  expect(built.failure!.actual).toBe(4_500)
  // The refusal names the count unit, so an operator can tell the packet's
  // code-point bound from the approval premise's byte bound.
  expect(built.failure!.message).toContain("Unicode code points")
  expect(built.failure!.message).toContain("inputs.task")
})

// The whole approval premise capacity belongs to the objective. A
// premise at the full 4096-UTF-8-byte approval limit reaches inputs.task
// byte-for-byte, with no adapter framing charged against it, and the packet
// validates against the unchanged 4096-code-point task bound.
test("an admitted maximum ASCII premise reaches inputs.task byte-for-byte", async () => {
  const premise = "o".repeat(4_096)
  expect(Buffer.byteLength(premise, "utf8")).toBe(4_096)
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(pinnedContract(OUTCOME_PAYLOAD, premise)) })
  expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
  const packet = built.packet!
  expect(packet.inputs.task).toBe(premise)
  expect(Buffer.byteLength(packet.inputs.task, "utf8")).toBe(4_096)
  expect([...packet.inputs.task].length).toBe(4_096)
  expect(validateAgentLanePacket(packet)).toBe(true)
  expect(packet.inputs.binding).toEqual({ objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "files_touched" })
})

// The same guarantee for a maximum-size premise the approval byte limit
// admits from astral code points: 1024 four-byte characters are 4096 UTF-8
// bytes, reach the task byte-for-byte, and fit the code-point bound with
// room to spare — a UTF-16 count would misreport them as 2048 units, and a
// byte count of the code points would misreport them as 4096.
test("an admitted maximum astral premise reaches inputs.task byte-for-byte", async () => {
  const astral = "𝕏"
  expect(Buffer.byteLength(astral, "utf8")).toBe(4)
  const premise = astral.repeat(1_024)
  expect(Buffer.byteLength(premise, "utf8")).toBe(4_096)
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(pinnedContract(OUTCOME_PAYLOAD, premise)) })
  expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
  const packet = built.packet!
  expect(packet.inputs.task).toBe(premise)
  expect(Buffer.byteLength(packet.inputs.task, "utf8")).toBe(4_096)
  expect([...packet.inputs.task].length).toBe(1_024)
  expect(validateAgentLanePacket(packet)).toBe(true)
})

// A BMP premise fills the byte limit with 2048 two-byte characters and still
// reaches the task verbatim.
test("an admitted maximum BMP premise reaches inputs.task byte-for-byte", async () => {
  const premise = "é".repeat(2_048)
  expect(Buffer.byteLength(premise, "utf8")).toBe(4_096)
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(pinnedContract(OUTCOME_PAYLOAD, premise)) })
  expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
  expect(built.packet!.inputs.task).toBe(premise)
  expect(validateAgentLanePacket(built.packet!)).toBe(true)
})

// The closed validator counts JSON Schema Unicode code points, the unit the
// Go payload-schema validator counts with utf8.RuneCountInString. A UTF-16
// count would see 8192 units in a 4096-code-point astral task and refuse a
// packet the contract admits.
test("packet string validation counts Unicode code points, not UTF-16 units", () => {
  const packet = (task: string) => ({
    schema_version: "1.0", attempt_id: "attempt-1", lane_id: "implement", lane_version: 1,
    lane_digest: "sha256:f4dad03f0b94430af796eecc1c53740d6441286c78879b46bee17ec79ce604e5",
    work_id: WORK_ID, step_id: "step-1",
    inputs: { task, binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "files_touched" } },
  })
  const failures: string[] = []
  expect(validateAgentLanePacket(packet("𝕏".repeat(4_096)), failures), failures.join("; ")).toBe(true)
  expect(validateAgentLanePacket(packet("𝕏".repeat(4_097)), failures)).toBe(false)
  expect(failures[0]).toContain("Unicode code points")
})

test("the serialized typed predicate bound stays fail-closed at the capacity the splice carried", async () => {
  const fatPredicates = (subjectPadding: number) =>
    Array.from({ length: 8 }, (_, ordinal) => ({
      predicate_id: `predicate:synthetic-${ordinal}`,
      ordinal,
      outcome_kind: "exists",
      outcome_payload: JSON.stringify({ kind: "exists", surface: "repository", subjects: Array.from({ length: 100 }, (_, index) => `file/path-${index}-${"a".repeat(subjectPadding)}`) }),
    }))
  const contract = pinnedContract()
  contract.outcome_predicates = fatPredicates(48)
  const over = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(contract) })
  expect(over.packet).toBeUndefined()
  expect(over.failure!.kind).toBe("projection_overflow")
  expect(over.failure!.field).toBe("outcome_predicates")
  expect(over.failure!.limit).toBe(64 * 512)
  expect(over.failure!.actual).toBe(JSON.stringify(decodedContractPredicates(contract)).length)
  expect(over.failure!.actual).toBeGreaterThan(64 * 512)

  const slim = pinnedContract()
  slim.outcome_predicates = fatPredicates(8)
  const under = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(slim) })
  expect(under.failure).toBeUndefined()
  expect(validateAgentLanePacket(under.packet!)).toBe(true)
  expect(typedPredicates(under.packet!)).toEqual(decodedContractPredicates(slim))
})

test("the closed packet schema enforces the strict per-kind outcome payload field sets", () => {
  const packet = (predicate: Record<string, unknown>) => ({
    schema_version: "1.0", attempt_id: "attempt-1", lane_id: "implement", lane_version: 1,
    lane_digest: "sha256:f4dad03f0b94430af796eecc1c53740d6441286c78879b46bee17ec79ce604e5",
    work_id: WORK_ID, step_id: "step-1",
    inputs: { task: "t", binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "files_touched" }, outcome_predicates: [predicate] },
  })
  const check = { predicate_id: "predicate:primary", ordinal: 0, outcome_kind: "check", outcome_payload: { kind: "check", check_ref: "check:one", immutable_subject_ref: "contracts/x.json", expected_result: "pass" } }
  const failures: string[] = []
  expect(validateAgentLanePacket(packet(check), failures)).toBe(true)

  // A check payload missing its strict field set is refused by the if-then branch.
  const incomplete = { ...check, outcome_payload: { kind: "check", check_ref: "check:one" } }
  expect(validateAgentLanePacket(packet(incomplete), failures)).toBe(false)

  // A payload carrying another kind's fields is refused: the branch schema is closed.
  const mixed = { ...check, outcome_payload: { ...check.outcome_payload, allowed: ["resolved"] } }
  expect(validateAgentLanePacket(packet(mixed), failures)).toBe(false)

  // An exists-kind payload with a kind mismatching outcome_kind is refused.
  const mismatched = { ...check, outcome_kind: "exists", outcome_payload: { kind: "check", check_ref: "check:one", immutable_subject_ref: "contracts/x.json", expected_result: "pass" } }
  expect(validateAgentLanePacket(packet(mismatched), failures)).toBe(false)

  // An exists payload missing distinguish_from's sibling rule stays valid, while
  // an absent payload without distinguish_from is refused.
  const absentIncomplete = { ...check, outcome_kind: "absent", outcome_payload: { kind: "absent", surface: "repository", subjects: ["file/one"] } }
  expect(validateAgentLanePacket(packet(absentIncomplete), failures)).toBe(false)
  const absentComplete = { ...check, outcome_kind: "absent", outcome_payload: { kind: "absent", surface: "repository", subjects: ["file/one"], distinguish_from: ["renamed"] } }
  expect(validateAgentLanePacket(packet(absentComplete), failures)).toBe(true)

  // An unknown field on the predicate object is refused, and a ninth predicate
  // is refused.
  const unknownField = { ...check, extra: true }
  expect(validateAgentLanePacket(packet(unknownField), failures)).toBe(false)
  const nine = Array.from({ length: 9 }, (_, ordinal) => ({ ...check, predicate_id: `predicate:nine-${ordinal}`, ordinal }))
  expect(validateAgentLanePacket({ ...packet(check), inputs: { ...packet(check).inputs, outcome_predicates: nine } }, failures)).toBe(false)
})

test("a typed packet field is deterministic across builds and carries the design context", async () => {
  const script = { ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), DESIGN_RECORD) }
  const first = await build(script)
  const second = await build(script)
  expect(first.failure).toBeUndefined()
  expect(second).toEqual(first)
  expect(validateAgentLanePacket(first.packet!)).toBe(true)
  expect(typedPredicates(first.packet!)).toEqual(decodedContractPredicates())
  expect(first.packet!.inputs.context).toContain("The typed design record.")
  expect(first.packet!.inputs.context).toEndWith(NARRATIVE)
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

test("a work item with no workflow instance is a typed workflow-absent failure", async () => {
  const absent = coreEnvelope("concord_work_trace", "continuity", "C19.Continuity", "ok", {
    result: { work_id: WORK_ID, pinned: { product_identity: [PRODUCT_ID], workflow_instance: "absent", workflow_step: null, step_actions: [], contract: null, spec_mandate: [] } },
  })
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": absent })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("workflow_absent")
  expect(built.failure!.message).toContain("holds no workflow instance")
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

// The persisted work task is the operator's recorded instruction for the
// worker. The scope read carries it on the work summary, and the packet must
// project it: the read-only question prefers it over the bare title, and the
// context carries it ahead of the narrative so a contract-mandated worker
// receives the concrete instructions, not only the approved premise.
const PERSISTED_TASK = "Reproduce the refusal, extract the combinator loop, and keep the complexity budget green."

test("the context carries the persisted work task ahead of the narrative", async () => {
  const withTask = coreEnvelope("concord_work_browse", "scope", "PM1.Q6", "ok", {
    result: {
      work: { id: WORK_ID, kind: "task", title: "Project dispatch inputs from durable state", lifecycle: "in_progress", version: 1, priority: 0, project_ids: [PRODUCT_ID], ready: true, narrative: NARRATIVE, task: PERSISTED_TASK, terminal_at: null },
      memberships: [{ project_id: PRODUCT_ID, role: "primary" }],
      items: [],
    },
  })
  const built = await build({ ...defaultScript(), "concord_work_browse.scope": withTask })
  expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
  const packet = built.packet!
  const context = packet.inputs.context!
  expect(context).toContain(PERSISTED_TASK)
  expect(context.indexOf(PERSISTED_TASK)).toBeLessThan(context.indexOf(NARRATIVE))
})

test("a read-only lane prefers the persisted work task as the recorded question", async () => {
  const withTask = coreEnvelope("concord_work_browse", "scope", "PM1.Q6", "ok", {
    result: {
      work: { id: WORK_ID, kind: "task", title: "Project dispatch inputs from durable state", lifecycle: "in_progress", version: 1, priority: 0, project_ids: [PRODUCT_ID], ready: true, narrative: NARRATIVE, task: PERSISTED_TASK, terminal_at: null },
      memberships: [{ project_id: PRODUCT_ID, role: "primary" }],
      items: [],
    },
  })
  const built = await build(
    { "concord_work_browse.scope": withTask, "concord_work_trace.continuity": continuityEnvelope(null) },
    { laneId: "research", stepId: "investigate" },
  )
  expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
  const packet = built.packet!
  expect(packet.inputs.task).toContain(PERSISTED_TASK)
})

// The why rides ahead of the how: the item's recorded value statement renders
// as one line before the design record, and an item without a value statement
// omits the line, so older items stay legal.
const VALUE_STATEMENT = "A dispatched worker reads why the work matters before the how."

const scopeWithValue = (valueStatement?: string, narrative: string = NARRATIVE) =>
  coreEnvelope("concord_work_browse", "scope", "PM1.Q6", "ok", {
    result: {
      work: { id: WORK_ID, kind: "task", title: "Project dispatch inputs from durable state", lifecycle: "in_progress", version: 1, priority: 0, project_ids: [PRODUCT_ID], ready: true, narrative, ...(valueStatement === undefined ? {} : { value_statement: valueStatement }), terminal_at: null },
      memberships: [{ project_id: PRODUCT_ID, role: "primary" }],
      items: [],
    },
  })

test("the context carries the value line ahead of the design record", async () => {
  const built = await build({
    "concord_work_browse.scope": scopeWithValue(VALUE_STATEMENT),
    "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), DESIGN_RECORD),
  })
  expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
  const packet = built.packet!
  expect(validateAgentLanePacket(packet)).toBe(true)
  const context = packet.inputs.context!
  expect(context).toContain(`Value: ${VALUE_STATEMENT}`)
  expect(context.indexOf(`Value: ${VALUE_STATEMENT}`)).toBe(0)
  expect(context.indexOf("Approved design record:")).toBeGreaterThan(context.indexOf(`Value: ${VALUE_STATEMENT}`))
  expect(context.indexOf("The dispatched worker goal")).toBeGreaterThan(context.indexOf("Approved design record:"))
})

test("a value statement carrying embedded newlines renders as one guaranteed line", async () => {
  const multiLineValue = "why it matters\r\nsecond line of the why\nthird line"
  const built = await build({
    "concord_work_browse.scope": scopeWithValue(multiLineValue),
    "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), DESIGN_RECORD),
  })
  expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
  const context = built.packet!.inputs.context!
  const rendered = `Value: why it matters second line of the why third line`
  expect(context.indexOf(rendered)).toBe(0)
  expect(context.slice(0, context.indexOf("\n\n"))).toBe(rendered)
  expect(context.indexOf("Approved design record:")).toBeGreaterThan(context.indexOf(rendered))
})

test("a work item without a value statement omits the value line", async () => {
  const withoutValue = await build({ ...defaultScript(), "concord_work_browse.scope": scopeWithValue() })
  expect(withoutValue.failure).toBeUndefined()
  expect(withoutValue.packet!.inputs.context).not.toContain("Value:")
  expect(withoutValue.packet!.inputs.context).toBe(NARRATIVE)
  const blankValue = await build({ ...defaultScript(), "concord_work_browse.scope": scopeWithValue("   ") })
  expect(blankValue.failure).toBeUndefined()
  expect(blankValue.packet!.inputs.context).toBe(NARRATIVE)
})

test("a value statement counts inside the unchanged context overflow bound", async () => {
  const fatValue = "v".repeat(16_000)
  const valueLine = `Value: ${fatValue}\n\n`
  const narrative = "n".repeat(16_384 - valueLine.length + 1)
  const built = await build({
    "concord_work_browse.scope": scopeWithValue(fatValue, narrative),
    "concord_work_trace.continuity": continuityEnvelope(),
  })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("projection_overflow")
  expect(built.failure!.field).toBe("context")
  expect(built.failure!.limit).toBe(16_384)
})

// The synthetic full-contract fixture: one pinned contract carrying all eight
// predicate slots, plus the context block, a recorded correction, and the
// resolved law and Domains the contract binds. The packet must preserve all
// eight predicate objects in order with identical payloads, and carry the
// context, correction, and authority metadata beside them — nothing the
// binding restructure may drop or reorder.
const EIGHT_PAYLOADS = [
  { kind: "exists", surface: "repository", subjects: ["file/one", "file/two"] },
  { kind: "absent", surface: "repository", subjects: ["file/gone"], distinguish_from: ["renamed"] },
  { kind: "outcome", allowed: ["resolved", "remediated"] },
  { kind: "check", check_ref: "check:con795/eight-0", immutable_subject_ref: "contracts/agent-lane-packet.schema.json", expected_result: "pass" },
  { kind: "check", check_ref: "check:con795/eight-1", immutable_subject_ref: "adapter/opencode/packet.test.ts", expected_result: "pass" },
  { kind: "check", check_ref: "check:con795/eight-2", immutable_subject_ref: "contracts/agent-tool-surface.v1.json", expected_result: "pass" },
  { kind: "outcome", allowed: ["completed"], decision_record: { question: "q", options_considered: ["o"], decision: "accepted_decision", rationale: "r", consequences: ["c"], inputs: ["i"], poc_findings: "p", supersedes: null, superseded_by: null, unknowns: [], required_to_decide: [], reviewer_actor_ref: `actor:${"a".repeat(64)}`, operator_approval_ref: "approval:con795" } },
  { kind: "exists", surface: "repository", subjects: ["file/three"] },
]
const EIGHT_CONTRACT = {
  version: 3,
  premise: "The synthetic full-contract fixture preserves every predicate slot.",
  outcome_predicates: EIGHT_PAYLOADS.map((outcome_payload, ordinal) => ({
    predicate_id: `predicate:con795-eight-${ordinal}`,
    ordinal,
    outcome_kind: outcome_payload.kind,
    outcome_payload: JSON.stringify(outcome_payload),
  })),
  required_evidence: [],
  route_conventions: [],
  spec_mandate: [],
  changes_product_truth: false,
}
const EIGHT_CORRECTION: AgentLanePacketCorrection = {
  disposition: "verification",
  attempt_count: 2,
  attempt_limit: 3,
  escalated: false,
  diagnosis: "the first attempt skipped the astral premise control",
  strategy: "rerun with the full fixture",
  predicate_ids: ["predicate:con795-eight-3"],
  evidence_refs: ["evidence:con795"],
}

test("the synthetic full-contract fixture preserves all eight predicates, context, correction, and authority metadata", async () => {
  const continuity = continuityEnvelope(EIGHT_CONTRACT, DESIGN_RECORD, { correction: EIGHT_CORRECTION }, LAW_CONTEXT, PROPOSAL)
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuity })
  expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
  const packet = built.packet!
  expect(validateAgentLanePacket(packet)).toBe(true)
  const predicates = typedPredicates(packet) as Array<Record<string, unknown>>
  expect(predicates).toHaveLength(8)
  predicates.forEach((predicate, index) => {
    expect(predicate.predicate_id).toBe(`predicate:con795-eight-${index}`)
    expect(predicate.ordinal).toBe(index)
    expect(predicate.outcome_kind).toBe(EIGHT_PAYLOADS[index].kind)
    expect(predicate.outcome_payload).toEqual(EIGHT_PAYLOADS[index])
  })
  // Context, correction, and authority metadata ride beside the predicates.
  expect(packet.inputs.correction).toEqual(EIGHT_CORRECTION)
  const context = packet.inputs.context!
  expect(context.indexOf("Approved design record:")).toBe(0)
  expect(context).toContain("Approved law and Domains (binding Product law):")
  expect(context).toContain("Recorded proposal:")
  expect(context).toEndWith(NARRATIVE)
  // The task stays the premise verbatim; none of the eight predicates or the
  // correction spills into it.
  expect(packet.inputs.task).toBe(EIGHT_CONTRACT.premise)
  expect(packet.inputs.task).not.toContain("predicate:")
})

// Missing, unknown, or contradictory binding data fails closed at the packet
// boundary: the binding is required, its source is a closed enum, and each
// source demands its matching contract_version shape.
test("strict binding rejection", () => {
  const valid = {
    schema_version: "1.0", attempt_id: "attempt-1", lane_id: "implement", lane_version: 1,
    lane_digest: "sha256:f4dad03f0b94430af796eecc1c53740d6441286c78879b46bee17ec79ce604e5",
    work_id: WORK_ID, step_id: "step-1",
    inputs: { task: "t", binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "files_touched" } },
  }
  const failures: string[] = []
  expect(validateAgentLanePacket(valid, failures), failures.join("; ")).toBe(true)
  const without = (key: string) => {
    const value: Record<string, unknown> = { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "files_touched" }
    delete value[key]
    return { ...valid, inputs: { task: "t", binding: value } }
  }
  for (const key of ["objective_source", "work_version", "contract_version", "assigned_result"]) {
    expect(validateAgentLanePacket(without(key), failures), `missing ${key}`).toBe(false)
  }
  // Unknown objective source.
  expect(validateAgentLanePacket({ ...valid, inputs: { task: "t", binding: { objective_source: "coordinator_prompt", work_version: 1, contract_version: 1, assigned_result: "files_touched" } } }, failures)).toBe(false)
  // Contradictory: a contract premise without a contract version, and a
  // read-only question that names one.
  expect(validateAgentLanePacket({ ...valid, inputs: { task: "t", binding: { objective_source: "contract_premise", work_version: 1, contract_version: null, assigned_result: "files_touched" } } }, failures)).toBe(false)
  expect(validateAgentLanePacket({ ...valid, inputs: { task: "t", binding: { objective_source: "work_question", work_version: 1, contract_version: 2, assigned_result: "files_touched" } } }, failures)).toBe(false)
  // Undeclared binding field and an out-of-vocabulary assigned result.
  expect(validateAgentLanePacket({ ...valid, inputs: { task: "t", binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "files_touched", step: "repair" } } }, failures)).toBe(false)
  expect(validateAgentLanePacket({ ...valid, inputs: { task: "t", binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "Everything" } } }, failures)).toBe(false)
})

// Generated worker guidance teaches the canonical task, the binding
// authority, and the one-result scope, and states the approval premise limit
// in UTF-8 bytes apart from the packet's code-point field limits and the
// model token budget. It directs no truncation and no reapproval, and names
// no host Task prompt cap.
test("generated guidance teaches the canonical task, binding authority, and count units", async () => {
  const agent = (await Bun.file(new URL("../../.opencode/agents/concord-implement.md", import.meta.url)).text()).replace(/\s+/g, " ")
  expect(agent).toContain("## Objective and binding")
  expect(agent).toContain("`inputs.task` is the canonical objective, carried verbatim")
  expect(agent).toContain("The packet adds no header or trailer")
  expect(agent).toContain("`inputs.binding` is the typed authority")
  expect(agent).toContain("`objective_source`")
  expect(agent).toContain("`contract_version` is null before a contract is approved")
  expect(agent).toContain("Complete only that assigned result")
  expect(agent).toContain("at most 4096 UTF-8 bytes")
  expect(agent).toContain("count JSON Schema Unicode code points")
  expect(agent).toContain("`context_tokens_max` is a model token limit")
  expect(agent).toContain("do not truncate approved content")
  expect(agent).toContain("do not ask to reapprove unchanged scope")
  expect(agent).not.toMatch(/Task prompt (cap|limit)/i)
})

// CD-0205: a job-executing lane at a step that declares record_worker_job
// binds the one dispatch-ready worker-job revision verbatim; zero or several
// ready revisions refuse, and a step without the declaration binds no job.
const READY_JOB = {
  job_id: "job:projected-repair",
  revision: 2,
  digest: `sha256:${"a".repeat(64)}`,
  objective: "Apply the bounded repair.",
  stopping_condition: "The recorded checks pass.",
  project_scope: "project-1",
  path_scope: ["internal/store"],
  predicate_ids: [],
  checks: ["go test ./internal/store/"],
  prerequisites: [{ job_id: "job:earlier", revision: 1 }],
  unresolved_refs: [],
  reserved_integration: "",
}

const jobContinuity = (stepActions: string[], ready: unknown[]) => {
  const envelope = continuityEnvelope() as any
  envelope.result.pinned.step_actions = stepActions
  if (ready.length > 0) envelope.result.pinned.ready_worker_jobs = ready
  return envelope
}

test("a job-executing lane binds the one ready worker-job revision verbatim", async () => {
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": jobContinuity(["dispatch_worker", "record_worker_job"], [READY_JOB]) })
  expect(built.failure).toBeUndefined()
  expect(built.packet!.inputs.worker_job).toEqual(READY_JOB)
  expect(built.packet!.inputs.task).toBe(pinnedContract().premise)
})

// The closed packet schema admits the same predicate-id grammar the store,
// the report schema, and the tool surface admit: one or more characters
// after the "predicate:" prefix, not an eleven-character suffix minimum. A
// recorded short id such as "predicate:primary" must pass production packet
// validation, because the core records and returns it verbatim.
test("a ready worker job with a short predicate id passes the closed packet schema", async () => {
  const short = { ...READY_JOB, predicate_ids: ["predicate:primary", "predicate:con795-eight-3"] }
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": jobContinuity(["dispatch_worker", "record_worker_job"], [short]) })
  expect(built.failure).toBeUndefined()
  expect(validateAgentLanePacket(built.packet!)).toBe(true)
  expect(built.packet!.inputs.worker_job).toEqual(short)
})

test("a job-capable step refuses a dispatch without exactly one ready worker job", async () => {
  const none = await build({ ...defaultScript(), "concord_work_trace.continuity": jobContinuity(["dispatch_worker", "record_worker_job"], []) })
  expect(none.failure?.kind).toBe("worker_job_unavailable")
  const several = await build({ ...defaultScript(), "concord_work_trace.continuity": jobContinuity(["dispatch_worker", "record_worker_job"], [READY_JOB, { ...READY_JOB, job_id: "job:other", revision: 1 }]) })
  expect(several.failure?.kind).toBe("worker_job_ambiguous")
  expect(several.failure?.message).toContain("job:projected-repair@2")
  expect(several.failure?.message).toContain("job:other@1")
})

test("a step without record_worker_job binds no worker job", async () => {
  const legacy = await build({ ...defaultScript(), "concord_work_trace.continuity": jobContinuity(["dispatch_worker"], [READY_JOB]) })
  expect(legacy.failure).toBeUndefined()
  expect(legacy.packet!.inputs.worker_job).toBeUndefined()
})

test("verification and review bind explicit checks instead of treating the parent premise as their job", async () => {
  const job = { ...READY_JOB, objective: "Verify the bounded store change with the recorded test command.", stopping_condition: "Report each check's exit code without integrating or delivering." }
  for (const laneId of ["verify", "review"]) {
    const built = await build({ ...defaultScript(), "concord_work_trace.continuity": jobContinuity(["dispatch_worker", "record_worker_job"], [job]) }, { laneId })
    expect(built.failure).toBeUndefined()
    expect(built.packet!.inputs.worker_job).toEqual(job)
    expect(built.packet!.inputs.task).toBe(pinnedContract().premise)
  }
})

test("verification refuses a job without recorded executable checks", async () => {
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": jobContinuity(["dispatch_worker", "record_worker_job"], [{ ...READY_JOB, checks: [] }]) }, { laneId: "verify" })
  expect(built.failure?.kind).toBe("worker_job_unavailable")
})

test("the legacy packet schema forbids job fields while the current identity binds them", async () => {
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": jobContinuity(["dispatch_worker", "record_worker_job"], [READY_JOB]) })
  expect(validateAgentLanePacket(built.packet!)).toBe(true)
  expect(validateAgentLanePacket({ ...built.packet!, schema_version: "1.0" })).toBe(false)
})
