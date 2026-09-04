import { sign as signBytes } from "node:crypto"
import { agentLanePacketSchema, agentLaneReportSchema, agentLanes, type AgentLane } from "./generated-agent-lanes"
import { maxEnvelopeBytes } from "./generated-contracts"
import { SecretToolCredentialStore, b64, clientRef, privateKeyObject, randomNonce, type CredentialStore } from "./credentials"
import { dispatchWindows, DispatchWindowError, TASK_TOOL_ID, type DispatchWindows } from "./dispatch-window"
import { readTaskResult } from "./task-result"

export const MAX_OUTPUT_BYTES = 65_536
const MAX_ERROR_BYTES = 8_192
const MAX_CLI_INPUT_BYTES = 65_536
// worker.failed detail is bounded at 1..4096 by validateWorkerFailedPayload in
// internal/store/worker_lanes.go; a longer detail would be refused at the fold.
const MAX_FAILURE_DETAIL_BYTES = 4_096
const PACKET_SCHEMA_VERSION = "1.0"
const REPORT_SCHEMA_VERSION = "1.0"

export interface DispatchRunner {
  run(argv: string[], input: string, signal: AbortSignal): Promise<{ exitCode: number; stdout: string; stderr: string }>
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

// AgentLaneReport mirrors contracts/agent-lane-report.schema.json, which the
// generator embeds as agentLaneReportSchema. The schema, not this type, is what
// a worker's output is validated against (CD-0056 D7).
export interface AgentLaneReportEvidence {
  obligation: string
  detail: string
}

export interface AgentLaneReport {
  schema_version: "1.0"
  attempt_id: string
  lane_id: string
  lane_version: number
  lane_digest: string
  readback_model: string
  status: "completed" | "failed"
  evidence: AgentLaneReportEvidence[]
}

export interface SessionMetadata {
  readback_model: string
  readback_agent: string
  session_id: string | null
}

export interface RunSessionMetadata {
  session_id: string
}

export interface RunLineMetadata extends RunSessionMetadata {
  official: boolean
  completed: boolean
}

export interface RunSessionObservation {
  sessions: Set<string>
  officialEvents: number
  completed: boolean
  refusal?: RunStreamRefusal
}

// RunStreamRefusal names why a run stream yielded no single completed session
// identity. Each cause carries a different operator response, so they stay
// distinct: an oversized stream ran to completion and overran a bound, while
// no_official_events means the stream never identified a session at all.
export type RunStreamRefusal =
  | "output_exceeded_bound"
  | "malformed_event"
  | "no_official_events"
  | "no_completion_event"
  | "multiple_session_identities"

export type RunSessionRead =
  | { ok: true; metadata: RunSessionMetadata }
  | { ok: false; refusal: RunStreamRefusal }

// One phrase per refusal, so the run path and the worker path name the same
// cause identically and an operator reading either can tell them apart.
export const runStreamRefusalMessage: Record<RunStreamRefusal, string> = {
  output_exceeded_bound: "exceeded the bounded adapter limit",
  malformed_event: "carried an official run event with no typed session identity",
  no_official_events: "carried no official run event",
  no_completion_event: "ended before a step completed",
  multiple_session_identities: "carried more than one session identity",
}

// An oversized stream is a budget problem and replaying it reproduces the
// overrun, so only that refusal routes to adjust_budget.
export const runStreamRefusalRecovery: Record<RunStreamRefusal, NonNullable<AgentResultEnvelope["error"]>["recovery_action"]> = {
  output_exceeded_bound: "adjust_budget",
  malformed_event: "reconcile_operation",
  no_official_events: "reconcile_operation",
  no_completion_event: "reconcile_operation",
  multiple_session_identities: "reconcile_operation",
}

// DispatchAuthorizer asks the core to authorize one dispatch_worker action and
// returns the core response envelope. The caller owns the transport, so the
// adapter never holds the authorization decision (CD-0017 D2, CD-0059 D1).
export type DispatchAuthorizer = (request: { work_id: string; attempt_id: string }) => Promise<Record<string, unknown>>

export interface AgentResultEnvelope {
  schema_version: "1.0"
  outcome: "ok" | "blocked" | "error"
  lane: { id: string; version: number; digest: string }
  agent: string
  readback_model: string | null
  session_id: string | null
  output?: string
  // CD-0102 D1. A dispatch returns before the worker runs, so an authorized
  // dispatch reports that the window is open and the host must now issue the
  // Task call. A completed attempt never carries this field.
  dispatch_state?: "awaiting_worker"
  error?: {
    // `unauthorized_dispatch` is the core refusing the dispatch_worker action.
    // `transport_failure` is the adapter being unable to ask. The two are
    // separate members because a refusal is the authorization boundary working
    // and a transport fault is the adapter being misconfigured; collapsing
    // them tells an operator to seek permission for a wiring defect.
    kind: "invalid_input" | "blocked" | "error" | "invalid_report" | "agent_identity_mismatch" | "unauthorized_dispatch" | "transport_failure"
    retry_safe: boolean
    recovery_action: "retry_same_request" | "adjust_budget" | "contact_operator" | "reconcile_operation"
    message: string
  }
}

function withHostBoundedOutput(envelope: AgentResultEnvelope, output: string): AgentResultEnvelope | null {
  const candidate = { ...envelope, output }
  return Buffer.byteLength(JSON.stringify(candidate)) <= maxEnvelopeBytes ? candidate : null
}

export const defaultRunner: DispatchRunner = {
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

const defaultCredentials: CredentialStore = new SecretToolCredentialStore()

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}
// resolveSchemaRef walks a same-document JSON pointer. Only local pointers are
// supported: the contracts are self-contained, and an unresolvable pointer is a
// validation failure rather than a silently skipped keyword.
function resolveSchemaRef(root: any, ref: string): any {
  if (typeof ref !== "string" || !ref.startsWith("#")) return undefined
  if (ref === "#") return root
  if (!ref.startsWith("#/")) return undefined
  let node = root
  for (const raw of ref.slice(2).split("/")) {
    if (!node || typeof node !== "object") return undefined
    node = node[raw.replace(/~1/g, "/").replace(/~0/g, "~")]
  }
  return node
}

// validateSchema is the adapter's closed validator for the generated lane
// contracts. It is deliberately a subset of JSON Schema 2020-12 — only the
// keywords those contracts use — and it fails closed on anything it cannot
// resolve. `failures` collects the first failure on each branch so a caller can
// name the field that broke rather than reporting a bare boolean.
function validateSchema(schema: any, value: unknown, root: any, path = "", failures?: string[]): boolean {
  const fail = (reason: string): false => {
    failures?.push(path ? `${path}: ${reason}` : reason)
    return false
  }
  if (!schema || typeof schema !== "object") return fail("no schema to validate against")
  if (schema.$ref !== undefined) {
    const target = resolveSchemaRef(root, schema.$ref)
    if (!target || typeof target !== "object") return fail(`unresolvable $ref ${String(schema.$ref)}`)
    if (!validateSchema(target, value, root, path, failures)) return false
  }
  if (schema.const !== undefined && JSON.stringify(schema.const) !== JSON.stringify(value)) return fail(`must equal ${JSON.stringify(schema.const)}`)
  if (schema.enum !== undefined) {
    if (!Array.isArray(schema.enum)) return fail("enum keyword is not a list")
    const encoded = JSON.stringify(value)
    if (!schema.enum.some((member: unknown) => JSON.stringify(member) === encoded)) return fail(`${encoded} is outside the closed enum`)
  }
  if (schema.type) {
    const types = Array.isArray(schema.type) ? schema.type : [schema.type]
    const valid = types.some((type: string) => type === "object" ? isRecord(value) : type === "array" ? Array.isArray(value) : type === "integer" ? typeof value === "number" && Number.isInteger(value) : type === "number" ? typeof value === "number" : typeof value === type)
    if (!valid) return fail(`is not of type ${types.join(" | ")}`)
  }
  if (typeof value === "string") {
    if (schema.minLength !== undefined && value.length < schema.minLength) return fail(`is shorter than ${schema.minLength} characters`)
    if (schema.maxLength !== undefined && value.length > schema.maxLength) return fail(`is longer than ${schema.maxLength} characters`)
    if (schema.pattern && !new RegExp(schema.pattern).test(value)) return fail(`does not match ${schema.pattern}`)
  }
  if (typeof value === "number") {
    if (schema.minimum !== undefined && value < schema.minimum) return fail(`is below the minimum ${schema.minimum}`)
    if (schema.maximum !== undefined && value > schema.maximum) return fail(`is above the maximum ${schema.maximum}`)
  }
  if (isRecord(value)) {
    const properties = schema.properties ?? {}
    const missing = (schema.required ?? []).filter((key: string) => !(key in value))
    if (missing.length > 0) return fail(`is missing required propert${missing.length === 1 ? "y" : "ies"} ${missing.join(", ")}`)
    for (const [key, child] of Object.entries(properties)) if (key in value && !validateSchema(child, value[key], root, path ? `${path}.${key}` : key, failures)) return false
    if (schema.additionalProperties === false) {
      const extra = Object.keys(value).filter((key) => !(key in properties))
      if (extra.length > 0) return fail(`carries undeclared propert${extra.length === 1 ? "y" : "ies"} ${extra.join(", ")}`)
    }
  }
  if (Array.isArray(value)) {
    if (schema.minItems !== undefined && value.length < schema.minItems) return fail(`carries fewer than ${schema.minItems} item(s)`)
    if (schema.maxItems !== undefined && value.length > schema.maxItems) return fail(`carries more than ${schema.maxItems} item(s)`)
    if (schema.uniqueItems && new Set(value.map((item) => JSON.stringify(item))).size !== value.length) return fail("carries duplicate items")
    if (schema.items) {
      for (let index = 0; index < value.length; index++) {
        if (!validateSchema(schema.items, value[index], root, `${path}[${index}]`, failures)) return false
      }
    }
  }
  return true
}

export function validateAgentLanePacket(value: unknown): value is AgentLanePacket {
  return validateSchema(agentLanePacketSchema, value, agentLanePacketSchema)
}

export function validateAgentLaneReport(value: unknown, failures?: string[]): value is AgentLaneReport {
  return validateSchema(agentLaneReportSchema, value, agentLaneReportSchema, "", failures)
}

// validateAgainstSchema validates a value against a self-contained schema
// document, resolving any `$ref` inside that same document.
export function validateAgainstSchema(schema: unknown, value: unknown, failures?: string[]): boolean {
  return validateSchema(schema, value, schema, "", failures)
}

const RUN_EVENT_TYPES = new Set(["step_start", "step_finish", "text", "reasoning", "tool_use", "error"])

// hostStatusMetadata extracts a session identifier from a host plugin log
// line that shares stdout with the run stream. The host plugin emits typed
// message-updated events whose sessionID lets the adapter associate its log
// with the dispatched run.
function hostStatusMetadata(value: Record<string, unknown>): string | null {
  if (value.type !== "message.updated" || !isRecord(value.properties)) return null
  const properties = value.properties
  if (typeof properties.sessionId === "string") return properties.sessionId
  if (typeof properties.sessionID === "string") return properties.sessionID
  return null
}

// readRunLineMetadata extracts the first durable session identity that the
// OpenCode stream makes observable. Invalid official events fail closed.
export function readRunLineMetadata(line: string): RunLineMetadata | null {
  let value: unknown
  try { value = JSON.parse(line) } catch { return null }
  if (!isRecord(value) || typeof value.type !== "string") return null
  if (RUN_EVENT_TYPES.has(value.type)) {
    if (typeof value.timestamp !== "number" || typeof value.sessionID !== "string" || value.sessionID.length === 0) throw new Error("official run event lacks a typed session identity")
    return { session_id: value.sessionID, official: true, completed: value.type === "step_finish" && isRecord(value.part) && value.part.reason === "stop" }
  }
  const sessionID = hostStatusMetadata(value)
  return sessionID ? { session_id: sessionID, official: false, completed: false } : null
}

export function createRunSessionObservation(): RunSessionObservation {
  return { sessions: new Set(), officialEvents: 0, completed: false }
}

export function observeRunSessionLine(observation: RunSessionObservation, line: string): RunLineMetadata | null {
  if (observation.refusal) return null
  let metadata: RunLineMetadata | null
  try { metadata = readRunLineMetadata(line) } catch {
    observation.refusal = "malformed_event"
    return null
  }
  if (!metadata) return null
  if (metadata.official) observation.officialEvents++
  if (metadata.completed) observation.completed = true
  observation.sessions.add(metadata.session_id)
  if (observation.sessions.size > 1) observation.refusal = "multiple_session_identities"
  return metadata
}

export function readRunSessionMetadata(observation: RunSessionObservation): RunSessionRead
export function readRunSessionMetadata(stdout: string): RunSessionRead
export function readRunSessionMetadata(input: string | RunSessionObservation): RunSessionRead {
  if (typeof input === "string") {
    if (Buffer.byteLength(input) > MAX_OUTPUT_BYTES) return { ok: false, refusal: "output_exceeded_bound" }
    const observation = createRunSessionObservation()
    for (const line of input.split("\n")) if (line.trim()) observeRunSessionLine(observation, line)
    return readRunSessionMetadata(observation)
  }
  if (input.refusal) return { ok: false, refusal: input.refusal }
  if (input.sessions.size > 1) return { ok: false, refusal: "multiple_session_identities" }
  if (input.officialEvents === 0) return { ok: false, refusal: "no_official_events" }
  if (!input.completed) return { ok: false, refusal: "no_completion_event" }
  // An official event always carries a typed identity, so officialEvents > 0
  // with no second identity leaves exactly one.
  return { ok: true, metadata: { session_id: [...input.sessions][0] } }
}

export function readExportSessionMetadata(stdout: string, expectedSessionID: string): Pick<SessionMetadata, "readback_model" | "readback_agent" | "session_id"> | null {
  if (Buffer.byteLength(stdout) > MAX_OUTPUT_BYTES) return null
  let value: unknown
  try { value = JSON.parse(stdout) } catch { return null }
  if (!isRecord(value) || !isRecord(value.info) || value.info.id !== expectedSessionID || !Array.isArray(value.messages)) return null
  const seen = new Set<string>()
  const assistants: { id: string; created: number; model: string; agent: string }[] = []
  for (const message of value.messages) {
    if (!isRecord(message) || !isRecord(message.info) || !Array.isArray(message.parts)) return null
    const info = message.info
    if (typeof info.id !== "string" || typeof info.sessionID !== "string" || info.sessionID !== expectedSessionID || !isRecord(info.time) || typeof info.time.created !== "number" || seen.has(info.id)) return null
    seen.add(info.id)
    if (info.role === "user") continue
    if (info.role !== "assistant" || typeof info.providerID !== "string" || typeof info.modelID !== "string" || typeof info.agent !== "string") return null
    assistants.push({ id: info.id, created: info.time.created, model: `${info.providerID}/${info.modelID}`, agent: info.agent })
  }
  assistants.sort((left, right) => left.created - right.created || left.id.localeCompare(right.id))
  const latest = assistants.at(-1)
  return latest ? { readback_model: latest.model, readback_agent: latest.agent, session_id: expectedSessionID } : null
}

// readRunTextParts returns the model's message text in emission order. The host
// writes one JSON run event per stdout line, and the assistant's text is carried
// only by `text` events, at `part.text`. No other event or key on the stream
// carries it.
export function readRunTextParts(stdout: string): string[] {
  const texts: string[] = []
  for (const line of stdout.split("\n")) {
    const trimmed = line.trim()
    if (!trimmed.startsWith("{")) continue
    let parsed: unknown
    try { parsed = JSON.parse(trimmed) } catch { continue }
    if (!isRecord(parsed) || parsed.type !== "text" || !isRecord(parsed.part)) continue
    const part = parsed.part
    if (part.type !== "text" || typeof part.text !== "string") continue
    texts.push(part.text)
  }
  return texts
}

// stripReportFence removes one Markdown code fence wrapping the whole text. A
// model instructed to return only JSON frequently fences it. Nothing beyond a
// single enclosing fence is unwrapped: extracting JSON out of surrounding prose
// is heuristic salvage, and a report that needs salvaging fails closed.
function stripReportFence(text: string): string {
  const trimmed = text.trim()
  if (!trimmed.startsWith("```") || !trimmed.endsWith("```") || trimmed.length < 6) return trimmed
  const firstBreak = trimmed.indexOf("\n")
  if (firstBreak < 0) return trimmed
  const info = trimmed.slice(3, firstBreak).trim()
  if (info.length > 0 && !/^[A-Za-z0-9_-]+$/.test(info)) return trimmed
  return trimmed.slice(firstBreak + 1, trimmed.length - 3).trim()
}

export type WorkerReportScan = { report: Record<string, unknown> | null; malformed: boolean }

// readWorkerReport locates the worker's report in the host run stream. A worker
// emits several text parts, so neither the first nor a concatenation is the
// report: the last text part that parses as a JSON object is, because that is
// the worker's final answer and every earlier part is working prose it
// superseded. `malformed` records that a part that announced itself as JSON did
// not parse, which distinguishes a worker that returned nothing from one that
// returned something broken.
export function readWorkerReport(stdout: string): WorkerReportScan {
  return scanReportTexts(readRunTextParts(stdout))
}

// scanReportTexts holds the scan itself, over message texts in emission order.
// The native task route supplies the worker's single final body and the run
// stream supplies every text part, and both admit a report the same way.
export function scanReportTexts(texts: string[]): WorkerReportScan {
  let malformed = false
  const found: Record<string, unknown>[] = []
  for (const text of texts) {
    const candidate = stripReportFence(text)
    if (!candidate.startsWith("{")) continue
    let parsed: unknown
    try { parsed = JSON.parse(candidate) } catch { malformed = true; continue }
    if (isRecord(parsed)) found.push(parsed)
    else malformed = true
  }
  return { report: found.at(-1) ?? null, malformed }
}

const REPORT_IDENTITY_FIELDS = ["attempt_id", "lane_id", "lane_version", "lane_digest"] as const

// resolveWorkerReport is the CD-0056 D7 admission boundary: a report is admitted
// only when it is present, parses, satisfies the closed report schema, and
// echoes the identity of the packet it was dispatched for. Anything else returns
// a bounded detail that names what was wrong, and the caller records
// worker.failed with the invalid_report kind rather than a completion.
export function resolveWorkerReport(stdout: string, packet: AgentLanePacket): { report: AgentLaneReport } | { detail: string } {
  return admitWorkerReport(readWorkerReport(stdout), packet)
}

// resolveWorkerReportFromText admits a report from one message body, which is
// what the native task route carries: the host has already resolved the
// worker's final text part before it renders the result (CD-0102 D5).
export function resolveWorkerReportFromText(text: string, packet: AgentLanePacket): { report: AgentLaneReport } | { detail: string } {
  return admitWorkerReport(scanReportTexts([text]), packet)
}

function admitWorkerReport(scan: WorkerReportScan, packet: AgentLanePacket): { report: AgentLaneReport } | { detail: string } {
  if (!scan.report) {
    return { detail: scan.malformed
      ? "worker output carried a malformed JSON document and no agent-lane-report.v1 report"
      : "worker output carried no agent-lane-report.v1 report" }
  }
  const failures: string[] = []
  if (!validateAgentLaneReport(scan.report, failures)) {
    return { detail: `worker report failed the closed agent-lane-report.v1 schema: ${failures[0] ?? "unknown field"}` }
  }
  for (const field of REPORT_IDENTITY_FIELDS) {
    if (scan.report[field] !== packet[field]) {
      return { detail: `worker report ${field} ${JSON.stringify(scan.report[field])} does not match dispatched packet ${JSON.stringify(packet[field])}` }
    }
  }
  return { report: scan.report }
}

// workerReportedFailureDetail renders a `failed` report's own evidence as the
// bounded failure detail. The worker's failure is recorded as the worker's, not
// reclassified as an invalid report.
function workerReportedFailureDetail(report: AgentLaneReport): string {
  const rendered = report.evidence.map((entry) => `${entry.obligation}: ${entry.detail}`).join("; ")
  return `worker reported failure: ${rendered}`.slice(0, MAX_FAILURE_DETAIL_BYTES)
}

function laneForPacket(packet: AgentLanePacket): AgentLane | null {
  return agentLanes.find((lane) => lane.id === packet.lane_id && lane.version === packet.lane_version && lane.digest === packet.lane_digest) ?? null
}

function baseEnvelope(lane: AgentLane | null, packet: Partial<AgentLanePacket>, outcome: AgentResultEnvelope["outcome"]): AgentResultEnvelope {
  const id = lane?.id ?? String(packet.lane_id ?? "")
  return { schema_version: "1.0", outcome, lane: { id, version: lane?.version ?? Number(packet.lane_version ?? 0), digest: lane?.digest ?? String(packet.lane_digest ?? "") }, agent: lane ? `concord-${lane.id}` : `concord-${id}`, readback_model: null, session_id: null }
}

function errorEnvelope(lane: AgentLane | null, packet: Partial<AgentLanePacket>, outcome: "blocked" | "error", kind: NonNullable<AgentResultEnvelope["error"]>["kind"], message: string, recovery_action: NonNullable<AgentResultEnvelope["error"]>["recovery_action"] = "contact_operator"): AgentResultEnvelope {
  return { ...baseEnvelope(lane, packet, outcome), error: { kind, retry_safe: outcome !== "blocked", recovery_action, message: message.slice(0, MAX_ERROR_BYTES) } }
}

// errorEnvelopeForLane is the public re-export of the private errorEnvelope
// helper. Lane dispatch (CD-0067 D5) constructs AgentResultEnvelope failures
// before any lane has been validated against the registry, so the caller can
// pass lane === null with a partial packet (for example, just {lane_id}); the
// helper mirrors dispatch.ts's internal contract exactly so envelopes
// returned from the dispatch path are structurally indistinguishable from
// envelopes returned here.
export function errorEnvelopeForLane(lane: AgentLane | null, packet: Partial<AgentLanePacket>, outcome: "blocked" | "error", kind: NonNullable<AgentResultEnvelope["error"]>["kind"], message: string, recovery_action: NonNullable<AgentResultEnvelope["error"]>["recovery_action"] = "contact_operator"): AgentResultEnvelope {
  return errorEnvelope(lane, packet, outcome, kind, message, recovery_action)
}

export function concordBinaryPath(override?: string): string {
  return override ?? process.env.CONCORD_BIN ?? "concord"
}

// canonicalWorkerEvidence mirrors CanonicalWorkerEvidenceAssertion in
// internal/agent/worker_evidence.go. The byte sequence is pinned by the shared
// vector at worker-evidence-vector.json, which both sides test against, so a
// drift between the two encoders fails a test rather than weakening the
// boundary. Field order is part of the contract. packet_digest sits between
// host_provenance_digest and issued_at: dispatch signs the core-recorded
// digest there, complete and fail sign an empty packet_digest because their
// window does not bind a packet (CD-0067 D6).
export function canonicalWorkerEvidence(assertion: Record<string, unknown>): Uint8Array {
  const names = ["client_ref", "verb", "work_id", "attempt_id", "lane_id", "lane_version", "lane_digest", "readback_model", "failure_kind", "host_provenance_digest", "packet_digest", "issued_at", "nonce"]
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
// global AGENTS.md, the AGENTS.md chain at spawn cwd, instruction files the
// host config declares, and instruction files declared through
// CONCORD_HOST_INSTRUCTIONS (colon-separated paths) — hashing each. Surfaces
// it cannot enumerate (provider hints, voice overlays, MCP tool prompts) are
// recorded by name as unenumerated. Injection is permitted only when
// recorded: a silent injection change changes this digest and is visible in
// dispatch evidence.
//
// Resolution is deliberately exact rather than faithful. Reproducing the host's
// own resolver — glob semantics, remote fetches, the merge order across every
// config layer — would be a second implementation that drifts from the first.
// An entry this cannot resolve exactly is recorded by name as unenumerated, so
// the manifest never claims to have bound content it did not read, and nothing
// injected is silently absent.
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

// OpenCode resolves its global config directory from OPENCODE_CONFIG_DIR, then
// XDG_CONFIG_HOME, then ~/.config — uniformly on every platform. The adapter
// mirrors that lookup so it binds the same global surfaces the host injects.
function opencodeConfigDir(): string {
  const override = process.env.OPENCODE_CONFIG_DIR
  if (override) return override.replace(/\/+$/, "")
  const home = process.env.HOME ?? ""
  const xdg = process.env.XDG_CONFIG_HOME
  return `${xdg && xdg.length > 0 ? xdg.replace(/\/+$/, "") : `${home}/.config`}/opencode`
}

// jsonc-parser is not available to the adapter and adding a dependency would
// change the release file set, so comments and trailing commas are stripped by
// a scanner that respects string literals. A file this cannot parse is never
// guessed at: the caller records it as unenumerated by path.
function stripJsonc(text: string): string {
  let out = ""
  let inString = false
  let escaped = false
  let comment: "" | "line" | "block" = ""
  for (let i = 0; i < text.length; i++) {
    const ch = text[i]
    const next = text[i + 1]
    if (comment === "line") {
      if (ch === "\n") { comment = ""; out += ch }
      continue
    }
    if (comment === "block") {
      if (ch === "*" && next === "/") { comment = ""; i++ }
      continue
    }
    if (inString) {
      out += ch
      if (escaped) escaped = false
      else if (ch === "\\") escaped = true
      else if (ch === '"') inString = false
      continue
    }
    if (ch === '"') { inString = true; out += ch; continue }
    if (ch === "/" && next === "/") { comment = "line"; i++; continue }
    if (ch === "/" && next === "*") { comment = "block"; i++; continue }
    out += ch
  }
  return out.replace(/,(\s*[}\]])/g, "$1")
}

