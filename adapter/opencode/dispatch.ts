import { sign as signBytes } from "node:crypto"
import { agentLanePacketSchema, agentLanes, routingPolicies, routingPolicyManifestDigest, routingPolicyVersion, type AgentLane } from "./generated-agent-lanes"
import { SecretToolCredentialStore, b64, clientRef, privateKeyObject, randomNonce, type CredentialStore } from "./credentials"

const MAX_OUTPUT_BYTES = 65_536
const MAX_ERROR_BYTES = 8_192
const MAX_CLI_INPUT_BYTES = 65_536
const PACKET_SCHEMA_VERSION = "1.0"
const REPORT_SCHEMA_VERSION = "1.0"
const MODEL_PATTERN = /^[a-z][a-z0-9_.-]*\/[^/ ]+$/

export interface DispatchRunner {
  run(argv: string[], input: string, signal: AbortSignal): Promise<{ exitCode: number; stdout: string; stderr: string; fallbackExhausted?: boolean }>
}

export interface AgentLanePacket {
  schema_version: "1.0"
  attempt_id: string
  lane_id: string
  lane_version: number
  lane_digest: string
  work_id: string
  step_id: string
  inputs: { task: string; context?: string; constraints?: string[] }
}

export interface SessionMetadata {
  readback_model: string
  session_id: string | null
  fallback_reason: "rate_limit" | "provider_unavailable" | "budget_exhausted" | "other" | null
}

export interface RunSessionMetadata {
  session_id: string
  fallback_reason: SessionMetadata["fallback_reason"]
}

export interface AgentResultEnvelope {
  schema_version: "1.0"
  outcome: "ok" | "blocked" | "fallback" | "error"
  lane: { id: string; version: number; digest: string }
  agent: string
  routing_policy_version: string
  routing_policy_digest: string
  resolved_model: string
  resolution_role: "preferred" | "fallback"
  fallback_reason: "rate_limit" | "provider_unavailable" | "budget_exhausted" | "other" | ""
  readback_model: string | null
  session_id: string | null
  output?: string
  error?: {
    kind: "invalid_input" | "blocked" | "fallback" | "error" | "model_identity_mismatch"
    retry_safe: boolean
    recovery_action: "retry_same_request" | "adjust_budget" | "contact_operator" | "reconcile_operation"
    message: string
  }
}

