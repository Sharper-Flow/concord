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
// session's persisted agent and model on the synthetic text part, identify the
// persisted user message carrying the report, confirm the assistant reply the
// host parented to that message, and queue the report for the next
// chat.message when any step fails. Every delivery failure is logged and
// queued; none is swallowed.

import { randomUUID } from "node:crypto"
import fs from "node:fs"
import { homedir } from "node:os"
import path from "node:path"
import { concordBinaryPath } from "./dispatch"
import { hostControlPlane, SESSION_MESSAGES_ROUTE, SESSION_ROUTE, type PluginClientHost, type RouteResult } from "./move-session"

const SESSION_STATUS_ROUTE = "/session/status"
const PROMPT_ASYNC_ROUTE = "/session/{id}/prompt_async"
const LOG_ROUTE = "/log"

// The verb's own request contract (cmd/concord/ci_wait.go): one JSON object
// on stdin, one JSON report on stdout, one bounded slice per invocation.
export type CiWatchArgs = {
  repo: string
  selector: { kind: "pr" | "sha" | "run"; value: string }
  mode?: "checks" | "merge"
  time_seconds_max?: number
}

type CiWaitReport = { status: string; reason?: string; state_file?: string; [key: string]: unknown }

type SessionIdentity = { agent?: string; model?: { providerID: string; modelID: string } }

