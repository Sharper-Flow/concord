import type { ToolContext } from "@opencode-ai/plugin"
import { validateAgentLanePacket, type AgentLanePacket } from "./dispatch"
import { agentLanePacketSchema, agentLanes, type AgentLane } from "./generated-agent-lanes"

// The packet bounds are read off the generated contract rather than restated,
// so a contract move cannot leave the builder enforcing a stale limit.
const INPUT_BOUNDS = agentLanePacketSchema.properties.inputs.properties
const TASK_MAX_LENGTH: number = INPUT_BOUNDS.task.maxLength
const CONTEXT_MAX_LENGTH: number = INPUT_BOUNDS.context.maxLength
const CONSTRAINT_MAX_LENGTH: number = INPUT_BOUNDS.constraints.items.maxLength
const CONSTRAINTS_MAX_ITEMS: number = INPUT_BOUNDS.constraints.maxItems
const PACKET_SCHEMA_VERSION = "1.0"

export type AgentLanePacketFailureKind =
  | "unregistered_lane"
  | "transport_failure"
  | "missing_work_item"
  | "mandate_unapproved"
  | "projection_overflow"
  | "packet_refused"

export type AgentLanePacketField = "task" | "context" | "constraints"

export interface AgentLanePacketFailure {
  kind: AgentLanePacketFailureKind
  message: string
  field?: AgentLanePacketField
  limit?: number
  actual?: number
}

export type AgentLanePacketBuild =
  | { packet: AgentLanePacket; failure?: undefined }
  | { packet?: undefined; failure: AgentLanePacketFailure }

export interface AgentLanePacketRequest {
  workId: string
  productId: string
  laneId: string
  attemptId: string
  stepId: string
}

// ConcordInvoke is the adapter transport signature. It defaults to
// invokeConcordOperation, the single context-resolution and invoke path in
// concord.ts; the seam exists so a caller can supply a scripted transport, the
// way dispatch.ts takes a DispatchRunner.
export type ConcordInvoke = (toolName: string, args: { operation: string; input: Record<string, unknown> }, context: ToolContext) => Promise<unknown>

export interface AgentLanePacketDeps {
  context: ToolContext
  invoke: ConcordInvoke
}

