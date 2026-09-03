import { clientRef } from "./credentials"
import { contractOperations, hostToolDescriptions, hostToolSchemas, manifestDigest, maxEnvelopeBytes, payloadSchemas } from "./generated-contracts"
import { validateGeneratedEnvelope, validateGeneratedPayload, envelopeFailurePath, payloadFailurePath } from "./generated-contract-tests"
import { dispatchLaneWorker, type LaneDispatchInput } from "./lane_dispatch"
import { hostControlPlane, MoveSessionUnavailable } from "./move-session"
import { createRunSessionObservation, errorEnvelopeForLane, MAX_OUTPUT_BYTES, observeRunSessionLine, readExportSessionMetadata, readRunSessionMetadata, readRunTextParts, runStreamRefusalMessage, runStreamRefusalRecovery, validateAgainstSchema, type AgentResultEnvelope, type RunLineMetadata, type RunSessionObservation } from "./dispatch"

type ToolContext = {
  sessionID: string
  messageID: string
  agent: string
  directory: string
  worktree: string
  abort: AbortSignal
  metadata: (input: { title?: string; metadata?: { [key: string]: any } }) => void
  ask: (request: { permission: string; patterns: string[]; always: string[]; metadata: { [key: string]: any } }) => Promise<void>
}
type ToolResult = string | { output: string; title?: string; metadata?: Record<string, unknown>; attachments?: unknown[] }
function tool<T>(definition: T): T { return definition }

const MAX_STDERR = 8192
const MAX_WORK_START_OUTPUT_BYTES = 16_384
const MAX_SALVAGE_BYTES = 16_384

type HostToolArgs = Record<string, unknown> & { operation: string; input: Record<string, unknown> }
type HostToolCall = { request: HostToolArgs }
type JSONSchema = Record<string, unknown>
type CoreConcordEnvelope = Record<string, unknown>
type HostConcordEnvelope = CoreConcordEnvelope | AgentResultEnvelope

export interface ChildRunnerOptions { cwd?: string; env?: Record<string, string>; onStdoutLine?: (line: string, metadata?: RunLineMetadata | null) => Promise<void>; runSessionObservation?: RunSessionObservation }
export interface ChildRunner { run(argv: string[], input: string, signal: AbortSignal, options?: ChildRunnerOptions): Promise<{ exitCode: number; stdout: string; stderr: string; runSessionObservation?: RunSessionObservation }> }

function appendOutputTail(current: string, text: string): string {
  const combined = current + text
  if (Buffer.byteLength(combined) <= MAX_OUTPUT_BYTES) return combined
  const bytes = Buffer.from(combined)
  let start = bytes.length - MAX_OUTPUT_BYTES
  while (start < bytes.length && (bytes[start] & 0xc0) === 0x80) start++
  return bytes.subarray(start).toString()
}

export async function readChildStdout(stream: ReadableStream<Uint8Array>, onLine?: (line: string, metadata?: RunLineMetadata | null) => Promise<void>, observation = createRunSessionObservation()): Promise<{ stdout: string; runSessionObservation: RunSessionObservation }> {
  const reader = stream.getReader()
  const decoder = new TextDecoder()
  let output = ""
  let pending = ""
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    const text = decoder.decode(value, { stream: true })
    output = appendOutputTail(output, text)
    pending += text
    for (;;) {
      const newline = pending.indexOf("\n")
      if (newline < 0) break
      const line = pending.slice(0, newline)
      pending = pending.slice(newline + 1)
      if (line.trim()) {
        const metadata = observeRunSessionLine(observation, line)
        if (onLine) await onLine(line, metadata)
      }
    }
  }
  const final = decoder.decode()
  output = appendOutputTail(output, final)
  pending += final
  if (pending.trim()) {
    const metadata = observeRunSessionLine(observation, pending)
    if (onLine) await onLine(pending, metadata)
  }
  return { stdout: output, runSessionObservation: observation }
}

const defaultRunner: ChildRunner = {
  async run(argv, input, signal, options) {
    const child = Bun.spawn(argv, {
      stdin: "pipe",
      stdout: "pipe",
      stderr: "pipe",
      ...(options?.cwd ? { cwd: options.cwd } : {}),
      env: { ...process.env, ...(options?.env ?? {}) } as Record<string, string>,
    })
    const abort = () => child.kill()
    if (signal.aborted) abort()
    signal.addEventListener("abort", abort, { once: true })
    await child.stdin.write(input)
    await child.stdin.end()
    const stderrPromise = new Response(child.stderr).text()
    try {
      const [captured, stderr, exitCode] = await Promise.all([readChildStdout(child.stdout, options?.onStdoutLine, options?.runSessionObservation), stderrPromise, child.exited])
      return { exitCode, stdout: captured.stdout, stderr, runSessionObservation: captured.runSessionObservation }
    } catch (error) {
      child.kill()
      await child.exited
      throw error
    } finally {
      signal.removeEventListener("abort", abort)
    }
  },
}

let runner: ChildRunner = defaultRunner

export function configureConcordAdapter(overrides: { runner?: ChildRunner; reset?: boolean } = {}) {
  if (overrides.reset) runner = defaultRunner
  if (overrides.runner) runner = overrides.runner
}

