import { clientRef } from "./credentials"
import { contractOperations, maxEnvelopeBytes, payloadSchemas, workflowActionPublicVariants } from "./generated-contracts"
import { activeManifestDigest, adoptManifestDigest, resolveDiskManifestDigest } from "./manifest-pin"
import { validateGeneratedEnvelope, validateGeneratedPayload, envelopeFailurePath, payloadFailurePath } from "./generated-contract-tests"
import { AdapterContractDefect } from "./adapter-contract-defect"
// The typed fail-closed error re-exports so callers of this module can catch
// it by class without importing the shared owner.
export { AdapterContractDefect }
import { hostControlPlane } from "./move-session"
import { concordBinaryPath, CoreBinaryUnavailable } from "./core-runtime"
import { createWorkStateReporter } from "./workflow-status"
import { hostLeaseFault, releaseStaleness, type ReleaseStaleness } from "./host-lease"
import { reconstructMutationResult } from "./mutation-result"

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
// `request` property: {request: {request: {operation, input}}}. No legitimate
// HostToolArgs carries a `request` property, so that key is the discriminator. Both host shapes
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
type HostConcordEnvelope = CoreConcordEnvelope

export interface ChildRunnerOptions { cwd?: string; env?: Record<string, string> }
export interface ChildRunner { run(argv: string[], input: string, signal: AbortSignal, options?: ChildRunnerOptions): Promise<{ exitCode: number; stdout: string; stderr: string }> }
const MAX_OUTPUT_BYTES = 65_536

function appendOutputTail(current: string, text: string): string {
  const combined = current + text
  if (Buffer.byteLength(combined) <= MAX_OUTPUT_BYTES) return combined
  const bytes = Buffer.from(combined)
  let start = bytes.length - MAX_OUTPUT_BYTES
  while (start < bytes.length && (bytes[start] & 0xc0) === 0x80) start++
  return bytes.subarray(start).toString()
}

export async function readChildStdout(stream: ReadableStream<Uint8Array>): Promise<{ stdout: string }> {
  const reader = stream.getReader()
  const decoder = new TextDecoder()
  let output = ""
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    const text = decoder.decode(value, { stream: true })
    output = appendOutputTail(output, text)
  }
  const final = decoder.decode()
  output = appendOutputTail(output, final)
  return { stdout: output }
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
      const [captured, stderr, exitCode] = await Promise.all([readChildStdout(child.stdout), stderrPromise, child.exited])
      signal.throwIfAborted()
      return { exitCode, stdout: captured.stdout, stderr }
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
  invalid_cli_input: { kind: "invalid_input", adapter_reason: "invalid_cli_input", effect_state: "none", recovery: "correct_request" },
  invalid_request_wrapper: { kind: "invalid_input", adapter_reason: "invalid_request_wrapper", effect_state: "none", recovery: "correct_request" },
  invalid_tool_input: { kind: "invalid_input", adapter_reason: "invalid_tool_input", effect_state: "none", recovery: "correct_request" },
} as const

