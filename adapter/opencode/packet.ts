import type { ToolContext } from "@opencode-ai/plugin"
import { validateAgentLanePacket, type AgentLanePacket, type AgentLanePacketCorrection, type AgentLanePacketOutcomePredicate } from "./dispatch"
import { agentLanePacketSchema, agentLanes, workerScopeAssignedResult, type AgentLane } from "./generated-agent-lanes"
import { laneStepDispatchKinds } from "./generated-lane-step-dispatch"

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
  | "lane_unassigned"
  | "transport_failure"
  | "missing_work_item"
  | "mandate_unapproved"
  | "workflow_absent"
  | "projection_overflow"
  | "packet_refused"

export type AgentLanePacketField = "task" | "context" | "constraints" | "outcome_predicates"

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

// renderLawContext projects the pinned contract's resolved law and Domain
// references into a readable block. The core resolves every bound ID against
// the law_subjects and domains projections; an added law with no subject yet,
// or a Domain missing from the registry, renders with what the core recorded.
// A law's criteria field (CD-0180) carries the law's acceptance criteria
// bound to this work item's own outcome predicates, so the worker sees the
// criterion-to-predicate chaining the item discharges without opening the
// manifest. The Domain registry path names the repository file that carries
// Domain structure, because the lane holds no Concord tool access to fetch it.
function renderLawContext(value: unknown): string {
  if (!isRecord(value)) return ""
  const laws = Array.isArray(value.laws) ? value.laws : []
  const domains = Array.isArray(value.domains) ? value.domains : []
  if (laws.length === 0 && domains.length === 0) return ""
  const lines = ["Approved law and Domains (binding Product law):"]
  for (const law of laws) {
    if (!isRecord(law)) continue
    const roles = Array.isArray(law.roles) ? law.roles.filter((role): role is string => typeof role === "string") : []
    const lawId = typeof law.law_id === "string" ? law.law_id : ""
    const obligations = Array.isArray(law.obligation_ids) ? law.obligation_ids.filter((id): id is string => typeof id === "string") : []
    const detail = [law.title, law.kind, law.status].filter((part): part is string => typeof part === "string" && part.length > 0).join(", ")
    const path = typeof law.path === "string" ? law.path : ""
    const obligationText = obligations.length > 0 ? ` (obligation ${obligations.join(", ")})` : ""
    const criteriaText = renderLawCriteria(law.criteria)
    lines.push(`- ${roles.join(", ")} law ${lawId}${obligationText}${detail.length > 0 ? `: ${detail}` : ""}${path.length > 0 ? ` — ${path}` : ""}${criteriaText}`)
  }
  for (const domain of domains) {
    if (!isRecord(domain)) continue
    const domainId = typeof domain.domain_id === "string" ? domain.domain_id : ""
    const name = typeof domain.name === "string" ? domain.name : ""
    const purpose = typeof domain.purpose === "string" ? domain.purpose : ""
    lines.push(`- Domain ${domainId}: ${name}${purpose.length > 0 ? ` — ${purpose}` : ""}`)
  }
  const registryPath = typeof value.registry_path === "string" ? value.registry_path : ""
  if (registryPath.length > 0) lines.push(`Domain registry: ${registryPath}`)
  return lines.join("\n") + "\n\n"
}

// renderLawCriteria renders the criteria the core already resolved against
// this work item's predicates. A malformed entry renders with its surviving
// parts rather than failing the packet: the law context is a readability
// projection, and the typed field remains the contract's discharge record.
function renderLawCriteria(value: unknown): string {
  if (!Array.isArray(value) || value.length === 0) return ""
  const bound = value
    .filter(isRecord)
    .map((entry) => ({ criterion: entry.criterion, predicateId: typeof entry.predicate_id === "string" ? entry.predicate_id : "" }))
    .filter((entry): entry is { criterion: number; predicateId: string } => typeof entry.criterion === "number" && entry.predicateId.length > 0)
  if (bound.length === 0) return ""
  return ` (criteria bound to this work item: ${bound.map((entry) => `criterion ${entry.criterion} discharges ${entry.predicateId}`).join("; ")})`
}

