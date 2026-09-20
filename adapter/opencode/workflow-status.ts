import { defaultRunner, type DispatchRunner } from "./dispatch"
import { hostControlPlane } from "./move-session"

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
}

type MutationEnvelope = { outcome?: unknown; result?: unknown; evidence_refs?: unknown }

const TAB_MAPPING_TTL_MS = 10_000
const MAX_NAME_CODE_POINTS = 64
const IDENTIFIER_CODE_POINTS = 8

function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

function safeText(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && !/[\u0000-\u001f\u007f|]/u.test(value)
}

function sanitizeTabField(value: string): string {
  return [...value].filter((character) => {
    const code = character.codePointAt(0) ?? 0
    return code > 0x1f && !(code >= 0x7f && code <= 0x9f) && character !== "|"
  }).join("")
}

function capCodePoints(value: string): string {
  return [...value].slice(0, MAX_NAME_CODE_POINTS).join("")
}

function shortWorkID(workID: string): string {
  return [...workIDSegment(workID)].slice(0, IDENTIFIER_CODE_POINTS).join("")
}

function safeTitle(value: unknown): string | null {
  if (typeof value !== "string" || value.length === 0) return null
  const sanitized = value.replace(/[\u0000-\u001f\u007f|]/gu, " ")
  return sanitized.length > 64 ? `${sanitized.slice(0, 63)}…` : sanitized
}

function workPin(value: unknown): WorkPin | null {
  if (!record(value) || typeof value.work_id !== "string" || typeof value.lifecycle !== "string" || typeof value.step !== "string") return null
  const title = safeTitle(value.title)
  const workID = sanitizeTabField(value.work_id)
  const lifecycle = sanitizeTabField(value.lifecycle)
  const step = sanitizeTabField(value.step)
  const projectID = typeof value.project_id === "string" ? sanitizeTabField(value.project_id) : ""
  const projectDisplayName = typeof value.project_display_name === "string" ? sanitizeTabField(value.project_display_name) : ""
  const linearIssueKey = typeof value.linear_issue_key === "string" ? sanitizeTabField(value.linear_issue_key) : ""
  if (title === null || workID === "" || lifecycle === "" || step === "" || typeof value.project_id !== "string" || typeof value.project_display_name !== "string" || typeof value.linear_issue_key !== "string") return null
  if (typeof value.version !== "number" || !Number.isInteger(value.version) || value.version < 1) return null
  if (value.pending_operator_decision !== null) {
    if (!record(value.pending_operator_decision) || !safeText(value.pending_operator_decision.action_id)) return null
  }
  return { ...value, work_id: workID, lifecycle, step, project_id: projectID, project_display_name: projectDisplayName, linear_issue_key: linearIssueKey, title } as unknown as WorkPin
}

function workIDSegment(workID: string): string {
  return workID.split(/[-/:]/u).pop() || workID
}

// The tab carries one stub: the linear_issue_key when the Product is
// Linear-enabled, otherwise the project stub. The full work state moves to the
// pane frame, which spans the pane width and holds what the tab bar cannot.
export function formatWorkTabName(value: unknown): string | null {
  const pin = workPin(value)
  if (!pin) return null
  return capCodePoints(pin.linear_issue_key || pin.project_id) || null
}

function paneField(value: unknown): string {
  return typeof value === "string" ? sanitizeTabField(value) : ""
}

// formatWorkPaneName renders the pane frame name: project display name,
// identifier, step, and work title joined with " | " and cut at 64 code
// points. It reads each field leniently and drops the absent ones, so the
// mutation reporter can pass a whole WorkPin while work_start passes the
// session-prepare title alone, and both produce the same shape.
export function formatWorkPaneName(value: unknown): string | null {
  if (!record(value)) return null
  const identifier = paneField(value.linear_issue_key) || shortWorkID(paneField(value.work_id))
  const fields = [paneField(value.project_display_name), identifier, paneField(value.step), paneField(value.title)]
  return capCodePoints(fields.filter(Boolean).join(" | ")) || null
}

function evidenceLocators(envelope: MutationEnvelope): string[] | null {
  if (envelope.outcome !== "ok" || !Array.isArray(envelope.evidence_refs)) return null
  const locators = envelope.evidence_refs.map((value) => record(value) && safeText(value.locator) ? value.locator : null)
  if (locators.some((locator) => locator === null)) return null
  return locators as string[]
}

const TERMINAL_LIFECYCLES: ReadonlySet<string> = new Set(["completed", "cancelled", "superseded"])

// formatQuestComplete renders the one closure banner a terminal work pin
// produces. The gate is the terminal lifecycle alone: evidence moved out of
// the gate and into the rendering, so a closure with no readable evidence
// prints `evidence=none` instead of closing the item in silence. A completed
// pin gets the celebratory banner; a cancelled or superseded pin gets a
// visibly plainer marker with no celebration. The exact bytes are fixed by
// the golden tests.
//
// The block is fenced. The host renders an assistant text part as markdown
// through `marked` with its default `breaks: false`, so a single newline is a
// soft break and collapses to a space: an unfenced banner would reach the
// operator as one run-on line. The fence also holds the glyph columns in a
// monospace block, which is the whole point of a banner.
export function formatQuestComplete(value: unknown, envelope: MutationEnvelope): string | null {
  const pin = workPin(value)
  if (!pin || !TERMINAL_LIFECYCLES.has(pin.lifecycle)) return null
  const locators = evidenceLocators(envelope)
  const evidence = locators !== null && locators.length > 0 ? `evidence=${locators.length}` : "evidence=none"
  const identifier = pin.linear_issue_key ? `${pin.linear_issue_key} (${pin.work_id})` : pin.work_id
  const head = pin.lifecycle === "completed" ? "◆◆◆ QUEST COMPLETE ◆◆◆" : "◆ CONCORD WORK CLOSED"
  return `\`\`\`\n${head}\n${identifier} | ${pin.project_display_name}\n${pin.title}\nlifecycle=${pin.lifecycle} | ${evidence}\n\`\`\``
}

