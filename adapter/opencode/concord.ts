import { createHash, sign as signBytes } from "node:crypto"
import { b64, clientRef, privateKeyObject, randomNonce, SecretToolCredentialStore, type CredentialStore } from "./credentials"
import { contractOperations, hostToolDescriptions, hostToolSchemas, maxEnvelopeBytes, payloadSchemas, workflowActionPublicVariants } from "./generated-contracts"
import { activeManifestDigest, adoptManifestDigest, resolveDiskManifestDigest } from "./manifest-pin"
import { validateGeneratedEnvelope, validateGeneratedPayload, envelopeFailurePath, payloadFailurePath, payloadFailureMessage } from "./generated-contract-tests"
import { dispatchLaneWorker, dispatchRetriedLaneWorker, type LaneDispatchInput } from "./lane_dispatch"
import { AdapterContractDefect } from "./adapter-contract-defect"
// The typed fail-closed error re-exports so callers of this module can catch
// it by class without importing the shared owner.
export { AdapterContractDefect }
import { abandonWorkerAttempt, hostAbandonmentObservation, hostSessionReader, withWorkerReadbackScope, type WorkerRecoveryContext } from "./dispatch"
import { reconcileRetainedWorker } from "./worker_recovery"
import { dispatchWindows, staleReleaseDispatchRefusal, dispatchDirectoryMismatch } from "./dispatch-window"
import { agentLanes, type AgentLane } from "./generated-agent-lanes"
import { hostControlPlane, MoveSessionUnavailable } from "./move-session"
import { createRunSessionObservation, errorEnvelopeForLane, MAX_OUTPUT_BYTES, observeRunSessionLine, validateAgainstSchema, type AgentResultEnvelope, type RunLineMetadata, type RunSessionObservation } from "./dispatch"
import { concordBinaryPath, CoreBinaryUnavailable } from "./dispatch"
import { createWorkStateReporter } from "./workflow-status"
import { hostLeaseFault, releaseStaleness, type ReleaseStaleness } from "./host-lease"
import { armTurnMoveBoundary, dispatchRequiresNextTurn } from "./turn-move-boundary"
import { armedClaimedWorktree, armClaimedWorktree, clearClaimedWorktree, pendingVacateDestination, recordPendingVacateDestination, recordUnlandedClaimedWorktree, samePath, unlandedClaimedWorktree } from "./claimed-worktree"
import { ensureConductLink } from "./project-link"
import { queueWorkStartContinuation, clearWorkStartContinuation, resetWorkStartContinuations } from "./work-start-continuation"
import { moveNoticeText, recordMoveNotice, takeMoveNotice } from "./move-notice"
import type { WorkerReadingRequest } from "./worker_source"
import type { WorkerDiffRequest } from "./worker_diff"
import { canonicalWorkerControl, type WorkerCheckContext, type WorkerControlRequest } from "./worker_control"
import { mutationResult, reconstructMutationResult } from "./packet"

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
const requestToolNames = new WeakMap<object, string>()
function tool<T extends { args: object; execute: (args: any, context: ToolContext) => Promise<ToolResult> }>(definition: T): T {
  const toolName = requestToolNames.get(definition.args)
  if (!toolName) return definition
  const execute = definition.execute
  return { ...definition, execute: async (args: any, context: ToolContext): Promise<ToolResult> => {
    const request: unknown = args?.request
    if (request === null || typeof request !== "object" || Array.isArray(request)) {
      const operation = typeof args?.operation === "string" && contractOperations.some((candidate) => candidate.tool === toolName && candidate.id === `${toolName}.${args.operation}`) ? args.operation : ""
      const requestID = `${context.sessionID}-${context.messageID}`
      const envelope = adapterError(toolName, operation, requestID, "invalid_request_wrapper", "Missing or invalid request wrapper: submit {request: {operation, input}}.")
      const { envelope: settled, extraWarnings } = withReleaseStaleness(envelope, releaseStaleness())
      return appendWarnings(encodeHostResult(toolName, operation, requestID, settled), extraWarnings)
    }
    return execute(args, context)
  } }
}

const MAX_STDERR = 8192
// cmd/concord's inputRefusalExit names a refusal before dispatch, not a
// generic process failure. A previous mutation call can still have an effect.
const CLI_INPUT_REFUSAL_EXIT = 64
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
    signal.throwIfAborted()
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
      signal.throwIfAborted()
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
  if (overrides.reset) { runner = defaultRunner; credentialsOverride = null; resetWorkStartContinuations() }
  if (overrides.runner) runner = overrides.runner
  if (overrides.credentials) credentialsOverride = overrides.credentials
}

function schemaName(ref: string): string {
  const prefix = ref.startsWith("#/$defs/") ? "#/$defs/" : ref.startsWith("#/schemas/") ? "#/schemas/" : ""
  const name = prefix ? ref.slice(prefix.length) : ""
  if (!name || !Object.hasOwn(payloadSchemas, name)) throw new Error(`unknown payload schema reference ${ref}`)
  return name
}

// Resolve authored references before projection. Required sets, selectors,
// bounds, exclusions, and annotations remain constraints of the published view.
function publishClosedSchema(value: unknown, resolving = new Set<string>()): JSONSchema {
  if (Array.isArray(value) || typeof value !== "object" || value === null) return {}
  const schema = value as JSONSchema
  if (typeof schema.$ref === "string") {
    const name = schemaName(schema.$ref)
    if (resolving.has(name)) return {}
    const next = new Set(resolving)
    next.add(name)
    const resolved = publishClosedSchema((payloadSchemas as Record<string, unknown>)[name], next)
    // Sibling keywords alongside a $ref apply in the authored contract, so
    // publication merges them into the resolved target instead of dropping
    // any: required sets union, properties overlay, annotations win. A
    // dropped structural sibling would silently loosen the published bound
    // (CON-412).
    const siblings = Object.fromEntries(Object.entries(schema).filter(([key]) => key !== "$ref" && !(key === "examples" && typeof schema.description === "string" && schema.description.includes("Accepted shape:"))))
    if (Object.keys(siblings).length === 0) return resolved
    const merged: Record<string, unknown> = { ...resolved }
    for (const [key, value] of Object.entries(siblings)) {
      if (key === "required" && Array.isArray(value) && Array.isArray(resolved.required)) {
        merged.required = [...new Set([...(resolved.required as unknown[]), ...value])]
      } else if (key === "properties" && typeof value === "object" && value !== null && !Array.isArray(value) && typeof resolved.properties === "object" && resolved.properties !== null) {
        merged.properties = { ...(resolved.properties as Record<string, unknown>), ...(value as Record<string, unknown>) }
      } else {
        merged[key] = value
      }
    }
    return merged as JSONSchema
  }
  const result: Record<string, unknown> = {}
  for (const [key, child] of Object.entries(schema)) {
    if (key === "$defs" || key === "definitions") continue
    // The generated description already carries this complete illustrative value.
    // Keep the canonical, validated examples annotation without publishing it twice.
    if (key === "examples" && typeof schema.description === "string" && schema.description.includes("Accepted shape:")) continue
    if (key === "properties" && typeof child === "object" && child !== null && !Array.isArray(child)) {
      result[key] = Object.fromEntries(Object.entries(child as Record<string, unknown>).map(([name, property]) => [name, publishClosedSchema(property, resolving)]))
    } else if (key === "items") {
      result[key] = Array.isArray(child) ? child.map((item) => publishClosedSchema(item, resolving)) : publishClosedSchema(child, resolving)
    } else if (["allOf", "anyOf", "oneOf"].includes(key) && Array.isArray(child)) {
      result[key] = child.map((member) => publishSchemaAlternative(member, resolving))
    } else if (["if", "then", "else", "not", "additionalProperties", "patternProperties", "propertyNames", "contains"].includes(key) && typeof child === "object" && child !== null) {
      result[key] = publishClosedSchema(child, resolving)
    } else {
      result[key] = child
    }
  }
  return result as JSONSchema
}

// An alternative container has no field of its own. Its property descriptions
// already teach every authored member; repeating a full request example on each
// container spends the provider's bounded input budget without adding a field.
function publishSchemaAlternative(value: unknown, resolving = new Set<string>()): JSONSchema {
  const schema = publishClosedSchema(value, resolving)
  if (schema.type === "object" && typeof schema.description === "string") {
    const marker = schema.description.indexOf("Accepted shape:")
    if (marker >= 0) {
      const original = schema.description.slice(0, marker).trimEnd()
      // The container's example duplicates its properties, but its own
      // cardinality or numeric constraints still need first-call teaching.
      const bounds = schema.description.indexOf("Bounds:", marker)
      const end = schema.description.indexOf("; example:", bounds)
      const teaching = bounds >= 0 ? schema.description.slice(bounds, end >= 0 ? end : undefined) : ""
      if (original || teaching) schema.description = [original, teaching].filter(Boolean).join(" ")
      else delete schema.description
    }
  }
  return schema
}

// Only schema-valued positions are traversed. Property maps and annotation
// objects cannot become references: their keys are not schema keywords.
function mapPublicationChildren(schema: JSONSchema, visit: (child: JSONSchema) => JSONSchema): JSONSchema {
  const result = { ...schema }
  for (const key of ["properties", "patternProperties"]) {
    const value = schema[key]
    if (value && typeof value === "object" && !Array.isArray(value)) {
      result[key] = Object.fromEntries(Object.entries(value).map(([name, child]) => [name, visit(child as JSONSchema)]))
    }
  }
  for (const key of ["items", "if", "then", "else", "not", "additionalProperties", "propertyNames", "contains"]) {
    const value = schema[key]
    if (value && typeof value === "object") result[key] = Array.isArray(value) ? value.map((child) => visit(child as JSONSchema)) : visit(value as JSONSchema)
  }
  for (const key of ["allOf", "anyOf", "oneOf", "prefixItems"]) {
    if (Array.isArray(schema[key])) result[key] = (schema[key] as JSONSchema[]).map(visit)
  }
  return result
}

// A containing object and a selected oneOf member both apply. Identical
// properties and common requireds therefore need only one constraint owner.
// Closed children still declare shared property names with empty schemas,
// because additionalProperties does not inherit names from its parent.
function factorPublication(schema: JSONSchema): JSONSchema {
  const node = mapPublicationChildren(schema, factorPublication)
  const branches = node.oneOf as JSONSchema[] | undefined
  if (!Array.isArray(branches) || branches.length < 2 || !branches.every((branch) => branch.type === "object" && branch.properties)) return node
  const properties = node.properties as Record<string, JSONSchema> | undefined
  const shared: Record<string, JSONSchema> = {}
  const firstProperties = branches[0].properties as Record<string, JSONSchema>
  for (const [key, value] of Object.entries(firstProperties)) {
    if (branches.every((branch) => JSON.stringify((branch.properties as Record<string, JSONSchema>)[key]) === JSON.stringify(value)) &&
        (!properties?.[key] || JSON.stringify(properties[key]) === "{}" || JSON.stringify(properties[key]) === JSON.stringify(value))) shared[key] = value
  }
  const commonRequired = ((branches[0].required as string[] | undefined) ?? []).filter((key) => branches.every((branch) => (branch.required as string[] | undefined)?.includes(key)))
  const required = [...new Set([...((node.required as string[] | undefined) ?? []), ...commonRequired])]
  const parent: JSONSchema = { ...node, type: "object", properties: { ...properties, ...shared }, ...(required.length ? { required } : {}) }
  parent.oneOf = branches.map((branch) => {
    const child: JSONSchema = { ...branch, properties: { ...(branch.properties as Record<string, JSONSchema>), ...Object.fromEntries(Object.keys(shared).map((key) => [key, {}])) } }
    delete child.type
    if (Array.isArray(child.required)) {
      const remaining = (child.required as string[]).filter((key) => !required.includes(key))
      if (remaining.length) child.required = remaining
      else delete child.required
    }
    if (parent.additionalProperties === false && JSON.stringify(Object.keys(parent.properties as object).sort()) === JSON.stringify(Object.keys(child.properties as object).sort())) delete child.additionalProperties
    return child
  })
  return parent
}

// Each publication owns its definition table. Only identical repeated schema
// nodes large enough to outweigh a local pointer/table entry are interned.
// Children are interned first, so definition dependencies form an acyclic DAG.
// Interning then re-measures the finished graph: an interned ancestor absorbs
// duplicate occurrences of a child, so a child definition can end up
// referenced fewer times than the expanded tree counted. Definitions whose
// final references no longer outweigh their table entry are inlined back,
// one candidate per pass in table order, to a fixpoint.
function internPublication(schema: JSONSchema): JSONSchema {
  const counts = new Map<string, number>()
  const count = (node: JSONSchema): JSONSchema => {
    const key = JSON.stringify(node)
    counts.set(key, (counts.get(key) ?? 0) + 1)
    mapPublicationChildren(node, count)
    return node
  }
  count(schema)
  const names = new Map<string, string>()
  const definitions: Record<string, JSONSchema> = {}
  const intern = (node: JSONSchema): JSONSchema => {
    const key = JSON.stringify(node)
    if (names.has(key)) return { $ref: `#/$defs/${names.get(key)}` }
    const body = mapPublicationChildren(node, intern)
    // Provider validators require reference targets to carry a concrete
    // schema, not a bare combinator. Keep those unions inline.
    if (Object.keys(body).every((keyword) => ["oneOf", "anyOf", "allOf"].includes(keyword))) return body
    const name = `d${names.size}`
    const pointer = { $ref: `#/$defs/${name}` }
    const occurrences = counts.get(key) ?? 0
    const bytes = Buffer.byteLength(JSON.stringify(body), "utf8")
    const pointerBytes = Buffer.byteLength(JSON.stringify(pointer), "utf8")
    const entryBytes = Buffer.byteLength(JSON.stringify(name), "utf8") + 2
    if (occurrences * bytes <= bytes + occurrences * pointerBytes + entryBytes) return body
    names.set(key, name)
    definitions[name] = body
    return pointer
  }
  let result = intern(schema)
  const referenceCounts = (): Map<string, number> => {
    const refs = new Map<string, number>()
    const tally = (node: JSONSchema): JSONSchema => {
      if (typeof node.$ref === "string") refs.set(node.$ref, (refs.get(node.$ref) ?? 0) + 1)
      mapPublicationChildren(node, tally)
      return node
    }
    tally(result)
    for (const body of Object.values(definitions)) tally(body)
    return refs
  }
  for (;;) {
    const refs = referenceCounts()
    const inlined = Object.keys(definitions).find((name) => {
      const body = definitions[name]
      const occurrences = refs.get(`#/$defs/${name}`) ?? 0
      const bytes = Buffer.byteLength(JSON.stringify(body), "utf8")
      const pointerBytes = Buffer.byteLength(JSON.stringify({ $ref: `#/$defs/${name}` }), "utf8")
      const entryBytes = Buffer.byteLength(JSON.stringify(name), "utf8") + 2
      return occurrences * bytes <= bytes + occurrences * pointerBytes + entryBytes
    })
    if (inlined === undefined) break
    const ref = `#/$defs/${inlined}`
    const body = definitions[inlined]
    // Each substitution site gets its own copy: published documents must stay
    // structurally alias-free because host hooks may mutate what they receive.
    const substitute = (node: JSONSchema): JSONSchema => {
      if (Object.keys(node).length === 1 && node.$ref === ref) return JSON.parse(JSON.stringify(body)) as JSONSchema
      return mapPublicationChildren(node, substitute)
    }
    result = substitute(result)
    for (const [name, definition] of Object.entries(definitions)) {
      if (name !== inlined) definitions[name] = substitute(definition)
    }
    delete definitions[inlined]
  }
  return Object.keys(definitions).length ? { ...result, $defs: definitions } : result
}