// renderProposalRecord projects the recorded proposal's problem, user
// outcomes, and constraints — typed planning state the pinned continuity
// already exposes.
function renderProposalRecord(value: unknown): string {
  if (!isRecord(value)) return ""
  const problem = typeof value.problem === "string" ? value.problem : ""
  const outcomes = Array.isArray(value.user_outcomes) ? value.user_outcomes.filter((item): item is string => typeof item === "string") : []
  const constraints = Array.isArray(value.constraints) ? value.constraints.filter((item): item is string => typeof item === "string") : []
  if (problem.length === 0 && outcomes.length === 0 && constraints.length === 0) return ""
  const lines = ["Recorded proposal:", `Problem: ${problem}`]
  if (outcomes.length > 0) {
    lines.push("User outcomes:")
    for (const outcome of outcomes) lines.push(`- ${outcome}`)
  }
  if (constraints.length > 0) {
    lines.push("Constraints:")
    for (const constraint of constraints) lines.push(`- ${constraint}`)
  }
  return lines.join("\n") + "\n\n"
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
  }
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
  const valueStatement = typeof work.value_statement === "string" ? work.value_statement.trim() : ""
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
  // contextRecordedTask is false when the recorded task already IS the task —
  // the read-only question prefers it — so the context never carries the
  // duplicate copy.
  let contextRecordedTask = true
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
    // The question already carries the recorded task or narrative text
    // verbatim, so neither rides the context a second time.
    contextRecordedTask = false
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
  const taskCodePoints = codePoints(task)
  if (taskCodePoints > TASK_MAX_LENGTH) {
    return failure("projection_overflow", `inputs.task carries ${taskCodePoints} Unicode code points against a limit of ${TASK_MAX_LENGTH}`, { field: "task", limit: TASK_MAX_LENGTH, actual: taskCodePoints })
  }

  const design = renderDesignRecord(pinned.design_record)
  // The why rides ahead of the how: the item's recorded value statement
  // renders as one line before the design record, the law context, the
  // proposal, and the recorded task, so a dispatched worker reads why the work
  // matters first. Older items without a value statement omit the line.
  // A value statement may carry embedded newlines the schema permits; the
  // packet renders one guaranteed line, so embedded line breaks collapse to
  // single spaces and no value can place text ahead of the real design
  // record or masquerade as another context block.
  const valueLine = valueStatement.length > 0 ? `Value: ${valueStatement.replace(/[\r\n]+/g, " ")}\n\n` : ""
  // The resolved contract-bound law and Domains, then the recorded proposal,
  // ride after the design record so the worker reads binding state before the
  // work narrative. Overflow stays fail-closed on the combined context.
  const lawContext = renderLawContext(pinned.law_context)
  const proposal = renderProposalRecord(pinned.proposal_record)
  const workPin = isRecord(pinned.work_pin) ? pinned.work_pin : null
  const correctionValue = workPin ? projectCorrectionContext(workPin.correction) : undefined
  // The persisted work task is the operator's recorded instruction for the
  // worker. Under a pinned contract the premise stays the approved objective
  // in inputs.task and the recorded task rides context ahead of the narrative,
  // so a contract-mandated worker receives the concrete instructions too. On
  // the read-only path the question IS the task or narrative verbatim, so the
  // duplicate copy stays out of the context.
  const readOnlyQuestion = objectiveSource === "work_question" ? task : null
  const context =
    valueLine +
    design +
    lawContext +
    proposal +
    (contextRecordedTask && hasRecordedTask ? `Recorded task:\n${recordedTask}\n\n` : "") +
    (readOnlyQuestion !== null && narrative === readOnlyQuestion ? "" : narrative)
  const contextCodePoints = codePoints(context)
  if (contextCodePoints > CONTEXT_MAX_LENGTH) {
    return failure("projection_overflow", `inputs.context carries ${contextCodePoints} Unicode code points against a limit of ${CONTEXT_MAX_LENGTH}`, { field: "context", limit: CONTEXT_MAX_LENGTH, actual: contextCodePoints })
  }

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
    inputs: { task, binding, ...(context.length > 0 ? { context } : {}), ...(correctionValue ? { correction: correctionValue } : {}), ...(decoded.predicates.length > 0 ? { outcome_predicates: decoded.predicates } : {}) },
  }

  const packetFailures: string[] = []
  if (!validateAgentLanePacket(packet, packetFailures)) {
    return failure("packet_refused", `the projected packet for work ${request.workId} failed the closed agent-lane-packet.v1 schema (${packetFailures[0] ?? "unknown validation failure"})`)
  }
  return { packet }
}
