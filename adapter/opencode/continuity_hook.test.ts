import { expect, test } from "bun:test"
import { createContinuityTransform, type ContinuitySessionSource } from "./continuity-hook"
import { configureCoreBinary } from "./dispatch"

const START = "<!-- concord:continuity:v1 -->"
const END = "<!-- /concord:continuity:v1 -->"
const pluginSource = await Bun.file(new URL("./concord-plugin.ts", import.meta.url)).text()

type Transform = (input: { sessionID?: string }, output: { system: string[] }) => Promise<void>

type SessionsProbe = ContinuitySessionSource & { parentChecks: number; directoryReads: number }

function fakeSessions(directory = "/worktrees/project-1/work-1", managedParent = false): SessionsProbe {
  return {
    parentChecks: 0,
    directoryReads: 0,
    hasManagedParent: async () => {
      return managedParent
    },
    sessionDirectory: async () => {
      return directory
    },
  }
}

function countingSessions(probe: SessionsProbe): SessionsProbe {
  const parent = probe.hasManagedParent
  const directory = probe.sessionDirectory
  probe.hasManagedParent = async (sessionID) => {
    probe.parentChecks += 1
    return parent(sessionID)
  }
  probe.sessionDirectory = async (sessionID) => {
    probe.directoryReads += 1
    return directory(sessionID)
  }
  return probe
}

function output(system: string): { system: string[] } {
  return { system: [system] }
}

test("plugin entry registers the continuity system transform", () => {
  expect(pluginSource).toContain("const continuityTransform = createContinuityTransform()")
  expect(pluginSource).toContain("await continuityTransform(input, output)")
})

test("continuity transform keeps identical cached output byte-stable", async () => {
  let calls = 0
  const sessions = countingSessions(fakeSessions())
  const transform = createContinuityTransform({
    now: () => 1_000,
    sessions,
    runner: { run: async () => { calls += 1; return { exitCode: 0, stdout: "fixture", stderr: "" } } },
  }) as Transform
  const first = output("system prefix")
  const second = output("system prefix")

  await transform({ sessionID: "ses-1" }, first)
  await transform({ sessionID: "ses-1" }, second)

  expect(first.system[0]).toBe(second.system[0])
  expect(calls).toBe(1)
  expect(sessions.parentChecks).toBe(2)
  expect(sessions.directoryReads).toBe(2)
})

test("continuity transform carries pending messages from core output", async () => {
  const packet = '{"continuity":{"pending_messages":1}}'
  const transformed = output("system prefix")
  const transform = createContinuityTransform({
    sessions: fakeSessions(),
    runner: { run: async () => ({ exitCode: 0, stdout: packet, stderr: "" }) },
  }) as Transform

  await transform({ sessionID: "ses-1" }, transformed)

  expect(transformed.system[0]).toContain(`${START}\n${packet}\n${END}`)
})

test("continuity transform carries only the core packet", async () => {
  const packet = JSON.stringify({ continuity: { pinned: { work_pin: {
    work_id: "work-1", title: "Repair the adapter", linear_issue_key: "", project_id: "project-1", project_display_name: "Concord", version: 4, lifecycle: "in_progress", workflow_type: "workflow.break_fix",
    step: "repair", pending_operator_decision: null,
  } } } })
  const transformed = output("system prefix")
  const transform = createContinuityTransform({
    sessions: fakeSessions(),
    runner: { run: async () => ({ exitCode: 0, stdout: packet, stderr: "" }) },
  }) as Transform

  await transform({ sessionID: "ses-1" }, transformed)

  expect(transformed.system[0]).toBe(`system prefix${START}\n${packet}\n${END}`)
})

test("continuity transform replaces a sentinel block instead of appending", async () => {
  let now = 1_000
  let content = "old"
  let calls = 0
  const transform = createContinuityTransform({
    now: () => now,
    sessions: fakeSessions(),
    runner: { run: async () => { calls += 1; return { exitCode: 0, stdout: content, stderr: "" } } },
  }) as Transform
  const first = output("system prefix")
  await transform({ sessionID: "ses-1" }, first)

  content = "new content"
  now += 10_001
  const previous = first.system[0]
  await transform({ sessionID: "ses-1" }, first)

  expect(first.system[0]).toContain(`${START}\nnew content\n${END}`)
  expect(first.system[0]).not.toContain(`${START}\nold\n${END}`)
  expect(first.system[0].indexOf(START)).toBe(first.system[0].lastIndexOf(START))
  expect(first.system[0].indexOf(END)).toBe(first.system[0].lastIndexOf(END))
  expect(first.system[0].length - previous.length).toBe("new content".length - "old".length)
  expect(calls).toBe(2)
})