const defaultRunner: DispatchRunner = {
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

let runner: DispatchRunner = defaultRunner
let evidenceRunner: DispatchRunner = defaultRunner
let defaultCredentials: CredentialStore = new SecretToolCredentialStore()

export function configureWorkerDispatch(overrides: { runner?: DispatchRunner; evidenceRunner?: DispatchRunner; credentials?: CredentialStore } = {}): void {
  runner = overrides.runner ?? defaultRunner
  evidenceRunner = overrides.evidenceRunner ?? defaultRunner
  defaultCredentials = overrides.credentials ?? new SecretToolCredentialStore()
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

function validateSchema(schema: any, value: unknown, root: any): boolean {
  if (!schema || typeof schema !== "object") return false
  if (schema.$ref) return validateSchema(root[schema.$ref.replace("#/$defs/", "")], value, root)
  if (schema.const !== undefined && JSON.stringify(schema.const) !== JSON.stringify(value)) return false
  if (schema.type) {
    const types = Array.isArray(schema.type) ? schema.type : [schema.type]
    const valid = types.some((type: string) => type === "object" ? isRecord(value) : type === "array" ? Array.isArray(value) : type === "integer" ? typeof value === "number" && Number.isInteger(value) : type === "number" ? typeof value === "number" : typeof value === type)
    if (!valid) return false
  }
  if (typeof value === "string") {
    if (schema.minLength !== undefined && value.length < schema.minLength || schema.maxLength !== undefined && value.length > schema.maxLength) return false
    if (schema.pattern && !new RegExp(schema.pattern).test(value)) return false
  }
  if (typeof value === "number" && (schema.minimum !== undefined && value < schema.minimum || schema.maximum !== undefined && value > schema.maximum)) return false
  if (isRecord(value)) {
    const properties = schema.properties ?? {}
    if ((schema.required ?? []).some((key: string) => !(key in value))) return false
    for (const [key, child] of Object.entries(properties)) if (key in value && !validateSchema(child, value[key], root)) return false
    if (schema.additionalProperties === false && Object.keys(value).some((key) => !(key in properties))) return false
  }
  if (Array.isArray(value)) {
    if (schema.minItems !== undefined && value.length < schema.minItems || schema.maxItems !== undefined && value.length > schema.maxItems) return false
    if (schema.uniqueItems && new Set(value.map((item) => JSON.stringify(item))).size !== value.length) return false
    if (schema.items && value.some((item) => !validateSchema(schema.items, item, root))) return false
  }
  return true
}

export function validateAgentLanePacket(value: unknown): value is AgentLanePacket {
  return validateSchema(agentLanePacketSchema, value, agentLanePacketSchema)
}

const RUN_EVENT_TYPES = new Set(["step_start", "step_finish", "text", "reasoning", "tool_use", "error"])

function typedFallbackReason(reason: unknown): SessionMetadata["fallback_reason"] {
  if (reason === "account_rate_limit" || reason === "rate_limit") return "rate_limit"
  if (reason === "provider_unavailable" || reason === "provider_error") return "provider_unavailable"
  if (reason === "budget_exhausted" || reason === "quota_exhausted") return "budget_exhausted"
  return typeof reason === "string" && reason.length > 0 ? "other" : null
}

function hostStatusMetadata(value: Record<string, unknown>): { sessionID: string; reason: unknown } | null {
  if (value.type !== "message.updated" || !isRecord(value.properties)) return null
  const properties = value.properties
  const sessionID = typeof properties.sessionId === "string" ? properties.sessionId : typeof properties.sessionID === "string" ? properties.sessionID : null
  if (!sessionID) return null
  const reason = isRecord(properties.status) && isRecord(properties.status.action) ? properties.status.action.reason : null
  return { sessionID, reason }
}

export function readRunSessionMetadata(stdout: string): RunSessionMetadata | null {
  if (Buffer.byteLength(stdout) > MAX_OUTPUT_BYTES) return null
  const sessions = new Set<string>()
  const reasons = new Set<NonNullable<SessionMetadata["fallback_reason"]>>()
  let officialEvents = 0
  let completed = false
  for (const line of stdout.split("\n")) {
    if (!line.trim()) continue
    let value: unknown
    try { value = JSON.parse(line) } catch { return null }
    if (!isRecord(value) || typeof value.type !== "string") return null
    if (RUN_EVENT_TYPES.has(value.type)) {
      if (typeof value.timestamp !== "number" || typeof value.sessionID !== "string" || value.sessionID.length === 0) return null
      officialEvents++
      sessions.add(value.sessionID)
      if (value.type === "step_finish" && isRecord(value.part) && value.part.reason === "stop") completed = true
      continue
    }
    const status = hostStatusMetadata(value)
    if (!status) return null
    sessions.add(status.sessionID)
    const reason = typedFallbackReason(status.reason)
    if (reason) reasons.add(reason)
  }
  if (officialEvents === 0 || !completed || sessions.size !== 1) return null
  return { session_id: [...sessions][0], fallback_reason: reasons.size === 0 ? null : reasons.size === 1 ? [...reasons][0] : "other" }
}

export function readExportSessionMetadata(stdout: string, expectedSessionID: string): Pick<SessionMetadata, "readback_model" | "session_id"> | null {
  if (Buffer.byteLength(stdout) > MAX_OUTPUT_BYTES) return null
  let value: unknown
  try { value = JSON.parse(stdout) } catch { return null }
  if (!isRecord(value) || !isRecord(value.info) || value.info.id !== expectedSessionID || !Array.isArray(value.messages)) return null
  const seen = new Set<string>()
  const assistants: { id: string; created: number; model: string }[] = []
  for (const message of value.messages) {
    if (!isRecord(message) || !isRecord(message.info) || !Array.isArray(message.parts)) return null
    const info = message.info
    if (typeof info.id !== "string" || typeof info.sessionID !== "string" || info.sessionID !== expectedSessionID || !isRecord(info.time) || typeof info.time.created !== "number" || seen.has(info.id)) return null
    seen.add(info.id)
    if (info.role === "user") continue
    if (info.role !== "assistant" || typeof info.providerID !== "string" || typeof info.modelID !== "string") return null
    const model = `${info.providerID}/${info.modelID}`
    if (!MODEL_PATTERN.test(model)) return null
    assistants.push({ id: info.id, created: info.time.created, model })
  }
  assistants.sort((left, right) => left.created - right.created || left.id.localeCompare(right.id))
  const latest = assistants.at(-1)
  return latest ? { readback_model: latest.model, session_id: expectedSessionID } : null
}

function laneForPacket(packet: AgentLanePacket): AgentLane | null {
  return agentLanes.find((lane) => lane.id === packet.lane_id && lane.version === packet.lane_version && lane.digest === packet.lane_digest) ?? null
}

function policyForLane(lane: AgentLane): (typeof routingPolicies)[number] | null {
  return routingPolicies.find((policy) => policy.capability_class === lane.capability_class) ?? null
}

function baseEnvelope(lane: AgentLane | null, packet: Partial<AgentLanePacket>, outcome: AgentResultEnvelope["outcome"]): AgentResultEnvelope {
  const id = lane?.id ?? String(packet.lane_id ?? "")
  return { schema_version: "1.0", outcome, lane: { id, version: lane?.version ?? Number(packet.lane_version ?? 0), digest: lane?.digest ?? String(packet.lane_digest ?? "") }, agent: lane ? `concord-${lane.id}` : `concord-${id}`, routing_policy_version: routingPolicyVersion, routing_policy_digest: routingPolicyManifestDigest, resolved_model: lane?.pinned_model ?? "", resolution_role: "preferred", fallback_reason: "", readback_model: null, session_id: null }
}

function errorEnvelope(lane: AgentLane | null, packet: Partial<AgentLanePacket>, outcome: "blocked" | "fallback" | "error", kind: AgentResultEnvelope["error"]["kind"], message: string, recovery_action: AgentResultEnvelope["error"]["recovery_action"] = "contact_operator"): AgentResultEnvelope {
  return { ...baseEnvelope(lane, packet, outcome), error: { kind, retry_safe: outcome !== "fallback", recovery_action, message: message.slice(0, MAX_ERROR_BYTES) } }
}

function hostReportedFallbackExhausted(result: { stdout: string; fallbackExhausted?: boolean }): boolean {
  if (result.fallbackExhausted === true) return true
  for (const line of result.stdout.split("\n")) {
    if (!line.trim()) continue
    try {
      const value = JSON.parse(line)
      if (!isRecord(value)) return false
      const status = hostStatusMetadata(value)
      if (status?.reason === "fallback_exhausted") return true
    } catch { return false }
  }
  return false
}

function concordBinaryPath(override?: string): string {
  return override ?? process.env.CONCORD_BIN ?? "concord"
}

// canonicalWorkerEvidence mirrors CanonicalWorkerEvidenceAssertion in
// internal/agent/worker_evidence.go. The byte sequence is pinned by the shared
// vector at worker-evidence-vector.json, which both sides test against, so a
// drift between the two encoders fails a test rather than weakening the
// boundary. Field order is part of the contract.
export function canonicalWorkerEvidence(assertion: Record<string, unknown>): Uint8Array {
  const names = ["client_ref", "verb", "work_id", "attempt_id", "lane_id", "lane_version", "lane_digest", "routing_policy_version", "routing_policy_digest", "resolved_model", "readback_model", "failure_kind", "host_provenance_digest", "issued_at", "nonce"]
  const body = names.map((key) => {
    const value = assertion[key]
    const text = value == null ? "" : typeof value === "number" ? String(value) : String(value)
    const bytes = new TextEncoder().encode(text)
    return `${key}=${bytes.length}:${text}|`
  }).join("")
  return new TextEncoder().encode(`worker-evidence-v1\0${body}`)
}

// signWorkerEvidence proves the adapter is the registered client authorized to
// record this exact attempt's evidence (CD-0044 / issue #185). The signature
// never reaches the worker: it is produced here, after the run, and is not part
// of the lane packet or any prompt surface.
async function signWorkerEvidence(credentials: CredentialStore, fields: Record<string, unknown>): Promise<Record<string, unknown>> {
  const assertion = { ...fields, client_ref: clientRef(), issued_at: new Date().toISOString(), nonce: randomNonce() }
  const privateKey = privateKeyObject(await credentials.getPrivateKey(clientRef()))
  return { ...assertion, signature: b64(signBytes(null, Buffer.from(canonicalWorkerEvidence(assertion)), privateKey)) }
}

// recordWorkerEvent appends one worker evidence event through the short-lived
// JSON CLI, the same transport concord.ts uses for every tool invocation. The
// adapter stays envelope-thin per CD-0017 D2 and never writes the event log
// directly. Returns a bounded diagnostic on failure, or null on success.
async function recordWorkerEvent(childRunner: DispatchRunner, binary: string, command: "worker-dispatch" | "worker-complete" | "worker-fail", request: Record<string, unknown>, signal: AbortSignal): Promise<string | null> {
  const input = JSON.stringify(request)
  if (Buffer.byteLength(input) > MAX_CLI_INPUT_BYTES) return `${command} request exceeded the bounded CLI input limit`
  let result: { exitCode: number; stdout: string; stderr: string }
  try { result = await childRunner.run([binary, command], input, signal) } catch (error) { return String(error).slice(0, MAX_ERROR_BYTES) }
  if (result.exitCode !== 0) return (result.stderr || result.stdout).slice(0, MAX_ERROR_BYTES) || `${command} failed without diagnostic output`
  return null
}

// CD-0034 / issue #103: host prompt provenance. The adapter enumerates the
// unversioned host surfaces it can bind — the lane agent definition file, the
// AGENTS.md chain at spawn cwd, and instruction files declared through
// CONCORD_HOST_INSTRUCTIONS (colon-separated paths) — hashing each. Surfaces
// it cannot enumerate (provider hints, voice overlays, MCP tool prompts) are
// recorded by name as unenumerated. Injection is permitted only when
// recorded: a silent injection change changes this digest and is visible in
// dispatch evidence.
export type HostProvenanceSource = { kind: "agent_definition" | "agents_md" | "instruction_file" | "unenumerated"; path?: string; sha256?: string }
export type HostProvenance = { digest: string; sources: HostProvenanceSource[] }

const UNENUMERATED_SURFACES: HostProvenanceSource[] = [
  { kind: "unenumerated" }, // provider behavioral hints injected per model family
  { kind: "unenumerated" }, // output-voice overlays applied per call
  { kind: "unenumerated" }, // MCP tool-definition prompt content
]

async function fileProvenance(kind: HostProvenanceSource["kind"], path: string): Promise<HostProvenanceSource | null> {
  try {
    const file = Bun.file(path)
    if (!(await file.exists())) return null
    const bytes = new Uint8Array(await file.arrayBuffer())
    return { kind, path, sha256: "sha256:" + Bun.SHA256.hash(bytes, "hex") }
  } catch {
    return null
  }
}

export async function computeHostPromptProvenance(laneId: string, cwd = process.cwd()): Promise<HostProvenance> {
  const sources: HostProvenanceSource[] = []
  const agentCandidates = [
    `${process.env.HOME ?? ""}/.config/opencode/agents/concord-${laneId}.md`,
    `${cwd}/.opencode/agents/concord-${laneId}.md`,
  ]
  for (const candidate of agentCandidates) {
    const source = await fileProvenance("agent_definition", candidate)
    if (source) {
      sources.push(source)
      break
    }
  }
  let dir = cwd
  for (let depth = 0; depth < 8 && sources.filter(s => s.kind === "agents_md").length < 4; depth++) {
    const source = await fileProvenance("agents_md", `${dir}/AGENTS.md`)
    if (source) sources.push(source)
    const parent = dir.slice(0, dir.lastIndexOf("/"))
    if (!parent || parent === dir) break
    dir = parent
  }
  const declared = (process.env.CONCORD_HOST_INSTRUCTIONS ?? "").split(":").filter(Boolean).slice(0, 16)
  for (const path of declared) {
    const source = await fileProvenance("instruction_file", path)
    if (source) sources.push(source)
  }
  sources.push(...UNENUMERATED_SURFACES)
  const manifest = sources.map(source => [source.kind, source.path ?? "", source.sha256 ?? ""].join("\n")).join("\n---\n")
  return { digest: "sha256:" + Bun.SHA256.hash(manifest, "hex"), sources: sources.slice(0, 32) }
}

export async function dispatchWorker(packet: unknown, options: { signal?: AbortSignal; runner?: DispatchRunner; readbackRunner?: DispatchRunner; evidenceRunner?: DispatchRunner; binary?: string; concordBinary?: string; credentials?: CredentialStore } = {}): Promise<AgentResultEnvelope> {
  if (!validateAgentLanePacket(packet)) return errorEnvelope(null, isRecord(packet) ? packet as Partial<AgentLanePacket> : {}, "error", "invalid_input", "agent lane packet failed the closed packet schema", "retry_same_request")
  const lane = laneForPacket(packet)
  if (!lane) return errorEnvelope(null, packet, "error", "invalid_input", "lane identity or digest is not registered", "retry_same_request")
  const policy = policyForLane(lane)
  if (!policy || policy.preferred_model !== lane.pinned_model || policy.resolution_set[0] !== lane.pinned_model) return errorEnvelope(lane, packet, "error", "invalid_input", "lane capability class has no matching routing policy", "contact_operator")
  if (!lane.pinned_model || !MODEL_PATTERN.test(lane.pinned_model)) return errorEnvelope(lane, packet, "error", "invalid_input", "registered lane has no valid pinned model", "contact_operator")
  const signal = options.signal ?? new AbortController().signal
  const childRunner = options.runner ?? runner
  const binary = options.binary ?? "opencode"
  const argv = [binary, "run", "--agent", `concord-${lane.id}`, "--model", lane.pinned_model, "--format", "json", JSON.stringify(packet)]
  let result: { exitCode: number; stdout: string; stderr: string; fallbackExhausted?: boolean }
  try { result = await childRunner.run(argv, "", signal) } catch (error) {
    return errorEnvelope(lane, packet, "blocked", "blocked", String(error), "retry_same_request")
  }
  if (Buffer.byteLength(result.stdout) > MAX_OUTPUT_BYTES) return errorEnvelope(lane, packet, "error", "error", "worker output exceeded the bounded adapter limit", "adjust_budget")
  if (result.exitCode !== 0) {
    if (hostReportedFallbackExhausted(result)) return errorEnvelope(lane, packet, "blocked", "blocked", result.stderr.slice(0, MAX_ERROR_BYTES) || "declared routing-policy resolution set was exhausted", "retry_same_request")
    return errorEnvelope(lane, packet, "error", "error", result.stderr.slice(0, MAX_ERROR_BYTES) || "OpenCode worker spawn failed without fallback exhaustion evidence", "reconcile_operation")
  }
  const runMetadata = readRunSessionMetadata(result.stdout)
  if (!runMetadata) return errorEnvelope(lane, packet, "error", "error", "worker output did not contain one typed session identity", "reconcile_operation")
  const readbackRunner = options.readbackRunner ?? options.runner ?? runner
  let exported: { exitCode: number; stdout: string; stderr: string }
  try { exported = await readbackRunner.run([binary, "export", runMetadata.session_id, "--sanitize"], "", signal) } catch (error) {
    return errorEnvelope(lane, packet, "error", "error", String(error), "reconcile_operation")
  }
  if (exported.exitCode !== 0) return errorEnvelope(lane, packet, "error", "error", exported.stderr.slice(0, MAX_ERROR_BYTES) || "OpenCode session export failed without diagnostic output", "reconcile_operation")
  const readback = readExportSessionMetadata(exported.stdout, runMetadata.session_id)
  if (!readback) return errorEnvelope(lane, packet, "error", "error", "OpenCode session export did not contain one typed executing-model readback", "reconcile_operation")
  const metadata: SessionMetadata = { ...readback, fallback_reason: runMetadata.fallback_reason }
  const resolutionIndex = policy.resolution_set.indexOf(metadata.readback_model)
  if (resolutionIndex < 0) return errorEnvelope(lane, packet, "error", "model_identity_mismatch", "host readback model is outside the declared routing-policy resolution set", "reconcile_operation")
  const isFallback = resolutionIndex > 0
  const envelope = baseEnvelope(lane, packet, isFallback ? "fallback" : "ok")
  envelope.readback_model = metadata.readback_model
  envelope.session_id = metadata.session_id
  envelope.output = result.stdout
  envelope.resolved_model = metadata.readback_model
  envelope.resolution_role = isFallback ? "fallback" : "preferred"
  envelope.fallback_reason = isFallback ? metadata.fallback_reason ?? "other" : ""
  if (isFallback) envelope.error = { kind: "fallback", retry_safe: false, recovery_action: "reconcile_operation", message: "host resolved a declared fallback model" }

  // CD-0017 D5: a worker attempt is durable evidence, not an in-memory envelope.
  // worker-complete binds to the dispatched attempt row, so the dispatch event
  // must land first. resolved_model is taken from readback because the host
  // expresses a fallback as a re-prompted message carrying the fallback model —
  // there is one model signal, which D5 accepts as legal fallback evidence.
  // A caller-injected runner controls process execution wholesale, so evidence
  // defaults to that same transport unless a distinct evidenceRunner is given.
  const cliRunner = options.evidenceRunner ?? options.runner ?? evidenceRunner
  const cli = concordBinaryPath(options.concordBinary)
  const credentials = options.credentials ?? defaultCredentials
  const provenance = await computeHostPromptProvenance(lane.id)
  let dispatchAssertion: Record<string, unknown>
  let completionAssertion: Record<string, unknown>
  try {
    dispatchAssertion = await signWorkerEvidence(credentials, {
      verb: "worker-dispatch",
      work_id: packet.work_id,
      attempt_id: packet.attempt_id,
      lane_id: lane.id,
      lane_version: lane.version,
      lane_digest: lane.digest,
      routing_policy_version: routingPolicyVersion,
      routing_policy_digest: routingPolicyManifestDigest,
      resolved_model: envelope.resolved_model,
      host_provenance_digest: provenance.digest,
    })
    completionAssertion = await signWorkerEvidence(credentials, {
      verb: "worker-complete",
      work_id: packet.work_id,
      attempt_id: packet.attempt_id,
      lane_id: lane.id,
      lane_version: lane.version,
      lane_digest: lane.digest,
      routing_policy_version: routingPolicyVersion,
      routing_policy_digest: routingPolicyManifestDigest,
      readback_model: metadata.readback_model,
    })
  } catch (error) {
    // Without a credential the adapter cannot authorize evidence, and evidence
    // that cannot be recorded is never reported as a successful run.
    return errorEnvelope(lane, packet, "error", "error", String(error).slice(0, MAX_ERROR_BYTES), "contact_operator")
  }
  const dispatchFailure = await recordWorkerEvent(cliRunner, cli, "worker-dispatch", {
    event_id: crypto.randomUUID(),
    work_id: packet.work_id,
    attempt_id: packet.attempt_id,
    lane_id: lane.id,
    lane_version: lane.version,
    lane_digest: lane.digest,
    routing_policy_version: routingPolicyVersion,
    routing_policy_digest: routingPolicyManifestDigest,
    resolved_model: envelope.resolved_model,
    resolution_role: envelope.resolution_role,
    fallback_reason: envelope.fallback_reason,
    packet_schema_version: PACKET_SCHEMA_VERSION,
    report_schema_version: REPORT_SCHEMA_VERSION,
    host_provenance: provenance,
    assertion: dispatchAssertion,
  }, signal)
  if (dispatchFailure) return errorEnvelope(lane, packet, "error", "error", dispatchFailure, "reconcile_operation")

  const completionFailure = await recordWorkerEvent(cliRunner, cli, "worker-complete", {
    event_id: crypto.randomUUID(),
    work_id: packet.work_id,
    attempt_id: packet.attempt_id,
    readback_model: metadata.readback_model,
    report_schema_version: REPORT_SCHEMA_VERSION,
    assertion: completionAssertion,
  }, signal)
  if (completionFailure) return errorEnvelope(lane, packet, "error", "error", completionFailure, "reconcile_operation")

  return envelope
}
