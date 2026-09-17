import { afterEach, expect, test } from "bun:test"
import { createWorkStateReporter, formatGateBrief, formatWorkClosureReceipt, formatWorkTabName } from "./workflow-status"
import { hostControlPlane } from "./move-session"

const pin = {
  work_id: "work-1",
  title: "Repair the adapter",
  linear_issue_key: "",
  project_id: "project-1",
  project_display_name: "Concord",
  version: 4,
  lifecycle: "in_progress",
  workflow_type: "workflow.break_fix",
  step: "repair",
  pending_operator_decision: null,
}

afterEach(() => {
  delete process.env.ZELLIJ_PANE_ID
  hostControlPlane().bind(undefined)
})

function response(status = 200): Response {
  return new Response(null, { status })
}

test("formats a bounded WorkPin tab name", () => {
  expect(formatWorkTabName(pin)).toBe("Concord | 1 | repair")
  expect(formatWorkTabName({ ...pin, project_id: "", project_display_name: "" })).toBe("1 | repair")
  expect(formatWorkTabName({ ...pin, linear_issue_key: "CON-42" })).toBe("Concord | CON-42 | repair")
  expect(formatWorkTabName({ ...pin, project_display_name: "bad|name\nwith control" })).toBe("badnamewith control | 1 | repair")
  expect(formatWorkTabName({ ...pin, linear_issue_key: "CON\u0085-42", step: "repair|verify" })).toBe("Concord | CON-42 | repairverify")
  expect([...formatWorkTabName({ ...pin, project_display_name: "x".repeat(100) })!]).toHaveLength(64)
})

test("rejects an incomplete WorkPin tab name", () => {
  expect(formatWorkTabName({ ...pin, project_id: undefined })).toBeNull()
  expect(formatWorkTabName({ ...pin, project_display_name: "\u0000" })).toBe("1 | repair")
  expect(formatWorkTabName({ ...pin, step: "|" })).toBeNull()
})

test("formats a completed WorkPin as a closure receipt", () => {
  const completed = { ...pin, lifecycle: "completed", step: "complete" }
  const envelope = { outcome: "ok", evidence_refs: [{ kind: "commit", authority: "git", locator_kind: "commit", locator: "commit:abc123" }] }
  expect(formatWorkClosureReceipt(completed, envelope)).toBe("◆ CONCORD WORK CLOSURE | work-1 | title=Repair the adapter | release=pending | evidence=commit:abc123")
  expect(formatWorkClosureReceipt(pin, envelope)).toBeNull()
})

test("renames the tab mapped from the session pane", async () => {
  process.env.ZELLIJ_PANE_ID = "42"
  const calls: string[][] = []
  const runner = { async run(argv: string[]) {
    calls.push(argv)
    if (argv[2] === "list-panes") return { exitCode: 0, stdout: JSON.stringify([{ id: 42, is_plugin: false, tab_id: 21 }, { id: 99, is_plugin: false, tab_id: 30 }]), stderr: "" }
    return { exitCode: 0, stdout: "", stderr: "" }
  } }
  const reporter = createWorkStateReporter({ runner })
  const context = { sessionID: "session-1", abort: new AbortController().signal }
  await reporter.report({ outcome: "ok", result: { work_pins: [pin] } }, context)
  await reporter.report({ outcome: "ok", result: { work_pins: [{ ...pin, step: "verify" }] } }, context)
  expect(calls).toEqual([
    ["zellij", "action", "list-panes", "-a", "-j"],
    ["zellij", "action", "rename-tab-by-id", "21", "Concord | 1 | repair"],
    ["zellij", "action", "rename-tab-by-id", "21", "Concord | 1 | verify"],
  ])
})

test("keeps a closure receipt in the operator channel", async () => {
  process.env.ZELLIJ_PANE_ID = "42"
  const messages: string[] = []
  hostControlPlane().bind({
    get: async () => ({ response: response(), data: {} }),
    post: async ({ body }) => { messages.push(String((body as { message: string }).message)); return { response: response(204) } },
  })
  const reporter = createWorkStateReporter({ runner: { async run() { return { exitCode: 0, stdout: JSON.stringify([{ id: 42, is_plugin: false, tab_id: 21 }]), stderr: "" } } } })
  await reporter.report({
    outcome: "ok",
    evidence_refs: [{ kind: "pull_request", authority: "github", locator_kind: "url", locator: "https://github.com/example/repo/pull/7" }],
    result: { work_pins: [{ ...pin, lifecycle: "completed", step: "complete" }] },
  }, { sessionID: "session-closure", abort: new AbortController().signal })
  expect(messages).toEqual(["◆ CONCORD WORK CLOSURE | work-1 | title=Repair the adapter | release=pending | evidence=https://github.com/example/repo/pull/7"])
})

test("keeps tab rename failure best effort", async () => {
  process.env.ZELLIJ_PANE_ID = "42"
  const reporter = createWorkStateReporter({ runner: { async run() { throw new Error("zellij is absent") } } })
  await expect(reporter.report({ outcome: "ok", result: { work_pins: [pin] } }, { sessionID: "session-failure", abort: new AbortController().signal })).resolves.toBeUndefined()
})

test("formats the gate brief from focused portfolio rows", () => {
  expect(formatGateBrief("product-1", [
    { focus: { work_id: "work-1", workflow_step_label: "planning", attention_kind: "approval_required" } },
    { focus: { work_id: "work-2", workflow_step_label: "execution", attention_kind: "in_progress" } },
  ])).toBe("◆ CONCORD GATE BRIEF | product=product-1 | work=work-1 | step=planning | decision=pending || work=work-2 | step=execution | decision=none")
})
