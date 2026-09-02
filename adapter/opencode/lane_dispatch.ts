// Lane dispatch is the adapter surface that takes the orchestrator's
// tool-level `fields.lane_id` (CD-0067 D5) and turns it into a core
// dispatch_worker action plus a worker spawn. The orchestrator cannot author
// a lane packet: packet bounds are derived from the closed schema, lane
// identity and digests are recorded in the generated lane registry, and the
// pinned workflow step is held by the core. Splitting packet authorship
// across the orchestrator and the adapter would either smuggle core state
// into the orchestrator's prompt or duplicate it in the adapter; this module
// is the seam that keeps both halves honest.
//
// The import graph below is acyclic by construction:
//   concord.ts → lane_dispatch.ts → {packet.ts, dispatch.ts}
// packet.ts imports dispatch.ts and generated-agent-lanes.ts, neither of
// which reaches back into lane_dispatch.ts. lane_dispatch.ts never imports
// concord.ts at runtime; the orchestrator-facing call site in concord.ts
// passes its own transport in through `deps.invoke`.
import type { ToolContext } from "@opencode-ai/plugin"
import type { ConcordInvoke } from "./packet"
import type { CredentialStore } from "./credentials"
import type { DispatchWindows } from "./dispatch-window"
import { dispatchWorker, errorEnvelopeForLane, type AgentLanePacket, type AgentResultEnvelope, type DispatchRunner } from "./dispatch"
import { agentLanes, type AgentLane } from "./generated-agent-lanes"
import { buildAgentLanePacket, type AgentLanePacketFailureKind } from "./packet"

export interface LaneDispatchInput {
  work_id: string
  expected_version: number
  idempotency_key: string
  lane_id: string
}

