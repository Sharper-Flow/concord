import { test, expect, mock, beforeEach, afterEach } from "bun:test"
import { createHash } from "node:crypto"
import nodeFs from "node:fs"
import * as os from "node:os"
import path from "node:path"
import { hostControlPlane, MANAGED_TASK_SCOPE_KEY } from "./move-session"
import { resetClaimedWorktrees } from "./claimed-worktree"
import { AdapterContractDefect } from "./adapter-contract-defect"
import { reconstructMutationResult } from "./packet"
import { largeMutationPayload, mutationProjectionFixture } from "./mutation-projection.fixture"
import { maxEnvelopeBytes } from "./generated-contracts"
const testDirectories: string[] = []
function mkdtempSync(prefix: string): string {
  const directory = nodeFs.mkdtempSync(prefix)
  testDirectories.push(directory)
  return directory
}
const fs = { ...nodeFs, mkdtempSync }

beforeEach(() => {
  // Another file's landing tests can arm a claim that leaks into this run;
  // dispatch checks here must not depend on file order.
  resetClaimedWorktrees()
  hostControlPlane().bind({
    get: async ({ path }) => ({ data: { id: path?.id, directory: process.cwd(), metadata: { [MANAGED_TASK_SCOPE_KEY]: "managed" } }, response: new Response(null, { status: 200 }) }),
    // Dispatch launches no worker and queues no subtask (CD-0102 D1), so the
    // default transport refuses every transcript write; a test that observes
    // a call binds its own recording post.
    post: async () => { throw new Error("dispatch must not write to the host transcript") },
  })
})
afterEach(() => {
  hostControlPlane().bind(undefined)
  for (const directory of testDirectories.splice(0)) fs.rmSync(directory, { recursive: true, force: true })
})

// The lane dispatcher reaches the Opencode plugin through ToolContext (typed
// only) and the dispatch worker through dispatchWorker, but the test never
// executes the real plugin path. With the shared stub the
// suite never loads the real host module.
mock.module("@opencode-ai/plugin", () => ({ tool: fakeTool }))

const { agentLanes, agentUtilities } = await import("./generated-agent-lanes")
const { validateAgentLanePacket } = await import("./dispatch")
import { DispatchWindows, TASK_TOOL_ID, laneTaskArguments, serializeLanePacket } from "./dispatch-window"
import type { AgentLanePacket, AgentLanePacketCorrection, DispatchRunner } from "./dispatch"
import type { CredentialStore } from "./credentials"
import { fakeTool } from "./plugin-tool.fixture"
const { dispatchAttemptID, dispatchLaneWorker } = await import("./lane_dispatch")
const { laneDispatchRequest } = await import("./concord")

const isRecord = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === "object" && !Array.isArray(value)

const testCredentials: CredentialStore = { async getPrivateKey() { return new Uint8Array(32).fill(7) } }

const lane = agentLanes[1]
const WORK_ID = "work-dispatch"
const PRODUCT_ID = "product-dispatch"
const WORKFLOW_STEP = "execution"
const NARRATIVE = "Project lane identity, step, narrative, and obligations into the closed agent-lane-packet.v1."
const OUTCOME_KIND = "check"
const OUTCOME_PAYLOAD = JSON.stringify({ kind: "check", check_ref: "check:projected-dispatch-inputs", immutable_subject_ref: "contracts/agent-lane-packet.schema.json", expected_result: "pass" })

function envelope<T>(body: T): T & { schema_version: "1.0"; origin: "core" } {
  return { schema_version: "1.0", origin: "core", ...body } as T & { schema_version: "1.0"; origin: "core" }
}

const scopeEnvelope = () => envelope({
  tool: "concord_work_browse", operation: "scope", outcome: "ok", authority: "authoritative", freshness: null,
  result: {
    work: { id: WORK_ID, kind: "task", title: "Reachability: dispatch through work_transition", lifecycle: "in_progress", version: 1, priority: 0, project_ids: [PRODUCT_ID], ready: true, narrative: NARRATIVE, terminal_at: null },
    memberships: [{ project_id: PRODUCT_ID, role: "primary" }],
    items: [],
  },
})

const continuityEnvelope = (overrides: Partial<{ pinned: Record<string, unknown>; resolvedScope: Record<string, unknown> | null }> = {}) => envelope({
  tool: "concord_work_trace", operation: "continuity", outcome: "ok", authority: "authoritative", freshness: null,
  resolved_scope: overrides.resolvedScope !== undefined ? overrides.resolvedScope : { product_id: PRODUCT_ID, project_ids: [PRODUCT_ID], scope_version: "sha256:" + "b".repeat(64) },
  result: {
    work_id: WORK_ID,
    pinned: {
      product_identity: [PRODUCT_ID],
      workflow_step: WORKFLOW_STEP,
      contract: {
        version: 1, premise: "Reachability test", outcome_predicates: [{ predicate_id: "predicate:primary", ordinal: 0, outcome_kind: OUTCOME_KIND, outcome_payload: OUTCOME_PAYLOAD }],
        required_evidence: [], route_conventions: [], spec_mandate: [], changes_product_truth: false,
      },
      spec_mandate: [], pending_operator_decision: null, latest_checkpoint: null, unresolved_failure: null,
      ...(overrides.pinned ?? {}),
    },
    boundaries: { count: 0, items: [], next_cursor: null, watermark: "seq:1" },
    typed_availability: { restart: "unavailable", reason: "typed restart is deliberately excluded (CD-0027); pinned continuity is re-derived per call" },
    pending_messages: 0, observations: [], observations_total: 0,
    observations_read: { tool: "concord_work_trace", operation: "observations", input: { work_id: WORK_ID } },
  },
})

// CD-0067 D6: the dispatch_worker response must carry worker_packet_digest on
// result so the adapter can quote it on the signed evidence assertion. Issue
// #1322: it must also carry worker_worktree, the durable claimed worktree the
// authorization rested on, so the tool-context gate survives a host restart.
// Every test that exercises the happy path uses this envelope; tests that
// probe the missing-field refusals override result to a partial record.
const CORE_PACKET_DIGEST = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
const coreOkEnvelope = (workerWorktree: string = process.cwd()) => envelope({
  tool: "concord_work_transition", operation: "workflow_action", outcome: "ok", authority: "authoritative", freshness: null,
  result: { worker_packet_digest: CORE_PACKET_DIGEST, worker_worktree: workerWorktree },
})

const coreErrorEnvelope = (kind: string, message: string) => envelope({
  tool: "concord_work_transition", operation: "workflow_action", outcome: "error", authority: "authoritative", freshness: null,
  error: { kind, retry_safe: false, recovery_action: { kind: "reconcile_operation" }, effect_state: "none", message },
})

const READBACK_MODEL = "openai/gpt-5.6-luna"
// The attempt id is derived from the complete dispatch request, so the report
// fixture can identify the packet without a clock.
const expectedAttemptId = "attempt-9dfc7234904d484b85fa07dbbc26db6fab196964c5bcfa6e071dd26be21d469f"
const reportEvent = () => JSON.stringify({ type: "text", timestamp: 2, sessionID: "session-1", part: { type: "text", text: JSON.stringify({
  schema_version: "1.0", attempt_id: expectedAttemptId, lane_id: lane.id, lane_digest: lane.digest,
  readback_model: READBACK_MODEL, status: "completed",
  evidence: lane.evidence_obligations.map((obligation) => ({ obligation, detail: `discharged ${obligation}` })),
}) } })
const runOutput = () => [
  JSON.stringify({ type: "step_start", timestamp: 1, sessionID: "session-1", part: { type: "step-start" } }),
  reportEvent(),
  JSON.stringify({ type: "step_finish", timestamp: 3, sessionID: "session-1", part: { type: "step-finish", reason: "stop" } }),
].join("\n")
const exportedSession = () => JSON.stringify({
  info: { id: "session-1" },
  messages: [{ info: { id: "message-1", sessionID: "session-1", role: "assistant", agent: `concord-${lane.id}`, providerID: READBACK_MODEL.split("/")[0], modelID: READBACK_MODEL.split("/").slice(1).join("/"), time: { created: 1 } }, parts: [] }],
})

// The ask default refuses: dispatch raises no permission ask of any kind —
// the host's native Task owns Task permission (CD-0102 D1) — so any call here
// fails the test that reached for it.
const contextFor = () => ({ sessionID: "session-1", messageID: "message-1", agent: "agent-1", worktree: process.cwd(), directory: process.cwd(), abort: new AbortController().signal, ask: async () => { throw new Error("dispatch must not raise a permission ask") } }) as any

test("failed scope enrollment cannot authorize a core dispatch or open a window", async () => {
  hostControlPlane().bind({
    get: async () => ({ data: { id: "session-1", metadata: {} }, response: new Response(null, { status: 200 }) }),
    post: async () => { throw new Error("dispatch must not move the session") },
  })
  const seen: string[] = []
  const invoke = async (toolName: string, args: { operation: string }) => {
    const key = `${toolName}.${args.operation}`
    seen.push(key)
    if (key === "concord_work_trace.continuity") return continuityEnvelope()
    if (key === "concord_work_browse.scope") return scopeEnvelope()
    throw new Error("failed enrollment must not authorize dispatch")
  }
  const windows = new DispatchWindows()
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "scope-refusal", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, windows })
  expect(result.outcome).toBe("blocked")
  expect(result.error?.message).toContain("managed Task scope")
  expect(result.error?.retry_safe).toBe(false)
  expect(result.error?.recovery_action).toBe("contact_operator")
  expect(seen).not.toContain("concord_work_transition.workflow_action")
  expect(windows.has("session-1")).toBe(false)
})

test("native dispatch consumes the complete projected mutation result without returning its oversized pins", async () => {
  const payload = { ...largeMutationPayload(), worker_packet_digest: CORE_PACKET_DIGEST, worker_worktree: process.cwd() }
  const fixture = mutationProjectionFixture(payload, "concord_work_transition", "workflow_action", "idemp-projection")
  let reads = 0
  const invoke = async (toolName: string) => {
    if (toolName === "concord_work_trace") return continuityEnvelope()
    if (toolName === "concord_work_browse") return scopeEnvelope()
    await reconstructMutationResult(fixture.envelope, fixture.source, async () => fixture.pages[reads++])
    return fixture.envelope
  }
  const windows = new DispatchWindows()
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "idemp-projection", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, credentials: testCredentials, windows })
  expect(result.outcome).toBe("ok")
  expect(result.dispatch_state).toBe("awaiting_worker")
  expect(windows.has("session-1")).toBe(true)
  expect(reads).toBe(fixture.pages.length)
  expect(result.result).toEqual(fixture.envelope.result)
  expect(result.changed_refs).toEqual(fixture.envelope.changed_refs)
  expect(result.work_pins).toBeUndefined()
  expect(Buffer.byteLength(JSON.stringify(result))).toBeLessThan(maxEnvelopeBytes)
})

test("unreconstructed projected results cannot open native dispatch windows", async () => {
  const fixture = mutationProjectionFixture({ ...largeMutationPayload(), worker_packet_digest: CORE_PACKET_DIGEST, worker_worktree: process.cwd() }, "concord_work_transition", "workflow_action", "idemp-projection")
  const invoke = async (toolName: string) => toolName === "concord_work_trace" ? continuityEnvelope() : toolName === "concord_work_browse" ? scopeEnvelope() : fixture.envelope
  const windows = new DispatchWindows()
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "idemp-projection", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, credentials: testCredentials, windows })
  expect(result.outcome).toBe("error")
  expect(windows.has("session-1")).toBe(false)
  expect(result.result).toEqual(fixture.envelope.result)
  expect(result.changed_refs).toEqual(fixture.envelope.changed_refs)
})

test("routing selects the lane dispatcher for dispatch_worker with object-form fields and a string lane_id", () => {
  const request = laneDispatchRequest({ operation: "workflow_action", input: { work_id: WORK_ID, expected_version: 3, action_id: "dispatch_worker", idempotency_key: "idemp-1", fields: { lane_id: "research" } } })
  expect(request).toEqual({ work_id: WORK_ID, expected_version: 3, idempotency_key: "idemp-1", lane_id: "research" })
})

