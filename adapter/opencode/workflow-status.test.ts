import { afterEach, expect, test } from "bun:test"
import { createWorkStateReporter, formatQuestComplete, formatWorkPaneName, formatWorkTabName } from "./workflow-status"
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

// Golden test: the closure banner's exact bytes are fixed here, not in prose.
// A completed pin is celebratory; a cancelled or superseded pin is a visibly
// plainer marker; the gate is the terminal lifecycle alone, so an envelope
// with no evidence renders `evidence=none` instead of suppressing the signal.
// The fence is required, not decoration: the host renders an assistant text
// part with `marked` under its default `breaks: false`, which collapses every
// single newline to a space, so an unfenced banner reaches the operator as one
// run-on line.
test("renders the terminal closure banners byte-exactly", () => {
  const envelope = { outcome: "ok", evidence_refs: [{ kind: "commit", locator: "commit:abc123" }, { kind: "pull_request", locator: "pr:7" }] }
  expect(formatQuestComplete({ ...pin, lifecycle: "completed", step: "complete" }, envelope)).toBe(
    "```\n◆◆◆ QUEST COMPLETE ◆◆◆\nwork-1 | Concord\nRepair the adapter\nlifecycle=completed | evidence=2\n```",
  )
  expect(formatQuestComplete({ ...pin, lifecycle: "cancelled" }, envelope)).toBe(
    "```\n◆ CONCORD WORK CLOSED\nwork-1 | Concord\nRepair the adapter\nlifecycle=cancelled | evidence=2\n```",
  )
  expect(formatQuestComplete({ ...pin, lifecycle: "superseded" }, { outcome: "ok" })).toBe(
    "```\n◆ CONCORD WORK CLOSED\nwork-1 | Concord\nRepair the adapter\nlifecycle=superseded | evidence=none\n```",
  )
  expect(formatQuestComplete({ ...pin, lifecycle: "completed", linear_issue_key: "CON-42" }, envelope)).toBe(
    "```\n◆◆◆ QUEST COMPLETE ◆◆◆\nCON-42 (work-1) | Concord\nRepair the adapter\nlifecycle=completed | evidence=2\n```",
  )
  expect(formatQuestComplete(pin, envelope)).toBeNull()
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
  expect(titles.length).toBe(1)
  expect(titles[0]?.url).toBe("/session/{id}")
  expect(titles[0]?.path).toEqual({ id: "session-goal" })
  expect(titles[0]?.body).toEqual({ title: "Goal: Revised intent" })
  expect(titles[0]?.signal).toBeInstanceOf(AbortSignal)
})

test("a failed best-effort side effect returns a warning instead of failing the report", async () => {
  process.env.ZELLIJ_PANE_ID = "42"
  hostControlPlane().bind({
    get: async () => ({ response: response(), data: {} }),
    post: async () => ({ response: response(204) }),
    patch: async () => { throw new Error("the route failed") },
  })
  const reporter = createWorkStateReporter({ runner: { async run() { throw new Error("zellij is absent") } } })
  await expect(reporter.report({ outcome: "ok", result: { work_pins: [pin] } }, { sessionID: "session-goal-failed", abort: new AbortController().signal })).resolves.toEqual([
    "Concord could not rename the work tab or pane frame: zellij is absent.",
    "Concord could not write the session goal title: the session title route is absent or refused the write.",
  ])
})

test("a completed pin queues the celebratory closure banner", async () => {
  process.env.ZELLIJ_PANE_ID = "42"
  hostControlPlane().bind({
    get: async () => ({ response: response(), data: {} }),
    post: async () => ({ response: response(204) }),
    // The reporter also refreshes the session goal title; a bound patch keeps
    // that write off the warnings this test reads.
    patch: async () => ({ response: response() }),
  })
  const reporter = createWorkStateReporter({ runner: { async run() { return { exitCode: 0, stdout: JSON.stringify([{ id: 42, is_plugin: false, tab_id: 21 }]), stderr: "" } } } })
  await reporter.report({
    outcome: "ok",
    evidence_refs: [{ kind: "pull_request", authority: "github", locator_kind: "url", locator: "https://github.com/example/repo/pull/7" }],
    result: { work_pins: [{ ...pin, lifecycle: "completed", step: "complete" }] },
  }, { sessionID: "session-closure", abort: new AbortController().signal })
  expect(reporter.takeNotices("session-closure")).toEqual([
    "```\n◆◆◆ QUEST COMPLETE ◆◆◆\nwork-1 | Concord\nRepair the adapter\nlifecycle=completed | evidence=1\n```",
  ])
  expect(reporter.takeNotices("session-closure")).toEqual([])
})

