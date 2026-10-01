import { createHash } from "node:crypto"
import fs from "node:fs"
import { clientRef, type CredentialStore } from "./credentials"
import { contractOperations, hostToolDescriptions, hostToolSchemas, maxEnvelopeBytes, payloadSchemas } from "./generated-contracts"
import { activeManifestDigest, adoptManifestDigest, resolveDiskManifestDigest } from "./manifest-pin"
import { validateGeneratedEnvelope, validateGeneratedPayload, envelopeFailurePath, payloadFailurePath } from "./generated-contract-tests"
import { dispatchLaneWorker, type LaneDispatchInput } from "./lane_dispatch"
import { abandonWorkerAttempt } from "./dispatch"
import { dispatchWindows, staleReleaseDispatchRefusal } from "./dispatch-window"
import { agentLanes, type AgentLane } from "./generated-agent-lanes"
import { hostControlPlane, MoveSessionUnavailable } from "./move-session"
import { createRunSessionObservation, errorEnvelopeForLane, MAX_OUTPUT_BYTES, observeRunSessionLine, readExportSessionMetadata, readRunSessionMetadata, readRunTextParts, runStreamRefusalMessage, runStreamRefusalRecovery, validateAgainstSchema, type AgentResultEnvelope, type RunLineMetadata, type RunSessionObservation } from "./dispatch"
import { concordBinaryPath, CoreBinaryUnavailable } from "./dispatch"
import { createWorkStateReporter, formatWorkPaneName } from "./workflow-status"
import { hostLeaseFault, releaseStaleness, type ReleaseStaleness } from "./host-lease"
import { armTurnMoveBoundary } from "./turn-move-boundary"
import { armedClaimedWorktree, armClaimedWorktree, clearClaimedWorktree, pendingVacateDestination, recordPendingVacateDestination, recordUnlandedClaimedWorktree, unlandedClaimedWorktree } from "./claimed-worktree"
import { ensureConductLink } from "./project-link"
import { moveNoticeText, recordMoveNotice, takeMoveNotice } from "./move-notice"

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