const CONFIG_FILE_NAMES = ["config.json", "opencode.json", "opencode.jsonc"]
const GLOB_METACHARACTERS = /[*?[\]{}]/

// Every config file OpenCode may merge an instructions array from, bounded so a
// dispatch cost stays fixed: the global directory, an explicit OPENCODE_CONFIG
// file, and the project walk from cwd toward the filesystem root.
function configFileCandidates(cwd: string): string[] {
  const dir = opencodeConfigDir()
  const candidates = CONFIG_FILE_NAMES.map(name => `${dir}/${name}`)
  if (process.env.OPENCODE_CONFIG) candidates.push(process.env.OPENCODE_CONFIG)
  let current = cwd
  for (let depth = 0; depth < 8; depth++) {
    candidates.push(`${current}/opencode.jsonc`, `${current}/opencode.json`)
    candidates.push(`${current}/.opencode/opencode.json`, `${current}/.opencode/opencode.jsonc`)
    const parent = current.slice(0, current.lastIndexOf("/"))
    if (!parent || parent === current) break
    current = parent
  }
  return candidates.slice(0, 48)
}

async function configInstructionEntries(cwd: string): Promise<{ entries: string[]; unreadable: string[] }> {
  const entries: string[] = []
  const unreadable: string[] = []
  for (const candidate of configFileCandidates(cwd)) {
    let parsed: unknown
    try {
      const file = Bun.file(candidate)
      if (!(await file.exists())) continue
      parsed = JSON.parse(stripJsonc(await file.text()))
    } catch {
      unreadable.push(candidate)
      continue
    }
    const declared = isRecord(parsed) ? parsed["instructions"] : null
    if (!Array.isArray(declared)) continue
    for (const entry of declared) {
      if (typeof entry === "string" && entry.length > 0 && !entries.includes(entry)) entries.push(entry)
    }
  }
  return { entries: entries.slice(0, 64), unreadable: unreadable.slice(0, 8) }
}

