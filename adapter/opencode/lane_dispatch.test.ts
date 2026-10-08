import { test, expect, mock, beforeEach, afterEach } from "bun:test"
import fs from "node:fs"
import * as os from "node:os"
import path from "node:path"
import { hostControlPlane, MANAGED_TASK_SCOPE_KEY } from "./move-session"
import { resetClaimedWorktrees } from "./claimed-worktree"

beforeEach(() => {
  // Another file's landing tests can arm a claim that leaks into this run;
  // dispatch checks here must not depend on file order.
  resetClaimedWorktrees()
  hostControlPlane().bind({
    get: async ({ path }) => ({ data: { id: path?.id, directory: process.cwd(), metadata: { [MANAGED_TASK_SCOPE_KEY]: "managed" } }, response: new Response(null, { status: 200 }) }),
    post: async () => { throw new Error("dispatch does not move the host session") },
  })
})
afterEach(() => hostControlPlane().bind(undefined))

// The lane dispatcher reaches the Opencode plugin through ToolContext (typed
// only) and the dispatch worker through dispatchWorker, but the test never
// executes the real plugin path. The stub mirrors concord.test.ts so the
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

const { agentLanes, agentUtilities } = await import("./generated-agent-lanes")
const { validateAgentLanePacket } = await import("./dispatch")
import { DispatchWindows, TASK_TOOL_ID } from "./dispatch-window"
import type { AgentLanePacket, DispatchRunner } from "./dispatch"
import type { CredentialStore } from "./credentials"
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
    pending_messages: 0, observations: [],
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
  schema_version: "1.0", attempt_id: expectedAttemptId, lane_id: lane.id, lane_version: lane.version, lane_digest: lane.digest,
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

const contextFor = () => ({ sessionID: "session-1", messageID: "message-1", agent: "agent-1", worktree: process.cwd(), directory: process.cwd(), abort: new AbortController().signal, ask: async () => {} }) as any

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

test("happy path: continuity → packet → core ok → spawn with stubbed runners reaches the runner", async () => {
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
  const runner: DispatchRunner = {
    async run(argv) {
      if (argv[1] === "run") return { exitCode: 0, stdout: runOutput(), stderr: "" }
      if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(), stderr: "" }
      return { exitCode: 0, stdout: "", stderr: "" }
    },
  }
  let evidenceCalls = 0
  const evidenceRunner: DispatchRunner = { async run() { evidenceCalls++; return { exitCode: 0, stdout: "", stderr: "" } } }
  const windows = new DispatchWindows()
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "idemp-1", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, credentials: testCredentials, runner, evidenceRunner, windows })
  expect(result.outcome).toBe("ok")
  expect(result.dispatch_state).toBe("awaiting_worker")
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
  // evidence. Both belong to completion (CD-0102 D5).
  expect(evidenceCalls).toBe(0)
  expect(windows.has("session-1")).toBe(true)
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
  expect(packet.inputs.context).toBe(NARRATIVE)
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

for (const refusal of [
  { kind: "approval_invalid", recovery: "request_approval", retry: false },
  { kind: "internal_error", recovery: "contact_operator", retry: false },
  { kind: "timeout", recovery: "retry_same_request", retry: true },
  { kind: "budget_refused", recovery: "adjust_budget", retry: false },
  { kind: "version_conflict", recovery: "reread_entities", retry: false },
] as const) {
  test(`core ${refusal.kind} retains its cause and recovery without an authorization denial`, async () => {
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
    expect(result.error?.recovery_action).toBe(refusal.recovery === "reread_entities" ? "contact_operator" : refusal.recovery)
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
      return coreOkEnvelope()
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

// The core mints the standard approval challenge when a dispatch faces the
// escalated correction wall (CD-0148). The lane refusal keeps the challenge
// metadata and typed summary in its error details, so the orchestrator can
// obtain one operator approval and re-invoke with its approval_ref. No spawn happens until the
// approved resubmission returns ok.
test("an escalated correction challenge forwards the failed attempt bindings for operator approval", async () => {
  const challengeDetails = {
    approval_ref: "challenge-1", operation_digest: "sha256:" + "a".repeat(64),
    work_id: WORK_ID, action_id: "dispatch_worker", contract_version: "1", selected_choice: "", premise_summary: "approved retry objective",
  }
  const consequenceSummary = {
    tool: "concord_work_transition", operation: "workflow_action", consequence: "workflow",
    operation_digest: challengeDetails.operation_digest,
    scope: ["failed_attempt_id:attempt:work-dispatch:3", "product_id:product-dispatch", "work_ids:work-dispatch"],
    versions: ["contract:1", "failed_attempt_epoch:3", "work:3"], expires_at: "2026-10-01T00:00:00Z",
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
  const request = { work_id: WORK_ID, expected_version: 3, idempotency_key: "escalated-approval", lane_id: lane.id }
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
  const invoke = async (toolName: string, args: { operation: string; input?: Record<string, unknown> }): Promise<unknown> => {
    if (toolName === "concord_work_trace") return continuityEnvelope()
    if (toolName === "concord_work_browse") return scopeEnvelope()
    if (toolName === "concord_work_transition") return coreOkEnvelope()
    throw new Error(`unscripted ${toolName}.${args.operation}`)
  }
  const windows = new DispatchWindows()
  const result = await dispatchLaneWorker({ work_id: WORK_ID, expected_version: 3, idempotency_key: "idemp-7", lane_id: lane.id }, { context: contextFor(), invoke: invoke as any, credentials: testCredentials, windows })
  expect(result.outcome).toBe("ok")

  // CD-0067 D6: the adapter never computes the digest. The value the core
  // recorded is carried across the host's Task call and quoted by the dispatch
  // assertion at completion.
  await windows.bind(TASK_TOOL_ID, "session-1", { subagent_type: "general", prompt: "x" }, undefined, async () => process.cwd(), process.cwd())
  expect(windows.takeInFlight("session-1")?.packetDigest).toBe(CORE_PACKET_DIGEST)
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
