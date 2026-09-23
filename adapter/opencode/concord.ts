import { createHash } from "node:crypto"
import { clientRef, type CredentialStore } from "./credentials"
import { contractOperations, hostToolDescriptions, hostToolSchemas, maxEnvelopeBytes, payloadSchemas } from "./generated-contracts"
import { activeManifestDigest, adoptManifestDigest, resolveDiskManifestDigest } from "./manifest-pin"
import { validateGeneratedEnvelope, validateGeneratedPayload, envelopeFailurePath, payloadFailurePath } from "./generated-contract-tests"
import { dispatchLaneWorker, type LaneDispatchInput } from "./lane_dispatch"
import { abandonWorkerAttempt } from "./dispatch"
import { dispatchWindows } from "./dispatch-window"
import { agentLanes } from "./generated-agent-lanes"
import { hostControlPlane, MoveSessionUnavailable } from "./move-session"
import { createRunSessionObservation, errorEnvelopeForLane, MAX_OUTPUT_BYTES, observeRunSessionLine, readExportSessionMetadata, readRunSessionMetadata, readRunTextParts, runStreamRefusalMessage, runStreamRefusalRecovery, validateAgainstSchema, type AgentResultEnvelope, type RunLineMetadata, type RunSessionObservation } from "./dispatch"
import { concordBinaryPath, CoreBinaryUnavailable } from "./dispatch"
import { createWorkStateReporter, formatWorkPaneName } from "./workflow-status"
import { hostLeaseFault } from "./host-lease"
import { armTurnMoveBoundary } from "./turn-move-boundary"
import { armClaimedWorktree, clearClaimedWorktree, recordUnlandedClaimedWorktree } from "./claimed-worktree"
import { ensureConductLink } from "./project-link"

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
type HostToolCall = { request: HostToolArgs | { request: HostToolArgs } }

// opencode 1.18.30's Code Mode bridge delivers the published arguments to
// plugin tool execute wrapped one extra time under the schema's own
// `request` property: {request: {request: {operation, input}}}. The flat
// work_start schema is unaffected, and no legitimate HostToolArgs carries a
// `request` property, so that key is the discriminator. Both host shapes
// normalize here, at the single boundary between host delivery and the
// shared transport.
//
// The bridge can also deliver that wrapper without the published top-level
// `operation`, which leaves a caller's copy inside `input` as the only
// surviving name. No tool input payload declares an `operation` property, so
// the key names the operation and never the payload: it is recovered when the
// top-level name is absent, and always removed before the payload reaches the
// core, which keeps sole authority over whether the named operation is
// admissible.
function hostRequest(args: HostToolCall): HostToolArgs {
  const outer: unknown = args["request"]
  let delivered = outer
  if (outer !== null && typeof outer === "object" && "request" in outer) {
    const inner: unknown = (outer as { request: unknown }).request
    if (inner !== null && typeof inner === "object") delivered = inner
  }
  return withOperationNamed(delivered as HostToolArgs)
}

function withOperationNamed(delivered: HostToolArgs): HostToolArgs {
  const input: unknown = delivered?.input
  if (input === null || typeof input !== "object" || !("operation" in input)) return delivered
  const { operation: named, ...payload } = input as Record<string, unknown>
  const operation = typeof delivered.operation === "string" ? delivered.operation : named
  return { ...delivered, ...(typeof operation === "string" ? { operation } : {}), input: payload }
}
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
let credentialsOverride: CredentialStore | null = null

export function configureConcordAdapter(overrides: { runner?: ChildRunner; credentials?: CredentialStore; reset?: boolean } = {}) {
  if (overrides.reset) { runner = defaultRunner; credentialsOverride = null }
  if (overrides.runner) runner = overrides.runner
  if (overrides.credentials) credentialsOverride = overrides.credentials
}

function schemaName(ref: string): string {
  const prefix = ref.startsWith("#/$defs/") ? "#/$defs/" : ref.startsWith("#/schemas/") ? "#/schemas/" : ""
  const name = prefix ? ref.slice(prefix.length) : ""
  if (!name || !Object.hasOwn(payloadSchemas, name)) throw new Error(`unknown payload schema reference ${ref}`)
  return name
}

const hostSchemaStructuralKeys = new Set(["$defs", "$ref", "additionalProperties", "allOf", "anyOf", "definitions", "else", "if", "not", "oneOf", "properties", "required", "then"])

function sameSchema(left: unknown, right: unknown): boolean {
  return JSON.stringify(left) === JSON.stringify(right)
}

function mergeHostSchemas(schemas: JSONSchema[]): JSONSchema {
  if (schemas.length === 0) return {}
  if (schemas.every((schema) => sameSchema(schema, schemas[0]))) return schemas[0]

  const objectLike = schemas.some((schema) => schema.type === "object" || schema.properties !== undefined)
  if (objectLike) {
    const properties: Record<string, unknown> = {}
    for (const schema of schemas) {
      for (const [name, property] of Object.entries((schema.properties ?? {}) as Record<string, unknown>)) {
        const previous = properties[name]
        properties[name] = previous === undefined
          ? property
          : mergeHostSchemas([previous as JSONSchema, property as JSONSchema])
      }
    }
    return { type: "object", properties, required: [], additionalProperties: true }
  }

  const result: JSONSchema = {}
  for (const [key, value] of Object.entries(schemas[0])) {
    if (hostSchemaStructuralKeys.has(key)) continue
    if (schemas.every((schema) => sameSchema(schema[key], value))) result[key] = value
  }
  const inferredTypes = schemas.map((schema) => {
    if (typeof schema.type === "string") return schema.type
    if (schema.type !== undefined) return JSON.stringify(schema.type)
    if (schema.const !== undefined) return typeof schema.const
    if (Array.isArray(schema.enum) && schema.enum.length > 0) return typeof schema.enum[0]
    return ""
  }).filter(Boolean)
  if (inferredTypes.length > 0 && inferredTypes.every((type) => type === inferredTypes[0])) result.type = inferredTypes[0]
  return result
}

function flattenHostSchema(value: unknown, resolving = new Set<string>()): JSONSchema {
  if (Array.isArray(value) || typeof value !== "object" || value === null) return {}
  const schema = value as JSONSchema
  if (typeof schema.$ref === "string") {
    const name = schemaName(schema.$ref)
    if (resolving.has(name)) return {}
    const next = new Set(resolving)
    next.add(name)
    return flattenHostSchema((payloadSchemas as Record<string, unknown>)[name], next)
  }

  const combinations = ["oneOf", "anyOf", "allOf", "then", "else"]
    .flatMap((key) => Array.isArray(schema[key]) ? schema[key] as unknown[] : schema[key] === undefined ? [] : [schema[key]])
  if (combinations.length > 0) {
    const base = Object.fromEntries(Object.entries(schema).filter(([key]) => !hostSchemaStructuralKeys.has(key)))
    const branches = [base, ...combinations].map((branch) => flattenHostSchema(branch, resolving))
    return mergeHostSchemas(branches)
  }

  const result: JSONSchema = {}
  for (const [key, child] of Object.entries(schema)) {
    if (hostSchemaStructuralKeys.has(key)) continue
    if (key === "items") {
      result[key] = Array.isArray(child) ? child.map((item) => flattenHostSchema(item, resolving)) : flattenHostSchema(child, resolving)
    } else {
      result[key] = child
    }
  }
  if (schema.properties !== undefined || schema.type === "object") {
    result.type = "object"
    result.properties = Object.fromEntries(Object.entries((schema.properties ?? {}) as Record<string, unknown>).map(([name, property]) => [name, flattenHostSchema(property, resolving)]))
    result.required = []
    result.additionalProperties = true
  }
  return result
}

