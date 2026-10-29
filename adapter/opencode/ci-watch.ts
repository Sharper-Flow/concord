// ci-watch — the plugin-owned CI watcher for the `concord ci-wait` verb.
//
// The watcher moves the CD-0160 wait loop out of the utility model session
// and into the plugin: it spawns the verb slice by slice as a plugin child,
// carries the state file across slices, and returns the calling session to
// its caller at once, so a wait spends zero model turns and the coordinator
// stays idle. The verb stays the only owner of polling, pacing, the
// deadline, and classification; this module adds no GitHub client and no
// second wait policy.
//
// Delivery survives the upstream promptAsync lost-wake defects (a busy
// session persists the prompt but never schedules a turn; an idle session can
// drop one). The protocol: deliver only to an idle session, carry the
// session's persisted agent, model, and variant in the prompt body, identify
// the persisted user message carrying the report, confirm actual
// assistant output (non-empty text or tool activity) the host parented to
// that message, and queue the report when any step fails. A queued report gets
// one more try through the same protocol, at once when its session is already
// idle and otherwise when the session next goes idle, and the next
// chat.message carries any report still queued. Watch admission bounds each
// session's unresolved watches plus queued reports at the queue cap, so every
// accepted report is retained and none is dropped to make room. Every delivery
// failure is logged and queued; none is swallowed.

import { randomUUID } from "node:crypto"
import fs from "node:fs"
import { homedir } from "node:os"
import path from "node:path"
import { hostToolDescriptions, hostToolSchemas } from "./generated-contracts"
import { concordBinaryPath } from "./core-runtime"
import { hostControlPlane, SESSION_MESSAGES_ROUTE, SESSION_ROUTE, type PluginClientHost, type RouteResult } from "./move-session"
import { promptAndConfirm as deliverHostPrompt, readSessionIdentity as hostSessionIdentity, type SessionIdentity } from "./host-prompt"

const SESSION_STATUS_ROUTE = "/session/status"
const LOG_ROUTE = "/log"

// The part metadata key that marks a user message as this module's operator
// notice. The plugin's chat.message hook reads it to skip the notice, so the
// marker lives here where the notice is written.
export const CI_WATCH_NOTICE_METADATA = "concord.ci_watch_notice"
// An idle redelivery's text part lists the watch ids of the queued reports it
// carries under this metadata key.
const CI_WATCH_REDELIVERY_METADATA = "concord.ci_watch_redelivery"

// The verb's own request contract (internal/ciwait/ci_wait.go): one JSON object
// on stdin, one JSON report on stdout, one bounded slice per invocation.
export type CiWatchArgs = {
  repo: string
  selector: { kind: "pr" | "sha" | "run"; value: string }
  head_sha?: string
  mode?: "checks" | "merge"
  time_seconds_max?: number
}

type CiWaitReport = { status: string; reason?: string; state_file?: string; [key: string]: unknown }

type ActiveWatch = {
  id: string
  watchEpoch: number
  bindingID: string | null
  lastObservation: CiWatchObservation | null
  lastReport?: CiWaitReport
  terminalObservation?: CiWatchObservation
  consecutiveSliceFailures: number
  sessionID: string
  args: CiWatchArgs
  binary: string
  stateFile: string
  startedAt: number
  // The start notice's task, set at admission and awaited by settle, so the
  // notice always precedes the terminal report. It never rejects: an announce
  // failure is logged at warn and resolves.
  notice?: Promise<void>
  waiting: boolean
  heartbeatTimer?: ReturnType<typeof setInterval>
  heartbeat?: Promise<void>
  // The idle check queueReport starts for this watch's report. It never
  // rejects.
  idleCheck?: Promise<void>
}

export type VerbSpawner = (
  argv: string[],
  stdin: string,
  signal: AbortSignal,
) => Promise<{ exitCode: number; stdout: string; stderr: string }>

// WatchRouteClient is the request surface ci-watch needs beneath the SDK
// client. The typed session.promptAsync method exists on the generated
// client, but the adapter reaches routes as data so it keeps the host's
// in-process transport without importing a host package at runtime.
type WatchRouteClient = {
  get: (options: { url: string; path?: Record<string, unknown>; query?: Record<string, unknown>; signal?: AbortSignal }) => Promise<RouteResult>
  post: (options: { url: string; path?: Record<string, unknown>; body?: unknown; signal?: AbortSignal }) => Promise<RouteResult>
}

type CiWatchConfig = {
  heartbeatIntervalMs: number
  idleTimeoutMs: number
  idlePollMs: number
  confirmWindowMs: number
  confirmPollMs: number
  sliceTimeoutMs: number
  maxSliceFailures: number
  maxQueuedPerSession: number
  maxActiveWatches: number
  stateDir: string
}

function defaultStateDir(): string {
  const base = process.env.XDG_STATE_HOME || path.join(homedir(), ".local", "state")
  return path.join(base, "concord")
}

const DEFAULT_CONFIG: CiWatchConfig = {
  heartbeatIntervalMs: 60_000,
  idleTimeoutMs: 5 * 60_000,
  idlePollMs: 2_000,
  confirmWindowMs: 3 * 60_000,
  confirmPollMs: 3_000,
  sliceTimeoutMs: 150_000,
  maxSliceFailures: 3,
  maxQueuedPerSession: 4,
  maxActiveWatches: 8,
  stateDir: defaultStateDir(),
}

let config: CiWatchConfig = { ...DEFAULT_CONFIG }
let spawner: VerbSpawner = defaultVerbSpawner
let routeClient: WatchRouteClient | null = null

const activeWatches = new Map<string, ActiveWatch>()
const watchKeys = new Map<string, string>()
// idleTried records that a queued report has had its one idle redelivery.
type QueuedReport = { record: CiWatchRecord; text: string; idleTried: boolean }
const queuedReports = new Map<string, QueuedReport[]>()
const settledWatches = new Map<string, Promise<void>>()
const idleRedeliveries = new Map<string, Set<Promise<void>>>()

type CiWatchCounts = { total: number; passing: number; failing: number; skipped: number; pending: number }
type CiWatchObservation = {
  status: string
  sha: string | null
  head_sha: string | null
  run_url: string | null
  checks: CiWatchCounts | null
  slice_started_at: number
  report_received_at: number
  stale: boolean
  evidence: "exact_head" | "unavailable"
  gaps: string[]
}
type CiWatchRecord = ReturnType<typeof ciWatchRecord>
type CiWatchReceipt = CiWatchRecord & {
  terminal_sequence: number
  terminal_observation: CiWatchObservation
  delivery_state: "delivering" | "queued" | "delivered" | "drained"
}
let runtimeEpoch = randomUUID()
let ownerStartedAt = Date.now()
let peerSequence = 0
let retentionFloor = 0
const terminalReceipts: CiWatchReceipt[] = []
// The terminal ring's fixed capacity, reported to peers as receipt_limit.
const CI_WATCH_RECEIPT_LIMIT = 64

function resetCiWatchSnapshots(): void {
  runtimeEpoch = randomUUID()
  ownerStartedAt = Date.now()
  peerSequence = 0
  retentionFloor = 0
  terminalReceipts.length = 0
  resetCiWatchPeer()
}

function ciWatchRecord(watch: ActiveWatch) {
  return {
    watch_id: watch.id, watch_epoch: watch.watchEpoch, session_id: watch.sessionID,
    repo: watch.args.repo, selector: watch.args.selector, mode: watch.args.mode ?? "checks",
    started_at: watch.startedAt, binding_id: watch.bindingID,
    phase: (watch.waiting ? "observing" : "delivering") as "observing" | "delivering" | "queued",
    consecutive_slice_failures: watch.consecutiveSliceFailures, last_observation: watch.lastObservation,
    terminal_observation: watch.terminalObservation ?? null,
  }
}

