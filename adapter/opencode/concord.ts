import { tool, type ToolContext } from "@opencode-ai/plugin"
import { clientRef } from "./credentials"
import { contractOperations, manifestDigest, payloadSchemas } from "./generated-contracts"
import { validateGeneratedEnvelope, validateGeneratedPayload } from "./generated-contract-tests"
import { dispatchLaneWorker, type LaneDispatchInput } from "./lane_dispatch"
import { errorEnvelopeForLane } from "./dispatch"

const MAX_STDERR = 8192

export interface ChildRunner { run(argv: string[], input: string, signal: AbortSignal): Promise<{ exitCode: number; stdout: string; stderr: string }> }

const defaultRunner: ChildRunner = {
  async run(argv, input, signal) {
    const child = Bun.spawn(argv, { stdin: "pipe", stdout: "pipe", stderr: "pipe" })
    const abort = () => child.kill()
    if (signal.aborted) abort()
    signal.addEventListener("abort", abort, { once: true })
    await child.stdin.write(input)
    await child.stdin.end()
    const [stdout, stderr, exitCode] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited])
    signal.removeEventListener("abort", abort)
    return { exitCode, stdout, stderr }
  },
}

let runner: ChildRunner = defaultRunner

export function configureConcordAdapter(overrides: { runner?: ChildRunner } = {}) {
  if (overrides.runner) runner = overrides.runner
}

function zodSchema(schema: any, root: Record<string, any>): any {
  const z = tool.schema
  if (schema?.$ref) return zodSchema(root[schema.$ref.replace("#/$defs/", "")], root)
  if (schema?.oneOf || schema?.anyOf) return z.union((schema.oneOf ?? schema.anyOf).map((item: any) => zodSchema(item, root)) as any)
  if (schema?.const !== undefined) return z.literal(schema.const)
  if (schema?.enum) return z.union(schema.enum.map((item: any) => z.literal(item)) as any)
  if (schema?.type === "object" || schema?.properties) {
    const required = new Set(schema.required ?? [])
    const shape: Record<string, any> = {}
    for (const [key, child] of Object.entries(schema.properties ?? {})) {
      const value = zodSchema(child, root)
      shape[key] = required.has(key) ? value : value.optional()
    }
    return z.object(shape).strict()
  }
  if (schema?.type === "array") {
    let value = z.array(zodSchema(schema.items ?? {}, root))
    if (schema.minItems !== undefined) value = value.min(schema.minItems)
    if (schema.maxItems !== undefined) value = value.max(schema.maxItems)
    return value
  }
  if (schema?.type === "integer" || schema?.type === "number") {
    let value = z.number()
    if (schema.type === "integer") value = value.int()
    if (schema.minimum !== undefined) value = value.min(schema.minimum)
    if (schema.maximum !== undefined) value = value.max(schema.maximum)
    return value
  }
  if (schema?.type === "string") {
    let value = z.string()
    if (schema.minLength !== undefined) value = value.min(schema.minLength)
    if (schema.maxLength !== undefined) value = value.max(schema.maxLength)
    if (schema.pattern) value = value.regex(new RegExp(schema.pattern))
    return value
  }
  if (Array.isArray(schema?.type) && schema.type.includes("null")) return z.union([z.string(), z.null()])
  return z.unknown()
}

function argsSchema(toolName: string): any {
  const z = tool.schema
  const variants = contractOperations.filter((op: any) => op.tool === toolName).map((op: any) => z.object({ operation: z.literal(op.id.slice(op.id.indexOf(".") + 1)), input: zodSchema((payloadSchemas as Record<string, unknown>)[op.input_schema.split("/").pop()!], payloadSchemas as any) }).strict())
  return z.union(variants as any)
}

;

function baseEnvelope(toolName: string, operation: string, requestID: string) {
  const queryID = (contractOperations.find((candidate: any) => candidate.tool === toolName && candidate.id.endsWith(`.${operation}`)) as any)?.query_id
  return { schema_version: "1.0", manifest_digest: manifestDigest, request_id: requestID, origin: "adapter", tool: toolName, operation, ...(queryID ? { query_id: queryID } : {}), outcome: "error", resolved_scope: null, authority: "unreachable", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false }
}

