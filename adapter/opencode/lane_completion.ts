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
import { boundedTextPrefix, completeWorkerAttempt, exactObservationMessage, failWorkerAttempt, abandonWorkerAttempt, TASK_CANCELLED_ERROR, type AgentResultEnvelope, type DispatchRunner, type WorkerFailureHostObservation, type WorkerFailureObservation, type WorkerFailureSource } from "./dispatch"
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
  hostObservations?: HostFailureObservations
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
  // The typed review summary (CD-0197): the disposition and per-severity counts
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

export async function completeDispatchedWorker(input: LaneCompletionInput, output: LaneCompletionOutput | undefined, deps: LaneCompletionDeps = {}): Promise<void> {
  if (input.tool !== TASK_TOOL_ID) return
  // A failed native subtask has no after-hook result. Its terminal part event
  // owns settlement, so completion must leave that attempt unclaimed.
  if (output === undefined) return
  const windows = deps.windows ?? dispatchWindows()
  const record = windows.claimSettlement(input.sessionID, input.callID)
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
    windows.refuseSettlement(input.sessionID)
    output.output += renderAttempt({ schema_version: "1.0", outcome: "error", lane: { id: record.packet.lane_id, version: record.packet.lane_version, digest: record.packet.lane_digest }, agent: `concord-${record.packet.lane_id}`, readback_model: null, session_id: null, error: { kind: "invalid_input", retry_safe: false, recovery_action: "contact_operator", message: "in-flight attempt names a lane at a version and digest the registry does not carry" } })
    return
  }
  const signal = deps.signal ?? new AbortController().signal
  let envelope: AgentResultEnvelope
  let recorded = false
  try {
    envelope = await completeWorkerAttempt(lane, record.packet, output.output, { credentials: deps.credentials, runner: deps.runner, evidenceRunner: deps.evidenceRunner, sessionReader: deps.sessionReader, concordBinary: deps.concordBinary, packetDigest: record.packetDigest, workerDirectory: record.workerDirectory, capturedProvenance: record.provenance, onRecorded: () => { recorded = true } }, signal)
  } catch (error) {
    envelope = { schema_version: "1.0", outcome: "error", lane: { id: lane.id, version: lane.version, digest: record.packet.lane_digest }, agent: `concord-${lane.id}`, readback_model: null, session_id: null, error: { kind: "error", retry_safe: false, recovery_action: "reconcile_operation", message: String(error).slice(0, 2048) } }
  }
  // onRecorded confirms the terminal evidence write, and only then does
  // settlement release the retained record. Anything else keeps it in the
  // refused state, so the worker_reconcile recovery the in-flight refusal
  // names stays available for exactly this attempt.
  if (recorded) windows.finishSettlement(input.sessionID, input.callID)
  else windows.refuseSettlement(input.sessionID)
  output.output += renderAttempt(envelope)
}