test("unchanged dispatch requests keep their attempt identity across approval resubmission", () => {
  const request = { work_id: WORK_ID, expected_version: 3, idempotency_key: "retry-1", lane_id: lane.id }
  expect(dispatchAttemptID(request)).toBe(dispatchAttemptID({ ...request, approval_ref: "challenge-1" }))
  expect(dispatchAttemptID(request)).not.toBe(dispatchAttemptID({ ...request, idempotency_key: "retry-2" }))
})

test("routing returns the typed error when fields.lane_id is missing", () => {
  expect(laneDispatchRequest({ operation: "workflow_action", input: { work_id: WORK_ID, expected_version: 3, action_id: "dispatch_worker", idempotency_key: "idemp-1", fields: { attempt_id: "attempt-1" } } })).toEqual({ error: "dispatch_worker requires fields.lane_id naming the target lane" })
  expect(laneDispatchRequest({ operation: "workflow_action", input: { work_id: WORK_ID, expected_version: 3, action_id: "dispatch_worker", idempotency_key: "idemp-1", fields: [{ name: "attempt_id", value: "attempt-1" }] } })).toEqual({ error: "dispatch_worker requires fields.lane_id naming the target lane" })
  expect(laneDispatchRequest({ operation: "workflow_action", input: { work_id: WORK_ID, expected_version: 3, action_id: "dispatch_worker", idempotency_key: "idemp-1" } })).toEqual({ error: "dispatch_worker requires fields.lane_id naming the target lane" })
})

test("routing returns null for non-dispatch_worker actions so the generic transport handles them", () => {
  expect(laneDispatchRequest({ operation: "workflow_action", input: { work_id: WORK_ID, expected_version: 3, action_id: "complete", idempotency_key: "idemp-1", fields: { impact_verdict: "non-breaking" } } })).toBeNull()
  expect(laneDispatchRequest({ operation: "snapshot", input: {} })).toBeNull()
  expect(laneDispatchRequest(undefined)).toBeNull()
  expect(laneDispatchRequest(null)).toBeNull()
})

test("happy path: continuity → packet → core ok → authorization directive with stubbed runners reaches no process", async () => {
  const seen: string[] = []
  const workflowInput: { value?: Record<string, unknown> } = {}
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
    const key = `${toolName}.${args.operation}`
    seen.push(key)
    if (key === "concord_work_trace.continuity") return continuityEnvelope()
    if (key === "concord_work_browse.scope") return scopeEnvelope()
    if (key === "concord_work_transition.workflow_action") { workflowInput.value = args.input; return coreOkEnvelope() }
    throw new Error(`unscripted ${key}`)
  }
  let processCalls = 0
  const runner: DispatchRunner = { async run() { processCalls++; return { exitCode: 0, stdout: "", stderr: "" } } }
  let evidenceCalls = 0
  const evidenceRunner: DispatchRunner = { async run() { evidenceCalls++; return { exitCode: 0, stdout: "", stderr: "" } } }
  const windows = new DispatchWindows()
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "idemp-1", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, credentials: testCredentials, runner, evidenceRunner, windows })
  expect(result.outcome).toBe("ok")
  expect(result.dispatch_state).toBe("awaiting_worker")
  expect(result.result?.worker_packet_digest).toBe(CORE_PACKET_DIGEST)
  expect(seen).toContain("concord_work_trace.continuity")
  expect(seen).toContain("concord_work_browse.scope")
  expect(seen).toContain("concord_work_transition.workflow_action")
  expect(isRecord(workflowInput.value)).toBe(true)
  const fields = (workflowInput.value as Record<string, unknown>).fields as Record<string, unknown>
  expect(typeof fields.attempt_id).toBe("string")
  const packet = fields.worker_packet as AgentLanePacket
  expect(isRecord(packet)).toBe(true)
  expect(packet.lane_id).toBe(lane.id)
  expect(packet.work_id).toBe(WORK_ID)
  expect(packet.step_id).toBe(WORKFLOW_STEP)
  expect(packet.attempt_id).toBe(fields.attempt_id as string)
  expect(packet.inputs.task).toBe("Reachability test")
  expect(packet.inputs.binding).toEqual({ objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "files_touched" })
  expect(validateAgentLanePacket(packet)).toBe(true)
  // The host runs the worker, so dispatch starts no process and records no
  // evidence. Both belong to the native Task and completion (CD-0102 D1/D5).
  expect(processCalls).toBe(0)
  expect(evidenceCalls).toBe(0)
  // The directive is authorization, not launch: it names the one native
  // foreground Task the coordinator must issue, with the exact selector.
  const selector = { subagent_type: `concord-${lane.id}`, prompt: packet.attempt_id, description: "Run authorized worker" }
  expect(result.output).toContain("Authorized only")
  expect(result.output).toContain("launches no worker")
  expect(result.output).toContain("one native foreground Task")
  expect(result.output).toContain(`exactly ${JSON.stringify(selector)}`)
  expect(JSON.parse(result.output!.slice(result.output!.indexOf("{"), result.output!.lastIndexOf("}") + 1))).toEqual(selector)
  expect(windows.has("session-1")).toBe(true)
})

// CD-0102 D1: dispatch returns authorization, not launch. It writes no
// subtask, raises no permission ask, and its one caller-visible instruction is
// the exact native foreground Task selector the coordinator must issue from
// the requesting session.
test("authorized dispatch writes no subtask, asks no permission, and its directive names the exact native Task selector", async () => {
  const windows = new DispatchWindows()
  const transcriptWrites: unknown[] = []
  const context = contextFor()
  hostControlPlane().bind({
    get: async ({ path }) => ({ data: { id: path?.id, directory: process.cwd(), metadata: { [MANAGED_TASK_SCOPE_KEY]: "managed" } }, response: new Response(null, { status: 200 }) }),
    async post(request) { transcriptWrites.push(request); throw new Error("dispatch must not write a native subtask") },
  })
  let authorizedPacket: AgentLanePacket | undefined
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }) => {
    const key = `${toolName}.${args.operation}`
    if (key === "concord_work_trace.continuity") return continuityEnvelope()
    if (key === "concord_work_browse.scope") return scopeEnvelope()
    if (key === "concord_work_transition.workflow_action") {
      authorizedPacket = (args.input!.fields as { worker_packet: AgentLanePacket }).worker_packet
      return coreOkEnvelope()
    }
    throw new Error(`unscripted ${key}`)
  }
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "native-task", lane_id: lane.id }, { context, invoke: invoke as any, credentials: testCredentials, windows })
  expect(result.outcome, JSON.stringify(result)).toBe("ok")
  expect(result.dispatch_state).toBe("awaiting_worker")
  expect(transcriptWrites).toHaveLength(0)
  const packet = authorizedPacket!
  const selector = { subagent_type: `concord-${lane.id}`, prompt: packet.attempt_id, description: "Run authorized worker" }
  expect(result.output).toContain("Authorized only")
  expect(result.output).toContain("launches no worker")
  expect(result.output).toContain("one native foreground Task")
  expect(result.output).toContain(`exactly ${JSON.stringify(selector)}`)
  expect(JSON.parse(result.output!.slice(result.output!.indexOf("{"), result.output!.lastIndexOf("}") + 1))).toEqual(selector)
  expect(windows.has("session-1")).toBe(true)
})

// The window the authorization opened binds one Task call — and only a call
// naming the exact registered lane and the exact recorded attempt reference
// with no other fields. A stale reference, the old queued full-packet prompt,
// a wrong lane, an ordinary utility agent, and every execution-control seed
// refuse without touching the armed window; the matching call then consumes it
// once, overwriting the caller's arguments with the recorded packet.
test("stale, full-packet, wrong-lane, utility, and execution-control Task seeds leave the window usable; the matching selector binds the recorded packet once", async () => {
  const windows = new DispatchWindows()
  let authorizedPacket: AgentLanePacket | undefined
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }) => {
    const key = `${toolName}.${args.operation}`
    if (key === "concord_work_trace.continuity") return continuityEnvelope()
    if (key === "concord_work_browse.scope") return scopeEnvelope()
    if (key === "concord_work_transition.workflow_action") {
      authorizedPacket = (args.input!.fields as { worker_packet: AgentLanePacket }).worker_packet
      return coreOkEnvelope()
    }
    throw new Error(`unscripted ${key}`)
  }
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "native-selector", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, credentials: testCredentials, windows })
  expect(result.outcome, JSON.stringify(result)).toBe("ok")
  const packet = authorizedPacket!
  const exact = () => ({ subagent_type: `concord-${lane.id}`, prompt: packet.attempt_id, description: "Run authorized worker" })
  const otherLane = agentLanes.find((candidate) => candidate.id !== lane.id)!
  const seeds: Array<[string, Record<string, unknown>]> = [
    ["stale attempt reference", { ...exact(), prompt: "attempt-stale" }],
    ["full queued packet as prompt", { ...exact(), prompt: serializeLanePacket(packet) }],
    ["wrong lane", { ...exact(), subagent_type: `concord-${otherLane.id}` }],
    ["ordinary utility agent", { ...exact(), subagent_type: "explore" }],
    ["resume control field", { ...exact(), task_id: "resumed-session" }],
    ["background control field", { ...exact(), background: true }],
    ["command control field", { ...exact(), command: "opencode run" }],
  ]
  for (const [name, seed] of seeds) {
    await expect(windows.bind(TASK_TOOL_ID, "session-1", { ...seed }, "seed-call", async () => process.cwd(), process.cwd()), name).rejects.toThrow("requires the exact authorized lane and attempt reference")
    expect(windows.has("session-1"), name).toBe(true)
  }
  const bound = exact()
  await windows.bind(TASK_TOOL_ID, "session-1", bound, "matching-call", async () => process.cwd(), process.cwd())
  expect(bound).toEqual(laneTaskArguments(packet))
  expect(windows.has("session-1")).toBe(false)
  await expect(windows.bind(TASK_TOOL_ID, "session-1", exact(), "replayed-call", async () => process.cwd(), process.cwd())).rejects.toThrow("no authorized dispatch window")
})

// Task permission is the host's native gate, not the adapter's: dispatch
// consults no ask and performs no transcript write, so a coordinator whose
// ask would deny anything still receives the authorization directive and its
// one consumable window.
test("dispatch consults no permission ask and no transcript write; native Task permission is the host's", async () => {
  const windows = new DispatchWindows()
  const transcriptWrites: unknown[] = []
  const context = contextFor()
  context.ask = async () => { throw new Error("synthetic host Task denial") }
  hostControlPlane().bind({
    get: async ({ path }) => ({ data: { id: path?.id, directory: process.cwd(), metadata: { [MANAGED_TASK_SCOPE_KEY]: "managed" } }, response: new Response(null, { status: 200 }) }),
    async post(request) { transcriptWrites.push(request); throw new Error("dispatch must not write a native subtask") },
  })
  const invoke = async (toolName: string, args: { operation: string }) => {
    const key = `${toolName}.${args.operation}`
    if (key === "concord_work_trace.continuity") return continuityEnvelope()
    if (key === "concord_work_browse.scope") return scopeEnvelope()
    if (key === "concord_work_transition.workflow_action") return coreOkEnvelope()
    throw new Error(`unscripted ${key}`)
  }
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "host-denied-task", lane_id: lane.id }, { context, invoke: invoke as any, credentials: testCredentials, windows })
  expect(result.outcome).toBe("ok")
  expect(result.dispatch_state).toBe("awaiting_worker")
  expect(result.output).toContain("launches no worker")
  expect(transcriptWrites).toHaveLength(0)
  expect(windows.has("session-1")).toBe(true)
})