export function publishedRequestSchema(toolName: string): JSONSchema {
  const operations = contractOperations.filter((operation: any) => operation.tool === toolName)
  if (operations.length === 0) throw new Error(`tool ${toolName} has no generated operations`)
  // The host receives one permissive request shape. ValidateOperationPayload
  // remains the closed operation boundary because it runs after host delivery.
  const publicInputSchema = (operation: any): string => operation.id === "concord_work_transition.workflow_action"
    ? "work_transition_action_public_input"
    : schemaName(operation.input_schema)
  const input = mergeHostSchemas(operations.map((operation: any) => flattenHostSchema((payloadSchemas as Record<string, unknown>)[publicInputSchema(operation)])))
  return {
    type: "object",
    additionalProperties: false,
    required: ["operation", "input"],
    properties: {
      operation: { type: "string", enum: operations.map((operation: any) => operation.id.slice(operation.id.indexOf(".") + 1)) },
      input,
    },
  }
}

function argsSchema(toolName: string): any {
  return { request: publishedRequestSchema(toolName) }
}

// The host definition hook publishes the flattened view with optional fields.
// The generated manifest and validateWorkStartArgs enforce the closed modes.
function workStartArgsSchema() {
  const properties = Object.assign({}, ...hostToolSchemas.concord_work_start.oneOf.map((branch) => branch.properties))
  // Host hooks can mutate published schemas, but never the runtime contract.
  return JSON.parse(JSON.stringify(properties))
}

export async function publishWorkStartDefinition(
  input: { toolID: string },
  output: { description: string; parameters: unknown; jsonSchema?: unknown },
): Promise<void> {
  if (input.toolID !== "concord_work_start") return
  // The host registry consumes jsonSchema independently of its runtime decoder.
  // Keep parameters unchanged so publication does not alter execution admission.
  output.jsonSchema = { type: "object", properties: workStartArgsSchema(), required: [], additionalProperties: false }
}

function baseEnvelope(toolName: string, operation: string, requestID: string) {
  const queryID = (contractOperations.find((candidate: any) => candidate.tool === toolName && candidate.id.endsWith(`.${operation}`)) as any)?.query_id
  return { schema_version: "1.0", manifest_digest: activeManifestDigest(), request_id: requestID, origin: "adapter", tool: toolName, operation, ...(queryID ? { query_id: queryID } : {}), outcome: "error", resolved_scope: null, authority: "unreachable", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false }
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
  if (error instanceof CoreBinaryUnavailable) return new AdapterFailure("transport_failure", "missing_binary", error.message)
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
  if (!response || typeof response !== "object" || response.schema_version !== "1.0" || response.manifest_digest !== activeManifestDigest() || response.origin !== "core" || response.tool !== toolName || response.operation !== operation || !["ok", "pending", "partial", "error"].includes(response.outcome)) return "the envelope identity"
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
  return !!response && typeof response === "object" && response.schema_version === "1.0" && response.origin === "core" && typeof response.manifest_digest === "string" && response.manifest_digest !== activeManifestDigest()
}

function operationIsMutation(toolName: string, operation: string): boolean {
  return contractOperations.some((item: any) => item.tool === toolName && item.id.endsWith(`.${operation}`) && item.kind === "mutation")
}


function selectedProductID() {
  const value = process.env.CONCORD_SELECTED_PRODUCT_ID ?? ""
  return /^[A-Za-z0-9][A-Za-z0-9._:-]{1,127}$/.test(value) ? value : ""
}

type AmbientContext = { projectID: string; productIDs: string[]; scopeVersion: string; mainWorktree: boolean }

