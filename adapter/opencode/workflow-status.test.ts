import { expect, test } from "bun:test"
import { appendPendingWorkStateLines, createPendingWorkStateLineBuffer, createWorkStateReporter, formatGateBrief, formatWorkStateLine, workStateLines } from "./workflow-status"

const pin = {
  work_id: "work-1",
  version: 4,
  lifecycle: "in_progress",
  workflow_type: "workflow.break_fix",
  step: "repair",
  pending_operator_decision: null,
}

test("formats the fixed WorkPin state line", () => {
  expect(formatWorkStateLine(pin)).toBe("◆ CONCORD WORK STATE | work=work-1 | version=4 | lifecycle=in_progress | workflow=workflow.break_fix | step=repair | decision=none")
  expect(formatWorkStateLine({ ...pin, pending_operator_decision: { action_id: "approve-repair" } })).toContain("decision=pending:approve-repair")
})

test("rejects an incomplete or unsafe WorkPin", () => {
  expect(formatWorkStateLine({ ...pin, version: 0 })).toBeNull()
  expect(formatWorkStateLine({ ...pin, step: "repair|unsafe" })).toBeNull()
  expect(formatWorkStateLine({ ...pin, pending_operator_decision: {} })).toBeNull()
})

test("renders every mutation WorkPin in stable order", () => {
  const second = { ...pin, work_id: "work-2", version: 5, step: "verify" }
  expect(workStateLines({ outcome: "ok", result: { work_pins: [second, pin] } })).toEqual([
    "◆ CONCORD WORK STATE | work=work-1 | version=4 | lifecycle=in_progress | workflow=workflow.break_fix | step=repair | decision=none",
    "◆ CONCORD WORK STATE | work=work-2 | version=5 | lifecycle=in_progress | workflow=workflow.break_fix | step=verify | decision=none",
  ])
  expect(workStateLines({ outcome: "ok", result: { work_pins: [pin, { ...pin, step: "unsafe|step" }] } })).toEqual([])
})

test("reports one toast for each mutation result", async () => {
  const messages: string[] = []
  const context = { sessionID: "session-toast", abort: new AbortController().signal }
  const reporter = createWorkStateReporter(async (message) => { messages.push(message); return true })
  await reporter.report({ outcome: "ok", result: { work_pins: [pin] } }, context)
  await reporter.report({ outcome: "ok", result: { work_pins: [pin] } }, context)
  expect(messages).toHaveLength(2)
  expect(messages[0]).toContain("◆ CONCORD WORK STATE")
})

test("buffers lines per session and appends them to completed text", async () => {
  const context = { sessionID: "session-1", abort: new AbortController().signal }
  const reporter = createWorkStateReporter(async () => true)
  await reporter.report({ outcome: "ok", result: { work_pins: [pin] } }, context)

  expect(appendPendingWorkStateLines("session-2", "assistant text")).toBe("assistant text")
  expect(appendPendingWorkStateLines("session-1", "assistant text")).toBe(
    "assistant text\n◆ CONCORD WORK STATE | work=work-1 | version=4 | lifecycle=in_progress | workflow=workflow.break_fix | step=repair | decision=none",
  )
  expect(appendPendingWorkStateLines("session-1", "next text")).toBe("next text")
})

test("keeps a bounded pending line buffer and drains it", () => {
  const buffer = createPendingWorkStateLineBuffer()
  buffer.append("session-1", Array.from({ length: 129 }, (_, index) => `line-${index}`))
  expect(buffer.drain("session-1")).toHaveLength(128)
  expect(buffer.drain("session-1")).toEqual([])
})

test("formats the gate brief from focused portfolio rows", () => {
  expect(formatGateBrief("product-1", [
    { focus: { work_id: "work-1", workflow_step_label: "planning", attention_kind: "approval_required" } },
    { focus: { work_id: "work-2", workflow_step_label: "execution", attention_kind: "in_progress" } },
  ])).toBe("◆ CONCORD GATE BRIEF | product=product-1 | work=work-1 | step=planning | decision=pending || work=work-2 | step=execution | decision=none")
})
