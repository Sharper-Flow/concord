export type WorkflowStatusContext = { sessionID: string; abort: AbortSignal }

type WorkPin = {
  work_id: string
  version: number
  lifecycle: string
  workflow_type: string
  step: string
  pending_operator_decision: { action_id: string } | null
}

type MutationEnvelope = { outcome?: unknown; result?: unknown }
type Toast = (message: string, context: WorkflowStatusContext) => Promise<boolean>

const MAX_PENDING_SESSIONS = 512
const MAX_PENDING_LINES_PER_SESSION = 128

export type PendingWorkStateLineBuffer = {
  append: (sessionID: string, lines: string[]) => void
  drain: (sessionID: string) => string[]
}

export function createPendingWorkStateLineBuffer(): PendingWorkStateLineBuffer {
  const pending = new Map<string, string[]>()

  function touch(sessionID: string, lines: string[]): void {
    pending.delete(sessionID)
    pending.set(sessionID, lines)
    while (pending.size > MAX_PENDING_SESSIONS) {
      const oldest = pending.keys().next()
      if (oldest.done) break
      pending.delete(oldest.value)
    }
  }

  return {
    append(sessionID, lines) {
      if (!sessionID || lines.length === 0) return
      const existing = pending.get(sessionID) ?? []
      touch(sessionID, existing.concat(lines).slice(-MAX_PENDING_LINES_PER_SESSION))
    },
    drain(sessionID) {
      const lines = pending.get(sessionID) ?? []
      pending.delete(sessionID)
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

function workPin(value: unknown): WorkPin | null {
  if (!record(value) || !safeText(value.work_id) || !safeText(value.lifecycle) || !safeText(value.workflow_type) || !safeText(value.step)) return null
  if (typeof value.version !== "number" || !Number.isInteger(value.version) || value.version < 1) return null
  if (value.pending_operator_decision !== null) {
    if (!record(value.pending_operator_decision) || !safeText(value.pending_operator_decision.action_id)) return null
  }
  return value as unknown as WorkPin
}

export function formatWorkStateLine(value: unknown): string | null {
  const pin = workPin(value)
  if (!pin) return null
  const decision = pin.pending_operator_decision === null ? "none" : `pending:${pin.pending_operator_decision.action_id}`
  return `◆ CONCORD WORK STATE | work=${pin.work_id} | version=${pin.version} | lifecycle=${pin.lifecycle} | workflow=${pin.workflow_type} | step=${pin.step} | decision=${decision}`
}

export function workStateLines(envelope: unknown): string[] {
  if (!record(envelope) || envelope.outcome !== "ok") return []
  const payload = record(envelope.result) ? envelope.result : envelope
  if (!Array.isArray(payload.work_pins)) return []
  const pins = payload.work_pins.map((value) => ({ value, line: formatWorkStateLine(value) }))
  if (pins.some((item) => item.line === null)) return []
  return pins
    .sort((left, right) => left.line! < right.line! ? -1 : left.line! > right.line! ? 1 : 0)
    .map((item) => item.line!)
}

export function createWorkStateReporter(toast: Toast) {
  return {
    async report(envelope: MutationEnvelope, context: WorkflowStatusContext): Promise<void> {
      const lines = workStateLines(envelope)
      pendingWorkStateLineBuffer.append(context.sessionID, lines)
      for (const line of lines) {
        try { await toast(line, context) } catch { /* state delivery is best effort */ }
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