function projectCiObservation(watch: ActiveWatch, report: CiWaitReport, startedAt: number, receivedAt = Date.now()): CiWatchObservation {
  const gaps: string[] = []
  const statuses = ["pending", "success", "failure", "merged", "closed", "superseded", "timeout", "error", "refused"]
  const status = statuses.includes(report.status) ? report.status : "unknown"
  const sha = typeof report.sha === "string" && /^[0-9a-f]{40}$/.test(report.sha) ? report.sha : null
  const head = typeof report.head_sha === "string" && /^[0-9a-f]{40}$/.test(report.head_sha) ? report.head_sha : null
  if (sha === null) gaps.push("sha_unknown")
  if (report.head_sha !== undefined && head === null) gaps.push("invalid_head_sha")
  if (watch.args.selector.kind === "sha" && sha !== watch.args.selector.value) gaps.push("subject_mismatch")
  if (watch.args.head_sha !== undefined && sha !== watch.args.head_sha) gaps.push("subject_mismatch")
  let checks: CiWatchCounts | null = null
  if (report.checks !== undefined) {
    const raw = report.checks as Record<string, unknown> | null
    const keys = ["total", "passing", "failing", "skipped", "pending"] as const
    if (raw !== null && typeof raw === "object" && keys.every((key) => Number.isSafeInteger(raw[key]) && Number(raw[key]) >= 0)) {
      checks = Object.fromEntries(keys.map((key) => [key, raw[key]])) as CiWatchCounts
      if (checks.total !== checks.passing + checks.failing + checks.skipped + checks.pending) checks = null
    }
    if (checks === null) gaps.push("invalid_checks")
  }
  if (status === "success" && (!checks || checks.total === 0 || checks.failing !== 0 || checks.pending !== 0)) gaps.push("invalid_checks")
  if (status === "failure" && (!checks || checks.failing === 0)) gaps.push("invalid_checks")
  if (["merged", "closed", "superseded"].includes(status) && watch.args.selector.kind !== "pr") gaps.push("selector_mismatch")
  if (status === "superseded" && (head === null || head === sha)) gaps.push("subject_mismatch")
  if (watch.args.selector.kind === "pr" && ["success", "failure", "merged", "closed"].includes(status) && head !== sha) gaps.push("subject_mismatch")
  if (watch.args.mode === "merge" && status === "success") gaps.push("merge_unproven")
  if (["timeout", "error", "refused", "unknown"].includes(status)) gaps.push("terminal_evidence_unavailable")
  if (!Number.isFinite(startedAt) || !Number.isFinite(receivedAt) || startedAt > receivedAt || startedAt < ownerStartedAt) gaps.push("invalid_interval")
  const url = typeof report.run_url === "string" && report.run_url.length <= 2048 ? report.run_url : null
  if (report.run_url !== undefined && url === null) gaps.push("invalid_run_url")
  return {
    status, sha, head_sha: head, run_url: url, checks,
    slice_started_at: startedAt, report_received_at: receivedAt,
    stale: false,
    evidence: gaps.length === 0 ? "exact_head" : "unavailable", gaps,
  }
}

function recordCiTerminal(watch: ActiveWatch, observation: CiWatchObservation): void {
  terminalReceipts.push({ ...ciWatchRecord(watch), terminal_sequence: ++peerSequence, terminal_observation: observation, delivery_state: "delivering" })
  if (terminalReceipts.length > CI_WATCH_RECEIPT_LIMIT) {
    terminalReceipts.shift()
    retentionFloor = terminalReceipts[0]!.terminal_sequence
  }
}

function updateCiDelivery(watchID: string, state: CiWatchReceipt["delivery_state"]): void {
  const receipt = terminalReceipts.find((entry) => entry.watch_id === watchID)
  if (receipt) receipt.delivery_state = state
  peerSequence++
}

// Test seams: the host test suite injects a fake spawner and a fake route
// client through the same exported functions production uses. No production
// path sets an override.
export function configureCiWatch(
  overrides: Partial<CiWatchConfig> & { spawner?: VerbSpawner; reset?: boolean } = {},
): void {
  if (overrides.reset) {
    for (const watch of activeWatches.values()) stopWatchHeartbeats(watch)
    config = { ...DEFAULT_CONFIG }
    spawner = defaultVerbSpawner
    routeClient = null
    activeWatches.clear()
    watchKeys.clear()
    queuedReports.clear()
    settledWatches.clear()
    idleRedeliveries.clear()
    resetCiWatchSnapshots()
    return
  }
  const { spawner: injected, ...rest } = overrides
  if (injected) spawner = injected
  config = { ...config, ...rest }
}

export function bindCiWatchClient(source: PluginClientHost | WatchRouteClient | undefined): void {
  ++bindingGeneration
  if (!source) {
    routeClient = null
    return
  }
  routeClient = isRouteClient(source) ? source : routeClientOf(source.client)
}

// ciWatchSettled exposes the watch loop's completion, and the idle check its
// queued report started, to the test suite; a watch id that never started
// answers null.
export function ciWatchSettled(watchID: string): Promise<void> | null {
  return settledWatches.get(watchID) ?? null
}

// ciWatchRedelivery exposes a session's in-flight idle redeliveries to the
// test suite; a session with no try in flight answers null.
export function ciWatchRedelivery(sessionID: string): Promise<void> | null {
  const tries = idleRedeliveries.get(sessionID)
  return tries ? Promise.all(tries).then(() => undefined) : null
}

function routeClientOf(source: unknown): WatchRouteClient | null {
  const raw = (source as { _client?: Partial<WatchRouteClient> } | null | undefined)?._client
  return isRouteClient(raw) ? raw : null
}

function isRouteClient(source: unknown): source is WatchRouteClient {
  return (
    source !== null &&
    typeof source === "object" &&
    typeof (source as WatchRouteClient).get === "function" &&
    typeof (source as WatchRouteClient).post === "function"
  )
}

function requireClient(purpose: string): WatchRouteClient {
  if (!routeClient) throw new Error(`ci-watch cannot ${purpose}: this host handed the plugin no client`)
  return routeClient
}

async function defaultVerbSpawner(argv: string[], stdin: string, signal: AbortSignal) {
  const child = Bun.spawn(argv, {
    stdin: "pipe",
    stdout: "pipe",
    stderr: "pipe",
    env: process.env as Record<string, string>,
  })
  const abort = () => child.kill()
  if (signal.aborted) abort()
  signal.addEventListener("abort", abort, { once: true })
  try {
    await child.stdin.write(stdin)
    await child.stdin.end()
    const [stdout, stderr, exitCode] = await Promise.all([
      new Response(child.stdout).text(),
      new Response(child.stderr).text(),
      child.exited,
    ])
    return { exitCode, stdout, stderr }
  } catch (error) {
    child.kill()
    await child.exited
    throw error
  } finally {
    signal.removeEventListener("abort", abort)
  }
}

function log(level: "debug" | "info" | "warn" | "error", message: string): void {
  const client = routeClient
  if (!client) return
  void client
    .post({ url: LOG_ROUTE, body: { service: "concord", level, message }, signal: AbortSignal.timeout(5_000) })
    .then((result) => {
      if (!result.response.ok) {
        // The host answered the log write with a failure status, so the entry
        // is not in the host log; the stderr line is the diagnostic that
        // survives a logging failure alongside the one being reported.
        console.error(`concord ci-watch: the host /log route answered ${result.response.status} for a ${level} entry: ${message}`)
      }
    })
    .catch((error) => {
      console.error(`concord ci-watch: the host /log route failed (${errorDetail(error)}) for a ${level} entry: ${message}`)
    })
}

