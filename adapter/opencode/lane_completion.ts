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
import { boundedTextPrefix, completeWorkerAttempt, failWorkerAttempt, abandonWorkerAttempt, type AgentResultEnvelope, type DispatchRunner } from "./dispatch"
import type { CredentialStore } from "./credentials"
import { dispatchWindows, DispatchWindows, TASK_TOOL_ID, type DispatchRecord } from "./dispatch-window"
import type { SessionReader } from "./move-session"
import { laneForIdentity } from "./generated-agent-lanes"

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
  providerCauses?: ProviderCauses
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
  // The optional base comparison is informational evidence: it reaches the
  // coordinator when the worker reported one, and no route reads it.
  if (envelope.base_comparison) summary.base_comparison = envelope.base_comparison
  // The per-predicate tie: which obligation discharged which declared
  // predicate ids, read back from the admitted evidence so the coordinator
  // sees the tie without re-deriving it from the worker output.
  if (envelope.predicate_discharge) summary.predicate_discharge = envelope.predicate_discharge
  // The typed review summary (CD-0197): the verdict and per-severity counts
  // a completed review report carried. It is report content only and records
  // no workflow verdict.
  if (envelope.review) summary.review = envelope.review
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
  const lane = laneForIdentity(record.packet.lane_id, record.packet.lane_version, record.packet.lane_digest)
  if (!lane) {
    output.output += renderAttempt({ schema_version: "1.0", outcome: "error", lane: { id: record.packet.lane_id, version: record.packet.lane_version, digest: record.packet.lane_digest }, agent: `concord-${record.packet.lane_id}`, readback_model: null, session_id: null, error: { kind: "invalid_input", retry_safe: false, recovery_action: "contact_operator", message: "in-flight attempt names a lane at a version and digest the registry does not carry" } })
    return
  }
  const signal = deps.signal ?? new AbortController().signal
  let envelope: AgentResultEnvelope
  try {
    envelope = await completeWorkerAttempt(lane, record.packet, output.output, { credentials: deps.credentials, runner: deps.runner, evidenceRunner: deps.evidenceRunner, sessionReader: deps.sessionReader, concordBinary: deps.concordBinary, packetDigest: record.packetDigest, workerDirectory: record.workerDirectory, capturedProvenance: record.provenance }, signal)
  } catch (error) {
    envelope = { schema_version: "1.0", outcome: "error", lane: { id: lane.id, version: lane.version, digest: record.packet.lane_digest }, agent: `concord-${lane.id}`, readback_model: null, session_id: null, error: { kind: "error", retry_safe: false, recovery_action: "reconcile_operation", message: String(error).slice(0, 2048) } }
  }
  output.output += renderAttempt(envelope)
}