// One session holds at most one window. A second dispatch authorizes nothing
// new to launch: the window owner refuses the second arming and the first
// authorization's window stays the only consumable one.
test("a second dispatch cannot arm a second window while one is open and never retries the launch", async () => {
  const windows = new DispatchWindows()
  const firstRequest = { work_id: WORK_ID, expected_version: 3, idempotency_key: "first-window", lane_id: lane.id }
  const secondRequest = { work_id: WORK_ID, expected_version: 3, idempotency_key: "second-window", lane_id: lane.id }
  const packets: AgentLanePacket[] = []
  let authorizations = 0
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }) => {
    const key = `${toolName}.${args.operation}`
    if (key === "concord_work_trace.continuity") return continuityEnvelope()
    if (key === "concord_work_browse.scope") return scopeEnvelope()
    if (key === "concord_work_transition.workflow_action") {
      authorizations++
      packets.push((args.input!.fields as { worker_packet: AgentLanePacket }).worker_packet)
      return coreOkEnvelope()
    }
    throw new Error(`unscripted ${key}`)
  }
  const first = await dispatchLaneWorker(firstRequest, { context: contextFor(), invoke: invoke as any, credentials: testCredentials, windows })
  expect(first.outcome).toBe("ok")
  expect(windows.has("session-1")).toBe(true)
  const second = await dispatchLaneWorker(secondRequest, { context: contextFor(), invoke: invoke as any, credentials: testCredentials, windows })
  expect(second.outcome).toBe("error")
  expect(second.error?.message).toContain("already holds an open dispatch window")
  expect(windows.has("session-1")).toBe(true)
  // The armed window is still the first authorization's: its exact selector
  // binds the first recorded packet, and the second attempt's reference does
  // not.
  const firstSelector = { subagent_type: `concord-${lane.id}`, prompt: packets[0].attempt_id, description: "Run authorized worker" }
  await windows.bind(TASK_TOOL_ID, "session-1", firstSelector, "first-call", async () => process.cwd(), process.cwd())
  expect(firstSelector.prompt).toBe(serializeLanePacket(packets[0]))
  const secondSelector = { subagent_type: `concord-${lane.id}`, prompt: packets[1].attempt_id, description: "Run authorized worker" }
  await expect(windows.bind(TASK_TOOL_ID, "session-1", secondSelector, "second-call", async () => process.cwd(), process.cwd())).rejects.toThrow("no authorized dispatch window")
  expect(authorizations).toBe(2)
})

test("pre-contract research dispatch carries the recorded question as the task", async () => {
  const research = agentLanes.find((candidate) => candidate.id === "research")!
  const workflowInput: { value?: Record<string, unknown> } = {}
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
    const key = `${toolName}.${args.operation}`
    if (key === "concord_work_trace.continuity") return continuityEnvelope({ pinned: { contract: null, workflow_step: "reproduce" } })
    if (key === "concord_work_browse.scope") return scopeEnvelope()
    if (key === "concord_work_transition.workflow_action") {
      workflowInput.value = args.input
      return coreOkEnvelope()
    }
    throw new Error(`unscripted ${key}`)
  }
  const windows = new DispatchWindows()
  const result = await dispatchLaneWorker(
    { work_id: WORK_ID, expected_version: 3, idempotency_key: "pre-contract-research", lane_id: research.id },
    { context: contextFor(), invoke: invoke as any, credentials: testCredentials, windows },
  )
  expect(result.outcome).toBe("ok")
  const fields = workflowInput.value!.fields as Record<string, unknown>
  const packet = fields.worker_packet as AgentLanePacket
  expect(packet.lane_id).toBe(research.id)
  // The recorded question rides the task verbatim, not an invented objective;
  // the narrative stays in context instead of a duplicate copy in the task.
  expect(packet.inputs.task).toBe("Reachability: dispatch through work_transition")
  expect(packet.inputs.task).not.toContain("Answer the recorded question")
  expect(packet.inputs.task).not.toContain(NARRATIVE)
  expect(packet.inputs.binding).toEqual({ objective_source: "work_question", work_version: 1, contract_version: null, assigned_result: "bounded_findings" })
  expect(packet.inputs.work_record).toEqual({ narrative: NARRATIVE })
  expect(packet.inputs.outcome_predicates).toBeUndefined()
  expect(packet.inputs.constraints).toBeUndefined()
  expect(windows.has("session-1")).toBe(true)
})

// A full public dispatch with a premise at the admitted maximum —
// 4096 UTF-8 bytes of ASCII — and eight synthetic predicates reaches the core
// with the objective byte-for-byte and opens the authorization window. The
// binding has independent capacity rather than consuming the task allowance.
test("a full dispatch at the admitted maximum premise carries the objective verbatim and eight predicates", async () => {
  const premise = "o".repeat(4_096)
  const eightPredicates = Array.from({ length: 8 }, (_, ordinal) => ({
    predicate_id: `predicate:dispatch-eight-${ordinal}`,
    ordinal,
    outcome_kind: "check",
    outcome_payload: JSON.stringify({ kind: "check", check_ref: `check:dispatch/eight-${ordinal}`, immutable_subject_ref: "contracts/agent-lane-packet.schema.json", expected_result: "pass" }),
  }))
  const workflowInput: { value?: Record<string, unknown> } = {}
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
    const key = `${toolName}.${args.operation}`
    if (key === "concord_work_trace.continuity") {
      return continuityEnvelope({ pinned: { contract: { version: 2, premise, outcome_predicates: eightPredicates, required_evidence: [], route_conventions: [], spec_mandate: [], changes_product_truth: false } } })
    }
    if (key === "concord_work_browse.scope") return scopeEnvelope()
    if (key === "concord_work_transition.workflow_action") { workflowInput.value = args.input; return coreOkEnvelope() }
    throw new Error(`unscripted ${key}`)
  }
  const windows = new DispatchWindows()
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "max-premise-dispatch", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, credentials: testCredentials, windows })
  expect(result.outcome, JSON.stringify(result)).toBe("ok")
  expect(result.dispatch_state).toBe("awaiting_worker")
  const fields = workflowInput.value!.fields as Record<string, unknown>
  const packet = fields.worker_packet as AgentLanePacket
  expect(packet.inputs.task).toBe(premise)
  expect(Buffer.byteLength(packet.inputs.task, "utf8")).toBe(4_096)
  expect(packet.inputs.binding).toEqual({ objective_source: "contract_premise", work_version: 1, contract_version: 2, assigned_result: "files_touched" })
  expect(packet.inputs.outcome_predicates).toHaveLength(8)
  packet.inputs.outcome_predicates!.forEach((predicate, ordinal) => {
    expect(predicate.ordinal).toBe(ordinal)
    expect(predicate.predicate_id).toBe(`predicate:dispatch-eight-${ordinal}`)
  })
  expect(validateAgentLanePacket(packet)).toBe(true)
  expect(windows.has("session-1")).toBe(true)
})

// Negative control: a premise the task bound refuses dispatches no core
// attempt, opens no authorization window, and starts no worker. The refusal
// names the field, the count unit, the actual, and the limit.
test("an oversize premise authorizes nothing in the core", async () => {
  const premise = "o".repeat(4_500)
  let workflowCalls = 0
  let spawned = 0
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
    const key = `${toolName}.${args.operation}`
    if (key === "concord_work_trace.continuity") {
      return continuityEnvelope({ pinned: { contract: { version: 1, premise, outcome_predicates: [{ predicate_id: "predicate:primary", ordinal: 0, outcome_kind: "check", outcome_payload: OUTCOME_PAYLOAD }], required_evidence: [], route_conventions: [], spec_mandate: [], changes_product_truth: false } } })
    }
    if (key === "concord_work_browse.scope") return scopeEnvelope()
    if (key === "concord_work_transition.workflow_action") { workflowCalls++; return coreOkEnvelope() }
    throw new Error(`unscripted ${key}`)
  }
  const runner: DispatchRunner = { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } }
  const windows = new DispatchWindows()
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "oversize-premise", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, runner, windows })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_input")
  expect(result.error?.message).toContain("inputs.task")
  expect(result.error?.message).toContain("Unicode code points")
  expect(result.error?.message).toContain("4500")
  expect(result.error?.message).toContain("4096")
  expect(workflowCalls).toBe(0)
  expect(spawned).toBe(0)
  expect(windows.has("session-1")).toBe(false)
})

test("predicate array overflow reports actual count before any authorization or window", async () => {
  const predicates = Array.from({ length: 9 }, (_, ordinal) => ({
    predicate_id: `predicate:overflow-${ordinal}`,
    ordinal,
    outcome_kind: "check",
    outcome_payload: OUTCOME_PAYLOAD,
  }))
  let workflowCalls = 0
  let runnerCalls = 0
  const invoke = async (toolName: string, args: { operation: string }): Promise<unknown> => {
    const key = `${toolName}.${args.operation}`
    if (key === "concord_work_trace.continuity") {
      return continuityEnvelope({ pinned: { contract: { version: 1, premise: "Verify the bound.", outcome_predicates: predicates, required_evidence: [], route_conventions: [], spec_mandate: [], changes_product_truth: false } } })
    }
    if (key === "concord_work_browse.scope") return scopeEnvelope()
    if (key === "concord_work_transition.workflow_action") { workflowCalls++; return coreOkEnvelope() }
    throw new Error(`unscripted ${key}`)
  }
  const runner: DispatchRunner = { async run() { runnerCalls++; return { exitCode: 0, stdout: "", stderr: "" } } }
  const windows = new DispatchWindows()
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "array-overflow", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, runner, windows })
  expect(result.outcome).toBe("error")
  expect(result.error?.message).toContain("inputs.outcome_predicates")
  expect(result.error?.message).toContain("9 item(s)")
  expect(result.error?.message).toContain("limit of 8")
  expect(workflowCalls).toBe(0)
  expect(runnerCalls).toBe(0)
  expect(windows.has("session-1")).toBe(false)
})

// Where the worker will run must be known before the core authorizes the
// dispatch. The directory itself is the host's answer and is not compared
// against the host process directory, which is not where Task runs; the window
// re-reads it at bind time to catch a session that moved in between.
test("an unreadable session directory refuses before core dispatch authorization", async () => {
  const seen: string[] = []
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
    const key = `${toolName}.${args.operation}`
    seen.push(key)
    if (key === "concord_work_trace.continuity") return continuityEnvelope()
    if (key === "concord_work_browse.scope") return scopeEnvelope()
    throw new Error(`unexpected ${key}`)
  }

  hostControlPlane().bind({
    get: async ({ path }) => ({
      data: { id: path?.id, metadata: { [MANAGED_TASK_SCOPE_KEY]: "managed" } },
      response: new Response(null, { status: 200 }),
    }),
    post: async () => { throw new Error("dispatch does not move the host session") },
  })

  const result = await dispatchLaneWorker(
    { work_id: WORK_ID, expected_version: 3, idempotency_key: "directory-mismatch", lane_id: lane.id },
    { context: contextFor(), invoke: invoke as any },
  )

  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("transport_failure")
  expect(result.error?.message).toContain("directory")
  expect(seen).not.toContain("concord_work_transition.workflow_action")
})

test("production dispatch refuses a session retargeted during core authorization", async () => {
  const root = fs.mkdtempSync(path.join(process.cwd(), "concord-dispatch-"))
  const claimed = path.join(root, "claimed")
  const other = path.join(root, "other")
  const alias = path.join(root, "alias")
  for (const directory of [claimed, other]) fs.mkdirSync(directory)
  fs.symlinkSync(claimed, alias)
  // The pre-effect gate refuses a tool context outside the host session
  // directory before the core is asked, so this race fixture lands the
  // context in the claimed worktree first and retargets the alias only
  // during the authorization call itself.
  const landed = fs.realpathSync(claimed)
  const landedContext = { sessionID: "session-1", messageID: "message-1", agent: "agent-1", worktree: landed, directory: landed, abort: new AbortController().signal, ask: async () => {} } as any
  try {
    hostControlPlane().bind({
      get: async ({ path: routePath }) => ({
        data: { id: routePath?.id, directory: alias, metadata: { [MANAGED_TASK_SCOPE_KEY]: "managed" } },
        response: new Response(null, { status: 200 }),
      }),
      post: async () => { throw new Error("dispatch does not move the host session") },
    })
    let transitionCalls = 0
    let authorizedDirectory = ""
    const invoke = async (toolName: string, args: { operation: string }, _context: unknown, sessionDirectory?: string): Promise<unknown> => {
      const key = `${toolName}.${args.operation}`
      if (key === "concord_work_trace.continuity") return continuityEnvelope()
      if (key === "concord_work_browse.scope") return scopeEnvelope()
      if (key === "concord_work_transition.workflow_action") {
        transitionCalls++
        authorizedDirectory = sessionDirectory ?? ""
        fs.unlinkSync(alias)
        fs.symlinkSync(other, alias)
        return coreOkEnvelope()
      }
      throw new Error(`unscripted ${key}`)
    }
    const windows = new DispatchWindows()
    const result = await dispatchLaneWorker(
      { work_id: WORK_ID, expected_version: 3, idempotency_key: "directory-race", lane_id: lane.id },
      { context: landedContext, invoke: invoke as any, credentials: testCredentials, windows },
    )
    expect(result.outcome).toBe("error")
    expect(result.error?.kind).toBe("unauthorized_dispatch")
    expect(result.error?.message).toMatch(/does not match the active claimed worktree/i)
    expect(transitionCalls).toBe(1)
    expect(authorizedDirectory).toBe(fs.realpathSync(claimed))
    expect(windows.has("session-1")).toBe(false)
  } finally {
    fs.rmSync(root, { recursive: true, force: true })
  }
})

