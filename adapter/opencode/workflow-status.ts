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
const MAX_TAB_NAME_CODE_POINTS = 64

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

function sanitizeTabName(value: string): string {
  return [...value].slice(0, MAX_TAB_NAME_CODE_POINTS).join("")
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

export function formatWorkTabName(value: unknown): string | null {
  const pin = workPin(value)
  if (!pin) return null
  const identifier = pin.linear_issue_key || workIDSegment(pin.work_id)
  return sanitizeTabName([pin.project_display_name, identifier, pin.step].filter(Boolean).join(" | ")) || null
}

function evidenceLocators(envelope: MutationEnvelope): string[] | null {
  if (envelope.outcome !== "ok" || !Array.isArray(envelope.evidence_refs)) return null
  const locators = envelope.evidence_refs.map((value) => record(value) && safeText(value.locator) ? value.locator : null)
  if (locators.some((locator) => locator === null)) return null
  return locators as string[]
}

export function formatWorkClosureReceipt(value: unknown, envelope: MutationEnvelope): string | null {
  const pin = workPin(value)
  const locators = evidenceLocators(envelope)
  if (!pin || pin.lifecycle !== "completed" || !locators || locators.length === 0) return null
  const identifier = pin.linear_issue_key || pin.work_id
  return `◆ CONCORD WORK CLOSURE | ${identifier} | title=${pin.title} | release=pending | evidence=${locators.join(",")}`
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

async function renameZellijTab(pin: WorkPin, context: WorkflowStatusContext, runner: DispatchRunner, now: () => number, mappings: Map<string, TabMapping>): Promise<void> {
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
  } catch (error) {
    mappings.set(context.sessionID, { attemptedAt: now(), ...(tabID ? { tabID } : {}) })
    try { await hostControlPlane().showToast(`Concord could not rename the work tab: ${error instanceof Error ? error.message : String(error)}.`, "warning", context.abort) } catch { /* best effort */ }
  }
}

export function createWorkStateReporter(options: WorkStateReporterOptions = {}) {
  const runner = options.runner ?? defaultRunner
  const now = options.now ?? Date.now
  const mappings = new Map<string, TabMapping>()
  return {
    async report(envelope: MutationEnvelope, context: WorkflowStatusContext): Promise<void> {
      const pins = workPins(envelope)
      for (const pin of pins) {
        await renameZellijTab(pin, context, runner, now, mappings)
        const receipt = formatWorkClosureReceipt(pin, envelope)
        if (receipt) {
          try { await hostControlPlane().showToast(receipt, "info", context.abort) } catch { /* closure delivery is best effort */ }
        }
      }
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
