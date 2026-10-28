import { defaultRunner, concordBinaryPath, CoreBinaryUnavailable, type DispatchRunner } from "./dispatch"
import { hostControlPlane } from "./move-session"
import { mutationResult } from "./packet"

export type WorkflowStatusContext = { sessionID: string; abort: AbortSignal }

type WorkPin = {
  work_id: string
  title: string
  linear_issue_key: string
  project_id: string
  project_display_name: string
  version: number
  lifecycle: string
  workflow_type: string
  step: string
  pending_operator_decision: { action_id: string } | null
  verified_criteria?: VerifiedCriterion[]
}

type VerifiedCriterion = {
  predicate_id: string
  ordinal: number
  outcome_kind: string
  outcome_payload: unknown
  verdict_kind: string
}

type MutationEnvelope = { outcome?: unknown; result?: unknown }

const MAX_CACHED_SESSIONS = 512
const MAX_CLOSURE_KEYS = 64

function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

function safeText(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && !/[\u0000-\u001f\u007f|]/u.test(value)
}

function sanitizeNameField(value: string): string {
  return [...value].filter((character) => {
    const code = character.codePointAt(0) ?? 0
    return code > 0x1f && !(code >= 0x7f && code <= 0x9f) && character !== "|"
  }).join("")
}

function safeTitle(value: unknown): string | null {
  if (typeof value !== "string" || value.length === 0) return null
  const sanitized = value.replace(/[\u0000-\u001f\u007f|]/gu, " ")
  return sanitized.length > 64 ? `${sanitized.slice(0, 63)}…` : sanitized
}

function workPin(value: unknown): WorkPin | null {
  if (!record(value) || typeof value.work_id !== "string" || typeof value.lifecycle !== "string" || typeof value.step !== "string") return null
  const title = safeTitle(value.title)
  const workID = sanitizeNameField(value.work_id)
  const lifecycle = sanitizeNameField(value.lifecycle)
  const step = sanitizeNameField(value.step)
  const projectID = typeof value.project_id === "string" ? sanitizeNameField(value.project_id) : ""
  const projectDisplayName = typeof value.project_display_name === "string" ? sanitizeNameField(value.project_display_name) : ""
  const linearIssueKey = typeof value.linear_issue_key === "string" ? sanitizeNameField(value.linear_issue_key) : ""
  if (title === null || workID === "" || lifecycle === "" || step === "" || typeof value.project_id !== "string" || typeof value.project_display_name !== "string" || typeof value.linear_issue_key !== "string") return null
  if (typeof value.version !== "number" || !Number.isInteger(value.version) || value.version < 1) return null
  if (value.pending_operator_decision !== null) {
    if (!record(value.pending_operator_decision) || !safeText(value.pending_operator_decision.action_id)) return null
  }
  return { ...value, work_id: workID, lifecycle, step, project_id: projectID, project_display_name: projectDisplayName, linear_issue_key: linearIssueKey, title } as unknown as WorkPin
}

const TERMINAL_LIFECYCLES: ReadonlySet<string> = new Set(["completed", "cancelled", "superseded"])

// formatWorkClosureReceipt renders the closure receipt for one terminal work
// pin. The format is product-owned law (CD-0169): the core's `concord
// receipt` verb renders it from the store, and this delegate keeps zero
// formatting logic of its own. The call rides only at a terminal lifecycle,
// because a non-terminal pin can never complete. The verb prints nothing for
// a work item outside the completed lifecycle, and a failed call is a
// best-effort warning, never a mutation failure: the receipt is a notice.
export async function formatWorkClosureReceipt(value: unknown, options: { runner?: DispatchRunner; binary?: string; abort?: AbortSignal; warnings?: string[] } = {}): Promise<string | null> {
  const pin = workPin(value)
  if (!pin || !TERMINAL_LIFECYCLES.has(pin.lifecycle)) return null
  const runner = options.runner ?? defaultRunner
  let binary: string
  try {
    binary = options.binary ?? concordBinaryPath()
  } catch (error) {
    if (error instanceof CoreBinaryUnavailable) return null
    throw error
  }
  let result: { exitCode: number; stdout: string; stderr: string }
  try {
    result = await runner.run([binary, "receipt"], JSON.stringify({ work_id: pin.work_id }), options.abort ?? new AbortController().signal)
  } catch (error) {
    options.warnings?.push(`Concord could not render the work closure receipt: ${error instanceof Error ? error.message : String(error)}.`)
    return null
  }
  if (result.exitCode !== 0) {
    options.warnings?.push(`Concord could not render the work closure receipt: receipt exited ${result.exitCode}.`)
    return null
  }
  const receipt = result.stdout.trim()
  return receipt.length > 0 ? receipt : null
}