function schemaName(ref: string): string {
  const prefix = ref.startsWith("#/$defs/") ? "#/$defs/" : ref.startsWith("#/schemas/") ? "#/schemas/" : ""
  const name = prefix ? ref.slice(prefix.length) : ""
  if (!name || !Object.hasOwn(payloadSchemas, name)) throw new Error(`unknown payload schema reference ${ref}`)
  return name
}

function collectSchemaRefs(value: unknown, names: Set<string>): void {
  if (Array.isArray(value)) {
    for (const item of value) collectSchemaRefs(item, names)
    return
  }
  if (typeof value !== "object" || value === null) return
  for (const [key, item] of Object.entries(value)) {
    if (key === "$ref" && typeof item === "string" && item.startsWith("#/$defs/")) names.add(schemaName(item))
    else collectSchemaRefs(item, names)
  }
}

function rewriteSchemaRefs(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(rewriteSchemaRefs)
  if (typeof value !== "object" || value === null) return value
  return Object.fromEntries(Object.entries(value).map(([key, item]) => [
    key,
    key === "$ref" && typeof item === "string" && item.startsWith("#/$defs/")
      ? `#/properties/request/definitions/${schemaName(item)}`
      : rewriteSchemaRefs(item),
  ]))
}

export function publishedRequestSchema(toolName: string): JSONSchema {
  const operations = contractOperations.filter((operation: any) => operation.tool === toolName)
  if (operations.length === 0) throw new Error(`tool ${toolName} has no generated operations`)
  const needed = new Set<string>(operations.map((operation: any) => schemaName(operation.input_schema)))
  const definitions: Record<string, unknown> = {}
  while (true) {
    const pending = [...needed].filter((name) => !Object.hasOwn(definitions, name)).sort()
    if (pending.length === 0) break
    for (const name of pending) {
      const schema = (payloadSchemas as Record<string, unknown>)[name]
      collectSchemaRefs(schema, needed)
      definitions[name] = rewriteSchemaRefs(schema)
    }
  }
  return {
    oneOf: operations.map((operation: any) => ({
      type: "object",
      additionalProperties: false,
      required: ["operation", "input"],
      properties: {
        operation: { type: "string", const: operation.id.slice(operation.id.indexOf(".") + 1) },
        input: { $ref: `#/properties/request/definitions/${schemaName(operation.input_schema)}` },
      },
    })),
    definitions,
  }
}

function argsSchema(toolName: string): any {
  return { request: publishedRequestSchema(toolName) }
}

function workStartArgsSchema() {
  const schema = (hostToolSchemas as Record<string, any>).concord_work_start
  const required = new Set(schema.required ?? [])
  return Object.fromEntries(Object.entries(schema.properties ?? {}).map(([key, value]) => [key, required.has(key) ? value : undefined]))
}

function baseEnvelope(toolName: string, operation: string, requestID: string) {
  const queryID = (contractOperations.find((candidate: any) => candidate.tool === toolName && candidate.id.endsWith(`.${operation}`)) as any)?.query_id
  return { schema_version: "1.0", manifest_digest: manifestDigest, request_id: requestID, origin: "adapter", tool: toolName, operation, ...(queryID ? { query_id: queryID } : {}), outcome: "error", resolved_scope: null, authority: "unreachable", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false }
}

function adapterError(toolName: string, operation: string, requestID: string, kind: string, reason: string, message: string, effect: "none" | "possible" | "partial" = "none", recovery = effect === "none" ? "retry_same_request" : "reconcile_operation", details?: Record<string, unknown>) {
  return { ...baseEnvelope(toolName, operation, requestID), error: { kind, retry_safe: effect === "none", recovery_action: { kind: recovery }, effect_state: effect, adapter_reason: reason, message, ...(details ? { details } : {}) } }
}

class AdapterFailure extends Error {
  constructor(readonly kind: string, readonly reason: string, message: string, readonly effect: "none" | "possible" | "partial" = "none", readonly recovery = effect === "none" ? "contact_operator" : "reconcile_operation") { super(message) }
}

function failureEnvelope(toolName: string, operation: string, requestID: string, error: unknown, fallbackReason: string, effect: "none" | "possible" | "partial" = "none", forcedEffect?: "none" | "possible" | "partial", forcedRecovery?: string) {
  if (error instanceof AdapterFailure) return adapterError(toolName, operation, requestID, error.kind, error.reason, error.message, forcedEffect ?? error.effect, forcedRecovery ?? error.recovery)
  return adapterError(toolName, operation, requestID, "transport_failure", fallbackReason, String(error), effect, effect === "none" ? "contact_operator" : "reconcile_operation")
}

