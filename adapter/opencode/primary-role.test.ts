// Registered roles hold disjoint Concord authority. Lanes have no workflow
// tools. Capability-bound helpers authenticate the held dispatch separately
// from the generated role ceiling.
import { afterAll, beforeAll, describe, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { agentLanes, agentUtilities } from "./generated-agent-lanes"
import { guardPrimaryRole, PrimaryRoleRefusal } from "./primary-role"
import { roleDeniedConcordTools } from "./generated-primary-profiles"
import { configureHostLease } from "./host-lease"
import { configureCiWatch } from "./ci-watch"
import { syntheticHostVersionFixture } from "./host-version.test-support"
import { WorkerCapabilityError } from "./lane_capabilities"

syntheticHostVersionFixture()

const MUTATING = ["concord_work_compact", "concord_work_define", "concord_work_relate", "concord_work_start", "concord_work_transition"]
const READS = ["concord_product_view", "concord_work_browse", "concord_work_trace", "concord_knowledge", "concord_domain"]
const WATCHING = ["concord_ci_watch"]
const WORKFLOW_CATALOG = [...MUTATING, ...READS, ...WATCHING].sort()
const CATALOG = [...WORKFLOW_CATALOG, "concord_worker_read", "concord_worker_check", "concord_worker_diff"].sort()
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
  test("the registration is exactly the declared tool catalog", () => {
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

  test("every lane and utility is refused every workflow tool before transport", async () => {
    for (const agent of [...LANE_AGENTS, ...UTILITY_AGENTS]) {
      for (const toolID of WORKFLOW_CATALOG) {
        const { calls, definition } = recording()
        const guarded = guardPrimaryRole(toolID, definition)
        await expect(guarded.execute({ request: {} }, context(agent))).rejects.toBeInstanceOf(PrimaryRoleRefusal)
        expect(calls).toEqual([])
      }
    }
  })

  test("the bounded reader passes the role ceiling only for lanes with repository read capability", async () => {
    for (const lane of agentLanes) {
      const { calls, definition } = recording()
      const call = guardPrimaryRole("concord_worker_read", definition).execute({}, context(`concord-${lane.id}`))
      if (lane.capabilities.includes("read_repository")) {
        expect(await call).toBe("ran")
        expect(calls).toHaveLength(1)
      } else {
        await expect(call).rejects.toBeInstanceOf(PrimaryRoleRefusal)
        expect(calls).toEqual([])
      }
    }
    for (const agent of UTILITY_AGENTS) {
      const { calls, definition } = recording()
      await expect(guardPrimaryRole("concord_worker_read", definition).execute({}, context(agent))).rejects.toBeInstanceOf(PrimaryRoleRefusal)
      expect(calls).toEqual([])
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

  test("the bounded checker passes the role ceiling only for execution-capable lanes", async () => {
    for (const agent of [...LANE_AGENTS, ...UTILITY_AGENTS]) {
      const lane = agentLanes.find((entry) => `concord-${entry.id}` === agent)
      const granted = lane?.capabilities.some((capability) => ["run_tests", "run_targeted_checks", "run_validators"].includes(capability))
      const { calls, definition } = recording()
      const call = guardPrimaryRole("concord_worker_check", definition).execute({}, context(agent))
      if (granted) {
        expect(await call).toBe("ran")
        expect(calls).toHaveLength(1)
      } else {
        await expect(call).rejects.toBeInstanceOf(PrimaryRoleRefusal)
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
      const lane = agentLanes.find((entry) => `concord-${entry.id}` === agent)
      const granted = new Set<string>()
      if (lane?.capabilities.includes("read_repository")) granted.add("concord_worker_read")
      if (lane?.capabilities.includes("inspect_diff")) granted.add("concord_worker_diff")
      if (lane?.capabilities.some((capability) => ["run_tests", "run_targeted_checks", "run_validators"].includes(capability))) granted.add("concord_worker_check")
      const expected = CATALOG.filter((tool) => !granted.has(tool))
      expect([...roleDeniedConcordTools[agent].tools].sort()).toEqual(expected)
      expect(roleDeniedConcordTools[agent].boundary.length).toBeGreaterThan(0)
    }
  })

  test("the plugin registers every Concord tool behind the guard", async () => {
    for (const toolID of MUTATING) {
      const definition = Reflect.get(plugin!.tool, toolID)
      await expect(definition.execute({ request: { operation: "lifecycle", input: {} } }, context("concord-coordinator"))).rejects.toBeInstanceOf(PrimaryRoleRefusal)
    }
    for (const toolID of WORKFLOW_CATALOG) {
      const definition = Reflect.get(plugin!.tool, toolID)
      await expect(definition.execute({ request: {} }, context("concord-implement"))).rejects.toBeInstanceOf(PrimaryRoleRefusal)
    }
    // An agent name alone supplies no attempt or source-reading authority.
    await expect(plugin!.tool.concord_worker_read.execute({ ordinal: 0, offset: 0, length: 1 }, context("concord-implement"))).rejects.toBeInstanceOf(WorkerCapabilityError)
    await expect(plugin!.tool.concord_worker_check.execute({ control_id: "control:missing" }, context("concord-implement"))).rejects.toBeInstanceOf(WorkerCapabilityError)
  })
})

// The factory's host-lease claim fails against the unstamped repository
// placeholder; the suite's files share one process in an order no file
// controls, so this file resets the factory's shared lease and peer state.
afterAll(() => {
  configureHostLease({ reset: true })
  configureCiWatch({ reset: true })
})
