import { afterEach, expect, test } from "bun:test"
import { createWorkStateReporter } from "./workflow-status"
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
}

afterEach(() => {
  hostControlPlane().bind(undefined)
})

function response(status = 200): Response {
  return new Response(null, { status })
}

// The reporter refreshes the session goal title and delegates the closure
// receipt to the core's `concord receipt` verb. The receipt bytes are pinned
// by the core's internal/receipt golden tests; here the verb's stdout is the
// fixture.
function reporterRunner(options: { receipt: string; receiptExitCode?: number; calls?: string[][] } = { receipt: "" }) {
  return { async run(argv: string[], stdin: string) {
    options.calls?.push([...argv, stdin])
    if (argv[0] === "concord-test") return { exitCode: options.receiptExitCode ?? 0, stdout: options.receipt, stderr: "" }
    return { exitCode: 0, stdout: "", stderr: "" }
  } }
}

// The reporter owns no terminal surface. A mutation report forks no
// terminal command, whatever the host environment exports, and a pin whose
// lifecycle is not terminal reaches no child at all — only the host title
// route and, at a terminal lifecycle, the core receipt verb.
test("the work state report forks no terminal command", async () => {
  const terminalRoot = "zellij"
  process.env.ZELLIJ_PANE_ID = "42"
  try {
    hostControlPlane().bind({
      get: async () => ({ response: response(), data: {} }),
      post: async () => ({ response: response(204) }),
      patch: async () => ({ response: response() }),
    })
    const calls: string[][] = []
    const reporter = createWorkStateReporter({ runner: { async run(argv: string[]) {
      calls.push(argv)
      return { exitCode: 0, stdout: "", stderr: "" }
    } }, binary: "concord-test" })
    const warnings = await reporter.report({ outcome: "ok", result: { work_pins: [pin] } }, { sessionID: "session-no-terminal", abort: new AbortController().signal })
    expect(warnings).toEqual([])
    expect(calls.filter((argv) => argv[0] === terminalRoot)).toEqual([])

    const completed = await reporter.report(
      { outcome: "ok", result: { work_pins: [{ ...pin, lifecycle: "completed", step: "complete" }] } },
      { sessionID: "session-no-terminal", abort: new AbortController().signal },
    )
    expect(completed).toEqual([])
    expect(calls.filter((argv) => argv[0] === terminalRoot)).toEqual([])
    expect(calls.every((argv) => argv[0] === "concord-test")).toBe(true)
  } finally {
    delete process.env.ZELLIJ_PANE_ID
  }
})

test("the reporter refreshes the session goal title from the pin", async () => {
  const titles: Array<{ url: string; path?: Record<string, unknown>; body?: unknown; signal?: unknown }> = []
  hostControlPlane().bind({
    get: async () => ({ response: response(), data: {} }),
    post: async () => ({ response: response(204) }),
    patch: async (options) => { titles.push(options); return { response: response() } },
  })
  const reporter = createWorkStateReporter({ runner: reporterRunner(), binary: "concord-test" })
  await reporter.report({ outcome: "ok", result: { work_pins: [{ ...pin, title: "Revised intent" }] } }, { sessionID: "session-goal", abort: new AbortController().signal })
  expect(titles.length).toBe(1)
  expect(titles[0]?.url).toBe("/session/{id}")
  expect(titles[0]?.path).toEqual({ id: "session-goal" })
  expect(titles[0]?.body).toEqual({ title: "Goal: Revised intent" })
  expect(titles[0]?.signal).toBeInstanceOf(AbortSignal)
})

test("a failed best-effort side effect returns a warning instead of failing the report", async () => {
  hostControlPlane().bind({
    get: async () => ({ response: response(), data: {} }),
    post: async () => ({ response: response(204) }),
    patch: async () => { throw new Error("the route failed") },
  })
  const reporter = createWorkStateReporter({ runner: reporterRunner(), binary: "concord-test" })
  const warnings = await reporter.report({ outcome: "ok", result: { work_pins: [pin] } }, { sessionID: "session-goal-failed", abort: new AbortController().signal })
  expect(warnings).toEqual([
    "Concord could not write the session goal title: the session title route is absent or refused the write.",
  ])
})

