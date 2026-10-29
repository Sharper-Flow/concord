import { test, expect, mock } from "bun:test"
import { createHash } from "node:crypto"
import { manifestDigest } from "./generated-contracts"
import { validateGeneratedEnvelope, validateGeneratedPayload } from "./generated-contract-tests"
import { configureCoreBinary, validateAgentLanePacket, type AgentLanePacketBinding, type AgentLanePacketCorrection, type AgentLanePacketLawContext, type AgentLanePacketWorkerJob, type AgentLanePacketWorkContext, type AgentLanePacketWorkContextResearchEntryResolved, type AgentLanePacketWorkContextResearchEntryUnresolved, type AgentLanePacketWorkContextResearchFindingView } from "./dispatch"
import { agentLaneReportSchema, agentLanes, workerScopeAssignedResult } from "./generated-agent-lanes"
import { fakeTool } from "./plugin-tool.fixture"
import { reviewBlockFixture } from "./review-test-fixture"

// The builder reaches core through the adapter transport in concord.ts, which
// imports the host plugin surface. With the shared stub the
// suite never loads the real host module.
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

const continuityEnvelope = (contract: unknown = pinnedContract(), designRecord: unknown = null, workPin: unknown = null, lawContext: unknown = null, proposalRecord: unknown = null, latestCheckpoint: unknown = null) => coreEnvelope("concord_work_trace", "continuity", "C19.Continuity", "ok", {
  result: {
    work_id: WORK_ID,
    pinned: {
      product_identity: [PRODUCT_ID],
      workflow_step: WORKFLOW_STEP,
      step_actions: [],
      contract,
      spec_mandate: [],
      pending_operator_decision: null,
      latest_checkpoint: latestCheckpoint,
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

test("new packets pin the worker report protocol and the current card-free revision", async () => {
  const result = await build(defaultScript())
  expect(result.packet).toBeDefined()
  expect(result.packet?.schema_version).toBe("2.0")
  expect(result.packet?.inputs.report_protocol).toBe("concord-worker-result-v1")
})

test("review input is copied verbatim from continuity only onto the review lane", async () => {
  const descriptor = { input_candidate: { project_id: "project-1", commit: "a".repeat(40), tree: "b".repeat(40) }, dependencies: { criteria: [{ predicate_id: "predicate:primary", content_digest: "sha256:" + "c".repeat(64) }], architecture_not_applicable: true } }
  const continuity = continuityEnvelope() as any
  continuity.result.pinned.review_input = descriptor
  for (const laneId of ["review", "implement"]) {
    const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuity }, { laneId })
    expect(built.failure).toBeUndefined()
    expect((built.packet!.inputs as any).review_input).toEqual(laneId === "review" ? descriptor : undefined)
  }
})

test("review packets preserve the recorded size and its provenance only on Review", async () => {
  for (const size of [
    { size: "small", model_size: "small", model_version: 2, recorded_at: "2026-10-28T00:00:00Z" },
    { size: "larger", model_size: "larger", model_version: 2, recorded_at: "2026-10-28T00:00:00Z" },
    { size: "crucial", model_size: "crucial", model_version: 2, recorded_at: "2026-10-28T00:00:00Z" },
    { size: "crucial", model_size: "small", operator_size: "crucial", model_version: 2, recorded_at: "2026-10-28T00:00:00Z" },
  ] as const) {
    const descriptor = { input_candidate: { project_id: "project-1", commit: "a".repeat(40), tree: "b".repeat(40) }, dependencies: { criteria: [{ predicate_id: "predicate:primary", content_digest: "sha256:" + "c".repeat(64) }], architecture_not_applicable: true }, size }
    const continuity = continuityEnvelope() as any
    continuity.result.pinned.review_input = descriptor
    for (const laneId of ["review", "implement"]) {
      const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuity }, { laneId })
      expect(built.failure).toBeUndefined()
      expect((built.packet!.inputs as any).review_input).toEqual(laneId === "review" ? descriptor : undefined)
      expect(built.packet!.inputs.review_input?.size).toEqual(laneId === "review" ? size : undefined)
    }
  }
})

test("review packets preserve the producer's retained failed-edit candidate and complete delta paths", async () => {
  const descriptor = {
    input_candidate: { project_id: "project-1", commit: "a".repeat(40), tree: "b".repeat(40) },
    dependencies: { criteria: [{ predicate_id: "predicate:primary", content_digest: "sha256:" + "c".repeat(64) }], architecture_not_applicable: true },
    retained_failed_edit: {
      accepted_candidate: { project_id: "project-1", commit: "d".repeat(40), tree: "e".repeat(40) },
      changed_paths: Array.from({ length: 80 }, (_, index) => `src/${"segment/".repeat(70)}file-${index}.ts`),
    },
  }
  const continuity = continuityEnvelope() as any
  continuity.result.pinned.review_input = descriptor
  for (const laneId of ["review", "implement"]) {
    const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuity }, { laneId })
    expect(built.failure).toBeUndefined()
    expect(built.packet!.inputs.review_input).toEqual(laneId === "review" ? descriptor : undefined)
  }
})

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
  expect(packet.schema_version).toBe("2.0")
  expect(packet.attempt_id).toBe("attempt-1")
  expect(packet.work_id).toBe(WORK_ID)
  expect(packet.step_id).toBe("step-1")
  expect(packet.lane_id).toBe("implement")
  expect(packet).not.toHaveProperty("lane_version")
  expect(packet.lane_digest).toBe(agentLanes.find((lane) => lane.id === "implement")!.digest)
  expect(packet.inputs.task).toBe("Dispatch inputs are retyped rather than projected.")
  expect(packet.inputs.task).not.toContain(OUTCOME_KIND)
  expect(packet.inputs.task).not.toContain(OUTCOME_PAYLOAD)
  expect(packet.inputs.binding).toEqual({ objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "files_touched" })
  expect(typedPredicates(packet)).toEqual(decodedContractPredicates())
  expect(packet.inputs.work_record).toEqual({ narrative: NARRATIVE })
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
    diagnosis: "worker attempt failed",
    strategy: "retry with a fresh fenced attempt",
    failure_kind: "transport_failure",
    failure_detail: "the worker could not reach the service",
    predicate_ids: [],
    evidence_refs: [],
    failed_attempt_id: "attempt-7",
    failed_attempt_epoch: 2,
    source_event_id: "record_worker_failure-work-1-12:semantic",
    source_event_seq: 12,
  }
  const built = await build({
    ...defaultScript(),
    "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), null, { correction }),
  })
  expect(built.failure).toBeUndefined()
  expect(built.packet!.inputs.correction).toEqual(correction)
  expect(built.packet!.inputs.work_record).toEqual({ narrative: NARRATIVE })
})

