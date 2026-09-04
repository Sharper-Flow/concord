export type WorkflowStatusContext = { sessionID: string; abort: AbortSignal }

type WorkflowStatus = {
  work_id: string
  workflow_type: string
  step_ordinal: number
  step_total: number
  step_name: string
  transition_from: string
  transition_to: string
  last_event_kind: string
  actor: string
  occurred_at: string
  sequence: number
}

type StatusEnvelope = { outcome?: unknown; result?: unknown }
type StatusReader = (workID: string, context: WorkflowStatusContext) => Promise<StatusEnvelope>
type Toast = (message: string, context: WorkflowStatusContext) => Promise<boolean>
const MAX_STATUS_ENTRIES = 512

function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

function status(value: unknown): WorkflowStatus | null {
  if (!record(value) || !["work_id", "workflow_type", "step_name", "transition_from", "transition_to", "last_event_kind", "actor", "occurred_at"].every((key) => typeof value[key] === "string" && value[key].length > 0)) return null
  const stepOrdinal = value.step_ordinal
  const stepTotal = value.step_total
  const sequence = value.sequence
  if (typeof stepOrdinal !== "number" || typeof stepTotal !== "number" || typeof sequence !== "number" || !Number.isInteger(stepOrdinal) || stepOrdinal < 1 || !Number.isInteger(stepTotal) || stepTotal < stepOrdinal || !Number.isInteger(sequence) || sequence < 1) return null
  return value as unknown as WorkflowStatus
}

export function formatWorkflowStatus(value: unknown): string | null {
  const item = status(value)
  if (!item) return null
  return `WORKFLOW STATUS | work=${item.work_id} | workflow=${item.workflow_type} | step=${item.step_ordinal}/${item.step_total} ${item.step_name} | transition=${item.transition_from} -> ${item.transition_to} | event=${item.last_event_kind} | actor=${item.actor} | time=${item.occurred_at}`
}

export function createWorkflowStatusReporter(read: StatusReader, toast: Toast) {
  const seen = new Map<string, number>()

  return {
    async report(_tool: string, _operation: string, input: unknown, envelope: StatusEnvelope, context: WorkflowStatusContext): Promise<void> {
      if (envelope.outcome !== "ok" || !record(input) || typeof input.work_id !== "string") return
      const workID = input.work_id
      let latest: StatusEnvelope
      try { latest = await read(workID, context) } catch { return }
      if (latest.outcome !== "ok" || !record(latest.result)) return
      const rendered = formatWorkflowStatus(latest.result.workflow_status)
      const item = status(latest.result.workflow_status)
      if (!rendered || !item || (!item.last_event_kind.startsWith("workflow.") && !item.last_event_kind.startsWith("worker."))) return
      const key = `${context.sessionID}:${workID}`
      if (seen.get(key) === item.sequence) return
      seen.set(key, item.sequence)
      while (seen.size > MAX_STATUS_ENTRIES) {
        const oldest = seen.keys().next()
        if (oldest.done) break
        seen.delete(oldest.value)
      }
      try { await toast(rendered, context) } catch { /* status delivery is best effort */ }
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
  return `GATE BRIEF | product=${productID} | ${items.map((item) => `work=${item.work_id} | step=${item.workflow_step} | decision=${item.decision}`).join(" || ")}`
}