// A session with no armed claimed worktree — including one whose host process
// restarted after a claim — must dispatch exactly as before the armed-claim
// check existed. The absence of a record never becomes a refusal.
test("a session with no armed claimed worktree dispatches exactly as before", async () => {
  const seen: string[] = []
  const invoke = async (toolName: string, args: { operation: string }): Promise<unknown> => {
    const key = `${toolName}.${args.operation}`
    seen.push(key)
    if (key === "concord_work_trace.continuity") return continuityEnvelope()
    if (key === "concord_work_browse.scope") return scopeEnvelope()
    if (key === "concord_work_transition.workflow_action") return coreOkEnvelope()
    throw new Error(`unscripted ${key}`)
  }
  const windows = new DispatchWindows()
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "no-armed-claim", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, credentials: testCredentials, windows })
  expect(result.outcome).toBe("ok")
  expect(result.dispatch_state).toBe("awaiting_worker")
  expect(seen).toContain("concord_work_transition.workflow_action")
  expect(windows.has("session-1")).toBe(true)
})

test("unregistered lane refuses before any core invoke or spawn", async () => {
  let workflowCalls = 0
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
    if (toolName === "concord_work_trace") return continuityEnvelope()
    if (toolName === "concord_work_transition") { workflowCalls++; return coreOkEnvelope() }
    throw new Error(`unscripted ${toolName}.${args.operation}`)
  }
  let spawned = 0
  const runner: DispatchRunner = { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } }
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 1, idempotency_key: "idemp-2", lane_id: "summarize" }, { context: contextFor(), invoke: invoke as any, runner })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_input")
  expect(result.error?.message).toContain("summarize")
  expect(spawned).toBe(0)
  expect(workflowCalls).toBe(0)
})

// A registered generated utility id is not a lane: dispatch_worker refuses it
// before any core call with a refusal distinct from the unregistered-lane one.
// The refusal names the utility and its native route — a coordinator-only Task
// with subagent_type concord-<id> and no dispatch window — stays retry-unsafe,
// and carries the machine-readable utility_dispatch boundary marker.
test("a registered utility id refuses dispatch_worker before any core call with its native Task route", async () => {
  let coreCalls = 0
  let spawned = 0
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
    coreCalls++
    if (toolName === "concord_work_trace") return continuityEnvelope()
    if (toolName === "concord_work_browse") return scopeEnvelope()
    if (toolName === "concord_work_transition") return coreOkEnvelope()
    throw new Error(`unscripted ${toolName}.${args.operation}`)
  }
  const runner: DispatchRunner = { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } }
  const windows = new DispatchWindows()
  const utility = agentUtilities.find((candidate) => candidate.id === "explore")!
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 1, idempotency_key: "idemp-utility-route", lane_id: utility.id }, { context: contextFor(), invoke: invoke as any, runner, windows })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_input")
  expect(result.error?.retry_safe).toBe(false)
  expect(result.error?.recovery_action).toBe("use_declared_route")
  expect(result.error?.message).toContain(utility.id)
  expect(result.error?.message).toContain(`concord-${utility.id}`)
  expect(result.error?.message).toMatch(/native task/i)
  expect(result.error?.message).toContain("coordinator")
  expect(result.error?.message).toContain("without a dispatch window")
  const details = result.error?.details as Record<string, unknown>
  expect(details.boundary).toBe("utility_dispatch")
  expect(details.utility).toBe(utility.id)
  expect(spawned).toBe(0)
  expect(windows.has("session-1")).toBe(false)
  expect(coreCalls).toBe(0)
})

// The utility refusal must not swallow an unknown lane id: a name no registry
// admits keeps the existing unregistered-lane refusal, distinct from the
// utility boundary marker.
test("an unknown lane id keeps the unregistered-lane refusal, not the utility refusal", async () => {
  let workflowCalls = 0
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
    if (toolName === "concord_work_trace") return continuityEnvelope()
    if (toolName === "concord_work_transition") { workflowCalls++; return coreOkEnvelope() }
    throw new Error(`unscripted ${toolName}.${args.operation}`)
  }
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 1, idempotency_key: "idemp-unknown-keeps-lane-refusal", lane_id: "summarize" }, { context: contextFor(), invoke: invoke as any })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_input")
  expect(result.error?.message).toContain("not in the generated lane registry")
  const details = (result.error?.details ?? {}) as Record<string, unknown>
  expect(details.boundary).toBeUndefined()
  expect(workflowCalls).toBe(0)
})

test("core authorization refusal on dispatch_worker surfaces as unauthorized_dispatch without spawn", async () => {
  let spawned = 0
  let workflowCalls = 0
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
    if (toolName === "concord_work_trace") return continuityEnvelope()
    if (toolName === "concord_work_browse") return scopeEnvelope()
    if (toolName === "concord_work_transition") { workflowCalls++; return coreErrorEnvelope("unauthorized_dispatch", "no authorized dispatch window exists for this work item at the current step") }
    throw new Error(`unscripted ${toolName}.${args.operation}`)
  }
  const runner: DispatchRunner = { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } }
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 1, idempotency_key: "idemp-3", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, runner })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("unauthorized_dispatch")
  expect(result.error?.message).toBe("no authorized dispatch window exists for this work item at the current step")
  expect(spawned).toBe(0)
  expect(workflowCalls).toBe(1)
})

// A core aggregate dispatch-context bound refusal is a typed limit_exceeded.
// The bound itself is store-owned; this asserts the adapter contract for that
// refusal class — the cause and recovery surface unchanged, no worker spawns,
// and no Task window arms. There is deliberately no adapter-side byte
// measurement to parallel the core bound.
test("a core aggregate context bound refusal surfaces without arming a Task window", async () => {
  let spawned = 0
  let workflowCalls = 0
  const invoke = async (toolName: string, args: { operation: string }): Promise<unknown> => {
    if (toolName === "concord_work_trace") return continuityEnvelope()
    if (toolName === "concord_work_browse") return scopeEnvelope()
    if (toolName === "concord_work_transition") {
      workflowCalls++
      return coreErrorEnvelope("limit_exceeded", "the dispatch context members measure 65537 bytes of canonical JSON, above the 65536-byte aggregate dispatch context bound; the request is refused whole and never trimmed")
    }
    throw new Error(`unscripted ${toolName}.${args.operation}`)
  }
  const runner: DispatchRunner = { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } }
  const windows = new DispatchWindows()
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 1, idempotency_key: "idemp-agg-bound", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, runner, windows })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("error")
  expect(result.error?.retry_safe).toBe(false)
  expect(result.error?.recovery_action).toBe("reconcile_operation")
  expect(result.error?.message).toContain("65537")
  const core = (result.error?.details ?? {}) as Record<string, unknown>
  expect((core.core_error as Record<string, unknown>).kind).toBe("limit_exceeded")
  expect(workflowCalls).toBe(1)
  expect(spawned).toBe(0)
  expect(windows.has("session-1")).toBe(false)
})

for (const refusal of [
  { kind: "approval_invalid", recovery: "request_approval", retry: false },
  { kind: "internal_error", recovery: "contact_operator", retry: false },
  { kind: "timeout", recovery: "retry_same_request", retry: true },
  { kind: "budget_refused", recovery: "adjust_budget", retry: false },
  { kind: "version_conflict", recovery: "reread_entities", retry: false },
  { kind: "missing_evidence", recovery: "provide_evidence", retry: false },
] as const) {
  test(`core ${refusal.kind} retains its cause and maps recovery without an authorization denial`, async () => {
    const coreError = {
      kind: refusal.kind, recovery_action: { kind: refusal.recovery }, retry_safe: refusal.retry,
      effect_state: "none", message: `core ${refusal.kind} diagnostic`,
      details: refusal.kind === "internal_error" ? { boundary: "host_approval_timestamp", reason: "expired", validated_at: "2026-01-01T00:02:01Z", issued_at: "2026-01-01T00:00:00Z" } : {},
      ...(refusal.kind === "budget_refused" ? { supported_budget_seconds: 30 } : {}),
      ...(refusal.kind === "version_conflict" ? { current_versions: [{ entity_kind: "work", id: WORK_ID, version: "2" }] } : {}),
    }
    let dispatches = 0
    let spawned = 0
    const windows = new DispatchWindows()
    const invoke = async (toolName: string): Promise<unknown> => {
      if (toolName === "concord_work_trace") return continuityEnvelope()
      if (toolName === "concord_work_browse") return scopeEnvelope()
      dispatches++
      return envelope({ outcome: "error", error: coreError })
    }
    const runner: DispatchRunner = { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } }
    const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 1, idempotency_key: `cause-${refusal.kind}`, lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, runner, windows })
    expect(result.error?.kind).toBe("error")
    expect(result.error?.details?.core_error).toEqual(coreError)
    expect(result.error?.retry_safe).toBe(refusal.retry)
    expect(result.error?.recovery_action).toBe(["reread_entities", "provide_evidence"].includes(refusal.recovery) ? "contact_operator" : refusal.recovery)
    expect(result.error?.message).toBe(coreError.message)
    expect(dispatches).toBe(1)
    expect(spawned).toBe(0)
    expect(windows.has("session-1")).toBe(false)
  })
}

test("a possible-effect approval failure reconciles instead of asking for another approval", async () => {
  const coreError = { kind: "approval_required", effect_state: "possible", retry_safe: false, recovery_action: { kind: "request_approval" }, message: "approval outcome uncertain" }
  const invoke = async (toolName: string): Promise<unknown> => toolName === "concord_work_trace" ? continuityEnvelope() : toolName === "concord_work_browse" ? scopeEnvelope() : envelope({ outcome: "error", error: coreError })
  const windows = new DispatchWindows()
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 1, idempotency_key: "possible-approval", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, windows })
  expect(result.error?.kind).toBe("error")
  expect(result.error?.recovery_action).toBe("reconcile_operation")
  expect(result.error?.retry_safe).toBe(false)
  expect(result.error?.details?.core_error).toEqual(coreError)
  expect(windows.has("session-1")).toBe(false)
})

test("a malformed core error never becomes a retry-safe authorization refusal", async () => {
  const windows = new DispatchWindows()
  const invoke = async (toolName: string): Promise<unknown> => toolName === "concord_work_trace" ? continuityEnvelope() : toolName === "concord_work_browse" ? scopeEnvelope() : envelope({ outcome: "error" })
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 1, idempotency_key: "malformed-core-error", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, windows })
  expect(result.error?.kind).toBe("transport_failure")
  expect(result.error?.recovery_action).toBe("contact_operator")
  expect(result.error?.retry_safe).toBe(false)
  expect(windows.has("session-1")).toBe(false)
})

test("an initially replayed ordinary authorization cannot recreate a missing window", async () => {
  const windows = new DispatchWindows()
  const request = { work_id: WORK_ID, expected_version: 1, idempotency_key: "initial-replay", lane_id: lane.id }
  let workflowCalls = 0
  const invoke = async (toolName: string): Promise<unknown> => {
    if (toolName === "concord_work_trace") return continuityEnvelope()
    if (toolName === "concord_work_browse") return scopeEnvelope()
    if (toolName === "concord_work_transition") { workflowCalls++; return { ...coreOkEnvelope(), replayed: true } }
    throw new Error(`unscripted ${toolName}`)
  }
  const result = await dispatchLaneWorker(request, { context: contextFor(), invoke: invoke as any, credentials: testCredentials, windows })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("unauthorized_dispatch")
  expect(result.error?.retry_safe).toBe(false)
  expect(result.error?.recovery_action).toBe("reconcile_operation")
  expect(result.error?.message).toContain(dispatchAttemptID(request))
  expect(result.dispatch_state).toBeUndefined()
  expect(windows.has("session-1")).toBe(false)
  expect(workflowCalls).toBe(1)
})