function adapterError(toolName: string, operation: string, requestID: string, kind: string, reason: string, message: string, effect: "none" | "possible" | "partial" = "none", recovery = effect === "none" ? "retry_same_request" : "reconcile_operation") {
  return { ...baseEnvelope(toolName, operation, requestID), error: { kind, retry_safe: effect === "none", recovery_action: { kind: recovery }, effect_state: effect, adapter_reason: reason, message } }
}

class AdapterFailure extends Error {
  constructor(readonly kind: string, readonly reason: string, message: string, readonly effect: "none" | "possible" | "partial" = "none", readonly recovery = effect === "none" ? "contact_operator" : "reconcile_operation") { super(message) }
}

function failureEnvelope(toolName: string, operation: string, requestID: string, error: unknown, fallbackReason: string, effect: "none" | "possible" | "partial" = "none") {
  if (error instanceof AdapterFailure) return adapterError(toolName, operation, requestID, error.kind, error.reason, error.message, error.effect, error.recovery)
  return adapterError(toolName, operation, requestID, "transport_failure", fallbackReason, String(error), effect, effect === "none" ? "contact_operator" : "reconcile_operation")
}

function runnerFailure(error: unknown, aborted: boolean) {
  const name = error instanceof Error ? error.name : ""
  const code = typeof error === "object" && error !== null && "code" in error ? String((error as any).code) : ""
  if (aborted || name === "AbortError") return new AdapterFailure("cancelled", "cancelled_no_effect", String(error), "none", "retry_same_request")
  if (name === "TimeoutError") return new AdapterFailure("timeout", "timeout_no_effect", String(error), "none", "retry_same_request")
  if (code === "ENOENT") return new AdapterFailure("transport_failure", "missing_binary", String(error))
  return new AdapterFailure("transport_failure", "spawn_failure", String(error))
}

function singleJSON(text: string): any {
  const value = text.trim()
  if (!value || value.includes("\n") && value.split("\n").filter(Boolean).length !== 1) throw new Error("core stdout was not exactly one JSON value")
  const parsed = JSON.parse(value)
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("core stdout was not a JSON object")
  return parsed
}

const coreErrorKinds = new Set(["unknown_scope", "ambiguous_scope", "stale_context", "unauthorized", "approval_required", "approval_invalid", "version_conflict", "idempotency_conflict", "operation_conflict", "invalid_transition", "invalid_relation", "invariant_violation", "missing_evidence", "not_terminal", "outcome_mismatch", "stale_requires_review", "stale_law_revision", "domain_overlap", "degraded_not_allowed", "unreachable", "invalid_cursor", "limit_exceeded", "budget_refused", "invalid_input", "cancelled", "timeout", "transport_failure", "malformed_response", "internal_error"])

function validateCoreResponse(response: any, toolName: string, operation: string): boolean {
  if (!response || typeof response !== "object" || response.schema_version !== "1.0" || response.manifest_digest !== manifestDigest || response.origin !== "core" || response.tool !== toolName || response.operation !== operation || !["ok", "pending", "partial", "error"].includes(response.outcome) || !validateGeneratedEnvelope(response)) return false
  if (response.outcome === "error") return !!response.error && coreErrorKinds.has(response.error.kind)
  if (response.result !== undefined) {
    const meta: any = contractOperations.find((item: any) => item.tool === toolName && item.id.endsWith(`.${operation}`))
    const resultName = meta?.result_schema?.split("/").pop()
    if (!resultName || !validateGeneratedPayload(resultName, response.result)) return false
  }
  return true
}


function selectedProductID() {
  const value = process.env.CONCORD_SELECTED_PRODUCT_ID ?? ""
  return /^[A-Za-z0-9][A-Za-z0-9._:-]{1,127}$/.test(value) ? value : ""
}

type AmbientContext = { projectID: string; productIDs: string[]; scopeVersion: string }

