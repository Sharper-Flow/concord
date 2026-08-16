import { createPrivateKey, sign as signBytes } from "node:crypto"
import { tool, type ToolContext } from "@opencode-ai/plugin"
import { contractOperations, canonicalAssertion, manifestDigest, manifestVersion, payloadSchemas } from "./generated-contracts"
import { validateGeneratedEnvelope, validateGeneratedPayload } from "./generated-contract-tests"

const ADAPTER_VERSION = manifestVersion
const SURFACE_RANGE = `${manifestVersion}-${manifestVersion}`
const ENVELOPE_VERSIONS = "1.0"
const MAX_STDERR = 8192
const GRANT_TTL_MS = 50 * 60 * 1000

export interface CredentialStore { getPrivateKey(clientRef: string): Promise<Uint8Array> }
export interface ChildRunner { run(argv: string[], input: string, signal: AbortSignal): Promise<{ exitCode: number; stdout: string; stderr: string }> }

class SecretToolCredentialStore implements CredentialStore {
  async getPrivateKey(clientRef: string): Promise<Uint8Array> {
    const child = Bun.spawn(["secret-tool", "lookup", "service", "concord", "account", clientRef], { stdin: "ignore", stdout: "pipe", stderr: "pipe" })
    const output = (await new Response(child.stdout).text()).trim()
    const code = await child.exited
    if (code !== 0 || !output) throw new Error("credential service unavailable")
    const value = output.replace(/^base64:/, "")
    try { return Uint8Array.from(Buffer.from(value, "base64")) } catch { throw new Error("credential value is not valid base64") }
  }
}

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

let credentials: CredentialStore = new SecretToolCredentialStore()
let runner: ChildRunner = defaultRunner
const grants = new Map<string, { token: string; ref: string; clientRef: string; principalRef: string; sessionRef: string; agentRef: string; clientVersion: string; surfaceVersion: string; envelopeVersion: string; manifestDigest: string; productIDs: string[]; projectIDs: string[]; scopeVersion: string; expiresAt: number }>()

export function configureConcordAdapter(overrides: { credentials?: CredentialStore; runner?: ChildRunner } = {}) {
  if (overrides.credentials) credentials = overrides.credentials
  if (overrides.runner) runner = overrides.runner
  grants.clear()
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
  const variants = contractOperations.filter((op: any) => op.tool === toolName).map((op: any) => z.object({ operation: z.literal(op.id.slice(op.id.indexOf(".") + 1)), input: zodSchema(payloadSchemas[op.input_schema.split("/").pop()!], payloadSchemas as any) }).strict())
  return z.union(variants as any)
}

;

