import type { ToolContext } from "@opencode-ai/plugin"
import { validateAgentLanePacket, type AgentLanePacket, type AgentLanePacketCheckpoint, type AgentLanePacketCorrection, type AgentLanePacketDesignRecord, type AgentLanePacketLawContext, type AgentLanePacketOutcomePredicate, type AgentLanePacketProposalRecord, type AgentLanePacketWorkContext, type AgentLanePacketWorkerJob, type AgentLanePacketWorkRecord } from "./dispatch"
import { agentLanePacketSchema, agentLanes, workerScopeAssignedResult, type AgentLane } from "./generated-agent-lanes"
import { laneStepDispatchKinds } from "./generated-lane-step-dispatch"

// The packet bounds are read off the generated contract rather than restated,
// so a contract move cannot leave the builder enforcing a stale limit.
const INPUT_BOUNDS = agentLanePacketSchema.properties.inputs.properties
const TASK_MAX_LENGTH: number = INPUT_BOUNDS.task.maxLength
const CONSTRAINT_MAX_LENGTH: number = INPUT_BOUNDS.constraints.items.maxLength
const CONSTRAINTS_MAX_ITEMS: number = INPUT_BOUNDS.constraints.maxItems
// The packet identity is versioned (CD-0205): the builder records the
// current identity — the last enum entry the generated schema declares —
// which is the job-capable packet that may carry inputs.worker_job.
const PACKET_SCHEMA_VERSION = agentLanePacketSchema.properties.schema_version.enum[agentLanePacketSchema.properties.schema_version.enum.length - 1]

export type AgentLanePacketFailureKind =
  | "unregistered_lane"
  | "lane_unassigned"
  | "transport_failure"
  | "missing_work_item"
  | "mandate_unapproved"
  | "workflow_absent"
  | "projection_overflow"
  | "packet_refused"
  | "worker_job_unavailable"
  | "worker_job_ambiguous"

export type AgentLanePacketField = "task" | "constraints" | "outcome_predicates"

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
// way dispatch.ts takes a DispatchRunner. Native worker dispatch can pass its
// already-canonical session directory so core authorization and the local
// dispatch window bind the same host observation.
export type ConcordInvoke = (toolName: string, args: { operation: string; input: Record<string, unknown> }, context: ToolContext, sessionDirectory?: string) => Promise<Record<string, unknown>>

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

// codePoints counts Unicode code points, the unit JSON Schema maxLength and
// minLength are defined in and the unit the Go payload-schema validator
// counts with utf8.RuneCountInString. A JavaScript .length counts UTF-16
// code units, so an astral code point would count twice and a packet the
// contract admits would be refused here with a false overflow.
function codePoints(value: string): number {
  return [...value].length
}

// The closed outcome_kind set the packet schema admits. The continuity read
// types outcome_kind only as a short string, so the builder refuses a kind
// outside this set before the closed schema sees the packet.
const OUTCOME_PREDICATE_KINDS: AgentLanePacketOutcomePredicate["outcome_kind"][] = ["exists", "absent", "outcome", "check"]

// decodeOutcomePredicates projects the pinned contract's outcome_predicates
// into the packet's typed field. The continuity read carries each
// outcome_payload as a JSON-encoded string, so the builder decodes it into
// the predicate object the closed schema validates; a payload the core
// recorded but that cannot decode is a typed transport failure, never a
// string to reassemble.
function decodeOutcomePredicates(workId: string, predicates: unknown[]): { predicates: AgentLanePacketOutcomePredicate[]; failure?: undefined } | { predicates?: undefined; failure: AgentLanePacketFailure } {
  const decoded: AgentLanePacketOutcomePredicate[] = []
  for (const predicate of predicates) {
    if (!isRecord(predicate)) return failure("transport_failure", `work ${workId} pinned contract carried a malformed outcome predicate`)
    const predicateId = typeof predicate.predicate_id === "string" ? predicate.predicate_id : ""
    const ordinal = typeof predicate.ordinal === "number" && Number.isInteger(predicate.ordinal) ? predicate.ordinal : null
    const kind = typeof predicate.outcome_kind === "string" ? predicate.outcome_kind : ""
    if (predicateId.length === 0 || ordinal === null) {
      return failure("transport_failure", `work ${workId} pinned contract carried an outcome predicate without a typed predicate_id and ordinal`)
    }
    if (!OUTCOME_PREDICATE_KINDS.includes(kind as AgentLanePacketOutcomePredicate["outcome_kind"])) {
      return failure("transport_failure", `work ${workId} outcome predicate ${predicateId} declares outcome_kind ${JSON.stringify(kind)}, outside the closed exists/absent/outcome/check set`)
    }
    let payload: unknown = predicate.outcome_payload
    if (typeof payload === "string") {
      try {
        payload = JSON.parse(payload)
      } catch {
        return failure("transport_failure", `work ${workId} outcome predicate ${predicateId} carried an outcome_payload that is not valid JSON`)
      }
    }
    if (!isRecord(payload)) {
      return failure("transport_failure", `work ${workId} outcome predicate ${predicateId} carried an outcome_payload that is not a JSON object`)
    }
    decoded.push({ predicate_id: predicateId, ordinal, outcome_kind: kind as AgentLanePacketOutcomePredicate["outcome_kind"], outcome_payload: payload })
  }
  return { predicates: decoded }
}