// CD-0034 admits a surface as bound only when its content is hashed. An entry
// the adapter cannot resolve exactly — a glob, a remote URL, a config it cannot
// parse — is recorded by name as unenumerated rather than dropped, so nothing
// the host injects is silently absent from the manifest.
async function instructionSources(cwd: string): Promise<HostProvenanceSource[]> {
  const sources: HostProvenanceSource[] = []
  const { entries, unreadable } = await configInstructionEntries(cwd)
  for (const path of unreadable) sources.push({ kind: "unenumerated", path })
  for (const entry of entries) {
    if (entry.startsWith("http://") || entry.startsWith("https://")) {
      sources.push({ kind: "unenumerated", path: entry })
      continue
    }
    const expanded = entry.startsWith("~/") ? `${process.env.HOME ?? ""}/${entry.slice(2)}` : entry
    // An absolute glob resolves against one fixed directory, so it can be
    // expanded exactly: no ancestor walking, no remote fetch. The conduct
    // corpus reaches projects this way, and CD-0063 D5 requires a corpus file
    // that reaches an agent to be bound by content hash, not merely named.
    if (expanded.startsWith("/") && GLOB_METACHARACTERS.test(expanded)) {
      const separator = expanded.lastIndexOf("/")
      const dir = expanded.slice(0, separator)
      const pattern = expanded.slice(separator + 1)
      const matches = await Array.fromAsync(new Bun.Glob(pattern).scan({ cwd: dir, onlyFiles: true })).catch(() => [])
      if (matches.length === 0) sources.push({ kind: "unenumerated", path: entry })
      for (const relative of matches.slice(0, 32)) {
        const source = await fileProvenance("instruction_file", `${dir}/${relative}`)
        if (source) sources.push(source)
      }
      continue
    }
    if (GLOB_METACHARACTERS.test(entry)) {
      sources.push({ kind: "unenumerated", path: entry })
      continue
    }
    if (expanded.startsWith("/")) {
      const source = await fileProvenance("instruction_file", expanded)
      sources.push(source ?? { kind: "unenumerated", path: entry })
      continue
    }
    // A relative entry resolves against every ancestor of the spawn directory,
    // so each existing match is bound, and an entry matching nothing anywhere
    // is still named.
    let matched = false
    let dir = cwd
    for (let depth = 0; depth < 8; depth++) {
      const source = await fileProvenance("instruction_file", `${dir}/${expanded}`)
      if (source) { sources.push(source); matched = true }
      const parent = dir.slice(0, dir.lastIndexOf("/"))
      if (!parent || parent === dir) break
      dir = parent
    }
    if (!matched) sources.push({ kind: "unenumerated", path: entry })
  }
  return sources
}

