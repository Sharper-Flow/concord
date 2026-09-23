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
import { createHash } from "node:crypto"
import type { ToolContext } from "@opencode-ai/plugin"
import type { ConcordInvoke } from "./packet"
import type { CredentialStore } from "./credentials"
import { canonicalDirectory, type DispatchWindows } from "./dispatch-window"
import { dispatchWorker, errorEnvelopeForLane, contextPreflightRefusal, type AgentLanePacket, type AgentResultEnvelope, type DispatchRunner } from "./dispatch"
import { agentLanes, agentUtilities, type AgentLane, type AgentUtility } from "./generated-agent-lanes"
import { buildAgentLanePacket, type AgentLanePacketFailureKind } from "./packet"
import { hostControlPlane } from "./move-session"
import { dispatchRequiresNextTurn, TURN_MOVE_DISPATCH_REFUSAL } from "./turn-move-boundary"

export interface LaneDispatchInput {
  work_id: string
  expected_version: number
  idempotency_key: string
  lane_id: string
  approval_ref?: string
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

// utilityForId looks up a registered generated utility by id. Utilities are
// coordinator-native Tasks, not lanes: they carry no packet, no dispatch
// window, and no lane evidence, so a dispatch_worker request naming one never
// reaches the packet builder or the core.
function utilityForId(laneId: string): AgentUtility | null {
  return agentUtilities.find((candidate) => candidate.id === laneId) ?? null
}

// utilityDispatchRefusal is the typed refusal for a utility id named at
// dispatch_worker. It is distinct from the unregistered-lane refusal: the
// details carry the utility_dispatch boundary marker and the native route, and
// the message names the utility and its correcting route — a coordinator-only
// native Task with subagent_type concord-<id> and no dispatch window. The
// refusal is retry-unsafe because retrying dispatch_worker with a utility id
// can never succeed; the caller must issue the Task call instead.
function utilityDispatchRefusal(utility: AgentUtility, input: LaneDispatchInput): AgentResultEnvelope {
  const refusal = errorEnvelopeForLane(
    null,
    { work_id: input.work_id, lane_id: input.lane_id },
    "error",
    "invalid_input",
    `dispatch_worker refuses utility id ${utility.id}: a utility runs as a native Task with subagent_type concord-${utility.id}, from a coordinator session only, and without a dispatch window; issue that Task call instead of dispatch_worker`,
    "use_declared_route",
    { details: { boundary: "utility_dispatch", utility: utility.id, route: `native Task with subagent_type concord-${utility.id}`, coordinator_session_only: true, dispatch_window: false } },
  )
  refusal.error!.retry_safe = false
  return refusal
}

// dispatchAttemptID identifies the exact adapter request, not the time at which
// the request reaches the host. Approval handling can resubmit the unchanged
// request after a prompt, so a clock-based identity would change its packet and
// operation digest during that round trip. A new idempotency key mints a new
// identity for the next retry.
export function dispatchAttemptID(input: LaneDispatchInput): string {
  const request = JSON.stringify({ work_id: input.work_id, expected_version: input.expected_version, idempotency_key: input.idempotency_key, lane_id: input.lane_id })
  const digest = createHash("sha256").update(request, "utf8").digest("hex")
  return `attempt-${digest}`
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
  if (dispatchRequiresNextTurn(deps.context.sessionID)) {
    return errorEnvelopeForLane(laneForId(input.lane_id), { work_id: input.work_id, lane_id: input.lane_id }, "error", "unauthorized_dispatch", TURN_MOVE_DISPATCH_REFUSAL, "retry_same_request", { details: { boundary: "turn_move" } })
  }
  // A registered utility id is not a lane. The refusal fires before any core
  // call — utility admission lives in the plugin's Task hook, so dispatch_worker
  // can never authorize one — while an unknown lane id falls through to the
  // packet builder's unregistered-lane refusal below.
  const utility = utilityForId(input.lane_id)
  if (utility) return utilityDispatchRefusal(utility, input)
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

  const pinnedWork = isRecord(pinned.work_pin) ? pinned.work_pin : null
  const correction = pinnedWork && isRecord(pinnedWork.correction) ? pinnedWork.correction : null
  const retrySuffix = correction && typeof correction.attempt_count === "number" ? `-retry-${correction.attempt_count}` : ""
  const attempt = `${dispatchAttemptID(input)}${retrySuffix}`
  // The packet builder performs the additional scope + trace reads it needs
  // and returns either a packet or a typed refusal; we forward refusals
  // verbatim after the kind → outcome mapping in CD-0067 D5.
  const built = await buildAgentLanePacket({ workId: input.work_id, productId: productIdentity[0], laneId: input.lane_id, attemptId: attempt, stepId: workflowStep }, { context: deps.context, invoke: deps.invoke })
  if (built.failure) return mapPacketFailure(built.failure, { work_id: input.work_id, lane_id: input.lane_id })
  const packet = built.packet

  try {
    await hostControlPlane().manageSession(deps.context.sessionID, deps.context.abort)
  } catch (error) {
    return errorEnvelopeForLane(laneForId(packet.lane_id), packet, "blocked", "transport_failure", error instanceof Error ? error.message : String(error), "contact_operator")
  }

  // The host session route is the source for the claimed directory, read per
  // call rather than stored (CD-0104 D1). Across turns, the native Task child
  // starts in that host-reported directory. The window re-reads it at bind time
  // and refuses if the session moved between authorization and use.
  let workerDirectory: string
  let pinnedWorkerDirectory: string
  try {
    workerDirectory = await hostControlPlane().sessionDirectory(deps.context.sessionID, deps.context.abort)
    const canonical = canonicalDirectory(workerDirectory)
    if (canonical === null) throw new Error("host session worktree identity cannot be resolved")
    pinnedWorkerDirectory = canonical
  } catch {
    return errorEnvelopeForLane(laneForId(packet.lane_id), packet, "error", "transport_failure", "host session directory identity cannot be resolved", "reconcile_operation")
  }

  // Issue #1322, the pre-effect gate: contextDirectory is where this dispatch
  // executes. The dispatch_worker action below persists an authorized attempt
  // in the core, so a calling tool context outside the host session directory
  // refuses here, before the core is asked; the durable claimed-worktree gate
  // after authorization stays as defense in depth.
  const contextDirectory = typeof deps.context.directory === "string" ? deps.context.directory : undefined
  const contextRefusal = contextPreflightRefusal(laneForId(packet.lane_id), packet, pinnedWorkerDirectory, contextDirectory)
  if (contextRefusal) return contextRefusal

  // Core invoke: the dispatch_worker action with the enriched fields. The
  // core records the packet digest (CD-0067 D2) and returns a typed
  // envelope; any non-ok response is an authorization boundary refusal,
  // surfaced to the caller as unauthorized_dispatch. lane_id is never
  // forwarded — it is tool-level vocabulary the adapter consumed above.
  let coreResponse: unknown
  try {
    const approval = input.approval_ref ? { approval: { approval_ref: input.approval_ref } } : {}
    coreResponse = await deps.invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: input.work_id, expected_version: input.expected_version, action_id: "dispatch_worker", idempotency_key: input.idempotency_key, fields: { attempt_id: packet.attempt_id, worker_packet: packet }, ...approval } }, deps.context, pinnedWorkerDirectory)
  } catch (error) {
    return errorEnvelopeForLane(laneForId(packet.lane_id), packet as Partial<AgentLanePacket>, "error", "transport_failure", `concord_work_transition.workflow_action threw before reaching the core: ${String(error)}`, "reconcile_operation")
  }
  if (!isRecord(coreResponse)) {
    return errorEnvelopeForLane(laneForId(packet.lane_id), packet as Partial<AgentLanePacket>, "error", "transport_failure", "concord_work_transition.workflow_action returned no envelope", "reconcile_operation")
  }
  if (coreResponse.outcome === "error") {
    const errorObj = isRecord(coreResponse.error) ? coreResponse.error : null
    const message = errorObj && typeof errorObj.message === "string" ? errorObj.message : "dispatch_worker authorization refused"
    const details = errorObj && isRecord(errorObj.details) ? errorObj.details : undefined
    if (errorObj?.kind === "approval_required") {
      const refusal = errorEnvelopeForLane(laneForId(packet.lane_id), packet as Partial<AgentLanePacket>, "error", "approval_required", message, "request_approval", details)
      refusal.error!.retry_safe = false
      return refusal
    }
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
  // next Task call the plugin hook rewrites (CD-0102 D1).
  const workPins = resultRecord && Array.isArray(resultRecord.work_pins) ? resultRecord.work_pins : undefined
  // Issue #1322: the dispatch_worker response also carries worker_worktree,
  // the durable claimed worktree the authorization rested on. A core that
  // answers ok without naming it is not an authorization whose tool-context
  // landing this adapter can gate, so it refuses as a server contract break
  // before any window opens; the gate then survives a host process restart
  // that empties the adapter's in-memory claim records.
  const authorizedWorktree = resultRecord && typeof resultRecord.worker_worktree === "string" ? resultRecord.worker_worktree : ""
  if (authorizedWorktree === "") {
    return errorEnvelopeForLane(laneForId(packet.lane_id), packet as Partial<AgentLanePacket>, "error", "transport_failure", "dispatch_worker response carried no worker_worktree", "reconcile_operation")
  }
  // contextDirectory is the directory the calling tool call runs in, the
  // observable that proves the session's tool context has landed in the
  // claimed worktree (issue #1322). It was resolved above for the pre-effect
  // gate; dispatchWorker compares it with the armed claim, with the record a
  // metadata-only work_start refusal left behind, and with the durable
  // claimed worktree the core names.
  return dispatchWorker(packet, { authorize: async () => coreResponse, credentials: deps.credentials, runner: deps.runner, evidenceRunner: deps.evidenceRunner, concordBinary: deps.concordBinary, packetDigest, sessionID: deps.context.sessionID, windows: deps.windows, workPins, workerDirectory, pinnedWorkerDirectory, authorizedWorktree, resolveWorkerDirectory: () => hostControlPlane().sessionDirectory(deps.context.sessionID, deps.context.abort), contextDirectory })
}