function projectCorrectionContext(value: unknown): AgentLanePacketCorrection | undefined {
  if (!isRecord(value)) return undefined
  const disposition = value.disposition === "failed" || value.disposition === "rejected" || value.disposition === "verification" ? value.disposition : null
  const attemptCount = typeof value.attempt_count === "number" ? value.attempt_count : null
  const attemptLimit = typeof value.attempt_limit === "number" ? value.attempt_limit : null
  const diagnosis = typeof value.diagnosis === "string" ? value.diagnosis : ""
  const strategy = typeof value.strategy === "string" ? value.strategy : ""
  const escalated = typeof value.escalated === "boolean" ? value.escalated : null
  const failureKind = typeof value.failure_kind === "string" ? value.failure_kind : ""
  const failureDetail = typeof value.failure_detail === "string" ? value.failure_detail : ""
  const predicateIDs = Array.isArray(value.predicate_ids) ? value.predicate_ids.filter((item): item is string => typeof item === "string") : []
  const evidenceRefs = Array.isArray(value.evidence_refs) ? value.evidence_refs.filter((item): item is string => typeof item === "string") : []
  const failedAttemptID = typeof value.failed_attempt_id === "string" ? value.failed_attempt_id : ""
  const failedAttemptEpoch = typeof value.failed_attempt_epoch === "number" ? value.failed_attempt_epoch : 0
  const sourceEventID = typeof value.source_event_id === "string" ? value.source_event_id : ""
  const sourceEventSeq = typeof value.source_event_seq === "number" ? value.source_event_seq : 0
  // The count is not bounded by the limit. Each operator-authorized retry past
  // the limit increments it, so a correction legitimately carries a count above
  // attempt_limit, and `escalated` is what marks that state. Dropping the
  // correction here would leave the packet unable to consume it, and the core
  // refuses a dispatch whose packet carries no correction while one is durable
  // — stranding the work item, because a correction clears only when a dispatch
  // follows it.
  if (disposition === null || attemptCount === null || attemptLimit !== 3 || escalated === null || diagnosis.length === 0 || strategy.length === 0) return undefined
  return {
    disposition,
    attempt_count: attemptCount,
    attempt_limit: 3,
    escalated,
    diagnosis,
    strategy,
    ...(failureKind.length > 0 ? { failure_kind: failureKind } : {}),
    ...(failureDetail.length > 0 ? { failure_detail: failureDetail } : {}),
    predicate_ids: predicateIDs,
    evidence_refs: evidenceRefs,
    ...(failedAttemptID.length > 0 ? { failed_attempt_id: failedAttemptID } : {}),
    ...(failedAttemptEpoch > 0 ? { failed_attempt_epoch: failedAttemptEpoch } : {}),
    ...(sourceEventID.length > 0 ? { source_event_id: sourceEventID } : {}),
    ...(sourceEventSeq > 0 ? { source_event_seq: sourceEventSeq } : {}),
  }
}