export function publishedRequestSchema(toolName: string): JSONSchema {
  const operations = contractOperations.filter((operation: any) => operation.tool === toolName)
  if (operations.length === 0) throw new Error(`tool ${toolName} has no generated operations`)
  const branchFor = (operationName: string, input: JSONSchema): JSONSchema => ({
    type: "object",
    additionalProperties: false,
    required: ["operation", "input"],
    properties: {
      operation: { type: "string", const: operationName },
      input,
    },
  })
  const branches = operations.map((operation: any) => {
    const operationName = operation.id.slice(operation.id.indexOf(".") + 1)
    if (operation.id !== "concord_work_transition.workflow_action") return branchFor(operationName, publishClosedSchema((payloadSchemas as Record<string, unknown>)[schemaName(operation.input_schema)]))
    const variants = workflowActionPublicVariants.map((variant) => {
      const input = publishSchemaAlternative((payloadSchemas as Record<string, unknown>)[variant.schema])
      return { ...input, description: [input.description, `workflow_action action ${variant.action_id}: closed action-discriminated variant`].filter(Boolean).join("\n") }
    })
    return branchFor(operationName, { type: "object", oneOf: variants })
  })
  return internPublication(factorPublication({
    type: "object",
    additionalProperties: false,
    required: ["operation", "input"],
    properties: {
      operation: { type: "string", enum: operations.map((operation: any) => operation.id.slice(operation.id.indexOf(".") + 1)) },
      input: { type: "object" },
    },
    oneOf: branches,
  }))
}

// OpenCode wraps plain argument entries in properties. JSON pointers resolve
// against that complete argument root, not the nested request document, so the
// definition hook moves its table to the root without changing runtime args.
export async function publishRequestDefinition(
  input: { toolID: string },
  output: { description: string; parameters: unknown; jsonSchema?: unknown },
): Promise<void> {
  if (!contractOperations.some((operation) => operation.tool === input.toolID)) return
  const { $defs, ...request } = publishedRequestSchema(input.toolID)
  output.jsonSchema = { type: "object", properties: { request }, required: ["request"], ...($defs ? { $defs } : {}) }
}

function argsSchema(toolName: string): any {
  const args = { request: publishedRequestSchema(toolName) }
  requestToolNames.set(args, toolName)
  return args
}

// The registration map exposes every declared field. The definition hook
// publishes an object with conditional capture/resume branches, so providers
// that reject root combinators still receive each mode's closed constraints.
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
  const [capture, resume] = hostToolSchemas.concord_work_start.oneOf
  // Anthropic rejects root oneOf/anyOf/allOf, and host publication admits no
  // bare anyOf. Presence of either selector chooses the same closed resume
  // branch as validateWorkStartArgs through nested conditionals.
  // Parameters remain unchanged: publication cannot alter runtime admission.
  // The merged root properties repeat each branch's field constraints, so the
  // published document interns like the request tools instead of carrying
  // every field schema twice.
  output.jsonSchema = internPublication(JSON.parse(JSON.stringify({
    type: "object",
    properties: { ...capture.properties, ...resume.properties },
    additionalProperties: false,
    if: { required: ["work_id"] },
    then: resume,
    else: { if: { required: ["issue_key"] }, then: resume, else: capture },
  })))
}

function baseEnvelope(toolName: string, operation: string, requestID: string) {
  const queryID = (contractOperations.find((candidate: any) => candidate.tool === toolName && candidate.id === `${toolName}.${operation}`) as any)?.query_id
  return { schema_version: "1.0", manifest_digest: activeManifestDigest(), request_id: requestID, origin: "adapter", tool: toolName, operation, ...(queryID ? { query_id: queryID } : {}), outcome: "error", resolved_scope: null, authority: "unreachable", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false }
}

// Producers select a closed tuple key; the generated envelope schema validates
// the tuple in its operation context. Retry safety is derived from the tuple.
export const adapterErrorTuples = {
  missing_binary: { kind: "transport_failure", adapter_reason: "missing_binary", effect_state: "none", recovery: "contact_operator" },
  spawn_failure: { kind: "transport_failure", adapter_reason: "spawn_failure", effect_state: "none", recovery: "contact_operator" },
  io_failure_retry: { kind: "transport_failure", adapter_reason: "io_failure", effect_state: "none", recovery: "retry_same_request" },
  io_failure_operator: { kind: "transport_failure", adapter_reason: "io_failure", effect_state: "none", recovery: "contact_operator" },
  malformed_core_response_retry: { kind: "malformed_response", adapter_reason: "malformed_core_response", effect_state: "none", recovery: "retry_same_request" },
  malformed_core_response_operator: { kind: "malformed_response", adapter_reason: "malformed_core_response", effect_state: "none", recovery: "contact_operator" },
  timeout_no_effect: { kind: "timeout", adapter_reason: "timeout_no_effect", effect_state: "none", recovery: "retry_same_request" },
  cancelled_no_effect: { kind: "cancelled", adapter_reason: "cancelled_no_effect", effect_state: "none", recovery: "retry_same_request" },
  manifest_mismatch: { kind: "transport_failure", adapter_reason: "manifest_mismatch", effect_state: "none", recovery: "contact_operator" },
  context_resolution_failed: { kind: "transport_failure", adapter_reason: "context_resolution_failed", effect_state: "none", recovery: "contact_operator" },
  host_lease_missing: { kind: "transport_failure", adapter_reason: "host_lease_missing", effect_state: "none", recovery: "contact_operator" },
  unknown_effect: { kind: "operation_conflict", adapter_reason: "unknown_effect", effect_state: "possible", recovery: "reconcile_operation" },
  session_directory_unreadable: { kind: "transport_failure", adapter_reason: "session_directory_unreadable", effect_state: "none", recovery: "retry_same_request" },
  worker_abandon_refused_none: { kind: "operation_conflict", adapter_reason: "worker_abandon_refused", effect_state: "none", recovery: "reconcile_operation" },
  worker_abandon_refused_possible: { kind: "operation_conflict", adapter_reason: "worker_abandon_refused", effect_state: "possible", recovery: "reconcile_operation" },
  worker_abandon_receipt_failed: { kind: "operation_conflict", adapter_reason: "worker_abandon_receipt_failed", effect_state: "possible", recovery: "reconcile_operation" },
  worker_reconcile_failed_recorded: { kind: "operation_conflict", adapter_reason: "worker_reconcile_failed_recorded", effect_state: "possible", recovery: "reconcile_operation" },
  lane_tool_refusal: { kind: "unauthorized", adapter_reason: "lane_tool_refusal", effect_state: "none", recovery: "contact_operator" },
  invalid_cli_input: { kind: "invalid_input", adapter_reason: "invalid_cli_input", effect_state: "none", recovery: "correct_request" },
  invalid_request_wrapper: { kind: "invalid_input", adapter_reason: "invalid_request_wrapper", effect_state: "none", recovery: "correct_request" },
  invalid_tool_input: { kind: "invalid_input", adapter_reason: "invalid_tool_input", effect_state: "none", recovery: "correct_request" },
  vacate_replay: { kind: "operation_conflict", adapter_reason: "unknown_effect", effect_state: "possible", recovery: "retry_same_request" },
  vacate_unavailable: { kind: "operation_conflict", adapter_reason: "unknown_effect", effect_state: "possible", recovery: "contact_operator" },
} as const

export type AdapterErrorKey = keyof typeof adapterErrorTuples
export type GenericAdapterErrorKey = Exclude<AdapterErrorKey, "vacate_replay" | "vacate_unavailable">
export type VacateAdapterErrorKey = Extract<AdapterErrorKey, "vacate_replay" | "vacate_unavailable">

type AdapterErrorTuple = (typeof adapterErrorTuples)[AdapterErrorKey]

// The typed shape the factories return: the closed base members plus the
// error member the tuple drives, so producers and tests read the tuple
// members without casts.
export type AdapterErrorEnvelope = {
  schema_version: "1.0"
  manifest_digest: string
  request_id: string
  origin: "adapter"
  tool: string
  operation: string
  query_id?: string
  outcome: "error"
  resolved_scope: null
  authority: "unreachable"
  freshness: null
  source_version_watermark: []
  ordering_keys: []
  next_cursor: null
  omissions: []
  warnings: []
  evidence_refs: []
  replayed: false
  error: {
    kind: AdapterErrorTuple["kind"]
    retry_safe: boolean
    recovery_action: { kind: AdapterErrorTuple["recovery"] }
    effect_state: AdapterErrorTuple["effect_state"]
    adapter_reason: AdapterErrorTuple["adapter_reason"]
    message: string
    details?: Record<string, unknown>
  }
}

// The vacate entries are reachable only through vacateError (CD-0190 D3):
// the generic factory refuses them under every pair, and vacateError refuses
// every generic key.
const VACATE_TABLE_KEYS: readonly string[] = ["vacate_replay", "vacate_unavailable"]

// R1.2: retry_safe is derived in this one place — false for the input
// entries and for possible effects, true for the other no-effect entries.
function adapterRetrySafe(tuple: AdapterErrorTuple): boolean {
  return tuple.kind !== "invalid_input" && tuple.effect_state !== "possible"
}

// The schema bounds the human diagnostic at 1000 code points; the bound here
// is bytes, which never yields more code points than the schema admits.
const ADAPTER_ERROR_MESSAGE_MAX_BYTES = 1000

// adapterContractDefect names the failing identity and member. For a
// mutation it states that the effect is unknown and must be reconciled
// before any retry, because a rejected envelope must never be read as a
// no-effect answer.
function adapterContractDefect(toolName: string, operation: string, requestID: string, detail: string): AdapterContractDefect {
  const duty = operationIsMutation(toolName, operation) ? "; the mutation effect is unknown: reconcile the operation before any retry" : ""
  return new AdapterContractDefect(`${toolName}.${operation} request ${requestID} failed the adapter envelope contract at ${detail}${duty}`)
}

function tupleForKey(key: AdapterErrorKey, toolName: string, operation: string, requestID: string): AdapterErrorTuple {
  const tuple = (adapterErrorTuples as Record<string, AdapterErrorTuple | undefined>)[key]
  if (!Object.hasOwn(adapterErrorTuples, key) || !tuple || VACATE_TABLE_KEYS.includes(key)) {
    throw adapterContractDefect(toolName, operation, requestID, `unknown adapter error key ${JSON.stringify(String(key))}: the admitted tuples are the adapterErrorTuples keys`)
  }
  return tuple
}

// adapterError is the sole generic adapter-origin TS7 constructor. It reads
// kind, reason, effect, and recovery only from the table, derives retry_safe
// from the tuple, bounds the diagnostic to the schema maximum, and fails
// closed on an unknown key, an undeclared tool/operation pair, or any
// envelope the generated contract refuses.
export function adapterError(toolName: string, operation: string, requestID: string, key: GenericAdapterErrorKey, message: string, details?: Record<string, unknown>): AdapterErrorEnvelope {
  const tuple = tupleForKey(key, toolName, operation, requestID)
  const envelope = { ...baseEnvelope(toolName, operation, requestID), error: { kind: tuple.kind, retry_safe: adapterRetrySafe(tuple), recovery_action: { kind: tuple.recovery }, effect_state: tuple.effect_state, adapter_reason: tuple.adapter_reason, message: boundedUTF8(message, ADAPTER_ERROR_MESSAGE_MAX_BYTES), ...(details ? { details } : {}) } }
  return validateAdapterEnvelope(envelope as AdapterErrorEnvelope)
}

// vacateError is the sole constructor for the two vacate tuples: it fixes
// tool concord_work_transition and operation session_vacate, whose
// post-effect refusals carry the CD-0190 D3 state-driven replay (or operator
// contact when the replay is unreachable) instead of a reconciliation no
// work id can drive.
export function vacateError(requestID: string, key: VacateAdapterErrorKey, message: string, details?: Record<string, unknown>): AdapterErrorEnvelope {
  const toolName = "concord_work_transition"
  const operation = "session_vacate"
  const tuple = (adapterErrorTuples as Record<string, AdapterErrorTuple | undefined>)[key]
  if (!Object.hasOwn(adapterErrorTuples, key) || !tuple || !VACATE_TABLE_KEYS.includes(key)) {
    throw adapterContractDefect(toolName, operation, requestID, `unknown vacate error key ${JSON.stringify(String(key))}: the vacate constructor admits only the vacate_replay and vacate_unavailable tuples`)
  }
  const envelope = { ...baseEnvelope(toolName, operation, requestID), error: { kind: tuple.kind, retry_safe: adapterRetrySafe(tuple), recovery_action: { kind: tuple.recovery }, effect_state: tuple.effect_state, adapter_reason: tuple.adapter_reason, message: boundedUTF8(message, ADAPTER_ERROR_MESSAGE_MAX_BYTES), ...(details ? { details } : {}) } }
  return validateAdapterEnvelope(envelope as AdapterErrorEnvelope)
}

// validateAdapterEnvelope fails closed only for adapter origin: a core, lane,
// or work-start envelope passes through untouched, and an adapter-origin
// envelope that fails the generated contract throws the typed defect with
// the failing member path.
export function validateAdapterEnvelope<T>(envelope: T): T {
  if (!record(envelope) || envelope.origin !== "adapter") return envelope
  if (validateGeneratedEnvelope(envelope)) return envelope
  throw adapterContractDefect(String(envelope.tool ?? "unknown tool"), String(envelope.operation ?? "unknown operation"), String(envelope.request_id ?? "unknown request"), `member ${envelopeFailurePath(envelope) ?? "envelope"} failed the generated envelope contract`)
}

// AdapterFailure is the typed TS7 transport failure: it names one closed
// table key and carries that tuple, so failureEnvelope maps it through the
// factory without free parameters.
export class AdapterFailure extends Error {
  readonly key: GenericAdapterErrorKey
  readonly kind: string
  readonly adapterReason: string
  readonly effectState: "none" | "possible"
  readonly recovery: string
  readonly details?: Record<string, unknown>
  constructor(key: GenericAdapterErrorKey, message: string, details?: Record<string, unknown>) {
    super(message)
    this.name = "AdapterFailure"
    const tuple = tupleForKey(key, "unknown tool", "unknown operation", "unknown request")
    this.key = key
    this.kind = tuple.kind
    this.adapterReason = tuple.adapter_reason
    this.effectState = tuple.effect_state
    this.recovery = tuple.recovery
    this.details = details
  }
}

// WorkStartFailure is the work_start refusal family:
// the WorkStartEnvelope result is a separate closed host shape with its own
// kind/reason vocabulary, so its failures keep free kind and reason fields
// instead of joining the TS7 table. Shared TS7 helpers
// (resolveAmbientContext, runnerFailure) still throw AdapterFailure, and
// workStartFailure maps both classes by their kind and recovery.
class WorkStartFailure extends Error {
  constructor(readonly kind: string, readonly reason: string, message: string, readonly effect: "none" | "possible" | "partial" = "none", readonly recovery = effect === "none" ? "contact_operator" : "reconcile_operation") { super(message) }
}

function failureEnvelope(toolName: string, operation: string, requestID: string, error: unknown, fallbackKey: GenericAdapterErrorKey): CoreConcordEnvelope {
  // A contract defect is never relabeled: these fallbacks run before any TS7
  // construction, so a defect reaching them must propagate unchanged.
  if (error instanceof AdapterContractDefect) throw error
  if (error instanceof AdapterFailure) return adapterError(toolName, operation, requestID, error.key, error.message, error.details)
  return adapterError(toolName, operation, requestID, fallbackKey, String(error))
}