export async function computeHostPromptProvenance(laneId: string, cwd = process.cwd()): Promise<HostProvenance> {
  const sources: HostProvenanceSource[] = []
  const configDir = opencodeConfigDir()
  const agentCandidates = [
    `${configDir}/agents/concord-${laneId}.md`,
    `${cwd}/.opencode/agents/concord-${laneId}.md`,
  ]
  for (const candidate of agentCandidates) {
    const source = await fileProvenance("agent_definition", candidate)
    if (source) {
      sources.push(source)
      break
    }
  }
  // The global AGENTS.md is injected into every session but sits outside the
  // spawn directory's ancestry, so the upward walk below can never reach it.
  const globalAgents = await fileProvenance("agents_md", `${configDir}/AGENTS.md`)
  if (globalAgents) sources.push(globalAgents)
  let dir = cwd
  for (let depth = 0; depth < 8 && sources.filter(s => s.kind === "agents_md").length < 5; depth++) {
    const source = await fileProvenance("agents_md", `${dir}/AGENTS.md`)
    if (source) sources.push(source)
    const parent = dir.slice(0, dir.lastIndexOf("/"))
    if (!parent || parent === dir) break
    dir = parent
  }
  sources.push(...(await instructionSources(cwd)))
  const declared = (process.env.CONCORD_HOST_INSTRUCTIONS ?? "").split(":").filter(Boolean).slice(0, 16)
  for (const path of declared) {
    if (sources.some(source => source.kind === "instruction_file" && source.path === path)) continue
    const source = await fileProvenance("instruction_file", path)
    if (source) sources.push(source)
  }
  sources.push(...UNENUMERATED_SURFACES)
  const manifest = sources.map(source => [source.kind, source.path ?? "", source.sha256 ?? ""].join("\n")).join("\n---\n")
  return { digest: "sha256:" + Bun.SHA256.hash(manifest, "hex"), sources: sources.slice(0, 64) }
}