function runnerFailure(error: unknown, aborted: boolean) {
  if (error instanceof AdapterFailure) return error
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

function saneWorkID(value: unknown): value is string {
  return typeof value === "string" && /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(value)
}

function saneWorktreePath(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= 4096 && value.startsWith("/")
}

function saneChangedRefs(value: unknown): value is Array<Record<string, string>> {
  return Array.isArray(value) && value.length <= 32 && value.every((item) => record(item)
    && typeof item.entity_kind === "string" && item.entity_kind.length > 0 && item.entity_kind.length <= 64
    && typeof item.id === "string" && item.id.length > 0 && item.id.length <= 128
    && typeof item.version === "string" && item.version.length > 0 && item.version.length <= 128
    && Object.keys(item).every((key) => key === "entity_kind" || key === "id" || key === "version"))
}

function salvageFailedResponse(raw: string): Record<string, unknown> | undefined {
  if (Buffer.byteLength(raw, "utf8") > MAX_SALVAGE_BYTES) return undefined
  let parsed: unknown
  try { parsed = JSON.parse(raw) } catch { return undefined }
  if (!record(parsed)) return undefined
  const salvaged: Record<string, unknown> = {}
  if (saneWorkID(parsed.work_id)) salvaged.work_id = parsed.work_id
  if (saneWorktreePath(parsed.worktree_path)) salvaged.worktree_path = parsed.worktree_path
  if (saneChangedRefs(parsed.changed_refs)) salvaged.changed_refs = parsed.changed_refs
  return Object.keys(salvaged).length > 0 ? salvaged : undefined
}

function salvageDetails(raw: string): Record<string, unknown> | undefined {
  const salvaged = salvageFailedResponse(raw)
  return salvaged ? { salvaged } : undefined
}

const coreErrorKinds = new Set(["unknown_scope", "ambiguous_scope", "stale_context", "unauthorized", "approval_required", "approval_invalid", "version_conflict", "idempotency_conflict", "operation_conflict", "resource_busy", "invalid_transition", "invalid_relation", "invariant_violation", "missing_evidence", "not_terminal", "outcome_mismatch", "stale_requires_review", "stale_law_revision", "domain_overlap", "degraded_not_allowed", "unreachable", "invalid_cursor", "limit_exceeded", "budget_refused", "invalid_input", "cancelled", "timeout", "transport_failure", "malformed_response", "internal_error"])

// coreResponseFailure names what a contract-failing response broke on, or
// returns null when the response satisfies the generated contract. The
// identity stage reports the envelope itself, because a mismatch there
// describes the whole response rather than one member, and version skew is
// classified at the call site before this detail reaches an operator.
function coreResponseFailure(response: any, toolName: string, operation: string): string | null {
  if (!response || typeof response !== "object" || response.schema_version !== "1.0" || response.manifest_digest !== manifestDigest || response.origin !== "core" || response.tool !== toolName || response.operation !== operation || !["ok", "pending", "partial", "error"].includes(response.outcome)) return "the envelope identity"
  if (!validateGeneratedEnvelope(response)) return `member ${envelopeFailurePath(response)} failed the generated envelope contract`
  if (response.outcome === "error") {
    if (!response.error) return "error is absent"
    if (!coreErrorKinds.has(response.error.kind)) return `member error.kind failed the generated envelope contract: ${JSON.stringify(response.error.kind)} is not a core error kind`
    return null
  }
  if (response.result !== undefined) {
    const meta: any = contractOperations.find((item: any) => item.tool === toolName && item.id.endsWith(`.${operation}`))
    const resultName = meta?.result_schema?.split("/").pop()
    if (!resultName) return "the operation declares no result schema"
    if (!validateGeneratedPayload(resultName, response.result)) return `member result.${payloadFailurePath(resultName, response.result)} failed the generated payload contract`
  }
  return null
}

// A core response shaped like an envelope but stamped with a manifest digest
// this adapter was not generated from is version skew: the adapter files on
// disk were replaced by a newer release while this session still runs the old
// module. The condition is deterministic, so it is classified instead of
// folded into malformed_core_response.
function isVersionSkew(response: any): boolean {
  return !!response && typeof response === "object" && response.schema_version === "1.0" && response.origin === "core" && typeof response.manifest_digest === "string" && response.manifest_digest !== manifestDigest
}

function operationIsMutation(toolName: string, operation: string): boolean {
  return contractOperations.some((item: any) => item.tool === toolName && item.id.endsWith(`.${operation}`) && item.kind === "mutation")
}


function selectedProductID() {
  const value = process.env.CONCORD_SELECTED_PRODUCT_ID ?? ""
  return /^[A-Za-z0-9][A-Za-z0-9._:-]{1,127}$/.test(value) ? value : ""
}

type AmbientContext = { projectID: string; productIDs: string[]; scopeVersion: string; mainWorktree: boolean }

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
  if (typeof response.project_id !== "string" || response.project_id.length === 0 || typeof response.scope_version !== "string" || response.scope_version.length === 0 || typeof response.main_worktree !== "boolean" || !Array.isArray(response.product_ids) || !response.product_ids.every((value: unknown) => typeof value === "string")) {
    throw new AdapterFailure("malformed_response", "malformed_core_response", "project-resolve response failed the context contract")
  }
  return { projectID: response.project_id, productIDs: response.product_ids, scopeVersion: response.scope_version, mainWorktree: response.main_worktree }
}