// The checkpoint failed-review return (amended CD-0143 D1) projects the same
// bounded context: the failure disposition stands beside the affected
// predicates and bound evidence the operator approved, and the narrative
// context stays separate from it.
test("a checkpoint failed-review correction projects its bounded verdict evidence into the packet", async () => {
  const correction: AgentLanePacketCorrection = {
    disposition: "failed",
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
  expect(built.packet!.inputs.work_record).toEqual({ narrative: NARRATIVE })
})

test("a rejected correction projects without retry budget authority", async () => {
  const correction: AgentLanePacketCorrection = {
    disposition: "rejected",
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
  expect(built.packet!.inputs.correction).not.toHaveProperty("attempt_count")
  expect(built.packet!.inputs.correction).not.toHaveProperty("attempt_limit")
  expect(built.packet!.inputs.correction).not.toHaveProperty("escalated")
})

// The pin's work-context view rides the packet verbatim. The core
// refuses a dispatch whose inputs.work_context differs from the current view
// byte-for-byte, so any re-derivation here would strand the dispatch.
const WORK_CONTEXT: AgentLanePacketWorkContext = {
  source_event_frontier: 19,
  required_reading: [
    {
      domain_id: "root",
      reason: "the reading carries the bounded reason",
      product_wide_rationale: "the claim spans every Domain of the Product",
      source: { kind: "repository_file", project_id: "project", path: "internal/store/work_context.go", commit_oid: "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1" },
    },
    {
      domain_id: "child-alpha",
      reason: "the law reading carries the bounded reason",
      source: { kind: "knowledge", source_id: "concord_knowledge", law_id: "CD-0001", content_hash: "sha256:" + "e".repeat(64) },
    },
  ],
  findings: [
    {
      finding_id: "finding:19:0",
      kind: "rejected_approach",
      statement: "the unbounded log scan was rejected",
      subject_ref: "internal/store/work_context.go",
      evidence_refs: ["internal/store/work_context_test.go"],
      domain_id: "child-alpha",
      origin: "declaration",
      status: "reported",
      source_event_id: "record_work_context-work-335-10:semantic",
      source_event_seq: 19,
      ordinal: 0,
    },
  ],
  domain_groups: [
    { domain_id: "child-alpha", required_reading_ordinals: [1], finding_ids: ["finding:19:0"] },
    { domain_id: "root", required_reading_ordinals: [0], finding_ids: [] },
  ],
}

test("a work context projects verbatim into the packet", async () => {
  const built = await build({
    ...defaultScript(),
    "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), null, { work_context: WORK_CONTEXT }),
  })
  expect(built.failure).toBeUndefined()
  const packet = built.packet!
  expect(packet.inputs.work_context).toEqual(WORK_CONTEXT)
  expect(validateAgentLanePacket(packet)).toBe(true)
})

// The core records a work-context unavailability marker in place of the view,
// never beside a work_context member, at either carrier the builder reads: the
// pinned snapshot or the embedded work pin. A packet projected past the marker
// would dispatch without the view the core's byte-for-byte comparison demands,
// so the builder must refuse before it emits one.
const WORK_CONTEXT_UNAVAILABLE = { reason: "limit_exceeded" }
const withSnapshotMarker = (envelope: any) => {
  envelope.result.pinned.work_context_unavailable = WORK_CONTEXT_UNAVAILABLE
  return envelope
}

test.each([
  ["embedded pin marker only", () => continuityEnvelope(pinnedContract(), null, { work_context_unavailable: WORK_CONTEXT_UNAVAILABLE })],
  ["snapshot marker, no pin", () => withSnapshotMarker(continuityEnvelope())],
  ["snapshot marker beside partial pin {}", () => withSnapshotMarker(continuityEnvelope(pinnedContract(), null, {}))],
  ["both marker locations", () => withSnapshotMarker(continuityEnvelope(pinnedContract(), null, { work_context_unavailable: WORK_CONTEXT_UNAVAILABLE }))],
] as [string, () => Record<string, unknown>][])("an unavailable work context marker (%s) refuses the packet", async (_label, continuity) => {
  const invoke = scriptedInvoke({
    ...defaultScript(),
    "concord_work_trace.continuity": continuity(),
  })
  const result = await buildAgentLanePacket(
    { workId: WORK_ID, productId: PRODUCT_ID, laneId: "implement", attemptId: "attempt-1", stepId: "step-1" } as any,
    { context: contextFor(), invoke: invoke as any },
  )
  expect(result.failure?.kind).toBe("packet_refused")
  expect(result.failure?.message).toContain("work_context_unavailable")
  expect(result.failure?.message).toContain("limit_exceeded")
  expect(result.packet).toBeUndefined()
  expect(invoke.seen()).toEqual(["concord_work_browse.scope", "concord_work_trace.continuity"])
})

// The selected research entries ride the same verbatim copy. The adapter
// copies the work pin's view; no parallel research fetch, special packet
// input, or adapter selection policy exists, and the generated packet schema
// admits the research member and the research view ids in the group id
// namespace.
const RESEARCH_FINDING: AgentLanePacketWorkContextResearchEntryResolved = {
  selection: { pack_id: "pack-packet-copy", revision: 1, finding_id: "f-packet-copy", domain_id: "child-alpha" },
  view_id: "research:19:0",
  selection_event_id: "record_work_context-work-335-11:semantic",
  selection_event_seq: 19,
  ordinal: 0,
  required: true,
  state: "resolved",
  finding: {
    pack_id: "pack-packet-copy",
    revision: 1,
    finding_id: "f-packet-copy",
    kind: "observation",
    statement: "the selected research finding rides the shared view",
    confidence: "high",
    freshness: "current",
    status: "active",
    source_ids: ["src-packet-copy"],
    scopes: { mode: "home" },
  },
  sources: [
    {
      pack_id: "pack-packet-copy",
      revision: 1,
      source_id: "src-packet-copy",
      kind: "source_code",
      locator: "internal/store/work_context.go",
      title: "the selected source",
      publisher_or_author: "concord",
      accessed_at: "2026-01-01T00:00:00Z",
    },
  ],
}
const RESEARCH_CONTEXT = {
  ...WORK_CONTEXT,
  research_findings: [RESEARCH_FINDING],
  domain_groups: [
    { domain_id: "child-alpha", required_reading_ordinals: [1], finding_ids: ["finding:19:0", "research:19:0"] },
    { domain_id: "root", required_reading_ordinals: [0], finding_ids: [] },
  ],
}

test("selected research projects verbatim into the packet through the work pin copy", async () => {
  const built = await build({
    ...defaultScript(),
    "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), null, { work_context: RESEARCH_CONTEXT }),
  })
  expect(built.failure).toBeUndefined()
  const packet = built.packet!
  expect(packet.inputs.work_context).toEqual(RESEARCH_CONTEXT)
  expect(packet.inputs.work_context!.research_findings).toEqual([RESEARCH_FINDING])
  expect(validateAgentLanePacket(packet)).toBe(true)
})

test("closed research entry branches decide the packet verdict", async () => {
  const built = await build(defaultScript())
  const base = built.packet!
  const withResearch = (entry: AgentLanePacketWorkContextResearchFindingView) =>
    validateAgentLanePacket({ ...base, inputs: { ...base.inputs, work_context: { ...RESEARCH_CONTEXT, research_findings: [entry] } } })
  // A typed unresolved entry carries references and provenance only; the
  // resolved entry carries the native finding and its linked sources; a
  // resolved-state entry without its body refuses, and an unresolved-state
  // entry carrying a body refuses. The body members are omitted from the
  // object, not set to undefined: absent and undefined are the same absence
  // on the wire. The two refusing packets are deliberately invalid, so only
  // they carry the test-only cast past the mirror's closed branches.
  const { finding: _finding, sources: _sources, ...provenanceOnly } = RESEARCH_FINDING
  const unresolved: AgentLanePacketWorkContextResearchEntryUnresolved = { ...provenanceOnly, state: "missing" }
  const withoutBody = { ...provenanceOnly } as any
  expect(withResearch(unresolved)).toBe(true)
  expect(withResearch(RESEARCH_FINDING)).toBe(true)
  expect(withResearch(withoutBody)).toBe(false)
  expect(withResearch({ ...unresolved, finding: RESEARCH_FINDING.finding, sources: RESEARCH_FINDING.sources } as any)).toBe(false)
})

test("a card-bearing work context refuses the current card-free packet", async () => {
  // Domain cards are retired. The released 1.0/1.1 branches of the
  // closed packet schema keep their card-bearing shapes for replay, and the
  // current 2.0 branch refuses the member whole — cards derived by any
  // reader never enter a packet, with or without inlined content.
  const source = { kind: "repository_file", project_id: "project", path: ".concord/navigation/domains/child-alpha.md", commit_oid: "a1".repeat(20) } as const
  const cardBearing: AgentLanePacketWorkContext = {
    ...WORK_CONTEXT,
    required_reading: [...WORK_CONTEXT.required_reading, { domain_id: "child-alpha", reason: "Read the pinned Domain card.", source }],
    domain_groups: WORK_CONTEXT.domain_groups.map(group => group.domain_id === "child-alpha"
      ? { ...group, required_reading_ordinals: [1, 2], domain_cards: [source] }
      : group),
  }
  const withCards = await build({
    ...defaultScript(),
    "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), null, { work_context: cardBearing }),
  })
  expect(withCards.failure?.kind).toBe("packet_refused")
  const inlined = { ...cardBearing, domain_groups: [{ ...cardBearing.domain_groups[0], domain_cards: [{ ...source, content: "must not enter packet" }] }] }
  const refused = await build({
    ...defaultScript(),
    "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), null, { work_context: inlined }),
  })
  expect(refused.failure?.kind).toBe("packet_refused")
})

test("retained card-bearing 1.1 packets still validate through their recorded branch", async () => {
  // The historical reader stays available for replay and retained-result
  // recovery: a released card-bearing packet validates under its recorded
  // identity while the same member refuses at the current 2.0 branch.
  const built = await build(defaultScript())
  const base = built.packet!
  const source = { kind: "repository_file", project_id: "project", path: ".concord/navigation/domains/child-alpha.md", commit_oid: "a1".repeat(20) }
  const retained = {
    ...base,
    schema_version: "1.1" as const,
    inputs: { ...base.inputs, work_context: { ...WORK_CONTEXT, domain_groups: [{ ...WORK_CONTEXT.domain_groups[0], domain_cards: [source] }] } },
  }
  expect(validateAgentLanePacket(retained)).toBe(true)
  expect(validateAgentLanePacket({ ...retained, schema_version: "2.0" as const })).toBe(false)
})
test("released 1.0 branch keeps its released semantic fields, not merely its enum entry", async () => {
  // The historical reader is the schema document's version branches, not the
  // current merged enum: a retained 1.0 packet keeps the released prohibition
  // (inputs.worker_job refused) and the released card-bearing shape, so the
  // branch behavior is the original artifact and the 2.0 entry is a widening.
  const built = await build(defaultScript())
  const base = built.packet!
  const jobbed = {
    ...base,
    schema_version: "1.0" as const,
    inputs: { ...base.inputs, worker_job: { job_id: "job:retained", revision: 1, digest: "sha256:" + "1".repeat(64), objective: "o", stopping_condition: "s", project_scope: "project", path_scope: [], predicate_ids: [], checks: [], prerequisites: [], unresolved_refs: [], reserved_integration: "" } },
  }
  expect(validateAgentLanePacket(jobbed)).toBe(false)
  const joblessInputs = { ...jobbed.inputs }
  delete (joblessInputs as Record<string, unknown>).worker_job
  const jobless = { ...jobbed, inputs: joblessInputs }
  expect(Object.hasOwn(jobless.inputs, "worker_job")).toBe(false)
  expect(validateAgentLanePacket(jobless)).toBe(true)
})

test("a pin without a work context adds no packet member", async () => {
  const built = await build(defaultScript())
  expect(built.failure).toBeUndefined()
  expect("work_context" in built.packet!.inputs).toBe(false)
  expect(validateAgentLanePacket(built.packet!)).toBe(true)
})

test("a malformed or over-bound work context refuses the packet", async () => {
  const malformed = { ...WORK_CONTEXT, findings: [{ ...WORK_CONTEXT.findings[0], status: "accepted" }] }
  const overBound = { ...WORK_CONTEXT, required_reading: Array.from({ length: 33 }, () => WORK_CONTEXT.required_reading[0]) }
  for (const work_context of [malformed, overBound]) {
    const built = await build({
      ...defaultScript(),
      "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), null, { work_context }),
    })
    expect(built.failure).toBeDefined()
    expect(built.failure!.kind).toBe("packet_refused")
    expect(validateAgentLanePacket({ ...built.packet, inputs: { ...built.packet?.inputs, work_context } } as any)).toBe(false)
  }
})

// CON-883: the pinned projection's latest context checkpoint rides the
// packet verbatim. The core refuses a dispatch whose inputs.checkpoint
// differs from the latest checkpoint byte-for-byte, so any re-derivation
// here would strand the dispatch.
const CHECKPOINT = {
  checkpoint_id: "checkpoint-context-work-6:context-checkpoint",
  work_version: 6,
  sequence: 2,
  step_id: "repair",
  attempt_epoch: 1,
  active_unit: "the bounded unit",
  hypothesis: "the bounded hypothesis",
  diagnosis: "the bounded diagnosis",
  strategy: "follow the recorded strategy",
  touched_refs: ["internal/store/work_context.go"],
  evidence_refs: ["evidence:checkpoint"],
  pending_questions: [],
  pending_decisions: [],
}

test("a latest checkpoint projects verbatim into the packet", async () => {
  const built = await build({
    ...defaultScript(),
    "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), null, null, null, null, CHECKPOINT),
  })
  expect(built.failure).toBeUndefined()
  const packet = built.packet!
  expect(packet.inputs.checkpoint).toEqual(CHECKPOINT)
  expect(validateAgentLanePacket(packet)).toBe(true)
})

test("a pinned projection without a checkpoint adds no packet member", async () => {
  const built = await build(defaultScript())
  expect(built.failure).toBeUndefined()
  expect("checkpoint" in built.packet!.inputs).toBe(false)
  expect(validateAgentLanePacket(built.packet!)).toBe(true)
})

test("a malformed checkpoint refuses the packet", async () => {
  const malformed = { ...CHECKPOINT, strategy: "x" }
  const unknownMember = { ...CHECKPOINT, extra: true }
  for (const checkpoint of [malformed, unknownMember]) {
    const built = await build({
      ...defaultScript(),
      "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), null, null, null, null, checkpoint),
    })
    expect(built.failure).toBeDefined()
    expect(built.failure!.kind).toBe("packet_refused")
    expect(validateAgentLanePacket({ ...built.packet, inputs: { ...built.packet?.inputs, checkpoint } } as any)).toBe(false)
  }
})

test("installed agents project worker-authored report bounds and leave model identity to the adapter", async () => {
  const built = await build(defaultScript())
  expect(built.failure).toBeUndefined()
  const agent = await Bun.file(new URL("../../.opencode/agents/concord-implement.md", import.meta.url)).text()
  const reportProperties = agentLaneReportSchema.properties
  const reportEntry = agentLaneReportSchema.$defs.evidence_entry
  expect(agent).toContain(`additionalProperties=${agentLaneReportSchema.additionalProperties}`)
  expect(agent).toContain(`maxItems=${reportProperties.evidence.maxItems}`)
  expect(agent).toContain(`maxLength=${reportEntry.properties.detail.maxLength}`)
  expect(agent).toContain("The adapter supplies report identity from the dispatch packet and host readback.")
  expect(agent).not.toContain("readback_model:")
  expect(agentLaneReportSchema.required).toContain("readback_model")
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
  expect(packet.inputs.work_record).toEqual({ narrative: NARRATIVE })
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
  expect(built.packet!.inputs.work_record).toEqual({ task: question, narrative: NARRATIVE })
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

test("the packet carries the pinned design record verbatim", async () => {
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), DESIGN_RECORD) })
  expect(built.failure).toBeUndefined()
  expect(validateAgentLanePacket(built.packet!)).toBe(true)
  expect(built.packet!.inputs.design_record).toEqual(DESIGN_RECORD)
  expect(built.packet!.inputs.work_record).toEqual({ narrative: NARRATIVE })
})