test("a completed pin queues the closure receipt the verb prints", async () => {
  hostControlPlane().bind({
    get: async () => ({ response: response(), data: {} }),
    post: async () => ({ response: response(204) }),
    // The reporter also refreshes the session goal title; a bound patch keeps
    // that write off the warnings this test reads.
    patch: async () => ({ response: response() }),
  })
  const receipt = "| 🛫 CON-42 Complete (work-1) |\n| :-- |\n| ✅ Delegate passes the verb bytes |\n| ✓ check check:repo:verify · pass |"
  const reporter = createWorkStateReporter({ runner: reporterRunner({ receipt }), binary: "concord-test" })
  await reporter.report({
    outcome: "ok",
    result: { work_pins: [{ ...pin, lifecycle: "completed", step: "complete" }] },
  }, { sessionID: "session-closure", abort: new AbortController().signal })
  expect(reporter.takeNotices("session-closure")).toEqual([receipt])
  expect(reporter.takeNotices("session-closure")).toEqual([])
})

test("a cancelled pin stays silent: the verb prints nothing for a non-completed closure", async () => {
  const reporter = createWorkStateReporter({ runner: reporterRunner({ receipt: "" }), binary: "concord-test" })
  await reporter.report({ outcome: "ok", result: { work_pins: [{ ...pin, lifecycle: "cancelled" }] } }, { sessionID: "session-cancelled", abort: new AbortController().signal })
  expect(reporter.takeNotices("session-cancelled")).toEqual([])
})

test("a failed receipt verb queues no notice but names the failure in a warning", async () => {
  hostControlPlane().bind({
    get: async () => ({ response: response(), data: {} }),
    post: async () => ({ response: response(204) }),
    patch: async () => ({ response: response() }),
  })
  const warnings = await createWorkStateReporter({ runner: reporterRunner({ receipt: "", receiptExitCode: 1 }), binary: "concord-test" })
    .report({ outcome: "ok", result: { work_pins: [{ ...pin, lifecycle: "completed" }] } }, { sessionID: "session-receipt-failed", abort: new AbortController().signal })
  expect(warnings).toEqual(["Concord could not render the work closure receipt: receipt exited 1."])
})

test("the closure notice emits once per session, work, and terminal lifecycle", async () => {
  const completed = { ...pin, lifecycle: "completed", step: "complete" }
  const envelope = { outcome: "ok", result: { work_pins: [completed] } }
  const calls: string[][] = []
  // The real verb prints nothing for a non-completed closure; the fake models
  // that gate with a flag the test flips before the superseded report.
  let verbPrints = true
  const runner = { async run(argv: string[], stdin: string) {
    calls.push([...argv, stdin])
    if (argv[0] === "concord-test") return { exitCode: 0, stdout: verbPrints ? "🛫 work-1 Complete" : "", stderr: "" }
    return { exitCode: 0, stdout: "", stderr: "" }
  } }
  const reporter = createWorkStateReporter({ runner, binary: "concord-test" })
  const context = { sessionID: "session-dedupe", abort: new AbortController().signal }
  await reporter.report(envelope, context)
  // A later mutation touching the same item carries the same terminal pin.
  await reporter.report(envelope, context)
  expect(reporter.takeNotices("session-dedupe")).toHaveLength(1)
  verbPrints = false
  await reporter.report({ outcome: "ok", result: { work_pins: [{ ...completed, lifecycle: "superseded" }] } }, context)
  verbPrints = true
  await reporter.report(envelope, { sessionID: "session-other", abort: new AbortController().signal })
  const blocks = reporter.takeNotices("session-dedupe")
  expect(blocks).toEqual([])
  expect(reporter.takeNotices("session-other")[0]).toBe("🛫 work-1 Complete")
  const receiptCalls = calls.filter((argv) => argv[0] === "concord-test")
  expect(receiptCalls).toHaveLength(4)
})