// invokeConcordOperation is the single `concord project-resolve` + `concord invoke`
// transport for every adapter surface, including host-side callers outside the
// tool exports below. It owns envelope construction, the closed core-response
// contract check, and the approval_required resubmission.
async function invokeConcordOperationRaw(toolName: string, args: HostToolArgs, context: ToolContext): Promise<CoreConcordEnvelope> {
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
  try { response = singleJSON(result.stdout) } catch (error) { return adapterError(toolName, operation, requestID, "malformed_response", "malformed_core_response", String(error), "possible", "reconcile_operation", salvageDetails(result.stdout)) }
  const contractFailure = coreResponseFailure(response, toolName, operation)
  if (contractFailure) {
    if (isVersionSkew(response)) {
      const skewDetail = `core contract digest ${response.manifest_digest} does not match this adapter's ${manifestDigest}; the adapter files were replaced on disk by a newer release while this session runs; restart the OpenCode session to load them`
      if (!operationIsMutation(toolName, operation)) return adapterError(toolName, operation, requestID, "transport_failure", "manifest_mismatch", skewDetail, "none", "contact_operator")
      return adapterError(toolName, operation, requestID, "operation_conflict", "unknown_effect", `${skewDetail}, then reconcile this operation`, "possible", "reconcile_operation")
    }
    return adapterError(toolName, operation, requestID, operationIsMutation(toolName, operation) ? "operation_conflict" : "malformed_response", operationIsMutation(toolName, operation) ? "unknown_effect" : "malformed_core_response", `core response failed the generated TS7 contract: ${contractFailure}`, "possible", "reconcile_operation", salvageDetails(result.stdout))
  }
  if (response?.outcome === "error" && response?.error?.kind === "approval_required") {
    const details = response.error.details ?? {}
    const requiredChallengeFields = ["approval_ref", "operation_digest"]
    if (toolName === "concord_work_transition" && operation === "workflow_action") {
      requiredChallengeFields.push("work_id", "action_id", "contract_version", "premise_summary")
      // `selected_choice` and `decision_context_digest` belong to
      // `confirm_premise` alone. The tool schema forbids both on every other
      // action, so the core emits `selected_choice` empty for them. Requiring
      // it unconditionally rejects a correct challenge and makes every
      // approval-gated action except `confirm_premise` unreachable.
      if (args.input?.action_id === "confirm_premise") requiredChallengeFields.push("selected_choice", "decision_context_digest")
    }
    if (toolName === "concord_work_relate" && operation === "resolve_overlap") {
      requiredChallengeFields.push("summary", "resolution_kind", "from_work_id", "to_work_id")
    }
    if (requiredChallengeFields.some((key) => typeof details[key] !== "string" || details[key].length === 0)) return adapterError(toolName, operation, requestID, "malformed_response", "malformed_core_response", "core approval challenge lacked exact workflow metadata")
    if (toolName === "concord_work_transition" && operation === "workflow_action" && Array.from(details.premise_summary ?? "").length > 256) return adapterError(toolName, operation, requestID, "malformed_response", "malformed_core_response", "core approval challenge premise summary exceeded the public bound")
    // The selection binds only where the surface admits one. `confirm_premise`
    // is the sole action whose schema carries `selected_choice`, and the core
    // emits the field as an empty string for every other action, so comparing
    // it against an absent caller value refuses a correct challenge and takes
    // every approval-gated action with it.
    const bindsSelection = toolName === "concord_work_transition" && operation === "workflow_action" && args.input?.action_id === "confirm_premise"
    if (!Array.isArray(details.scope) || !Array.isArray(details.versions) || (bindsSelection && details.selected_choice !== args.input?.selected_choice)) return adapterError(toolName, operation, requestID, "malformed_response", "malformed_core_response", "core approval challenge did not bind the exact workflow selection")
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
    try { result = await run(approvedInput) } catch (error) { return failureEnvelope(toolName, operation, requestID, runnerFailure(error, context.abort.aborted), "unknown_effect", "possible", "possible", "reconcile_operation") }
    try { response = singleJSON(result.stdout) } catch (error) { return adapterError(toolName, operation, requestID, "operation_conflict", "unknown_effect", String(error), "possible", "reconcile_operation", salvageDetails(result.stdout)) }
    const approvedFailure = coreResponseFailure(response, toolName, operation)
    if (approvedFailure) return adapterError(toolName, operation, requestID, "operation_conflict", "unknown_effect", `post-approval response failed the TS7 contract: ${approvedFailure}`, "possible", "reconcile_operation", salvageDetails(result.stdout))
  }
  return response as CoreConcordEnvelope;
}

function requestWorkID(args: HostToolArgs): string | undefined {
  const input = args.input
  return record(input) && saneWorkID(input.work_id) ? input.work_id : undefined
}

function addErrorDetails(response: CoreConcordEnvelope, details: Record<string, unknown>): CoreConcordEnvelope {
  if (!record(response.error)) return response
  return { ...response, error: { ...response.error, details: { ...(record(response.error.details) ? response.error.details : {}), ...details } } }
}