// The core resolves the approved contract's bound law and Domains at
// continuity read time; the builder carries that law context and the
// recorded proposal verbatim as typed packet members.
const LAW_CONTEXT: AgentLanePacketLawContext = {
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

test("the packet carries the resolved law context and proposal verbatim", async () => {
  const continuity = continuityEnvelope(pinnedContract(), DESIGN_RECORD, null, LAW_CONTEXT, PROPOSAL)
  expect(validateGeneratedEnvelope(continuity)).toBe(true)
  expect(validateGeneratedPayload("continuity_snapshot", (continuity as any).result)).toBe(true)
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuity })
  expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
  expect(validateAgentLanePacket(built.packet!)).toBe(true)
  expect(built.packet!.inputs.law_context).toEqual(LAW_CONTEXT)
  expect(built.packet!.inputs.proposal_record).toEqual(PROPOSAL)
  expect(built.packet!.inputs.design_record).toEqual(DESIGN_RECORD)
  expect(built.packet!.inputs.work_record).toEqual({ narrative: NARRATIVE })
})

// A retained packet from an attempt dispatched before the typed record
// members carried prose in inputs.context. Recovery re-validates that exact
// packet, so the lane-packet schema still admits the member, while the
// builder never emits it.
test("a retained pre-record packet with prose context still validates, and the builder never emits it", async () => {
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), DESIGN_RECORD, null, LAW_CONTEXT, PROPOSAL) })
  expect(built.failure).toBeUndefined()
  expect("context" in built.packet!.inputs).toBe(false)
  const { law_context: _law, design_record: _design, proposal_record: _proposal, work_record: _work, ...legacyInputs } = built.packet!.inputs
  const failures: string[] = []
  expect(validateAgentLanePacket({ ...built.packet!, inputs: { ...legacyInputs, context: `Approved design record:\n${NARRATIVE}` } }, failures), failures.join("; ")).toBe(true)
})