// A core failure after the dispatch boundary committed carries effect_state
// possible and retry_safe true. The adapter must replay the byte-identical
// request itself: a caller-level retry rebuilds the packet against the work
// version the commit advanced and refuses as idempotency_conflict. When the replay keeps failing, the answer
// reconciles rather than advising that unsafe retry.
test("a retry-safe possible effect replays the identical request, then reconciles", async () => {
  let spawned = 0
  const dispatchInputs: Record<string, unknown>[] = []
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
    if (toolName === "concord_work_trace") return continuityEnvelope()
    if (toolName === "concord_work_browse") return scopeEnvelope()
    if (toolName === "concord_work_transition") {
      dispatchInputs.push(JSON.parse(JSON.stringify(args.input)))
      return envelope({
        tool: "concord_work_transition", operation: "workflow_action", outcome: "error", authority: "unreachable", freshness: null,
        error: { kind: "unreachable", retry_safe: true, recovery_action: { kind: "retry_same_request" }, effect_state: "possible", message: "durability checkpoint did not complete: busy=1 log=45 checkpointed=4" },
      })
    }
    throw new Error(`unscripted ${toolName}.${args.operation}`)
  }
  const runner: DispatchRunner = { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } }
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 1, idempotency_key: "idemp-possible", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, runner })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("error")
  expect(result.error?.recovery_action).toBe("reconcile_operation")
  expect(result.error?.retry_safe).toBe(false)
  expect(result.error?.message).toContain("busy=1 log=45 checkpointed=4")
  const details = (result.error?.details ?? {}) as Record<string, unknown>
  expect(details.effect_state).toBe("possible")
  expect(typeof details.attempt_id).toBe("string")
  // one original attempt plus two bounded replays, each byte-identical
  expect(dispatchInputs).toHaveLength(3)
  expect(dispatchInputs[1]).toEqual(dispatchInputs[0])
  expect(dispatchInputs[2]).toEqual(dispatchInputs[0])
  expect(spawned).toBe(0)
})

test("CON-913 dispatch and replay rethrow the same contract defect without opening a window", async () => {
  for (const failAt of [1, 2]) {
    const defect = new AdapterContractDefect("concord_work_transition.workflow_action request dispatch-913 failed at error; effect unknown, reconcile before retry")
    const windows = new DispatchWindows()
    let calls = 0
    const invoke = async (toolName: string): Promise<unknown> => {
      if (toolName === "concord_work_trace") return continuityEnvelope()
      if (toolName === "concord_work_browse") return scopeEnvelope()
      calls++
      if (calls === failAt) throw defect
      return envelope({
        tool: "concord_work_transition", operation: "workflow_action", outcome: "error", authority: "unreachable", freshness: null,
        error: { kind: "unreachable", retry_safe: true, recovery_action: { kind: "retry_same_request" }, effect_state: "possible", message: "durability checkpoint pending" },
      })
    }
    let caught: unknown
    try {
      await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 1, idempotency_key: `defect-913-${failAt}`, lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, windows })
    } catch (error) {
      caught = error
    }
    expect(caught).toBe(defect)
    expect(calls).toBe(failAt)
    expect(windows.has("session-1")).toBe(false)
  }
})

// When the replay reaches the committed authorization, the dispatch continues
// on the replayed ok answer with no second packet build.
test("a retry-safe possible effect replays into the committed authorization", async () => {
  let spawned = 0
  let workflowCalls = 0
  const dispatchInputs: Record<string, unknown>[] = []
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
    if (toolName === "concord_work_trace") return continuityEnvelope()
    if (toolName === "concord_work_browse") return scopeEnvelope()
    if (toolName === "concord_work_transition") {
      workflowCalls++
      dispatchInputs.push(JSON.parse(JSON.stringify(args.input)))
      if (workflowCalls === 1) {
        return envelope({
          tool: "concord_work_transition", operation: "workflow_action", outcome: "error", authority: "unreachable", freshness: null,
          error: { kind: "unreachable", retry_safe: true, recovery_action: { kind: "retry_same_request" }, effect_state: "possible", message: "durability checkpoint did not complete: busy=1 log=45 checkpointed=4" },
        })
      }
      return { ...coreOkEnvelope(), replayed: true }
    }
    throw new Error(`unscripted ${toolName}.${args.operation}`)
  }
  const runner: DispatchRunner = {
    async run(argv) {
      if (argv[1] === "run") return { exitCode: 0, stdout: runOutput(), stderr: "" }
      if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(), stderr: "" }
      spawned++
      return { exitCode: 0, stdout: "", stderr: "" }
    },
  }
  const evidenceRunner: DispatchRunner = { async run() { return { exitCode: 0, stdout: "", stderr: "" } } }
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 1, idempotency_key: "idemp-replay-ok", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, credentials: testCredentials, runner, evidenceRunner, windows: new DispatchWindows() })
  expect(result.outcome).toBe("ok")
  expect(result.dispatch_state).toBe("awaiting_worker")
  expect(workflowCalls).toBe(2)
  expect(dispatchInputs[1]).toEqual(dispatchInputs[0])
})

test("approval challenge and approved resubmission preserve the exact packet identity", async () => {
  const packets: Record<string, unknown>[] = []
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
    if (toolName === "concord_work_trace") return continuityEnvelope()
    if (toolName === "concord_work_browse") return scopeEnvelope()
    if (toolName === "concord_work_transition") {
      const input = args.input ?? {}
      packets.push(input.fields as Record<string, unknown>)
      return input.approval ? coreOkEnvelope() : coreErrorEnvelope("approval_required", "exact operator approval is required")
    }
    throw new Error(`unscripted ${toolName}.${args.operation}`)
  }
  const request = { work_id: WORK_ID, expected_version: 1, idempotency_key: "retry-approval", lane_id: lane.id }
  const first = await dispatchLaneWorker(request, { context: contextFor(), invoke: invoke as any })
  expect(first.error?.kind).toBe("approval_required")
  const second = await dispatchLaneWorker({ ...request, approval_ref: "challenge-1" }, { context: contextFor(), invoke: invoke as any, credentials: testCredentials })
  expect(second.outcome).toBe("ok")
  expect(packets).toHaveLength(2)
  expect(packets[0].attempt_id).toBe(packets[1].attempt_id)
  expect(packets[0].worker_packet).toEqual(packets[1].worker_packet)
})

// The adapter transports a core approval refusal without inferring its cause
// from correction history. Only core admission can authorize the resubmission.
test("a core approval refusal forwards its exact consequence bindings", async () => {
  const challengeDetails = {
    approval_ref: "challenge-1", operation_digest: "sha256:" + "a".repeat(64),
    work_id: WORK_ID, action_id: "dispatch_worker", contract_version: "1", selected_choice: "", premise_summary: "approved retry objective",
  }
  const consequenceSummary = {
    tool: "concord_work_transition", operation: "workflow_action", consequence: "workflow",
    operation_digest: challengeDetails.operation_digest,
    scope: ["product_id:product-dispatch", "work_ids:work-dispatch"],
    versions: ["contract:1", "work:3"], expires_at: "2026-10-01T00:00:00Z",
  }
  let spawned = 0
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
    if (toolName === "concord_work_trace") return continuityEnvelope()
    if (toolName === "concord_work_browse") return scopeEnvelope()
    if (toolName === "concord_work_transition") {
      if (args.input?.approval) return coreOkEnvelope()
      return envelope({
        tool: "concord_work_transition", operation: "workflow_action", outcome: "error", authority: "authoritative", freshness: null,
        error: { kind: "approval_required", retry_safe: false, recovery_action: { kind: "request_approval" }, effect_state: "none", message: "core approval is required for this workflow action", details: challengeDetails, consequence_summary: consequenceSummary },
      })
    }
    throw new Error(`unscripted ${toolName}.${args.operation}`)
  }
  const runner: DispatchRunner = { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } }
  const windows = new DispatchWindows()
  const request = { work_id: WORK_ID, expected_version: 3, idempotency_key: "core-approval", lane_id: lane.id }
  const first = await dispatchLaneWorker(request, { context: contextFor(), invoke: invoke as any, runner, windows })
  expect(first.outcome).toBe("error")
  expect(first.error?.kind).toBe("approval_required")
  expect(first.error?.recovery_action).toBe("request_approval")
  const forwarded = first.error!.details!
  expect(forwarded.approval_ref).toBe("challenge-1")
  expect(forwarded.operation_digest).toBe("sha256:" + "a".repeat(64))
  expect(forwarded.work_id).toBe(WORK_ID)
  expect(forwarded.action_id).toBe("dispatch_worker")
  expect(forwarded.contract_version).toBe("1")
  expect(forwarded.premise_summary).toBe("approved retry objective")
  expect(forwarded.consequence_summary).toEqual(consequenceSummary)
  expect(spawned).toBe(0)
  const second = await dispatchLaneWorker({ ...request, approval_ref: "challenge-1" }, { context: contextFor(), invoke: invoke as any, runner, windows, credentials: testCredentials })
  expect(second.outcome).toBe("ok")
  expect(spawned).toBe(0)
})

// product_identity is the distinct product set across the work item's
// projects, so zero or several identities is valid core state. The packet
// Product is the dispatching session's core-resolved ambient Product
// (resolved_scope.product_id): dispatch admits a cross-Product work item when
// that Product is one of the identities — the scope read and the packet both
// project it, and the first-listed membership never chooses instead.
test("cross-product dispatch projects the session's ambient Product when it is one of the identities", async () => {
  let spawned = 0
  let workflowCalls = 0
  const scopeInputs: Array<Record<string, unknown>> = []
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
    const key = `${toolName}.${args.operation}`
    if (key === "concord_work_trace.continuity") return continuityEnvelope({ pinned: { product_identity: ["product-elsewhere", PRODUCT_ID], workflow_step: WORKFLOW_STEP } })
    if (key === "concord_work_browse.scope") { scopeInputs.push(args.input ?? {}); return scopeEnvelope() }
    if (key === "concord_work_transition.workflow_action") { workflowCalls++; return coreOkEnvelope() }
    throw new Error(`unscripted ${key}`)
  }
  const runner: DispatchRunner = { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } }
  const windows = new DispatchWindows()
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 1, idempotency_key: "idemp-cross-product", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, credentials: testCredentials, runner, windows })
  expect(result.outcome).toBe("ok")
  expect(result.dispatch_state).toBe("awaiting_worker")
  expect(scopeInputs).toHaveLength(1)
  expect(scopeInputs[0].product_id).toBe(PRODUCT_ID)
  expect(workflowCalls).toBe(1)
  expect(spawned).toBe(0)
  expect(windows.has("session-1")).toBe(true)
})

// The session's ambient scope decides: a work item that does not carry the
// session's Product — including an unscoped one with zero identities — refuses
// as blocked invalid_input before the packet builder, before dispatch_worker,
// and before any spawn.
test("a Product the work item does not belong to refuses before dispatch_worker without spawn", async () => {
  for (const identities of [["product-elsewhere"], []]) {
    let spawned = 0
    let scopeCalls = 0
    let workflowCalls = 0
    const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
      const key = `${toolName}.${args.operation}`
      if (key === "concord_work_trace.continuity") return continuityEnvelope({ pinned: { product_identity: identities, workflow_step: WORKFLOW_STEP } })
      if (key === "concord_work_browse.scope") { scopeCalls++; return scopeEnvelope() }
      if (key === "concord_work_transition.workflow_action") { workflowCalls++; return coreOkEnvelope() }
      throw new Error(`unscripted ${key}`)
    }
    const runner: DispatchRunner = { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } }
    const windows = new DispatchWindows()
    const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 1, idempotency_key: `idemp-nonmember-${identities.length}`, lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, runner, windows })
    expect(result.outcome).toBe("blocked")
    expect(result.error?.kind).toBe("invalid_input")
    expect(result.error?.message).toContain("does not belong to the session's ambient Product")
    expect(result.error?.message).toContain(identities.length === 0 ? "no product identities" : "product-elsewhere")
    expect(scopeCalls).toBe(0)
    expect(workflowCalls).toBe(0)
    expect(spawned).toBe(0)
    expect(windows.has("session-1")).toBe(false)
  }
})