test("continuity transform renders no block without a session identity", async () => {
  const original = "agent.generate bytes"
  const unchanged = output(original)
  let calls = 0
  let parentChecks = 0
  const transform = createContinuityTransform({
    sessions: {
      hasManagedParent: async () => { parentChecks += 1; return false },
      sessionDirectory: async () => "/worktrees/project-1/work-1",
    },
    runner: { run: async () => { calls += 1; return { exitCode: 0, stdout: "unexpected", stderr: "" } } },
  }) as Transform

  await transform({}, unchanged)

  expect(unchanged.system[0]).toBe(original)
  expect(calls).toBe(0)
  expect(parentChecks).toBe(0)
})

test("continuity transform renders no block for a managed parent session", async () => {
  const original = "lane session bytes"
  const unchanged = output(original)
  let calls = 0
  let directoryReads = 0
  const transform = createContinuityTransform({
    sessions: {
      hasManagedParent: async () => true,
      sessionDirectory: async () => { directoryReads += 1; return "/worktrees/project-1/work-1" },
    },
    runner: { run: async () => { calls += 1; return { exitCode: 0, stdout: "unexpected", stderr: "" } } },
  }) as Transform

  await transform({ sessionID: "ses-lane" }, unchanged)

  expect(unchanged.system[0]).toBe(original)
  expect(calls).toBe(0)
  expect(directoryReads).toBe(0)
})

test("continuity transform re-resolves after a work_start retarget", async () => {
  let now = 1_000
  const sessions = fakeSessions("/worktrees/project-1/work-launch")
  const directories: string[] = []
  const transform = createContinuityTransform({
    now: () => now,
    sessions,
    runner: { run: async (argv) => {
      directories.push(argv[2] ?? "")
      return { exitCode: 0, stdout: `packet for ${argv[2]}`, stderr: "" }
    } },
  }) as Transform
  const transformed = output("system prefix")
  await transform({ sessionID: "ses-1" }, transformed)
  expect(transformed.system[0]).toContain("packet for /worktrees/project-1/work-launch")

  // work_start moves the session to the claimed worktree of another item;
  // the next turn renders that item's block, even inside the TTL window.
  sessions.sessionDirectory = async () => "/worktrees/project-1/work-landed"
  now += 1
  await transform({ sessionID: "ses-1" }, transformed)

  expect(directories).toEqual(["/worktrees/project-1/work-launch", "/worktrees/project-1/work-landed"])
  expect(transformed.system[0]).toContain(`packet for /worktrees/project-1/work-landed`)
  expect(transformed.system[0].indexOf(START)).toBe(transformed.system[0].lastIndexOf(START))
})

test("continuity transform ignores the launcher identity environment", async () => {
  // Clause 3's negative: a session whose directory resolves no active claim
  // renders no block even with the launcher identity environment set, so the
  // stale launch item's continuity can never reach a moved session.
  const previousProduct = Bun.env.CONCORD_SELECTED_PRODUCT_ID
  const previousWork = Bun.env.CONCORD_SELECTED_WORK_ID
  try {
    Bun.env.CONCORD_SELECTED_PRODUCT_ID = "concord"
    Bun.env.CONCORD_SELECTED_WORK_ID = "work-launch-item"
    const original = "coordinator bytes"
    const unchanged = output(original)
    let calls = 0
    const transform = createContinuityTransform({
      sessions: {
        hasManagedParent: async () => false,
        sessionDirectory: async () => "",
      },
      runner: { run: async () => { calls += 1; return { exitCode: 0, stdout: "unexpected", stderr: "" } } },
    }) as Transform

    await transform({ sessionID: "ses-moved" }, unchanged)

    expect(unchanged.system[0]).toBe(original)
    expect(calls).toBe(0)
  } finally {
    if (previousProduct === undefined) delete Bun.env.CONCORD_SELECTED_PRODUCT_ID
    else Bun.env.CONCORD_SELECTED_PRODUCT_ID = previousProduct
    if (previousWork === undefined) delete Bun.env.CONCORD_SELECTED_WORK_ID
    else Bun.env.CONCORD_SELECTED_WORK_ID = previousWork
  }
  expect(Bun.env.CONCORD_SELECTED_PRODUCT_ID).toBe(previousProduct)
  expect(Bun.env.CONCORD_SELECTED_WORK_ID).toBe(previousWork)
})