// selectReadyWorkerJob binds the one dispatch-ready worker-job revision the
// pinned continuity carries (CD-0205). The pinned step declares
// record_worker_job exactly where a job-capable definition dispatches a
// job-executing lane, so that declaration, not a version table, decides
// whether the packet must bind a job. The revision content rides verbatim:
// the core refuses any packet whose worker_job differs from the recorded
// ready revision. Zero ready revisions, or more than one, refuse: the
// dispatcher never chooses between ready jobs or authors job content.
function selectReadyWorkerJob(workId: string, pinned: Record<string, unknown>): { job?: Record<string, unknown>; failure?: AgentLanePacketFailure } {
  const ready = Array.isArray(pinned.ready_worker_jobs) ? pinned.ready_worker_jobs.filter(isRecord) : []
  if (ready.length === 0) {
    return failure("worker_job_unavailable", `work ${workId} holds no dispatch-ready worker-job revision; record the bounded job with record_worker_job, ready, before dispatching this lane`)
  }
  if (ready.length > 1) {
    const ids = ready.map((job) => `${String(job.job_id)}@${String(job.revision)}`).join(", ")
    return failure("worker_job_ambiguous", `work ${workId} holds ${ready.length} dispatch-ready worker-job revisions (${ids}); record the jobs not to dispatch now as not ready, or order them through prerequisites, so exactly one is ready`)
  }
  return { job: ready[0] }
}

