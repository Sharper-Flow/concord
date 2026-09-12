export type WorkflowStatusContext = { sessionID: string; abort: AbortSignal }

type WorkPin = {
  work_id: string
  title: string
  linear_issue_key: string
  version: number
  lifecycle: string
  workflow_type: string
  step: string
  pending_operator_decision: { action_id: string } | null
}

type MutationEnvelope = { outcome?: unknown; result?: unknown; evidence_refs?: unknown }
type Toast = (message: string, context: WorkflowStatusContext) => Promise<boolean>

const MAX_PENDING_SESSIONS = 512
const MAX_PENDING_WORK_ITEMS_PER_SESSION = 64

// A turn touches the session's own work item in every mutation it records, and
// a peer item only in the mutations that name it. The most-seen item is
// therefore the session's, which identifies it without an environment
// variable. A launcher-booted session exports CONCORD_SELECTED_WORK_ID and
// overrides the count.
export type WorkStatePin = { work_id: string; line: string; receipt?: string }
type PendingEntry = { line: string; receipt?: string; count: number; seq: number }

export type PendingWorkStateLineBuffer = {
  append: (sessionID: string, pins: WorkStatePin[]) => void
  drain: (sessionID: string) => string[]
}

function selectedWorkID(): string {
  const value = process.env.CONCORD_SELECTED_WORK_ID ?? ""
  return /^[A-Za-z0-9][A-Za-z0-9._:-]{1,127}$/.test(value) ? value : ""
}

export function createPendingWorkStateLineBuffer(): PendingWorkStateLineBuffer {
  const pending = new Map<string, Map<string, PendingEntry>>()
  let clock = 0

  function touch(sessionID: string): Map<string, PendingEntry> {
    const entries = pending.get(sessionID) ?? new Map<string, PendingEntry>()
    pending.delete(sessionID)
    pending.set(sessionID, entries)
    while (pending.size > MAX_PENDING_SESSIONS) {
      const oldest = pending.keys().next()
      if (oldest.done) break
      pending.delete(oldest.value)
    }
    return entries
  }

  return {
    append(sessionID, pins) {
      if (!sessionID || pins.length === 0) return
      const entries = touch(sessionID)
      for (const pin of pins) {
        clock += 1
        const existing = entries.get(pin.work_id)
        entries.set(pin.work_id, { line: pin.line, receipt: pin.receipt, count: (existing?.count ?? 0) + 1, seq: clock })
      }
      while (entries.size > MAX_PENDING_WORK_ITEMS_PER_SESSION) {
        let coldest: string | undefined
        for (const [workID, entry] of entries) {
          const held = coldest === undefined ? undefined : entries.get(coldest)
          if (!held || entry.count < held.count || (entry.count === held.count && entry.seq < held.seq)) coldest = workID
        }
        if (coldest === undefined) break
        entries.delete(coldest)
      }
    },
    // CD-0134: one session, one current state, one line. The buffer holds the
    // latest pin per work item so a turn reports where the session stands, not
    // every transition it passed through.
    drain(sessionID) {
      const entries = pending.get(sessionID)
      pending.delete(sessionID)
      if (!entries || entries.size === 0) return []
      const selected = selectedWorkID()
      const pinned = selected ? entries.get(selected) : undefined
      const chosenLine = pinned?.line
      let chosen: PendingEntry | undefined
      for (const entry of entries.values()) {
        if (!chosen || entry.count > chosen.count || (entry.count === chosen.count && entry.seq > chosen.seq)) chosen = entry
      }
      const lines = chosenLine ? [chosenLine] : chosen ? [chosen.line] : []
      for (const entry of [...entries.values()].sort((left, right) => left.seq - right.seq)) {
        if (entry.receipt) lines.push(entry.receipt)
      }
      return lines
    },
  }
}

export const pendingWorkStateLineBuffer = createPendingWorkStateLineBuffer()

function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

function safeText(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && !/[\u0000-\u001f\u007f|]/u.test(value)
}

function safeTitle(value: unknown): string | null {
  if (typeof value !== "string" || value.length === 0) return null
  const sanitized = value.replace(/[\u0000-\u001f\u007f|]/gu, " ")
  return sanitized.length > 64 ? `${sanitized.slice(0, 63)}…` : sanitized
}

function workPin(value: unknown): WorkPin | null {
  if (!record(value) || !safeText(value.work_id) || !safeText(value.lifecycle) || !safeText(value.step)) return null
  const title = safeTitle(value.title)
  if (title === null || typeof value.linear_issue_key !== "string" || (!safeText(value.linear_issue_key) && value.linear_issue_key !== "")) return null
  if (typeof value.version !== "number" || !Number.isInteger(value.version) || value.version < 1) return null
  if (value.pending_operator_decision !== null) {
    if (!record(value.pending_operator_decision) || !safeText(value.pending_operator_decision.action_id)) return null
  }
  return { ...value, title } as unknown as WorkPin
}

export function formatWorkStateLine(value: unknown): string | null {
  const pin = workPin(value)
  if (!pin) return null
  const decision = pin.pending_operator_decision === null ? "none" : `pending:${pin.pending_operator_decision.action_id}`
  const identifier = pin.linear_issue_key || pin.work_id
  return `◆ CONCORD WORK STATE | ${identifier} | title=${pin.title} | version=${pin.version} | lifecycle=${pin.lifecycle} | step=${pin.step} | decision=${decision}`
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

export function workStatePins(envelope: unknown): WorkStatePin[] {
  if (!record(envelope) || envelope.outcome !== "ok") return []
  const payload = record(envelope.result) ? envelope.result : envelope
  if (!Array.isArray(payload.work_pins)) return []
  const pins = payload.work_pins.map((value) => ({ pin: workPin(value), line: formatWorkStateLine(value) }))
  if (pins.some((item) => item.pin === null || item.line === null)) return []
  return pins
    .map((item) => {
      const result: WorkStatePin = { work_id: item.pin!.work_id, line: item.line! }
      const receipt = formatWorkClosureReceipt(item.pin, envelope as MutationEnvelope)
      if (receipt) result.receipt = receipt
      return result
    })
    .sort((left, right) => left.line < right.line ? -1 : left.line > right.line ? 1 : 0)
}

export function workStateLines(envelope: unknown): string[] {
  return workStatePins(envelope).map((pin) => pin.line)
}

export function createWorkStateReporter(toast: Toast) {
  return {
    async report(envelope: MutationEnvelope, context: WorkflowStatusContext): Promise<void> {
      const pins = workStatePins(envelope)
      pendingWorkStateLineBuffer.append(context.sessionID, pins)
      for (const pin of pins) {
        try { await toast(pin.line, context) } catch { /* state delivery is best effort */ }
        if (pin.receipt) {
          try { await toast(pin.receipt, context) } catch { /* closure delivery is best effort */ }
        }
      }
    },
  }
}

export function appendPendingWorkStateLines(sessionID: string, text: string): string {
  const lines = pendingWorkStateLineBuffer.drain(sessionID)
  if (lines.length === 0) return text
  return text.length === 0 ? lines.join("\n") : `${text}\n${lines.join("\n")}`
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
