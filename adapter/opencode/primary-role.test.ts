// Registered primary and utility roles retain their declared tool boundaries.
import { afterAll, beforeAll, describe, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { guardPrimaryRole, PrimaryRoleRefusal } from "./primary-role"
import { roleDeniedConcordTools } from "./generated-primary-profiles"
import { configureHostLease } from "./host-lease"
import { syntheticHostVersionFixture } from "./host-version.test-support"

syntheticHostVersionFixture()

const MUTATING = ["concord_work_compact", "concord_work_define", "concord_work_relate", "concord_work_start", "concord_work_transition"]
const READS = ["concord_product_view", "concord_work_browse", "concord_work_trace", "concord_knowledge", "concord_domain"]
const WORKFLOW_CATALOG = [...MUTATING, ...READS].sort()
const CATALOG = WORKFLOW_CATALOG
const utilityRegistry = await Bun.file(new URL("../../contracts/agent-utilities.v1.json", import.meta.url)).json() as { utilities: { id: string }[] }
const UTILITY_AGENTS = utilityRegistry.utilities.map((utility) => `concord-${utility.id}`)

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
  test("the registration is exactly the declared tool catalog", () => {
    expect(registered).toEqual(CATALOG)
  })

  test("a utility refusal names the tool, the role, and the utility boundary", async () => {
    const guarded = guardPrimaryRole("concord_work_transition", recording().definition)
    const refusal = await guarded.execute({}, context("concord-explore")).catch((error: unknown) => error)
    expect(refusal).toBeInstanceOf(PrimaryRoleRefusal)
    const message = String((refusal as Error).message)
    expect(message).toContain("concord_work_transition")
    expect(message).toContain("concord-explore")
    expect(message).toContain("declared tool set")
    expect(message).toContain("records nothing")
  })

  test("Concord and the release train engineer keep every registered Concord tool", async () => {
    // Both primary profiles deny no tool, so no primary profile denial
    // refuses here.
    for (const agent of ["Concord", "Concord-RTE"]) {
      for (const toolID of registered) {
        const { calls, definition } = recording()
        expect(await guardPrimaryRole(toolID, definition).execute({ id: toolID }, context(agent))).toBe("ran")
        expect(calls).toEqual([{ id: toolID }])
      }
    }
  })

  test("Concord-Planner is refused every work-state writer and keeps the reads", async () => {
    for (const toolID of MUTATING) {
      const { calls, definition } = recording()
      const refusal = await guardPrimaryRole(toolID, definition).execute({ request: {} }, context("Concord-Planner")).catch((error: unknown) => error)
      expect(refusal).toBeInstanceOf(PrimaryRoleRefusal)
      const message = String((refusal as Error).message)
      expect(message).toContain(`${toolID} is refused for Concord-Planner`)
      expect(message).toContain("writes work state")
      expect(message).toContain("records nothing")
      expect(calls).toEqual([])
    }
    for (const toolID of READS) {
      const { calls, definition } = recording()
      expect(await guardPrimaryRole(toolID, definition).execute({ id: toolID }, context("Concord-Planner"))).toBe("ran")
      expect(calls).toEqual([{ id: toolID }])
    }
  })

  test("every utility is refused every workflow tool before transport", async () => {
    for (const agent of UTILITY_AGENTS) {
      for (const toolID of WORKFLOW_CATALOG) {
        const { calls, definition } = recording()
        const guarded = guardPrimaryRole(toolID, definition)
        await expect(guarded.execute({ request: {} }, context(agent))).rejects.toBeInstanceOf(PrimaryRoleRefusal)
        expect(calls).toEqual([])
      }
    }
  })

  test("a step with no agent identity is refused every registered tool", async () => {
    for (const agent of [undefined, "", 7]) {
      for (const toolID of CATALOG) {
        const { calls, definition } = recording()
        await expect(guardPrimaryRole(toolID, definition).execute({}, context(agent))).rejects.toBeInstanceOf(PrimaryRoleRefusal)
        expect(calls).toEqual([])
      }
    }
  })

  test("unknown host agents, including the retired primary names, keep established pass-through behavior", async () => {
    for (const agent of ["concord-coordinator", "concord-operator", "concord-repair", "concord-1", "concord-2", "build"]) {
      for (const toolID of registered) {
        const { calls, definition } = recording()
        expect(await guardPrimaryRole(toolID, definition).execute({ agent }, context(agent))).toBe("ran")
        expect(calls).toEqual([{ agent }])
      }
    }
    expect(Object.hasOwn(roleDeniedConcordTools, "concord-repair")).toBe(false)
    // The retired names are no longer registered roles: a stale installed
    // definition gets no registry denial and no registry entry.
    expect(Object.hasOwn(roleDeniedConcordTools, "concord-coordinator")).toBe(false)
    expect(Object.hasOwn(roleDeniedConcordTools, "concord-operator")).toBe(false)
  })

  test("the guard reads one generated registry covering exactly the registered roles", () => {
    const agents = Object.keys(roleDeniedConcordTools).sort()
    expect(agents).toEqual(["Concord-RTE", "Concord", "Concord-Planner", ...UTILITY_AGENTS].sort())
    // Neither full primary profile denies a tool; Planner denies exactly the
    // work-state writers. Every remaining refusal is a lane or utility
    // boundary.
    expect([...roleDeniedConcordTools["Concord-Planner"].tools].sort()).toEqual(MUTATING)
    expect(roleDeniedConcordTools["Concord-Planner"].boundary.length).toBeGreaterThan(0)
    expect(roleDeniedConcordTools["Concord-RTE"].tools).toEqual([])
    expect(roleDeniedConcordTools["Concord"].tools).toEqual([])
    expect(roleDeniedConcordTools["Concord-RTE"].boundary).toBe("")
    expect(roleDeniedConcordTools["Concord"].boundary).toBe("")
    for (const agent of UTILITY_AGENTS) {
      expect([...roleDeniedConcordTools[agent].tools].sort()).toEqual(CATALOG)
      expect(roleDeniedConcordTools[agent].boundary.length).toBeGreaterThan(0)
    }
  })

  test("the plugin registers every Concord tool behind the guard", async () => {
    for (const toolID of WORKFLOW_CATALOG) {
      const definition = Reflect.get(plugin!.tool, toolID)
      await expect(definition.execute({ request: { operation: "lifecycle", input: {} } }, context("concord-explore"))).rejects.toBeInstanceOf(PrimaryRoleRefusal)
    }
  })
})

// The factory's host-lease claim fails against the unstamped repository
// placeholder; the suite's files share one process in an order no file
// controls, so this file resets the factory's shared lease and peer state.
afterAll(() => {
  configureHostLease({ reset: true })
})