test("a contract with no bound law dispatches without a law context or proposal", async () => {
  const built = await build(defaultScript())
  expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
  expect(built.packet!.inputs.law_context).toBeUndefined()
  expect(built.packet!.inputs.proposal_record).toBeUndefined()
  expect(built.packet!.inputs.design_record).toBeUndefined()
})

// The core resolves the mandated spec's criteria bound to this work item's
// predicates (CD-0180) into the law entry's criteria field; the packet carries
// them typed on the law entry so the worker sees which criterion each of this
// item's predicates discharges.
test("the law context carries the mandated criteria bound to this work item's predicates", async () => {
  const lawContext: AgentLanePacketLawContext = {
    laws: [
      { roles: ["mandated"], law_id: "spec:one", kind: "spec", status: "accepted", title: "Synthetic test law", path: ".concord/docs/spec.md", criteria: [{ criterion: 2, predicate_id: "predicate:criterion-bindings-predicate-form" }, { criterion: 1, predicate_id: "predicate:packet-mandated-criteria" }] },
      { roles: ["mandated"], law_id: "spec:plain", kind: "spec", status: "accepted", title: "Unbound spec", path: ".concord/docs/plain.md" },
    ],
    domains: [],
  }
  const continuity = continuityEnvelope(pinnedContract(), null, null, lawContext)
  expect(validateGeneratedPayload("continuity_snapshot", (continuity as any).result)).toBe(true)
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuity })
  expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
  expect(validateAgentLanePacket(built.packet!)).toBe(true)
  expect(built.packet!.inputs.law_context).toEqual(lawContext)
})

test("a law context past the packet bounds is refused, not truncated", async () => {
  const oversized = { laws: Array.from({ length: 129 }, (_, index) => ({ roles: ["mandated"], law_id: `spec:big-${index}` })), domains: [] }
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), null, null, oversized) })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("packet_refused")
  expect(built.failure!.message).toContain("law_context")
})

test("the packet carries bounded correction data outside the work record", async () => {
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
      predicate_ids: ["predicate:primary"],
      evidence_refs: ["evidence:review"],
      diagnosis: "the result misses the boundary case",
      strategy: "change the helper and add a test",
    },
  }
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuity })
  expect(built.failure).toBeUndefined()
  expect(built.packet!.inputs.correction).toEqual(pinned.work_pin.correction)
  expect(built.packet!.inputs.work_record).toEqual({ narrative: NARRATIVE })
})