export type AdapterErrorKey = keyof typeof adapterErrorTuples
export type GenericAdapterErrorKey = AdapterErrorKey

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
  if (!Object.hasOwn(adapterErrorTuples, key) || !tuple) {
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
// was transport or malformed JSON. A read cannot have written, so it keeps
// the transport event the failure names.
function unknownOutcomeEnvelope(toolName: string, operation: string, requestID: string, transportFailure: boolean, message: string, details?: Record<string, unknown>): CoreConcordEnvelope {
  if (operationIsMutation(toolName, operation)) {
    return adapterError(toolName, operation, requestID, "unknown_effect", message, details)
  }
  return adapterError(toolName, operation, requestID, transportFailure ? "io_failure_retry" : "malformed_core_response_retry", message, details)
}

// One classifier for a thrown runner error at any invoke stage: the first
// invoke and the version-skew retry. invokeRan marks
// the version-skew retry: its first child already ran and may have committed,
// so a missing binary on the retry no longer proves an absent effect and
// every mutation throw there takes the mutation classification. A read cannot
// have written, so it keeps the transport event the error names. A mutation
// whose child started may have committed before the abort, the timeout, or
// the crash killed it, so the refusal takes the operation's unknown-outcome
// classification: a possible effect with the reconcile recovery.
function invokeRunnerFailureEnvelope(toolName: string, operation: string, requestID: string, error: unknown, signal: AbortSignal, invokeRan: boolean) {
  const failure = runnerFailure(error, signal)
  if ((failure.key === "missing_binary" && !invokeRan) || !operationIsMutation(toolName, operation)) {
    return failureEnvelope(toolName, operation, requestID, failure, "spawn_failure")
  }
  return unknownOutcomeEnvelope(toolName, operation, requestID, true, failure.message)
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
// contract check. Bootstrap supplies
// its already-resolved placement when consuming an addressed handoff.
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
  let result: any
  try { result = await run(args.input) } catch (error) { return invokeRunnerFailureEnvelope(toolName, operation, requestID, error, context.abort, false) }
  if (result.exitCode !== 0 && !result.stdout.trim()) {
    if (result.exitCode === CLI_INPUT_REFUSAL_EXIT && result.stdout === "") return cliInputRefusal(toolName, operation, requestID, result.stderr)
    return unknownOutcomeEnvelope(toolName, operation, requestID, true, result.stderr.slice(0, MAX_STDERR))
  }
  let response: any
  try { response = singleJSON(result.stdout) } catch (error) {
    return unknownOutcomeEnvelope(toolName, operation, requestID, false, String(error), salvageDetails(result.stdout))
  }
  const skewRefusal = () => {
    const disk = resolveDiskManifestDigest()
    const diskDetail = disk === null ? "the digest on disk could not be read" : `the adapter files on disk stamp ${disk}`
    // CD-0111 D4: no refusal names a session restart. The session's core and
    // contract pin to one release (D1), so a digest the retry could not heal
    // is a defect; the operator gets both digests.
    const skewDetail = `core contract digest ${response.manifest_digest} does not match this adapter's ${activeManifestDigest()}; ${diskDetail}; under the pinned release pair this mismatch is a defect, so contact the operator with both digests`
    if (!operationIsMutation(toolName, operation)) return adapterError(toolName, operation, requestID, "manifest_mismatch", skewDetail)
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
        return unknownOutcomeEnvelope(toolName, operation, requestID, true, retryResult.stderr.slice(0, MAX_STDERR))
      }
      let retryResponse: any
      try { retryResponse = singleJSON(retryResult.stdout) } catch (error) {
        return unknownOutcomeEnvelope(toolName, operation, requestID, false, String(error), salvageDetails(retryResult.stdout))
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
    return unknownOutcomeEnvelope(toolName, operation, requestID, false, `core response failed the generated TS7 contract: ${contractFailure}`, salvageDetails(result.stdout))
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

export async function invokeConcordOperation(toolName: string, args: HostToolArgs, context: ToolContext): Promise<CoreConcordEnvelope> {
  let response = await invokeConcordOperationRaw(toolName, args, context)
  try {
    await reconstructMutationResult(response, { tool: toolName, operation: args.operation, idempotencyKey: args.input?.idempotency_key },
      input => invokeConcordOperationRaw("concord_work_trace", { operation: "mutation_result", input }, context))
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
// way the gate brief enqueues.
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

async function executeHostTool(toolName: string, args: HostToolArgs, context: ToolContext): Promise<ToolResult> {
  // One staleness observation per tool call keeps its result notice consistent.
  const staleness = releaseStaleness()
  const envelope = await invokeConcordOperation(toolName, args, context)
  const { envelope: settled, extraWarnings } = withReleaseStaleness(envelope, staleness)
  const warnings = operationIsMutation(toolName, args.operation) ? await workStateReporter.report(settled, context) : []
  return appendWarnings(await encodeHostToolResult(toolName, args, context, settled), [...warnings, ...extraWarnings])
}

function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

function boundedUTF8(value: string, maxBytes: number): string {
  if (Buffer.byteLength(value) <= maxBytes) return value
  return new TextDecoder().decode(Buffer.from(value).subarray(0, maxBytes))
}

export const product_view = tool({ description: "Concord product view", args: argsSchema("concord_product_view"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_product_view", hostRequest(args), context) })
export const work_browse = tool({ description: "Concord work browse", args: argsSchema("concord_work_browse"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_browse", hostRequest(args), context) })
export const work_trace = tool({ description: "Concord work trace", args: argsSchema("concord_work_trace"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_trace", hostRequest(args), context) })
export const knowledge = tool({ description: "Concord knowledge", args: argsSchema("concord_knowledge"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_knowledge", hostRequest(args), context) })
export const work_define = tool({ description: "Concord work define", args: argsSchema("concord_work_define"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_define", hostRequest(args), context) })
export const domain = tool({ description: "Concord domain", args: argsSchema("concord_domain"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_domain", hostRequest(args), context) })
export const work_transition = tool({ description: "Concord work transition. Use operation workflow_action for declared workflow actions, including record_review and record_verdict. Route discovery does not prove admission at the current workflow step.", args: argsSchema("concord_work_transition"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_transition", hostRequest(args), context) })
export const work_relate = tool({
  description: "Concord work relate",
  args: argsSchema("concord_work_relate"),
  execute: async (args: HostToolCall, context: ToolContext): Promise<ToolResult> => {
    const staleness = releaseStaleness()
    const request = hostRequest(args)
    const envelope = await invokeConcordOperation("concord_work_relate", request, context)
    const { envelope: settled, extraWarnings } = withReleaseStaleness(envelope, staleness)
    const warnings = operationIsMutation("concord_work_relate", request.operation) ? await workStateReporter.report(settled, context) : []
    return appendWarnings(await encodeHostToolResult("concord_work_relate", request, context, settled), [...warnings, ...extraWarnings])
  },
})
export const work_compact = tool({ description: "Concord work compact", args: argsSchema("concord_work_compact"), execute: (args: HostToolCall, context: ToolContext): Promise<ToolResult> => executeHostTool("concord_work_compact", hostRequest(args), context) })