// flattenHostSchema projects an authored payload schema into the host-safe
// view. At merged object levels the projection stays permissive so one
// multi-operation tool stays advertisable at bounded size. Bounded nodes —
// array items and the variant oneOf they carry — keep their authored closed
// structure instead: the item required set and additionalProperties survive,
// and a oneOf keeps its branches rather than merging into one property bag
// that would falsely admit every variant's fields at once. This is what lets
// the advertised schema teach the admission rules ValidateOperationPayload
// enforces; the store remains the closed admission boundary.
function flattenHostSchema(value: unknown, resolving = new Set<string>(), bounded = false): JSONSchema {
  if (Array.isArray(value) || typeof value !== "object" || value === null) return {}
  const schema = value as JSONSchema
  if (typeof schema.$ref === "string") {
    const name = schemaName(schema.$ref)
    if (resolving.has(name)) return {}
    const next = new Set(resolving)
    next.add(name)
    const resolved = flattenHostSchema((payloadSchemas as Record<string, unknown>)[name], next, bounded)
    // Sibling keywords alongside a $ref apply in the authored contract and
    // carry the description annotations the host renders, so they overlay the
    // resolved target instead of being dropped with the reference.
    const siblings = Object.fromEntries(Object.entries(schema).filter(([key]) => key !== "$ref" && !hostSchemaStructuralKeys.has(key)))
    return Object.keys(siblings).length > 0 ? { ...resolved, ...siblings } : resolved
  }

  if (bounded && Array.isArray(schema.oneOf)) {
    const result: JSONSchema = {}
    for (const [key, child] of Object.entries(schema)) {
      if (hostSchemaStructuralKeys.has(key) || key === "oneOf") continue
      result[key] = child
    }
    result.oneOf = schema.oneOf.map((branch) => flattenHostSchema(branch, resolving, true))
    return result
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
      result[key] = Array.isArray(child) ? child.map((item) => flattenHostSchema(item, resolving, true)) : flattenHostSchema(child, resolving, true)
    } else {
      result[key] = child
    }
  }
  if (schema.properties !== undefined || schema.type === "object") {
    result.type = "object"
    result.properties = Object.fromEntries(Object.entries((schema.properties ?? {}) as Record<string, unknown>).map(([name, property]) => [name, flattenHostSchema(property, resolving, bounded)]))
    const authoredRequired = Array.isArray(schema.required) ? schema.required as string[] : []
    const authoredProperties = (schema.properties ?? {}) as Record<string, unknown>
    result.required = bounded ? authoredRequired.filter((name) => Object.hasOwn(authoredProperties, name)) : []
    result.additionalProperties = bounded && schema.additionalProperties === false ? false : true
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

function failureEnvelope(toolName: string, operation: string, requestID: string, error: unknown, fallbackReason: string) {
  if (error instanceof AdapterFailure) return adapterError(toolName, operation, requestID, error.kind, error.reason, error.message, error.effect, error.recovery)
  return adapterError(toolName, operation, requestID, "transport_failure", fallbackReason, String(error), "none", "contact_operator")
}

function runnerFailure(error: unknown, aborted: boolean) {
  if (error instanceof AdapterFailure) return error
  if (error instanceof CoreBinaryUnavailable) return new AdapterFailure("transport_failure", "missing_binary", error.message)
  const name = error instanceof Error ? error.name : ""
  const code = typeof error === "object" && error !== null && "code" in error ? String((error as any).code) : ""
  // ENOENT names the spawn's own failure: the binary never resolved and no
  // child started, whatever the abort signal's state, so it outranks the
  // abort and timeout names.
  if (code === "ENOENT") return new AdapterFailure("transport_failure", "missing_binary", String(error))
  if (aborted || name === "AbortError") return new AdapterFailure("cancelled", "cancelled_no_effect", String(error), "none", "retry_same_request")
  if (name === "TimeoutError") return new AdapterFailure("timeout", "timeout_no_effect", String(error), "none", "retry_same_request")
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

// A failed invocation is classified by what its operation kind can have
// done. A read cannot have written: its failures carry effect none, a
// recovery that never reconciles a write, and the transport kind the event
// names. operation_conflict is reserved for an operation whose effect may
// need reconciling, and the envelope law couples it to that recovery, so a
// read never carries it. A mutation's effect stays possible until the
// readback reconciles it.
function unknownOutcomeClassification(toolName: string, operation: string, transportFailure: boolean): [string, string, "none" | "possible", "retry_same_request" | "reconcile_operation"] {
  if (operationIsMutation(toolName, operation)) {
    // session_vacate carries no work id, so the generic reconciliation has
    // no read to drive, and the core commits the relocation request before
    // it answers. The state-driven replay resolves or refuses the committed
    // request from wherever the session sits (CD-0190 D3), so the recovery
    // is the same request again — the adapter remembers nothing the core
    // did not return, and the retry needs no remembered destination.
    if (toolName === "concord_work_transition" && operation === "session_vacate") {
      return transportFailure
        ? ["operation_conflict", "unknown_effect", "possible", "retry_same_request"]
        : ["malformed_response", "malformed_core_response", "possible", "retry_same_request"]
    }
    return transportFailure
      ? ["operation_conflict", "unknown_effect", "possible", "reconcile_operation"]
      : ["malformed_response", "malformed_core_response", "possible", "reconcile_operation"]
  }
  return transportFailure
    ? ["transport_failure", "io_failure", "none", "retry_same_request"]
    : ["malformed_response", "malformed_core_response", "none", "retry_same_request"]
}

// The vacate-specific answer for an unreadable post-commit response: the
// core may have committed the relocation request, and the state-driven
// replay resolves it from wherever the session sits.
function vacateReplayRecovery(message: string): string {
  return `${message}; the core commits the relocation request before it answers, so replay session_vacate — the state-driven replay resolves the committed request from wherever the session sits and the verified landing releases the rows`
}

// One classifier for a thrown runner error at any invoke stage: the first
// invoke, the version-skew retry, and the post-approval run. invokeRan marks
// the version-skew retry: its first child already ran and may have committed,
// so a missing binary on the retry no longer proves an absent effect and
// every mutation throw there takes the mutation classification. A read cannot
// have written, so it keeps the transport event the error names. A mutation
// whose child started may have committed before the abort, the timeout, or
// the crash killed it, so the refusal takes the operation's unknown-outcome
// classification: a possible effect with the reconcile recovery, or the
// session_vacate state-driven replay where no work id can drive a
// reconciliation (CD-0190 D3).
function invokeRunnerFailureEnvelope(toolName: string, operation: string, requestID: string, error: unknown, aborted: boolean, invokeRan: boolean) {
  const failure = runnerFailure(error, aborted)
  if ((failure.reason === "missing_binary" && !invokeRan) || !operationIsMutation(toolName, operation)) {
    return failureEnvelope(toolName, operation, requestID, failure, "spawn_failure")
  }
  const [kind, reason, effect, recovery] = unknownOutcomeClassification(toolName, operation, true)
  const replay = toolName === "concord_work_transition" && operation === "session_vacate"
  return adapterError(toolName, operation, requestID, kind, reason, replay ? vacateReplayRecovery(failure.message) : failure.message, effect, recovery)
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
  // An unreadable session_vacate answer leaves the committed relocation
  // request's state to the replay, so the refusal names that recovery
  // instead of a reconciliation the operation cannot drive.
  const vacateOperation = toolName === "concord_work_transition" && operation === "session_vacate"
  const outcomeMessage = (message: string) => (vacateOperation ? vacateReplayRecovery(message) : message)
  let result: any
  try { result = await run(args.input) } catch (error) { return invokeRunnerFailureEnvelope(toolName, operation, requestID, error, context.abort.aborted, false) }
  if (result.exitCode !== 0 && !result.stdout.trim()) {
    const [kind, reason, effect, recovery] = unknownOutcomeClassification(toolName, operation, true)
    return adapterError(toolName, operation, requestID, kind, reason, outcomeMessage(result.stderr.slice(0, MAX_STDERR)), effect, recovery)
  }
  let response: any
  try { response = singleJSON(result.stdout) } catch (error) {
    const [kind, reason, effect, recovery] = unknownOutcomeClassification(toolName, operation, false)
    return adapterError(toolName, operation, requestID, kind, reason, outcomeMessage(String(error)), effect, recovery, salvageDetails(result.stdout))
  }
  const skewRefusal = () => {
    const disk = resolveDiskManifestDigest()
    const diskDetail = disk === null ? "the digest on disk could not be read" : `the adapter files on disk stamp ${disk}`
    // CD-0111 D4: no refusal names a session restart. The session's core and
    // contract pin to one release (D1), so a digest the retry could not heal
    // is a defect; the operator gets both digests.
    const skewDetail = `core contract digest ${response.manifest_digest} does not match this adapter's ${activeManifestDigest()}; ${diskDetail}; under the pinned release pair this mismatch is a defect, so contact the operator with both digests`
    if (!operationIsMutation(toolName, operation)) return adapterError(toolName, operation, requestID, "transport_failure", "manifest_mismatch", skewDetail, "none", "contact_operator")
    // CD-0190 D3: session_vacate names no work id, so a reconcile_operation
    // recovery cannot drive a reconciliation. The committed relocation
    // request's recovery is the state-driven replay, which keeps the skew
    // detail and both digests in the refusal message.
    if (vacateOperation) return adapterError(toolName, operation, requestID, "operation_conflict", "unknown_effect", vacateReplayRecovery(skewDetail), "possible", "retry_same_request")
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
      try { retryResult = await run(args.input) } catch (error) { return invokeRunnerFailureEnvelope(toolName, operation, requestID, error, context.abort.aborted, true) }
      if (retryResult.exitCode !== 0 && !retryResult.stdout.trim()) {
        const [kind, reason, effect, recovery] = unknownOutcomeClassification(toolName, operation, true)
        return adapterError(toolName, operation, requestID, kind, reason, outcomeMessage(retryResult.stderr.slice(0, MAX_STDERR)), effect, recovery)
      }
      let retryResponse: any
      try { retryResponse = singleJSON(retryResult.stdout) } catch (error) {
        const [kind, reason, effect, recovery] = unknownOutcomeClassification(toolName, operation, false)
        return adapterError(toolName, operation, requestID, kind, reason, outcomeMessage(String(error)), effect, recovery, salvageDetails(retryResult.stdout))
      }
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
    const [, , effect, recovery] = unknownOutcomeClassification(toolName, operation, false)
    return adapterError(toolName, operation, requestID, operationIsMutation(toolName, operation) ? "operation_conflict" : "malformed_response", operationIsMutation(toolName, operation) ? "unknown_effect" : "malformed_core_response", outcomeMessage(`core response failed the generated TS7 contract: ${contractFailure}`), effect, recovery, salvageDetails(result.stdout))
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
    if (toolName === "concord_work_relate" && operation === "client_policy_grant_request") {
      // The operator must see whose policy widens, at which policy version,
      // and why before the expansion can apply (CD-0097 D6).
      requiredChallengeFields.push("summary", "client_ref", "policy_version", "reason")
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
          ...(typeof details.client_ref === "string" ? { client_ref: details.client_ref } : {}),
          ...(typeof details.policy_version === "string" ? { policy_version: details.policy_version } : {}),
          ...(typeof details.reason === "string" ? { reason: details.reason } : {}),
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
    try {
      result = await run(approvedInput)
    } catch (error) {
      return invokeRunnerFailureEnvelope(toolName, operation, requestID, error, context.abort.aborted, false)
    }
    try { response = singleJSON(result.stdout) } catch (error) {
      if (vacateOperation) return adapterError(toolName, operation, requestID, "operation_conflict", "unknown_effect", vacateReplayRecovery(String(error)), "possible", "retry_same_request", salvageDetails(result.stdout))
      return adapterError(toolName, operation, requestID, "operation_conflict", "unknown_effect", String(error), "possible", "reconcile_operation", salvageDetails(result.stdout))
    }
    const approvedFailure = coreResponseFailure(response, toolName, operation)
    if (approvedFailure) {
      if (vacateOperation) return adapterError(toolName, operation, requestID, "operation_conflict", "unknown_effect", vacateReplayRecovery(`post-approval response failed the TS7 contract: ${approvedFailure}`), "possible", "retry_same_request", salvageDetails(result.stdout))
      return adapterError(toolName, operation, requestID, "operation_conflict", "unknown_effect", `post-approval response failed the TS7 contract: ${approvedFailure}`, "possible", "reconcile_operation", salvageDetails(result.stdout))
    }
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

// appendMoveNotice drains the notice a confirmed session move recorded during
// this call and appends it after the envelope line, where the agent reads it.
// The envelope itself stays schema-clean: the published envelope contract is
// closed, so the notice rides the same output layer the warnings use. The
// tool result is the owning surface — it reaches every agent in every
// repository at the moment of the move, while a repository AGENTS.md line
// reaches only that repository's agents.
function appendMoveNotice(result: ToolResult, context: ToolContext): ToolResult {
  const notice = takeMoveNotice(context.sessionID)
  if (!notice) return result
  return appendWarnings(result, [notice])
}

// CD-0191: release staleness rides the result a stale session already gets.
// The notice lives in the envelope's bounded warnings channel — the TS7
// notice shape, no schema change — and names both versions and the restart
// remedy. The warnings array holds at most 16 notices, so a result whose
// core envelope is already full carries the same notice on the output layer
// instead: one notice per result, never an envelope this adapter would
// itself refuse.
const ENVELOPE_WARNINGS_LIMIT = 16
const STALE_RELEASE_NOTICE_KIND = "release_stale"
const STALE_RELEASE_REMEDY = "restart this session to load the installed release"

function stalenessNotice(staleness: ReleaseStaleness): Record<string, unknown> {
  return {
    kind: STALE_RELEASE_NOTICE_KIND,
    source_id: "adapter",
    details: {
      pinned_release: staleness.pinnedRelease,
      installed_release: staleness.installedRelease,
      remedy: STALE_RELEASE_REMEDY,
    },
  }
}

function stalenessSummary(staleness: ReleaseStaleness): string {
  return `Concord staleness: this session pinned release ${staleness.pinnedRelease} but the host installed ${staleness.installedRelease}; ${STALE_RELEASE_REMEDY}.`
}

export function withReleaseStaleness(envelope: HostConcordEnvelope, staleness: ReleaseStaleness | null): { envelope: HostConcordEnvelope; extraWarnings: string[] } {
  if (!staleness) return { envelope, extraWarnings: [] }
  const warnings = record(envelope) && Array.isArray(envelope.warnings) ? envelope.warnings : null
  if (warnings && warnings.length < ENVELOPE_WARNINGS_LIMIT) {
    const carried = { ...envelope, warnings: [...warnings, stalenessNotice(staleness)] }
    // A notice must never degrade the result it annotates: when the carried
    // envelope would exceed the byte cap that the encoder enforces, the
    // notice falls back to the output layer and the core result stands.
    if (Buffer.byteLength(JSON.stringify(carried)) <= maxEnvelopeBytes) {
      return { envelope: carried, extraWarnings: [] }
    }
  }
  return { envelope, extraWarnings: [stalenessSummary(staleness)] }
}

function encodeHostResult(toolName: string, operation: string, requestID: string, envelope: HostConcordEnvelope): ToolResult {
  let output = JSON.stringify(envelope)
  if (Buffer.byteLength(output) > maxEnvelopeBytes) {
    const [, , effect, recovery] = unknownOutcomeClassification(toolName, operation, false)
    output = JSON.stringify(adapterError(toolName, operation, requestID, "malformed_response", "malformed_core_response", `Concord result exceeds ${maxEnvelopeBytes} bytes`, effect, recovery))
  }
  return { title: toolName, output, metadata: {} }
}

async function encodeHostToolResult(toolName: string, args: HostToolArgs, context: ToolContext, envelope: HostConcordEnvelope): Promise<ToolResult> {
  if (Buffer.byteLength(JSON.stringify(envelope)) > maxEnvelopeBytes) {
    const [, , effect, recovery] = unknownOutcomeClassification(toolName, args.operation, false)
    const oversized = adapterError(toolName, args.operation, `${context.sessionID}-${context.messageID}`, "malformed_response", "malformed_core_response", `Concord result exceeds ${maxEnvelopeBytes} bytes`, effect, recovery)
    return encodeHostResult(toolName, args.operation, `${context.sessionID}-${context.messageID}`, await reconcileUnknownEffect(toolName, args, context, oversized))
  }
  return encodeHostResult(toolName, args.operation, `${context.sessionID}-${context.messageID}`, envelope)
}

async function executeHostTool(toolName: string, args: HostToolArgs, context: ToolContext): Promise<ToolResult> {
  // One staleness observation per tool call (CD-0191): the same reading
  // feeds the dispatch gate and the result notice.
  const staleness = releaseStaleness()
  const envelope = await invokeConcordOperation(toolName, args, context)
  const { envelope: settled, extraWarnings } = withReleaseStaleness(envelope, staleness)
  const warnings = operationIsMutation(toolName, args.operation) ? await workStateReporter.report(settled, context) : []
  return appendWarnings(await encodeHostToolResult(toolName, args, context, settled), [...warnings, ...extraWarnings])
}

async function executeHostTransition(args: HostToolArgs, context: ToolContext): Promise<ToolResult> {
  const staleness = releaseStaleness()
  const envelope = await executeWorkTransition(args, context, staleness)
  const { envelope: settled, extraWarnings } = withReleaseStaleness(envelope, staleness)
  const warnings = await workStateReporter.report(settled, context)
  return appendMoveNotice(appendWarnings(await encodeHostToolResult("concord_work_transition", args, context, settled), [...warnings, ...extraWarnings]), context)
}

// CD-0017 D4, extended by CD-0196: a dispatched worker lane runs under a
// managed parent and holds no Concord tool access — the dispatch packet is
// its complete Concord context. The guard consults the host parent boundary,
// which does not trust the selected agent or its prompt, and refuses before
// the core is invoked, so a lane call records nothing. The guard is
// fail-closed: only a host answer that positively resolves the caller as an
// unparented coordinator session passes. A session with a managed parent, a
// session whose scope the host cannot resolve, and a control plane that
// cannot answer at all all refuse unauthorized with effect_state none — each
// leaves the caller unproven, and an unproven caller may be a lane. No
// coordinator route changes: a coordinator session resolves unparented and
// passes unchanged. A caller-cancelled signal is not a scope answer: the
// boundary stands down for it, so the transport keeps the typed cancelled
// envelope and the reconciliation retry paths stay reachable.
const laneToolBoundary = "Concord worker lanes hold no Concord tool access (CD-0017 D4, extended by CD-0196): the dispatch packet is the lane's complete Concord context"
async function laneConcordRefusalReason(context: ToolContext): Promise<string | null> {
  if (context.abort.aborted) return null
  // Without a bound control plane the parent boundary cannot answer, so the
  // caller cannot be proven a coordinator session and the call refuses.
  if (!hostControlPlane().available()) {
    return `${laneToolBoundary}; the host control plane is unbound, so the caller cannot be proven a coordinator session`
  }
  let managedParent: boolean
  try {
    managedParent = await hostControlPlane().hasManagedParent(context.sessionID, context.abort)
  } catch (error) {
    if (context.abort.aborted) return null
    const detail = error instanceof Error ? error.message : String(error)
    return `${laneToolBoundary}; Concord cannot resolve this session's managed Task scope, so it cannot prove the caller is a coordinator session (${detail})`
  }
  if (!managedParent) return null
  return `${laneToolBoundary}; the caller session runs under a managed parent`
}

function laneConcordRefusalEnvelope(toolName: string, operation: string, context: ToolContext, reason: string): ToolResult {
  const requestID = `${context.sessionID}-${context.messageID}`
  const envelope = adapterError(toolName, operation, requestID, "unauthorized", "lane_tool_refusal", `${reason}; the call is refused with effect_state none and records nothing.`, "none", "contact_operator")
  return encodeHostResult(toolName, operation, requestID, envelope)
}

// laneGuarded fronts one concord_* tool entry with the lane guard, so every
// entry checks the caller before its transport runs. work_start carries its
// own closed result shape, so it supplies the refusal builder that speaks it.
function laneGuarded(toolName: string, run: (args: any, context: ToolContext) => Promise<ToolResult>, refuse: (operation: string, context: ToolContext, reason: string) => ToolResult = (operation, context, reason) => laneConcordRefusalEnvelope(toolName, operation, context, reason)) {
  return async (args: any, context: ToolContext): Promise<ToolResult> => {
    const wrapped = args !== null && typeof args === "object" && args.request !== null && typeof args.request === "object"
    const operation = wrapped ? String(hostRequest(args)?.operation ?? "") : ""
    const reason = await laneConcordRefusalReason(context)
    if (reason !== null) return refuse(operation, context, reason)
    return run(args, context)
  }
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
// records nothing, so it carries no idempotency_key and no capture fields. An
// optional project_id names a member Project of the work whose landing the
// resume resolves against (CD-0182); the empty case keeps the calling
// session's Project.
type WorkStartArgs = WorkStartCaptureArgs | { work_id: string; project_id?: string }

// LinearRemoteComment is one remote comment created after the recorded
// remote freshness, with its body already bounded by the core.
type LinearRemoteComment = { author: string; created_at: string; body: string }

// LinearRemoteSection mirrors the resume-time remote Linear check the core
// attaches to a work-resume result. Degraded authority carries only the
// typed reason; ok carries the full comparison. The core writes nothing on
// this path, so the section is information the resuming session reads.
type LinearRemoteSection = {
  authority: "ok" | "degraded"
  reason?: string
  changed_since_recorded?: boolean
  updated_at?: string
  status?: { expected: string; actual: string; remote_state_type: string; mismatch: boolean }
  title?: { remote: string; differs: boolean }
  description?: string
  description_truncated?: boolean
  comments?: { items: LinearRemoteComment[]; truncated: boolean; reason?: string }
}

type WorkStartResume = {
  schema_version: "1.0"
  product_id: string
  project_id: string
  work_id: string
  worktree: { set_id: string; path: string; branch: string; base_sha: string; state: "active" }
  linear_remote?: LinearRemoteSection
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

// WorkStartLaunch is the exact second-session launch: the core launch argv
// as separate elements, the target Project directory it lands in, and a
// runnable rendering of the same command for the operator.
type WorkStartLaunch = { argv: string[]; directory: string; runnable: string }

// WorkStartEnvelope is the host-tool result. There is no partial outcome:
// every step of work_start is idempotent on the derived key, so a failure
// leaves nothing a replay cannot adopt, and the answer is ok or a refusal.
// A second-coordinator-session route reports the launch and, when the
// host-registered opener ran, its substituted argv and exit status; it never
// claims the new session is running.
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
  linear_remote?: LinearRemoteSection
  launch?: WorkStartLaunch
  opener?: { argv: string[]; exit_code: number }
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
// shared resume response. linear_remote is the optional remote Linear check
// section: absent exactly when the resume applies no remote check.
function validateWorkStartResume(value: unknown): value is WorkStartResume {
  if (!record(value)) return false
  const baseKeys = ["schema_version", "product_id", "project_id", "work_id", "worktree"]
  if (!baseKeys.every((key) => key in value)) return false
  const extraKeys = Object.keys(value).filter((key) => !baseKeys.includes(key))
  if (extraKeys.length > 1 || (extraKeys.length === 1 && extraKeys[0] !== "linear_remote")) return false
  if (value.schema_version !== "1.0" || !nonEmptyString(value.product_id) || !nonEmptyString(value.project_id) || !nonEmptyString(value.work_id) || !record(value.worktree)) return false
  const worktree = value.worktree
  const worktreeOk = exactKeys(worktree, ["set_id", "path", "branch", "base_sha", "state"])
    && nonEmptyString(worktree.set_id)
    && typeof worktree.path === "string" && worktree.path.startsWith("/")
    && nonEmptyString(worktree.branch) && /^[0-9a-f]{40}$/.test(String(worktree.base_sha)) && worktree.state === "active"
  if (!worktreeOk) return false
  return !("linear_remote" in value) || validateLinearRemoteSection(value.linear_remote)
}

const linearRemoteReasons = new Set(["missing_credentials", "unauthorized", "rate_limited", "timeout", "unavailable", "not_found", "local_unavailable"])

function validateLinearRemoteComment(value: unknown): value is LinearRemoteComment {
  return record(value) && exactKeys(value, ["author", "created_at", "body"])
    && typeof value.author === "string" && typeof value.created_at === "string" && typeof value.body === "string"
}

// validateLinearRemoteSection is the strict shape for the remote Linear
// check section. Degraded authority carries only its typed reason; ok
// carries the full comparison with the comments reason as the one optional
// key. Exact keys keep the contract closed on both sides.
function validateLinearRemoteSection(value: unknown): boolean {
  if (!record(value)) return false
  if (value.authority === "degraded") {
    return exactKeys(value, ["authority", "reason"]) && typeof value.reason === "string" && linearRemoteReasons.has(value.reason)
  }
  if (value.authority !== "ok") return false
  if (!exactKeys(value, ["authority", "changed_since_recorded", "updated_at", "status", "title", "description", "description_truncated", "comments"])) return false
  if (typeof value.changed_since_recorded !== "boolean" || typeof value.updated_at !== "string" || typeof value.description !== "string" || typeof value.description_truncated !== "boolean") return false
  const status = value.status
  if (!record(status) || !exactKeys(status, ["expected", "actual", "remote_state_type", "mismatch"])) return false
  if (typeof status.expected !== "string" || typeof status.actual !== "string" || typeof status.remote_state_type !== "string" || typeof status.mismatch !== "boolean") return false
  const title = value.title
  if (!record(title) || !exactKeys(title, ["remote", "differs"]) || typeof title.remote !== "string" || typeof title.differs !== "boolean") return false
  const comments = value.comments
  if (!record(comments)) return false
  const commentKeys = Object.keys(comments)
  if (commentKeys.length !== 2 && commentKeys.length !== 3) return false
  if (typeof comments.truncated !== "boolean" || !Array.isArray(comments.items) || !comments.items.every(validateLinearRemoteComment)) return false
  if (!("reason" in comments)) return commentKeys.length === 2
  return commentKeys.length === 3 && typeof comments.reason === "string" && linearRemoteReasons.has(comments.reason)
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

function workStartError(kind: string, message: string, identity: Partial<WorkStartEnvelope> = {}, recovery = "retry_same_request", retrySafe = true, extra: Partial<WorkStartEnvelope> = {}): WorkStartEnvelope {
  return {
    schema_version: "1.0",
    outcome: "error",
    ...identity,
    ...extra,
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

// The session opener is host placement (CD-0078 as amended by CD-0182): the
// operator registers one argv template in the options of the Concord plugin
// tuple entry in the OpenCode config, and the host hands it to the plugin
// factory. Concord owns no multiplexer and ships none; the template is data
// the operator chose. Registering it is the operator's standing consent for
// coordinators to open sessions and spend model quota.
let sessionOpener: unknown
export function configureSessionOpener(value: unknown): void {
  sessionOpener = value
}
function registeredSessionOpener(): unknown {
  return sessionOpener
}

const openerPlaceholderNames = new Set(["directory", "title", "command"])

// sessionOpenerTemplate validates the registered argv template structurally:
// an array of strings, the {command} placeholder as exactly one whole
// element, and no placeholder outside the closed set. Anything else refuses
// naming the invalid field.
function sessionOpenerTemplate(value: unknown): { ok: true; argv: string[] } | { ok: false; detail: string } {
  if (!Array.isArray(value)) return { ok: false, detail: "session_opener is not an array" }
  if (value.length === 0) return { ok: false, detail: "session_opener is empty" }
  for (const [index, element] of value.entries()) {
    if (typeof element !== "string") return { ok: false, detail: `session_opener[${index}] is not a string` }
    if (element !== "{command}" && element.includes("{command}")) return { ok: false, detail: `session_opener[${index}] embeds {command} inside a larger element; {command} must be one whole element` }
    for (const match of element.matchAll(/\{([^{}]*)\}/g)) {
      if (!openerPlaceholderNames.has(match[1])) return { ok: false, detail: `session_opener[${index}] carries the unknown placeholder {${match[1]}}` }
    }
  }
  const commandElements = value.filter((element) => element === "{command}")
  if (commandElements.length !== 1) return { ok: false, detail: `session_opener must carry the {command} placeholder exactly once as a whole element, found ${commandElements.length}` }
  return { ok: true, argv: value as string[] }
}

// spliceOpener substitutes the placeholders into the template. {directory}
// and {title} are string substitutions; {command} is the core launch argv
// spliced as separate elements, so no shell ever re-quotes a title or a
// path.
function spliceOpener(argv: string[], values: { directory: string; title: string; command: string[] }): string[] {
  const substitute = (element: string) => element.replaceAll("{directory}", values.directory).replaceAll("{title}", values.title)
  const index = argv.indexOf("{command}")
  return [
    ...argv.slice(0, index).map(substitute),
    ...values.command,
    ...argv.slice(index + 1).map(substitute),
  ]
}

// shellQuote renders one argv element for the operator's shell. Elements the
// safe pattern admits pass through; everything else gets single quotes with
// the POSIX escape.
function shellQuote(value: string): string {
  return /^[A-Za-z0-9_@%+=:,./-]+$/.test(value) ? value : `'${value.replaceAll("'", `'\\''`)}'`
}

// secondSessionLaunch builds the core launch argv for the second coordinator
// session: the zl verb with the explicit member-Project selector (CD-0182).
// The command itself resolves the landing directory when the new session
// starts; directory is the target Project's canonical path the opener and
// the operator place the new terminal at.
function secondSessionLaunch(workID: string, projectID: string, directory: string): WorkStartLaunch {
  const argv = [concordBinaryPath(), "zl", workID, "--project", projectID]
  return { argv, directory, runnable: argv.map(shellQuote).join(" ") }
}

// projectCanonicalRepository resolves a Project's registered canonical
// repository path through the core's project-canonical-path read. The
// canonical path is the comparison value for the repository gate: two equal
// canonical paths are one repository, and a differing path never reaches the
// host move that crosses repositories. The read resolves no git ref and no
// commit, so a member Project whose repository has no resolvable default ref
// still routes; worktree-locate, which derives those git facts for the
// worktree claim, stays out of the routing decision (CD-0182).
async function projectCanonicalRepository(projectID: string, context: ToolContext): Promise<string> {
  const result = await runWorkStartChild([concordBinaryPath(), "project-canonical-path"], JSON.stringify({ project_id: projectID }), context.abort, { cwd: context.directory })
  if (result.exitCode !== 0) throw new AdapterFailure("invalid_input", "project_location_failed", result.stderr.slice(0, MAX_STDERR), "none", "correct_request")
  let parsed: any
  try { parsed = singleJSON(result.stdout) } catch (error) { throw new AdapterFailure("malformed_response", "malformed_core_response", String(error)) }
  if (typeof parsed.canonical_path !== "string" || !parsed.canonical_path.startsWith("/")) {
    throw new AdapterFailure("malformed_response", "malformed_core_response", "project-canonical-path response failed the location contract")
  }
  return parsed.canonical_path
}

// canonicalPathResolves probes that a registered canonical path still
// resolves to an existing directory on this filesystem. The core's
// project-canonical-path verb answers stored Product truth unprobed, so the
// adapter — which owns the host placement decision — probes here, before any
// opener runs (CD-0182 D2; CD-0093 D3's fail-closed rule). statSync follows
// symlinks and throws on a missing entry, so every failure refuses.
function canonicalPathResolves(directory: string): boolean {
  try { return fs.statSync(directory).isDirectory() } catch { return false }
}

// openSecondCoordinatorSession routes a resume whose named Project lives in
// another repository (CD-0178 D2, CD-0182). Concord never claims the new
// session is running: with a registered opener it probes that the target
// directory resolves, then runs the substituted argv without a shell and
// reports the exit status and argv; with none registered, with an invalid
// one, or with a canonical path that no longer resolves, it returns the
// exact launch command and directory for the operator without running
// anything. Every answer leaves this session un-moved, so the refusal names
// contact_operator: the operator or the new session owns the next step.
async function openSecondCoordinatorSession(workID: string, projectID: string, directory: string, context: ToolContext): Promise<WorkStartEnvelope> {
  const launch = secondSessionLaunch(workID, projectID, directory)
  const identity = { work_id: workID, project_id: projectID }
  const command = `Run the launch command yourself in ${directory}: ${launch.runnable}`
  const registered = registeredSessionOpener()
  if (registered === undefined) {
    return workStartError("session_opener_unregistered", `No session opener is registered on this host, so Concord cannot open the second session itself. ${command}`, identity, "contact_operator", false, { launch })
  }
  const opener = sessionOpenerTemplate(registered)
  if (!opener.ok) {
    return workStartError("invalid_session_opener", `The host-registered session opener is invalid: ${opener.detail}. ${command}`, identity, "contact_operator", false, { launch })
  }
  if (!canonicalPathResolves(directory)) {
    return workStartError("canonical_path_unresolved", `The canonical path ${directory} does not resolve on this filesystem, so Concord refuses to run the session opener against it. ${command}`, identity, "contact_operator", false, { launch })
  }
  const argv = spliceOpener(opener.argv, { directory, title: workID, command: launch.argv })
  let result
  try {
    result = await runner.run(argv, "", context.abort, { cwd: context.directory })
  } catch (error) {
    const failure = runnerFailure(error, context.abort.aborted)
    return workStartError("session_opener_failed", `The session opener could not run: ${failure.message}. Concord does not claim the second session is running. ${command}`, identity, "contact_operator", false, { launch })
  }
  const openerReport = { argv, exit_code: result.exitCode }
  if (result.exitCode !== 0) {
    return workStartError("session_opener_failed", `The session opener exited ${result.exitCode}: ${result.stderr.slice(0, MAX_STDERR) || "no diagnostic"}. Concord does not claim the second session is running. ${command}`, identity, "contact_operator", false, { launch, opener: openerReport })
  }
  return workStartError(
    "second_session_opened",
    `The session opener ran with exit ${result.exitCode}; Concord does not claim the new session is running. The new coordinator session starts in ${directory} and resumes this work by calling concord_work_start with work_id ${workID} and project_id ${projectID}. This session must not claim or drive the other repository.`,
    identity,
    "contact_operator",
    false,
    { launch, opener: openerReport },
  )
}

// The core reports a deterministic session-prepare refusal — invalid input,
// or a state or identity check that fails the same way until state changes —
// with this typed exit status, declared in the core's session-prepare help.
// Classification uses the status alone, never stderr text: replaying the same
// request cannot clear a refusal, so it maps to contact_operator, while any
// other session-prepare failure stays retryable.
export const sessionPrepareRefusalExit = 2

// The core reports a deterministic work-bootstrap refusal — invalid input,
// an idempotency key bound to different input, or an origin or Project check
// that fails the same way until state changes — with this typed exit status,
// declared in the core's work-bootstrap help. Classification uses the status
// alone, never stderr text: replaying the same request cannot clear a
// refusal, so it maps to contact_operator, while any other work-bootstrap
// failure stays retryable.
export const workBootstrapRefusalExit = 2

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
//   5. A resume records the verified landing through the core's claim-landing
//      verb. Its resume read records nothing (CD-0104 D1), so the store holds
//      no occupancy for the session until the record lands, and the removal
//      gates (worktree_audit_reclaim, worktree_reclaim) hold the worktree for
//      the session that runs in it.
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
    // CD-0182: a resume that names a member Project branches before any
    // Product derivation or host probe. A Project whose canonical path is
    // the calling Project's never leaves the existing claim-and-move route;
    // a Project whose canonical path differs from the calling one never
    // reaches the host move that crossing repositories would refuse.
    if (resume && typeof (args as { project_id?: string }).project_id === "string") {
      const selectedProject = (args as { project_id: string }).project_id
      if (selectedProject !== ambient.projectID) {
        const workID = (args as { work_id: string }).work_id
        const selectedRepo = await projectCanonicalRepository(selectedProject, context)
        const ambientRepo = await projectCanonicalRepository(ambient.projectID, context)
        if (selectedRepo !== ambientRepo) {
          return await openSecondCoordinatorSession(workID, selectedProject, selectedRepo, context)
        }
      }
    }
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
    let resumeRemote: LinearRemoteSection | undefined
    if (resume) {
      const workID = (args as { work_id: string }).work_id
      // An explicit project_id names the Project the resume claims in; the
      // empty case keeps the calling session's resolved Project (CD-0182).
      const projectID = typeof (args as { project_id?: string }).project_id === "string" ? (args as { project_id: string }).project_id : ambient.projectID
      const resumed = await runWorkStartChild([concordBinaryPath(), "work-resume"], JSON.stringify({ product_id: productID, project_id: projectID, work_id: workID, session_ref: context.sessionID }), context.abort, { cwd: context.directory })
      if (resumed.exitCode !== 0) throw new AdapterFailure("resume_failure", "resume_refused", resumed.stderr.slice(0, MAX_STDERR), "none", "retry_same_request")
      let resumedValue: unknown
      try { resumedValue = singleJSON(resumed.stdout) } catch (error) { throw new AdapterFailure("malformed_response", "malformed_resume_response", String(error), "none", "retry_same_request") }
      if (!validateWorkStartResume(resumedValue) || resumedValue.product_id !== productID || resumedValue.project_id !== projectID || resumedValue.work_id !== workID) throw new AdapterFailure("malformed_response", "malformed_resume_response", "work-resume response failed the strict resume contract", "none", "retry_same_request")
      target = resumedValue
      resumeRemote = resumedValue.linear_remote
      prepareTask = ""
    } else {
      const capture = args as WorkStartCaptureArgs
      // host_pid gives the bootstrap claim's occupancy row its process
      // identity from creation (CD-0179); the landing re-records it.
      const bootstrapInput = { product_id: productID, project_id: ambient.projectID, ...capture, session_ref: context.sessionID, host_pid: process.pid }
      const boot = await runWorkStartChild([concordBinaryPath(), "work-bootstrap"], JSON.stringify(bootstrapInput), context.abort, { cwd: context.directory })
      if (boot.exitCode === workBootstrapRefusalExit) throw new AdapterFailure("bootstrap_failure", "bootstrap_refused", boot.stderr.slice(0, MAX_STDERR), "none", "contact_operator")
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
    // A resumed session claims no worktree: the read that derives its active
    // worktree records nothing (CD-0104 D1), so the store holds no occupancy
    // for it. Once both readbacks name the worktree, the landing records
    // itself through the same claim-landing owner the verified claim route
    // uses, so worktree_audit_reclaim and worktree_reclaim hold the worktree
    // for the session that runs in it. The record replays idempotently.
    // Occupancy never refuses the move (CD-0104 D5, CD-0119): the session has
    // landed, so a refused record is a warning and the start still succeeds.
    // A worktree another live session occupies is already held by that
    // recorded occupant.
    if (resume) {
      try {
        await recordClaimLanding(target.work_id, context.sessionID, target.worktree.path, context.abort)
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error)
        warnings.push(`Concord did not record this session as the occupant of ${target.worktree.path}: ${message}. The worktree removal gates may not hold it for this session; replay work_start to retry the record.`)
      }
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
      // The move notice replaces the bare runs-in line: the agent needs the
      // new path, the paths-under-it rule, and the stale surfaces, not only
      // the fact of the move.
      output: moveNoticeText(target.worktree.path),
      // The remote Linear check rides the resume result into the envelope so
      // the resuming session sees remote drift before it acts.
      ...(resumeRemote ? { linear_remote: resumeRemote } : {}),
    }
  } catch (error) {
    return workStartFailure(error, target, "work_start_failed")
  }
}

export const product_view = tool({ description: "Concord product view", args: argsSchema("concord_product_view"), execute: laneGuarded("concord_product_view", (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_product_view", hostRequest(args), context)) })
export const work_browse = tool({ description: "Concord work browse", args: argsSchema("concord_work_browse"), execute: laneGuarded("concord_work_browse", (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_browse", hostRequest(args), context)) })
export const work_trace = tool({ description: "Concord work trace", args: argsSchema("concord_work_trace"), execute: laneGuarded("concord_work_trace", (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_trace", hostRequest(args), context)) })
export const knowledge = tool({ description: "Concord knowledge", args: argsSchema("concord_knowledge"), execute: laneGuarded("concord_knowledge", (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_knowledge", hostRequest(args), context)) })
export const work_define = tool({ description: "Concord work define", args: argsSchema("concord_work_define"), execute: laneGuarded("concord_work_define", (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_define", hostRequest(args), context)) })
export const domain = tool({ description: "Concord domain", args: argsSchema("concord_domain"), execute: laneGuarded("concord_domain", (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_domain", hostRequest(args), context)) })
export const work_initiative = tool({ description: "Concord work initiative", args: argsSchema("concord_work_initiative"), execute: laneGuarded("concord_work_initiative", (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_initiative", hostRequest(args), context)) })
export const work_transition = tool({ description: "Concord work transition. Use operation workflow_action for declared workflow actions. Use operation worker_abandon with the attempt and lane identity to close a dispatched attempt with no report. Use action_id dispatch_worker with fields.lane_id for the native worker route. Route discovery does not prove admission at the current workflow step.", args: argsSchema("concord_work_transition"), execute: laneGuarded("concord_work_transition", (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTransition(hostRequest(args), context)) })
export const work_relate = tool({
  description: "Concord work relate",
  args: argsSchema("concord_work_relate"),
  execute: laneGuarded("concord_work_relate", async (args: HostToolCall, context: ToolContext): Promise<ToolResult> => {
    // Same shape as the transition tool: a supersede that terminalizes the
    // work whose worktree this session runs in carries a vacate_target the
    // adapter applies before the result is reported (CD-0179).
    const staleness = releaseStaleness()
    const request = hostRequest(args)
    const envelope = await invokeConcordOperation("concord_work_relate", request, context)
    const vacated = await vacateTerminalWorktree("concord_work_relate", request, context, envelope)
    const { envelope: settled, extraWarnings } = withReleaseStaleness(vacated, staleness)
    const warnings = operationIsMutation("concord_work_relate", request.operation) ? await workStateReporter.report(settled, context) : []
    return appendMoveNotice(appendWarnings(await encodeHostToolResult("concord_work_relate", request, context, settled), [...warnings, ...extraWarnings]), context)
  }),
})
export const work_compact = tool({ description: "Concord work compact", args: argsSchema("concord_work_compact"), execute: laneGuarded("concord_work_compact", (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_compact", hostRequest(args), context)) })
export const work_start = tool({ description: `${hostToolDescriptions.concord_work_start} ${workStartUsage}`, args: workStartArgsSchema(), execute: laneGuarded("concord_work_start", async (args: any, context: ToolContext): Promise<ToolResult> => {
  const staleness = releaseStaleness()
  // The work_start result envelope is the adapter's own closed shape with no
  // warnings member, so the staleness notice rides the output layer the
  // start already uses for best-effort warnings (CD-0191).
  const warnings: string[] = staleness ? [stalenessSummary(staleness)] : []
  const envelope = await executeWorkStart(args as WorkStartArgs, context, warnings)
  let output = JSON.stringify(envelope)
  if (Buffer.byteLength(output) > maxEnvelopeBytes) output = JSON.stringify(workStartError("output_exceeded", `work_start result exceeds ${maxEnvelopeBytes} bytes`, { product_id: envelope.product_id, project_id: envelope.project_id, work_id: envelope.work_id, worktree_path: envelope.worktree_path }))
  return appendWarnings({ title: "concord_work_start", output, metadata: {} }, warnings)
}, (operation, context, reason) => ({ title: "concord_work_start", output: JSON.stringify(workStartError("unauthorized", `${reason}; the call is refused with effect_state none and records nothing.`, {}, "contact_operator")), metadata: {} })) })

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

// ownRowAbandonRefusal reports whether an abandonment refusal is the
// worktree-ownership gate naming the calling session itself as the live
// holder. The store's live-occupancy refusal writes the holder's session ref
// into the detail (validateNoLiveWorkerSession in internal/store/
// worker_lanes.go), so the adapter can compare the named holder with the
// calling session it owns. Both refusal shapes carry the gate kind, and both
// release through session_vacate when the holder is this session; a refusal
// naming any other session keeps the operator-owned recovery.
export function ownRowAbandonRefusal(message: string, sessionID: string): boolean {
  if (!message.includes("worktree_ownership_conflict")) return false
  if (!sessionID) return false
  return message.includes(`session ${sessionID} still holds the worker attempt worktree`) || message.includes(`session ${sessionID} holds a legacy occupancy row`)
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
  // Own-row recovery: the gate refused because the calling session itself
  // still occupies the worker attempt worktree. The named release route
  // (vacate, abandon retry, re-land) runs here instead of falling to the
  // operator; a foreign holder never reaches it.
  if (ownRowAbandonRefusal(message, context.sessionID)) {
    return recoverOwnRowAbandon(args, context, abandonInput, lane, token, requestID, message)
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

// recoverOwnRowAbandon runs the release route the live-occupancy refusal
// names, for the one holder the adapter owns: the calling session. The worker
// attempt close refuses while the calling session itself still occupies the
// worker attempt worktree, and the vacate-abandon-re-land sequence was left
// to the operator. The adapter now runs it in order, reporting each step on
// the answer: session_vacate releases the calling session's own row and moves
// the session to the registered main checkout, one abandon retry with the
// same derived event identity closes the attempt, the durable receipt records
// it, and work_start re-lands the session in the claimed worktree it vacated.
// A step that fails stops the sequence with the steps already taken named; a
// refused re-land leaves the closed attempt standing and names the replay.
async function recoverOwnRowAbandon(args: HostToolArgs, context: ToolContext, abandonInput: { work_id: string; attempt_id: string; lane_id: string; detail: string; idempotency_key: string }, lane: AgentLane, token: string, requestID: string, refusedMessage: string): Promise<HostConcordEnvelope> {
  const steps: string[] = []
  const recoveryNotices = (): Array<Record<string, unknown>> => steps.map((taken) => ({ kind: "worker_abandon_recovery", source_id: "adapter", details: { step: taken } }))
  // The re-land returns the session to the claimed worktree the vacate moved
  // it out of, so the recovery leaves it where it started. A refused abandon
  // retry needs it as much as a closed one: after the vacate the session sits
  // at the registered main checkout, so stopping there strands the
  // coordinator — the re-land runs before that stop too, and its outcome
  // rides the answer as a step either way.
  const reland = async (): Promise<void> => {
    const relandWarnings: string[] = []
    try {
      const relanded = await executeWorkStart({ work_id: abandonInput.work_id }, context, relandWarnings)
      if (record(relanded) && relanded.outcome === "ok") {
        const relandPath = typeof relanded.worktree_path === "string" ? relanded.worktree_path : null
        // The confirmed re-land supersedes the vacate's intermediate move
        // notice: the composed answer must point the agent at the destination
        // the session finally occupies, so the pending main-checkout notice
        // is replaced before the result drains the queue. The notice rides
        // the composed answer here rather than the re-land envelope, whose
        // output this sequence discards.
        if (relandPath) recordMoveNotice(context.sessionID, moveNoticeText(relandPath))
        steps.push(`work_start: the session re-landed in ${relandPath ?? "the claimed worktree"}`)
      } else {
        const failure = record(relanded) && record(relanded.error) ? relanded.error : null
        const detail = failure && typeof failure.message === "string" ? failure.message : "no diagnostic"
        steps.push(`work_start re-land refused: ${detail}; replay work_start once the session's tool context runs in the claimed worktree`)
      }
    } catch (error) {
      const detail = error instanceof Error ? error.message : String(error)
      steps.push(`work_start re-land refused: ${detail}; replay work_start once the session's tool context runs in the claimed worktree`)
    }
    for (const warning of relandWarnings) steps.push(`work_start warning: ${warning}`)
  }
  // 1. session_vacate: the named release route. The composed route records
  // the release at the core and moves the session to the registered main
  // checkout the core derives, never a destination this adapter names.
  const vacateArgs: HostToolArgs = { operation: "session_vacate", input: { idempotency_key: `${requestID}-abandon-vacate` } }
  let vacateCommitted = false
  let vacateEffectState: "none" | "possible" | undefined
  try {
    const vacated = await invokeConcordOperation("concord_work_transition", vacateArgs, context)
    if (!record(vacated) || vacated.outcome !== "ok") {
      const failure = record(vacated) && record(vacated.error) ? vacated.error : null
      if (failure && (failure.effect_state === "none" || failure.effect_state === "possible")) {
        vacateEffectState = failure.effect_state
      }
      const detail = failure && typeof failure.message === "string" ? failure.message : "the core refused the vacate"
      throw new Error(detail)
    }
    vacateCommitted = true
    const moved = await moveSessionToRegisteredMainCheckout(vacateArgs, context, vacated)
    if (!record(moved) || moved.outcome !== "ok") {
      const failure = record(moved) && record(moved.error) ? moved.error : null
      const detail = failure && typeof failure.message === "string" ? failure.message : "the vacate move did not confirm a landing"
      throw new Error(detail)
    }
  } catch (error) {
    const detail = error instanceof Error ? error.message : String(error)
    // A committed core vacate records the relocation request durably even
    // when its host move or readback failed afterwards; the occupancy rows
    // stand until the vacate-landing verb records the verified landing. The
    // stop reports a possible effect and names the committed request as a
    // step (TS7 durable-outcome honesty); a refusal before the write keeps
    // none.
    if (vacateCommitted) steps.push("session_vacate: the relocation request is recorded at the core; the occupancy rows stand until the landing is recorded")
    return adapterError("concord_work_transition", WORKER_ABANDON_OPERATION, requestID, "operation_conflict", "worker_abandon_refused", `${refusedMessage}; the own-row recovery stopped at session_vacate: ${detail}`, vacateCommitted ? "possible" : vacateEffectState ?? "none", "reconcile_operation", { recovery_stopped_at: "session_vacate", ...(steps.length ? { recovery_steps: steps } : {}) })
  }
  steps.push("session_vacate: the session moved to the registered main checkout and the verified landing released its occupancy rows")
  // 2. one abandon retry. The identity derives from the same idempotency key,
  // so the retry carries the event identity the refused first write never
  // committed; it either closes the attempt or reports why it still cannot.
  const retry = await abandonWorkerAttempt(lane, { work_id: abandonInput.work_id, attempt_id: abandonInput.attempt_id }, abandonInput.detail, {
    credentials: credentialsOverride ?? undefined,
    evidenceRunner: runner,
    abandonEventID: `worker-abandon-${token}`,
    abandonNonce: token,
  }, context.abort)
  // A recorded abandonment is itself an error envelope — the attempt is
  // closed, so its result is terminal — and marks that outcome with
  // retry_safe false. Any other error envelope is a genuine refusal of the
  // retry (transport, signing, or a durable gate that still refuses).
  if (!retry.error || retry.error.retry_safe !== false) {
    const retryMessage = retry.error?.message ?? "the abandon retry returned no diagnostic"
    // The vacate moved this session to the registered main checkout, so the
    // re-land runs before the stop: a refusal that ended at the main checkout
    // would strand the coordinator away from its claimed worktree.
    await reland()
    return adapterError("concord_work_transition", WORKER_ABANDON_OPERATION, requestID, "operation_conflict", "worker_abandon_refused", `${refusedMessage}; the own-row recovery released the occupancy row, but the abandon retry still refused: ${retryMessage}`, "possible", "reconcile_operation", { recovery_stopped_at: "worker_abandon_retry", ...(steps.length ? { recovery_steps: steps } : {}) })
  }
  steps.push("worker_abandon: the retry closed the dispatched attempt")
  // 3. the durable receipt, the same route the direct-accept path records.
  const receipt = await invokeConcordOperation("concord_work_transition", args, context)
  if (receipt.outcome !== "ok") {
    await reland()
    return adapterError("concord_work_transition", WORKER_ABANDON_OPERATION, requestID, "operation_conflict", "worker_abandon_receipt_failed", `the worker attempt was closed, but the durable replay receipt was not recorded: ${JSON.stringify(receipt.error ?? receipt)}`, "possible", "reconcile_operation", { recovery_stopped_at: "worker_abandon_receipt", ...(steps.length ? { recovery_steps: steps } : {}) })
  }
  dispatchWindows().releaseRetained(context.sessionID, abandonInput.attempt_id, abandonInput.lane_id)
  steps.push("worker_abandon: the durable replay receipt is recorded")
  // 4. work_start re-land: the session returns to the claimed worktree it
  // vacated, so the recovery leaves it where it started. A refusal here
  // leaves the closed attempt standing and rides the answer as a step.
  await reland()
  return { ...receipt, warnings: [...(Array.isArray(receipt.warnings) ? receipt.warnings : []), ...recoveryNotices()] } as HostConcordEnvelope
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
  // The verified landing is recorded in the core: one transaction confirms
  // the destination row is active and occupied by this session, clears this
  // session's occupancy on its other active rows of the same work item, and
  // appends one durable event. A landing the core refuses records nothing, so
  // every source row stays occupied — the conservative direction for the
  // removal gate — and replaying worktree_claim adopts the durable claim and
  // retries the move and the landing record.
  const input = args.input
  const workID = record(input) ? input.work_id : undefined
  if (typeof workID !== "string" || workID === "") {
    return adapterError("concord_work_transition", "worktree_claim", requestID, "malformed_response", "claim_landing_unattributable", "the claim's landing verified by readback carries no work id to record it under", "none", "retry_same_request")
  }
  try {
    await recordClaimLanding(workID, context.sessionID, path, context.abort)
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    return adapterError("concord_work_transition", "worktree_claim", requestID, "operation_conflict", "claim_landing_refused", `${message}; the verified landing is not recorded and the source occupancy stands, replay worktree_claim to retry the landing record`, "none", "retry_same_request")
  }
  // The landing is confirmed, so this session's active claimed worktree is
  // armed for the dispatch check.
  armClaimedWorktree(context.sessionID, path)
  if (!samePath(context.directory, path)) armTurnMoveBoundary(context.sessionID)
  // The confirmed move is a fact the agent must act on, so the result this
  // call encodes carries the notice: the new path, the paths-under-it rule,
  // and the surfaces the move made stale.
  recordMoveNotice(context.sessionID, moveNoticeText(path))
  return envelope
}

// recordClaimLanding runs the adapter-only claim-landing verb. The
// verb is not an agent tool operation, like host-lease: the agent names
// nothing, and the core refuses any landing its projection does not already
// hold true. host_pid names this OpenCode process, which holds the session;
// the core reads its start time from /proc and ends the occupancy when the
// process ends.
async function recordClaimLanding(workID: string, sessionRef: string, landedDirectory: string, signal: AbortSignal): Promise<void> {
  const result = await runner.run([concordBinaryPath(), "claim-landing"], JSON.stringify({ work_id: workID, session_ref: sessionRef, landed_directory: landedDirectory, host_pid: process.pid }), signal)
  if (result.exitCode !== 0) {
    throw new Error(`claim-landing failed with exit ${result.exitCode}: ${result.stderr.slice(0, 400)}`)
  }
}

// recordVacateLanding runs the adapter-only vacate-landing verb, mirroring
// claim-landing. The agent names nothing, and the core verifies the landed
// path against the committed relocation request before it releases the
// session's occupancy rows. The adapter calls it only after the host readback
// names the registered main checkout, so the landing the core records is
// evidence, and every refusal leaves occupancy standing for the CD-0096 D3
// removal gate.
async function recordVacateLanding(workID: string, sessionRef: string, landedDirectory: string, signal: AbortSignal): Promise<void> {
  const result = await runner.run([concordBinaryPath(), "vacate-landing"], JSON.stringify({ work_id: workID, session_ref: sessionRef, landed_directory: landedDirectory, host_pid: process.pid }), signal)
  if (result.exitCode !== 0) {
    throw new Error(`vacate-landing failed with exit ${result.exitCode}: ${result.stderr.slice(0, 400)}`)
  }
}

// moveSessionToRegisteredMainCheckout applies the core-derived vacate target.
// The agent can request the operation but cannot name or replace its destination.
export async function moveSessionToRegisteredMainCheckout(args: HostToolArgs, context: ToolContext, envelope: HostConcordEnvelope): Promise<HostConcordEnvelope> {
  if (args?.operation !== "session_vacate") return envelope
  const requestID = `${context.sessionID}-${context.messageID}`
  const input = args.input
  if (!record(input) || Object.keys(input).some((key) => key === "destination" || key === "destination_directory" || key === "path")) {
    // The composed routes strip a destination before the core call, so this
    // refusal is pre-commit. A caller that reaches it on an ok envelope
    // reports the recorded relocation request truthfully instead.
    const committed = record(envelope) && envelope.outcome === "ok"
    return adapterError("concord_work_transition", "session_vacate", requestID, "invalid_input", "agent_named_destination", "session_vacate derives the registered main checkout and refuses an agent-named destination", committed ? "possible" : "none", "correct_request")
  }
  if (!record(envelope) || envelope.outcome !== "ok") return envelope
  const result = envelope.result
  const destination = record(result) ? result.destination_directory : undefined
  if (typeof destination !== "string" || !destination.startsWith("/")) {
    return adapterError("concord_work_transition", "session_vacate", requestID, "malformed_response", "vacate_destination_unreadable", "core session_vacate response did not carry an absolute derived destination", "possible", "retry_same_request")
  }
  // The core committed the relocation request (a fresh request or a replay
  // resolution), so the adapter keeps its registered main checkout for this
  // session: a later session_vacate first moves the host session there and
  // resolves the core call from it, so the pending-request replay and the
  // readback-verified landing run from wherever the host session sits
  // (CD-0190 D3). The verified landing clears it with the claimed worktree.
  recordPendingVacateDestination(context.sessionID, destination)
  // The host readback, not the tool context, decides whether the move runs.
  // A stale tool context can name the registered main checkout while the
  // host session still sits in the source worktree, and a move skipped on
  // that name strands the standing request behind a mismatch no retry can
  // clear (CD-0190 D3). A readback that fails moves. A replay that resolves
  // a pending landing arrives with the readback already naming the
  // registered main checkout, so its move is skipped and the readback below
  // still verifies the landing before the vacate-landing verb records it.
  // The core vacate committed the relocation request, so every refusal from
  // here on reports a possible effect: the request stands and the occupancy
  // rows wait for the landing.
  let hostDirectory: string | null = null
  try { hostDirectory = await hostControlPlane().sessionDirectory(context.sessionID, context.abort) } catch { hostDirectory = null }
  let moved = false
  if (hostDirectory === null || !samePath(hostDirectory, destination)) {
    try {
      await hostControlPlane().moveSession(context.sessionID, destination, context.abort)
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      const kind = error instanceof MoveSessionUnavailable ? "unreachable" : "transport_failure"
      const reason = error instanceof MoveSessionUnavailable ? "move_session_route_unavailable" : "vacate_move_refused"
      const recovery = error instanceof MoveSessionUnavailable ? "contact_operator" : "retry_same_request"
      return adapterError("concord_work_transition", "session_vacate", requestID, kind, reason, `${message}; the committed relocation request stands and names ${JSON.stringify(destination)} as its registered main checkout`, "possible", recovery)
    }
    moved = true
  }
  let landed: string
  try {
    landed = await hostControlPlane().sessionDirectory(context.sessionID, context.abort)
  } catch (error) {
    return adapterError("concord_work_transition", "session_vacate", requestID, "malformed_response", "vacate_destination_unreadable", error instanceof Error ? error.message : String(error), "possible", "retry_same_request")
  }
  if (!samePath(landed, destination)) {
    // The relocation request stands, so the effect is possible. When the
    // adapter remembers this request's destination, a retry is reachable
    // from wherever the host session sits: it first moves the session to
    // the remembered destination and resolves the core call from there. A
    // retry without that memory (an adapter restart) resolves its Project
    // from the landed directory and refuses before the pending-request
    // replay can run, so only the remembered request offers the retry and
    // every other state reports contact_operator (CD-0190 D3).
    const reachable = pendingVacateDestination(context.sessionID) === destination
    return adapterError("concord_work_transition", "session_vacate", requestID, "session_directory_mismatch", "vacate_destination_mismatch", `the session landed in ${JSON.stringify(landed)} rather than the registered main checkout ${JSON.stringify(destination)}`, "possible", reachable ? "retry_same_request" : "contact_operator")
  }
  // The host readback names the registered main checkout, so the verified
  // landing records itself through the same adapter-only landing owner the
  // claim route uses. The core vacate committed only the relocation request
  // and left every occupancy row standing; the landing releases the
  // session's rows in one transaction. The landing verb commits before it
  // answers, so a failed call reports the possible effect and the replay
  // confirms or completes the record; a landing the core refuses records
  // nothing, so the source occupancy stands and the removal gate never sees
  // a live session's worktree as empty.
  const workID = record(result) ? result.work_id : undefined
  if (typeof workID !== "string" || workID === "") {
    return adapterError("concord_work_transition", "session_vacate", requestID, "malformed_response", "vacate_landing_unattributable", "the vacate's landing verified by readback carries no work id to record it under", "possible", "retry_same_request")
  }
  try {
    await recordVacateLanding(workID, context.sessionID, destination, context.abort)
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    // The vacate-landing verb commits before it answers, so a failed call
    // may have recorded the landing and released the rows. The refusal
    // never asserts the landing is not recorded: it reports the possible
    // effect and the replay that confirms or completes the record
    // (CD-0190 D3).
    return adapterError("concord_work_transition", "session_vacate", requestID, "operation_conflict", "vacate_landing_refused", `${message}; the landing may be recorded because the verb commits before it answers, so replay session_vacate from the verified destination to confirm or complete the landing record`, "possible", "retry_same_request")
  }
  // The verified landing released the occupancy rows, so no claimed worktree
  // is armed for this session any more.
  clearClaimedWorktree(context.sessionID)
  // A move the host readback decided completed in this turn even when a
  // stale tool context already names the destination, so the move fact, not
  // the tool context, arms the boundary.
  if (moved || !samePath(context.directory, destination)) armTurnMoveBoundary(context.sessionID)
  // The verified landing moved the session to the registered main checkout,
  // so the result this call encodes carries the move notice.
  recordMoveNotice(context.sessionID, moveNoticeText(destination))
  return envelope
}

// The removal verbs share the worktree-removal report path: an ok removal
// queues the notice that tells a neighbouring session the directory is gone.
export const WORKTREE_REMOVAL_OPERATIONS = new Set(["worktree_reclaim", "worktree_destroy", "worktree_audit_reclaim"])

async function executeWorkTransition(args: HostToolArgs, context: ToolContext, staleness: ReleaseStaleness | null): Promise<HostConcordEnvelope> {
  if (args?.operation === WORKER_ABANDON_OPERATION) return executeWorkerAbandon(args, context)
  if (WORKTREE_REMOVAL_OPERATIONS.has(args?.operation)) {
    const envelope = await invokeConcordOperation("concord_work_transition", args, context)
    await reportWorktreeRemoval(args, context, envelope)
    return envelope
  }
  if (args?.operation === "workflow_action") {
    // CD-0191: the stale-release dispatch gate. A dispatch_worker action from
    // a session whose pinned release differs from the installed release
    // refuses here, before any core call, so no worker attempt opens on lane
    // text the install replaced. Every other action keeps working. The
    // refusal rides the dispatch surface's adapter-gate envelope
    // (unauthorized_dispatch, boundary release_stale, refusal_kind
    // stale_context in the details): TS7 pins adapter-origin tool-envelope
    // error kinds to the transport set, and this envelope is the shape
    // dispatch refusals already return.
    const staleDispatch = record(args?.input) && args.input.action_id === "dispatch_worker" ? staleness : null
    if (staleDispatch) {
      const input = args.input
      const fields = record(input.fields) ? input.fields : {}
      const partial: { work_id?: string; lane_id?: string } = {}
      if (typeof input.work_id === "string") partial.work_id = input.work_id
      if (typeof fields.lane_id === "string") partial.lane_id = fields.lane_id
      return errorEnvelopeForLane(null, partial, "error", "unauthorized_dispatch", staleReleaseDispatchRefusal(staleDispatch), "contact_operator", {
        details: { boundary: "release_stale", refusal_kind: "stale_context", pinned_release: staleDispatch.pinnedRelease, installed_release: staleDispatch.installedRelease, remedy: STALE_RELEASE_REMEDY },
      })
    }
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
    const requestID = `${context.sessionID}-${context.messageID}`
    const remembered = pendingVacateDestination(context.sessionID)
    // The host's own readback names where the session sits now. When that
    // directory is the claimed worktree a confirmed or refused move armed,
    // the session genuinely occupies claimed work: the state-driven replay
    // must hear the call from there and refuse with the later-claim
    // recovery, so the host never leaves claimed work for a request that
    // cannot complete (CD-0190 D3). Every other state — a stale tool
    // context, a post-commit refusal that left the session outside its
    // claimed worktree — keeps the remembered-destination pre-move.
    let hostDirectory: string | null = null
    try { hostDirectory = await hostControlPlane().sessionDirectory(context.sessionID, context.abort) } catch { hostDirectory = null }
    const claimed = armedClaimedWorktree(context.sessionID) ?? unlandedClaimedWorktree(context.sessionID)
    const occupiesClaimedWork = claimed !== null && hostDirectory !== null && samePath(claimed, hostDirectory)
    // The host readback, not the tool context, decides the pre-move — the
    // same authority the in-route move applies. A stale tool context can
    // name the remembered destination while the host session still sits in
    // a directory that resolves to no Project, and a move skipped on that
    // name strands the standing request behind the context-resolution
    // refusal every retry repeats (CD-0190 D3). A failed readback moves.
    const atRemembered = typeof remembered === "string" && hostDirectory !== null && samePath(hostDirectory, remembered)
    const movedToRemembered = typeof remembered === "string" && !occupiesClaimedWork && !atRemembered
    if (movedToRemembered && typeof remembered === "string") {
      // A post-commit refusal left this session's relocation request
      // standing and this adapter remembers its registered main checkout.
      // The retry moves the host session there first and resolves the core
      // call from it, so the pending-request replay runs from wherever the
      // host session sits; from a directory that resolves to no Project the
      // core call would refuse before the replay could run (CD-0190 D3).
      try {
        await hostControlPlane().moveSession(context.sessionID, remembered, context.abort)
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error)
        const kind = error instanceof MoveSessionUnavailable ? "unreachable" : "transport_failure"
        const reason = error instanceof MoveSessionUnavailable ? "move_session_route_unavailable" : "vacate_move_refused"
        const recovery = error instanceof MoveSessionUnavailable ? "contact_operator" : "retry_same_request"
        return adapterError("concord_work_transition", "session_vacate", requestID, kind, reason, `${message}; the committed relocation request stands and names ${JSON.stringify(remembered)} as its registered main checkout, so the occupancy rows wait for the verified landing there`, "possible", recovery)
      }
    }
    if ((movedToRemembered || atRemembered) && typeof remembered === "string") {
      // The host session sits at the remembered destination, so the stale
      // tool context must not name anything else.
      context.directory = remembered
    }
    const envelope = await invokeConcordOperation("concord_work_transition", args, context)
    const settled = await moveSessionToRegisteredMainCheckout(args, context, envelope)
    // The pre-move relocated the session during this turn, so the turn move
    // boundary arms exactly as it does for the in-route move once the
    // verified landing confirms the destination.
    if (movedToRemembered && settled.outcome === "ok") armTurnMoveBoundary(context.sessionID)
    return settled
  }
  if (args?.operation === "worktree_claim") {
    // The claimed worktree's occupancy row records the recording host's
    // process identity from creation (CD-0179). The pid belongs to this
    // process, not to the agent, so the adapter injects it and overwrites
    // anything an agent named.
    args = { ...args, input: { ...(record(args.input) ? args.input : {}), host_pid: process.pid } }
  }
  const envelope = await invokeConcordOperation("concord_work_transition", args, context)
  const settled = await vacateTerminalWorktree("concord_work_transition", args, context, envelope)
  const landed = await moveSessionToClaimedWorktree(args, context, settled)
  return landed
}

// vacateTerminalWorktree moves a session out of a work item's worktree after
// the work reached a terminal state (complete, cancel, supersede). The core
// attaches vacate_target to the transition result only when the calling
// session's linked worktree is that work item's active worktree, so the hook
// never releases another work item's occupancy. The vacate runs first — it
// records the relocation request and resolves the registered main checkout —
// the move follows it, and the verified landing the move readback earns
// releases the session's occupancy rows. A failed vacate or move queues a
// notice and keeps the transition result: the transition is durable, the
// occupancy rows stand, and the release is replayable by an explicit
// session_vacate. A vacate that committed the relocation request before the
// failure reports an effect_state possible in its notice and names the
// verified-destination replay as the recovery, because the session may
// already sit at the registered main checkout where a worktree retry cannot
// reach it; a vacate refused before the commit keeps the worktree retry.
export async function vacateTerminalWorktree(toolName: string, args: HostToolArgs, context: ToolContext, envelope: HostConcordEnvelope): Promise<HostConcordEnvelope> {
  const transitioned = args?.operation === "lifecycle" && record(args.input) && ["completed", "cancelled"].includes(String(args.input.target))
  const superseded = toolName === "concord_work_relate" && args?.operation === "supersede"
  if (!transitioned && !superseded) return envelope
  if (!record(envelope) || envelope.outcome !== "ok") return envelope
  const target = record(envelope.result) ? envelope.result.vacate_target : undefined
  if (!record(target) || typeof target.work_id !== "string" || typeof target.destination_directory !== "string") return envelope
  const requestID = `${context.sessionID}-${context.messageID}`
  const vacateArgs: HostToolArgs = { operation: "session_vacate", input: { idempotency_key: `${requestID}-terminal-vacate` } }
  let committed = false
  try {
    const vacated = await invokeConcordOperation("concord_work_transition", vacateArgs, context)
    committed = Boolean(record(vacated) && vacated.outcome === "ok")
    const moved = await moveSessionToRegisteredMainCheckout(vacateArgs, context, vacated)
    const failure = record(moved) && record(moved.error) ? moved.error : undefined
    const reason = failure && typeof failure.kind === "string" && typeof failure.message === "string"
      ? `${failure.kind}: ${failure.message}`
      : "the vacate move did not confirm a landing"
    if (!record(moved) || moved.outcome !== "ok") throw new Error(reason)
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    if (committed) {
      // The relocation request stands recorded (CD-0190 D3), so the effect
      // is possible and the session may already sit at the registered main
      // checkout: the move landed but the landing record failed, or the
      // move itself was refused. The recovery is plain session_vacate
      // again: the adapter remembers the committed destination, so the
      // retry first moves the host session there and replays the pending
      // request wherever the session sits; without the remembered
      // destination a retry from the worktree commits a new request and
      // retries the move.
      enqueueWorkNotice(context.sessionID, `Concord finished ${target.work_id}, and its relocation request stands recorded with the occupancy rows waiting on the verified landing (effect_state possible): ${message} Run session_vacate again — from the verified destination ${target.destination_directory} when the session sits there, otherwise from the worktree — to confirm or complete the verified landing and release the rows.`)
      return envelope
    }
    enqueueWorkNotice(context.sessionID, `Concord finished ${target.work_id}, but the session could not return to the registered main checkout: ${message} Run session_vacate from the worktree to retry the move.`)
  }
  return envelope
}