// #903: non-Initiative work items carry no narrative, and a missing
// narrative must not erase the objective from the packet.
test("a non-Initiative work item with no narrative still carries the approved objective", async () => {
  const built = await build({ ...defaultScript(), "concord_work_browse.scope": scopeEnvelope("") })
  expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
  const packet = built.packet!
  expect(validateAgentLanePacket(packet)).toBe(true)
  expect(packet.inputs.work_record).toBeUndefined()
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
    expect(packet.lane_digest).toBe(lane.digest)
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

// The lane contract owns what inputs.law_context means and what the report
// must disclose, so every generated lane definition carries the conformance
// rule. Only a lane granted edit_scoped_files is told to change
// files or law documents.
test("every installed lane definition carries the law conformance rule", async () => {
  for (const lane of agentLanes) {
    const agent = (await Bun.file(`${import.meta.dir}/../../.opencode/agents/concord-${lane.id}.md`).text()).replace(/\s+/g, " ")
    expect(agent, `${lane.id} omitted the law conformance rule`).toContain("## Approved law and Domains")
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

test("a narrative past the work record bound is refused, not truncated", async () => {
  const built = await build({ ...defaultScript(), "concord_work_browse.scope": scopeEnvelope("n".repeat(16_385)) })
  expect(built.packet).toBeUndefined()
  expect(built.failure!.kind).toBe("packet_refused")
  expect(built.failure!.message).toContain("narrative")
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
    schema_version: "1.0", attempt_id: "attempt-1", lane_id: "implement",
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
    schema_version: "1.0", attempt_id: "attempt-1", lane_id: "implement",
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

test("a typed packet field is deterministic across builds and carries the design record", async () => {
  const script = { ...defaultScript(), "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), DESIGN_RECORD) }
  const first = await build(script)
  const second = await build(script)
  expect(first.failure).toBeUndefined()
  expect(second).toEqual(first)
  expect(validateAgentLanePacket(first.packet!)).toBe(true)
  expect(typedPredicates(first.packet!)).toEqual(decodedContractPredicates())
  expect(first.packet!.inputs.design_record).toEqual(DESIGN_RECORD)
})

test("a narrative at the work record bound still fits", async () => {
  const narrative = "n".repeat(16_384)
  const built = await build({ ...defaultScript(), "concord_work_browse.scope": scopeEnvelope(narrative) })
  expect(built.failure).toBeUndefined()
  expect(validateAgentLanePacket(built.packet!)).toBe(true)
  expect(built.packet!.inputs.work_record!.narrative!.length).toBe(16_384)
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
// work record carries it beside the narrative so a contract-mandated worker
// receives the concrete instructions, not only the approved premise.
const PERSISTED_TASK = "Reproduce the refusal, extract the combinator loop, and keep the complexity budget green."

test("the work record carries the persisted work task beside the narrative", async () => {
  const withTask = coreEnvelope("concord_work_browse", "scope", "PM1.Q6", "ok", {
    result: {
      work: { id: WORK_ID, kind: "task", title: "Project dispatch inputs from durable state", lifecycle: "in_progress", version: 1, priority: 0, project_ids: [PRODUCT_ID], ready: true, narrative: NARRATIVE, task: PERSISTED_TASK, terminal_at: null },
      memberships: [{ project_id: PRODUCT_ID, role: "primary" }],
      items: [],
    },
  })
  const built = await build({ ...defaultScript(), "concord_work_browse.scope": withTask })
  expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
  expect(built.packet!.inputs.work_record).toEqual({ task: PERSISTED_TASK, narrative: NARRATIVE })
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

// The item's recorded value statement rides the work record verbatim, and an
// item without one omits the member, so older items stay legal.
const VALUE_STATEMENT = "A dispatched worker reads why the work matters before the how."

const scopeWithValue = (valueStatement?: string, narrative: string = NARRATIVE) =>
  coreEnvelope("concord_work_browse", "scope", "PM1.Q6", "ok", {
    result: {
      work: { id: WORK_ID, kind: "task", title: "Project dispatch inputs from durable state", lifecycle: "in_progress", version: 1, priority: 0, project_ids: [PRODUCT_ID], ready: true, narrative, ...(valueStatement === undefined ? {} : { value_statement: valueStatement }), terminal_at: null },
      memberships: [{ project_id: PRODUCT_ID, role: "primary" }],
      items: [],
    },
  })

test("the work record carries the value statement verbatim", async () => {
  const multiLineValue = "why it matters\r\nsecond line of the why"
  for (const value of [VALUE_STATEMENT, multiLineValue, "  padded why  "]) {
    const built = await build({
      "concord_work_browse.scope": scopeWithValue(value),
      "concord_work_trace.continuity": continuityEnvelope(pinnedContract(), DESIGN_RECORD),
    })
    expect(built.failure, JSON.stringify(built.failure)).toBeUndefined()
    expect(validateAgentLanePacket(built.packet!)).toBe(true)
    expect(built.packet!.inputs.work_record).toEqual({ value_statement: value, narrative: NARRATIVE })
  }
})

test("a work item without a value statement omits the member", async () => {
  const withoutValue = await build({ ...defaultScript(), "concord_work_browse.scope": scopeWithValue() })
  expect(withoutValue.failure).toBeUndefined()
  expect(withoutValue.packet!.inputs.work_record).toEqual({ narrative: NARRATIVE })
})

// The synthetic full-contract fixture: one pinned contract carrying all eight
// predicate slots, plus the design record, a recorded correction, and the
// resolved law and Domains the contract binds. The packet must preserve all
// eight predicate objects in order with identical payloads, and carry the
// records, correction, and authority metadata beside them — nothing the
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
  diagnosis: "the first attempt skipped the astral premise control",
  strategy: "rerun with the full fixture",
  predicate_ids: ["predicate:con795-eight-3"],
  evidence_refs: ["evidence:con795"],
}

test("the synthetic full-contract fixture preserves all eight predicates, records, correction, and authority metadata", async () => {
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
  // Records, correction, and authority metadata ride beside the predicates.
  expect(packet.inputs.correction).toEqual(EIGHT_CORRECTION)
  expect(packet.inputs.design_record).toEqual(DESIGN_RECORD)
  expect(packet.inputs.law_context).toEqual(LAW_CONTEXT)
  expect(packet.inputs.proposal_record).toEqual(PROPOSAL)
  expect(packet.inputs.work_record).toEqual({ narrative: NARRATIVE })
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
    schema_version: "1.0", attempt_id: "attempt-1", lane_id: "implement",
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
// authority, and the one-result scope, and names no host Task prompt cap.
test("generated guidance teaches the canonical task and binding authority", async () => {
  const agent = (await Bun.file(new URL("../../.opencode/agents/concord-implement.md", import.meta.url)).text()).replace(/\s+/g, " ")
  expect(agent).toContain("## Objective and binding")
  expect(agent).toContain("`inputs.task` is the canonical objective, carried verbatim")
  expect(agent).toContain("The packet adds no header or trailer")
  expect(agent).toContain("`inputs.binding` is the typed authority")
  expect(agent).toContain("`objective_source`")
  expect(agent).toContain("`contract_version` is null before a contract is approved")
  expect(agent).toContain("Complete only that assigned result")
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
  const review = reviewBlockFixture()
  envelope.result.pinned.review_input = { input_candidate: review.input_candidate, dependencies: review.dependencies }
  envelope.result.pinned.step_actions = stepActions
  if (ready.length > 0) envelope.result.pinned.ready_worker_jobs = ready
  return envelope
}

test("a bound review cannot dispatch without its current input descriptor", async () => {
  const continuity = jobContinuity(["dispatch_worker", "record_worker_job"], [READY_JOB])
  delete continuity.result.pinned.review_input
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuity }, { laneId: "review" })
  expect(built.failure?.kind).toBe("packet_refused")
  expect(built.failure?.message).toContain("review input candidate and dependencies")
})

test("a job-executing lane binds the one ready worker-job revision verbatim", async () => {
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": jobContinuity(["dispatch_worker", "record_worker_job"], [READY_JOB]) })
  expect(built.failure).toBeUndefined()
  expect(built.packet!.inputs.worker_job).toEqual(READY_JOB)
  expect(built.packet!.inputs.task).toBe(pinnedContract().premise)
})

test("a checks repair review binds the satisfied revision and its old checks verbatim", async () => {
  const repair = {
    ...READY_JOB,
    checks_repair: {
      reason: "The old command had no executable control.",
      revisions: [{ worker_job: { job_id: READY_JOB.job_id, revision: 1, digest: `sha256:${"b".repeat(64)}` }, checks: ["unavailable old command"] }],
    },
  }
  const continuity = jobContinuity(["dispatch_worker", "record_worker_job"], [])
  continuity.result.pinned.checks_repair_review_jobs = [repair]
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuity }, { laneId: "review" })
  expect(built.failure).toBeUndefined()
  expect(built.packet!.inputs.worker_job).toEqual(repair)
  expect(built.packet!.inputs.task).toBe(pinnedContract().premise)
  expect(validateAgentLanePacket(built.packet!)).toBe(true)
})

test("a checks repair review never supplies an implementation or verification job", async () => {
  const continuity = jobContinuity(["dispatch_worker", "record_worker_job"], [])
  continuity.result.pinned.checks_repair_review_jobs = [READY_JOB]
  for (const laneId of ["implement", "verify"]) {
    const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuity }, { laneId })
    expect(built.failure?.kind).toBe("worker_job_unavailable")
  }
})

test("a checks repair review does not choose between eligible job revisions", async () => {
  const continuity = jobContinuity(["dispatch_worker", "record_worker_job"], [{ ...READY_JOB, job_id: "job:ordinary" }])
  continuity.result.pinned.checks_repair_review_jobs = [READY_JOB]
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuity }, { laneId: "review" })
  expect(built.failure?.kind).toBe("worker_job_ambiguous")
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

// CD-0227 D1: the continuity job carries the caller exclusions its
// implementation dispatch binds; the core admits them on no other lane.
test("only an implementation lane binds the published caller exclusions", async () => {
  const job = { ...READY_JOB, caller_exclusions: ["internal/store/worker_jobs.go"] }
  const implement = await build({ ...defaultScript(), "concord_work_trace.continuity": jobContinuity(["dispatch_worker", "record_worker_job"], [job]) })
  expect(implement.failure).toBeUndefined()
  expect(implement.packet!.inputs.worker_job).toEqual(job)
  for (const laneId of ["verify", "review"]) {
    const built = await build({ ...defaultScript(), "concord_work_trace.continuity": jobContinuity(["dispatch_worker", "record_worker_job"], [job]) }, { laneId })
    expect(built.failure).toBeUndefined()
    expect(built.packet!.inputs.worker_job).toEqual(READY_JOB)
  }
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

// The owner-level acceptance oracle is typed immutable job content.
// The builder rides the recorded revision verbatim, so the oracle rides with
// it byte-for-byte inside inputs.worker_job — the same copy every admitted
// lane receives — and the closed packet schema owns the oracle bounds.
const ORACLE_RECIPE_SOURCE = { kind: "repository_file" as const, project_id: "project-1", path: "internal/store/worker_oracle_harness_test.go", commit_oid: `a1${"0".repeat(38)}` }
const ORACLE = {
  owners: [{
    owner_id: "owner:acceptance-graph",
    domain_id: "workflow-engine",
    mechanism: { project_id: "project-1", path: "internal/store/worker_jobs.go", entry_point: "DeriveWorkerJobDigest" },
    obligation: "The job digest covers every recorded content field, the oracle included.",
    predicate_ids: ["predicate:primary"],
    law_bindings: [{ source: { kind: "knowledge" as const, source_id: "records", law_id: "CD-0205", content_hash: `sha256:${"b".repeat(64)}` }, clause: "D1 lines 39-53" }],
  }],
  cases: [{
    case_id: "case:digest-covers-oracle",
    owner_id: "owner:acceptance-graph",
    entry_point: "record_worker_job",
    input_class: "one oracle-bearing revision",
    expected_state: "the digest changes when the oracle changes",
    control_ids: ["control:harness-digest"],
  }],
  controls: [{
    control_id: "control:harness-digest",
    owner_id: "owner:acceptance-graph",
    predicate_ids: ["predicate:primary"],
    case_ids: ["case:digest-covers-oracle"],
    recipe_source: ORACLE_RECIPE_SOURCE,
    argv: ["go", "test", "./internal/store", "-run", "TestOwnerOracleGraph"],
    cwd: ".",
    expected_result: "pass" as const,
    required_evidence_role: "reported" as const,
    readiness_evidence_refs: ["run:harness-selftest"],
  }],
}
const ORACLE_JOB = { ...READY_JOB, predicate_ids: ["predicate:primary"], acceptance_oracle: ORACLE }

test("caller scope rides unchanged while caller exclusions remain implementation-only", async () => {
  const job: AgentLanePacketWorkerJob = {
    ...ORACLE_JOB,
    direct_caller_allowance: {
      project_id: "project-1",
      mechanisms: [ORACLE.owners[0].mechanism],
      domain_ids: [ORACLE.owners[0].domain_id],
      law_bindings: ORACLE.owners[0].law_bindings,
    },
    caller_exclusions: ["internal/store/unresolved.go"],
  }
  const before = structuredClone(job)
  for (const laneId of ["implement", "verify", "review"]) {
    const built = await build({ ...defaultScript(), "concord_work_trace.continuity": jobContinuity(["dispatch_worker", "record_worker_job"], [job]) }, { laneId })
    expect(built.failure).toBeUndefined()
    expect(validateAgentLanePacket(built.packet!)).toBe(true)
    const { caller_exclusions: _excluded, ...withoutExclusions } = job
    expect(built.packet!.inputs.worker_job).toEqual(laneId === "implement" ? job : withoutExclusions)
    expect(job).toEqual(before)
  }
})

test("a ready oracle job rides verbatim beside typed records and the report protocol", async () => {
  const continuity = jobContinuity(["dispatch_worker", "record_worker_job"], [ORACLE_JOB])
  Object.assign(continuity.result.pinned, { law_context: LAW_CONTEXT, design_record: DESIGN_RECORD, proposal_record: PROPOSAL })
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": continuity })
  expect(built.failure).toBeUndefined()
  expect(validateAgentLanePacket(built.packet!)).toBe(true)
  expect(built.packet!.inputs.worker_job).toEqual(ORACLE_JOB)
  expect(built.packet!.inputs.worker_job!.acceptance_oracle).toEqual(ORACLE)
  expect(built.packet!.inputs.law_context).toEqual(LAW_CONTEXT)
  expect(built.packet!.inputs.design_record).toEqual(DESIGN_RECORD)
  expect(built.packet!.inputs.proposal_record).toEqual(PROPOSAL)
  expect(built.packet!.inputs.work_record).toEqual({ narrative: NARRATIVE })
  expect(built.packet!.inputs.report_protocol).toBe("concord-worker-result-v1")
  expect("context" in built.packet!.inputs).toBe(false)
})

test("a malformed acceptance oracle refuses the packet build rather than riding it", async () => {
  const malformed = { ...ORACLE, owners: [{ ...ORACLE.owners[0], owner_id: "not-an-owner-identity" }] }
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": jobContinuity(["dispatch_worker", "record_worker_job"], [{ ...ORACLE_JOB, acceptance_oracle: malformed }]) })
  expect(built.failure?.kind).toBe("packet_refused")
  expect(built.failure?.message).toContain("acceptance_oracle")
})

test("an oracle past the closed control bound refuses the packet build", async () => {
  const overflow = { ...ORACLE, controls: [...ORACLE.controls, ...Array.from({ length: 64 }, (_, index) => ({ ...ORACLE.controls[0], control_id: `control:extra-${index}` }))] }
  const built = await build({ ...defaultScript(), "concord_work_trace.continuity": jobContinuity(["dispatch_worker", "record_worker_job"], [{ ...ORACLE_JOB, acceptance_oracle: overflow }]) })
  expect(built.failure?.kind).toBe("packet_refused")
})

test("TestReviewSubjectLaneMismatch", async () => {
  const agent = await Bun.file(new URL(`../../.opencode/agents/concord-review.md`, import.meta.url)).text()
  expect(agent).toContain("`inputs.work_context.subject_commit`")
  expect(agent).toContain("The host checks")
  expect(agent).toContain("before access and report settlement")
  expect(agent).not.toContain("Do not run shell commands")
  expect(agent).toContain("git rev-parse HEAD^{tree}")
  expect(agent).toContain("`failed`")
})

// An oversized projection travels as lossless projection pages of one
// canonical JSON document (CD-0016 as amended), and a consumer reconstructs
// every page before it uses any authority-bearing field. The fixtures mirror
// the Go agent's page emission: whole-code-point UTF-8 byte pages of the
// inline result's canonical JSON, each bound to one document digest, chained
// by the signed next_cursor the envelope carries.
const pagedUtf8 = new TextEncoder()
const pagedDocumentOf = (result: Record<string, unknown>) => JSON.stringify(result)
const pagedDigestOf = (document: string) => `sha256:${createHash("sha256").update(pagedUtf8.encode(document)).digest("hex")}`

const PAGED_UTF8_TEXT = "境contenido 🎯 anchor"
const PAGED_CONTEXT_FINDING = {
  ...WORK_CONTEXT.findings[0],
  finding_id: "finding:21:1",
  statement: `the paged statement repeats across page boundaries ${PAGED_UTF8_TEXT} ${"x".repeat(900)}`,
  source_event_seq: 21,
  ordinal: 1,
}
const PAGED_WORK_CONTEXT = {
  ...WORK_CONTEXT,
  // The paged projection carries the selected research bytes too,
  // so the paged-join assertions prove the research entry and its group view
  // id survive reconstruction verbatim.
  research_findings: [
    {
      ...RESEARCH_FINDING,
      view_id: "research:21:0",
      selection_event_id: "record_work_context-work-335-21:semantic",
      selection_event_seq: 21,
    },
  ],
  domain_groups: [
    { ...WORK_CONTEXT.domain_groups[0], finding_ids: ["finding:19:0", "research:21:0"] },
    ...WORK_CONTEXT.domain_groups.slice(1),
  ],
  findings: [PAGED_CONTEXT_FINDING, ...WORK_CONTEXT.findings],
}
const PAGED_CORRECTION: AgentLanePacketCorrection = {
  disposition: "failed",
  diagnosis: "the paged attempt failed",
  strategy: "retry the paged dispatch with a fresh fenced attempt",
  predicate_ids: [],
  evidence_refs: [],
}

// The oversized document grows through admitted store members the packet
// builder never reads: the full observation window and the pinned
// projection's second work-context copy. Their shapes and bounds follow the
// closed continuity schema; the packet-bound fields stay within their own
// recorded limits.
const OVERSIZED_CONTEXT = {
  source_event_frontier: 40,
  required_reading: [],
  findings: Array.from({ length: 30 }, (_, index) => ({
    finding_id: `finding:40:${index}`,
    kind: "direction",
    statement: "境".repeat(340),
    subject_ref: "internal/agent/runtime.go",
    evidence_refs: ["internal/agent/continuity_collection_budget_test.go"],
    domain_id: "agent-surface",
    origin: "declaration",
    status: "reported",
    source_event_id: "record_work_context-work-335-40:semantic",
    source_event_seq: 40,
    ordinal: index,
  })),
  domain_groups: [],
}
const oversizedObservation = (index: number) => ({
  observation_id: `observation:paged-${index}`,
  work_id: WORK_ID,
  statement: "境".repeat(512),
  recorded_at: "2026-09-09T00:00:00Z",
  refs: [`evidence:paged-${index}`],
  tags: ["paged"],
})

const pagedPinned = () => ({
  product_identity: [PRODUCT_ID],
  workflow_step: WORKFLOW_STEP,
  step_actions: ["dispatch_worker", "record_worker_job"],
  contract: pinnedContract(),
  spec_mandate: [],
  pending_operator_decision: null,
  latest_checkpoint: CHECKPOINT,
  design_record: null,
  unresolved_failure: null,
  work_pin: { correction: PAGED_CORRECTION, work_context: PAGED_WORK_CONTEXT },
  ready_worker_jobs: [ORACLE_JOB],
  work_context: OVERSIZED_CONTEXT,
})

const pagedResult = (): Record<string, unknown> => ({
  work_id: WORK_ID,
  pinned: pagedPinned(),
  boundaries: { count: 0, items: [], next_cursor: null, watermark: "seq:1" },
  typed_availability: { restart: "unavailable", reason: RESTART_UNAVAILABLE_REASON },
  pending_messages: 0,
  observations: Array.from({ length: 16 }, (_, index) => oversizedObservation(index)),
  observations_total: 16,
  observations_read: { tool: "concord_work_trace", operation: "observations", input: { work_id: WORK_ID } },
})

// fragmentPages cuts the canonical document the way the Go agent emits it:
// whole code points only, at most limitBytes UTF-8 bytes per page.
const fragmentPages = (document: string, limitBytes: number) => {
  const pages: { data: string; offset: number }[] = []
  let data = ""
  let dataBytes = 0
  let offset = 0
  for (const codePoint of document) {
    const width = pagedUtf8.encode(codePoint).length
    if (data.length > 0 && dataBytes + width > limitBytes) {
      pages.push({ data, offset })
      offset += dataBytes
      data = ""
      dataBytes = 0
    }
    data += codePoint
    dataBytes += width
  }
  if (data.length > 0) pages.push({ data, offset })
  return pages
}

const defaultToken = (index: number, count: number) => (index === count - 1 ? null : `cursor-${index + 1}`)

// pageEnvelope mirrors the agent's continuityProjectionEnvelope for one page
// of document: the scalar identity header, the exact byte page, and the
// continuation kind.
const pageEnvelope = (document: string, page: { data: string; offset: number }, index: number, count: number, nextCursor: string | null) =>
  coreEnvelope("concord_work_trace", "continuity", "C19.Continuity", "ok", {
    next_cursor: nextCursor,
    result: {
      work_id: WORK_ID,
      workflow_instance: "present",
      workflow_step: WORKFLOW_STEP,
      watermark: "seq:1",
      projection_page: {
        encoding: "json-utf8",
        digest: pagedDigestOf(document),
        total_bytes: pagedUtf8.encode(document).length,
        offset: page.offset,
        data: page.data,
        document_complete: index === count - 1,
      },
      next_kind: index === count - 1 ? null : "projection",
    },
  })

// pageChain serves the continuity read by cursor token, so repeated reads —
// the dispatch caller's and the packet builder's — replay the same unchanged
// page sequence. A cursor the chain does not script throws, the way an
// unscripted core read refuses; tests mutate the chain to break it.
const pageChain = (chain: unknown[], extra: Record<string, unknown> = {}) => {
  const seen: string[] = []
  const byCursor = new Map<unknown, unknown>([[null, chain[0]]])
  chain.slice(0, -1).forEach((envelope, index) => byCursor.set((envelope as any).next_cursor, chain[index + 1]))
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }) => {
    const key = `${toolName}.${args.operation}`
    seen.push(key)
    if (key === "concord_work_trace.continuity") {
      const cursor = (args.input as any)?.page?.cursor ?? null
      if (!byCursor.has(cursor)) throw new Error(`unscripted continuity cursor ${String(cursor)}`)
      return byCursor.get(cursor)
    }
    if (!(key in extra)) throw new Error(`unscripted read ${key}`)
    return extra[key]
  }
  return Object.assign(invoke, { seen: () => seen })
}

const pagedBuild = async (result: Record<string, unknown>, limitBytes: number, mutate?: (chain: unknown[]) => void) => {
  const document = pagedDocumentOf(result)
  const pages = fragmentPages(document, limitBytes)
  const chain: unknown[] = pages.map((page, index) => pageEnvelope(document, page, index, pages.length, defaultToken(index, pages.length)))
  mutate?.(chain)
  return buildAgentLanePacket(
    { workId: WORK_ID, productId: PRODUCT_ID, laneId: "implement", attemptId: "attempt-1", stepId: "step-1" },
    { context: contextFor(), invoke: pageChain(chain, { "concord_work_browse.scope": scopeEnvelope() }) as any },
  )
}

// splitBuild pages the document into exactly two fragments whose boundary is
// the given code-unit index, so a test decides where one page ends.
const splitBuild = async (result: Record<string, unknown>, splitAt: number) => {
  const document = pagedDocumentOf(result)
  const first = document.slice(0, splitAt)
  const pages = [{ data: first, offset: 0 }, { data: document.slice(splitAt), offset: pagedUtf8.encode(first).length }]
  const chain = pages.map((page, index) => pageEnvelope(document, page, index, pages.length, defaultToken(index, pages.length)))
  return buildAgentLanePacket(
    { workId: WORK_ID, productId: PRODUCT_ID, laneId: "implement", attemptId: "attempt-1", stepId: "step-1" },
    { context: contextFor(), invoke: pageChain(chain, { "concord_work_browse.scope": scopeEnvelope() }) as any },
  )
}

const inlineEnvelopeOf = (result: Record<string, unknown>) => coreEnvelope("concord_work_trace", "continuity", "C19.Continuity", "ok", { result })

test("a paged oversized continuity builds the exact packet the inline result builds", async () => {
  const result = pagedResult()
  // The canonical document is a genuinely oversized projection: its UTF-8
  // length exceeds the whole-envelope cap the inline branch must fit.
  expect(pagedUtf8.encode(pagedDocumentOf(result)).length).toBeGreaterThan(51_200)
  const inlineBuilt = await build({ ...defaultScript(), "concord_work_trace.continuity": inlineEnvelopeOf(result) })
  expect(inlineBuilt.failure).toBeUndefined()
  const built = await pagedBuild(result, 20_000)
  expect(built.failure).toBeUndefined()
  expect(built.packet).toEqual(inlineBuilt.packet)
  // The oracle-bearing ready job, the pin's context and correction, and the
  // latest checkpoint all survive the page join verbatim.
  expect(built.packet!.inputs.worker_job).toEqual(ORACLE_JOB)
  expect(built.packet!.inputs.work_context).toEqual(PAGED_WORK_CONTEXT)
  expect(built.packet!.inputs.checkpoint).toEqual(CHECKPOINT)
  expect(built.packet!.inputs.correction).toEqual(PAGED_CORRECTION)
  expect(validateAgentLanePacket(built.packet!)).toBe(true)
})

test("two ready jobs split across fragments still refuse the packet", async () => {
  const result = pagedResult()
  const second = { ...ORACLE_JOB, job_id: "job:paged-second", revision: 3, digest: `sha256:${"b".repeat(64)}` }
  ;(result.pinned as Record<string, unknown>).ready_worker_jobs = [ORACLE_JOB, second]
  const document = pagedDocumentOf(result)
  const secondAt = document.indexOf("job:paged-second")
  expect(secondAt).toBeGreaterThan(0)
  // The page boundary falls exactly before the second job, so a
  // fragment-only reader would see one ready revision and wrongly succeed.
  const built = await splitBuild(result, secondAt)
  expect(built.failure?.kind).toBe("worker_job_ambiguous")
  expect(built.failure?.message).toContain("job:paged-second")
  const inlineBuilt = await build({ ...defaultScript(), "concord_work_trace.continuity": inlineEnvelopeOf(result) })
  expect(inlineBuilt.failure?.kind).toBe("worker_job_ambiguous")
})

test("a page boundary before a multi-byte code point keeps byte offsets exact", async () => {
  const result = pagedResult()
  const document = pagedDocumentOf(result)
  const marker = "🎯"
  const markerAt = document.indexOf(marker)
  expect(markerAt).toBeGreaterThan(0)
  expect(pagedUtf8.encode(marker).length).toBe(4)
  const built = await splitBuild(result, markerAt)
  expect(built.failure).toBeUndefined()
  expect(built.packet!.inputs.work_context).toEqual(PAGED_WORK_CONTEXT)
  const inlineBuilt = await build({ ...defaultScript(), "concord_work_trace.continuity": inlineEnvelopeOf(result) })
  expect(built.packet).toEqual(inlineBuilt.packet)
})

test("a fragment carrying a lonely surrogate refuses instead of replacing it", async () => {
  const result = pagedResult()
  const built = await pagedBuild(result, 20_000, (chain) => {
    ;((chain[1] as any).result.projection_page as Record<string, unknown>).data = "\uD800git"
  })
  expect(built.failure?.kind).toBe("transport_failure")
  expect(built.failure?.message).toContain("surrogate")
})