async function resolveAmbientContext(context: ToolContext): Promise<AmbientContext> {
  let result
  try {
    result = await runner.run([process.env.CONCORD_BIN ?? "concord", "project-resolve"], JSON.stringify({ directory: context.directory, worktree: context.worktree }), context.abort)
  } catch (error) {
    throw runnerFailure(error, context.abort.aborted)
  }
  if (result.exitCode !== 0) throw new AdapterFailure("transport_failure", "io_failure", result.stderr.slice(0, MAX_STDERR))
  let response
  try { response = singleJSON(result.stdout) } catch (error) { throw new AdapterFailure("malformed_response", "malformed_core_response", String(error)) }
  if (typeof response.project_id !== "string" || response.project_id.length === 0 || typeof response.scope_version !== "string" || response.scope_version.length === 0 || !Array.isArray(response.product_ids) || !response.product_ids.every((value: unknown) => typeof value === "string")) {
    throw new AdapterFailure("malformed_response", "malformed_core_response", "project-resolve response failed the context contract")
  }
  return { projectID: response.project_id, productIDs: response.product_ids, scopeVersion: response.scope_version }
}

// invokeConcordOperation is the single `concord project-resolve` + `concord invoke`
// transport for every adapter surface, including host-side callers outside the
// tool exports below. It owns envelope construction, the closed core-response
// contract check, and the approval_required resubmission.
export async function invokeConcordOperation(toolName: string, args: any, context: ToolContext): Promise<any> {
  const operation = args.operation
  const requestID = `${context.sessionID}-${context.messageID}`
  if (toolName === "concord_work_transition" && operation === "workflow_action" && args.input?.action_id === "confirm_premise") {
    if (args.input.selected_choice !== "confirm" || typeof args.input.decision_context_digest !== "string" || !/^sha256:[0-9a-f]{64}$/.test(args.input.decision_context_digest)) {
      return adapterError(toolName, operation, requestID, "invalid_input", "missing_question_selection", "confirm_premise requires the closed confirm choice and a decision context digest", "none", "reread_entities")
    }
  }
  let ambient: AmbientContext
  try { ambient = await resolveAmbientContext(context) } catch (error) { return failureEnvelope(toolName, operation, requestID, error, "context_resolution_failed") }
  const selectedProduct = selectedProductID() || (ambient.productIDs.length === 1 ? ambient.productIDs[0] : "")
  const envelope: any = { schema_version: "1.0", request_id: requestID, client_ref: clientRef(), principal_ref: "", session_ref: context.sessionID, agent_ref: context.agent, directory: context.directory, worktree: context.worktree, ambient_project_id: ambient.projectID, selected_product_id: selectedProduct, scope_version: ambient.scopeVersion, manifest_digest: manifestDigest }
  const run = async (input: any) => runner.run([process.env.CONCORD_BIN ?? "concord", "invoke"], JSON.stringify({ call_envelope: envelope, tool: toolName, operation, input }), context.abort)
  let result: any
  try { result = await run(args.input) } catch (error) { return failureEnvelope(toolName, operation, requestID, runnerFailure(error, context.abort.aborted), "spawn_failure") }
  if (result.exitCode !== 0 && !result.stdout.trim()) return adapterError(toolName, operation, requestID, "operation_conflict", "unknown_effect", result.stderr.slice(0, MAX_STDERR), "possible", "reconcile_operation")
  let response: any
  try { response = singleJSON(result.stdout) } catch (error) { return adapterError(toolName, operation, requestID, "malformed_response", "malformed_core_response", String(error), "possible", "reconcile_operation") }
  if (!validateCoreResponse(response, toolName, operation)) return adapterError(toolName, operation, requestID, "malformed_response", "malformed_core_response", "core response failed the generated TS7 contract", "possible", "reconcile_operation")
  if (response?.outcome === "error" && response?.error?.kind === "approval_required") {
    const details = response.error.details ?? {}
    const requiredChallengeFields = ["approval_ref", "operation_digest"]
    if (toolName === "concord_work_transition" && operation === "workflow_action") {
      requiredChallengeFields.push("work_id", "action_id", "contract_version", "selected_choice", "premise_summary")
      if (args.input?.action_id === "confirm_premise") requiredChallengeFields.push("decision_context_digest")
    }
    if (toolName === "concord_work_relate" && operation === "resolve_overlap") {
      requiredChallengeFields.push("summary", "resolution_kind", "from_work_id", "to_work_id")
    }
    if (requiredChallengeFields.some((key) => typeof details[key] !== "string" || details[key].length === 0)) return adapterError(toolName, operation, requestID, "malformed_response", "malformed_core_response", "core approval challenge lacked exact workflow metadata")
    if (toolName === "concord_work_transition" && operation === "workflow_action" && Array.from(details.premise_summary ?? "").length > 256) return adapterError(toolName, operation, requestID, "malformed_response", "malformed_core_response", "core approval challenge premise summary exceeded the public bound")
    if (!Array.isArray(details.scope) || !Array.isArray(details.versions) || (toolName === "concord_work_transition" && operation === "workflow_action" && details.selected_choice !== args.input?.selected_choice)) return adapterError(toolName, operation, requestID, "malformed_response", "malformed_core_response", "core approval challenge did not bind the exact workflow selection")
    // CD-0037 D5: the typed consequence summary rides host permission metadata
    // unchanged. The host renders the operator prompt; this is transport, not
    // adapter-owned domain logic.
    const consequenceSummary = response.error.consequence_summary && typeof response.error.consequence_summary === "object" ? response.error.consequence_summary : null
    const askMetadata = toolName === "concord_work_transition" && operation === "workflow_action"
      ? { approval_ref: details.approval_ref, operation_digest: details.operation_digest, work_id: details.work_id, action_id: details.action_id, contract_version: details.contract_version, selected_choice: details.selected_choice, decision_context_digest: details.decision_context_digest, premise_summary: details.premise_summary, ...(consequenceSummary ? { consequence_summary: consequenceSummary } : {}) }
      : {
          approval_ref: details.approval_ref,
          operation_digest: details.operation_digest,
          ...(consequenceSummary ? { consequence_summary: consequenceSummary } : {}),
          ...(typeof details.summary === "string" ? { summary: details.summary } : {}),
          ...(Array.isArray(details.scope) ? { scope: details.scope } : {}),
          ...(Array.isArray(details.versions) ? { versions: details.versions } : {}),
          ...(typeof details.resolution_kind === "string" ? { resolution_kind: details.resolution_kind } : {}),
          ...(typeof details.from_work_id === "string" ? { from_work_id: details.from_work_id } : {}),
          ...(typeof details.to_work_id === "string" ? { to_work_id: details.to_work_id } : {}),
          // CD-0037 D5: the typed consequence summary is copied unchanged
          // into host permission metadata; the host renders it.
          ...(response.error?.consequence_summary ? { consequence_summary: response.error.consequence_summary } : {}),
        }
    // Built-in question supplies semantic choice; ToolContext.ask authorizes
    // only this exact core-issued challenge.
    try { await context.ask({ permission: `concord:${toolName}.${operation}`, patterns: [], always: [], metadata: askMetadata }) } catch { return adapterError(toolName, operation, requestID, "cancelled", "cancelled_no_effect", "host approval was rejected") }
    envelope.host_approval_assertion = { challenge_ref: details.approval_ref, request_digest: details.operation_digest, scope: details.scope, versions: details.versions, session_ref: envelope.session_ref, agent_ref: envelope.agent_ref, worktree: context.worktree, issued_at: new Date().toISOString() }
    const approvedInput = args.input && typeof args.input === "object" && !Array.isArray(args.input) ? { ...args.input, approval: { approval_ref: details.approval_ref } } : null
    if (!approvedInput) return adapterError(toolName, operation, requestID, "malformed_response", "malformed_core_response", "approval resubmission requires object input")
    try { result = await run(approvedInput) } catch (error) { return failureEnvelope(toolName, operation, requestID, runnerFailure(error, context.abort.aborted), "unknown_effect", "possible") }
    try { response = singleJSON(result.stdout) } catch (error) { return adapterError(toolName, operation, requestID, "operation_conflict", "unknown_effect", String(error), "possible", "reconcile_operation") }
    if (!validateCoreResponse(response, toolName, operation)) return adapterError(toolName, operation, requestID, "operation_conflict", "unknown_effect", "post-approval response failed the TS7 contract", "possible", "reconcile_operation")
  }
  return response;
}

