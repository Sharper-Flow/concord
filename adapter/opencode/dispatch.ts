import { agentLanePacketSchema, agentLanes, routingPolicies, routingPolicyManifestDigest, routingPolicyVersion, type AgentLane } from "./generated-agent-lanes"

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

export function configureWorkerDispatch(overrides: { runner?: DispatchRunner; evidenceRunner?: DispatchRunner } = {}): void {
  runner = overrides.runner ?? defaultRunner
  evidenceRunner = overrides.evidenceRunner ?? defaultRunner
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

function modelFromValue(value: unknown): string | null {
  if (typeof value === "string" && MODEL_PATTERN.test(value)) return value
  if (!isRecord(value)) return null
  if (typeof value.providerID === "string" && typeof value.modelID === "string") {
    const model = `${value.providerID}/${value.modelID}`
    return MODEL_PATTERN.test(model) ? model : null
  }
  if (typeof value.provider_id === "string" && typeof value.model_id === "string") {
    const model = `${value.provider_id}/${value.model_id}`
    return MODEL_PATTERN.test(model) ? model : null
  }
  return null
}

function walkMetadata(value: unknown, models: string[], sessions: string[]): void {
  if (Array.isArray(value)) {
    for (const item of value) walkMetadata(item, models, sessions)
    return
  }
  if (!isRecord(value)) return
  const directModel = modelFromValue(value.model)
  if (directModel) models.push(directModel)
  const nestedModel = isRecord(value.message) ? modelFromValue(value.message.model) : modelFromValue(value.message)
  if (nestedModel) models.push(nestedModel)
  for (const key of ["sessionID", "sessionId", "session_id"]) if (typeof value[key] === "string") sessions.push(value[key] as string)
  for (const [key, child] of Object.entries(value)) if (key !== "model") walkMetadata(child, models, sessions)
}

function fallbackReasonFromOutput(stdout: string): SessionMetadata["fallback_reason"] {
  const reasons = new Set<string>()
  for (const line of stdout.split("\n")) {
    if (!line.trim()) continue
    try {
      const value = JSON.parse(line)
      const visit = (node: unknown): void => {
        if (Array.isArray(node)) { for (const item of node) visit(item); return }
        if (!isRecord(node)) return
        if (typeof node.reason === "string") reasons.add(node.reason)
        for (const child of Object.values(node)) visit(child)
      }
      visit(value)
    } catch { return "other" }
  }
  for (const reason of reasons) {
    if (reason === "account_rate_limit" || reason === "rate_limit") return "rate_limit"
    if (reason === "provider_unavailable" || reason === "provider_error") return "provider_unavailable"
    if (reason === "budget_exhausted" || reason === "quota_exhausted") return "budget_exhausted"
  }
  return reasons.size > 0 ? "other" : null
}

export function readSessionMetadata(stdout: string): SessionMetadata | null {
  if (Buffer.byteLength(stdout) > MAX_OUTPUT_BYTES) return null
  const models: string[] = []
  const sessions: string[] = []
  for (const line of stdout.split("\n")) {
    if (!line.trim()) continue
    try { walkMetadata(JSON.parse(line), models, sessions) } catch { return null }
  }
  const readbackModel = models.at(-1)
  if (!readbackModel) return null
  return { readback_model: readbackModel, session_id: sessions[0] ?? null, fallback_reason: fallbackReasonFromOutput(stdout) }
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
      const visit = (node: unknown): boolean => {
        if (Array.isArray(node)) return node.some(visit)
        if (!isRecord(node)) return false
        if (node.fallback_exhausted === true || node.reason === "fallback_exhausted") return true
        return Object.values(node).some(visit)
      }
      if (visit(value)) return true
    } catch { return false }
  }
  return false
}

function concordBinaryPath(override?: string): string {
  return override ?? process.env.CONCORD_BIN ?? "concord"
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

export async function dispatchWorker(packet: unknown, options: { signal?: AbortSignal; runner?: DispatchRunner; evidenceRunner?: DispatchRunner; binary?: string; concordBinary?: string } = {}): Promise<AgentResultEnvelope> {
  if (!validateAgentLanePacket(packet)) return errorEnvelope(null, isRecord(packet) ? packet as Partial<AgentLanePacket> : {}, "error", "invalid_input", "agent lane packet failed the closed packet schema", "retry_same_request")
  const lane = laneForPacket(packet)
  if (!lane) return errorEnvelope(null, packet, "error", "invalid_input", "lane identity or digest is not registered", "retry_same_request")
  const policy = policyForLane(lane)
  if (!policy || policy.preferred_model !== lane.pinned_model || policy.resolution_set[0] !== lane.pinned_model) return errorEnvelope(lane, packet, "error", "invalid_input", "lane capability class has no matching routing policy", "contact_operator")
  if (!lane.pinned_model || !MODEL_PATTERN.test(lane.pinned_model)) return errorEnvelope(lane, packet, "error", "invalid_input", "registered lane has no valid pinned model", "contact_operator")
  const signal = options.signal ?? new AbortController().signal
  const childRunner = options.runner ?? runner
  const argv = [options.binary ?? "opencode", "run", "--agent", `concord-${lane.id}`, "--model", lane.pinned_model, "--format", "json", JSON.stringify(packet)]
  let result: { exitCode: number; stdout: string; stderr: string; fallbackExhausted?: boolean }
  try { result = await childRunner.run(argv, "", signal) } catch (error) {
    return errorEnvelope(lane, packet, "blocked", "blocked", String(error), "retry_same_request")
  }
  if (Buffer.byteLength(result.stdout) > MAX_OUTPUT_BYTES) return errorEnvelope(lane, packet, "error", "error", "worker output exceeded the bounded adapter limit", "adjust_budget")
  if (result.exitCode !== 0) {
    if (hostReportedFallbackExhausted(result)) return errorEnvelope(lane, packet, "blocked", "blocked", result.stderr.slice(0, MAX_ERROR_BYTES) || "declared routing-policy resolution set was exhausted", "retry_same_request")
    return errorEnvelope(lane, packet, "error", "error", result.stderr.slice(0, MAX_ERROR_BYTES) || "OpenCode worker spawn failed without fallback exhaustion evidence", "reconcile_operation")
  }
  const metadata = readSessionMetadata(result.stdout)
  if (!metadata) return errorEnvelope(lane, packet, "error", "error", "worker output did not contain exactly one executing model readback", "reconcile_operation")
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
  }, signal)
  if (dispatchFailure) return errorEnvelope(lane, packet, "error", "error", dispatchFailure, "reconcile_operation")

  const completionFailure = await recordWorkerEvent(cliRunner, cli, "worker-complete", {
    event_id: crypto.randomUUID(),
    work_id: packet.work_id,
    attempt_id: packet.attempt_id,
    readback_model: metadata.readback_model,
    report_schema_version: REPORT_SCHEMA_VERSION,
  }, signal)
  if (completionFailure) return errorEnvelope(lane, packet, "error", "error", completionFailure, "reconcile_operation")

  return envelope
}