function failure(kind: AgentLanePacketFailureKind, message: string, extra: Omit<AgentLanePacketFailure, "kind" | "message"> = {}): { failure: AgentLanePacketFailure } {
  return { failure: { kind, message, ...extra } }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

// readOperation refuses anything that is not an ok core read with an object
// result. A degraded, pending, partial, or error-enveloped response carries no
// approved state, so projecting from it would produce a packet that asserts
// more than Concord recorded.
async function readOperation(
  toolName: string,
  operation: string,
  input: Record<string, unknown>,
  deps: AgentLanePacketDeps,
): Promise<{ result: Record<string, unknown>; failure?: undefined } | { result?: undefined; failure: AgentLanePacketFailure }> {
  const transport = deps.invoke
  const response = await transport(toolName, { operation, input }, deps.context)
  if (!isRecord(response)) return failure("transport_failure", `${toolName}.${operation} returned no envelope`)
  if (response.outcome !== "ok") {
    const error = isRecord(response.error) ? String(response.error.kind) : "no_error_detail"
    return failure("transport_failure", `${toolName}.${operation} returned outcome ${String(response.outcome)} (${error})`)
  }
  if (!isRecord(response.result)) return failure("transport_failure", `${toolName}.${operation} returned no result payload`)
  return { result: response.result }
}

// buildAgentLanePacket projects durable Concord state into the closed
// agent-lane-packet.v1 input triple. It reads the work item narrative from
// concord_work_browse.scope and the pinned workflow contract and step from
// concord_work_trace.continuity, and takes the lane's evidence obligations from
// the generated lane registry. Nothing here is authored: every field is derived
// from recorded state, which is the point — a dispatched worker's goal must not
// be retyped prose.
export async function buildAgentLanePacket(request: AgentLanePacketRequest, deps: AgentLanePacketDeps): Promise<AgentLanePacketBuild> {
  const lane: AgentLane | undefined = agentLanes.find((candidate) => candidate.id === request.laneId)
  if (!lane) {
    return failure("unregistered_lane", `lane ${request.laneId} is not in the generated lane registry (${agentLanes.map((candidate) => candidate.id).join(", ")})`)
  }

  const scope = await readOperation("concord_work_browse", "scope", { product_id: request.productId, work_id: request.workId }, deps)
  if (scope.failure) return scope
  const work = scope.result.work
  if (!isRecord(work)) {
    return failure("missing_work_item", `concord_work_browse.scope returned no work item for ${request.workId}`)
  }
  const narrative = typeof work.narrative === "string" ? work.narrative : ""

  const continuity = await readOperation("concord_work_trace", "continuity", { work_id: request.workId, page: { cursor: null, limit: 1 } }, deps)
  if (continuity.failure) return continuity
  const pinned = continuity.result.pinned
  if (!isRecord(pinned)) {
    return failure("transport_failure", `concord_work_trace.continuity returned no pinned continuity for ${request.workId}`)
  }
  const contract = pinned.contract
  if (!isRecord(contract)) {
    return failure("mandate_unapproved", `work ${request.workId} has no pinned workflow contract, so no required end-state has been approved to dispatch against`)
  }
  const outcomeKind = contract.outcome_kind
  const outcomePayload = contract.outcome_payload
  const workflowStep = pinned.workflow_step
  if (typeof outcomeKind !== "string" || typeof outcomePayload !== "string" || typeof workflowStep !== "string") {
    return failure("transport_failure", `work ${request.workId} pinned contract did not carry a typed outcome_kind, outcome_payload, and workflow_step`)
  }

  const task = [
    `Deliver the approved required end-state of kind "${outcomeKind}" for work ${request.workId}, at workflow step "${workflowStep}".`,
    "",
    "Approved end-state mandate:",
    outcomePayload,
  ].join("\n")
  if (task.length > TASK_MAX_LENGTH) {
    return failure("projection_overflow", `the approved end-state mandate does not fit inputs.task: ${task.length} characters against a limit of ${TASK_MAX_LENGTH}`, { field: "task", limit: TASK_MAX_LENGTH, actual: task.length })
  }

  if (narrative.length > CONTEXT_MAX_LENGTH) {
    return failure("projection_overflow", `the work item narrative does not fit inputs.context: ${narrative.length} characters against a limit of ${CONTEXT_MAX_LENGTH}`, { field: "context", limit: CONTEXT_MAX_LENGTH, actual: narrative.length })
  }

  // CD-0056: the fold refuses a report that leaves a declared obligation
  // undischarged, so the obligation set travels with the packet by name.
  const constraints = lane.evidence_obligations.map(
    (obligation) => `Evidence obligation "${obligation}": your agent-lane-report.v1 report must carry an evidence entry whose obligation is "${obligation}". An undischarged obligation is refused.`,
  )
  if (constraints.length > CONSTRAINTS_MAX_ITEMS) {
    return failure("projection_overflow", `lane ${lane.id} declares ${constraints.length} evidence obligations, above the inputs.constraints limit of ${CONSTRAINTS_MAX_ITEMS}`, { field: "constraints", limit: CONSTRAINTS_MAX_ITEMS, actual: constraints.length })
  }
  const oversized = constraints.find((entry) => entry.length > CONSTRAINT_MAX_LENGTH)
  if (oversized !== undefined) {
    return failure("projection_overflow", `a rendered evidence obligation does not fit an inputs.constraints entry: ${oversized.length} characters against a limit of ${CONSTRAINT_MAX_LENGTH}`, { field: "constraints", limit: CONSTRAINT_MAX_LENGTH, actual: oversized.length })
  }

  const packet = {
    schema_version: PACKET_SCHEMA_VERSION,
    attempt_id: request.attemptId,
    lane_id: lane.id,
    lane_version: lane.version,
    lane_digest: lane.digest,
    work_id: request.workId,
    step_id: request.stepId,
    inputs: { task, ...(narrative.length > 0 ? { context: narrative } : {}), constraints },
  }

  if (!validateAgentLanePacket(packet)) {
    return failure("packet_refused", `the projected packet for work ${request.workId} failed the closed agent-lane-packet.v1 schema`)
  }
  return { packet }
}
