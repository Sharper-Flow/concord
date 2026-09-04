import { expect, test } from "bun:test"
import { createWorkflowStatusReporter, formatGateBrief, formatWorkflowStatus } from "./workflow-status"

const status = {
  work_id: "work-1",
  workflow_type: "workflow.implementation",
  step_ordinal: 4,
  step_total: 7,
  step_name: "execution",
  transition_from: "planning",
  transition_to: "execution",
  last_event_kind: "workflow.action_completed",
  actor: "principal:operator",
  occurred_at: "2026-09-04T00:00:00Z",
  sequence: 12,
}

test("formats the fixed workflow status field set", () => {
  expect(formatWorkflowStatus(status)).toBe("WORKFLOW STATUS | work=work-1 | workflow=workflow.implementation | step=4/7 execution | transition=planning -> execution | event=workflow.action_completed | actor=principal:operator | time=2026-09-04T00:00:00Z")
})

test("rejects an incomplete workflow status", () => {
  expect(formatWorkflowStatus({ ...status, step_total: 3 })).toBeNull()
  expect(formatWorkflowStatus({ ...status, actor: "" })).toBeNull()
})

test("reports one toast for each new event sequence", async () => {
  const messages: string[] = []
  const context = { sessionID: "session-1", abort: new AbortController().signal }
  const reporter = createWorkflowStatusReporter(
    async () => ({ outcome: "ok", result: { workflow_status: status } }),
    async (message) => { messages.push(message); return true },
  )
  await reporter.report("concord_work_transition", "workflow_action", { work_id: "work-1" }, { outcome: "ok" }, context)
  await reporter.report("concord_work_transition", "workflow_action", { work_id: "work-1" }, { outcome: "ok" }, context)
  expect(messages).toHaveLength(1)
  expect(messages[0]).toContain("step=4/7 execution")
})

test("formats the gate brief from focused portfolio rows", () => {
  expect(formatGateBrief("product-1", [
    { focus: { work_id: "work-1", workflow_step_label: "planning", attention_kind: "approval_required" } },
    { focus: { work_id: "work-2", workflow_step_label: "execution", attention_kind: "in_progress" } },
  ])).toBe("GATE BRIEF | product=product-1 | work=work-1 | step=planning | decision=pending || work=work-2 | step=execution | decision=none")
})