function runnerFailure(error: unknown, signal: AbortSignal) {
  if (error instanceof AdapterContractDefect) throw error
  if (error instanceof AdapterFailure) return error
  if (error instanceof CoreBinaryUnavailable) return new AdapterFailure("missing_binary", error.message)
  const name = error instanceof Error ? error.name : ""
  const code = typeof error === "object" && error !== null && "code" in error ? String((error as any).code) : ""
  // ENOENT names the spawn's own failure: the binary never resolved and no
  // child started, whatever the abort signal's state, so it outranks the
  // abort and timeout names.
  if (code === "ENOENT") return new AdapterFailure("missing_binary", String(error))
  // A transport can throw AbortError for a deadline. The shared caller
  // signal retains the cause even when the transport erases its name.
  if (name === "TimeoutError" || signal.aborted && signal.reason?.name === "TimeoutError") return new AdapterFailure("timeout_no_effect", String(error))
  if (signal.aborted || name === "AbortError") return new AdapterFailure("cancelled_no_effect", String(error))
  return new AdapterFailure("spawn_failure", String(error))
}

function singleJSON(text: string): any {
  const value = text.trim()
  if (!value || value.includes("\n") && value.split("\n").filter(Boolean).length !== 1) throw new Error("core stdout was not exactly one JSON value")
  const parsed = JSON.parse(value)
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("core stdout was not a JSON object")
  return parsed
}

export async function readBoundWorkerSource(request: WorkerReadingRequest, signal: AbortSignal, directory: string): Promise<unknown> {
  return readBoundWorkerPage("worker-read", request, signal, directory)
}

export async function readBoundWorkerDiff(request: WorkerDiffRequest, signal: AbortSignal, directory: string): Promise<unknown> {
  return readBoundWorkerPage("worker-diff", request, signal, directory)
}

async function readBoundWorkerPage(verb: "worker-read" | "worker-diff", request: WorkerReadingRequest | WorkerDiffRequest, signal: AbortSignal, directory: string): Promise<unknown> {
  const label = verb === "worker-diff" ? "diff" : "source"
  const leaseFault = hostLeaseFault()
  if (leaseFault) throw new Error(`${leaseFault}; no ${label} read ran`)
  const result = await runner.run([concordBinaryPath(), verb], JSON.stringify(request), signal, { cwd: directory })
  if (result.exitCode !== 0) throw new Error(result.stderr.slice(-MAX_STDERR).trim() || `the core refused the pinned ${label} read`)
  if (Buffer.byteLength(result.stdout) > 16_384) throw new Error(`pinned ${label} page exceeded its transport bound`)
  return singleJSON(result.stdout)
}