function errorDetail(error: unknown): string {
  const detail = error instanceof Error ? error.message : String(error)
  return detail.length > 400 ? `${detail.slice(0, 400)}…` : detail
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

// --- Tool surface ---

// The watcher's description and parameter schema are authored in the host
// tool catalog (contracts/host-tool-surface.v1.json) and projected into the
// generated contracts beside work start, so the published document and the
// runtime bounds have exactly one source. The per-kind semantic checks stay
// in this module: the contract bounds a selector value's length, never its
// shape.
const ciWatchContract = hostToolSchemas.concord_ci_watch

const ciWatchRepoSchema = ciWatchContract.properties.repo
const ciWatchSelectorSchema = ciWatchContract.properties.selector
const ciWatchModeSchema = ciWatchContract.properties.mode
const headPattern = new RegExp(ciWatchContract.properties.head_sha.pattern)
const ciWatchBudgetSchema = ciWatchContract.properties.time_seconds_max
const repoPattern = new RegExp(ciWatchRepoSchema.pattern)

function modeBudget(mode: unknown): number {
  return mode === "merge" ? ciWatchBudgetSchema.maximum : 1800
}

function watchBudget(args: CiWatchArgs): number {
  return args.time_seconds_max ?? modeBudget(args.mode)
}

const CI_WATCH_BOUNDARY =
  "Concord worker lanes hold no Concord tool access; the CI watcher is a coordinator surface"

// The Code Mode bridge can deliver the tool arguments wrapped one extra time
// under a `request` property. No legitimate argument payload carries that key
// at the top level, so the wrapper names itself. Arguments whose shape matches
// the published schema arrive as the schema's own fields; an argument payload
// carrying schema keywords instead of fields is a mispublication and is
// refused by the closed validation.
function unwrapArgs(args: unknown): unknown {
  const outer = args as { request?: unknown } | null
  if (outer !== null && typeof outer === "object" && outer.request !== null && typeof outer.request === "object") {
    return outer.request
  }
  return args
}

export function validateCiWatchArgs(args: unknown): CiWatchArgs | string {
  if (args === null || typeof args !== "object" || Array.isArray(args)) return "the request body must be a JSON object"
  const record = { ...(args as Record<string, unknown>) }
  // The Code Mode bridge can deliver a nested argument as its serialized JSON
  // string, and schema-presenting callers can flatten selector into sibling
  // kind and value fields. Normalize both shapes before the closed validation.
  if (typeof record.selector === "string") {
    try {
      record.selector = JSON.parse(record.selector)
    } catch {
      return "selector must be an object with kind and value, not a malformed JSON string"
    }
  }
  if (record.selector === undefined && record.kind !== undefined && record.value !== undefined) {
    record.selector = { kind: record.kind, value: record.value }
    delete record.kind
    delete record.value
  }
  if (typeof record.time_seconds_max === "string" && /^[0-9]+$/.test(record.time_seconds_max)) {
    record.time_seconds_max = Number(record.time_seconds_max)
  }
  // The published argument surface is closed: the contract's properties are
  // the only top-level fields, enforced after the normalizations above.
  const allowedKeys = Object.keys(ciWatchContract.properties)
  const unexpectedKeys = Object.keys(record).filter((key) => !allowedKeys.includes(key))
  if (unexpectedKeys.length > 0) {
    return `unexpected argument(s) ${unexpectedKeys.map((key) => JSON.stringify(key)).join(", ")}; the request admits only ${allowedKeys.join(", ")}`
  }
  if (typeof record.repo !== "string" || !repoPattern.test(record.repo)) {
    return "repo is required as owner/name"
  }
  if (record.repo.length < ciWatchRepoSchema.minLength || record.repo.length > ciWatchRepoSchema.maxLength) {
    return `repo must be ${ciWatchRepoSchema.minLength} to ${ciWatchRepoSchema.maxLength} characters`
  }
  const selector = record.selector
  if (selector === null || typeof selector !== "object" || Array.isArray(selector)) {
    return "selector is required with kind pr, sha, or run and its value"
  }
  const { kind, value } = selector as Record<string, unknown>
  const allowedSelectorKeys = Object.keys(ciWatchSelectorSchema.properties)
  const unexpectedSelectorKeys = Object.keys(selector as Record<string, unknown>).filter((key) => !allowedSelectorKeys.includes(key))
  if (unexpectedSelectorKeys.length > 0) {
    return `unexpected selector field(s) ${unexpectedSelectorKeys.map((key) => JSON.stringify(key)).join(", ")}; the selector admits only ${allowedSelectorKeys.join(", ")}`
  }
  // The matched literal is the generated enum's own member type, so the
  // validated kind narrows structurally instead of through a cast.
  const matchedKind = ciWatchSelectorSchema.properties.kind.enum.find((candidate) => candidate === kind)
  if (matchedKind === undefined) {
    return `selector.kind must be one of ${ciWatchSelectorSchema.properties.kind.enum.join(", ")}`
  }
  if (typeof value !== "string" || value.length < ciWatchSelectorSchema.properties.value.minLength) {
    return "selector.value is required"
  }
  if (value.length > ciWatchSelectorSchema.properties.value.maxLength) {
    return `selector.value must be at most ${ciWatchSelectorSchema.properties.value.maxLength} characters`
  }
  if (matchedKind === "pr" && !/^[0-9]+$/.test(value)) return "a pr selector value must be a pull request number"
  if (matchedKind === "sha" && !/^[0-9a-f]{40}$/.test(value)) return "a sha selector value must be a 40-character commit SHA"
  if (matchedKind === "run" && !/^[0-9]+$/.test(value)) return "a run selector value must be a run id"
  if (record.head_sha !== undefined) {
    if (matchedKind !== "pr") return "head_sha applies only to a pr selector"
    if (typeof record.head_sha !== "string" || !headPattern.test(record.head_sha)) return "head_sha must be a 40-character lowercase commit SHA"
  }
  if (record.mode !== undefined && !ciWatchModeSchema.enum.some((candidate) => candidate === record.mode)) {
    return `mode must be ${ciWatchModeSchema.enum.join(" or ")}`
  }
  if (record.time_seconds_max !== undefined) {
    const budget = record.time_seconds_max
    if (typeof budget !== "number" || !Number.isInteger(budget) || budget < ciWatchBudgetSchema.minimum || budget > modeBudget(record.mode)) {
      return `time_seconds_max must be an integer between ${ciWatchBudgetSchema.minimum} and ${modeBudget(record.mode)} for ${record.mode ?? "checks"} mode`
    }
  }
  return {
    repo: record.repo,
    selector: { kind: matchedKind, value },
    ...(record.head_sha === undefined ? {} : { head_sha: record.head_sha as string }),
    ...(record.mode === undefined ? {} : { mode: record.mode as "checks" | "merge" }),
    ...(record.time_seconds_max === undefined ? {} : { time_seconds_max: record.time_seconds_max as number }),
  }
}

// The watcher is a coordinator surface. The check mirrors the adapter's lane
// boundary and fails closed: a caller the host cannot positively resolve as
// an unparented coordinator session is refused. A cancelled context is not a
// scope answer either, and unlike the core-transport tools nothing downstream
// cancels the spawn, so a cancellation before or during the scope lookup
// refuses admission instead of standing down.
async function coordinatorRefusal(context: { sessionID: string; abort?: AbortSignal }): Promise<string | null> {
  if (context.abort?.aborted) return cancelledCallerRefusal()
  const controlPlane = hostControlPlane()
  if (!controlPlane.available()) {
    return `${CI_WATCH_BOUNDARY}; the host control plane is unbound, so the caller cannot be proven a coordinator session`
  }
  try {
    if (await controlPlane.hasManagedParent(context.sessionID, context.abort)) {
      return `${CI_WATCH_BOUNDARY}; the caller session runs under a managed parent`
    }
  } catch (error) {
    if (context.abort?.aborted) return cancelledCallerRefusal()
    return `${CI_WATCH_BOUNDARY}; Concord cannot resolve this session's managed Task scope, so it cannot prove the caller is a coordinator session (${errorDetail(error)})`
  }
  // The lookup answered unparented, but the caller may have been cancelled
  // while it ran; admission reads the signal once more so a cancellation that
  // lands during the lookup cannot convert into a started watch.
  if (context.abort?.aborted) return cancelledCallerRefusal()
  return null
}

function cancelledCallerRefusal(): string {
  return `${CI_WATCH_BOUNDARY}; the calling context was cancelled before admission, so the caller cannot be proven a coordinator session`
}

function watchKey(sessionID: string, args: CiWatchArgs): string {
  const selector = `${args.selector.kind}:${args.selector.value}`
  return `${sessionID}|${args.repo}|${selector}|${args.head_sha ?? ""}|${args.mode ?? "checks"}`
}

function refusalResult(reason: string): { title: string; output: string } {
  return { title: "concord ci-watch", output: JSON.stringify({ schema_version: "1.0", tool: "concord_ci_watch", status: "refused", reason }) }
}

function watchResult(watch: ActiveWatch, status: "started" | "already_watching"): { title: string; output: string } {
  return {
    title: "concord ci-watch",
    output: JSON.stringify({
      schema_version: "1.0",
      tool: "concord_ci_watch",
      status,
      watch_id: watch.id,
      repo: watch.args.repo,
      selector: watch.args.selector,
      ...(watch.args.head_sha === undefined ? {} : { head_sha: watch.args.head_sha }),
      mode: watch.args.mode ?? "checks",
      time_seconds_max: watchBudget(watch.args),
      state_file: watch.stateFile,
      delivery: "the terminal JSON report arrives as a message in this session when the wait ends",
    }),
  }
}

// The host autoloads every tool-shaped named export of a module under the
// custom tools directory as `<file>_<export>`, beside the plugin
// registration. Only the plugin registration may name this tool (CD-0199
// D1), so the definition leaves this module through a factory: a function
// export is not a tool shape. The optional binding id names the peer
// registration the plugin wiring passes; the watches this definition admits
// stamp it, and the peer read qualifies watches against it.
export function ciWatchTool(bindingID: string | null = null): {
  description: string
  args: typeof ciWatchContract.properties
  execute: (args: unknown, context: { sessionID: string; abort?: AbortSignal }) => Promise<{ title: string; output: string }>
} {
  return {
    description: hostToolDescriptions.concord_ci_watch,
    args: ciWatchContract.properties,
    execute: (args, context) => executeConcordCiWatch(args, context, bindingID),
  }
}

// The host's per-field conversion marks every args entry required, but mode
// and time_seconds_max are optional. The definition hook republishes the
// contract's own document, which requires only repo and selector;
// validateCiWatchArgs stays the runtime boundary.
export async function publishCiWatchDefinition(
  input: { toolID: string },
  output: { description: string; parameters: unknown; jsonSchema?: unknown },
): Promise<void> {
  if (input.toolID !== "concord_ci_watch") return
  output.jsonSchema = JSON.parse(JSON.stringify(ciWatchContract))
}

async function executeConcordCiWatch(
  rawArgs: unknown,
  context: { sessionID: string; abort?: AbortSignal },
  bindingID: string | null = null,
): Promise<{ title: string; output: string }> {
  const boundaryRefusal = await coordinatorRefusal(context)
  if (boundaryRefusal !== null) return refusalResult(boundaryRefusal)
  if (routeClient === null) {
    return refusalResult("concord_ci_watch cannot deliver the terminal report: this host handed the plugin no client")
  }
  const args = validateCiWatchArgs(unwrapArgs(rawArgs))
  if (typeof args === "string") return refusalResult(args)
  let binary: string
  try {
    binary = concordBinaryPath()
  } catch (error) {
    return refusalResult(`concord_ci_watch cannot run the concord ci-wait verb: ${errorDetail(error)}`)
  }
  const key = watchKey(context.sessionID, args)
  const existing = watchKeys.get(key)
  if (existing !== undefined && activeWatches.has(existing)) {
    const watch = activeWatches.get(existing)!
    return watchResult(watch, "already_watching")
  }
  if (activeWatches.size >= config.maxActiveWatches) {
    return refusalResult(`concord_ci_watch already carries ${config.maxActiveWatches} active watches; wait for one to settle`)
  }
  const unresolved = unresolvedSessionDeliveries(context.sessionID)
  if (unresolved >= config.maxQueuedPerSession) {
    return refusalResult(
      `concord_ci_watch already holds ${unresolved} unresolved watches and undelivered reports for this session, and the delivery queue caps at ${config.maxQueuedPerSession}; receive a delivered report or send a message to release capacity`,
    )
  }
  const watch = createWatch(context.sessionID, args, binary, bindingID)
  watchKeys.set(key, watch.id)
  startWatch(watch)
  announceWatchNotice(watch)
  return watchResult(watch, "started")
}

function createWatch(sessionID: string, args: CiWatchArgs, binary: string, bindingID: string | null = null): ActiveWatch {
  const id = randomUUID()
  fs.mkdirSync(config.stateDir, { recursive: true })
  return {
    id,
    watchEpoch: ++peerSequence,
    bindingID,
    lastObservation: null,
    consecutiveSliceFailures: 0,
    sessionID,
    args,
    binary,
    stateFile: path.join(config.stateDir, `ci-wait-watch-${id}.json`),
    startedAt: Date.now(),
    waiting: true,
  }
}

function startWatch(watch: ActiveWatch): void {
  const loop = runWatch(watch).catch((error) => {
    // runWatch settles every path internally; this backstop keeps a structural
    // escape from becoming an unhandled rejection.
    log("error", `ci-watch ${watch.id}: the watcher loop escaped: ${errorDetail(error)}`)
  })
  const settled = loop.then(() => watch.idleCheck)
  settledWatches.set(watch.id, settled)
  void loop.finally(() => watchKeys.delete(watchKey(watch.sessionID, watch.args)))
  void settled.finally(() => settledWatches.delete(watch.id))
}

// --- The wait loop ---

function verbRequestBody(watch: ActiveWatch): string {
  return JSON.stringify({
    repo: watch.args.repo,
    selector: watch.args.selector,
    ...(watch.args.head_sha === undefined ? {} : { head_sha: watch.args.head_sha }),
    ...(watch.args.mode === undefined ? {} : { mode: watch.args.mode }),
    ...(watch.args.time_seconds_max === undefined ? {} : { time_seconds_max: watch.args.time_seconds_max }),
    state_file: watch.stateFile,
  })
}

async function runVerbSlice(watch: ActiveWatch): Promise<CiWaitReport> {
  const sliceStartedAt = Date.now()
  const { exitCode, stdout, stderr } = await spawner([watch.binary, "ci-wait"], verbRequestBody(watch), AbortSignal.timeout(config.sliceTimeoutMs))
  const report = parseVerbReport(stdout)
  if (report !== null) {
    // Projected beside this exact report; the terminal settle passes it back.
    watch.lastObservation = projectCiObservation(watch, report, sliceStartedAt)
    watch.lastReport = report
    peerSequence++
    return report
  }
  const detail = stderr.trim() ? `: ${stderr.trim().slice(0, 400)}` : ""
  throw new Error(`the concord ci-wait verb produced no JSON report (exit ${exitCode}${detail})`)
}

function parseVerbReport(stdout: string): CiWaitReport | null {
  const text = stdout.trim()
  if (!text.startsWith("{")) return null
  try {
    const parsed: unknown = JSON.parse(text)
    if (parsed !== null && typeof parsed === "object" && !Array.isArray(parsed)) {
      const status = (parsed as Record<string, unknown>).status
      if (typeof status === "string" && status.length > 0) return parsed as CiWaitReport
    }
  } catch {
    // A non-JSON stdout is a failed slice, reported by the caller.
  }
  return null
}

function watchCeilingMs(watch: ActiveWatch): number {
  const budget = watchBudget(watch.args) * 1000
  return budget + config.sliceTimeoutMs + 30_000
}

async function runWatch(watch: ActiveWatch): Promise<void> {
  activeWatches.set(watch.id, watch)
  try {
    for (;;) {
      if (Date.now() - watch.startedAt >= watchCeilingMs(watch)) {
        await settle(watch, {
          ...watch.lastReport,
          status: "timeout",
          reason: `the watcher ceiling of ${Math.round(watchCeilingMs(watch) / 1000)}s expired while the wait stayed pending`,
        })
        return
      }
      let report: CiWaitReport
      try {
        report = await runVerbSlice(watch)
      } catch (error) {
        watch.consecutiveSliceFailures++
        peerSequence++
        log("error", `ci-watch ${watch.id}: verb slice failed (${errorDetail(error)})`)
        if (watch.consecutiveSliceFailures >= config.maxSliceFailures) {
          await settle(watch, {
            status: "error",
            reason: `the concord ci-wait verb failed ${watch.consecutiveSliceFailures} consecutive slices: ${errorDetail(error)}`,
          })
          return
        }
        continue
      }
      watch.consecutiveSliceFailures = 0
      // pending means re-invoke: the verb already paced this slice, so the
      // next slice starts immediately.
      if (report.status === "pending") continue
      await settle(watch, report, watch.lastObservation)
      return
    }
  } finally {
    stopWatchHeartbeats(watch)
    activeWatches.delete(watch.id)
    peerSequence++
    // The loop owns abandonment as well as normal terminal reports. A child
    // that dies without a report cannot remove its own resumable state.
    try {
      fs.unlinkSync(watch.stateFile)
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ENOENT") {
        log("error", `ci-watch ${watch.id}: cannot remove wait state: ${errorDetail(error)}`)
      }
    }
  }
}