function workPins(envelope: unknown): WorkPin[] {
  if (!record(envelope) || envelope.outcome !== "ok") return []
  const payload = mutationResult(envelope) ?? (record(envelope.result) ? undefined : envelope)
  if (!payload || !Array.isArray(payload.work_pins)) return []
  const pins = payload.work_pins.map(workPin)
  return pins.every((pin): pin is WorkPin => pin !== null) ? pins : []
}

type WorkStateReporterOptions = { runner?: DispatchRunner; binary?: string }

function rememberSession<T>(entries: Map<string, T>, sessionID: string, value: T): void {
  while (!entries.has(sessionID) && entries.size >= MAX_CACHED_SESSIONS) {
    const oldest = entries.keys().next()
    if (oldest.done) break
    entries.delete(oldest.value)
  }
  entries.set(sessionID, value)
}

// The session title carries the goal, and the reporter refreshes it from the
// post-state pin so a revised intent reaches the title the compaction hook
// restates. The write is best effort: an absent route, an empty title, or a
// failed call adds a warning and never changes the reported outcome.
async function refreshSessionGoalTitle(pin: WorkPin, context: WorkflowStatusContext, warnings: string[]): Promise<void> {
  const wrote = await hostControlPlane().setSessionTitle(context.sessionID, `Goal: ${pin.title}`, context.abort)
  if (wrote) return
  warnings.push("Concord could not write the session goal title: the session title route is absent or refused the write.")
}

export function createWorkStateReporter(options: WorkStateReporterOptions = {}) {
  const runner = options.runner ?? defaultRunner
  const binary = options.binary
  // Closure notices are informational: an evicted identity may emit again.
  // Eviction changes only deduplication, never the pending notice queue.
  const emittedClosures = new Map<string, Set<string>>()
  // The text-part channel. The plugin's experimental.text.complete hook
  // drains these blocks into the assistant's own message, so they reach the
  // transcript while the agent spends no tokens forming them.
  const notices = new Map<string, string[]>()
  const emitClosure = (sessionID: string, pin: WorkPin): boolean => {
    const key = `${pin.work_id}\u0000${pin.lifecycle}`
    let seen = emittedClosures.get(sessionID)
    if (!seen) {
      seen = new Set()
      rememberSession(emittedClosures, sessionID, seen)
    }
    if (seen.has(key)) return false
    while (seen.size >= MAX_CLOSURE_KEYS) {
      const oldest = seen.values().next()
      if (oldest.done) break
      seen.delete(oldest.value)
    }
    seen.add(key)
    return true
  }
  const enqueueNotice = (sessionID: string, block: string): void => {
    const queue = notices.get(sessionID) ?? []
    queue.push(block)
    notices.set(sessionID, queue)
  }
  return {
    // report refreshes the session title from each returned pin, queues the
    // closure banner for every terminal pin, and returns the warnings for
    // failed best-effort side effects. The caller appends the warnings to the
    // tool result output, because the agent is the only reader that can
    // respond to them. The reporter owns no terminal surface: it forks no
    // terminal command and names no terminal session.
    async report(envelope: MutationEnvelope, context: WorkflowStatusContext): Promise<string[]> {
      const warnings: string[] = []
      for (const pin of workPins(envelope)) {
        await refreshSessionGoalTitle(pin, context, warnings)
        const block = await formatWorkClosureReceipt(pin, { runner, binary, abort: context.abort, warnings })
        if (block !== null && emitClosure(context.sessionID, pin)) enqueueNotice(context.sessionID, block)
      }
      return warnings
    },
    enqueueNotice,
    takeNotices(sessionID: string): string[] {
      const queue = notices.get(sessionID)
      notices.delete(sessionID)
      return queue ?? []
    },
  }
}