export async function dispatchWorker(packet: unknown, options: { signal?: AbortSignal; runner?: DispatchRunner; readbackRunner?: DispatchRunner; evidenceRunner?: DispatchRunner; binary?: string; concordBinary?: string; credentials?: CredentialStore; authorize?: DispatchAuthorizer; packetDigest?: string; sessionID?: string; windows?: DispatchWindows } = {}): Promise<AgentResultEnvelope> {
  if (!validateAgentLanePacket(packet)) return errorEnvelope(null, isRecord(packet) ? packet as Partial<AgentLanePacket> : {}, "error", "invalid_input", "agent lane packet failed the closed packet schema", "retry_same_request")
  const lane = laneForPacket(packet)
  if (!lane) return errorEnvelope(null, packet, "error", "invalid_input", "lane identity or digest is not registered", "retry_same_request")
  const signal = options.signal ?? new AbortController().signal

  // CD-0059 D1: authorize before the worker starts, unconditionally. The dispatch_worker
  // workflow action opens the worker attempt window against the current step
  // epoch. The authorizer is supplied by the caller rather than taken from the
  // host ToolContext: ToolContext is the host's tool-call surface and declares
  // no way to reach Concord's core, so an adapter that probed it for one would
  // fail closed on every host and report the absence as a refusal (issue
  // #436). The caller wires this to the same `concord invoke` transport every
  // other operation uses.
  if (typeof options.authorize !== "function") {
    return errorEnvelope(lane, packet as Partial<AgentLanePacket>, "error", "transport_failure", "dispatch authorizer is not configured; dispatch_worker authorization is mandatory before spawn", "contact_operator")
  }
  let response: unknown
  try {
    response = await options.authorize({
      work_id: String((packet as Partial<AgentLanePacket>).work_id ?? ""),
      attempt_id: String((packet as Partial<AgentLanePacket>).attempt_id ?? ""),
    })
  } catch {
    return errorEnvelope(lane, packet as Partial<AgentLanePacket>, "error", "transport_failure", "dispatch authorization transport threw before reaching the core", "contact_operator")
  }
  if (!isRecord(response)) {
    return errorEnvelope(lane, packet as Partial<AgentLanePacket>, "error", "transport_failure", "dispatch authorization returned no core response envelope", "contact_operator")
  }
  if (response.outcome === "error") {
    const errorObj = isRecord(response.error) ? response.error : null
    const message = errorObj && typeof errorObj.message === "string" ? errorObj.message : "dispatch_worker authorization refused"
    return errorEnvelope(lane, packet as Partial<AgentLanePacket>, "error", "unauthorized_dispatch", message, "reconcile_operation")
  }

  // CD-0102 D1/D2: the authorized dispatch opens one single-use window on the
  // calling session. The host, not the adapter, starts the worker: the next
  // Task call from this session has its agent and prompt overwritten with this
  // packet by the plugin hook, and the window closes on that call.
  //
  // CD-0058 D1 still holds and needs no model here. The adapter names the lane
  // executor and never asserts which model runs it; the executing model is read
  // back from the worker session at completion.
  const sessionID = options.sessionID
  if (!sessionID) {
    return errorEnvelope(lane, packet as Partial<AgentLanePacket>, "error", "invalid_input", "dispatch requires the calling session identifier to open an authorization window", "contact_operator")
  }
  const windows = options.windows ?? dispatchWindows()
  try {
    windows.open(sessionID, packet, options.packetDigest ?? "")
  } catch (error) {
    const detail = error instanceof DispatchWindowError ? error.message : String(error)
    return errorEnvelope(lane, packet as Partial<AgentLanePacket>, "error", "error", detail.slice(0, MAX_ERROR_BYTES), "reconcile_operation")
  }
  const directive = baseEnvelope(lane, packet, "ok")
  directive.dispatch_state = "awaiting_worker"
  return directive
}

