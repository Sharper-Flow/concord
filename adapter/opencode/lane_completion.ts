// Lane completion is the second adapter entry point CD-0102 D5 names. The host
// runs the worker between dispatch and completion, so the only place the
// adapter sees the finished worker is the `tool.execute.after` hook for the
// Task call the dispatch window bound. This module takes that hook's input,
// drains the in-flight attempt, and admits the result through
// completeWorkerAttempt, which exports the worker session, reads the executing
// model and agent, uses the forwarded worker directory, signs, and records the attempt.
//
// The hook contract offers no return channel and a thrown error fails the tool
// call in place of its result. A completion refusal is therefore appended to
// the tool output as a typed element the coordinator reads, and the hook never
// throws: the worker's own result stays visible either way.
import { createHash } from "node:crypto"
import { completeWorkerAttempt, failWorkerAttempt, abandonWorkerAttempt, type AgentResultEnvelope, type DispatchRunner } from "./dispatch"
import type { CredentialStore } from "./credentials"
import { dispatchWindows, DispatchWindows, TASK_TOOL_ID, type DispatchRecord } from "./dispatch-window"
import type { SessionReader } from "./move-session"
import { agentLanes } from "./generated-agent-lanes"

export interface LaneCompletionInput {
  tool: string
  sessionID: string
  callID: string
  args: unknown
}

export interface LaneCompletionOutput {
  title: string
  output: string
  metadata: unknown
}

export interface LaneCompletionDeps {
  windows?: DispatchWindows
  credentials?: CredentialStore
  runner?: DispatchRunner
  evidenceRunner?: DispatchRunner
  sessionReader?: SessionReader
  concordBinary?: string
  signal?: AbortSignal
}

// The element the coordinator reads after the host's task wrapper. It carries
// the completion envelope so a refusal names its kind and recovery action.
const ATTEMPT_ELEMENT = "concord_attempt"

function renderAttempt(envelope: AgentResultEnvelope): string {
  const summary: Record<string, unknown> = {
    outcome: envelope.outcome,
    lane: envelope.lane,
    readback_model: envelope.readback_model,
    session_id: envelope.session_id,
  }
  // A completed attempt names the one assigned result its completion
  // disposes, so the coordinator reads the bounded disposition directly.
  if (envelope.assigned_result) summary.assigned_result = envelope.assigned_result
  if (envelope.error) summary.error = envelope.error
  return `\n<${ATTEMPT_ELEMENT}>\n${JSON.stringify(summary)}\n</${ATTEMPT_ELEMENT}>`
}

// A retained attempt that cannot settle reports the reconcile route rather
// than a bare refusal.
function unavailableEnvelope(pending: DispatchRecord, message: string): AgentResultEnvelope {
  return {
    schema_version: "1.0", outcome: "error",
    lane: { id: pending.packet.lane_id, version: pending.packet.lane_version, digest: pending.packet.lane_digest },
    agent: `concord-${pending.packet.lane_id}`, readback_model: null, session_id: null,
    error: { kind: "invalid_input", retry_safe: false, recovery_action: "reconcile_operation", message },
  }
}

export async function completeDispatchedWorker(input: LaneCompletionInput, output: LaneCompletionOutput, deps: LaneCompletionDeps = {}): Promise<void> {
  if (input.tool !== TASK_TOOL_ID) return
  const windows = deps.windows ?? dispatchWindows()
  const record = windows.takeInFlight(input.sessionID, input.callID)
  if (!record) return
  // The packet pins the lane's version and digest, so completion binds to the
  // definition the dispatch authorized rather than to whatever the registry
  // carries now. A registry that drifted between dispatch and completion — an
  // upgrade mid-flight — would otherwise sign its own version and digest onto
  // the attempt, recording the worker as having run a contract it never
  // received. The attempt row is written from those same values, so nothing
  // downstream can catch the substitution.
  const lane = agentLanes.find((candidate) => candidate.id === record.packet.lane_id && candidate.version === record.packet.lane_version && candidate.digest === record.packet.lane_digest)
  if (!lane) {
    output.output += renderAttempt({ schema_version: "1.0", outcome: "error", lane: { id: record.packet.lane_id, version: record.packet.lane_version, digest: record.packet.lane_digest }, agent: `concord-${record.packet.lane_id}`, readback_model: null, session_id: null, error: { kind: "invalid_input", retry_safe: false, recovery_action: "contact_operator", message: "in-flight attempt names a lane at a version and digest the registry does not carry" } })
    return
  }
  const signal = deps.signal ?? new AbortController().signal
  let envelope: AgentResultEnvelope
  try {
    envelope = await completeWorkerAttempt(lane, record.packet, output.output, { credentials: deps.credentials, runner: deps.runner, evidenceRunner: deps.evidenceRunner, sessionReader: deps.sessionReader, concordBinary: deps.concordBinary, packetDigest: record.packetDigest, workerDirectory: record.workerDirectory }, signal)
  } catch (error) {
    envelope = { schema_version: "1.0", outcome: "error", lane: { id: lane.id, version: lane.version, digest: lane.digest }, agent: `concord-${lane.id}`, readback_model: null, session_id: null, error: { kind: "error", retry_safe: false, recovery_action: "reconcile_operation", message: String(error).slice(0, 2048) } }
  }
  output.output += renderAttempt(envelope)
}

