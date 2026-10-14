// The named coordinator profile holds no Concord execution authority. Its
// frontmatter denies the mutating tools, but a session permission granted
// earlier can outlive an agent switch, so the plugin refuses those tools by
// the step agent the host supplies with each tool call.
import { afterAll, describe, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { guardPrimaryRole, PrimaryRoleRefusal } from "./primary-role"
import { primaryProfileDeniedConcordTools } from "./generated-primary-profiles"
import { configureHostLease } from "./host-lease"
import { syntheticHostVersionFixture } from "./host-version.test-support"

syntheticHostVersionFixture()

const MUTATING = ["concord_work_compact", "concord_work_define", "concord_work_relate", "concord_work_start", "concord_work_transition"]
const READS = ["concord_product_view", "concord_work_browse", "concord_work_trace", "concord_knowledge", "concord_domain"]

function context(agent: unknown) {
  return { sessionID: "ses_role", messageID: "msg_role", agent, directory: "/nonexistent", worktree: "/nonexistent", abort: new AbortController().signal, metadata() {}, ask: async () => {} } as any
}

function recording() {
  const calls: unknown[] = []
  return { calls, definition: { description: "stub", args: {}, execute: async (args: unknown, _context: unknown) => { calls.push(args); return "ran" } } }
}

describe("primary role guard", () => {
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

  test("the coordinator keeps its scoped Concord reads", async () => {
    for (const toolID of READS) {
      const { calls, definition } = recording()
      expect(await guardPrimaryRole(toolID, definition).execute({ id: toolID }, context("concord-coordinator"))).toBe("ran")
      expect(calls).toEqual([{ id: toolID }])
    }
  })

  test("the operator, numbered host agents, and other agents pass unchanged", async () => {
    for (const agent of ["concord-operator", "concord-1", "concord-2", "build"]) {
      for (const toolID of MUTATING) {
        const { calls, definition } = recording()
        expect(await guardPrimaryRole(toolID, definition).execute({ agent }, context(agent))).toBe("ran")
        expect(calls).toEqual([{ agent }])
      }
    }
  })

  test("a step with no agent identity cannot be proven not to be the coordinator", async () => {
    for (const agent of [undefined, "", 7]) {
      const { calls, definition } = recording()
      await expect(guardPrimaryRole("concord_work_transition", definition).execute({}, context(agent))).rejects.toBeInstanceOf(PrimaryRoleRefusal)
      expect(calls).toEqual([])
    }
  })

  test("the guard reads the generated profile contract", () => {
    expect([...primaryProfileDeniedConcordTools["concord-coordinator"]].sort()).toEqual(MUTATING)
    expect(primaryProfileDeniedConcordTools["concord-operator"]).toEqual([])
  })

  test("the plugin registers every Concord tool behind the guard", async () => {
    const plugin = await ConcordAdapterPlugin()
    for (const toolID of MUTATING) {
      const definition = Reflect.get(plugin.tool, toolID)
      await expect(definition.execute({ request: { operation: "lifecycle", input: {} } }, context("concord-coordinator"))).rejects.toBeInstanceOf(PrimaryRoleRefusal)
    }
  })
})

// The factory's host-lease claim fails against the unstamped repository
// placeholder; the suite's files share one process in an order no file
// controls, so this file leaves the lease state clean.
afterAll(() => configureHostLease({ reset: true }))