// --- Operator notices ---

// noticeText is the operator-facing line the chat shows while a watch runs.
// The tool result itself is one generic line the TUI collapses, so without
// this the session looks dead between admission and the terminal report.
function noticeText(watch: ActiveWatch): string {
  const selector = `${watch.args.selector.kind}:${watch.args.selector.value}`
  const mode = watch.args.mode ?? "checks"
  const budget = watchBudget(watch.args)
  return `⏳ Watching CI for ${watch.args.repo} ${selector} (${mode} mode, up to ${budget}s). This session stays idle; the result arrives here when the wait ends.`
}

// The notice is posted from a task started at admission, beside the slice
// loop, and only after the session goes idle: a user message inserted while
// the coordinator's turn still runs becomes the loop's lastUser and can change
// that turn. The part is ignored and not synthetic, so the TUI renders the
// line while the host's message-v2 conversion drops it from model context, and
// noReply makes the synchronous route return before any turn runs. A failure
// is logged at warn and never blocks or fails the watch or its report.
function announceWatchNotice(watch: ActiveWatch): void {
  watch.notice = postWatchNotice(watch).catch((error) => {
    log("warn", `ci-watch ${watch.id}: the start notice was not posted (${errorDetail(error)})`)
  })
  watch.heartbeatTimer = setInterval(() => {
    if (!watch.waiting || watch.heartbeat !== undefined) return
    watch.heartbeat = postWatchHeartbeat(watch)
      .catch((error) => {
        log("warn", `ci-watch ${watch.id}: the heartbeat was not posted (${errorDetail(error)})`)
      })
      .finally(() => { watch.heartbeat = undefined })
  }, config.heartbeatIntervalMs)
  watch.heartbeatTimer.unref()
}