async function reconcileUnknownEffect(toolName: string, args: HostToolArgs, context: ToolContext, response: CoreConcordEnvelope): Promise<CoreConcordEnvelope> {
  if (!operationIsMutation(toolName, args.operation) || !record(response.error) || response.error.effect_state !== "possible") return response
  const workID = requestWorkID(args)
  if (!workID) return response
  try {
    const readback = await invokeConcordOperation("concord_work_browse", {
      operation: "list",
      input: { page: { cursor: null, limit: 1 }, work_ids: [workID] },
    }, context)
    if (readback.outcome !== "ok" || !record(readback.result) || !Array.isArray(readback.result.items)) throw new Error("reconcile read failed")
    const item = readback.result.items.find((candidate: unknown) => record(candidate) && candidate.id === workID)
    return addErrorDetails(response, { reconciled: { found: !!item, lifecycle: item?.lifecycle ?? null, version: item?.version ?? null } })
  } catch {
    return addErrorDetails(response, { reconcile_attempted: true })
  }
}

export async function invokeConcordOperation(toolName: string, args: HostToolArgs, context: ToolContext): Promise<CoreConcordEnvelope> {
  return reconcileUnknownEffect(toolName, args, context, await invokeConcordOperationRaw(toolName, args, context))
}

function encodeHostResult(toolName: string, operation: string, requestID: string, envelope: HostConcordEnvelope): ToolResult {
  let output = JSON.stringify(envelope)
  if (Buffer.byteLength(output) > maxEnvelopeBytes) {
    output = JSON.stringify(adapterError(toolName, operation, requestID, "malformed_response", "malformed_core_response", `Concord result exceeds ${maxEnvelopeBytes} bytes`, "possible", "reconcile_operation"))
  }
  return { title: toolName, output, metadata: {} }
}

async function encodeHostToolResult(toolName: string, args: HostToolArgs, context: ToolContext, envelope: HostConcordEnvelope): Promise<ToolResult> {
  if (Buffer.byteLength(JSON.stringify(envelope)) > maxEnvelopeBytes) {
    const oversized = adapterError(toolName, args.operation, `${context.sessionID}-${context.messageID}`, "malformed_response", "malformed_core_response", `Concord result exceeds ${maxEnvelopeBytes} bytes`, "possible", "reconcile_operation")
    return encodeHostResult(toolName, args.operation, `${context.sessionID}-${context.messageID}`, await reconcileUnknownEffect(toolName, args, context, oversized))
  }
  return encodeHostResult(toolName, args.operation, `${context.sessionID}-${context.messageID}`, envelope)
}

async function executeHostTool(toolName: string, args: HostToolArgs, context: ToolContext): Promise<ToolResult> {
  return encodeHostToolResult(toolName, args, context, await invokeConcordOperation(toolName, args, context))
}

async function executeHostTransition(args: HostToolArgs, context: ToolContext): Promise<ToolResult> {
  return encodeHostToolResult("concord_work_transition", args, context, await executeWorkTransition(args, context))
}

type WorkStartArgs = {
  title: string
  value_statement: string
  kind: string
  task: string
  idempotency_key: string
  priority?: number
  urgency?: string
  tags?: string[]
  workflow_type_ref?: string
  external_ref?: string
  governing_requirements?: string[]
  ref?: string
}

type WorkStartBootstrap = {
  schema_version: "1.0"
  operation_id: string
  replayed: boolean
  product_id: string
  project_id: string
  work_id: string
  work_version: number
  worktree: { set_id: string; path: string; branch: string; base_sha: string; state: "active" }
}

type WorkStartPrepared = { schema_version: "1.0"; agent: string; directory: string; product_id: string; work_id: string; prompt: string }

// WorkStartEnvelope is the host-tool result. There is no partial outcome:
// every step of work_start is idempotent on the derived key, so a failure
// leaves nothing a replay cannot adopt, and the answer is ok or a refusal.
type WorkStartEnvelope = {
  schema_version: "1.0"
  outcome: "ok" | "error"
  product_id?: string
  project_id?: string
  work_id?: string
  worktree_path?: string
  agent?: string
  session_id?: string | null
  output?: string
  error?: { kind: string; retry_safe: boolean; recovery_action: { kind: string }; effect_state: "none"; message: string }
}

function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

function exactKeys(value: Record<string, unknown>, required: string[]): boolean {
  return Object.keys(value).length === required.length && required.every((key) => key in value)
}

function nonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.length > 0
}

const workStartSchema = (hostToolSchemas as Record<string, any>).concord_work_start
const workStartFields = new Set(Object.keys(workStartSchema.properties ?? {}))

function validateWorkStartArgs(value: unknown): value is WorkStartArgs {
  if (!record(value) || !validateAgainstSchema(workStartSchema, value)) return false
  if (Object.keys(value).some((key) => !workStartFields.has(key))) return false
  for (const field of ["title", "value_statement", "external_ref"] as const) {
    const candidate = value[field]
    if (candidate !== undefined && Buffer.byteLength(String(candidate)) > 256) return false
  }
  return Buffer.byteLength(String(value.task)) <= 8192
}