// A session whose Project holds no Product, or several Products without an
// explicit selection, resolves no ambient Product: the core echoes no
// product_id on resolved_scope, and dispatch refuses instead of choosing one.
test("a session that resolves no ambient Product refuses before dispatch_worker without spawn", async () => {
  for (const resolvedScope of [null, { project_ids: [PRODUCT_ID], scope_version: "sha256:" + "b".repeat(64) }]) {
    let spawned = 0
    let scopeCalls = 0
    let workflowCalls = 0
    const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
      const key = `${toolName}.${args.operation}`
      if (key === "concord_work_trace.continuity") return continuityEnvelope({ resolvedScope })
      if (key === "concord_work_browse.scope") { scopeCalls++; return scopeEnvelope() }
      if (key === "concord_work_transition.workflow_action") { workflowCalls++; return coreOkEnvelope() }
      throw new Error(`unscripted ${key}`)
    }
    const runner: DispatchRunner = { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } }
    const windows = new DispatchWindows()
    const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 1, idempotency_key: `idemp-no-ambient-${resolvedScope === null ? "null" : "empty"}`, lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, runner, windows })
    expect(result.outcome).toBe("blocked")
    expect(result.error?.kind).toBe("invalid_input")
    expect(result.error?.message).toContain("resolves no ambient Product (none, or ambiguous)")
    expect(result.error?.message).toContain(`dispatching ${WORK_ID}`)
    expect(scopeCalls).toBe(0)
    expect(workflowCalls).toBe(0)
    expect(spawned).toBe(0)
    expect(windows.has("session-1")).toBe(false)
  }
})

// An imported work item holds no workflow instance. Continuity states that
// absence as typed data, so dispatch refuses it as blocked, not as a
// transport fault.
test("a work item with no workflow instance is a blocked refusal without spawn", async () => {
  let spawned = 0
  let workflowCalls = 0
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
    if (toolName === "concord_work_trace") return continuityEnvelope({ pinned: { product_identity: [PRODUCT_ID], workflow_instance: "absent", workflow_step: null, contract: null } })
    if (toolName === "concord_work_transition") { workflowCalls++; return coreOkEnvelope() }
    throw new Error(`unscripted ${toolName}.${args.operation}`)
  }
  const runner: DispatchRunner = { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } }
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 1, idempotency_key: "idemp-absent", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, runner })
  expect(result.outcome).toBe("blocked")
  expect(result.error?.kind).toBe("invalid_input")
  expect(result.error?.message).toContain("holds no workflow instance")
  expect(spawned).toBe(0)
  expect(workflowCalls).toBe(0)
})

test("mandate_unapproved refusal maps to outcome blocked", async () => {
  let spawned = 0
  let workflowCalls = 0
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
    if (toolName === "concord_work_trace") {
      return continuityEnvelope({ pinned: { product_identity: [PRODUCT_ID], workflow_step: WORKFLOW_STEP, contract: null } })
    }
    if (toolName === "concord_work_browse") return scopeEnvelope()
    if (toolName === "concord_work_transition") { workflowCalls++; return coreOkEnvelope() }
    throw new Error(`unscripted ${toolName}.${args.operation}`)
  }
  const runner: DispatchRunner = { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } }
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 1, idempotency_key: "idemp-5", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, runner })
  expect(result.outcome).toBe("blocked")
  expect(result.error?.kind).toBe("invalid_input")
  expect(result.error?.message).toContain("no pinned workflow contract")
  expect(spawned).toBe(0)
  expect(workflowCalls).toBe(0)
})

// CD-0067 D6: a core that answers ok without recording worker_packet_digest
// is not the core that authored the window — the dispatch_worker response
// surface must carry the digest the dispatch authorization recorded, or the
// adapter refuses with transport_failure so a hostile or misconfigured
// core cannot smuggle evidence past the gate. Spawn never executes.
test("dispatch_worker response without worker_packet_digest refuses before spawn", async () => {
  let spawned = 0
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
    if (toolName === "concord_work_trace") return continuityEnvelope()
    if (toolName === "concord_work_browse") return scopeEnvelope()
    if (toolName === "concord_work_transition") return envelope({
      tool: "concord_work_transition", operation: "workflow_action", outcome: "ok", authority: "authoritative", freshness: null,
      // result is an empty record — the cutover break this regression guards.
      result: {},
    })
    throw new Error(`unscripted ${toolName}.${args.operation}`)
  }
  const runner: DispatchRunner = { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } }
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "idemp-6", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, runner })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("transport_failure")
  expect(result.error?.message).toContain("worker_packet_digest")
  expect(result.error?.recovery_action).toBe("reconcile_operation")
  expect(spawned).toBe(0)
})

// Issue #1322: the same contract break as a missing digest — an ok response
// that names no claimed worktree is not an authorization whose tool-context
// landing the adapter can gate, so it refuses before any window opens.
test("dispatch_worker response without worker_worktree refuses before spawn", async () => {
  let spawned = 0
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
    if (toolName === "concord_work_trace") return continuityEnvelope()
    if (toolName === "concord_work_browse") return scopeEnvelope()
    if (toolName === "concord_work_transition") return envelope({
      tool: "concord_work_transition", operation: "workflow_action", outcome: "ok", authority: "authoritative", freshness: null,
      result: { worker_packet_digest: CORE_PACKET_DIGEST },
    })
    throw new Error(`unscripted ${toolName}.${args.operation}`)
  }
  const runner: DispatchRunner = { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } }
  const windows = new DispatchWindows()
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "idemp-worktree-missing", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, runner, credentials: testCredentials, windows })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("transport_failure")
  expect(result.error?.message).toContain("worker_worktree")
  expect(result.error?.recovery_action).toBe("reconcile_operation")
  expect(spawned).toBe(0)
  expect(windows.has("session-1")).toBe(false)
})

// Issue #1322, the durable gate end to end: the core's response names a
// claimed worktree the calling tool context does not run in, and no in-memory
// claim record exists (the post-restart posture), so dispatch refuses with the
// replay route and opens no window.
test("dispatch refuses when the tool context sits outside the authorized claimed worktree", async () => {
  const root = fs.mkdtempSync(path.join(process.cwd(), "concord-dispatch-"))
  fs.mkdirSync(path.join(root, "claimed"))
  const claimed = fs.realpathSync(path.join(root, "claimed"))
  try {
    const invoke = async (toolName: string, args: { operation: string }): Promise<unknown> => {
      const key = `${toolName}.${args.operation}`
      if (key === "concord_work_trace.continuity") return continuityEnvelope()
      if (key === "concord_work_browse.scope") return scopeEnvelope()
      if (key === "concord_work_transition.workflow_action") return coreOkEnvelope(claimed)
      throw new Error(`unscripted ${key}`)
    }
    hostControlPlane().bind({
      get: async () => ({ data: { id: "session-1", directory: process.cwd(), metadata: { [MANAGED_TASK_SCOPE_KEY]: "managed" } }, response: new Response(null, { status: 200 }) }),
      post: async () => { throw new Error("dispatch does not move the host session") },
    })
    const windows = new DispatchWindows()
    const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "idemp-durable-gate", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, credentials: testCredentials, windows })
    expect(result.outcome).toBe("error")
    expect(result.error?.kind).toBe("unauthorized_dispatch")
    expect(result.error?.message).toContain(claimed)
    expect(result.error?.message).toMatch(/replay work_start or worktree_claim/)
    expect(windows.has("session-1")).toBe(false)
  } finally {
    fs.rmSync(root, { recursive: true, force: true })
  }
})

// Issue #1322, the pre-effect gate end to end: the host session directory has
// converged on the claimed worktree while the calling tool context still runs
// elsewhere, so the lane dispatch refuses before the core dispatch_worker
// action — the core persists no authorized attempt and no window opens.
test("a mismatched tool context authorizes nothing in the core", async () => {
  const root = fs.mkdtempSync(path.join(process.cwd(), "concord-dispatch-"))
  fs.mkdirSync(path.join(root, "claimed"))
  fs.mkdirSync(path.join(root, "elsewhere"))
  const claimed = fs.realpathSync(path.join(root, "claimed"))
  const elsewhere = fs.realpathSync(path.join(root, "elsewhere"))
  try {
    let transitionCalls = 0
    const invoke = async (toolName: string, args: { operation: string }): Promise<unknown> => {
      const key = `${toolName}.${args.operation}`
      if (key === "concord_work_trace.continuity") return continuityEnvelope()
      if (key === "concord_work_browse.scope") return scopeEnvelope()
      if (key === "concord_work_transition.workflow_action") { transitionCalls++; return coreOkEnvelope(claimed) }
      throw new Error(`unscripted ${key}`)
    }
    hostControlPlane().bind({
      get: async () => ({ data: { id: "session-1", directory: claimed, metadata: { [MANAGED_TASK_SCOPE_KEY]: "managed" } }, response: new Response(null, { status: 200 }) }),
      post: async () => { throw new Error("dispatch does not move the host session") },
    })
    const windows = new DispatchWindows()
    const context = { sessionID: "session-1", messageID: "message-1", agent: "agent-1", worktree: elsewhere, directory: elsewhere, abort: new AbortController().signal, ask: async () => {} } as any
    const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "idemp-context-gate", lane_id: lane.id }, { context, invoke: invoke as any, credentials: testCredentials, windows })
    expect(result.outcome).toBe("error")
    expect(result.error?.kind).toBe("unauthorized_dispatch")
    expect(result.error?.message).toContain(claimed)
    expect(result.error?.message).toContain(elsewhere)
    expect(result.error?.message).toMatch(/replay work_start or worktree_claim/)
    expect(transitionCalls).toBe(0)
    expect(windows.has("session-1")).toBe(false)
  } finally {
    fs.rmSync(root, { recursive: true, force: true })
  }
})

test("a missing tool context authorizes nothing in the core", async () => {
  let transitionCalls = 0
  const invoke = async (toolName: string, args: { operation: string }): Promise<unknown> => {
    const key = `${toolName}.${args.operation}`
    if (key === "concord_work_trace.continuity") return continuityEnvelope()
    if (key === "concord_work_browse.scope") return scopeEnvelope()
    if (key === "concord_work_transition.workflow_action") { transitionCalls++; return coreOkEnvelope() }
    throw new Error(`unscripted ${key}`)
  }
  hostControlPlane().bind({
    get: async () => ({ data: { id: "session-1", directory: process.cwd(), metadata: { [MANAGED_TASK_SCOPE_KEY]: "managed" } }, response: new Response(null, { status: 200 }) }),
    post: async () => { throw new Error("dispatch does not move the host session") },
  })
  const windows = new DispatchWindows()
  const context = { ...contextFor(), directory: undefined } as any
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "idemp-missing-context-gate", lane_id: lane.id }, { context, invoke: invoke as any, credentials: testCredentials, windows })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("unauthorized_dispatch")
  expect(transitionCalls).toBe(0)
  expect(windows.has("session-1")).toBe(false)
})

test("dispatchLaneWorker retains the core's packet digest for completion", async () => {
  const request = { work_id: WORK_ID, expected_version: 3, idempotency_key: "idemp-7", lane_id: lane.id }
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
    if (toolName === "concord_work_trace") return continuityEnvelope()
    if (toolName === "concord_work_browse") return scopeEnvelope()
    if (toolName === "concord_work_transition") return coreOkEnvelope()
    throw new Error(`unscripted ${toolName}.${args.operation}`)
  }
  const windows = new DispatchWindows()
  const result = await dispatchLaneWorker(request, { context: contextFor(), invoke: invoke as any, credentials: testCredentials, windows })
  expect(result.outcome).toBe("ok")

  // CD-0067 D6: the adapter never computes the digest. The value the core
  // recorded is carried across the host's Task call and quoted by the dispatch
  // assertion at completion. The native selector from the directive binds.
  const selector = { subagent_type: `concord-${lane.id}`, prompt: dispatchAttemptID(request), description: "Run authorized worker" }
  await windows.bind(TASK_TOOL_ID, "session-1", selector, undefined, async () => process.cwd(), process.cwd())
  expect(windows.takeInFlight("session-1")?.packetDigest).toBe(CORE_PACKET_DIGEST)
})