// completeDispatchedTaskCall is the `tool.execute.after` body. The host runs the
// worker between dispatch and completion (CD-0102 D5), so this is where the
// adapter regains control: it takes the attempt the window put in flight and
// admits the host's rendered task output as that attempt's result.
//
// A call the window never bound is not a worker result, and neither is a call to
// another tool. Both leave the output untouched, because this hook observes every
// tool call in the session.
export async function completeDispatchedTaskCall(
  tool: string,
  sessionID: string,
  output: { output: string },
  options: { windows?: DispatchWindows; signal?: AbortSignal; runner?: DispatchRunner; readbackRunner?: DispatchRunner; evidenceRunner?: DispatchRunner; binary?: string; concordBinary?: string; credentials?: CredentialStore } = {},
): Promise<void> {
  if (tool !== TASK_TOOL_ID) return
  const record = (options.windows ?? dispatchWindows()).takeInFlight(sessionID)
  if (!record) return
  const lane = laneForPacket(record.packet)
  const signal = options.signal ?? new AbortController().signal
  // A packet whose lane no longer resolves cannot be completed, and silence
  // would report the run as if it had been recorded.
  const envelope = lane
    ? await completeWorkerAttempt(lane, record.packet, output.output, { ...options, packetDigest: record.packetDigest }, signal)
    : errorEnvelopeForLane(null, record.packet, "error", "invalid_input", `dispatched lane ${JSON.stringify(record.packet.lane_id)} is not installed at the dispatched version and digest`, "contact_operator")
  if (envelope.outcome === "ok") return
  // The coordinator reads the tool result, not the adapter's return value. A
  // refusal that stayed here would leave a clean-looking lane report beside a
  // store that recorded nothing — the failure this whole route exists to end.
  output.output = `${output.output}\n${JSON.stringify(envelope)}`
}