test("continuity transform leaves system bytes unchanged on failure, empty output, or unreadable session", async () => {
  const original = "prefix\n\nexact bytes"
  const failed = output(original)
  await (createContinuityTransform({
    sessions: fakeSessions(),
    runner: { run: async () => { throw new Error("spawn failed") } },
  }) as Transform)({ sessionID: "ses-1" }, failed)
  expect(failed.system[0]).toBe(original)

  const nonzero = output(original)
  await (createContinuityTransform({
    sessions: fakeSessions(),
    runner: { run: async () => ({ exitCode: 1, stdout: "unexpected", stderr: "failure" }) },
  }) as Transform)({ sessionID: "ses-1" }, nonzero)
  expect(nonzero.system[0]).toBe(original)

  const empty = output(original)
  await (createContinuityTransform({
    sessions: fakeSessions(),
    runner: { run: async () => ({ exitCode: 0, stdout: "", stderr: "" }) },
  }) as Transform)({ sessionID: "ses-1" }, empty)
  expect(empty.system[0]).toBe(original)

  // A session the host control plane cannot read renders no block and does
  // not throw: failure is absence.
  const unreadable = output(original)
  await (createContinuityTransform({
    sessions: {
      hasManagedParent: async () => { throw new Error("the host session does not exist") },
      sessionDirectory: async () => "/unused",
    },
    runner: { run: async () => ({ exitCode: 0, stdout: "unexpected", stderr: "" }) },
  }) as Transform)({ sessionID: "ses-gone" }, unreadable)
  expect(unreadable.system[0]).toBe(original)
})

test("continuity transform renders a held-work block without parsing step actions", async () => {
  // Synthetic core bytes stay opaque: rendering them grants no repair or
  // reconciliation authority and creates no adapter-owned action.
  const heldPacket = JSON.stringify({
    continuity: {
      work_id: "work-869",
      pinned: {
        product_identity: ["concord-product"],
        workflow_step: null,
        step_actions: [],
        contract: null,
        spec_mandate: [],
        pending_operator_decision: null,
        latest_checkpoint: null,
        design_record: null,
        unresolved_failure: null,
        work_pin: {
          work_id: "work-869",
          title: "Outside repair",
          linear_issue_key: "",
          project_id: "project-869",
          project_display_name: "Concord",
          version: 4,
          lifecycle: "in_progress",
          workflow_type: "workflow.implementation",
          step: "execution",
          pending_operator_decision: null,
          outside_repair_disposition: { state: "active", reason: "bounded outside defect repair" },
          outside_repair_route: ["outside_repair_reconcile"],
        },
      },
      latest_checkpoint: null,
      boundaries: { count: 0, items: [], next_cursor: null, watermark: "seq:0" },
      typed_availability: { restart: "unavailable", reason: "outside-repair disposition owns the work" },
      pending_messages: 0,
      observations: [],
    },
  })
  const transformed = output("system prefix")
  let calls = 0
  const transform = createContinuityTransform({
    sessions: fakeSessions(),
    runner: { run: async () => { calls += 1; return { exitCode: 0, stdout: heldPacket, stderr: "" } } },
  }) as Transform

  await transform({ sessionID: "ses-held" }, transformed)

  expect(transformed.system[0]).toContain(`${START}\n${heldPacket}\n${END}`)
  expect(calls).toBe(1)
  expect(transformed.system[0]).not.toContain("step_action_pin")
  expect(transformed.system[0]).not.toContain("next_valid_intent")
})

test("continuity transform gates spawns by session and directory for ten seconds", async () => {
  let now = 5_000
  let calls = 0
  const transform = createContinuityTransform({
    now: () => now,
    sessions: fakeSessions(),
    runner: { run: async () => { calls += 1; return { exitCode: 0, stdout: "fixture", stderr: "" } } },
  }) as Transform
  await transform({ sessionID: "ses-1" }, output("one"))
  now += 9_999
  await transform({ sessionID: "ses-1" }, output("two"))
  expect(calls).toBe(1)
})

// Fake-runner suite: bind the continuity hook transport to a nominal core
// path instead of the unstamped repository placeholder (CD-0111 D1).
configureCoreBinary("concord")