function workPins(envelope: unknown): WorkPin[] {
  if (!record(envelope) || envelope.outcome !== "ok") return []
  const payload = record(envelope.result) ? envelope.result : envelope
  if (!Array.isArray(payload.work_pins)) return []
  const pins = payload.work_pins.map(workPin)
  return pins.every((pin): pin is WorkPin => pin !== null) ? pins : []
}

type TabMapping = { attemptedAt: number; tabID?: string }
type WorkStateReporterOptions = { runner?: DispatchRunner; now?: () => number }

async function renameZellijTab(pin: WorkPin, context: WorkflowStatusContext, runner: DispatchRunner, now: () => number, mappings: Map<string, TabMapping>, warnings: string[]): Promise<void> {
  const paneID = process.env.ZELLIJ_PANE_ID
  if (!paneID) return
  const name = formatWorkTabName(pin)
  if (!name) return
  const cached = mappings.get(context.sessionID)
  const attemptedAt = now()
  let tabID = cached && attemptedAt >= cached.attemptedAt && attemptedAt - cached.attemptedAt < TAB_MAPPING_TTL_MS ? cached.tabID : undefined
  try {
    if (!tabID) {
      const listing = await runner.run(["zellij", "action", "list-panes", "-a", "-j"], "", context.abort)
      if (listing.exitCode !== 0) throw new Error(`list-panes exited ${listing.exitCode}`)
      const panes = JSON.parse(listing.stdout)
      const pane = Array.isArray(panes) ? panes.find((candidate) => record(candidate) && String(candidate.id) === paneID && candidate.is_plugin === false) : undefined
      if (!record(pane) || (typeof pane.tab_id !== "string" && typeof pane.tab_id !== "number") || String(pane.tab_id) === "") throw new Error("the pane is absent from the zellij listing")
      tabID = String(pane.tab_id)
      mappings.set(context.sessionID, { attemptedAt: now(), tabID })
    }
    const result = await runner.run(["zellij", "action", "rename-tab-by-id", tabID, name], "", context.abort)
    if (result.exitCode !== 0) throw new Error(`rename-tab-by-id exited ${result.exitCode}`)
    const paneName = formatWorkPaneName(pin)
    if (paneName) {
      const paneResult = await runner.run(["zellij", "action", "rename-pane", "-p", paneID, paneName], "", context.abort)
      if (paneResult.exitCode !== 0) throw new Error(`rename-pane exited ${paneResult.exitCode}`)
    }
  } catch (error) {
    mappings.set(context.sessionID, { attemptedAt: now(), ...(tabID ? { tabID } : {}) })
    warnings.push(`Concord could not rename the work tab or pane frame: ${error instanceof Error ? error.message : String(error)}.`)
  }
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
  const now = options.now ?? Date.now
  const mappings = new Map<string, TabMapping>()
  // A terminal pin reappears in the pins of any later mutation that touches
  // the same item, so the closure banner is emitted once per sessionID,
  // work_id, and terminal lifecycle triple.
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
      emittedClosures.set(sessionID, seen)
    }
    if (seen.has(key)) return false
    seen.add(key)
    return true
  }
  const enqueueNotice = (sessionID: string, block: string): void => {
    const queue = notices.get(sessionID) ?? []
    queue.push(block)
    notices.set(sessionID, queue)
  }
  return {
    // report refreshes the tab, pane, and session title from each returned
    // pin, queues the closure banner for every terminal pin, and returns the
    // warnings for failed best-effort side effects. The caller appends the
    // warnings to the tool result output, because the agent is the only
    // reader that can respond to them.
    async report(envelope: MutationEnvelope, context: WorkflowStatusContext): Promise<string[]> {
      const warnings: string[] = []
      for (const pin of workPins(envelope)) {
        await renameZellijTab(pin, context, runner, now, mappings, warnings)
        await refreshSessionGoalTitle(pin, context, warnings)
        const block = formatQuestComplete(pin, envelope)
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

export type GateBriefRow = { work_id: string; workflow_step: string; decision: "pending" | "none" }

export function formatGateBrief(productID: string, rows: unknown): string | null {
  if (!productID || !Array.isArray(rows) || rows.length === 0) return null
  const items: GateBriefRow[] = []
  for (const row of rows) {
    if (!record(row) || !record(row.focus) || typeof row.focus.work_id !== "string" || typeof row.focus.workflow_step_label !== "string") continue
    items.push({ work_id: row.focus.work_id, workflow_step: row.focus.workflow_step_label, decision: row.focus.attention_kind === "approval_required" ? "pending" : "none" })
  }
  if (items.length === 0) return null
  return `◆ CONCORD GATE BRIEF | product=${productID} | ${items.map((item) => `work=${item.work_id} | step=${item.workflow_step} | decision=${item.decision}`).join(" || ")}`
}