function baseEnvelope(toolName: string, operation: string, requestID: string) {
  const queryID = (contractOperations.find((candidate: any) => candidate.tool === toolName && candidate.id.endsWith(`.${operation}`)) as any)?.query_id
  return { schema_version: "1.0", adapter_contract_version: ADAPTER_VERSION, request_id: requestID, origin: "adapter", tool: toolName, operation, ...(queryID ? { query_id: queryID } : {}), outcome: "error", resolved_scope: null, authority: "unreachable", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false }
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

const coreErrorKinds = new Set(["unknown_scope", "ambiguous_scope", "stale_context", "unauthorized", "approval_required", "approval_invalid", "version_conflict", "idempotency_conflict", "operation_conflict", "invalid_transition", "invalid_relation", "invariant_violation", "missing_evidence", "not_terminal", "outcome_mismatch", "stale_requires_review", "degraded_not_allowed", "unreachable", "invalid_cursor", "limit_exceeded", "budget_refused", "invalid_input", "cancelled", "timeout", "transport_failure", "malformed_response", "internal_error"])

function validateCoreResponse(response: any, toolName: string, operation: string): boolean {
  if (!response || typeof response !== "object" || response.schema_version !== "1.0" || response.origin !== "core" || response.tool !== toolName || response.operation !== operation || !["ok", "pending", "partial", "error"].includes(response.outcome) || !validateGeneratedEnvelope(response)) return false
  if (response.outcome === "error") return !!response.error && coreErrorKinds.has(response.error.kind)
  if (response.result !== undefined) {
    const meta: any = contractOperations.find((item: any) => item.tool === toolName && item.id.endsWith(`.${operation}`))
    const resultName = meta?.result_schema?.split("/").pop()
    if (!resultName || !validateGeneratedPayload(resultName, response.result)) return false
  }
  return true
}

function privateKeyObject(raw: Uint8Array) {
  if (raw.length !== 32) throw new Error("credential is not an Ed25519 seed")
  const prefix = Buffer.from("302e020100300506032b657004220420", "hex")
  return createPrivateKey({ key: Buffer.concat([prefix, Buffer.from(raw)]), format: "der", type: "pkcs8" })
}

function b64(value: Uint8Array) { return Buffer.from(value).toString("base64") }
export function canonicalHostApproval(assertion: Record<string, any>) {
  const names = ["challenge_ref", "request_digest", "scope", "versions", "session_ref", "agent_ref", "worktree", "client_version", "issued_at", "nonce"]
  const body = names.map((key) => { const value = assertion[key]; const text = value == null ? "" : typeof value === "string" ? value : JSON.stringify(value); const bytes = new TextEncoder().encode(text); return `${key}=${bytes.length}:${text}|` }).join("")
  return new TextEncoder().encode(`host-approval-v1\0${body}`)
}
function randomNonce() { return crypto.randomUUID().replaceAll("-", "") + crypto.randomUUID().replaceAll("-", "") }
function cacheKey(context: ToolContext, capability: string) { return `${context.sessionID}|${context.agent}|${context.worktree}|${ADAPTER_VERSION}|${selectedProductID()}|${capability}` }
function clientRef() { return process.env.CONCORD_CLIENT_REF ?? "opencode" }
function selectedProductID() {
  const value = process.env.CONCORD_SELECTED_PRODUCT_ID ?? ""
  return /^[A-Za-z0-9][A-Za-z0-9._:-]{1,127}$/.test(value) ? value : ""
}

async function grantFor(context: ToolContext, capability: string): Promise<any> {
	const key = cacheKey(context, capability)
  const current = grants.get(key)
  if (current && current.expiresAt > Date.now() && current.clientRef === clientRef()) return current as any
  let privateKey
  try { privateKey = privateKeyObject(await credentials.getPrivateKey(clientRef())) } catch (error) { throw new AdapterFailure("transport_failure", "grant_bootstrap_failed", String(error)) }
  const issuedAt = new Date().toISOString()
  const assertionFields = { client_ref: clientRef(), client_version: ADAPTER_VERSION, session_ref: context.sessionID, agent_ref: context.agent, directory: context.directory, worktree: context.worktree, requested_product_id: selectedProductID(), requested_project_ids: [] as string[], requested_capabilities: [capability], issued_at: issuedAt, nonce: randomNonce(), surface_range: SURFACE_RANGE, envelope_versions: ENVELOPE_VERSIONS, manifest_digest: manifestDigest }
  const assertion = { ...assertionFields, signature: b64(signBytes(null, Buffer.from(canonicalAssertion(assertionFields)), privateKey)) }
  let result
  try { result = await runner.run([process.env.CONCORD_BIN ?? "concord", "grant"], JSON.stringify({ assertion, expires_at: new Date(Date.now() + 60 * 60 * 1000).toISOString(), max_uses: 0 }), context.abort) } catch (error) { throw runnerFailure(error, context.abort.aborted) }
  if (result.exitCode !== 0) throw new AdapterFailure("transport_failure", "grant_bootstrap_failed", "grant bootstrap failed")
  let response
  try { response = singleJSON(result.stdout) } catch (error) { throw new AdapterFailure("malformed_response", "malformed_core_response", String(error)) }
  if (response.manifest_digest !== manifestDigest) throw new AdapterFailure("transport_failure", "manifest_mismatch", "core manifest digest does not match adapter")
  if (response.surface_version !== manifestVersion) throw new AdapterFailure("transport_failure", "incompatible_contract", "core surface version is incompatible with adapter")
  const value = { token: response.grant_token, ref: response.grant_ref, clientRef: response.client_ref, principalRef: response.principal_ref, sessionRef: response.session_ref, agentRef: response.agent_ref, clientVersion: ADAPTER_VERSION, surfaceVersion: response.surface_version, envelopeVersion: response.envelope_version, manifestDigest: response.manifest_digest, productIDs: response.product_ids ?? [], projectIDs: response.project_ids ?? [], scopeVersion: response.scope_version, expiresAt: Date.now() + GRANT_TTL_MS }
  grants.set(key, value)
  return value as any
}

async function invoke(toolName: string, args: any, context: ToolContext): Promise<any> {
  const operation = args.operation
  const requestID = `${context.sessionID}-${context.messageID}`
  if (toolName === "concord_work_transition" && operation === "workflow_action" && args.input?.action_id === "confirm_premise") {
    if (args.input.selected_choice !== "confirm" || typeof args.input.decision_context_digest !== "string" || !/^sha256:[0-9a-f]{64}$/.test(args.input.decision_context_digest)) {
      return adapterError(toolName, operation, requestID, "invalid_input", "missing_question_selection", "confirm_premise requires the closed confirm choice and a decision context digest", "none", "reread_entities")
    }
  }
  let grant: any
  try { grant = await grantFor(context, (contractOperations.find((op: any) => op.tool === toolName && op.id.endsWith(`.${operation}`)) as any)?.capability ?? "product_read") } catch (error) { return failureEnvelope(toolName, operation, requestID, error, "grant_bootstrap_failed") }
  const envelope: any = { schema_version: "1.0", request_id: requestID, grant_ref: grant.token, client_ref: grant.clientRef, client_version: grant.clientVersion, principal_ref: grant.principalRef, session_ref: grant.sessionRef, agent_ref: grant.agentRef, directory: context.directory, worktree: context.worktree, ambient_project_id: grant.projectIDs.length === 1 ? grant.projectIDs[0] : "", selected_product_id: grant.productIDs.length === 1 ? grant.productIDs[0] : "", scope_version: grant.scopeVersion, surface_version: grant.surfaceVersion, envelope_version: grant.envelopeVersion, manifest_digest: grant.manifestDigest }
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
    if (requiredChallengeFields.some((key) => typeof details[key] !== "string" || details[key].length === 0)) return adapterError(toolName, operation, requestID, "malformed_response", "malformed_core_response", "core approval challenge lacked exact workflow metadata")
    if (toolName === "concord_work_transition" && operation === "workflow_action" && Array.from(details.premise_summary ?? "").length > 256) return adapterError(toolName, operation, requestID, "malformed_response", "malformed_core_response", "core approval challenge premise summary exceeded the public bound")
    if (!Array.isArray(details.scope) || !Array.isArray(details.versions) || (toolName === "concord_work_transition" && operation === "workflow_action" && details.selected_choice !== args.input?.selected_choice)) return adapterError(toolName, operation, requestID, "malformed_response", "malformed_core_response", "core approval challenge did not bind the exact workflow selection")
    const askMetadata = toolName === "concord_work_transition" && operation === "workflow_action"
      ? { approval_ref: details.approval_ref, operation_digest: details.operation_digest, work_id: details.work_id, action_id: details.action_id, contract_version: details.contract_version, selected_choice: details.selected_choice, decision_context_digest: details.decision_context_digest, premise_summary: details.premise_summary }
      : { approval_ref: details.approval_ref, operation_digest: details.operation_digest }
    // Built-in question supplies semantic choice; ToolContext.ask authorizes
    // only this exact core-issued challenge.
    try { await context.ask({ permission: `concord:${toolName}.${operation}`, patterns: [], always: [], metadata: askMetadata }) } catch { return adapterError(toolName, operation, requestID, "cancelled", "cancelled_no_effect", "host approval was rejected") }
    let privateKey
    try { privateKey = privateKeyObject(await credentials.getPrivateKey(clientRef())) } catch (error) { return failureEnvelope(toolName, operation, requestID, error, "grant_bootstrap_failed") }
    const assertion = { challenge_ref: details.approval_ref, request_digest: details.operation_digest, scope: details.scope, versions: details.versions, session_ref: grant.sessionRef, agent_ref: grant.agentRef, worktree: context.worktree, client_version: grant.clientVersion, issued_at: new Date().toISOString(), nonce: randomNonce() }
    const signed = { ...assertion, signature: b64(signBytes(null, Buffer.from(canonicalHostApproval(assertion)), privateKey)) }
    envelope.host_approval_assertion = signed
    const approvedInput = args.input && typeof args.input === "object" && !Array.isArray(args.input) ? { ...args.input, approval: { approval_ref: details.approval_ref } } : null
    if (!approvedInput) return adapterError(toolName, operation, requestID, "malformed_response", "malformed_core_response", "approval resubmission requires object input")
    try { result = await run(approvedInput) } catch (error) { return failureEnvelope(toolName, operation, requestID, runnerFailure(error, context.abort.aborted), "unknown_effect", "possible") }
    try { response = singleJSON(result.stdout) } catch (error) { return adapterError(toolName, operation, requestID, "operation_conflict", "unknown_effect", String(error), "possible", "reconcile_operation") }
    if (!validateCoreResponse(response, toolName, operation)) return adapterError(toolName, operation, requestID, "operation_conflict", "unknown_effect", "post-approval response failed the TS7 contract", "possible", "reconcile_operation")
  }
  return response;
}