async function resolveAmbientContext(context: ToolContext, sessionDirectory: string): Promise<AmbientContext> {
  let result
  try {
    result = await runner.run([concordBinaryPath(), "project-resolve"], JSON.stringify({ directory: sessionDirectory, worktree: sessionDirectory }), context.abort)
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

async function resolveSessionDirectory(context: ToolContext): Promise<string> {
  try {
    return await hostControlPlane().sessionDirectory(context.sessionID, context.abort)
  } catch (error) {
    throw new AdapterFailure("transport_failure", "session_directory_unreadable", error instanceof Error ? error.message : String(error), "none", "retry_same_request")
  }
}

// invokeConcordOperation is the single `concord project-resolve` + `concord invoke`
// transport for every adapter surface, including host-side callers outside the
// tool exports below. It owns envelope construction, the closed core-response
// contract check, and the approval_required resubmission. Native worker
// dispatch can supply the canonical session directory that its window pins.
async function invokeConcordOperationRaw(toolName: string, args: HostToolArgs, context: ToolContext, sessionDirectoryOverride?: string): Promise<CoreConcordEnvelope> {
  const operation = args.operation
  const requestID = `${context.sessionID}-${context.messageID}`
  // CD-0111 D2: a session that could not claim its host lease runs closed.
  // The installer would treat it as absent and could remove the release it
  // runs, so no core call leaves before the lease exists.
  const leaseFault = hostLeaseFault()
  if (leaseFault) return adapterError(toolName, operation, requestID, "transport_failure", "host_lease_missing", `${leaseFault}; no operation ran`, "none", "contact_operator")
  let ambient: AmbientContext
  let sessionDirectory: string
  try { sessionDirectory = sessionDirectoryOverride ?? await resolveSessionDirectory(context) } catch (error) { return failureEnvelope(toolName, operation, requestID, error, "context_resolution_failed") }
  try { ambient = await resolveAmbientContext(context, sessionDirectory) } catch (error) { return failureEnvelope(toolName, operation, requestID, error, "context_resolution_failed") }
  const selectedProduct = selectedProductID() || (ambient.productIDs.length === 1 ? ambient.productIDs[0] : "")
  const envelope: any = { schema_version: "1.0", request_id: requestID, client_ref: clientRef(), principal_ref: "", session_ref: context.sessionID, agent_ref: context.agent, directory: sessionDirectory, worktree: sessionDirectory, ambient_project_id: ambient.projectID, selected_product_id: selectedProduct, scope_version: ambient.scopeVersion, manifest_digest: activeManifestDigest() }
  const run = async (input: any) => runner.run([concordBinaryPath(), "invoke"], JSON.stringify({ call_envelope: envelope, tool: toolName, operation, input }), context.abort)
  let result: any
  try { result = await run(args.input) } catch (error) { return failureEnvelope(toolName, operation, requestID, runnerFailure(error, context.abort.aborted), "spawn_failure") }
  if (result.exitCode !== 0 && !result.stdout.trim()) return adapterError(toolName, operation, requestID, "operation_conflict", "unknown_effect", result.stderr.slice(0, MAX_STDERR), "possible", "reconcile_operation")
  let response: any
  try { response = singleJSON(result.stdout) } catch (error) { return adapterError(toolName, operation, requestID, "malformed_response", "malformed_core_response", String(error), "possible", "reconcile_operation", salvageDetails(result.stdout)) }
  const skewRefusal = () => {
    const disk = resolveDiskManifestDigest()
    const diskDetail = disk === null ? "the digest on disk could not be read" : `the adapter files on disk stamp ${disk}`
    // CD-0111 D4: no refusal names a session restart. The session's core and
    // contract pin to one release (D1), so a digest the retry could not heal
    // is a defect; the operator gets both digests.
    const skewDetail = `core contract digest ${response.manifest_digest} does not match this adapter's ${activeManifestDigest()}; ${diskDetail}; under the pinned release pair this mismatch is a defect, so contact the operator with both digests`
    if (!operationIsMutation(toolName, operation)) return adapterError(toolName, operation, requestID, "transport_failure", "manifest_mismatch", skewDetail, "none", "contact_operator")
    return adapterError(toolName, operation, requestID, "operation_conflict", "unknown_effect", `${skewDetail}, then reconcile this operation`, "possible", "reconcile_operation")
  }
  const contractFailure = coreResponseFailure(response, toolName, operation)
  if (contractFailure && isVersionSkew(response)) {
    // Version skew self-heal (issue #885). A release that lands mid-session
    // leaves this process pinning the previous contract digest, and a session
    // resumed by id restores that pin even across a process restart. When the
    // files on disk stamp exactly the digest the core answered with, only the
    // pin is stale: adopt the disk digest and retry the same request once.
    // Reads retry freely; a mutation's replay is absorbed by the core's
    // idempotency, so the retry cannot double-apply.
    const disk = resolveDiskManifestDigest()
    if (disk !== null && disk === response.manifest_digest && adoptManifestDigest(disk)) {
      envelope.manifest_digest = activeManifestDigest()
      let retryResult: any
      try { retryResult = await run(args.input) } catch (error) { return failureEnvelope(toolName, operation, requestID, runnerFailure(error, context.abort.aborted), "spawn_failure") }
      if (retryResult.exitCode !== 0 && !retryResult.stdout.trim()) return adapterError(toolName, operation, requestID, "operation_conflict", "unknown_effect", retryResult.stderr.slice(0, MAX_STDERR), "possible", "reconcile_operation")
      let retryResponse: any
      try { retryResponse = singleJSON(retryResult.stdout) } catch (error) { return adapterError(toolName, operation, requestID, "malformed_response", "malformed_core_response", String(error), "possible", "reconcile_operation", salvageDetails(retryResult.stdout)) }
      if (!coreResponseFailure(retryResponse, toolName, operation)) {
        response = retryResponse
      } else {
        response = retryResponse
        return skewRefusal()
      }
    } else {
      return skewRefusal()
    }
  } else if (contractFailure) {
    return adapterError(toolName, operation, requestID, operationIsMutation(toolName, operation) ? "operation_conflict" : "malformed_response", operationIsMutation(toolName, operation) ? "unknown_effect" : "malformed_core_response", `core response failed the generated TS7 contract: ${contractFailure}`, "possible", "reconcile_operation", salvageDetails(result.stdout))
  }
  if (response?.outcome === "error" && response?.error?.kind === "approval_required") {
    const details = response.error.details ?? {}
    // allOf[16] and allOf[17] of the envelope schema pair `consequence_summary`
    // with `details.approval_ref`; that pair is the schema's only declaration
    // that a challenge was minted. allOf[5] permits an approval_required
    // refusal with no challenge, and one arrives here unchanged instead of
    // failing challenge validation.
    if (details.approval_ref === undefined && response.error.consequence_summary === undefined) return response as CoreConcordEnvelope
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
    envelope.host_approval_assertion = { challenge_ref: details.approval_ref, request_digest: details.operation_digest, scope: details.scope, versions: details.versions, session_ref: envelope.session_ref, agent_ref: envelope.agent_ref, worktree: sessionDirectory, issued_at: new Date().toISOString() }
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

export async function invokeConcordOperation(toolName: string, args: HostToolArgs, context: ToolContext, sessionDirectoryOverride?: string): Promise<CoreConcordEnvelope> {
  return reconcileUnknownEffect(toolName, args, context, await invokeConcordOperationRaw(toolName, args, context, sessionDirectoryOverride))
}

const workStateReporter = createWorkStateReporter(
  { runner: { run: (argv, input, signal) => runner.run(argv, input, signal) } },
)

// The text-part queue's public face. The plugin's experimental.text.complete
// hook drains it into the assistant's own message; the tests seed it the same
// way the gate brief and the worktree-removal notice enqueue.
export function enqueueWorkNotice(sessionID: string, block: string): void {
  workStateReporter.enqueueNotice(sessionID, block)
}

export function takeWorkNotices(sessionID: string): string[] {
  return workStateReporter.takeNotices(sessionID)
}

// appendWarnings puts a failed best-effort side effect where an actor can read
// it. `ToolResult` is a union with a bare-string arm, so both arms are handled
// here rather than assumed away: the envelope stays the first line, and each
// warning follows on its own.
function appendWarnings(result: ToolResult, warnings: string[]): ToolResult {
  if (warnings.length === 0) return result
  const suffix = `\n${warnings.join("\n")}`
  if (typeof result === "string") return `${result}${suffix}`
  return { ...result, output: `${result.output}${suffix}` }
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
  const envelope = await invokeConcordOperation(toolName, args, context)
  const warnings = operationIsMutation(toolName, args.operation) ? await workStateReporter.report(envelope, context) : []
  return appendWarnings(await encodeHostToolResult(toolName, args, context, envelope), warnings)
}

async function executeHostTransition(args: HostToolArgs, context: ToolContext): Promise<ToolResult> {
  const envelope = await executeWorkTransition(args, context)
  const warnings = await workStateReporter.report(envelope, context)
  return appendWarnings(await encodeHostToolResult("concord_work_transition", args, context, envelope), warnings)
}

type WorkStartCaptureArgs = {
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

// WorkStartArgs is one of two shapes (issue #891): the capture shape above, or
// the resume shape naming an existing work item by work identity. Resume
// records nothing, so it carries no idempotency_key and no capture fields.
type WorkStartArgs = WorkStartCaptureArgs | { work_id: string }

type WorkStartResume = {
  schema_version: "1.0"
  product_id: string
  project_id: string
  work_id: string
  worktree: { set_id: string; path: string; branch: string; base_sha: string; state: "active" }
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

type WorkStartPrepared = { schema_version: "1.0"; agent: string; directory: string; product_id: string; work_id: string; title: string; prompt: string }

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

const [workStartCaptureBranch, workStartResumeBranch] = hostToolSchemas.concord_work_start.oneOf
const workStartUsage = `Capture requires ${workStartCaptureBranch.required.join(", ")}. Resume requires only ${workStartResumeBranch.required.join(", ")}. Do not combine capture and resume fields.`

function isWorkStartResumeArgs(value: Record<string, unknown>): value is { work_id: string } {
  return saneWorkID(value.work_id)
}

// The published per-field view cannot enforce the closed capture/resume modes.
// Select one generated branch so its diagnostics name the applicable fields.
function validateWorkStartArgs(value: unknown, failures: string[]): value is WorkStartArgs {
  if (!record(value)) {
    failures.push("arguments: is not of type object")
    return false
  }
  const branch = "work_id" in value ? workStartResumeBranch : workStartCaptureBranch
  return validateAgainstSchema(branch, value, failures)
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

function validateWorkStartPrepared(value: unknown, bootstrap: { product_id: string; work_id: string; worktree: { path: string } }, agent: string): value is WorkStartPrepared {
  if (!record(value) || !exactKeys(value, ["schema_version", "agent", "directory", "product_id", "work_id", "title", "prompt"])) return false
  return value.schema_version === "1.0"
    && value.directory === bootstrap.worktree.path
    && value.product_id === bootstrap.product_id
    && value.work_id === bootstrap.work_id
    && value.agent === agent
    && nonEmptyString(value.agent) && /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(value.agent)
    && nonEmptyString(value.title) && Buffer.byteLength(value.title) <= 256
    && typeof value.prompt === "string" && value.prompt.length > 0 && Buffer.byteLength(value.prompt) <= 65_536
}

// validateWorkStartResume is the strict read-back contract for the work-resume
// child. It mirrors validateWorkStartBootstrap minus the capture-only fields
// (operation id, replay flag, work version): the active-entry read records
// nothing, while missing-entry bootstrap keeps those fields outside this
// shared resume response.
function validateWorkStartResume(value: unknown): value is WorkStartResume {
  if (!record(value) || !exactKeys(value, ["schema_version", "product_id", "project_id", "work_id", "worktree"])) return false
  if (value.schema_version !== "1.0" || !nonEmptyString(value.product_id) || !nonEmptyString(value.project_id) || !nonEmptyString(value.work_id) || !record(value.worktree)) return false
  const worktree = value.worktree
  return exactKeys(worktree, ["set_id", "path", "branch", "base_sha", "state"])
    && nonEmptyString(worktree.set_id)
    && typeof worktree.path === "string" && worktree.path.startsWith("/")
    && nonEmptyString(worktree.branch) && /^[0-9a-f]{40}$/.test(String(worktree.base_sha)) && worktree.state === "active"
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
// when a capture bootstrap or a resume read ran, so the caller can see the
// work item and worktree a replay will adopt. effect_state is none because
// nothing here is an effect a replay cannot reproduce or reuse; the durable
// state is the work item and the claim, both keyed on the request digest.
function workStartFailure(error: unknown, target: { product_id: string; project_id: string; work_id: string; worktree: { path: string } } | null, fallbackKind: string): WorkStartEnvelope {
  const failure = error instanceof AdapterFailure ? error : new AdapterFailure("transport_failure", fallbackKind, String(error))
  const identity = target ? { product_id: target.product_id, project_id: target.project_id, work_id: target.work_id, worktree_path: target.worktree.path } : {}
  const retrySafe = failure.recovery === "retry_same_request"
  return workStartError(failure.kind, failure.message, identity, failure.recovery, retrySafe)
}

async function runWorkStartChild(argv: string[], input: string, signal: AbortSignal, options?: ChildRunnerOptions) {
  try { return await runner.run(argv, input, signal, options) } catch (error) { throw runnerFailure(error, signal.aborted) }
}

// The core reports a deterministic session-prepare refusal — invalid input,
// or a state or identity check that fails the same way until state changes —
// with this typed exit status, declared in the core's session-prepare help.
// Classification uses the status alone, never stderr text: replaying the same
// request cannot clear a refusal, so it maps to contact_operator, while any
// other session-prepare failure stays retryable.
export const sessionPrepareRefusalExit = 2

// renameZellijPaneFrame names the zellij pane frame after the work a
// successful work_start just entered (issue #917). The session-prepare
// contract returns the title alone, and the adapter does not add a database
// read for a cosmetic name, so it renders the shared pane formatter with the
// title alone; the first mutation replaces it with the full work state. One
// bounded fork per success; the pane belongs to the host, so no
// ZELLIJ_PANE_ID, an empty name, or a failed fork returns a warning message
// that never changes the completed start.
async function renameZellijPaneFrame(title: string, context: ToolContext): Promise<string | null> {
  const paneID = process.env.ZELLIJ_PANE_ID
  if (paneID === undefined || paneID === "") return null
  const name = formatWorkPaneName({ title })
  if (name === null) return null
  try {
    const result = await runner.run(["zellij", "action", "rename-pane", "-p", paneID, name], "", context.abort)
    if (result.exitCode !== 0) return `Concord could not rename the pane frame to the work title: exit ${result.exitCode}.`
  } catch {
    return "Concord could not rename the pane frame to the work title: the fork failed."
  }
  return null
}

// writeSessionGoalTitle names the host session "Goal: <title>" with the title
// session-prepare derived, so the session list states the objective and the
// plugin's compaction hook can restate it. The write is best effort: an
// absent route, an empty title, or a failed call returns a warning message
// that never changes the completed start.
async function writeSessionGoalTitle(sessionID: string, title: string, context: ToolContext): Promise<string | null> {
  if (await hostControlPlane().setSessionTitle(sessionID, `Goal: ${title}`, context.abort)) return null
  return "Concord could not write the session goal title: the session title route is absent or refused the write."
}

// executeWorkStart replays to convergence. Each step is idempotent on the
// request's derived identity, so a replay under the same idempotency_key
// adopts whatever an earlier attempt left and runs only what is missing:
//
  //   1. work-bootstrap derives new work, while work-resume reuses an active
  //      entry or durably bootstraps a missing entry under the existing work
  //      identity (issue #891).
//   2. session-prepare verifies that worktree and derives the boot packet. It
//      records nothing.
//   3. moveSession moves the calling session, and is a no-op when the session
//      already runs there.
//   4. The host reports the directory the session runs in, and the host tool
//      context this call runs in must resolve there too. Success is refused
//      unless both name the claimed worktree (issue #1322): a metadata-only
//      move — the host accepted the retarget but the tool context has not
//      landed — refuses and leaves the claimed worktree unarmed, so a replay
//      after the context lands may succeed.
//
// No step records intent ahead of its effect, so there is no partial state.
// The session's worktree is the directory it runs in, and the host owns that
// answer (CD-0098 D3).
async function executeWorkStart(args: WorkStartArgs, context: ToolContext, warnings: string[]): Promise<WorkStartEnvelope> {
  let target: { product_id: string; project_id: string; work_id: string; worktree: { path: string } } | null = null
  const resume = record(args) && isWorkStartResumeArgs(args)
  try {
    // Input refusals have no effect; correction belongs to the caller.
    const failures: string[] = []
    if (!validateWorkStartArgs(args, failures)) throw new AdapterFailure("invalid_input", "invalid_work_start_input", `work_start arguments failed the host-tool contract: ${failures.join("; ")}. ${workStartUsage} Submit a corrected request; resubmitting unchanged arguments will fail again.`, "none", "correct_request")
    if (context.abort.aborted) throw new AdapterFailure("cancelled", "cancelled_no_effect", `work_start was cancelled before ${resume ? "the resume read" : "bootstrap"}`)
    const ambient = await resolveAmbientContext(context, context.directory)
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
    try {
      await hostControlPlane().manageSession(context.sessionID, context.abort)
    } catch (error) {
      throw new AdapterFailure("unreachable", "managed_scope_unavailable", error instanceof Error ? error.message : String(error), "none", "contact_operator")
    }
    let prepareTask: string
    if (resume) {
      const workID = (args as { work_id: string }).work_id
      const resumed = await runWorkStartChild([concordBinaryPath(), "work-resume"], JSON.stringify({ product_id: productID, project_id: ambient.projectID, work_id: workID, session_ref: context.sessionID }), context.abort, { cwd: context.directory })
      if (resumed.exitCode !== 0) throw new AdapterFailure("resume_failure", "resume_refused", resumed.stderr.slice(0, MAX_STDERR), "none", "retry_same_request")
      let resumedValue: unknown
      try { resumedValue = singleJSON(resumed.stdout) } catch (error) { throw new AdapterFailure("malformed_response", "malformed_resume_response", String(error), "none", "retry_same_request") }
      if (!validateWorkStartResume(resumedValue) || resumedValue.product_id !== productID || resumedValue.project_id !== ambient.projectID || resumedValue.work_id !== workID) throw new AdapterFailure("malformed_response", "malformed_resume_response", "work-resume response failed the strict resume contract", "none", "retry_same_request")
      target = resumedValue
      prepareTask = ""
    } else {
      const capture = args as WorkStartCaptureArgs
      const bootstrapInput = { product_id: productID, project_id: ambient.projectID, ...capture, session_ref: context.sessionID }
      const boot = await runWorkStartChild([concordBinaryPath(), "work-bootstrap"], JSON.stringify(bootstrapInput), context.abort, { cwd: context.directory })
      if (boot.exitCode !== 0) throw new AdapterFailure("bootstrap_failure", "bootstrap_failed", boot.stderr.slice(0, MAX_STDERR), "none", "retry_same_request")
      let bootValue: unknown
      try { bootValue = singleJSON(boot.stdout) } catch (error) { throw new AdapterFailure("malformed_response", "malformed_bootstrap_response", String(error), "none", "retry_same_request") }
      if (!validateWorkStartBootstrap(bootValue) || bootValue.product_id !== productID || bootValue.project_id !== ambient.projectID) throw new AdapterFailure("malformed_response", "malformed_bootstrap_response", "work-bootstrap response failed the strict bootstrap contract", "none", "retry_same_request")
      target = bootValue
      prepareTask = capture.task
    }

    if (context.abort.aborted) throw new AdapterFailure("cancelled", "cancelled_after_bootstrap", `work_start was cancelled after ${resume ? "the resume read" : "bootstrap"}; replay the same idempotency_key to resume`, "none", "retry_same_request")
    try {
      await ensureConductLink(target.worktree.path, context.abort)
    } catch (error) {
      throw new AdapterFailure("transport_failure", "conduct_link_failed", error instanceof Error ? error.message : String(error), "none", "contact_operator")
    }
    // session-prepare verifies the ACTIVE host agent: the request carries the
    // agent this session runs as (context.agent), the core resolves that
    // agent's definition and registry entry, and the read-back must name the
    // same agent. A core that answers with any other agent fails the strict
    // contract below.
    const prepared = await runWorkStartChild([concordBinaryPath(), "session-prepare"], JSON.stringify({ product_id: target.product_id, work_id: target.work_id, task: prepareTask, agent: context.agent }), context.abort, { cwd: target.worktree.path })
    if (prepared.exitCode === sessionPrepareRefusalExit) throw new AdapterFailure("session_prepare_failure", "session_prepare_refused", prepared.stderr.slice(0, MAX_STDERR), "none", "contact_operator")
    if (prepared.exitCode !== 0) throw new AdapterFailure("session_prepare_failure", "session_prepare_failed", prepared.stderr.slice(0, MAX_STDERR), "none", "retry_same_request")
    let preparedValue: unknown
    try { preparedValue = singleJSON(prepared.stdout) } catch (error) { throw new AdapterFailure("malformed_response", "malformed_prepare_response", String(error), "none", "retry_same_request") }
    if (!validateWorkStartPrepared(preparedValue, target, context.agent)) throw new AdapterFailure("malformed_response", "malformed_prepare_response", "session-prepare response failed the strict prepare contract", "none", "retry_same_request")
    const agent = preparedValue.agent

    if (context.abort.aborted) throw new AdapterFailure("cancelled", "cancelled_before_move", "work_start was cancelled before the move; replay the same idempotency_key to resume", "none", "retry_same_request")
    // CD-0098 D2. The move is the only route to the worktree. An absent route
    // refuses here; what the capture recorded (or the resume read) stays
    // adoptable rather than being rolled back for a host-capability gap the
    // operator can repair.
    try {
      await hostControlPlane().moveSession(context.sessionID, target.worktree.path, context.abort)
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      if (error instanceof MoveSessionUnavailable) {
        throw new AdapterFailure("unreachable", "move_session_route_unavailable", message, "none", "contact_operator")
      }
      throw new AdapterFailure("transport_failure", "move_session_refused", message, "none", "retry_same_request")
    }
    // CD-0098 D3. The destination is read back from the host, not assumed from
    // the request that asked for it, and success is refused unless the session
    // now runs in the claimed worktree. Every refusal past the accepted move
    // records the pending target first: the move was accepted but never proved
    // to have landed, so the dispatch gate stays closed for this session until
    // a confirmed landing, a vacate, or the durable gate takes over.
    let landed: string
    try {
      landed = await hostControlPlane().sessionDirectory(context.sessionID, context.abort)
    } catch (error) {
      recordUnlandedClaimedWorktree(context.sessionID, target.worktree.path)
      throw new AdapterFailure("malformed_response", "session_directory_unreadable", error instanceof Error ? error.message : String(error), "none", "retry_same_request")
    }
    if (!samePath(landed, target.worktree.path)) {
      recordUnlandedClaimedWorktree(context.sessionID, target.worktree.path)
      throw new AdapterFailure("session_directory_mismatch", "move_destination_mismatch", `the session moved to ${JSON.stringify(landed)} rather than the claimed worktree ${JSON.stringify(target.worktree.path)}`, "none", "retry_same_request")
    }
    // Issue #1322: a host that accepted the retarget can keep running this
    // session's tools in the pre-move directory, so the confirmed read-back
    // alone is a metadata-only move. Success waits until the host tool context
    // this call runs in resolves inside the claimed worktree; until then the
    // declared refusal leaves the claimed worktree unarmed and a replay after
    // the context lands may succeed. The unlanded record keeps the dispatch
    // gate closed for this session while that move has not landed.
    if (!samePath(context.directory, target.worktree.path)) {
      recordUnlandedClaimedWorktree(context.sessionID, target.worktree.path)
      throw new AdapterFailure("session_directory_mismatch", "move_context_not_landed", `the host reports the session in the claimed worktree ${JSON.stringify(target.worktree.path)}, but this session's tool context still resolves in ${JSON.stringify(context.directory)}; the move has not landed, so Concord reports no success and arms no claimed worktree. Replay work_start once the session's tool context runs in the claimed worktree.`, "none", "retry_same_request")
    }
    // The tool context landed in the claimed worktree, so this session's
    // active claimed worktree is armed for the dispatch check.
    armClaimedWorktree(context.sessionID, target.worktree.path)
    // Issue #917: the pane frame now names the work this session runs. The
    // rename sits after every refusal point, so it fires once per success and
    // never changes the outcome the envelope reports. A failure returns a
    // warning the tool result carries to the agent.
    const paneWarning = await renameZellijPaneFrame(preparedValue.title, context)
    if (paneWarning) warnings.push(paneWarning)
    // The session title names the goal the session-prepare contract derived.
    // Like the pane frame it sits after every refusal point and never changes
    // the outcome the envelope reports.
    const titleWarning = await writeSessionGoalTitle(context.sessionID, preparedValue.title, context)
    if (titleWarning) warnings.push(titleWarning)
    return {
      schema_version: "1.0",
      outcome: "ok",
      product_id: target.product_id,
      project_id: target.project_id,
      work_id: target.work_id,
      worktree_path: target.worktree.path,
      agent,
      session_id: context.sessionID,
      output: `This session now runs in ${target.worktree.path} on work item ${target.work_id}.`,
    }
  } catch (error) {
    return workStartFailure(error, target, "work_start_failed")
  }
}

export const product_view = tool({ description: "Concord product view", args: argsSchema("concord_product_view"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_product_view", hostRequest(args), context) })
export const work_browse = tool({ description: "Concord work browse", args: argsSchema("concord_work_browse"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_browse", hostRequest(args), context) })
export const work_trace = tool({ description: "Concord work trace", args: argsSchema("concord_work_trace"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_trace", hostRequest(args), context) })
export const knowledge = tool({ description: "Concord knowledge", args: argsSchema("concord_knowledge"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_knowledge", hostRequest(args), context) })
export const work_define = tool({ description: "Concord work define", args: argsSchema("concord_work_define"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_define", hostRequest(args), context) })
export const domain = tool({ description: "Concord domain", args: argsSchema("concord_domain"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_domain", hostRequest(args), context) })
export const work_initiative = tool({ description: "Concord work initiative", args: argsSchema("concord_work_initiative"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_initiative", hostRequest(args), context) })
export const work_transition = tool({ description: "Concord work transition. Use operation workflow_action for declared workflow actions. Use operation worker_abandon with the attempt and lane identity to close a dispatched attempt with no report. Use action_id dispatch_worker with fields.lane_id for the native worker route. Route discovery does not prove admission at the current workflow step.", args: argsSchema("concord_work_transition"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTransition(hostRequest(args), context) })
export const work_relate = tool({ description: "Concord work relate", args: argsSchema("concord_work_relate"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_relate", hostRequest(args), context) })
export const work_compact = tool({ description: "Concord work compact", args: argsSchema("concord_work_compact"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_compact", hostRequest(args), context) })
export const work_start = tool({ description: `${hostToolDescriptions.concord_work_start} ${workStartUsage}`, args: workStartArgsSchema(), execute: async (args: any, context: ToolContext): Promise<ToolResult> => {
  const warnings: string[] = []
  const envelope = await executeWorkStart(args as WorkStartArgs, context, warnings)
  let output = JSON.stringify(envelope)
  if (Buffer.byteLength(output) > maxEnvelopeBytes) output = JSON.stringify(workStartError("output_exceeded", `work_start result exceeds ${maxEnvelopeBytes} bytes`, { product_id: envelope.product_id, project_id: envelope.project_id, work_id: envelope.work_id, worktree_path: envelope.worktree_path }))
  return appendWarnings({ title: "concord_work_start", output, metadata: {} }, warnings)
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
  const approval = inner.approval
  const approvalRef = approval && typeof approval === "object" && !Array.isArray(approval) && typeof (approval as Record<string, unknown>).approval_ref === "string" ? (approval as Record<string, unknown>).approval_ref as string : undefined
  return { work_id: inner.work_id, expected_version: inner.expected_version, idempotency_key: inner.idempotency_key, lane_id: laneId, ...(approvalRef ? { approval_ref: approvalRef } : {}) }
}

// executeWorkTransition is the work_transition tool body. dispatch_worker is
// routed through dispatchLaneWorker (CD-0067 D5); every other workflow_action
// falls through to the generic core transport. The dispatch path shares the
// same transport seam as every other adapter tool, so a host-side caller
// receives the same envelope shape on either branch.
export const WORKTREE_REMOVAL_OPERATIONS = new Set(
  contractOperations
    .filter((operation) => operation.tool === "concord_work_transition" && operation.input_schema.startsWith("#/schemas/"))
    .filter((operation) => {
      const schemaName = operation.input_schema.slice("#/schemas/".length)
      const schema = (payloadSchemas as Record<string, { properties?: Record<string, unknown> }>)[schemaName]
      return schema?.properties !== undefined && Object.hasOwn(schema.properties, "observed_session_directories")
    })
    .map((operation) => operation.id.slice("concord_work_transition.".length)),
)

// attachLiveSessionObservation supplies the host's live session observation
// to a worktree removal the caller left unobserved. The core's occupancy gate
// releases a dead recorded occupant only on such an observation, so without
// one a coordinator that never gathers sessions strands every crashed
// occupant on the operator. An explicit observation from the caller is
// authoritative and passes through untouched. When the host session list
// cannot be read the call proceeds unobserved and the store keeps its
// refusal: an unreadable host attests nothing.
async function attachLiveSessionObservation(args: HostToolArgs, context: ToolContext): Promise<void> {
  const input = args?.input
  if (!record(input) || input.observed_session_directories !== undefined) return
  try {
    const observed = await hostControlPlane().liveSessionDirectories(context.abort)
    input.observed_session_directories = observed
  } catch {
    return
  }
}

// reportWorktreeRemoval puts the completed removal in front of the operator.
// The agent that made the call may end its turn without relaying anything, and
// the session that was running in a neighbouring worktree has no other way to
// learn the directory is gone. The notice rides the text-part channel: the
// plugin's experimental.text.complete hook appends it to the assistant's own
// message, so delivery needs no host route at all.
async function reportWorktreeRemoval(args: HostToolArgs, context: ToolContext, envelope: HostConcordEnvelope): Promise<void> {
  if (!record(envelope) || envelope.outcome !== "ok") return
  const workID = typeof args.input?.work_id === "string" ? args.input.work_id : "unknown work"
  workStateReporter.enqueueNotice(context.sessionID, `Concord removed the worktree of ${workID}.`)
}

const WORKER_ABANDON_OPERATION = "worker_abandon"

function workerAbandonToken(idempotencyKey: string): string {
  return createHash("sha256").update(`concord_work_transition.${WORKER_ABANDON_OPERATION}\0${idempotencyKey}`).digest("hex")
}

// The worker evidence transport answers with the CLI's operator diagnostic, so
// the store's typed refusal arrives as text. A worker attempt row that does
// not exist is kind projection_not_found with the detail that names the
// missing worker dispatch row, and no other store refusal carries both
// markers, so a refusal with any other cause keeps the existing behaviour.
function workerAbandonFoundNothingDurable(message: string): boolean {
  return message.includes("projection_not_found") && message.includes("worker dispatch row does not exist")
}

// nothing-durable-release composes the ok receipt for a worker_abandon whose
// durable refusal says the named attempt never dispatched. The core writes no
// event and produces no receipt of its own here — planWorkerAbandon refuses on
// the same missing row — so the adapter composes the receipt the core's typed
// refusal implies. The core is the origin because the generated contract
// permits an ok outcome only on a core envelope, and the authority for the
// statement is the core's own refusal.
function nothingDurableReleaseEnvelope(requestID: string, abandonInput: { work_id: string; attempt_id: string }, releasedRetainedRecord: boolean): HostConcordEnvelope {
  const queryID = (contractOperations.find((candidate: any) => candidate.tool === "concord_work_transition" && candidate.id.endsWith(`.${WORKER_ABANDON_OPERATION}`)) as any)?.query_id
  return {
    schema_version: "1.0",
    manifest_digest: activeManifestDigest(),
    request_id: requestID,
    origin: "core",
    tool: "concord_work_transition",
    operation: WORKER_ABANDON_OPERATION,
    ...(queryID ? { query_id: queryID } : {}),
    outcome: "ok",
    resolved_scope: null,
    authority: "authoritative",
    freshness: null,
    source_version_watermark: [],
    ordering_keys: [],
    next_cursor: null,
    omissions: [],
    warnings: [],
    evidence_refs: [],
    replayed: false,
    result: {
      changed_refs: [],
      next_valid_intents: [],
      nothing_durable_to_abandon: `the core holds no worker attempt row for attempt ${abandonInput.attempt_id} on ${abandonInput.work_id}, so nothing durable existed to abandon and no worker.failed event was written${releasedRetainedRecord ? "; the retained in-flight dispatch record is released and the session can dispatch again" : ""}`,
    },
    changed_refs: [],
    next_valid_intents: [],
  }
}

async function executeWorkerAbandon(args: HostToolArgs, context: ToolContext): Promise<HostConcordEnvelope> {
  const requestID = `${context.sessionID}-${context.messageID}`
  const leaseFault = hostLeaseFault()
  if (leaseFault) return adapterError("concord_work_transition", WORKER_ABANDON_OPERATION, requestID, "transport_failure", "host_lease_missing", `${leaseFault}; no worker attempt was closed`, "none", "contact_operator")
  const input = args.input
  if (!validateGeneratedPayload("work_transition_worker_abandon_input", input)) {
    return adapterError("concord_work_transition", WORKER_ABANDON_OPERATION, requestID, "invalid_input", "invalid_worker_abandon_input", `worker_abandon input failed the generated contract at ${payloadFailurePath("work_transition_worker_abandon_input", input)}`, "none", "correct_request")
  }
  const abandonInput = input as { work_id: string; attempt_id: string; lane_id: string; detail: string; idempotency_key: string }
  const lane = agentLanes.find((candidate) => candidate.id === abandonInput.lane_id)
  if (!lane) return adapterError("concord_work_transition", WORKER_ABANDON_OPERATION, requestID, "invalid_input", "unknown_worker_lane", `worker_abandon does not recognize lane ${JSON.stringify(abandonInput.lane_id)}`, "none", "correct_request")
  const token = workerAbandonToken(abandonInput.idempotency_key)
  const result = await abandonWorkerAttempt(lane, { work_id: abandonInput.work_id, attempt_id: abandonInput.attempt_id }, abandonInput.detail, {
    credentials: credentialsOverride ?? undefined,
    evidenceRunner: runner,
    abandonEventID: `worker-abandon-${token}`,
    abandonNonce: token,
  }, context.abort)
  const message = result.error?.message ?? "worker abandonment returned no diagnostic"
  if (workerAbandonFoundNothingDurable(message)) {
    // nothing-durable-release: the durable state refused because the attempt
    // never dispatched, so the retained record this session still holds has no
    // durable counterpart to wait for. The release demands the exact attempt
    // and lane identity the record holds, so a record naming a different
    // attempt stays retained.
    const releasedRetainedRecord = dispatchWindows().releaseRetained(context.sessionID, abandonInput.attempt_id, abandonInput.lane_id)
    return nothingDurableReleaseEnvelope(requestID, abandonInput, releasedRetainedRecord)
  }
  if (result.error?.retry_safe === false) {
    const receipt = await invokeConcordOperation("concord_work_transition", args, context)
    if (receipt.outcome === "ok") {
      // The attempt is closed at the core, so a retained in-flight record this
      // session still holds for it has no settlement left to wait for.
      // Releasing it here makes the reconciliation route the failure
      // envelopes name real: an abandon accepted now and an idempotent replay
      // answered already terminal both reach this receipt, and the session
      // dispatches again without a host restart. A record naming a different
      // attempt stays retained.
      dispatchWindows().releaseRetained(context.sessionID, abandonInput.attempt_id, abandonInput.lane_id)
      return receipt
    }
    return adapterError("concord_work_transition", WORKER_ABANDON_OPERATION, requestID, "operation_conflict", "worker_abandon_receipt_failed", `the worker attempt was closed, but the durable replay receipt was not recorded: ${JSON.stringify(receipt.error ?? receipt)}`, "possible", "reconcile_operation")
  }
  return adapterError("concord_work_transition", WORKER_ABANDON_OPERATION, requestID, "operation_conflict", "worker_abandon_refused", message, "none", "reconcile_operation")
}

// A claimed worktree is only the session's worktree when the session runs in
// it. work_start moves the session through the same route; worktree_claim
// recorded the claim durably but never moved the session, so a later lane
// dispatch inherited the session's original directory and wrote its delivery
// into the wrong tree (issue #822). The move runs after the claim succeeds,
// the landing is read back from the host rather than assumed, and a miss is
// reported as a typed refusal whose remedy is a replay: the claim is
// idempotent, so retrying worktree_claim adopts the durable claim and retries
// only the move.
export async function moveSessionToClaimedWorktree(args: HostToolArgs, context: ToolContext, envelope: HostConcordEnvelope): Promise<HostConcordEnvelope> {
  if (args?.operation !== "worktree_claim") return envelope
  if (!record(envelope) || envelope.outcome !== "ok") return envelope
  const requestID = `${context.sessionID}-${context.messageID}`
  const result = envelope.result
  const path = record(result) ? result.path : undefined
  if (typeof path !== "string" || !path.startsWith("/")) {
    return adapterError("concord_work_transition", "worktree_claim", requestID, "malformed_response", "claim_destination_unreadable", "core worktree_claim response did not carry an absolute derived destination", "none", "retry_same_request")
  }
  try {
    await ensureConductLink(path, context.abort)
    await hostControlPlane().moveSession(context.sessionID, path, context.abort)
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    return adapterError("concord_work_transition", "worktree_claim", requestID, "transport_failure", "claim_move_refused", `${message}; the claim is durable, replay worktree_claim to retry the move`, "none", "retry_same_request")
  }
  // The move was accepted but the landing cannot be proved, so every refusal
  // past the move records the pending target: dispatch stays closed for this
  // session until a confirmed landing, a vacate, or the durable gate takes
  // over (issue #1322).
  let landed: string
  try {
    landed = await hostControlPlane().sessionDirectory(context.sessionID, context.abort)
  } catch (error) {
    recordUnlandedClaimedWorktree(context.sessionID, path)
    const message = error instanceof Error ? error.message : String(error)
    return adapterError("concord_work_transition", "worktree_claim", requestID, "malformed_response", "claim_move_destination_unreadable", message, "none", "retry_same_request")
  }
  if (!samePath(landed, path)) {
    recordUnlandedClaimedWorktree(context.sessionID, path)
    return adapterError("concord_work_transition", "worktree_claim", requestID, "session_directory_mismatch", "claim_move_destination_mismatch", `the claim recorded ${JSON.stringify(path)} but the session runs in ${JSON.stringify(landed)}`, "none", "retry_same_request")
  }
  // The landing is confirmed, so this session's active claimed worktree is
  // armed for the dispatch check.
  armClaimedWorktree(context.sessionID, path)
  if (!samePath(context.directory, path)) armTurnMoveBoundary(context.sessionID)
  return envelope
}

// moveSessionToRegisteredMainCheckout applies the core-derived vacate target.
// The agent can request the operation but cannot name or replace its destination.
export async function moveSessionToRegisteredMainCheckout(args: HostToolArgs, context: ToolContext, envelope: HostConcordEnvelope): Promise<HostConcordEnvelope> {
  if (args?.operation !== "session_vacate") return envelope
  const requestID = `${context.sessionID}-${context.messageID}`
  const input = args.input
  if (!record(input) || Object.keys(input).some((key) => key === "destination" || key === "destination_directory" || key === "path")) {
    return adapterError("concord_work_transition", "session_vacate", requestID, "invalid_input", "agent_named_destination", "session_vacate derives the registered main checkout and refuses an agent-named destination", "none", "correct_request")
  }
  if (!record(envelope) || envelope.outcome !== "ok") return envelope
  const result = envelope.result
  const destination = record(result) ? result.destination_directory : undefined
  if (typeof destination !== "string" || !destination.startsWith("/")) {
    return adapterError("concord_work_transition", "session_vacate", requestID, "malformed_response", "vacate_destination_unreadable", "core session_vacate response did not carry an absolute derived destination", "none", "retry_same_request")
  }
  try {
    await hostControlPlane().moveSession(context.sessionID, destination, context.abort)
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    const kind = error instanceof MoveSessionUnavailable ? "unreachable" : "transport_failure"
    const reason = error instanceof MoveSessionUnavailable ? "move_session_route_unavailable" : "vacate_move_refused"
    const recovery = error instanceof MoveSessionUnavailable ? "contact_operator" : "retry_same_request"
    return adapterError("concord_work_transition", "session_vacate", requestID, kind, reason, message, "none", recovery)
  }
  let landed: string
  try {
    landed = await hostControlPlane().sessionDirectory(context.sessionID, context.abort)
  } catch (error) {
    return adapterError("concord_work_transition", "session_vacate", requestID, "malformed_response", "vacate_destination_unreadable", error instanceof Error ? error.message : String(error), "none", "retry_same_request")
  }
  if (!samePath(landed, destination)) {
    return adapterError("concord_work_transition", "session_vacate", requestID, "session_directory_mismatch", "vacate_destination_mismatch", `the session landed in ${JSON.stringify(landed)} rather than the registered main checkout ${JSON.stringify(destination)}`, "none", "retry_same_request")
  }
  // The session is back at the main checkout, so no claimed worktree is armed
  // for it any more.
  clearClaimedWorktree(context.sessionID)
  if (!samePath(context.directory, destination)) armTurnMoveBoundary(context.sessionID)
  return envelope
}

async function executeWorkTransition(args: HostToolArgs, context: ToolContext): Promise<HostConcordEnvelope> {
  if (args?.operation === WORKER_ABANDON_OPERATION) return executeWorkerAbandon(args, context)
  if (WORKTREE_REMOVAL_OPERATIONS.has(args?.operation)) {
    await attachLiveSessionObservation(args, context)
    const envelope = await invokeConcordOperation("concord_work_transition", args, context)
    await reportWorktreeRemoval(args, context, envelope)
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
  if (args?.operation === "session_vacate") {
    const input = args.input
    if (!record(input) || Object.keys(input).some((key) => key === "destination" || key === "destination_directory" || key === "path")) {
      return adapterError("concord_work_transition", "session_vacate", `${context.sessionID}-${context.messageID}`, "invalid_input", "agent_named_destination", "session_vacate derives the registered main checkout and refuses an agent-named destination", "none", "correct_request")
    }
    const envelope = await invokeConcordOperation("concord_work_transition", args, context)
    return moveSessionToRegisteredMainCheckout(args, context, envelope)
  }
  const envelope = await invokeConcordOperation("concord_work_transition", args, context)
  const landed = await moveSessionToClaimedWorktree(args, context, envelope)
  return landed
}