// CD-0102 D1: the evidence-credential probe runs before the consumable window
// opens. A probe that fails admits no window and no worker execution; the
// post-authorization refusal keeps its own diagnosis and the coordinator's
// receipt names the operator recovery.
test("a failing evidence credential probe opens no window and instructs no native Task", async () => {
  const failingCredentials: CredentialStore = { async getPrivateKey() { throw new Error("credential vault is sealed") } }
  const invoke = async (toolName: string, args: { operation: string }): Promise<unknown> => {
    const key = `${toolName}.${args.operation}`
    if (key === "concord_work_trace.continuity") return continuityEnvelope()
    if (key === "concord_work_browse.scope") return scopeEnvelope()
    if (key === "concord_work_transition.workflow_action") return coreOkEnvelope()
    throw new Error(`unscripted ${key}`)
  }
  const windows = new DispatchWindows()
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "credential-fail", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, credentials: failingCredentials, windows })
  expect(result.outcome).toBe("error")
  expect(result.dispatch_state).toBeUndefined()
  expect(result.error?.kind).toBe("error")
  expect(result.error?.message).toContain("credential probe failed before worker execution")
  expect(result.error?.message).toContain("credential vault is sealed")
  expect(result.error?.recovery_action).toBe("contact_operator")
  expect(result.output).toBeUndefined()
  expect(windows.has("session-1")).toBe(false)
})

// A native Task can bind concurrently, so the window must not be consumable
// while the credential probe is still pending: a matching Task issued during
// the suspension finds no window, and a probe that then fails leaves none.
test("a suspended evidence credential probe admits no concurrent Task bind and a failing probe leaves no window", async () => {
  let settleProbe!: (privateKey: Promise<Uint8Array>) => void
  let probeStarted!: () => void
  const started = new Promise<void>(resolve => { probeStarted = resolve })
  const suspendedCredentials: CredentialStore = {
    getPrivateKey() {
      return new Promise<Uint8Array>((resolve) => { settleProbe = resolve; probeStarted() })
    },
  }
  const request = { work_id: WORK_ID, expected_version: 3, idempotency_key: "credential-suspended", lane_id: lane.id }
  const invoke = async (toolName: string, args: { operation: string }): Promise<unknown> => {
    const key = `${toolName}.${args.operation}`
    if (key === "concord_work_trace.continuity") return continuityEnvelope()
    if (key === "concord_work_browse.scope") return scopeEnvelope()
    if (key === "concord_work_transition.workflow_action") return coreOkEnvelope()
    throw new Error(`unscripted ${key}`)
  }
  const windows = new DispatchWindows()
  const pending = dispatchLaneWorker(request, { context: contextFor(), invoke: invoke as any, credentials: suspendedCredentials, windows })
  await started
  // The authorization committed, but no consumable window exists yet: a
  // concurrent native Task carrying the exact selector cannot bind.
  expect(windows.has("session-1")).toBe(false)
  const concurrentSelector = { subagent_type: `concord-${lane.id}`, prompt: dispatchAttemptID(request), description: "Run authorized worker" }
  await expect(windows.bind(TASK_TOOL_ID, "session-1", { ...concurrentSelector }, "concurrent-call", async () => process.cwd(), process.cwd())).rejects.toThrow("no authorized dispatch window")
  settleProbe(Promise.reject(new Error("credential store suspended then refused")))
  const result = await pending
  expect(result.outcome).toBe("error")
  expect(result.dispatch_state).toBeUndefined()
  expect(result.error?.message).toContain("credential probe failed before worker execution")
  expect(result.error?.message).toContain("suspended then refused")
  expect(windows.has("session-1")).toBe(false)
})

// CON-821 / CD-0034 on the lane route: a manifest the store would refuse at
// the evidence fold refuses here, before the core dispatch_worker action
// persists an authorized attempt and before any window opens. The provenance
// producer is the real one; only the core transport and the host control
// plane are fixtures, exactly as every other test in this file drives them.

// Snapshot the host-config environment the provenance producer reads and
// point it at a synthetic config directory, so the source count depends on
// the fixture alone.
function isolateProvenanceEnv(configDir: string): () => void {
  const previous = {
    OPENCODE_CONFIG_DIR: process.env.OPENCODE_CONFIG_DIR,
    OPENCODE_CONFIG: process.env.OPENCODE_CONFIG,
    CONCORD_HOST_INSTRUCTIONS: process.env.CONCORD_HOST_INSTRUCTIONS,
  }
  process.env.OPENCODE_CONFIG_DIR = configDir
  delete process.env.OPENCODE_CONFIG
  delete process.env.CONCORD_HOST_INSTRUCTIONS
  return () => {
    for (const [key, value] of Object.entries(previous)) {
      if (value === undefined) delete process.env[key]
      else process.env[key] = value
    }
  }
}