export const product_view = tool({ description: "Concord product view", args: argsSchema("concord_product_view"), execute: (args: any, context: ToolContext) => invoke("concord_product_view", args, context) })
export const work_browse = tool({ description: "Concord work browse", args: argsSchema("concord_work_browse"), execute: (args: any, context: ToolContext) => invoke("concord_work_browse", args, context) })
export const work_trace = tool({ description: "Concord work trace", args: argsSchema("concord_work_trace"), execute: (args: any, context: ToolContext) => invoke("concord_work_trace", args, context) })
export const knowledge = tool({ description: "Concord knowledge", args: argsSchema("concord_knowledge"), execute: (args: any, context: ToolContext) => invoke("concord_knowledge", args, context) })
export const work_define = tool({ description: "Concord work define", args: argsSchema("concord_work_define"), execute: (args: any, context: ToolContext) => invoke("concord_work_define", args, context) })
export const work_epic = tool({ description: "Concord work epic", args: argsSchema("concord_work_epic"), execute: (args: any, context: ToolContext) => invoke("concord_work_epic", args, context) })
export const work_transition = tool({ description: "Concord work transition", args: argsSchema("concord_work_transition"), execute: (args: any, context: ToolContext) => invoke("concord_work_transition", args, context) })
export const work_relate = tool({ description: "Concord work relate", args: argsSchema("concord_work_relate"), execute: (args: any, context: ToolContext) => invoke("concord_work_relate", args, context) })
export const work_compact = tool({ description: "Concord work compact", args: argsSchema("concord_work_compact"), execute: (args: any, context: ToolContext) => invoke("concord_work_compact", args, context) })
