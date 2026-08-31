import { clientRef } from "./credentials"
import { contractOperations, hostToolDescriptions, hostToolSchemas, manifestDigest, maxEnvelopeBytes, payloadSchemas } from "./generated-contracts"
import { validateGeneratedEnvelope, validateGeneratedPayload } from "./generated-contract-tests"
import { dispatchLaneWorker, type LaneDispatchInput } from "./lane_dispatch"
import { errorEnvelopeForLane, readExportSessionMetadata, readRunLineMetadata, readRunSessionMetadata, readRunTextParts, validateAgainstSchema, type AgentResultEnvelope } from "./dispatch"

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

type HostToolArgs = Record<string, unknown> & { operation: string; input: Record<string, unknown> }
type HostToolCall = { request: HostToolArgs }
type JSONSchema = Record<string, unknown>
type CoreConcordEnvelope = Record<string, unknown>
type HostConcordEnvelope = CoreConcordEnvelope | AgentResultEnvelope

export interface ChildRunnerOptions { cwd?: string; env?: Record<string, string>; onStdoutLine?: (line: string) => Promise<void> }
export interface ChildRunner { run(argv: string[], input: string, signal: AbortSignal, options?: ChildRunnerOptions): Promise<{ exitCode: number; stdout: string; stderr: string }> }

async function readChildStdout(stream: ReadableStream<Uint8Array>, onLine?: (line: string) => Promise<void>): Promise<string> {
  const reader = stream.getReader()
  const decoder = new TextDecoder()
  let output = ""
  let pending = ""
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    const text = decoder.decode(value, { stream: true })
    output += text
    pending += text
    for (;;) {
      const newline = pending.indexOf("\n")
      if (newline < 0) break
      const line = pending.slice(0, newline)
      pending = pending.slice(newline + 1)
      if (onLine && line.trim()) await onLine(line)
    }
  }
  const final = decoder.decode()
  output += final
  pending += final
  if (onLine && pending.trim()) await onLine(pending)
  return output
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
      const [stdout, stderr, exitCode] = await Promise.all([readChildStdout(child.stdout, options?.onStdoutLine), stderrPromise, child.exited])
      return { exitCode, stdout, stderr }
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
export async function invokeConcordOperation(toolName: string, args: HostToolArgs, context: ToolContext): Promise<CoreConcordEnvelope> {
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
  return response as CoreConcordEnvelope;
}

function encodeHostResult(toolName: string, operation: string, requestID: string, envelope: HostConcordEnvelope): ToolResult {
  let output = JSON.stringify(envelope)
  if (Buffer.byteLength(output) > maxEnvelopeBytes) {
    output = JSON.stringify(adapterError(toolName, operation, requestID, "malformed_response", "malformed_core_response", `Concord result exceeds ${maxEnvelopeBytes} bytes`, "possible", "reconcile_operation"))
  }
  return { title: toolName, output, metadata: {} }
}

async function executeHostTool(toolName: string, args: HostToolArgs, context: ToolContext): Promise<ToolResult> {
  return encodeHostResult(toolName, args.operation, `${context.sessionID}-${context.messageID}`, await invokeConcordOperation(toolName, args, context))
}

