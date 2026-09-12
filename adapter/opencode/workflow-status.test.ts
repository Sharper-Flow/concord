import { expect, test } from "bun:test"
import { appendPendingWorkStateLines, createPendingWorkStateLineBuffer, createWorkStateReporter, formatGateBrief, formatWorkClosureReceipt, formatWorkStateLine, workStateLines } from "./workflow-status"

const pin = {
  work_id: "work-1",
  title: "Repair the adapter",
  linear_issue_key: "",
  version: 4,
  lifecycle: "in_progress",
  workflow_type: "workflow.break_fix",
  step: "repair",
  pending_operator_decision: null,
}

test("formats the fixed WorkPin state line", () => {
  expect(formatWorkStateLine(pin)).toBe("◆ CONCORD WORK STATE | work-1 | title=Repair the adapter | version=4 | lifecycle=in_progress | step=repair | decision=none")
  expect(formatWorkStateLine({ ...pin, linear_issue_key: "CON-42" })).toContain("◆ CONCORD WORK STATE | CON-42 | title=Repair the adapter")
  expect(formatWorkStateLine({ ...pin, title: "bad|title\nwith control" })).toContain("title=bad title with control")
  expect(formatWorkStateLine({ ...pin, title: "x".repeat(65) })).toContain(`title=${"x".repeat(63)}…`)
  expect(formatWorkStateLine({ ...pin, pending_operator_decision: { action_id: "approve-repair" } })).toContain("decision=pending:approve-repair")
})

test("rejects an incomplete or unsafe WorkPin", () => {
  expect(formatWorkStateLine({ ...pin, version: 0 })).toBeNull()
  expect(formatWorkStateLine({ ...pin, step: "repair|unsafe" })).toBeNull()
  expect(formatWorkStateLine({ ...pin, pending_operator_decision: {} })).toBeNull()
})

test("formats a completed WorkPin as a closure receipt", () => {
  const completed = { ...pin, lifecycle: "completed", step: "complete" }
  const envelope = { outcome: "ok", evidence_refs: [{ kind: "commit", authority: "git", locator_kind: "commit", locator: "commit:abc123" }] }
  expect(formatWorkClosureReceipt(completed, envelope)).toBe("◆ CONCORD WORK CLOSURE | work-1 | title=Repair the adapter | release=pending | evidence=commit:abc123")
  expect(formatWorkClosureReceipt(pin, envelope)).toBeNull()
  expect(formatWorkClosureReceipt(completed, { outcome: "ok", evidence_refs: [] })).toBeNull()
})