function deriveWorkStartProduct(context: AmbientContext): string {
  const selected = process.env.CONCORD_SELECTED_PRODUCT_ID
  if (selected !== undefined && selected !== "") {
    if (!/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(selected) || !context.productIDs.includes(selected)) throw new AdapterFailure("invalid_input", "product_selection_not_in_project", "selected Product is not a member of the resolved Project")
    return selected
  }
  if (context.productIDs.length !== 1) throw new AdapterFailure("invalid_input", "ambiguous_product_selection", "the resolved Project does not have one unambiguous Product")
  return context.productIDs[0]
}

function validateWorkStartBootstrap(value: unknown): value is WorkStartBootstrap {
  if (!record(value) || !exactKeys(value, ["schema_version", "operation_id", "replayed", "product_id", "project_id", "work_id", "work_version", "worktree"])) return false
  if (value.schema_version !== "1.0" || !nonEmptyString(value.operation_id) || typeof value.replayed !== "boolean" || !nonEmptyString(value.product_id) || !nonEmptyString(value.project_id) || !nonEmptyString(value.work_id) || typeof value.work_version !== "number" || !Number.isInteger(value.work_version) || value.work_version < 1 || !record(value.worktree)) return false
  const worktree = value.worktree
  return exactKeys(worktree, ["set_id", "path", "branch", "base_sha", "state"])
    && nonEmptyString(worktree.set_id)
    && typeof worktree.path === "string" && worktree.path.startsWith("/")
    && nonEmptyString(worktree.branch) && /^[0-9a-f]{40}$/.test(String(worktree.base_sha)) && worktree.state === "active"
}

function validateWorkStartPrepared(value: unknown, bootstrap: WorkStartBootstrap): value is WorkStartPrepared {
  if (!record(value) || !exactKeys(value, ["schema_version", "agent", "directory", "product_id", "work_id", "prompt"])) return false
  return value.schema_version === "1.0"
    && value.directory === bootstrap.worktree.path
    && value.product_id === bootstrap.product_id
    && value.work_id === bootstrap.work_id
    && nonEmptyString(value.agent) && /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(value.agent)
    && typeof value.prompt === "string" && value.prompt.length > 0 && Buffer.byteLength(value.prompt) <= 65_536
}

function boundedUTF8(value: string, maxBytes: number): string {
  if (Buffer.byteLength(value) <= maxBytes) return value
  return new TextDecoder().decode(Buffer.from(value).subarray(0, maxBytes))
}

// samePath compares an absolute path the host reported with one Concord
// claimed. Both are absolute and already resolved, so only a trailing
// separator distinguishes two spellings of one directory.
function samePath(left: string, right: string): boolean {
  const trim = (value: string) => (value.length > 1 && value.endsWith("/") ? value.slice(0, -1) : value)
  return trim(left) === trim(right)
}

function workStartError(kind: string, message: string, identity: Partial<WorkStartEnvelope> = {}, recovery = "retry_same_request", retrySafe = true): WorkStartEnvelope {
  return {
    schema_version: "1.0",
    outcome: "error",
    ...identity,
    error: {
      kind,
      retry_safe: retrySafe,
      recovery_action: { kind: recovery },
      effect_state: "none",
      message: boundedUTF8(message, MAX_STDERR),
    },
  }
}

// workStartFailure shapes a refusal. The identity of what exists rides along
// when bootstrap ran, so the caller can see the work item and worktree a
// replay will adopt. effect_state is none because nothing here is an effect
// a replay cannot reproduce or reuse; the durable state is the work item and
// the claim, both keyed on the request digest.
function workStartFailure(error: unknown, bootstrap: WorkStartBootstrap | null, fallbackKind: string): WorkStartEnvelope {
  const failure = error instanceof AdapterFailure ? error : new AdapterFailure("transport_failure", fallbackKind, String(error))
  const identity = bootstrap ? { product_id: bootstrap.product_id, project_id: bootstrap.project_id, work_id: bootstrap.work_id, worktree_path: bootstrap.worktree.path } : {}
  const retrySafe = failure.recovery === "retry_same_request"
  return workStartError(failure.kind, failure.message, identity, failure.recovery, retrySafe)
}

async function runWorkStartChild(argv: string[], input: string, signal: AbortSignal, options?: ChildRunnerOptions) {
  try { return await runner.run(argv, input, signal, options) } catch (error) { throw runnerFailure(error, signal.aborted) }
}