async function executeHostTransition(args: HostToolArgs, context: ToolContext): Promise<ToolResult> {
  return encodeHostResult("concord_work_transition", args.operation, `${context.sessionID}-${context.messageID}`, await executeWorkTransition(args, context))
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

type WorkStartLaunch = { schema_version: "1.0"; operation_id: string; attempt_id: string; launch_state: string; session_id: string | null; spawn_permitted: boolean; rollback_permitted: boolean; agent: string; directory: string; product_id: string; work_id: string; prompt: string }

type WorkStartEnvelope = {
  schema_version: "1.0"
  outcome: "ok" | "partial" | "error"
  product_id?: string
  project_id?: string
  work_id?: string
  worktree_path?: string
  agent?: string
  readback_agent?: string
  readback_model?: string | null
  session_id?: string | null
  output?: string
  error?: { kind: string; retry_safe: boolean; recovery_action: { kind: string }; effect_state: "none" | "partial"; message: string }
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

function validateWorkStartLaunch(value: unknown, bootstrap: WorkStartBootstrap): value is WorkStartLaunch {
  if (!record(value) || !exactKeys(value, ["schema_version", "operation_id", "attempt_id", "launch_state", "session_id", "spawn_permitted", "rollback_permitted", "agent", "directory", "product_id", "work_id", "prompt"])) return false
  return value.schema_version === "1.0"
    && nonEmptyString(value.operation_id)
    && nonEmptyString(value.attempt_id)
    && ["prepared", "running", "failed", "completed"].includes(String(value.launch_state))
    && (value.session_id === null || (nonEmptyString(value.session_id) && /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(value.session_id)))
    && typeof value.spawn_permitted === "boolean"
    && typeof value.rollback_permitted === "boolean"
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

async function currentLaunchOwner(): Promise<{ owner_pid: number; owner_start: string }> {
  const stat = await Bun.file("/proc/self/stat").text()
  const closeParen = stat.lastIndexOf(")")
  if (closeParen < 0) throw new AdapterFailure("unreachable", "process_identity_unavailable", "host process stat has no command boundary")
  const fields = stat.slice(closeParen + 1).trim().split(/\s+/)
  const ownerStart = fields[19]
  if (!ownerStart || !/^\d+$/.test(ownerStart)) throw new AdapterFailure("unreachable", "process_identity_unavailable", "host process stat has no start identity")
  return { owner_pid: process.pid, owner_start: ownerStart }
}

function workStartError(kind: string, message: string, effect_state: "none" | "partial", identity: Partial<WorkStartEnvelope> = {}, recovery = effect_state === "partial" ? "exact_replay" : "retry_same_request", retrySafe = effect_state === "none"): WorkStartEnvelope {
  return {
    schema_version: "1.0",
    outcome: effect_state === "partial" ? "partial" : "error",
    ...identity,
    error: {
      kind,
      retry_safe: retrySafe,
      recovery_action: { kind: recovery },
      effect_state,
      message: boundedUTF8(message, MAX_STDERR),
    },
  }
}

function workStartFailure(error: unknown, bootstrap: WorkStartBootstrap | null, fallbackKind: string): WorkStartEnvelope {
  const failure = error instanceof AdapterFailure ? error : new AdapterFailure("transport_failure", fallbackKind, String(error))
  const identity = bootstrap ? { product_id: bootstrap.product_id, project_id: bootstrap.project_id, work_id: bootstrap.work_id, worktree_path: bootstrap.worktree.path } : {}
  const effect = bootstrap || failure.effect === "partial" || failure.effect === "possible" ? "partial" : "none"
  return workStartError(failure.kind, failure.message, effect, identity, failure.recovery, effect === "none")
}

async function runWorkStartChild(argv: string[], input: string, signal: AbortSignal, options?: ChildRunnerOptions) {
  try { return await runner.run(argv, input, signal, options) } catch (error) { throw runnerFailure(error, signal.aborted) }
}

async function executeWorkStart(args: WorkStartArgs, context: ToolContext): Promise<WorkStartEnvelope> {
  const concord = process.env.CONCORD_BIN ?? "concord"
  const opencode = process.env.OPENCODE_BIN ?? "opencode"
  let bootstrap: WorkStartBootstrap | null = null
  let rolledBack = false
  const rollbackBeforeLaunch = async (reason: string) => {
    if (!bootstrap) return
    const rollback = await runWorkStartChild([concord, "work-bootstrap-rollback"], JSON.stringify({ product_id: bootstrap.product_id, work_id: bootstrap.work_id, operation_id: bootstrap.operation_id, directory: bootstrap.worktree.path, reason: boundedUTF8(reason, MAX_STDERR) }), new AbortController().signal, { cwd: bootstrap.worktree.path })
    if (rollback.exitCode !== 0) throw new AdapterFailure("rollback_failure", "rollback_failed", rollback.stderr.slice(0, MAX_STDERR), "partial", "reconcile_operation")
    rolledBack = true
  }
  try {
    if (!validateWorkStartArgs(args)) throw new AdapterFailure("invalid_input", "invalid_work_start_input", "work_start arguments failed the host-tool contract")
    if (context.abort.aborted) throw new AdapterFailure("cancelled", "cancelled_no_effect", "work_start was cancelled before bootstrap")
    const ambient = await resolveAmbientContext(context)
    if (!ambient.mainWorktree) throw new AdapterFailure("invalid_input", "requires_main_worktree", "work_start requires a resolved default checkout")
    const productID = deriveWorkStartProduct(ambient)
    const bootstrapInput = { product_id: productID, project_id: ambient.projectID, ...args }
    const boot = await runWorkStartChild([concord, "work-bootstrap"], JSON.stringify(bootstrapInput), context.abort, { cwd: context.directory })
    if (boot.exitCode !== 0) throw new AdapterFailure("bootstrap_failure", "bootstrap_failed", boot.stderr.slice(0, MAX_STDERR))
    let bootValue: unknown
    try { bootValue = singleJSON(boot.stdout) } catch (error) { throw new AdapterFailure("malformed_response", "malformed_bootstrap_response", String(error)) }
    if (!validateWorkStartBootstrap(bootValue) || bootValue.product_id !== productID || bootValue.project_id !== ambient.projectID) throw new AdapterFailure("malformed_response", "malformed_bootstrap_response", "work-bootstrap response failed the strict bootstrap contract")
    bootstrap = bootValue

    if (context.abort.aborted) {
      await rollbackBeforeLaunch("work_start was cancelled before the child launch")
      throw new AdapterFailure("cancelled", "cancelled_after_bootstrap", "work_start was cancelled after bootstrap")
    }
    let launchOwner
    try {
      launchOwner = await currentLaunchOwner()
    } catch (error) {
      await rollbackBeforeLaunch(error instanceof Error ? error.message : String(error))
      throw error
    }
    const prepared = await runWorkStartChild([concord, "session-prepare"], JSON.stringify({ product_id: bootstrap.product_id, work_id: bootstrap.work_id, task: args.task, ...launchOwner }), context.abort, { cwd: bootstrap.worktree.path })
    if (prepared.exitCode !== 0) {
      await rollbackBeforeLaunch(prepared.stderr.slice(0, MAX_STDERR) || "session preparation failed")
      throw new AdapterFailure("session_prepare_failure", "session_prepare_failed", prepared.stderr.slice(0, MAX_STDERR))
    }
    let launchValue: unknown
    try {
      launchValue = singleJSON(prepared.stdout)
    } catch (error) {
      await rollbackBeforeLaunch("session preparation returned malformed output")
      throw new AdapterFailure("malformed_response", "malformed_launch_contract", String(error))
    }
    if (!validateWorkStartLaunch(launchValue, bootstrap)) {
      await rollbackBeforeLaunch("session preparation returned a malformed launch contract")
      throw new AdapterFailure("malformed_response", "malformed_launch_contract", "session-prepare response failed the strict launch contract")
    }
    const launch = launchValue
    if (!launch.spawn_permitted) {
      if (launch.rollback_permitted) {
        await rollbackBeforeLaunch("the prior host process ended before it recorded a session identity")
        throw new AdapterFailure("cancelled", "stale_launch_rolled_back", "the stale launch had no recoverable session identity")
      }
      throw new AdapterFailure("operation_conflict", "launch_in_progress", "another host invocation owns this launch", "partial", "reconcile_operation")
    }
    const activeBootstrap = bootstrap
    if (!activeBootstrap) throw new AdapterFailure("malformed_response", "malformed_bootstrap_response", "bootstrap state was lost before launch")
    let sessionID = launch.session_id ?? ""
    let sessionIdentityRecorded = false
    const recordLaunch = async (state: "running" | "completed" | "failed", reason: string, model = "", recordedSessionID = sessionID) => {
      const record = await runWorkStartChild([concord, "session-record"], JSON.stringify({
        operation_id: launch.operation_id,
        attempt_id: launch.attempt_id,
        product_id: activeBootstrap.product_id,
        work_id: activeBootstrap.work_id,
        agent: launch.agent,
        directory: activeBootstrap.worktree.path,
        session_id: recordedSessionID,
        model,
        state,
        failure_reason: reason,
        ...launchOwner,
      }), context.abort, { cwd: activeBootstrap.worktree.path })
      if (record.exitCode !== 0) throw new AdapterFailure("session_record_failure", "session_record_failed", record.stderr.slice(0, MAX_STDERR), "partial", "reconcile_operation")
    }
    if (context.abort.aborted) {
      await rollbackBeforeLaunch("work_start was cancelled before the child launch")
      throw new AdapterFailure("cancelled", "cancelled_after_prepare", "work_start was cancelled before the child launch")
    }
    try {
      await recordLaunch("running", "")
      sessionIdentityRecorded = sessionID !== ""
    } catch (error) {
      await rollbackBeforeLaunch(error instanceof Error ? error.message : String(error))
      throw error
    }
    const observeRunLine = async (line: string) => {
      let metadata
      try { metadata = readRunLineMetadata(line) } catch (error) { throw new AdapterFailure("malformed_response", "malformed_run_stream", String(error), sessionID ? "partial" : "none", sessionID ? "reconcile_operation" : "contact_operator") }
      if (!metadata) return
      if (sessionID && metadata.session_id !== sessionID) throw new AdapterFailure("malformed_response", "session_identity_mismatch", "OpenCode emitted more than one session identity", "partial", "reconcile_operation")
      if (!sessionID) {
        sessionID = metadata.session_id
        await recordLaunch("running", "", "", metadata.session_id)
        sessionIdentityRecorded = true
      }
    }
    const runArgs = launch.session_id
      ? [opencode, "run", "--session", launch.session_id, "--format", "json", "--dir", bootstrap.worktree.path, launch.prompt]
      : [opencode, "run", "--agent", launch.agent, "--format", "json", "--dir", bootstrap.worktree.path, launch.prompt]
    let run
    try {
      run = await runWorkStartChild(runArgs, "", context.abort, {
        cwd: bootstrap.worktree.path,
        env: { CONCORD_SELECTED_PRODUCT_ID: bootstrap.product_id, CONCORD_SELECTED_WORK_ID: bootstrap.work_id },
        onStdoutLine: observeRunLine,
      })
    } catch (error) {
      const failure = error instanceof AdapterFailure ? error : new AdapterFailure("transport_failure", "run_unknown_effect", String(error), "possible", "reconcile_operation")
      if (failure.effect === "none") throw new AdapterFailure(failure.kind, `${failure.reason}_unknown_effect`, failure.message, "possible", "reconcile_operation")
      throw failure
    }
    let runMetadata: ReturnType<typeof readRunSessionMetadata>
    try {
      runMetadata = readRunSessionMetadata(run.stdout)
    } catch (error) {
      throw new AdapterFailure("malformed_response", "malformed_run_stream", String(error), "possible", "reconcile_operation")
    }
    if (runMetadata && sessionID && runMetadata.session_id !== sessionID) throw new AdapterFailure("malformed_response", "session_identity_mismatch", "OpenCode emitted more than one session identity", "partial", "reconcile_operation")
    if (runMetadata && !sessionID) sessionID = runMetadata.session_id
    if (run.exitCode !== 0) {
      if (runMetadata && launch.session_id && runMetadata.session_id !== launch.session_id) {
        await recordLaunch("failed", "OpenCode resumed a different session", "", launch.session_id)
        throw new AdapterFailure("malformed_response", "session_identity_mismatch", "OpenCode resumed a different session")
      }
      if (sessionID) await recordLaunch("failed", run.stderr.slice(0, MAX_STDERR))
      throw new AdapterFailure("child_spawn_failure", "child_nonzero", run.stderr.slice(0, MAX_STDERR), "partial", sessionID ? "exact_replay" : "reconcile_operation")
    }
    if (!runMetadata) {
      if (sessionID) await recordLaunch("failed", "OpenCode run output did not contain one typed session identity")
      throw new AdapterFailure("malformed_response", "malformed_run_stream", "OpenCode run output did not contain one typed session identity", "partial", sessionID ? "exact_replay" : "reconcile_operation")
    }
    if (launch.session_id && runMetadata.session_id !== launch.session_id) {
      await recordLaunch("failed", "OpenCode resumed a different session", "", launch.session_id)
      throw new AdapterFailure("malformed_response", "session_identity_mismatch", "OpenCode resumed a different session")
    }
    if (!sessionIdentityRecorded) await recordLaunch("running", "")
    const exported = await runWorkStartChild([opencode, "export", runMetadata.session_id, "--sanitize"], "", context.abort, { cwd: bootstrap.worktree.path })
    if (exported.exitCode !== 0) {
      await recordLaunch("failed", exported.stderr.slice(0, MAX_STDERR))
      throw new AdapterFailure("session_export_failure", "export_failed", exported.stderr.slice(0, MAX_STDERR))
    }
    const readback = readExportSessionMetadata(exported.stdout, runMetadata.session_id)
    if (!readback) {
      await recordLaunch("failed", "OpenCode export did not contain one typed session readback")
      throw new AdapterFailure("malformed_response", "malformed_session_export", "OpenCode export did not contain one typed session readback")
    }
    if (readback.readback_agent !== launch.agent) {
      const reason = `executing agent ${JSON.stringify(readback.readback_agent)} does not match launch agent ${JSON.stringify(launch.agent)}`
      await recordLaunch("failed", reason)
      throw new AdapterFailure("agent_identity_mismatch", "executing_agent_mismatch", reason)
    }
    await recordLaunch("completed", "", readback.readback_model ?? "unknown/unknown")
    const output = boundedUTF8(readRunTextParts(run.stdout).join(""), MAX_WORK_START_OUTPUT_BYTES)
    return {
      schema_version: "1.0",
      outcome: "ok",
      product_id: bootstrap.product_id,
      project_id: bootstrap.project_id,
      work_id: bootstrap.work_id,
      worktree_path: bootstrap.worktree.path,
      agent: launch.agent,
      readback_agent: readback.readback_agent,
      readback_model: readback.readback_model,
      session_id: readback.session_id,
      output,
    }
  } catch (error) {
    if (rolledBack && bootstrap) {
      const failure = error instanceof AdapterFailure ? error : new AdapterFailure("transport_failure", "work_start_failed", String(error))
      return workStartError(failure.kind, `${failure.message}; bootstrap state was rolled back, so use a new idempotency_key`, "partial", { product_id: bootstrap.product_id, project_id: bootstrap.project_id, work_id: bootstrap.work_id, worktree_path: bootstrap.worktree.path }, "contact_operator", false)
    }
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
  if (Buffer.byteLength(output) > maxEnvelopeBytes) output = JSON.stringify(workStartError("output_exceeded", `work_start result exceeds ${maxEnvelopeBytes} bytes`, envelope.outcome === "partial" ? "partial" : "none", { product_id: envelope.product_id, project_id: envelope.project_id, work_id: envelope.work_id, worktree_path: envelope.worktree_path }))
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
async function executeWorkTransition(args: HostToolArgs, context: ToolContext): Promise<HostConcordEnvelope> {
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