function object(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

// The host ends a provider-broken worker with the bare tool string "Task
// cancelled", so the recorded failure detail loses the cause. The cause rides
// the same event stream this module already drains: session.status retry
// events carry the provider message the host retried on, and the child
// session's terminal message carries a typed info.error. This hold keeps the
// last observed cause per session identity until the cancelled Task settles,
// so the failure record can name a rate limit instead of a bare cancel.
//
// The hold is keyed by session identity because a retry event and the
// cancelled tool part name no shared call: the child session id is the only
// join. Events for every session in this process pass through, so the hold is
// capacity-bounded with oldest-entry eviction, and a settle consumes the entry
// it read.
export class ProviderCauses {
  static readonly CAPACITY = 64
  readonly #held = new Map<string, { retry?: string; info?: string }>()

  record(sessionID: string, cause: { source: "retry"; message: string } | { source: "info"; message: string }): void {
    const held = this.#held.get(sessionID) ?? {}
    if (cause.source === "retry") held.retry = cause.message
    else held.info = cause.message
    // Re-insert to refresh recency, so a live worker session's cause survives
    // eviction over idle identities.
    this.#held.delete(sessionID)
    this.#held.set(sessionID, held)
    while (this.#held.size > ProviderCauses.CAPACITY) {
      const oldest = this.#held.keys().next().value
      if (oldest === undefined) break
      this.#held.delete(oldest)
    }
  }

  // take returns the session's provider cause — a retry message when one was
  // observed, otherwise the terminal message error — and drops the entry, so
  // one cancelled Task consumes one cause exactly once.
  take(sessionID: string): string | null {
    const held = this.#held.get(sessionID)
    if (!held) return null
    this.#held.delete(sessionID)
    return held.retry ?? held.info ?? null
  }
}

// sharedProviderCauses backs the production plugin: the event hook and the
// cancelled-Task settle path run in the same module graph, so one instance
// joins them without another shared mutable surface.
const sharedProviderCauses = new ProviderCauses()

// observeProviderCause records the provider cause an event carries, if any.
// Event shapes follow the host's own event contract (EventSessionStatus and
// EventMessageUpdated): a retry status carries the provider message verbatim,
// and a message info error is a typed { name, data } record. An abort marker
// is deliberately not a cause: the cancel string this module appends to must
// say what ended the worker, and MessageAbortedError names the operator's
// interrupt, not the provider fault the distinction exists to keep.
function observeProviderCause(causes: ProviderCauses, event: Record<string, unknown>): void {
  const properties = event.properties
  if (!object(properties)) return
  if (event.type === "session.status") {
    const status = properties.status
    if (typeof properties.sessionID === "string" && object(status) && status.type === "retry" && typeof status.message === "string" && status.message.length > 0) {
      causes.record(properties.sessionID, { source: "retry", message: status.message })
    }
    return
  }
  if (event.type === "message.updated") {
    const info = properties.info
    if (object(info) && typeof info.sessionID === "string" && object(info.error) && info.error.name !== "MessageAbortedError") {
      const message = providerErrorDiagnostic(info.error)
      if (message !== null) causes.record(info.sessionID, { source: "info", message })
    }
  }
}

// providerErrorDiagnostic renders a typed message info error as one line:
// the error name when the shape carries one, then its message. Returns null
// when the record carries nothing readable.
function providerErrorDiagnostic(error: Record<string, unknown>): string | null {
  const name = typeof error.name === "string" && error.name.length > 0 ? error.name : undefined
  const data = object(error.data) && typeof error.data.message === "string" && error.data.message.length > 0 ? error.data.message : undefined
  const message = data ?? (typeof error.message === "string" && error.message.length > 0 ? error.message : undefined)
  if (message === undefined) return name ?? null
  return name === undefined || name === message ? message : `${name}: ${message}`
}

// The bare host cancel string the task tool leaves on an ended worker. Only
// this string hides its cause; any other terminal part error already carries
// one and stays verbatim.
const TASK_CANCELLED_ERROR = "Task cancelled"
// The appended cause is bounded so both the cancel string and the cause
// survive the worker-fail detail fold's own 4096-byte bound intact.
const MAX_PROVIDER_CAUSE_BYTES = 2_048

// cancelledDetail appends the held provider cause to the bare host cancel
// string. A cause observed for another session, or none at all, leaves the
// detail exactly as the host wrote it.
function cancelledDetail(error: string, cause: string | null): string {
  if (error !== TASK_CANCELLED_ERROR || cause === null) return error
  return `${error}; provider error during the worker session: ${boundedTextPrefix(cause, MAX_PROVIDER_CAUSE_BYTES)}`
}

// Cancellation can end Task without tool.execute.after. Consume only the
// terminal error for the exact host call that consumed the dispatch window.
// A spawn that dies before any result or error part takes a third route: the
// host publishes session.error, the tool part stays running, and neither the
// completion hook nor an error part ever arrives. That event settles through
// failSpawnWithoutPartEvent below.
export async function failDispatchedWorker(event: unknown, deps: LaneCompletionDeps = {}): Promise<AgentResultEnvelope | null> {
  if (!object(event) || !object(event.properties)) return null
  // Every host event reaches this entry through the plugin's event hook, so
  // the provider cause a retry or terminal message carries is held here for
  // the cancelled Task that may settle later on the same stream.
  const causes = deps.providerCauses ?? sharedProviderCauses
  observeProviderCause(causes, event)
  if (event.type === "session.error") return failSpawnWithoutPartEvent(event.properties, deps)
  if (event.type !== "message.part.updated") return null
  const part = event.properties.part
  if (!object(part) || part.type !== "tool" || part.tool !== TASK_TOOL_ID || typeof part.sessionID !== "string" || typeof part.callID !== "string") return null
  const state = part.state
  if (!object(state) || state.status !== "error" || typeof state.error !== "string") return null
  const windows = deps.windows ?? dispatchWindows()
  const pending = windows.inFlight(part.sessionID, part.callID)
  if (!pending) return null
  const lane = laneForIdentity(pending.packet.lane_id, pending.packet.lane_version, pending.packet.lane_digest)
  if (!lane) return unavailableEnvelope(pending, "in-flight attempt names a lane the registry does not carry")
  const metadata = state.metadata
  if (!object(metadata) || typeof metadata.sessionId !== "string" || !/^ses_[a-zA-Z0-9]+$/.test(metadata.sessionId)) {
    return unavailableEnvelope(pending, "cancelled Task has no host child session identity; retain the in-flight attempt for reconciliation")
  }
  if (!windows.claimSettlement(part.sessionID, part.callID)) return null
  const sessionID = part.sessionID
  const callID = part.callID
  const detail = cancelledDetail(state.error, causes.take(metadata.sessionId))
  try {
    const envelope = await failWorkerAttempt(lane, pending.packet, metadata.sessionId, detail, {
      credentials: deps.credentials, runner: deps.runner, evidenceRunner: deps.evidenceRunner, sessionReader: deps.sessionReader, concordBinary: deps.concordBinary, packetDigest: pending.packetDigest, workerDirectory: pending.workerDirectory, capturedProvenance: pending.provenance,
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
  const lane = laneForIdentity(pending.packet.lane_id, pending.packet.lane_version, pending.packet.lane_digest)
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