function object(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

// Host observations describe transient source events, not cancellation causes.
export interface HostFailureObservation {
  tag: "retry" | "terminal"
  event_type: "session.status" | "message.updated"
  session_id: string
  message_id?: string
  error_name?: string
  status_code?: number
  retry_attempt?: number
  next_retry?: number
  message: string
  source_digest: string
}

const MAX_HOST_OBSERVATION_BYTES = 2_048
const MAX_HOST_OBSERVATION_REF_BYTES = 128

// One latest observation per source kind and child session. Settlement consumes
// both sources; no message preference can turn a prior retry into a terminal fault.
export class HostFailureObservations {
  static readonly CAPACITY = 64
  readonly #held = new Map<string, HostFailureObservation[]>()

  record(input: Omit<HostFailureObservation, "source_digest">): void {
    if (!boundedObservationText(input.session_id, MAX_HOST_OBSERVATION_REF_BYTES, true)
      || !boundedObservationText(input.message, MAX_HOST_OBSERVATION_BYTES, true)
      || (input.message_id !== undefined && !boundedObservationText(input.message_id, MAX_HOST_OBSERVATION_REF_BYTES, true))
      || (input.error_name !== undefined && !boundedObservationText(input.error_name, MAX_HOST_OBSERVATION_REF_BYTES, true))) return
    if (input.status_code !== undefined && (!Number.isInteger(input.status_code) || input.status_code < 100 || input.status_code > 599)) return
    if (input.tag === "retry") {
      if (input.event_type !== "session.status" || input.message_id !== undefined || input.error_name !== undefined || input.status_code !== undefined) return
      if (input.retry_attempt !== undefined && (!Number.isSafeInteger(input.retry_attempt) || input.retry_attempt < 1)) return
      if (input.next_retry !== undefined && (!Number.isSafeInteger(input.next_retry) || input.next_retry < 0)) return
    } else if (input.tag === "terminal") {
      if (input.event_type !== "message.updated" || input.retry_attempt !== undefined || input.next_retry !== undefined) return
    } else return
    const source = {
      tag: input.tag, event_type: input.event_type, session_id: input.session_id,
      ...(input.message_id !== undefined ? { message_id: input.message_id } : {}),
      ...(input.error_name !== undefined ? { error_name: input.error_name } : {}),
      ...(input.status_code !== undefined ? { status_code: input.status_code } : {}),
      message: input.message,
      ...(input.retry_attempt !== undefined ? { retry_attempt: input.retry_attempt } : {}),
      ...(input.next_retry !== undefined ? { next_retry: input.next_retry } : {}),
    }
    const source_digest = `sha256:${createHash("sha256").update(JSON.stringify(source)).digest("hex")}`
    const held = (this.#held.get(input.session_id) ?? []).filter((entry) => entry.tag !== input.tag)
    this.#held.delete(input.session_id)
    this.#held.set(input.session_id, [...held, { ...source, source_digest }])
    while (this.#held.size > HostFailureObservations.CAPACITY) {
      const oldest = this.#held.keys().next().value
      if (oldest === undefined) break
      this.#held.delete(oldest)
    }
  }

  take(sessionID: string): HostFailureObservation[] {
    const held = this.#held.get(sessionID) ?? []
    this.#held.delete(sessionID)
    return held.map((entry) => ({ ...entry }))
  }
}

function boundedObservationText(value: string, bytes: number, nonempty: boolean): boolean {
  return (!nonempty || value.length > 0) && Buffer.byteLength(value, "utf8") <= bytes && Buffer.from(value, "utf8").toString("utf8") === value
}

const sharedHostObservations = new HostFailureObservations()

function observeHostFailure(observations: HostFailureObservations, event: Record<string, unknown>): void {
  const properties = event.properties
  if (!object(properties)) return
  if (event.type === "session.status") {
    const status = properties.status
    if (typeof properties.sessionID !== "string" || !object(status)) return
    if (status.type === "retry" && typeof status.message === "string") {
      observations.record({
        tag: "retry", event_type: "session.status", session_id: properties.sessionID, message: status.message,
        ...(typeof status.attempt === "number" ? { retry_attempt: status.attempt } : {}),
        ...(typeof status.next === "number" ? { next_retry: status.next } : {}),
      })
    }
    return
  }
  if (event.type === "message.updated") {
    const info = properties.info
    if (!object(info) || typeof info.sessionID !== "string" || !object(info.error) || info.error.name === "MessageAbortedError") return
    const error = info.error
    const data = object(error.data) ? error.data : undefined
    const message = typeof data?.message === "string" ? data.message : typeof error.message === "string" ? error.message : typeof error.name === "string" ? error.name : undefined
    if (message === undefined) return
    observations.record({
      tag: "terminal", event_type: "message.updated", session_id: info.sessionID, message,
      ...(typeof info.id === "string" ? { message_id: info.id } : {}),
      ...(typeof error.name === "string" ? { error_name: error.name } : {}),
      ...(typeof data?.statusCode === "number" ? { status_code: data.statusCode } : {}),
    })
  }
}

// The bare host cancellation string. The host ends a Task with it when nothing
// supplied a typed diagnostic, and it stays verbatim in every diagnostic it
// rides: an interrupt is not a provider fault (owned by dispatch.ts, mirrored
// here as a re-export for callers that read this module only).
export { TASK_CANCELLED_ERROR } from "./dispatch"

// ForeignFailureObservationSource is the typed refusal for a held source that
// names a session other than the exact child the dispatch window bound: a
// wrong-child observation cannot ride this attempt. The settle path surfaces
// it as a typed refusal and keeps the retained attempt for reconciliation.
export class ForeignFailureObservationSource extends Error {}

// hostSourceDigest binds one typed host source to its content: the digest
// covers exactly the fields the core host shape carries plus the FULL
// observed message, so a changed diagnostic or a changed identity moves the
// digest. The digest never hashes a truncated form.
function hostSourceDigest(host: WorkerFailureHostObservation, message: string): string {
  return `sha256:${createHash("sha256").update(JSON.stringify({ host, message }), "utf8").digest("hex")}`
}

// retainedHostSource projects one held host observation onto the core source
// shape: the message id is the locator when the host supplied one, the
// retained source digest rides unchanged, and the retry counters stay out of
// the core host shape — they live in the retained digest only. Held messages
// are exact: the hold refuses an over-bound observation whole.
function retainedHostSource(entry: HostFailureObservation): WorkerFailureSource {
  return {
    kind: "host_event",
    ...(entry.message_id !== undefined ? { locator: entry.message_id } : {}),
    digest: entry.source_digest,
    message: entry.message,
    host: {
      tag: entry.tag,
      event_type: entry.event_type,
      session_id: entry.session_id,
      ...(entry.message_id !== undefined ? { message_id: entry.message_id } : {}),
      ...(entry.error_name !== undefined ? { error_name: entry.error_name } : {}),
      ...(entry.status_code !== undefined ? { status_code: entry.status_code } : {}),
    },
  }
}

// cancellationFailureObservation is the typed observation for the terminal
// settle of one child Task. Every held source must name the bound child; a
// foreign one refuses the attribution whole. The held sources ride first, in
// the order the boundary observed them; the arm source records the tool-part
// error itself, byte-exact within the bound and never truncated — an
// over-bound diagnostic omits the copied message while the digest keeps
// binding the full content.
//
// A bare cancellation is the explicit absence case: cancellation tag, unknown
// cause, unknown basis. A held retry never becomes the cause, and message
// text alone never upgrades it. A non-cancel terminal error beside a typed
// host fault carries a host_provider cause at hypothesis only: the host
// record never links the fault to the terminal error, and a locator alone
// proves no causality, so the basis never becomes supported here.
export function cancellationFailureObservation(stateError: string, childSessionID: string, held: HostFailureObservation[]): WorkerFailureObservation {
  const foreign = held.find((entry) => entry.session_id !== childSessionID)
  if (foreign !== undefined) {
    throw new ForeignFailureObservationSource(`held host observation names child ${foreign.session_id}, not the bound child ${childSessionID}; a wrong-child observation cannot ride this attempt`)
  }
  const bareCancellation = stateError === TASK_CANCELLED_ERROR
  const heldFault = held.find((entry) => entry.tag === "terminal" && (entry.error_name !== undefined || entry.status_code !== undefined))
  let cause: WorkerFailureObservation["cause"] = "unknown"
  let basis: WorkerFailureObservation["basis"] = "unknown"
  if (!bareCancellation && heldFault !== undefined) {
    cause = "host_provider"
    basis = "hypothesis"
  }
  const message = exactObservationMessage(stateError)
  const host: WorkerFailureHostObservation = { tag: bareCancellation ? "cancellation" : "terminal", event_type: "message.part.updated", session_id: childSessionID }
  return {
    observer: "host_adapter",
    phase: "host_execution",
    cause,
    basis,
    sources: [
      ...held.map(retainedHostSource),
      { kind: "host_event", digest: hostSourceDigest(host, stateError), ...(message !== undefined ? { message } : {}), host },
    ],
  }
}

// spawnFailureObservation records the session.error event that settled a
// spawn which died before any part event. No child session existed, so the
// session id is the parent session the host published the event on, and the
// locator stays empty: the digest binds the observed fields, never an
// invented event id. The diagnostic rides byte-exact within the bound and
// never truncated — an over-bound diagnostic omits the copied message while
// the digest keeps binding the full content.
export function spawnFailureObservation(sessionID: string, error: unknown): WorkerFailureObservation {
  const errorName = object(error) && typeof error.name === "string" && error.name !== "MessageAbortedError" && boundedObservationText(error.name, MAX_HOST_OBSERVATION_REF_BYTES, true)
    ? error.name
    : undefined
  const host: WorkerFailureHostObservation = { tag: "terminal", event_type: "session.error", session_id: sessionID, ...(errorName !== undefined ? { error_name: errorName } : {}) }
  const diagnostic = spawnFailureDiagnostic(error)
  const message = exactObservationMessage(diagnostic)
  return {
    observer: "host_adapter",
    phase: "host_execution",
    cause: "unknown",
    basis: "unknown",
    sources: [{ kind: "host_event", digest: hostSourceDigest(host, diagnostic), ...(message !== undefined ? { message } : {}), host }],
  }
}

// Preserve other host terminal diagnostics verbatim.

// Rendering observations does not assert that either source caused cancellation.
function cancelledDetail(error: string, observations: HostFailureObservation[]): string {
  if (error !== TASK_CANCELLED_ERROR) return error
  const parts = observations.map((entry) => {
    const diagnostic = entry.error_name && entry.error_name !== entry.message ? `${entry.error_name}: ${entry.message}` : entry.message
    return { label: `; host ${entry.tag} observation: `, diagnostic }
  })
  const detail = error + parts.map(({ label, diagnostic }) => label + diagnostic).join("")
  if (Buffer.byteLength(detail, "utf8") <= 4096) return detail
  // The legacy detail is a summary with its own 4096-byte wire bound.
  // Reserve every source label; the structured observations stay byte-exact.
  const marker = " [summary truncated]"
  const overhead = error + parts.map(({ label }) => label + marker).join("")
  const budget = Math.floor((4096 - Buffer.byteLength(overhead, "utf8")) / parts.length)
  return error + parts.map(({ label, diagnostic }) => {
    const summary = boundedTextPrefix(diagnostic, budget)
    return label + summary + (summary === diagnostic ? "" : marker)
  }).join("")
}

// Cancellation can end Task without tool.execute.after. Consume only the
// terminal error for the exact host call that consumed the dispatch window.
// A spawn that dies before any result or error part takes a third route: the
// host publishes session.error, the tool part stays running, and neither the
// completion hook nor an error part ever arrives. That event settles through
// failSpawnWithoutPartEvent below.
export async function failDispatchedWorker(event: unknown, deps: LaneCompletionDeps = {}): Promise<AgentResultEnvelope | null> {
  if (!object(event) || !object(event.properties)) return null
  // Transient observations stay separate until the exact child Task settles.
  const observations = deps.hostObservations ?? sharedHostObservations
  observeHostFailure(observations, event)
  if (event.type === "session.error") return failSpawnWithoutPartEvent(event.properties, deps)
  if (event.type !== "message.part.updated") return null
  const part = event.properties.part
  if (!object(part) || part.type !== "tool" || part.tool !== TASK_TOOL_ID || typeof part.sessionID !== "string" || typeof part.callID !== "string") return null
  const state = part.state
  if (!object(state) || state.status !== "error" || typeof state.error !== "string") return null
  const windows = deps.windows ?? dispatchWindows()
  const pending = windows.inFlightAttempt(part.sessionID)
  if (!pending) return null
  // Native hooks identify the part; model-issued Task hooks identify its call.
  // Both identities must come from this exact host part and retained attempt.
  const callID = pending.callID ?? part.callID
  if (callID !== part.callID && callID !== part.id) return null
  const lane = laneForIdentity(pending.packet.lane_id, pending.packet.lane_version, pending.packet.lane_digest)
  if (!lane) return unavailableEnvelope(pending, "in-flight attempt names a lane the registry does not carry")
  const metadata = state.metadata
  if (!object(metadata) || typeof metadata.sessionId !== "string" || !/^ses_[a-zA-Z0-9]+$/.test(metadata.sessionId)) {
    return unavailableEnvelope(pending, "cancelled Task has no host child session identity; retain the in-flight attempt for reconciliation")
  }
  if (!windows.claimSettlement(part.sessionID, callID)) return null
  const sessionID = part.sessionID
  const observed = observations.take(metadata.sessionId)
  const detail = cancelledDetail(state.error, observed)
  try {
    // the retained sources ride the terminal write as typed
    // attribution, not only the returned envelope. The arm observation binds
    // the exact child this dispatch window authorized.
    const envelope = await failWorkerAttempt(lane, pending.packet, metadata.sessionId, detail, {
      credentials: deps.credentials, runner: deps.runner, evidenceRunner: deps.evidenceRunner, sessionReader: deps.sessionReader, concordBinary: deps.concordBinary, packetDigest: pending.packetDigest, workerDirectory: pending.workerDirectory, capturedProvenance: pending.provenance,
      failureAttribution: cancellationFailureObservation(state.error, metadata.sessionId, observed),
    }, deps.signal ?? new AbortController().signal, () => windows.finishSettlement(sessionID, callID))
    if (envelope.error && observed.length > 0) {
      envelope.error.details = { ...envelope.error.details, host_observations: observed }
    }
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
    const { envelope } = await abandonWorkerAttempt(lane, pending.packet, detail, {
      credentials: deps.credentials, runner: deps.runner, evidenceRunner: deps.evidenceRunner, concordBinary: deps.concordBinary,
      abandonEventID: `worker-abandon-${token}`, abandonNonce: token,
      failureAttribution: spawnFailureObservation(sessionID, properties.error),
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