async function postWatchNotice(watch: ActiveWatch): Promise<void> {
  await waitForIdle(watch.sessionID)
  const identity = await readSessionIdentity(watch.sessionID)
  await postOperatorNotice(watch, identity, noticeText(watch))
}

async function postWatchHeartbeat(watch: ActiveWatch): Promise<void> {
  await watch.notice
  if (!watch.waiting) return
  const identity = await readSessionIdentity(watch.sessionID)
  // A busy session misses this tick; never queue a UI notice behind its turn.
  const status = await readSessionStatus(watch.sessionID)
  if (!watch.waiting || status !== "idle") return
  const seconds = Math.max(0, Math.floor((Date.now() - watch.startedAt) / 1000))
  const elapsed = `${Math.floor(seconds / 60)}m ${seconds % 60}s`
  const selector = `${watch.args.selector.kind}:${watch.args.selector.value}`
  await postOperatorNotice(watch, identity,
    `⏳ Still watching CI for ${watch.args.repo} ${selector} (${watch.args.mode ?? "checks"} mode) — ${elapsed} elapsed. The result arrives here when the wait ends.`)
}

function stopWatchHeartbeats(watch: ActiveWatch): void {
  watch.waiting = false
  clearInterval(watch.heartbeatTimer)
  watch.heartbeatTimer = undefined
}

async function postOperatorNotice(watch: ActiveWatch, identity: SessionIdentity, text: string): Promise<void> {
  const client = requireClient("post an operator notice")
  const body = {
    ...identity,
    noReply: true,
    parts: [
      {
        type: "text",
        text,
        ignored: true,
        metadata: { [CI_WATCH_NOTICE_METADATA]: watch.id },
      },
    ],
  }
  const result = await client.post({ url: SESSION_MESSAGES_ROUTE, path: { id: watch.sessionID }, body, signal: AbortSignal.timeout(10_000) })
  if (!result.response.ok) throw new Error(`the host session message route answered ${result.response.status}`)
}

// isCiWatchNoticeMessage reports whether every part the host's chat.message
// hook receives is a ci-watch notice part. A message with no parts is
// never a notice: the host may still be adding parts when the hook fires.
export function isCiWatchNoticeMessage(parts: unknown): boolean {
  if (!Array.isArray(parts) || parts.length === 0) return false
  return parts.every((part) => {
    if (part === null || typeof part !== "object") return false
    const metadata = (part as { metadata?: unknown }).metadata
    if (metadata === null || typeof metadata !== "object") return false
    return CI_WATCH_NOTICE_METADATA in (metadata as Record<string, unknown>)
  })
}

// --- Delivery ---

function formatReport(watch: ActiveWatch, report: CiWaitReport): string {
  const selector = `${watch.args.selector.kind}:${watch.args.selector.value}`
  const mode = watch.args.mode === undefined ? "" : ` (${watch.args.mode} mode)`
  // The watch id in the header makes the report text unique, which is how the
  // persisted user message carrying it is identified after the 204.
  return `[concord ci-watch ${watch.id}] CI wait ${report.status} for ${watch.args.repo} ${selector}${mode}.\n\n\`\`\`json\n${JSON.stringify(report, null, 2)}\n\`\`\``
}

async function settle(watch: ActiveWatch, report: CiWaitReport, sliceObservation: CiWatchObservation | null = null): Promise<void> {
  stopWatchHeartbeats(watch)
  // The terminal receipt records before any await. Only the call site that
  // just received this report from runVerbSlice hands its observation down;
  // adapter-generated timeout/error reports project fresh evidence at
  // Date.now and borrow no earlier slice's SHA.
  watch.terminalObservation = sliceObservation ?? projectCiObservation(watch, report, Date.now())
  recordCiTerminal(watch, watch.terminalObservation)
  // The notice always precedes the report: settle waits for the announce task
  // before it delivers. The task never rejects, so this cannot fail delivery.
  await watch.notice
  // Drain the bounded post already in flight before delivering the report.
  await watch.heartbeat
  const text = formatReport(watch, report)
  try {
    await waitForIdle(watch.sessionID)
    const outcome = await promptAndConfirm(watch.sessionID, text)
    if (outcome === "delivered") {
      updateCiDelivery(watch.id, "delivered")
      log("info", `ci-watch ${watch.id}: the terminal report was delivered and the assistant reply parented to it followed`)
    } else {
      queueReport(watch, text)
      log("warn", `ci-watch ${watch.id}: no assistant reply parented to the injected report followed within ${Math.round(config.confirmWindowMs / 1000)}s; the report is queued for the next message`)
    }
  } catch (error) {
    queueReport(watch, text)
    log("error", `ci-watch ${watch.id}: delivery failed (${errorDetail(error)}); the report is queued for the next message`)
  }
}

async function promptAndConfirm(sessionID: string, text: string, metadata?: Record<string, unknown>): Promise<"delivered" | "unconfirmed"> {
  return deliverHostPrompt(requireClient("deliver the terminal report"), sessionID, text, config, metadata)
}

async function waitForIdle(sessionID: string): Promise<void> {
  const deadline = Date.now() + config.idleTimeoutMs
  for (;;) {
    const status = await readSessionStatus(sessionID)
    if (status === "idle") return
    if (Date.now() + config.idlePollMs > deadline) {
      throw new Error(`session ${sessionID} stayed ${status} for ${Math.round(config.idleTimeoutMs / 1000)}s`)
    }
    await sleep(config.idlePollMs)
  }
}

async function readSessionStatus(sessionID: string): Promise<string> {
  const client = requireClient("read session status")
  const result = await client.get({ url: SESSION_STATUS_ROUTE, signal: AbortSignal.timeout(10_000) })
  if (!result.response.ok) throw new Error(`the host session status route answered ${result.response.status}`)
  const statuses = (result.data ?? {}) as Record<string, { type?: string }>
  // The host omits sessions with no active run; its runtime defaults to idle.
  return statuses[sessionID]?.type ?? "idle"
}