function isReadOnlyCapabilityClass(capabilityClass: AgentLane["capability_class"]): boolean {
  return laneStepDispatchKinds[capabilityClass].some((kind) => kind === "internal_sqlite" || kind === "cross_authority")
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
// The pinned contract is the mandate's authority whenever one is present:
// its premise is the approved objective the worker must deliver, its
// typed outcome predicates ride inputs.outcome_predicates, and its version
// plus the work item version bind the packet to the exact recorded state it
// projected. inputs.task is that premise verbatim — the builder adds no
// header, trailer, or duplicate copy, so the full approval premise capacity
// reaches the worker instead of being spent on adapter framing. Read-only
// classes without a contract carry the recorded work question verbatim at a
// joined step instead. inputs.binding is the closed typed authority that
// names the objective source, the recorded work and contract versions, and
// the one assigned result the worker-scope contract derives from the lane;
// the step and lane identity stay packet root fields.
export async function buildAgentLanePacket(request: AgentLanePacketRequest, deps: AgentLanePacketDeps): Promise<AgentLanePacketBuild> {
  const lane: AgentLane | undefined = agentLanes.find((candidate) => candidate.id === request.laneId)
  if (!lane) {
    return failure("unregistered_lane", `lane ${request.laneId} is not in the generated lane registry (${agentLanes.map((candidate) => candidate.id).join(", ")})`)
  }
  // The worker-scope contract bounds every attempt to the lane's one assigned
  // result. A registered lane without an assignment is a contract/registry
  // drift that generation refuses, so the dispatch fails closed here too: a
  // packet without an assigned result would ask a worker to complete an
  // unbounded result.
  const assignedResult = workerScopeAssignedResult(lane.id)
  if (assignedResult === null) {
    return failure("lane_unassigned", `lane ${lane.id} carries no assigned result in the worker-scope contract, so no attempt can be bounded to one result`)
  }

  const scope = await readOperation("concord_work_browse", "scope", { product_id: request.productId, work_id: request.workId }, deps)
  if (scope.failure) return scope
  const work = scope.result.work
  if (!isRecord(work)) {
    return failure("missing_work_item", `concord_work_browse.scope returned no work item for ${request.workId}`)
  }
  const narrative = typeof work.narrative === "string" ? work.narrative : ""
  const title = typeof work.title === "string" ? work.title : ""
  const recordedTask = typeof work.task === "string" ? work.task : ""
  const hasRecordedTask = recordedTask.trim().length > 0
  const valueStatement = typeof work.value_statement === "string" ? work.value_statement : ""
  const workVersion = typeof work.version === "number" ? work.version : null

  const continuity = await readOperation("concord_work_trace", "continuity", { work_id: request.workId, page: { cursor: null, limit: 1 } }, deps)
  if (continuity.failure) return continuity
  const pinned = continuity.result.pinned
  if (!isRecord(pinned)) {
    return failure("transport_failure", `concord_work_trace.continuity returned no pinned continuity for ${request.workId}`)
  }
  if (pinned.workflow_instance === "absent") {
    return failure("workflow_absent", `work ${request.workId} holds no workflow instance; a lane packet needs a workflow step`)
  }
  const workflowStep = pinned.workflow_step
  if (typeof workflowStep !== "string") {
    return failure("transport_failure", `work ${request.workId} pinned continuity did not carry workflow_step`)
  }
  const contract = pinned.contract
  const readOnly = isReadOnlyCapabilityClass(lane.capability_class)
  let outcomePredicates: unknown[] = []
  let task: string
  let contractVersion: number | null
  let objectiveSource: "contract_premise" | "work_question"
  if (isRecord(contract)) {
    const premise = typeof contract.premise === "string" ? contract.premise : ""
    const pinnedContractVersion = typeof contract.version === "number" ? contract.version : null
    outcomePredicates = contract.outcome_predicates as unknown[]
    if (!Array.isArray(outcomePredicates) || outcomePredicates.length === 0) {
      return failure("transport_failure", `work ${request.workId} pinned contract did not carry typed outcome_predicates`)
    }
    if (premise.trim().length === 0) {
      return failure("mandate_unapproved", `work ${request.workId} pinned contract carries no approved objective, so there is no recorded change to dispatch against`)
    }
    if (workVersion === null || pinnedContractVersion === null) {
      return failure("transport_failure", `work ${request.workId} pinned state did not carry the typed work and contract versions the packet must bind to`)
    }
    task = premise
    contractVersion = pinnedContractVersion
    objectiveSource = "contract_premise"
  } else if (readOnly) {
    if (workVersion === null) {
      return failure("transport_failure", `work ${request.workId} pinned state did not carry the typed work version the packet must bind to`)
    }
    if (title.trim().length === 0 && narrative.trim().length === 0) {
      return failure("mandate_unapproved", `work ${request.workId} carries no recorded question or narrative for the read-only dispatch`)
    }
    task = hasRecordedTask ? recordedTask : title.trim().length > 0 ? title : narrative
    contractVersion = null
    objectiveSource = "work_question"
  } else {
    return failure("mandate_unapproved", `work ${request.workId} has no pinned workflow contract, so no required end-state has been approved to dispatch against`)
  }
  // The assigned result bounds the attempt to the one obligation whose
  // discharge completes it, and the binding records it as typed data: the
  // objective source, the recorded versions, and that result. Completion
  // disposes only that result: every other required result stays explicit
  // with the parent workflow, which dispatches one further bounded attempt
  // per remaining result.
  const binding = { objective_source: objectiveSource, work_version: workVersion, contract_version: contractVersion, assigned_result: assignedResult }
  const stepActions = Array.isArray(pinned.step_actions) ? pinned.step_actions : []
  let workerJob: Record<string, unknown> | undefined
  if (stepActions.includes("record_worker_job")) {
    const selected = selectReadyWorkerJob(request.workId, pinned)
    if (selected.failure) return { failure: selected.failure }
    workerJob = selected.job
    if (lane.capability_class === "verification" && (!Array.isArray(workerJob?.checks) || workerJob.checks.length === 0)) {
      return failure("worker_job_unavailable", `work ${request.workId} requires recorded executable checks for a verification job; record the bounded checks before dispatching this lane`)
    }
  }
  const taskCodePoints = codePoints(task)
  if (taskCodePoints > TASK_MAX_LENGTH) {
    return failure("projection_overflow", `inputs.task carries ${taskCodePoints} Unicode code points against a limit of ${TASK_MAX_LENGTH}`, { field: "task", limit: TASK_MAX_LENGTH, actual: taskCodePoints })
  }

  // The recorded law context, design record, and proposal ride the packet
  // verbatim from the pinned continuity, and the work item's recorded value
  // statement, task, and narrative ride inputs.work_record verbatim. The core
  // refuses a dispatch whose member differs from the current record, so the
  // builder authors no prose around them and re-derives nothing; the closed
  // packet schema owns the bounds.
  const lawContextValue = isRecord(pinned.law_context) ? (pinned.law_context as unknown as AgentLanePacketLawContext) : undefined
  const designValue = isRecord(pinned.design_record) ? (pinned.design_record as unknown as AgentLanePacketDesignRecord) : undefined
  const proposalValue = isRecord(pinned.proposal_record) ? (pinned.proposal_record as unknown as AgentLanePacketProposalRecord) : undefined
  const workRecord: AgentLanePacketWorkRecord = {
    ...(valueStatement.length > 0 ? { value_statement: valueStatement } : {}),
    ...(recordedTask.length > 0 ? { task: recordedTask } : {}),
    ...(narrative.length > 0 ? { narrative } : {}),
  }
  const workPin = isRecord(pinned.work_pin) ? pinned.work_pin : null
  const correctionValue = workPin ? projectCorrectionContext(workPin.correction) : undefined
  // CON-887: the pin's work-context view rides the packet verbatim. The core
  // refuses a dispatch whose inputs.work_context differs from the current
  // view byte-for-byte, so any re-derivation, filtering, or truncation here
  // would strand the dispatch; the closed packet schema owns the bounds.
  const workContextValue = workPin && isRecord(workPin.work_context) ? (workPin.work_context as unknown as AgentLanePacketWorkContext) : undefined
  // CON-883: the pinned projection's latest context checkpoint rides the
  // packet verbatim. The core refuses a dispatch whose inputs.checkpoint
  // differs from the latest checkpoint byte-for-byte, so the builder never
  // re-derives or filters the coordinator directions it carries. The
  // checkpoint stays seated on the continuity snapshot alone: the pinned
  // envelope's byte budget admits exactly one copy of a max-size
  // checkpoint, and the work pin embeds inside that same envelope.
  const checkpointValue = isRecord(pinned.latest_checkpoint) ? (pinned.latest_checkpoint as unknown as AgentLanePacketCheckpoint) : undefined

  // The typed outcome predicates ride inputs.outcome_predicates as validated
  // predicate objects, decoded from the continuity read's serialized
  // payloads. The packet preserves the structure the fold uses to bind each
  // predicate's discharge requirement rather than flattening it into prose.
  const decoded = decodeOutcomePredicates(request.workId, outcomePredicates)
  if (decoded.failure) return { failure: decoded.failure }
  // Fail-closed bound on the serialized typed field. The field inherits the
  // capacity the spliced constraint entries carried, so a contract that
  // outgrew the splice refuses here as a typed projection overflow rather
  // than shipping an unbounded packet.
  const serializedPredicates = JSON.stringify(decoded.predicates)
  const predicatesBound = CONSTRAINTS_MAX_ITEMS * CONSTRAINT_MAX_LENGTH
  const serializedCodePoints = codePoints(serializedPredicates)
  if (serializedCodePoints > predicatesBound) {
    return failure("projection_overflow", `inputs.outcome_predicates serializes to ${serializedCodePoints} Unicode code points against a limit of ${predicatesBound}`, { field: "outcome_predicates", limit: predicatesBound, actual: serializedCodePoints })
  }

  const packet = {
    schema_version: PACKET_SCHEMA_VERSION,
    attempt_id: request.attemptId,
    lane_id: lane.id,
    lane_version: lane.version,
    lane_digest: lane.digest,
    work_id: request.workId,
    step_id: request.stepId,
    inputs: { task, binding, report_protocol: agentLanePacketSchema.properties.inputs.properties.report_protocol.const, ...(workerJob ? { worker_job: workerJob as unknown as AgentLanePacketWorkerJob } : {}), ...(lawContextValue ? { law_context: lawContextValue } : {}), ...(designValue ? { design_record: designValue } : {}), ...(proposalValue ? { proposal_record: proposalValue } : {}), ...(Object.keys(workRecord).length > 0 ? { work_record: workRecord } : {}), ...(correctionValue ? { correction: correctionValue } : {}), ...(workContextValue ? { work_context: workContextValue } : {}), ...(checkpointValue ? { checkpoint: checkpointValue } : {}), ...(decoded.predicates.length > 0 ? { outcome_predicates: decoded.predicates } : {}) },
  }

  const packetFailures: string[] = []
  if (!validateAgentLanePacket(packet, packetFailures)) {
    return failure("packet_refused", `the projected packet for work ${request.workId} failed the closed agent-lane-packet.v1 schema (${packetFailures[0] ?? "unknown validation failure"})`)
  }
  return { packet }
}