export interface LaneDispatchDeps {
  context: ToolContext
  invoke: ConcordInvoke
  credentials?: CredentialStore
  runner?: DispatchRunner
  evidenceRunner?: DispatchRunner
  concordBinary?: string
  // windows is the authorization-window store. Production callers leave it
  // unset and the dispatch path uses the per-instance store the plugin hook
  // reads; tests supply an isolated one.
  windows?: DispatchWindows
  // now is an injected clock so the attempt id is deterministic in tests;
  // production callers leave it unset and the seam defaults to Date.now.
  now?: () => number
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

// laneForId looks up a registered lane by id only. The full packet schema
// also pins lane_version and lane_digest, but at the dispatch boundary we
// accept any lane the registry has admitted by id; buildAgentLanePacket
// re-validates the full triple against the packet it produces and refuses
// unregistered lanes up front.
function laneForId(laneId: string): AgentLane | null {
  return agentLanes.find((candidate) => candidate.id === laneId) ?? null
}

// toHexRadix turns a number into a non-negative integer string in base 16,
// padded to twelve lowercase hex digits. attempt ids embed this so the same
// millisecond does not collide across lanes, but the form stays deterministic
// for tests that inject `now`.
function toHexRadix(value: number): string {
  return Math.max(0, Math.floor(value)).toString(16).padStart(12, "0")
}

// mapPacketFailure converts the packet builder's typed refusal into an
// AgentResultEnvelope with the kind/outcome mapping CD-0067 D5 fixes. The
// outcome is "blocked" when the failure is operator-fixable on the same
// attempt (mandate_unapproved), and "error" otherwise; the error kind stays
// "invalid_input" for every refusal because the builder is the source of
// truth for what shape an input failed.
function mapPacketFailure(failure: { kind: AgentLanePacketFailureKind; message: string }, partial: Partial<AgentLanePacket>): AgentResultEnvelope {
  if (failure.kind === "mandate_unapproved") return errorEnvelopeForLane(laneForId(typeof partial.lane_id === "string" ? partial.lane_id : ""), partial, "blocked", "invalid_input", failure.message, "reconcile_operation")
  return errorEnvelopeForLane(laneForId(typeof partial.lane_id === "string" ? partial.lane_id : ""), partial, "error", "invalid_input", failure.message, "reconcile_operation")
}

// dispatchLaneWorker completes the orchestrator's dispatch_worker request:
// it derives the lane identity, product identity, and workflow step from
// recorded state, builds the closed agent-lane-packet.v1, performs the
// dispatch_worker action with the enriched fields, and spawns the worker
// only after the core has authorized the attempt window. The returned
// envelope is the AgentResultEnvelope dispatchWorker produces on the happy
// path, or one constructed by errorEnvelopeForLane on every refusal the
// adapter raises before spawn; the orchestrator-facing call site in
// concord.ts forwards whichever one lands.
export async function dispatchLaneWorker(input: LaneDispatchInput, deps: LaneDispatchDeps): Promise<AgentResultEnvelope> {
  // The continuity read supplies the durable anchors: product identity and
  // workflow step. Anything else — narrative, mandate — is read once the
  // packet builder runs below. The strict-refusal style mirrors packet.ts's
  // readOperation so a degraded or error-enveloped core response cannot
  // seed an attempt whose step is not the operator's pinned one.
  const continuity = await deps.invoke("concord_work_trace", { operation: "continuity", input: { work_id: input.work_id, page: { cursor: null, limit: 1 } } }, deps.context)
  if (!isRecord(continuity) || continuity.outcome !== "ok" || !isRecord(continuity.result) || !isRecord(continuity.result.pinned)) {
    const detail = !isRecord(continuity) ? "no envelope" : continuity.outcome !== "ok" ? `outcome ${String(continuity.outcome)}` : "missing pinned continuity"
    return errorEnvelopeForLane(null, { work_id: input.work_id, lane_id: input.lane_id }, "error", "transport_failure", `concord_work_trace.continuity ${detail} for ${input.work_id}`, "reconcile_operation")
  }
  const pinned = continuity.result.pinned
  const productIdentity = pinned.product_identity
  const workflowStep = pinned.workflow_step
  // product_identity is the distinct product set across the work item's
  // projects, so zero or several identities is valid core state, not a
  // transport fault: the work item is unscoped or spans products, and
  // dispatch needs exactly one product to project the packet from. The
  // refusal is blocked/reconcile_operation because the operator must fix
  // the project scoping before any retry can succeed.
  if (!Array.isArray(productIdentity) || productIdentity.length !== 1 || typeof productIdentity[0] !== "string") {
    const count = Array.isArray(productIdentity) ? String(productIdentity.length) : "none"
    return errorEnvelopeForLane(null, { work_id: input.work_id, lane_id: input.lane_id }, "blocked", "invalid_input", `work item ${input.work_id} carries ${count} product identities; dispatch requires exactly one`, "reconcile_operation")
  }
  if (typeof workflowStep !== "string") {
    return errorEnvelopeForLane(null, { work_id: input.work_id, lane_id: input.lane_id }, "error", "transport_failure", `concord_work_trace.continuity pinned workflow_step is not a string for ${input.work_id}`, "reconcile_operation")
  }

  const now = deps.now ?? Date.now
  const attempt = `attempt-${input.work_id}-${input.lane_id}-${toHexRadix(now())}`
  // The packet builder performs the additional scope + trace reads it needs
  // and returns either a packet or a typed refusal; we forward refusals
  // verbatim after the kind → outcome mapping in CD-0067 D5.
  const built = await buildAgentLanePacket({ workId: input.work_id, productId: productIdentity[0], laneId: input.lane_id, attemptId: attempt, stepId: workflowStep }, { context: deps.context, invoke: deps.invoke })
  if (built.failure) return mapPacketFailure(built.failure, { work_id: input.work_id, lane_id: input.lane_id })
  const packet = built.packet

  // Core invoke: the dispatch_worker action with the enriched fields. The
  // core records the packet digest (CD-0067 D2) and returns a typed
  // envelope; any non-ok response is an authorization boundary refusal,
  // surfaced to the caller as unauthorized_dispatch. lane_id is never
  // forwarded — it is tool-level vocabulary the adapter consumed above.
  let coreResponse: unknown
  try {
    coreResponse = await deps.invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: input.work_id, expected_version: input.expected_version, action_id: "dispatch_worker", idempotency_key: input.idempotency_key, fields: { attempt_id: packet.attempt_id, worker_packet: packet } } }, deps.context)
  } catch (error) {
    return errorEnvelopeForLane(laneForId(packet.lane_id), packet as Partial<AgentLanePacket>, "error", "transport_failure", `concord_work_transition.workflow_action threw before reaching the core: ${String(error)}`, "reconcile_operation")
  }
  if (!isRecord(coreResponse)) {
    return errorEnvelopeForLane(laneForId(packet.lane_id), packet as Partial<AgentLanePacket>, "error", "transport_failure", "concord_work_transition.workflow_action returned no envelope", "reconcile_operation")
  }
  if (coreResponse.outcome === "error") {
    const errorObj = isRecord(coreResponse.error) ? coreResponse.error : null
    const message = errorObj && typeof errorObj.message === "string" ? errorObj.message : "dispatch_worker authorization refused"
    return errorEnvelopeForLane(laneForId(packet.lane_id), packet as Partial<AgentLanePacket>, "error", "unauthorized_dispatch", message, "reconcile_operation")
  }

  // CD-0067 D6: the dispatch_worker response carries worker_packet_digest
  // on result; the adapter signs that exact value on the dispatch
  // assertion. A core that answers ok without recording the digest is not
  // the core that authored the window, so it cannot be trusted to gate
  // evidence against it. The refusal is transport_failure because it is
  // a server contract break, not an operator-fixable input.
  const resultRecord = isRecord(coreResponse.result) ? coreResponse.result : null
  const packetDigest = resultRecord && typeof resultRecord.worker_packet_digest === "string" ? resultRecord.worker_packet_digest : ""
  if (packetDigest === "") {
    return errorEnvelopeForLane(laneForId(packet.lane_id), packet as Partial<AgentLanePacket>, "error", "transport_failure", "dispatch_worker response carried no worker_packet_digest", "reconcile_operation")
  }

  // Dispatch: the worker authorizer is the core response we just received;
  // dispatchWorker forwards that envelope to its own authorize() seam and
  // re-validates it against outcome === "error" before opening the window, so
  // the happy-path ok envelope reaches it untouched. packetDigest is the value
  // the dispatch assertion will quote (D6).
  //
  // The window binds to the calling session, because that is the session whose
  // next Task call the plugin hook rewrites (CD-0097 D1).
  return dispatchWorker(packet, { authorize: async () => coreResponse, credentials: deps.credentials, runner: deps.runner, evidenceRunner: deps.evidenceRunner, concordBinary: deps.concordBinary, packetDigest, sessionID: deps.context.sessionID, windows: deps.windows })
}