async function readSessionIdentity(sessionID: string): Promise<SessionIdentity> {
  return hostSessionIdentity(requireClient("read the session record"), sessionID)
}

// unresolvedSessionDeliveries counts the session's accepted watches that can
// still produce a terminal report plus its already queued reports. Admission
// refuses at the queue cap, so every accepted watch can retain its report: a
// watch leaves the count only when its report is delivered, drained, or
// queued, and a queued report leaves only when it is delivered.
function unresolvedSessionDeliveries(sessionID: string): number {
  let active = 0
  for (const watch of activeWatches.values()) {
    if (watch.sessionID === sessionID) active++
  }
  return active + (queuedReports.get(sessionID)?.length ?? 0)
}

function queueReport(watch: ActiveWatch, text: string): void {
  const queue = queuedReports.get(watch.sessionID) ?? []
  if (queue.length >= config.maxQueuedPerSession) {
    // Admission bounds a session's unresolved watches plus queued reports, so
    // this branch means that bound was exceeded; the accepted report is
    // retained anyway and the breach is logged, because dropping an earlier
    // accepted report to make room is the one outcome admission exists to
    // prevent.
    log("error", `ci-watch ${watch.id}: session ${watch.sessionID} holds more than ${config.maxQueuedPerSession} queued reports, beyond the admission bound; the accepted report is retained and none is dropped`)
  }
  queue.push({ record: { ...ciWatchRecord(watch), phase: "queued" }, text, idleTried: false })
  queuedReports.set(watch.sessionID, queue)
  updateCiDelivery(watch.id, "queued")
  watch.idleCheck = redeliverIfIdle(watch.sessionID)
  trackIdleTry(watch.sessionID, watch.idleCheck)
}

// redeliverIfIdle starts the idle try at once for a report queued while its
// session is already idle. That session's idle event has passed, and no other
// comes until a later turn ends. A busy session's reports wait for its idle
// event instead.
async function redeliverIfIdle(sessionID: string): Promise<void> {
  let status: string
  try {
    status = await readSessionStatus(sessionID)
  } catch (error) {
    log("warn", `ci-watch: could not read whether session ${sessionID} is idle (${errorDetail(error)}); its queued reports wait for its next idle event or message`)
    return
  }
  if (status !== "idle") return
  const tried = claimIdleTry(sessionID)
  if (tried.length > 0) await redeliverQueuedReports(sessionID, tried)
}

// redeliverQueuedCiReports gives a session's unclaimed queued reports their
// one idle try when the host reports that session idle; a busy session's
// report otherwise waits for a message an idle session never receives. The
// caller does not wait for the try, because the confirm window must not hold
// the host's event dispatch, and it never throws.
export function redeliverQueuedCiReports(event: unknown): void {
  if (event === null || typeof event !== "object") return
  const { type, properties } = event as { type?: unknown; properties?: unknown }
  if (type !== "session.idle" || properties === null || typeof properties !== "object") return
  const sessionID = (properties as { sessionID?: unknown }).sessionID
  if (typeof sessionID !== "string") return
  const tried = claimIdleTry(sessionID)
  if (tried.length > 0) trackIdleTry(sessionID, redeliverQueuedReports(sessionID, tried))
}

// claimIdleTry marks the session's queued reports that have not had their idle
// try, so no later trigger repeats it, and returns them.
function claimIdleTry(sessionID: string): QueuedReport[] {
  const tried = (queuedReports.get(sessionID) ?? []).filter((entry) => !entry.idleTried)
  for (const entry of tried) entry.idleTried = true
  return tried
}

function trackIdleTry(sessionID: string, attempt: Promise<void>): void {
  const tries = idleRedeliveries.get(sessionID) ?? new Set<Promise<void>>()
  const tracked: Promise<void> = attempt.finally(() => {
    tries.delete(tracked)
    if (tries.size === 0 && idleRedeliveries.get(sessionID) === tries) idleRedeliveries.delete(sessionID)
  })
  tries.add(tracked)
  idleRedeliveries.set(sessionID, tries)
}

async function redeliverQueuedReports(sessionID: string, tried: QueuedReport[]): Promise<void> {
  let carried: QueuedReport[] = []
  let outcome: "delivered" | "unconfirmed"
  try {
    await waitForIdle(sessionID)
    // A chat.message during the wait already carried the reports it drained.
    const queue = queuedReports.get(sessionID) ?? []
    carried = tried.filter((entry) => queue.includes(entry))
    if (carried.length === 0) return
    // The fresh header keeps an earlier unanswered copy of a report from
    // matching the injected message this try confirms against.
    const text = [`[concord ci-watch queued delivery ${randomUUID()}]`, ...carried.map((entry) => entry.text)].join("\n\n")
    outcome = await promptAndConfirm(sessionID, text, { [CI_WATCH_REDELIVERY_METADATA]: carried.map((entry) => entry.record.watch_id) })
  } catch (error) {
    log("error", `ci-watch: the idle redelivery for session ${sessionID} failed (${errorDetail(error)}); any of its reports not yet drained stay queued for the next message`)
    return
  }
  const count = `${carried.length} queued report(s) for session ${sessionID}`
  if (outcome === "unconfirmed") {
    log("warn", `ci-watch: no assistant reply parented to the idle redelivery of ${count} followed within ${Math.round(config.confirmWindowMs / 1000)}s; any not yet drained stay queued for the next message`)
    return
  }
  for (const entry of carried) updateCiDelivery(entry.record.watch_id, "delivered")
  const remaining = (queuedReports.get(sessionID) ?? []).filter((entry) => !carried.includes(entry))
  if (remaining.length === 0) queuedReports.delete(sessionID)
  else queuedReports.set(sessionID, remaining)
  log("info", `ci-watch: the idle redelivery of ${count} was delivered and the assistant reply parented to it followed`)
}

// drainQueuedCiReports injects every queued terminal report the message does
// not already carry into the message the host is about to create, so a wake the
// confirmation protocol could not prove still reaches the model on the next
// turn. The host validates every part against the stored part schema before
// save, so each drained part is complete: a fresh `prt_` id, the session and
// message it belongs to, and the synthetic flag. It never throws: a drained
// queue can only append parts.
export function drainQueuedCiReports(sessionID: string, messageID: string, output: { parts: unknown[] }): void {
  const drained = drainableReports(sessionID, output.parts)
  if (drained.length === 0) return
  for (const { record, text } of drained) {
    updateCiDelivery(record.watch_id, "drained")
    output.parts.push({ id: `prt_ciwatch-${randomUUID()}`, sessionID, messageID, type: "text", text, synthetic: true })
  }
  const kept = (queuedReports.get(sessionID) ?? []).filter((entry) => !drained.includes(entry))
  if (kept.length === 0) queuedReports.delete(sessionID)
  else queuedReports.set(sessionID, kept)
}

// An idle redelivery's own prompted message reaches chat.message too, and its
// marked part already carries the reports it lists, so the drain skips those.
function drainableReports(sessionID: string, parts: unknown[]): QueuedReport[] {
  const carried = new Set<unknown>()
  for (const part of parts) {
    const metadata = part !== null && typeof part === "object" ? (part as { metadata?: unknown }).metadata : undefined
    const listed = metadata !== null && typeof metadata === "object" ? (metadata as Record<string, unknown>)[CI_WATCH_REDELIVERY_METADATA] : undefined
    if (Array.isArray(listed)) for (const watchID of listed) carried.add(watchID)
  }
  return (queuedReports.get(sessionID) ?? []).filter((entry) => !carried.has(entry.record.watch_id))
}

// drainQueuedCiReportsForMessage resolves the drain identity the way the host
// delivers it on chat.message: the input messageID when the caller supplied
// one, else the host-generated id on the output message record. A queue with
// no resolvable identity stays queued and the failure is logged — it is never
// swallowed, and parts are never written without their owning message id.
export function drainQueuedCiReportsForMessage(
  sessionID: string,
  inputMessageID: string | undefined,
  output: { message?: { id?: unknown }; parts?: unknown[] },
): void {
  if (!Array.isArray(output?.parts)) return
  const queued = drainableReports(sessionID, output.parts)
  if (queued.length === 0) return
  const message = output.message
  const messageID =
    inputMessageID ??
    (message !== null && typeof message === "object" && typeof message.id === "string" && message.id.length > 0
      ? message.id
      : undefined)
  if (messageID === undefined) {
    log(
      "error",
      `ci-watch: the host's chat.message carried no message id, so ${queued.length} queued report(s) for session ${sessionID} cannot be injected and stay queued`,
    )
    return
  }
  drainQueuedCiReports(sessionID, messageID, output as { parts: unknown[] })
}