type ActiveWatch = {
  id: string
  sessionID: string
  args: CiWatchArgs
  binary: string
  stateFile: string
  startedAt: number
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
const queuedReports = new Map<string, Array<{ watchID: string; text: string }>>()
const settledWatches = new Map<string, Promise<void>>()

// Test seams: the host test suite injects a fake spawner and a fake route
// client through the same exported functions production uses. No production
// path sets an override.
export function configureCiWatch(
  overrides: Partial<CiWatchConfig> & { spawner?: VerbSpawner; reset?: boolean } = {},
): void {
  if (overrides.reset) {
    config = { ...DEFAULT_CONFIG }
    spawner = defaultVerbSpawner
    routeClient = null
    activeWatches.clear()
    watchKeys.clear()
    queuedReports.clear()
    settledWatches.clear()
    return
  }
  const { spawner: injected, ...rest } = overrides
  if (injected) spawner = injected
  config = { ...config, ...rest }
}

export function bindCiWatchClient(source: PluginClientHost | WatchRouteClient | undefined): void {
  if (!source) {
    routeClient = null
    return
  }
  routeClient = isRouteClient(source) ? source : routeClientOf(source.client)
}

// ciWatchSettled exposes the watch loop's completion to the test suite; a
// watch id that never started answers null.
export function ciWatchSettled(watchID: string): Promise<void> | null {
  return settledWatches.get(watchID) ?? null
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
    .catch(() => {
      // A logging transport failure must not break the watcher loop; the
      // condition it tried to report is already carried by the queue or the
      // delivered report.
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

const CI_WATCH_DESCRIPTION =
  "Start watching GitHub CI and return at once: the concord ci-wait verb runs as a plugin-owned child with zero model " +
  "turns, and this session receives the terminal JSON report as a message when the wait ends. Pass repo (owner/name), " +
  "one selector (kind pr, sha, or run with its value), and optionally mode (checks or merge) and time_seconds_max."

const ciWatchArgsSchema = {
  type: "object",
  additionalProperties: false,
  required: ["repo", "selector"],
  properties: {
    repo: { type: "string", pattern: "^[A-Za-z0-9][A-Za-z0-9.-]*/[A-Za-z0-9][A-Za-z0-9.-]*$", minLength: 3, maxLength: 256 },
    selector: {
      type: "object",
      additionalProperties: false,
      required: ["kind", "value"],
      properties: {
        kind: { type: "string", enum: ["pr", "sha", "run"] },
        value: { type: "string", minLength: 1, maxLength: 64 },
      },
    },
    mode: { type: "string", enum: ["checks", "merge"] },
    time_seconds_max: { type: "integer", minimum: 0, maximum: 1800 },
  },
} as const

const CI_WATCH_BOUNDARY =
  "Concord worker lanes hold no Concord tool access; the CI watcher is a coordinator surface"

// Two host bridges can each deliver tool arguments wrapped one extra time:
// the Code Mode bridge under a `request` property, and schema-presenting
// callers under the JSON Schema envelope itself (`properties` holding the
// real fields beside `type`, `required`, and `additionalProperties`). No
// legitimate argument payload carries those keys at the top level, so each
// shape names its wrapper.
function unwrapArgs(args: unknown): unknown {
  const outer = args as { request?: unknown; properties?: unknown } | null
  if (outer !== null && typeof outer === "object" && outer.request !== null && typeof outer.request === "object") {
    return outer.request
  }
  if (
    outer !== null &&
    typeof outer === "object" &&
    outer.properties !== null &&
    typeof outer.properties === "object" &&
    !Array.isArray(outer.properties)
  ) {
    const keys = Object.keys(outer)
    const envelopeKeys = keys.every((key) => key === "type" || key === "properties" || key === "required" || key === "additionalProperties")
    const inner = outer.properties as Record<string, unknown>
    if (envelopeKeys && (inner.repo !== undefined || inner.selector !== undefined)) {
      return inner
    }
  }
  return args
}

function validateCiWatchArgs(args: unknown): CiWatchArgs | string {
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
  if (typeof record.repo !== "string" || !/^[A-Za-z0-9][A-Za-z0-9.-]*\/[A-Za-z0-9][A-Za-z0-9.-]*$/.test(record.repo)) {
    return "repo is required as owner/name"
  }
  const selector = record.selector
  if (selector === null || typeof selector !== "object" || Array.isArray(selector)) {
    return "selector is required with kind pr, sha, or run and its value"
  }
  const { kind, value } = selector as Record<string, unknown>
  if (kind !== "pr" && kind !== "sha" && kind !== "run") return "selector.kind must be one of pr, sha, or run"
  if (typeof value !== "string" || value.length === 0) return "selector.value is required"
  if (kind === "pr" && !/^[0-9]+$/.test(value)) return "a pr selector value must be a pull request number"
  if (kind === "sha" && !/^[0-9a-f]{40}$/.test(value)) return "a sha selector value must be a 40-character commit SHA"
  if (kind === "run" && !/^[0-9]+$/.test(value)) return "a run selector value must be a run id"
  if (record.mode !== undefined && record.mode !== "checks" && record.mode !== "merge") {
    return "mode must be checks or merge"
  }
  if (record.time_seconds_max !== undefined) {
    const budget = record.time_seconds_max
    if (typeof budget !== "number" || !Number.isInteger(budget) || budget < 0 || budget > 1800) {
      return "time_seconds_max must be an integer between 0 and 1800"
    }
  }
  return {
    repo: record.repo,
    selector: { kind, value },
    ...(record.mode === undefined ? {} : { mode: record.mode as "checks" | "merge" }),
    ...(record.time_seconds_max === undefined ? {} : { time_seconds_max: record.time_seconds_max as number }),
  }
}

// The watcher is a coordinator surface. The check mirrors the adapter's lane
// boundary and fails closed: a caller the host cannot positively resolve as
// an unparented coordinator session is refused.
async function coordinatorRefusal(context: { sessionID: string; abort?: AbortSignal }): Promise<string | null> {
  if (context.abort?.aborted) return null
  const controlPlane = hostControlPlane()
  if (!controlPlane.available()) {
    return `${CI_WATCH_BOUNDARY}; the host control plane is unbound, so the caller cannot be proven a coordinator session`
  }
  try {
    if (await controlPlane.hasManagedParent(context.sessionID, context.abort)) {
      return `${CI_WATCH_BOUNDARY}; the caller session runs under a managed parent`
    }
  } catch (error) {
    if (context.abort?.aborted) return null
    return `${CI_WATCH_BOUNDARY}; Concord cannot resolve this session's managed Task scope, so it cannot prove the caller is a coordinator session (${errorDetail(error)})`
  }
  return null
}

function watchKey(sessionID: string, args: CiWatchArgs): string {
  const selector = `${args.selector.kind}:${args.selector.value}`
  return `${sessionID}|${args.repo}|${selector}|${args.mode ?? "checks"}`
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
      mode: watch.args.mode ?? "checks",
      time_seconds_max: watch.args.time_seconds_max ?? 1800,
      state_file: watch.stateFile,
      delivery: "the terminal JSON report arrives as a message in this session when the wait ends",
    }),
  }
}

export const concord_ci_watch = {
  description: CI_WATCH_DESCRIPTION,
  args: ciWatchArgsSchema,
  execute: (args: unknown, context: { sessionID: string; abort?: AbortSignal }): Promise<{ title: string; output: string }> =>
    executeConcordCiWatch(args, context),
}

async function executeConcordCiWatch(
  rawArgs: unknown,
  context: { sessionID: string; abort?: AbortSignal },
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
  const watch = createWatch(context.sessionID, args, binary)
  watchKeys.set(key, watch.id)
  startWatch(watch)
  return watchResult(watch, "started")
}

function createWatch(sessionID: string, args: CiWatchArgs, binary: string): ActiveWatch {
  const id = randomUUID()
  fs.mkdirSync(config.stateDir, { recursive: true })
  return {
    id,
    sessionID,
    args,
    binary,
    stateFile: path.join(config.stateDir, `ci-wait-watch-${id}.json`),
    startedAt: Date.now(),
  }
}

function startWatch(watch: ActiveWatch): void {
  const loop = runWatch(watch).catch((error) => {
    // runWatch settles every path internally; this backstop keeps a structural
    // escape from becoming an unhandled rejection.
    log("error", `ci-watch ${watch.id}: the watcher loop escaped: ${errorDetail(error)}`)
  })
  settledWatches.set(watch.id, loop)
  void loop.finally(() => {
    settledWatches.delete(watch.id)
    watchKeys.delete(watchKey(watch.sessionID, watch.args))
  })
}

// --- The wait loop ---

function verbRequestBody(watch: ActiveWatch): string {
  return JSON.stringify({
    repo: watch.args.repo,
    selector: watch.args.selector,
    ...(watch.args.mode === undefined ? {} : { mode: watch.args.mode }),
    ...(watch.args.time_seconds_max === undefined ? {} : { time_seconds_max: watch.args.time_seconds_max }),
    state_file: watch.stateFile,
  })
}

async function runVerbSlice(watch: ActiveWatch): Promise<CiWaitReport> {
  const { exitCode, stdout, stderr } = await spawner([watch.binary, "ci-wait"], verbRequestBody(watch), AbortSignal.timeout(config.sliceTimeoutMs))
  const report = parseVerbReport(stdout)
  if (report !== null) return report
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
  const budget = (watch.args.time_seconds_max ?? 1800) * 1000
  return budget + config.sliceTimeoutMs + 30_000
}

async function runWatch(watch: ActiveWatch): Promise<void> {
  activeWatches.set(watch.id, watch)
  try {
    let sliceFailures = 0
    let verbErrors = 0
    for (;;) {
      if (Date.now() - watch.startedAt >= watchCeilingMs(watch)) {
        await settle(watch, {
          status: "timeout",
          reason: `the watcher ceiling of ${Math.round(watchCeilingMs(watch) / 1000)}s expired while the wait stayed pending`,
        })
        return
      }
      let report: CiWaitReport
      try {
        report = await runVerbSlice(watch)
      } catch (error) {
        sliceFailures++
        log("error", `ci-watch ${watch.id}: verb slice failed (${errorDetail(error)})`)
        if (sliceFailures >= config.maxSliceFailures) {
          await settle(watch, {
            status: "error",
            reason: `the concord ci-wait verb failed ${sliceFailures} consecutive slices: ${errorDetail(error)}`,
          })
          return
        }
        continue
      }
      sliceFailures = 0
      // pending means re-invoke: the verb already paced this slice, so the
      // next slice starts immediately.
      if (report.status === "pending") continue
      if (report.status === "error") {
        // One error report re-invokes with the same state file, the same
        // rule the generated utility body carried.
        verbErrors++
        if (verbErrors >= 2) {
          await settle(watch, report)
          return
        }
        continue
      }
      await settle(watch, report)
      return
    }
  } finally {
    activeWatches.delete(watch.id)
  }
}

// --- Delivery ---

function formatReport(watch: ActiveWatch, report: CiWaitReport): string {
  const selector = `${watch.args.selector.kind}:${watch.args.selector.value}`
  const mode = watch.args.mode === undefined ? "" : ` (${watch.args.mode} mode)`
  // The watch id in the header makes the report text unique, which is how the
  // persisted user message carrying it is identified after the 204.
  return `[concord ci-watch ${watch.id}] CI wait ${report.status} for ${watch.args.repo} ${selector}${mode}.\n\n\`\`\`json\n${JSON.stringify(report, null, 2)}\n\`\`\``
}

async function settle(watch: ActiveWatch, report: CiWaitReport): Promise<void> {
  const text = formatReport(watch, report)
  try {
    const outcome = await deliverTerminalReport(watch, text)
    if (outcome === "delivered") {
      log("info", `ci-watch ${watch.id}: the terminal report was delivered and the assistant reply parented to it followed`)
    } else {
      queueReport(watch.sessionID, watch.id, text)
      log("warn", `ci-watch ${watch.id}: no assistant reply parented to the injected report followed within ${Math.round(config.confirmWindowMs / 1000)}s; the report is queued for the next message`)
    }
  } catch (error) {
    queueReport(watch.sessionID, watch.id, text)
    log("error", `ci-watch ${watch.id}: delivery failed (${errorDetail(error)}); the report is queued for the next message`)
  }
}

async function deliverTerminalReport(watch: ActiveWatch, text: string): Promise<"delivered" | "unconfirmed"> {
  await waitForIdle(watch.sessionID)
  const identity = await readSessionIdentity(watch.sessionID)
  await promptAsync(watch.sessionID, identity, text)
  const injected = await injectedReportMessageID(watch.sessionID, text)
  if (injected === null) {
    throw new Error(`the host never persisted the injected report message for session ${watch.sessionID}`)
  }
  const confirmed = await confirmAssistantReply(watch.sessionID, injected)
  return confirmed ? "delivered" : "unconfirmed"
}

async function waitForIdle(sessionID: string): Promise<void> {
  const deadline = Date.now() + config.idleTimeoutMs
  for (;;) {
    const client = requireClient("read session status")
    const result = await client.get({ url: SESSION_STATUS_ROUTE, signal: AbortSignal.timeout(10_000) })
    if (!result.response.ok) throw new Error(`the host session status route answered ${result.response.status}`)
    const statuses = (result.data ?? {}) as Record<string, { type?: string }>
    // The status map tracks sessions the host is running; an absent entry is
    // a session with no active run, which is idle by the host's own runtime
    // default (`data.get(sessionID) ?? { type: "idle" }`).
    const status = statuses[sessionID]?.type ?? "idle"
    if (status === "idle") return
    if (Date.now() + config.idlePollMs > deadline) {
      throw new Error(`session ${sessionID} stayed ${status} for ${Math.round(config.idleTimeoutMs / 1000)}s`)
    }
    await sleep(config.idlePollMs)
  }
}

async function readSessionIdentity(sessionID: string): Promise<SessionIdentity> {
  const client = requireClient("read the session record")
  const result = await client.get({ url: SESSION_ROUTE, path: { id: sessionID }, signal: AbortSignal.timeout(10_000) })
  if (!result.response.ok) throw new Error(`the host session record answered ${result.response.status}`)
  const record = (result.data ?? {}) as { agent?: unknown; model?: unknown }
  const identity: SessionIdentity = {}
  if (typeof record.agent === "string" && record.agent.length > 0) identity.agent = record.agent
  if (record.model !== null && typeof record.model === "object") {
    // The host session record carries the selected model as { id, providerID,
    // variant? }; the prompt payload's model reference names the same pair
    // { providerID, modelID }, so the host id maps onto modelID.
    const model = record.model as { id?: unknown; providerID?: unknown }
    if (typeof model.providerID === "string" && model.providerID.length > 0 && typeof model.id === "string" && model.id.length > 0) {
      identity.model = { providerID: model.providerID, modelID: model.id }
    }
  }
  return identity
}

async function promptAsync(sessionID: string, identity: SessionIdentity, text: string): Promise<void> {
  const client = requireClient("deliver the terminal report")
  const body: Record<string, unknown> = { parts: [{ type: "text", text, synthetic: true }] }
  if (identity.agent !== undefined) body.agent = identity.agent
  if (identity.model !== undefined) body.model = identity.model
  const result = await client.post({ url: PROMPT_ASYNC_ROUTE, path: { id: sessionID }, body, signal: AbortSignal.timeout(10_000) })
  if (!result.response.ok) throw new Error(`the host prompt_async route answered ${result.response.status}`)
}

type SessionMessage = {
  info?: { id?: unknown; role?: unknown; parentID?: unknown }
  parts?: Array<{ type?: unknown; text?: unknown }>
}

async function sessionMessages(sessionID: string): Promise<SessionMessage[]> {
  const client = requireClient("read the session messages")
  const result = await client.get({ url: SESSION_MESSAGES_ROUTE, path: { id: sessionID }, query: { limit: "20" }, signal: AbortSignal.timeout(10_000) })
  if (!result.response.ok) throw new Error(`the host session messages route answered ${result.response.status}`)
  return Array.isArray(result.data) ? (result.data as SessionMessage[]) : []
}

// The prompt_async route answers 204 before the host's forked prompt persists
// anything and its response carries no message id, so the injected report is
// identified by its own text on the session's user messages.
async function injectedReportMessageID(sessionID: string, text: string): Promise<string | null> {
  const deadline = Date.now() + config.confirmWindowMs
  for (;;) {
    for (const message of await sessionMessages(sessionID)) {
      const info = message.info
      if (info === null || typeof info !== "object" || info.role !== "user" || typeof info.id !== "string") continue
      const parts = Array.isArray(message.parts) ? message.parts : []
      const carries = parts.some((part) => part !== null && typeof part === "object" && part.type === "text" && part.text === text)
      if (carries) return info.id
    }
    if (Date.now() + config.confirmPollMs > deadline) return null
    await sleep(config.confirmPollMs)
  }
}

// Delivery is proven only by the assistant reply the host parented to the
// injected report message (the host's Assistant schema requires parentID).
// Any other new assistant message is concurrent traffic, not the wake.
async function confirmAssistantReply(sessionID: string, injectedMessageID: string): Promise<boolean> {
  const deadline = Date.now() + config.confirmWindowMs
  for (;;) {
    for (const message of await sessionMessages(sessionID)) {
      const info = message.info
      if (info !== null && typeof info === "object" && info.role === "assistant" && info.parentID === injectedMessageID) return true
    }
    if (Date.now() + config.confirmPollMs > deadline) return false
    await sleep(config.confirmPollMs)
  }
}

function queueReport(sessionID: string, watchID: string, text: string): void {
  const queue = queuedReports.get(sessionID) ?? []
  if (queue.length >= config.maxQueuedPerSession) {
    const dropped = queue.shift()
    log("error", `ci-watch ${watchID}: the delivery queue for session ${sessionID} is full; the oldest queued report (${dropped?.watchID ?? "unknown"}) is dropped`)
  }
  queue.push({ watchID, text })
  queuedReports.set(sessionID, queue)
}

// drainQueuedCiReports injects every queued terminal report into the message
// the host is about to create, so a wake the confirmation protocol could not
// prove still reaches the model on the next turn. The host validates every
// part against the stored part schema before save, so each drained part is
// complete: a fresh `prt_` id, the session and message it belongs to, and the
// synthetic flag. It never throws: a drained queue can only append parts.
export function drainQueuedCiReports(sessionID: string, messageID: string, output: { parts: unknown[] }): void {
  const queue = queuedReports.get(sessionID)
  if (queue === undefined || queue.length === 0) return
  queuedReports.delete(sessionID)
  for (const { text } of queue) {
    output.parts.push({ id: `prt_ciwatch-${randomUUID()}`, sessionID, messageID, type: "text", text, synthetic: true })
  }
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
  const queued = queuedReports.get(sessionID)
  if (queued === undefined || queued.length === 0) return
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