function object(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

// Cancellation can end Task without tool.execute.after. Consume only the
// terminal error for the exact host call that consumed the dispatch window.
// A spawn that dies before any result or error part takes a third route: the
// host publishes session.error, the tool part stays running, and neither the
// completion hook nor an error part ever arrives. That event settles through
// failSpawnWithoutPartEvent below.
export async function failDispatchedWorker(event: unknown, deps: LaneCompletionDeps = {}): Promise<AgentResultEnvelope | null> {
  if (!object(event) || !object(event.properties)) return null
  if (event.type === "session.error") return failSpawnWithoutPartEvent(event.properties, deps)
  if (event.type !== "message.part.updated") return null
  const part = event.properties.part
  if (!object(part) || part.type !== "tool" || part.tool !== TASK_TOOL_ID || typeof part.sessionID !== "string" || typeof part.callID !== "string") return null
  const state = part.state
  if (!object(state) || state.status !== "error" || typeof state.error !== "string") return null
  const windows = deps.windows ?? dispatchWindows()
  const pending = windows.inFlight(part.sessionID, part.callID)
  if (!pending) return null
  const lane = agentLanes.find((candidate) => candidate.id === pending.packet.lane_id && candidate.version === pending.packet.lane_version && candidate.digest === pending.packet.lane_digest)
  if (!lane) return unavailableEnvelope(pending, "in-flight attempt names a lane the registry does not carry")
  const metadata = state.metadata
  if (!object(metadata) || typeof metadata.sessionId !== "string" || !/^ses_[a-zA-Z0-9]+$/.test(metadata.sessionId)) {
    return unavailableEnvelope(pending, "cancelled Task has no host child session identity; retain the in-flight attempt for reconciliation")
  }
  if (!windows.claimSettlement(part.sessionID, part.callID)) return null
  const sessionID = part.sessionID
  const callID = part.callID
  try {
    const envelope = await failWorkerAttempt(lane, pending.packet, metadata.sessionId, state.error, {
      credentials: deps.credentials, runner: deps.runner, evidenceRunner: deps.evidenceRunner, sessionReader: deps.sessionReader, concordBinary: deps.concordBinary, packetDigest: pending.packetDigest, workerDirectory: pending.workerDirectory,
    }, deps.signal ?? new AbortController().signal, () => windows.finishSettlement(sessionID, callID))
    // A recorded failure already dropped the record with its claim. A refused
    // write left the record retained: the claim releases into the refused
    // state, so a repeat event cannot re-attempt the write and the
    // worker_abandon route the in-flight refusal names stays live for the
    // coordinator.
    if (windows.inFlight(sessionID, callID) !== null) windows.refuseSettlement(sessionID)
    return envelope
  } catch (error) {
    windows.refuseSettlement(sessionID)
    return unavailableEnvelope(pending, String(error).slice(0, 2048))
  }
}

// A spawn that dies before any result or error part leaves the in-flight
// record with no settle path: the completion hook never fires and the tool
// part never reaches an error state, so the host's session.error event on the
// parent session is the only observable. The record names no child session,
// so the settle route is the abandonment the dispatch refusal names: the core
// closes the attempt from its dispatch record and the retained record drops.
// When the abandonment cannot be recorded, the returned envelope carries the
// reason, the claim releases, and the record stays retained for the
// coordinator's worker_abandon route.
async function failSpawnWithoutPartEvent(properties: Record<string, unknown>, deps: LaneCompletionDeps): Promise<AgentResultEnvelope | null> {
  const sessionID = properties.sessionID
  if (typeof sessionID !== "string") return null
  const windows = deps.windows ?? dispatchWindows()
  const pending = windows.inFlightAttempt(sessionID)
  if (!pending) return null
  // An aborted worker is cancellation, not a spawn failure: the host moves
  // the tool part to an error state, and the part-event path settles it with
  // the child session identity.
  const errorName = object(properties.error) ? properties.error.name : undefined
  if (errorName === "MessageAbortedError") return null
  const lane = agentLanes.find((candidate) => candidate.id === pending.packet.lane_id && candidate.version === pending.packet.lane_version && candidate.digest === pending.packet.lane_digest)
  if (!lane) return unavailableEnvelope(pending, "in-flight attempt names a lane the registry does not carry")
  const detail = `spawn failed before any result or error part; closing the attempt as abandoned: ${spawnFailureDiagnostic(properties.error)}`
  if (!windows.claimSettlement(sessionID)) return null
  // The event identity derives from the attempt and the diagnostic, so a
  // repeated event replays the same abandonment instead of recording a new one.
  const token = createHash("sha256").update(`spawn-failure-without-part-event\0${pending.packet.work_id}\0${pending.packet.attempt_id}\0${detail}`).digest("hex")
  try {
    const envelope = await abandonWorkerAttempt(lane, pending.packet, detail, {
      credentials: deps.credentials, runner: deps.runner, evidenceRunner: deps.evidenceRunner, concordBinary: deps.concordBinary,
      abandonEventID: `worker-abandon-${token}`, abandonNonce: token,
    }, deps.signal ?? new AbortController().signal, () => windows.finishSettlement(sessionID))
    // A recorded abandonment already dropped the record. A refused one left it
    // retained, so the claim releases and the coordinator's worker_abandon
    // route stays live for it.
    if (windows.inFlightAttempt(sessionID) !== null) windows.unclaimSettlement(sessionID)
    return envelope
  } catch (error) {
    windows.unclaimSettlement(sessionID)
    return unavailableEnvelope(pending, String(error).slice(0, 2048))
  }
}

function spawnFailureDiagnostic(error: unknown): string {
  if (object(error)) {
    const data = object(error.data) && typeof error.data.message === "string" ? error.data.message : undefined
    const message = data ?? (typeof error.message === "string" ? error.message : undefined)
    if (message !== undefined) return message
    return JSON.stringify(error)
  }
  if (typeof error === "string" && error.length > 0) return error
  return "the host published no readable diagnostic"
}
