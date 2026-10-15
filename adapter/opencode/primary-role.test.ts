// Registered roles hold disjoint Concord authority: the coordinator denies its
// mutating tools, the operator denies none, and every lane and utility denies
// the complete catalog. Frontmatter denials are host permissions, and session
// permissions merge over them, so the plugin refuses off-role tools by the
// step agent the host supplies with each tool call. The denied set is
// generated from both role registries.
import { afterAll, beforeAll, describe, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { agentLanes, agentUtilities } from "./generated-agent-lanes"
import { guardPrimaryRole, PrimaryRoleRefusal } from "./primary-role"
import { roleDeniedConcordTools } from "./generated-primary-profiles"
import { configureHostLease } from "./host-lease"
import { syntheticHostVersionFixture } from "./host-version.test-support"

syntheticHostVersionFixture()

const MUTATING = ["concord_work_compact", "concord_work_define", "concord_work_relate", "concord_work_start", "concord_work_transition"]
const READS = ["concord_product_view", "concord_work_browse", "concord_work_trace", "concord_knowledge", "concord_domain"]
const WATCHING = ["concord_ci_watch"]
// The complete registered catalog. REFUSED_BEFORE names the tools that denied
// an unidentified step before the denial map covered every registered role;
// REFUSED_SINCE_WIDENING names the read and watch tools the widening added.
// Keep both cohorts explicit: if either half goes green on a narrowed map,
// the widening did not land.
const REFUSED_BEFORE = MUTATING
const REFUSED_SINCE_WIDENING = [...READS, ...WATCHING]
const CATALOG = [...MUTATING, ...READS, ...WATCHING].sort()
const LANE_AGENTS = agentLanes.map((lane) => `concord-${lane.id}`)
const UTILITY_AGENTS = agentUtilities.map((utility) => `concord-${utility.id}`)

let registered: string[] = []
let plugin: Awaited<ReturnType<typeof ConcordAdapterPlugin>> | undefined

function context(agent: unknown) {
  return { sessionID: "ses_role", messageID: "msg_role", agent, directory: "/nonexistent", worktree: "/nonexistent", abort: new AbortController().signal, metadata() {}, ask: async () => {} } as any
}

function recording() {
  const calls: unknown[] = []
  return { calls, definition: { description: "stub", args: {}, execute: async (args: unknown, _context: unknown) => { calls.push(args); return "ran" } } }
}

beforeAll(async () => {
  plugin = await ConcordAdapterPlugin()
  registered = Object.keys(plugin.tool).sort()
})

describe("primary role guard", () => {
  test("the registration is exactly the eleven-tool catalog", () => {
    expect(registered).toEqual(CATALOG)
  })

  test("the coordinator is refused every mutating Concord tool before transport", async () => {
    for (const toolID of MUTATING) {
      const { calls, definition } = recording()
      const guarded = guardPrimaryRole(toolID, definition)
      await expect(guarded.execute({ request: {} }, context("concord-coordinator"))).rejects.toBeInstanceOf(PrimaryRoleRefusal)
      expect(calls).toEqual([])
    }
  })

  test("the refusal names the tool, the role, and the operator route", async () => {
    const guarded = guardPrimaryRole("concord_work_start", recording().definition)
    const refusal = await guarded.execute({}, context("concord-coordinator")).catch((error: unknown) => error)
    expect(refusal).toBeInstanceOf(PrimaryRoleRefusal)
    const message = String((refusal as Error).message)
    expect(message).toContain("concord_work_start")
    expect(message).toContain("concord-coordinator")
    expect(message).toContain("repository-local operator")
    expect(message).toContain("records nothing")
  })

  test("a lane refusal names the tool, the role, and the lane boundary", async () => {
    const guarded = guardPrimaryRole("concord_work_transition", recording().definition)
    const refusal = await guarded.execute({}, context("concord-implement")).catch((error: unknown) => error)
    expect(refusal).toBeInstanceOf(PrimaryRoleRefusal)
    const message = String((refusal as Error).message)
    expect(message).toContain("concord_work_transition")
    expect(message).toContain("concord-implement")
    expect(message).toContain("dispatch packet")
    expect(message).toContain("records nothing")
  })

  test("the coordinator keeps its scoped reads and the watcher", async () => {
    for (const toolID of [...READS, ...WATCHING]) {
      const { calls, definition } = recording()
      expect(await guardPrimaryRole(toolID, definition).execute({ id: toolID }, context("concord-coordinator"))).toBe("ran")
      expect(calls).toEqual([{ id: toolID }])
    }
  })

  test("the operator keeps every registered Concord tool", async () => {
    for (const toolID of registered) {
      const { calls, definition } = recording()
      expect(await guardPrimaryRole(toolID, definition).execute({ id: toolID }, context("concord-operator"))).toBe("ran")
      expect(calls).toEqual([{ id: toolID }])
    }
  })

  test("every lane and utility is refused every registered Concord tool before transport", async () => {
    for (const agent of [...LANE_AGENTS, ...UTILITY_AGENTS]) {
      for (const toolID of registered) {
        const { calls, definition } = recording()
        const guarded = guardPrimaryRole(toolID, definition)
        await expect(guarded.execute({ request: {} }, context(agent))).rejects.toBeInstanceOf(PrimaryRoleRefusal)
        expect(calls).toEqual([])
      }
    }
  })

  test("a step with no agent identity is refused every registered tool, both cohorts explicit", async () => {
    for (const agent of [undefined, "", 7]) {
      // The five mutating tools denied an unidentified step before the denial
      // map covered every registered role.
      for (const toolID of REFUSED_BEFORE) {
        const { calls, definition } = recording()
        await expect(guardPrimaryRole(toolID, definition).execute({}, context(agent))).rejects.toBeInstanceOf(PrimaryRoleRefusal)
        expect(calls).toEqual([])
      }
      // The six reads and the watcher denied an unidentified step only once
      // lanes and utilities entered the same registry projection.
      for (const toolID of REFUSED_SINCE_WIDENING) {
        const { calls, definition } = recording()
        await expect(guardPrimaryRole(toolID, definition).execute({}, context(agent))).rejects.toBeInstanceOf(PrimaryRoleRefusal)
        expect(calls).toEqual([])
      }
    }
  })

  test("unknown host agents, including unregistered repair, keep established pass-through behavior", async () => {
    for (const agent of ["concord-repair", "concord-1", "concord-2", "build"]) {
      for (const toolID of registered) {
        const { calls, definition } = recording()
        expect(await guardPrimaryRole(toolID, definition).execute({ agent }, context(agent))).toBe("ran")
        expect(calls).toEqual([{ agent }])
      }
    }
    expect(Object.hasOwn(roleDeniedConcordTools, "concord-repair")).toBe(false)
  })

  test("the guard reads one generated registry covering exactly the registered roles", () => {
    const agents = Object.keys(roleDeniedConcordTools).sort()
    expect(agents).toEqual(["concord-coordinator", "concord-operator", ...LANE_AGENTS, ...UTILITY_AGENTS].sort())
    expect([...roleDeniedConcordTools["concord-coordinator"].tools].sort()).toEqual(MUTATING)
    expect(roleDeniedConcordTools["concord-operator"].tools).toEqual([])
    expect(roleDeniedConcordTools["concord-coordinator"].boundary).toContain("operator")
    for (const agent of [...LANE_AGENTS, ...UTILITY_AGENTS]) {
      expect([...roleDeniedConcordTools[agent].tools].sort()).toEqual(CATALOG)
      expect(roleDeniedConcordTools[agent].boundary.length).toBeGreaterThan(0)
    }
  })

  test("the plugin registers every Concord tool behind the guard", async () => {
    for (const toolID of MUTATING) {
      const definition = Reflect.get(plugin!.tool, toolID)
      await expect(definition.execute({ request: { operation: "lifecycle", input: {} } }, context("concord-coordinator"))).rejects.toBeInstanceOf(PrimaryRoleRefusal)
    }
    // A lane agent is refused before the managed-ancestry boundary runs, so
    // the refusal is the typed role refusal for every registered tool.
    for (const toolID of registered) {
      const definition = Reflect.get(plugin!.tool, toolID)
      await expect(definition.execute({ request: {} }, context("concord-implement"))).rejects.toBeInstanceOf(PrimaryRoleRefusal)
    }
  })
})

// The factory's host-lease claim fails against the unstamped repository
// placeholder; the suite's files share one process in an order no file
// controls, so this file leaves the lease state clean.
afterAll(() => configureHostLease({ reset: true }))