// --- Peer read snapshot ---

// A peer read is passive: observations are freshened on detached copies only,
// and the result is JSON-cloned once so nested selectors, counts, and
// observations cannot alias the owner's maps or mutate them later.
export type CiWatchSnapshot = {
  version: 1
  pid: number
  runtime_epoch: string
  owner_started_at: number
  sequence: number
  session_id: string
  read_at: number
  active: CiWatchRecord[]
  receipts: CiWatchReceipt[]
  receipt_limit: number
  retention_floor: number
  history_gap: boolean
}

// Freshness marks the detached copy only: a slice older than the slice
// timeout plus the 30s grace watchCeilingMs also grants is stale, an interval
// off this read's clock adds clock_gap, and either marks evidence unavailable.
function freshenObservation(observation: CiWatchObservation, now: number): CiWatchObservation {
  const stale = now - observation.slice_started_at > config.sliceTimeoutMs + 30_000
  const gaps = [...observation.gaps]
  let evidence = observation.evidence
  if (observation.report_received_at > now || observation.slice_started_at > observation.report_received_at) {
    gaps.push("clock_gap")
    evidence = "unavailable"
  }
  if (stale) {
    gaps.push("observation_stale")
    evidence = "unavailable"
  }
  return { ...observation, stale, evidence, gaps }
}

// ciWatchSnapshot reads one session's watch state for a peer. It runs before
// the first watch, holds no client, and truncates nothing: the peer boundary
// owns its own bounds. A queued record wins a transient overlap with its
// still-active watch, so the session sees the settled phase.
export function ciWatchSnapshot(sessionID: string, sinceSequence?: number): CiWatchSnapshot {
  const now = Date.now()
  const byWatchID = new Map<string, CiWatchRecord>()
  for (const watch of activeWatches.values()) {
    if (watch.sessionID === sessionID) byWatchID.set(watch.id, ciWatchRecord(watch))
  }
  for (const { record } of queuedReports.get(sessionID) ?? []) {
    byWatchID.set(record.watch_id, record)
  }
  const active = [...byWatchID.values()].map((record) => ({
    ...record,
    last_observation: record.last_observation && freshenObservation(record.last_observation, now),
    terminal_observation: record.terminal_observation && freshenObservation(record.terminal_observation, now),
  }))
  const receipts = terminalReceipts
    .filter((receipt) => receipt.session_id === sessionID && (sinceSequence === undefined || receipt.terminal_sequence > sinceSequence))
    .map((receipt) => ({
      ...receipt,
      last_observation: receipt.last_observation && freshenObservation(receipt.last_observation, now),
      terminal_observation: freshenObservation(receipt.terminal_observation, now),
    }))
  const snapshot = {
    version: 1,
    pid: process.pid,
    runtime_epoch: runtimeEpoch,
    owner_started_at: ownerStartedAt,
    sequence: peerSequence,
    session_id: sessionID,
    read_at: now,
    active,
    receipts,
    receipt_limit: CI_WATCH_RECEIPT_LIMIT,
    retention_floor: retentionFloor,
    history_gap: sinceSequence !== undefined && sinceSequence < retentionFloor,
  }
  return JSON.parse(JSON.stringify(snapshot)) as CiWatchSnapshot
}

// --- Peer bindings and publication ---
// A same-process peer plugin reads this plugin's watch state through a frozen port
// published on a well-known global symbol inside registerCiWatchPeer, never
// at module load.
export type PeerInput = PluginClientHost & { serverUrl?: URL; directory?: string; worktree?: string }

export type CiWatchPeerRead = {
  version: 1; pid: number
  runtime_epoch: string; read_at: number
  binding_generation: number; client_bound: boolean
  coverage: "unknown" | "complete_current_epoch"
  gaps: string[]
  bindings: Array<{ id: string; generation: number; endpoint: string; directory: string; worktree: string }>
  snapshot: CiWatchSnapshot | null
}

// The published object carries only the version and the read call.
type CiWatchPeer = { version: 1; read: (request: unknown) => Promise<CiWatchPeerRead> }
// Owner-side record; peerBindingInfo strips client and live before exposure.
type PeerBinding = {
  id: string; generation: number; live: boolean; qualified: boolean
  client: WatchRouteClient | null
  endpoint: string; directory: string; worktree: string
}

// Bindings occupy lifetime slots; disposal frees none and overflow sticks until reset.
const CI_WATCH_PEER_BINDING_LIMIT = 64
const CI_WATCH_PEER_SYMBOL = Symbol.for("concord.ci-watch.peer.v1")
// One typed globalThis view; register, reset, and the owner check share it.
const peerGlobals = globalThis as typeof globalThis & { [CI_WATCH_PEER_SYMBOL]?: CiWatchPeer }
const CI_WATCH_PEER_PUBLISH_FAILED = "concord ci-watch: the peer port could not be published on its global symbol; same-process peer reads stay unavailable"
let bindingGeneration = 0
let peerBindingOverflow = false
const peerBindings = new Map<string, PeerBinding>()
let publishedPeer: CiWatchPeer | null = null

function peerUrlValid(serverUrl: unknown): serverUrl is URL {
  return serverUrl instanceof URL &&
    (serverUrl.protocol === "http:" || serverUrl.protocol === "https:") &&
    serverUrl.username === "" && serverUrl.password === "" && serverUrl.search === "" &&
    serverUrl.hash === "" && (serverUrl.pathname === "/" || serverUrl.pathname === "") &&
    serverUrl.href.length <= 2048
}

function peerDirValid(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && Buffer.byteLength(value, "utf8") <= 4096
}
// Fresh record per call; no client reference or liveness state leaves the module.
function peerBindingInfo(binding: PeerBinding): CiWatchPeerRead["bindings"][number] {
  return { id: binding.id, generation: binding.generation, endpoint: binding.endpoint, directory: binding.directory, worktree: binding.worktree }
}

// Each publication cycle mints one port whose read hands readCiWatchPeer the
// exact installed object; reset retires the reference, so a cached retired port
// fails the later identity check and stays unknown.
function makePeerPort(): CiWatchPeer {
  const port: CiWatchPeer = Object.freeze({ version: 1, read: async (request: unknown) => readCiWatchPeer(request, port) })
  return port
}

// A foreign symbol occupant (another module instance or an unrelated publisher)
// is replaced by this frozen sentinel answering unknown for every read; it
// never serves a complete map.
const peerConflictSentinel: CiWatchPeer = Object.freeze({
  version: 1,
  read: async () => refusedPeerRead("owner_conflict"),
})

// Records one plugin-instance binding and publishes the peer port; a
// publication failure logs at warn and never blocks the calling watch. A
// typed null input is unsupported; the omitted-input default covers the
// no-fields call. A field is stored only when it qualifies: an invalid URL
// or oversized path stores an empty string, never a raw credential URL.
export function registerCiWatchPeer(input: PeerInput = {}): { id: string; dispose: () => void } {
  const binding: PeerBinding = {
    id: randomUUID(), generation: ++bindingGeneration, live: true,
    qualified: peerUrlValid(input.serverUrl) && peerDirValid(input.directory) && peerDirValid(input.worktree),
    client: routeClientOf(input.client),
    endpoint: peerUrlValid(input.serverUrl) ? input.serverUrl.href : "",
    directory: peerDirValid(input.directory) ? input.directory : "",
    worktree: peerDirValid(input.worktree) ? input.worktree : "",
  }
  if (peerBindings.size >= CI_WATCH_PEER_BINDING_LIMIT) peerBindingOverflow = true
  else peerBindings.set(binding.id, binding)
  if (publishedPeer === null) publishedPeer = makePeerPort()
  // A foreign occupant can hold the slot read-only or behind a throwing
  // getter; neither may break the watch this call is admitting.
  try {
    const current = peerGlobals[CI_WATCH_PEER_SYMBOL]
    peerGlobals[CI_WATCH_PEER_SYMBOL] = current !== undefined && current !== publishedPeer ? peerConflictSentinel : publishedPeer
  } catch {
    log("warn", CI_WATCH_PEER_PUBLISH_FAILED)
  }
  return {
    id: binding.id,
    // Idempotent: the generation moves once per binding, never per dispose.
    dispose: () => {
      if (!binding.live) return
      binding.live = false
      ++bindingGeneration
    },
  }
}