// executeWorkStart replays to convergence. Each step is idempotent on the
// request's derived identity, so a replay under the same idempotency_key
// adopts whatever an earlier attempt left and runs only what is missing:
//
//   1. work-bootstrap derives the work item and the worktree from the request
//      digest, and replays the same operation on the same key.
//   2. session-prepare verifies that worktree and derives the boot packet. It
//      records nothing.
//   3. moveSession moves the calling session, and is a no-op when the session
//      already runs there.
//   4. The host reports the directory the session runs in. Success is refused
//      unless it is the claimed worktree.
//
// No step records intent ahead of its effect, so there is no partial state.
// The session's worktree is the directory it runs in, and the host owns that
// answer (CD-0098 D3).
async function executeWorkStart(args: WorkStartArgs, context: ToolContext): Promise<WorkStartEnvelope> {
  const concord = process.env.CONCORD_BIN ?? "concord"
  let bootstrap: WorkStartBootstrap | null = null
  try {
    if (!validateWorkStartArgs(args)) throw new AdapterFailure("invalid_input", "invalid_work_start_input", "work_start arguments failed the host-tool contract", "none", "contact_operator")
    if (context.abort.aborted) throw new AdapterFailure("cancelled", "cancelled_no_effect", "work_start was cancelled before bootstrap")
    const ambient = await resolveAmbientContext(context)
    if (!ambient.mainWorktree) throw new AdapterFailure("invalid_input", "requires_main_worktree", "work_start requires a resolved default checkout", "none", "contact_operator")
    const productID = deriveWorkStartProduct(ambient)
    // CD-0098 D2 makes the move the only route into the claimed worktree, so
    // a session that cannot reach its host cannot start work at all. Asking
    // first keeps that discovery in front of every effect.
    try {
      await hostControlPlane().probe(context.sessionID, context.abort)
    } catch (error) {
      const detail = error instanceof Error ? error.message : String(error)
      throw new AdapterFailure(
        "unreachable",
        "control_plane_unreachable",
        // CD-0098 D2 reaches the host through the client the plugin factory
        // hands the adapter, never through a URL it rebuilds, so a separate
        // server supplies nothing this probe needs. The remedy names what the
        // operator can actually change: the build this session runs on.
        `${detail}; nothing was captured or claimed. Concord reaches the host through the client the plugin factory hands it, so running a separate server does not supply one: restart this session on an OpenCode build that hands plugins a client and serves the session routes`,
        "none",
        "contact_operator",
      )
    }
    const bootstrapInput = { product_id: productID, project_id: ambient.projectID, ...args }
    const boot = await runWorkStartChild([concord, "work-bootstrap"], JSON.stringify(bootstrapInput), context.abort, { cwd: context.directory })
    if (boot.exitCode !== 0) throw new AdapterFailure("bootstrap_failure", "bootstrap_failed", boot.stderr.slice(0, MAX_STDERR), "none", "retry_same_request")
    let bootValue: unknown
    try { bootValue = singleJSON(boot.stdout) } catch (error) { throw new AdapterFailure("malformed_response", "malformed_bootstrap_response", String(error), "none", "retry_same_request") }
    if (!validateWorkStartBootstrap(bootValue) || bootValue.product_id !== productID || bootValue.project_id !== ambient.projectID) throw new AdapterFailure("malformed_response", "malformed_bootstrap_response", "work-bootstrap response failed the strict bootstrap contract", "none", "retry_same_request")
    bootstrap = bootValue

    if (context.abort.aborted) throw new AdapterFailure("cancelled", "cancelled_after_bootstrap", "work_start was cancelled after bootstrap; replay the same idempotency_key to resume", "none", "retry_same_request")
    const prepared = await runWorkStartChild([concord, "session-prepare"], JSON.stringify({ product_id: bootstrap.product_id, work_id: bootstrap.work_id, task: args.task }), context.abort, { cwd: bootstrap.worktree.path })
    if (prepared.exitCode !== 0) throw new AdapterFailure("session_prepare_failure", "session_prepare_failed", prepared.stderr.slice(0, MAX_STDERR), "none", "retry_same_request")
    let preparedValue: unknown
    try { preparedValue = singleJSON(prepared.stdout) } catch (error) { throw new AdapterFailure("malformed_response", "malformed_prepare_response", String(error), "none", "retry_same_request") }
    if (!validateWorkStartPrepared(preparedValue, bootstrap)) throw new AdapterFailure("malformed_response", "malformed_prepare_response", "session-prepare response failed the strict prepare contract", "none", "retry_same_request")
    const agent = preparedValue.agent

    if (context.abort.aborted) throw new AdapterFailure("cancelled", "cancelled_before_move", "work_start was cancelled before the move; replay the same idempotency_key to resume", "none", "retry_same_request")
    // CD-0098 D2. The move is the only route to the worktree. An absent route
    // refuses here; the claim the bootstrap recorded stays resumable rather
    // than being rolled back for a host-capability gap the operator can repair.
    try {
      await hostControlPlane().moveSession(context.sessionID, bootstrap.worktree.path, context.abort)
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      if (error instanceof MoveSessionUnavailable) {
        throw new AdapterFailure("unreachable", "move_session_route_unavailable", message, "none", "contact_operator")
      }
      throw new AdapterFailure("transport_failure", "move_session_refused", message, "none", "retry_same_request")
    }
    // CD-0098 D3. The destination is read back from the host, not assumed from
    // the request that asked for it, and success is refused unless the session
    // now runs in the claimed worktree.
    let landed: string
    try {
      landed = await hostControlPlane().sessionDirectory(context.sessionID, context.abort)
    } catch (error) {
      throw new AdapterFailure("malformed_response", "session_directory_unreadable", error instanceof Error ? error.message : String(error), "none", "retry_same_request")
    }
    if (!samePath(landed, bootstrap.worktree.path)) {
      throw new AdapterFailure("session_directory_mismatch", "move_destination_mismatch", `the session moved to ${JSON.stringify(landed)} rather than the claimed worktree ${JSON.stringify(bootstrap.worktree.path)}`, "none", "retry_same_request")
    }
    return {
      schema_version: "1.0",
      outcome: "ok",
      product_id: bootstrap.product_id,
      project_id: bootstrap.project_id,
      work_id: bootstrap.work_id,
      worktree_path: bootstrap.worktree.path,
      agent,
      session_id: context.sessionID,
      output: `This session now runs in ${bootstrap.worktree.path} on work item ${bootstrap.work_id}.`,
    }
  } catch (error) {
    return workStartFailure(error, bootstrap, "work_start_failed")
  }
}