export const product_view = tool({ description: "Concord product view", args: argsSchema("concord_product_view"), execute: (args: any, context: ToolContext) => invokeConcordOperation("concord_product_view", args, context) })
export const work_browse = tool({ description: "Concord work browse", args: argsSchema("concord_work_browse"), execute: (args: any, context: ToolContext) => invokeConcordOperation("concord_work_browse", args, context) })
export const work_trace = tool({ description: "Concord work trace", args: argsSchema("concord_work_trace"), execute: (args: any, context: ToolContext) => invokeConcordOperation("concord_work_trace", args, context) })
export const knowledge = tool({ description: "Concord knowledge", args: argsSchema("concord_knowledge"), execute: (args: any, context: ToolContext) => invokeConcordOperation("concord_knowledge", args, context) })
export const work_define = tool({ description: "Concord work define", args: argsSchema("concord_work_define"), execute: (args: any, context: ToolContext) => invokeConcordOperation("concord_work_define", args, context) })
export const domain = tool({ description: "Concord domain", args: argsSchema("concord_domain"), execute: (args: any, context: ToolContext) => invokeConcordOperation("concord_domain", args, context) })
export const work_initiative = tool({ description: "Concord work initiative", args: argsSchema("concord_work_initiative"), execute: (args: any, context: ToolContext) => invokeConcordOperation("concord_work_initiative", args, context) })
export const work_transition = tool({ description: "Concord work transition", args: argsSchema("concord_work_transition"), execute: (args: any, context: ToolContext) => executeWorkTransition(args, context) })
export const work_relate = tool({ description: "Concord work relate", args: argsSchema("concord_work_relate"), execute: (args: any, context: ToolContext) => invokeConcordOperation("concord_work_relate", args, context) })
export const work_compact = tool({ description: "Concord work compact", args: argsSchema("concord_work_compact"), execute: (args: any, context: ToolContext) => invokeConcordOperation("concord_work_compact", args, context) })