test("a cancelled pin queues the plainer closure marker", async () => {
  const reporter = createWorkStateReporter({ runner: { async run() { return { exitCode: 0, stdout: "", stderr: "" } } } })
  await reporter.report({ outcome: "ok", result: { work_pins: [{ ...pin, lifecycle: "cancelled" }] } }, { sessionID: "session-cancelled", abort: new AbortController().signal })
  expect(reporter.takeNotices("session-cancelled")).toEqual([
    "```\n◆ CONCORD WORK CLOSED\nwork-1 | Concord\nRepair the adapter\nlifecycle=cancelled | evidence=none\n```",
  ])
})

test("the closure banner emits once per session, work, and terminal lifecycle", async () => {
  const completed = { ...pin, lifecycle: "completed", step: "complete" }
  const envelope = { outcome: "ok", result: { work_pins: [completed] } }
  const reporter = createWorkStateReporter({ runner: { async run() { return { exitCode: 0, stdout: "", stderr: "" } } } })
  const context = { sessionID: "session-dedupe", abort: new AbortController().signal }
  await reporter.report(envelope, context)
  // A later mutation touching the same item carries the same terminal pin.
  await reporter.report(envelope, context)
  expect(reporter.takeNotices("session-dedupe")).toHaveLength(1)
  // A different terminal lifecycle on the same work emits its own marker, and
  // a different session receives its own copy.
  await reporter.report({ outcome: "ok", result: { work_pins: [{ ...completed, lifecycle: "superseded" }] } }, context)
  await reporter.report(envelope, { sessionID: "session-other", abort: new AbortController().signal })
  const blocks = reporter.takeNotices("session-dedupe")
  expect(blocks).toHaveLength(1)
  expect(blocks[0]).toContain("lifecycle=superseded")
  expect(reporter.takeNotices("session-other")[0]).toContain("QUEST COMPLETE")
})

test("a refused envelope emits no closure banner", async () => {
  const reporter = createWorkStateReporter({ runner: { async run() { return { exitCode: 0, stdout: "", stderr: "" } } } })
  await reporter.report({
    outcome: "error",
    result: { work_pins: [{ ...pin, lifecycle: "completed", step: "complete" }] },
  }, { sessionID: "session-refused", abort: new AbortController().signal })
  expect(reporter.takeNotices("session-refused")).toEqual([])
})

test("keeps tab rename failure best effort", async () => {
  process.env.ZELLIJ_PANE_ID = "42"
  const reporter = createWorkStateReporter({ runner: { async run() { throw new Error("zellij is absent") } } })
  const warnings = await reporter.report({ outcome: "ok", result: { work_pins: [pin] } }, { sessionID: "session-failure", abort: new AbortController().signal })
  expect(warnings).toEqual(["Concord could not rename the work tab or pane frame: zellij is absent.", "Concord could not write the session goal title: the session title route is absent or refused the write."])
})

test("keeps a pane rename failure best effort", async () => {
  process.env.ZELLIJ_PANE_ID = "42"
  const reporter = createWorkStateReporter({ runner: { async run(argv: string[]) {
    if (argv[2] === "rename-pane") return { exitCode: 1, stdout: "", stderr: "no such pane" }
    return { exitCode: 0, stdout: JSON.stringify([{ id: 42, is_plugin: false, tab_id: 21 }]), stderr: "" }
  } } })
  const warnings = await reporter.report({ outcome: "ok", result: { work_pins: [pin] } }, { sessionID: "session-pane-failure", abort: new AbortController().signal })
  expect(warnings).toEqual(["Concord could not rename the work tab or pane frame: rename-pane exited 1.", "Concord could not write the session goal title: the session title route is absent or refused the write."])
})
