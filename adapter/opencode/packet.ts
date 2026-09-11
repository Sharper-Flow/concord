import type { ToolContext } from "@opencode-ai/plugin"
import { validateAgentLanePacket, type AgentLanePacket } from "./dispatch"
import { agentLanePacketSchema, agentLaneReportConstraints, agentLanes, type AgentLane } from "./generated-agent-lanes"

// The packet bounds are read off the generated contract rather than restated,
// so a contract move cannot leave the builder enforcing a stale limit.
const INPUT_BOUNDS = agentLanePacketSchema.properties.inputs.properties
const TASK_MAX_LENGTH: number = INPUT_BOUNDS.task.maxLength
const CONTEXT_MAX_LENGTH: number = INPUT_BOUNDS.context.maxLength
const CONSTRAINT_MAX_LENGTH: number = INPUT_BOUNDS.constraints.items.maxLength
const CONSTRAINTS_MAX_ITEMS: number = INPUT_BOUNDS.constraints.maxItems
const PACKET_SCHEMA_VERSION = agentLanePacketSchema.properties.schema_version.const

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
export type ConcordInvoke = (toolName: string, args: { operation: string; input: Record<string, unknown> }, context: ToolContext) => Promise<Record<string, unknown>>

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

function renderDesignRecord(value: unknown): string {
  if (!isRecord(value)) return ""
  const approach = typeof value.approach === "string" ? value.approach : ""
  const decisions = Array.isArray(value.decisions) ? value.decisions : []
  const touchedRefs = Array.isArray(value.touched_refs) ? value.touched_refs.filter((ref): ref is string => typeof ref === "string") : []
  if (approach.length === 0 && decisions.length === 0 && touchedRefs.length === 0) return ""
  const lines = ["Approved design record:", `Approach: ${approach}`, "Decisions:"]
  for (const decision of decisions) {
    if (!isRecord(decision)) continue
    const id = typeof decision.id === "string" ? decision.id : ""
    const question = typeof decision.question === "string" ? decision.question : ""
    const choice = typeof decision.choice === "string" ? decision.choice : ""
    const rationale = typeof decision.rationale === "string" ? decision.rationale : ""
    const rejected = Array.isArray(decision.rejected) ? decision.rejected.filter((item): item is string => typeof item === "string") : []
    lines.push(`- ${id}: ${question} Choice: ${choice}. Rationale: ${rationale}${rejected.length > 0 ? ` Rejected: ${rejected.join(", ")}.` : ""}`)
  }
  lines.push(`Touched refs: ${touchedRefs.join(", ")}`, "")
  return lines.join("\n")
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
//
// The pinned contract is the mandate's authority (#903): its premise is the
// approved objective the worker must deliver, and its version plus the work
// item version bind the packet to the exact recorded state it projected. A
// packet built from a contract with no objective would let baseline-passing
// predicates masquerade as delivery, so a contentless premise is a typed
// refusal, never an omitted field. Numbered constraints carry the complete
// serialized mandate, with boundaries that preserve Unicode characters.
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
  const workVersion = typeof work.version === "number" ? work.version : null

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
  const premise = typeof contract.premise === "string" ? contract.premise : ""
  const contractVersion = typeof contract.version === "number" ? contract.version : null
  const outcomePredicates = contract.outcome_predicates
  const workflowStep = pinned.workflow_step
  if (!Array.isArray(outcomePredicates) || outcomePredicates.length === 0 || typeof workflowStep !== "string") {
    return failure("transport_failure", `work ${request.workId} pinned contract did not carry typed outcome_predicates and workflow_step`)
  }
  if (premise.trim().length === 0) {
    return failure("mandate_unapproved", `work ${request.workId} pinned contract carries no approved objective, so there is no recorded change to dispatch against`)
  }
  if (workVersion === null || contractVersion === null) {
    return failure("transport_failure", `work ${request.workId} pinned state did not carry the typed work and contract versions the packet must bind to`)
  }
  const task = [
    `Deliver the approved objective for work ${request.workId}, at workflow step "${workflowStep}" (work v${workVersion}, contract v${contractVersion}).`,
    "",
    "Approved objective:",
    premise,
  ].join("\n")
  if (task.length > TASK_MAX_LENGTH) {
    return failure("projection_overflow", `the approved objective does not fit inputs.task: ${task.length} characters against a limit of ${TASK_MAX_LENGTH}`, { field: "task", limit: TASK_MAX_LENGTH, actual: task.length })
  }

  const design = renderDesignRecord(pinned.design_record)
  const context = design + narrative
  if (context.length > CONTEXT_MAX_LENGTH) {
    return failure("projection_overflow", `the pinned design and work item narrative do not fit inputs.context: ${context.length} characters against a limit of ${CONTEXT_MAX_LENGTH}`, { field: "context", limit: CONTEXT_MAX_LENGTH, actual: context.length })
  }

  // CD-0056: the fold refuses a report that leaves a declared obligation
  // undischarged, so the obligation set travels with the packet by name.
  const mandateLabel = "Approved end-state mandate (join parts in order) "
  const partLimit = CONSTRAINT_MAX_LENGTH - `${mandateLabel}${CONSTRAINTS_MAX_ITEMS}/${CONSTRAINTS_MAX_ITEMS}: `.length
  const mandateParts: string[] = []
  let part = ""
  for (const character of JSON.stringify(outcomePredicates)) {
    if (part.length + character.length > partLimit) {
      mandateParts.push(part)
      part = ""
    }
    part += character
  }
  if (part.length > 0) mandateParts.push(part)
  const predicateConstraints = mandateParts.map((text, index) => `${mandateLabel}${index + 1}/${mandateParts.length}: ${text}`)

  const constraints = [
    ...predicateConstraints,
    ...lane.evidence_obligations.map(
      (obligation) => `Evidence obligation "${obligation}": your agent-lane-report.v1 report must carry an evidence entry whose obligation is "${obligation}". An undischarged obligation is refused.`,
    ),
    ...agentLaneReportConstraints[lane.id],
  ]
  if (constraints.length > CONSTRAINTS_MAX_ITEMS) {
    return failure("projection_overflow", `lane ${lane.id} declares ${constraints.length} evidence obligations, above the inputs.constraints limit of ${CONSTRAINTS_MAX_ITEMS}`, { field: "constraints", limit: CONSTRAINTS_MAX_ITEMS, actual: constraints.length })
  }
  const oversized = constraints.find((entry) => entry.length > CONSTRAINT_MAX_LENGTH)
  if (oversized !== undefined) {
    return failure("projection_overflow", `a rendered packet constraint does not fit an inputs.constraints entry: ${oversized.length} characters against a limit of ${CONSTRAINT_MAX_LENGTH}`, { field: "constraints", limit: CONSTRAINT_MAX_LENGTH, actual: oversized.length })
  }

  const packet = {
    schema_version: PACKET_SCHEMA_VERSION,
    attempt_id: request.attemptId,
    lane_id: lane.id,
    lane_version: lane.version,
    lane_digest: lane.digest,
    work_id: request.workId,
    step_id: request.stepId,
    inputs: { task, ...(context.length > 0 ? { context } : {}), constraints },
  }

  if (!validateAgentLanePacket(packet)) {
    return failure("packet_refused", `the projected packet for work ${request.workId} failed the closed agent-lane-packet.v1 schema`)
  }
  return { packet }
}