// laneDispatchRequest decides whether a work_transition invocation routes to
// the lane dispatcher (CD-0067 D5) or falls through to the generic core
// transport. It is a pure helper so the routing decision can be tested
// without standing up the dispatch path.
//
// Returns:
//   - null: the invocation is not a dispatch_worker request; the caller must
//     use the generic transport.
//   - { error: string }: the request is a dispatch_worker request but lacks
//     the tool-level lane_id vocabulary the adapter needs to derive the
//     packet. The caller returns a typed invalid_input envelope.
//   - LaneDispatchInput: the request is a well-formed dispatch_worker call
//     with object-form fields and a string lane_id. The caller forwards it
//     to dispatchLaneWorker.
export function laneDispatchRequest(args: any): LaneDispatchInput | { error: string } | null {
  if (!args || typeof args !== "object" || Array.isArray(args)) return null
  const input = (args as { input?: unknown }).input
  if (!input || typeof input !== "object" || Array.isArray(input)) return null
  const inner = input as Record<string, unknown>
  if (inner.action_id !== "dispatch_worker") return null
  const fields = inner.fields
  if (!fields || typeof fields !== "object" || Array.isArray(fields)) {
    return { error: "dispatch_worker requires fields.lane_id naming the target lane" }
  }
  const laneId = (fields as Record<string, unknown>).lane_id
  if (typeof laneId !== "string" || laneId.length === 0) {
    return { error: "dispatch_worker requires fields.lane_id naming the target lane" }
  }
  if (typeof inner.work_id !== "string" || typeof inner.expected_version !== "number" || typeof inner.idempotency_key !== "string") {
    return null
  }
  return { work_id: inner.work_id, expected_version: inner.expected_version, idempotency_key: inner.idempotency_key, lane_id: laneId }
}

// executeWorkTransition is the work_transition tool body. dispatch_worker is
// routed through dispatchLaneWorker (CD-0067 D5); every other workflow_action
// falls through to the generic core transport. The dispatch path shares the
// same transport seam as every other adapter tool, so a host-side caller
// receives the same envelope shape on either branch.
async function executeWorkTransition(args: any, context: ToolContext): Promise<unknown> {
  if (args?.operation === "workflow_action") {
    const request = laneDispatchRequest(args)
    if (request && "error" in request) {
      const fields = args?.input?.fields
      const partial: { lane_id?: string } = {}
      if (fields && typeof fields === "object" && !Array.isArray(fields)) {
        const candidate = (fields as Record<string, unknown>).lane_id
        if (typeof candidate === "string") partial.lane_id = candidate
      }
      return errorEnvelopeForLane(null, partial, "error", "invalid_input", request.error, "retry_same_request")
    }
    if (request) {
      return dispatchLaneWorker(request, { context, invoke: invokeConcordOperation })
    }
  }
  return invokeConcordOperation("concord_work_transition", args, context)
}