test("renders every mutation WorkPin in stable order", () => {
  const second = { ...pin, work_id: "work-2", version: 5, step: "verify" }
  expect(workStateLines({ outcome: "ok", result: { work_pins: [second, pin] } })).toEqual([
    "◆ CONCORD WORK STATE | work-1 | title=Repair the adapter | version=4 | lifecycle=in_progress | step=repair | decision=none",
    "◆ CONCORD WORK STATE | work-2 | title=Repair the adapter | version=5 | lifecycle=in_progress | step=verify | decision=none",
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
    "assistant text\n◆ CONCORD WORK STATE | work-1 | title=Repair the adapter | version=4 | lifecycle=in_progress | step=repair | decision=none",
  )
  expect(appendPendingWorkStateLines("session-1", "next text")).toBe("next text")
})

test("appends a completed receipt with the state line to the transcript", async () => {
  const context = { sessionID: "session-closure", abort: new AbortController().signal }
  const reporter = createWorkStateReporter(async () => true)
  await reporter.report({
    outcome: "ok",
    evidence_refs: [{ kind: "pull_request", authority: "github", locator_kind: "url", locator: "https://github.com/example/repo/pull/7" }],
    result: { work_pins: [{ ...pin, lifecycle: "completed", step: "complete" }] },
  }, context)

  expect(appendPendingWorkStateLines("session-closure", "assistant text")).toBe(
    "assistant text\n◆ CONCORD WORK STATE | work-1 | title=Repair the adapter | version=4 | lifecycle=completed | step=complete | decision=none\n◆ CONCORD WORK CLOSURE | work-1 | title=Repair the adapter | release=pending | evidence=https://github.com/example/repo/pull/7",
  )
})

test("keeps one closure receipt for each completed WorkPin", async () => {
  const context = { sessionID: "session-closures", abort: new AbortController().signal }
  const reporter = createWorkStateReporter(async () => true)
  const evidence_refs = [{ kind: "commit", authority: "git", locator_kind: "commit", locator: "commit:abc123" }]
  await reporter.report({
    outcome: "ok",
    evidence_refs,
    result: { work_pins: [
      { ...pin, work_id: "work-1", lifecycle: "completed", step: "complete" },
      { ...pin, work_id: "work-2", lifecycle: "completed", step: "complete" },
    ] },
  }, context)

  const text = appendPendingWorkStateLines("session-closures", "")
  expect(text.match(/◆ CONCORD WORK CLOSURE/g)).toHaveLength(2)
  expect(text).toContain("◆ CONCORD WORK CLOSURE | work-1")
  expect(text).toContain("◆ CONCORD WORK CLOSURE | work-2")
})

test("keeps a bounded pending buffer and drains one line", () => {
  const buffer = createPendingWorkStateLineBuffer()
  buffer.append("session-1", Array.from({ length: 129 }, (_, index) => ({ work_id: `work-${index}`, line: `line-${index}` })))
  expect(buffer.drain("session-1")).toEqual(["line-128"])
  expect(buffer.drain("session-1")).toEqual([])
})

test("emits one line for the session work item a turn touches most", async () => {
  const context = { sessionID: "session-many", abort: new AbortController().signal }
  const reporter = createWorkStateReporter(async () => true)
  const peer = (index: number) => ({ ...pin, work_id: `peer-${index}`, version: 9 })
  // A turn that resolves twenty overlaps records the session item in every
  // mutation and each peer once.
  for (let index = 0; index < 20; index += 1) {
    await reporter.report({ outcome: "ok", result: { work_pins: [pin, peer(index)] } }, context)
  }

  const text = appendPendingWorkStateLines("session-many", "assistant text")
  expect(text.split("\n").filter((line) => line.startsWith("◆ CONCORD WORK STATE"))).toEqual([
    "◆ CONCORD WORK STATE | work-1 | title=Repair the adapter | version=4 | lifecycle=in_progress | step=repair | decision=none",
  ])
})

test("prefers the launcher-selected work item over the turn count", async () => {
  const context = { sessionID: "session-selected", abort: new AbortController().signal }
  const reporter = createWorkStateReporter(async () => true)
  process.env.CONCORD_SELECTED_WORK_ID = "work-2"
  try {
    const selected = { ...pin, work_id: "work-2", version: 5, step: "verify" }
    await reporter.report({ outcome: "ok", result: { work_pins: [pin, selected] } }, context)
    await reporter.report({ outcome: "ok", result: { work_pins: [pin] } }, context)
    expect(appendPendingWorkStateLines("session-selected", "")).toBe(
      "◆ CONCORD WORK STATE | work-2 | title=Repair the adapter | version=5 | lifecycle=in_progress | step=verify | decision=none",
    )
  } finally {
    delete process.env.CONCORD_SELECTED_WORK_ID
  }
})

test("formats the gate brief from focused portfolio rows", () => {
  expect(formatGateBrief("product-1", [
    { focus: { work_id: "work-1", workflow_step_label: "planning", attention_kind: "approval_required" } },
    { focus: { work_id: "work-2", workflow_step_label: "execution", attention_kind: "in_progress" } },
  ])).toBe("◆ CONCORD GATE BRIEF | product=product-1 | work=work-1 | step=planning | decision=pending || work=work-2 | step=execution | decision=none")
})