// Retires this module's own port or sentinel and clears all binding state.
function resetCiWatchPeer(): void {
  const current = peerGlobals[CI_WATCH_PEER_SYMBOL]
  if (current === publishedPeer || current === peerConflictSentinel) delete peerGlobals[CI_WATCH_PEER_SYMBOL]
  peerBindings.clear()
  peerBindingOverflow = false
  bindingGeneration = 0
  publishedPeer = null
}

// A refusal names its gaps and serves no snapshot: a bad request or a failed
// identity lookup proves nothing about watch state. Like every read it also
// carries prior_epoch_unverified — nothing here proves history from before
// its own epoch.
function refusedPeerRead(...gaps: string[]): CiWatchPeerRead {
  return {
    version: 1, pid: process.pid, runtime_epoch: runtimeEpoch, read_at: Date.now(), binding_generation: bindingGeneration,
    client_bound: routeClient !== null, coverage: "unknown", gaps: ["prior_epoch_unverified", ...gaps], bindings: [], snapshot: null,
  }
}

function parseCiWatchPeerRequest(request: unknown): { session_id: string; expected_runtime_epoch?: string; since_sequence?: number } | null {
  if (request === null || typeof request !== "object" || Array.isArray(request)) return null
  const record = request as Record<string, unknown>
  if (Object.keys(record).some((key) => key !== "session_id" && key !== "expected_runtime_epoch" && key !== "since_sequence")) return null
  const sessionID = record.session_id
  if (typeof sessionID !== "string" || sessionID.trim().length === 0 || Buffer.byteLength(sessionID, "utf8") > 256) return null
  const epoch = record.expected_runtime_epoch
  if (epoch !== undefined && (typeof epoch !== "string" || epoch.length === 0 || Buffer.byteLength(epoch, "utf8") > 128)) return null
  const cursor = record.since_sequence
  if (cursor !== undefined) {
    if (typeof cursor !== "number" || !Number.isSafeInteger(cursor) || cursor < 0) return null
  }
  return {
    session_id: sessionID,
    ...(epoch === undefined ? {} : { expected_runtime_epoch: epoch }),
    ...(cursor === undefined ? {} : { since_sequence: cursor }),
  }
}

function peerOwnerHolds(port: CiWatchPeer): boolean {
  if (publishedPeer !== port) return false
  try {
    return peerGlobals[CI_WATCH_PEER_SYMBOL] === port
  } catch {
    return false
  }
}

// The peer read validates the closed request, the port identity, and the epoch
// expectation before any GET, captures binding state before its awaits, and
// copies the snapshot synchronously after the last one, so no response serves
// a stale generation. Every gap besides prior_epoch_unverified is a coverage
// gap; the published array is the sorted gap set, complete and deterministic.
async function readCiWatchPeer(request: unknown, port: CiWatchPeer): Promise<CiWatchPeerRead> {
  const parsed = parseCiWatchPeerRequest(request)
  if (parsed === null) return refusedPeerRead("invalid_request")
  if (!peerOwnerHolds(port)) return refusedPeerRead("owner_conflict")
  if (parsed.expected_runtime_epoch !== undefined && parsed.expected_runtime_epoch !== runtimeEpoch) return refusedPeerRead("epoch_mismatch")
  const epoch = runtimeEpoch
  const generation = bindingGeneration
  const gaps = new Set<string>()
  const live = [...peerBindings.values()].filter((binding) => binding.live)
  if (peerBindingOverflow) gaps.add("binding_limit")
  if (live.length === 0) gaps.add("no_live_binding")
  if (live.some((binding) => !binding.qualified)) gaps.add("binding_unqualified")
  if (routeClient === null || live.some((binding) => binding.qualified && binding.client === null)) gaps.add("client_unbound")
  const matched: PeerBinding[] = []
  const probed = live.filter((binding) => binding.qualified && binding.client !== null)
  await Promise.all(probed.map(async (binding) => {
    try {
      const result = await binding.client!.get({ url: SESSION_ROUTE, path: { id: parsed.session_id }, signal: AbortSignal.timeout(10_000) })
      if (result.response.status === 404) return
      if (!result.response.ok) { gaps.add("session_unreachable"); return }
      const record = (result.data ?? {}) as { id?: unknown }
      if (record.id !== parsed.session_id) { gaps.add("session_identity_mismatch"); return }
      matched.push(binding)
    } catch {
      gaps.add("session_unreachable")
    }
  }))
  const lookupFailed = gaps.has("session_unreachable") || gaps.has("session_identity_mismatch")
  if (matched.length === 0 && probed.length > 0 && !lookupFailed) gaps.add("session_missing")
  else if (new Set(matched.map((binding) => binding.endpoint)).size > 1) gaps.add("ambiguous_host")
  if (!peerOwnerHolds(port)) return refusedPeerRead("owner_conflict")
  if (runtimeEpoch !== epoch) return refusedPeerRead("epoch_mismatch")
  if (bindingGeneration !== generation) return refusedPeerRead("binding_changed")
  // A failed identity lookup proves nothing about watch state: an empty
  // snapshot would read as a proven-empty session, so refuse with the whole
  // collected gap set — no single-gap selection hides a sibling cause.
  const hasIdentityGap = ["ambiguous_host", "session_identity_mismatch", "session_missing", "session_unreachable"].some((gap) => gaps.has(gap))
  if (hasIdentityGap) return refusedPeerRead(...[...gaps].sort())
  const snapshot = ciWatchSnapshot(parsed.session_id, parsed.since_sequence)
  // The envelope carries the same clock invariant freshenObservation applies
  // to observations: a start stamp after this read's clock gaps, even empty.
  if (snapshot.owner_started_at > snapshot.read_at) gaps.add("clock_gap")
  const referenced = new Map<string, PeerBinding>()
  for (const record of snapshot.active) {
    const binding = record.binding_id === null ? undefined : peerBindings.get(record.binding_id)
    if (binding === undefined) gaps.add("unqualified_watch")
    else if (!binding.live) gaps.add("disposed_watch")
    else if (!binding.qualified) gaps.add("unqualified_watch")
    // The watch proves state only for the endpoint its binding names; a
    // same-named session on another endpoint is a different session.
    else if (matched[0] !== undefined && binding.endpoint !== matched[0].endpoint) gaps.add("watch_host_mismatch")
    if (binding !== undefined) referenced.set(binding.id, binding)
  }
  for (const receipt of snapshot.receipts) {
    const binding = receipt.binding_id === null ? undefined : peerBindings.get(receipt.binding_id)
    if (binding !== undefined) referenced.set(binding.id, binding)
  }
  if (snapshot.history_gap) gaps.add("history_gap")
  for (const binding of matched) referenced.set(binding.id, binding)
  const others = [...gaps].sort()
  const coverage: CiWatchPeerRead["coverage"] = others.length === 0 ? "complete_current_epoch" : "unknown"
  const response: CiWatchPeerRead = {
    version: 1, pid: process.pid, runtime_epoch: epoch, read_at: Date.now(), binding_generation: generation,
    client_bound: routeClient !== null, coverage, gaps: ["prior_epoch_unverified", ...others],
    bindings: [...referenced.values()].map(peerBindingInfo), snapshot,
  }
  // The whole response is bound, never truncated: an oversized set is
  // refused whole, because truncation could understate the watch state.
  if (Buffer.byteLength(JSON.stringify(response), "utf8") > 256 * 1024) return refusedPeerRead("response_limit")
  return response
}