// completeWorkerAttempt admits a finished worker: it reads executing-model and
// executing-agent evidence from the worker session export, resolves the report,
// signs the dispatch and terminal assertions, and records the attempt outcome.
//
// It takes the worker session identifier and the result body as parameters
// because the host, not the adapter, runs the worker between dispatch and
// completion (CD-0102 D5).
export async function completeWorkerAttempt(
  lane: AgentLane,
  packet: AgentLanePacket,
  taskResult: string,
  options: { signal?: AbortSignal; runner?: DispatchRunner; readbackRunner?: DispatchRunner; evidenceRunner?: DispatchRunner; binary?: string; concordBinary?: string; credentials?: CredentialStore; authorize?: DispatchAuthorizer; packetDigest?: string },
  signal: AbortSignal,
): Promise<AgentResultEnvelope> {
  // The wrapper carries the worker session identifier, so a body that is not a
  // host task result cannot be admitted: without that identifier there is no
  // readback, and without readback there is no executing-model evidence.
  const read = readTaskResult(taskResult)
  if (!read) return errorEnvelope(lane, packet, "error", "invalid_report", "worker result is not a host task result wrapper", "reconcile_operation")
  const workerSessionID = read.sessionID
  const resultBody = read.text
  const binary = options.binary ?? "opencode"
  const readbackRunner = options.readbackRunner ?? options.runner ?? defaultRunner
  let exported: { exitCode: number; stdout: string; stderr: string }
  try { exported = await readbackRunner.run([binary, "export", workerSessionID, "--sanitize"], "", signal) } catch (error) {
    return errorEnvelope(lane, packet, "error", "error", String(error), "reconcile_operation")
  }
  if (exported.exitCode !== 0) return errorEnvelope(lane, packet, "error", "error", exported.stderr.slice(0, MAX_ERROR_BYTES) || "OpenCode session export failed without diagnostic output", "reconcile_operation")
  const readback = readExportSessionMetadata(exported.stdout, workerSessionID)
  if (!readback) return errorEnvelope(lane, packet, "error", "error", "OpenCode session export did not contain one typed executing-model readback", "reconcile_operation")
  // The adapter names the lane executor; the host owns which model executes
  // it (CD-0058 D1). Because the host may also substitute the agent itself —
  // run mode falls back to the default agent when an agent definition is not
  // selectable — the executor identity is asserted against the export the same
  // way model evidence is read from it. A substituted executor never recorded
  // the lane contract, so its output is admitted as no worker's evidence.
  const expectedAgent = `concord-${lane.id}`
  if (readback.readback_agent !== expectedAgent) {
    return errorEnvelope(lane, packet, "error", "agent_identity_mismatch", `executed agent ${JSON.stringify(readback.readback_agent)} does not match the dispatched lane agent ${JSON.stringify(expectedAgent)}`, "contact_operator")
  }
  const base = baseEnvelope(lane, packet, "ok")
  base.readback_model = readback.readback_model
  base.session_id = readback.session_id
  const envelope = withHostBoundedOutput(base, resultBody)
  if (!envelope) return errorEnvelope(lane, packet, "error", "error", "worker result exceeds the pinned host output limit", "adjust_budget")

  // CD-0017 D5: a worker attempt is durable evidence, not an in-memory envelope.
  // worker-complete binds to the dispatched attempt row, so the dispatch event
  // must land first. Readback is recorded on both events because the host may
  // surface a fallback via a re-prompted message, but D5 accepts one model
  // signal — the readback — as the executing-model evidence.
  // A caller-injected runner controls process execution wholesale, so evidence
  // defaults to that same transport unless a distinct evidenceRunner is given.
  const cliRunner = options.evidenceRunner ?? options.runner ?? defaultRunner
  const cli = concordBinaryPath(options.concordBinary)
  const credentials = options.credentials ?? defaultCredentials
  const provenance = await computeHostPromptProvenance(lane.id)

  // CD-0056 D7: the adapter is the only component that sees worker output, so
  // the report is admitted here. A report that is absent, unparseable, invalid,
  // or bound to another packet is a typed failure, never a completion.
  const resolution = resolveWorkerReportFromText(resultBody, packet)
  const terminal: { verb: "worker-complete"; report: AgentLaneReport } | { verb: "worker-fail"; failure_kind: string; detail: string } =
    "detail" in resolution
      ? { verb: "worker-fail", failure_kind: "invalid_report", detail: resolution.detail.slice(0, MAX_FAILURE_DETAIL_BYTES) }
      : resolution.report.status === "failed"
        ? { verb: "worker-fail", failure_kind: "worker_error", detail: workerReportedFailureDetail(resolution.report) }
        : { verb: "worker-complete", report: resolution.report }

  // CD-0067 D6: the dispatch assertion's packet_digest quotes the value
  // the core returned on the dispatch_worker response. The adapter does
  // not compute the digest itself — matching Go's canonicalJSON byte-for-byte
  // from TypeScript would couple the two languages' JSON encoders for
  // arbitrary narrative text. The signing path therefore fails closed
  // when the caller did not pass the digest the core recorded, so an
  // unsigned or empty digest can never reach the evidence boundary.
  if (!options.packetDigest) {
    return errorEnvelope(lane, packet as Partial<AgentLanePacket>, "error", "invalid_input", "dispatch evidence requires the packet digest recorded by the dispatch authorization", "reconcile_operation")
  }
  let dispatchAssertion: Record<string, unknown>
  let terminalAssertion: Record<string, unknown>
  try {
    dispatchAssertion = await signWorkerEvidence(credentials, {
      verb: "worker-dispatch",
      work_id: packet.work_id,
      attempt_id: packet.attempt_id,
      lane_id: lane.id,
      lane_version: lane.version,
      lane_digest: lane.digest,
      readback_model: readback.readback_model,
      host_provenance_digest: provenance.digest,
      packet_digest: options.packetDigest,
    })
    terminalAssertion = terminal.verb === "worker-complete"
      ? await signWorkerEvidence(credentials, {
        verb: "worker-complete",
        work_id: packet.work_id,
        attempt_id: packet.attempt_id,
        lane_id: lane.id,
        lane_version: lane.version,
        lane_digest: lane.digest,
        readback_model: readback.readback_model,
      })
      // Both terminal verbs bind lane identity: the CLI enriches lane_id,
      // lane_version, and lane_digest from the stored attempt row before it
      // compares the assertion (cmd/concord/main.go applyWorkerEvidence), so a
      // failure assertion must claim them too. The field set each verb signs is
      // pinned per verb by worker-evidence-vector.json. packet_digest stays
      // absent from the JSON so the assertion carries exactly the bound fields
      // for that verb; the canonical encoder emits an empty packet_digest slot
      // regardless, which is what the shared vector pins.
      : await signWorkerEvidence(credentials, {
        verb: "worker-fail",
        work_id: packet.work_id,
        attempt_id: packet.attempt_id,
        lane_id: lane.id,
        lane_version: lane.version,
        lane_digest: lane.digest,
        readback_model: readback.readback_model,
        failure_kind: terminal.failure_kind,
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
    readback_model: readback.readback_model,
    packet_schema_version: PACKET_SCHEMA_VERSION,
    report_schema_version: REPORT_SCHEMA_VERSION,
    host_provenance: provenance,
    assertion: dispatchAssertion,
  }, signal)
  if (dispatchFailure) return errorEnvelope(lane, packet, "error", "error", dispatchFailure, "reconcile_operation")

  if (terminal.verb === "worker-fail") {
    const failureRecordFailure = await recordWorkerEvent(cliRunner, cli, "worker-fail", {
      event_id: crypto.randomUUID(),
      work_id: packet.work_id,
      attempt_id: packet.attempt_id,
      readback_model: readback.readback_model,
      failure_kind: terminal.failure_kind,
      detail: terminal.detail,
      assertion: terminalAssertion,
    }, signal)
    if (failureRecordFailure) return errorEnvelope(lane, packet, "error", "error", failureRecordFailure, "reconcile_operation")
    const failed = errorEnvelope(lane, packet, "error", terminal.failure_kind === "invalid_report" ? "invalid_report" : "error", terminal.detail, "reconcile_operation")
    failed.readback_model = readback.readback_model
    failed.session_id = readback.session_id
    return withHostBoundedOutput(failed, resultBody) ?? failed
  }

  const completionFailure = await recordWorkerEvent(cliRunner, cli, "worker-complete", {
    event_id: crypto.randomUUID(),
    work_id: packet.work_id,
    attempt_id: packet.attempt_id,
    readback_model: readback.readback_model,
    report_schema_version: REPORT_SCHEMA_VERSION,
    evidence_origin: "reported",
    evidence: terminal.report.evidence,
    assertion: terminalAssertion,
  }, signal)
  if (completionFailure) return errorEnvelope(lane, packet, "error", "error", completionFailure, "reconcile_operation")

  return envelope
}