test("a provenance manifest over the store bound authorizes nothing in the core on the lane route", async () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "provenance-lane-"))
  fs.mkdirSync(path.join(root, "worktree"))
  const worktree = fs.realpathSync(path.join(root, "worktree"))
  const configDir = fs.mkdtempSync(path.join(os.tmpdir(), "provenance-lane-config-"))
  fs.mkdirSync(path.join(worktree, ".git"), { recursive: true })
  fs.mkdirSync(path.join(configDir, "corpus"), { recursive: true })
  // 30 corpus files through one absolute glob plus the 3 unenumerated
  // surfaces = 33 sources: one past the 32-source bound the store enforces.
  for (let index = 0; index < 30; index++) {
    fs.writeFileSync(path.join(configDir, "corpus", `rule-${String(index).padStart(2, "0")}.md`), `# corpus rule ${index}\n`)
  }
  fs.writeFileSync(path.join(configDir, "opencode.jsonc"), `{"instructions": [${JSON.stringify(`${configDir}/corpus/*.md`)}]}\n`)
  const restoreEnv = isolateProvenanceEnv(configDir)
  try {
    let workflowCalls = 0
    let evidenceCalls = 0
    const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
      const key = `${toolName}.${args.operation}`
      if (key === "concord_work_trace.continuity") return continuityEnvelope()
      if (key === "concord_work_browse.scope") return scopeEnvelope()
      if (key === "concord_work_transition.workflow_action") { workflowCalls++; return coreOkEnvelope(worktree) }
      throw new Error(`unscripted ${key}`)
    }
    hostControlPlane().bind({
      get: async ({ path: routePath }) => ({
        data: { id: routePath?.id, directory: worktree, metadata: { [MANAGED_TASK_SCOPE_KEY]: "managed" } },
        response: new Response(null, { status: 200 }),
      }),
      post: async () => { throw new Error("dispatch does not move the host session") },
    })
    const windows = new DispatchWindows()
    const context = { sessionID: "session-1", messageID: "message-1", agent: "agent-1", worktree, directory: worktree, abort: new AbortController().signal, ask: async () => {} } as any
    const result = await dispatchLaneWorker(
      { work_id: WORK_ID, expected_version: 3, idempotency_key: "provenance-overflow", lane_id: lane.id },
      { context, invoke: invoke as any, credentials: testCredentials, evidenceRunner: { async run() { evidenceCalls++; return { exitCode: 0, stdout: "", stderr: "" } } }, windows },
    )
    expect(result.outcome).toBe("error")
    expect(result.dispatch_state).toBeUndefined()
    expect(result.error?.message).toMatch(/provenance/i)
    expect(result.error?.message).toMatch(/\b32\b/)
    expect(result.error?.recovery_action).toBe("contact_operator")
    expect(result.error?.retry_safe).toBe(false)
    expect(workflowCalls).toBe(0)
    expect(evidenceCalls).toBe(0)
    expect(windows.has("session-1")).toBe(false)
  } finally {
    restoreEnv()
    fs.rmSync(root, { recursive: true, force: true })
    fs.rmSync(configDir, { recursive: true, force: true })
  }
})

// Every declared required reading resolves in the core before
// a worker spawns. The core owns the entire resolution — the adapter adds no
// second source resolver and no Git probe of its own. A typed core refusal
// that names a declared reading reaches the coordinator with its cause and
// recovery intact, spawns no native Task, and arms no window; a dispatch the
// core admits over its resolved declared readings spawns exactly one native
// Task.
test("TestDispatchRequiredReadingResolutionBeforeTask: a core declared-reading refusal spawns no native Task", async () => {
  let spawned = 0
  let workflowCalls = 0
  const invoke = async (toolName: string, args: { operation: string }): Promise<unknown> => {
    if (toolName === "concord_work_trace") return continuityEnvelope()
    if (toolName === "concord_work_browse") return scopeEnvelope()
    if (toolName === "concord_work_transition") {
      workflowCalls++
      return coreErrorEnvelope(
        "projection_not_found",
        'required reading 0 (project-repo/internal/store/work_context.go@' + "a".repeat(40) + "): the declared commit does not resolve in the declared Project's repository",
      )
    }
    throw new Error(`unscripted ${toolName}.${args.operation}`)
  }
  const runner: DispatchRunner = { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } }
  const windows = new DispatchWindows()
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 1, idempotency_key: "idemp-reading-refusal", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, runner, windows })
  expect(result.outcome).toBe("error")
  expect(result.error?.retry_safe).toBe(false)
  expect(result.error?.message).toContain("required reading 0")
  const core = (result.error?.details ?? {}) as Record<string, unknown>
  expect((core.core_error as Record<string, unknown>).kind).toBe("projection_not_found")
  expect(workflowCalls).toBe(1)
  expect(spawned).toBe(0)
  expect(windows.has("session-1")).toBe(false)
})

test("TestDispatchRequiredReadingResolutionBeforeTask: a core-admitted dispatch over its resolved declared readings arms exactly one native Task window", async () => {
  let spawned = 0
  let workflowCalls = 0
  const invoke = async (toolName: string, args: { operation: string }): Promise<unknown> => {
    if (toolName === "concord_work_trace") return continuityEnvelope()
    if (toolName === "concord_work_browse") return scopeEnvelope()
    if (toolName === "concord_work_transition") {
      workflowCalls++
      return coreOkEnvelope()
    }
    throw new Error(`unscripted ${toolName}.${args.operation}`)
  }
  const runner: DispatchRunner = { async run() { spawned++; return { exitCode: 0, stdout: runOutput(), stderr: "" } } }
  const windows = new DispatchWindows()
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "idemp-reading-admit", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, credentials: testCredentials, runner, windows })
  // The core resolved the declared readings and authorized the dispatch: the
  // host runs exactly one native Task from the one armed window. The adapter
  // itself spawns nothing (CD-0102 D5) and adds no second source resolution.
  expect(result.outcome).toBe("ok")
  expect(result.dispatch_state).toBe("awaiting_worker")
  expect(workflowCalls).toBe(1)
  expect(spawned).toBe(0)
  expect(windows.has("session-1")).toBe(true)
})

// An oversized projection travels as lossless projection pages of one
// canonical JSON document (CD-0016 as amended). The dispatch anchors —
// product identity, step, instance absence, correction source — are
// authority-bearing, so the shared reconstruction helper joins every page
// before any of them is read. The fixtures mirror the Go agent's page
// emission: whole-code-point UTF-8 byte pages of the inline result's
// canonical JSON, chained by the signed next_cursor the envelope carries.
const pagedUtf8 = new TextEncoder()
const pagedDocumentOf = (result: Record<string, unknown>) => JSON.stringify(result)
const pagedDigestOf = (document: string) => `sha256:${createHash("sha256").update(pagedUtf8.encode(document)).digest("hex")}`

const PAGED_PREMISE = `境content across page boundaries 🎯 ${"p".repeat(1600)}`
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
    source_event_id: "record_work_context-work-dispatch-40:semantic",
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

const pagedResult = (): Record<string, unknown> => ({
  work_id: WORK_ID,
  pinned: {
    product_identity: [PRODUCT_ID],
    workflow_step: WORKFLOW_STEP,
    contract: { version: 1, premise: PAGED_PREMISE, outcome_predicates: [{ predicate_id: "predicate:primary", ordinal: 0, outcome_kind: OUTCOME_KIND, outcome_payload: OUTCOME_PAYLOAD }], required_evidence: [], route_conventions: [], spec_mandate: [], changes_product_truth: false },
    spec_mandate: [],
    pending_operator_decision: null,
    latest_checkpoint: null,
    unresolved_failure: null,
    work_pin: { correction: PAGED_CORRECTION },
    work_context: OVERSIZED_CONTEXT,
  },
  boundaries: { count: 0, items: [], next_cursor: null, watermark: "seq:1" },
  typed_availability: { restart: "unavailable", reason: "typed restart is deliberately excluded (CD-0027); pinned continuity is re-derived per call" },
  pending_messages: 0,
  observations: Array.from({ length: 16 }, (_, index) => oversizedObservation(index)),
  observations_total: 16,
  observations_read: { tool: "concord_work_trace", operation: "observations", input: { work_id: WORK_ID } },
})

// pagedChain cuts the canonical document into whole-code-point pages of at
// most limitBytes UTF-8 bytes and renders one page envelope per fragment.
const pagedChain = (result: Record<string, unknown>, limitBytes: number) => {
  const document = pagedDocumentOf(result)
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
  const envelopes = pages.map((page, index) =>
    envelope({
      tool: "concord_work_trace", operation: "continuity", outcome: "ok", authority: "authoritative", freshness: null,
      resolved_scope: { product_id: PRODUCT_ID, project_ids: [PRODUCT_ID], scope_version: "sha256:" + "b".repeat(64) },
      next_cursor: index === pages.length - 1 ? null : `cursor-${index + 1}`,
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
          document_complete: index === pages.length - 1,
        },
        next_kind: index === pages.length - 1 ? null : "projection",
      },
    }),
  )
  return { document, envelopes }
}

// pagedScript serves the continuity chain by cursor token — repeated reads
// replay the same unchanged pages — and answers the scope read and the
// managed dispatch the way the happy path does.
const pagedScript = (chain: unknown[], workflowInput: { value?: Record<string, unknown> }) => {
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
    if (key === "concord_work_browse.scope") return scopeEnvelope()
    if (key === "concord_work_transition.workflow_action") { workflowInput.value = args.input; return coreOkEnvelope() }
    throw new Error(`unscripted ${key}`)
  }
  return Object.assign(invoke, { seen: () => seen })
}

test("dispatch from a paged oversized item uses request identity without a retry count", async () => {
  const result = pagedResult()
  const input = { work_id: WORK_ID, expected_version: 3, idempotency_key: "paged-dispatch", lane_id: lane.id }
  // The canonical document is a genuinely oversized projection: its UTF-8
  // length exceeds the whole-envelope cap the inline branch must fit.
  expect(pagedUtf8.encode(pagedDocumentOf(result)).length).toBeGreaterThan(51_200)
  const { envelopes } = pagedChain(result, 20_000)
  expect(envelopes.length).toBeGreaterThan(2)
  const workflowInput: { value?: Record<string, unknown> } = {}
  const windows = new DispatchWindows()
  const outcome = await dispatchLaneWorker(input, { context: contextFor(), invoke: pagedScript(envelopes, workflowInput) as any, credentials: testCredentials, windows })
  expect(outcome.outcome, JSON.stringify(outcome)).toBe("ok")
  const packet = (workflowInput.value!.fields as Record<string, unknown>).worker_packet as AgentLanePacket
  // Transport and correction history cannot change request identity.
  expect(packet.step_id).toBe(WORKFLOW_STEP)
  expect(packet.attempt_id).toBe(dispatchAttemptID(input))
  expect(packet.inputs.correction).toEqual(PAGED_CORRECTION)
  // The joined packet is byte-identical to the same dispatch served the
  // inline result: reconstruction changes transport, never packet content.
  const inlineWorkflow: { value?: Record<string, unknown> } = {}
  const inlineInvoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }) => {
    const key = `${toolName}.${args.operation}`
    if (key === "concord_work_trace.continuity") return continuityEnvelope({ pinned: { contract: (result.pinned as Record<string, unknown>).contract, work_pin: (result.pinned as Record<string, unknown>).work_pin } })
    if (key === "concord_work_browse.scope") return scopeEnvelope()
    if (key === "concord_work_transition.workflow_action") { inlineWorkflow.value = args.input; return coreOkEnvelope() }
    throw new Error(`unscripted ${key}`)
  }
  const inlineOutcome = await dispatchLaneWorker(input, { context: contextFor(), invoke: inlineInvoke as any, credentials: testCredentials, windows: new DispatchWindows() })
  expect(inlineOutcome.outcome, JSON.stringify(inlineOutcome)).toBe("ok")
  const inlinePacket = (inlineWorkflow.value!.fields as Record<string, unknown>).worker_packet as AgentLanePacket
  expect(JSON.stringify(packet)).toBe(JSON.stringify(inlinePacket))
  expect(validateAgentLanePacket(packet)).toBe(true)
  expect(windows.has("session-1")).toBe(true)
})

test("new request identity does not depend on correction history", async () => {
  const attempts: string[] = []
  const request = { work_id: WORK_ID, expected_version: 3, idempotency_key: "identity-first", lane_id: lane.id }
  for (const [sourceSeq, key] of [[1, "identity-first"], [2, "identity-first"], [2, "identity-next"]] as const) {
    const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }) => {
      if (toolName === "concord_work_trace") return continuityEnvelope({ pinned: { work_pin: { correction: { ...PAGED_CORRECTION, source_event_id: `failure:${sourceSeq}`, source_event_seq: sourceSeq } } } })
      if (toolName === "concord_work_browse") return scopeEnvelope()
      if (toolName === "concord_work_transition") {
        attempts.push((args.input!.fields as Record<string, unknown>).attempt_id as string)
        return coreErrorEnvelope("operation_conflict", "reconcile the unresolved launch outcome")
      }
      throw new Error(`unscripted ${toolName}.${args.operation}`)
    }
    const outcome = await dispatchLaneWorker({ ...request, idempotency_key: key }, { context: contextFor(), invoke: invoke as any })
    expect(outcome.error?.details?.core_error).toEqual({ kind: "operation_conflict", retry_safe: false, recovery_action: { kind: "reconcile_operation" }, effect_state: "none", message: "reconcile the unresolved launch outcome" })
    expect(outcome.error?.retry_safe).toBe(false)
    expect(outcome.error?.recovery_action).toBe("reconcile_operation")
  }
  expect(attempts).toHaveLength(3)
  expect(attempts[0]).toBe(dispatchAttemptID(request))
  expect(attempts[1]).toBe(attempts[0])
  expect(attempts[2]).not.toBe(attempts[0])
})

test("a broken page sequence refuses dispatch without authorizing an attempt", async () => {
  const result = pagedResult()
  const cases: Array<[string, string, (chain: unknown[]) => void]> = [
    ["missing page", "threw", (chain) => { chain.splice(1) }],
    ["duplicated page", "offset", (chain) => {
      const copy = JSON.parse(JSON.stringify(chain[1]))
      ;(copy as any).next_cursor = "cursor-dup"
      chain.splice(2, 0, copy)
    }],
    ["reordered page", "offset", (chain) => {
      const swapped = chain[1]
      chain[1] = chain[2]
      chain[2] = swapped
    }],
    ["changed digest", "digest", (chain) => { ((chain[1] as any).result.projection_page as Record<string, unknown>).digest = `sha256:${"f".repeat(64)}` }],
    ["joined digest mismatch", "digest", (chain) => {
      // Same UTF-8 byte length, different bytes: every page's metadata stays
      // consistent and only the joined-byte hash diverges from the digest
      // the pages declare.
      const last = chain[chain.length - 1] as any
      const page = last.result.projection_page as Record<string, unknown>
      const before = page.data as string
      const after = before.replace('"observations_total":16', '"observations_total":17')
      expect(after.length).toBe(before.length)
      expect(after).not.toBe(before)
      page.data = after
    }],
    ["work mismatch", "work", (chain) => { ((chain[1] as any).result as Record<string, unknown>).work_id = "work-elsewhere" }],
    ["reconstructed work mismatch", "names work work-mismatch", (chain) => {
      // The page headers keep naming the requested work. The corruption
      // lands in the first fragment's actual bytes — the document's
      // top-level work_id — and the digest is recomputed from the mutated
      // fragments, so every metadata check passes and only the final
      // parsed-work check can refuse.
      const joinedBefore = chain.map((envelope) => (envelope as any).result.projection_page.data as string).join("")
      const corrupted = joinedBefore.replace('"work_id":"work-dispatch"', '"work_id":"work-mismatch"')
      expect(corrupted.length).toBe(joinedBefore.length)
      expect(corrupted).not.toBe(joinedBefore)
      const first = (chain[0] as any).result.projection_page as Record<string, unknown>
      const mutated = (first.data as string).replace('"work_id":"work-dispatch"', '"work_id":"work-mismatch"')
      expect(mutated.length).toBe((first.data as string).length)
      expect(mutated).not.toBe(first.data)
      first.data = mutated
      expect(chain.map((envelope) => (envelope as any).result.projection_page.data as string).join("")).toBe(corrupted)
      const digest = pagedDigestOf(corrupted)
      for (const envelope of chain) ((envelope as any).result.projection_page as Record<string, unknown>).digest = digest
    }],
    ["incomplete final page", "complete", (chain) => {
      const last = chain[chain.length - 1] as any
      const page = last.result.projection_page as Record<string, unknown>
      page.data = (page.data as string).slice(0, 8)
    }],
  ]
  for (const [name, fragment, mutate] of cases) {
    const { envelopes } = pagedChain(result, 20_000)
    const chain: unknown[] = envelopes
    mutate(chain)
    const workflowInput: { value?: Record<string, unknown> } = {}
    let spawned = 0
    const runner: DispatchRunner = { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } }
    const windows = new DispatchWindows()
    const invoke = pagedScript(chain, workflowInput)
    const outcome = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: `paged-refusal-${name}`, lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, credentials: testCredentials, runner, windows })
    expect(outcome.outcome, `${name}: ${JSON.stringify(outcome)}`).toBe("error")
    expect(outcome.error?.kind, name).toBe("transport_failure")
    expect(outcome.error?.message, name).toContain("continuity")
    expect(outcome.error?.message, name).toContain(fragment)
    expect(invoke.seen(), name).not.toContain("concord_work_transition.workflow_action")
    expect(spawned, name).toBe(0)
    expect(windows.has("session-1"), name).toBe(false)
  }
})

test("a throwing initial continuity read refuses dispatch without authorizing an attempt", async () => {
  let continuityCalls = 0
  let transitionCalls = 0
  let spawned = 0
  const invoke = async (toolName: string, args: { operation: string }): Promise<unknown> => {
    const key = `${toolName}.${args.operation}`
    if (key === "concord_work_trace.continuity") { continuityCalls++; throw new Error("the synthetic core is unreachable") }
    if (key === "concord_work_transition.workflow_action") { transitionCalls++; return coreOkEnvelope() }
    throw new Error(`unscripted ${key}`)
  }
  const runner: DispatchRunner = { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } }
  const windows = new DispatchWindows()
  const outcome = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "continuity-initial-throw", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, credentials: testCredentials, runner, windows })
  expect(outcome.outcome, JSON.stringify(outcome)).toBe("error")
  expect(outcome.error?.kind).toBe("transport_failure")
  expect(outcome.error?.message).toContain("continuity")
  expect(outcome.error?.message).toContain("unreachable")
  expect(continuityCalls).toBe(1)
  expect(transitionCalls).toBe(0)
  expect(spawned).toBe(0)
  expect(windows.has("session-1")).toBe(false)
})

test("a throwing continuation read refuses dispatch without authorizing an attempt", async () => {
  const { envelopes } = pagedChain(pagedResult(), 20_000)
  let continuityCalls = 0
  let transitionCalls = 0
  let spawned = 0
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
    const key = `${toolName}.${args.operation}`
    if (key === "concord_work_trace.continuity") {
      continuityCalls++
      const cursor = (args.input as any)?.page?.cursor ?? null
      if (cursor === null) return envelopes[0]
      throw new Error("the continuation core call failed")
    }
    if (key === "concord_work_transition.workflow_action") { transitionCalls++; return coreOkEnvelope() }
    throw new Error(`unscripted ${key}`)
  }
  const runner: DispatchRunner = { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } }
  const windows = new DispatchWindows()
  const outcome = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "continuation-throw", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, credentials: testCredentials, runner, windows })
  expect(outcome.outcome, JSON.stringify(outcome)).toBe("error")
  expect(outcome.error?.kind).toBe("transport_failure")
  expect(outcome.error?.message).toContain("continuity")
  expect(outcome.error?.message).toContain("continuation core call failed")
  expect(continuityCalls).toBe(2)
  expect(transitionCalls).toBe(0)
  expect(spawned).toBe(0)
  expect(windows.has("session-1")).toBe(false)
})
