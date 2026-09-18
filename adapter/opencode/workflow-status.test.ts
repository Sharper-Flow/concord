import { afterEach, expect, test } from "bun:test"
import { createWorkStateReporter, formatGateBrief, formatWorkClosureReceipt, formatWorkPaneName, formatWorkTabName } from "./workflow-status"
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

test("formats the tab name as the Linear key or project stub alone", () => {
  expect(formatWorkTabName(pin)).toBe("project-1")
  expect(formatWorkTabName({ ...pin, linear_issue_key: "CON-42" })).toBe("CON-42")
  expect(formatWorkTabName({ ...pin, work_id: "work-6f7ebcb6c3b8e11924e6c6a7" })).toBe("project-1")
  expect(formatWorkTabName({ ...pin, linear_issue_key: "CON\u0085-42", step: "repair|verify" })).toBe("CON-42")
})

test("rejects an incomplete WorkPin tab name", () => {
  expect(formatWorkTabName({ ...pin, project_id: undefined })).toBeNull()
  expect(formatWorkTabName({ ...pin, project_id: "" })).toBeNull()
  expect(formatWorkTabName({ ...pin, project_display_name: "\u0000" })).toBe("project-1")
  expect(formatWorkTabName({ ...pin, step: "|" })).toBeNull()
  expect([...formatWorkTabName({ ...pin, linear_issue_key: "x".repeat(100) })!]).toHaveLength(64)
})

test("formats the pane name as the full work state", () => {
  expect(formatWorkPaneName(pin)).toBe("Concord | 1 | repair | Repair the adapter")
  expect(formatWorkPaneName({ ...pin, linear_issue_key: "CON-42" })).toBe("Concord | CON-42 | repair | Repair the adapter")
  expect(formatWorkPaneName({ ...pin, project_id: "", project_display_name: "" })).toBe("1 | repair | Repair the adapter")
  expect(formatWorkPaneName({ ...pin, title: "bad|name\nwith control" })).toBe("Concord | 1 | repair | badnamewith control")
  expect([...formatWorkPaneName({ ...pin, title: "x".repeat(100) })!]).toHaveLength(64)
})

test("formats the pane name from a title alone", () => {
  expect(formatWorkPaneName({ title: "Add atomic start" })).toBe("Add atomic start")
  expect(formatWorkPaneName({ title: `ab\u0001cd\u009Fef${"g".repeat(70)}` })).toBe(`abcdefg${"g".repeat(57)}`)
  expect(formatWorkPaneName({ title: "" })).toBeNull()
  expect(formatWorkPaneName(null)).toBeNull()
  expect(formatWorkPaneName(42)).toBeNull()
})

test("formats a completed WorkPin as a closure receipt", () => {
  const completed = { ...pin, lifecycle: "completed", step: "complete" }
  const envelope = { outcome: "ok", evidence_refs: [{ kind: "commit", authority: "git", locator_kind: "commit", locator: "commit:abc123" }] }
  expect(formatWorkClosureReceipt(completed, envelope)).toBe("◆ CONCORD WORK CLOSURE | work-1 | title=Repair the adapter | release=pending | evidence=commit:abc123")
  expect(formatWorkClosureReceipt(pin, envelope)).toBeNull()
})

test("renames the tab and pane frame mapped from the session pane", async () => {
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
    ["zellij", "action", "rename-tab-by-id", "21", "project-1"],
    ["zellij", "action", "rename-pane", "-p", "42", "Concord | 1 | repair | Repair the adapter"],
    ["zellij", "action", "rename-tab-by-id", "21", "project-1"],
    ["zellij", "action", "rename-pane", "-p", "42", "Concord | 1 | verify | Repair the adapter"],
  ])
})

test("the reporter refreshes the session goal title from the pin", async () => {
  process.env.ZELLIJ_PANE_ID = "42"
  const titles: Array<{ url: string; path?: Record<string, unknown>; body?: unknown; signal?: unknown }> = []
  hostControlPlane().bind({
    get: async () => ({ response: response(), data: {} }),
    post: async () => ({ response: response(204) }),
    patch: async (options) => { titles.push(options); return { response: response() } },
  })
  const reporter = createWorkStateReporter({ runner: { async run() { return { exitCode: 0, stdout: JSON.stringify([{ id: 42, is_plugin: false, tab_id: 21 }]), stderr: "" } } } })
  await reporter.report({ outcome: "ok", result: { work_pins: [{ ...pin, title: "Revised intent" }] } }, { sessionID: "session-goal", abort: new AbortController().signal })
  expect(titles).toEqual([{ url: "/session/{id}", path: { id: "session-goal" }, body: { title: "Goal: Revised intent" }, signal: expect.any(AbortSignal) }])
})

test("a failed session goal title write stays best effort", async () => {
  process.env.ZELLIJ_PANE_ID = "42"
  hostControlPlane().bind({
    get: async () => ({ response: response(), data: {} }),
    post: async () => ({ response: response(204) }),
    patch: async () => { throw new Error("the route failed") },
  })
  const reporter = createWorkStateReporter({ runner: { async run() { throw new Error("zellij is absent") } } })
  await expect(reporter.report({ outcome: "ok", result: { work_pins: [pin] } }, { sessionID: "session-goal-failed", abort: new AbortController().signal })).resolves.toBeUndefined()
})

test("keeps a closure receipt in the operator channel", async () => {
  process.env.ZELLIJ_PANE_ID = "42"
  const messages: string[] = []
  hostControlPlane().bind({
    get: async () => ({ response: response(), data: {} }),
    post: async ({ body }) => { messages.push(String((body as { message: string }).message)); return { response: response(204) } },
    // The reporter also refreshes the session goal title; a bound patch keeps
    // that write off the operator channel this test reads.
    patch: async () => ({ response: response() }),
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

test("keeps a pane rename failure best effort", async () => {
  process.env.ZELLIJ_PANE_ID = "42"
  const reporter = createWorkStateReporter({ runner: { async run(argv: string[]) {
    if (argv[2] === "rename-pane") return { exitCode: 1, stdout: "", stderr: "no such pane" }
    return { exitCode: 0, stdout: JSON.stringify([{ id: 42, is_plugin: false, tab_id: 21 }]), stderr: "" }
  } } })
  await expect(reporter.report({ outcome: "ok", result: { work_pins: [pin] } }, { sessionID: "session-pane-failure", abort: new AbortController().signal })).resolves.toBeUndefined()
})

test("formats the gate brief from focused portfolio rows", () => {
  expect(formatGateBrief("product-1", [
    { focus: { work_id: "work-1", workflow_step_label: "planning", attention_kind: "approval_required" } },
    { focus: { work_id: "work-2", workflow_step_label: "execution", attention_kind: "in_progress" } },
  ])).toBe("◆ CONCORD GATE BRIEF | product=product-1 | work=work-1 | step=planning | decision=pending || work=work-2 | step=execution | decision=none")
})