export const product_view = tool({ description: "Concord product view", args: argsSchema("concord_product_view"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_product_view", args.request, context) })
export const work_browse = tool({ description: "Concord work browse", args: argsSchema("concord_work_browse"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_browse", args.request, context) })
export const work_trace = tool({ description: "Concord work trace", args: argsSchema("concord_work_trace"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_trace", args.request, context) })
export const knowledge = tool({ description: "Concord knowledge", args: argsSchema("concord_knowledge"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_knowledge", args.request, context) })
export const work_define = tool({ description: "Concord work define", args: argsSchema("concord_work_define"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_define", args.request, context) })
export const domain = tool({ description: "Concord domain", args: argsSchema("concord_domain"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_domain", args.request, context) })
export const work_initiative = tool({ description: "Concord work initiative", args: argsSchema("concord_work_initiative"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_initiative", args.request, context) })
export const work_transition = tool({ description: "Concord work transition", args: argsSchema("concord_work_transition"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTransition(args.request, context) })
export const work_relate = tool({ description: "Concord work relate", args: argsSchema("concord_work_relate"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_relate", args.request, context) })
export const work_compact = tool({ description: "Concord work compact", args: argsSchema("concord_work_compact"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_compact", args.request, context) })
export const work_start = tool({ description: hostToolDescriptions.concord_work_start, args: workStartArgsSchema(), execute: async (args: any, context: ToolContext): Promise<ToolResult> => {
  const envelope = await executeWorkStart(args as WorkStartArgs, context)
  let output = JSON.stringify(envelope)
  if (Buffer.byteLength(output) > maxEnvelopeBytes) output = JSON.stringify(workStartError("output_exceeded", `work_start result exceeds ${maxEnvelopeBytes} bytes`, { product_id: envelope.product_id, project_id: envelope.project_id, work_id: envelope.work_id, worktree_path: envelope.worktree_path }))
  return { title: "concord_work_start", output, metadata: {} }
} })

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
// WORKTREE_REMOVAL_OPERATIONS are the two typed operations that delete a
// worktree directory. Both reach `reclaimWorktreeRawTx`, so both carry the
// same occupancy observation and both report the same way (issue #722).
const WORKTREE_REMOVAL_OPERATIONS = new Set(["worktree_reclaim", "worktree_destroy"])

// observeSessionsForRemoval attaches the host's live session directories to a
// worktree removal. The store owns the worktree path and refuses on it; this
// side owns the only truthful answer to which sessions are live and where.
//
// A host it cannot read refuses the removal rather than reporting an empty
// list. "No session occupies this worktree" and "I could not look" are
// different answers, and only one of them makes a removal safe.
async function observeSessionsForRemoval(args: HostToolArgs, context: ToolContext): Promise<HostToolArgs | CoreConcordEnvelope> {
  try {
    const observed = await hostControlPlane().liveSessionDirectories(context.abort)
    return { ...args, input: { ...args.input, observed_session_directories: observed } }
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    return adapterError(
      "concord_work_transition",
      args.operation,
      `${context.sessionID}-${context.messageID}`,
      "transport_failure",
      "session_occupancy_unreadable",
      `${message}; the removal was refused because it cannot be shown safe. Nothing was removed`,
      "none",
      "contact_operator",
    )
  }
}

// reportWorktreeRemoval puts the completed removal in front of the operator.
// The agent that made the call may end its turn without relaying anything, and
// the session that was running in a neighbouring worktree has no other way to
// learn the directory is gone. Delivery is best effort: the removal already
// happened, and a host with no attached TUI must not turn it into a failure.
async function reportWorktreeRemoval(args: HostToolArgs, context: ToolContext, envelope: HostConcordEnvelope): Promise<void> {
  if (!record(envelope) || envelope.outcome !== "ok") return
  const workID = typeof args.input?.work_id === "string" ? args.input.work_id : "unknown work"
  await hostControlPlane().showToast(`Concord removed the worktree of ${workID}.`, "info", context.abort)
}

async function executeWorkTransition(args: HostToolArgs, context: ToolContext): Promise<HostConcordEnvelope> {
  if (WORKTREE_REMOVAL_OPERATIONS.has(args?.operation)) {
    const observed = await observeSessionsForRemoval(args, context)
    // An unreadable host answers with the refusal itself, so nothing reaches
    // the core and nothing is reported.
    if (!("operation" in observed && "input" in observed)) return observed as CoreConcordEnvelope
    const request = observed as HostToolArgs
    const envelope = await invokeConcordOperation("concord_work_transition", request, context)
    await reportWorktreeRemoval(request, context, envelope)
    return envelope
  }
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