export async function executeBoundWorkerControl(request: WorkerControlRequest, context: WorkerCheckContext, packetDigest: string, directory: string): Promise<unknown> {
  const leaseFault = hostLeaseFault()
  if (leaseFault) throw new Error(`${leaseFault}; no control execution ran`)
  context.abort.throwIfAborted()
  const ambient = await resolveAmbientContext(context, directory)
  const productID = selectedProductID() || (ambient.productIDs.length === 1 ? ambient.productIDs[0] : "")
  if (!productID || ambient.projectID !== request.project_id) throw new Error("recorded control requires the authorized Project and an unambiguous selected Product")
  const client = clientRef()
  const key = privateKeyObject(await (credentialsOverride ?? new SecretToolCredentialStore()).getPrivateKey(client))
  context.abort.throwIfAborted()
  const leaseID = crypto.randomUUID()
  const assertion = {
    client_ref: client, agent_ref: context.agent, session_ref: context.sessionID,
    directory: context.directory, worktree: directory, manifest_digest: activeManifestDigest(),
    work_id: request.work_id, product_id: productID, project_id: request.project_id,
    attempt_id: request.attempt_id, packet_digest: packetDigest, worktree_identity: request.worktree_identity,
    control_id: request.control_id, lease_id: leaseID, request_id: `${context.sessionID}-${context.messageID}`,
    issued_at: new Date().toISOString(), nonce: randomNonce(),
  }
  const signed = { ...assertion, signature: b64(signBytes(null, Buffer.from(canonicalWorkerControl(assertion)), key)) }
  // This route never retries a possibly executed workload or relabels a native
  // failure as no effect. The lease identifies the existing producer's record.
  const result = await runner.run([concordBinaryPath(), "worker-control"], JSON.stringify({ ...request, assertion: signed }), context.abort, { cwd: directory })
  const refusal = `${result.stderr.slice(-MAX_STDERR).trim() || "native control result unavailable"}; reconcile native lease ${leaseID}`
  if (result.exitCode !== 0 && !result.stdout.trim()) throw new Error(refusal)
  if (Buffer.byteLength(result.stdout) > maxEnvelopeBytes) throw new Error(`native control response exceeded its transport bound; ${refusal}`)
  let response: any
  try { response = singleJSON(result.stdout) } catch (error) { throw new Error(`${String(error)}; ${refusal}`) }
  if (response.work_id !== request.work_id || response.project_id !== request.project_id || response.lease_id !== leaseID || !record(response.oracle) || response.oracle.phase !== "execute" || response.oracle.attempt_id !== request.attempt_id || response.oracle.control_id !== request.control_id || response.oracle.worker_packet_digest !== packetDigest) {
    throw new Error(`native control response differs from its bound request; ${refusal}`)
  }
  if (result.exitCode !== 0) {
    const disposition = response.lease_disposition
    const knownDisposition = record(disposition) && (disposition.state === "unknown" && disposition.outcome === undefined || disposition.state === "released" && (disposition.outcome === "completed" || disposition.outcome === "aborted"))
    if (response.outcome !== "refused" || response.oracle.qualification !== "unavailable" || response.operation_ref !== `worktree_verify:${leaseID}` || typeof response.diagnostic !== "string" || !response.diagnostic || !knownDisposition) throw new Error(refusal)
  }
  return response
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

// coreResponseFailure names what a contract-failing response broke on, or
// returns null when the response satisfies the generated contract. The
// identity stage reports the envelope itself, because a mismatch there
// describes the whole response rather than one member, and version skew is
// classified at the call site before this detail reaches an operator.
function coreResponseFailure(response: any, toolName: string, operation: string): string | null {
  if (!response || typeof response !== "object" || response.schema_version !== "1.0" || response.manifest_digest !== activeManifestDigest() || response.origin !== "core" || response.tool !== toolName || response.operation !== operation || !["ok", "pending", "partial", "error"].includes(response.outcome)) return "the envelope identity"
  if (!validateGeneratedEnvelope(response)) return `member ${envelopeFailurePath(response)} failed the generated envelope contract`
  if (response.outcome === "error") return null
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
// done, through the closed table: every mutation takes the
// operation-conflict unknown-effect tuple regardless of whether the event
// was transport or malformed JSON — the transport/malformed kinds cannot
// carry a possible effect — and the session_vacate branch routes through the
// vacate constructor, whose state-driven replay resolves the committed
// relocation request from wherever the session sits (CD-0190 D3). A read
// cannot have written, so it keeps the transport event the failure names.
function unknownOutcomeEnvelope(toolName: string, operation: string, requestID: string, transportFailure: boolean, message: string, details?: Record<string, unknown>): CoreConcordEnvelope {
  if (operationIsMutation(toolName, operation)) {
    if (toolName === "concord_work_transition" && operation === "session_vacate") return vacateError(requestID, "vacate_replay", message, details)
    return adapterError(toolName, operation, requestID, "unknown_effect", message, details)
  }
  return adapterError(toolName, operation, requestID, transportFailure ? "io_failure_retry" : "malformed_core_response_retry", message, details)
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
function invokeRunnerFailureEnvelope(toolName: string, operation: string, requestID: string, error: unknown, signal: AbortSignal, invokeRan: boolean) {
  const failure = runnerFailure(error, signal)
  if ((failure.key === "missing_binary" && !invokeRan) || !operationIsMutation(toolName, operation)) {
    return failureEnvelope(toolName, operation, requestID, failure, "spawn_failure")
  }
  const replay = toolName === "concord_work_transition" && operation === "session_vacate"
  return unknownOutcomeEnvelope(toolName, operation, requestID, true, replay ? vacateReplayRecovery(failure.message) : failure.message)
}

function selectedProductID() {
  const value = process.env.CONCORD_SELECTED_PRODUCT_ID ?? ""
  return /^[A-Za-z0-9][A-Za-z0-9._:-]{1,127}$/.test(value) ? value : ""
}

type AmbientContext = { projectID: string; productIDs: string[]; scopeVersion: string; mainWorktree: boolean }

function concordCallEnvelope(context: ToolContext, sessionDirectory: string, ambient: AmbientContext, selectedProduct: string) {
  return { schema_version: "1.0", request_id: `${context.sessionID}-${context.messageID}`, client_ref: clientRef(), principal_ref: "", session_ref: context.sessionID, agent_ref: context.agent, directory: sessionDirectory, worktree: sessionDirectory, ambient_project_id: ambient.projectID, selected_product_id: selectedProduct, scope_version: ambient.scopeVersion, manifest_digest: activeManifestDigest() }
}

async function resolveAmbientContext(context: Pick<ToolContext, "abort">, sessionDirectory: string): Promise<AmbientContext> {
  let result
  try {
    result = await runner.run([concordBinaryPath(), "project-resolve"], JSON.stringify({ directory: sessionDirectory, worktree: sessionDirectory }), context.abort)
  } catch (error) {
    throw runnerFailure(error, context.abort)
  }
  if (result.exitCode !== 0) throw new AdapterFailure("io_failure_operator", result.stderr.slice(0, MAX_STDERR))
  let response
  try { response = singleJSON(result.stdout) } catch (error) { throw new AdapterFailure("malformed_core_response_operator", String(error)) }
  if (typeof response.project_id !== "string" || response.project_id.length === 0 || typeof response.scope_version !== "string" || response.scope_version.length === 0 || typeof response.main_worktree !== "boolean" || !Array.isArray(response.product_ids) || !response.product_ids.every((value: unknown) => typeof value === "string")) {
    throw new AdapterFailure("malformed_core_response_operator", "project-resolve response failed the context contract")
  }
  return { projectID: response.project_id, productIDs: response.product_ids, scopeVersion: response.scope_version, mainWorktree: response.main_worktree }
}

async function resolveSessionDirectory(context: ToolContext): Promise<string> {
  try {
    return await hostControlPlane().sessionDirectory(context.sessionID, context.abort)
  } catch (error) {
    if (context.abort.aborted) throw runnerFailure(error, context.abort)
    throw new AdapterFailure("session_directory_unreadable", error instanceof Error ? error.message : String(error))
  }
}

// invokeConcordOperation is the single `concord project-resolve` + `concord invoke`
// transport for every adapter surface, including host-side callers outside the
// tool exports below. It owns envelope construction, the closed core-response
// contract check, and the approval_required resubmission. Native worker
// dispatch can supply the canonical session directory that its window pins.
async function invokeConcordOperationRaw(toolName: string, args: HostToolArgs, context: ToolContext, sessionDirectoryOverride?: string, ambientOverride?: AmbientContext): Promise<CoreConcordEnvelope> {
  const operation = args.operation
  const requestID = `${context.sessionID}-${context.messageID}`
  // CD-0111 D2: a session that could not claim its host lease runs closed.
  // The installer would treat it as absent and could remove the release it
  // runs, so no core call leaves before the lease exists.
  const leaseFault = hostLeaseFault()
  if (leaseFault) return adapterError(toolName, operation, requestID, "host_lease_missing", `${leaseFault}; no operation ran`)
  let ambient: AmbientContext
  let sessionDirectory: string
  try { sessionDirectory = sessionDirectoryOverride ?? await resolveSessionDirectory(context) } catch (error) { return failureEnvelope(toolName, operation, requestID, error, "context_resolution_failed") }
  try { ambient = ambientOverride ?? await resolveAmbientContext(context, sessionDirectory) } catch (error) { return failureEnvelope(toolName, operation, requestID, error, "context_resolution_failed") }
  const selectedProduct = selectedProductID() || (ambient.productIDs.length === 1 ? ambient.productIDs[0] : "")
  const envelope: any = concordCallEnvelope(context, sessionDirectory, ambient, selectedProduct)
  const run = async (input: any) => runner.run([concordBinaryPath(), "invoke"], JSON.stringify({ call_envelope: envelope, tool: toolName, operation, input }), context.abort)
  // An unreadable session_vacate answer leaves the committed relocation
  // request's state to the replay, so the refusal names that recovery
  // instead of a reconciliation the operation cannot drive.
  const vacateOperation = toolName === "concord_work_transition" && operation === "session_vacate"
  const outcomeMessage = (message: string) => (vacateOperation ? vacateReplayRecovery(message) : message)
  let result: any
  try { result = await run(args.input) } catch (error) { return invokeRunnerFailureEnvelope(toolName, operation, requestID, error, context.abort, false) }
  if (result.exitCode !== 0 && !result.stdout.trim()) {
    if (result.exitCode === CLI_INPUT_REFUSAL_EXIT && result.stdout === "") return cliInputRefusal(toolName, operation, requestID, result.stderr)
    return unknownOutcomeEnvelope(toolName, operation, requestID, true, outcomeMessage(result.stderr.slice(0, MAX_STDERR)))
  }
  let response: any
  try { response = singleJSON(result.stdout) } catch (error) {
    return unknownOutcomeEnvelope(toolName, operation, requestID, false, outcomeMessage(String(error)), salvageDetails(result.stdout))
  }
  const skewRefusal = () => {
    const disk = resolveDiskManifestDigest()
    const diskDetail = disk === null ? "the digest on disk could not be read" : `the adapter files on disk stamp ${disk}`
    // CD-0111 D4: no refusal names a session restart. The session's core and
    // contract pin to one release (D1), so a digest the retry could not heal
    // is a defect; the operator gets both digests.
    const skewDetail = `core contract digest ${response.manifest_digest} does not match this adapter's ${activeManifestDigest()}; ${diskDetail}; under the pinned release pair this mismatch is a defect, so contact the operator with both digests`
    if (!operationIsMutation(toolName, operation)) return adapterError(toolName, operation, requestID, "manifest_mismatch", skewDetail)
    // CD-0190 D3: session_vacate names no work id, so a reconcile_operation
    // recovery cannot drive a reconciliation. The committed relocation
    // request's recovery is the state-driven replay, which keeps the skew
    // detail and both digests in the refusal message.
    if (vacateOperation) return vacateError(requestID, "vacate_replay", vacateReplayRecovery(skewDetail))
    return adapterError(toolName, operation, requestID, "unknown_effect", `${skewDetail}, then reconcile this operation`)
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
      try { retryResult = await run(args.input) } catch (error) { return invokeRunnerFailureEnvelope(toolName, operation, requestID, error, context.abort, true) }
      if (retryResult.exitCode !== 0 && !retryResult.stdout.trim()) {
        if (retryResult.exitCode === CLI_INPUT_REFUSAL_EXIT && retryResult.stdout === "" && !operationIsMutation(toolName, operation)) return cliInputRefusal(toolName, operation, requestID, retryResult.stderr)
        return unknownOutcomeEnvelope(toolName, operation, requestID, true, outcomeMessage(retryResult.stderr.slice(0, MAX_STDERR)))
      }
      let retryResponse: any
      try { retryResponse = singleJSON(retryResult.stdout) } catch (error) {
        return unknownOutcomeEnvelope(toolName, operation, requestID, false, outcomeMessage(String(error)), salvageDetails(retryResult.stdout))
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
    return unknownOutcomeEnvelope(toolName, operation, requestID, false, outcomeMessage(`core response failed the generated TS7 contract: ${contractFailure}`), salvageDetails(result.stdout))
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
    if (requiredChallengeFields.some((key) => typeof details[key] !== "string" || details[key].length === 0)) return adapterError(toolName, operation, requestID, "malformed_core_response_operator", "core approval challenge lacked exact workflow metadata")
    if (toolName === "concord_work_transition" && operation === "workflow_action" && Array.from(details.premise_summary ?? "").length > 256) return adapterError(toolName, operation, requestID, "malformed_core_response_operator", "core approval challenge premise summary exceeded the public bound")
    // The selection binds only where the surface admits one. `confirm_premise`
    // is the sole action whose schema carries `selected_choice`, and the core
    // emits the field as an empty string for every other action, so comparing
    // it against an absent caller value refuses a correct challenge and takes
    // every approval-gated action with it.
    const bindsSelection = toolName === "concord_work_transition" && operation === "workflow_action" && args.input?.action_id === "confirm_premise"
    const consequenceSummary = response.error.consequence_summary
    if (!consequenceSummary || !Array.isArray(consequenceSummary.scope) || !Array.isArray(consequenceSummary.versions) || consequenceSummary.tool !== toolName || consequenceSummary.operation !== operation || consequenceSummary.operation_digest !== details.operation_digest || (bindsSelection && details.selected_choice !== args.input?.selected_choice)) return adapterError(toolName, operation, requestID, "malformed_core_response_operator", "core approval challenge did not bind the exact workflow selection")
    // CD-0037 D5: the typed consequence summary rides host permission metadata
    // unchanged. The host renders the operator prompt; this is transport, not
    // adapter-owned domain logic.
    const askMetadata = toolName === "concord_work_transition" && operation === "workflow_action"
      ? { approval_ref: details.approval_ref, operation_digest: details.operation_digest, scope: consequenceSummary.scope, versions: consequenceSummary.versions, work_id: details.work_id, action_id: details.action_id, contract_version: details.contract_version, selected_choice: details.selected_choice, decision_context_digest: details.decision_context_digest, premise_summary: details.premise_summary, consequence_summary: consequenceSummary }
      : {
          approval_ref: details.approval_ref,
          operation_digest: details.operation_digest,
          consequence_summary: consequenceSummary,
          ...(typeof details.summary === "string" ? { summary: details.summary } : {}),
          scope: consequenceSummary.scope,
          versions: consequenceSummary.versions,
          ...(typeof details.resolution_kind === "string" ? { resolution_kind: details.resolution_kind } : {}),
          ...(typeof details.from_work_id === "string" ? { from_work_id: details.from_work_id } : {}),
          ...(typeof details.to_work_id === "string" ? { to_work_id: details.to_work_id } : {}),
          ...(typeof details.client_ref === "string" ? { client_ref: details.client_ref } : {}),
          ...(typeof details.policy_version === "string" ? { policy_version: details.policy_version } : {}),
          ...(typeof details.reason === "string" ? { reason: details.reason } : {}),
        }
    // Built-in question supplies semantic choice; ToolContext.ask authorizes
    // only this exact core-issued challenge. OpenCode evaluates permission per
    // pattern; an empty list skips the policy and never asks the operator.
    try { await context.ask({ permission: `concord:${toolName}.${operation}`, patterns: [details.operation_digest], always: [], metadata: askMetadata }) } catch (error) {
      if (error instanceof AdapterContractDefect) throw error
      return adapterError(toolName, operation, requestID, "cancelled_no_effect", "host approval was rejected")
    }
    envelope.host_approval_assertion = { challenge_ref: details.approval_ref, request_digest: details.operation_digest, scope: consequenceSummary.scope, versions: consequenceSummary.versions, session_ref: envelope.session_ref, agent_ref: envelope.agent_ref, worktree: sessionDirectory, issued_at: new Date().toISOString() }
    const approvedInput = args.input && typeof args.input === "object" && !Array.isArray(args.input) ? { ...args.input, approval: { approval_ref: details.approval_ref } } : null
    if (!approvedInput) return adapterError(toolName, operation, requestID, "malformed_core_response_operator", "approval resubmission requires object input")
    try {
      result = await run(approvedInput)
    } catch (error) {
      return invokeRunnerFailureEnvelope(toolName, operation, requestID, error, context.abort, false)
    }
    try { response = singleJSON(result.stdout) } catch (error) {
      if (vacateOperation) return vacateError(requestID, "vacate_replay", vacateReplayRecovery(String(error)), salvageDetails(result.stdout))
      return adapterError(toolName, operation, requestID, "unknown_effect", String(error), salvageDetails(result.stdout))
    }
    const approvedFailure = coreResponseFailure(response, toolName, operation)
    if (approvedFailure) {
      if (vacateOperation) return vacateError(requestID, "vacate_replay", vacateReplayRecovery(`post-approval response failed the TS7 contract: ${approvedFailure}`), salvageDetails(result.stdout))
      return adapterError(toolName, operation, requestID, "unknown_effect", `post-approval response failed the TS7 contract: ${approvedFailure}`, salvageDetails(result.stdout))
    }
  }
  return response as CoreConcordEnvelope;
}

function cliInputRefusal(toolName: string, operation: string, requestID: string, stderr: string): CoreConcordEnvelope {
  return adapterError(toolName, operation, requestID, "invalid_cli_input", stderr.slice(0, ADAPTER_ERROR_MESSAGE_MAX_BYTES))
}

function requestWorkID(args: HostToolArgs): string | undefined {
  const input = args.input
  return record(input) && saneWorkID(input.work_id) ? input.work_id : undefined
}

// claimIdentity keeps the durable claim's known work identity on a post-claim
// refusal, so the reconcile-then-replay diagnostic names what the replay
// adopts. A request without a sane work id supplies nothing: the missing
// identity is the reconciliation blocker, never a value to invent.
function claimIdentity(args: HostToolArgs): Record<string, unknown> | undefined {
  const workID = requestWorkID(args)
  return workID === undefined ? undefined : { work_id: workID }
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
  } catch (error) {
    // The readback runs inside the invoke that must return a schema-valid
    // envelope, so a contract defect from the nested transport propagates
    // unchanged instead of degrading to a reconcile-attempted note.
    if (error instanceof AdapterContractDefect) throw error
    return addErrorDetails(response, { reconcile_attempted: true })
  }
}

export async function invokeConcordOperation(toolName: string, args: HostToolArgs, context: ToolContext, sessionDirectoryOverride?: string): Promise<CoreConcordEnvelope> {
  let response = await invokeConcordOperationRaw(toolName, args, context, sessionDirectoryOverride)
  try {
    await reconstructMutationResult(response, { tool: toolName, operation: args.operation, idempotencyKey: args.input?.idempotency_key },
      input => invokeConcordOperationRaw("concord_work_trace", { operation: "mutation_result", input }, context, sessionDirectoryOverride))
  } catch (error) {
    if (error instanceof AdapterContractDefect) throw error
    // The successful receipt remains truthful even when its retained document
    // cannot be read. Keep it bounded and grant no private pin authority.
    const message = String(error instanceof Error ? error.message : error).slice(0, 1024)
    const warnings = Array.isArray(response.warnings) ? response.warnings : []
    const annotated = { ...response, warnings: [...warnings, { kind: "mutation_result_unavailable", source_id: "adapter", details: { message } }] }
    if (warnings.length < 16 && Buffer.byteLength(JSON.stringify(annotated)) <= maxEnvelopeBytes) response = annotated
    else enqueueWorkNotice(context.sessionID, message)
  }
  return validateAdapterEnvelope(await reconcileUnknownEffect(toolName, args, context, response))
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
  let settled = envelope
  if (Buffer.byteLength(JSON.stringify(settled)) > maxEnvelopeBytes) {
    // The 51,200-byte output guard stands: an oversized result is replaced by
    // the whole classifier tuple for the operation before serialization.
    settled = unknownOutcomeEnvelope(toolName, operation, requestID, false, `Concord result exceeds ${maxEnvelopeBytes} bytes`)
  }
  // Final host exit: the serialized bytes must carry a schema-valid
  // adapter-origin envelope, after staleness enrichment and any replacement.
  validateAdapterEnvelope(settled)
  return { title: toolName, output: JSON.stringify(settled), metadata: {} }
}

async function encodeHostToolResult(toolName: string, args: HostToolArgs, context: ToolContext, envelope: HostConcordEnvelope): Promise<ToolResult> {
  const requestID = `${context.sessionID}-${context.messageID}`
  if (Buffer.byteLength(JSON.stringify(envelope)) > maxEnvelopeBytes) {
    const oversized = unknownOutcomeEnvelope(toolName, args.operation, requestID, false, `Concord result exceeds ${maxEnvelopeBytes} bytes`)
    return encodeHostResult(toolName, args.operation, requestID, await reconcileUnknownEffect(toolName, args, context, oversized))
  }
  return encodeHostResult(toolName, args.operation, requestID, envelope)
}

// The Project-session retirement read composes the core derivation with this
// session's own adapter dispatch-window quiescence (CD-0182 amendment): the
// core cannot see the adapter's windows, so an open, in-flight, settling, or
// refused window downgrades a derived ready state to pending with the named
// blocker. The composition only narrows readiness — it never upgrades a
// pending core answer, and a session with no retained window passes the
// core's answer through unchanged.
function composeRetirementQuiescence(toolName: string, args: HostToolArgs, envelope: HostConcordEnvelope, sessionID: string): HostConcordEnvelope {
  if (toolName !== "concord_work_trace" || args.operation !== "project_retirement" || envelope.outcome !== "ok") return envelope
  const blocker = dispatchWindows().retirementBlocker(sessionID)
  if (blocker === null) return envelope
  const result = (envelope as { result?: { state?: string; blockers?: string[] } }).result
  if (result?.state !== "ready_to_close_or_replace") return envelope
  result.state = "pending"
  result.blockers = [...(result.blockers ?? []), blocker]
  return envelope
}

async function executeHostTool(toolName: string, args: HostToolArgs, context: ToolContext): Promise<ToolResult> {
  // One staleness observation per tool call (CD-0191): the same reading
  // feeds the dispatch gate and the result notice.
  const staleness = releaseStaleness()
  const envelope = await invokeConcordOperation(toolName, args, context)
  const { envelope: settled, extraWarnings } = withReleaseStaleness(envelope, staleness)
  const composed = composeRetirementQuiescence(toolName, args, settled, context.sessionID)
  const warnings = operationIsMutation(toolName, args.operation) ? await workStateReporter.report(composed, context) : []
  return appendWarnings(await encodeHostToolResult(toolName, args, context, composed), [...warnings, ...extraWarnings])
}

async function executeHostTransition(args: HostToolArgs, context: ToolContext): Promise<ToolResult> {
  const staleness = releaseStaleness()
  const envelope = await executeWorkTransition(args, context, staleness)
  const { envelope: settled, extraWarnings } = withReleaseStaleness(envelope, staleness)
  const warnings = await workStateReporter.report(settled, context)
  return appendMoveNotice(appendWarnings(await encodeHostToolResult("concord_work_transition", args, context, settled), [...warnings, ...extraWarnings]), context)
}

// CD-0017 D4, extended by CD-0223: a dispatched worker lane runs under a
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
const laneToolBoundary = "Concord worker lanes hold no Concord tool access (CD-0017 D4, extended by CD-0223): the dispatch packet is the lane's complete Concord context"
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
  const envelope = adapterError(toolName, operation, requestID, "lane_tool_refusal", `${reason}; the call is refused with effect_state none and records nothing.`)
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
  defect_intake?: {
    failure_shape: string
    reproduction: string
    searched: string
    related_defect_ids: string[]
    root_cause_work_id?: string
  }
  governing_requirements?: string[]
  ref?: string
  // What the change will touch, for the work-size model; it carries no size.
  size_inputs?: {
    paths?: string[]
    behavior?: string
    security: boolean
    user_visible: boolean
    blast_radius: string
    estimated_production_lines: number
  }
}

// Resume carries exactly one selector and no capture fields or Project override.
// The CLI alone resolves recorded issue keys to canonical work identities.
type WorkStartResumeArgs = { work_id: string; issue_key?: never } | { issue_key: string; work_id?: never }
type WorkStartArgs = WorkStartCaptureArgs | WorkStartResumeArgs

// RecordedLinearIssueSection carries only the identity held by the local store.
type RecordedLinearIssueSection = { human_key: string; remote_issue_uuid: string; url: string }

// ProjectHandoffSection is the bounded addressed handoff a Project-selected
// work-resume renders (CD-0182 amendment): the durable handoff identity, the
// bounded repository job, and the exact next action the receiving session
// must consume through project_handoff_consume before managed execution.
// The section is visibility only: rendering it consumes nothing and
// authorizes nothing.
type ProjectHandoffSection = {
  handoff_id: string
  source_project_id: string
  bounded_job: string
  changes: string[] | null
  verification: string[] | null
  artifact_refs: string[] | null
  blockers: string[] | null
  next_action: string
  recorded_at: string
}

type WorkStartResume = {
  schema_version: "1.0"
  product_id: string
  project_id: string
  work_id: string
  worktree: { set_id: string; path: string; branch: string; base_sha: string; state: "active"; restored?: true; restored_tip?: string; restoration_warning?: string }
  branch_freshness: BranchFreshness
  linear_issue?: RecordedLinearIssueSection
  project_handoff?: ProjectHandoffSection
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

// BranchFreshness is the typed freshness the work-resume child samples for
// the worktree the session enters: how far its checked-out branch sits
// behind the origin default branch after one bounded refresh. The unknown
// status carries only its typed reason and never an authoritative count.
type BranchFreshness =
  | { status: "ok"; head_sha: string; default_ref: string; default_sha: string; behind_count: number }
  | { status: "unknown"; reason: string }

// A refused start can retain a durable capture and claim. Replay adopts them;
// retry safety does not mean that no effect occurred.
type WorkStartEffect = "none" | "possible" | "partial"
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
  linear_issue?: RecordedLinearIssueSection
  project_handoff?: ProjectHandoffSection
  branch_freshness?: BranchFreshness
  error?: { kind: string; retry_safe: boolean; recovery_action: { kind: string }; effect_state: WorkStartEffect; message: string }
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
const workStartUsage = `Capture requires ${workStartCaptureBranch.required.join(", ")}. Resume requires exactly one of work_id or issue_key, and names no Project: the resume claims in the Project the calling session resolves. Another Project of this repository is selected through concord_work_transition worktree_claim instead, and another repository needs a host-started session there that resumes by work_id or issue_key. Do not combine capture and resume fields.`

function isWorkStartResumeArgs(value: Record<string, unknown>): value is WorkStartResumeArgs {
  return "work_id" in value || "issue_key" in value
}

// The published per-field view cannot enforce the closed capture/resume modes.
// Select one generated branch so its diagnostics name the applicable fields.
function validateWorkStartArgs(value: unknown, failures: string[]): value is WorkStartArgs {
  if (!record(value)) {
    failures.push("arguments: is not of type object")
    return false
  }
  const branch = isWorkStartResumeArgs(value) ? workStartResumeBranch : workStartCaptureBranch
  return validateAgainstSchema(branch, value, failures)
}

function deriveWorkStartProduct(context: AmbientContext): string {
  const selected = process.env.CONCORD_SELECTED_PRODUCT_ID
  if (selected !== undefined && selected !== "") {
    if (!/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(selected) || !context.productIDs.includes(selected)) throw new WorkStartFailure("invalid_input", "product_selection_not_in_project", "selected Product is not a member of the resolved Project")
    return selected
  }
  if (context.productIDs.length !== 1) throw new WorkStartFailure("invalid_input", "ambiguous_product_selection", "the resolved Project does not have one unambiguous Product")
  return context.productIDs[0]
}

function validateWorkStartBootstrap(value: unknown): value is WorkStartBootstrap {
  if (!record(value) || !exactKeys(value, ["schema_version", "operation_id", "replayed", "product_id", "project_id", "work_id", "work_version", "worktree"])) return false
  if (value.schema_version !== "1.0" || !nonEmptyString(value.operation_id) || typeof value.replayed !== "boolean" || !nonEmptyString(value.product_id) || !nonEmptyString(value.project_id) || !nonEmptyString(value.work_id) || typeof value.work_version !== "number" || !Number.isInteger(value.work_version) || value.work_version < 1 || !record(value.worktree)) return false
  const worktree = value.worktree
  return exactKeys(worktree, ["set_id", "path", "branch", "base_sha", "state"])
    && nonEmptyString(worktree.set_id)
    && typeof worktree.path === "string" && worktree.path.startsWith("/")
    && nonEmptyString(worktree.branch) && typeof worktree.base_sha === "string" && /^[0-9a-f]{40}$/.test(worktree.base_sha) && worktree.state === "active"
}

type WorkStartBootstrapFailure = {
  schema_version: "1.0"; outcome: "error"; effect_state: WorkStartEffect
  operation_id?: string; product_id?: string; project_id?: string; work_id?: string; worktree_path?: string
}

function validateWorkStartBootstrapFailure(value: unknown, productID: string, projectID: string): value is WorkStartBootstrapFailure {
  if (!record(value) || value.schema_version !== "1.0" || value.outcome !== "error") return false
  const required = ["schema_version", "outcome", "effect_state"]
  const identity = ["operation_id", "product_id", "project_id", "work_id", "worktree_path"]
  if (!required.every(key => key in value) || Object.keys(value).some(key => !required.includes(key) && !identity.includes(key))) return false
  if (value.effect_state !== "none" && value.effect_state !== "possible" && value.effect_state !== "partial") return false
  if (value.effect_state !== "partial") return exactKeys(value, required)
  if (value.product_id !== productID || value.project_id !== projectID || !nonEmptyString(value.operation_id)) return false
  if ("work_id" in value && !saneWorkID(value.work_id)) return false
  return !("worktree_path" in value) || (typeof value.worktree_path === "string" && value.worktree_path.startsWith("/"))
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
// linear_issue is the optional locally recorded issue identity.
// branch_freshness is the typed behind-the-default sample the resume always
// carries; a fetch or probe failure degrades it to the typed unknown.
// project_handoff is the optional bounded addressed handoff the core renders
// for a Project-selected resume (CD-0182 amendment): absent exactly when no
// handoff addresses the resuming Project under the active contract and the
// resume carries no standing bind of the resuming session — a consume whose
// response was lost re-renders the receiver's own bind so its replay
// recovers the bounded job — and never consumable from this rendering alone.
function validateWorkStartResume(value: unknown): value is WorkStartResume {
  if (!record(value)) return false
  const baseKeys = ["schema_version", "product_id", "project_id", "work_id", "worktree", "branch_freshness"]
  const optionalKeys = ["linear_issue", "project_handoff"]
  if (!baseKeys.every((key) => key in value)) return false
  const extraKeys = Object.keys(value).filter((key) => !baseKeys.includes(key))
  if (extraKeys.some((key) => !optionalKeys.includes(key))) return false
  if (value.schema_version !== "1.0" || !nonEmptyString(value.product_id) || !nonEmptyString(value.project_id) || !saneWorkID(value.work_id) || !record(value.worktree)) return false
  const worktree = value.worktree
  const restoredKeys = ["restored", "restored_tip", "restoration_warning"]
  const restorationPresent = restoredKeys.some((key) => key in worktree)
  const worktreeKeys = ["set_id", "path", "branch", "base_sha", "state", ...(restorationPresent ? restoredKeys : [])]
  const worktreeOk = exactKeys(worktree, worktreeKeys)
    && nonEmptyString(worktree.set_id)
    && typeof worktree.path === "string" && worktree.path.startsWith("/")
    && nonEmptyString(worktree.branch) && typeof worktree.base_sha === "string" && /^[0-9a-f]{40}$/.test(worktree.base_sha) && worktree.state === "active"
  if (!worktreeOk) return false
  if (restorationPresent && (worktree.restored !== true || typeof worktree.restored_tip !== "string" || !/^[0-9a-f]{40}$/.test(worktree.restored_tip) || !nonEmptyString(worktree.restoration_warning) || Buffer.byteLength(worktree.restoration_warning) > 1000)) return false
  if (!validateBranchFreshness(value.branch_freshness)) return false
  if ("linear_issue" in value && !validateRecordedLinearIssueSection(value.linear_issue)) return false
  return !("project_handoff" in value) || validateProjectHandoffSection(value.project_handoff)
}

function validateRecordedLinearIssueSection(value: unknown): value is RecordedLinearIssueSection {
  if (!record(value) || !exactKeys(value, ["human_key", "remote_issue_uuid", "url"])) return false
  const fields = payloadSchemas.work_define_issue_link_record_input.properties
  return validateAgainstSchema(fields.human_key, value.human_key)
    && validateAgainstSchema(fields.remote_issue_uuid, value.remote_issue_uuid)
    && validateAgainstSchema(fields.url, value.url)
}

// validateProjectHandoffSection is the strict shape for the rendered bounded
// handoff. Exact keys keep the section closed on both sides; the four list
// fields accept null because the core marshals an absent list as null, and
// otherwise carry only bounded non-empty entries.
function validateProjectHandoffSection(value: unknown): value is ProjectHandoffSection {
  if (!record(value) || !exactKeys(value, ["handoff_id", "source_project_id", "bounded_job", "changes", "verification", "artifact_refs", "blockers", "next_action", "recorded_at"])) return false
  const boundedStrings = (values: unknown): values is string[] | null =>
    values === null || (Array.isArray(values) && values.length <= 16 && values.every(nonEmptyString))
  return nonEmptyString(value.handoff_id) && value.handoff_id.length <= 256
    && nonEmptyString(value.source_project_id)
    && nonEmptyString(value.bounded_job) && Buffer.byteLength(String(value.bounded_job)) <= 4000
    && boundedStrings(value.changes) && boundedStrings(value.verification) && boundedStrings(value.artifact_refs) && boundedStrings(value.blockers)
    && nonEmptyString(value.next_action) && Buffer.byteLength(String(value.next_action)) <= 1000
    && nonEmptyString(value.recorded_at)
}

const branchFreshnessReasons = new Set(["timeout", "fetch_failed", "probe_failed"])

// isValidOriginDefaultRef mirrors the owning Git-ref contract
// (git-check-ref-format) for the origin default ref the core reports: the
// origin prefix plus a branch name git itself accepts, so nested names like
// origin/release/stable validate. The rules are the negative set git
// imposes: no control character, space, ~ ^ : ? * [ or backslash anywhere,
// no "..", no "@{", no leading or trailing slash or double slash, no
// trailing dot, and no slash-separated component that starts with a dot or
// ends with ".lock".
function isValidOriginDefaultRef(value: unknown): value is string {
  if (typeof value !== "string" || !value.startsWith("origin/")) return false
  const ref = `refs/remotes/${value}`
  if (/[\u0000-\u001f\u007f ~^:?*[\\]/.test(ref)) return false
  if (ref.includes("..") || ref.includes("@{")) return false
  if (ref.startsWith("/") || ref.endsWith("/") || ref.includes("//")) return false
  if (ref.endsWith(".")) return false
  return !ref.split("/").some((component) => component.startsWith(".") || component.endsWith(".lock"))
}

// validateBranchFreshness is the strict shape for the freshness sample. The
// ok status carries the sampled SHAs, the default ref, and a nonnegative
// count; the unknown status carries only its typed reason. The sampled SHAs
// are string fields: an array or object wrapping the hex fails the contract
// instead of coercing through String(). The owning contract is
// contracts/branch-freshness.v1.json, whose generated fixtures this
// validator is held to in branch_freshness_contract.test.ts.
export function validateBranchFreshness(value: unknown): value is BranchFreshness {
  if (!record(value)) return false
  if (value.status === "ok") {
    return exactKeys(value, ["status", "head_sha", "default_ref", "default_sha", "behind_count"])
      && typeof value.head_sha === "string" && /^[0-9a-f]{40}$/.test(value.head_sha)
      && isValidOriginDefaultRef(value.default_ref)
      && typeof value.default_sha === "string" && /^[0-9a-f]{40}$/.test(value.default_sha)
      && typeof value.behind_count === "number" && Number.isInteger(value.behind_count) && value.behind_count >= 0
  }
  if (value.status === "unknown") {
    return exactKeys(value, ["status", "reason"]) && typeof value.reason === "string" && branchFreshnessReasons.has(value.reason)
  }
  return false
}

function boundedUTF8(value: string, maxBytes: number): string {
  if (Buffer.byteLength(value) <= maxBytes) return value
  return new TextDecoder().decode(Buffer.from(value).subarray(0, maxBytes))
}

function workStartError(kind: string, message: string, identity: Partial<WorkStartEnvelope> = {}, recovery = "retry_same_request", retrySafe = true, extra: Partial<WorkStartEnvelope> = {}, effectState: WorkStartEffect = "none"): WorkStartEnvelope {
  return {
    schema_version: "1.0",
    outcome: "error",
    ...identity,
    ...extra,
    error: {
      kind,
      retry_safe: retrySafe,
      recovery_action: { kind: recovery },
      effect_state: effectState,
      message: boundedUTF8(message, MAX_STDERR),
    },
  }
}

// Identity and effect describe retained state, independently of placement or
// retry safety. A lost child response cannot prove that capture rolled back.
function workStartFailure(error: unknown, identity: Partial<WorkStartEnvelope>, effectState: WorkStartEffect, fallbackKind: string): WorkStartEnvelope {
  // A contract defect is never relabeled: executeWorkStart's runner-backed
  // child calls cross the shared transport helpers, which rethrow the defect
  // unchanged, so it propagates instead of becoming a work-start refusal.
  if (error instanceof AdapterContractDefect) throw error
  const failure = error instanceof WorkStartFailure || error instanceof AdapterFailure ? error : new WorkStartFailure("transport_failure", fallbackKind, String(error))
  const retrySafe = failure.recovery === "retry_same_request"
  const continuation = effectState === "partial" && retrySafe ? " The recorded capture or claim remains; after satisfying the recovery conditions above, replay this same work_start request to continue that identity. Do not submit a new capture key." : ""
  return workStartError(failure.kind, failure.message + continuation, identity, failure.recovery, retrySafe, {}, effectState)
}

async function runWorkStartChild(argv: string[], input: string, signal: AbortSignal, options?: ChildRunnerOptions) {
  try { return await runner.run(argv, input, signal, options) } catch (error) { throw runnerFailure(error, signal) }
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

// The core reports a deterministic work-resume refusal — invalid input, a
// Project the invocation does not resolve to, an origin or default-branch
// check that fails the same way until state changes, or a typed store
// failure the store marks unsafe to repeat — with this typed exit status,
// declared in the core's work-resume help. Classification uses the status
// alone, never stderr text: replaying the same request cannot clear a
// refusal, so it maps to contact_operator, while any other work-resume
// failure keeps the retry route. The classification holds at both resume
// call sites: the initial resume read and the post-landing handoff re-read.
export const workResumeRefusalExit = 2

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
// Capture and claim may already be durable when a later step refuses. Only
// the host's verified landing proves placement (CD-0098 D3).
// The receiving side of the v1 Project-session handoff (CD-0182 amendment).
// After the host readback proves this session landed in its claimed worktree,
// the adapter binds the addressed handoff to this session through the same
// invokeConcordOperationRaw transport every adapter surface uses, so the core
// consume resolves the addressed handoff against the session's authenticated
// Project and call context and the strict call-envelope validation stays in
// force. The adapter names no receiver identity, and the core refuses
// missing, wrong-target, or stale binds closed. A failed consume is a
// warning, never a bypass: the core's managed-execution admission gate stays
// closed until the bind stands.

// projectHandoffConsumeKey derives the automatic consume idempotency key from
// the complete consume identity — work and handoff — through a JSON encoding
// the digest unambiguously decodes back into exactly one identity pair. Every
// accepted pair (each id up to the contract's 128-character bound) hashes to
// the same bounded 48-character key, so the generated input always satisfies
// the owning idempotency_key schema, while equal identities replay the
// standing bind and a different work or handoff can never reuse a prior
// bind's cached answer. The export serves the consume-boundary tests that
// pin that bound at maximum accepted identity lengths.
export function projectHandoffConsumeKey(workID: string, handoffID: string): string {
  return `handoff-consume-${createHash("sha256").update(JSON.stringify([workID, handoffID])).digest("hex").slice(0, 32)}`
}

// consumeAddressedProjectHandoff issues the typed consume for one rendered
// handoff after the session's verified landing, through the owning invoke
// route — never a second raw invoke path. The verified landing supplies the
// transport facts: the session directory the host readback proved and the
// ambient context the flow already resolved, so the consume re-resolves
// neither and the core binds against the authenticated boundary. The input
// carries the handoff_id the boot rendered: the generated tool contract
// requires it, and the key is derived from the complete consume identity so
// a successor handoff under a renewed contract can never replay a prior
// bind's cached answer. Every refusal surfaces as the returned message the
// caller carries as a refusal — the consuming session either binds the
// rendered handoff or fails closed. The export serves this file's own
// consume-boundary tests.
export async function consumeAddressedProjectHandoff(workID: string, handoffID: string, context: ToolContext, transport: { sessionDirectory: string; ambient: AmbientContext }): Promise<{ consumed: boolean; handoffID: string; message?: string }> {
  if (!workID || !handoffID || !context.sessionID) return { consumed: false, handoffID: "", message: "consume skipped: the landing named no work, handoff, and session" }
  const envelope = await invokeConcordOperationRaw("concord_work_transition", { operation: "project_handoff_consume", input: { work_id: workID, handoff_id: handoffID, idempotency_key: projectHandoffConsumeKey(workID, handoffID) } }, context, transport.sessionDirectory, transport.ambient)
  if (envelope.outcome === "ok") {
    const result = envelope.result as { handoff_id?: string } | undefined
    if (typeof result?.handoff_id === "string" && result.handoff_id) {
      return { consumed: true, handoffID: result.handoff_id }
    }
    return { consumed: false, handoffID: "", message: "the addressed handoff consume answered ok without a handoff id" }
  }
  const error = envelope.error as { kind?: string; message?: string } | undefined
  const refusal = typeof error?.message === "string" ? error.message : "the addressed handoff consume refused without a typed message"
  return { consumed: false, handoffID: "", message: `the addressed handoff consume refused (${error?.kind ?? "unknown"}): ${refusal}` }
}

async function executeWorkStart(args: WorkStartArgs, context: ToolContext, warnings: string[]): Promise<WorkStartEnvelope> {
  let target: { product_id: string; project_id: string; work_id: string; worktree: { path: string } } | null = null
  let effectState: WorkStartEffect = "none"
  let retainedIdentity: Partial<WorkStartEnvelope> = {}
  const resume = record(args) && isWorkStartResumeArgs(args)
  try {
    // Input refusals have no effect; correction belongs to the caller.
    const failures: string[] = []
    if (!validateWorkStartArgs(args, failures)) throw new WorkStartFailure("invalid_input", "invalid_work_start_input", `work_start arguments failed the host-tool contract: ${failures.join("; ")}. ${workStartUsage} Submit a corrected request; resubmitting unchanged arguments will fail again.`, "none", "correct_request")
    if (context.abort.aborted) throw new WorkStartFailure("cancelled", "cancelled_no_effect", `work_start was cancelled before ${resume ? "the resume read" : "bootstrap"}`)
    const ambient = await resolveAmbientContext(context, context.directory)
    const productID = deriveWorkStartProduct(ambient)
    // CD-0098 D2 makes the move the only route into the claimed worktree, so
    // a session that cannot reach its host cannot start work at all. Asking
    // first keeps that discovery in front of every effect.
    try {
      await hostControlPlane().probe(context.sessionID, context.abort)
    } catch (error) {
      const detail = error instanceof Error ? error.message : String(error)
      throw new WorkStartFailure(
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
      throw new WorkStartFailure("unreachable", "managed_scope_unavailable", error instanceof Error ? error.message : String(error), "none", "contact_operator")
    }
    let prepareTask: string
    let recordedIssue: RecordedLinearIssueSection | undefined
    let renderedHandoff: ProjectHandoffSection | undefined
    let resumeFreshness: BranchFreshness | undefined
    if (resume) {
      const selector = args as WorkStartResumeArgs
      // The CLI resolves the selector within the calling session's scope.
      effectState = "possible"
      const resumed = await runWorkStartChild([concordBinaryPath(), "work-resume"], JSON.stringify({ product_id: productID, project_id: ambient.projectID, ...selector, session_ref: context.sessionID, call_envelope: concordCallEnvelope(context, context.directory, ambient, productID) }), context.abort, { cwd: context.directory })
      if (resumed.exitCode === workResumeRefusalExit) throw new WorkStartFailure("resume_failure", "resume_refused", resumed.stderr.slice(0, MAX_STDERR), "none", "contact_operator")
      if (resumed.exitCode !== 0) throw new WorkStartFailure("resume_failure", "resume_refused", resumed.stderr.slice(0, MAX_STDERR), "none", "retry_same_request")
      let resumedValue: unknown
      try { resumedValue = singleJSON(resumed.stdout) } catch (error) { throw new WorkStartFailure("malformed_response", "malformed_resume_response", String(error), "none", "retry_same_request") }
      if (!validateWorkStartResume(resumedValue) || resumedValue.product_id !== productID || resumedValue.project_id !== ambient.projectID || (selector.work_id !== undefined ? resumedValue.work_id !== selector.work_id : resumedValue.linear_issue?.human_key !== selector.issue_key)) throw new WorkStartFailure("malformed_response", "malformed_resume_response", "work-resume response failed the strict resume contract", "none", "retry_same_request")
      target = resumedValue
      effectState = "partial"
      recordedIssue = resumedValue.linear_issue
      renderedHandoff = resumedValue.project_handoff
      resumeFreshness = resumedValue.branch_freshness
      if (resumedValue.worktree.restored) warnings.push(`${resumedValue.worktree.restoration_warning} Restored tip: ${resumedValue.worktree.restored_tip}`)
      prepareTask = ""
    } else {
      const capture = args as WorkStartCaptureArgs
      // host_pid gives the bootstrap claim's occupancy row its process
      // identity from creation (CD-0179); the landing re-records it.
      const bootstrapInput = { product_id: productID, project_id: ambient.projectID, ...capture, session_ref: context.sessionID, host_pid: process.pid }
      effectState = "possible"
      const boot = await runWorkStartChild([concordBinaryPath(), "work-bootstrap"], JSON.stringify(bootstrapInput), context.abort, { cwd: context.directory })
      if (boot.exitCode !== 0 && boot.stdout.trim() !== "") {
        let failureValue: unknown
        try { failureValue = singleJSON(boot.stdout) } catch { /* Unknown output cannot prove no effect. */ }
        if (validateWorkStartBootstrapFailure(failureValue, productID, ambient.projectID)) {
          effectState = failureValue.effect_state
          if (effectState === "partial") retainedIdentity = {
            product_id: failureValue.product_id, project_id: failureValue.project_id,
            work_id: failureValue.work_id, worktree_path: failureValue.worktree_path,
          }
        }
      }
      if (boot.exitCode === workBootstrapRefusalExit) throw new WorkStartFailure("bootstrap_failure", "bootstrap_refused", boot.stderr.slice(0, MAX_STDERR), "none", "contact_operator")
      if (boot.exitCode !== 0) throw new WorkStartFailure("bootstrap_failure", "bootstrap_failed", boot.stderr.slice(0, MAX_STDERR), "none", "retry_same_request")
      let bootValue: unknown
      try { bootValue = singleJSON(boot.stdout) } catch (error) { throw new WorkStartFailure("malformed_response", "malformed_bootstrap_response", String(error), "none", "retry_same_request") }
      if (!validateWorkStartBootstrap(bootValue) || bootValue.product_id !== productID || bootValue.project_id !== ambient.projectID) throw new WorkStartFailure("malformed_response", "malformed_bootstrap_response", "work-bootstrap response failed the strict bootstrap contract", "none", "retry_same_request")
      target = bootValue
      effectState = "partial"
      prepareTask = capture.task
    }

    if (context.abort.aborted) throw new WorkStartFailure("cancelled", "cancelled_after_bootstrap", `work_start was cancelled after ${resume ? "the resume read" : "bootstrap"}; replay the same idempotency_key to resume`, "none", "retry_same_request")
    try {
      await ensureConductLink(target.worktree.path, context.abort)
    } catch (error) {
      throw new WorkStartFailure("transport_failure", "conduct_link_failed", error instanceof Error ? error.message : String(error), "none", "contact_operator")
    }
    // session-prepare verifies the ACTIVE host agent: the request carries the
    // agent this session runs as (context.agent), the core resolves that
    // agent's definition and registry entry, and the read-back must name the
    // same agent. A core that answers with any other agent fails the strict
    // contract below.
    const prepared = await runWorkStartChild([concordBinaryPath(), "session-prepare"], JSON.stringify({ product_id: target.product_id, work_id: target.work_id, task: prepareTask, agent: context.agent }), context.abort, { cwd: target.worktree.path })
    if (prepared.exitCode === sessionPrepareRefusalExit) throw new WorkStartFailure("session_prepare_failure", "session_prepare_refused", prepared.stderr.slice(0, MAX_STDERR), "none", "contact_operator")
    if (prepared.exitCode !== 0) throw new WorkStartFailure("session_prepare_failure", "session_prepare_failed", prepared.stderr.slice(0, MAX_STDERR), "none", "retry_same_request")
    let preparedValue: unknown
    try { preparedValue = singleJSON(prepared.stdout) } catch (error) { throw new WorkStartFailure("malformed_response", "malformed_prepare_response", String(error), "none", "retry_same_request") }
    if (!validateWorkStartPrepared(preparedValue, target, context.agent)) throw new WorkStartFailure("malformed_response", "malformed_prepare_response", "session-prepare response failed the strict prepare contract", "none", "retry_same_request")
    const agent = preparedValue.agent

    if (context.abort.aborted) throw new WorkStartFailure("cancelled", "cancelled_before_move", "work_start was cancelled before the move; replay the same idempotency_key to resume", "none", "retry_same_request")
    // CD-0098 D2. The move is the only route to the worktree. An absent route
    // refuses here; what the capture recorded (or the resume read) stays
    // adoptable rather than being rolled back for a host-capability gap the
    // operator can repair.
    try {
      await hostControlPlane().moveSession(context.sessionID, target.worktree.path, context.abort)
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      if (error instanceof MoveSessionUnavailable) {
        throw new WorkStartFailure("unreachable", "move_session_route_unavailable", message, "none", "contact_operator")
      }
      throw new WorkStartFailure("transport_failure", "move_session_refused", message, "none", "retry_same_request")
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
      throw new WorkStartFailure("malformed_response", "session_directory_unreadable", error instanceof Error ? error.message : String(error), "none", "retry_same_request")
    }
    if (!samePath(landed, target.worktree.path)) {
      recordUnlandedClaimedWorktree(context.sessionID, target.worktree.path)
      throw new WorkStartFailure("session_directory_mismatch", "move_destination_mismatch", `the session moved to ${JSON.stringify(landed)} rather than the claimed worktree ${JSON.stringify(target.worktree.path)}`, "none", "retry_same_request")
    }
    // Metadata readback does not change this call's executing context. A native
    // continuation can open a fresh turn, but cannot stand in for its proof.
    if (!samePath(context.directory, target.worktree.path)) {
      recordUnlandedClaimedWorktree(context.sessionID, target.worktree.path)
      const scheduled = queueWorkStartContinuation(args, context, target.worktree.path, target.work_id)
      const recovery = scheduled ? "Concord scheduled one automatic continuation after this run ends; end this turn without requesting another operator message." : "The automatic continuation for this move is unavailable or already spent; no automatic retry cycle will run."
      throw new WorkStartFailure("session_directory_mismatch", "move_context_not_landed", `the host reports the session in the claimed worktree ${JSON.stringify(target.worktree.path)}, but this session's tool context still resolves in ${JSON.stringify(context.directory)}, so the move has not landed: Concord reports no success, arms no claimed worktree, and this refusal arms no turn-move boundary. ${recovery} A continuation or new message is not placement proof: confirm from the new turn's actual tool context, for example the shell working directory, that it actually resolves in ${JSON.stringify(target.worktree.path)}; only then replay this same work_start request.`, "none", "retry_same_request")
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
    let landingRecorded = false
    if (resume) {
      try {
        await recordClaimLanding(target.work_id, context.sessionID, target.worktree.path, context.abort)
        landingRecorded = true
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error)
        if (renderedHandoff) {
          // Fail closed on a handoff-bearing boot: without the recorded
          // occupancy the core admission gate cannot resolve this session's
          // Project, so managed execution would refuse. Reporting success
          // here would boot the session into a state where every
          // consequential action refuses.
          throw new WorkStartFailure("resume_failure", "claim_landing_unrecorded", `Concord did not record this session as the occupant of ${target.worktree.path}: ${message}. The addressed Project handoff requires verified placement before managed execution; replay work_start to retry the record.`, "none", "retry_same_request")
        }
        warnings.push(`Concord did not record this session as the occupant of ${target.worktree.path}: ${message}. The worktree removal gates may not hold it for this session; replay work_start to retry the record.`)
      }
    }
    // CD-0182 D5 amendment: the resume read above ran before this session's
    // verified landing, and the frontier read renders a consumed bind only
    // to a session whose verified placement stands, so a cold second
    // coordinator session of the receiving Project captured no handoff — it
    // would boot admitted by the shared bind without ever seeing the
    // bounded job it dispatches under. The landing has now recorded this
    // session's placement, so the boot re-reads the addressed handoff once:
    // the shared bind's bounded job renders to this session too, and the
    // consume below binds it before any dispatch arms. A re-read that still
    // renders no handoff changes nothing: a handoff-free work stays
    // handoff-free, and a boot whose landing did not record stays on the
    // warning path above.
    if (resume && !renderedHandoff && landingRecorded) {
      const refreshed = await runWorkStartChild([concordBinaryPath(), "work-resume"], JSON.stringify({ product_id: target.product_id, project_id: target.project_id, work_id: target.work_id, session_ref: context.sessionID, call_envelope: concordCallEnvelope(context, context.directory, ambient, target.product_id) }), context.abort, { cwd: context.directory })
      if (refreshed.exitCode === workResumeRefusalExit) throw new WorkStartFailure("resume_failure", "resume_refused", `the post-landing handoff re-read refused: ${refreshed.stderr.slice(0, MAX_STDERR)}`, "none", "contact_operator")
      if (refreshed.exitCode !== 0) throw new WorkStartFailure("resume_failure", "resume_refused", `the post-landing handoff re-read refused: ${refreshed.stderr.slice(0, MAX_STDERR)}`, "none", "retry_same_request")
      let refreshedValue: unknown
      try { refreshedValue = singleJSON(refreshed.stdout) } catch (error) { throw new WorkStartFailure("malformed_response", "malformed_resume_response", `the post-landing handoff re-read failed the strict resume contract: ${String(error)}`, "none", "retry_same_request") }
      if (!validateWorkStartResume(refreshedValue) || refreshedValue.product_id !== target.product_id || refreshedValue.project_id !== target.project_id || refreshedValue.work_id !== target.work_id) throw new WorkStartFailure("malformed_response", "malformed_resume_response", "the post-landing handoff re-read failed the strict resume contract", "none", "retry_same_request")
      renderedHandoff = refreshedValue.project_handoff
      if (refreshedValue.worktree.restored) warnings.push(`${refreshedValue.worktree.restoration_warning} Restored tip: ${refreshedValue.worktree.restored_tip}`)
    }
    // The verified placement arms the receiving side of the Project-session
    // handoff (CD-0182 amendment): the addressed handoff binds to this
    // session before any managed execution it issues. The boot consumes
    // exactly the handoff the resume rendered, by its own id, through the
    // authenticated boundary; a boot whose rendered handoff does not bind
    // refuses and arms no claimed worktree, because the core admission gate
    // stays closed until a typed consume succeeds.
    if (renderedHandoff) {
      const handoff = await consumeAddressedProjectHandoff(target.work_id, renderedHandoff.handoff_id, context, { sessionDirectory: target.worktree.path, ambient })
      if (!handoff.consumed) {
        throw new WorkStartFailure("resume_failure", "project_handoff_unconsumed", `Concord did not consume the addressed Project handoff for ${target.work_id}: ${handoff.message ?? "the consume refused"}. Managed execution stays gated until a typed consume succeeds.`, "none", "retry_same_request")
      }
    }
    // The tool context landed in the claimed worktree, so this session's
    // active claimed worktree is armed for the dispatch check. The arm sits
    // behind every refusal the landing and the handoff bind can raise, so a
    // boot that fails closed arms no dispatch.
    armClaimedWorktree(context.sessionID, target.worktree.path)
    clearWorkStartContinuation(context.sessionID)
    // The session title names the goal the session-prepare contract derived.
    // Like the closure receipt it sits after every refusal point and never
    // changes the outcome the envelope reports.
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
      // the fact of the move. A rendered handoff appends the bounded job and
      // next action so the receiving session holds both without the envelope.
      output: renderedHandoff
        ? `${moveNoticeText(target.worktree.path, dispatchRequiresNextTurn(context.sessionID))} Bounded job: ${renderedHandoff.bounded_job} Next action: ${renderedHandoff.next_action}`
        : moveNoticeText(target.worktree.path, dispatchRequiresNextTurn(context.sessionID)),
      ...(recordedIssue ? { linear_issue: recordedIssue } : {}),
      // The consumed handoff's bounded job rides the result so the receiving
      // session holds its repository job and exact next action without the
      // operator copying context (CD-0182 amendment).
      ...(renderedHandoff ? { project_handoff: renderedHandoff } : {}),
      // The branch freshness sample rides the resume result into the
      // envelope so the resuming session sees how far its branch sits
      // behind the origin default branch before it builds on it.
      ...(resumeFreshness ? { branch_freshness: resumeFreshness } : {}),
    }
  } catch (error) {
    const identity = target ? { product_id: target.product_id, project_id: target.project_id, work_id: target.work_id, worktree_path: target.worktree.path } : retainedIdentity
    return workStartFailure(error, identity, effectState, "work_start_failed")
  }
}

export const product_view = tool({ description: "Concord product view", args: argsSchema("concord_product_view"), execute: laneGuarded("concord_product_view", (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_product_view", hostRequest(args), context)) })
export const work_browse = tool({ description: "Concord work browse", args: argsSchema("concord_work_browse"), execute: laneGuarded("concord_work_browse", (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_browse", hostRequest(args), context)) })
export const work_trace = tool({ description: "Concord work trace", args: argsSchema("concord_work_trace"), execute: laneGuarded("concord_work_trace", (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_trace", hostRequest(args), context)) })
export const knowledge = tool({ description: "Concord knowledge", args: argsSchema("concord_knowledge"), execute: laneGuarded("concord_knowledge", (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_knowledge", hostRequest(args), context)) })
export const work_define = tool({ description: "Concord work define", args: argsSchema("concord_work_define"), execute: laneGuarded("concord_work_define", (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_define", hostRequest(args), context)) })
export const domain = tool({ description: "Concord domain", args: argsSchema("concord_domain"), execute: laneGuarded("concord_domain", (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_domain", hostRequest(args), context)) })
const guardedWorkTransition = laneGuarded("concord_work_transition", (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTransition(hostRequest(args), context))
export const work_transition = tool({ description: "Concord work transition. Use operation workflow_action for declared workflow actions. Use operation worker_abandon with the attempt and lane identity to close a dispatched attempt with no report. Use operation worker_reconcile to recover the original retained completed Task without a new worker run. Use action_id dispatch_worker with fields.lane_id to authorize a native worker; dispatch itself launches nothing. After authorization, issue one native foreground Task with the exact lane and attempt reference in the response, without a packet, resume, background or command fields. Route discovery does not prove admission at the current workflow step.", args: argsSchema("concord_work_transition"), execute: async (args: HostToolCall, context: ToolContext): Promise<ToolResult> => {
  const request = hostRequest(args)
  if (request.operation === "worker_reconcile") {
    const declaration = contractOperations.find((item) => item.id === "concord_work_transition.worker_reconcile")
    if (!declaration) throw new Error("worker_reconcile has no generated operation declaration")
    const seconds = request.input?.requested_budget_seconds
    // Unsupported values reach core budget admission unchanged. One admitted
    // signal bounds the host guard, preparation, readback, evidence and receipt.
    if (typeof seconds === "number" && Number.isSafeInteger(seconds) && seconds >= 1 && seconds <= declaration.supported_budget_seconds) {
      context = { ...context, abort: AbortSignal.any([context.abort, AbortSignal.timeout(seconds * 1_000)]) }
    }
    return withWorkerReadbackScope(context.abort, abort => guardedWorkTransition(args, { ...context, abort }))
  }
  return guardedWorkTransition(args, context)
} })
export const work_relate = tool({
  description: "Concord work relate",
  args: argsSchema("concord_work_relate"),
  execute: laneGuarded("concord_work_relate", async (args: HostToolCall, context: ToolContext): Promise<ToolResult> => {
    const staleness = releaseStaleness()
    const request = hostRequest(args)
    const envelope = await invokeConcordOperation("concord_work_relate", request, context)
    const { envelope: settled, extraWarnings } = withReleaseStaleness(envelope, staleness)
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
}, (_operation, _context, reason) => ({ title: "concord_work_start", output: JSON.stringify(workStartError("unauthorized", `${reason}; the call is refused with effect_state none and records nothing.`, {}, "contact_operator")), metadata: {} })) })

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

async function executeWorkerReconcile(args: HostToolArgs, context: ToolContext): Promise<HostConcordEnvelope> {
  const operation = "worker_reconcile"
  const requestID = `${context.sessionID}-${context.messageID}`
  if (!validateGeneratedPayload("work_transition_worker_reconcile_input", args.input)) return adapterError("concord_work_transition", operation, requestID, "invalid_tool_input", `worker_reconcile input failed the generated contract: ${payloadFailureMessage("work_transition_worker_reconcile_input", args.input)}`)
  const input = args.input as { work_id: string; attempt_id: string; task_part_id: string; idempotency_key: string }
  const key = (phase: string) => `worker-reconcile-${createHash("sha256").update(`${phase}\0${input.idempotency_key}`).digest("hex")}`
  const prepared = await invokeConcordOperation("concord_work_transition", { ...args, input: { ...args.input, idempotency_key: key("prepare") } }, context)
  if (prepared.outcome !== "ok") return prepared
  const recovery = (prepared.result as any)?.worker_recovery
  if (!validateGeneratedPayload("worker_recovery_context", recovery)) return adapterError("concord_work_transition", operation, requestID, "malformed_core_response_operator", "the core supplied no closed original-attempt recovery context")
  let result: AgentResultEnvelope
  let recorded = false
  try {
    const mismatch = dispatchDirectoryMismatch(recovery.worker_worktree, await resolveSessionDirectory(context))
    if (mismatch) throw new Error(mismatch)
    result = await reconcileRetainedWorker(recovery as WorkerRecoveryContext, input.work_id, input.attempt_id, input.task_part_id, { credentials: credentialsOverride ?? undefined, evidenceRunner: runner, sessionReader: hostSessionReader() ?? undefined, workerDirectory: recovery.worker_worktree, onRecorded: () => { recorded = true } }, context.abort)
  } catch (error) {
    // The composite catch spans the retained-report proof read and the
    // routine that submits evidence, so it cannot prove the composite
    // stopped before any submission: the refusal carries the underlying
    // cause and reconciles the original attempt.
    return adapterError("concord_work_transition", operation, requestID, "unknown_effect", `retained-report recovery stopped: ${String(error)}`)
  }
  if (result.outcome !== "ok") {
    if (recorded) {
      dispatchWindows().releaseRetained(recovery.coordinator_session, input.attempt_id, recovery.dispatch.lane_id)
      return adapterError("concord_work_transition", operation, requestID, "worker_reconcile_failed_recorded", result.error?.message ?? "the original worker attempt failed", { work_id: input.work_id, attempt_id: input.attempt_id, lifecycle_state: "failed" })
    }
    return adapterError("concord_work_transition", operation, requestID, "unknown_effect", result.error?.message ?? "worker reconciliation returned no completion")
  }
  const receipt = await invokeConcordOperation("concord_work_transition", { ...args, input: { ...args.input, idempotency_key: key("receipt") } }, context)
  if (receipt.outcome !== "ok" || (receipt.result as any)?.worker_recovery?.lifecycle_state !== "completed") return adapterError("concord_work_transition", operation, requestID, "unknown_effect", "the worker evidence was acknowledged, but the matching completed-attempt receipt is unavailable")
  dispatchWindows().releaseRetained(recovery.coordinator_session, input.attempt_id, recovery.dispatch.lane_id)
  return receipt
}

function workerAbandonToken(idempotencyKey: string): string {
  return createHash("sha256").update(`concord_work_transition.${WORKER_ABANDON_OPERATION}\0${idempotencyKey}`).digest("hex")
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

async function executeWorkerAbandon(args: HostToolArgs, context: ToolContext): Promise<HostConcordEnvelope> {
  const requestID = `${context.sessionID}-${context.messageID}`
  const leaseFault = hostLeaseFault()
  if (leaseFault) return adapterError("concord_work_transition", WORKER_ABANDON_OPERATION, requestID, "host_lease_missing", `${leaseFault}; no worker attempt was closed`)
  const input = args.input
  if (!validateGeneratedPayload("work_transition_worker_abandon_input", input)) {
    return adapterError("concord_work_transition", WORKER_ABANDON_OPERATION, requestID, "invalid_tool_input", `worker_abandon input failed the generated contract: ${payloadFailureMessage("work_transition_worker_abandon_input", input)}`)
  }
  const abandonInput = input as { work_id: string; attempt_id: string; lane_id: string; detail: string; idempotency_key: string }
  const lane = agentLanes.find((candidate) => candidate.id === abandonInput.lane_id)
  if (!lane) return adapterError("concord_work_transition", WORKER_ABANDON_OPERATION, requestID, "invalid_tool_input", `worker_abandon does not recognize lane ${JSON.stringify(abandonInput.lane_id)}`)
  const token = workerAbandonToken(abandonInput.idempotency_key)
  const abandonEventID = `worker-abandon-${token}`
  const result = await abandonWorkerAttempt(lane, { work_id: abandonInput.work_id, attempt_id: abandonInput.attempt_id }, abandonInput.detail, {
    credentials: credentialsOverride ?? undefined,
    evidenceRunner: runner,
    abandonEventID,
    failureAttribution: hostAbandonmentObservation(abandonInput.detail, { work_id: abandonInput.work_id, attempt_id: abandonInput.attempt_id, lane_id: abandonInput.lane_id, event_id: abandonEventID }),
  }, context.abort, () => { dispatchWindows().releaseRetained(context.sessionID, abandonInput.attempt_id, abandonInput.lane_id) })
  // The receipt follows the explicit committed fact the abandon route
  // returned: the CLI success result. A committed close runs the receipt
  // before any refusal vocabulary is matched: its envelope carries the
  // coordinator's own detail as the diagnostic, so a detail quoting a
  // refusal shape must never reroute the close through recovery below.
  // A refusal never reaches the receipt or claims the attempt was closed:
  // a none effect reports the core
  // message, a possible effect keeps the reconcile instruction, and an
  // untyped failure reports the uncertain effect.
  if (result.committed) {
    const receipt = await invokeConcordOperation("concord_work_transition", args, context)
    if (receipt.outcome === "ok") {
      return receipt
    }
    return workerAbandonReceiptFailure(requestID, receipt)
  }
  const message = result.envelope.error?.message ?? "worker abandonment returned no diagnostic"
  // Own-row recovery: the gate refused because the calling session itself
  // still occupies the worker attempt worktree. The named release route
  // (vacate, abandon retry, re-land) runs here instead of falling to the
  // operator; a foreign holder never reaches it.
  if (ownRowAbandonRefusal(message, context.sessionID)) {
    return recoverOwnRowAbandon(args, context, abandonInput, lane, token, requestID, message)
  }
  const effectState = result.envelope.error?.details?.effect_state
  if (effectState === "none") {
    return adapterError("concord_work_transition", WORKER_ABANDON_OPERATION, requestID, "worker_abandon_refused_none", message)
  }
  if (effectState === "possible") {
    // The refusal keeps the same-abandon receipt instruction and the
    // underlying durable cause; the reconcile recovery replaces the retry,
    // which cannot confirm a receipt the adapter never read.
    return adapterError("concord_work_transition", WORKER_ABANDON_OPERATION, requestID, "worker_abandon_refused_possible", `the abandonment has an uncertain effect: ${message}; reconcile the original worker_abandon attempt and its receipt`)
  }
  return adapterError("concord_work_transition", WORKER_ABANDON_OPERATION, requestID, "worker_abandon_refused_possible", "worker abandonment could not be recorded", { abandon_error_message: message })
}

function workerAbandonReceiptFailure(requestID: string, receipt: CoreConcordEnvelope, steps: string[] = []): HostConcordEnvelope {
  const error = record(receipt.error) ? receipt.error : {}
  return adapterError("concord_work_transition", WORKER_ABANDON_OPERATION, requestID, "worker_abandon_receipt_failed", "the worker attempt was closed, but the durable replay receipt was not confirmed", {
    recovery_stopped_at: "worker_abandon_receipt",
    ...(typeof receipt.outcome === "string" ? { receipt_outcome: receipt.outcome } : {}),
    ...(typeof error.kind === "string" ? { receipt_error_kind: error.kind } : {}),
    ...(typeof error.message === "string" ? { receipt_error_message: error.message } : {}),
    ...(steps.length ? { recovery_steps: steps } : {}),
  })
}

// recoverOwnRowAbandon releases the calling session's occupancy and closes the
// attempt with the same derived event identity. The session re-lands before
// the durable receipt, whose authority requires a linked worktree. A confirmed
// close retires the exact dispatch authorization independently of the receipt;
// later failures report the completed steps and do not retain a closed worker.
async function recoverOwnRowAbandon(args: HostToolArgs, context: ToolContext, abandonInput: { work_id: string; attempt_id: string; lane_id: string; detail: string; idempotency_key: string }, lane: AgentLane, token: string, requestID: string, refusedMessage: string): Promise<HostConcordEnvelope> {
  const steps: string[] = []
  const recoveryNotices = (): Array<Record<string, unknown>> => steps.map((taken) => ({ kind: "worker_abandon_recovery", source_id: "adapter", details: { step: taken } }))
  // The re-land returns the session to the claimed worktree the vacate moved
  // it out of, so the recovery leaves it where it started. A refused abandon
  // retry needs it as much as a closed one: after the vacate the session sits
  // at the registered main checkout, so stopping there strands the
  // coordinator — the re-land runs before that stop too, and its outcome
  // rides the answer as a step either way.
  const reland = async (): Promise<boolean> => {
    const relandWarnings: string[] = []
    let landed = false
    try {
      const relanded = await executeWorkStart({ work_id: abandonInput.work_id }, context, relandWarnings)
      if (record(relanded) && relanded.outcome === "ok") {
        landed = true
        const relandPath = typeof relanded.worktree_path === "string" ? relanded.worktree_path : null
        // The confirmed re-land supersedes the vacate's intermediate move
        // notice: the composed answer must point the agent at the destination
        // the session finally occupies, so the pending main-checkout notice
        // is replaced before the result drains the queue. The notice rides
        // the composed answer here rather than the re-land envelope, whose
        // output this sequence discards.
        if (relandPath) recordMoveNotice(context.sessionID, moveNoticeText(relandPath, dispatchRequiresNextTurn(context.sessionID)))
        steps.push(`work_start: the session re-landed in ${relandPath ?? "the claimed worktree"}`)
      } else {
        const failure = record(relanded) && record(relanded.error) ? relanded.error : null
        const detail = failure && typeof failure.message === "string" ? failure.message : "no diagnostic"
        steps.push(`work_start re-land refused: ${detail}; replay work_start once the session's tool context runs in the claimed worktree`)
      }
    } catch (error) {
      if (error instanceof AdapterContractDefect) throw error
      const detail = error instanceof Error ? error.message : String(error)
      steps.push(`work_start re-land refused: ${detail}; replay work_start once the session's tool context runs in the claimed worktree`)
    }
    for (const warning of relandWarnings) steps.push(`work_start warning: ${warning}`)
    return landed
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
    if (error instanceof AdapterContractDefect) throw error
    const message = error instanceof Error ? error.message : String(error)
    // A committed core vacate records the relocation request durably even
    // when its host move or readback failed afterwards; the occupancy rows
    // stand until the vacate-landing verb records the verified landing. The
    // stop reports a possible effect and names the committed request as a
    // step (TS7 durable-outcome honesty); a refusal before the write keeps
    // none.
    if (vacateCommitted) steps.push("session_vacate: the relocation request is recorded at the core; the occupancy rows stand until the landing is recorded")
    return adapterError("concord_work_transition", WORKER_ABANDON_OPERATION, requestID, (vacateCommitted ? "possible" : vacateEffectState ?? "none") === "possible" ? "worker_abandon_refused_possible" : "worker_abandon_refused_none", "the own-row recovery stopped at session_vacate", { recovery_stopped_at: "session_vacate", abandon_error_message: refusedMessage, recovery_error_message: message, ...(steps.length ? { recovery_steps: steps } : {}) })
  }
  steps.push("session_vacate: the session moved to the registered main checkout and the verified landing released its occupancy rows")
  // 2. one abandon retry. The identity derives from the same idempotency key,
  // so the retry carries the event identity the refused first write never
  // committed; it either closes the attempt or reports why it still cannot.
  const retry = await abandonWorkerAttempt(lane, { work_id: abandonInput.work_id, attempt_id: abandonInput.attempt_id }, abandonInput.detail, {
    credentials: credentialsOverride ?? undefined,
    evidenceRunner: runner,
    abandonEventID: `worker-abandon-${token}`,
    failureAttribution: hostAbandonmentObservation(abandonInput.detail, { work_id: abandonInput.work_id, attempt_id: abandonInput.attempt_id, lane_id: abandonInput.lane_id, event_id: `worker-abandon-${token}` }),
  }, context.abort, () => { dispatchWindows().releaseRetained(context.sessionID, abandonInput.attempt_id, abandonInput.lane_id) })
  // A recorded abandonment is itself an error envelope — the attempt is
  // closed, so its result is terminal — and the retry's committed fact comes
  // from its CLI success result. Any other outcome is a refusal of the retry
  // (transport, signing, or a durable gate that still refuses) and reports
  // its bound effect: none carries the core message without a closure claim,
  // possible keeps the reconcile instruction.
  const retryEffectState = retry.envelope.error?.details?.effect_state
  if (!retry.committed) {
    const retryMessage = retry.envelope.error?.message ?? "the abandon retry returned no diagnostic"
    // The vacate moved this session to the registered main checkout, so the
    // re-land runs before the stop: a refusal that ended at the main checkout
    // would strand the coordinator away from its claimed worktree.
    await reland()
    return adapterError("concord_work_transition", WORKER_ABANDON_OPERATION, requestID, retryEffectState === "none" ? "worker_abandon_refused_none" : "worker_abandon_refused_possible", retryEffectState === "none" ? retryMessage : "the own-row recovery released the occupancy row, but the abandon retry still refused", { recovery_stopped_at: "worker_abandon_retry", abandon_error_message: refusedMessage, recovery_error_message: retryMessage, ...(steps.length ? { recovery_steps: steps } : {}) })
  }
  steps.push("worker_abandon: the retry closed the dispatched attempt")
  // 3. Return to the linked worktree before the receipt's authority check.
  if (!await reland()) {
    return adapterError("concord_work_transition", WORKER_ABANDON_OPERATION, requestID, "worker_abandon_receipt_failed", "the worker attempt was closed, but the session did not re-land in the claimed worktree; the durable replay receipt was not recorded", { recovery_stopped_at: "work_start", recovery_steps: steps })
  }
  // 4. Record the durable receipt from the confirmed linked worktree.
  const receipt = await invokeConcordOperation("concord_work_transition", args, context)
  if (receipt.outcome !== "ok") {
    return workerAbandonReceiptFailure(requestID, receipt, steps)
  }
  steps.push("worker_abandon: the durable replay receipt is recorded")
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
    return adapterError("concord_work_transition", "worktree_claim", requestID, "unknown_effect", "core worktree_claim response did not carry an absolute derived destination; the claim is durable: reconcile the recorded claim, then replay the same worktree_claim request to finish host placement", claimIdentity(args))
  }
  try {
    await ensureConductLink(path, context.abort)
    await hostControlPlane().moveSession(context.sessionID, path, context.abort)
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    return adapterError("concord_work_transition", "worktree_claim", requestID, "unknown_effect", `${message}; the claim is durable: reconcile the recorded claim, then replay the same worktree_claim request to finish host placement`, claimIdentity(args))
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
    return adapterError("concord_work_transition", "worktree_claim", requestID, "unknown_effect", `${message}; the claim is durable: reconcile the recorded claim, then replay the same worktree_claim request to finish placement and landing`, claimIdentity(args))
  }
  if (!samePath(landed, path)) {
    recordUnlandedClaimedWorktree(context.sessionID, path)
    return adapterError("concord_work_transition", "worktree_claim", requestID, "unknown_effect", `the claim recorded ${JSON.stringify(path)} but the session runs in ${JSON.stringify(landed)}; the claim is durable: reconcile the recorded claim, then replay the same worktree_claim request to finish host placement`, claimIdentity(args))
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
    return adapterError("concord_work_transition", "worktree_claim", requestID, "unknown_effect", "the claim's landing verified by readback carries no work id to record it under; the original work identity needs repair before the landing can be reconciled: reconcile the recorded claim, then replay the same worktree_claim request with the corrected identity")
  }
  try {
    await recordClaimLanding(workID, context.sessionID, path, context.abort)
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    // The landing verb commits before it answers, so a refused call leaves
    // the landing record unsettled rather than absent: the diagnostic reports
    // the possible effect and the replay that confirms or completes it, and
    // never asserts that nothing was recorded.
    return adapterError("concord_work_transition", "worktree_claim", requestID, "unknown_effect", `${message}; the landing record is unsettled because the verb commits before it answers and the source occupancy stands: reconcile the recorded claim, then replay the same worktree_claim request to confirm or complete the landing record`, claimIdentity(args))
  }
  // The landing is confirmed, so this session's active claimed worktree is
  // armed for the dispatch check.
  armClaimedWorktree(context.sessionID, path)
  clearWorkStartContinuation(context.sessionID)
  if (!samePath(context.directory, path)) armTurnMoveBoundary(context.sessionID)
  // The confirmed move is a fact the agent must act on, so the result this
  // call encodes carries the notice: the new path, the paths-under-it rule,
  // and the surfaces the move made stale.
  recordMoveNotice(context.sessionID, moveNoticeText(path, dispatchRequiresNextTurn(context.sessionID)))
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
// premoved reports that the caller already moved the host session to the
// remembered destination in this turn, so the verified landing arms the turn
// move boundary before the notice is rendered from it.
export async function moveSessionToRegisteredMainCheckout(args: HostToolArgs, context: ToolContext, envelope: HostConcordEnvelope, premoved = false): Promise<HostConcordEnvelope> {
  if (args?.operation !== "session_vacate") return envelope
  const requestID = `${context.sessionID}-${context.messageID}`
  const input = args.input
  if (!record(input) || Object.keys(input).some((key) => key === "destination" || key === "destination_directory" || key === "path")) {
    // The composed routes strip a destination before the core call, so this
    // refusal is pre-commit. A caller that reaches it on an ok envelope
    // reports the recorded relocation request truthfully instead: the
    // request stands, so the state-driven replay is the recovery.
    const committed = record(envelope) && envelope.outcome === "ok"
    if (committed) return vacateError(requestID, "vacate_replay", "session_vacate derives the registered main checkout and refuses an agent-named destination; the relocation request stands, replay session_vacate from wherever the session sits to resolve it")
    return adapterError("concord_work_transition", "session_vacate", requestID, "invalid_tool_input", "session_vacate derives the registered main checkout and refuses an agent-named destination")
  }
  if (!record(envelope) || envelope.outcome !== "ok") return envelope
  const result = envelope.result
  const destination = record(result) ? result.destination_directory : undefined
  if (typeof destination !== "string" || !destination.startsWith("/")) {
    return vacateError(requestID, "vacate_replay", "core session_vacate response did not carry an absolute derived destination")
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
      // A move route the host cannot serve makes the replay unreachable, so
      // the operator is the recovery; any other refusal keeps the replay,
      // and both keep the committed request's destination named.
      if (error instanceof MoveSessionUnavailable) return vacateError(requestID, "vacate_unavailable", `${message}; the committed relocation request stands and names ${JSON.stringify(destination)} as its registered main checkout`)
      return vacateError(requestID, "vacate_replay", `${message}; the committed relocation request stands and names ${JSON.stringify(destination)} as its registered main checkout`)
    }
    moved = true
  }
  let landed: string
  try {
    landed = await hostControlPlane().sessionDirectory(context.sessionID, context.abort)
  } catch (error) {
    return vacateError(requestID, "vacate_replay", error instanceof Error ? error.message : String(error))
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
    return vacateError(requestID, reachable ? "vacate_replay" : "vacate_unavailable", `the session landed in ${JSON.stringify(landed)} rather than the registered main checkout ${JSON.stringify(destination)}`)
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
    return vacateError(requestID, "vacate_replay", "the vacate's landing verified by readback carries no work id to record it under; replay session_vacate to re-read the committed request instead of naming a work id")
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
    return vacateError(requestID, "vacate_replay", `${message}; the landing may be recorded because the verb commits before it answers, so replay session_vacate from the verified destination to confirm or complete the landing record`)
  }
  // The verified landing released the occupancy rows, so no claimed worktree
  // is armed for this session any more.
  clearClaimedWorktree(context.sessionID)
  clearWorkStartContinuation(context.sessionID)
  // A move the host readback decided completed in this turn even when a
  // stale tool context already names the destination, so the move fact, not
  // the tool context, arms the boundary. A caller's pre-move counts as the
  // same move fact.
  if (premoved || moved || !samePath(context.directory, destination)) armTurnMoveBoundary(context.sessionID)
  // The verified landing moved the session to the registered main checkout,
  // so the result this call encodes carries the move notice.
  recordMoveNotice(context.sessionID, moveNoticeText(destination, dispatchRequiresNextTurn(context.sessionID)))
  return envelope
}

// The removal verbs share the worktree-removal report path: an ok removal
// queues the notice that tells a neighbouring session the directory is gone.
export const WORKTREE_REMOVAL_OPERATIONS = new Set(["worktree_reclaim", "worktree_destroy", "worktree_audit_reclaim"])

async function executeWorkTransition(args: HostToolArgs, context: ToolContext, staleness: ReleaseStaleness | null): Promise<HostConcordEnvelope> {
  if (args?.operation === WORKER_ABANDON_OPERATION) return executeWorkerAbandon(args, context)
  if (args?.operation === "worker_reconcile") return executeWorkerReconcile(args, context)
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
      return adapterError("concord_work_transition", "session_vacate", `${context.sessionID}-${context.messageID}`, "invalid_tool_input", "session_vacate derives the registered main checkout and refuses an agent-named destination")
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
        // A move route the host cannot serve makes the replay unreachable;
        // any other refusal keeps it. Both keep the committed request and its
        // remembered destination named for the retry.
        if (error instanceof MoveSessionUnavailable) return vacateError(requestID, "vacate_unavailable", `${message}; the committed relocation request stands and names ${JSON.stringify(remembered)} as its registered main checkout, so the occupancy rows wait for the verified landing there`)
        return vacateError(requestID, "vacate_replay", `${message}; the committed relocation request stands and names ${JSON.stringify(remembered)} as its registered main checkout, so the occupancy rows wait for the verified landing there`)
      }
    }
    if ((movedToRemembered || atRemembered) && typeof remembered === "string") {
      // The host session sits at the remembered destination, so the stale
      // tool context must not name anything else.
      context.directory = remembered
    }
    const envelope = await invokeConcordOperation("concord_work_transition", args, context)
    // The pre-move relocated the session during this turn, so the verified
    // landing arms the turn-move boundary exactly as the in-route move does.
    return await moveSessionToRegisteredMainCheckout(args, context, envelope, movedToRemembered)
  }
  if (args?.operation === "worktree_claim") {
    // The claimed worktree's occupancy row records the recording host's
    // process identity from creation (CD-0179). The pid belongs to this
    // process, not to the agent, so the adapter injects it and overwrites
    // anything an agent named.
    args = { ...args, input: { ...(record(args.input) ? args.input : {}), host_pid: process.pid } }
  }
  const envelope = await invokeConcordOperation("concord_work_transition", args, context)
  if (args?.operation === "workflow_action") {
    const launched = await launchRetriedWorkerDispatch(args, context, envelope)
    if (launched !== null) return launched
  }
  const settled = await vacateTerminalWorktree(args, context, envelope)
  const landed = await moveSessionToClaimedWorktree(args, context, settled)
  return landed
}

// launchRetriedWorkerDispatch routes a combined rejection's committed retry
// dispatch through the one post-commit host launch owner (CD-0198 D5). The
// core's response carries the exact admitted retry dispatch — the derived
// packet, the minted attempt identity, the digest the fresh window opened
// for, and the claimed worktree — and this boundary arms the launch window
// from those values alone. An envelope that carries no retry members (a
// rejection-only response, an error such as the escalation wall, or any
// other action) returns null and the ordinary path continues untouched; no
// launch is attempted for a response that did not commit one.
//
// CD-0198 D5: the acknowledged output preserves the committed rejection's
// mutation receipt beside the Task directive the launch owner produces:
// changed_refs, next_valid_intents, and the core result receipt ride the
// launch envelope under their core names. The copies come from the core
// envelope the invoke boundary already validated against the generated
// contracts, so no second validator runs; retry_worker_packet stays off the
// copy because the launch consumed those exact bytes into the armed window
// and the digest names the durable copy the store holds.
async function launchRetriedWorkerDispatch(args: HostToolArgs, context: ToolContext, envelope: unknown): Promise<HostConcordEnvelope | null> {
  if (!record(args?.input) || args.input.action_id !== "reject_worker_result") return null
  if (!record(envelope) || envelope.outcome !== "ok") return null
  const result = mutationResult(envelope)
  if (!result || result.retry_worker_packet === null || typeof result.retry_worker_packet !== "object") return null
  const attemptID = typeof result.retry_attempt_id === "string" ? result.retry_attempt_id : ""
  const digest = typeof result.worker_packet_digest === "string" ? result.worker_packet_digest : ""
  const worktree = typeof result.worker_worktree === "string" ? result.worker_worktree : ""
  const launch = envelope.replayed === true
    ? errorEnvelopeForLane(null, { attempt_id: attemptID }, "error", "unauthorized_dispatch", `the combined rejection replay names retry attempt ${attemptID}; it cannot recreate a lost dispatch window or authorize another Task. Recover the original attempt with worker_reconcile, or worker_abandon when no worker report exists; never redispatch this attempt`, "reconcile_operation", { details: { retry_attempt_id: attemptID, committed_dispatch: true } })
    : await dispatchRetriedLaneWorker({ packet: result.retry_worker_packet, packetDigest: digest, attemptID, authorizedWorktree: worktree }, { context, credentials: credentialsOverride ?? undefined })
  if (envelope.replayed === true) launch.error!.retry_safe = false
  if (!record(launch)) return launch as HostConcordEnvelope
  const receipt = Array.isArray(envelope.changed_refs) ? envelope.changed_refs : undefined
  const intents = Array.isArray(envelope.next_valid_intents) ? envelope.next_valid_intents : undefined
  const { retry_worker_packet: _consumed, ...resultReceipt } = result
  const projected = record(envelope.result) && Object.hasOwn(envelope.result, "projection_reference")
  return { ...launch, changed_refs: receipt, next_valid_intents: intents, result: projected ? envelope.result : resultReceipt,
    ...(projected ? { warnings: envelope.warnings } : {}) } as HostConcordEnvelope
}

// vacateTerminalWorktree moves a session out of a work item's worktree after
// the work reached a terminal state (complete, cancel). The core attaches
// vacate_target to the transition result only when the calling session's
// linked worktree is that work item's active worktree, so the hook
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
export async function vacateTerminalWorktree(args: HostToolArgs, context: ToolContext, envelope: HostConcordEnvelope): Promise<HostConcordEnvelope> {
  const transitioned = args?.operation === "lifecycle" && record(args.input) && ["completed", "cancelled"].includes(String(args.input.target))
  if (!transitioned) return envelope
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
    if (error instanceof AdapterContractDefect) throw error
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