test("closure deduplication evicts the oldest session after 512 entries", async () => {
  const receipt = "closure notice"
  const reporter = createWorkStateReporter({ runner: reporterRunner({ receipt }), binary: "concord-test" })
  const envelope = { outcome: "ok", result: { work_pins: [{ ...pin, lifecycle: "completed" }] } }
  const report = (session: number) => reporter.report(envelope, { sessionID: `session-${session}`, abort: new AbortController().signal })
  for (let session = 0; session < 512; session++) {
    await report(session)
    expect(reporter.takeNotices(`session-${session}`)).toEqual([receipt])
  }
  await report(0)
  expect(reporter.takeNotices("session-0")).toEqual([])
  await report(512)
  expect(reporter.takeNotices("session-512")).toEqual([receipt])
  await report(1)
  expect(reporter.takeNotices("session-1")).toEqual([])
  await report(0)
  expect(reporter.takeNotices("session-0")).toEqual([receipt])
})

test("closure deduplication retains only 64 inserted keys per session", async () => {
  const receipt = "closure notice"
  const reporter = createWorkStateReporter({ runner: reporterRunner({ receipt }), binary: "concord-test" })
  const context = { sessionID: "session-keys", abort: new AbortController().signal }
  const report = (work: number) => reporter.report({ outcome: "ok", result: { work_pins: [{ ...pin, work_id: `work-${work}`, lifecycle: "completed" }] } }, context)
  for (let work = 0; work < 64; work++) {
    await report(work)
    expect(reporter.takeNotices(context.sessionID)).toEqual([receipt])
  }
  await report(0)
  expect(reporter.takeNotices(context.sessionID)).toEqual([])
  await report(64)
  expect(reporter.takeNotices(context.sessionID)).toEqual([receipt])
  await report(1)
  expect(reporter.takeNotices(context.sessionID)).toEqual([])
  await report(0)
  expect(reporter.takeNotices(context.sessionID)).toEqual([receipt])
})

test("eviction preserves queued first banners and emits the first banner for each new key", async () => {
  const receipt = "closure notice"
  const reporter = createWorkStateReporter({ runner: reporterRunner({ receipt }), binary: "concord-test" })
  const context = { sessionID: "session-pending", abort: new AbortController().signal }
  const completed = { ...pin, lifecycle: "completed" }
  await reporter.report({ outcome: "ok", result: { work_pins: Array.from({ length: 65 }, (_, work) => ({ ...completed, work_id: `work-${work}` })) } }, context)
  for (let session = 0; session < 512; session++) {
    const sessionID = `session-other-${session}`
    await reporter.report({ outcome: "ok", result: { work_pins: [completed] } }, { ...context, sessionID })
    expect(reporter.takeNotices(sessionID)).toEqual([receipt])
  }
  await reporter.report({ outcome: "ok", result: { work_pins: [{ ...completed, work_id: "work-new" }] } }, context)
  expect(reporter.takeNotices(context.sessionID)).toEqual(Array(66).fill(receipt))
  expect(reporter.takeNotices(context.sessionID)).toEqual([])
})

test("a refused envelope emits no closure notice", async () => {
  const reporter = createWorkStateReporter({ runner: reporterRunner({ receipt: "🛫 work-1 Complete" }), binary: "concord-test" })
  await reporter.report({
    outcome: "error",
    result: { work_pins: [{ ...pin, lifecycle: "completed", step: "complete" }] },
  }, { sessionID: "session-refused", abort: new AbortController().signal })
  expect(reporter.takeNotices("session-refused")).toEqual([])
})
